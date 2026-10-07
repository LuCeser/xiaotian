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

// TestApplyVerdictIdempotent (13.1-01): one screen verdict is an idempotent
// write — the same request_id replays the FIRST stored result without a
// second write, while a stale expected_version with a NEW request_id hits
// the optimistic lock as *ConflictError (T-13.1-03).
func TestApplyVerdictIdempotent(t *testing.T) {
	ex, store := newExecutorForTest(t)
	ctx := context.Background()

	if _, err := store.EnsureLessonCards(ctx, "L1", []Word{
		{Word: "苹果", Meaning: "cat", Example: "x"},
		{Word: "香蕉", Meaning: "dog", Example: "y"},
	}); err != nil {
		t.Fatalf("EnsureLessonCards: %v", err)
	}
	ec, err := ex.EnterCheck(ctx, "L1", 1, "L1-enter-check", map[string]WordOutcome{
		"0": {Outcome: "confirmed", Hints: 0},
		"1": {Outcome: "confirmed", Hints: 1},
	})
	if err != nil {
		t.Fatalf("EnterCheck: %v", err)
	}
	if ec.Idempotent || ec.Stage != "checking" {
		t.Fatalf("EnterCheck first call = %+v, want fresh checking stage", ec)
	}

	first, err := ex.ApplyVerdict(ctx, "L1", ec.NewVersion, "req-v-0", CheckVerdict{WordIndex: 0, Correct: true, Manual: true})
	if err != nil {
		t.Fatalf("ApplyVerdict first call: %v", err)
	}
	if first.Idempotent {
		t.Errorf("first ApplyVerdict = %+v, want a real write (Idempotent:false)", first)
	}
	if len(first.Verdicts) != 1 || first.Verdicts[0] != (CheckVerdict{WordIndex: 0, Correct: true, Manual: true}) {
		t.Errorf("first ApplyVerdict verdicts = %+v, want the stored manual verdict", first.Verdicts)
	}

	// Network retry with the SAME request_id: replay the stored result, no
	// second write, version untouched.
	replay, err := ex.ApplyVerdict(ctx, "L1", ec.NewVersion, "req-v-0", CheckVerdict{WordIndex: 0, Correct: true, Manual: true})
	if err != nil {
		t.Fatalf("ApplyVerdict replay: %v", err)
	}
	if !replay.Idempotent {
		t.Errorf("replay ApplyVerdict = %+v, want Idempotent:true", replay)
	}
	if len(replay.Verdicts) != 1 {
		t.Errorf("replay verdicts = %+v, want still exactly 1 entry (no double write)", replay.Verdicts)
	}
	var version int
	if err := store.db.QueryRowContext(ctx,
		"SELECT version FROM lessons WHERE lesson_id = 'L1'",
	).Scan(&version); err != nil {
		t.Fatalf("read lessons.version: %v", err)
	}
	if version != first.NewVersion {
		t.Errorf("lessons.version after replay = %d, want %d (replay must not bump)", version, first.NewVersion)
	}

	// A NEW request_id with the STALE expected_version is a conflict, and
	// nothing is written.
	_, err = ex.ApplyVerdict(ctx, "L1", ec.NewVersion, "req-v-1", CheckVerdict{WordIndex: 1, Correct: false, Manual: true})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("ApplyVerdict stale version error = %v, want ErrVersionConflict", err)
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("ApplyVerdict stale version error = %T (%v), want *ConflictError", err, err)
	}
	if conflict.CurrentVersion != first.NewVersion {
		t.Errorf("ConflictError.CurrentVersion = %d, want %d", conflict.CurrentVersion, first.NewVersion)
	}
	var verdictsJSON string
	if err := store.db.QueryRowContext(ctx,
		"SELECT verdicts FROM lessons WHERE lesson_id = 'L1'",
	).Scan(&verdictsJSON); err != nil {
		t.Fatalf("read lessons.verdicts: %v", err)
	}
	if strings.Contains(verdictsJSON, `"word_index":1`) {
		t.Errorf("conflicted verdict leaked into the row: %s", verdictsJSON)
	}
}

