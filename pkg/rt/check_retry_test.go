package rt

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// receiptStoreForTest returns a migrated receipt store plus its *sql.DB —
// the db lets a test break ONE store path in isolation (e.g. an INSERT
// ABORT trigger that leaves the SELECT path working).
func receiptStoreForTest(t *testing.T) (*lesson.ReceiptStore, *sql.DB) {
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
	return store, db
}

// TestCheckRetryDuplicateDuringSummaryAck (review P2-1): while the FIRST
// full-save summary is still waiting for its sentence_ack, a duplicate
// check_submit must REPLAY the receipt and never speak a second summary.
// The first submit's ending does not hold the in-flight marker, so this is
// exactly the window where a re-run ending would double-speak the summary
// and starve the read loop's ack wait.
func TestCheckRetryDuplicateDuringSummaryAck(t *testing.T) {
	receipts, _ := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果", "香蕉"}}
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))
	ttSubmitCheck(t, tm, s)

	// The summary is streaming; wait for it to START, then let the rest of
	// the stream settle before counting.
	results := s.countOf(TypeReviewResult)
	spoken := s.countOf(TypeTeacherSentence)
	if !waitFor(t, 5*time.Second, func() bool {
		return s.countOf(TypeTeacherSentence) > spoken
	}) {
		t.Fatal("the full-save summary never spoke")
	}
	time.Sleep(400 * time.Millisecond)
	stable := s.countOf(TypeTeacherSentence)

	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-dup-during-ack"}))
	time.Sleep(400 * time.Millisecond)
	if got := s.countOf(TypeTeacherSentence); got != stable {
		t.Fatalf("duplicate submit during the summary ack spoke again: %d sentences -> %d", stable, got)
	}
	if got := s.countOf(TypeReviewResult); got != results+1 {
		t.Fatalf("review_result count = %d, want %d (the duplicate replays the receipt)", got, results+1)
	}
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want still 1 (the replay writes nothing)", got)
	}

	// The one summary still ends the session once its ack arrives.
	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the summary ack")
	}
}

func TestCheckRetryAfterFailure(t *testing.T) {
	receipts, _ := receiptStoreForTest(t)
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
	summaryCountBefore := s.countOf(TypeTeacherSentence)
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-retry-3"}))
	time.Sleep(200 * time.Millisecond)
	if got := s.countOf(TypeTeacherSentence); got != summaryCountBefore {
		t.Fatalf("teacher_sentence count after full-save replay = %d, want unchanged %d", got, summaryCountBefore)
	}
}

func TestCheckRetryPartialOnlyMissingWords(t *testing.T) {
	receipts, _ := receiptStoreForTest(t)
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

// TestCheckRetryConcurrentSingleWrite: while one retry is parked inside its
// SubmitReview, a second check_submit must be consumed by the in-flight
// marker — silently: no second write AND no replayed receipt frame (the
// marker check precedes the receipt read). With the marker removed, the
// second request reads the retry's pending receipt and answers with a
// pending review_result — the unchanged-frame assertion goes red.
func TestCheckRetryConcurrentSingleWrite(t *testing.T) {
	receipts, _ := receiptStoreForTest(t)
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
	entered := make(chan struct{})
	words.reviewBlock = block
	words.reviewBlockEnter = entered

	// Request A: the in-flight retry, parked INSIDE its SubmitReview (its
	// receipt Begin has already landed by the time `entered` fires).
	go func() {
		tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-a"}))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first retry never reached its parked SubmitReview")
	}

	// Request B: arrives while A's write is in flight.
	resultsBefore := s.countOf(TypeReviewResult)
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-b"}))
	}()
	select {
	case <-doneB:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent submit never returned while a retry was in flight")
	}
	if got := s.countOf(TypeReviewResult); got != resultsBefore {
		t.Fatalf("review_result count = %d during an in-flight retry, want unchanged %d", got, resultsBefore)
	}

	close(block)
	if !waitFor(t, 5*time.Second, func() bool { return len(words.submissions()) == 2 }) {
		t.Fatalf("SubmitReview calls = %d, want 2 (one failed first pass + one retry)", len(words.submissions()))
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended expected after the single successful retry")
	}
	if got := len(words.submissions()); got != 2 {
		t.Fatalf("SubmitReview calls settled at %d, want 2", got)
	}
}

