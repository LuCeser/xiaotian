package rt

import (
	"context"
	"sync"
	"time"
)

// checkPersistTimeout bounds one accepted check-stage save end-to-end
// (latch write, downstream persistence, receipts). The context outlives the
// turn's cancellation (1.09) but must not wait without limit when the pool
// is starved.
const checkPersistTimeout = 90 * time.Second

// checkPersistContext derives the independent, bounded context for one
// accepted save tied to turnCtx's lifetime but not its cancellation.
func checkPersistContext(turnCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(turnCtx), checkPersistTimeout)
}

// checkWriteAdmission represents one persistence operation accepted before
// closing sealed the session. Its done signal is independent from presentation
// work: callers complete it when database work and its receipt have finished.
// lessonID names the row the accepted save will write (issue #69 rethink3 S4):
// a same-lessonID re-init can ask whether the row's state is still moving.
type checkWriteAdmission struct {
	done     chan struct{}
	lessonID string
	once     sync.Once
}

// admitCheckWriteLocked registers one accepted write. Caller holds tm.mu.
// Formal dictation check_submit must take validity, snapshot, scope in-flight
// dedup, and this registration in the same tm.mu hold (issue #69 R2).
func (tm *TurnManager) admitCheckWriteLocked(lessonID string) (*checkWriteAdmission, bool) {
	if tm.closed || tm.checkWritesSealed {
		return nil, false
	}
	w := &checkWriteAdmission{done: make(chan struct{}), lessonID: lessonID}
	if tm.checkWrites == nil {
		tm.checkWrites = make(map[*checkWriteAdmission]struct{})
	}
	tm.checkWrites[w] = struct{}{}
	return w, true
}

// lessonSaveInFlightLocked reports whether an accepted save for lessonID is
// still running (its admission has not completed). Caller holds tm.mu. A
// re-init of that row while a save is in flight must not build a classroom
// on the pre-save row state (issue #69 rethink3 S4 same-lessonID reentry).
func (tm *TurnManager) lessonSaveInFlightLocked(lessonID string) bool {
	for w := range tm.checkWrites {
		if w.lessonID == lessonID {
			return true
		}
	}
	return false
}

// completeCheckWrite releases an admission on every return path. It is safe
// to call more than once and never holds tm.mu while notifying waiters. The
// row's completion counter moves in the same hold as the deletion (review
// P1-2): a same-lessonID re-init that captured the counter before its row
// read can detect a save landing inside the read→check gap even though the
// registration is already gone by the check.
func (tm *TurnManager) completeCheckWrite(w *checkWriteAdmission) {
	if w == nil {
		return
	}
	w.once.Do(func() {
		tm.mu.Lock()
		delete(tm.checkWrites, w)
		if w.lessonID != "" {
			if tm.lessonSaveDone == nil {
				tm.lessonSaveDone = make(map[string]uint64)
			}
			tm.lessonSaveDone[w.lessonID]++
		}
		tm.mu.Unlock()
		close(w.done)
	})
}

// lessonSaveDoneCountLocked returns how many accepted saves for lessonID
// have completed so far. Caller holds tm.mu.
func (tm *TurnManager) lessonSaveDoneCountLocked(lessonID string) uint64 {
	return tm.lessonSaveDone[lessonID]
}

// lessonRowSaveGuard is the same-lessonID reentry baseline carried from the
// row-read half of continueSessionInit to the INSTALL decision (review P1-2
// fix2): doneBefore snapshots the row's completion counter BEFORE the row
// read; any admission still running, or any completion that moved the
// counter past the snapshot, means the row state the request holds predates
// a save and must never be installed as a classroom — no matter how late
// the check runs. The zero value (empty lessonID) guards nothing: fresh
// rows have no save history to race with.
type lessonRowSaveGuard struct {
	lessonID   string
	doneBefore uint64
}

// lessonRowSaveMovedLocked reports whether an accepted save for the guarded
// row started or landed after the baseline was captured. Caller holds
// tm.mu; the check must run in the SAME tm.mu hold that installs, so the
// admission registry (also mutated under tm.mu) cannot move underneath it.
func (tm *TurnManager) lessonRowSaveMovedLocked(g lessonRowSaveGuard) bool {
	if g.lessonID == "" {
		return false
	}
	return tm.lessonSaveInFlightLocked(g.lessonID) ||
		tm.lessonSaveDoneCountLocked(g.lessonID) != g.doneBefore
}

// waitCheckWrites waits without holding either TurnManager lock. Returns
// false when checkPersistTimeout elapses before every admission completes.
func waitCheckWrites(writes []*checkWriteAdmission) bool {
	if len(writes) == 0 {
		return true
	}
	timer := time.NewTimer(checkPersistTimeout)
	defer timer.Stop()
	for _, w := range writes {
		select {
		case <-w.done:
		case <-timer.C:
			return false
		}
	}
	return true
}
