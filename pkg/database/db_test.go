package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newTestDB returns an in-memory SQLite database with Go-owned tables migrated.
// Uses modernc.org/sqlite (same driver as production). The :memory: DSN creates
// a fresh private database per test — no cross-test leakage.
// Reused by Plan 04's repository tests (lowercase = package-internal).
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	// Enable foreign keys (matches production Open() DSN pragma).
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpen(t *testing.T) {
	t.Run("returns non-nil db with temp file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.db")
		db, err := Open(path)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		if db == nil {
			t.Fatal("Open() returned nil db")
		}
		defer db.Close()
	})

	t.Run("creates parent directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sub", "dir", "test.db")
		db, err := Open(path)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer db.Close()

		if _, err := os.Stat(filepath.Dir(path)); os.IsNotExist(err) {
			t.Error("Open() did not create parent directory")
		}
	})

	t.Run("empty path returns error", func(t *testing.T) {
		_, err := Open("")
		if err == nil {
			t.Fatal("Open(\"\") expected error, got nil")
		}
		if !errors.Is(err, ErrEmptyPath) {
			t.Errorf("Open(\"\") error = %v, want errors.Is ErrEmptyPath", err)
		}
	})
}

func TestOpen_Pragmas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pragma_test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{"journal_mode", "PRAGMA journal_mode", "wal"},
		{"busy_timeout", "PRAGMA busy_timeout", "5000"},
		{"foreign_keys", "PRAGMA foreign_keys", "1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			if err := db.QueryRow(tt.query).Scan(&got); err != nil {
				t.Fatalf("%s query failed: %v", tt.name, err)
			}
			if got != tt.expected {
				t.Errorf("%s = %q, want %q", tt.name, got, tt.expected)
			}
		})
	}
}

func TestOpen_MaxOpenConns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maxconns_test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	stats := db.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
}

func TestNewTestDB(t *testing.T) {
	db := newTestDB(t)

	// Verify tables exist
	tables := []string{"sessions", "session_events"}
	for _, table := range tables {
		var count int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?",
			table,
		).Scan(&count)
		if err != nil {
			t.Fatalf("query sqlite_master for %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s not found in sqlite_master", table)
		}
	}

	// Verify we can query the sessions table
	_, err := db.Exec("INSERT INTO sessions (connection_id) VALUES (?)", "test-conn")
	if err != nil {
		t.Errorf("failed to insert into sessions: %v", err)
	}
}
