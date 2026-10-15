// Package database provides the Go-owned database access layer for the
// Home Tutor Agent. It manages session lifecycle tables (sessions,
// session_events) and provides connection management for the shared
// SQLite database.
//
// Write-separation contract (D-03-a):
//   - Python owns writes to: cards, review_logs (managed by vocab_manager.py)
//   - Go owns writes to: sessions, session_events (managed by this package)
//   - Cross-system writes are PROHIBITED (no Go writes to Python tables,
//     no Python writes to Go tables)
//
// The canonical vocabulary/FSRS schema lives in third_party/python/vocab_manager.py
// (lines 103-128). This package does NOT duplicate or modify that schema.
package database

// SessionState represents the lifecycle state of a dictation session.
// Stored as TEXT in the database. The Phase 7 lifecycle is:
//
//	IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK → IDLE
//
// Ticket 02 adds PAUSED: DICTATING ↔ PAUSED (student-driven pause/resume).
// Ticket 05 adds SUBMISSION: DICTATING|WAITING_PARENT → SUBMISSION → FEEDBACK
// (batch answer submission — the student writes all words on paper, then
// submits and grades everything at once).
type SessionState string

const (
	// StateIdle is the initial state — no active dictation.
	StateIdle SessionState = "IDLE"
	// StateListening means the system is playing TTS audio for the student.
	StateListening SessionState = "LISTENING"
	// StateDictating means the student is speaking (recording audio input).
	StateDictating SessionState = "DICTATING"
	// StatePaused means the student has paused dictation. The session can
	// resume from the current word or replay it.
	StatePaused SessionState = "PAUSED"
	// StateWaitingParent means the session is waiting for parent feedback.
	StateWaitingParent SessionState = "WAITING_PARENT"
	// StateSubmission means a batch answer submission is being processed
	// (Ticket 05). Transient by design: grading is a synchronous comparison
	// pass, so the session passes through on its way to FEEDBACK.
	StateSubmission SessionState = "SUBMISSION"
	// StateFeedback means the system is delivering post-session feedback.
	StateFeedback SessionState = "FEEDBACK"
)

// Session represents a dictation session row in the sessions table.
// The sessions table is Go-owned — Python never writes to it.
type Session struct {
	ConnectionID string
	State        SessionState
	Metadata     string // JSON column for Phase 7 extensibility
	CreatedAt    string
	UpdatedAt    string
	ID           int64
	WordCount    int
}

// SessionEvent represents a lifecycle event in the session_events table.
// Each state transition or significant action produces one row.
type SessionEvent struct {
	EventType  string
	Payload    string
	OccurredAt string
	ID         int64
	SessionID  int64
}

// Table DDL constants — Go-owned tables only.
// Mirrors the vocab_manager.py init_db() pattern (CREATE TABLE IF NOT EXISTS).
var (
	sessionsDDL = `CREATE TABLE IF NOT EXISTS sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		connection_id TEXT NOT NULL,
		state TEXT NOT NULL DEFAULT 'IDLE',
		word_count INTEGER NOT NULL DEFAULT 0,
		metadata TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`

	sessionEventsDDL = `CREATE TABLE IF NOT EXISTS session_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id INTEGER NOT NULL,
		event_type TEXT NOT NULL,
		payload TEXT NOT NULL DEFAULT '',
		occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
		FOREIGN KEY (session_id) REFERENCES sessions(id)
	)`

	idxSessionsConnection = `CREATE INDEX IF NOT EXISTS idx_sessions_connection ON sessions(connection_id)`
	idxEventsSession      = `CREATE INDEX IF NOT EXISTS idx_events_session ON session_events(session_id)`
)
