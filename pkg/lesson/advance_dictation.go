// advance_dictation.go — path B (issue #56): the dictation classroom's ONE
// atomic advance command. The same SQLite transaction claims the request_id,
// validates the fixed target against the authoritative row, merges the
// current word's behavior (outcome + hints) into the row's outcomes JSON,
// moves the cursor, bumps the version once, and — on the LAST word — flips
// the stage to checking in the SAME commit, so no cancel can ever strand a
// lesson between "index exhausted" and "check open". No FSRS write, no
// correctness judgement, no skipped human check (完成确认只表示写完).
package lesson

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/pkg/errors"
	sqlite "modernc.org/sqlite"
	lib "modernc.org/sqlite/lib"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
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

// AdvanceDictation executes the dictation classroom's ONE atomic advance
// (path B, issue #56): claim the request_id (a duplicate replays the FIRST
// stored result, never a second advance), load the authoritative row and
// validate it, merge the current word's outcome into the row's outcomes
// JSON, and move the cursor + version in the SAME commit — and on the last
// word flip the stage to checking in that same commit, so a cancel can
// never strand a lesson between "index moved" and "check open" nor
// separate the behavior save from the cursor move. Same short-transaction
// discipline as AdvanceWord (L-3: three statements plus the readback).
func (e *Executor) AdvanceDictation(ctx context.Context, in AdvanceDictationInput) (AdvanceDictationResult, error) {
	if err := in.validate(); err != nil {
		e.logger.Warn("advance dictation rejected: invalid arguments",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", in.LessonID),
			infra.F("request_id", in.RequestID),
			infra.F("target_index", in.TargetIndex),
			infra.F("expected_version", in.ExpectedVersion),
		)
		return AdvanceDictationResult{}, err
	}
	res, err := e.advanceDictationTx(ctx, in, false)
	var stale *staleRecordError
	if !errors.As(err, &stale) {
		return res, err
	}
	// U-2 self-heal, the existing stale-record rule (AdvanceWord's): the
	// record belongs to a dead generation of the row — purge it and
	// re-execute ONCE. The single-connection pool serializes write
	// transactions, so nothing can re-create a stale record between
	// detection and retry.
	return e.advanceDictationTx(ctx, in, true)
}

