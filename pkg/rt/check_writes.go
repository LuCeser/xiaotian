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
type checkWriteAdmission struct {
	done chan struct{}
	once sync.Once
}

// admitCheckWriteLocked registers one accepted write. Caller holds tm.mu.
// Formal dictation check_submit must take validity, snapshot, scope in-flight
// dedup, and this registration in the same tm.mu hold (issue #69 R2).
func (tm *TurnManager) admitCheckWriteLocked() (*checkWriteAdmission, bool) {
	if tm.closed || tm.checkWritesSealed {
		return nil, false
	}
	w := &checkWriteAdmission{done: make(chan struct{})}
	if tm.checkWrites == nil {
		tm.checkWrites = make(map[*checkWriteAdmission]struct{})
	}
	tm.checkWrites[w] = struct{}{}
	return w, true
}

// completeCheckWrite releases an admission on every return path. It is safe
// to call more than once and never holds tm.mu while notifying waiters.
func (tm *TurnManager) completeCheckWrite(w *checkWriteAdmission) {
	if w == nil {
		return
	}
	w.once.Do(func() {
		tm.mu.Lock()
		delete(tm.checkWrites, w)
		tm.mu.Unlock()
		close(w.done)
	})
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
