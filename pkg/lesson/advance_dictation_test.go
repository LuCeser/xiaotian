package lesson

import (
	"context"
	"errors"
	"testing"
)

// TestAdvanceDictationArguments covers the caller-supplied half of the
// advance_dictation validation: every rejected shape fails BEFORE any
// database work, and the one well-formed shape passes.
func TestAdvanceDictationArguments(t *testing.T) {
	valid := AdvanceDictationInput{
		LessonID: "L1", RequestID: "L1-advance-0",
		TargetIndex: 0, ExpectedVersion: 1,
		Outcome: WordOutcome{Outcome: OutcomeConfirmed},
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	for name, mutate := range map[string]func(*AdvanceDictationInput){
		"empty lesson id":     func(in *AdvanceDictationInput) { in.LessonID = "" },
		"empty request id":    func(in *AdvanceDictationInput) { in.RequestID = "" },
		"zero version":        func(in *AdvanceDictationInput) { in.ExpectedVersion = 0 },
		"negative version":    func(in *AdvanceDictationInput) { in.ExpectedVersion = -1 },
		"negative target":     func(in *AdvanceDictationInput) { in.TargetIndex = -1 },
		"unknown outcome":     func(in *AdvanceDictationInput) { in.Outcome.Outcome = "guessed" },
		"empty outcome":       func(in *AdvanceDictationInput) { in.Outcome.Outcome = "" },
		"negative hint count": func(in *AdvanceDictationInput) { in.Outcome.Hints = -1 },
	} {
		in := valid
		mutate(&in)
		if err := in.validate(); !errors.Is(err, ErrInvalidLesson) {
			t.Fatalf("%s: err = %v, want ErrInvalidLesson", name, err)
		}
	}

	// Every U-9 outcome value is accepted with its hint range intact.
	for _, o := range []string{OutcomeConfirmed, OutcomeTimeout, OutcomeHint} {
		in := valid
		in.Outcome = WordOutcome{Outcome: o, Hints: 2}
		if err := in.validate(); err != nil {
			t.Fatalf("outcome %s rejected: %v", o, err)
		}
	}

	// The argument half must not touch the database at all: a nonexistent
	// lesson id passes validate() — row-level checks are the transaction's.
	if err := (AdvanceDictationInput{
		LessonID: "no-such-lesson", RequestID: "no-such-lesson-advance-0",
		TargetIndex: 0, ExpectedVersion: 1,
		Outcome: WordOutcome{Outcome: OutcomeConfirmed},
	}).validate(); err != nil {
		t.Fatalf("validate() reached past its caller-supplied half: %v", err)
	}
}

// The row-level half runs inside one open transaction against a real
// migrated store — exactly the read-and-check the command's own transaction
// will perform. 空课堂、诗文课堂、阶段限制与目标越界都拒绝；恰好的
// 固定目标通过。
func TestAdvanceDictationRowArguments(t *testing.T) {
	ex, store := newExecutorForTest(t)
	ctx := context.Background()

	cards := []Word{{Word: "苹果"}, {Word: "香蕉"}}
	if _, err := store.EnsureLessonCards(ctx, "word-lesson", cards, ""); err != nil {
		t.Fatalf("EnsureLessonCards: %v", err)
	}
	if _, err := store.EnsurePoemLesson(ctx, "poem-lesson", 7, []string{"床前明月光"}); err != nil {
		t.Fatalf("EnsurePoemLesson: %v", err)
	}
	if _, err := store.EnsurePassageLesson(ctx, "passage-lesson", 9, 3, true, nil, []string{"One row."}, "english"); err != nil {
		t.Fatalf("EnsurePassageLesson: %v", err)
	}
	if _, err := store.EnsureLessonCards(ctx, "checked-lesson", cards, ""); err != nil {
		t.Fatalf("EnsureLessonCards (checked): %v", err)
	}
	if _, err := ex.EnterCheck(ctx, "checked-lesson", 1, "checked-lesson-enter-check", nil); err != nil {
		t.Fatalf("EnterCheck: %v", err)
	}
	// A row with zero words (空课堂 with a row): only reachable via direct
	// SQL — every store creation path demands words.
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO lessons (lesson_id, words) VALUES ('empty-lesson', '[]')"); err != nil {
		t.Fatalf("seed empty lesson: %v", err)
	}
	// A row whose cursor moved once (via the legacy tool): target 0 is now
	// behind the cursor, target 1 matches.
	if _, err := ex.AdvanceWord(ctx, "word-lesson", 1, "word-lesson-advance-0"); err != nil {
		t.Fatalf("AdvanceWord setup: %v", err)
	}

	load := func(lessonID string, target int) error {
		tx, err := ex.store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		row, err := ex.loadDictationRowTx(ctx, tx, lessonID)
		if err != nil {
			return err
		}
		return validateDictationRowTx(row, target)
	}

	for _, tc := range []struct {
		name      string
		lessonID  string
		target    int
		wantError error
	}{
		{name: "missing lesson row", lessonID: "no-such-lesson", target: 0, wantError: ErrLessonNotFound},
		{name: "poem classroom", lessonID: "poem-lesson", target: 0, wantError: ErrInvalidLesson},
		{name: "passage classroom", lessonID: "passage-lesson", target: 0, wantError: ErrInvalidLesson},
		{name: "checking stage", lessonID: "checked-lesson", target: 0, wantError: ErrInvalidLesson},
		{name: "zero-word classroom", lessonID: "empty-lesson", target: 0, wantError: ErrInvalidLesson},
		{name: "target beyond the word list", lessonID: "word-lesson", target: 2, wantError: ErrInvalidLesson},
		{name: "target behind the cursor", lessonID: "word-lesson", target: 0, wantError: ErrInvalidLesson},
		{name: "fixed target at the cursor", lessonID: "word-lesson", target: 1},
	} {
		err := load(tc.lessonID, tc.target)
		switch {
		case tc.wantError == nil:
			if err != nil {
				t.Fatalf("%s: err = %v, want accepted", tc.name, err)
			}
		case !errors.Is(err, tc.wantError):
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.wantError)
		}
	}
}

