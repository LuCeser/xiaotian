package database

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSharedDBPath(t *testing.T) {
	home := t.TempDir()

	t.Run("both empty uses default under home", func(t *testing.T) {
		got, err := ResolveSharedDBPath("", "", home)
		if err != nil {
			t.Fatalf("ResolveSharedDBPath: %v", err)
		}
		want := DefaultVocabDBPath(home)
		wantAbs, _ := filepath.Abs(want)
		if got != filepath.Clean(wantAbs) {
			t.Fatalf("got %q, want %q", got, wantAbs)
		}
	})

	t.Run("database only", func(t *testing.T) {
		dir := t.TempDir()
		rel := filepath.Join(dir, "only-go.db")
		got, err := ResolveSharedDBPath(rel, "", home)
		if err != nil {
			t.Fatalf("ResolveSharedDBPath: %v", err)
		}
		wantAbs, _ := filepath.Abs(rel)
		if got != filepath.Clean(wantAbs) {
			t.Fatalf("got %q, want %q", got, wantAbs)
		}
	})

	t.Run("python only", func(t *testing.T) {
		dir := t.TempDir()
		rel := filepath.Join(dir, "only-py.db")
		got, err := ResolveSharedDBPath("", rel, home)
		if err != nil {
			t.Fatalf("ResolveSharedDBPath: %v", err)
		}
		wantAbs, _ := filepath.Abs(rel)
		if got != filepath.Clean(wantAbs) {
			t.Fatalf("got %q, want %q", got, wantAbs)
		}
	})

	t.Run("relative and absolute same file", func(t *testing.T) {
		dir := t.TempDir()
		abs := filepath.Join(dir, "shared.db")
		origWD, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(origWD) })

		got, err := ResolveSharedDBPath(abs, "shared.db", home)
		if err != nil {
			t.Fatalf("ResolveSharedDBPath: %v", err)
		}
		wantAbs, _ := filepath.Abs(abs)
		if got != filepath.Clean(wantAbs) {
			t.Fatalf("got %q, want %q", got, wantAbs)
		}
	})

	t.Run("split paths error", func(t *testing.T) {
		dir := t.TempDir()
		_, err := ResolveSharedDBPath(
			filepath.Join(dir, "a.db"),
			filepath.Join(dir, "b.db"),
			home,
		)
		if !errors.Is(err, ErrDatabasePathSplit) {
			t.Fatalf("error = %v, want ErrDatabasePathSplit", err)
		}
	})
}

func TestOpen_specialPathCharacters(t *testing.T) {
	dir := t.TempDir()
	// Literal percent sequence in the filename (Python opens this literally).
	literal := filepath.Join(dir, "vocab%20test.db")
	db, err := Open(literal)
	if err != nil {
		t.Fatalf("Open(%q): %v", literal, err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS path_probe (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if _, err := os.Stat(literal); err != nil {
		t.Fatalf("stat db file: %v", err)
	}

	spacePath := filepath.Join(dir, "vocab test.db")
	db2, err := Open(spacePath)
	if err != nil {
		t.Fatalf("Open space path: %v", err)
	}
	defer db2.Close()
}

func TestSameSQLiteFile(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "x.db")
	origWD, _ := os.Getwd()
	_ = os.Chdir(dir)
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	same, err := SameSQLiteFile(abs, "x.db")
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("expected same file for abs vs relative")
	}
}
