package lesson

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	sqlite "modernc.org/sqlite"
	lib "modernc.org/sqlite/lib"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// ErrVersionConflict is the sentinel behind every optimistic-concurrency
// rejection in AdvanceWord: the lessons row's version moved past the
// caller's expected_version, so the write was refused. Detectable via
// errors.Is; *ConflictError carries the current version.
var ErrVersionConflict = errors.New("lesson: version conflict")

// ConflictError reports an optimistic-concurrency rejection from
// AdvanceWord. CurrentVersion is the version actually stored in the lessons
// row at rejection time, so the caller (13-04 turn manager) can surface it
// back to the LLM for a verbal resolution instead of retrying the write.
type ConflictError struct {
	CurrentVersion int
}

// Error implements the error interface.
func (e *ConflictError) Error() string { return "version conflict" }

// Unwrap returns the ErrVersionConflict sentinel so errors.Is matches.
func (e *ConflictError) Unwrap() error { return ErrVersionConflict }

// staleRecordError reports a duplicate request_id whose stored result was
// produced by a previous generation of the lessons row (U-2): the stored
// NewVersion is greater than the row's CURRENT version. Within one
// generation version only ever increases (UPDATE ... version = version + 1)
// and a committed record always satisfies stored == the lessons version at
// its commit time, so stored > current is only reachable when the row was
// deleted and recreated — the record is stale and must never be replayed as
// an idempotent hit. Detected via errors.As; AdvanceWord responds by
// purging the stale record and re-executing the advance inside one atomic
// retry transaction.
type staleRecordError struct {
	StoredNewVersion int
	CurrentVersion   int
}

// Error implements the error interface.
func (e *staleRecordError) Error() string { return "stale dedupe record" }

// AdvanceResult is the outcome of one advance_word execution. Idempotent is
// false on the first execution and true when a duplicate request_id was
// served from the first stored result (tool_requests.result).
type AdvanceResult struct {
	NewIndex   int
	NewVersion int
	Idempotent bool
}

// Executor runs classroom tools idempotently: request_id is the dedupe key
// (tool_requests PK) and expected_version is the optimistic-concurrency
// guard (UPDATE ... WHERE version=?). Both are enforced in one SQLite
// transaction on the tool-execution side — never via LLM memory (13-CONTEXT).
type Executor struct {
	store  *Store
	logger infra.Logger
}

