//go:build e2e

// Real-Python end-to-end tests for the Phase 9 vocab-management ENGINE
// contracts (Plan 09-02) and the Bridge management methods that ride them
// (Plan 09-03):
//
//   - TestE2E_VocabEdit_* — the `edit` subcommand's edit-text-only semantics
//     (D-07): update word/meaning/example/unit by card id, NEVER touch
//     fsrs_card (preserves FSRS learning progress — VOC-01 edit intent).
//   - TestE2E_FSRSConfig_* — the config-driven SCHEDULER loader (D-13/D-14):
//     fsrs_config.json consumed at startup, silent defaults when absent,
//     warn-and-fallback on malformed/out-of-range values (Pitfall 9).
//   - TestE2E_RunEditRoundTrip / TestE2E_RunRenameUnit /
//     TestE2E_RunRenameUnitNotFound / TestE2E_RunDeleteWords (Plan 09-03) —
//     the Bridge.RunEdit / RunRenameUnit / RunDeleteWords methods driven
//     THROUGH the real interpreter + script + SQLite stack, proving the argv
//     contracts and the ExitError.Stdout diagnostics end-to-end.
//
// Build tag: compiled ONLY under `go test -tags=e2e` (make py-e2e), like
// e2e_test.go. The Plan 09-02 tests shell the vendored vocab_manager.py
// DIRECTLY (interpreter + script + --db argv) to pin the Python-side
// contract; the Plan 09-03 tests go through the Bridge methods exactly as
// production traffic will.
//
// Conventions follow e2e_test.go: shared uv-bootstrapped venv from TestMain
// (e2eInterpreter / e2eSkipReason), per-test temp dirs, skip-cleanly when no
// interpreter is available, manual fakes/no testify (D-48).
package pybridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	// parseTrailingJSON, not TrimSpace+Unmarshal: once the loader ships, any
	// command's stdout may carry a leading Warning line (invalid config file).
	if err := json.Unmarshal(parseTrailingJSON(t, stdout), &rep); err != nil || rep.Added != 1 {
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

// parseTrailingJSON extracts the JSON document from stdout that may carry
// leading Warning lines — the same tolerance the bridge's extractTrailingJSON
// implements for production traffic (the loader's Warning goes to stdout
// BEFORE any command JSON).
func parseTrailingJSON(t *testing.T, stdout string) json.RawMessage {
	t.Helper()
	s := strings.TrimSpace(stdout)
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		rest := strings.TrimSpace(s[i+1:])
		if json.Valid([]byte(rest)) {
			return json.RawMessage(rest)
		}
	}
	t.Fatalf("stdout has no trailing JSON: %q", stdout)
	return nil
}

// parseNextDue parses vocab_manager.py's next_due format (Asia/Shanghai,
// "2006-01-02 15:04") into a comparable time.
func parseNextDue(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		t.Fatalf("parse next_due %q: %v", s, err)
	}
	return ts
}

// fsrsConfigFixture creates a temp data dir (+ optional fsrs_config.json
// written into it) and a fresh DB with one seeded card, returning the child
// env and DB path. The config lands at DICTATION_DATA_DIR/fsrs_config.json —
// the exact path the Pitfall 8 single-resolution rule names (env forwarded by
// Bridge.childEnv, read by DEFAULT_DATA_DIR in vocab_manager.py).
func fsrsConfigFixture(t *testing.T, configJSON string) ([]string, string) {
	t.Helper()
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	env := e2eMgmtEnv(t, dataDir)
	dbPath := filepath.Join(base, "vocab.db")
	if configJSON != "" {
		if err := os.WriteFile(filepath.Join(dataDir, "fsrs_config.json"), []byte(configJSON), 0o644); err != nil {
			t.Fatalf("write fsrs_config.json: %v", err)
		}
	}
	seedCard(t, env, dbPath, "apple", "苹果", "", "U1")
	return env, dbPath
}

