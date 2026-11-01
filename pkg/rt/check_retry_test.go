package rt

import (
	"database/sql"
	"encoding/json"
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
	store, db, _ := receiptStoreForTestPath(t)
	return store, db
}

// receiptStoreForTestPath is receiptStoreForTest that also exposes the db
// PATH: the concurrency regression opens a SECOND handle to the same file
// to hold the receipts write lock from outside the store's own pool.
func receiptStoreForTestPath(t *testing.T) (*lesson.ReceiptStore, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "retry-receipt.db")
	db, err := database.Open(path)
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
	return store, db, path
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

// TestCheckRetryConcurrentSingleWrite (#96 review P3): while one retry's
// receipt Begin is stalled on the receipts WRITE LOCK — i.e. before its
// pending row exists — a second check_submit must be consumed by the
// in-flight marker with NO second write. The lock construction is what
// makes the regression provable on business counts: with the marker
// removed, request B reads the SAME stale receipt (A's Begin has not
// committed), enters the write branch, and BOTH retries SubmitReview —
// the settled write count goes 2 -> 3. (An A parked INSIDE SubmitReview
// cannot prove this: its pending receipt already turns B into a replay.)
func TestCheckRetryConcurrentSingleWrite(t *testing.T) {
	receipts, db, path := receiptStoreForTestPath(t)
	// Two pooled connections for the store: A's stalled Begin occupies one,
	// B's receipt SELECT needs the other (WAL readers proceed during a held
	// write transaction; the single-conn default would queue behind A).
	db.SetMaxOpenConns(2)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果"}, Missing: []string{"香蕉"}}
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))
	ttSubmitCheck(t, tm, s) // partial save on record: receipt saved, missing=[香蕉]

	// The retry still cannot finish: its result keeps the batch unfixed.
	words.reviewResult = ReviewWriteResult{Missing: []string{"香蕉"}}

	// Hold the receipts write lock from a SECOND handle: A's retry Begin
	// (an INSERT) stalls on it while ListByLesson (a SELECT) keeps reading
	// the pre-retry receipt.
	lockDb, err := database.Open(path)
	if err != nil {
		t.Fatalf("open lock db: %v", err)
	}
	t.Cleanup(func() { _ = lockDb.Close() })
	lockCtx := t.Context()
	lockConn, err := lockDb.Conn(lockCtx)
	if err != nil {
		t.Fatalf("grab lock conn: %v", err)
	}
	if _, err = lockConn.ExecContext(lockCtx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("hold receipts write lock: %v", err)
	}
	t.Cleanup(func() { _ = lockConn.Close() })

	// Request A: synchronous — it takes the marker, reads the partial
	// receipt, and hands off to its goroutine, whose Begin stalls on the
	// lock (busy_timeout 5s bounds the stall; release below is immediate).
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-a"}))

	// Request B: arrives while A's Begin is still uncommitted. The marker
	// must consume it silently — no frame, and (once released) no write.
	resultsBefore := s.countOf(TypeReviewResult)
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-b"}))
	if got := s.countOf(TypeReviewResult); got != resultsBefore {
		t.Fatalf("review_result count = %d during an in-flight retry, want unchanged %d", got, resultsBefore)
	}

	// Release the lock: A's retry is the ONLY write that follows.
	if _, err = lockConn.ExecContext(lockCtx, "COMMIT"); err != nil {
		t.Fatalf("release receipts write lock: %v", err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return len(words.submissions()) >= 2 }) {
		t.Fatalf("SubmitReview calls = %d, want 2 (one partial first pass + one retry)", len(words.submissions()))
	}
	time.Sleep(500 * time.Millisecond) // let a would-be second retry land too
	if got := len(words.submissions()); got != 2 {
		t.Fatalf("SubmitReview calls settled at %d, want 2 (the concurrent duplicate must not write)", got)
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended = %d, want 0 (the unfixed retry keeps the session open)", got)
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

// TestCheckSubmitCompleteFailureShowsPending (#96 review P2-3): when the
// receipt's Complete never lands (the row stays pending), the FIRST
// review_result must already carry pending — the panel shows 待核实 and
// disables the retry on this frame. A full-success write with a failed
// Complete must NOT run the summary ending.
func TestCheckSubmitCompleteFailureShowsPending(t *testing.T) {
	receipts, db := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果", "香蕉"}}
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))

	// Break Complete only: the ABORT trigger fails every receipt UPDATE
	// while Begin (INSERT) and ListByLesson (SELECT) keep answering.
	if _, err := db.Exec("CREATE TRIGGER block_receipt_complete BEFORE UPDATE ON lesson_save_receipts " +
		"BEGIN SELECT RAISE(ABORT, 'complete blocked'); END"); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	spokenBefore := s.countOf(TypeTeacherSentence)
	result := ttSubmitCheck(t, tm, s)
	if result["pending"] != true {
		t.Fatalf("review_result.pending = %v, want true on the first frame", result["pending"])
	}
	if int(result["updated"].(float64)) != len(cards) {
		t.Fatalf("review_result.updated = %v, want the real count %d", result["updated"], len(cards))
	}
	time.Sleep(300 * time.Millisecond)
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended = %d, want 0 (a pending receipt never ends the session)", got)
	}
	if got := s.countOf(TypeTeacherSentence); got != spokenBefore {
		t.Fatalf("teacher sentences = %d, want unchanged %d (no summary over a pending receipt)", got, spokenBefore)
	}
	if !waitFor(t, 5*time.Second, func() bool { return gradingCount(s) > 0 }) {
		t.Fatal("the panel never returned to grading")
	}

	// A re-submit replays the pending receipt — still no second write.
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-pending-replay"}))
	time.Sleep(300 * time.Millisecond)
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want 1 (a pending receipt is not retryable)", got)
	}
	results := s.controlsOf(t, TypeReviewResult)
	if len(results) < 2 {
		t.Fatal("the pending replay never answered")
	}
	if results[len(results)-1]["pending"] != true {
		t.Fatalf("replayed review_result.pending = %v, want true", results[len(results)-1]["pending"])
	}
}

