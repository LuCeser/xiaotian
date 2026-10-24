package rt

import "sync"

// checkWriteAdmission represents one persistence operation accepted before
// closing sealed the session. Its done signal is independent from presentation
// work: callers complete it when database work and its receipt have finished.
type checkWriteAdmission struct {
	done chan struct{}
	once sync.Once
}

// admitCheckWriteLocked registers one accepted write. Caller holds tm.mu.
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

// waitCheckWrites waits without holding either TurnManager lock.
func waitCheckWrites(writes []*checkWriteAdmission) {
	for _, w := range writes {
		<-w.done
	}
}
