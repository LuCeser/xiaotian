// Package lesson provides the R0 classroom-state and idempotent
// tool-execution layer for the realtime tutor loop (Phase 13). It owns two
// Go-side SQLite tables: lessons (per-lesson word list and progress cursor)
// and tool_requests (one row per executed LLM tool call, keyed by request_id
// for idempotency).
//
// Lesson writes run in short SQLite transactions that are decoupled from the
// voice/cancel stack: cancelling TTS generation never rolls back an
// already-committed classroom tool result (13-CONTEXT).
package lesson

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Sentinel errors for the lesson package. Callers MUST branch on these via
// errors.Is rather than string matching (mirrors database.SessionRepository,
// D-05 rich error handling).
var (
	// ErrLessonNotFound is returned when no lessons row exists for the given
	// lesson_id (GetLesson, Executor.AdvanceWord).
	ErrLessonNotFound = errors.New("lesson: lesson not found")

	// ErrInvalidLesson is returned when caller-supplied arguments are invalid
	// (empty lesson_id, empty words, empty request_id, or a corrupted
	// tool_requests result residue). Nothing is written to the database when
	// it is returned.
	ErrInvalidLesson = errors.New("lesson: invalid lesson arguments")
)

// Table DDL constants — R0 classroom tables, Go-owned (13-PATTERNS.md).
// Mirrors the database package's CREATE TABLE IF NOT EXISTS registration
// pattern (schema.go): words/result columns carry JSON payloads.
var (
	lessonsDDL = `CREATE TABLE IF NOT EXISTS lessons (
    lesson_id TEXT PRIMARY KEY,
    words TEXT NOT NULL,            -- JSON array
    cards TEXT NOT NULL DEFAULT '[]', -- JSON array, elements are Word
    current_index INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
)`

	toolRequestsDDL = `CREATE TABLE IF NOT EXISTS tool_requests (
    request_id TEXT PRIMARY KEY,    -- MiMo tool_call id
    lesson_id TEXT NOT NULL,
    tool TEXT NOT NULL,
    result TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
)`

	idxToolRequestsLesson = `CREATE INDEX IF NOT EXISTS idx_tool_requests_lesson ON tool_requests(lesson_id)`

	// alterLessonsAddCards backfills the cards column on legacy lessons
	// tables created before it existed (13-06 deployments). Purely additive:
	// existing rows keep their data and read cards as the empty '[]' array.
	alterLessonsAddCards = `ALTER TABLE lessons ADD COLUMN cards TEXT NOT NULL DEFAULT '[]'`
)

// Word is one vocabulary card carried with the lesson row: the surface word,
// its meaning, and an example sentence. It is the ground-truth payload the
// server uses for prompts ("不会写" hints) and meaning explanations — the
// LLM never invents these.
//
// The json tags align verbatim with pybridge.DueCard's wire contract
// (word/meaning/example, vocab_manager.py's due output), so the FSRS due
// fetch (13-10 PyFSRS adapter) maps into this type losslessly.
type Word struct {
	Word    string `json:"word"`
	Meaning string `json:"meaning"`
	Example string `json:"example"`
}

// Migrate creates the lesson package's Go-owned tables and index if they do
// not exist, then idempotently backfills the cards column on legacy lessons
// tables that predate it: the live schema is probed via PRAGMA
// table_info(lessons) and ALTER TABLE runs only when the column is absent,
// so VPS databases created during 13-06 gain the column with no manual
// intervention. Idempotent overall: safe to call on every boot, mirroring
// database.Migrate.
func Migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		lessonsDDL,
		toolRequestsDDL,
		idxToolRequestsLesson,
	}

	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("lesson/migrate: %w", err)
		}
	}

	hasCards, err := lessonsHasCardsColumn(ctx, db)
	if err != nil {
		return fmt.Errorf("lesson/migrate: %w", err)
	}
	if !hasCards {
		if _, err := db.ExecContext(ctx, alterLessonsAddCards); err != nil {
			return fmt.Errorf("lesson/migrate: %w", err)
		}
	}

	return nil
}

