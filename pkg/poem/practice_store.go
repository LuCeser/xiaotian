// practice_store.go — the Issue #12 whole-poem practice-record store. One
// 背默 attempt writes EXACTLY ONE row here, keyed by the POEM (poem_id +
// lesson_id): a poem is one work, never a bag of per-line word entries, so
// its practice history never lands in lesson.PracticeStore (whose rows are
// WORD-shaped and feed the 当日已默 word exclusion) and never touches the
// FSRS schedule (poems are not cards — the store.go ownership contract).
//
// The 后续练习安排 seam (issue #12: 本任务明确与现有学习调度的衔接方式):
// poems schedule OUTSIDE FSRS by design. Eligibility to PRACTICE stays the
// Issue #4 scope verdict (poem.InScope — 已学进度/手动加入/补充); what this
// table adds is the mastery history per poem — LatestByPoem is the single
// read the parent records view (and a future auto-arrangement) consume. A
// row with Mastered=true means 整首全对 for THAT attempt only; mastery of
// one poem never implies anything about another poem or about 课文 (US-43).
package poem

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// poemPracticeDDL creates the Go-owned poem_practice_logs table: one row per
// real 背默 submit. reviewed_day is the Beijing-calendar date the CALLER
// stamps (the practice_logs discipline: SQLite datetime('now') is UTC, and
// 「当日」 must mean the Beijing day the r0p- lesson ids use).
var poemPracticeDDL = `CREATE TABLE IF NOT EXISTS poem_practice_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    poem_id INTEGER NOT NULL,
    lesson_id TEXT NOT NULL,
    total_lines INTEGER NOT NULL,
    correct_lines INTEGER NOT NULL DEFAULT 0,
    hints INTEGER NOT NULL DEFAULT 0,
    mastered INTEGER NOT NULL DEFAULT 0,
    reviewed_at TEXT NOT NULL DEFAULT (datetime('now')),
    reviewed_day TEXT NOT NULL DEFAULT ''
)`

// idxPoemPracticePoem serves ListByPoem/LatestByPoem.
var idxPoemPracticePoem = `CREATE INDEX IF NOT EXISTS idx_poem_practice_poem ON poem_practice_logs(poem_id)`

// PracticeRecord is ONE whole-poem attempt's result: the judged line counts
// (the lessons row's verdicts are the per-line detail; this row is the
// whole-poem SCOPE summary), the hint total across lines, and the mastery
// verdict (整首全对). ReviewedAt is optional (empty defers to the column
// DEFAULT); ReviewedDay is REQUIRED (Beijing "2006-01-02").
type PracticeRecord struct {
	LessonID     string
	ReviewedAt   string
	ReviewedDay  string
	PoemID       int64
	TotalLines   int
	CorrectLines int
	Hints        int
	Mastered     bool
}

// PracticeStore is the append-only write + per-poem read path for
// poem_practice_logs. Repository pattern mirrors poem.Store: nil-guarded
// constructor, NoOpLogger fallback, sentinel errors via errors.Is.
type PracticeStore struct {
	db     *sql.DB
	logger infra.Logger
}