// TestCheckRetryCompleteFailureShowsPending: the retry path owes the same
// immediate 待核实 (#96 review P2-3) — a full-success retry whose Complete
// fails reports pending, stays open, and never speaks the summary.
func TestCheckRetryCompleteFailureShowsPending(t *testing.T) {
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
	if _, err := db.Exec("CREATE TRIGGER block_retry_complete BEFORE UPDATE ON lesson_save_receipts " +
		"BEGIN SELECT RAISE(ABORT, 'complete blocked'); END"); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	spokenBefore := s.countOf(TypeTeacherSentence)
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-retry-complete-fail"}))
	// Wait for the retry's FRAME, not its SubmitReview return (#96 N4): the
	// call count moved the moment the provider call returned, while the
	// receipt Complete and the publish still lay ahead on the same goroutine
	// — a correct implementation could legitimately lose the race between
	// the old wait and the frame read. The second frame on the wire IS the
	// settled publish.
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeReviewResult) >= 2 }) {
		t.Fatal("the retry's review_result frame never arrived")
	}
	if got := len(words.submissions()); got != 2 {
		t.Fatalf("SubmitReview calls = %d, want 2 after the retry", got)
	}
	results := s.controlsOf(t, TypeReviewResult)
	last := results[len(results)-1]
	if last["pending"] != true {
		t.Fatalf("retry review_result.pending = %v, want true", last["pending"])
	}
	time.Sleep(300 * time.Millisecond)
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended = %d, want 0 (a pending receipt never ends the session)", got)
	}
	if got := s.countOf(TypeTeacherSentence); got != spokenBefore {
		t.Fatalf("teacher sentences = %d, want unchanged %d (no summary over a pending receipt)", got, spokenBefore)
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

// reviewEndRaceSender races ONE end_session against a review_result publish
// (#96 N1, the endRaceSender pattern): the probe fires only when tm.mu is
// FREE at the moment the write path ships its frame. A disciplined publish
// holds the lock through the liveness gate AND the enqueue, so the probe can
// never fire — the frame lands, no goodbye exists. An undisciplined one
// (check the gate, unlock, then send) gets the goodbye strictly BEFORE its
// review_result, and the wire-order assertion goes red.
type reviewEndRaceSender struct {
	*captureSender
	tm    *TurnManager
	fired bool
}

// SendControl implements Sender.
func (e *reviewEndRaceSender) SendControl(v any) error {
	if !e.fired {
		var probe struct {
			Type string `json:"type"`
		}
		if b, err := json.Marshal(v); err == nil && json.Unmarshal(b, &probe) == nil && probe.Type == TypeReviewResult {
			if e.tm.mu.TryLock() {
				e.tm.mu.Unlock()
				e.tm.handleEndSession(e)
				e.fired = true
			}
		}
	}
	return e.captureSender.SendControl(v)
}

// ttAssertNothingTrailsEnded fails the test when any frame of typ ships
// after the first session_ended — the wire shape an unlocked publish leaves
// behind (session_ended must stay the LAST control message on the
// connection, #96 N1 probes).
func ttAssertNothingTrailsEnded(t *testing.T, s *captureSender, typ string) {
	t.Helper()
	order := s.typeOrder()
	firstEnded := -1
	for i, got := range order {
		if got == TypeSessionEnded && firstEnded < 0 {
			firstEnded = i
		}
	}
	if firstEnded < 0 {
		return
	}
	for i := firstEnded + 1; i < len(order); i++ {
		if order[i] == typ {
			t.Errorf("%s(@%d) trails session_ended(@%d) — the publish escaped its liveness gate's lock hold", typ, i, firstEnded)
		}
	}
}

// TestCheckSubmitPublishesReviewUnderLock (#96 N1): the FIRST submit's
// review_result — and, because a partial save never ends the session, the
// grading state that follows it in the same publish — must ride ONE tm.mu
// hold with its liveness gate. The probe's end_session lands only in the
// unlocked gap of a split implementation, and there it orders the terminal
// session_ended ahead of the frame.
func TestCheckSubmitPublishesReviewUnderLock(t *testing.T) {
	receipts, _ := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	// Partial save: the publish is at its widest (review_result AND grading).
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果"}, Missing: []string{"香蕉"}}
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))

	p := &reviewEndRaceSender{captureSender: s, tm: tm}
	tm.HandleControl(p, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-race"}))
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeReviewResult) > 0 }) {
		t.Fatal("review_result never arrived after check_submit")
	}
	if p.fired {
		ttAssertNothingTrailsEnded(t, s, TypeReviewResult)
		return
	}
	// The probe could not slip in: no client goodbye exists at all, and the
	// partial save settled the panel into grading with the session open.
	if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 0 {
		t.Fatalf("session_ended count = %d, want 0 (the probe never fired)", len(ended))
	}
	if !waitFor(t, 5*time.Second, func() bool { return gradingCount(s) > 0 }) {
		t.Fatal("the partial save never settled the panel into grading")
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended = %d, want 0 (a partial save keeps the session open)", got)
	}
}

