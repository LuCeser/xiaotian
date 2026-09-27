//go:build e2e

// Real-Python end-to-end tests for the Phase 9 vocab-management ENGINE
// contracts (Plan 09-02):
//
//   - TestE2E_VocabEdit_* — the `edit` subcommand's edit-text-only semantics
//     (D-07): update word/meaning/example/unit by card id, NEVER touch
//     fsrs_card (preserves FSRS learning progress — VOC-01 edit intent).
//   - TestE2E_FSRSConfig_* — the config-driven SCHEDULER loader (D-13/D-14):
//     fsrs_config.json consumed at startup, silent defaults when absent,
//     warn-and-fallback on malformed/out-of-range values (Pitfall 9).
//
// Build tag: compiled ONLY under `go test -tags=e2e` (make py-e2e), like
// e2e_test.go. These tests shell the vendored vocab_manager.py DIRECTLY
// (interpreter + script + --db argv) rather than through the Bridge — the
// Bridge methods for these commands arrive in later Phase 9 plans and are
// exercised there; here we pin the Python-side contract those methods ride on.
//
// Conventions follow e2e_test.go: shared uv-bootstrapped venv from TestMain
// (e2eInterpreter / e2eSkipReason), per-test temp dirs, skip-cleanly when no
// interpreter is available, manual fakes/no testify (D-48).
package pybridge

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // same pure-Go driver pkg/database uses (read fsrs_card bytes)
)

// e2eMgmtEnv returns the child environment for a direct vocab_manager.py
// invocation, mirroring Bridge.childEnv: the inherited environment plus the
// UTF-8-forcing vars, plus DICTATION_DATA_DIR pointed at dataDir (any outer
// value is dropped so the test's temp dir is authoritative — the Pitfall 8
// single-resolution rule this suite pins). The data dir is created because
// vocab_manager.py resolves DEFAULT_DATA_DIR from it at import time.
func e2eMgmtEnv(t *testing.T, dataDir string) []string {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir %s: %v", dataDir, err)
	}
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "DICTATION_DATA_DIR=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"DICTATION_DATA_DIR="+dataDir,
		"PYTHONUTF8=1",
		"PYTHONIOENCODING=utf-8",
		"PYTHONUNBUFFERED=1",
	)
}

// runVocabManager shells the vendored vocab_manager.py directly:
//
//	<interpreter> <script> --db <dbPath> <args...>
//
// mirroring the bridge's argv contract (--db BEFORE the subcommand,
// vocab_manager.py top-level parser) and its childEnv behavior. Returns
// stdout, stderr and the exit code: non-zero exits are CONTRACT outcomes
// here (e.g. `edit` on a missing id exits 1 with a stdout diagnostic), so
// they are returned rather than fatal.
func runVocabManager(t *testing.T, env []string, dbPath string, args ...string) (string, string, int) {
	t.Helper()
	if e2eInterpreter == "" {
		t.Skipf("real-Python E2E unavailable: %s", e2eSkipReason)
	}
	script := filepath.Join(e2eScriptsDir, "vocab_manager.py")
	cmd := exec.Command(e2eInterpreter, append([]string{script, "--db", dbPath}, args...)...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run vocab_manager.py %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout.String(), stderr.String())
		}
		code = ee.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// seedCard inserts one card through the script's own `add` subcommand
