package rt

import "github.com/xiaotian/home-tutor-agent/pkg/infra"

// applyTurnAt accepts one turn's in-memory changes and control-message
// enqueues as a unit. Provider calls, database work and waits belong outside
// the effect; Sender enqueues are non-blocking and may run while mu is held.
func (tm *TurnManager) applyTurnAt(epoch uint64, effect func() error) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.applyTurnLocked(epoch, effect)
}

// applyTurnLocked is for callers already coordinating several changes under
// tm.mu. A false result means the turn was rejected or its enqueue failed;
// callers must not continue with later presentation work.
func (tm *TurnManager) applyTurnLocked(epoch uint64, effect func() error) bool {
	if tm.epoch != epoch || tm.closed {
		return false
	}
	if err := effect(); err != nil {
		tm.logger.Warn("turn effect enqueue failed",
			infra.F("epoch", epoch),
			infra.ErrField(err))
		return false
	}
	return true
}

// applyCheckStageEffect is the check-stage sibling of applyTurnAt for the
// surfaces that run OUTSIDE any turn — the photo OCR pass carries no epoch
// of its own. The closed check, the still-checking check, the memory update
// and the control enqueue are ONE hold: a session that ended — or a check
// stage that already submitted — never sees a late presentation, while the
// accepted writes themselves complete outside the effect (the upload was
// admitted when its final chunk landed in a live check stage).
func (tm *TurnManager) applyCheckStageEffect(effect func() error) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.closed || tm.tutor == nil || !tm.tutor.checking {
		return false
	}
	if err := effect(); err != nil {
		tm.logger.Warn("check stage effect enqueue failed",
			infra.ErrField(err))
		return false
	}
	return true
}

// applySessionEffect is the session-scoped sibling of applyTurnAt for the
// classroom starters (pre-turn: no epoch to match): the closed check, the
// memory bind (state, tutor, sessionReadyAt) and the ready/error enqueue
// are ONE hold, so a close that races the init IO either refuses the whole
// start or lands behind the accepted message — session_ready/error can
// never trail session_ended on the wire. Locked helpers that take tm.mu
// themselves (sessionCategory, lessonViewFor, …) must run BEFORE the call.
func (tm *TurnManager) applySessionEffect(effect func() error) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.closed {
		return false
	}
	if err := effect(); err != nil {
		tm.logger.Warn("session effect enqueue failed", infra.ErrField(err))
		return false
	}
	return true
}
