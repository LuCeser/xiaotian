package lesson

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// newExecutorForTest opens a fresh migrated temp DB and returns an Executor
// wired to its Store.
func newExecutorForTest(t *testing.T) (*Executor, *Store) {
	t.Helper()
	ex, store, _ := newExecutorWithCaptureForTest(t)
	return ex, store
}

// newExecutorWithCaptureForTest opens a fresh migrated temp DB and returns an
// Executor wired to its Store together with the capturing logger it logs to,
// so tests can assert on emitted entries.
func newExecutorWithCaptureForTest(t *testing.T) (*Executor, *Store, *captureLogger) {
	t.Helper()
	store, err := NewStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	logger := &captureLogger{}
	ex, err := NewExecutor(store, logger)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	return ex, store, logger
}

// capturedLog is one recorded logger call.
type capturedLog struct {
	Level  string
	Msg    string
	Fields []infra.Field
}

// captureLogger is the test-local capturing logger (manual fake, D-48;
// pattern from pkg/infra/middleware_test.go's fakeLogger). It implements
// infra.Logger by recording every entry into a mutex-guarded slice the test
// can inspect directly.
type captureLogger struct {
	mu      sync.Mutex
	entries []capturedLog
}

// record appends one captured entry with a field-slice copy to prevent
// aliasing.
func (c *captureLogger) record(level, msg string, fs []infra.Field) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, capturedLog{Level: level, Msg: msg, Fields: append([]infra.Field{}, fs...)})
}

func (c *captureLogger) Debug(m string, fs ...infra.Field) { c.record("debug", m, fs) }
func (c *captureLogger) Info(m string, fs ...infra.Field)  { c.record("info", m, fs) }
func (c *captureLogger) Warn(m string, fs ...infra.Field)  { c.record("warn", m, fs) }
func (c *captureLogger) Error(m string, fs ...infra.Field) { c.record("error", m, fs) }

// With returns the receiver: the executor passes explicit per-call fields,
// so field accumulation is unnecessary for these tests.
func (c *captureLogger) With(_ ...infra.Field) infra.Logger { return c }

func (c *captureLogger) Sync() error { return nil }

