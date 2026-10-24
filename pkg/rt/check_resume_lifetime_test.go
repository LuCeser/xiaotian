package rt

import (
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

func TestCheckResumeLifetime(t *testing.T) {
	for _, tc := range []struct {
		stop func(*TurnManager, Sender)
		name string
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

	t.Run("bridge keeps registered epoch", func(t *testing.T) {
		exec := newFakeExecutor(ttLessonID, ttCards("苹果"))
		exec.mu.Lock()
		exec.lesson.Stage = lesson.StageChecking
		exec.lesson.CurrentIndex = 1
		exec.mu.Unlock()
		tts := newFakeTTS(1, 320)
		tm := NewTurnManager(newFakeASR(), newFakeLLM(), tts, exec,
			ttConfig(newFakeWords()), NewEventRecorder(&captureLogger{}, "t"), nil)
		captured := &captureSender{}
		s := &starterOutputSender{captureSender: captured, late: make(chan struct{})}
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		tm.starterInterleave = func(task func()) {
			close(entered)
			<-release
			task()
			close(done)
		}

		ttInitWithSender(t, tm, s)
		ttAwaitGate(t, entered, "check bridge task was not registered")
		tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		close(release)
		select {
		case <-done:
		case <-s.late:
			tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
			ttAwaitGate(t, done, "stale check bridge did not exit")
		}

		if got := captured.countOf(TypeTeacherSentence); got != 0 {
			t.Errorf("teacher sentences = %d, want 0", got)
		}
		if got := captured.countOf(TypeAudioStart); got != 0 {
			t.Errorf("audio starts = %d, want 0", got)
		}
		if got := len(tts.recordedTexts()); got != 0 {
			t.Errorf("TTS calls = %d, want 0", got)
		}
	})
}
