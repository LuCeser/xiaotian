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
		tm.finishCheckSubmit(s, ctx, cancel, epoch, map[string]string{"苹果": "good"}, 1, 1, "submit-race", nil)
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

func TestCheckSubmitSummaryLifetime(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	tm, captured, _, _ := ttDriveToCheck(t, ttCards("苹果"), words)
	tm.mu.Lock()
	tm.tutor.checking = false
	epoch := tm.epoch
	tm.mu.Unlock()
	resultsBefore := captured.countOf(TypeReviewResult)
	sentBefore := captured.countOf(TypeTeacherSentence)
	s := &closeAtControlSender{
		captureSender: captured, tm: tm, target: TypeTeacherSentence, closeDone: make(chan struct{}),
	}
	hit := make(chan struct{})
	tm.checkSubmitSummaryInterleave = func() {
		tm.checkSubmitSummaryInterleave = nil
		tm.HandleControl(captured, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		close(hit)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tm.finishCheckSubmit(s, ctx, cancel, epoch, map[string]string{"苹果": "good"}, 1, 1, "summary-race", nil)
		close(done)
	}()
	ttAwaitGate(t, hit, "check submit summary probe was not reached")
	ttAwaitGate(t, done, "stale check submit summary did not finish")

	if got := captured.countOf(TypeReviewResult); got != resultsBefore+1 {
		t.Fatalf("review_result count = %d, want %d", got, resultsBefore+1)
	}
	if got := captured.countOf(TypeTeacherSentence); got != sentBefore {
		t.Fatalf("teacher sentences = %d, want %d", got, sentBefore)
	}
	tm.mu.Lock()
	reviewSeen := tm.tutor.reviewSeen
	tm.mu.Unlock()
	if !reviewSeen {
		t.Fatal("accepted review result was not retained")
	}
	tm.handleEndSession(captured)
}

func TestCorrectionSubmitPresentationLifetime(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	tm, s, _, exec := ttDriveToCheck(t, ttCards("苹果"), words)
	tm.mu.Lock()
	tm.tutor.checking = false
	tm.tutor.correction = true
	epoch := tm.epoch
	tm.mu.Unlock()
	exec.mu.Lock()
	exec.lesson.Stage = "submitted"
	exec.lesson.Unchecked = true
	version := exec.lesson.Version
	exec.mu.Unlock()
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
		tm.finishCorrections(s, ctx, cancel, correctionInputs{
			epoch: epoch, lessonID: ttLessonID, version: version,
			requestID: "correction-race", changed: map[string]string{"苹果": "good"}, total: 1, correct: 1,
		})
		close(done)
	}()
	ttAwaitGate(t, hit, "correction presentation probe was not reached")
	ttAwaitGate(t, done, "stale correction presentation did not finish")

	row, err := exec.GetLessonState(context.Background(), ttLessonID)
	if err != nil || row.Unchecked {
		t.Fatalf("stored lesson = %+v, err=%v; want cleared correction marker", row, err)
	}
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want the accepted correction save preserved", got)
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