// NewPracticeStore constructs a PracticeStore from an open *sql.DB (D-47
// nil-guard; nil logger degrades to the NoOp one).
func NewPracticeStore(db *sql.DB, logger infra.Logger) (*PracticeStore, error) {
	if db == nil {
		return nil, errors.Wrap(ErrInvalidPoem, "poem/practice_store: *sql.DB must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &PracticeStore{db: db, logger: logger}, nil
}

// RecordPractice writes ONE attempt row. The gate is strict on purpose: the
// row is the parent-facing mastery record, so a half-shaped row (poem 0,
// empty lesson, no day, out-of-range counts) rejects BEFORE anything is
// written rather than storing a lie. A duplicate submit is deduplicated
// UPSTREAM by the CompleteCheck stage latch (one submitted lesson → one
// save attempt), not by a unique index here — re-attempts of the SAME poem
// on another day are legitimate new rows.
func (s *PracticeStore) RecordPractice(ctx context.Context, r PracticeRecord) error {
	if r.PoemID <= 0 || r.LessonID == "" || r.ReviewedDay == "" {
		return errors.Wrap(ErrInvalidPoem, "record poem practice: poem_id, lesson_id and reviewed_day must be set")
	}
	if r.TotalLines <= 0 || r.CorrectLines < 0 || r.CorrectLines > r.TotalLines {
		return errors.Wrap(ErrInvalidPoem, "record poem practice: line counts out of range")
	}
	mastered := 0
	if r.Mastered {
		mastered = 1
	}
	var (
		res sql.Result
		err error
	)
	if r.ReviewedAt != "" {
		res, err = s.db.ExecContext(ctx,
			`INSERT INTO poem_practice_logs (poem_id, lesson_id, total_lines, correct_lines, hints, mastered, reviewed_at, reviewed_day)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.PoemID, r.LessonID, r.TotalLines, r.CorrectLines, r.Hints, mastered, r.ReviewedAt, r.ReviewedDay,
		)
	} else {
		res, err = s.db.ExecContext(ctx,
			`INSERT INTO poem_practice_logs (poem_id, lesson_id, total_lines, correct_lines, hints, mastered, reviewed_day)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.PoemID, r.LessonID, r.TotalLines, r.CorrectLines, r.Hints, mastered, r.ReviewedDay,
		)
	}
	if err != nil {
		s.logger.Error("poem practice record: insert failed",
			infra.F("component", "poem/practice_store"),
			infra.F("poem_id", r.PoemID),
			infra.F("lesson_id", r.LessonID),
			infra.ErrField(err),
		)
		return errors.Wrap(err, "record poem practice: insert")
	}
	if n, cerr := res.RowsAffected(); cerr == nil && n != 1 {
		return errors.Wrap(ErrInvalidPoem, fmt.Sprintf("record poem practice: unexpected affected rows %d", n))
	}
	return nil
}

// ListByPoem returns every attempt of one poem, first-recorded first. An
// unknown poem lists zero rows with no error (the poem may predate the
// table or never have been practiced — not a failure).
func (s *PracticeStore) ListByPoem(ctx context.Context, poemID int64) ([]PracticeRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT poem_id, lesson_id, total_lines, correct_lines, hints, mastered, reviewed_at, reviewed_day
		 FROM poem_practice_logs WHERE poem_id = ? ORDER BY id`, poemID)
	if err != nil {
		s.logger.Error("poem practice list failed",
			infra.F("component", "poem/practice_store"),
			infra.F("poem_id", poemID),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "list poem practice")
	}
	defer rows.Close()
	return scanPracticeRows(rows)
}

// LatestByPoem returns the poem's most recent attempt and whether one
// exists — the single mastery projection the parent records view (and the
// future poem scheduling) read. Absent is a legal answer, never an error.
func (s *PracticeStore) LatestByPoem(ctx context.Context, poemID int64) (PracticeRecord, bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT poem_id, lesson_id, total_lines, correct_lines, hints, mastered, reviewed_at, reviewed_day
		 FROM poem_practice_logs WHERE poem_id = ? ORDER BY id DESC LIMIT 1`, poemID)
	if err != nil {
		s.logger.Error("poem practice latest failed",
			infra.F("component", "poem/practice_store"),
			infra.F("poem_id", poemID),
			infra.ErrField(err),
		)
		return PracticeRecord{}, false, errors.Wrap(err, "latest poem practice")
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
// as the bool projection of the 0/1 column.
func scanPracticeRows(rows *sql.Rows) ([]PracticeRecord, error) {
	var records []PracticeRecord
	for rows.Next() {
		var (
			r        PracticeRecord
			mastered int
		)
		if err := rows.Scan(&r.PoemID, &r.LessonID, &r.TotalLines, &r.CorrectLines, &r.Hints, &mastered, &r.ReviewedAt, &r.ReviewedDay); err != nil {
			return nil, errors.Wrap(err, "scan poem practice")
		}
		r.Mastered = mastered != 0
		records = append(records, r)
	}
	return records, rows.Err()
}
