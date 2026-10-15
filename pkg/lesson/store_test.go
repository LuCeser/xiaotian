package lesson

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// newLessonTestDB opens a temp SQLite database via database.Open (production
// DSN pragmas: WAL, busy_timeout, foreign_keys) and runs lesson.Migrate.
func newLessonTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "lesson.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func TestStoreMigrateIdempotent(t *testing.T) {
	db := newLessonTestDB(t)
	ctx := context.Background()

	// Second call must succeed and leave exactly one of each object.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}

	for _, name := range []string{"lessons", "tool_requests"} {
		var count int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name,
		).Scan(&count); err != nil {
			t.Fatalf("count table %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("table %s count = %d, want 1", name, count)
		}
	}

	var idxCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_tool_requests_lesson'",
	).Scan(&idxCount); err != nil {
		t.Fatalf("count index: %v", err)
	}
	if idxCount != 1 {
		t.Errorf("index idx_tool_requests_lesson count = %d, want 1", idxCount)
	}
}

func TestStoreEnsureLesson(t *testing.T) {
	store, err := NewStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	t.Run("first create returns version 1 index 0", func(t *testing.T) {
		got, err := store.EnsureLesson(ctx, "L1", []string{"猫", "狗", "鸟"})
		if err != nil {
			t.Fatalf("EnsureLesson: %v", err)
		}
		if got.LessonID != "L1" {
			t.Errorf("LessonID = %q, want %q", got.LessonID, "L1")
		}
		if got.Version != 1 {
			t.Errorf("Version = %d, want 1", got.Version)
		}
		if got.CurrentIndex != 0 {
			t.Errorf("CurrentIndex = %d, want 0", got.CurrentIndex)
		}
		if len(got.Words) != 3 || got.Words[0] != "猫" {
			t.Errorf("Words = %v, want [猫 狗 鸟]", got.Words)
		}
	})

	t.Run("same id different words keeps first content", func(t *testing.T) {
		got, err := store.EnsureLesson(ctx, "L1", []string{"别的词"})
		if err != nil {
			t.Fatalf("EnsureLesson second call: %v", err)
		}
		if len(got.Words) != 3 || got.Words[0] != "猫" {
			t.Errorf("Words after IGNORE = %v, want first-stored [猫 狗 鸟]", got.Words)
		}
		if got.Version != 1 {
			t.Errorf("Version after IGNORE = %d, want 1", got.Version)
		}
	})

	t.Run("invalid arguments rejected without write", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			lessonID string
			words    []string
		}{
			{"empty lesson id", "", []string{"猫"}},
			{"empty words", "L2", nil},
		} {
			_, err := store.EnsureLesson(ctx, tc.lessonID, tc.words)
			if !errors.Is(err, ErrInvalidLesson) {
				t.Errorf("%s: error = %v, want ErrInvalidLesson", tc.name, err)
			}
		}

		// Nothing was written by the rejected calls: only L1 exists.
		var count int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM lessons",
		).Scan(&count); err != nil {
			t.Fatalf("count lessons: %v", err)
		}
		if count != 1 {
			t.Errorf("lessons row count = %d, want 1 (only the first-stored L1)", count)
		}
	})
}

func TestStoreGetLessonNotFound(t *testing.T) {
	store, err := NewStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = store.GetLesson(context.Background(), "no-such-lesson")
	if !errors.Is(err, ErrLessonNotFound) {
		t.Fatalf("GetLesson unknown id error = %v, want ErrLessonNotFound", err)
	}
}

func TestStoreNewStoreGuards(t *testing.T) {
	db := newLessonTestDB(t)

	if _, err := NewStore(nil, infra.NewNoOpLogger()); err == nil {
		t.Error("NewStore(nil, logger) expected error, got nil")
	}

	// nil logger is substituted with NoOpLogger — constructor must not panic
	// and subsequent reads must work.
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore(db, nil) error = %v", err)
	}
	if store.logger == nil {
		t.Error("NewStore(db, nil) left logger nil, want NoOpLogger substitute")
	}
}

