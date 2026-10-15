package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Sentinel errors for SessionRepository. Callers MUST branch on these via
// errors.Is rather than string matching (D-05 rich error handling).
var (
	// ErrSessionNotFound is returned by GetByID when no session exists with
	// the given id. Detectable via errors.Is, mirroring infra.ErrConnectionNotFound.
	ErrSessionNotFound = errors.New("session not found")

	// ErrEmptyConnectionID is returned by Create when the session's
	// ConnectionID is empty (input validation, V5).
	ErrEmptyConnectionID = errors.New("session: connection_id must not be empty")
)

// SessionResult is one persisted grading outcome: the session row joined
// with its batch_graded event (Ticket 06). Payload is the raw JSON the
// grading handler wrote — per-word results (word, heard, correct, near-miss)
// and counts — so trend queries never re-derive it. The char diff is
// deliberately absent: it re-derives purely from word+heard.
type SessionResult struct {
	CreatedAt string
	Payload   string
	SessionID int64
}

// SessionRepository manages Go-owned session bookkeeping tables
// (sessions, session_events). Write-separation contract (D-03-a):
// Python never writes to these tables; SessionRepository is the sole
// Go write path for session lifecycle.
//
// Read-only by design is NOT applicable here — SessionRepository
// performs full CRUD on Go-owned session tables.
type SessionRepository interface {
	// Create inserts a new session record, returning its auto-generated ID.
	// Returns ErrEmptyConnectionID if session.ConnectionID is empty.
	Create(ctx context.Context, session Session) (int64, error)

	// UpdateState transitions a session to the given lifecycle state.
	UpdateState(ctx context.Context, id int64, state SessionState) error

	// AppendEvent records a session lifecycle event.
	AppendEvent(ctx context.Context, event SessionEvent) error

	// GetByID returns the session with the given database ID.
	// Returns ErrSessionNotFound when no session exists.
	GetByID(ctx context.Context, id int64) (Session, error)

	// ListRecentResults returns up to limit graded sessions, newest first,
	// each carrying its batch_graded event payload (Ticket 06 trends and
	// repeat-miss analysis). limit <= 0 returns an empty slice, no error.
	ListRecentResults(ctx context.Context, limit int) ([]SessionResult, error)
}

// Compile-time interface satisfaction check (D-43, mirrors pybridge pattern).
var _ SessionRepository = (*sqliteSessionRepository)(nil)

// sqliteSessionRepository is the concrete SessionRepository implementation
// backed by *sql.DB (modernc.org/sqlite).
type sqliteSessionRepository struct {
	db     *sql.DB
	logger infra.Logger
}