// lessonsHasCardsColumn reports whether the lessons table already carries
// the cards column.
func lessonsHasCardsColumn(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(lessons)")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == "cards" {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Lesson is one row of the lessons table. Words is the JSON-serialized word
// list on disk ([]string); Cards is the JSON-serialized word-card list
// ([]Word) — the meaning/example ground truth persisted with the classroom
// row so a same-day reconnect does not lose it. Version drives optimistic
// concurrency for advance_word writes (Executor.AdvanceWord).
type Lesson struct {
	LessonID     string
	Words        []string
	Cards        []Word
	CurrentIndex int
	Version      int
	CreatedAt    string
	UpdatedAt    string
}

// Store is the create-once/read path for lessons rows. Repository pattern
// mirrors database.sqliteSessionRepository: nil-guarded constructor,
// NoOpLogger fallback, sentinel errors detected via errors.Is.
type Store struct {
	db     *sql.DB
	logger infra.Logger
}

// NewStore constructs a lesson Store from an open *sql.DB. Returns an error
// if db is nil (D-47 nil-guard). If logger is nil, infra.NewNoOpLogger() is
// substituted so the store is always safe to use.
func NewStore(db *sql.DB, logger infra.Logger) (*Store, error) {
	if db == nil {
		return nil, errors.Wrap(ErrInvalidLesson, "lesson/store: *sql.DB must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &Store{db: db, logger: logger}, nil
}

// EnsureLesson creates the lessons row for lessonID if absent (INSERT OR
// IGNORE: a second call with different words returns the FIRST stored
// content, version untouched) and reads the full row back. Empty lessonID or
// empty words returns ErrInvalidLesson without touching the database.
func (s *Store) EnsureLesson(ctx context.Context, lessonID string, words []string) (Lesson, error) {
	if lessonID == "" || len(words) == 0 {
		return Lesson{}, errors.Wrap(ErrInvalidLesson, "ensure lesson: lesson_id and words must be non-empty")
	}

	wordsJSON, err := json.Marshal(words)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure lesson: marshal words")
	}

	if _, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO lessons (lesson_id, words) VALUES (?, ?)",
		lessonID,
		string(wordsJSON),
	); err != nil {
		s.logger.Error("lesson ensure: insert failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, "ensure lesson: insert")
	}

	created, err := s.GetLesson(ctx, lessonID)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure lesson: read back")
	}
	return created, nil
}

// EnsureLessonCards creates the lessons row for lessonID if absent, carrying
// both the plain word list (extracted from the cards) and the full word-card
// JSON, and reads the full row back. INSERT OR IGNORE semantics identical to
// EnsureLesson: first content wins, a second call with different cards
// returns the first stored content and leaves version untouched.
//
// Boundary (deliberate, tested): INSERT OR IGNORE is NOT a replacement
// mechanism. A pre-existing row with an EMPTY cards column — e.g. a legacy
// row created before cards existed — is not backfilled by a second
// EnsureLessonCards call; the caller (13-08 FSM) must detect that state and
// surface lesson_unavailable instead of indexing empty word cards or
// re-running RunDue. Completed rows are likewise read-only for 13-08; this
// store never rebuilds them.
func (s *Store) EnsureLessonCards(ctx context.Context, lessonID string, cards []Word) (Lesson, error) {
	if lessonID == "" || len(cards) == 0 {
		return Lesson{}, errors.Wrap(ErrInvalidLesson, "ensure lesson cards: lesson_id and cards must be non-empty")
	}

	cardsJSON, err := json.Marshal(cards)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure lesson cards: marshal cards")
	}

	words := make([]string, 0, len(cards))
	for _, c := range cards {
		words = append(words, c.Word)
	}
	wordsJSON, err := json.Marshal(words)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure lesson cards: marshal words")
	}

	if _, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO lessons (lesson_id, words, cards) VALUES (?, ?, ?)",
		lessonID,
		string(wordsJSON),
		string(cardsJSON),
	); err != nil {
		s.logger.Error("lesson ensure cards: insert failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, "ensure lesson cards: insert")
	}

	created, err := s.GetLesson(ctx, lessonID)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure lesson cards: read back")
	}
	return created, nil
}

// GetLesson returns the lessons row for lessonID. Returns ErrLessonNotFound
// when no row exists. A corrupted words or cards column (invalid JSON)
// surfaces as a wrapped unmarshal error.
func (s *Store) GetLesson(ctx context.Context, lessonID string) (Lesson, error) {
	var (
		l         Lesson
		wordsJSON string
		cardsJSON string
	)

	err := s.db.QueryRowContext(ctx,
		"SELECT lesson_id, words, cards, current_index, version, created_at, updated_at FROM lessons WHERE lesson_id = ?",
		lessonID,
	).Scan(
		&l.LessonID,
		&wordsJSON,
		&cardsJSON,
		&l.CurrentIndex,
		&l.Version,
		&l.CreatedAt,
		&l.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Lesson{}, errors.Wrap(ErrLessonNotFound, fmt.Sprintf("get lesson %q", lessonID))
		}
		s.logger.Error("lesson get failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, "get lesson")
	}

	if err := json.Unmarshal([]byte(wordsJSON), &l.Words); err != nil {
		s.logger.Error("lesson get: unmarshal words failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, fmt.Sprintf("get lesson %q: unmarshal words", lessonID))
	}

	if err := json.Unmarshal([]byte(cardsJSON), &l.Cards); err != nil {
		s.logger.Error("lesson get: unmarshal cards failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, fmt.Sprintf("get lesson %q: unmarshal cards", lessonID))
	}

	return l, nil
}
