package rt

import "testing"

func TestAnnouncementKeepsEpoch(t *testing.T) {
	t.Run("advance cannot announce the next word on a new epoch", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "好了"})
		llm := newFakeLLM(intentScript(intentConfirm, ""))
		tm, s, _, exec := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, llm)
		sentBefore := s.countOf(TypeTeacherSentence)
		hit := ttInterruptBeforeAnnouncement(t, tm, s)

		done := ttStartTurn(tm, ttCloseOnTeacherSender(tm, s))
		ttAwaitGate(t, hit, "next-word announcement probe was not reached")
		ttAwaitGate(t, done, "advance turn did not finish")

		if got := s.countOf(TypeLessonUpdate); got != 1 {
			t.Fatalf("lesson_update count = %d, want committed update", got)
		}
		if got := exec.advanceCount(); got != 1 {
			t.Fatalf("advance calls = %d, want 1", got)
		}
		if got := s.countOf(TypeTeacherSentence); got != sentBefore {
			t.Fatalf("teacher sentences = %d, want %d after interrupt", got, sentBefore)
		}
	})

	for _, tc := range []struct {
		name       string
		transcript string
		intent     string
		reply      string
	}{
		{name: "chat reply", transcript: "老师你好", intent: intentChat, reply: "你好呀"},
		{name: "repeat", transcript: "再读一遍", intent: intentRepeat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asr := newFakeASR(asrScript{text: tc.transcript})
			llm := newFakeLLM(intentScript(tc.intent, tc.reply))
			tm, s, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, llm)
			sentBefore := s.countOf(TypeTeacherSentence)
			hit := ttInterruptBeforeAnnouncement(t, tm, s)

			done := ttStartTurn(tm, ttCloseOnTeacherSender(tm, s))
			ttAwaitGate(t, hit, "reply announcement probe was not reached")
			ttAwaitGate(t, done, "reply turn did not finish")

			if got := s.countOf(TypeTeacherSentence); got != sentBefore {
				t.Fatalf("teacher sentences = %d, want %d after interrupt", got, sentBefore)
			}
		})
	}
}

func ttInterruptBeforeAnnouncement(t *testing.T, tm *TurnManager, s Sender) chan struct{} {
	t.Helper()
	hit := make(chan struct{})
	tm.turnAnnouncementInterleave = func() {
		tm.turnAnnouncementInterleave = nil
		tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		close(hit)
	}
	return hit
}

func ttCloseOnTeacherSender(tm *TurnManager, s *captureSender) Sender {
	return &closeAtControlSender{
		captureSender: s,
		tm:            tm,
		target:        TypeTeacherSentence,
		closeDone:     make(chan struct{}),
	}
}
