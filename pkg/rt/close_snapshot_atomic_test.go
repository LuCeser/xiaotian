package rt

import (
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// ttDriveToCheckPresentation drives a ONE-word lesson through the last
// word's confirm until enterCheck's stage write has COMMITTED (the row
// already reads stage='checking') and the presentation — check_started plus
// the tutor's checking flip — is parked at checkPresentationInterleave.
// issue #69 备选一 步骤 8: this is the old two-lock snapshot window's
// deciding moment, driven through the real presentation path.
func ttDriveToCheckPresentation(t *testing.T, atWindow, proceed chan struct{}) (*TurnManager, *captureSender, *captureLogger, *fakeExecutor, chan struct{}) {
	t.Helper()
	words := newFakeWords(ttCards("苹果"))
	log := &captureLogger{}
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(
		newFakeASR(asrScript{text: "好了"}),
		newFakeLLM(intentScript(intentConfirm, "")),
		newFakeTTS(2, 320),
		exec, ttConfig(words), NewEventRecorder(log, "t"), nil,
	)
	s := &captureSender{}
	tm.checkPresentationInterleave = func() {
		close(atWindow)
		<-proceed
	}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)

	turnDone := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(turnDone)
	}()
	ttAwaitGate(t, atWindow, "the check presentation window was never reached")
	return tm, s, log, exec, turnDone
}

// ttRowStage reads the fake executor's stored row stage/unchecked (the
// persistence the next reconnect would read).
func ttRowStage(t *testing.T, exec *fakeExecutor) (stage string, unchecked bool) {
	t.Helper()
	exec.mu.Lock()
	defer exec.mu.Unlock()
	return exec.lesson.Stage, exec.lesson.Unchecked
}

// TestCloseSnapshotAtomic pins the unified claim's one-critical-section
// contract (issue #69 备选一 步骤 8): the fallback decision, its snapshot and
// the closed marking observe ONE instant. A check stage presented inside
// the close's old snapshot window (after the init-write lock, before tm.mu
// — driven through the REAL presentation effect) still gets its fallback;
// a close that wins the window refuses the presentation outright and keeps
// the row's checking stage for recovery.
func TestCloseSnapshotAtomic(t *testing.T) {
	t.Run("a check stage presented inside the close window still gets its fallback", func(t *testing.T) {
		atWindow, proceed := make(chan struct{}), make(chan struct{})
		tm, s, log, exec, turnDone := ttDriveToCheckPresentation(t, atWindow, proceed)

		// The close enters its claim and parks in the pre-tm.mu window; the
		// presentation is released INTO that window and lands first.
		closeEntered, presentationLanded := make(chan struct{}), make(chan struct{})
		tm.closeEntryInterleave = func() {
			close(closeEntered)
			<-presentationLanded
		}
		closeDone := make(chan struct{})
		go func() {
			tm.handleEndSession(s)
			close(closeDone)
		}()
		ttAwaitGate(t, closeEntered, "the close entry window was never reached")
		close(proceed)
		if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeCheckStarted) == 1 }) {
			t.Fatal("the check presentation never landed inside the close window")
		}
		close(presentationLanded)
		ttAwaitGate(t, closeDone, "the close never finished after the presentation")
		ttAwaitGate(t, turnDone, "the entering turn never settled")

		// The claim decided on the POST-presentation state: the classroom
		// was in checking when it closed, so the fallback is owned and
		// attempted exactly once, and the goodbye stays the last word. The
		// owned fallback runs on its own goroutine (1.11): poll for its
		// terminal event, then judge the rest.
		if !waitFor(t, 5*time.Second, func() bool { return ttEventCount(t, log, EventUncheckedFallback) == 1 }) {
			t.Fatalf("unchecked_fallback events = %d, want 1", ttEventCount(t, log, EventUncheckedFallback))
		}
		if got := ttCompleteCheckCalls(exec); got != 1 {
			t.Fatalf("complete_check calls = %d, want 1 (the owned fallback)", got)
		}
		stage, unchecked := ttRowStage(t, exec)
		if stage != lesson.StageSubmitted || !unchecked {
			t.Fatalf("row stage=%q unchecked=%v, want submitted+unchecked", stage, unchecked)
		}
		if got := s.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want 1", got)
		}
		ttAssertNothingAfterEnd(t, s)
	})

	t.Run("a close that wins the window refuses the presentation and keeps checking", func(t *testing.T) {
		atWindow, proceed := make(chan struct{}), make(chan struct{})
		tm, s, log, exec, turnDone := ttDriveToCheckPresentation(t, atWindow, proceed)

		// The goodbye runs to completion while the presentation is still
		// parked: the claim saw checking=false, so no fallback exists.
		tm.handleEndSession(s)
		close(proceed)
		ttAwaitGate(t, turnDone, "the entering turn never settled")

		tm.mu.Lock()
		closed := tm.closed
		tm.mu.Unlock()
		if !closed {
			t.Fatal("the goodbye left the session open")
		}
		if got := s.countOf(TypeCheckStarted); got != 0 {
			t.Fatalf("check_started after the close = %d, want 0", got)
		}
		if got := ttCompleteCheckCalls(exec); got != 0 {
			t.Fatalf("complete_check calls = %d, want 0 (no fallback without the check stage)", got)
		}
		if got := ttEventCount(t, log, EventUncheckedFallback); got != 0 {
			t.Fatalf("unchecked_fallback events = %d, want 0", got)
		}
		// The committed EnterCheck write stands for recovery (plan 六: the
		// last word closed after the DB commit but before the panel was
		// shown keeps its checking stage).
		stage, unchecked := ttRowStage(t, exec)
		if stage != lesson.StageChecking || unchecked {
			t.Fatalf("row stage=%q unchecked=%v, want checking for recovery", stage, unchecked)
		}
		if got := s.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want 1", got)
		}
		ttAssertNothingAfterEnd(t, s)
	})
}
