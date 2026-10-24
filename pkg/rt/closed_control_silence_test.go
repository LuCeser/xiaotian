package rt

import (
	"sync"
	"testing"
)

// ttTotalControls snapshots the captured control count (the zero-output
// assertion's baseline).
func ttTotalControls(s *captureSender) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.controls)
}

// TestClosedControlSilence pins the closed session's control surface
// (issue #69 备选一 步骤 10): after session_ended, generic control errors,
// interrupts and read_word produce ZERO wire output and no epoch bump, and
// an interrupt racing the goodbye — whichever wins — can never enqueue
// anything that trails session_ended.
func TestClosedControlSilence(t *testing.T) {
	t.Run("closed session answers nothing and keeps its epoch", func(t *testing.T) {
		tm, s, log, _ := ttDriveToCheck(t, ttCards("苹果"), newFakeWords(ttCards("苹果")))
		tm.handleEndSession(s)
		if got := s.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want 1", got)
		}
		before, epochBefore := ttTotalControls(s), tm.currentEpoch()

		// The post-terminal barrage: an unparseable frame, a server-only
		// type, an interrupt, a read_word and a ping.
		tm.HandleControl(s, []byte("not json"))
		tm.HandleControl(s, mustJSON(t, TeacherSentenceMsg{Type: TypeTeacherSentence}))
		tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt, LastSentenceID: "t1-s0"}))
		tm.HandleControl(s, mustJSON(t, ReadWordMsg{Type: TypeReadWord, WordIndex: 0, CurrentIndex: 0}))
		tm.HandleControl(s, mustJSON(t, PingMsg{Type: TypePing, TClientMs: 9}))

		if got := ttTotalControls(s); got != before {
			t.Fatalf("controls after the close = %d, want unchanged %d (order = %v)", got, before, s.typeOrder())
		}
		if got := tm.currentEpoch(); got != epochBefore {
			t.Fatalf("epoch after the closed interrupt = %d, want unchanged %d", got, epochBefore)
		}
		if got := ttEventCount(t, log, EventInterruptHandled); got != 0 {
			t.Fatalf("interrupt_handled events after the close = %d, want 0", got)
		}
	})

	t.Run("an interrupt racing the goodbye never trails session_ended", func(t *testing.T) {
		tm, s, _, _ := ttDriveToCheck(t, ttCards("苹果"), newFakeWords(ttCards("苹果")))

		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, c := range []func(){
			func() { tm.handleInterrupt(s, InterruptMsg{Type: TypeInterrupt}) },
			func() { tm.handleEndSession(s) },
		} {
			c := c
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				c()
			}()
		}
		close(start)
		wg.Wait()

		if got := s.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want 1", got)
		}
		order := s.typeOrder()
		endAt := -1
		for i, typ := range order {
			if typ == TypeSessionEnded {
				endAt = i
				break
			}
		}
		if endAt < 0 {
			t.Fatalf("session_ended missing (order = %v)", order)
		}
		for i, typ := range order[endAt+1:] {
			if typ == TypeInterruptAck || typ == TypeState {
				t.Fatalf("%s at %d trails session_ended at %d (order = %v)", typ, endAt+1+i, endAt, order)
			}
		}
	})
}