// NewSessionRepository constructs a SessionRepository from an open *sql.DB.
// Returns an error if db is nil (D-47 nil-guard). If logger is nil,
// infra.NewNoOpLogger() is substituted so the repository is always safe to use.
func NewSessionRepository(db *sql.DB, logger infra.Logger) (SessionRepository, error) {
	if db == nil {
		return nil, errors.Wrap(ErrSessionNotFound, "database/session_repo: *sql.DB must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &sqliteSessionRepository{db: db, logger: logger}, nil
}

// Create inserts a new session record. The ConnectionID must be non-empty
// (input validation, V5). Returns the auto-generated row ID on success.
func (r *sqliteSessionRepository) Create(ctx context.Context, session Session) (int64, error) {
	if session.ConnectionID == "" {
		return 0, errors.Wrap(ErrEmptyConnectionID, "create session")
	}

	result, err := r.db.ExecContext(ctx,
		"INSERT INTO sessions (connection_id, state, word_count, metadata) VALUES (?, ?, ?, ?)",
		session.ConnectionID,
		string(session.State),
		session.WordCount,
		session.Metadata,
	)
	if err != nil {
		r.logger.Error("session create failed",
			infra.F("component", "database/session_repo"),
			infra.ErrField(err),
		)
		return 0, errors.Wrap(err, "session create")
	}

	id, err := result.LastInsertId()
	if err != nil {
		r.logger.Error("session create: last insert id",
			infra.F("component", "database/session_repo"),
			infra.ErrField(err),
		)
		return 0, errors.Wrap(err, "session create: last insert id")
	}

	return id, nil
}

// GetByID returns the session with the given database ID.
// Returns ErrSessionNotFound when no session exists.
func (r *sqliteSessionRepository) GetByID(ctx context.Context, id int64) (Session, error) {
	var s Session
	var state string

	err := r.db.QueryRowContext(ctx,
		"SELECT id, connection_id, state, word_count, metadata, created_at, updated_at FROM sessions WHERE id = ?",
		id,
	).Scan(
		&s.ID,
		&s.ConnectionID,
		&state,
		&s.WordCount,
		&s.Metadata,
		&s.CreatedAt,
		&s.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, errors.Wrap(ErrSessionNotFound, fmt.Sprintf("get session by id %d", id))
		}
		r.logger.Error("session get by id failed",
			infra.F("component", "database/session_repo"),
			infra.F("session_id", id),
			infra.ErrField(err),
		)
		return Session{}, errors.Wrap(err, "session get by id")
	}

	s.State = SessionState(state)
	return s, nil
}

// UpdateState transitions a session to the given lifecycle state.
func (r *sqliteSessionRepository) UpdateState(ctx context.Context, id int64, state SessionState) error {
	result, err := r.db.ExecContext(ctx,
		"UPDATE sessions SET state = ?, updated_at = datetime('now') WHERE id = ?",
		string(state),
		id,
	)
	if err != nil {
		r.logger.Error("session update state failed",
			infra.F("component", "database/session_repo"),
			infra.F("session_id", id),
			infra.ErrField(err),
		)
		return errors.Wrap(err, "session update state")
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "session update state: rows affected")
	}
	if rows == 0 {
		return errors.Wrap(ErrSessionNotFound, fmt.Sprintf("update state for session %d", id))
	}

	return nil
}

// AppendEvent inserts a row into session_events linked to the session id.
func (r *sqliteSessionRepository) AppendEvent(ctx context.Context, event SessionEvent) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO session_events (session_id, event_type, payload) VALUES (?, ?, ?)",
		event.SessionID,
		event.EventType,
		event.Payload,
	)
	if err != nil {
		r.logger.Error("session append event failed",
			infra.F("component", "database/session_repo"),
			infra.F("session_id", event.SessionID),
			infra.F("event_type", event.EventType),
			infra.ErrField(err),
		)
		return errors.Wrap(err, "session append event")
	}

	return nil
}

// ListRecentResults returns up to limit sessions carrying a batch_graded
// event, newest first (highest event id first — insertion order within and
// across sessions). Ungraded sessions never appear. limit <= 0 short-circuits
// to an empty slice without touching the database.
func (r *sqliteSessionRepository) ListRecentResults(ctx context.Context, limit int) ([]SessionResult, error) {
	if limit <= 0 {
		return []SessionResult{}, nil
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT s.id, s.created_at, e.payload
		FROM sessions s
		JOIN session_events e ON e.session_id = s.id
		WHERE e.event_type = 'batch_graded'
		ORDER BY e.id DESC
		LIMIT ?`,
		limit,
	)
	if err != nil {
		r.logger.Error("session list recent results failed",
			infra.F("component", "database/session_repo"),
			infra.F("limit", limit),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "session list recent results")
	}
	defer func() { _ = rows.Close() }()

	var out []SessionResult
	for rows.Next() {
		var res SessionResult
		if err := rows.Scan(&res.SessionID, &res.CreatedAt, &res.Payload); err != nil {
			r.logger.Error("session list recent results scan failed",
				infra.F("component", "database/session_repo"),
				infra.ErrField(err),
			)
			return nil, errors.Wrap(err, "session list recent results: scan")
		}
		out = append(out, res)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "session list recent results: rows")
	}
	return out, nil
}
