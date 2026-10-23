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
