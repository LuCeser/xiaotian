package rt

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// holdFirstCompleteCheck parks the first checked CompleteCheck until release.
// callCount/returnedCount are ATOMIC (rethink2 R0 计数同步): the submit task
// increments them on its own goroutine while the test body polls — if a
// mutation shape ever lets a second submit through, the failure must land on
// the BUSINESS assertion, not on a race report (计划 §5.3: race 报错不算
// 合格红).
type holdFirstCompleteCheck struct {
	LessonExecutor
	entered     chan struct{}
	release     chan struct{}
	calls       atomic.Int64
	returned    atomic.Int64
	once        sync.Once
	releaseOnce sync.Once
}

func newHoldFirstCompleteCheck(inner LessonExecutor) *holdFirstCompleteCheck {
	return &holdFirstCompleteCheck{
		LessonExecutor: inner,
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
}

func (h *holdFirstCompleteCheck) CompleteCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, unchecked bool) (lesson.CompleteCheckResult, error) {
	if !unchecked {
		if n := h.calls.Add(1); n == 1 {
			h.once.Do(func() { close(h.entered) })
			<-h.release
		}
	}
	res, err := h.LessonExecutor.CompleteCheck(ctx, lessonID, expectedVersion, requestID, unchecked)
	if !unchecked {
		h.returned.Add(1)
	}
	return res, err
}

func (h *holdFirstCompleteCheck) callCount() int64     { return h.calls.Load() }
func (h *holdFirstCompleteCheck) returnedCount() int64 { return h.returned.Load() }
func (h *holdFirstCompleteCheck) openUp() {
	h.releaseOnce.Do(func() { close(h.release) })
}

// ttFormalSubmitSettled is the per-task completion observation for the formal
// submit hold gate (review R0-P2). It needs BOTH halves:
//   - returned >= calls observes EVERY checked CompleteCheck, so a mutation
//     that lets a second submit task coexist (dedup removed) still waits for
//     that task's executor call — the single-flight flag alone would not;
//   - active scope formalSubmit cleared observes the accepted task's OWN tail:
//     releaseFormalCheckSubmitOp runs only after writeback, review_result and
//     receipt, so counting messages after the wait is deterministic. Counts
//     alone would let the assertions race that tail (CompleteCheck returns
//     before finishCheckSubmit sends anything).
func ttFormalSubmitSettled(tm *TurnManager, hold *holdFirstCompleteCheck) bool {
	if calls := hold.callCount(); calls > 0 && hold.returnedCount() < calls {
		return false
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.activeScope == nil {
		return true
	}
	return tm.activeScope.formalSubmit == nil
}

func waitFormalSubmitSettled(t *testing.T, tm *TurnManager, hold *holdFirstCompleteCheck, timeout time.Duration) bool {
	t.Helper()
	return waitFor(t, timeout, func() bool { return ttFormalSubmitSettled(tm, hold) })
}

// TestHoldFirstCompleteCheckProtocol pins the hold gate's own execution
// protocol (rethink2 R0): the hit and the release stay separated — release
// never closes entered — and the call/return counts stay observable while
// callers are still in flight (a plain int here goes red under -race).
func TestHoldFirstCompleteCheckProtocol(t *testing.T) {
	exec := newFakeExecutorNoRow(ttLessonID)
	hold := newHoldFirstCompleteCheck(exec)

	const parallel = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = hold.CompleteCheck(context.Background(), ttLessonID, 1, "proto", false)
		}()
	}
	close(start)
	select {
	case <-hold.entered:
	case <-time.After(3 * time.Second):
		hold.openUp()
		wg.Wait()
		t.Fatal("the first CompleteCheck never hit the hold")
	}
	// Read the counts while the other callers may still be mid-call: this is
	// the observation the assertions rely on, so it must be race-free by
	// construction, not by scheduling luck.
	_ = hold.callCount()
	_ = hold.returnedCount()
	hold.openUp()
	wg.Wait()
	if got := hold.callCount(); got != parallel {
		t.Fatalf("CompleteCheck calls = %d, want %d", got, parallel)
	}
	if got := hold.returnedCount(); got != parallel {
		t.Fatalf("CompleteCheck returns = %d, want %d", got, parallel)
	}

	// Releasing a never-hit gate must not fabricate the hit (§5.1).
	late := newHoldFirstCompleteCheck(exec)
	late.openUp()
	late.openUp() // release must be idempotent (§5.1 + Cleanup)
	select {
	case <-late.entered:
		t.Fatal("release closed entered — 解除控制点伪造了命中")
	default:
	}
}