// TestCheckRetryPublishesReviewUnderLock (#96 N1): the RETRY's review_result
// owes the same one-hold publish — its write path runs off the read loop,
// exactly where an end_session can land between a split gate and the enqueue.
func TestCheckRetryPublishesReviewUnderLock(t *testing.T) {
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
	results := s.countOf(TypeReviewResult)
	p := &reviewEndRaceSender{captureSender: s, tm: tm}
	tm.HandleControl(p, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-race-retry"}))
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeReviewResult) >= results+1 }) {
		t.Fatal("the retry never published its review_result")
	}
	if p.fired {
		ttAssertNothingTrailsEnded(t, s, TypeReviewResult)
		return
	}
	if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 0 {
		t.Fatalf("session_ended count = %d, want 0 (the probe never fired — the retry's own summary ending comes later, via its ack)", len(ended))
	}
}

// TestCheckSubmitBeginFailureRewritesOnNextTap (#96 N3, criteria 1+2): with
// the receipt INSERT broken (a BEFORE INSERT ABORT trigger — the SELECT path
// keeps answering), the FIRST submit must not write at all: it answers an
// honest zero-updated review_result, latches reviewSeen, keeps the stage
// submitted and the session open. Once the trigger is gone, the NEXT
// check_submit finds no check row, sees the never-wrote memory result, and
// rewrites the WHOLE batch through a fresh receipt — the map and the new
// row's requested set both carry every word, and the honest full result
// (updated = requested) follows.
func TestCheckSubmitBeginFailureRewritesOnNextTap(t *testing.T) {
	receipts, db := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果", "香蕉"}}
	tm, s, _, exec := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))

	if _, err := db.Exec("CREATE TRIGGER block_first_begin BEFORE INSERT ON lesson_save_receipts " +
		"BEGIN SELECT RAISE(ABORT, 'begin blocked'); END"); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	// Criterion 1: no write, an honest zero frame, the latch still fell.
	result := ttSubmitCheck(t, tm, s)
	if int(result["updated"].(float64)) != 0 {
		t.Fatalf("review_result.updated = %v, want 0 (the receipt Begin failed)", result["updated"])
	}
	if int(result["missing"].(float64)) != len(cards) {
		t.Fatalf("review_result.missing = %v, want %d (the whole batch is unsaved)", result["missing"], len(cards))
	}
	if result["pending"] != nil {
		t.Fatalf("review_result.pending = %v, want absent (nothing was ever written)", result["pending"])
	}
	if got := len(words.submissions()); got != 0 {
		t.Fatalf("SubmitReview calls = %d, want 0: no receipt row, no write", got)
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended = %d, want 0 (the panel stays open for another tap)", got)
	}
	l, err := exec.GetLessonState(t.Context(), ttLessonID)
	if err != nil {
		t.Fatalf("GetLessonState: %v", err)
	}
	if l.Stage != lesson.StageSubmitted {
		t.Fatalf("stage = %q, want submitted (the CompleteCheck latch does not roll back)", l.Stage)
	}

	// Criterion 2: trigger gone — the next tap 补写s the whole batch.
	if _, err := db.Exec("DROP TRIGGER block_first_begin"); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-rewrite"}))
	if !waitFor(t, 5*time.Second, func() bool { return len(words.submissions()) == 1 }) {
		t.Fatal("the rewrite never ran after the trigger was dropped")
	}
	batch := words.submissions()[0]
	for _, w := range []string{"苹果", "香蕉"} {
		if _, ok := batch[w]; !ok {
			t.Fatalf("rewrite map missing %q: %v", w, batch)
		}
	}
	rows, rerr := receipts.ListByLesson(t.Context(), ttLessonID)
	if rerr != nil {
		t.Fatalf("ListByLesson: %v", rerr)
	}
	var rec *lesson.Receipt
	for i := range rows {
		if rows[i].Kind == lesson.ReceiptKindCheck {
			rec = &rows[i]
		}
	}
	if rec == nil {
		t.Fatal("the rewrite never created its receipt row")
	}
	if len(rec.Requested) != len(cards) {
		t.Fatalf("receipt requested = %v, want the whole batch %v", rec.Requested, cards)
	}
	results := s.controlsOf(t, TypeReviewResult)
	last := results[len(results)-1]
	if int(last["updated"].(float64)) != len(cards) {
		t.Fatalf("rewrite review_result.updated = %v, want %d", last["updated"], len(cards))
	}
	// The rewritten batch ends the lesson once its summary acks.
	ttAckSummaryAndEnd(t, tm, s, nil)
}

