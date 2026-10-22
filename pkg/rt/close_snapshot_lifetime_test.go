package rt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// gatingCompleteCheckExec parks the FIRST CompleteCheck call (the close-path
// fallback's latch write) until the test releases it: the close has marked
// the session closed and shipped its goodbye by then, so whatever returns
// while the write is in flight meets a closed session. Later calls pass
// straight through without waiting — a broken close path that starts a
// SECOND fallback must be observable, not deadlocked.
type gatingCompleteCheckExec struct {
	*fakeExecutor
	mu      sync.Mutex
	fired   bool
	entered chan struct{}
	release chan struct{}
}

func (g *gatingCompleteCheckExec) CompleteCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, unchecked bool) (lesson.CompleteCheckResult, error) {
	g.mu.Lock()
	first := !g.fired
	g.fired = true
	g.mu.Unlock()
	if first {
		close(g.entered)
		<-g.release
	}
	return g.fakeExecutor.CompleteCheck(ctx, lessonID, expectedVersion, requestID, unchecked)
}

// ttCloseFixture drives a two-word lesson into the checking stage and mounts
// the gated executor, returning everything needed to observe one owned
// fallback.
func ttCloseFixture(t *testing.T) (*TurnManager, *captureSender, *captureLogger, *gatingCompleteCheckExec) {
	t.Helper()
	tm, s, log, exec := ttDriveToCheck(t, ttCards("苹果", "香蕉"), newFakeWords(ttCards("苹果", "香蕉")))
	g := &gatingCompleteCheckExec{
		fakeExecutor: exec, entered: make(chan struct{}), release: make(chan struct{}),
	}
	tm.exec = g
	return tm, s, log, g
}

// ttCompleteCheckCalls counts complete_check tool calls on the executor.
func ttCompleteCheckCalls(exec *fakeExecutor) int {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	n := 0
	for _, c := range exec.checkCalls {
		if c.tool == "complete_check" {
			n++
		}
	}
	return n
}

// TestCloseSnapshot pins the close path's ownership and ordering: the first
// closer snapshots the D-03 fallback input under the lock, marks the session
// closed, ships the goodbye, and only then persists — old callbacks that
// return mid-persistence meet a closed session and stay silent, and no
// second closer (repeat close or racing goodbye) runs a second fallback.
func TestCloseSnapshot(t *testing.T) {
	t.Run("a late provider callback during the fallback persistence stays silent", func(t *testing.T) {
		tm, s, log, g := ttCloseFixture(t)
		asr := newFakeASR(asrScript{text: "这个词怎么写"})
		entered, release := asr.gateNextTranscribe()
		tm.asr = asr
		transcriptsBefore := len(s.controlsOf(t, TypeTranscript))
		historyBefore := len(ttShortHistory(t, tm))

		done := ttStartTurn(tm, s) // the check-stage turn parks inside ASR
		ttAwaitGate(t, entered, "check-stage ASR was not entered")

		closed := make(chan struct{})
		go func() {
			tm.handleEndSession(s)
			close(closed)
		}()
		ttAwaitGate(t, g.entered, "fallback CompleteCheck was not entered")

		// The old turn's ASR returns WHILE the fallback write is parked:
		// the session is already closed — nothing may ship or mutate. The
		// generous finish bound also lets a broken (open-session) path run
		// its full announcement wait before the silence assertions judge it,
		// and tolerates scheduler starvation on a loaded box (the gates are
		// real channels; only the bound is wide).
		close(release)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("stale check turn did not finish")
		}
		if got := len(s.controlsOf(t, TypeTranscript)); got != transcriptsBefore {
			t.Fatalf("transcripts during fallback = %d, want %d", got, transcriptsBefore)
		}
		if got := len(s.controlsOf(t, TypeError)); got != 0 {
			t.Fatalf("errors during fallback = %d, want none", got)
		}
		if got := len(ttShortHistory(t, tm)); got != historyBefore {
			t.Fatalf("history rounds during fallback = %d, want %d", got, historyBefore)
		}
		tm.mu.Lock()
		state, closedFlag := tm.state, tm.closed
		tm.mu.Unlock()
		if state != "" || !closedFlag {
			t.Fatalf("state=%q closed=%v, want the close to stand during persistence", state, closedFlag)
		}

		close(g.release)
		ttAwaitGate(t, closed, "close did not finish")
		if got := ttEventCount(t, log, EventUncheckedFallback); got != 1 {
			t.Fatalf("unchecked_fallback events = %d, want 1", got)
		}
		if got := ttCompleteCheckCalls(g.fakeExecutor); got != 1 {
			t.Fatalf("complete_check calls = %d, want 1", got)
		}
	})

	t.Run("repeated closes run the fallback exactly once", func(t *testing.T) {
		tm, s, log, g := ttCloseFixture(t)

		closed := make(chan struct{})
		go func() {
			tm.handleEndSession(s)
			close(closed)
		}()
		ttAwaitGate(t, g.entered, "fallback CompleteCheck was not entered")

		// While the owned fallback is parked, the transport dies AND a late
		// end_session frame arrives: neither may start a second fallback.
		tm.Close(s)
		tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))

		close(g.release)
		ttAwaitGate(t, closed, "first close did not finish")

		if got := ttCompleteCheckCalls(g.fakeExecutor); got != 1 {
			t.Fatalf("complete_check calls = %d, want 1 (one-time ownership)", got)
		}
		if got := ttEventCount(t, log, EventUncheckedFallback); got != 1 {
			t.Fatalf("unchecked_fallback events = %d, want 1", got)
		}
	})
}