// TestCompleteCheckGate (13.1-01): the checking→submitted stage transition
// IS the unified-submit idempotency latch. A same-request_id replay returns
// the stored result; a NEW request_id at the CURRENT version must not pass
// the latch either — the row is already submitted, so no caller can drive a
// second submission through (T-13.1-01).
func TestCompleteCheckGate(t *testing.T) {
	ex, store := newExecutorForTest(t)
	ctx := context.Background()

	if _, err := store.EnsureLessonCards(ctx, "L1", []Word{
		{Word: "苹果", Meaning: "cat", Example: "x"},
		{Word: "香蕉", Meaning: "dog", Example: "y"},
	}); err != nil {
		t.Fatalf("EnsureLessonCards: %v", err)
	}
	ec, err := ex.EnterCheck(ctx, "L1", 1, "L1-enter-check", nil)
	if err != nil {
		t.Fatalf("EnterCheck: %v", err)
	}
	if _, err := ex.ApplyVerdict(ctx, "L1", ec.NewVersion, "req-v-0", CheckVerdict{WordIndex: 0, Correct: true, Manual: true}); err != nil {
		t.Fatalf("ApplyVerdict 0: %v", err)
	}
	av, err := ex.ApplyVerdict(ctx, "L1", ec.NewVersion+1, "req-v-1", CheckVerdict{WordIndex: 1, Correct: false, Manual: true})
	if err != nil {
		t.Fatalf("ApplyVerdict 1: %v", err)
	}

	first, err := ex.CompleteCheck(ctx, "L1", av.NewVersion, "sub-1", false)
	if err != nil {
		t.Fatalf("CompleteCheck first call: %v", err)
	}
	if first.Idempotent || first.Stage != "submitted" || first.Unchecked {
		t.Errorf("first CompleteCheck = %+v, want fresh submitted stage, unchecked=false", first)
	}

	// Same request_id replay.
	replay, err := ex.CompleteCheck(ctx, "L1", av.NewVersion, "sub-1", false)
	if err != nil {
		t.Fatalf("CompleteCheck same-request replay: %v", err)
	}
	if !replay.Idempotent || replay.Stage != "submitted" {
		t.Errorf("CompleteCheck replay = %+v, want idempotent replay of the stored result", replay)
	}

	// A NEW request_id at the CURRENT version cannot pass the latch either:
	// the stage gate (checking→submitted) refuses it as an idempotent no-op,
	// never a second submission.
	second, err := ex.CompleteCheck(ctx, "L1", first.NewVersion, "sub-2", false)
	if err != nil {
		t.Fatalf("CompleteCheck new-request after submit: %v", err)
	}
	if !second.Idempotent || second.Stage != "submitted" {
		t.Errorf("CompleteCheck after latch = %+v, want idempotent replay (stage gate)", second)
	}

	// The row itself must show exactly one transition: version bumped once
	// past the verdict writes, stage submitted, unchecked flag stored.
	var version int
	var stage string
	var unchecked int
	if err := store.db.QueryRowContext(ctx,
		"SELECT version, stage, unchecked FROM lessons WHERE lesson_id = 'L1'",
	).Scan(&version, &stage, &unchecked); err != nil {
		t.Fatalf("read lessons row: %v", err)
	}
	if version != first.NewVersion {
		t.Errorf("lessons.version after replays = %d, want %d (no double transition)", version, first.NewVersion)
	}
	if stage != "submitted" || unchecked != 0 {
		t.Errorf("lessons stage/unchecked = %q/%d, want submitted/0", stage, unchecked)
	}
}