func TestEnsureLessonCards(t *testing.T) {
	store, err := NewStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	first := []Word{
		{Word: "猫", Meaning: "cat", Example: "我家的猫很小。"},
		{Word: "狗", Meaning: "dog", Example: "那只狗在跑。"},
	}

	t.Run("roundtrip stores words and full cards", func(t *testing.T) {
		got, err := store.EnsureLessonCards(ctx, "LC1", first)
		if err != nil {
			t.Fatalf("EnsureLessonCards: %v", err)
		}
		if got.Version != 1 || got.CurrentIndex != 0 {
			t.Errorf("Version = %d, CurrentIndex = %d, want 1 / 0", got.Version, got.CurrentIndex)
		}
		if len(got.Words) != 2 || got.Words[0] != "猫" || got.Words[1] != "狗" {
			t.Errorf("Words = %v, want [猫 狗] (extracted from cards)", got.Words)
		}
		if len(got.Cards) != 2 {
			t.Fatalf("Cards len = %d, want 2", len(got.Cards))
		}
		if got.Cards[0] != (Word{Word: "猫", Meaning: "cat", Example: "我家的猫很小。"}) {
			t.Errorf("Cards[0] = %+v, want full first card", got.Cards[0])
		}
	})

	t.Run("same id different cards keeps first content", func(t *testing.T) {
		got, err := store.EnsureLessonCards(ctx, "LC1", []Word{
			{Word: "别的", Meaning: "other", Example: "别的句子。"},
		})
		if err != nil {
			t.Fatalf("EnsureLessonCards second call: %v", err)
		}
		if len(got.Cards) != 2 || got.Cards[0].Word != "猫" {
			t.Errorf("Cards after IGNORE = %+v, want first-stored 猫/狗 cards", got.Cards)
		}
		if got.Version != 1 {
			t.Errorf("Version after IGNORE = %d, want 1", got.Version)
		}
	})

	t.Run("invalid arguments rejected without write", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			lessonID string
			cards    []Word
		}{
			{"empty lesson id", "", first},
			{"empty cards", "LC-EMPTY", nil},
		} {
			_, err := store.EnsureLessonCards(ctx, tc.lessonID, tc.cards)
			if !errors.Is(err, ErrInvalidLesson) {
				t.Errorf("%s: error = %v, want ErrInvalidLesson", tc.name, err)
			}
		}
	})
}

// TestMigrateAddsCardsColumnToLegacyTable simulates a lessons table created
// before the cards column existed (13-06-era schema), seeds one row, then
// runs Migrate: the column must be backfilled idempotently and the legacy
// row must read back intact with an empty Cards slice.
func TestMigrateAddsCardsColumnToLegacyTable(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	legacyDDL := `CREATE TABLE IF NOT EXISTS lessons (
    lesson_id TEXT PRIMARY KEY,
    words TEXT NOT NULL,
    current_index INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
)`
	if _, err = db.ExecContext(ctx, legacyDDL); err != nil {
		t.Fatalf("create legacy lessons: %v", err)
	}
	if _, err = db.ExecContext(ctx,
		"INSERT INTO lessons (lesson_id, words) VALUES ('LEGACY-1', ?)",
		`["旧词","第二词"]`,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate on legacy db: %v", err)
	}
	// Second run must stay idempotent (probe finds the column, no ALTER).
	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate on legacy db: %v", err)
	}

	hasCards, err := lessonsHasColumn(ctx, db, "cards")
	if err != nil {
		t.Fatalf("probe cards column: %v", err)
	}
	if !hasCards {
		t.Fatal("cards column missing after Migrate on legacy table")
	}

	store, err := NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	got, err := store.GetLesson(ctx, "LEGACY-1")
	if err != nil {
		t.Fatalf("GetLesson legacy row: %v", err)
	}
	if len(got.Words) != 2 || got.Words[0] != "旧词" {
		t.Errorf("legacy Words = %v, want [旧词 第二词] (intact)", got.Words)
	}
	if got.Cards == nil || len(got.Cards) != 0 {
		t.Errorf("legacy Cards = %#v, want empty slice (cards default '[]')", got.Cards)
	}
}

// TestEnsureLessonCardsDoesNotReplaceLegacyEmptyCards proves INSERT OR
// IGNORE is not a replacement mechanism: a pre-existing row whose cards
// column is empty (the legacy-migration state) is NOT backfilled by a later
// EnsureLessonCards call — 13-08 must detect that state explicitly instead
// of mistaking the ignore for a successful card write.
func TestEnsureLessonCardsDoesNotReplaceLegacyEmptyCards(t *testing.T) {
	store, err := NewStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	// Seed a row the way a pre-cards writer would have: words only, cards
	// stays at the DDL default '[]'.
	if _, err = store.db.ExecContext(ctx,
		"INSERT INTO lessons (lesson_id, words) VALUES ('L-EMPTY', ?)",
		`["旧词"]`,
	); err != nil {
		t.Fatalf("seed empty-cards row: %v", err)
	}

	got, err := store.EnsureLessonCards(ctx, "L-EMPTY", []Word{
		{Word: "新词", Meaning: "new", Example: "新例句。"},
	})
	if err != nil {
		t.Fatalf("EnsureLessonCards over empty-cards row: %v", err)
	}
	if len(got.Cards) != 0 {
		t.Errorf("Cards = %+v, want still empty (INSERT OR IGNORE must not replace)", got.Cards)
	}
	if len(got.Words) != 1 || got.Words[0] != "旧词" {
		t.Errorf("Words = %v, want first-stored [旧词] (unreplaced)", got.Words)
	}
}

