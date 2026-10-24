package rt

import "github.com/xiaotian/home-tutor-agent/pkg/infra"

// TurnEffectOutcome classifies one effect wrapper's execution (issue #69
// 备选一步骤 5). The legacy boolean shape folded two fundamentally
// different outcomes into one false — identity-stale (the closure never
// ran, memory and queues untouched) and executed-but-delivery-failed (the
// closure ran, memory may be partially mutated, at least one enqueue
// failed) — leaving callers unable to pick the right failure cleanup.
type TurnEffectOutcome int

const (
	// TurnEffectStale: the epoch was superseded or the session closed; the
	// closure never executed and nothing changed.
	TurnEffectStale TurnEffectOutcome = iota
	// TurnEffectApplied: the closure ran and every memory change and
	// enqueue it made was accepted.
	TurnEffectApplied
	// TurnEffectDeliveryFailed: the closure ran and at least one enqueue or
	// send failed; Err keeps the concrete delivery error. Callers must run
	// their failure cleanup — the effect is NOT retryable as success.
	TurnEffectDeliveryFailed
)

// TurnEffectResult is the effect entry's discriminated return shape.
type TurnEffectResult struct {
	// Err carries the concrete delivery error exactly when Outcome is
	// TurnEffectDeliveryFailed; nil otherwise.
	Err     error
	Outcome TurnEffectOutcome
}

// Applied reports whether the effect fully landed (legacy-bool equivalent).
func (r TurnEffectResult) Applied() bool { return r.Outcome == TurnEffectApplied }

// applyTurnResultLocked is the locked core: short in-memory changes plus
// non-blocking enqueues only. It NEVER logs — provider calls, database
// work, waits and logging belong outside tm.mu (issue #69 alt1 step 5;
// the rethink's "锁内日志" finding). Caller holds tm.mu.
func (tm *TurnManager) applyTurnResultLocked(epoch uint64, effect func() error) TurnEffectResult {
	if tm.epoch != epoch || tm.closed {
		return TurnEffectResult{Outcome: TurnEffectStale}
	}
	if err := effect(); err != nil {
		return TurnEffectResult{Outcome: TurnEffectDeliveryFailed, Err: err}
	}
	return TurnEffectResult{Outcome: TurnEffectApplied}
}

// applySessionResultLocked is applyTurnResultLocked's session-scoped
// sibling (pre-turn starters: no epoch to match, only the closed check).
// Caller holds tm.mu; no logging here.
func (tm *TurnManager) applySessionResultLocked(effect func() error) TurnEffectResult {
	if tm.closed {
		return TurnEffectResult{Outcome: TurnEffectStale}
	}
	if err := effect(); err != nil {
		return TurnEffectResult{Outcome: TurnEffectDeliveryFailed, Err: err}
	}
	return TurnEffectResult{Outcome: TurnEffectApplied}
}

// applyCheckStageResultLocked is the check-stage sibling: the surfaces that
// run OUTSIDE any turn (photo OCR carries no epoch of its own). Caller
// holds tm.mu; no logging here.
func (tm *TurnManager) applyCheckStageResultLocked(effect func() error) TurnEffectResult {
	if tm.closed || tm.tutor == nil || !tm.tutor.checking {
		return TurnEffectResult{Outcome: TurnEffectStale}
	}
	if err := effect(); err != nil {
		return TurnEffectResult{Outcome: TurnEffectDeliveryFailed, Err: err}
	}
	return TurnEffectResult{Outcome: TurnEffectApplied}
}

// applyTurnOutcomeAt takes one turn's in-memory changes and control-message
// enqueues as a unit and returns the discriminated result. Provider calls,
// database work and waits belong outside the effect. The delivery-failure
// log fires AFTER the lock is released — a blocked logger can never hold
// tm.mu hostage.
func (tm *TurnManager) applyTurnOutcomeAt(epoch uint64, effect func() error) TurnEffectResult {
	tm.mu.Lock()
	res := tm.applyTurnResultLocked(epoch, effect)
	tm.mu.Unlock()
	if res.Outcome == TurnEffectDeliveryFailed {
		tm.logger.Warn("turn effect enqueue failed",
			infra.F("epoch", epoch),
			infra.ErrField(res.Err))
	}
	return res
}

// applySessionOutcome is applyTurnOutcomeAt's session-scoped sibling; the
// failure log likewise fires outside tm.mu.
func (tm *TurnManager) applySessionOutcome(effect func() error) TurnEffectResult {
	tm.mu.Lock()
	res := tm.applySessionResultLocked(effect)
	tm.mu.Unlock()
	if res.Outcome == TurnEffectDeliveryFailed {
		tm.logger.Warn("session effect enqueue failed", infra.ErrField(res.Err))
	}
	return res
}

// applyCheckStageOutcome is applyTurnOutcomeAt's check-stage sibling; the
// failure log likewise fires outside tm.mu.
func (tm *TurnManager) applyCheckStageOutcome(effect func() error) TurnEffectResult {
	tm.mu.Lock()
	res := tm.applyCheckStageResultLocked(effect)
	tm.mu.Unlock()
	if res.Outcome == TurnEffectDeliveryFailed {
		tm.logger.Warn("check stage effect enqueue failed", infra.ErrField(res.Err))
	}
	return res
}

// applyTurnAt is the legacy boolean wrapper over applyTurnOutcomeAt for
// entrances not yet migrated to the result shape (they migrate per
// issue-69-alt1 steps 19–30; this wrapper then disappears).
func (tm *TurnManager) applyTurnAt(epoch uint64, effect func() error) bool {
	return tm.applyTurnOutcomeAt(epoch, effect).Applied()
}

// applyTurnLocked is the legacy shape for callers already holding tm.mu.
// DELIBERATELY no logging here: the four remaining callers (read_word,
// speakTurn, settleAnnouncementAt siblings) migrate to the result entries
// in steps 19/21/24–27, at which point this wrapper is deleted. Until then
// a delivery failure on those paths is observable through the result the
// caller already handles.
func (tm *TurnManager) applyTurnLocked(epoch uint64, effect func() error) bool {
	return tm.applyTurnResultLocked(epoch, effect).Applied()
}

// applySessionEffect is the legacy boolean wrapper over applySessionOutcome.
func (tm *TurnManager) applySessionEffect(effect func() error) bool {
	return tm.applySessionOutcome(effect).Applied()
}

// applyCheckStageEffect is the legacy boolean wrapper over
// applyCheckStageOutcome.
func (tm *TurnManager) applyCheckStageEffect(effect func() error) bool {
	return tm.applyCheckStageOutcome(effect).Applied()
}
