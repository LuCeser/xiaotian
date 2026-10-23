package rt

import "testing"

func TestPracticePresentationLifetime(t *testing.T) {
	practice := &fakePracticeSource{}
	captured := &captureSender{}
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID),
		TurnManagerConfig{
			LessonID: ttLessonID, Words: newFakeWords(), Practice: practice,
			PracticeStore: newTestPracticeStore(t),
		}, NewEventRecorder(&captureLogger{}, "t"), nil)
	wrapped := ttCloseAtInit(t, tm, captured, TypeSessionReady)

	tm.HandleControl(wrapped, mustJSON(t, SessionInitMsg{
		Type: "session_init", Practice: true,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	ttAwaitGate(t, wrapped.closeDone, "close at empty practice ready did not finish")

	if got := captured.countOf(TypeSessionReady); got != 1 {
		t.Fatalf("session_ready count = %d, want 1", got)
	}
	ttReadyBeforeEnd(t, captured, TypeSessionReady)
	ttAssertNothingAfterEnd(t, captured)
}
