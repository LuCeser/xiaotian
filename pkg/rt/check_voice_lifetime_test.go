package rt

import (
	"context"
	"testing"
)

func TestCheckVoiceLifetime(t *testing.T) {
	t.Run("classification returned after interrupt is discarded", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "第一个词对了"})
		llm := newFakeLLM(llmScript{resp: wireChatResponse(t,
			`{"choices":[{"message":{"content":"{\"intent\":\"check_right\",\"reply\":\"\",\"index\":1}"}}]}`)})
		entered, release := llm.gateNextChat()
		tm, s, _, exec := ttIntentFixture(t, ttCards("苹果"), asr, llm)
		tm.mu.Lock()
		tm.tutor.checking = true
		tm.mu.Unlock()
		exec.mu.Lock()
		exec.lesson.Stage = "checking"
		exec.mu.Unlock()
		sentBefore := s.countOf(TypeTeacherSentence)
		echoesBefore := s.countOf(TypeCheckVerdicts)
		turnSender := &closeAtControlSender{
			captureSender: s, tm: tm, target: TypeCheckVerdicts, closeDone: make(chan struct{}),
		}

		done := ttStartTurn(tm, turnSender)
		ttAwaitGate(t, entered, "check classification was not entered")
		tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		close(release)
		ttAwaitGate(t, done, "stale check classification did not finish")

		exec.mu.Lock()
		calls := len(exec.checkCalls)
		exec.mu.Unlock()
		if calls != 0 || s.countOf(TypeCheckVerdicts) != echoesBefore || s.countOf(TypeTeacherSentence) != sentBefore {
			t.Fatalf("check calls=%d verdict echoes=%d teacher sentences=%d, want no stale dispatch",
				calls, s.countOf(TypeCheckVerdicts)-echoesBefore, s.countOf(TypeTeacherSentence)-sentBefore)
		}
	})

	t.Run("stale voice verdict is not admitted", func(t *testing.T) {
		tm, s, _, exec := ttIntentFixture(t, ttCards("苹果"), newFakeASR(), newFakeLLM())
		tm.mu.Lock()
		tm.tutor.checking = true
		stale := tm.epoch + 1
		tm.mu.Unlock()
		exec.mu.Lock()
		exec.lesson.Stage = "checking"
		callsBefore := len(exec.checkCalls)
		exec.mu.Unlock()
		echoesBefore := s.countOf(TypeCheckVerdicts)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		tm.applyVoiceVerdict(s, ctx, func() {}, stale, intentPayload{Intent: intentCheckRight, Index: 1})

		exec.mu.Lock()
		calls := len(exec.checkCalls)
		exec.mu.Unlock()
		if calls != callsBefore || s.countOf(TypeCheckVerdicts) != echoesBefore {
			t.Fatalf("check calls=%d verdict echoes=%d, want %d/%d unchanged",
				calls, s.countOf(TypeCheckVerdicts), callsBefore, echoesBefore)
		}
	})

	t.Run("stale resume keeps the current check paused", func(t *testing.T) {
		tm, s, _, _ := ttIntentFixture(t, ttCards("苹果"), newFakeASR(), newFakeLLM())
		tm.mu.Lock()
		tm.tutor.checking = true
		tm.tutor.paused = true
		stale := tm.epoch + 1
		tm.mu.Unlock()
		sentBefore := s.countOf(TypeTeacherSentence)

		tm.checkDispatch(s, context.Background(), func() {}, 1, stale, intentPayload{Intent: intentResume})

		tm.mu.Lock()
		paused := tm.tutor.paused
		tm.mu.Unlock()
		if !paused || s.countOf(TypeTeacherSentence) != sentBefore {
			t.Fatalf("paused=%v teacher sentences=%d, want true/%d", paused, s.countOf(TypeTeacherSentence), sentBefore)
		}
	})
}
