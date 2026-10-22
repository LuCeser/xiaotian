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
