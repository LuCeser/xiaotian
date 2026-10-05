package lesson

import (
	"context"
	"database/sql"
	"errors"
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
	if _, err := db.ExecContext(ctx, legacyDDL); err != nil {
		t.Fatalf("create legacy lessons: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO lessons (lesson_id, words) VALUES ('LEGACY-1', ?)",
		`["旧词","第二词"]`,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate on legacy db: %v", err)
	}
	// Second run must stay idempotent (probe finds the column, no ALTER).
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate on legacy db: %v", err)
	}

	hasCards, err := lessonsHasCardsColumn(ctx, db)
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
	if _, err := store.db.ExecContext(ctx,
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

	if _, err := store.db.ExecContext(ctx,
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
