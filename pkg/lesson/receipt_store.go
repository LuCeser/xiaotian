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
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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
// sorted requested word set, and returns the receipt (id filled). An empty
// lesson_id/request_id/kind rejects before anything is written — an
// anonymous row could never be joined back to a lesson honestly.
func (s *ReceiptStore) Begin(ctx context.Context, in ReceiptInput) (Receipt, error) {
	if in.LessonID == "" || in.RequestID == "" || in.Kind == "" {
		return Receipt{}, errors.Wrap(ErrInvalidLesson, "begin receipt: lesson_id, request_id and kind must be non-empty")
	}
	requested, err := encodeWordSet(in.Requested)
	if err != nil {
		return Receipt{}, errors.Wrap(err, "begin receipt: encode requested set")
	}
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO lesson_save_receipts ("+receiptColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		in.LessonID, in.RequestID, in.Kind, requested, "[]", "[]", ReceiptStatusPending, "", "",
	)
	if err != nil {
		s.logger.Error("receipt begin: insert failed",
			infra.F("component", "lesson/receipt_store"),
			infra.F("lesson_id", in.LessonID),
			infra.F("request_id", in.RequestID),
			infra.F("kind", in.Kind),
			infra.ErrField(err),
		)
		return Receipt{}, errors.Wrap(err, "begin receipt: insert")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Receipt{}, errors.Wrap(err, "begin receipt: last insert id")
	}
	// Single-statement contract: the returned Receipt carries what the
	// INSERT wrote; created_at/completed_at stay DB-side (the row readback
	// happens through ListByLesson).
	return Receipt{
		ID:        id,
		LessonID:  in.LessonID,
		RequestID: in.RequestID,
		Kind:      in.Kind,
		Requested: sortedCopy(in.Requested),
		Status:    ReceiptStatusPending,
	}, nil
}

// Complete records phase two: UPDATE the pending row to saved/failed with
// the real updated/missing sets and a completion timestamp. Idempotent by
// construction — the WHERE status='pending' guard makes a second Complete
// zero-match into a no-op (the FIRST completion is history; a late
// duplicate callback never rewrites a settled outcome).
func (s *ReceiptStore) Complete(ctx context.Context, id int64, updated, missing []string, saved bool) error {
	if id <= 0 {
		return errors.Wrap(ErrInvalidLesson, "complete receipt: id must be positive")
	}
	updatedJSON, err := encodeWordSet(updated)
	if err != nil {
		return errors.Wrap(err, "complete receipt: encode updated set")
	}
	missingJSON, err := encodeWordSet(missing)
	if err != nil {
		return errors.Wrap(err, "complete receipt: encode missing set")
	}
	status := ReceiptStatusFailed
	if saved {
		status = ReceiptStatusSaved
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE lesson_save_receipts SET updated = ?, missing = ?, status = ?, completed_at = datetime('now') WHERE id = ? AND status = ?",
		updatedJSON, missingJSON, status, id, ReceiptStatusPending,
	); err != nil {
		s.logger.Error("receipt complete: update failed",
			infra.F("component", "lesson/receipt_store"),
			infra.F("receipt_id", id),
			infra.F("status", status),
			infra.ErrField(err),
		)
		return errors.Wrap(err, "complete receipt: update")
	}
	// Zero affected rows = the row was already completed: the deliberate
	// idempotent no-op, not an error.
	return nil
}

// ListByLesson returns every receipt row of the lesson, first-recorded
// first. An unknown lesson lists zero rows with no error. Word-set columns
// decode with legacy-default tolerance (an empty raw value reads as the
// empty set); corrupted non-empty JSON surfaces as a wrapped error — a
// silently-empty set would misstate what was saved.
func (s *ReceiptStore) ListByLesson(ctx context.Context, lessonID string) ([]Receipt, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, "+receiptColumns+" FROM lesson_save_receipts WHERE lesson_id = ? ORDER BY id",
		lessonID,
	)
	if err != nil {
		s.logger.Error("receipt list by lesson failed",
			infra.F("component", "lesson/receipt_store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "list receipts by lesson")
	}
	defer rows.Close()

	var receipts []Receipt
	for rows.Next() {
		var (
			r             Receipt
			requestedJSON string
			updatedJSON   string
			missingJSON   string
		)
		if err := rows.Scan(&r.ID, &r.LessonID, &r.RequestID, &r.Kind, &requestedJSON, &updatedJSON, &missingJSON, &r.Status, &r.CreatedAt, &r.CompletedAt); err != nil {
			return nil, errors.Wrap(err, "list receipts: scan")
		}
		if r.Requested, err = decodeWordSet(requestedJSON); err != nil {
			s.logger.Error("receipt list: decode requested failed",
				infra.F("component", "lesson/receipt_store"),
				infra.F("receipt_id", r.ID),
				infra.ErrField(err),
			)
			return nil, errors.Wrap(err, fmt.Sprintf("list receipts: receipt %d: decode requested", r.ID))
		}
		if r.Updated, err = decodeWordSet(updatedJSON); err != nil {
			s.logger.Error("receipt list: decode updated failed",
				infra.F("component", "lesson/receipt_store"),
				infra.F("receipt_id", r.ID),
				infra.ErrField(err),
			)
			return nil, errors.Wrap(err, fmt.Sprintf("list receipts: receipt %d: decode updated", r.ID))
		}
		if r.Missing, err = decodeWordSet(missingJSON); err != nil {
			s.logger.Error("receipt list: decode missing failed",
				infra.F("component", "lesson/receipt_store"),
				infra.F("receipt_id", r.ID),
				infra.ErrField(err),
			)
			return nil, errors.Wrap(err, fmt.Sprintf("list receipts: receipt %d: decode missing", r.ID))
		}
		receipts = append(receipts, r)
	}
	return receipts, rows.Err()
}

// LatestByLessonIDs aggregates, for each requested lesson, its LATEST receipt
// row (id max = last recorded) — the list-page save-status source (13.3-02).
// An empty id slice is the zero-SQL fast path (the list page can be bounded
// to zero rows); lessons with no receipt row are simply absent from the map.
// RED stub (13.3-02): returns nil — the GREEN step implements the grouped
// SELECT.
func (s *ReceiptStore) LatestByLessonIDs(ctx context.Context, lessonIDs []string) (map[string]Receipt, error) {
	return nil, nil
}

// encodeWordSet serializes a word set deterministically: sorted copy, then
// JSON — map iteration order must never leak into a stored row payload.
func encodeWordSet(words []string) (string, error) {
	b, err := json.Marshal(sortedCopy(words))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeWordSet decodes a stored word-set column: an empty raw value (a row
// predating the column, or a hand edit) reads as the empty set, corrupted
// non-empty JSON surfaces as an error (same honesty contract as
// decodeOutcomesJSON).
func decodeWordSet(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	var words []string
	if err := json.Unmarshal([]byte(raw), &words); err != nil {
		return nil, err
	}
	if words == nil {
		words = []string{}
	}
	return words, nil
}

// sortedCopy returns a sorted copy of words — the caller's slice order is
// never mutated.
func sortedCopy(words []string) []string {
	sorted := make([]string, len(words))
	copy(sorted, words)
	sort.Strings(sorted)
	return sorted
}
