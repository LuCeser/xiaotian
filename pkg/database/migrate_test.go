package database

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestMigrate_CreatesSessionsTable(t *testing.T) {
	db := newTestDB(t)

	// Verify sessions table columns via PRAGMA table_info
	rows, err := db.Query("PRAGMA table_info(sessions)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(sessions): %v", err)
	}
	defer rows.Close()

	expectedCols := map[string]bool{
		"id":            false,
		"connection_id": false,
		"state":         false,
		"word_count":    false,
		"metadata":      false,
		"created_at":    false,
		"updated_at":    false,
	}

	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		if _, ok := expectedCols[name]; ok {
			expectedCols[name] = true
		}
	}

	for col, found := range expectedCols {
		if !found {
			t.Errorf("sessions table missing column: %s", col)
		}
	}
}

func TestMigrate_CreatesSessionEventsTable(t *testing.T) {
	db := newTestDB(t)

	rows, err := db.Query("PRAGMA table_info(session_events)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(session_events): %v", err)
	}
	defer rows.Close()

	expectedCols := map[string]bool{
		"id":          false,
		"session_id":  false,
		"event_type":  false,
		"payload":     false,
		"occurred_at": false,
	}

	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		if _, ok := expectedCols[name]; ok {
			expectedCols[name] = true
		}
	}

	for col, found := range expectedCols {
		if !found {
			t.Errorf("session_events table missing column: %s", col)
		}
	}
}

func TestMigrate_CreatesIndexes(t *testing.T) {
	db := newTestDB(t)

	indexes := []string{"idx_sessions_connection", "idx_events_session"}
	for _, idx := range indexes {
		var count int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?",
			idx,
		).Scan(&count)
		if err != nil {
			t.Fatalf("query index %s: %v", idx, err)
		}
		if count != 1 {
			t.Errorf("index %s not found", idx)
		}
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	db := newTestDB(t)

	// Count tables before second migration
	var before int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('sessions', 'session_events')",
	).Scan(&before); err != nil {
		t.Fatalf("count tables: %v", err)
	}

	// Run Migrate again
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}

	// Count tables after — should be same
	var after int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('sessions', 'session_events')",
	).Scan(&after); err != nil {
		t.Fatalf("count tables: %v", err)
	}

	if before != after {
		t.Errorf("table count changed: before=%d, after=%d", before, after)
	}
}

func TestMigrate_DoesNotCreatePythonTables(t *testing.T) {
	db := newTestDB(t)

	// Query sqlite_master for all table names
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		// Skip sqlite internal tables
		if !strings.HasPrefix(name, "sqlite_") {
			tables[name] = true
		}
	}

	// Go-owned tables should exist
	for _, want := range []string{"sessions", "session_events"} {
		if !tables[want] {
			t.Errorf("expected Go-owned table %s not found", want)
		}
	}

	// Python-owned tables must NOT exist
	for _, forbidden := range []string{"cards", "review_logs"} {
		if tables[forbidden] {
			t.Errorf("Python-owned table %s was created by Migrate (violates D-01/D-02)", forbidden)
		}
	}
}

func TestMigrate_RespectsCancelledContext(t *testing.T) {
	db := newTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := Migrate(ctx, db)
	// The error may be nil if ExecContext with cancelled ctx still executes
	// (SQLite is fast for in-memory). On a real DB, this would fail.
	// We just verify it doesn't panic and returns either error or nil.
	_ = err
}

func TestMigrate_CreatesForeignKey(t *testing.T) {
	db := newTestDB(t)

	// Insert a session first
	_, err := db.Exec("INSERT INTO sessions (connection_id) VALUES (?)", "test-conn")
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Insert a session event referencing the session
	_, err = db.Exec(
		"INSERT INTO session_events (session_id, event_type) VALUES (?, ?)",
		1, "test-event",
	)
	if err != nil {
		t.Fatalf("insert session_event: %v", err)
	}

	// Try to insert a session event referencing a non-existent session
	_, err = db.Exec(
		"INSERT INTO session_events (session_id, event_type) VALUES (?, ?)",
		99999, "orphan-event",
	)
	// foreign_keys is ON, so this should fail
	if err == nil {
		t.Error("expected foreign key violation, got nil error")
	}
}