// (list-mode payload so example survives — Pitfall 4) into a fresh DB, where
// it receives id 1.
func seedCard(t *testing.T, env []string, dbPath, word, meaning, example, unit string) {
	t.Helper()
	payload, err := json.Marshal([]map[string]string{{
		"word":    word,
		"meaning": meaning,
		"example": example,
	}})
	if err != nil {
		t.Fatalf("marshal seed card: %v", err)
	}
	stdout, stderr, code := runVocabManager(t, env, dbPath,
		"add", "--category", "english", "--unit", unit, "--words", string(payload))
	if code != 0 {
		t.Fatalf("seed add failed (exit %d):\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var rep struct {
		Added int `json:"added"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &rep); err != nil || rep.Added != 1 {
		t.Fatalf("seed add did not add exactly 1 card: out=%q err=%v", stdout, err)
	}
}

// listCards runs the script's `list` subcommand and decodes the JSON rows —
// the script-side read path used to assert post-edit column state.
func listCards(t *testing.T, env []string, dbPath string) []map[string]any {
	t.Helper()
	stdout, stderr, code := runVocabManager(t, env, dbPath, "list", "--category", "english")
	if code != 0 {
		t.Fatalf("list failed (exit %d):\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &rows); err != nil {
		t.Fatalf("parse list output %q: %v", stdout, err)
	}
	return rows
}

// fsrsCardBytes reads the RAW fsrs_card column straight from SQLite — the
// byte-level proof that `edit` never touches learning state (D-07).
func fsrsCardBytes(t *testing.T, dbPath, id string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer db.Close()
	var fsrs string
	if err := db.QueryRow("SELECT fsrs_card FROM cards WHERE id = ?", id).Scan(&fsrs); err != nil {
		t.Fatalf("query fsrs_card id=%s: %v", id, err)
	}
	return fsrs
}

// TestE2E_VocabEdit_UpdatesOnlyGivenFields proves the happy path: edit --id N
// --word newword updates ONLY the word column, leaves meaning/example/unit
// untouched, and prints the {"edited":{"id":N,"word":"..."}} contract.
func TestE2E_VocabEdit_UpdatesOnlyGivenFields(t *testing.T) {
	base := t.TempDir()
	dbPath := filepath.Join(base, "vocab.db")
	env := e2eMgmtEnv(t, filepath.Join(base, "data"))
	seedCard(t, env, dbPath, "apple", "苹果", "I eat an apple.", "U1")

	stdout, stderr, code := runVocabManager(t, env, dbPath, "edit", "--id", "1", "--word", "banana")
	if code != 0 {
		t.Fatalf("edit exited %d:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var rep struct {
		Edited struct {
			ID   int    `json:"id"`
			Word string `json:"word"`
		} `json:"edited"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &rep); err != nil {
		t.Fatalf("parse edit output %q: %v", stdout, err)
	}
	if rep.Edited.ID != 1 || rep.Edited.Word != "banana" {
		t.Fatalf("edit output = %+v, want {id:1 word:banana}", rep.Edited)
	}

	rows := listCards(t, env, dbPath)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row["word"] != "banana" {
		t.Errorf("word = %v, want banana", row["word"])
	}
	if row["meaning"] != "苹果" {
		t.Errorf("meaning changed by --word-only edit: %q", row["meaning"])
	}
	if row["example"] != "I eat an apple." {
		t.Errorf("example changed by --word-only edit: %q", row["example"])
	}
	if row["unit"] != "U1" {
		t.Errorf("unit changed by --word-only edit: %q", row["unit"])
	}
}

// TestE2E_VocabEdit_PreservesFSRS proves the D-07 core promise at the byte
// level: after add + one review (non-trivial learning state), edit --id N
// --meaning x leaves the fsrs_card column BYTE-IDENTICAL.
func TestE2E_VocabEdit_PreservesFSRS(t *testing.T) {
	base := t.TempDir()
	dbPath := filepath.Join(base, "vocab.db")
	env := e2eMgmtEnv(t, filepath.Join(base, "data"))
	seedCard(t, env, dbPath, "apple", "苹果", "", "U1")

	// One review so fsrs_card carries non-trivial state (stability,
	// difficulty, due) — the exact state a destructive edit would destroy.
	if stdout, stderr, code := runVocabManager(t, env, dbPath, "review", "--results", `{"1":"good"}`); code != 0 {
		t.Fatalf("seed review failed (exit %d):\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	before := fsrsCardBytes(t, dbPath, "1")
	if before == "" {
		t.Fatal("fsrs_card empty after add+review — fixture broken")
	}

	if stdout, stderr, code := runVocabManager(t, env, dbPath, "edit", "--id", "1", "--meaning", "红苹果"); code != 0 {
		t.Fatalf("edit exited %d:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	after := fsrsCardBytes(t, dbPath, "1")
	if before != after {
		t.Fatalf("fsrs_card changed by edit (VOC-01 violation):\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestE2E_VocabEdit_NotFound proves the missing-id shape mirrors cmd_reset:
// print "Card N not found" to stdout, exit 1, no JSON output.
func TestE2E_VocabEdit_NotFound(t *testing.T) {
	base := t.TempDir()
	dbPath := filepath.Join(base, "vocab.db")
	env := e2eMgmtEnv(t, filepath.Join(base, "data"))
	seedCard(t, env, dbPath, "apple", "苹果", "", "U1")

	stdout, _, code := runVocabManager(t, env, dbPath, "edit", "--id", "999999", "--word", "x")
	if code != 1 {
		t.Fatalf("edit missing-id exit code = %d, want 1 (stdout: %q)", code, stdout)
	}
	if !strings.Contains(stdout, "Card 999999 not found") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "Card 999999 not found")
	}
	if strings.Contains(stdout, "{") {
		t.Errorf("not-found path must print no JSON, got %q", stdout)
	}
}

// TestE2E_VocabEdit_ClearsWithEmptyString proves the None-vs-empty
// distinction: an explicit empty string CLEARS the column, while an ABSENT
// flag keeps the current value.
func TestE2E_VocabEdit_ClearsWithEmptyString(t *testing.T) {
	base := t.TempDir()
	dbPath := filepath.Join(base, "vocab.db")
	env := e2eMgmtEnv(t, filepath.Join(base, "data"))
	seedCard(t, env, dbPath, "apple", "苹果", "I eat an apple.", "U1")

	// Explicit empty string clears meaning only.
	if stdout, stderr, code := runVocabManager(t, env, dbPath, "edit", "--id", "1", "--meaning", ""); code != 0 {
		t.Fatalf("edit --meaning '' exited %d:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	rows := listCards(t, env, dbPath)
	if rows[0]["meaning"] != "" {
		t.Errorf("meaning = %q, want cleared by explicit empty string", rows[0]["meaning"])
	}
	if rows[0]["example"] != "I eat an apple." {
		t.Errorf("example changed by --meaning-only edit: %q", rows[0]["example"])
	}

	// Absent flag keeps the (just-cleared) current value — no resurrection.
	if _, stderr, code := runVocabManager(t, env, dbPath, "edit", "--id", "1", "--word", "banana"); code != 0 {
		t.Fatalf("edit --word exited %d:\nstderr: %s", code, stderr)
	}
	rows = listCards(t, env, dbPath)
	if rows[0]["meaning"] != "" {
		t.Errorf("meaning resurrected by absent-flag edit: %q", rows[0]["meaning"])
	}
	if rows[0]["word"] != "banana" {
		t.Errorf("word = %v, want banana", rows[0]["word"])
	}
}
