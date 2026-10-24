package rt

import (
	"testing"
	"time"
)

func TestCheckWriteAdmission(t *testing.T) {
	t.Run("close wins and seals admission", func(t *testing.T) {
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, nil, nil)
		claim := tm.closeSessionAt(0, false, false, "", nil)
		tm.mu.Lock()
		write, admitted := tm.admitCheckWriteLocked()
		tm.mu.Unlock()
		if !claim.owned || len(claim.checkWrites) != 0 || admitted || write != nil {
			t.Fatalf("claim=%+v admitted=%v write=%v, want owned close with zero writes", claim, admitted, write)
		}
	})

	t.Run("admission wins and close can wait for completion", func(t *testing.T) {
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, nil, nil)
		tm.mu.Lock()
		write, admitted := tm.admitCheckWriteLocked()
		tm.mu.Unlock()
		if !admitted {
			t.Fatal("live session refused check write admission")
		}
		claim := tm.closeSessionAt(0, false, false, "", nil)
		if len(claim.checkWrites) != 1 || claim.checkWrites[0] != write {
			t.Fatalf("captured writes = %v, want the admitted write", claim.checkWrites)
		}

		waited := make(chan struct{})
		go func() {
			waitCheckWrites(claim.checkWrites)
			close(waited)
		}()
		select {
		case <-waited:
			t.Fatal("close wait returned before the admitted write completed")
		case <-time.After(20 * time.Millisecond):
		}

		// Waiting owns no manager lock: both remain immediately available.
		if !tm.initWriteMu.TryLock() {
			t.Fatal("wait held initWriteMu")
		}
		if !tm.mu.TryLock() {
			tm.initWriteMu.Unlock()
			t.Fatal("wait held tm.mu")
		}
		tm.mu.Unlock()
		tm.initWriteMu.Unlock()

		tm.completeCheckWrite(write)
		tm.completeCheckWrite(write)
		select {
		case <-waited:
		case <-time.After(time.Second):
			t.Fatal("close wait did not observe write completion")
		}
	})
}
