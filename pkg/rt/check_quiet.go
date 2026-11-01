package rt

import (
	"context"
	"time"
)

// announceCheckQuiet speaks the check-stage bridge like runAnnouncementDisplayAt,
// but a playback-ack timeout never emits playback_ack_timeout or orderlyCloseAt.
// When the wait ends (ack, timeout, or interrupt), the FSM enters StateGrading
// so uplink is gated without returning to listening.
func (tm *TurnManager) announceCheckQuiet(s Sender, epoch uint64, ctx context.Context, cancel context.CancelFunc, spoken, display, purpose string) {
	defer cancel()

	tm.mu.Lock()
	if tm.epoch != epoch || tm.closed {
		tm.mu.Unlock()
		return
	}
	turnID := tm.nextTurnIDLocked()
	tm.mu.Unlock()
	if tm.announceEntryInterleave != nil {
		tm.announceEntryInterleave()
	}

	ref := tm.speakTurn(s, turnID, epoch, spoken, display, purpose, ctx, cancel)
	tm.waitCheckBridgePlayback(ref)
	tm.enterGradingAt(s, epoch)
}

// waitCheckBridgePlayback blocks up to playbackAckTimeout without sending
// playback_ack_timeout on expiry (Issue #96 U2).
func (tm *TurnManager) waitCheckBridgePlayback(ref PlaybackRef) {
	if ref.FinalSentenceID == "" {
		return
	}
	tm.mu.Lock()
	fin, ok := tm.pendingPlayback[pendingKey(ref.Epoch, ref.FinalSentenceID)]
	remaining := 0
	for _, sid := range ref.SentenceIDs {
		if p, found := tm.pendingPlayback[pendingKey(ref.Epoch, sid)]; found && !p.completed {
			remaining += p.pcmSamples
		}
	}
	tm.mu.Unlock()
	if !ok {
		return
	}

	wait := playbackAckTimeout(remaining)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-fin.done:
	case <-timer.C:
	}
}

// enterGradingLocked is the grading transition's locked half (caller holds
// tm.mu and has already verified the session is live at its epoch): no
// listening, no echo tail — the check panel keeps the uplink gated.
func (tm *TurnManager) enterGradingLocked(s Sender) {
	tm.state = StateGrading
	tm.playbackPending = false
	_ = s.SendControl(StateMsg{Type: TypeState, Value: tm.effectiveClientStateLocked()})
}

// enterGradingAt sets the post-bridge check stage. The liveness gate, the
// state write, and the state enqueue are ONE tm.mu hold (#96 N1): SendControl
// is a non-blocking enqueue, so an end_session can no longer land its
// terminal session_ended between the gate and the frame.
func (tm *TurnManager) enterGradingAt(s Sender, epoch uint64) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.state == "" || tm.closed || tm.epoch != epoch {
		return
	}
	tm.enterGradingLocked(s)
}
