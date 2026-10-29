package database

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ErrDatabasePathSplit is returned when database.path and python.vocabDBPath
// are both set but refer to different SQLite files after resolution.
var ErrDatabasePathSplit = errors.New("database.path and python.vocabDBPath must refer to the same SQLite file")

// DefaultVocabDBPath returns the legacy shared vocab DB location under home.
func DefaultVocabDBPath(homeDir string) string {
	return filepath.Join(homeDir, ".home-tutor-agent", "vocab.db")
}

// ResolveSharedDBPath picks the single SQLite file Go and Python must share.
// Empty database.path falls back to python.vocabDBPath, then DefaultVocabDBPath.
// When both inputs are non-empty they must denote the same file (absolute vs
// relative paths allowed). The returned path is absolute and cleaned.
func ResolveSharedDBPath(databasePath, pythonPath, homeDir string) (string, error) {
	effective := strings.TrimSpace(databasePath)
	if effective == "" {
		effective = strings.TrimSpace(pythonPath)
	}
	if effective == "" {
		effective = DefaultVocabDBPath(homeDir)
	}
	abs, err := filepath.Abs(effective)
	if err != nil {
		return "", fmt.Errorf("database: resolve shared path: %w", err)
	}
	abs = filepath.Clean(abs)

	dbSet := strings.TrimSpace(databasePath) != ""
	pySet := strings.TrimSpace(pythonPath) != ""
	if dbSet && pySet {
		same, sameErr := SameSQLiteFile(databasePath, pythonPath)
		if sameErr != nil {
			return "", sameErr
		}
		if !same {
			return "", ErrDatabasePathSplit
		}
	}
	return abs, nil
}

// SameSQLiteFile reports whether a and b refer to the same filesystem object
// after absolute-path normalization (and os.SameFile when both paths exist).
func SameSQLiteFile(a, b string) (bool, error) {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false, fmt.Errorf("database: abs %q: %w", a, err)
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false, fmt.Errorf("database: abs %q: %w", b, err)
	}
	absA = filepath.Clean(absA)
	absB = filepath.Clean(absB)
	if absA == absB {
		return true, nil
	}
	fa, errA := os.Stat(absA)
	fb, errB := os.Stat(absB)
	if errA == nil && errB == nil {
		return os.SameFile(fa, fb), nil
	}
	return false, nil
}

// sqliteFileDSN builds a modernc.org/sqlite DSN with URI path encoding so
// percent sequences in filenames are not misread (plain python sqlite3.connect
// uses the literal filesystem path).
func sqliteFileDSN(absPath string) string {
	u := url.URL{
		Scheme: "file",
		Path:   filepath.ToSlash(absPath),
	}
	return u.String() + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
}
