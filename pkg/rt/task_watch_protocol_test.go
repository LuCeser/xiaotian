package rt

import (
	"bytes"
	"context"
	"encoding/json"
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
	// starter is task 2, parked before it runs via the production seam. The
	// arm takes mu: A's earlier starter goroutines are still in flight and
	// runStarterTask snapshots the seam under the same hold.
	starter := newTtPark()
	tm.mu.Lock()
	tm.starterInterleave = func(task func()) { starter.hit(); starter.wait(); task() }
	tm.mu.Unlock()
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

// TestVoiceTurnInitContinuationObserved (review P1-4): the answered ask's
// classroom-init continuation runs synchronously INSIDE the voice turn's
// processTurn — fetch, row read and install all on that one goroutine, a
// middle spanned by NEITHER the ask announcement's registration NOR the
// final opening starter's. The whole-task observation must therefore cover
// the voice turn itself: parked at the continuation's own row fetch the task
// is still live, and settled may only report true after the turn's true
// exit.
func TestVoiceTurnInitContinuationObserved(t *testing.T) {
	words := newFakeWords()
	words.setPeek(lesson.CategoryChinese, ttCards("苹果", "香蕉"))
	words.setPeek(lesson.CategoryEnglish, ttCards("cat", "dog"))
	words.setDue(lesson.CategoryChinese, ttCards("苹果", "香蕉"))
	fetchHold := newHoldFirstDueFetch(words)
	asr := newFakeASR(asrScript{text: "语文"})
	tm := NewTurnManager(asr, newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID),
		TurnManagerConfig{LessonID: ttLessonID, Words: fetchHold},
		NewEventRecorder(&captureLogger{}, "t"), nil)
	tm.taskTrack = &serverTaskTracker{}
	s := &captureSender{}

	// The clarify round: question out, final sentence acked, uplink open.
	ttSubjectInit(t, tm, s)
	ttAwaitAskSent(t, s, 0)
	ttAckLast(t, tm, s, 0)
	ttWaitClarifyOpen(t, tm)
	tm.mu.Lock()
	tm.echoTailUntil = time.Now().Add(-time.Millisecond)
	tm.mu.Unlock()

	// The spoken 语文 answer rides the REAL capture→VAD→ASR→turn path; its
	// continuation parks at the answered round's own row fetch.
	tm.IngestAudio(s, ttUtterance())
	select {
	case <-fetchHold.entered:
	case <-time.After(5 * time.Second):
		fetchHold.openUp()
		ttPoll(5*time.Second, tm.serverTasksSettled)
		t.Fatal("the answered continuation never reached its own row fetch")
	}
	if tm.serverTasksSettled() {
		fetchHold.openUp()
		ttPoll(5*time.Second, tm.serverTasksSettled)
		t.Fatal("settled reported done while the voice turn's init continuation was still parked at its row fetch — the ask and starter registrations never span this middle")
	}

	// The released turn opens the classroom; its greeting plays and settles;
	// every server task then truly ends before the final assertions.
	sentsBeforeGreeting := s.countOf(TypeTeacherSentence)
	fetchHold.openUp()
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionReady) == 1 }) {
		t.Fatal("the answered classroom never shipped session_ready")
	}
	ttAwaitAskSent(t, s, sentsBeforeGreeting)
	tm.mu.Lock()
	epoch := tm.epoch
	tm.mu.Unlock()
	ttAckLast(t, tm, s, epoch)
	if !ttPoll(5*time.Second, tm.serverTasksSettled) {
		t.Fatal("the voice turn task never settled after the release — the continuation's exit is part of the turn's whole-task end")
	}
}

// ttReclaimOrderLog records the reclaim protocol's ORDER. The entries that
// decide it are written by instrumentation OUTSIDE the helper under test —
// the release wrappers, the settled probe, the recv wrapper and the caller's
// report — so deleting the helper's wait turns the recorded ORDER red, never
// a necessarily-false evidence field (review P2-6).
type ttReclaimOrderLog struct {
	entries []string
	mu      sync.Mutex
}

func (l *ttReclaimOrderLog) add(kind string) {
	l.mu.Lock()
	l.entries = append(l.entries, kind)
	l.mu.Unlock()
}

// index returns the FIRST occurrence's position, or -1 when absent.
func (l *ttReclaimOrderLog) index(kind string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, e := range l.entries {
		if e == kind {
			return i
		}
	}
	return -1
}

// ttBrowserReclaimCore is the browser fixture's failure-reclaim ORDER as a
// returning function — reclaimAndFail's body minus the report (review P2-6):
// release every held control point, then wait (bounded) the whole-task
// observation, then take the driver's result. It never Fatals; the caller's
// report (reclaimAndFail's t.Fatalf) runs strictly after. The bound only
// limits the observation — the verdict rides the observed exit, not the
// clock. reclaimAndFail runs THIS function; the protocol test drives it with
// the same order instrumentation so what is verified is the actual browser
// recovery path, not a look-alike helper.
func ttBrowserReclaimCore(releases []func(), settled func() bool, recvDriver func() bool, bound time.Duration) (settledSeen, driverGot bool) {
	for _, release := range releases {
		if release != nil {
			release()
		}
	}
	settledSeen = ttPoll(bound, settled)
	driverGot = recvDriver()
	return settledSeen, driverGot
}