// TestAdvanceDictationMiddle covers the middle-word advance through a real
// migrated store: exactly ONE word's outcome changes (others preserved),
// and the cursor + version move by exactly one in the same commit.
func TestAdvanceDictationMiddle(t *testing.T) {
	ex, store := newExecutorForTest(t)
	ctx := context.Background()
	cards := []Word{{Word: "苹果"}, {Word: "香蕉"}, {Word: "葡萄"}}
	if _, err := store.EnsureLessonCards(ctx, "mid-lesson", cards, ""); err != nil {
		t.Fatalf("EnsureLessonCards: %v", err)
	}
	// Word 0 advances first (timeout after one hint); its stored behavior
	// must survive word 1's advance untouched.
	if _, err := ex.AdvanceDictation(ctx, AdvanceDictationInput{
		LessonID: "mid-lesson", RequestID: "mid-lesson-advance-0",
		TargetIndex: 0, ExpectedVersion: 1,
		Outcome: WordOutcome{Outcome: OutcomeTimeout, Hints: 1},
	}); err != nil {
		t.Fatalf("AdvanceDictation (word 0): %v", err)
	}

	res, err := ex.AdvanceDictation(ctx, AdvanceDictationInput{
		LessonID: "mid-lesson", RequestID: "mid-lesson-advance-1",
		TargetIndex: 1, ExpectedVersion: 2,
		Outcome: WordOutcome{Outcome: OutcomeConfirmed, Hints: 2},
	})
	if err != nil {
		t.Fatalf("AdvanceDictation: %v", err)
	}
	if res.Idempotent || res.NewIndex != 2 || res.NewVersion != 3 || res.Stage != "" || res.LastWord {
		t.Fatalf("result = %+v, want index 2 / version 3 / stage '' / last=false / first execution", res)
	}

	row, err := store.GetLesson(ctx, "mid-lesson")
	if err != nil {
		t.Fatalf("GetLesson: %v", err)
	}
	if row.CurrentIndex != 2 || row.Version != 3 || row.Stage != "" {
		t.Fatalf("row index/version/stage = %d/%d/%q, want 2/3/''", row.CurrentIndex, row.Version, row.Stage)
	}
	want := map[string]WordOutcome{
		"0": {Outcome: "timeout", Hints: 1},
		"1": {Outcome: "confirmed", Hints: 2},
	}
	if len(row.Outcomes) != len(want) {
		t.Fatalf("outcomes = %+v, want exactly %d entries", row.Outcomes, len(want))
	}
	for k, v := range want {
		if got, ok := row.Outcomes[k]; !ok || got != v {
			t.Fatalf("outcomes[%s] = %+v, want %+v", k, got, v)
		}
	}

	// The request claim left a replayable first result.
	var stored string
	if err := store.db.QueryRowContext(ctx,
		"SELECT result FROM tool_requests WHERE request_id = ?", "mid-lesson-advance-1",
	).Scan(&stored); err != nil || stored == "" {
		t.Fatalf("stored result = %q err=%v — the first execution must persist its result", stored, err)
	}
}

