package rt

import (
	"context"
	"testing"
)

func TestPoemTurnLifetime(t *testing.T) {
	tm, s, exec, _ := newPoemTestManager(t, newFakeASR(), newFakeLLM(), nil)
	ttPoemInit(t, tm, s, 42, ModeStudent)
	tm.mu.Lock()
	stale := tm.epoch + 1
	pausedBefore := tm.tutor.paused
	hintsBefore := tm.tutor.perWordHints[0]
	tm.mu.Unlock()
	sentBefore := s.countOf(TypeTeacherSentence)
	exec.mu.Lock()
	checkCallsBefore := len(exec.checkCalls)
	exec.mu.Unlock()

	ctx := context.Background()
	tm.dispatchPoemTurn(s, ctx, func() {}, 1, stale, intentPayload{Intent: intentConfirm})
	tm.dispatchPoemTurn(s, ctx, func() {}, 1, stale, intentPayload{Intent: intentPause})
	tm.dispatchPoemTurn(s, ctx, func() {}, 1, stale, intentPayload{Intent: intentHint, Index: 1})
	tm.dispatchPoemTurn(s, ctx, func() {}, 1, stale, intentPayload{Intent: intentChat, Reply: "旧回复"})

	tm.mu.Lock()
	paused, hints := tm.tutor.paused, tm.tutor.perWordHints[0]
	tm.mu.Unlock()
	exec.mu.Lock()
	checkCalls := len(exec.checkCalls)
	exec.mu.Unlock()
	if paused != pausedBefore || hints != hintsBefore || checkCalls != checkCallsBefore ||
		s.countOf(TypeTeacherSentence) != sentBefore {
		t.Fatalf("paused=%v hints=%d check calls=%d sentences=%d, want %v/%d/%d/%d",
			paused, hints, checkCalls, s.countOf(TypeTeacherSentence),
			pausedBefore, hintsBefore, checkCallsBefore, sentBefore)
	}
}

func TestPoemSessionReadyLifetime(t *testing.T) {
	tm, captured, _, _ := newPoemTestManager(t, newFakeASR(), newFakeLLM(), nil)
	wrapped := ttCloseAtInit(t, tm, captured, TypeSessionReady)

	tm.HandleControl(wrapped, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Poem: 42,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	ttAwaitGate(t, wrapped.closeDone, "close at poem session_ready did not finish")

	if got := captured.countOf(TypeSessionReady); got != 1 {
		t.Fatalf("session_ready count = %d, want 1", got)
	}
	ttReadyBeforeEnd(t, captured, TypeSessionReady)
	ttAssertNothingAfterEnd(t, captured)
}
