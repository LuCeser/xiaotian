package rt

import "github.com/pkg/errors"

// errClassroomInstallStale signals a superseded init reservation inside an
// install effect; the install entry maps it to TurnEffectStale.
var errClassroomInstallStale = errors.New("rt: stale classroom install")

// classroomScope is the private per-classroom identity on one WebSocket
// connection (issue #69 rethink2 R1). Each successful install gets a fresh
// generation even when the lesson row id is unchanged; superseded scopes
// keep no memory writeback rights on the connection.
type classroomScope struct {
	gen      uint64
	lessonID string
	tutor    *DictationTutor
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
// classroom replacement it cancels in-flight presentation work and bumps
// the playback epoch (distinct from scope generation). Caller holds tm.mu.
func (tm *TurnManager) replaceActiveClassroomLocked(gen uint64, lessonID string, tutor *DictationTutor) bool {
	if !tm.classroomInstallAllowed(gen) {
		return false
	}
	if tm.activeScope != nil {
		tm.stopAskPlaybackLocked()
	}
	tm.activeScope = &classroomScope{gen: gen, lessonID: lessonID, tutor: tutor}
	tm.tutor = tutor
	return true
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

// scopeWritebackAllowed reports whether scopeGen may still mutate the
// connection's current classroom memory (issue #69 R1 writeback half).
func (tm *TurnManager) scopeWritebackAllowed(scopeGen uint64) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.scopeWritebackAllowedLocked(scopeGen)
}

// scopeWritebackAllowedLocked is the locked form. Caller holds tm.mu.
func (tm *TurnManager) scopeWritebackAllowedLocked(scopeGen uint64) bool {
	return !tm.closed && tm.activeScope != nil && tm.activeScope.gen == scopeGen
}