// errorEntriesContaining returns every captured entry logged at error level
// whose message contains substr.
func (c *captureLogger) errorEntriesContaining(substr string) []capturedLog {
	c.mu.Lock()
	defer c.mu.Unlock()
	var hits []capturedLog
	for _, e := range c.entries {
		if e.Level == "error" && strings.Contains(e.Msg, substr) {
			hits = append(hits, e)
		}
	}
	return hits
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

func TestAdvanceWordStaleRecordAfterLessonReset(t *testing.T) {
	ex, store, logger := newExecutorWithCaptureForTest(t)
	ctx := context.Background()

	if _, err := store.EnsureLesson(ctx, "L1", []string{"猫", "狗", "鸟"}); err != nil {
		t.Fatalf("EnsureLesson: %v", err)
	}

	first, err := ex.AdvanceWord(ctx, "L1", 1, "L1-advance-0")
	if err != nil {
		t.Fatalf("AdvanceWord first call: %v", err)
	}
	if first.NewIndex != 1 || first.NewVersion != 2 || first.Idempotent {
		t.Fatalf("first call = %+v, want {NewIndex:1 NewVersion:2 Idempotent:false}", first)
	}

	// Simulate a same-day lesson reset: delete the lessons row (the old
	// tool_requests record survives, carrying stored NewVersion=2 from the
	// previous lesson generation) and rebuild it — INSERT OR IGNORE creates
	// a fresh row with version=1, current_index=0.
	if _, err := store.db.ExecContext(ctx, "DELETE FROM lessons WHERE lesson_id = 'L1'"); err != nil {
		t.Fatalf("delete lessons row: %v", err)
	}
	if _, err := store.EnsureLesson(ctx, "L1", []string{"鱼", "虫", "花"}); err != nil {
		t.Fatalf("EnsureLesson rebuild: %v", err)
	}
	rebuilt, err := store.GetLesson(ctx, "L1")
	if err != nil {
		t.Fatalf("GetLesson rebuilt: %v", err)
	}
	if rebuilt.Version != 1 || rebuilt.CurrentIndex != 0 {
		t.Fatalf("rebuilt lesson = version %d index %d, want version 1 index 0", rebuilt.Version, rebuilt.CurrentIndex)
	}

	// U-2: the stale tool_requests record (stored NewVersion=2 > current
	// version=1) must NOT be replayed as an idempotent hit — the advance
	// must really execute so the student hears the next word.
	second, err := ex.AdvanceWord(ctx, "L1", 1, "L1-advance-0")
	if err != nil {
		t.Fatalf("AdvanceWord after lesson reset: %v", err)
	}
	if second.Idempotent {
		t.Errorf("advance after reset = %+v, want real execution (Idempotent:false); stale dedupe record was replayed", second)
	}
	if second.NewIndex != 1 || second.NewVersion != 2 {
		t.Errorf("advance after reset = %+v, want {NewIndex:1 NewVersion:2}", second)
	}

	// The lessons row must have really advanced.
	var version, index int
	if err := store.db.QueryRowContext(ctx,
		"SELECT version, current_index FROM lessons WHERE lesson_id = 'L1'",
	).Scan(&version, &index); err != nil {
		t.Fatalf("read lessons after re-advance: %v", err)
	}
	if version != 2 || index != 1 {
		t.Errorf("lessons after re-advance = version %d index %d, want version 2 index 1", version, index)
	}

	// A third call with the same request_id replays the NEW result (the
	// re-execution replaced the stale record).
	third, err := ex.AdvanceWord(ctx, "L1", 2, "L1-advance-0")
	if err != nil {
		t.Fatalf("AdvanceWord third call: %v", err)
	}
	if !third.Idempotent || third.NewIndex != 1 || third.NewVersion != 2 {
		t.Errorf("third call = %+v, want replay of new result {NewIndex:1 NewVersion:2 Idempotent:true}", third)
	}
	var versionAfter int
	if err := store.db.QueryRowContext(ctx,
		"SELECT version FROM lessons WHERE lesson_id = 'L1'",
	).Scan(&versionAfter); err != nil {
		t.Fatalf("read lessons.version after third call: %v", err)
	}
	if versionAfter != 2 {
		t.Errorf("lessons.version after third call = %d, want 2 (replay must not advance)", versionAfter)
	}

	// U-2: the stale-record detection must log loudly at Error level —
	// never silently.
	if stale := logger.errorEntriesContaining("stale"); len(stale) == 0 {
		t.Errorf("no Error-level log containing %q was captured; stale dedupe record was handled silently", "stale")
	}
}

func TestAdvanceWordIdempotentReplayWhenLessonAdvanced(t *testing.T) {
	ex, store, _ := newExecutorWithCaptureForTest(t)
	ctx := context.Background()

	if _, err := store.EnsureLesson(ctx, "L1", []string{"猫", "狗", "鸟"}); err != nil {
		t.Fatalf("EnsureLesson: %v", err)
	}

	// call_1: first real advance v1→v2.
	first, err := ex.AdvanceWord(ctx, "L1", 1, "call_1")
	if err != nil {
		t.Fatalf("AdvanceWord call_1: %v", err)
	}
	if first.NewIndex != 1 || first.NewVersion != 2 || first.Idempotent {
		t.Fatalf("call_1 = %+v, want {NewIndex:1 NewVersion:2 Idempotent:false}", first)
	}

	// call_2: second real advance v2→v3.
	second, err := ex.AdvanceWord(ctx, "L1", 2, "call_2")
	if err != nil {
		t.Fatalf("AdvanceWord call_2: %v", err)
	}
	if second.NewIndex != 2 || second.NewVersion != 3 || second.Idempotent {
		t.Fatalf("call_2 = %+v, want {NewIndex:2 NewVersion:3 Idempotent:false}", second)
	}

	// Re-sending call_1 after the lesson advanced further: stored(2) <
	// current(3) is a LEGITIMATE replay — the stale-record guard must not
	// touch it.
	replay, err := ex.AdvanceWord(ctx, "L1", 3, "call_1")
	if err != nil {
		t.Fatalf("AdvanceWord call_1 replay: %v", err)
	}
	if !replay.Idempotent {
		t.Errorf("call_1 replay = %+v, want Idempotent:true (legitimate replay)", replay)
	}
	if replay.NewIndex != 1 || replay.NewVersion != 2 {
		t.Errorf("call_1 replay = %+v, want stored result {NewIndex:1 NewVersion:2}", replay)
	}

	var version int
	if err := store.db.QueryRowContext(ctx,
		"SELECT version FROM lessons WHERE lesson_id = 'L1'",
	).Scan(&version); err != nil {
		t.Fatalf("read lessons.version: %v", err)
	}
	if version != 3 {
		t.Errorf("lessons.version after replay = %d, want 3 (replay must not advance)", version)
	}
}

func TestAdvanceWordStaleRecordLessonMissing(t *testing.T) {
	ex, store, _ := newExecutorWithCaptureForTest(t)
	ctx := context.Background()

	if _, err := store.EnsureLesson(ctx, "L1", []string{"猫", "狗", "鸟"}); err != nil {
		t.Fatalf("EnsureLesson: %v", err)
	}
	if _, err := ex.AdvanceWord(ctx, "L1", 1, "call_1"); err != nil {
		t.Fatalf("AdvanceWord first call: %v", err)
	}

	// Delete the lessons row WITHOUT rebuilding it: the tool_requests
	// record survives, but the lesson it belongs to is gone.
	if _, err := store.db.ExecContext(ctx, "DELETE FROM lessons WHERE lesson_id = 'L1'"); err != nil {
		t.Fatalf("delete lessons row: %v", err)
	}

	// U-2: a dedupe hit with no lessons row must surface
	// ErrLessonNotFound — never replay the stale result as success.
	_, err := ex.AdvanceWord(ctx, "L1", 2, "call_1")
	if !errors.Is(err, ErrLessonNotFound) {
		t.Fatalf("AdvanceWord dedupe hit with missing lesson error = %v, want ErrLessonNotFound", err)
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