// TestCheckSubmitUnreadableStoreRewritesWholeBatchAfterRecovery (#96 N3,
// Cursor review strike 1): the WHOLLY unreadable store — Begin AND
// ListByLesson both fail, e.g. the SQLite handle is gone — must take the
// same first-submit skip the INSERT trigger does (criterion 1 of the
// sibling test), and once a fresh handle on the same file answers again,
// the NEXT check_submit finds no check row over a never-wrote memory
// result and 补写s the WHOLE batch through a fresh receipt. This is the
// half the old ListByLesson bypass stranded: FSRS written with no row, the
// recovered store answering pending forever.
func TestCheckSubmitUnreadableStoreRewritesWholeBatchAfterRecovery(t *testing.T) {
	receipts, db, path := receiptStoreForTestPath(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果", "香蕉"}}
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))

	// Break the WHOLE store: every receipt call fails, not just the INSERT.
	if err := db.Close(); err != nil {
		t.Fatalf("close receipt db: %v", err)
	}
	result := ttSubmitCheck(t, tm, s)
	if int(result["updated"].(float64)) != 0 {
		t.Fatalf("review_result.updated = %v, want 0 (the whole store is down)", result["updated"])
	}
	if got := len(words.submissions()); got != 0 {
		t.Fatalf("SubmitReview calls = %d, want 0 before recovery", got)
	}

	// Recovery: a fresh handle on the same file answers again.
	recoveredDb, err := database.Open(path)
	if err != nil {
		t.Fatalf("reopen receipt db: %v", err)
	}
	t.Cleanup(func() { _ = recoveredDb.Close() })
	recovered, err := lesson.NewReceiptStore(recoveredDb, nil)
	if err != nil {
		t.Fatalf("NewReceiptStore: %v", err)
	}
	tm.cfg.Receipts = recovered

	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-recover-rewrite"}))
	if !waitFor(t, 5*time.Second, func() bool { return len(words.submissions()) == 1 }) {
		t.Fatal("the whole-batch 补写 never ran after the store recovered")
	}
	batch := words.submissions()[0]
	for _, w := range []string{"苹果", "香蕉"} {
		if _, ok := batch[w]; !ok {
			t.Fatalf("补写 map missing %q: %v", w, batch)
		}
	}
	rows, rerr := recovered.ListByLesson(t.Context(), ttLessonID)
	if rerr != nil {
		t.Fatalf("ListByLesson: %v", rerr)
	}
	var rec *lesson.Receipt
	for i := range rows {
		if rows[i].Kind == lesson.ReceiptKindCheck {
			rec = &rows[i]
		}
	}
	if rec == nil {
		t.Fatal("the 补写 never created its receipt row")
	}
	if len(rec.Requested) != len(cards) {
		t.Fatalf("receipt requested = %v, want the whole batch %v", rec.Requested, cards)
	}
	results := s.controlsOf(t, TypeReviewResult)
	last := results[len(results)-1]
	if int(last["updated"].(float64)) != len(cards) {
		t.Fatalf("补写 review_result.updated = %v, want %d", last["updated"], len(cards))
	}
	// The rewritten batch ends the lesson once its summary acks.
	ttAckSummaryAndEnd(t, tm, s, nil)
}