// TestCheckSubmitInFlightBlocksSameConnectionRetry pins issue #69 review P1:
// a second check_submit on the same connection while the first accepted save
// is still in its latch write must not spawn a second CompleteCheck or take
// the cross-connection idempotent terminal close. 失败路径先回收：park 中的
// 断言失败也要先 openUp 并等提交任务退出（§5.1）。
func TestCheckSubmitInFlightBlocksSameConnectionRetry(t *testing.T) {
	words := newFakeWords(ttCards("苹果", "香蕉"))
	tm, s, _, inner := ttDriveToCheck(t, ttCards("苹果", "香蕉"), words)
	ttCheckAll(t, tm, s, 2)

	hold := newHoldFirstCompleteCheck(inner)
	tm.exec = hold

	reclaimAndFail := func(msg string) {
		hold.openUp()
		if !waitFormalSubmitSettled(t, tm, hold, 5*time.Second) {
			t.Fatalf("%s (reclaim: CompleteCheck calls=%d returned=%d never settled)",
				msg, hold.callCount(), hold.returnedCount())
		}
		t.Fatal(msg)
	}

	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-one"}))
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-one"}))

	hit := false
	select {
	case <-hold.entered:
		hit = true
	case <-time.After(3 * time.Second):
	}
	if !hit {
		reclaimAndFail("the first submit never reached CompleteCheck")
	}
	// Observations recorded BEFORE the failure assert, parked gate still held.
	parkedCalls, parkedEnded := hold.callCount(), s.countOf(TypeSessionEnded)
	if parkedCalls != 1 {
		reclaimAndFail(fmt.Sprintf("CompleteCheck calls while parked = %d, want 1 (the retry must not compete)", parkedCalls))
	}
	if parkedEnded != 0 {
		reclaimAndFail(fmt.Sprintf("session_ended count = %d while in flight, want 0", parkedEnded))
	}

	hold.openUp()
	if !waitFormalSubmitSettled(t, tm, hold, 5*time.Second) {
		t.Fatalf("the accepted submit task never finished (CompleteCheck calls=%d returned=%d)",
			hold.callCount(), hold.returnedCount())
	}
	if got := hold.callCount(); got != 1 {
		t.Fatalf("CompleteCheck calls after finish = %d, want exactly 1", got)
	}
	if got := s.countOf(TypeReviewResult); got != 1 {
		t.Fatalf("review_result count = %d, want 1", got)
	}
}

// TestFormalCheckSubmitFreezesWordListAtAdmission: mutating the live tutor
// during the latch write must not change the rating batch assembled from the
// admission snapshot. The frozen-batch assertion waits for the submit task's
// OWN completion first (ttFormalSubmitSettled: every call returned AND the
// task's defer-tail — writeback, review_result, receipt — has landed).
func TestFormalCheckSubmitFreezesWordListAtAdmission(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	tm, s, _, inner := ttDriveToCheck(t, ttCards("苹果"), words)
	ttCheckAll(t, tm, s, 1)

	hold := newHoldFirstCompleteCheck(inner)
	tm.exec = hold

	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "freeze-req"}))
	hit := false
	select {
	case <-hold.entered:
		hit = true
	case <-time.After(3 * time.Second):
	}
	if !hit {
		hold.openUp()
		if !waitFormalSubmitSettled(t, tm, hold, 5*time.Second) {
			t.Fatalf("submit reclaim: CompleteCheck calls=%d returned=%d never settled",
				hold.callCount(), hold.returnedCount())
		}
		t.Fatal("submit never reached CompleteCheck")
	}
	tm.mu.Lock()
	tm.tutor.words[0].Word = "篡改"
	tm.tutor.category = lesson.CategoryEnglish
	tm.mu.Unlock()
	hold.openUp()

	if !waitFormalSubmitSettled(t, tm, hold, 5*time.Second) {
		t.Fatalf("the accepted submit task never finished (CompleteCheck calls=%d returned=%d)",
			hold.callCount(), hold.returnedCount())
	}
	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want 1", len(subs))
	}
	if _, ok := subs[0]["苹果"]; !ok {
		t.Fatalf("rating batch = %v, want frozen key 苹果 (not the mutated tutor word)", subs[0])
	}
}
