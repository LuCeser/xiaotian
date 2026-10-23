package rt

import (
	"context"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

func TestPassageTurnLifetime(t *testing.T) {
	tm, s, exec, _ := newPassageTestManager(t, newFakeASR(), newFakeLLM(), nil)
	ttPassageInit(t, tm, s, 77, 51, ModeStudent)
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
	tm.dispatchPassageTurn(s, ctx, func() {}, 1, stale, intentPayload{Intent: intentConfirm})
	tm.dispatchPassageTurn(s, ctx, func() {}, 1, stale, intentPayload{Intent: intentPause})
	tm.dispatchPassageTurn(s, ctx, func() {}, 1, stale, intentPayload{Intent: intentHint, Index: 2})
	tm.dispatchPassageTurn(s, ctx, func() {}, 1, stale, intentPayload{Intent: intentChat, Reply: "旧回复"})

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

func TestPassageSessionReadyLifetime(t *testing.T) {
	tm, captured, _, _ := newPassageTestManager(t, newFakeASR(), newFakeLLM(), nil)
	wrapped := ttCloseAtInit(t, tm, captured, TypeSessionReady)

	tm.HandleControl(wrapped, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Passage: 77, PassageScope: 51,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	ttAwaitGate(t, wrapped.closeDone, "close at passage session_ready did not finish")

	if got := captured.countOf(TypeSessionReady); got != 1 {
		t.Fatalf("session_ready count = %d, want 1", got)
	}
	ttReadyBeforeEnd(t, captured, TypeSessionReady)
	ttAssertNothingAfterEnd(t, captured)
}

func TestPassageSubmitPresentationLifetime(t *testing.T) {
	tm, s, _, practice := newPassageTestManager(t, newFakeASR(), newFakeLLM(), nil)
	ttPassageInit(t, tm, s, 77, 51, ModeStudent)
	tm.mu.Lock()
	tm.tutor.checking = false
	epoch := tm.epoch
	ref := *tm.tutor.passage
	tm.mu.Unlock()
	resultsBefore := s.countOf(TypeReviewResult)
	sentBefore := s.countOf(TypeTeacherSentence)
	hit := make(chan struct{})
	tm.checkSubmitPresentationInterleave = func() {
		tm.checkSubmitPresentationInterleave = nil
		tm.handleEndSession(s)
		close(hit)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tm.finishPassageSubmit(s, ctx, cancel, epoch, ref, ttLessonID, 2, lesson.CompleteCheckResult{
			Verdicts: []lesson.CheckVerdict{{WordIndex: 0, Correct: true, Manual: true}},
		}, "passage-race")
		close(done)
	}()
	ttAwaitGate(t, hit, "passage presentation probe was not reached")
	ttAwaitGate(t, done, "stale passage presentation did not finish")

	if got := len(practice.recorded()); got != 1 {
		t.Fatalf("passage practice records = %d, want the accepted save preserved", got)
	}
	if s.countOf(TypeReviewResult) != resultsBefore || s.countOf(TypeTeacherSentence) != sentBefore {
		t.Fatalf("review results=%d sentences=%d, want %d/%d",
			s.countOf(TypeReviewResult), s.countOf(TypeTeacherSentence), resultsBefore, sentBefore)
	}
	tm.mu.Lock()
	reviewSeen, closed := tm.tutor.reviewSeen, tm.closed
	tm.mu.Unlock()
	if reviewSeen || !closed {
		t.Fatalf("reviewSeen=%v closed=%v, want false/true", reviewSeen, closed)
	}
}
