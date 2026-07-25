//go:build e2e

// Real-Python end-to-end integration tests for the pybridge subsystem.
//
// Build tag: this file is compiled ONLY under `go test -tags=e2e` (and driven
// by `make py-e2e`). The default `go test` / `go test -short` runs never
// include it, so the bridge's unit tests (bridge_test.go, vocab_test.go,
// pregenerate_test.go) remain the fast, hermetic, fake-driven verification
// layer; these tests layer a REAL-subprocess check on top.
//
// What this proves that the unit tests cannot:
//   - The vendored vocab_manager.py actually imports + runs under a real CPython
//     interpreter inside a uv-built venv (fsrs imports, SQLite schema init, the
//     argparse --db/global/subcommand contract).
//   - (*Bridge).RunDue / RunReview drive that real script end-to-end and the
//     trailing-JSON contract round-trips into typed DueResult / ReviewResult
//     with the field shapes the bridge assumes.
//   - (*Bridge).RunPregenerate drives pregenerate_dictation.py's keyless
//     edge-tts fallback to actually write mp3s (Linux/WSL only — see the
//     fcntl note on TestE2E_PregenerateEdgeTTS).
//   - The 5-class error classification (logResult) fires on real timeout and
//     real non-zero exits, not just synthetic fixture bytes.
//   - The D-05/T-03-08 secret-forwarding-then-never-log invariant holds across
//     the full exec path (childEnv reads os.Getenv → cmd.Env → subprocess;
//     logResult's whitelist never carries env), not just at the logResult unit.
//
// Venv bootstrap is uv-based and lives in TestMain so every test shares one
// venv (bootstrap cost amortised once per `go test` invocation). The user
// preference for this repo is uv for all Python venv management; `make
// py-bootstrap` (deploy target) uses the same toolchain.
//
// These tests reuse the manual fakes defined in bridge_test.go (validTestCfg,
// newFakeLogger, fakeLogger, findField) because they live in the same package
// and compile together under -tags=e2e (D-48 manual-fake convention, no
// testify).
package pybridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// tsinghuaPyPI is the default pip/uv index for this China-based dev machine
// (memory: china-mirror-installs). Overridable via the standard UV_INDEX_URL so
// CI / non-China runners can point elsewhere without editing this file.
const tsinghuaPyPI = "https://pypi.tuna.tsinghua.edu.cn/simple"

// Package-level bootstrap state, populated once by TestMain and read by every
// E2E test via e2eBridge(). e2eInterpreter == "" means the real-Python path is
// unavailable on this host; tests then t.Skip with e2eSkipReason so the suite
// stays green on machines without uv/Python (CI sandboxes, fresh checkouts).
var (
	e2eInterpreter string // absolute path to the venv's python; "" => skip all
	e2eScriptsDir  string // absolute path to third_party/python (vendored scripts)
	e2eVenvDir     string // venv dir, recorded so TestMain cleanup can remove it
	e2eSkipReason  string // human-readable reason when e2eInterpreter == ""
)

