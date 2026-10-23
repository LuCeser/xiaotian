package rt

import (
	"context"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

func TestCheckPresentationLifetime(t *testing.T) {
	tm, s, _, exec := ttIntentFixture(t, ttCards("苹果"), newFakeASR(), newFakeLLM())
	tm.mu.Lock()
	epoch := tm.epoch
	tm.mu.Unlock()
	sentBefore := s.countOf(TypeTeacherSentence)
	hit := make(chan struct{})
	tm.checkPresentationInterleave = func() {
		tm.checkPresentationInterleave = nil
		tm.handleEndSession(s)
		close(hit)
	}

	done := make(chan struct{})
	go func() {
		tm.enterCheck(s, context.Background(), func() {}, epoch)
		close(done)
	}()
	ttAwaitGate(t, hit, "successful check presentation probe was not reached")
	ttAwaitGate(t, done, "stale successful check presentation did not finish")

	row, err := exec.GetLessonState(context.Background(), ttLessonID)
	if err != nil || row.Stage != lesson.StageChecking {
		t.Fatalf("stored lesson = %+v, err=%v; want committed checking stage", row, err)
	}
	if got := s.countOf(TypeCheckStarted); got != 0 {
		t.Fatalf("check_started count = %d, want 0", got)
	}
	if got := s.countOf(TypeTeacherSentence); got != sentBefore {
		t.Fatalf("teacher sentences = %d, want %d", got, sentBefore)
	}
	tm.mu.Lock()
	checking, closed := tm.tutor.checking, tm.closed
	tm.mu.Unlock()
	if checking || !closed {
		t.Fatalf("checking=%v closed=%v, want false/true", checking, closed)
	}
}
