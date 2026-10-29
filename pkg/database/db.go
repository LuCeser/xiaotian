package database

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // Pure-Go SQLite driver (CGO_ENABLED=0)
)

// Sentinel errors for database connection failures.
var (
	// ErrEmptyPath is returned by Open when dbPath is empty.
	// Detectable via errors.Is.
	ErrEmptyPath = errors.New("database: dbPath must not be empty")
)

// Open returns a *sql.DB configured for shared multi-process WAL access
// to the SQLite database at dbPath. The caller is responsible for Close()
// at service shutdown.
//
// Pragmas applied (D-03, D-07):
//   - journal_mode=WAL: enables concurrent readers during writes (matches Python)
//   - busy_timeout=5000: cross-process write contention waits up to 5s
//   - foreign_keys=ON: SQLite defaults to OFF; we want FK enforcement
//   - synchronous=NORMAL: good WAL balance of safety and speed (RESEARCH Pitfall 3)
//
// Connection limits (RESEARCH Pattern 2):
//   - SetMaxOpenConns(1): serializes all Go DB operations through one connection,
//     preventing SQLITE_BUSY errors within Go's own pool
//   - SetMaxIdleConns(1): matches MaxOpenConns for consistent pool behavior
//   - SetConnMaxLifetime(0): connection lives for service lifetime
//
// Open mirrors vocab_manager.py get_db (lines 87-95): directory creation,
// WAL pragma, and a configured connection. It does NOT call db.Ping — the
// caller decides when to verify connectivity (pure construction step).
func Open(dbPath string) (*sql.DB, error) {
	if dbPath == "" {
		return nil, ErrEmptyPath
	}

	// Create parent directory (mirrors vocab_manager.py get_db lines 89-91).
	// Guard the empty/dir=="." case to avoid os.MkdirAll with ".".
	dir := filepath.Dir(dbPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("database: create db directory: %w", err)
		}
	}

	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("database: abs db path: %w", err)
	}
	absPath = filepath.Clean(absPath)

	// DSN with pragmas. Path is URI-encoded (see sqliteFileDSN) so Go opens
	// the same file Python reaches via sqlite3.connect(literal path).
	dsn := sqliteFileDSN(absPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("database: sql.Open: %w", err)
	}

	// Serialize Go DB access through one connection. SQLite serializes
	// writes regardless; WAL allows concurrent reads but the single
	// connection keeps the model simple and correct for the 1C/1G VPS
	// scale where DB throughput is NOT the bottleneck (RESEARCH Pattern 2).
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	return db, nil
}
