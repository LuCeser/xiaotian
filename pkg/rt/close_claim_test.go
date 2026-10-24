package rt

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// failingControlSender rejects the enqueue of one wire type (issue #69
// 备选一 步骤 6/7: the terminal message's transport refusal) while recording
// the attempt, so a close can be observed completing PAST a failed
// session_ended.
type failingControlSender struct {
	*captureSender
	failType string
	failErr  error
	attempts int32
}

func (s *failingControlSender) SendControl(v any) error {
	b, _ := json.Marshal(v)
	var probe struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(b, &probe)
	if probe.Type == s.failType {
		atomic.AddInt32(&s.attempts, 1)
		return s.failErr
	}
	return s.captureSender.SendControl(v)
}

// ttArmCancelCounting installs a counting stand-in for the in-flight turn
// cancellation under tm.mu and returns the counter the close's cleanup is
// observed through (internalCloseLocked cancels it exactly once — the
// one-owner close must never run the cleanup twice).
func ttArmCancelCounting(tm *TurnManager) *int32 {
	n := new(int32)
	tm.mu.Lock()
	tm.turnCancel = func() { atomic.AddInt32(n, 1) }
	tm.mu.Unlock()
	return n
}

// TestCloseClaim pins the unified close entry (issue #69 备选一 步骤 6):
// every close flavor funnels through one ownership claim, so whichever
// paths race only ONE wins the close, the cleanup runs exactly once, and a
// session_ended enqueue failure is swallowed — the close itself (the
// closed marking, the cancellation, the cleanup) must never be blocked or
// reopened by the notification it tried to send.
func TestCloseClaim(t *testing.T) {
	t.Run("racing close paths elect exactly one owner and one cleanup", func(t *testing.T) {
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, NewEventRecorder(&captureLogger{}, "t"), nil)
		s := &captureSender{}
		cancelCount := ttArmCancelCounting(tm)
		epoch := tm.currentEpoch()

		// Every production close flavor races from one barrier: the goodbye,
		// the transport close, the degraded orderly close, the terminal
		// closes and the bare internalClose.
		start := make(chan struct{})
		var wg sync.WaitGroup
		closers := []func(){
			func() { tm.handleEndSession(s) },
			func() { tm.Close(s) },
			func() { tm.orderlyCloseAt(epoch) },
			func() { tm.tryTerminalClose(s, ReasonLessonComplete) },
			func() { tm.tryTerminalCloseAt(s, epoch, ReasonLessonComplete) },
			func() { tm.internalClose() },
		}
		for _, c := range closers {
			c := c
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				c()
			}()
		}
		close(start)
		wg.Wait()

		tm.mu.Lock()
		closed := tm.closed
		tm.mu.Unlock()
		if !closed {
			t.Fatal("the racing closes left the session open")
		}
		if got := atomic.LoadInt32(cancelCount); got != 1 {
			t.Fatalf("in-flight cancel calls = %d, want exactly 1 (one cleanup)", got)
		}
		// Whichever flavor won, the wire sees at most the one goodbye —
		// a messageless close (transport close / orderly / internalClose)
		// legitimately ships none.
		if got := s.countOf(TypeSessionEnded); got > 1 {
			t.Fatalf("session_ended count = %d, want at most 1", got)
		}
	})

	t.Run("racing terminal closes ship exactly one session_ended", func(t *testing.T) {
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, NewEventRecorder(&captureLogger{}, "t"), nil)
		s := &captureSender{}
		cancelCount := ttArmCancelCounting(tm)
		epoch := tm.currentEpoch()

		start := make(chan struct{})
		var wg sync.WaitGroup
		closers := []func(){
			func() { tm.handleEndSession(s) },
			func() { tm.tryTerminalClose(s, ReasonLessonComplete) },
			func() { tm.tryTerminalCloseAt(s, epoch, ReasonLessonComplete) },
		}
		for _, c := range closers {
			c := c
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				c()
			}()
		}
		close(start)
		wg.Wait()

		if got := atomic.LoadInt32(cancelCount); got != 1 {
			t.Fatalf("in-flight cancel calls = %d, want exactly 1 (one cleanup)", got)
		}
		if got := s.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want 1", got)
		}
		ttAssertNothingAfterEnd(t, s)
	})

	t.Run("a rejected session_ended still completes the close", func(t *testing.T) {
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, NewEventRecorder(&captureLogger{}, "t"), nil)
		inner := &captureSender{}
		s := &failingControlSender{captureSender: inner, failType: TypeSessionEnded, failErr: errors.New("queue refused")}
		cancelCount := ttArmCancelCounting(tm)

		done := make(chan struct{})
		go func() {
			tm.handleEndSession(s)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("handleEndSession never returned past the failed goodbye")
		}

		if got := atomic.LoadInt32(&s.attempts); got != 1 {
			t.Fatalf("session_ended attempts = %d, want 1", got)
		}
		tm.mu.Lock()
		closed := tm.closed
		tm.mu.Unlock()
		if !closed {
			t.Fatal("the failed session_ended left the session open — notification failure must not block the close")
		}
		if got := atomic.LoadInt32(cancelCount); got != 1 {
			t.Fatalf("in-flight cancel calls = %d, want exactly 1", got)
		}
		if got := inner.countOf(TypeSessionEnded); got != 0 {
			t.Fatalf("delivered session_ended = %d, want 0", got)
		}
	})
}
