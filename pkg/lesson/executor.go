package lesson

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	sqlite "modernc.org/sqlite"
	lib "modernc.org/sqlite/lib"
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
// Both guards run in ONE SQLite transaction (RESEARCH Pattern 5):
//
//  1. INSERT INTO tool_requests — request_id PRIMARY KEY; a duplicate
//     request_id rolls back and returns the FIRST stored result with
//     Idempotent=true (never a second advance).
//  2. UPDATE lessons ... WHERE lesson_id=? AND version=? — 0 affected rows
//     is a version conflict; *ConflictError carries the current version and
//     nothing is written.
//
// L-3 (13-01 review): database.Open's SetMaxOpenConns(1) single-connection
// pool means this transaction holds the ONLY connection for its duration,
// briefly blocking /ws frozen-stack session-table writes. The R0
// transaction is three short statements — acceptable. Any change that
// lengthens this transaction MUST re-review this premise first.
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

	// (a) Claim the request_id first. A PRIMARY KEY violation means this
	// request already executed: serve the stored result, never re-advance.
	// Constraint detection is typed (review L-2): errors.As(*sqlite.Error)
	// + Code() against lib constants — never error-string matching.
	// request_id is the table's PRIMARY KEY, so SQLite surfaces
	// SQLITE_CONSTRAINT_PRIMARYKEY (1555); the UNIQUE (2067) and base
	// CONSTRAINT (19) codes are kept for defense in depth against driver
	// code-shape changes.
	if _, err := tx.ExecContext(ctx,
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
	if err := tx.QueryRowContext(ctx,
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
	stored.Idempotent = true

	e.logger.Info("advance word idempotent hit",
		infra.F("component", "lesson/executor"),
		infra.F("lesson_id", lessonID),
		infra.F("request_id", requestID),
		infra.F("stored_new_version", stored.NewVersion),
	)
	return stored, nil
}
