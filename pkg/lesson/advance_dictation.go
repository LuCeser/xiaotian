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
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

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

// dictationRowTx is the authoritative dictation-relevant state of one
// lessons row, read INSIDE the caller's open transaction — the single read
// the whole advance command trusts (RESEARCH Pattern 5: no read-then-write
// outside the transaction).
type dictationRowTx struct {
	currentIndex int
	version      int
	wordCount    int
	stage        string
	poem         bool
	passage      bool
	outcomes     map[string]WordOutcome
}

// loadDictationRowTx reads the row's dictation-relevant state inside the
// open transaction. A missing row maps to ErrLessonNotFound (空课堂).
func (e *Executor) loadDictationRowTx(ctx context.Context, tx *sql.Tx, lessonID string) (dictationRowTx, error) {
	var (
		row          dictationRowTx
		poemID       int64
		passageID    int64
		wordsJSON    string
		outcomesJSON string
	)
	err := tx.QueryRowContext(ctx,
		"SELECT current_index, version, stage, poem_id, passage_id, words, outcomes FROM lessons WHERE lesson_id = ?",
		lessonID,
	).Scan(&row.currentIndex, &row.version, &row.stage, &poemID, &passageID, &wordsJSON, &outcomesJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dictationRowTx{}, errors.Wrap(ErrLessonNotFound, fmt.Sprintf("advance dictation for lesson %q", lessonID))
		}
		return dictationRowTx{}, errors.Wrap(err, "advance dictation: read lesson row")
	}
	var words []string
	if err := json.Unmarshal([]byte(wordsJSON), &words); err != nil {
		return dictationRowTx{}, errors.Wrap(err, "advance dictation: decode words")
	}
	row.wordCount = len(words)
	row.poem = poemID != 0
	row.passage = passageID != 0
	outcomes, err := decodeOutcomesJSON(outcomesJSON)
	if err != nil {
		return dictationRowTx{}, errors.Wrap(err, "advance dictation: decode outcomes")
	}
	row.outcomes = outcomes
	return row, nil
}

// validateDictationRowTx checks the row-level half inside the transaction:
// 诗文/课文课堂 keep their own pacing models and never advance through this
// command; the stage must still be the dictation stage ('' — checking and
// submitted rows belong to the check-stage tools, so an exhausted classroom
// cannot be re-advanced); the classroom must hold words; and the fixed
// target must equal the row's CURRENT cursor (每次只推进一词： within one
// row generation the cursor and the version move together, so a mismatch
// is a garbled or stale command — never silently reapplied to another
// word).
func validateDictationRowTx(row dictationRowTx, targetIndex int) error {
	if row.poem || row.passage {
		return errors.Wrap(ErrInvalidLesson, "advance dictation: poem/passage classrooms keep their own pacing")
	}
	if row.stage != "" {
		return errors.Wrap(ErrInvalidLesson, "advance dictation: stage "+row.stage+" is past the dictation stage")
	}
	if row.wordCount == 0 {
		return errors.Wrap(ErrInvalidLesson, "advance dictation: classroom holds no words")
	}
	if targetIndex >= row.wordCount {
		return errors.Wrap(ErrInvalidLesson, "advance dictation: target index beyond the word list")
	}
	if targetIndex != row.currentIndex {
		return errors.Wrap(ErrInvalidLesson, fmt.Sprintf("advance dictation: target %d does not match the cursor %d", targetIndex, row.currentIndex))
	}
	return nil
}
