// practice_store_test.go — the 13.1-05 practice_logs store tests (D-12):
// batch roundtrip through the Beijing-calendar day column, and the DDL's
// idempotent/legacy-safe migration inside lesson.Migrate.
package lesson

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// TestPracticeStoreRoundtrip: RecordPractice batch-writes one practice set
// and ListPracticeWordsOn(当日) returns every practiced word for that
// Beijing-calendar day — and does NOT leak words across days (the reviewed_day
// column is the 「当日」 authority, matching the r0-YYYYMMDD semantics).
func TestPracticeStoreRoundtrip(t *testing.T) {
	store, err := NewPracticeStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewPracticeStore: %v", err)
	}
	ctx := context.Background()

	batch := []PracticeRecord{
		{LessonID: "r0x-20261008-120000", Word: "apple", Rating: "good", Correct: true, Hints: 0, ReviewedDay: "2026-10-08"},
		{LessonID: "r0x-20261008-120000", Word: "banana", Rating: "again", Correct: false, Hints: 2, ReviewedDay: "2026-10-08"},
		{LessonID: "r0x-20261008-120000", Word: "猫", Rating: "hard", Correct: true, Hints: 1, ReviewedDay: "2026-10-08"},
	}
	if err = store.RecordPractice(ctx, batch); err != nil {
		t.Fatalf("RecordPractice: %v", err)
	}

	got, err := store.ListPracticeWordsOn(ctx, "2026-10-08")
	if err != nil {
		t.Fatalf("ListPracticeWordsOn today: %v", err)
	}
	if len(got) != 3 || got[0] != "apple" || got[1] != "banana" || got[2] != "猫" {
		t.Errorf("ListPracticeWordsOn(2026-10-08) = %v, want [apple banana 猫]", got)
	}

	// 跨日查询不串: another Beijing day sees none of today's practice words.
	other, err := store.ListPracticeWordsOn(ctx, "2026-10-09")
	if err != nil {
		t.Fatalf("ListPracticeWordsOn next day: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("ListPracticeWordsOn(2026-10-09) = %v, want empty (no cross-day leak)", other)
	}

	// reviewed_at defaults (UTC datetime('now')) — mappable to review_logs
	// (D-12). Only assert non-empty: the DB fills it.
	var reviewedAt string
	if err := store.db.QueryRowContext(ctx,
		"SELECT reviewed_at FROM practice_logs WHERE word = 'apple'",
	).Scan(&reviewedAt); err != nil {
		t.Fatalf("read reviewed_at: %v", err)
	}
	if reviewedAt == "" {
		t.Error("reviewed_at empty, want the datetime('now') default")
	}
}

// TestPracticeStoreExplicitReviewedAt: a caller-supplied ReviewedAt (e.g. a
// migration importing practice history) is stored verbatim.
func TestPracticeStoreExplicitReviewedAt(t *testing.T) {
	store, err := NewPracticeStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewPracticeStore: %v", err)
	}
	ctx := context.Background()

	if err := store.RecordPractice(ctx, []PracticeRecord{
		{LessonID: "r0x-import", Word: "dog", Rating: "good", Correct: true, ReviewedAt: "2026-10-01 08:00:00", ReviewedDay: "2026-10-01"},
	}); err != nil {
		t.Fatalf("RecordPractice: %v", err)
	}
	var reviewedAt string
	if err := store.db.QueryRowContext(ctx,
		"SELECT reviewed_at FROM practice_logs WHERE word = 'dog'",
	).Scan(&reviewedAt); err != nil {
		t.Fatalf("read reviewed_at: %v", err)
	}
	if reviewedAt != "2026-10-01 08:00:00" {
		t.Errorf("reviewed_at = %q, want the caller-supplied value", reviewedAt)
	}
}