// advanceDictationTx is the whole advance in one SQLite transaction:
// (a) claim the request_id — a PRIMARY KEY violation rolls back and replays
//     the FIRST stored result (typed constraint detection, review L-2);
// (b) load + validate the authoritative row (stage/pacing/cursor checks);
// (c) merge the current word's outcome, preserving every other word's
//     stored behavior;
// (d) the guarded write — optimistic version lock plus the cursor guard
//     (defense in depth: within one generation the two move together);
// (e) read back, persist the typed result, commit — all atomic.
// purgeStale is the U-2 retry switch: the stale record is DELETEd by
// primary key BEFORE the INSERT re-claims the request_id, atomically with
// the re-executed advance (a failed purge never destroys a record a
// legitimate replay would have used — the 0-rows paths unwind everything).
func (e *Executor) advanceDictationTx(ctx context.Context, in AdvanceDictationInput, purgeStale bool) (AdvanceDictationResult, error) {
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		e.logger.Error("advance dictation: begin tx failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", in.LessonID),
			infra.F("request_id", in.RequestID),
			infra.ErrField(err),
		)
		return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: begin tx")
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	// U-2 retry: delete the stale dedupe record inside THIS transaction,
	// before the claim below re-claims the request_id.
	if purgeStale {
		if _, err = tx.ExecContext(ctx,
			"DELETE FROM tool_requests WHERE request_id = ?",
			in.RequestID,
		); err != nil {
			e.logger.Error("advance dictation: purge stale dedupe record failed",
				infra.F("component", "lesson/executor"),
				infra.F("lesson_id", in.LessonID),
				infra.F("request_id", in.RequestID),
				infra.ErrField(err),
			)
			return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: purge stale dedupe record")
		}
	}

	// (a) Claim the request_id (typed constraint detection, review L-2).
	if _, err = tx.ExecContext(ctx,
		"INSERT INTO tool_requests (request_id, lesson_id, tool, result) VALUES (?, ?, 'advance_dictation', '')",
		in.RequestID, in.LessonID,
	); err != nil {
		var serr *sqlite.Error
		if errors.As(err, &serr) && (serr.Code() == lib.SQLITE_CONSTRAINT_PRIMARYKEY ||
			serr.Code() == lib.SQLITE_CONSTRAINT_UNIQUE ||
			serr.Code() == lib.SQLITE_CONSTRAINT) {
			_ = tx.Rollback()
			return e.replayAdvanceDictation(ctx, in)
		}
		e.logger.Error("advance dictation: insert tool_requests failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", in.LessonID),
			infra.F("request_id", in.RequestID),
			infra.ErrField(err),
		)
		return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: insert tool_requests")
	}

	// (b) The authoritative row, read and validated inside this transaction.
	row, err := e.loadDictationRowTx(ctx, tx, in.LessonID)
	if err != nil {
		return AdvanceDictationResult{}, err
	}
	if err := validateDictationRowTx(row, in.TargetIndex); err != nil {
		e.logger.Warn("advance dictation rejected: row validation failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", in.LessonID),
			infra.F("request_id", in.RequestID),
			infra.F("target_index", in.TargetIndex),
		)
		return AdvanceDictationResult{}, err
	}

	// (c) Merge the current word's behavior; every other word's stored
	// outcome is preserved untouched (同连接对齐不抹掉其他词的记录).
	outcomes := row.outcomes
	if outcomes == nil {
		outcomes = map[string]WordOutcome{}
	}
	outcomes[strconv.Itoa(in.TargetIndex)] = in.Outcome
	outJSON, err := json.Marshal(outcomes)
	if err != nil {
		return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: marshal outcomes")
	}

	// (d) The guarded write: one version bump carries the cursor move, the
	// behavior save AND — on the last word — the checking flip, so no cancel
	// can ever strand a lesson between "index exhausted" and "check open".
	// Middle words rewrite the (validated-empty) dictation stage as-is: the
	// version still rises exactly once per word (最后一词版本只增加一次).
	lastWord := in.TargetIndex+1 == row.wordCount
	nextStage := row.stage
	if lastWord {
		nextStage = StageChecking
	}
	result, err := tx.ExecContext(ctx,
		"UPDATE lessons SET current_index = current_index + 1, outcomes = ?, stage = ?, version = version + 1, updated_at = datetime('now') WHERE lesson_id = ? AND version = ? AND current_index = ?",
		string(outJSON), nextStage, in.LessonID, in.ExpectedVersion, in.TargetIndex,
	)
	if err != nil {
		e.logger.Error("advance dictation: update lessons failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", in.LessonID),
			infra.F("request_id", in.RequestID),
			infra.ErrField(err),
		)
		return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: update lessons")
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: rows affected")
	}
	if rows == 0 {
		return AdvanceDictationResult{}, e.advanceDictationGuardFailedTx(ctx, tx, in)
	}

	// (e) Read back, persist the typed result, commit — atomically.
	res, err := e.advanceDictationReadbackTx(ctx, tx, in, row, outcomes)
	if err != nil {
		return AdvanceDictationResult{}, err
	}
	resultJSON, err := json.Marshal(res)
	if err != nil {
		return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: marshal result")
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE tool_requests SET result = ? WHERE request_id = ?",
		string(resultJSON), in.RequestID,
	); err != nil {
		e.logger.Error("advance dictation: store result failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", in.LessonID),
			infra.F("request_id", in.RequestID),
			infra.ErrField(err),
		)
		return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: store result")
	}
	if err := tx.Commit(); err != nil {
		e.logger.Error("advance dictation: commit failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", in.LessonID),
			infra.F("request_id", in.RequestID),
			infra.ErrField(err),
		)
		return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: commit")
	}

	e.logger.Info("advance dictation committed",
		infra.F("component", "lesson/executor"),
		infra.F("lesson_id", in.LessonID),
		infra.F("request_id", in.RequestID),
		infra.F("target_index", in.TargetIndex),
		infra.F("new_version", res.NewVersion),
	)
	return res, nil
}

// advanceDictationGuardFailedTx classifies a 0-rows guarded UPDATE inside
// the open transaction: the row moved under us (version conflict — the
// normal optimistic-lock rejection) or the cursor no longer matches the
// fixed target (a garbled command the load had already cleared; surfaced
// loudly, never retried against another word).
func (e *Executor) advanceDictationGuardFailedTx(ctx context.Context, tx *sql.Tx, in AdvanceDictationInput) error {
	var (
		curVersion int
		curIndex   int
	)
	qerr := tx.QueryRowContext(ctx,
		"SELECT version, current_index FROM lessons WHERE lesson_id = ?",
		in.LessonID,
	).Scan(&curVersion, &curIndex)
	if qerr != nil {
		if errors.Is(qerr, sql.ErrNoRows) {
			return errors.Wrap(ErrLessonNotFound, fmt.Sprintf("advance dictation for lesson %q", in.LessonID))
		}
		return errors.Wrap(qerr, "advance dictation: read current row")
	}
	if curIndex != in.TargetIndex {
		return errors.Wrap(ErrInvalidLesson, fmt.Sprintf("advance dictation: cursor moved to %d under target %d", curIndex, in.TargetIndex))
	}
	e.logger.Warn("advance dictation: version conflict",
		infra.F("component", "lesson/executor"),
		infra.F("lesson_id", in.LessonID),
		infra.F("request_id", in.RequestID),
		infra.F("expected_version", in.ExpectedVersion),
		infra.F("current_version", curVersion),
	)
	return &ConflictError{CurrentVersion: curVersion}
}