// TestGetLessonCorruptedCardsJSON asserts a corrupted cards column surfaces
// as a wrapped unmarshal error (never a panic), mirroring the words-column
// error shape.
func TestGetLessonCorruptedCardsJSON(t *testing.T) {
	store, err := NewStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	if _, err = store.db.ExecContext(ctx,
		"INSERT INTO lessons (lesson_id, words, cards) VALUES ('L-BAD', ?, ?)",
		`["词"]`,
		`{not valid json`,
	); err != nil {
		t.Fatalf("seed corrupted-cards row: %v", err)
	}

	_, err = store.GetLesson(ctx, "L-BAD")
	if err == nil {
		t.Fatal("GetLesson corrupted cards expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unmarshal cards") {
		t.Errorf("GetLesson corrupted cards error = %v, want wrapped unmarshal-cards failure", err)
	}
}

// TestStoreMigrateAddsStageColumns (13.1-01): a lessons table from before the
// check stage (13-06-era schema: cards exists, the four 13.1 columns do not)
// gains stage/outcomes/verdicts/unchecked idempotently — historical rows keep
// their short-circuit defaults (stage=”/outcomes='{}'/verdicts='[]'/unchecked=0,
// the values startCompletedSession relies on), two consecutive Migrate runs
// stay no-ops, and fresh rows through the store default cleanly.
func TestStoreMigrateAddsStageColumns(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "legacy-stage.db"))
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	legacyDDL := `CREATE TABLE IF NOT EXISTS lessons (
    lesson_id TEXT PRIMARY KEY,
    words TEXT NOT NULL,
    cards TEXT NOT NULL DEFAULT '[]',
    current_index INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
)`
	if _, err = db.ExecContext(ctx, legacyDDL); err != nil {
		t.Fatalf("create legacy lessons: %v", err)
	}
	// A completed historical lesson row (the startCompletedSession case).
	if _, err = db.ExecContext(ctx,
		"INSERT INTO lessons (lesson_id, words, cards, current_index, version) VALUES ('LEGACY-DONE', ?, '[]', 1, 2)",
		`["旧词"]`,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate on legacy db: %v", err)
	}
	// Second run must stay idempotent (probe finds every column, no ALTER).
	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate on legacy db: %v", err)
	}

	for _, col := range []string{"stage", "outcomes", "verdicts", "unchecked"} {
		has, colErr := lessonsHasColumn(ctx, db, col)
		if colErr != nil {
			t.Fatalf("probe %s column: %v", col, colErr)
		}
		if !has {
			t.Errorf("%s column missing after Migrate on legacy table", col)
		}
	}

	var stage, outcomes, verdicts string
	var unchecked int
	if err = db.QueryRowContext(ctx,
		"SELECT stage, outcomes, verdicts, unchecked FROM lessons WHERE lesson_id = 'LEGACY-DONE'",
	).Scan(&stage, &outcomes, &verdicts, &unchecked); err != nil {
		t.Fatalf("read legacy row defaults: %v", err)
	}
	if stage != "" || outcomes != "{}" || verdicts != "[]" || unchecked != 0 {
		t.Errorf("legacy row defaults = stage %q / outcomes %q / verdicts %q / unchecked %d, want '' / '{}' / '[]' / 0",
			stage, outcomes, verdicts, unchecked)
	}

	// The historical row reads back through the store with legacy-default
	// tolerance (empty Stage / empty collections / Unchecked=false) — the
	// completed-session short circuit keeps working.
	store, err := NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	got, err := store.GetLesson(ctx, "LEGACY-DONE")
	if err != nil {
		t.Fatalf("GetLesson legacy row: %v", err)
	}
	if got.Stage != "" || len(got.Outcomes) != 0 || len(got.Verdicts) != 0 || got.Unchecked {
		t.Errorf("legacy read-back = stage %q / outcomes %+v / verdicts %+v / unchecked %v, want defaults",
			got.Stage, got.Outcomes, got.Verdicts, got.Unchecked)
	}

	// Fresh rows created after the migration default cleanly too.
	fresh, err := store.EnsureLessonCards(ctx, "FRESH", []Word{{Word: "新词", Meaning: "new", Example: "新例句。"}})
	if err != nil {
		t.Fatalf("EnsureLessonCards fresh: %v", err)
	}
	if fresh.Stage != "" || len(fresh.Outcomes) != 0 || len(fresh.Verdicts) != 0 || fresh.Unchecked {
		t.Errorf("fresh read-back = %+v, want check-stage defaults", fresh)
	}
}

