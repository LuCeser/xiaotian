package rt

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

func receiptStoreForTest(t *testing.T) *lesson.ReceiptStore {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "retry-receipt.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	if err = lesson.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := lesson.NewReceiptStore(db, nil)
	if err != nil {
		t.Fatalf("NewReceiptStore: %v", err)
	}
	return store
}

func TestCheckReviewRetryAfterFailure(t *testing.T) {
	receipts := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewErr = errors.New("review failed")
	tm, s, _, exec := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))
	ttSubmitCheck(t, tm, s)

	if s.countOf(TypeSessionEnded) != 0 {
		t.Fatal("session_ended must not fire after a failed review write")
	}
	l, err := exec.GetLessonState(t.Context(), ttLessonID)
	if err != nil {
		t.Fatalf("GetLessonState: %v", err)
	}
	if l.Stage != lesson.StageSubmitted {
		t.Fatalf("stage = %q, want submitted", l.Stage)
	}
	if len(words.submissions()) != 1 {
		t.Fatalf("SubmitReview calls after first submit = %d, want 1", len(words.submissions()))
	}

	words.reviewErr = nil
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果", "香蕉"}}
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-retry-1"}))
	if !waitFor(t, 5*time.Second, func() bool { return len(words.submissions()) == 2 }) {
		t.Fatalf("SubmitReview calls = %d, want 2 after retry", len(words.submissions()))
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended expected after successful retry")
	}

	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-retry-2"}))
	time.Sleep(200 * time.Millisecond)
	if len(words.submissions()) != 2 {
		t.Fatalf("SubmitReview calls after full replay = %d, want still 2", len(words.submissions()))
	}
	if got := s.countOf(TypeReviewResult); got < 2 {
		t.Fatalf("review_result count = %d, want at least 2", got)
	}
}

func TestCheckReviewRetryPartialOnlyMissingWords(t *testing.T) {
	receipts := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{
		Updated: []string{"苹果"},
		Missing: []string{"香蕉"},
	}
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, 2)
	ttSubmitCheck(t, tm, s)
	if s.countOf(TypeSessionEnded) != 0 {
		t.Fatal("partial save must not end the session")
	}

	words.reviewResult = ReviewWriteResult{Updated: []string{"香蕉"}}
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-partial-retry"}))
	if !waitFor(t, 5*time.Second, func() bool { return len(words.submissions()) == 2 }) {
		t.Fatalf("SubmitReview calls = %d, want 2", len(words.submissions()))
	}
	second := words.submissions()[1]
	if _, ok := second["苹果"]; ok {
		t.Fatalf("retry map must not contain 苹果: %v", second)
	}
	if _, ok := second["香蕉"]; !ok {
		t.Fatalf("retry map must contain 香蕉: %v", second)
	}
}

func TestCheckReviewRetryConcurrentSingleWrite(t *testing.T) {
	receipts := receiptStoreForTest(t)
	cards := ttCards("苹果")
	words := newFakeWords(cards)
	words.reviewErr = errors.New("fail once")
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, 1)
	ttSubmitCheck(t, tm, s)

	words.reviewErr = nil
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果"}}
	block := make(chan struct{})
	words.reviewBlock = block
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-a"}))
	}()
	go func() {
		defer wg.Done()
		tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-b"}))
	}()
	time.Sleep(50 * time.Millisecond)
	close(block)
	wg.Wait()
	if !waitFor(t, 5*time.Second, func() bool { return len(words.submissions()) == 2 }) {
		t.Fatalf("SubmitReview calls = %d, want 2 (one failed + one retry)", len(words.submissions()))
	}
}