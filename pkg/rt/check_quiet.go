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

// enterGradingAt sets the post-bridge check stage: no listening, no echo tail.
func (tm *TurnManager) enterGradingAt(s Sender, epoch uint64) {
	tm.mu.Lock()
	if tm.state == "" || tm.closed || tm.epoch != epoch {
		tm.mu.Unlock()
		return
	}
	tm.state = StateGrading
	tm.playbackPending = false
	value := tm.effectiveClientStateLocked()
	tm.mu.Unlock()
	_ = s.SendControl(StateMsg{Type: TypeState, Value: value})
}
