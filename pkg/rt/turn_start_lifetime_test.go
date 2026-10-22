package rt

import (
	"testing"
	"time"
)

func TestTurnStartLifetime(t *testing.T) {
	asr := newFakeASR(asrScript{text: "旧转写"})
	entered, release := asr.gateNextTranscribe()
	tm, captured, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, newFakeLLM())
	before := len(captured.typeOrder())
	s := &closeAtControlSender{
		captureSender: captured,
		tm:            tm,
		target:        TypeState,
		closeDone:     make(chan struct{}),
	}
	done := ttStartTurn(tm, s)
	ttAwaitGate(t, entered, "ASR was not entered after turn_start")
	ttAwaitGate(t, s.closeDone, "close did not finish")
	close(release)
	ttAwaitGate(t, done, "turn did not finish")

	got := captured.typeOrder()[before:]
	if len(got) < 3 || got[0] != TypeState || got[1] != TypeTurnStart || got[2] != TypeSessionEnded {
		t.Fatalf("controls after turn start = %v, want state and turn_start before session_ended", got)
	}
}

func ttStartTurn(tm *TurnManager, s Sender) chan struct{} {
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	return done
}

func ttAwaitGate(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(what)
	}
}
