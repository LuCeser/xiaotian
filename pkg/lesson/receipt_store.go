// receipt_store.go — the 13.3-01 persistent save-receipt store (ROADMAP
// success criterion 1). Every REAL save attempt at the four submission
// endpoints (normal check, post-hoc correction, unchecked-close fallback,
// extra practice) records exactly ONE lesson_save_receipts row via a
// two-phase write: Begin INSERTs status='pending' BEFORE the downstream
// write; Complete UPDATEs the row to saved/failed with the REAL
// requested/updated/missing word sets AFTER the downstream write returns.
// 「已提交」 and 「已写入 FSRS」 are henceforth distinguishable: a process exit
// BETWEEN the two statements leaves the row pending — the one honest source
// of the parent view's 「待核实」 (there is no cross-process transaction to
// lean on: the FSRS write happens inside the Python subprocess). Receipts
// are a BYPASS record: a receipt write failure never blocks the submission,
// never changes review_result, and NEVER drives an automatic rewrite (S-05;
// reconciliation is Phase 15). The two phases are deliberately TWO
// statements, not a transaction — the crash semantics ARE the gap between
// them.
package lesson

import (
	"context"
	"database/sql"

	"github.com/pkg/errors"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// receiptDDL creates the Go-owned lesson_save_receipts table: one row per
// real save attempt. The word-set columns carry JSON string arrays (13.3-01
// user decision ②: sets, not counts — the parent view's per-word save detail
// needs the actual set; ≤10 words/lesson keeps rows small). status starts
// pending and is completed exactly once (the Complete WHERE guard).
var receiptDDL = `CREATE TABLE IF NOT EXISTS lesson_save_receipts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    lesson_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    requested TEXT NOT NULL DEFAULT '[]',
    updated TEXT NOT NULL DEFAULT '[]',
    missing TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT NOT NULL DEFAULT ''
)`

// idxReceiptsLesson serves ListByLesson and the parent-facing per-lesson
// receipt aggregation (13.3-02's read path).
var idxReceiptsLesson = `CREATE INDEX IF NOT EXISTS idx_receipts_lesson ON lesson_save_receipts(lesson_id)`

// receiptColumns is the explicit insert/query column list (never SELECT * —
// the practiceLogsColumns discipline; the SQLite pool is ONE connection and
// classroom writes share it).
const receiptColumns = "lesson_id, request_id, kind, requested, updated, missing, status, created_at, completed_at"

// Receipt kind values — the four submission endpoints (13.3-01). Frozen
// string values: the parent view's type badges read them verbatim.
const (
	// ReceiptKindCheck marks the normal check-stage unified submission.
	ReceiptKindCheck = "check"
	// ReceiptKindCorrection marks the post-hoc correction diff submission
	// (13.1-04's correction session).
	ReceiptKindCorrection = "correction"
	// ReceiptKindUncheckedFallback marks the unchecked-close fallback write
	// (13.1-04's D-03 path, request id lessonID+"-complete-check").
	ReceiptKindUncheckedFallback = "unchecked_fallback"
	// ReceiptKindPractice marks the 「再练一组」 practice submission. Its
	// saved/failed expresses the practice_logs storage result, NOT an FSRS
	// write (practice never enters the FSRS schedule, D-11).
	ReceiptKindPractice = "practice"
)

// Receipt status values — the two-phase lifecycle. pending = Begin landed
// and Complete never did (the crash window → the parent view's 「待核实」);
// saved/failed = the REAL downstream write result, filled by Complete. A
// lesson with NO receipt row reads as 「保存状态未知」 (historical rows), never
// as saved.
const (
	ReceiptStatusPending = "pending"
	ReceiptStatusSaved   = "saved"
	ReceiptStatusFailed  = "failed"
)

// Receipt is one row of lesson_save_receipts. Requested is the submitted
// word set (sorted before storage so map iteration order never leaks into
// the row); Updated/Missing are the real downstream outcome sets (empty
// until Complete). CreatedAt/CompletedAt are SQLite datetime('now') strings
// (UTC) — CompletedAt stays empty while the row is pending.
type Receipt struct {
	ID          int64
	LessonID    string
	RequestID   string
	Kind        string
	Requested   []string
	Updated     []string
	Missing     []string
	Status      string
	CreatedAt   string
	CompletedAt string
}

// ReceiptInput is the Begin-side payload: who submitted (RequestID — the
// client's idempotency key, or lessonID+"-complete-check" for the fallback),
// where (LessonID), which endpoint (Kind), and the requested word set.
type ReceiptInput struct {
	LessonID  string
	RequestID string
	Kind      string
	Requested []string
}

// ReceiptStore is the two-phase write + per-lesson read path for
// lesson_save_receipts. Repository pattern mirrors lesson.PracticeStore:
// nil-guarded constructor, NoOpLogger fallback, sentinel errors via
// errors.Is.
type ReceiptStore struct {
	db     *sql.DB
	logger infra.Logger
}

// NewReceiptStore constructs a ReceiptStore from an open *sql.DB. Returns
// an error if db is nil (D-47 nil-guard); a nil logger becomes the no-op
// logger (same constructor discipline as NewPracticeStore).
func NewReceiptStore(db *sql.DB, logger infra.Logger) (*ReceiptStore, error) {
	if db == nil {
		return nil, errors.Wrap(ErrInvalidLesson, "lesson/receipt_store: *sql.DB must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &ReceiptStore{db: db, logger: logger}, nil
}

// Begin records phase one: INSERT one status='pending' row carrying the
// sorted requested word set. RED scaffold — the INSERT lands in the GREEN
// commit.
func (s *ReceiptStore) Begin(ctx context.Context, in ReceiptInput) (Receipt, error) {
	_ = ctx
	_ = in
	return Receipt{}, nil // RED scaffold
}

// Complete records phase two: UPDATE the pending row to saved/failed with
// the real updated/missing sets and a completion timestamp. Idempotent by
// construction — the WHERE status='pending' guard makes a second Complete
// zero-match into a no-op. RED scaffold — the UPDATE lands in GREEN.
func (s *ReceiptStore) Complete(ctx context.Context, id int64, updated, missing []string, saved bool) error {
	_ = ctx
	_ = id
	_ = updated
	_ = missing
	_ = saved
	return nil // RED scaffold
}

// ListByLesson returns every receipt row of the lesson, first-recorded
// first. RED scaffold — the query lands in GREEN.
func (s *ReceiptStore) ListByLesson(ctx context.Context, lessonID string) ([]Receipt, error) {
	_ = ctx
	_ = lessonID
	return nil, nil // RED scaffold
}
