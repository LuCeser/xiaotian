// 补充回归（路径 A 单元 1）：本文件内的 *SubmitPresentationLifetime 在测
// 试里自登 admitCheckWriteLocked，撤掉 poem 生产接纳不会变红，不计入
// 1.10c 保存接纳证明；真实旅程见 TestPoemSubmitBrowser（rtbrowser）。
package rt

import (
	"context"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
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

func TestPoemSubmitPresentationLifetime(t *testing.T) {
	tm, s, _, practice := newPoemTestManager(t, newFakeASR(), newFakeLLM(), nil)
	ttPoemInit(t, tm, s, 42, ModeStudent)
	tm.mu.Lock()
	tm.tutor.checking = false
	epoch := tm.epoch
	ref := *tm.tutor.poem
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
	tm.mu.Lock()
	write, admitted := tm.admitCheckWriteLocked(tm.tutor.lessonID)
	tm.mu.Unlock()
	if !admitted {
		t.Fatal("poem save was not admitted")
	}
	cancel() // accepted persistence must outlive the turn context
	done := make(chan struct{})
	go func() {
		persistCtx, persistDone := checkPersistContext(ctx)
		defer persistDone()
		tm.finishPoemSubmit(s, persistCtx, ctx, cancel, epoch, ref, ttLessonID, 4, lesson.CompleteCheckResult{
			Verdicts: []lesson.CheckVerdict{{WordIndex: 0, Correct: true, Manual: true}},
		}, "poem-race", write)
		close(done)
	}()
	ttAwaitGate(t, hit, "poem presentation probe was not reached")
	ttAwaitGate(t, done, "stale poem presentation did not finish")

	if got := len(practice.recorded()); got != 1 {
		t.Fatalf("poem practice records = %d, want the accepted save preserved", got)
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
	if pending := len(tm.checkWrites); pending != 0 {
		t.Fatalf("completed poem save admissions = %d, want 0 before presentation", pending)
	}
}
