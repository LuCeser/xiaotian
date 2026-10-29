package rt

import (
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// ttInitWithSender is ttInit with the caller's sender: the wrapped sender
// rides the whole session_init so the close-at-enqueue probe sees the
// starter's own session_ready/error.
func ttInitWithSender(t *testing.T, tm *TurnManager, s Sender) {
	t.Helper()
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type:  TypeSessionInit,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
}

// ttCloseAtInit wraps the sender so the real session closes immediately
// before the FIRST message of the given type is queued.
func ttCloseAtInit(t *testing.T, tm *TurnManager, captured *captureSender, target string) *closeAtControlSender {
	t.Helper()
	return &closeAtControlSender{captureSender: captured, tm: tm, target: target, closeDone: make(chan struct{})}
}

// ttAssertNothingAfterEnd fails when any presentation message trails the
// first session_ended: the starters' ready/error messages must precede the
// goodbye whatever races them.
func ttAssertNothingAfterEnd(t *testing.T, s *captureSender) {
	t.Helper()
	order := s.typeOrder()
	endAt := -1
	for i, typ := range order {
		if typ == TypeSessionEnded {
			endAt = i
			break
		}
	}
	if endAt < 0 {
		t.Fatalf("session_ended never shipped (order = %v)", order)
	}
	for i, typ := range order[endAt+1:] {
		switch typ {
		case TypeSessionReady, TypeState, TypeError, TypeTeacherSentence, TypeAudioStart, TypeAudioEnd:
			t.Fatalf("message %s at %d after session_ended (order = %v)", typ, endAt+1+i, order)
		}
	}
}

// ttReadyBeforeEnd pins the accepted ready/error message's position.
func ttReadyBeforeEnd(t *testing.T, s *captureSender, typ string) {
	t.Helper()
	order := s.typeOrder()
	msgAt, endAt := -1, -1
	for i, t2 := range order {
		if t2 == typ && msgAt < 0 {
			msgAt = i
		}
		if t2 == TypeSessionEnded && endAt < 0 {
			endAt = i
		}
	}
	if msgAt < 0 {
		t.Fatalf("no %s shipped (order = %v)", typ, order)
	}
	if endAt < 0 {
		t.Fatalf("no session_ended shipped (order = %v)", order)
	}
	if msgAt > endAt {
		t.Fatalf("%s at %d trails session_ended at %d (order = %v)", typ, msgAt, endAt, order)
	}
}

// TestSessionReadyLifetime pins every classroom starter to the unified
// session-effect entry: the memory bind (state, tutor, sessionReadyAt) and
// the session_ready enqueue are one hold — a close landing anywhere in the
// init window either refuses the start outright or trails the accepted
// ready on the wire.
func TestSessionReadyLifetime(t *testing.T) {
	t.Run("no-due start keeps session_ready ahead of a racing close", func(t *testing.T) {
		exec := newFakeExecutorNoRow(ttLessonID)
		captured := &captureSender{}
		tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec,
			ttConfig(newFakeWords()), NewEventRecorder(&captureLogger{}, "t"), nil)
		wrapped := ttCloseAtInit(t, tm, captured, TypeSessionReady)

		ttInitWithSender(t, tm, wrapped)
		ttAwaitGate(t, wrapped.closeDone, "close at session_ready did not finish")

		if got := captured.countOf(TypeSessionReady); got != 1 {
			t.Fatalf("session_ready count = %d, want 1", got)
		}
		ttReadyBeforeEnd(t, captured, TypeSessionReady)
		ttAssertNothingAfterEnd(t, captured)
		tm.mu.Lock()
		closed := tm.closed
		tm.mu.Unlock()
		if !closed {
			t.Fatal("session is not closed")
		}
	})

	t.Run("completed replay keeps session_ready ahead of a racing close", func(t *testing.T) {
		exec := newFakeExecutorNoRow(ttLessonID)
		exec.mu.Lock()
		exec.exists = true
		exec.lesson = lesson.Lesson{LessonID: ttLessonID, Stage: "dictation", Category: "chinese", Cards: ttCards("苹果"), CurrentIndex: 1}
		exec.mu.Unlock()
		captured := &captureSender{}
		tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec,
			ttConfig(newFakeWords()), NewEventRecorder(&captureLogger{}, "t"), nil)
		wrapped := ttCloseAtInit(t, tm, captured, TypeSessionReady)

		ttInitWithSender(t, tm, wrapped)
		ttAwaitGate(t, wrapped.closeDone, "close at session_ready did not finish")

		if got := captured.countOf(TypeSessionReady); got != 1 {
			t.Fatalf("session_ready count = %d, want 1", got)
		}
		ttReadyBeforeEnd(t, captured, TypeSessionReady)
		ttAssertNothingAfterEnd(t, captured)
		if got := captured.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want exactly 1", got)
		}
	})

	t.Run("dictation start keeps session_ready ahead of a racing close", func(t *testing.T) {
		exec := newFakeExecutorNoRow(ttLessonID)
		captured := &captureSender{}
		tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec,
			ttConfig(newFakeWords(ttCards("苹果"))), NewEventRecorder(&captureLogger{}, "t"), nil)
		wrapped := ttCloseAtInit(t, tm, captured, TypeSessionReady)

		ttInitWithSender(t, tm, wrapped)
		ttAwaitGate(t, wrapped.closeDone, "close at session_ready did not finish")

		if got := captured.countOf(TypeSessionReady); got != 1 {
			t.Fatalf("session_ready count = %d, want 1", got)
		}
		ttReadyBeforeEnd(t, captured, TypeSessionReady)
		ttAssertNothingAfterEnd(t, captured)
	})
}

// TestInitFailureLifetime pins failLessonInit to the same entry: the init
// failure notice either refuses on a closed session or precedes the goodbye
// — never a late lesson_unavailable past session_ended.
func TestInitFailureLifetime(t *testing.T) {
	exec := newFakeExecutorNoRow(ttLessonID)
	exec.mu.Lock()
	exec.exists = true
	exec.lesson = lesson.Lesson{LessonID: ttLessonID, Category: "chinese"} // legacy empty cards
	exec.mu.Unlock()
	captured := &captureSender{}
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec,
		ttConfig(newFakeWords(ttCards("苹果"))), NewEventRecorder(&captureLogger{}, "t"), nil)
	wrapped := ttCloseAtInit(t, tm, captured, TypeError)

	ttInitWithSender(t, tm, wrapped)
	ttAwaitGate(t, wrapped.closeDone, "close at the init error did not finish")

	errs := captured.controlsOf(t, TypeError)
	if len(errs) != 1 || errs[0]["code"] != ErrCodeLessonUnavailable {
		t.Fatalf("error messages = %v, want exactly one lesson_unavailable", errs)
	}
	if got := captured.countOf(TypeSessionReady); got != 0 {
		t.Fatalf("session_ready count = %d, want none on a failed init", got)
	}
	ttReadyBeforeEnd(t, captured, TypeError)
	ttAssertNothingAfterEnd(t, captured)
}