// ttExitRecordingSender records the submit task's OWN final wire action:
// session_ended ships from inside the task goroutine strictly before its
// registered handle releases — so the log entry it writes causally precedes
// ANY observation of settled (review P2-6 fix2: the exit timing must be
// recorded by the task's real exit itself, never by the probe the core
// under test happens to call — withdrawing the core's wait must not
// withdraw the recording).
type ttExitRecordingSender struct {
	*captureSender
	order *ttReclaimOrderLog
	once  sync.Once
}

func (w *ttExitRecordingSender) SendControl(v any) error {
	b, err := json.Marshal(v)
	if err == nil && bytes.Contains(b, []byte(`"type":"`+TypeSessionEnded+`"`)) {
		w.once.Do(func() { w.order.add("task_exit") })
	}
	return w.captureSender.SendControl(v)
}

// TestBrowserReclaimOrderProtocol (review P2-6 fix2): the browser fixture's
// failure-reclaim core — the exact function reclaimAndFail runs before its
// report — must observe, IN ORDER: every release, the task's REAL exit (its
// own final wire action recorded by the sender wrapper, independent of the
// core's probe), the driver's result, and only then the caller's report.
// The task's tail is slowed deterministically: after the released FSRS gate
// it parks at the presentation seam until a timer opens it, so a core that
// skips its settled wait reports ~immediately while the task provably has
// not exited yet — the log then shows the report BEFORE task_exit (red),
// instead of a merely-unset evidence field. The self-check reclaims itself
// (ack the summary, settle) BEFORE asserting (S1).
func TestBrowserReclaimOrderProtocol(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	tm, s, _, _ := ttDriveToCheck(t, ttCards("苹果"), words)
	ttCheckAll(t, tm, s, 1)
	tm.taskTrack = &serverTaskTracker{}

	order := &ttReclaimOrderLog{}
	exitSender := &ttExitRecordingSender{captureSender: s, order: order}

	park := newTtPark() // the FSRS gate the reclaim releases
	tail := newTtPark() // the slow tail: opens on a timer after the release
	tm.cfg.Words = &ttGateWords{WordSource: words, park: park}
	tm.checkSubmitPresentationInterleave = func() { tail.hit(); tail.wait() }

	tm.mu.Lock()
	epoch := tm.epoch
	tm.mu.Unlock()
	sentsBefore := s.countOf(TypeTeacherSentence)
	tm.HandleControl(exitSender, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "s1-reclaim-order"}))
	if !ttHitWithin(park, 5*time.Second) {
		park.openUp()
		ttPoll(5*time.Second, tm.serverTasksSettled)
		t.Fatal("the submit never reached its FSRS control point")
	}
	if tm.serverTasksSettled() {
		park.openUp()
		ttPoll(5*time.Second, tm.serverTasksSettled)
		t.Fatal("settled reported the submit done while it was parked at the FSRS gate")
	}
	// The slow tail: 300ms after the reclaim's release, the task may finish.
	// A core that reports without waiting does so ~immediately after the
	// release — long before this fires — so the ordering red is deterministic.
	tailTimer := time.AfterFunc(300*time.Millisecond, tail.openUp)

	acked := make(chan struct{})
	go func() {
		defer close(acked)
		// The summary must be acked for the task to truly finish (both the
		// green and the mutated run reclaim through this).
		if !ttPoll(15*time.Second, func() bool {
			sentences := s.countOf(TypeTeacherSentence)
			return sentences > sentsBefore && s.countOf(TypeAudioEnd) >= sentences
		}) {
			return
		}
		ttAckLast(t, tm, s, epoch)
	}()

	coreDone := make(chan struct{})
	go func() {
		defer close(coreDone)
		// The core runs on its own goroutine and RETURNS — no Fatal inside
		// the goroutine (S1); the caller's report follows the same way
		// reclaimAndFail's Fatal follows the core.
		ttBrowserReclaimCore(
			[]func(){func() { order.add("release"); park.openUp() }},
			tm.serverTasksSettled,
			func() bool { order.add("recv"); return true },
			5*time.Second,
		)
	}()
	select {
	case <-coreDone:
	case <-time.After(10 * time.Second):
		park.openUp()
		tail.openUp()
		ttPoll(5*time.Second, tm.serverTasksSettled)
		t.Fatal("the reclaim core never returned")
	}
	order.add("report") // the caller's report point (reclaimAndFail's Fatal)

	// Self-reclaim FIRST, then assert (review P2-6: the old order Fatalf'd
	// before its own supplementary reclaim wait).
	tailTimer.Stop()
	tail.openUp()
	if !ttPoll(5*time.Second, tm.serverTasksSettled) {
		t.Fatal("server tasks never settled after the reclaim — the ack of the summary never landed")
	}

	if got := order.index("release"); got != 0 {
		t.Fatalf("reclaim order log = %v — the release must come first, was %d", order.entries, got)
	}
	if got, report := order.index("task_exit"), order.index("report"); got < 0 || got > report {
		t.Fatalf("reclaim order log = %v — the task's true exit must be observed before the report (task_exit at %d, report at %d); a core that skips its settled wait reports before the task's own final frame",
			order.entries, got, report)
	}
	if got, report := order.index("recv"), order.index("report"); got < 0 || got > report {
		t.Fatalf("reclaim order log = %v — the driver's result must be taken before the report", order.entries)
	}
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("FSRS submissions = %d, want the released save to land exactly once", got)
	}
}
