package lesson

import (
	"context"
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// newExecutorForTest opens a fresh migrated temp DB and returns an Executor
// wired to its Store.
func newExecutorForTest(t *testing.T) (*Executor, *Store) {
	t.Helper()
	store, err := NewStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ex, err := NewExecutor(store, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	return ex, store
}

func TestAdvanceWordIdempotent(t *testing.T) {
	ex, store := newExecutorForTest(t)
	ctx := context.Background()

	if _, err := store.EnsureLesson(ctx, "L1", []string{"猫", "狗", "鸟"}); err != nil {
		t.Fatalf("EnsureLesson: %v", err)
	}

	first, err := ex.AdvanceWord(ctx, "L1", 1, "call_1")
	if err != nil {
		t.Fatalf("AdvanceWord first call: %v", err)
	}
	if first.NewIndex != 1 || first.NewVersion != 2 || first.Idempotent {
		t.Errorf("first call = %+v, want {NewIndex:1 NewVersion:2 Idempotent:false}", first)
	}

	second, err := ex.AdvanceWord(ctx, "L1", 1, "call_1")
	if err != nil {
		t.Fatalf("AdvanceWord duplicate request_id: %v", err)
	}
	if second.NewIndex != 1 || second.NewVersion != 2 || !second.Idempotent {
		t.Errorf("duplicate call = %+v, want {NewIndex:1 NewVersion:2 Idempotent:true}", second)
	}

	// The duplicate execution must not have advanced the lesson again.
	var version int
	if err := store.db.QueryRowContext(ctx,
		"SELECT version FROM lessons WHERE lesson_id = 'L1'",
	).Scan(&version); err != nil {
		t.Fatalf("read lessons.version: %v", err)
	}
	if version != 2 {
		t.Errorf("lessons.version after duplicate request = %d, want 2 (only one advance)", version)
	}
}

func TestAdvanceWordVersionConflict(t *testing.T) {
	ex, store := newExecutorForTest(t)
	ctx := context.Background()

	if _, err := store.EnsureLesson(ctx, "L1", []string{"猫", "狗"}); err != nil {
		t.Fatalf("EnsureLesson: %v", err)
	}
	// Move the lesson from version 1 to version 2.
	if _, err := ex.AdvanceWord(ctx, "L1", 1, "call_a"); err != nil {
		t.Fatalf("AdvanceWord setup call: %v", err)
	}

	_, err := ex.AdvanceWord(ctx, "L1", 1, "call_b") // stale expected_version
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("AdvanceWord stale version error = %v, want ErrVersionConflict", err)
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("AdvanceWord stale version error = %T (%v), want *ConflictError", err, err)
	}
	if conflict.CurrentVersion != 2 {
		t.Errorf("ConflictError.CurrentVersion = %d, want 2", conflict.CurrentVersion)
	}

	// The rejected write must leave the lesson row and tool_requests intact.
	var index int
	if err := store.db.QueryRowContext(ctx,
		"SELECT current_index FROM lessons WHERE lesson_id = 'L1'",
	).Scan(&index); err != nil {
		t.Fatalf("read current_index: %v", err)
	}
	if index != 1 {
		t.Errorf("current_index after conflict = %d, want 1 (unchanged)", index)
	}

	var toolRows int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tool_requests WHERE request_id = 'call_b'",
	).Scan(&toolRows); err != nil {
		t.Fatalf("count tool_requests: %v", err)
	}
	if toolRows != 0 {
		t.Errorf("tool_requests rows for call_b = %d, want 0 (conflict rolled back)", toolRows)
	}
}

func TestAdvanceWordLessonChange(t *testing.T) {
	ex, store := newExecutorForTest(t)
	ctx := context.Background()

	lesson, err := store.EnsureLesson(ctx, "L1", []string{"猫", "狗", "鸟"})
	if err != nil {
		t.Fatalf("EnsureLesson: %v", err)
	}
	if lesson.Version != 1 || lesson.CurrentIndex != 0 {
		t.Fatalf("initial lesson = version %d index %d, want version 1 index 0", lesson.Version, lesson.CurrentIndex)
	}

	first, err := ex.AdvanceWord(ctx, "L1", 1, "call_1")
	if err != nil {
		t.Fatalf("AdvanceWord first: %v", err)
	}
	second, err := ex.AdvanceWord(ctx, "L1", 2, "call_2")
	if err != nil {
		t.Fatalf("AdvanceWord second: %v", err)
	}

	if first.NewIndex != 1 || first.NewVersion != 2 {
		t.Errorf("first advance = %+v, want index 1 version 2", first)
	}
	if second.NewIndex != 2 || second.NewVersion != 3 {
		t.Errorf("second advance = %+v, want index 2 version 3", second)
	}

	got, err := store.GetLesson(ctx, "L1")
	if err != nil {
		t.Fatalf("GetLesson: %v", err)
	}
	if got.CurrentIndex != 2 || got.Version != 3 {
		t.Errorf("persisted lesson = index %d version %d, want index 2 version 3", got.CurrentIndex, got.Version)
	}
}

func TestAdvanceWordNotFound(t *testing.T) {
	ex, _ := newExecutorForTest(t)

	_, err := ex.AdvanceWord(context.Background(), "no-such-lesson", 1, "call_x")
	if !errors.Is(err, ErrLessonNotFound) {
		t.Fatalf("AdvanceWord unknown lesson error = %v, want ErrLessonNotFound", err)
	}
}