// TestCheckRetryNoReceiptRowWithUpdatesAnswersPending (#96 N3, criterion 3):
// the receipt row is gone but the memory result already claims real updates
// — without a row there is no safe rewrite list, and re-sending the whole
// map would push already-updated words a second time. The answer is 待核实:
// one pending frame, no write, the retry gate shut.
func TestCheckRetryNoReceiptRowWithUpdatesAnswersPending(t *testing.T) {
	receipts, db := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果"}, Missing: []string{"香蕉"}}
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))
	first := ttSubmitCheck(t, tm, s)
	if int(first["updated"].(float64)) != 1 {
		t.Fatalf("first review_result.updated = %v, want 1 (the partial save is on record)", first["updated"])
	}

	if _, err := db.Exec("DELETE FROM lesson_save_receipts"); err != nil {
		t.Fatalf("delete receipt rows: %v", err)
	}
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-no-row"}))
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeReviewResult) >= 2 }) {
		t.Fatal("the no-row submit never answered")
	}
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want still 1 (no safe rewrite list without a row)", got)
	}
	last := s.controlsOf(t, TypeReviewResult)[s.countOf(TypeReviewResult)-1]
	if last["pending"] != true {
		t.Fatalf("review_result.pending = %v, want true (待核实, not a rewrite)", last["pending"])
	}
	if int(last["updated"].(float64)) != 0 {
		t.Fatalf("review_result.updated = %v, want 0", last["updated"])
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended = %d, want 0", got)
	}
}

