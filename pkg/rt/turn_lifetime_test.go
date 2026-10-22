package rt

import (
	"testing"
	"time"
)

func TestASRResultLifetime(t *testing.T) {
	t.Run("close while ASR is out refuses transcript and history", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "旧转写"})
		entered, release := asr.gateNextTranscribe()
		tm, captured, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, newFakeLLM())
		before := ttShortHistory(t, tm)

		done := ttStartTurn(tm, captured)
		ttAwaitGate(t, entered, "ASR was not entered")
		tm.HandleControl(captured, []byte(`{"type":"end_session"}`))
		close(release)
		ttAwaitGate(t, done, "stale turn did not finish")

		if got := captured.controlsOf(t, TypeTranscript); len(got) != 0 {
			t.Fatalf("transcripts after close = %v, want none", got)
		}
		if got := ttShortHistory(t, tm); len(got) != len(before) {
			t.Fatalf("history rounds after close = %d, want %d", len(got), len(before))
		}
	})

	t.Run("interrupt while ASR is out keeps the next turn history clean", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "旧转写"}, asrScript{text: "新转写"})
		entered, release := asr.gateNextTranscribe()
		llm := newFakeLLM(intentScript(intentChat, "嗯嗯"))
		llmIn, llmOut := llm.gateNextChat()
		tm, captured, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, llm)
		before := ttShortHistory(t, tm)

		done := ttStartTurn(tm, captured)
		ttAwaitGate(t, entered, "first ASR was not entered")
		tm.HandleControl(captured, []byte(`{"type":"interrupt"}`))
		close(release)
		select {
		case <-done:
		case <-llmIn:
		case <-time.After(2 * time.Second):
			t.Fatal("stale turn neither finished nor classified")
		}
		stale, history := captured.controlsOf(t, TypeTranscript), ttShortHistory(t, tm)
		if len(stale) != 0 || len(history) != len(before) {
			close(llmOut)
			t.Fatalf("stale ASR result leaked: transcripts=%v history=%+v", stale, history)
		}

		next := ttStartTurn(tm, captured)
		ttAwaitGate(t, llmIn, "next turn classification was not entered")
		transcripts := captured.controlsOf(t, TypeTranscript)
		if len(transcripts) != 1 || transcripts[0]["text"] != "新转写" {
			t.Fatalf("next turn transcripts = %v, want exactly one 新转写", transcripts)
		}
		history = ttShortHistory(t, tm)
		if len(history) != len(before)+1 || history[len(history)-1].userText != "新转写" {
			t.Fatalf("next turn history = %+v, want one new 新转写 round after %d rounds", history, len(before))
		}
		for _, round := range history {
			if round.userText == "旧转写" {
				t.Fatalf("stale transcript leaked into next turn history: %+v", history)
			}
		}

		tm.HandleControl(captured, []byte(`{"type":"end_session"}`))
		close(llmOut)
		ttAwaitGate(t, next, "next turn did not finish")
	})
}

func ttShortHistory(t *testing.T, tm *TurnManager) []historyRound {
	t.Helper()
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return append([]historyRound(nil), tm.tutor.shortHistory...)
}
