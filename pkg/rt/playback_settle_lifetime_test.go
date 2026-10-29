package rt

import (
	"testing"
	"time"
)

// TestPlaybackSettleLifetime pins the announcement's ACK post-processing to
// ONE protected turn effect: the settle (listening state + state message +
// echo tail) and the work it licenses (word event, per-word timer arm) all
// apply at the announcement's own epoch or not at all. A close or interrupt
// landing between the valid final ack and the settle suppresses the whole
// lot — no listening revival, no timer armed for a dead or replaced turn.
func TestPlaybackSettleLifetime(t *testing.T) {
	// ttArmSettleProbe parks the turn goroutine between the announcement's
	// valid final ack (awaitPlaybackCompletion returned completed) and the
	// protected settle — the real execution position of the race.
	ttArmSettleProbe := func(tm *TurnManager) (hit, release chan struct{}) {
		hit, release = make(chan struct{}), make(chan struct{})
		tm.settleInterleave = func() {
			tm.settleInterleave = nil
			close(hit)
			<-release
		}
		return hit, release
	}

	// ttDriveConfirmAnnouncement drives a real confirm turn (好了) whose
	// next-word announcement carries the announceAcked post-processing, and
	// parks it at the settle probe once its final sentence is acked. The
	// returned release channel lets the test resume the parked settle.
	ttDriveConfirmAnnouncement := func(t *testing.T) (*TurnManager, *captureSender, *captureLogger, chan struct{}, chan struct{}) {
		t.Helper()
		asr := newFakeASR(asrScript{text: "好了"})
		llm := newFakeLLM(intentScript(intentConfirm, ""))
		tm, s, log, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, llm)
		hit, release := ttArmSettleProbe(tm)
		sentBefore := s.countOf(TypeTeacherSentence)

		done := ttStartTurn(tm, s)
		ttAwaitSpoken(t, s, sentBefore+1) // the next-word announcement spoke
		ttAckLast(t, tm, s, 0)            // its valid final ack
		ttAwaitGate(t, hit, "settle probe was not reached after the ack")
		return tm, s, log, done, release
	}

	t.Run("close between the ack and the settle suppresses state, event and timer", func(t *testing.T) {
		tm, s, log, done, release := ttDriveConfirmAnnouncement(t)
		listeningBefore := s.listeningCount()
		announcedBefore := ttEventCount(t, log, EventWordAnnounced)
		tm.mu.Lock()
		completedAtBefore := tm.lastPlaybackCompletedAt
		tm.mu.Unlock()

		tm.handleEndSession(s)
		genAfterClose, _ := ttTimerState(tm) // the close stops the live timer
		release <- struct{}{}                // resume the parked settle

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("confirm turn did not finish after the close")
		}

		if got := s.listeningCount(); got != listeningBefore {
			t.Fatalf("listening states = %d, want %d after close", got, listeningBefore)
		}
		if got := ttEventCount(t, log, EventWordAnnounced); got != announcedBefore {
			t.Fatalf("word_announced events = %d, want %d after close", got, announcedBefore)
		}
		gen, timer := ttTimerState(tm)
		if gen != genAfterClose || timer != nil {
			t.Fatalf("timerGen=%d (want %d) timer=%v (want none): the dead turn re-armed the countdown",
				gen, genAfterClose, timer != nil)
		}
		tm.mu.Lock()
		state, completedAt := tm.state, tm.lastPlaybackCompletedAt
		tm.mu.Unlock()
		if state != "" {
			t.Fatalf("state = %q, want the closed session's empty state", state)
		}
		if !completedAt.Equal(completedAtBefore) {
			t.Fatal("a closed session's settle stamped a fresh playback-completion time")
		}
	})

	t.Run("interrupt between the ack and the settle suppresses state, event and timer", func(t *testing.T) {
		tm, s, log, done, release := ttDriveConfirmAnnouncement(t)
		announcedBefore := ttEventCount(t, log, EventWordAnnounced)
		tm.mu.Lock()
		completedAtBefore := tm.lastPlaybackCompletedAt
		tm.mu.Unlock()

		tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		// The interrupt's own state message (listening) is the new baseline.
		listeningAfterInterrupt := s.listeningCount()
		genAfter, timerAfter := ttTimerState(tm)
		release <- struct{}{}

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("confirm turn did not finish after the interrupt")
		}

		if got := s.listeningCount(); got != listeningAfterInterrupt {
			t.Fatalf("listening states = %d, want %d after interrupt", got, listeningAfterInterrupt)
		}
		if got := ttEventCount(t, log, EventWordAnnounced); got != announcedBefore {
			t.Fatalf("word_announced events = %d, want %d after interrupt", got, announcedBefore)
		}
		gen, timer := ttTimerState(tm)
		if gen != genAfter || timer != timerAfter {
			t.Fatal("the interrupted turn's settle armed or replaced the countdown")
		}
		tm.mu.Lock()
		completedAt := tm.lastPlaybackCompletedAt
		tm.mu.Unlock()
		if !completedAt.Equal(completedAtBefore) {
			t.Fatal("an interrupted turn's settle stamped a fresh playback-completion time")
		}
	})
}
