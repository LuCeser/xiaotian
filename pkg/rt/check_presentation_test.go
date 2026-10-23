package rt

import (
	"testing"
)

// ttEnterCheckCalls counts enter_check tool calls on the executor.
func ttEnterCheckCalls(exec *fakeExecutor) int {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	n := 0
	for _, c := range exec.checkCalls {
		if c.tool == "enter_check" {
			n++
		}
	}
	return n
}

// TestCheckPresentationPreparation pins the first half of the split: preparing
// the persistence input retains the plaintext words and behavior data without
// issuing another EnterCheck write.
func TestCheckPresentationPreparation(t *testing.T) {
	tm, _, _, exec := ttDriveToCheck(t, ttCards("苹果", "香蕉"), newFakeWords(ttCards("苹果", "香蕉")))
	tm.mu.Lock()
	epoch := tm.epoch
	tm.tutor.perWordOutcome[1] = outcomeConfirmed
	tm.tutor.perWordHints[1] = 2
	tm.mu.Unlock()

	enterCallsBefore := ttEnterCheckCalls(exec)
	in := tm.prepareCheckStageInputs(epoch)
	if got := in.outcomes["1"]; got.Outcome != outcomeConfirmed || got.Hints != 2 {
		t.Fatalf("prepared outcome = %+v, want confirmed with two hints", got)
	}
	if len(in.words) != 2 || in.words[0] != "苹果" || in.words[1] != "香蕉" {
		t.Fatalf("prepared words = %v, want the full plaintext list", in.words)
	}
	if got := ttEnterCheckCalls(exec); got != enterCallsBefore {
		t.Fatalf("enter_check calls = %d, want %d", got, enterCallsBefore)
	}
}
