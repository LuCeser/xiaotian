// practice_store.go — the Issue #14 passage 背默 practice-record store. One
// 背默 attempt writes EXACTLY ONE row here, keyed by CONTENT + RANGE
// (passage_id + whole + canonical paragraph set): 掌握身份是「这篇课文的这
// 个范围」— 第 2 段掌握不能算第 3 段或全文掌握, so the paragraph set IS part
// of the identity, never a mere annotation. A passage is one work, never a
// bag of per-paragraph word entries: its practice history never lands in
// lesson.PracticeStore (whose rows are WORD-shaped and feed the 当日已默 word
// exclusion) and never touches the FSRS schedule (passages are not cards —
// the store.go ownership contract; AC 调度衔接: the existing word FSRS and
// whole-poem scheduling semantics stay untouched, each with its own tables).
//
// scope_id rides along as TRACEABILITY only (which passage_scopes row the
// attempt practiced). The row is NOT the identity: a range the parent removes
// and re-adds gets a NEW scope row id while the history keyed by
// (passage, whole, paragraphs) keeps counting — 保留过去段落历史, and
// practicing the whole passage later never inherits the paragraph rows'
// history (不伪造全文练习历史).
//
// Per-range scheduling mirrors the poem classroom's Go-owned discipline: a
// non-mastered attempt is due again the next Beijing day; a mastered attempt
// is due for consolidation seven days later. One schedule row per RANGE.
package passage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// passagePracticeDDL creates the Go-owned passage_practice_logs table: one
// row per real 背默 submit. reviewed_day is the Beijing-calendar date the
// CALLER stamps (the poem_practice_logs discipline: SQLite datetime('now')
// is UTC, and 「当日」 must mean the Beijing day the r0w- lesson ids use).
var passagePracticeDDL = `CREATE TABLE IF NOT EXISTS passage_practice_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    passage_id INTEGER NOT NULL,
    whole INTEGER NOT NULL DEFAULT 0,
    paragraphs TEXT NOT NULL,              -- canonical JSON array; "[]" when whole
    scope_id INTEGER NOT NULL DEFAULT 0,   -- traceability only, never identity
    lesson_id TEXT NOT NULL,
    total_paragraphs INTEGER NOT NULL,
    correct_paragraphs INTEGER NOT NULL DEFAULT 0,
    hints INTEGER NOT NULL DEFAULT 0,
    mastered INTEGER NOT NULL DEFAULT 0,
    reviewed_at TEXT NOT NULL DEFAULT (datetime('now')),
    reviewed_day TEXT NOT NULL DEFAULT ''
)`

// idxPassagePracticeRange serves ListByRange/LatestByRange and the content
// view's per-scope projection.
var idxPassagePracticeRange = `CREATE INDEX IF NOT EXISTS idx_passage_practice_range ON passage_practice_logs(passage_id, whole, paragraphs)` //nolint:gosec // G101 false positive: "passage" merely contains "pass"; a DDL index statement, never a credential

// passageReviewScheduleDDL stores the actionable next review for ONE range —
// the row a re-attempt replaces only after a successfully committed attempt.
// The primary key is the same content + range identity as the logs.
var passageReviewScheduleDDL = `CREATE TABLE IF NOT EXISTS passage_review_schedule (
    passage_id INTEGER NOT NULL,
    whole INTEGER NOT NULL DEFAULT 0,
    paragraphs TEXT NOT NULL,
    last_lesson_id TEXT NOT NULL,
    next_review_day TEXT NOT NULL,
    last_mastered INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (passage_id, whole, paragraphs)
)`

// PracticeRecord is ONE range attempt's result: the judged paragraph counts
// (the lessons row's verdicts are the per-paragraph detail; this row is the
// RANGE-scope summary), the hint total across paragraphs, and the mastery
// verdict (范围内全部段落全对). Whole=false carries the 1-based paragraph set;
// Whole=true stores the empty set. ReviewedAt is optional (empty defers to
// the column DEFAULT); ReviewedDay is REQUIRED (Beijing "2006-01-02").
type PracticeRecord struct {
	LessonID          string
	ReviewedAt        string
	ReviewedDay       string
	Paragraphs        []int
	PassageID         int64
	ScopeID           int64
	TotalParagraphs   int
	CorrectParagraphs int
	Hints             int
	Whole             bool
	Mastered          bool
}