// TestCheckRetryNoRowPendingFrameLatchesMemory (#96 review P3): the no-row
// 待核实 frame must LATCH into tutor.reviewResult — a later submit whose
// ListByLesson fails too (the query-failed branch) replays the in-memory
// result, and an unlatched frame would hand back the stale non-pending
// partial result, flipping the button from 待核实 back to 再保存一次.
func TestCheckRetryNoRowPendingFrameLatchesMemory(t *testing.T) {
	receipts, db := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果"}, Missing: []string{"香蕉"}}
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))
	ttSubmitCheck(t, tm, s) // the partial save is on record (updated=1)

	// No row + Updated>0: the pending frame (#96 N3 criterion 3).
	if _, err := db.Exec("DELETE FROM lesson_save_receipts"); err != nil {
		t.Fatalf("delete receipt rows: %v", err)
	}
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-no-row"}))
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeReviewResult) >= 2 }) {
		t.Fatal("the no-row submit never answered")
	}
	last := s.controlsOf(t, TypeReviewResult)[s.countOf(TypeReviewResult)-1]
	if last["pending"] != true {
		t.Fatalf("no-row review_result.pending = %v, want true", last["pending"])
	}

	// List fails too: the replay answers from memory — still pending.
	if err := db.Close(); err != nil {
		t.Fatalf("close receipt db: %v", err)
	}
	results := s.countOf(TypeReviewResult)
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-list-dead"}))
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeReviewResult) >= results+1 }) {
		t.Fatal("the query-failed replay never answered")
	}
	replayed := s.controlsOf(t, TypeReviewResult)[s.countOf(TypeReviewResult)-1]
	if replayed["pending"] != true {
		t.Fatalf("replayed review_result.pending = %v, want true (the no-row pending frame latched)", replayed["pending"])
	}
	if int(replayed["updated"].(float64)) != 0 {
		t.Fatalf("replayed review_result.updated = %v, want 0", replayed["updated"])
	}
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want still 1 (no write on either tap)", got)
	}
}

// TestCheckRetryReceiptQueryFailureWritesNothing (#96 N3, criterion 4): the
// receipt store itself is unreachable — whether a write landed is UNKNOWN,
// so this submit must not write either; it replays the in-memory result and
// stays open.
func TestCheckRetryReceiptQueryFailureWritesNothing(t *testing.T) {
	receipts, db := receiptStoreForTest(t)
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果"}, Missing: []string{"香蕉"}}
	tm, s, _, _ := ttDriveToCheckCfg(t, cards, words, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttCheckAll(t, tm, s, len(cards))
	ttSubmitCheck(t, tm, s) // the partial receipt is on record

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	results := s.countOf(TypeReviewResult)
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-query-dead"}))
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeReviewResult) >= results+1 }) {
		t.Fatal("the query-failed submit never answered")
	}
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want still 1 (an unknown answer never licenses a write)", got)
	}
	last := s.controlsOf(t, TypeReviewResult)[s.countOf(TypeReviewResult)-1]
	if int(last["updated"].(float64)) != 1 {
		t.Fatalf("replayed review_result.updated = %v, want the in-memory 1", last["updated"])
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended = %d, want 0", got)
	}
}