// TestMain bootstraps the shared E2E venv (or reuses a caller-supplied one) and
// then runs the test binary. Bootstrap precedence (first match wins):
//  1. HTA_PYTHON_INTERPRETER — absolute interpreter path; skip venv creation
//     entirely (lets the operator point at `make py-bootstrap`'s venv).
//  2. HTA_E2E_VENV — an existing venv directory; TestMain resolves the
//     interpreter binary inside it (reuse across runs, no re-bootstrap).
//  3. uv bootstrap — `uv venv --python 3.12 <tmp>` + `uv pip install -r
//     third_party/python/requirements.txt` into a throwaway temp venv. Removed
//     on exit.
//
// If none of the above yields a working interpreter (uv absent, network down,
// Python 3.12 unavailable), TestMain sets e2eSkipReason and still calls m.Run;
// each E2E test then t.Skip(reason) individually so `go test -v` shows the skip
// per-test rather than a silent zero-test exit.
func TestMain(m *testing.M) {
	// Resolve the vendored-scripts dir from THIS file's path (pkg/pybridge) so
	// the tests work regardless of the CWD `go test` was invoked from.
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		e2eSkipReason = "runtime.Caller failed; cannot locate third_party/python"
		os.Exit(m.Run())
	}
	pkgDir := filepath.Dir(file)               // .../pkg/pybridge
	repoRoot := filepath.Dir(filepath.Dir(pkgDir)) // .../home-tutor-agent
	e2eScriptsDir = filepath.Join(repoRoot, "third_party", "python")

	switch {
	case os.Getenv("HTA_PYTHON_INTERPRETER") != "":
		e2eInterpreter = os.Getenv("HTA_PYTHON_INTERPRETER")
	case os.Getenv("HTA_E2E_VENV") != "":
		e2eInterpreter = venvInterpreter(os.Getenv("HTA_E2E_VENV"))
	default:
		interp, venvDir, reason, err := uvBootstrap(repoRoot)
		if err != nil {
			e2eSkipReason = reason
		} else {
			e2eInterpreter, e2eVenvDir = interp, venvDir
		}
	}

	code := m.Run()

	// Best-effort cleanup of the throwaway venv we created. Errors here are
	// non-fatal (the OS temp scavenger handles leftovers); do not mutate the
	// exit code.
	if e2eVenvDir != "" {
		_ = os.RemoveAll(e2eVenvDir)
	}
	os.Exit(code)
}

// uvBootstrap creates a throwaway venv via uv and installs the vendored
// requirements into it. Returns (interpreterPath, venvDir, "", nil) on success
// or ("", "", reason, err) on failure. The venv is created under the OS temp
// dir so it never touches the repo (unlike `make py-bootstrap`, which writes
// third_party/python/.venv for deploy reproducibility).
func uvBootstrap(repoRoot string) (interp, venvDir, reason string, err error) {
	if _, lookErr := exec.LookPath("uv"); lookErr != nil {
		return "", "", "uv not on PATH — install uv (https://docs.astral.sh/uv/) " +
			"or set HTA_PYTHON_INTERPRETER / HTA_E2E_VENV", lookErr
	}

	venvDir, err = os.MkdirTemp("", "hta-e2e-venv-*")
	if err != nil {
		return "", "", "mktemp failed: " + err.Error(), err
	}

	// `uv venv --python 3.12 <dir>` — uv finds or fetches a 3.12 interpreter
	// and materialises the venv at <dir>.
	venvCmd := exec.Command("uv", "venv", "--python", "3.12", venvDir)
	venvCmd.Stdout, venvCmd.Stderr = os.Stdout, os.Stderr
	if err = venvCmd.Run(); err != nil {
		return "", "", "uv venv failed: " + err.Error(), err
	}

	// Install vendored deps into the venv. --python takes the interpreter
	// binary inside the freshly-built venv; UV_INDEX_URL routes through the
	// China mirror (overridable) so the install is fast on this dev host.
	interp = venvInterpreter(venvDir)
	reqFile := filepath.Join(repoRoot, "third_party", "python", "requirements.txt")
	index := os.Getenv("UV_INDEX_URL")
	if index == "" {
		index = tsinghuaPyPI
	}
	installCmd := exec.Command("uv", "pip", "install",
		"--python", interp, "-r", reqFile)
	installCmd.Env = append(os.Environ(), "UV_INDEX_URL="+index)
	installCmd.Stdout, installCmd.Stderr = os.Stdout, os.Stderr
	if err = installCmd.Run(); err != nil {
		return "", "", "uv pip install failed: " + err.Error(), err
	}
	return interp, venvDir, "", nil
}

// venvInterpreter returns the python binary path inside a venv, accounting for
// the platform's layout: Windows uses Scripts/python.exe, POSIX uses bin/python.
func venvInterpreter(venvDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", "python.exe")
	}
	return filepath.Join(venvDir, "bin", "python")
}