// TestAdvanceDictationLast covers the last word's advance: the checking
// flip rides the SAME commit and the SAME single version bump — the row
// lands on "index exhausted AND check open" atomically, never between.
func TestAdvanceDictationLast(t *testing.T) {
	ex, store := newExecutorForTest(t)
	ctx := context.Background()
	cards := []Word{{Word: "苹果"}, {Word: "香蕉"}}
	if _, err := store.EnsureLessonCards(ctx, "last-lesson", cards, ""); err != nil {
		t.Fatalf("EnsureLessonCards: %v", err)
	}
	if _, err := ex.AdvanceDictation(ctx, AdvanceDictationInput{
		LessonID: "last-lesson", RequestID: "last-lesson-advance-0",
		TargetIndex: 0, ExpectedVersion: 1,
		Outcome: WordOutcome{Outcome: OutcomeConfirmed},
	}); err != nil {
		t.Fatalf("AdvanceDictation (word 0): %v", err)
	}

	res, err := ex.AdvanceDictation(ctx, AdvanceDictationInput{
		LessonID: "last-lesson", RequestID: "last-lesson-advance-1",
		TargetIndex: 1, ExpectedVersion: 2,
		Outcome: WordOutcome{Outcome: OutcomeConfirmed, Hints: 1},
	})
	if err != nil {
		t.Fatalf("AdvanceDictation (last word): %v", err)
	}
	if !res.LastWord || res.Stage != StageChecking || res.NewIndex != 2 || res.NewVersion != 3 || res.Idempotent {
		t.Fatalf("result = %+v, want index 2 / version 3 / checking / last=true / first execution", res)
	}

	row, err := store.GetLesson(ctx, "last-lesson")
	if err != nil {
		t.Fatalf("GetLesson: %v", err)
	}
	// 最后一词版本只增加一次：2 words, 2 advances, version 1 → 3.
	if row.CurrentIndex != 2 || row.Version != 3 || row.Stage != StageChecking {
		t.Fatalf("row index/version/stage = %d/%d/%q, want 2/3/checking", row.CurrentIndex, row.Version, row.Stage)
	}
	if len(row.Outcomes) != 2 {
		t.Fatalf("outcomes = %+v, want both words' behavior on the row", row.Outcomes)
	}
}

