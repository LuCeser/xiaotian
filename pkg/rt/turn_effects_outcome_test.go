package rt

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// turnEffectBlockingLogger blocks inside Warn once, letting the test prove
// the delivery-failure log fires OUTSIDE tm.mu (a blocked logger must not
// hold the manager's lock hostage — issue #69 alt1 step 5).
type turnEffectBlockingLogger struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (l *turnEffectBlockingLogger) Debug(string, ...infra.Field) {}
func (l *turnEffectBlockingLogger) Info(string, ...infra.Field)  {}
func (l *turnEffectBlockingLogger) Error(string, ...infra.Field) {}

func (l *turnEffectBlockingLogger) Warn(string, ...infra.Field) {
	l.once.Do(func() { close(l.entered) })
	<-l.release
}

func (l *turnEffectBlockingLogger) With(...infra.Field) infra.Logger { return l }

func (l *turnEffectBlockingLogger) Sync() error { return nil }

// TestTurnEffectOutcome is 步骤 5 的验收（R，†race）：
//   - 未执行（身份失效）与已执行但交付失败在新入口上结果不同，Err 保留；
//   - 闭包至多执行一次（失效零次、成功/失败恰一次）；
//   - 交付失败的日志在锁外发出——日志阻塞期间 tm.mu 必须可用；
//   - bool 兼容包装保持旧形状。
func TestTurnEffectOutcome(t *testing.T) {
	t.Run("stale and delivery failure are distinct", func(t *testing.T) {
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, nil, nil)
		tm.epoch = 7

		calls := 0
		res := tm.applyTurnOutcomeAt(8, func() error { calls++; return nil })
		if res.Outcome != TurnEffectStale || calls != 0 || res.Err != nil || res.Applied() {
			t.Fatalf("stale: res=%+v calls=%d", res, calls)
		}

		sentinel := errors.New("enqueue refused")
		calls = 0
		res = tm.applyTurnOutcomeAt(7, func() error { calls++; return sentinel })
		if res.Outcome != TurnEffectDeliveryFailed || calls != 1 || !errors.Is(res.Err, sentinel) {
			t.Fatalf("delivery failure: res=%+v calls=%d", res, calls)
		}

		calls = 0
		res = tm.applyTurnOutcomeAt(7, func() error { calls++; return nil })
		if res.Outcome != TurnEffectApplied || calls != 1 || !res.Applied() || res.Err != nil {
			t.Fatalf("applied: res=%+v calls=%d", res, calls)
		}
	})

	t.Run("closed session is stale even when the effect would fail", func(t *testing.T) {
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, nil, nil)
		tm.epoch, tm.closed = 7, true
		calls := 0
		res := tm.applyTurnOutcomeAt(7, func() error { calls++; return errors.New("boom") })
		if res.Outcome != TurnEffectStale || calls != 0 {
			t.Fatalf("closed: res=%+v calls=%d, want stale with zero calls", res, calls)
		}
	})

	t.Run("session and check-stage entries share the shape", func(t *testing.T) {
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, nil, nil)

		sentinel := errors.New("session enqueue refused")
		if res := tm.applySessionOutcome(func() error { return sentinel }); res.Outcome != TurnEffectDeliveryFailed || res.Err != sentinel {
			t.Fatalf("session delivery: %+v", res)
		}
		if res := tm.applySessionOutcome(func() error { return nil }); res.Outcome != TurnEffectApplied {
			t.Fatalf("session applied: %+v", res)
		}
		tm.closed = true
		if res := tm.applySessionOutcome(func() error { return nil }); res.Outcome != TurnEffectStale {
			t.Fatalf("session stale: %+v", res)
		}
		tm.closed = false

		// Check stage: no tutor / not checking ⇒ stale; checking ⇒ applied.
		if res := tm.applyCheckStageOutcome(func() error { return nil }); res.Outcome != TurnEffectStale {
			t.Fatalf("check-stage without tutor: %+v", res)
		}
		tm.tutor = newDictationTutor(lesson.Lesson{LessonID: "tt-effect-outcome", Cards: ttCards("苹果", "香蕉"), Version: 1})
		if res := tm.applyCheckStageOutcome(func() error { return nil }); res.Outcome != TurnEffectStale {
			t.Fatalf("check-stage without checking flag: %+v", res)
		}
		tm.tutor.checking = true
		if res := tm.applyCheckStageOutcome(func() error { return sentinel }); res.Outcome != TurnEffectDeliveryFailed || res.Err != sentinel {
			t.Fatalf("check-stage delivery: %+v", res)
		}
		if res := tm.applyCheckStageOutcome(func() error { return nil }); res.Outcome != TurnEffectApplied {
			t.Fatalf("check-stage applied: %+v", res)
		}
	})

	t.Run("blocked failure log does not hold tm.mu", func(t *testing.T) {
		logger := &turnEffectBlockingLogger{entered: make(chan struct{}), release: make(chan struct{})}
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, nil, logger)
		tm.epoch = 7

		done := make(chan TurnEffectResult, 1)
		go func() {
			done <- tm.applyTurnOutcomeAt(7, func() error { return errors.New("deliver boom") })
		}()

		select {
		case <-logger.entered:
			// The Warn call is now parked inside the logger. tm.mu must be
			// free — the log fired after Unlock.
		case <-time.After(5 * time.Second):
			t.Fatal("the failure log never fired")
		}
		if !tm.mu.TryLock() {
			t.Fatal("tm.mu still held while the failure log is blocked — logging must happen outside the lock")
		}
		tm.mu.Unlock()

		close(logger.release)
		select {
		case res := <-done:
			if res.Outcome != TurnEffectDeliveryFailed {
				t.Fatalf("res=%+v, want delivery failure", res)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the effect never returned after the log was released")
		}
	})

	t.Run("bool wrappers keep the legacy shape", func(t *testing.T) {
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, nil, nil)
		tm.epoch = 7

		if tm.applyTurnAt(8, func() error { return nil }) {
			t.Fatal("stale must stay false")
		}
		if tm.applyTurnAt(7, func() error { return errors.New("boom") }) {
			t.Fatal("delivery failure must stay false")
		}
		if !tm.applyTurnAt(7, func() error { return nil }) {
			t.Fatal("applied must stay true")
		}
	})
}
