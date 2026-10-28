package rt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// S1 协议测试（rethink3 §五 S1）：整任务观察必须只在任务真正结束后才
// 报告完成——停在执行器之前、FSRS、回执尾部、事件尾部都不算；被替换课
// 堂的任务仍被逐任务等待；失败路径先回收再报告。这些证明的是测试设施
// 的协议（taskDone 观察），不是生产业务修复证明。

// ttPark is a one-hit control point: hit() closes entered exactly once,
// wait() parks until openUp. The §5.1 discipline applies — a failed
// assertion path must openUp and wait the task before Fatalling.
type ttPark struct {
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	openOnce sync.Once
}

func newTtPark() *ttPark {
	return &ttPark{entered: make(chan struct{}), release: make(chan struct{})}
}

func (p *ttPark) hit()  { p.once.Do(func() { close(p.entered) }) }
func (p *ttPark) wait() { <-p.release }
func (p *ttPark) openUp() {
	p.openOnce.Do(func() { close(p.release) })
}

// ttHitWithin bounds the OBSERVATION only (§5.1): it never releases the
// park and never Fatals — the caller owns the reclaim-then-assert order.
func ttHitWithin(p *ttPark, d time.Duration) bool {
	select {
	case <-p.entered:
		return true
	case <-time.After(d):
		return false
	}
}

// ttParkHit is ttHitWithin's non-blocking form for polling loops.
func ttParkHit(p *ttPark) bool {
	select {
	case <-p.entered:
		return true
	default:
		return false
	}
}

// ttPoll is waitFor without *testing.T — the wait helpers RETURN results,
// they never Fatal (S1: 异步 goroutine 与等待 helper 返回结果).
func ttPoll(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// ttGateWords parks the FSRS write (SubmitReview) — the whole-task tail
// between the executor's latch and the receipt/result presentation.
type ttGateWords struct {
	WordSource
	park *ttPark
}

func (w *ttGateWords) SubmitReview(ctx context.Context, results map[string]string, category string) (ReviewWriteResult, error) {
	w.park.hit()
	w.park.wait()
	return w.WordSource.SubmitReview(ctx, results, category)
}

// ttHoldUnchecked parks the close fallback's CompleteCheck BEFORE the real
// executor call — the pre-executor park the old bottom tracker could not
// see (第三轮第 4 项).
type ttHoldUnchecked struct {
	LessonExecutor
	park *ttPark
}

func (h *ttHoldUnchecked) CompleteCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, unchecked bool) (lesson.CompleteCheckResult, error) {
	if unchecked {
		h.park.hit()
		h.park.wait()
	}
	return h.LessonExecutor.CompleteCheck(ctx, lessonID, expectedVersion, requestID, unchecked)
}

// TestServerTaskParkedAtTailsNotReportedDone: the formal submit task parked
// at ANY of its tails — before the executor call, at the FSRS write, at the
// receipt/result window, at the summary event tail — must never be reported
// as completed by the whole-task observation.
func TestServerTaskParkedAtTailsNotReportedDone(t *testing.T) {
	run := func(t *testing.T, name string, arm func(tm *TurnManager, words *fakeWordSource) (hit func() bool, release func())) {
		t.Run(name, func(t *testing.T) {
			words := newFakeWords(ttCards("苹果"))
			tm, s, _, _ := ttDriveToCheck(t, ttCards("苹果"), words)
			ttCheckAll(t, tm, s, 1)
			tm.taskTrack = &serverTaskTracker{}

			hit, release := arm(tm, words)
			tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "s1-" + name}))
			if !ttPoll(5*time.Second, hit) {
				release()
				ttPoll(5*time.Second, tm.serverTasksSettled)
				t.Fatalf("the submit never reached its %s control point", name)
			}
			if parkedSettled := tm.serverTasksSettled(); parkedSettled {
				release()
				ttPoll(5*time.Second, tm.serverTasksSettled)
				t.Fatal("settled reported the submit done while it was still parked at " + name)
			}
			release()
			if !ttPoll(5*time.Second, tm.serverTasksSettled) {
				t.Fatalf("the submit task never settled after releasing %s", name)
			}
			if got := s.countOf(TypeReviewResult); got != 1 {
				t.Fatalf("review_result count = %d, want 1 (the whole task finished)", got)
			}
		})
	}

	run(t, "before-executor", func(tm *TurnManager, _ *fakeWordSource) (func() bool, func()) {
		hold := newHoldFirstCompleteCheck(tm.exec)
		tm.exec = hold
		hit := func() bool {
			select {
			case <-hold.entered:
				return true
			default:
				return false
			}
		}
		return hit, hold.openUp
	})
	run(t, "fsrs-write", func(tm *TurnManager, words *fakeWordSource) (func() bool, func()) {
		park := newTtPark()
		tm.cfg.Words = &ttGateWords{WordSource: words, park: park}
		return func() bool { return ttParkHit(park) }, park.openUp
	})
	run(t, "receipt-result-tail", func(tm *TurnManager, _ *fakeWordSource) (func() bool, func()) {
		park := newTtPark()
		tm.checkSubmitPresentationInterleave = func() { park.hit(); park.wait() }
		return func() bool { return ttParkHit(park) }, park.openUp
	})
	run(t, "summary-tail", func(tm *TurnManager, _ *fakeWordSource) (func() bool, func()) {
		park := newTtPark()
		tm.checkSubmitSummaryInterleave = func() { park.hit(); park.wait() }
		return func() bool { return ttParkHit(park) }, park.openUp
	})
}