// TestAdvanceDictationRollback drives the result write to FAIL inside the
// open transaction (a SQLite trigger rejects the tool_requests update) and
// verifies the WHOLE command unwound: index, stage, outcomes and the
// request claim all rolled back together.
func TestAdvanceDictationRollback(t *testing.T) {
	ex, store := newExecutorForTest(t)
	ctx := context.Background()
	cards := []Word{{Word: "苹果"}, {Word: "香蕉"}}
	if _, err := store.EnsureLessonCards(ctx, "rb-lesson", cards, ""); err != nil {
		t.Fatalf("EnsureLessonCards: %v", err)
	}
	if _, err := ex.AdvanceDictation(ctx, AdvanceDictationInput{
		LessonID: "rb-lesson", RequestID: "rb-lesson-advance-0",
		TargetIndex: 0, ExpectedVersion: 1,
		Outcome: WordOutcome{Outcome: OutcomeConfirmed},
	}); err != nil {
		t.Fatalf("AdvanceDictation (word 0): %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`CREATE TRIGGER reject_rb_result BEFORE UPDATE ON tool_requests
		 WHEN NEW.request_id = 'rb-lesson-advance-1'
		 BEGIN SELECT RAISE(ABORT, 'result write rejected'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	_, err := ex.AdvanceDictation(ctx, AdvanceDictationInput{
		LessonID: "rb-lesson", RequestID: "rb-lesson-advance-1",
		TargetIndex: 1, ExpectedVersion: 2,
		Outcome: WordOutcome{Outcome: OutcomeConfirmed, Hints: 1},
	})
	if err == nil {
		t.Fatal("the rejected result write must surface an error")
	}

	row, gerr := store.GetLesson(ctx, "rb-lesson")
	if gerr != nil {
		t.Fatalf("GetLesson: %v", gerr)
	}
	if row.CurrentIndex != 1 || row.Version != 2 || row.Stage != "" {
		t.Fatalf("row index/version/stage = %d/%d/%q, want the pre-advance 1/2/''", row.CurrentIndex, row.Version, row.Stage)
	}
	if len(row.Outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want only word 0's stored behavior", row.Outcomes)
	}
	var claimed int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tool_requests WHERE request_id = ?", "rb-lesson-advance-1",
	).Scan(&claimed); err != nil || claimed != 0 {
		t.Fatalf("claim rows = %d err=%v — the request claim must roll back with the transaction", claimed, err)
	}

	// Drop the trigger: the SAME command now succeeds on a clean retry —
	// the rollback left no half-state behind (no claim, no bump).
	if _, err := store.db.ExecContext(ctx, "DROP TRIGGER reject_rb_result"); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	res, err := ex.AdvanceDictation(ctx, AdvanceDictationInput{
		LessonID: "rb-lesson", RequestID: "rb-lesson-advance-1",
		TargetIndex: 1, ExpectedVersion: 2,
		Outcome: WordOutcome{Outcome: OutcomeConfirmed, Hints: 1},
	})
	if err != nil {
		t.Fatalf("AdvanceDictation retry: %v", err)
	}
	if !res.LastWord || res.Stage != StageChecking || res.NewVersion != 3 {
		t.Fatalf("retry result = %+v, want a clean last-word advance", res)
	}
}

// advanceWord0 runs one first advance of word 0 on a fresh 2-word lesson.
func advanceWord0(t *testing.T, ex *Executor, store *Store, lessonID, requestID, outcome string, hints int) AdvanceDictationResult {
	t.Helper()
	if _, err := store.EnsureLessonCards(context.Background(), lessonID, []Word{{Word: "苹果"}, {Word: "香蕉"}}, ""); err != nil {
		t.Fatalf("EnsureLessonCards: %v", err)
	}
	res, err := ex.AdvanceDictation(context.Background(), AdvanceDictationInput{
		LessonID: lessonID, RequestID: requestID,
		TargetIndex: 0, ExpectedVersion: 1,
		Outcome: WordOutcome{Outcome: outcome, Hints: hints},
	})
	if err != nil {
		t.Fatalf("AdvanceDictation (word 0): %v", err)
	}
	return res
}

// TestAdvanceDictationIdempotent pins the duplicate-request semantics:
// the FIRST outcome is final (a same-id retry carrying a different outcome
// replays without overwriting), a DIFFERENT request on the same target is
// rejected as a cursor mismatch, and both confirm/timeout orders keep
// exactly one advance.
func TestAdvanceDictationIdempotent(t *testing.T) {
	firstWins := func(t *testing.T, first, second string) {
		t.Helper()
		ex, store := newExecutorForTest(t)
		ctx := context.Background()
		lesson := "idem-" + first
		res := advanceWord0(t, ex, store, lesson, lesson+"-advance-0", first, 0)

		replay, err := ex.AdvanceDictation(ctx, AdvanceDictationInput{
			LessonID: lesson, RequestID: lesson + "-advance-0",
			TargetIndex: 0, ExpectedVersion: 1,
			Outcome: WordOutcome{Outcome: second, Hints: 5},
		})
		if err != nil {
			t.Fatalf("same-id retry: %v", err)
		}
		if !replay.Idempotent || replay.NewVersion != res.NewVersion || replay.NewIndex != res.NewIndex {
			t.Fatalf("replay = %+v, want the first stored result (version %d)", replay, res.NewVersion)
		}
		row, err := store.GetLesson(ctx, lesson)
		if err != nil {
			t.Fatalf("GetLesson: %v", err)
		}
		if row.CurrentIndex != 1 || row.Version != res.NewVersion {
			t.Fatalf("row index/version = %d/%d, want ONE advance at version %d", row.CurrentIndex, row.Version, res.NewVersion)
		}
		if got := row.Outcomes["0"]; got.Outcome != first || got.Hints != 0 {
			t.Fatalf("outcomes[0] = %+v, want the FIRST outcome %s untouched", got, first)
		}

		// A DIFFERENT request on the same target must not advance or
		// overwrite anything: the cursor moved, the target is stale.
		if _, err := ex.AdvanceDictation(ctx, AdvanceDictationInput{
			LessonID: lesson, RequestID: lesson + "-advance-0-retry",
			TargetIndex: 0, ExpectedVersion: res.NewVersion,
			Outcome: WordOutcome{Outcome: second},
		}); !errors.Is(err, ErrInvalidLesson) {
			t.Fatalf("different-request same-target err = %v, want ErrInvalidLesson", err)
		}
		row, err = store.GetLesson(ctx, lesson)
		if err != nil {
			t.Fatalf("GetLesson (after stale target): %v", err)
		}
		if row.CurrentIndex != 1 || row.Outcomes["0"].Outcome != first {
			t.Fatalf("row = %+v — a stale target must change nothing", row)
		}
	}
	t.Run("confirm first, timeout retry loses", func(t *testing.T) {
		firstWins(t, OutcomeConfirmed, OutcomeTimeout)
	})
	t.Run("timeout first, confirm retry loses", func(t *testing.T) {
		firstWins(t, OutcomeTimeout, OutcomeConfirmed)
	})
}

// TestAdvanceDictationConflict pins the conflict half: a stale
// expectedVersion is a typed ConflictError whose failed attempt leaves NO
// dedupe residue, and a request_id claimed by another lesson or another
// tool is refused instead of replayed.
func TestAdvanceDictationConflict(t *testing.T) {
	ctx := context.Background()

	t.Run("version conflict leaves no dedupe residue", func(t *testing.T) {
		ex, store := newExecutorForTest(t)
		advanceWord0(t, ex, store, "cf-lesson", "cf-lesson-advance-0", OutcomeConfirmed, 0)
		// Word 1 with the STALE version 1 (the row already moved to 2).
		_, err := ex.AdvanceDictation(ctx, AdvanceDictationInput{
			LessonID: "cf-lesson", RequestID: "cf-lesson-advance-1",
			TargetIndex: 1, ExpectedVersion: 1,
			Outcome: WordOutcome{Outcome: OutcomeConfirmed},
		})
		var conflict *ConflictError
		if !errors.As(err, &conflict) || conflict.CurrentVersion != 2 {
			t.Fatalf("err = %v, want ConflictError{2}", err)
		}
		var claimed int
		if qerr := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM tool_requests WHERE request_id = ?", "cf-lesson-advance-1",
		).Scan(&claimed); qerr != nil || claimed != 0 {
			t.Fatalf("claim rows = %d err=%v — a refused conflict must leave no dedupe record", claimed, qerr)
		}
		row, gerr := store.GetLesson(ctx, "cf-lesson")
		if gerr != nil || row.Version != 2 || row.CurrentIndex != 1 {
			t.Fatalf("row = %+v err=%v — the refused conflict must write nothing", row, gerr)
		}
	})

	for _, tc := range []struct{ name, recLesson, recTool string }{
		{name: "wrong lesson", recLesson: "other-lesson", recTool: "advance_dictation"},
		{name: "wrong tool", recLesson: "mine-lesson", recTool: "advance_word"},
	} {
		t.Run(tc.name+" dedupe record is refused", func(t *testing.T) {
			ex, store := newExecutorForTest(t)
			if _, err := store.EnsureLessonCards(ctx, "mine-lesson", []Word{{Word: "苹果"}, {Word: "香蕉"}}, ""); err != nil {
				t.Fatalf("EnsureLessonCards: %v", err)
			}
			// Seed a foreign claim under the SAME request id. Its stored
			// version is deliberately NOT stale (new_version 1 == current),
			// so ONLY the identity check can refuse it.
			if _, err := store.db.ExecContext(ctx,
				"INSERT INTO tool_requests (request_id, lesson_id, tool, result) VALUES (?, ?, ?, ?)",
				"stolen-id", tc.recLesson, tc.recTool,
				`{"new_index":0,"new_version":1,"stage":"","last_word":false}`,
			); err != nil {
				t.Fatalf("seed foreign record: %v", err)
			}
			if _, err := ex.AdvanceDictation(ctx, AdvanceDictationInput{
				LessonID: "mine-lesson", RequestID: "stolen-id",
				TargetIndex: 0, ExpectedVersion: 1,
				Outcome: WordOutcome{Outcome: OutcomeConfirmed},
			}); !errors.Is(err, ErrInvalidLesson) {
				t.Fatalf("err = %v, want ErrInvalidLesson — a foreign record must never replay", err)
			}
			row, gerr := store.GetLesson(ctx, "mine-lesson")
			if gerr != nil || row.CurrentIndex != 0 || row.Version != 1 {
				t.Fatalf("row = %+v err=%v — the refused replay must not advance", row, gerr)
			}
		})
	}
}
