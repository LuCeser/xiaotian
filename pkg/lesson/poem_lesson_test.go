// poem_lesson_test.go — the Issue #12 whole-poem 背默 lesson-row tests: the
// poem_id column's additive migration, EnsurePoemLesson's one-line-one-card
// shape (subject always 语文, first content wins) and the LatestPoemLessonID
// resume probe.
package lesson

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// TestEnsurePoemLesson — the 背默 attempt row: ordered lines ride as
// words/cards (one line = one card), the subject is always 语文 and
// poem_id stamps the practiced work; INSERT OR IGNORE keeps first content.
func TestEnsurePoemLesson(t *testing.T) {
	db := newLessonTestDB(t)
	store, err := NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	lines := []string{"床前明月光", "疑是地上霜", "举头望明月", "低头思故乡"}
	l, err := store.EnsurePoemLesson(ctx, "r0p-20261018-080000", 7, lines)
	if err != nil {
		t.Fatalf("EnsurePoemLesson: %v", err)
	}
	if l.PoemID != 7 || l.Category != CategoryChinese {
		t.Fatalf("row = poem %d / category %q, want 7 / chinese", l.PoemID, l.Category)
	}
	if len(l.Cards) != len(lines) || len(l.Words) != len(lines) {
		t.Fatalf("row carries %d cards / %d words, want %d ordered lines", len(l.Cards), len(l.Words), len(lines))
	}
	for i, line := range lines {
		if l.Cards[i].Word != line || l.Words[i] != line {
			t.Errorf("line %d = %q/%q, want %q in order", i, l.Cards[i].Word, l.Words[i], line)
		}
	}
	if l.CurrentIndex != 0 || l.Stage != "" {
		t.Errorf("fresh attempt currentIndex/stage = %d/%q, want 0/\"\"", l.CurrentIndex, l.Stage)
	}

	// Same id with different lines: first content wins (INSERT OR IGNORE).
	if _, err = store.EnsurePoemLesson(ctx, "r0p-20261018-080000", 7, []string{"完全不同的诗"}); err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	again, err := store.GetLesson(ctx, "r0p-20261018-080000")
	if err != nil {
		t.Fatalf("GetLesson re-ensure: %v", err)
	}
	if len(again.Cards) != 4 || again.Cards[0].Word != lines[0] {
		t.Errorf("re-ensure row = %d cards starting %q, want the first-stored 4 lines", len(again.Cards), again.Cards[0].Word)
	}

	// Shape gate: nothing writes for an empty id, a missing poem or no lines.
	for _, bad := range []struct {
		id    string
		lines []string
		poem  int64
	}{
		{id: "", poem: 7, lines: lines},
		{id: "r0p-x", poem: 0, lines: lines},
		{id: "r0p-x", poem: 7, lines: nil},
	} {
		if _, err = store.EnsurePoemLesson(ctx, bad.id, bad.poem, bad.lines); !errors.Is(err, ErrInvalidLesson) {
			t.Errorf("EnsurePoemLesson(%q,%d,%d lines) err = %v, want ErrInvalidLesson", bad.id, bad.poem, len(bad.lines), err)
		}
	}

	// The summary projection carries the join key for the records view.
	summaries, err := store.ListLessons(ctx, 10)
	if err != nil {
		t.Fatalf("ListLessons: %v", err)
	}
	if len(summaries) != 1 || summaries[0].PoemID != 7 || summaries[0].Category != CategoryChinese {
		t.Fatalf("summaries = %+v, want the poem row with PoemID 7 / chinese", summaries)
	}
}

