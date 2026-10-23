package rt

import "testing"

func TestReadWordLifetime(t *testing.T) {
	tm, captured, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), newFakeASR(), newFakeLLM())
	before := len(captured.typeOrder())
	wrapped := ttCloseAtInit(t, tm, captured, TypeTurnStart)

	tm.HandleControl(wrapped, mustJSON(t, ReadWordMsg{
		Type: TypeReadWord, WordIndex: 0, CurrentIndex: 0,
	}))
	ttAwaitGate(t, wrapped.closeDone, "close at read_word turn_start did not finish")

	got := captured.typeOrder()[before:]
	if len(got) < 2 || got[0] != TypeTurnStart {
		t.Fatalf("read_word controls = %v, want turn_start before session_ended", got)
	}
	ttAssertNothingAfterEnd(t, captured)
	tm.mu.Lock()
	timer, closed := tm.wordTimer, tm.closed
	tm.mu.Unlock()
	if timer != nil || !closed {
		t.Fatalf("wordTimer=%v closed=%v, want nil/true", timer, closed)
	}
}