// TestReplacedClassroomTasksWaitedIndividually: classroom A's accepted
// submit is parked when B replaces the classroom on the same connection and
// B's own starter task parks too — the observation must keep BOTH tasks
// live (逐任务等待), releasing A alone must not report done, and A's exit
// must not be lost to the replacement (第三轮第 5 项: 观察缩回活动 scope
// 会漏掉 A).
func TestReplacedClassroomTasksWaitedIndividually(t *testing.T) {
	words := newFakeWords(ttCards("苹果", "香蕉"))
	exec := newFakeExecutor(ttLessonID, ttCards("苹果", "香蕉"))
	tm, s := ttInitCheckingRow(t, exec, words)
	tm.taskTrack = &serverTaskTracker{}

	hold := newHoldFirstCompleteCheck(exec)
	tm.exec = hold
	reclaimAll := func(msg string) {
		hold.openUp()
		ttPoll(5*time.Second, tm.serverTasksSettled)
		t.Fatal(msg)
	}

	// Task 1: A's accepted formal submit parks before its executor call.
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "scope-a-s1"}))
	if !ttHitWithinPark(hold) {
		reclaimAll("classroom A submit never reached CompleteCheck")
	}

	// B replaces the classroom (real production re-init); B's greeting
	// starter is task 2, parked before it runs via the production seam.
	starter := newTtPark()
	tm.starterInterleave = func(task func()) { starter.hit(); starter.wait(); task() }
	practice := &fakePracticeSource{cards: ptPracticeCards("cat", "dog")}
	ps := newTestPracticeStore(t)
	tm.cfg = TurnManagerConfig{
		LessonID: ttLessonID, Words: words,
		Practice: practice, PracticeStore: ps,
	}
	exec.mu.Lock()
	exec.lesson.CurrentIndex = 0
	exec.lesson.Stage = ""
	exec.lesson.Verdicts = nil
	exec.mu.Unlock()
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Practice: true,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	if !ttHitWithin(starter, 5*time.Second) {
		starter.openUp()
		reclaimAll("classroom B starter never parked")
	}

	// BOTH tasks live — A's scope was replaced, the observation must not
	// have shrunk to the active classroom.
	if tm.serverTasksSettled() {
		starter.openUp()
		reclaimAll("settled reported done while replaced A and B's starter were both live")
	}

	// Release A alone: A's task truly ends (executor signal), but B's
	// starter is still parked — done must stay unreported.
	hold.openUp()
	if !ttPoll(5*time.Second, func() bool { return hold.returnedCount() >= hold.callCount() && hold.callCount() > 0 }) {
		starter.openUp()
		reclaimAll("classroom A submit task never finished")
	}
	if tm.serverTasksSettled() {
		starter.openUp()
		ttPoll(5*time.Second, tm.serverTasksSettled)
		t.Fatal("settled reported done after A exited while B's starter was still parked")
	}

	// Release B: now and only now does every task settle.
	starter.openUp()
	if !ttPoll(5*time.Second, tm.serverTasksSettled) {
		t.Fatal("server tasks never settled after both releases")
	}
	// A's whole task truly ran: its latch write landed (version 4 → 5,
	// stage submitted — the accepted save's own responsibility), while the
	// superseded classroom presented nothing (the stale-epoch gate held).
	exec.mu.Lock()
	gotStage, gotVer := exec.lesson.Stage, exec.lesson.Version
	exec.mu.Unlock()
	if gotStage != lesson.StageSubmitted || gotVer != 5 {
		t.Fatalf("stored stage=%q version=%d, want A's whole save (submitted/5)", gotStage, gotVer)
	}
	if got := s.countOf(TypeReviewResult); got != 0 {
		t.Fatalf("review_result count = %d, want 0 — the superseded classroom's task must not present", got)
	}
}