// TestPoemIDColumnMigration — a legacy lessons table predating poem_id gains
// the column on Migrate, and legacy rows read back PoemID=0 (non-poem rows
// keep their meaning), never an error.
func TestPoemIDColumnMigration(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "poemcol.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	legacyDDL := `CREATE TABLE IF NOT EXISTS lessons (
    lesson_id TEXT PRIMARY KEY,
    words TEXT NOT NULL,
    cards TEXT NOT NULL DEFAULT '[]',
    current_index INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    stage TEXT NOT NULL DEFAULT '',
    outcomes TEXT NOT NULL DEFAULT '{}',
    verdicts TEXT NOT NULL DEFAULT '[]',
    unchecked INTEGER NOT NULL DEFAULT 0,
    category TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
)`
	if _, err = db.ExecContext(ctx, legacyDDL); err != nil {
		t.Fatalf("create legacy lessons: %v", err)
	}
	if _, err = db.ExecContext(ctx,
		`INSERT INTO lessons (lesson_id, words, cards, current_index) VALUES ('LEGACY-WORD', '["cat"]', '[{"word":"cat"}]', 1)`,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate legacy db: %v", err)
	}
	has, err := lessonsHasColumn(ctx, db, "poem_id")
	if err != nil || !has {
		t.Fatalf("poem_id column after Migrate: has=%v err=%v", has, err)
	}

	store, err := NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	legacy, err := store.GetLesson(ctx, "LEGACY-WORD")
	if err != nil {
		t.Fatalf("GetLesson legacy: %v", err)
	}
	if legacy.PoemID != 0 {
		t.Errorf("legacy row PoemID = %d, want 0 (a word lesson)", legacy.PoemID)
	}
	summaries, err := store.ListLessons(ctx, 10)
	if err != nil {
		t.Fatalf("ListLessons: %v", err)
	}
	if len(summaries) != 1 || summaries[0].PoemID != 0 {
		t.Fatalf("summaries = %+v, want the legacy row with PoemID 0", summaries)
	}
}

// TestLatestPoemLessonID — the resume probe: "" for a never-practiced poem,
// the newest attempt's id once rows exist (same ORDER BY shape as
// ListLessons), and the shape gate rejects a non-positive poem id.
func TestLatestPoemLessonID(t *testing.T) {
	db := newLessonTestDB(t)
	store, err := NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	if id, lerr := store.LatestPoemLessonID(ctx, 3); lerr != nil || id != "" {
		t.Fatalf("LatestPoemLessonID unpracticed = %q/%v, want \"\"/nil", id, lerr)
	}
	if _, err = store.LatestPoemLessonID(ctx, 0); !errors.Is(err, ErrInvalidLesson) {
		t.Errorf("LatestPoemLessonID(0) err = %v, want ErrInvalidLesson", err)
	}

	// Two attempts of poem 3 and one of poem 4; created_at is stamped by
	// SQLite datetime('now') (second resolution), so the same-second
	// tiebreak must fall to lesson_id DESC — the r0p- id encodes the
	// attempt time, exactly the ordering the probe needs.
	for _, row := range []struct {
		id   string
		poem int64
	}{{"r0p-20261018-080000", 3}, {"r0p-20261018-090000", 3}, {"r0p-20261018-093000", 4}} {
		if _, err = store.EnsurePoemLesson(ctx, row.id, row.poem, []string{"一行诗"}); err != nil {
			t.Fatalf("EnsurePoemLesson %s: %v", row.id, err)
		}
	}
	if id, lerr := store.LatestPoemLessonID(ctx, 3); lerr != nil || id != "r0p-20261018-090000" {
		t.Errorf("LatestPoemLessonID(3) = %q/%v, want the 090000 attempt", id, lerr)
	}
	if id, lerr := store.LatestPoemLessonID(ctx, 4); lerr != nil || id != "r0p-20261018-093000" {
		t.Errorf("LatestPoemLessonID(4) = %q/%v, want the 093000 attempt", id, lerr)
	}

	// A word lesson never interferes: poem 5 has no rows even though other
	// lessons exist.
	var one int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM lessons").Scan(&one); err != nil || one != 3 {
		t.Fatalf("lessons count = %d/%v, want 3", one, err)
	}
	if id, err := store.LatestPoemLessonID(ctx, 5); err != nil || id != "" {
		t.Errorf("LatestPoemLessonID(5) = %q/%v, want \"\"", id, err)
	}
}

// TestPoemLessonCheckStageRoundtrip — the poem row rides the SAME check-stage
// machinery: EnterCheck/ApplyVerdict/CompleteCheck operate on line indexes
// exactly as they do on word indexes, and PoemID survives the whole path
// (the parent projection joins it after submit).
func TestPoemLessonCheckStageRoundtrip(t *testing.T) {
	db := newLessonTestDB(t)
	store, err := NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	exec, err := NewExecutor(store, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	ctx := context.Background()

	const lessonID = "r0p-20261018-110000"
	if _, err = store.EnsurePoemLesson(ctx, lessonID, 11, []string{"春眠不觉晓", "处处闻啼鸟"}); err != nil {
		t.Fatalf("EnsurePoemLesson: %v", err)
	}

	enter, err := exec.EnterCheck(ctx, lessonID, 1, lessonID+"-enter", map[string]WordOutcome{})
	if err != nil {
		t.Fatalf("EnterCheck: %v", err)
	}
	if _, err = exec.ApplyVerdict(ctx, lessonID, enter.NewVersion, "rv-0", CheckVerdict{WordIndex: 0, Correct: true, Manual: true}); err != nil {
		t.Fatalf("ApplyVerdict line 0: %v", err)
	}
	v2, err := store.GetLesson(ctx, lessonID)
	if err != nil {
		t.Fatalf("GetLesson after verdict: %v", err)
	}
	if _, err = exec.ApplyVerdict(ctx, lessonID, v2.Version, "rv-1", CheckVerdict{WordIndex: 1, Correct: false, Manual: true}); err != nil {
		t.Fatalf("ApplyVerdict line 1: %v", err)
	}
	v3, err := store.GetLesson(ctx, lessonID)
	if err != nil {
		t.Fatalf("GetLesson after verdict 2: %v", err)
	}
	if _, err = exec.CompleteCheck(ctx, lessonID, v3.Version, lessonID+"-submit", false); err != nil {
		t.Fatalf("CompleteCheck: %v", err)
	}
	final, err := store.GetLesson(ctx, lessonID)
	if err != nil {
		t.Fatalf("GetLesson final: %v", err)
	}
	if final.Stage != StageSubmitted || final.PoemID != 11 || len(final.Verdicts) != 2 {
		t.Fatalf("final row = stage %q / poem %d / %d verdicts, want submitted/11/2", final.Stage, final.PoemID, len(final.Verdicts))
	}
	sum := SummarizeLesson(final)
	if sum.Correct != 1 || sum.Wrong != 1 || !sum.Completed || sum.PoemID != 11 {
		t.Fatalf("summary = %+v, want 1 correct / 1 wrong / completed / poem 11", sum)
	}
}
