// passage_lesson_test.go — the Issue #14 passage 背默 lesson-row tests: the
// passage columns' additive migration, EnsurePassageLesson's range-shaped row
// (the SELECTED paragraphs ride as cards in the original's own order, the
// range identity travels with the attempt) and the LatestPassageLessonID
// resume probe keyed by content + range — never the scope row id.
package lesson

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// passageTexts is the fixture range: paragraphs 2 and 3 of a stored passage,
// in the original's own order (按所选段落的原文顺序).
var passageTexts = []string{
	"秋天的雨，有一盒五彩缤纷的颜料。",
	"秋天的雨，藏着非常好闻的气味。",
}

// TestEnsurePassageLesson — the 背默 attempt row: the selected paragraphs
// ride as words/cards in original order, the row carries the passage's own
// subject, and the FULL range identity (scope id + whole + canonical
// paragraphs) is stamped; INSERT OR IGNORE keeps first content.
func TestEnsurePassageLesson(t *testing.T) {
	db := newLessonTestDB(t)
	store, err := NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	l, err := store.EnsurePassageLesson(ctx, "r0w-20261019-080000", 21, 5, false, []int{3, 2}, passageTexts, CategoryChinese)
	if err != nil {
		t.Fatalf("EnsurePassageLesson: %v", err)
	}
	if l.PassageID != 21 || l.PassageScopeID != 5 || l.PassageWhole || l.Category != CategoryChinese {
		t.Fatalf("row = passage %d / scope %d / whole %v / category %q", l.PassageID, l.PassageScopeID, l.PassageWhole, l.Category)
	}
	if len(l.PassageParagraphs) != 2 || l.PassageParagraphs[0] != 2 || l.PassageParagraphs[1] != 3 {
		t.Fatalf("range = %v, want canonical [2 3]", l.PassageParagraphs)
	}
	if len(l.Cards) != len(passageTexts) {
		t.Fatalf("row carries %d cards, want %d paragraphs", len(l.Cards), len(passageTexts))
	}
	for i, text := range passageTexts {
		if l.Cards[i].Word != text || l.Words[i] != text {
			t.Errorf("card %d = %q/%q, want %q", i, l.Cards[i].Word, l.Words[i], text)
		}
	}

	// Same id with a different range: first content wins (INSERT OR IGNORE).
	if _, err = store.EnsurePassageLesson(ctx, "r0w-20261019-080000", 21, 5, false, []int{1, 2, 3}, passageTexts, CategoryChinese); err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	again, err := store.GetLesson(ctx, "r0w-20261019-080000")
	if err != nil {
		t.Fatalf("GetLesson re-ensure: %v", err)
	}
	if len(again.PassageParagraphs) != 2 || again.PassageParagraphs[0] != 2 {
		t.Fatalf("re-ensure range = %v, want the first-stored [2 3]", again.PassageParagraphs)
	}

	// Shape gates: nothing writes for an empty id, a missing passage, no
	// texts, an empty paragraph range, or a non-positive index.
	for _, bad := range []struct {
		name       string
		id         string
		paragraphs []int
		texts      []string
		passage    int64
		whole      bool
	}{
		{"empty id", "", []int{2}, passageTexts, 21, false},
		{"missing passage", "r0w-x", []int{2}, passageTexts, 0, false},
		{"no texts", "r0w-x", []int{2}, nil, 21, false},
		{"empty range", "r0w-x", nil, passageTexts, 21, false},
		{"negative index", "r0w-x", []int{0}, passageTexts, 21, false},
	} {
		if _, err = store.EnsurePassageLesson(ctx, bad.id, bad.passage, 5, bad.whole, bad.paragraphs, bad.texts, CategoryChinese); !errors.Is(err, ErrInvalidLesson) {
			t.Errorf("EnsurePassageLesson(%s) err = %v, want ErrInvalidLesson", bad.name, err)
		}
	}

	// The summary projection carries the range identity for the records view.
	summaries, err := store.ListLessons(ctx, 10)
	if err != nil {
		t.Fatalf("ListLessons: %v", err)
	}
	if len(summaries) != 1 || summaries[0].PassageID != 21 {
		t.Fatalf("summaries = %+v, want the passage row", summaries)
	}
	if summaries[0].PassageWhole || len(summaries[0].PassageParagraphs) != 2 {
		t.Fatalf("summary range = whole %v / %v, want [2 3]", summaries[0].PassageWhole, summaries[0].PassageParagraphs)
	}
}

