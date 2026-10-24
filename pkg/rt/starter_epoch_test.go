package rt

import (
	"sync"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

type starterOutputSender struct {
	*captureSender
	late chan struct{}
	once sync.Once
}

func (s *starterOutputSender) SendControl(v any) error {
	if _, ok := v.(TeacherSentenceMsg); ok {
		s.once.Do(func() { close(s.late) })
	}
	return s.captureSender.SendControl(v)
}

func TestStarterKeepsEpoch(t *testing.T) {
	tests := []struct {
		start func(*TurnManager, Sender)
		name  string
	}{
		{
			name: "dictation greeting",
			start: func(tm *TurnManager, s Sender) {
				tm.startDictationSession(s, lesson.Lesson{
					LessonID: ttLessonID, Version: 1, Cards: ttCards("苹果"),
				}, false)
			},
		},
		{
			name:  "no-due ending",
			start: func(tm *TurnManager, s Sender) { tm.startNoDueSession(s, ttLessonID) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tts := newFakeTTS(1, 320)
			tm := NewTurnManager(newFakeASR(), newFakeLLM(), tts, newFakeExecutorNoRow(ttLessonID),
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

			tc.start(tm, s)
			ttAwaitGate(t, entered, "starter task was not registered")
			tm.mu.Lock()
			historyBefore := 0
			if tm.tutor != nil {
				historyBefore = len(tm.tutor.shortHistory)
			}
			tm.mu.Unlock()
			sentencesBefore, startsBefore := captured.countOf(TypeTeacherSentence), captured.countOf(TypeAudioStart)

			tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
			close(release)
			historyAfter, pending, playback := 0, 0, false
			snapshot := func() {
				tm.mu.Lock()
				defer tm.mu.Unlock()
				if tm.tutor != nil {
					historyAfter = len(tm.tutor.shortHistory)
				}
				pending, playback = len(tm.pendingPlayback), tm.playbackPending
			}
			select {
			case <-done:
				snapshot()
			case <-s.late:
				snapshot()
				// Let a broken old task exit without fabricating playback success.
				tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
				ttAwaitGate(t, done, "stale starter task did not exit after cleanup interrupt")
			}

			if got := captured.countOf(TypeTeacherSentence); got != sentencesBefore {
				t.Errorf("teacher sentences = %d, want %d", got, sentencesBefore)
			}
			if got := captured.countOf(TypeAudioStart); got != startsBefore {
				t.Errorf("audio starts = %d, want %d", got, startsBefore)
			}
			if got := len(tts.recordedTexts()); got != 0 {
				t.Errorf("TTS calls = %d, want 0", got)
			}
			if historyAfter != historyBefore || pending != 0 || playback {
				t.Errorf("history=%d (want %d), pending=%d, playback=%v", historyAfter, historyBefore, pending, playback)
			}
		})
	}
}
