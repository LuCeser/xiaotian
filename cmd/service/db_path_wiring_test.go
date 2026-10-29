// Issue #80 U10 fix2: real startup config proves run() unifies database.path
// and python.vocabDBPath before the Python bridge is constructed.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeDBPathYaml(t *testing.T, addr, dbPath, vocabDBPathLine string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	root := filepath.Dir(filepath.Dir(wd))
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := fmt.Sprintf(`server:
  addr: %s
  staticDir: %s
database:
  path: %s
python:
  interpreter: python3
  scriptsDir: %s
  dataDir: %s
%s`,
		addr,
		filepath.ToSlash(filepath.Join(root, "pkg", "web", "static")),
		filepath.ToSlash(dbPath),
		filepath.ToSlash(filepath.Join(root, "third_party", "python")),
		filepath.ToSlash(dir),
		vocabDBPathLine)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// TestRunUnifiedDBPath_databaseOnly boots when only database.path is set.
func TestRunUnifiedDBPath_databaseOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)

	healthAddr, err := freePortAddr()
	if err != nil {
		t.Fatal(err)
	}
	serverAddr, err := freePortAddr()
	if err != nil {
		t.Fatal(err)
	}
	dbFile := filepath.Join(t.TempDir(), "service-only-db.db")
	cfgPath := writeDBPathYaml(t, serverAddr, dbFile, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- run(ctx, cfgPath, healthAddr) }()

	waitReadyOrFatal(t, healthAddr)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (database.path only should boot)", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return")
	}
}

// TestRunUnifiedDBPath_relativeAndAbsoluteSameFile — absolute database.path
// plus relative python.vocabDBPath to the same file must not fail startup.
func TestRunUnifiedDBPath_relativeAndAbsoluteSameFile(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)

	healthAddr, err := freePortAddr()
	if err != nil {
		t.Fatal(err)
	}
	serverAddr, err := freePortAddr()
	if err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	absDB := filepath.Join(dir, "unified.db")
	relDB, err := filepath.Rel(wd, absDB)
	if err != nil {
		t.Fatal(err)
	}

	vocabLine := fmt.Sprintf("  vocabDBPath: %s\n", filepath.ToSlash(relDB))
	cfgPath := writeDBPathYaml(t, serverAddr, absDB, vocabLine)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- run(ctx, cfgPath, healthAddr) }()

	waitReadyOrFatal(t, healthAddr)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (abs/rel same file must boot)", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return")
	}
}