// TestEnsurePassageLessonWholeRange — a whole attempt stores the canonical
// empty paragraph set with whole=1: the identity columns mirror the passage
// store's own whole-scope row shape.
func TestEnsurePassageLessonWholeRange(t *testing.T) {
	db := newLessonTestDB(t)
	store, _ := NewStore(db, infra.NewNoOpLogger())
	ctx := context.Background()

	wholeTexts := append([]string{"第一段。"}, passageTexts...)
	l, err := store.EnsurePassageLesson(ctx, "r0w-20261019-090000", 21, 6, true, nil, wholeTexts, CategoryChinese)
	if err != nil {
		t.Fatalf("EnsurePassageLesson whole: %v", err)
	}
	if !l.PassageWhole || len(l.PassageParagraphs) != 0 {
		t.Fatalf("whole row = whole %v / paragraphs %v", l.PassageWhole, l.PassageParagraphs)
	}

	// A whole range naming paragraphs is a contradiction: reject.
	if _, err = store.EnsurePassageLesson(ctx, "r0w-x", 21, 6, true, []int{1}, wholeTexts, CategoryChinese); err == nil {
		t.Fatal("whole + paragraphs must reject")
	}
}

// TestPassageColumnsMigration — a legacy lessons table predating the passage
// columns gains them on Migrate, and legacy rows read back the non-passage
// defaults (PassageID 0, empty set), never an error.
func TestPassageColumnsMigration(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "passagecol.db"))
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
    poem_id INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
)`
	if _, err = db.ExecContext(ctx, legacyDDL); err != nil {
		t.Fatalf("create legacy lessons: %v", err)
	}
	if _, err = db.ExecContext(ctx,
		`INSERT INTO lessons (lesson_id, words, cards, current_index) VALUES ('LEGACY-POEM', '["床前明月光"]', '[{"word":"床前明月光"}]', 1)`,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate legacy db: %v", err)
	}
	for _, col := range []string{"passage_id", "passage_scope_id", "passage_whole", "passage_paragraphs"} {
		has, cerr := lessonsHasColumn(ctx, db, col)
		if cerr != nil || !has {
			t.Fatalf("column %s after Migrate: has=%v err=%v", col, has, cerr)
		}
	}

	store, err := NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	legacy, err := store.GetLesson(ctx, "LEGACY-POEM")
	if err != nil {
		t.Fatalf("GetLesson legacy: %v", err)
	}
	if legacy.PassageID != 0 || legacy.PassageWhole || len(legacy.PassageParagraphs) != 0 {
		t.Errorf("legacy row passage fields = %d/%v/%v, want non-passage defaults", legacy.PassageID, legacy.PassageWhole, legacy.PassageParagraphs)
	}
}

// TestLatestPassageLessonID — the resume probe keys on content + range: a
// paragraph attempt does not surface as the whole range's attempt, a
// DIFFERENT paragraph set has its own probe result, and the scope row id is
// irrelevant to the lookup.
func TestLatestPassageLessonID(t *testing.T) {
	db := newLessonTestDB(t)
	store, err := NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	if id, lerr := store.LatestPassageLessonID(ctx, 21, false, []int{2, 3}); lerr != nil || id != "" {
		t.Fatalf("unpracticed probe = %q/%v, want \"\"/nil", id, lerr)
	}
	if _, err = store.LatestPassageLessonID(ctx, 0, false, []int{2}); !errors.Is(err, ErrInvalidLesson) {
		t.Errorf("probe(0) err = %v, want ErrInvalidLesson", err)
	}
	if _, err = store.LatestPassageLessonID(ctx, 21, false, nil); !errors.Is(err, ErrInvalidLesson) {
		t.Errorf("probe(empty range) err = %v, want ErrInvalidLesson", err)
	}

	// Two attempts of range 2,3 under DIFFERENT scope ids (a removed and
	// re-added scope), one attempt of the whole range, one of range 5.
	rows := []struct {
		id         string
		paragraphs []int
		scope      int64
		whole      bool
	}{
		{"r0w-20261019-080000", []int{2, 3}, 5, false},
		{"r0w-20261019-090000", []int{3, 2}, 9, false}, // canonicalized: same range
		{"r0w-20261019-093000", nil, 6, true},
		{"r0w-20261019-100000", []int{5}, 7, false},
	}
	for _, row := range rows {
		if _, err = store.EnsurePassageLesson(ctx, row.id, 21, row.scope, row.whole, row.paragraphs, passageTexts, CategoryChinese); err != nil {
			t.Fatalf("EnsurePassageLesson %s: %v", row.id, err)
		}
	}

	// created_at is second-resolution: the lesson_id DESC tiebreak picks the
	// newest attempt of the SAME range regardless of which scope id it rode.
	if id, lerr := store.LatestPassageLessonID(ctx, 21, false, []int{2, 3}); lerr != nil || id != "r0w-20261019-090000" {
		t.Errorf("probe(2,3) = %q/%v, want the 090000 attempt", id, lerr)
	}
	if id, lerr := store.LatestPassageLessonID(ctx, 21, true, nil); lerr != nil || id != "r0w-20261019-093000" {
		t.Errorf("probe(whole) = %q/%v, want the 093000 attempt", id, lerr)
	}
	if id, lerr := store.LatestPassageLessonID(ctx, 21, false, []int{5}); lerr != nil || id != "r0w-20261019-100000" {
		t.Errorf("probe(5) = %q/%v, want the 100000 attempt", id, lerr)
	}
	// A range that was never practiced (paragraph 1) stays absent — the
	// whole attempt never counts as a paragraph attempt (不伪造历史).
	if id, lerr := store.LatestPassageLessonID(ctx, 21, false, []int{1}); lerr != nil || id != "" {
		t.Errorf("probe(1) = %q/%v, want \"\"", id, lerr)
	}

	// A word/poem lesson never interferes with another passage's probe.
	if id, lerr := store.LatestPassageLessonID(ctx, 22, false, []int{2}); lerr != nil || id != "" {
		t.Errorf("probe(passage 22) = %q/%v, want \"\"", id, lerr)
	}
}

// TestPassageLessonCheckStageRoundtrip — the passage row rides the SAME
// check-stage machinery on paragraph indexes, and the range identity
// survives EnterCheck/ApplyVerdict/CompleteCheck verbatim.
func TestPassageLessonCheckStageRoundtrip(t *testing.T) {
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

	const lessonID = "r0w-20261019-110000"
	if _, err = store.EnsurePassageLesson(ctx, lessonID, 31, 5, false, []int{2, 3}, passageTexts, CategoryChinese); err != nil {
		t.Fatalf("EnsurePassageLesson: %v", err)
	}
	enter, err := exec.EnterCheck(ctx, lessonID, 1, lessonID+"-enter", map[string]WordOutcome{})
	if err != nil {
		t.Fatalf("EnterCheck: %v", err)
	}
	if _, err = exec.ApplyVerdict(ctx, lessonID, enter.NewVersion, "pv-0", CheckVerdict{WordIndex: 0, Correct: true, Manual: true}); err != nil {
		t.Fatalf("ApplyVerdict paragraph 0: %v", err)
	}
	v2, err := store.GetLesson(ctx, lessonID)
	if err != nil {
		t.Fatalf("GetLesson: %v", err)
	}
	if _, err = exec.ApplyVerdict(ctx, lessonID, v2.Version, "pv-1", CheckVerdict{WordIndex: 1, Correct: true, Manual: true}); err != nil {
		t.Fatalf("ApplyVerdict paragraph 1: %v", err)
	}
	v3, err := store.GetLesson(ctx, lessonID)
	if err != nil {
		t.Fatalf("GetLesson v3: %v", err)
	}
	if _, err = exec.CompleteCheck(ctx, lessonID, v3.Version, lessonID+"-submit", false); err != nil {
		t.Fatalf("CompleteCheck: %v", err)
	}
	final, err := store.GetLesson(ctx, lessonID)
	if err != nil {
		t.Fatalf("GetLesson final: %v", err)
	}
	if final.Stage != StageSubmitted || final.PassageID != 31 || len(final.Verdicts) != 2 {
		t.Fatalf("final row = stage %q / passage %d / %d verdicts", final.Stage, final.PassageID, len(final.Verdicts))
	}
	if final.PassageWhole || len(final.PassageParagraphs) != 2 || final.PassageParagraphs[0] != 2 {
		t.Fatalf("final range = whole %v / %v, want [2 3]", final.PassageWhole, final.PassageParagraphs)
	}
	sum := SummarizeLesson(final)
	if sum.Correct != 2 || !sum.Completed || sum.PassageID != 31 || sum.WordCount != 2 {
		t.Fatalf("summary = %+v, want 2 correct / completed / passage 31 / 2 paragraphs", sum)
	}
}