// advanceDictationReadbackTx reads the row back inside the open
// transaction and assembles the typed result. The last word's readback
// carries the checking stage its own UPDATE just wrote.
func (e *Executor) advanceDictationReadbackTx(ctx context.Context, tx *sql.Tx, in AdvanceDictationInput, row dictationRowTx, outcomes map[string]WordOutcome) (AdvanceDictationResult, error) {
	var (
		newVersion int
		stage      string
	)
	if err := tx.QueryRowContext(ctx,
		"SELECT version, stage FROM lessons WHERE lesson_id = ?",
		in.LessonID,
	).Scan(&newVersion, &stage); err != nil {
		return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: read advanced lesson")
	}
	return AdvanceDictationResult{
		NewIndex:   in.TargetIndex + 1,
		NewVersion: newVersion,
		Stage:      stage,
		Outcomes:   outcomes,
		LastWord:   in.TargetIndex+1 == row.wordCount,
	}, nil
}

// replayAdvanceDictation serves a duplicate request_id from the FIRST
// stored result (the claim INSERT conflicted and the transaction rolled
// back). Identity first: a record claimed by ANOTHER lesson, or by a tool
// outside the advance family, is never replayed — the collision surfaces
// loudly instead of handing this classroom a foreign result. Same-lesson
// records of the LEGACY advance_word tool replay explicitly AS legacy:
// index and version only, never a guessed stage/outcome/last-word fact the
// old format never had (旧数据不可猜测修复；显式区分新旧格式). An empty
// stored result is the residue of a crashed first write (never success).
// A stored version GREATER than the current row version belongs to a dead
// generation of the row (U-2): *staleRecordError drives the caller's
// purge-and-re-execute — the existing stale-record rule.
func (e *Executor) replayAdvanceDictation(ctx context.Context, in AdvanceDictationInput) (AdvanceDictationResult, error) {
	var (
		resultJSON string
		recLesson  string
		recTool    string
	)
	err := e.store.db.QueryRowContext(ctx,
		"SELECT result, lesson_id, tool FROM tool_requests WHERE request_id = ?",
		in.RequestID,
	).Scan(&resultJSON, &recLesson, &recTool)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdvanceDictationResult{}, errors.Wrap(ErrLessonNotFound, fmt.Sprintf("advance dictation for lesson %q", in.LessonID))
		}
		return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: read idempotent record")
	}
	if recLesson != in.LessonID || (recTool != "advance_dictation" && recTool != "advance_word") {
		e.logger.Error("advance dictation: dedupe record identity mismatch",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", in.LessonID),
			infra.F("request_id", in.RequestID),
			infra.F("record_lesson_id", recLesson),
			infra.F("record_tool", recTool),
		)
		return AdvanceDictationResult{}, errors.Wrap(ErrInvalidLesson, "advance dictation: request_id already claimed by another lesson/tool")
	}
	if resultJSON == "" {
		e.logger.Warn("advance dictation: idempotent hit with empty stored result",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", in.LessonID),
			infra.F("request_id", in.RequestID),
		)
		return AdvanceDictationResult{}, errors.Wrap(ErrInvalidLesson, "advance dictation: first execution left no stored result")
	}

	var res AdvanceDictationResult
	if recTool == "advance_word" {
		// Legacy format: the old result knew index and version only.
		var legacy AdvanceResult
		if uerr := json.Unmarshal([]byte(resultJSON), &legacy); uerr != nil {
			return AdvanceDictationResult{}, errors.Wrap(uerr, "advance dictation: unmarshal legacy result")
		}
		res = AdvanceDictationResult{NewIndex: legacy.NewIndex, NewVersion: legacy.NewVersion}
	} else {
		if uerr := json.Unmarshal([]byte(resultJSON), &res); uerr != nil {
			return AdvanceDictationResult{}, errors.Wrap(uerr, "advance dictation: unmarshal stored result")
		}
	}

	var currentVersion int
	if err := e.store.db.QueryRowContext(ctx,
		"SELECT version FROM lessons WHERE lesson_id = ?",
		in.LessonID,
	).Scan(&currentVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			e.logger.Error("advance dictation: idempotent hit with no lessons row",
				infra.F("component", "lesson/executor"),
				infra.F("lesson_id", in.LessonID),
				infra.F("request_id", in.RequestID),
				infra.F("stored_new_version", res.NewVersion),
			)
			return AdvanceDictationResult{}, errors.Wrap(ErrLessonNotFound, fmt.Sprintf("advance dictation for lesson %q", in.LessonID))
		}
		return AdvanceDictationResult{}, errors.Wrap(err, "advance dictation: read current lesson version")
	}
	if res.NewVersion > currentVersion {
		e.logger.Error("advance dictation: stale dedupe record (lesson reset/rolled back), purging and re-executing",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", in.LessonID),
			infra.F("request_id", in.RequestID),
			infra.F("stored_new_version", res.NewVersion),
			infra.F("current_lesson_version", currentVersion),
		)
		return AdvanceDictationResult{}, &staleRecordError{StoredNewVersion: res.NewVersion, CurrentVersion: currentVersion}
	}
	res.Idempotent = true
	e.logger.Info("advance dictation idempotent hit",
		infra.F("component", "lesson/executor"),
		infra.F("lesson_id", in.LessonID),
		infra.F("request_id", in.RequestID),
		infra.F("stored_new_version", res.NewVersion),
		infra.F("legacy", recTool == "advance_word"),
	)
	return res, nil
}
