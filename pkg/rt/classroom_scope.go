package rt

import "github.com/pkg/errors"

// errClassroomInstallStale signals a superseded init reservation inside an
// install effect; the install entry maps it to TurnEffectStale.
var errClassroomInstallStale = errors.New("rt: stale classroom install")

// formalCheckSubmitOp identifies one accepted formal dictation check_submit on
// its owning classroom scope (issue #69 rethink2 R2). Pointer identity is the
// operation id; only that operation's completion may clear the scope slot.
type formalCheckSubmitOp struct{}

// classroomScope is the private per-classroom identity on one WebSocket
// connection (issue #69 rethink2 R1). Each successful install gets a fresh
// generation even when the lesson row id is unchanged; superseded scopes
// keep no memory writeback rights on the connection.
type classroomScope struct {
	tutor *DictationTutor
	// formalSubmit is non-nil while that scope's accepted formal save goroutine
	// is in flight (admission through defer tail). Superseded scopes are
	// dropped on replace; their ops never touch the active scope's slot.
	formalSubmit *formalCheckSubmitOp
	lessonID     string
	gen          uint64
}

// releaseFormalCheckSubmitOp clears the in-flight slot only when it still
// belongs to op (old operations must not clear a newer admission on B).
func (tm *TurnManager) releaseFormalCheckSubmitOp(op *formalCheckSubmitOp) {
	if op == nil {
		return
	}
	tm.mu.Lock()
	if tm.activeScope != nil && tm.activeScope.formalSubmit == op {
		tm.activeScope.formalSubmit = nil
	}
	tm.mu.Unlock()
}

// beginClassroomAttempt reserves the next classroom generation before any
// blocking init I/O. A later attempt on the same connection bumps the
// pending generation and makes earlier reservations stale at install time.
func (tm *TurnManager) beginClassroomAttempt() uint64 {
	tm.mu.Lock()
	tm.classroomSeq++
	gen := tm.classroomSeq
	tm.pendingClassroomGen = gen
	tm.mu.Unlock()
	return gen
}

// classroomInstallAllowed reports whether gen is still the latest reservation
// and the connection is live. Caller holds tm.mu.
func (tm *TurnManager) classroomInstallAllowed(gen uint64) bool {
	return !tm.closed && gen == tm.pendingClassroomGen
}

// replaceActiveClassroomLocked installs tutor under gen when allowed. On
// classroom replacement it cancels in-flight presentation work, bumps the
// playback epoch, and (when s is non-nil) ships interrupt_ack BEFORE any
// session_ready in the same install effect so the client adopts the new
// epoch and drops queued audio from the prior classroom. Caller holds tm.mu.
func (tm *TurnManager) replaceActiveClassroomLocked(gen uint64, lessonID string, tutor *DictationTutor, s Sender) error {
	if !tm.classroomInstallAllowed(gen) {
		return errClassroomInstallStale
	}
	if tm.activeScope != nil {
		old := tm.stopAskPlaybackLocked()
		if s != nil {
			if err := s.SendControl(InterruptAckMsg{
				Type:     TypeInterruptAck,
				OldEpoch: old,
				NewEpoch: old + 1,
			}); err != nil {
				return err
			}
		}
	}
	tm.activeScope = &classroomScope{gen: gen, lessonID: lessonID, tutor: tutor}
	tm.tutor = tutor
	return nil
}

// activeClassroomGen returns the live classroom generation, or 0 when none.
func (tm *TurnManager) activeClassroomGen() uint64 {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.activeScope == nil {
		return 0
	}
	return tm.activeScope.gen
}

// scopeWritebackAllowedLocked reports whether scopeGen may still mutate the
// connection's current classroom memory (issue #69 R1 writeback half).
// Caller holds tm.mu.
func (tm *TurnManager) scopeWritebackAllowedLocked(scopeGen uint64) bool {
	return !tm.closed && tm.activeScope != nil && tm.activeScope.gen == scopeGen
}
