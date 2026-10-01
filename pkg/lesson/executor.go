package lesson

import (
	"context"

	"github.com/pkg/errors"
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
// RED stub — implemented in the GREEN commit.
func (e *Executor) GetLessonState(ctx context.Context, lessonID string) (Lesson, error) {
	return Lesson{}, errors.New("lesson: GetLessonState not implemented (RED stub)")
}

// AdvanceWord moves the lesson cursor forward by one word.
// RED stub — implemented in the GREEN commit.
func (e *Executor) AdvanceWord(ctx context.Context, lessonID string, expectedVersion int, requestID string) (AdvanceResult, error) {
	return AdvanceResult{}, errors.New("lesson: AdvanceWord not implemented (RED stub)")
}
