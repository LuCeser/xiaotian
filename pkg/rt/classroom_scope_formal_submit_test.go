package rt

import "testing"

// TestReleaseFormalCheckSubmitOpDoesNotClearPeer (rethink2 T5 / R2): only the
// operation object that still owns the active scope slot may clear it — a
// superseded classroom A task must not wipe C's in-flight formal submit.
func TestReleaseFormalCheckSubmitOpDoesNotClearPeer(t *testing.T) {
	tm := &TurnManager{activeScope: &classroomScope{gen: 3}}
	opA := &formalCheckSubmitOp{}
	opC := &formalCheckSubmitOp{}
	tm.activeScope.formalSubmit = opC

	tm.releaseFormalCheckSubmitOp(opA)
	tm.mu.Lock()
	if tm.activeScope.formalSubmit != opC {
		t.Fatalf("formalSubmit = %p, want %p (A must not clear C's slot)", tm.activeScope.formalSubmit, opC)
	}
	tm.mu.Unlock()

	tm.releaseFormalCheckSubmitOp(opC)
	tm.mu.Lock()
	if tm.activeScope.formalSubmit != nil {
		t.Fatal("C's release did not clear the slot")
	}
	tm.mu.Unlock()
}

// TestFormalCheckSubmitInflightOnActiveScope pins R2 dedup: the in-flight
// latch lives on the active classroom scope, not on the connection.
func TestFormalCheckSubmitInflightOnActiveScope(t *testing.T) {
	tm := &TurnManager{activeScope: &classroomScope{gen: 1}}
	op := &formalCheckSubmitOp{}
	tm.activeScope.formalSubmit = op
	tm.mu.Lock()
	blocked := tm.activeScope == nil || tm.activeScope.formalSubmit != nil
	tm.mu.Unlock()
	if !blocked {
		t.Fatal("second formal submit must see scope in-flight latch")
	}
	tm.releaseFormalCheckSubmitOp(op)
	tm.mu.Lock()
	open := tm.activeScope != nil && tm.activeScope.formalSubmit == nil
	tm.mu.Unlock()
	if !open {
		t.Fatal("slot must open after the owning task releases")
	}
}