// utf8ChildEnv returns os.Environ() with the UTF-8-forcing vars the bridge itself
// injects via childEnv (PYTHONUTF8 / PYTHONIOENCODING / PYTHONUNBUFFERED). Used
// by seedWords so the seeding subprocess behaves identically to a bridge-spawned
// one (no mojibake on Chinese vocabulary under a non-UTF-8 locale).
func utf8ChildEnv() []string {
	return append(os.Environ(),
		"PYTHONUTF8=1",
		"PYTHONIOENCODING=utf-8",
		"PYTHONUNBUFFERED=1",
	)
}

// e2eBridge builds a Bridge wired to the real bootstrapped venv + a fresh
// fakeLogger, with per-test temp dirs for every runtime path (DB / data / cache
// / manifest) so tests never collide. The optional mutate closure lets a test
// tweak one cfg field (e.g. shrink DueTimeout for the timeout test) after the
// E2E defaults are applied.
//
// Returns (bridge, logger, cfg). Tests assert on typed results AND on the
// logger's captured entries (for logResult error-type classification + the
// secret-no-leak scan).
func e2eBridge(t *testing.T, mutate func(*infra.PythonConfig)) (*Bridge, *fakeLogger, infra.PythonConfig) {
	t.Helper()
	if e2eInterpreter == "" {
		t.Skipf("real-Python E2E unavailable: %s", e2eSkipReason)
	}

	cfg := validTestCfg()
	cfg.Interpreter = e2eInterpreter
	cfg.ScriptsDir = e2eScriptsDir
	// The bridge invokes `python <cfg.VocabScript> ...` verbatim and does NOT
	// cd into ScriptsDir or join the path (verified: no cmd.Dir in bridge.run).
	// Use absolute script paths so the subprocess locates them regardless of
	// the test binary's CWD.
	cfg.VocabScript = filepath.Join(e2eScriptsDir, "vocab_manager.py")
	cfg.PregenScript = filepath.Join(e2eScriptsDir, "pregenerate_dictation.py")

	// Per-test runtime dirs. vocab_manager.py creates the DB on first open
	// (CREATE TABLE IF NOT EXISTS), so VocabDBPath's parent must exist; the
	// data/cache/manifest dirs are passed via DICTATION_* env to the subprocess
	// and must pre-exist for the scripts' os.makedirs calls.
	base := t.TempDir()
	cfg.VocabDBPath = filepath.Join(base, "vocab.db")
	cfg.DataDir = filepath.Join(base, "data")
	cfg.CacheDir = filepath.Join(base, "cache", "audio")
	cfg.ManifestDir = filepath.Join(base, "cache")
	cfg.EnvFile = "" // no non-secret .env for E2E
	for _, d := range []string{cfg.DataDir, cfg.CacheDir, cfg.ManifestDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	if mutate != nil {
		mutate(&cfg)
	}

	lg := newFakeLogger()
	b, err := NewBridge(cfg, lg)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	return b, lg, cfg
}

// seedWords invokes vocab_manager.py's `add` subcommand directly (the Bridge
// exposes no Add method — seeding is a test-setup concern, not a Phase 3
// contract surface) to insert words into cfg's DB. Each word becomes an FSRS
// card with due=now, so a subsequent RunDue returns them (vocab_manager.py:253
// "new cards have due=now, so always due").
func seedWords(t *testing.T, cfg infra.PythonConfig, words map[string]string) {
	t.Helper()
	payload, err := json.Marshal(words)
	if err != nil {
		t.Fatalf("marshal seed words: %v", err)
	}
	cmd := exec.CommandContext(context.Background(), cfg.Interpreter,
		cfg.VocabScript, "--db", cfg.VocabDBPath,
		"add", "--category", "english", "--words", string(payload))
	cmd.Env = utf8ChildEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("seed add failed: %v\n%s", err, out)
	}
	// add prints {"added": N, "skipped": N}; assert at least the expected
	// number landed so a silent integrity-conflict skip (duplicate seed) does
	// not masquerade as a bridge bug later.
	var rep struct {
		Added   int `json:"added"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal(bytesTrim(out), &rep); err != nil {
		t.Fatalf("parse add output %q: %v", string(out), err)
	}
	if rep.Added != len(words) {
		t.Fatalf("seed added %d, want %d (skipped %d): %s",
			rep.Added, len(words), rep.Skipped, string(out))
	}
}

// bytesTrim returns s with leading/trailing whitespace removed. add's output is
// a single JSON line but may carry a trailing newline from print(); json.Unmarshal
// tolerates that, but trimming keeps the error message in the caller readable.
func bytesTrim(s []byte) []byte { return []byte(strings.TrimSpace(string(s))) }

// assertNoSecretLeak fails the test if the sentinel substring appears anywhere
// in the captured log stream — in any message OR any field value. This is the
// E2E counterpart of TestLogResultNoSecretLeak: it proves that even though
// childEnv forwarded the sentinel value into the subprocess env (so the value
// genuinely transited the exec path), logResult's fixed field whitelist never
// let it reach the logger.
func assertNoSecretLeak(t *testing.T, lg *fakeLogger, sentinel string) {
	t.Helper()
	for _, e := range lg.msgs {
		if strings.Contains(e.Msg, sentinel) {
			t.Errorf("secret sentinel %q leaked into log msg %q", sentinel, e.Msg)
		}
		for _, f := range e.Fields {
			if strings.Contains(fmt.Sprint(f.Value), sentinel) {
				t.Errorf("secret sentinel %q leaked into log field %q=%v",
					sentinel, f.Key, f.Value)
			}
		}
	}
}

// findErrorType scans the captured log entries for one whose "error_type" field
// equals want and returns whether such an entry exists. Used by the
// error-classification tests to assert logResult emitted the right bucket.
func findErrorType(lg *fakeLogger, want string) bool {
	for _, e := range lg.msgs {
		if v, ok := findField(e.Fields, "error_type"); ok && fmt.Sprint(v) == want {
			return true
		}
	}
	return false
}

// TestE2E_RunDueRealPython proves the due path end-to-end against the real
// vendored vocab_manager.py: seed three English words, then RunDue returns a
// non-empty DueResult.Cards whose fields round-trip the script's JSON contract
// (id/word/meaning/unit/state/due). Peek:true avoids the fetch-lock side
// effect so the seeded cards remain queryable.
func TestE2E_RunDueRealPython(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	seedWords(t, cfg, map[string]string{"apple": "苹果", "banana": "香蕉", "cherry": "樱桃"})

	res, err := b.RunDue(context.Background(), DueOpts{Category: "english", Peek: true})
	if err != nil {
		t.Fatalf("RunDue: %v", err)
	}
	if len(res.Cards) != 3 {
		t.Fatalf("got %d cards, want 3", len(res.Cards))
	}
	for _, c := range res.Cards {
		if c.Word == "" || c.ID <= 0 || c.State == "" || c.Due == "" {
			t.Errorf("malformed card %+v: want non-empty Word/State/Due and ID>0", c)
		}
	}
}

// TestE2E_RunReviewRealPython proves the review path: seed words, then RunReview
// with three distinct ratings (good/hard/easy) returns ReviewResult.Updated
// entries carrying the FSRS scheduling fields (word/rating/next_due/state).
// Ratings pass through verbatim — the bridge neither validates nor rewrites
// them (RESEARCH Pitfall 4).
func TestE2E_RunReviewRealPython(t *testing.T) {
	b, _, cfg := e2eBridge(t, nil)
	seedWords(t, cfg, map[string]string{"apple": "苹果", "banana": "香蕉", "cherry": "樱桃"})

	res, err := b.RunReview(context.Background(), ReviewOpts{
		Category: "english",
		Results: map[string]string{
			"apple":  "good",
			"banana": "hard",
			"cherry": "easy",
		},
	})
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if len(res.Updated) != 3 {
		t.Fatalf("got %d updated, want 3", len(res.Updated))
	}
	for _, u := range res.Updated {
		if u.Word == "" || u.Rating == "" || u.NextDue == "" || u.State == "" {
			t.Errorf("malformed updated card %+v: want non-empty Word/Rating/NextDue/State", u)
		}
	}
}

// TestE2E_PregenerateEdgeTTS proves the pregenerate happy path end-to-end via
// pregenerate_dictation.py's keyless edge-tts fallback: with no VOLCENGINE_TTS_*
// secrets set, the script falls back to Microsoft Edge TTS, synthesises audio,
// runs ffmpeg, and prints absolute mp3 paths the bridge parses into
// PregenResult.Paths.
//
// Constraints:
//   - Windows SKIP: pregenerate_dictation.py:12 imports fcntl (POSIX-only);
//     the script cannot even start on Windows. Run under Linux/WSL to exercise
//     this path.
//   - Network SKIP: if edge-tts endpoints are unreachable (offline CI, captive
//     portal), the script exits non-zero with a "All TTS providers failed" /
//     connection diagnostic; that is an environment limitation, not a bridge
//     defect, so the test skips rather than fails.
func TestE2E_PregenerateEdgeTTS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pregenerate_dictation.py imports fcntl (POSIX-only); run under Linux/WSL")
	}
	b, _, _ := e2eBridge(t, nil)

	res, err := b.RunPregenerate(context.Background(), PregenOpts{
		Words:    []string{"hello", "world"},
		Language: "english",
	})
	if err != nil {
		var ee *ExitError
		if errors.As(err, &ee) && looksLikeNetworkFailure(ee.Stderr) {
			t.Skipf("edge-tts unreachable (network): %s", strings.TrimSpace(ee.Stderr))
		}
		t.Fatalf("RunPregenerate: %v", err)
	}
	if len(res.Paths) == 0 {
		t.Fatal("RunPregenerate returned no paths")
	}
	for _, p := range res.Paths {
		if info, statErr := os.Stat(p); statErr != nil || info.Size() == 0 {
			t.Errorf("expected non-empty mp3 at %s: %v", p, statErr)
		}
	}
}

// looksLikeNetworkFailure reports whether a pregenerate stderr text indicates
// an environment/network failure (no provider could synthesise) rather than a
// bridge-level defect. Used to downgrade such failures to t.Skip.
func looksLikeNetworkFailure(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, needle := range []string{
		"all tts providers failed",
		"network",
		"unreachable",
		"connection",
		"timed out",
		"timeout",
		"getaddrinfo",
		"dns",
		"edge",
		"no such host",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// TestE2E_ErrorClassification proves logResult's error-type buckets fire on
// REAL subprocess outcomes (the unit tests drive logResult with synthetic
// bytes/exit codes). Two sub-cases:
//
//	timeout — DueTimeout shrunk to 1ms: CPython startup (~30-100ms) always
//	exceeds it, the per-call context expires, b.run returns
//	context.DeadlineExceeded, RunDue wraps ErrPythonTimeout, and logResult
//	emits error_type=timeout.
//
//	nonzero_exit — VocabScript pointed at a non-existent file: CPython exits 2
//	("can't open file"), b.run returns *exec.ExitError, RunDue returns an
//	ExitError wrapping ErrPythonExit, and logResult emits error_type=nonzero_exit.
//
// The empty-stdout (ErrPythonEmpty) bucket is intentionally NOT exercised here:
// the real vocab_manager.py always prints JSON on exit 0, so it cannot be
// triggered without a fake script. That bucket stays covered by the
// fake-driven unit tests (TestClassifyPregenResult, TestRunDueParse).
func TestE2E_ErrorClassification(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		// Linux-only: the bridge detects timeouts via
		// errors.Is(err, context.DeadlineExceeded). On Windows, Go's exec
		// surfaces a context-killed subprocess as exec.ExitError("exit status 1")
		// with errors.Is(err, DeadlineExceeded)==false (and the context-afterfunc
		// vs reap ordering makes it a race), so the classification cannot be
		// exercised via a REAL subprocess on Windows. It is covered on Linux
		// here + by bridge_linux_test.go's process-group kill, and at the unit
		// level by logResult driven with a synthetic DeadlineExceeded.
		if runtime.GOOS == "windows" {
			t.Skip("real-subprocess timeout classification is Linux-only " +
				"(Windows exec returns exit-status-1 for context-killed processes)")
		}
		// A helper that sleeps well past the per-call timeout, so the context
		// fires WHILE the process is mid-execution (CPython is past startup and
		// blocked in time.sleep). On Linux the Cancel hook SIGKILLs the process
		// group and cmd.Run returns context.DeadlineExceeded, which RunDue maps
		// to ErrPythonTimeout.
		slow := filepath.Join(t.TempDir(), "slow.py")
		if err := os.WriteFile(slow, []byte("import time; time.sleep(30)\n"), 0o644); err != nil {
			t.Fatalf("write slow.py: %v", err)
		}
		b, lg, _ := e2eBridge(t, func(c *infra.PythonConfig) {
			c.DueTimeout = 300 * time.Millisecond
			c.VocabScript = slow
		})
		_, err := b.RunDue(context.Background(), DueOpts{Category: "english", Peek: true})
		if !errors.Is(err, ErrPythonTimeout) {
			t.Fatalf("RunDue err = %v, want ErrPythonTimeout", err)
		}
		if !findErrorType(lg, "timeout") {
			t.Error("log stream missing error_type=timeout entry")
		}
	})

	t.Run("nonzero_exit", func(t *testing.T) {
		b, lg, _ := e2eBridge(t, func(c *infra.PythonConfig) {
			c.VocabScript = filepath.Join(e2eScriptsDir, "does_not_exist.py")
		})
		_, err := b.RunDue(context.Background(), DueOpts{Category: "english", Peek: true})
		var ee *ExitError
		if !errors.As(err, &ee) || !errors.Is(err, ErrPythonExit) {
			t.Fatalf("RunDue err = %v, want *ExitError wrapping ErrPythonExit", err)
		}
		if !findErrorType(lg, "nonzero_exit") {
			t.Error("log stream missing error_type=nonzero_exit entry")
		}
	})
}

// TestE2E_SecretNoLeak proves the D-05/T-03-08 invariant across the FULL exec
// path: sentinel values are placed in the four VOLCENGINE_TTS_* env vars
// (which childEnv forwards into the subprocess env via os.Getenv), a real
// vocab_manager.py call runs, and NONE of the sentinel substrings appear in any
// captured log message or field. This layers on top of TestLogResultNoSecretLeak
// (which fakes a leaky stderr at the unit level) by exercising the real
// childEnv → cmd.Env → subprocess → logResult chain.
func TestE2E_SecretNoLeak(t *testing.T) {
	const (
		apiKey    = "e2e-sentinel-api-key"
		appID     = "e2e-sentinel-app-id"
		accessKey = "e2e-sentinel-access-key"
		resID     = "e2e-sentinel-resource-id"
	)
	// t.Setenv restores the prior values on test completion; the bridge reads
	// these via os.Getenv inside childEnv at run time.
	t.Setenv("VOLCENGINE_TTS_API_KEY", apiKey)
	t.Setenv("VOLCENGINE_TTS_APP_ID", appID)
	t.Setenv("VOLCENGINE_TTS_ACCESS_KEY", accessKey)
	t.Setenv("VOLCENGINE_TTS_RESOURCE_ID", resID)

	b, lg, cfg := e2eBridge(t, nil)
	seedWords(t, cfg, map[string]string{"apple": "苹果"})
	if _, err := b.RunDue(context.Background(), DueOpts{Category: "english", Peek: true}); err != nil {
		t.Fatalf("RunDue: %v", err)
	}
	for _, sentinel := range []string{apiKey, appID, accessKey, resID} {
		assertNoSecretLeak(t, lg, sentinel)
	}
}