// TestLessonCompleted (13.3-02 behavior 1) — the completion whitelist for the
// parent-facing read projection, enumerated branch-by-branch against the
// authoritative completion branch table (pkg/rt/turn.go lesson-init switch):
// ONLY stage=submitted (the unified submission migrated the row) or a legacy
// row (stage=” whose cursor walked past the last card) counts as completed.
// checking (check panel open — words are on the student's screen), the
// in-progress default branch, and the empty-cards legacy unavailable row are
// ALL incomplete. Unchecked is deliberately NOT an input: the fallback also
// lands at stage=submitted, so it only drives a display marker.
func TestLessonCompleted(t *testing.T) {
	tests := []struct {
		name         string
		stage        string
		currentIndex int
		cardLen      int
		want         bool
	}{
		{"empty stage in progress", "", 1, 3, false},
		{"checking never completed", StageChecking, 3, 3, false},
		{"submitted completed", StageSubmitted, 3, 3, true},
		{"submitted with unchecked fallback still completed", StageSubmitted, 3, 3, true},
		{"legacy completed by cursor", "", 3, 3, true},
		{"legacy cursor past cards completed", "", 5, 3, true},
		{"legacy empty cards unavailable", "", 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LessonCompleted(tc.stage, tc.currentIndex, tc.cardLen); got != tc.want {
				t.Errorf("LessonCompleted(stage=%q, currentIndex=%d, cardLen=%d) = %v, want %v",
					tc.stage, tc.currentIndex, tc.cardLen, got, tc.want)
			}
		})
	}
}