// reviewFreshCard graduates the fixture's card with two "good" reviews and
// returns the second review's reported next_due. The FIRST review only
// advances the hard-coded Learning steps (1min/10min — desired_retention does
// not shape Learning-state intervals); the SECOND graduates the card to
// Review state, where the configured desired_retention drives the interval.
// next_due is therefore the observable that proves whether fsrs_config.json
// actually drives the scheduler.
func reviewFreshCard(t *testing.T, env []string, dbPath string) (time.Time, string) {
	t.Helper()
	if stdout, stderr, code := runVocabManager(t, env, dbPath, "review", "--results", `{"1":"good"}`); code != 0 {
		t.Fatalf("first (learning-step) review exited %d:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	stdout, stderr, code := runVocabManager(t, env, dbPath, "review", "--results", `{"1":"good"}`)
	if code != 0 {
		t.Fatalf("second (graduating) review exited %d:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var rep struct {
		Updated []struct {
			NextDue string `json:"next_due"`
		} `json:"updated"`
	}
	if err := json.Unmarshal(parseTrailingJSON(t, stdout), &rep); err != nil {
		t.Fatalf("parse review output %q: %v", stdout, err)
	}
	if len(rep.Updated) != 1 {
		t.Fatalf("got %d updated entries, want 1: %s", len(rep.Updated), stdout)
	}
	return parseNextDue(t, rep.Updated[0].NextDue), stdout
}

// TestE2E_FSRSConfig_MissingFileDefaults proves the ABSENT config file falls
// back SILENTLY: due exits 0 with valid trailing JSON and no Warning line —
// defaults are not an error condition.
func TestE2E_FSRSConfig_MissingFileDefaults(t *testing.T) {
	env, dbPath := fsrsConfigFixture(t, "")

	stdout, stderr, code := runVocabManager(t, env, dbPath, "due", "--category", "english")
	if code != 0 {
		t.Fatalf("due exited %d:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	parseTrailingJSON(t, stdout)
	if strings.Contains(stdout, "Warning") {
		t.Errorf("absent config must fall back silently, stdout = %q", stdout)
	}
}

// TestE2E_FSRSConfig_InvalidFallsBack proves malformed JSON degrades to
// defaults with a Warning — the student dictation path NEVER breaks
// (Pitfall 9, threat T-09-06).
func TestE2E_FSRSConfig_InvalidFallsBack(t *testing.T) {
	env, dbPath := fsrsConfigFixture(t, "{ this is not json")

	stdout, stderr, code := runVocabManager(t, env, dbPath, "due", "--category", "english")
	if code != 0 {
		t.Fatalf("due exited %d with malformed config:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	parseTrailingJSON(t, stdout)
	if !strings.Contains(stdout, "Warning") {
		t.Errorf("malformed config must print a Warning line, stdout = %q", stdout)
	}
}

// TestE2E_FSRSConfig_OutOfRangeFallsBack proves an out-of-range value (the
// fsrs library validates only its 21 weights, so OUR loader must range-check)
// warns AND schedules identically to a defaults run — the bad value is
// neither consumed nor fatal. next_due is compared with a 2-minute tolerance:
// the two live runs are seconds apart, while a consumed retention of 1.7
// would produce a wildly different (even negative) interval.
func TestE2E_FSRSConfig_OutOfRangeFallsBack(t *testing.T) {
	envBad, dbBad := fsrsConfigFixture(t, `{"desired_retention": 1.7}`)
	envOK, dbOK := fsrsConfigFixture(t, "")

	dueBad, stdoutBad := reviewFreshCard(t, envBad, dbBad)
	if !strings.Contains(stdoutBad, "Warning") {
		t.Errorf("out-of-range config must print a Warning line, stdout = %q", stdoutBad)
	}
	dueOK, _ := reviewFreshCard(t, envOK, dbOK)

	if d := dueBad.Sub(dueOK); d > 2*time.Minute || d < -2*time.Minute {
		t.Fatalf("out-of-range config changed scheduling (not fallen back): retention-1.7 due=%s, defaults due=%s (Δ=%s)", dueBad, dueOK, d)
	}
}

// TestE2E_FSRSConfig_ValuesAreConsumed proves the file values actually drive
// scheduling: two identical fresh cards in two data dirs configured with
// DIFFERENT IN-RANGE retentions must produce different next_due dates.
//
// NOTE (deviation from the plan draft): the plan suggested 0.70 vs 0.99, but
// 0.99 sits OUTSIDE the loader's locked [0.7, 0.95] range (D-13) and would
// exercise the warn-and-fallback path, not consumption. 0.95 keeps both sides
// in-range so the assertion measures what its name claims. Lower retention
// schedules FURTHER out, so dueLo must not be before dueHi.
func TestE2E_FSRSConfig_ValuesAreConsumed(t *testing.T) {
	// Both keys present, as the Go writer (Plan 09-05) always emits them —
	// the loader treats a missing key as invalid and falls back entirely.
	envLo, dbLo := fsrsConfigFixture(t, `{"desired_retention": 0.70, "maximum_interval": 180}`)
	envHi, dbHi := fsrsConfigFixture(t, `{"desired_retention": 0.95, "maximum_interval": 180}`)

	dueLo, stdoutLo := reviewFreshCard(t, envLo, dbLo)
	if strings.Contains(stdoutLo, "Warning") {
		t.Errorf("valid in-range config must be consumed silently, stdout = %q", stdoutLo)
	}
	dueHi, stdoutHi := reviewFreshCard(t, envHi, dbHi)
	if strings.Contains(stdoutHi, "Warning") {
		t.Errorf("valid in-range config must be consumed silently, stdout = %q", stdoutHi)
	}

	if dueLo.Equal(dueHi) {
		t.Fatalf("next_due identical across retentions 0.70/0.95 (%s) — fsrs_config.json values are not driving the scheduler", dueLo)
	}
	if dueLo.Before(dueHi) {
		t.Errorf("retention 0.70 scheduled earlier (%s) than 0.95 (%s) — scheduling direction inverted", dueLo, dueHi)
	}
}

// ---------------------------------------------------------------------------
// Plan 09-03 Task 2: Bridge.RunEdit / RunRenameUnit / RunDeleteWords driven
// through the real interpreter + script + SQLite stack (e2eBridge → Bridge →
// vocab_manager.py). Read-back goes through the script's `list` subcommand
// via the direct-subprocess helpers above — the same file the Bridge wrote.
// ---------------------------------------------------------------------------

// mgmtListEnv derives the (env, ok) pair the direct-subprocess read helpers
// need from a bridge test's cfg: DICTATION_DATA_DIR pointed at cfg.DataDir
// (dropping any outer value — Pitfall 8 single-resolution rule) and
// cfg.VocabDBPath as the DB the bridge just wrote.
func mgmtListEnv(t *testing.T, dataDir string) []string {
	return e2eMgmtEnv(t, dataDir)
}

// TestE2E_RunEditRoundTrip proves the edit path through the Bridge: add via
// RunAdd, edit via RunEdit changing the word ONLY, then read back — other
// columns unchanged, and EditResult.Edited mirrors the {"edited":{"id",
// "word"}} contract (the post-update word from cmd_edit's fresh SELECT).
func TestE2E_RunEditRoundTrip(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	env := mgmtListEnv(t, cfg.DataDir)

	addRes, err := b.RunAdd(context.Background(), AddOpts{
		Category: "english",
		Unit:     "U1",
		Entries:  []AddEntry{{Word: "apple", Meaning: "苹果", Example: "I eat an apple."}},
	})
	if err != nil {
		t.Fatalf("RunAdd: %v", err)
	}
	if addRes.Added != 1 {
		t.Fatalf("RunAdd added %d, want 1", addRes.Added)
	}

	word := "banana"
	editRes, err := b.RunEdit(context.Background(), EditOpts{ID: 1, Word: &word})
	if err != nil {
		t.Fatalf("RunEdit: %v", err)
	}
	if editRes.Edited.ID != 1 || editRes.Edited.Word != "banana" {
		t.Fatalf("EditResult.Edited = %+v, want {id:1 word:banana}", editRes.Edited)
	}

	rows := listCards(t, env, cfg.VocabDBPath)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row["word"] != "banana" {
		t.Errorf("word = %v, want banana", row["word"])
	}
	if row["meaning"] != "苹果" {
		t.Errorf("meaning changed by word-only edit: %q", row["meaning"])
	}
	if row["example"] != "I eat an apple." {
		t.Errorf("example changed by word-only edit: %q", row["example"])
	}
	if row["unit"] != "U1" {
		t.Errorf("unit changed by word-only edit: %q", row["unit"])
	}
}

func TestE2E_ParticipationExcludesDueAndPreservesFSRS(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	if _, err := b.RunAdd(context.Background(), AddOpts{
		Category: "english", Unit: "U1",
		Entries: []AddEntry{{Word: "apple", Meaning: "苹果"}},
	}); err != nil {
		t.Fatalf("RunAdd: %v", err)
	}
	before := fsrsCardBytes(t, cfg.VocabDBPath, "1")
	if _, err := b.RunSetParticipation(context.Background(), SetParticipationOpts{IDs: []int64{1}, Participates: false}); err != nil {
		t.Fatalf("disable participation: %v", err)
	}
	if after := fsrsCardBytes(t, cfg.VocabDBPath, "1"); after != before {
		t.Fatalf("FSRS changed while disabling: before=%q after=%q", before, after)
	}
	due, err := b.RunDue(context.Background(), DueOpts{Category: "english", Peek: true})
	if err != nil {
		t.Fatalf("RunDue: %v", err)
	}
	if len(due.Cards) != 0 {
		t.Fatalf("disabled card returned by due: %+v", due)
	}
	if _, err := b.RunSetParticipation(context.Background(), SetParticipationOpts{IDs: []int64{1}, Participates: true}); err != nil {
		t.Fatalf("enable participation: %v", err)
	}
	due, err = b.RunDue(context.Background(), DueOpts{Category: "english", Peek: true})
	if err != nil || len(due.Cards) != 1 || due.Cards[0].ID != 1 {
		t.Fatalf("reenabled due = %+v, err=%v; want card 1", due, err)
	}
}

// e2eOpenDB opens the vocabulary DB read-only through the same pure-Go
// driver fsrsCardBytes uses, for the participation suite's direct column
// assertions.
func e2eOpenDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// e2eFsrsSnapshot hashes every card's fsrs_card column in id order — the
// byte-level baseline for "participation changes never touch learning
// state" at batch scale.
func e2eFsrsSnapshot(t *testing.T, dbPath string) string {
	t.Helper()
	db := e2eOpenDB(t, dbPath)
	rows, err := db.Query("SELECT id, fsrs_card FROM cards ORDER BY id")
	if err != nil {
		t.Fatalf("snapshot query: %v", err)
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		var id int
		var fsrs string
		if err := rows.Scan(&id, &fsrs); err != nil {
			t.Fatalf("snapshot scan: %v", err)
		}
		fmt.Fprintf(h, "%d:%s\n", id, fsrs)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot rows: %v", err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// e2eReviewLogsCount counts the Python-owned review history rows.
func e2eReviewLogsCount(t *testing.T, dbPath string) int {
	t.Helper()
	db := e2eOpenDB(t, dbPath)
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM review_logs").Scan(&n); err != nil {
		t.Fatalf("count review_logs: %v", err)
	}
	return n
}

// e2eParticipates reads one card's classroom-eligibility flag straight
// from SQLite.
func e2eParticipates(t *testing.T, dbPath string, id int64) bool {
	t.Helper()
	db := e2eOpenDB(t, dbPath)
	var flag int
	if err := db.QueryRow("SELECT participates FROM cards WHERE id = ?", id).Scan(&flag); err != nil {
		t.Fatalf("read participates id=%d: %v", id, err)
	}
	return flag == 1
}

// TestE2E_SetParticipationBatch100 proves the batch contract at the scale
// the parent UI can reach — ONE request = ONE Python process = ONE SQLite
// transaction (the Standards finding that killed the per-card fan-out):
//
//   - 100 cards seeded in one RunAdd (the entry cap), three of them given
//     real review history first;
//   - 60 disabled in one RunSetParticipation; due (peek) then returns
//     exactly the 40 participants;
//   - fsrs_card bytes for ALL 100 cards and the review_logs count are
//     identical before/after every participation write;
//   - re-enabling restores due to 100;
//   - a batch naming a missing id fails with the not-found stdout
//     diagnostic AND rolls back atomically — the surviving card keeps its
//     previous flag (all-or-nothing, never a partial apply).
func TestE2E_SetParticipationBatch100(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)

	entries := make([]AddEntry, 0, 100)
	for i := 1; i <= 100; i++ {
		entries = append(entries, AddEntry{
			Word:    fmt.Sprintf("batch%03d", i),
			Meaning: "批量验证",
		})
	}
	add, err := b.RunAdd(context.Background(), AddOpts{
		Category: "english", Unit: "batch100", Entries: entries,
	})
	if err != nil {
		t.Fatalf("RunAdd: %v", err)
	}
	if add.Added != 100 {
		t.Fatalf("RunAdd added = %d, want 100", add.Added)
	}

	// Real learning history on three cards: review_logs rows + fsrs_card
	// moved off the fresh-card bytes (the exact state a broken batch write
	// would destroy).
	if _, err := b.RunReview(context.Background(), ReviewOpts{
		Results:  map[string]string{"batch001": "good", "batch002": "good", "batch003": "hard"},
		Category: "english",
	}); err != nil {
		t.Fatalf("seed reviews: %v", err)
	}
	beforeFsrs := e2eFsrsSnapshot(t, cfg.VocabDBPath)
	if logs := e2eReviewLogsCount(t, cfg.VocabDBPath); logs != 3 {
		t.Fatalf("review_logs after seeding = %d, want 3", logs)
	}

	// One batch disables ids 41..100 (fresh DB: ids are 1..100).
	ids := make([]int64, 0, 60)
	for id := int64(41); id <= 100; id++ {
		ids = append(ids, id)
	}
	res, err := b.RunSetParticipation(context.Background(), SetParticipationOpts{IDs: ids, Participates: false})
	if err != nil {
		t.Fatalf("disable batch: %v", err)
	}
	if res.Requested != 60 || res.Updated != 60 || len(res.Missing) != 0 {
		t.Fatalf("batch result = %+v, want requested/updated 60, no missing", res)
	}

	due, err := b.RunDue(context.Background(), DueOpts{Category: "english", Peek: true})
	if err != nil {
		t.Fatalf("RunDue after disable: %v", err)
	}
	// 40 participants, of which the 3 reviewed cards (ids 1..3) sit in a
	// learning step (+1min/+10min) — scheduled, not due now. So exactly 37
	// are due, every one of them a participant.
	if len(due.Cards) != 37 {
		t.Fatalf("due cards after disabling 60 of 100 = %d, want 37 (40 participants minus 3 in a learning step)", len(due.Cards))
	}
	for _, c := range due.Cards {
		if c.ID > 40 {
			t.Fatalf("disabled card %d returned by due (exclusion broken)", c.ID)
		}
	}
	if got := e2eFsrsSnapshot(t, cfg.VocabDBPath); got != beforeFsrs {
		t.Fatal("fsrs_card bytes changed across the 60-card disable (learning state must be untouched)")
	}
	if logs := e2eReviewLogsCount(t, cfg.VocabDBPath); logs != 3 {
		t.Fatalf("review_logs after disable = %d, want 3", logs)
	}

	// Re-enable restores the pool — the flag is data, not a deletion. The
	// 60 re-enabled cards are fresh (never reviewed) so all are due; the
	// same 37+60 arithmetic holds.
	if _, err := b.RunSetParticipation(context.Background(), SetParticipationOpts{IDs: ids, Participates: true}); err != nil {
		t.Fatalf("enable batch: %v", err)
	}
	due, err = b.RunDue(context.Background(), DueOpts{Category: "english", Peek: true})
	if err != nil {
		t.Fatalf("RunDue after re-enable: %v", err)
	}
	if len(due.Cards) != 97 {
		t.Fatalf("due cards after re-enable = %d, want 97 (100 minus 3 in a learning step)", len(due.Cards))
	}
	if got := e2eFsrsSnapshot(t, cfg.VocabDBPath); got != beforeFsrs {
		t.Fatal("fsrs_card bytes changed across disable+enable round trip")
	}

	// All-or-nothing: a batch naming a missing id exits 1 with the
	// not-found stdout diagnostic, and card 7 (which DOES exist) keeps its
	// pre-call state — the rollback proof.
	_, err = b.RunSetParticipation(context.Background(), SetParticipationOpts{
		IDs: []int64{7, 999999}, Participates: false,
	})
	if err == nil {
		t.Fatal("batch with missing id returned nil error, want ExitError")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %v, want *ExitError", err)
	}
	if !strings.Contains(exitErr.Stdout, "card ids not found: 999999") {
		t.Fatalf("stdout = %q, want the missing-id diagnostic", exitErr.Stdout)
	}
	if !e2eParticipates(t, cfg.VocabDBPath, 7) {
		t.Fatal("card 7 disabled by a rolled-back batch (partial apply leaked)")
	}
	if got := e2eFsrsSnapshot(t, cfg.VocabDBPath); got != beforeFsrs {
		t.Fatal("fsrs_card bytes changed across the rolled-back batch")
	}
}

// TestE2E_ParticipationAppliesToChineseCategory pins the AC「英语、语文沿用
// 统一管理入口」on the selection side: the participates filter rides the
// SAME due query for the chinese category (语文), not just english.
func TestE2E_ParticipationAppliesToChineseCategory(t *testing.T) {
	b, _, _ := e2eBridge(t, nil)
	if _, err := b.RunAdd(context.Background(), AddOpts{
		Category: "chinese", Unit: "语文一组",
		Entries: []AddEntry{
			{Word: "春晓", Meaning: "spring dawn"},
			{Word: "咏鹅", Meaning: "ode to the goose"},
		},
	}); err != nil {
		t.Fatalf("RunAdd chinese: %v", err)
	}
	// Fresh DB ⇒ ids 1..2; disable 春晓 (id 1) only.
	if _, err := b.RunSetParticipation(context.Background(), SetParticipationOpts{
		IDs: []int64{1}, Participates: false,
	}); err != nil {
		t.Fatalf("disable chinese card: %v", err)
	}
	due, err := b.RunDue(context.Background(), DueOpts{Category: "chinese", Peek: true})
	if err != nil {
		t.Fatalf("RunDue chinese: %v", err)
	}
	if len(due.Cards) != 1 || due.Cards[0].Word != "咏鹅" {
		t.Fatalf("chinese due = %+v, want only the participating 咏鹅", due.Cards)
	}
}

// TestE2E_RunRenameUnit proves the rename path through the Bridge: two words
// added in unit old-x, RunRenameUnit old-x -> new-x returns
// RenameUnitResult{Renamed:2, From:"old-x", To:"new-x"} and both rows now
// carry the new unit.
func TestE2E_RunRenameUnit(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	env := mgmtListEnv(t, cfg.DataDir)

	addRes, err := b.RunAdd(context.Background(), AddOpts{
		Category: "english",
		Unit:     "old-x",
		Entries: []AddEntry{
			{Word: "apple", Meaning: "苹果"},
			{Word: "banana", Meaning: "香蕉"},
		},
	})
	if err != nil {
		t.Fatalf("RunAdd: %v", err)
	}
	if addRes.Added != 2 {
		t.Fatalf("RunAdd added %d, want 2", addRes.Added)
	}

	res, err := b.RunRenameUnit(context.Background(), RenameUnitOpts{
		OldUnit:  "old-x",
		NewUnit:  "new-x",
		Category: "english",
	})
	if err != nil {
		t.Fatalf("RunRenameUnit: %v", err)
	}
	if res.Renamed != 2 || res.From != "old-x" || res.To != "new-x" {
		t.Fatalf("RenameUnitResult = %+v, want {renamed:2 from:old-x to:new-x}", res)
	}

	rows := listCards(t, env, cfg.VocabDBPath)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for _, row := range rows {
		if row["unit"] != "new-x" {
			t.Errorf("word %v still in unit %q, want new-x", row["word"], row["unit"])
		}
	}
}

// TestE2E_RunRenameUnitNotFound is the Pitfall 2 proof at the Bridge level:
// renaming a nonexistent unit makes the script print its "No cards found"
// diagnostic to STDOUT and exit 1, so the typed error must be
// errors.Is(ErrPythonExit) AND carry that diagnostic in ExitError.Stdout —
// an empty message here means the diagnostic was lost and the API layer
// could not tell the parent what went wrong.
func TestE2E_RunRenameUnitNotFound(t *testing.T) {
	b, _, _ := e2eBridge(t, nil)

	_, err := b.RunRenameUnit(context.Background(), RenameUnitOpts{
		OldUnit:  "ghost-unit",
		NewUnit:  "phantom-unit",
		Category: "english",
	})
	if err == nil {
		t.Fatal("RunRenameUnit on a nonexistent unit returned nil error, want exit-1 error")
	}
	if !errors.Is(err, ErrPythonExit) {
		t.Fatalf("err = %v, want errors.Is ErrPythonExit", err)
	}
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want recoverable *ExitError", err)
	}
	if !strings.Contains(ee.Stdout, "No cards found") {
		t.Errorf("ExitError.Stdout = %q, want it to contain the 'No cards found' diagnostic "+
			"(Pitfall 2: rename-unit prints it to STDOUT with exit 1)", ee.Stdout)
	}
}

// TestE2E_RunDeleteWords proves the delete path through the Bridge: three
// words added, two deleted by word list (Deleted:2, third remains), then the
// rest deleted by unit. Every invocation's argv carried --confirm — asserted
// VIA SUCCESS: without --confirm the script prints a non-JSON preview and
// exits 0, which the bridge would surface as ErrPythonEmpty/ErrPythonJSONParse
// instead of the typed Deleted count (Pitfall 1).
func TestE2E_RunDeleteWords(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	env := mgmtListEnv(t, cfg.DataDir)

	addRes, err := b.RunAdd(context.Background(), AddOpts{
		Category: "english",
		Unit:     "del-u",
		Entries: []AddEntry{
			{Word: "apple", Meaning: "苹果"},
			{Word: "banana", Meaning: "香蕉"},
			{Word: "cherry", Meaning: "樱桃"},
		},
	})
	if err != nil {
		t.Fatalf("RunAdd: %v", err)
	}
	if addRes.Added != 3 {
		t.Fatalf("RunAdd added %d, want 3", addRes.Added)
	}

	// Word-list mode: delete two of the three.
	delRes, err := b.RunDeleteWords(context.Background(), DeleteWordsOpts{
		Words:    []string{"apple", "banana"},
		Category: "english",
	})
	if err != nil {
		t.Fatalf("RunDeleteWords (words mode): %v", err)
	}
	if delRes.Deleted != 2 {
		t.Fatalf("words-mode Deleted = %d, want 2", delRes.Deleted)
	}
	rows := listCards(t, env, cfg.VocabDBPath)
	if len(rows) != 1 || rows[0]["word"] != "cherry" {
		t.Fatalf("after words-mode delete rows = %v, want only cherry", rows)
	}

	// Unit mode: delete the remaining card via its unit.
	delRes, err = b.RunDeleteWords(context.Background(), DeleteWordsOpts{
		Unit:     "del-u",
		Category: "english",
	})
	if err != nil {
		t.Fatalf("RunDeleteWords (unit mode): %v", err)
	}
	if delRes.Deleted != 1 {
		t.Fatalf("unit-mode Deleted = %d, want 1", delRes.Deleted)
	}
	rows = listCards(t, env, cfg.VocabDBPath)
	if len(rows) != 0 {
		t.Fatalf("after unit-mode delete rows = %v, want none", rows)
	}

	// WR-03: deleting a target that matches NOTHING is an idempotent
	// no-op — the script prints trailing {"deleted": 0} with exit 0, so
	// the Bridge must return a nil error and Deleted 0 (never
	// ErrPythonJSONParse → 502, which a stale second tab's delete would
	// have surfaced before the fix).
	delRes, err = b.RunDeleteWords(context.Background(), DeleteWordsOpts{
		Unit:     "ghost-unit",
		Category: "english",
	})
	if err != nil {
		t.Fatalf("RunDeleteWords (unit mode, not found): %v", err)
	}
	if delRes.Deleted != 0 {
		t.Fatalf("unit-mode not-found Deleted = %d, want 0", delRes.Deleted)
	}
	delRes, err = b.RunDeleteWords(context.Background(), DeleteWordsOpts{
		Words:    []string{"never-added"},
		Category: "english",
	})
	if err != nil {
		t.Fatalf("RunDeleteWords (words mode, not found): %v", err)
	}
	if delRes.Deleted != 0 {
		t.Fatalf("words-mode not-found Deleted = %d, want 0", delRes.Deleted)
	}
}
