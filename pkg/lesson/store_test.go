package lesson

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
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
