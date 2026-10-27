// 补充回归（路径 A 单元 1）：TestPracticeSubmitPresentationLifetime 在测试
// 里自登 admitCheckWriteLocked，撤掉 practice 生产接纳不会变红，不计入
// 1.10b 保存接纳证明；真实旅程见 TestPracticeSubmitBrowser（rtbrowser）。
package rt

import (
	"context"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

func TestPracticePresentationLifetime(t *testing.T) {
	practice := &fakePracticeSource{}
	captured := &captureSender{}
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID),
		TurnManagerConfig{
			LessonID: ttLessonID, Words: newFakeWords(), Practice: practice,
			PracticeStore: newTestPracticeStore(t),
		}, NewEventRecorder(&captureLogger{}, "t"), nil)
	wrapped := ttCloseAtInit(t, tm, captured, TypeSessionReady)

	tm.HandleControl(wrapped, mustJSON(t, SessionInitMsg{
		Type: "session_init", Practice: true,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	ttAwaitGate(t, wrapped.closeDone, "close at empty practice ready did not finish")

	if got := captured.countOf(TypeSessionReady); got != 1 {
		t.Fatalf("session_ready count = %d, want 1", got)
	}
	ttReadyBeforeEnd(t, captured, TypeSessionReady)
	ttAssertNothingAfterEnd(t, captured)
}

func TestPracticeSubmitPresentationLifetime(t *testing.T) {
	store := newTestPracticeStore(t)
	tm, s, _, _ := ttDriveToCheckCfg(t, ttCards("苹果"), newFakeWords(ttCards("苹果")), func(cfg *TurnManagerConfig) {
		cfg.PracticeStore = store
	})
	tm.mu.Lock()
	tm.tutor.checking = false
	epoch := tm.epoch
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
	write, admitted := tm.admitCheckWriteLocked()
	tm.mu.Unlock()
	if !admitted {
		t.Fatal("practice save was not admitted")
	}
	cancel() // accepted persistence must outlive the turn context
	done := make(chan struct{})
	go func() {
		persistCtx, persistDone := checkPersistContext(ctx)
		defer persistDone()
		tm.finishPracticeSubmit(s, persistCtx, ctx, cancel, epoch, ttLessonID, ttCards("苹果"), lesson.CompleteCheckResult{
			Verdicts: []lesson.CheckVerdict{{WordIndex: 0, Correct: true, Manual: true}},
			Outcomes: map[string]lesson.WordOutcome{"0": {Outcome: outcomeConfirmed}},
		}, "practice-race", write)
		close(done)
	}()
	ttAwaitGate(t, hit, "practice presentation probe was not reached")
	ttAwaitGate(t, done, "stale practice presentation did not finish")

	stored, err := store.ListPracticeWordsOn(context.Background(), beijingDay(time.Now()))
	if err != nil || len(stored) != 1 || stored[0] != "苹果" {
		t.Fatalf("stored practice words = %v, err=%v; want [苹果]", stored, err)
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
		t.Fatalf("completed practice save admissions = %d, want 0 before presentation", pending)
	}
}
