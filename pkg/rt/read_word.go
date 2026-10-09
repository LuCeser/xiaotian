package rt

import "context"

func (tm *TurnManager) handleReadWord(s Sender, m ReadWordMsg) {
	tm.mu.Lock()
	t := tm.tutor
	if tm.closed || t == nil || (t.completed && !t.checking) || tm.state != StateListening || tm.playbackPending ||
		m.WordIndex < 0 || m.WordIndex >= len(t.words) || m.CurrentIndex != t.currentIndex ||
		(!t.checking && (m.WordIndex > t.currentIndex || t.currentIndex >= len(t.words))) {
		tm.mu.Unlock()
		tm.sendError(s, ErrCodeInvalidMessage, "现在不能重读这个词，请点已经报过的序号", true)
		return
	}
	spoken := repeatText(t.words[m.WordIndex].Word)
	display := spoken
	if tm.mode != ModeTest && !t.checking {
		display = repeatMaskedText(m.WordIndex + 1)
	}
	tm.stopWordTimerLocked()
	epoch, turnID := tm.epoch, tm.nextTurnIDLocked()
	ctx, cancel := context.WithCancel(context.Background())
	tm.turnCancel = cancel
	tm.state = StateProcessing
	tm.mu.Unlock()
	_ = s.SendControl(TurnStartMsg{Type: TypeTurnStart, TurnID: turnID, Epoch: epoch})
	go func() {
		defer cancel()
		ref := tm.speakTurn(s, turnID, epoch, spoken, display, purposeChat, ctx, cancel)
		completed, timedOut := tm.awaitPlaybackCompletion(s, ref, purposeChat)
		if timedOut {
			tm.orderlyClose()
		} else if completed {
			tm.settleListening(s)
			tm.armWordTimer()
		}
	}()
}
