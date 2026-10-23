package rt

import (
	"context"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
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
	in, ok := tm.prepareCheckStageInputs(epoch)
	if !ok {
		t.Fatal("current check preparation was rejected")
	}
	if got := in.outcomes["1"]; got.Outcome != outcomeConfirmed || got.Hints != 2 {
		t.Fatalf("prepared outcome = %+v, want confirmed with two hints", got)
	}
	if len(in.words) != 2 || in.words[0] != "苹果" || in.words[1] != "香蕉" {
		t.Fatalf("prepared words = %v, want the full plaintext list", in.words)
	}
	if got := ttEnterCheckCalls(exec); got != enterCallsBefore {
		t.Fatalf("enter_check calls = %d, want %d", got, enterCallsBefore)
	}

	tm.mu.Lock()
	tm.tutor.completed = false
	stale := tm.epoch + 1
	tm.mu.Unlock()
	if _, ok := tm.prepareCheckStageInputs(stale); ok {
		t.Fatal("stale check preparation was accepted")
	}
	tm.mu.Lock()
	completed := tm.tutor.completed
	tm.mu.Unlock()
	if completed {
		t.Fatal("stale check preparation marked the current tutor completed")
	}
}

func TestCheckPresentation(t *testing.T) {
	tm, s, log, exec := ttDriveToCheck(t, ttCards("苹果", "香蕉"), newFakeWords(ttCards("苹果", "香蕉")))
	wantVerdicts := []lesson.CheckVerdict{{WordIndex: 0, Correct: true, Manual: true}}
	tm.mu.Lock()
	epoch := tm.epoch
	tm.tutor.checkVerdicts = wantVerdicts
	tm.mu.Unlock()
	startedBefore := s.countOf(TypeCheckStarted)
	eventsBefore := ttEventCount(t, log, EventCheckStarted)
	enterCallsBefore := ttEnterCheckCalls(exec)
	sentBefore := s.countOf(TypeTeacherSentence)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tm.presentCheckedStage(s, ctx, cancel, checkStageInputs{
			epoch: epoch, total: 2, words: []string{"苹果", "香蕉"},
			reason: CheckStartedReasonResume, bridge: "我们来看看写得怎么样。",
		}, func() {})
		close(done)
	}()
	ttAwaitSpoken(t, s, sentBefore+1)
	ttAckLast(t, tm, s, 0)
	ttAwaitGate(t, done, "presentation did not settle after the ack")

	started := s.controlsOf(t, TypeCheckStarted)
	last := started[len(started)-1]
	if len(started) != startedBefore+1 || last["reason"] != CheckStartedReasonResume {
		t.Fatalf("check_started = %+v, want one resume presentation", last)
	}
	if got := last["words"].([]any); len(got) != 2 || got[0] != "苹果" || got[1] != "香蕉" {
		t.Fatalf("check_started.words = %v, want the full plaintext list", last["words"])
	}
	if got := ttEnterCheckCalls(exec); got != enterCallsBefore {
		t.Fatalf("enter_check calls = %d, want %d", got, enterCallsBefore)
	}
	if got := ttEventCount(t, log, EventCheckStarted); got != eventsBefore+1 {
		t.Fatalf("check_started events = %d, want %d", got, eventsBefore+1)
	}
	tm.mu.Lock()
	gotVerdicts := append([]lesson.CheckVerdict(nil), tm.tutor.checkVerdicts...)
	tm.mu.Unlock()
	if len(gotVerdicts) != 1 || gotVerdicts[0] != wantVerdicts[0] {
		t.Fatalf("verdicts = %+v, want %+v unchanged", gotVerdicts, wantVerdicts)
	}
}