// rangeIdentity canonicalizes one practice range onto its storage identity:
// the whole flag plus the canonical JSON index set ("[]" when whole). The
// store compares ranges by plain string equality of this projection, so
// [3,2] and [2,3] are the SAME range (选择第 2、3 段 lands as exactly one
// identity) while a paragraph set and whole stay DIFFERENT identities.
func rangeIdentity(whole bool, paragraphs []int) (int, string, error) {
	wholeFlag := 0
	indexes := paragraphs
	if whole {
		wholeFlag = 1
		indexes = nil // a whole range stores the canonical empty set
	}
	canonical, encoded, ok := canonicalParagraphs(indexes, 1<<30)
	if !ok || (!whole && len(canonical) == 0) {
		return 0, "", errors.Wrap(ErrInvalidPassage,
			"passage practice: a paragraph range must name at least one positive paragraph")
	}
	return wholeFlag, encoded, nil
}

// PracticeStore is the append-only write + per-range read path for
// passage_practice_logs. Repository pattern mirrors passage.Store and
// poem.PracticeStore: nil-guarded constructor, NoOpLogger fallback, sentinel
// errors via errors.Is.
type PracticeStore struct {
	db     *sql.DB
	logger infra.Logger
}

// NewPracticeStore constructs a PracticeStore from an open *sql.DB (D-47
// nil-guard; nil logger degrades to the NoOp one).
func NewPracticeStore(db *sql.DB, logger infra.Logger) (*PracticeStore, error) {
	if db == nil {
		return nil, errors.Wrap(ErrInvalidPassage, "passage/practice_store: *sql.DB must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &PracticeStore{db: db, logger: logger}, nil
}

// RecordPractice writes ONE attempt row plus its per-range schedule
// replacement in a single transaction. The gate is strict on purpose: the
// row is the parent-facing mastery record, so a half-shaped row (passage 0,
// empty lesson, no day, out-of-range counts) rejects BEFORE anything is
// written rather than storing a lie. A duplicate submit is deduplicated
// UPSTREAM by the CompleteCheck stage latch (one submitted lesson → one save
// attempt), not by a unique index here — re-attempts of the SAME range on
// another day are legitimate new rows.
func (s *PracticeStore) RecordPractice(ctx context.Context, r PracticeRecord) error {
	if r.PassageID <= 0 || r.LessonID == "" || r.ReviewedDay == "" {
		return errors.Wrap(ErrInvalidPassage, "record passage practice: passage_id, lesson_id and reviewed_day must be set")
	}
	if r.TotalParagraphs <= 0 || r.CorrectParagraphs < 0 || r.CorrectParagraphs > r.TotalParagraphs {
		return errors.Wrap(ErrInvalidPassage, "record passage practice: paragraph counts out of range")
	}
	if !r.Whole && len(r.Paragraphs) == 0 {
		return errors.Wrap(ErrInvalidPassage, "record passage practice: a paragraph range must name its paragraphs")
	}
	if r.Whole && len(r.Paragraphs) > 0 {
		return errors.Wrap(ErrInvalidPassage, "record passage practice: a whole range carries no paragraph set")
	}
	wholeFlag, canonical, err := rangeIdentity(r.Whole, r.Paragraphs)
	if err != nil {
		return err
	}
	reviewedDay, err := time.Parse("2006-01-02", r.ReviewedDay)
	if err != nil {
		return errors.Wrap(ErrInvalidPassage, "record passage practice: reviewed_day must be YYYY-MM-DD")
	}
	mastered := 0
	if r.Mastered {
		mastered = 1
	}
	daysUntilReview := 1
	if r.Mastered {
		daysUntilReview = 7
	}
	nextReviewDay := reviewedDay.AddDate(0, 0, daysUntilReview).Format("2006-01-02")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "record passage practice: begin")
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var res sql.Result
	if r.ReviewedAt != "" {
		res, err = tx.ExecContext(ctx,
			`INSERT INTO passage_practice_logs (passage_id, whole, paragraphs, scope_id, lesson_id, total_paragraphs, correct_paragraphs, hints, mastered, reviewed_at, reviewed_day)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.PassageID, wholeFlag, canonical, r.ScopeID, r.LessonID,
			r.TotalParagraphs, r.CorrectParagraphs, r.Hints, mastered, r.ReviewedAt, r.ReviewedDay,
		)
	} else {
		res, err = tx.ExecContext(ctx,
			`INSERT INTO passage_practice_logs (passage_id, whole, paragraphs, scope_id, lesson_id, total_paragraphs, correct_paragraphs, hints, mastered, reviewed_day)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.PassageID, wholeFlag, canonical, r.ScopeID, r.LessonID,
			r.TotalParagraphs, r.CorrectParagraphs, r.Hints, mastered, r.ReviewedDay,
		)
	}
	if err != nil {
		s.logger.Error("passage practice record: insert failed",
			infra.F("component", "passage/practice_store"),
			infra.F("passage_id", r.PassageID),
			infra.F("lesson_id", r.LessonID),
			infra.ErrField(err),
		)
		return errors.Wrap(err, "record passage practice: insert")
	}
	if n, cerr := res.RowsAffected(); cerr == nil && n != 1 {
		return errors.Wrap(ErrInvalidPassage, fmt.Sprintf("record passage practice: unexpected affected rows %d", n))
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO passage_review_schedule (passage_id, whole, paragraphs, last_lesson_id, next_review_day, last_mastered, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(passage_id, whole, paragraphs) DO UPDATE SET
		 last_lesson_id=excluded.last_lesson_id,
		 next_review_day=excluded.next_review_day,
		 last_mastered=excluded.last_mastered,
		 updated_at=datetime('now')`,
		r.PassageID, wholeFlag, canonical, r.LessonID, nextReviewDay, mastered,
	); err != nil {
		s.logger.Error("passage practice record: schedule failed",
			infra.F("component", "passage/practice_store"),
			infra.F("passage_id", r.PassageID),
			infra.F("lesson_id", r.LessonID),
			infra.ErrField(err),
		)
		return errors.Wrap(err, "record passage practice: schedule")
	}
	if err = tx.Commit(); err != nil {
		return errors.Wrap(err, "record passage practice: commit")
	}
	return nil
}

// passagePracticeColumns is the shared read column list of both range
// queries (the poem twin's discipline: one list, two callers, no drift).
const passagePracticeColumns = `passage_id, whole, paragraphs, scope_id, lesson_id, total_paragraphs, correct_paragraphs, hints, mastered, reviewed_at, reviewed_day` //nolint:gosec // G101 false positive: "passage" merely contains "pass"; a SELECT column list, never a credential

// ListByRange returns every attempt of ONE range (content + range identity),
// first-recorded first. An unpracticed range lists zero rows with no error
// (never practiced is a legal answer, not a failure).
func (s *PracticeStore) ListByRange(ctx context.Context, passageID int64, whole bool, paragraphs []int) ([]PracticeRecord, error) {
	wholeFlag, canonical, err := rangeIdentity(whole, paragraphs)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+passagePracticeColumns+`
		 FROM passage_practice_logs WHERE passage_id = ? AND whole = ? AND paragraphs = ? ORDER BY id`,
		passageID, wholeFlag, canonical,
	)
	if err != nil {
		s.logger.Error("passage practice list failed",
			infra.F("component", "passage/practice_store"),
			infra.F("passage_id", passageID),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "list passage practice")
	}
	defer rows.Close()
	return scanPracticeRows(rows)
}

// LatestByRange returns the range's most recent attempt and whether one
// exists — the single mastery projection the parent content view reads.
// Absent is a legal answer, never an error.
func (s *PracticeStore) LatestByRange(ctx context.Context, passageID int64, whole bool, paragraphs []int) (PracticeRecord, bool, error) {
	wholeFlag, canonical, err := rangeIdentity(whole, paragraphs)
	if err != nil {
		return PracticeRecord{}, false, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+passagePracticeColumns+`
		 FROM passage_practice_logs WHERE passage_id = ? AND whole = ? AND paragraphs = ? ORDER BY id DESC LIMIT 1`,
		passageID, wholeFlag, canonical,
	)
	if err != nil {
		s.logger.Error("passage practice latest failed",
			infra.F("component", "passage/practice_store"),
			infra.F("passage_id", passageID),
			infra.ErrField(err),
		)
		return PracticeRecord{}, false, errors.Wrap(err, "latest passage practice")
	}
	defer rows.Close()
	records, err := scanPracticeRows(rows)
	if err != nil {
		return PracticeRecord{}, false, err
	}
	if len(records) == 0 {
		return PracticeRecord{}, false, nil
	}
	return records[0], true, nil
}

// scanPracticeRows materializes the shared column list. mastered reads back
// as the bool projection of the 0/1 column; paragraphs decode defensively
// (an unreadable column is an empty set — whole rows store "[]" anyway).
func scanPracticeRows(rows *sql.Rows) ([]PracticeRecord, error) {
	var records []PracticeRecord
	for rows.Next() {
		var (
			r          PracticeRecord
			mastered   int
			whole      int
			paragraphs string
		)
		if err := rows.Scan(&r.PassageID, &whole, &paragraphs, &r.ScopeID, &r.LessonID,
			&r.TotalParagraphs, &r.CorrectParagraphs, &r.Hints, &mastered, &r.ReviewedAt, &r.ReviewedDay); err != nil {
			return nil, errors.Wrap(err, "scan passage practice")
		}
		r.Whole = whole == 1
		r.Mastered = mastered != 0
		r.Paragraphs = decodeIndexes(paragraphs)
		records = append(records, r)
	}
	return records, rows.Err()
}
