package rt

import (
	"context"
	"fmt"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// pendingDictationAdvance is one accepted write. Its completion channel is
// closed on every executor return so all callers sharing the fixed target
// can wait without starting another write.
type pendingDictationAdvance struct {
	err    error
	done   chan struct{}
	input  lesson.AdvanceDictationInput
	result lesson.AdvanceDictationResult
}

// admitDictationAdvance defines the cancellation boundary: validation and
// registration share tm.mu. A cancellation that wins the lock rejects the
// command; once registered, the executor call is allowed to finish.
func (tm *TurnManager) admitDictationAdvance(epoch uint64, target TurnTarget, outcome lesson.WordOutcome) (*pendingDictationAdvance, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.closed || tm.epoch != epoch || tm.tutor == nil || tm.tutor.paused ||
		tm.tutor.checking || tm.tutor.completed || target.WordIndex < 0 ||
		target.WordIndex >= len(tm.tutor.words) {
		return nil, false
	}
	current := tm.tutor.turnTarget()
	if current != target {
		return nil, false
	}
	if pending := tm.pendingAdvance; pending != nil {
		if pending.input.LessonID == tm.tutor.lessonID && pending.input.TargetIndex == target.WordIndex {
			return pending, false
		}
		return nil, false
	}
	pending := &pendingDictationAdvance{
		input: lesson.AdvanceDictationInput{
			LessonID: tm.tutor.lessonID, RequestID: fmt.Sprintf("%s-advance-%d", tm.tutor.lessonID, target.WordIndex),
			TargetIndex: target.WordIndex, ExpectedVersion: target.ExpectedVersion, Outcome: outcome,
		},
		done: make(chan struct{}),
	}
	tm.pendingAdvance = pending
	return pending, true
}

func (tm *TurnManager) finishDictationAdvance(pending *pendingDictationAdvance, result lesson.AdvanceDictationResult, err error) {
	tm.mu.Lock()
	pending.result, pending.err = result, err
	if tm.pendingAdvance == pending {
		tm.pendingAdvance = nil
	}
	close(pending.done)
	tm.mu.Unlock()
}

// executeDictationAdvance admits once, executes only for the owner, and
// makes same-target callers observe that owner's result.
func (tm *TurnManager) executeDictationAdvance(ctx context.Context, epoch uint64, target TurnTarget, outcome lesson.WordOutcome) (lesson.AdvanceDictationResult, error, bool) {
	pending, owner := tm.admitDictationAdvance(epoch, target, outcome)
	if pending == nil {
		return lesson.AdvanceDictationResult{}, nil, false
	}
	if owner {
		result, err := tm.exec.AdvanceDictation(ctx, pending.input)
		tm.finishDictationAdvance(pending, result, err)
	}
	<-pending.done
	return pending.result, pending.err, true
}
