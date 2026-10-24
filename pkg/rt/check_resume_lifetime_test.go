package rt

import (
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

func TestCheckResumeLifetime(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(*TurnManager, Sender)
	}{
		{name: "close", stop: func(tm *TurnManager, s Sender) { tm.handleEndSession(s) }},
		{name: "interrupt", stop: func(tm *TurnManager, s Sender) {
			tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := newFakeExecutor(ttLessonID, ttCards("苹果", "香蕉"))
			exec.mu.Lock()
			exec.lesson.Stage = lesson.StageChecking
			exec.lesson.CurrentIndex = len(exec.lesson.Cards)
			exec.mu.Unlock()
			tts := newFakeTTS(1, 320)
			tm := NewTurnManager(newFakeASR(), newFakeLLM(), tts, exec,
				ttConfig(newFakeWords()), NewEventRecorder(&captureLogger{}, "t"), nil)
			s := &captureSender{}
			hit := make(chan struct{})
			tm.checkResumeInterleave = func() {
				tc.stop(tm, s)
				close(hit)
			}

			ttInitWithSender(t, tm, s)
			ttAwaitGate(t, hit, "check resume did not reach the post-IO probe")

			for _, typ := range []string{
				TypeSessionReady, TypeCheckStarted, TypeCheckVerdicts,
				TypeTeacherSentence, TypeAudioStart,
			} {
				if got := s.countOf(typ); got != 0 {
					t.Errorf("%s count = %d, want 0", typ, got)
				}
			}
			if got := len(tts.recordedTexts()); got != 0 {
				t.Errorf("TTS calls = %d, want 0", got)
			}
			tm.mu.Lock()
			bound, taskRegistered, state := tm.tutor != nil, tm.turnCancel != nil, tm.state
			tm.mu.Unlock()
			if bound || taskRegistered || state != "" {
				t.Errorf("bound=%v taskRegistered=%v state=%q, want no resumed panel", bound, taskRegistered, state)
			}
		})
	}
}
