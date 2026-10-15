// practice_store.go — the 13.1-05 practice-record store (D-11/D-12). One
// practice set ("再练一组") records its per-word results HERE and ONLY here:
// practice never enters the FSRS main schedule (no review_logs row, no
// RunReview/SubmitReview — the ADR-2026-08-01 write-separation red line), so
// same-day extra practice cannot distort the scheduler. The schema keeps the
// review_logs-mappable fields (word/rating/correct/hints/reviewed_at — the
// future promotion migration's input, D-12) plus a Beijing-calendar
// reviewed_day column: SQLite datetime('now') is UTC, and 「当日已默」 must
// mean the SAME Beijing day the r0-YYYYMMDD lesson ids use, so the caller
// stamps reviewed_day explicitly and ListPracticeWordsOn queries it.
package lesson

import (
	"context"
	"database/sql"
	"strings"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// practiceLogsDDL creates the Go-owned practice_logs table. The reviewed_at
// default is UTC datetime('now') (SQLite); the reviewed_day column carries
// the Beijing-calendar date the caller computed (the 「当日」 semantics that
// matches r0-YYYYMMDD), so the day query never depends on server timezone.
var practiceLogsDDL = `CREATE TABLE IF NOT EXISTS practice_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    lesson_id TEXT NOT NULL,
    word TEXT NOT NULL,
    rating TEXT NOT NULL,
    correct INTEGER NOT NULL,
    hints INTEGER NOT NULL DEFAULT 0,
    reviewed_at TEXT NOT NULL DEFAULT (datetime('now')),
    reviewed_day TEXT NOT NULL DEFAULT ''
)`

// idxPracticeLogsDay serves ListPracticeWordsOn's day-scoped lookups.
var idxPracticeLogsDay = `CREATE INDEX IF NOT EXISTS idx_practice_logs_date ON practice_logs(reviewed_day)`

// practiceLogsColumns is the explicit insert/query column list (never
// SELECT * — RESEARCH Pitfall 4 discipline, mirrors cardColumns).
const practiceLogsColumns = "lesson_id, word, rating, correct, hints, reviewed_at, reviewed_day"

// PracticeRecord is one word's practice result (the D-02 rating vocabulary
// shared with the main path — one matrix, never a fork). ReviewedAt is
// optional: empty defers to the reviewed_at DEFAULT; ReviewedDay is REQUIRED
// (the Beijing-calendar date string "2006-01-02" — 「当日已默」's authority).
type PracticeRecord struct {
	LessonID    string
	Word        string
	Rating      string
	Correct     bool
	Hints       int
	ReviewedAt  string
	ReviewedDay string
}

// PracticeStore is the append-only write + day-query path for practice_logs.
// Repository pattern mirrors lesson.Store: nil-guarded constructor,
// NoOpLogger fallback, sentinel errors via errors.Is.
type PracticeStore struct {
	db     *sql.DB
	logger infra.Logger
}

// NewPracticeStore constructs a PracticeStore from an open *sql.DB. Returns
// an error if db is nil (D-47 nil-guard); a nil logger becomes the no-op
// logger (same constructor discipline as NewStore).
func NewPracticeStore(db *sql.DB, logger infra.Logger) (*PracticeStore, error) {
	if db == nil {
		return nil, errors.Wrap(ErrInvalidLesson, "lesson/practice_store: *sql.DB must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &PracticeStore{db: db, logger: logger}, nil
}

// RecordPractice writes one practice set in a single transaction: every row
// lands or none does (a half-recorded practice set would lie to the
// 「当日已默」 exclusion on the NEXT practice pick). An empty batch is a
// legal no-op; a record with an empty lesson_id/word/reviewed_day rejects
// the WHOLE batch with ErrInvalidLesson before anything is written.
func (s *PracticeStore) RecordPractice(ctx context.Context, records []PracticeRecord) error {
	if len(records) == 0 {
		return nil
	}
	for _, r := range records {
		if r.LessonID == "" || r.Word == "" || r.ReviewedDay == "" {
			return errors.Wrap(ErrInvalidLesson, "record practice: lesson_id, word and reviewed_day must be non-empty")
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "record practice: begin tx")
	}
	defer func() { _ = tx.Rollback() }()

	for _, r := range records {
		correct := 0
		if r.Correct {
			correct = 1
		}
		var err error
		if r.ReviewedAt != "" {
			_, err = tx.ExecContext(ctx,
				"INSERT INTO practice_logs ("+practiceLogsColumns+") VALUES (?, ?, ?, ?, ?, ?, ?)",
				r.LessonID, r.Word, r.Rating, correct, r.Hints, r.ReviewedAt, r.ReviewedDay,
			)
		} else {
			_, err = tx.ExecContext(ctx,
				"INSERT INTO practice_logs (lesson_id, word, rating, correct, hints, reviewed_day) VALUES (?, ?, ?, ?, ?, ?)",
				r.LessonID, r.Word, r.Rating, correct, r.Hints, r.ReviewedDay,
			)
		}
		if err != nil {
			s.logger.Error("practice record: insert failed",
				infra.F("component", "lesson/practice_store"),
				infra.F("lesson_id", r.LessonID),
				infra.F("reviewed_day", r.ReviewedDay),
				infra.ErrField(err),
			)
			return errors.Wrap(err, "record practice: insert")
		}
	}

	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "record practice: commit")
	}
	return nil
}

// ListPracticeWordsOn returns every word practiced on the given
// Beijing-calendar day ("2006-01-02"), first-recorded first. The
// exclusion-set input of the next practice pick (D-11 避开当日已默) and the
// parent-facing query both read this one shape.
func (s *PracticeStore) ListPracticeWordsOn(ctx context.Context, day string) ([]string, error) {
	day = trimPracticeDay(day)
	rows, err := s.db.QueryContext(ctx,
		"SELECT word FROM practice_logs WHERE reviewed_day = ? ORDER BY id",
		day,
	)
	if err != nil {
		s.logger.Error("practice list by day failed",
			infra.F("component", "lesson/practice_store"),
			infra.F("reviewed_day", day),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "list practice words on day")
	}
	defer rows.Close()

	var words []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, errors.Wrap(err, "list practice words: scan")
		}
		words = append(words, w)
	}
	return words, rows.Err()
}

