package rt

import (
	"context"
	"testing"
)

func TestCheckSubmitPresentationLifetime(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	tm, s, _, _ := ttDriveToCheck(t, ttCards("苹果"), words)
	tm.mu.Lock()
	tm.tutor.checking = false // CompleteCheck already committed before the finishing goroutine.
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
	done := make(chan struct{})
	go func() {
		tm.finishCheckSubmit(s, ctx, cancel, epoch, map[string]string{"苹果": "good"}, 1, 1, "submit-race")
		close(done)
	}()
	ttAwaitGate(t, hit, "check submit presentation probe was not reached")
	ttAwaitGate(t, done, "stale check submit presentation did not finish")

	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want the accepted save preserved", got)
	}
	if got := s.countOf(TypeReviewResult); got != resultsBefore {
		t.Fatalf("review_result count = %d, want %d", got, resultsBefore)
	}
	if got := s.countOf(TypeTeacherSentence); got != sentBefore {
		t.Fatalf("teacher sentences = %d, want %d", got, sentBefore)
	}
	tm.mu.Lock()
	reviewSeen, closed := tm.tutor.reviewSeen, tm.closed
	tm.mu.Unlock()
	if reviewSeen || !closed {
		t.Fatalf("reviewSeen=%v closed=%v, want false/true", reviewSeen, closed)
	}
}
