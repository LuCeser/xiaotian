package rt

import (
	"context"
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

type admissionGateExecutor struct {
	*fakeExecutor
	entered chan struct{}
	release chan struct{}
	err     error
}

func (g *admissionGateExecutor) AdvanceDictation(ctx context.Context, in lesson.AdvanceDictationInput) (lesson.AdvanceDictationResult, error) {
	close(g.entered)
	<-g.release
	if g.err != nil {
		return lesson.AdvanceDictationResult{}, g.err
	}
	if err := ctx.Err(); err != nil {
		return lesson.AdvanceDictationResult{}, err
	}
	return g.fakeExecutor.AdvanceDictation(ctx, in)
}

func admissionFixture(t *testing.T, exec LessonExecutor) (*TurnManager, *captureSender, TurnTarget) {
	t.Helper()
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec,
		ttConfig(newFakeWords(ttCards("苹果", "香蕉"))), NewEventRecorder(infra.NewNoOpLogger(), "admission"), nil)
	tm.tutor = newDictationTutor(lesson.Lesson{LessonID: ttLessonID, Cards: ttCards("苹果", "香蕉"), Version: 1})
	tm.state = StateListening
	return tm, &captureSender{}, tm.tutor.turnTarget()
}

func TestAdvanceAdmission(t *testing.T) {
	t.Run("cancellation before admission rejects without an executor call", func(t *testing.T) {
		base := newFakeExecutor(ttLessonID, ttCards("苹果", "香蕉"))
		gate := &admissionGateExecutor{fakeExecutor: base, entered: make(chan struct{}), release: make(chan struct{})}
		close(gate.release)
		tm, sender, target := admissionFixture(t, gate)
		tm.HandleControl(sender, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		if _, _, accepted := tm.executeDictationAdvance(context.Background(), 0, target, lesson.WordOutcome{Outcome: lesson.OutcomeConfirmed}); accepted {
			t.Fatal("stale command was accepted")
		}
		select {
		case <-gate.entered:
			t.Fatal("executor was called after cancellation")
		default:
		}
	})

	t.Run("admission before cancellation lets the accepted write finish", func(t *testing.T) {
		base := newFakeExecutor(ttLessonID, ttCards("苹果", "香蕉"))
		gate := &admissionGateExecutor{fakeExecutor: base, entered: make(chan struct{}), release: make(chan struct{})}
		tm, sender, target := admissionFixture(t, gate)
		done := make(chan error, 1)
		go func() {
			_, err, _ := tm.executeDictationAdvance(context.Background(), 0, target, lesson.WordOutcome{Outcome: lesson.OutcomeConfirmed})
			done <- err
		}()
		ttAwaitGate(t, gate.entered, "accepted executor call did not start")
		tm.HandleControl(sender, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		close(gate.release)
		if err := <-done; err != nil {
			t.Fatalf("accepted write: %v", err)
		}
		if base.lesson.CurrentIndex != 1 || tm.pendingAdvance != nil {
			t.Fatalf("stored index/pending = %d/%v, want 1/nil", base.lesson.CurrentIndex, tm.pendingAdvance)
		}
	})

	sentinel := errors.New("write failed")
	for _, tc := range []struct {
		gateErr  error
		want     error
		name     string
		conflict int
		cancel   bool
	}{
		{name: "executor error", gateErr: sentinel, want: sentinel},
		{name: "version conflict", conflict: 7},
		{name: "canceled executor context", cancel: true, want: context.Canceled},
	} {
		t.Run(tc.name+" releases every same-target waiter", func(t *testing.T) {
			base := newFakeExecutor(ttLessonID, ttCards("苹果", "香蕉"))
			base.conflictVersion = tc.conflict
			gate := &admissionGateExecutor{fakeExecutor: base, entered: make(chan struct{}), release: make(chan struct{}), err: tc.gateErr}
			tm, _, target := admissionFixture(t, gate)
			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancel {
				cancel()
			} else {
				defer cancel()
			}
			done := make(chan error, 2)
			go func() {
				_, err, _ := tm.executeDictationAdvance(ctx, 0, target, lesson.WordOutcome{Outcome: lesson.OutcomeConfirmed})
				done <- err
			}()
			ttAwaitGate(t, gate.entered, "executor call did not start")
			pending, owner := tm.admitDictationAdvance(0, target, lesson.WordOutcome{Outcome: lesson.OutcomeTimeout})
			if pending == nil || owner {
				t.Fatal("same target did not join the accepted command")
			}
			go func() { <-pending.done; done <- pending.err }()
			close(gate.release)
			for range 2 {
				if err := <-done; err == nil || tc.want != nil && !errors.Is(err, tc.want) {
					t.Fatalf("waiter error = %v, want %v", err, tc.want)
				}
			}
			if tm.pendingAdvance != nil {
				t.Fatal("failed command left a pending record")
			}
		})
	}
}