func ttHitWithinPark(h *holdFirstCompleteCheck) bool {
	select {
	case <-h.entered:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

// TestFallbackObservedAtBothCloseEntries: the D-03 fallback launched by the
// goodbye entry (handleEndSession) and by the transport entry (Close) is
// registered by the close claim itself; parked before its executor call it
// must never be reported done (第三轮第 4 项 — the old observation missed it
// in all three registers it read).
func TestFallbackObservedAtBothCloseEntries(t *testing.T) {
	run := func(t *testing.T, name string, close func(tm *TurnManager, s *captureSender)) {
		t.Run(name, func(t *testing.T) {
			words := newFakeWords(ttCards("苹果", "香蕉"))
			exec := newFakeExecutor(ttLessonID, ttCards("苹果", "香蕉"))
			log := &captureLogger{}
			exec.lesson.Stage = lesson.StageChecking
			exec.lesson.CurrentIndex = len(exec.lesson.Cards)
			exec.lesson.Version = 4
			exec.lesson.Verdicts = make([]lesson.CheckVerdict, len(exec.lesson.Cards))
			for i := range exec.lesson.Verdicts {
				exec.lesson.Verdicts[i] = lesson.CheckVerdict{WordIndex: i, Correct: true, Manual: true}
			}
			tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec,
				ttConfig(words), NewEventRecorder(log, "t"), nil)
			tm.taskTrack = &serverTaskTracker{}
			s := &captureSender{}
			ttInit(t, tm, s)

			park := newTtPark()
			tm.exec = &ttHoldUnchecked{LessonExecutor: exec, park: park}
			close(tm, s)
			if !ttHitWithin(park, 5*time.Second) {
				park.openUp()
				ttPoll(5*time.Second, tm.serverTasksSettled)
				t.Fatalf("the %s fallback never reached its pre-executor park", name)
			}
			if tm.serverTasksSettled() {
				park.openUp()
				ttPoll(5*time.Second, tm.serverTasksSettled)
				t.Fatalf("settled reported the %s fallback done while it was parked before its executor call", name)
			}
			park.openUp()
			if !ttPoll(5*time.Second, tm.serverTasksSettled) {
				t.Fatalf("the %s fallback task never settled after release", name)
			}
			if exec.lesson.Stage != lesson.StageSubmitted || !exec.lesson.Unchecked {
				t.Fatalf("stored stage=%q unchecked=%v, want the fallback's submitted+unchecked",
					exec.lesson.Stage, exec.lesson.Unchecked)
			}
		})
	}
	run(t, "goodbye-entry", func(tm *TurnManager, s *captureSender) { tm.handleEndSession(s) })
	run(t, "transport-entry", func(tm *TurnManager, _ *captureSender) { tm.Close(nil) })
}

// ttReclaimEvidence is what a §5.1 failure path must establish BEFORE it
// reports: the observation recorded while parked, whether the true task
// exit was observed before the report point, and a business-side effect as
// the report-point witness.
type ttReclaimEvidence struct {
	parkedSettled bool // 记录观察：park 中的 settled 读数
	taskExitSeen  bool // 等待原任务：报告前是否观察到整任务退出
	submitLanded  bool // 报告点旁证：业务效果是否已落地
}

// ttReclaim is the §5.1 failure order as a returning helper — 记录观察 →
// 释放控制点 → 等待原任务真正退出 → （真实失败路径在此报告）。It never
// Fatals itself (S1: 异步 goroutine 与等待 helper 返回结果，不直接 Fatal)。
func ttReclaim(settled func() bool, release func(), landed func() bool) ttReclaimEvidence {
	ev := ttReclaimEvidence{parkedSettled: settled()} // 1. 记录观察
	release()                                         // 2. 释放控制点
	ev.taskExitSeen = ttPoll(5*time.Second, settled)  // 3. 等待原任务
	ev.submitLanded = landed()                        // 4. 报告点在此之后
	return ev
}

// TestReclaimOrderProtocol: the §5.1 failure path's own order — record the
// observation, release EVERY control point, wait the task's true exit, and
// only then report. The reclaim helper runs on its own goroutine and
// RETURNS its evidence (no Fatal inside the goroutine); the report point
// must sit after the observed exit, which is exactly what a withdrawn wait
// breaks (撤掉等待后断言时点早于真实退出).
func TestReclaimOrderProtocol(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	tm, s, _, _ := ttDriveToCheck(t, ttCards("苹果"), words)
	ttCheckAll(t, tm, s, 1)
	tm.taskTrack = &serverTaskTracker{}

	park := newTtPark()
	tm.cfg.Words = &ttGateWords{WordSource: words, park: park}
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "s1-reclaim-order"}))
	if !ttHitWithin(park, 5*time.Second) {
		park.openUp()
		ttPoll(5*time.Second, tm.serverTasksSettled)
		t.Fatal("the submit never reached its FSRS control point")
	}

	// 异步 goroutine 返回结果，不在 goroutine 内 Fatal。
	resCh := make(chan ttReclaimEvidence, 1)
	go func() {
		resCh <- ttReclaim(tm.serverTasksSettled, park.openUp, func() bool {
			return len(words.submissions()) == 1
		})
	}()
	select {
	case ev := <-resCh:
		if ev.parkedSettled {
			t.Fatal("the reclaim recorded settled=true while the task was parked")
		}
		if !ev.taskExitSeen {
			t.Fatal("the reclaim reported before observing the task's true exit — the wait was skipped or too short")
		}
		if !ev.submitLanded {
			t.Fatal("the FSRS submission had not landed at the report point")
		}
	case <-time.After(10 * time.Second):
		park.openUp()
		ttPoll(5*time.Second, tm.serverTasksSettled)
		t.Fatal("the reclaim goroutine never returned its evidence")
	}
}
