// advance_dictation.go — path B (issue #56): the dictation classroom's ONE
// atomic advance command. The same SQLite transaction claims the request_id,
// validates the fixed target against the authoritative row, merges the
// current word's behavior (outcome + hints) into the row's outcomes JSON,
// moves the cursor, bumps the version once, and — on the LAST word — flips
// the stage to checking in the SAME commit, so no cancel can ever strand a
// lesson between "index exhausted" and "check open". No FSRS write, no
// correctness judgement, no skipped human check (完成确认只表示写完).
//
// This file starts with the command's vocabulary, input and result shapes
// plus the caller-supplied half of validation; the transaction itself lands
// with the next commits and reuses these pieces unchanged.
package lesson

import (
	"github.com/pkg/errors"
)

// The dictation-stage outcome vocabulary (U-9). These are the ROW's
// contract — pkg/rt's per-word constants map onto these exact strings; a
// value outside the three is rejected before any database work.
const (
	// OutcomeConfirmed: the student confirmed the word as written.
	OutcomeConfirmed = "confirmed"
	// OutcomeTimeout: the 13-09 word timer fired and auto-advanced.
	OutcomeTimeout = "timeout"
	// OutcomeHint: the student asked for a hint and moved on.
	OutcomeHint = "hint"
)

// AdvanceDictationInput is one advance_dictation command. LessonID and
// RequestID identify the write (RequestID keeps the existing
// `<lessonID>-advance-<index>` dedupe key family, tool_requests PK);
// TargetIndex is the FIXED word this command advances — it must equal the
// row's current cursor, never a relative step; ExpectedVersion is the
// optimistic-concurrency guard; Outcome carries the CURRENT word's behavior
// so the same transaction that moves the cursor also persists this word's
// data (two facts that must never tear).
type AdvanceDictationInput struct {
	LessonID        string
	RequestID       string
	TargetIndex     int
	ExpectedVersion int
	Outcome         WordOutcome
}

// AdvanceDictationResult is one advance_dictation execution's outcome:
// the cursor and version after the write, the stage it landed in ("" for a
// middle word, StageChecking when the LAST word's advance opened the check
// stage), the merged behavior snapshot read back from the row, and the
// idempotent flag for same-request replays.
type AdvanceDictationResult struct {
	NewIndex   int                    `json:"new_index"`
	NewVersion int                    `json:"new_version"`
	Stage      string                 `json:"stage"`
	Outcomes   map[string]WordOutcome `json:"outcomes,omitempty"`
	LastWord   bool                   `json:"last_word"`
	Idempotent bool                   `json:"-"`
}

// validate checks the caller-supplied half of one advance_dictation command
// (T-13-01b: LLM-derived state is untrusted input): identifiers non-empty,
// version and target non-negative, the outcome inside the U-9 vocabulary,
// and a non-negative hint count. Everything here is decidable without the
// database; the row-level half (stage, poem/passage identity, cursor match)
// lives in validateDictationRowTx inside the transaction.
func (in AdvanceDictationInput) validate() error {
	if in.LessonID == "" || in.RequestID == "" || in.ExpectedVersion < 1 || in.TargetIndex < 0 {
		return errors.Wrap(ErrInvalidLesson, "advance dictation: lesson_id/request_id must be non-empty and expected_version >= 1, target_index >= 0")
	}
	switch in.Outcome.Outcome {
	case OutcomeConfirmed, OutcomeTimeout, OutcomeHint:
	default:
		return errors.Wrap(ErrInvalidLesson, "advance dictation: outcome must be confirmed/timeout/hint, got "+in.Outcome.Outcome)
	}
	if in.Outcome.Hints < 0 {
		return errors.Wrap(ErrInvalidLesson, "advance dictation: hints must be >= 0")
	}
	return nil
}
