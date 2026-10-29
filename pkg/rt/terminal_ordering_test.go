package rt

import (
	"context"
	"testing"
)

// TestTerminalOrdering pins the terminal close family: exactly ONE
// session_ended can ever ship per session, whichever paths race (client
// goodbye, natural ending, repeated goodbye frames), and an ending whose
// wait outlived an interrupt must not close the classroom that replaced it.
func TestTerminalOrdering(t *testing.T) {
	t.Run("a summary wait that outlived an interrupt closes nothing", func(t *testing.T) {
		tm, s, _, _ := ttDriveToCheck(t, ttCards("苹果"), newFakeWords(ttCards("苹果")))
		sentBefore := s.countOf(TypeTeacherSentence)

		// The real ending entry on the test's own context: `ended` closes
		// when the ending goroutine has fully settled, so the assertions
		// below judge its ACTUAL last move, not a timing guess.
		ctx, cancel := context.WithCancel(context.Background())
		ended := make(chan struct{})
		go func() {
			tm.runNaturalEnding(s, ctx, cancel, "今天就写到这里。", purposeNoDue, ReasonNoDueWords)
			close(ended)
		}()
		ttAwaitSpoken(t, s, sentBefore+1) // the ending's announcement spoke
		tm.mu.Lock()
		epochBefore := tm.epoch
		tm.mu.Unlock()

		// The interrupt cancels the parked ending's playback wait and bumps
		// the epoch; the ending's only remaining move is the epoch-gated
		// orderlyCloseAt — the stale wait must not close (or goodbye) the
		// classroom the interrupt kept open.
		tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		ttAwaitGate(t, ended, "the ending never settled after the interrupt")

		tm.mu.Lock()
		closed, state, epoch := tm.closed, tm.state, tm.epoch
		tm.mu.Unlock()
		if closed || state != StateListening || epoch != epochBefore+1 {
			t.Fatalf("closed=%v state=%q epoch=%d, want the interrupted classroom alive at %d",
				closed, state, epoch, epochBefore+1)
		}
		if got := s.countOf(TypeSessionEnded); got != 0 {
			t.Fatalf("session_ended count = %d, want none from the stale wait", got)
		}
		// The gates themselves, at the stale epoch: both terminal flavors
		// must refuse as well.
		if tm.tryTerminalCloseAt(s, epochBefore, ReasonLessonComplete) {
			t.Fatal("a stale-epoch ending claimed the terminal close")
		}
		tm.handleEndSession(s)
		if got := s.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want exactly the fresh goodbye", got)
		}
	})

	t.Run("repeated goodbye frames ship one session_ended", func(t *testing.T) {
		tm, s, _, _ := ttDriveToCheck(t, ttCards("苹果", "香蕉"), newFakeWords(ttCards("苹果", "香蕉")))

		tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))

		if got := s.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want exactly 1", got)
		}
		ended := s.controlsOf(t, TypeSessionEnded)
		if ended[0]["reason"] != ReasonClientEnd {
			t.Fatalf("session_ended reason = %v, want %q", ended[0]["reason"], ReasonClientEnd)
		}
		tm.mu.Lock()
		closed := tm.closed
		tm.mu.Unlock()
		if !closed {
			t.Fatal("session is not closed after the goodbye")
		}
	})

	t.Run("an active goodbye silences the parked natural ending", func(t *testing.T) {
		tm, s, _, _ := ttConfirmToCheckSummary(t, newFakeWords(ttCards("苹果")))

		// The client hangs up while the battle summary waits for its ack:
		// the active goodbye wins, the ending's cancelled wait says nothing.
		tm.handleEndSession(s)
		if got := s.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count after the active goodbye = %d, want 1", got)
		}
		ended := s.controlsOf(t, TypeSessionEnded)
		if ended[0]["reason"] != ReasonClientEnd {
			t.Fatalf("session_ended reason = %v, want the client end's own", ended[0]["reason"])
		}
	})
}