// TestPracticeStoreInvalidArguments: invalid records are rejected without a
// partial write (the package's ErrInvalidLesson discipline).
func TestPracticeStoreInvalidArguments(t *testing.T) {
	store, err := NewPracticeStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewPracticeStore: %v", err)
	}
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		batch  []PracticeRecord
		reason string
	}{
		{"empty lesson id", []PracticeRecord{{Word: "apple", Rating: "good", ReviewedDay: "2026-10-08"}}, "empty lesson_id"},
		{"empty word", []PracticeRecord{{LessonID: "r0x-1", Rating: "good", ReviewedDay: "2026-10-08"}}, "empty word"},
		{"empty day", []PracticeRecord{{LessonID: "r0x-1", Word: "apple", Rating: "good"}}, "empty reviewed_day"},
	} {
		if err := store.RecordPractice(ctx, tc.batch); err == nil {
			t.Errorf("%s: RecordPractice error = nil, want ErrInvalidLesson (%s)", tc.name, tc.reason)
		} else if !errors.Is(err, ErrInvalidLesson) {
			t.Errorf("%s: error = %v, want ErrInvalidLesson wrap", tc.name, err)
		}
	}

	// Nothing landed.
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM practice_logs").Scan(&count); err != nil {
		t.Fatalf("count practice_logs: %v", err)
	}
	if count != 0 {
		t.Errorf("practice_logs rows = %d, want 0 after rejected batches", count)
	}

	// An empty batch is a legal no-op, not an error.
	if err := store.RecordPractice(ctx, nil); err != nil {
		t.Errorf("empty batch error = %v, want nil no-op", err)
	}
}

// TestListPracticeByLesson (13.3-02 behavior 3): the per-lesson practice
// query behind the parent lesson detail — every practice_logs row of ONE
// lesson in id (insert) order with the full record shape, and ONLY that
// lesson's rows. An unknown lesson lists zero rows with no error.
func TestListPracticeByLesson(t *testing.T) {
	store, err := NewPracticeStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewPracticeStore: %v", err)
	}
	ctx := context.Background()

	batch := []PracticeRecord{
		{LessonID: "r0x-20261008-120000", Word: "apple", Rating: "good", Correct: true, Hints: 0, ReviewedDay: "2026-10-08"},
		{LessonID: "r0x-20261008-120000", Word: "banana", Rating: "again", Correct: false, Hints: 2, ReviewedDay: "2026-10-08"},
		{LessonID: "r0x-20261009-090000", Word: "猫", Rating: "hard", Correct: true, Hints: 1, ReviewedDay: "2026-10-09"},
	}
	if err = store.RecordPractice(ctx, batch); err != nil {
		t.Fatalf("RecordPractice: %v", err)
	}

	got, err := store.ListPracticeByLesson(ctx, "r0x-20261008-120000")
	if err != nil {
		t.Fatalf("ListPracticeByLesson: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListPracticeByLesson rows = %d, want 2 (only the requested lesson)", len(got))
	}
	first, second := got[0], got[1]
	if first.Word != "apple" || second.Word != "banana" {
		t.Errorf("rows = [%s %s], want [apple banana] (id ASC insert order)", first.Word, second.Word)
	}
	if first.LessonID != "r0x-20261008-120000" || first.Rating != "good" || !first.Correct || first.Hints != 0 {
		t.Errorf("first row = %+v, want the seeded apple record", first)
	}
	if second.Rating != "again" || second.Correct || second.Hints != 2 {
		t.Errorf("second row = %+v, want the seeded banana record", second)
	}
	if first.ReviewedDay != "2026-10-08" || first.ReviewedAt == "" {
		t.Errorf("first row day/at = %q/%q, want seeded day + datetime('now') default", first.ReviewedDay, first.ReviewedAt)
	}

	none, err := store.ListPracticeByLesson(ctx, "r0x-unknown")
	if err != nil {
		t.Fatalf("ListPracticeByLesson unknown lesson: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ListPracticeByLesson(unknown) rows = %d, want 0", len(none))
	}
}

// TestPracticeMigrateIdempotent: lesson.Migrate creates practice_logs on a
// legacy database (lessons exists, practice_logs does not), two consecutive
// runs stay idempotent, and the reviewed_day column exists for the
// 「当日」 queries.
func TestPracticeMigrateIdempotent(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "legacy-practice.db"))
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	// A pre-13.1-05 database: only the lessons table exists.
	legacyDDL := `CREATE TABLE IF NOT EXISTS lessons (
    lesson_id TEXT PRIMARY KEY,
    words TEXT NOT NULL,
    current_index INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1
)`
	if _, err = db.ExecContext(ctx, legacyDDL); err != nil {
		t.Fatalf("create legacy lessons: %v", err)
	}

	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("first Migrate on legacy db: %v", err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate on legacy db: %v", err)
	}

	var count int
	if err = db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='practice_logs'",
	).Scan(&count); err != nil {
		t.Fatalf("count practice_logs: %v", err)
	}
	if count != 1 {
		t.Errorf("practice_logs table count = %d, want 1 (idempotent)", count)
	}

	hasDay, err := practiceHasColumn(ctx, db, "reviewed_day")
	if err != nil {
		t.Fatalf("probe reviewed_day: %v", err)
	}
	if !hasDay {
		t.Error("reviewed_day column missing from practice_logs")
	}
}
