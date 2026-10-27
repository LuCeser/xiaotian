package rt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// holdFirstCompleteCheck parks the first checked CompleteCheck until release.
type holdFirstCompleteCheck struct {
	LessonExecutor
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	calls   int
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
		h.calls++
		if h.calls == 1 {
			h.once.Do(func() { close(h.entered) })
			<-h.release
		}
	}
	return h.LessonExecutor.CompleteCheck(ctx, lessonID, expectedVersion, requestID, unchecked)
}

func (h *holdFirstCompleteCheck) openUp() { close(h.release) }

// TestCheckSubmitInFlightBlocksSameConnectionRetry pins issue #69 review P1:
// a second check_submit on the same connection while the first accepted save
// is still in its latch write must not spawn a second CompleteCheck or take
// the cross-connection idempotent terminal close.
func TestCheckSubmitInFlightBlocksSameConnectionRetry(t *testing.T) {
	words := newFakeWords(ttCards("苹果", "香蕉"))
	tm, s, _, inner := ttDriveToCheck(t, ttCards("苹果", "香蕉"), words)
	ttCheckAll(t, tm, s, 2)

	hold := newHoldFirstCompleteCheck(inner)
	tm.exec = hold

	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-one"}))
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-one"}))

	select {
	case <-hold.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the first submit never reached CompleteCheck")
	}
	if hold.calls != 1 {
		t.Fatalf("CompleteCheck calls while parked = %d, want 1 (the retry must not compete)", hold.calls)
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended count = %d while in flight, want 0", got)
	}

	hold.openUp()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hold.calls == 1 && s.countOf(TypeReviewResult) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if hold.calls != 1 {
		t.Fatalf("CompleteCheck calls after finish = %d, want exactly 1", hold.calls)
	}
	if got := s.countOf(TypeReviewResult); got != 1 {
		t.Fatalf("review_result count = %d, want 1", got)
	}
}

// TestFormalCheckSubmitFreezesWordListAtAdmission: mutating the live tutor
// during the latch write must not change the rating batch assembled from the
// admission snapshot.
func TestFormalCheckSubmitFreezesWordListAtAdmission(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	tm, s, _, inner := ttDriveToCheck(t, ttCards("苹果"), words)
	ttCheckAll(t, tm, s, 1)

	hold := newHoldFirstCompleteCheck(inner)
	tm.exec = hold

	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "freeze-req"}))
	select {
	case <-hold.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("submit never reached CompleteCheck")
	}
	tm.mu.Lock()
	tm.tutor.words[0].Word = "篡改"
	tm.tutor.category = lesson.CategoryEnglish
	tm.mu.Unlock()
	hold.openUp()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(words.submissions()) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want 1", len(subs))
	}
	if _, ok := subs[0]["苹果"]; !ok {
		t.Fatalf("rating batch = %v, want frozen key 苹果 (not the mutated tutor word)", subs[0])
	}
}
