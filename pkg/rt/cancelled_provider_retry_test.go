package rt

import (
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

func ttCancelledRetryManager(asr *fakeASR, llm *fakeLLM) (*TurnManager, *captureSender) {
	tm := NewTurnManager(asr, llm, newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID),
		ttConfig(newFakeWords()), NewEventRecorder(&captureLogger{}, "t"), nil)
	ttBindTutor(tm, ttCards("苹果"))
	tm.mu.Lock()
	tm.state = StateListening
	tm.mu.Unlock()
	return tm, &captureSender{}
}

func TestCancelledProviderRetry(t *testing.T) {
	t.Run("ASR invalid response", func(t *testing.T) {
		asr := newFakeASR(
			asrScript{err: mimo.ErrInvalidResponse},
			asrScript{text: "不应发生的重试"},
		)
		entered, release := asr.gateNextTranscribe()
		tm, s := ttCancelledRetryManager(asr, newFakeLLM())

		done := ttStartTurn(tm, s)
		ttAwaitGate(t, entered, "first ASR call was not entered")
		tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		close(release)
		ttAwaitGate(t, done, "cancelled ASR turn did not exit")

		if got := asr.fedCount(); got != 1 {
			t.Errorf("ASR calls = %d, want 1", got)
		}
	})

	t.Run("LLM invalid response", func(t *testing.T) {
		garbage := llmScript{resp: wireChatResponse(t,
			`{"choices":[{"message":{"content":"not json"}}]}`)}
		llm := newFakeLLM(garbage, intentScript(intentChat, "不应发生的重试"))
		entered, release := llm.gateNextChat()
		tm, s := ttCancelledRetryManager(newFakeASR(asrScript{text: "你好"}), llm)

		done := ttStartTurn(tm, s)
		ttAwaitGate(t, entered, "first LLM call was not entered")
		tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		close(release)
		ttAwaitGate(t, done, "cancelled LLM turn did not exit")

		if got := llm.callCount(); got != 1 {
			t.Errorf("LLM calls = %d, want 1", got)
		}
	})
}