// TestListLessons (13.3-02 behavior 2) — the recent-lessons projection:
// created_at DESC (RESEARCH A4: same-day r0x- practice rows sort by timestamp,
// not lesson-id lexicographic order), both r0- and r0x- rows included, and
// the per-row aggregates honest: WordCount from the cards JSON, Correct/Wrong
// counting ONLY manual verdicts (完成勾≠写对), Unconfirmed = the remainder.
func TestListLessons(t *testing.T) {
	store, err := NewStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	seed := []struct {
		lessonID   string
		cards      string
		verdicts   string
		stage      string
		currentIdx int
		unchecked  int
		createdAt  string
	}{
		{"r0-20261010", `[{"word":"苹果","meaning":"apple","example":"s1"},{"word":"香蕉","meaning":"banana","example":"s2"},{"word":"橘子","meaning":"orange","example":"s3"}]`,
			`[{"word_index":0,"correct":true,"manual":true},{"word_index":1,"correct":false,"manual":true},{"word_index":2,"correct":true,"manual":false}]`,
			StageSubmitted, 3, 0, "2026-10-10 08:00:00"},
		{"r0-20261009", `[{"word":"猫","meaning":"cat","example":"s1"},{"word":"狗","meaning":"dog","example":"s2"},{"word":"鸟","meaning":"bird","example":"s3"}]`,
			`[]`, StageChecking, 1, 0, "2026-10-09 09:00:00"},
		{"r0x-20261008-120000", `[{"word":"apple","meaning":"苹果","example":"s1"},{"word":"banana","meaning":"香蕉","example":"s2"}]`,
			`[]`, "", 2, 0, "2026-10-08 12:00:00"},
		{"r0-20261008", `[{"word":"猫","meaning":"cat","example":"s1"},{"word":"狗","meaning":"dog","example":"s2"}]`,
			`[]`, "", 0, 0, "2026-10-08 10:00:00"},
		{"r0-20261007", `[{"word":"太阳","meaning":"sun","example":"s1"},{"word":"月亮","meaning":"moon","example":"s2"}]`,
			`[]`, StageSubmitted, 2, 1, "2026-10-07 10:00:00"},
	}
	for _, s := range seed {
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO lessons (lesson_id, words, cards, verdicts, stage, current_index, unchecked, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			s.lessonID, `["词"]`, s.cards, s.verdicts, s.stage, s.currentIdx, s.unchecked, s.createdAt, s.createdAt,
		); err != nil {
			t.Fatalf("seed %s: %v", s.lessonID, err)
		}
	}

	// fetch runs ListLessons and fails the calling subtest unless at least n
	// rows came back (keeps every subtest panic-free on a short read).
	fetch := func(t *testing.T, n int) []LessonSummary {
		t.Helper()
		got, err := store.ListLessons(ctx, 20)
		if err != nil {
			t.Fatalf("ListLessons: %v", err)
		}
		if len(got) < n {
			t.Fatalf("ListLessons rows = %d, want >= %d", len(got), n)
		}
		return got
	}

	t.Run("returns newest first including r0x- practice rows", func(t *testing.T) {
		got := fetch(t, 5)
		if len(got) != 5 {
			t.Fatalf("ListLessons rows = %d, want 5", len(got))
		}
		wantOrder := []string{"r0-20261010", "r0-20261009", "r0x-20261008-120000", "r0-20261008", "r0-20261007"}
		for i, wantID := range wantOrder {
			if got[i].LessonID != wantID {
				t.Errorf("row %d = %s, want %s (created_at DESC)", i, got[i].LessonID, wantID)
			}
		}
	})

	t.Run("submitted row aggregates manual verdicts only", func(t *testing.T) {
		got := fetch(t, 1)
		r := got[0] // r0-20261010
		if !r.Completed {
			t.Errorf("Completed = false, want true (stage=submitted)")
		}
		if r.WordCount != 3 {
			t.Errorf("WordCount = %d, want 3 (from cards JSON)", r.WordCount)
		}
		// 完成勾≠写对: only Manual=true verdicts count; the manual=false OCR
		// prefill lands in Unconfirmed, never in Correct.
		if r.Correct != 1 || r.Wrong != 1 {
			t.Errorf("Correct = %d, Wrong = %d, want 1 / 1 (manual verdicts only)", r.Correct, r.Wrong)
		}
		if r.Unconfirmed != 1 {
			t.Errorf("Unconfirmed = %d, want 1 (3 words - 2 manual verdicts)", r.Unconfirmed)
		}
		if r.Stage != StageSubmitted || r.Unchecked || r.CurrentIndex != 3 {
			t.Errorf("row fields = stage:%q unchecked:%v index:%d, want submitted/false/3", r.Stage, r.Unchecked, r.CurrentIndex)
		}
	})

	t.Run("in-progress and checking rows are not completed", func(t *testing.T) {
		got := fetch(t, 4)
		if got[1].Completed {
			t.Error("checking row completed = true, want false")
		}
		if got[3].Completed {
			t.Error("in-progress row completed = true, want false")
		}
	})

	t.Run("legacy completed practice row and unchecked marker", func(t *testing.T) {
		got := fetch(t, 5)
		if !got[2].Completed {
			t.Error("legacy cursor-completed row completed = false, want true")
		}
		if got[2].WordCount != 2 || got[2].Correct != 0 || got[2].Wrong != 0 || got[2].Unconfirmed != 2 {
			t.Errorf("legacy row counts = wc:%d c:%d w:%d u:%d, want 2/0/0/2 (no verdicts)",
				got[2].WordCount, got[2].Correct, got[2].Wrong, got[2].Unconfirmed)
		}
		if !got[4].Unchecked {
			t.Error("submitted+unchecked row unchecked = false, want true (display marker)")
		}
	})
}

// TestListLessonsLimitBounds (13.3-02 behavior 2, cap unit) — the limit
// normalization lives in the STORE so a client-supplied large value can never
// penetrate: <=0 → default 20, >50 → hard cap 50 (the single-connection pool
// read-amplification guard, RESEARCH Pitfall 4).
func TestListLessonsLimitBounds(t *testing.T) {
	store, err := NewStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	const total = 55
	for i := 0; i < total; i++ {
		createdAt := fmt.Sprintf("2026-10-01 %02d:%02d:00", i/60, i%60)
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO lessons (lesson_id, words, cards, created_at, updated_at) VALUES (?, ?, '[]', ?, ?)",
			fmt.Sprintf("L-%02d", i), `["词"]`, createdAt, createdAt,
		); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	for _, tc := range []struct {
		name  string
		limit int
		want  int
	}{
		{"zero takes default 20", 0, 20},
		{"negative takes default 20", -3, 20},
		{"above cap clamps to 50", 100, 50},
		{"within bounds passes through", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.ListLessons(ctx, tc.limit)
			if err != nil {
				t.Fatalf("ListLessons(%d): %v", tc.limit, err)
			}
			if len(got) != tc.want {
				t.Fatalf("ListLessons(%d) rows = %d, want %d", tc.limit, len(got), tc.want)
			}
			if got[0].LessonID != "L-54" {
				t.Errorf("first row = %s, want L-54 (newest)", got[0].LessonID)
			}
		})
	}
}