// ListPracticeByLesson returns every practice_logs row of the given lesson in
// id (insert) order — the per-lesson practice detail behind the parent
// lesson-detail read (13.3-02). An unknown lesson lists zero rows with no
// error. Explicit columns only (the practiceLogsColumns discipline — the
// single shared connection carries the classroom write path too, P4).
func (s *PracticeStore) ListPracticeByLesson(ctx context.Context, lessonID string) ([]PracticeRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+practiceLogsColumns+" FROM practice_logs WHERE lesson_id = ? ORDER BY id ASC",
		lessonID,
	)
	if err != nil {
		s.logger.Error("practice list by lesson failed",
			infra.F("component", "lesson/practice_store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "list practice by lesson")
	}
	defer rows.Close()

	var records []PracticeRecord
	for rows.Next() {
		var (
			r       PracticeRecord
			correct int
		)
		if err := rows.Scan(&r.LessonID, &r.Word, &r.Rating, &correct, &r.Hints, &r.ReviewedAt, &r.ReviewedDay); err != nil {
			return nil, errors.Wrap(err, "list practice by lesson: scan")
		}
		r.Correct = correct != 0
		records = append(records, r)
	}
	return records, rows.Err()
}

// practiceHasColumn reports whether practice_logs carries the named column
// (kept symmetric with lessonsHasColumn for the migration tests).
func practiceHasColumn(ctx context.Context, db *sql.DB, name string) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(practice_logs)")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			colName   string
			ctype     string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &colName, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if colName == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

// trimPracticeDay guards the day-string shape at the one place it matters —
// a whitespace-padded day would silently miss the index. Exported logic
// stays in ListPracticeWordsOn; this is its input hygiene.
func trimPracticeDay(day string) string {
	return strings.TrimSpace(day)
}