// NewExecutor constructs an Executor over a lesson Store. Returns an error
// if store is nil (D-47 nil-guard). If logger is nil,
// infra.NewNoOpLogger() is substituted.
func NewExecutor(store *Store, logger infra.Logger) (*Executor, error) {
	if store == nil {
		return nil, errors.Wrap(ErrInvalidLesson, "lesson/executor: store must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &Executor{store: store, logger: logger}, nil
}

// GetLessonState returns the current lessons row. Read tools have no
// idempotency requirement, so R0 records no tool_requests row.
func (e *Executor) GetLessonState(ctx context.Context, lessonID string) (Lesson, error) {
	return e.store.GetLesson(ctx, lessonID)
}

// AdvanceWord moves the lesson cursor forward by one word, idempotently.
//
// All guards run in ONE SQLite transaction (RESEARCH Pattern 5):
//
//  1. INSERT INTO tool_requests — request_id PRIMARY KEY; a duplicate
//     request_id rolls back and returns the FIRST stored result with
//     Idempotent=true (never a second advance).
//  2. UPDATE lessons ... WHERE lesson_id=? AND version=? — 0 affected rows
//     is a version conflict; *ConflictError carries the current version and
//     nothing is written.
//  3. U-2 stale-record guard — a duplicate whose stored NewVersion is
//     greater than the current lessons version comes from a dead generation
//     of the row (lesson reset/rollback; version only ever increases within
//     one generation, so stored > current can never be a legitimate
//     duplicate). It is never replayed: the stale record is DELETEd by
//     primary key and the advance re-executes, atomically in one retry
//     transaction (single retry — see advanceTx).
//
// L-3 (13-01 review): database.Open's SetMaxOpenConns(1) single-connection
// pool means this transaction holds the ONLY connection for its duration,
// briefly blocking /ws frozen-stack session-table writes. The R0
// transaction is three short statements — acceptable, and the U-2 purge
// adds exactly one more primary-key DELETE on the retry path, so the
// short-statement transaction-duration premise is unchanged. Any change
// that lengthens this transaction MUST re-review this premise first.
func (e *Executor) AdvanceWord(ctx context.Context, lessonID string, expectedVersion int, requestID string) (AdvanceResult, error) {
	// T-13-01b: LLM-produced arguments are untrusted; empty identifiers or
	// an impossible version are rejected before any database work.
	if lessonID == "" || requestID == "" || expectedVersion < 1 {
		e.logger.Warn("advance word rejected: invalid arguments",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.F("expected_version", expectedVersion),
		)
		return AdvanceResult{}, errors.Wrap(ErrInvalidLesson, "advance word: lesson_id/request_id must be non-empty and expected_version >= 1")
	}

	res, err := e.advanceTx(ctx, lessonID, expectedVersion, requestID, false)
	var stale *staleRecordError
	if !errors.As(err, &stale) {
		return res, err
	}

	// U-2 self-heal: idempotentHit detected a stale dedupe record (its
	// stored result predates a lesson reset). Retry ONCE with the purge
	// switch. A single retry cannot loop: the single-connection pool
	// serializes write transactions, so nothing can re-create a stale
	// record between detection and retry. A still-stale retry is surfaced
	// to the caller (turn.go's generic advance-failure branch, user-visible
	// as 「课堂进度更新失败，请再试一次」) instead of spinning forever.
	return e.advanceTx(ctx, lessonID, expectedVersion, requestID, true)
}

// advanceTx is one advance_word attempt: the entire original transaction
// body of AdvanceWord (BeginTx through Commit), plus the U-2 purge switch.
// With purgeStale set, the stale tool_requests record is DELETEd by primary
// key BEFORE the INSERT re-claims the request_id, so the purge, the
// re-claim, the advance and the stored result all commit atomically — any
// failure rolls the whole transaction back, including the DELETE (the
// 0-rows UPDATE conflict path unwinds everything, so a failed purge never
// destroys a record for a legitimate replay). Every other statement and log
// line is identical between the first attempt and the purge retry.
func (e *Executor) advanceTx(ctx context.Context, lessonID string, expectedVersion int, requestID string, purgeStale bool) (AdvanceResult, error) {
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		e.logger.Error("advance word: begin tx failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return AdvanceResult{}, errors.Wrap(err, "advance word: begin tx")
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	// U-2 retry: the first attempt hit a stale dedupe record (idempotentHit
	// returned *staleRecordError). Delete it by primary key inside THIS
	// transaction, before the INSERT below re-claims the request_id.
	if purgeStale {
		if _, err = tx.ExecContext(ctx,
			"DELETE FROM tool_requests WHERE request_id = ?",
			requestID,
		); err != nil {
			e.logger.Error("advance word: purge stale dedupe record failed",
				infra.F("component", "lesson/executor"),
				infra.F("lesson_id", lessonID),
				infra.F("request_id", requestID),
				infra.ErrField(err),
			)
			return AdvanceResult{}, errors.Wrap(err, "advance word: purge stale dedupe record")
		}
	}

	// (a) Claim the request_id first. A PRIMARY KEY violation means this
	// request already executed: serve the stored result, never re-advance.
	// Constraint detection is typed (review L-2): errors.As(*sqlite.Error)
	// + Code() against lib constants — never error-string matching.
	// request_id is the table's PRIMARY KEY, so SQLite surfaces
	// SQLITE_CONSTRAINT_PRIMARYKEY (1555); the UNIQUE (2067) and base
	// CONSTRAINT (19) codes are kept for defense in depth against driver
	// code-shape changes.
	if _, err = tx.ExecContext(ctx,
		"INSERT INTO tool_requests (request_id, lesson_id, tool, result) VALUES (?, ?, 'advance_word', '')",
		requestID,
		lessonID,
	); err != nil {
		var serr *sqlite.Error
		if errors.As(err, &serr) && (serr.Code() == lib.SQLITE_CONSTRAINT_PRIMARYKEY ||
			serr.Code() == lib.SQLITE_CONSTRAINT_UNIQUE ||
			serr.Code() == lib.SQLITE_CONSTRAINT) {
			_ = tx.Rollback()
			return e.idempotentHit(ctx, lessonID, requestID)
		}
		e.logger.Error("advance word: insert tool_requests failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return AdvanceResult{}, errors.Wrap(err, "advance word: insert tool_requests")
	}

	// (b) Optimistic concurrency: the UPDATE only lands on the expected
	// version. 0 affected rows = conflict (or missing lesson, read back
	// below to distinguish).
	result, err := tx.ExecContext(ctx,
		"UPDATE lessons SET current_index = current_index + 1, version = version + 1, updated_at = datetime('now') WHERE lesson_id = ? AND version = ?",
		lessonID,
		expectedVersion,
	)
	if err != nil {
		e.logger.Error("advance word: update lessons failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return AdvanceResult{}, errors.Wrap(err, "advance word: update lessons")
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return AdvanceResult{}, errors.Wrap(err, "advance word: rows affected")
	}
	if rows == 0 {
		var currentVersion int
		qerr := tx.QueryRowContext(ctx,
			"SELECT version FROM lessons WHERE lesson_id = ?",
			lessonID,
		).Scan(&currentVersion)
		if qerr != nil {
			if errors.Is(qerr, sql.ErrNoRows) {
				e.logger.Error("advance word: lesson not found",
					infra.F("component", "lesson/executor"),
					infra.F("lesson_id", lessonID),
					infra.F("request_id", requestID),
				)
				return AdvanceResult{}, errors.Wrap(ErrLessonNotFound, fmt.Sprintf("advance word for lesson %q", lessonID))
			}
			return AdvanceResult{}, errors.Wrap(qerr, "advance word: read current version")
		}
		e.logger.Warn("advance word: version conflict",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.F("expected_version", expectedVersion),
			infra.F("current_version", currentVersion),
		)
		return AdvanceResult{}, &ConflictError{CurrentVersion: currentVersion}
	}

	var (
		newIndex   int
		newVersion int
	)
	if err = tx.QueryRowContext(ctx,
		"SELECT current_index, version FROM lessons WHERE lesson_id = ?",
		lessonID,
	).Scan(&newIndex, &newVersion); err != nil {
		return AdvanceResult{}, errors.Wrap(err, "advance word: read advanced lesson")
	}

	// (c) Persist the first real result so an idempotent hit replays THIS,
	// not a re-derivation.
	resultJSON, err := json.Marshal(AdvanceResult{NewIndex: newIndex, NewVersion: newVersion, Idempotent: false})
	if err != nil {
		return AdvanceResult{}, errors.Wrap(err, "advance word: marshal result")
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE tool_requests SET result = ? WHERE request_id = ?",
		string(resultJSON),
		requestID,
	); err != nil {
		e.logger.Error("advance word: store result failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return AdvanceResult{}, errors.Wrap(err, "advance word: store result")
	}

	if err := tx.Commit(); err != nil {
		e.logger.Error("advance word: commit failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return AdvanceResult{}, errors.Wrap(err, "advance word: commit")
	}

	e.logger.Info("advance word committed",
		infra.F("component", "lesson/executor"),
		infra.F("lesson_id", lessonID),
		infra.F("request_id", requestID),
		infra.F("expected_version", expectedVersion),
		infra.F("new_version", newVersion),
	)
	return AdvanceResult{NewIndex: newIndex, NewVersion: newVersion, Idempotent: false}, nil
}

// idempotentHit serves a duplicate request_id from the first stored result.
// Called after the re-INSERT conflicted and the transaction was rolled
// back, so this reads the previously committed tool_requests row. An empty
// stored result (residue of a first write that crashed before storing its
// result) is treated as ErrInvalidLesson — never as success.
//
// Before replaying, the current lessons version is read and checked against
// the stored result (U-2 guard): version only ever increases within one
// generation of a lessons row, so a stored NewVersion GREATER than the
// current version means the row was deleted and recreated — the record is
// stale, a loud Error is logged (never silent), and *staleRecordError is
// returned for AdvanceWord to purge and re-execute. A stored version ≤ the
// current version is a legitimate duplicate and replays unchanged. When the
// lessons row is gone entirely, ErrLessonNotFound is returned — same
// contract as a first execution against a missing row.
func (e *Executor) idempotentHit(ctx context.Context, lessonID, requestID string) (AdvanceResult, error) {
	var resultJSON string
	err := e.store.db.QueryRowContext(ctx,
		"SELECT result FROM tool_requests WHERE request_id = ?",
		requestID,
	).Scan(&resultJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdvanceResult{}, errors.Wrap(ErrLessonNotFound, fmt.Sprintf("advance word for lesson %q", lessonID))
		}
		return AdvanceResult{}, errors.Wrap(err, "advance word: read idempotent result")
	}
	if resultJSON == "" {
		e.logger.Warn("advance word: idempotent hit with empty stored result",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
		)
		return AdvanceResult{}, errors.Wrap(ErrInvalidLesson, "advance word: first execution left no stored result")
	}

	var stored AdvanceResult
	if err := json.Unmarshal([]byte(resultJSON), &stored); err != nil {
		return AdvanceResult{}, errors.Wrap(err, "advance word: unmarshal stored result")
	}

	// U-2 stale-record guard: replay only if the record belongs to the
	// current generation of this lessons row. See the function doc for the
	// monotonicity argument — stored > current is unreachably stale, never
	// a legitimate duplicate.
	var currentVersion int
	if err := e.store.db.QueryRowContext(ctx,
		"SELECT version FROM lessons WHERE lesson_id = ?",
		lessonID,
	).Scan(&currentVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			e.logger.Error("advance word: idempotent hit with no lessons row",
				infra.F("component", "lesson/executor"),
				infra.F("lesson_id", lessonID),
				infra.F("request_id", requestID),
				infra.F("stored_new_version", stored.NewVersion),
			)
			return AdvanceResult{}, errors.Wrap(ErrLessonNotFound, fmt.Sprintf("advance word for lesson %q", lessonID))
		}
		return AdvanceResult{}, errors.Wrap(err, "advance word: read current lesson version")
	}
	if stored.NewVersion > currentVersion {
		e.logger.Error("advance word: stale dedupe record (lesson reset/rolled back), purging and re-executing",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.F("stored_new_version", stored.NewVersion),
			infra.F("current_lesson_version", currentVersion),
		)
		return AdvanceResult{}, &staleRecordError{StoredNewVersion: stored.NewVersion, CurrentVersion: currentVersion}
	}

	stored.Idempotent = true

	e.logger.Info("advance word idempotent hit",
		infra.F("component", "lesson/executor"),
		infra.F("lesson_id", lessonID),
		infra.F("request_id", requestID),
		infra.F("stored_new_version", stored.NewVersion),
	)
	return stored, nil
}

// ---------------------------------------------------------------------------
// Check-stage writes (13.1-01): the three-stage classroom persists its check
// stage on the lessons row through the SAME idempotent framework as
// AdvanceWord — request_id dedupe (tool_requests PK) + expected_version
// optimistic lock, first-commit/replay/conflict mutually exclusive. The
// checking→submitted stage transition of CompleteCheck doubles as the
// UNIFIED-SUBMIT LATCH (T-13.1-01): only its first commit may drive the one
// FSRS review write (Pattern 4 / D-07).
// ---------------------------------------------------------------------------

// EnterCheckResult is the outcome of one enter_check execution: the row's
// stage after the write ("checking"), the new version, the stored behavior
// snapshot and any verdicts already on the row (a replay of a re-entry).
type EnterCheckResult struct {
	Stage      string                 `json:"stage"`
	NewVersion int                    `json:"new_version"`
	Outcomes   map[string]WordOutcome `json:"outcomes,omitempty"`
	Verdicts   []CheckVerdict         `json:"verdicts,omitempty"`
	Idempotent bool                   `json:"-"`
}

// ApplyVerdictResult is the outcome of one check_verdict execution: the FULL
// stored verdict list (the single render source, RT-07) after the upsert.
type ApplyVerdictResult struct {
	Stage      string         `json:"stage"`
	NewVersion int            `json:"new_version"`
	Verdicts   []CheckVerdict `json:"verdicts"`
	Idempotent bool           `json:"-"`
}

// CompleteCheckResult is the outcome of one check_submit execution: the
// checking→submitted transition IS the unified-submit idempotency latch.
// Verdicts and Outcomes carry the stored readback so the caller assembles
// the unified rating from the row's authoritative state — never from an
// in-memory cache.
type CompleteCheckResult struct {
	Stage      string                 `json:"stage"`
	NewVersion int                    `json:"new_version"`
	Unchecked  bool                   `json:"unchecked"`
	Verdicts   []CheckVerdict         `json:"verdicts"`
	Outcomes   map[string]WordOutcome `json:"outcomes,omitempty"`
	Idempotent bool                   `json:"-"`
}

// checkStageReadback is the common post-write readback of one check-stage
// mutation (the row's authoritative state after the guarded UPDATE).
type checkStageReadback struct {
	Stage      string
	NewVersion int
	Unchecked  bool
	Outcomes   map[string]WordOutcome
	Verdicts   []CheckVerdict
}

// EnterCheck moves the lesson into the check stage (13.1-01, D-01): the last
// word's first advance commit no longer writes ratings — it snapshots the
// per-word behavior (WordOutcome map keyed by word index) into the row and
// flips stage to "checking". Idempotent by requestID (the FSM derives the
// stable "lessonID-enter-check" key, so a re-entry replays the stored row
// state); guarded by expectedVersion (a conflict carries the current
// version so the caller can align from the stored truth).
//
// Same transaction discipline as AdvanceWord: one short SQLite transaction
// (L-3 premise unchanged — three statements plus the verdict readback).
func (e *Executor) EnterCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, outcomes map[string]WordOutcome) (EnterCheckResult, error) {
	if lessonID == "" || requestID == "" || expectedVersion < 1 {
		e.logger.Warn("enter check rejected: invalid arguments",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.F("expected_version", expectedVersion),
		)
		return EnterCheckResult{}, errors.Wrap(ErrInvalidLesson, "enter check: lesson_id/request_id must be non-empty and expected_version >= 1")
	}
	if outcomes == nil {
		outcomes = map[string]WordOutcome{}
	}
	outJSON, err := json.Marshal(outcomes)
	if err != nil {
		return EnterCheckResult{}, errors.Wrap(err, "enter check: marshal outcomes")
	}

	rb, stored, latch, err := e.checkStageTx(ctx, lessonID, expectedVersion, requestID, "enter_check",
		func(tx *sql.Tx) (int64, error) {
			res, execErr := tx.ExecContext(ctx,
				"UPDATE lessons SET stage = ?, outcomes = ?, version = version + 1, updated_at = datetime('now') WHERE lesson_id = ? AND version = ?",
				StageChecking, string(outJSON), lessonID, expectedVersion)
			if execErr != nil {
				return 0, execErr
			}
			return res.RowsAffected()
		},
		func(rb checkStageReadback) (any, error) {
			return EnterCheckResult{Stage: rb.Stage, NewVersion: rb.NewVersion, Outcomes: rb.Outcomes, Verdicts: rb.Verdicts}, nil
		})
	if err != nil {
		return EnterCheckResult{}, err
	}
	res := EnterCheckResult{Stage: rb.Stage, NewVersion: rb.NewVersion, Outcomes: rb.Outcomes, Verdicts: rb.Verdicts}
	if latch {
		// Not reachable for enter_check (no stage gate on its UPDATE), kept
		// for exhaustiveness: surface the stored row state as a replay.
		res.Idempotent = true
		return res, nil
	}
	if stored != "" {
		if uerr := json.Unmarshal([]byte(stored), &res); uerr != nil {
			return EnterCheckResult{}, errors.Wrap(uerr, "enter check: unmarshal stored result")
		}
	}
	res.Idempotent = res.Idempotent || stored != "" || latch
	return res, nil
}

// ApplyVerdict records one check-stage verdict (13.1-01): the verdict is
// upserted into the row's verdicts JSON — a legitimate 对→错→对 flip arrives
// under a NEW request_id and replaces the stored verdict for that word index
// (the latest human call wins), while a retried frame reusing the SAME
// request_id replays the first stored result without a second write
// (RT-07: taps and voice corrections share this one server-side path).
// Guarded by expectedVersion like every classroom write.
func (e *Executor) ApplyVerdict(ctx context.Context, lessonID string, expectedVersion int, requestID string, verdict CheckVerdict) (ApplyVerdictResult, error) {
	if lessonID == "" || requestID == "" || expectedVersion < 1 {
		e.logger.Warn("apply verdict rejected: invalid arguments",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.F("expected_version", expectedVersion),
		)
		return ApplyVerdictResult{}, errors.Wrap(ErrInvalidLesson, "apply verdict: lesson_id/request_id must be non-empty and expected_version >= 1")
	}

	rb, stored, latch, err := e.checkStageTx(ctx, lessonID, expectedVersion, requestID, "apply_verdict",
		func(tx *sql.Tx) (int64, error) {
			// Read the current verdicts (plain read — the optimistic lock is
			// enforced by the UPDATE below), upsert by word index, write back.
			var curJSON string
			if err := tx.QueryRowContext(ctx,
				"SELECT verdicts FROM lessons WHERE lesson_id = ?",
				lessonID,
			).Scan(&curJSON); err != nil {
				return 0, err
			}
			cur, err := decodeVerdictsJSON(curJSON)
			if err != nil {
				return 0, err
			}
			replaced := false
			for i := range cur {
				if cur[i].WordIndex == verdict.WordIndex {
					cur[i] = verdict
					replaced = true
					break
				}
			}
			if !replaced {
				cur = append(cur, verdict)
			}
			newJSON, err := json.Marshal(cur)
			if err != nil {
				return 0, err
			}
			res, err := tx.ExecContext(ctx,
				"UPDATE lessons SET verdicts = ?, version = version + 1, updated_at = datetime('now') WHERE lesson_id = ? AND version = ?",
				string(newJSON), lessonID, expectedVersion)
			if err != nil {
				return 0, err
			}
			return res.RowsAffected()
		},
		func(rb checkStageReadback) (any, error) {
			return ApplyVerdictResult{Stage: rb.Stage, NewVersion: rb.NewVersion, Verdicts: rb.Verdicts}, nil
		})
	if err != nil {
		return ApplyVerdictResult{}, err
	}
	res := ApplyVerdictResult{Stage: rb.Stage, NewVersion: rb.NewVersion, Verdicts: rb.Verdicts}
	if latch {
		res.Idempotent = true
		return res, nil
	}
	if stored != "" {
		if uerr := json.Unmarshal([]byte(stored), &res); uerr != nil {
			return ApplyVerdictResult{}, errors.Wrap(uerr, "apply verdict: unmarshal stored result")
		}
		res.Idempotent = true
	}
	return res, nil
}

// CompleteCheck executes the unified-submit transition (13.1-01, D-07): the
// row flips stage checking→submitted and stores the unchecked flag (D-03;
// 13.1-04's fallback path writes unchecked=true here). THIS TRANSITION IS
// THE LATCH: the UPDATE carries `AND stage = 'checking'`, so a second
// submission under a new request_id cannot pass — it surfaces as an
// idempotent no-op carrying the stored state, never a second submission
// path (T-13.1-01). Same-request_id replays return the first stored result.
func (e *Executor) CompleteCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, unchecked bool) (CompleteCheckResult, error) {
	if lessonID == "" || requestID == "" || expectedVersion < 1 {
		e.logger.Warn("complete check rejected: invalid arguments",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.F("expected_version", expectedVersion),
		)
		return CompleteCheckResult{}, errors.Wrap(ErrInvalidLesson, "complete check: lesson_id/request_id must be non-empty and expected_version >= 1")
	}
	uncheckedInt := 0
	if unchecked {
		uncheckedInt = 1
	}

	rb, stored, latch, err := e.checkStageTx(ctx, lessonID, expectedVersion, requestID, "complete_check",
		func(tx *sql.Tx) (int64, error) {
			res, err := tx.ExecContext(ctx,
				"UPDATE lessons SET stage = ?, unchecked = ?, version = version + 1, updated_at = datetime('now') WHERE lesson_id = ? AND version = ? AND stage = ?",
				StageSubmitted, uncheckedInt, lessonID, expectedVersion, StageChecking)
			if err != nil {
				return 0, err
			}
			return res.RowsAffected()
		},
		func(rb checkStageReadback) (any, error) {
			return CompleteCheckResult{Stage: rb.Stage, NewVersion: rb.NewVersion, Unchecked: rb.Unchecked, Verdicts: rb.Verdicts, Outcomes: rb.Outcomes}, nil
		})
	if err != nil {
		return CompleteCheckResult{}, err
	}
	res := CompleteCheckResult{Stage: rb.Stage, NewVersion: rb.NewVersion, Unchecked: rb.Unchecked, Verdicts: rb.Verdicts, Outcomes: rb.Outcomes}
	if latch {
		// The latch already fell (a second submission under a new
		// request_id): an idempotent no-op with the stored row state.
		res.Idempotent = true
		return res, nil
	}
	if stored != "" {
		if uerr := json.Unmarshal([]byte(stored), &res); uerr != nil {
			return CompleteCheckResult{}, errors.Wrap(uerr, "complete check: unmarshal stored result")
		}
		res.Idempotent = true
	}
	return res, nil
}

// ClearUncheckedResult is the outcome of one clear_unchecked execution
// (13.1-04): the unchecked marker is gone and the row version moved.
type ClearUncheckedResult struct {
	Stage      string `json:"stage"`
	NewVersion int    `json:"new_version"`
	Idempotent bool   `json:"-"`
}

// ClearUnchecked resets the lessons row's unchecked marker (13.1-04): the
// correction path flips it once every word is human-confirmed and the
// diff-only review correction is recorded. Same idempotent framework as the
// other classroom tools — request_id dedupe (tool_requests PK) plus the
// expected_version optimistic lock. There is deliberately NO stage gate (the
// row is already 'submitted'); the UPDATE carries `AND unchecked = 1`, so an
// already-cleared row under a NEW request_id is an idempotent no-op instead
// of a second version bump, and a stale expected_version is a conflict that
// writes nothing. Same short-transaction discipline as AdvanceWord (L-3).
func (e *Executor) ClearUnchecked(ctx context.Context, lessonID string, expectedVersion int, requestID string) (ClearUncheckedResult, error) {
	if lessonID == "" || requestID == "" || expectedVersion < 1 {
		e.logger.Warn("clear unchecked rejected: invalid arguments",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.F("expected_version", expectedVersion),
		)
		return ClearUncheckedResult{}, errors.Wrap(ErrInvalidLesson, "clear unchecked: lesson_id/request_id must be non-empty and expected_version >= 1")
	}

	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		e.logger.Error("clear unchecked: begin tx failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return ClearUncheckedResult{}, errors.Wrap(err, "clear unchecked: begin tx")
	}
	defer func() { _ = tx.Rollback() }()

	// (a) Claim the request_id (typed constraint detection, review L-2). A
	// duplicate replays the FIRST stored result, never a second write.
	if _, err = tx.ExecContext(ctx,
		"INSERT INTO tool_requests (request_id, lesson_id, tool, result) VALUES (?, ?, 'clear_unchecked', '')",
		requestID, lessonID,
	); err != nil {
		var serr *sqlite.Error
		if errors.As(err, &serr) && (serr.Code() == lib.SQLITE_CONSTRAINT_PRIMARYKEY ||
			serr.Code() == lib.SQLITE_CONSTRAINT_UNIQUE ||
			serr.Code() == lib.SQLITE_CONSTRAINT) {
			_ = tx.Rollback()
			stored, gerr := e.storedCheckResult(ctx, lessonID, requestID, "clear_unchecked")
			if gerr != nil {
				return ClearUncheckedResult{}, gerr
			}
			var res ClearUncheckedResult
			if uerr := json.Unmarshal([]byte(stored), &res); uerr != nil {
				return ClearUncheckedResult{}, errors.Wrap(uerr, "clear unchecked: unmarshal stored result")
			}
			res.Idempotent = true
			return res, nil
		}
		e.logger.Error("clear unchecked: insert tool_requests failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return ClearUncheckedResult{}, errors.Wrap(err, "clear unchecked: insert tool_requests")
	}

	// (b) The guarded write: optimistic lock plus the unchecked=1 gate.
	result, err := tx.ExecContext(ctx,
		"UPDATE lessons SET unchecked = 0, version = version + 1, updated_at = datetime('now') WHERE lesson_id = ? AND version = ? AND unchecked = 1",
		lessonID, expectedVersion,
	)
	if err != nil {
		e.logger.Error("clear unchecked: update lessons failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return ClearUncheckedResult{}, errors.Wrap(err, "clear unchecked: update lessons")
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return ClearUncheckedResult{}, errors.Wrap(err, "clear unchecked: rows affected")
	}
	if rows == 0 {
		// Distinguish already-cleared (idempotent no-op) from version
		// conflict — both leave the row untouched, only the verdict differs.
		var curVersion int
		var curStage string
		var curUnchecked int
		qerr := tx.QueryRowContext(ctx,
			"SELECT version, stage, unchecked FROM lessons WHERE lesson_id = ?",
			lessonID,
		).Scan(&curVersion, &curStage, &curUnchecked)
		if qerr != nil {
			if errors.Is(qerr, sql.ErrNoRows) {
				e.logger.Error("clear unchecked: lesson not found",
					infra.F("component", "lesson/executor"),
					infra.F("lesson_id", lessonID),
					infra.F("request_id", requestID),
				)
				return ClearUncheckedResult{}, errors.Wrap(ErrLessonNotFound, fmt.Sprintf("clear unchecked for lesson %q", lessonID))
			}
			return ClearUncheckedResult{}, errors.Wrap(qerr, "clear unchecked: read current version")
		}
		if curUnchecked == 0 {
			// Already cleared (a lost response retried under a fresh
			// request_id, or a racing clear): an idempotent no-op. The
			// request claim rolls back with the transaction — no empty
			// result residue is left behind.
			e.logger.Info("clear unchecked: already cleared, replaying stored state",
				infra.F("component", "lesson/executor"),
				infra.F("lesson_id", lessonID),
				infra.F("request_id", requestID),
			)
			return ClearUncheckedResult{Stage: curStage, NewVersion: curVersion, Idempotent: true}, nil
		}
		e.logger.Warn("clear unchecked: version conflict",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.F("expected_version", expectedVersion),
			infra.F("current_version", curVersion),
		)
		return ClearUncheckedResult{}, &ConflictError{CurrentVersion: curVersion}
	}

	// (c) Read back, persist the typed result, commit — atomically with the
	// marker reset above.
	rb, err := readCheckStageTx(ctx, tx, lessonID)
	if err != nil {
		return ClearUncheckedResult{}, errors.Wrap(err, "clear unchecked: read back")
	}
	res := ClearUncheckedResult{Stage: rb.Stage, NewVersion: rb.NewVersion}
	resultJSON, err := json.Marshal(res)
	if err != nil {
		return ClearUncheckedResult{}, errors.Wrap(err, "clear unchecked: marshal result")
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE tool_requests SET result = ? WHERE request_id = ?",
		string(resultJSON), requestID,
	); err != nil {
		e.logger.Error("clear unchecked: store result failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return ClearUncheckedResult{}, errors.Wrap(err, "clear unchecked: store result")
	}
	if err := tx.Commit(); err != nil {
		e.logger.Error("clear unchecked: commit failed",
			infra.F("component", "lesson/executor"),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return ClearUncheckedResult{}, errors.Wrap(err, "clear unchecked: commit")
	}

	e.logger.Info("clear unchecked committed",
		infra.F("component", "lesson/executor"),
		infra.F("lesson_id", lessonID),
		infra.F("request_id", requestID),
		infra.F("expected_version", expectedVersion),
		infra.F("new_version", rb.NewVersion),
	)
	return res, nil
}

// checkStageTx is the shared transaction skeleton of the three check-stage
// tools, mirroring advanceTx: (a) claim request_id in tool_requests (a
// duplicate rolls back and replays the FIRST stored result), (b) apply the
// caller's guarded lessons UPDATE (rows==0 → latch replay when the row is
// already submitted, otherwise *ConflictError / ErrLessonNotFound), (c) read
// the row back, persist the typed result JSON, commit — all atomic.
func (e *Executor) checkStageTx(
	ctx context.Context,
	lessonID string,
	expectedVersion int,
	requestID, tool string,
	apply func(tx *sql.Tx) (int64, error),
	resultFor func(rb checkStageReadback) (any, error),
) (rb checkStageReadback, storedResult string, latchReplay bool, err error) {
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		e.logger.Error("check stage: begin tx failed",
			infra.F("component", "lesson/executor"),
			infra.F("tool", tool),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return checkStageReadback{}, "", false, errors.Wrap(err, tool+": begin tx")
	}
	defer func() { _ = tx.Rollback() }()

	// (a) Claim the request_id (typed constraint detection, review L-2).
	if _, err = tx.ExecContext(ctx,
		"INSERT INTO tool_requests (request_id, lesson_id, tool, result) VALUES (?, ?, ?, '')",
		requestID, lessonID, tool,
	); err != nil {
		var serr *sqlite.Error
		if errors.As(err, &serr) && (serr.Code() == lib.SQLITE_CONSTRAINT_PRIMARYKEY ||
			serr.Code() == lib.SQLITE_CONSTRAINT_UNIQUE ||
			serr.Code() == lib.SQLITE_CONSTRAINT) {
			_ = tx.Rollback()
			stored, gerr := e.storedCheckResult(ctx, lessonID, requestID, tool)
			if gerr != nil {
				return checkStageReadback{}, "", false, gerr
			}
			return checkStageReadback{}, stored, false, nil
		}
		e.logger.Error("check stage: insert tool_requests failed",
			infra.F("component", "lesson/executor"),
			infra.F("tool", tool),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return checkStageReadback{}, "", false, errors.Wrap(err, tool+": insert tool_requests")
	}

	// (b) The guarded lessons write.
	rows, err := apply(tx)
	if err != nil {
		e.logger.Error("check stage: lessons update failed",
			infra.F("component", "lesson/executor"),
			infra.F("tool", tool),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return checkStageReadback{}, "", false, errors.Wrap(err, tool+": update lessons")
	}
	if rows == 0 {
		var curVersion int
		var curStage string
		var curUnchecked int
		qerr := tx.QueryRowContext(ctx,
			"SELECT version, stage, unchecked FROM lessons WHERE lesson_id = ?",
			lessonID,
		).Scan(&curVersion, &curStage, &curUnchecked)
		if qerr != nil {
			if errors.Is(qerr, sql.ErrNoRows) {
				e.logger.Error("check stage: lesson not found",
					infra.F("component", "lesson/executor"),
					infra.F("tool", tool),
					infra.F("lesson_id", lessonID),
					infra.F("request_id", requestID),
				)
				return checkStageReadback{}, "", false, errors.Wrap(ErrLessonNotFound, fmt.Sprintf("%s for lesson %q", tool, lessonID))
			}
			return checkStageReadback{}, "", false, errors.Wrap(qerr, tool+": read current version")
		}
		if curStage == StageSubmitted {
			// The unified-submit latch already fell: an idempotent no-op
			// carrying the stored state — never a second submission path.
			e.logger.Info("check stage: latch already submitted, replaying stored state",
				infra.F("component", "lesson/executor"),
				infra.F("tool", tool),
				infra.F("lesson_id", lessonID),
				infra.F("request_id", requestID),
			)
			return checkStageReadback{Stage: curStage, NewVersion: curVersion, Unchecked: curUnchecked != 0}, "", true, nil
		}
		e.logger.Warn("check stage: version conflict",
			infra.F("component", "lesson/executor"),
			infra.F("tool", tool),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.F("expected_version", expectedVersion),
			infra.F("current_version", curVersion),
		)
		return checkStageReadback{}, "", false, &ConflictError{CurrentVersion: curVersion}
	}

	// (c) Read back, persist the typed result, commit.
	rb, err = readCheckStageTx(ctx, tx, lessonID)
	if err != nil {
		return checkStageReadback{}, "", false, errors.Wrap(err, tool+": read back")
	}
	res, err := resultFor(rb)
	if err != nil {
		return checkStageReadback{}, "", false, errors.Wrap(err, tool+": build result")
	}
	resultJSON, err := json.Marshal(res)
	if err != nil {
		return checkStageReadback{}, "", false, errors.Wrap(err, tool+": marshal result")
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE tool_requests SET result = ? WHERE request_id = ?",
		string(resultJSON), requestID,
	); err != nil {
		e.logger.Error("check stage: store result failed",
			infra.F("component", "lesson/executor"),
			infra.F("tool", tool),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return checkStageReadback{}, "", false, errors.Wrap(err, tool+": store result")
	}
	if err := tx.Commit(); err != nil {
		e.logger.Error("check stage: commit failed",
			infra.F("component", "lesson/executor"),
			infra.F("tool", tool),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.ErrField(err),
		)
		return checkStageReadback{}, "", false, errors.Wrap(err, tool+": commit")
	}

	e.logger.Info("check stage committed",
		infra.F("component", "lesson/executor"),
		infra.F("tool", tool),
		infra.F("lesson_id", lessonID),
		infra.F("request_id", requestID),
		infra.F("expected_version", expectedVersion),
		infra.F("new_version", rb.NewVersion),
	)
	return rb, "", false, nil
}

// storedCheckResult reads a prior execution's stored result for replay: an
// empty stored result is the residue of a crashed first write (never
// success), and a stored version GREATER than the current row version
// belongs to a dead generation of the row (U-2 analog) — it is never
// replayed; check tools are re-derivable from the row, so the conflict
// surfaces instead of a purge path.
func (e *Executor) storedCheckResult(ctx context.Context, lessonID, requestID, tool string) (string, error) {
	var resultJSON string
	err := e.store.db.QueryRowContext(ctx,
		"SELECT result FROM tool_requests WHERE request_id = ?",
		requestID,
	).Scan(&resultJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.Wrap(ErrLessonNotFound, fmt.Sprintf("%s for lesson %q", tool, lessonID))
		}
		return "", errors.Wrap(err, tool+": read idempotent result")
	}
	if resultJSON == "" {
		e.logger.Warn("check stage: idempotent hit with empty stored result",
			infra.F("component", "lesson/executor"),
			infra.F("tool", tool),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
		)
		return "", errors.Wrap(ErrInvalidLesson, tool+": first execution left no stored result")
	}

	var peek struct {
		NewVersion int `json:"new_version"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &peek); err != nil {
		return "", errors.Wrap(err, tool+": unmarshal stored result")
	}
	var currentVersion int
	if err := e.store.db.QueryRowContext(ctx,
		"SELECT version FROM lessons WHERE lesson_id = ?",
		lessonID,
	).Scan(&currentVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			e.logger.Error("check stage: idempotent hit with no lessons row",
				infra.F("component", "lesson/executor"),
				infra.F("tool", tool),
				infra.F("lesson_id", lessonID),
				infra.F("request_id", requestID),
			)
			return "", errors.Wrap(ErrLessonNotFound, fmt.Sprintf("%s for lesson %q", tool, lessonID))
		}
		return "", errors.Wrap(err, tool+": read current lesson version")
	}
	if peek.NewVersion > currentVersion {
		e.logger.Error("check stage: stale dedupe record (lesson reset/rolled back), refusing replay",
			infra.F("component", "lesson/executor"),
			infra.F("tool", tool),
			infra.F("lesson_id", lessonID),
			infra.F("request_id", requestID),
			infra.F("stored_new_version", peek.NewVersion),
			infra.F("current_lesson_version", currentVersion),
		)
		return "", errors.Wrap(ErrVersionConflict, tool+": stale dedupe record")
	}
	return resultJSON, nil
}

// readCheckStageTx reads the row's check-stage state inside the open
// transaction (the authoritative post-write readback).
func readCheckStageTx(ctx context.Context, tx *sql.Tx, lessonID string) (checkStageReadback, error) {
	var (
		rb           checkStageReadback
		unchecked    int
		outcomesJSON string
		verdictsJSON string
	)
	if err := tx.QueryRowContext(ctx,
		"SELECT stage, version, unchecked, outcomes, verdicts FROM lessons WHERE lesson_id = ?",
		lessonID,
	).Scan(&rb.Stage, &rb.NewVersion, &unchecked, &outcomesJSON, &verdictsJSON); err != nil {
		return checkStageReadback{}, err
	}
	rb.Unchecked = unchecked != 0
	outcomes, err := decodeOutcomesJSON(outcomesJSON)
	if err != nil {
		return checkStageReadback{}, err
	}
	rb.Outcomes = outcomes
	verdicts, err := decodeVerdictsJSON(verdictsJSON)
	if err != nil {
		return checkStageReadback{}, err
	}
	rb.Verdicts = verdicts
	return rb, nil
}
