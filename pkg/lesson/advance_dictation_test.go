package lesson

import (
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
