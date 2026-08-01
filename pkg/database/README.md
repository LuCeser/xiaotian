# pkg/database — Go-Owned Database Access Layer

## Overview

This package provides the Go-owned database access layer for the Home Tutor Agent. It manages the shared SQLite database file (`vocab.db`) used by both Go and Python processes.

## Write-Separation Contract (D-03-a)

**Python owns writes to:**
- `cards` table — vocabulary words and FSRS state
- `review_logs` table — parent feedback and review history

**Go owns writes to:**
- `sessions` table — dictation session lifecycle
- `session_events` table — session state transitions and events

**Cross-system writes are PROHIBITED:**
- Go MUST NEVER write to `cards` or `review_logs` (D-01/D-02)
- Python MUST NEVER write to `sessions` or `session_events`
- All FSRS state changes flow through `pybridge.Executor.RunReview` (Phase 3 D-09)

## Schema Architecture

### Go-Owned Tables

**sessions** — Dictation session lifecycle tracking
- `id` INTEGER PRIMARY KEY AUTOINCREMENT
- `connection_id` TEXT NOT NULL — WebSocket connection identifier
- `state` TEXT NOT NULL DEFAULT 'IDLE' — SessionState (IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK)
- `word_count` INTEGER NOT NULL DEFAULT 0 — Number of words in the session
- `metadata` TEXT NOT NULL DEFAULT '{}' — JSON column for Phase 7 extensibility
- `created_at` TEXT NOT NULL DEFAULT (datetime('now'))
- `updated_at` TEXT NOT NULL DEFAULT (datetime('now'))
- Index: `idx_sessions_connection` ON sessions(connection_id)

**session_events** — Session lifecycle events
- `id` INTEGER PRIMARY KEY AUTOINCREMENT
- `session_id` INTEGER NOT NULL — Foreign key to sessions(id)
- `event_type` TEXT NOT NULL — Event classification
- `payload` TEXT NOT NULL DEFAULT '' — Event data
- `occurred_at` TEXT NOT NULL DEFAULT (datetime('now'))
- Index: `idx_events_session` ON session_events(session_id)

### Python-Owned Tables (DO NOT MODIFY)

**cards** — Vocabulary words with FSRS state (defined in `vocab_manager.py` lines 103-113)
**review_logs** — Parent feedback and review history (defined in `vocab_manager.py` lines 114-123)

## Connection Management

### Open() Constructor

`Open(dbPath string) (*sql.DB, error)` opens the shared SQLite file with:

- **journal_mode=WAL**: Enables concurrent readers during writes (matches Python)
- **busy_timeout=5000**: Cross-process write contention waits up to 5s (RESEARCH Pitfall 2)
- **foreign_keys=ON**: SQLite defaults to OFF; FK enforcement enabled
- **synchronous=NORMAL**: WAL-appropriate safety/speed balance (RESEARCH Pitfall 3)

### Connection Limits (RESEARCH Pattern 2)

- `SetMaxOpenConns(1)`: Serializes Go DB operations through one connection
- `SetMaxIdleConns(1)`: Matches MaxOpenConns for consistent pool behavior
- `SetConnMaxLifetime(0)`: Connection lives for service lifetime

## Multi-Process WAL Concurrency

Go and Python access the same SQLite database file safely via WAL mode:

1. Python writes `cards`/`review_logs` (via pybridge subprocess, WAL serializes)
2. Go reads `cards` via CardRepository (WAL allows concurrent read during Python write)
3. Go writes `sessions`/`session_events` via SessionRepository (separate tables, no conflict)
4. `busy_timeout` handles brief lock contention between Go and Python writers

## Migration

`Migrate(ctx context.Context, db *sql.DB) error` creates Go-owned tables idempotently:

- Uses `CREATE TABLE IF NOT EXISTS` (mirrors `vocab_manager.py` init_db pattern)
- Safe to call on every boot
- Does NOT touch Python-owned tables (`cards`, `review_logs`)
- Schema drift detection vs Python is a Phase 5 integration-test responsibility (D-08-a)

## Testing

Tests use in-memory SQLite (`:memory:` DSN) with the shared `newTestDB` helper:

```go
db := newTestDB(t) // Returns migrated in-memory *sql.DB with FK enabled
```

This helper is reused by Plan 04's repository tests.

## Canonical Schema References

- **Python schema**: `third_party/python/vocab_manager.py` (lines 103-128) — canonical for `cards`/`review_logs`
- **ADR**: `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` — ownership decision
- **Research**: `.planning/phases/04-database-schema-design/04-RESEARCH.md` — driver choice, concurrency model

## Driver

Uses `modernc.org/sqlite` (pure-Go, CGO-free) registered under driver name `"sqlite"`. CGO_ENABLED=0 is locked in Makefile and CI (Phase 4 Plan 01).