// TestClearUnchecked (13.1-04): the correction path's marker reset. The
// UPDATE carries `AND unchecked = 1` (plus the optimistic lock), so the
// first clear is a real write; an already-cleared row is an idempotent
// no-op (never a second version bump), a stale expected_version is a
// conflict writing nothing, and a same-request_id retry replays the stored
// result.
func TestClearUnchecked(t *testing.T) {
	ex, store := newExecutorForTest(t)
	ctx := context.Background()

	if _, err := store.EnsureLessonCards(ctx, "L1", []Word{
		{Word: "苹果", Meaning: "cat", Example: "x"},
		{Word: "香蕉", Meaning: "dog", Example: "y"},
	}); err != nil {
		t.Fatalf("EnsureLessonCards: %v", err)
	}
	ec, err := ex.EnterCheck(ctx, "L1", 1, "L1-enter-check", map[string]WordOutcome{
		"0": {Outcome: "confirmed", Hints: 0},
	})
	if err != nil {
		t.Fatalf("EnterCheck: %v", err)
	}
	cc, err := ex.CompleteCheck(ctx, "L1", ec.NewVersion, "sub-1", true)
	if err != nil {
		t.Fatalf("CompleteCheck: %v", err)
	}
	if !cc.Unchecked {
		t.Fatalf("seed CompleteCheck = %+v, want unchecked=true", cc)
	}

	// Stale expected_version: conflict, the marker stays.
	_, err = ex.ClearUnchecked(ctx, "L1", ec.NewVersion, "clear-stale")
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("ClearUnchecked stale version = %v, want ErrVersionConflict", err)
	}
	var unchecked int
	if err := store.db.QueryRowContext(ctx,
		"SELECT unchecked FROM lessons WHERE lesson_id = 'L1'",
	).Scan(&unchecked); err != nil {
		t.Fatalf("read lessons.unchecked: %v", err)
	}
	if unchecked != 1 {
		t.Errorf("unchecked after conflicted clear = %d, want 1 (nothing written)", unchecked)
	}

	// First clear at the current version: a real write that resets the
	// marker and bumps the version.
	cu, err := ex.ClearUnchecked(ctx, "L1", cc.NewVersion, "clear-1")
	if err != nil {
		t.Fatalf("ClearUnchecked first: %v", err)
	}
	if cu.Idempotent || cu.Stage != StageSubmitted {
		t.Errorf("first ClearUnchecked = %+v, want a fresh write (Idempotent:false, stage submitted)", cu)
	}
	var version int
	if err := store.db.QueryRowContext(ctx,
		"SELECT version, unchecked FROM lessons WHERE lesson_id = 'L1'",
	).Scan(&version, &unchecked); err != nil {
		t.Fatalf("read lessons row: %v", err)
	}
	if version != cu.NewVersion || unchecked != 0 {
		t.Errorf("row after clear = version %d unchecked %d, want version %d unchecked 0", version, unchecked, cu.NewVersion)
	}

	// Same request_id retry: stored-result replay, version untouched.
	replay, err := ex.ClearUnchecked(ctx, "L1", cc.NewVersion, "clear-1")
	if err != nil {
		t.Fatalf("ClearUnchecked replay: %v", err)
	}
	if !replay.Idempotent || replay.NewVersion != cu.NewVersion {
		t.Errorf("ClearUnchecked replay = %+v, want idempotent replay of %d", replay, cu.NewVersion)
	}

	// Already-cleared row under a NEW request_id: idempotent no-op, never a
	// second version bump.
	noop, err := ex.ClearUnchecked(ctx, "L1", cu.NewVersion, "clear-2")
	if err != nil {
		t.Fatalf("ClearUnchecked after clear: %v", err)
	}
	if !noop.Idempotent || noop.NewVersion != cu.NewVersion {
		t.Errorf("ClearUnchecked no-op = %+v, want idempotent no-op at version %d", noop, cu.NewVersion)
	}
	var version2 int
	if err := store.db.QueryRowContext(ctx,
		"SELECT version FROM lessons WHERE lesson_id = 'L1'",
	).Scan(&version2); err != nil {
		t.Fatalf("read lessons.version: %v", err)
	}
	if version2 != cu.NewVersion {
		t.Errorf("lessons.version after no-op = %d, want %d (no double transition)", version2, cu.NewVersion)
	}
}