// TestCheckRetryEndSessionDuringWriteDoesNotTrailTerminal (#96 review
// P2-2): a retry parked inside its provider write when end_session lands
// must go silent once the write returns — the session_ended that already
// shipped is terminal, so no review_result, no state mutation, and no
// second ending may follow it. The fake's IgnoreCtx mode models the
// provider whose write commits anyway: the result IS real, the session it
// would report to is gone.
func TestCheckRetryEndSessionDuringWriteDoesNotTrailTerminal(t *testing.T) {
	receipts, _ := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewErr = errors.New("fail once")
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))
	ttSubmitCheck(t, tm, s) // the failed receipt is on record (review_result #1)

	words.reviewErr = nil
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果", "香蕉"}}
	block := make(chan struct{})
	entered := make(chan struct{})
	words.reviewBlock = block
	words.reviewBlockEnter = entered
	words.reviewBlockIgnoreCtx = true

	go tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-end-mid-write"}))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the retry never reached its parked SubmitReview")
	}

	tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
	if got := s.countOf(TypeSessionEnded); got != 1 {
		t.Fatalf("session_ended count = %d, want 1 (the client goodbye)", got)
	}
	if ended := s.controlsOf(t, TypeSessionEnded)[0]; ended["reason"] != ReasonClientEnd {
		t.Fatalf("session_ended.reason = %v, want %s", ended["reason"], ReasonClientEnd)
	}
	resultsAtEnd := s.countOf(TypeReviewResult)
	spokenAtEnd := s.countOf(TypeTeacherSentence)

	close(block) // the committed write returns — into a closed session
	if !waitFor(t, 5*time.Second, func() bool { return len(words.submissions()) == 2 }) {
		t.Fatalf("SubmitReview calls = %d, want 2 (the write itself is not undoable)", len(words.submissions()))
	}
	time.Sleep(300 * time.Millisecond)
	if got := s.countOf(TypeReviewResult); got != resultsAtEnd {
		t.Fatalf("review_result count = %d after the write returned post-terminal, want unchanged %d", got, resultsAtEnd)
	}
	if got := s.countOf(TypeSessionEnded); got != 1 {
		t.Fatalf("session_ended count = %d, want still 1 (no second ending)", got)
	}
	if got := s.countOf(TypeTeacherSentence); got != spokenAtEnd {
		t.Fatalf("teacher sentences = %d after the goodbye, want unchanged %d (no summary behind it)", got, spokenAtEnd)
	}
}

// TestCheckSubmitEndSessionDuringWriteDoesNotTrailTerminal: the FIRST
// submit's write owes the same terminal silence (#96 review P2-2 applied to
// finishCheckSubmit): end_session while its SubmitReview is parked means the
// goodbye already shipped — the write's late return must not answer it.
func TestCheckSubmitEndSessionDuringWriteDoesNotTrailTerminal(t *testing.T) {
	receipts, _ := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果", "香蕉"}}
	block := make(chan struct{})
	entered := make(chan struct{})
	words.reviewBlock = block
	words.reviewBlockEnter = entered
	words.reviewBlockIgnoreCtx = true
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))

	go tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-end-mid-first-write"}))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first submit never reached its parked SubmitReview")
	}

	tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
	if got := s.countOf(TypeSessionEnded); got != 1 {
		t.Fatalf("session_ended count = %d, want 1 (the client goodbye)", got)
	}
	spokenAtEnd := s.countOf(TypeTeacherSentence)

	close(block) // the committed write returns — into a closed session
	if !waitFor(t, 5*time.Second, func() bool { return len(words.submissions()) == 1 }) {
		t.Fatalf("SubmitReview calls = %d, want 1 (the write itself is not undoable)", len(words.submissions()))
	}
	time.Sleep(300 * time.Millisecond)
	if got := s.countOf(TypeReviewResult); got != 0 {
		t.Fatalf("review_result count = %d after the write returned post-terminal, want 0", got)
	}
	if got := s.countOf(TypeSessionEnded); got != 1 {
		t.Fatalf("session_ended count = %d, want still 1 (no second ending)", got)
	}
	if got := s.countOf(TypeTeacherSentence); got != spokenAtEnd {
		t.Fatalf("teacher sentences = %d after the goodbye, want unchanged %d (no summary behind it)", got, spokenAtEnd)
	}
}

// TestCheckRetryBeginFailureSkipsWrite: when the retry's receipt Begin
// fails, the write must NOT run — without a fresh receipt row there is no
// dedup basis for a later tap, so the honest answer is a zero-updated
// review_result and a session left open for another try.
func TestCheckRetryBeginFailureSkipsWrite(t *testing.T) {
	receipts, db := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewErr = errors.New("fail once")
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))
	ttSubmitCheck(t, tm, s) // the failed receipt is on record

	words.reviewErr = nil
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果", "香蕉"}}
	// Break Begin only: the ABORT trigger fails every receipt INSERT while
	// ListByLesson (a SELECT) keeps answering.
	if _, err := db.Exec("CREATE TRIGGER block_receipt_begin BEFORE INSERT ON lesson_save_receipts " +
		"BEGIN SELECT RAISE(ABORT, 'begin blocked'); END"); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-begin-fail"}))
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeReviewResult) >= 2 }) {
		t.Fatal("retry never answered after its receipt Begin failed")
	}
	results := s.controlsOf(t, TypeReviewResult)
	last := results[len(results)-1]
	if last["updated"] != float64(0) {
		t.Fatalf("retry review_result updated = %v, want 0", last["updated"])
	}
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want 1: a failed receipt Begin must not write", got)
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended = %d, want 0 (the retry stays open for another tap)", got)
	}
}
