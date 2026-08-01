package database

import (
	"context"
	"database/sql"
	"fmt"
)

// Migrate creates Go-owned auxiliary tables if they do not exist.
// Idempotent: safe to call on every boot. Does NOT touch Python-owned
// tables (cards, review_logs) — those are managed by vocab_manager.py.
//
// Per D-08: Uses CREATE TABLE IF NOT EXISTS for idempotent migration,
// mirroring vocab_manager.py init_db (lines 102-132).
//
// Per D-08-a: Schema drift detection vs the Python schema is a Phase 5
// integration-test responsibility. This Migrate only validates Go's own tables.
func Migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		sessionsDDL,
		sessionEventsDDL,
		idxSessionsConnection,
		idxEventsSession,
	}

	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("database/migrate: %w", err)
		}
	}

	return nil
}
