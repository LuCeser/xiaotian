// Bridge unit tests. These tests exercise the Plan 03-03 contract surface
// that does NOT depend on actually spawning a Python subprocess:
//   - NewBridge field-by-field validation
//   - NewBridge nil-logger substitution (D-42 / D-47)
//   - childEnv composition (UTF-8 + DICTATION_* + D-05 secret forwarding)
//   - logResult 5-class structured-log classification (SC-4 / T-03-08)
//
// The subprocess-spawning integration test lives in bridge_linux_test.go
// (//go:build linux) so Windows/macOS dev hosts run only the fake-driven
// tests via `go test -short ./pkg/pybridge/`. Test framework is the stdlib
// `testing` package with manual fakes (D-48, no testify/mock); see
// worker_test.go and middleware_test.go for the same convention.
package pybridge

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// validTestCfg returns a PythonConfig that satisfies every NewBridge /
// PythonConfig.Validate invariant. Tests that want to trigger a specific
// validation failure mutate one field of this base; tests that want a
// constructed Bridge pass it to NewBridge unchanged.
func validTestCfg() infra.PythonConfig {
	return infra.PythonConfig{
		Interpreter:   "/opt/hta/venv/bin/python",
		ScriptsDir:    "third_party/python",
		VocabScript:   "vocab_manager.py",
		PregenScript:  "pregenerate_dictation.py",
		VocabDBPath:   "/var/lib/hta/vocab.db",
		DataDir:       "/var/lib/hta/data",
		CacheDir:      "/var/lib/hta/cache",
		ManifestDir:   "/var/lib/hta/manifest",
		DueTimeout:    10 * time.Second,
		ReviewTimeout: 10 * time.Second,
		PregenTimeout: 120 * time.Second,
		MgmtTimeout:   10 * time.Second,
		MaxConcurrent: 2,
	}
}

// fakeLogger records every call so tests can assert on Level/Msg/Fields. The
// shape mirrors pkg/infra/middleware_test.go's fakeLogger (D-48 manual fake).
// Lives in the pybridge package because infra.fakeLogger is unexported.
type fakeLogger struct {
	msgs []fakeLogEntry
}

type fakeLogEntry struct {
	Level  string
	Msg    string
	Fields []infra.Field
}

func newFakeLogger() *fakeLogger { return &fakeLogger{} }

func (f *fakeLogger) record(level, msg string, fs []infra.Field) {
	// Copy fs so later test mutations do not retroactively change captured
	// entries (analogous to middleware_test.go's slice copy).
	cp := append([]infra.Field{}, fs...)
	f.msgs = append(f.msgs, fakeLogEntry{Level: level, Msg: msg, Fields: cp})
}

func (f *fakeLogger) Debug(m string, fs ...infra.Field) { f.record("debug", m, fs) }
func (f *fakeLogger) Info(m string, fs ...infra.Field)  { f.record("info", m, fs) }
func (f *fakeLogger) Warn(m string, fs ...infra.Field)  { f.record("warn", m, fs) }
func (f *fakeLogger) Error(m string, fs ...infra.Field) { f.record("error", m, fs) }

// With returns a child fakeLogger that shares the parent's record buffer so a
// test can observe entries emitted via the child. Matches ZapLogger.With's
// semantics; not strictly needed by the current tests but cheap to include.
func (f *fakeLogger) With(_ ...infra.Field) infra.Logger { return f }

func (f *fakeLogger) Sync() error { return nil }

// findField returns the first Field with the given key in the slice and a
// boolean indicating presence. Mirrors middleware_test.go's helper.
func findField(fields []infra.Field, key string) (any, bool) {
	for _, f := range fields {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

// envHas reports whether the "KEY=VALUE" string slice contains an entry that
// starts with the given prefix. Used to assert childEnv composition without
// caring about the full value (which may include the operator's real path).
func envHas(env []string, prefix string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// TestNewBridgeValidation exercises every cfg-validation branch of NewBridge.
// Each subtest mutates exactly one field of an otherwise-valid cfg and asserts
// errors.Is(err, infra.ErrConfigInvalid). Mirrors config_test.go's
// TestLoadConfigInvalid* style.
func TestNewBridgeValidation(t *testing.T) {
	cases := []struct {
		mutate func(*infra.PythonConfig)
		name   string
	}{
		{mutate: func(c *infra.PythonConfig) { c.Interpreter = "" }, name: "empty interpreter"},
		{mutate: func(c *infra.PythonConfig) { c.ScriptsDir = "" }, name: "empty scriptsDir"},
		{mutate: func(c *infra.PythonConfig) { c.VocabScript = "" }, name: "empty vocabScript"},
		{mutate: func(c *infra.PythonConfig) { c.PregenScript = "" }, name: "empty pregenScript"},
		{mutate: func(c *infra.PythonConfig) { c.MaxConcurrent = 0 }, name: "maxConcurrent zero"},
		{mutate: func(c *infra.PythonConfig) { c.MaxConcurrent = 5 }, name: "maxConcurrent five"},
		{mutate: func(c *infra.PythonConfig) { c.DueTimeout = 0 }, name: "dueTimeout zero"},
		{mutate: func(c *infra.PythonConfig) { c.ReviewTimeout = 0 }, name: "reviewTimeout zero"},
		{mutate: func(c *infra.PythonConfig) { c.PregenTimeout = -1 * time.Second }, name: "pregenTimeout negative"},
		{mutate: func(c *infra.PythonConfig) { c.MgmtTimeout = 0 }, name: "mgmtTimeout zero"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validTestCfg()
			tc.mutate(&cfg)
			b, err := NewBridge(cfg, nil)
			if err == nil {
				t.Fatalf("NewBridge returned nil error, want ErrConfigInvalid")
			}
			if !errors.Is(err, infra.ErrConfigInvalid) {
				t.Errorf("NewBridge error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
			}
			if b != nil {
				t.Errorf("NewBridge returned non-nil *Bridge on validation failure")
			}
		})
	}
}

// TestNewBridgeValid asserts the happy path: a fully-valid cfg plus a real
// logger returns a non-nil Bridge with no error.
func TestNewBridgeValid(t *testing.T) {
	b, err := NewBridge(validTestCfg(), newFakeLogger())
	if err != nil {
		t.Fatalf("NewBridge returned unexpected error: %v", err)
	}
	if b == nil {
		t.Fatalf("NewBridge returned nil *Bridge on valid cfg")
	}
}

// TestNewBridgeNilLogger asserts the D-42 nil-logger substitution: a nil
// logger argument must NOT panic, and the resulting Bridge must route log
// calls through infra.NewNoOpLogger() without crashing. We exercise logResult
// because it is the only log call site in this plan; routing a nil-err success
// entry through it is enough to prove the substitution worked end-to-end.
func TestNewBridgeNilLogger(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewBridge or logResult panicked on nil logger: %v", r)
		}
	}()
	b, err := NewBridge(validTestCfg(), nil)
	if err != nil {
		t.Fatalf("NewBridge returned unexpected error on nil logger: %v", err)
	}
	if b == nil {
		t.Fatalf("NewBridge returned nil *Bridge on nil logger")
	}
	// Driving a log call exercises the substituted NoOpLogger. If the
	// substitution failed (logger still nil), this dereferences a nil
	// interface and panics -- caught by the defer above.
	b.logResult("TestNewBridgeNilLogger", time.Now(), nil, nil, 0, nil)
}

// TestChildEnvSecrets asserts childEnv composition (D-05 + UTF-8 + DICTATION_*).
// Uses t.Setenv so the test process's env is restored on cleanup (Go 1.17+).
func TestChildEnvSecrets(t *testing.T) {
	// Set one D-05 secret. We deliberately do NOT set the other three so we
	// can also assert childEnv omits unset secrets (defence against future
	// refactors that might blindly append placeholder entries).
	t.Setenv("VOLCENGINE_TTS_API_KEY", "test-key-from-parent-env")
	// Explicitly UNSET the other three (t.Setenv with "" still keeps the
	// var present in os.Environ() as an empty entry, which would confuse
	// the leak assertion below). Manual save/restore via Cleanup so we do
	// not pollute the test process env after this test returns.
	for _, k := range []string{
		"VOLCENGINE_TTS_APP_ID",
		"VOLCENGINE_TTS_ACCESS_KEY",
		"VOLCENGINE_TTS_RESOURCE_ID",
	} {
		orig, had := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, orig)
			}
		})
	}

	b, err := NewBridge(validTestCfg(), nil)
	if err != nil {
		t.Fatalf("NewBridge returned unexpected error: %v", err)
	}

	env := b.childEnv()

	// (a) UTF-8 forcing is unconditional.
	if !envHas(env, "PYTHONUTF8=1") {
		t.Errorf("childEnv: missing PYTHONUTF8=1 (Chinese text mojibake defence)")
	}
	if !envHas(env, "PYTHONIOENCODING=utf-8") {
		t.Errorf("childEnv: missing PYTHONIOENCODING=utf-8")
	}
	if !envHas(env, "PYTHONUNBUFFERED=1") {
		t.Errorf("childEnv: missing PYTHONUNBUFFERED=1 (stdout flush before Wait)")
	}

	// (b) DICTATION_* operational paths come from cfg.
	if !envHas(env, "DICTATION_DB_PATH="+"/var/lib/hta/vocab.db") {
		t.Errorf("childEnv: missing DICTATION_DB_PATH from cfg.VocabDBPath")
	}
	if !envHas(env, "DICTATION_DATA_DIR=") {
		t.Errorf("childEnv: missing DICTATION_DATA_DIR")
	}
	if !envHas(env, "DICTATION_CACHE_DIR=") {
		t.Errorf("childEnv: missing DICTATION_CACHE_DIR")
	}
	if !envHas(env, "DICTATION_MANIFEST_DIR=") {
		t.Errorf("childEnv: missing DICTATION_MANIFEST_DIR")
	}

	// (c) D-05: VOLCENGINE_TTS_API_KEY forwarded from parent env verbatim.
	if !envHas(env, "VOLCENGINE_TTS_API_KEY=test-key-from-parent-env") {
		t.Errorf("childEnv: VOLCENGINE_TTS_API_KEY not forwarded from os.Getenv")
	}

	// (d) Defence: the three unset secrets are NOT appended as placeholders.
	// The bridge must ONLY append secrets whose os.Getenv returns non-empty.
	for _, k := range []string{
		"VOLCENGINE_TTS_APP_ID=",
		"VOLCENGINE_TTS_ACCESS_KEY=",
		"VOLCENGINE_TTS_RESOURCE_ID=",
	} {
		if envHas(env, k) {
			t.Errorf("childEnv: unset secret %q leaked into child env as placeholder", k)
		}
	}
}

// TestChildEnvExtraPassthrough asserts the variadic `extra` argument is
// appended verbatim. (Currently unused by callers; kept so future per-call
// overrides in Plan 03-04/05 do not silently regress.)
func TestChildEnvExtraPassthrough(t *testing.T) {
	b, err := NewBridge(validTestCfg(), nil)
	if err != nil {
		t.Fatalf("NewBridge returned unexpected error: %v", err)
	}
	env := b.childEnv("DICTATION_TEST_OVERRIDE=1", "DEBUG_PYBRIDGE=1")
	if !envHas(env, "DICTATION_TEST_OVERRIDE=1") {
		t.Errorf("childEnv: extra[0] not appended")
	}
	if !envHas(env, "DEBUG_PYBRIDGE=1") {
		t.Errorf("childEnv: extra[1] not appended")
	}
}

// TestLogResultClassification exercises every branch of logResult's switch.
// Each subtest drives logResult with inputs that hit exactly one branch and
// then asserts the fakeLogger captured the expected Level, Msg, and Fields
// (notably error_type for the failure branches). This is the SC-4 / T-03-08
// acceptance surface: structured error classification + log field whitelist.
func TestLogResultClassification(t *testing.T) {
	cases := []struct {
		err        error
		name       string
		wantLevel  string
		wantMsg    string
		wantErrTyp string // "" means: error_type field MUST NOT be present
		exit       int
	}{
		{
			name:      "success exit 0 nil err",
			exit:      0,
			err:       nil,
			wantLevel: "debug",
			wantMsg:   "python subprocess ok",
		},
		{
			name:       "context deadline exceeded",
			exit:       -1,
			err:        context.DeadlineExceeded,
			wantLevel:  "error",
			wantMsg:    "python subprocess timed out",
			wantErrTyp: "timeout",
		},
		{
			name:       "non-zero exit",
			exit:       1,
			err:        nil,
			wantLevel:  "error",
			wantMsg:    "python subprocess failed",
			wantErrTyp: "nonzero_exit",
		},
		{
			name:       "other err (exec.ErrWaitDelay-like)",
			exit:       0,
			err:        errors.New("some other infrastructure error"),
			wantLevel:  "error",
			wantMsg:    "python subprocess error",
			wantErrTyp: "other",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fl := newFakeLogger()
			b := &Bridge{cfg: validTestCfg(), logger: fl, sem: newSemaphore(1)}
			b.logResult("testMethod", time.Now(), []byte("out"), []byte("err"), tc.exit, tc.err)

			if len(fl.msgs) != 1 {
				t.Fatalf("logResult emitted %d entries, want 1", len(fl.msgs))
			}
			got := fl.msgs[0]
			if got.Level != tc.wantLevel {
				t.Errorf("level: got %q, want %q", got.Level, tc.wantLevel)
			}
			if got.Msg != tc.wantMsg {
				t.Errorf("msg: got %q, want %q", got.Msg, tc.wantMsg)
			}

			// error_type whitelist: success branch must NOT carry it; every
			// failure branch must carry the expected value.
			errType, hasErrType := findField(got.Fields, "error_type")
			if tc.wantErrTyp == "" {
				if hasErrType {
					t.Errorf("error_type field leaked into success entry: %v", errType)
				}
			} else {
				if !hasErrType {
					t.Fatalf("error_type field missing; want %q", tc.wantErrTyp)
				}
				if errType != tc.wantErrTyp {
					t.Errorf("error_type: got %v, want %q", errType, tc.wantErrTyp)
				}
			}

			// Every entry carries the invariant fields. This is the SC-4
			// contract: structured classification always identifies what
			// command ran and how long it took.
			for _, k := range []string{"component", "command", "exit_code", "duration_ms"} {
				if _, ok := findField(got.Fields, k); !ok {
					t.Errorf("missing invariant field %q in %q entry", k, tc.name)
				}
			}
			// Component is always "pybridge" so operators can filter.
			if comp, _ := findField(got.Fields, "component"); comp != "pybridge" {
				t.Errorf("component: got %v, want \"pybridge\"", comp)
			}
		})
	}
}

// TestLogResultNoSecretLeak is the T-03-08 dedicated regression guard: drive
// logResult with a stderr payload that *contains* a fake secret-looking
// string, then assert that no field in the captured entry accidentally
// carries the pythonSecretEnvKeys names. The structured fields are a fixed
// whitelist; this test catches a future refactor that adds an "env" field or
// dumps the full childEnv to the log.
func TestLogResultNoSecretLeak(t *testing.T) {
	fl := newFakeLogger()
	b := &Bridge{cfg: validTestCfg(), logger: fl, sem: newSemaphore(1)}
	// Hostile stderr payload an upstream script might emit if its own log
	// line accidentally included env context.
	hostile := []byte("upstream diagnostic: VOLCENGINE_TTS_API_KEY=super-secret\n")
	b.logResult("TestLogResultNoSecretLeak", time.Now(), []byte("out"), hostile, 1, nil)

	if len(fl.msgs) != 1 {
		t.Fatalf("logResult emitted %d entries, want 1", len(fl.msgs))
	}
	// Whitelist of field keys the bridge is allowed to log. Anything else
	// is a T-03-08 regression.
	allowed := map[string]bool{
		"component":   true,
		"command":     true,
		"exit_code":   true,
		"duration_ms": true,
		"error_type":  true,
		"stderr":      true,
		"stdout":      true,
		"error":       true, // ErrField for the "other" branch
	}
	for _, f := range fl.msgs[0].Fields {
		if !allowed[f.Key] {
			t.Errorf("non-whitelisted field %q leaked into log entry (T-03-08)", f.Key)
		}
		if f.Key == "key" || strings.Contains(strings.ToLower(f.Key), "secret") {
			t.Errorf("suspicious field name %q in log entry", f.Key)
		}
	}
}

func TestNormalizeRunErr(t *testing.T) {
	killed := errors.New("signal: killed")

	deadlineCtx, cancelDeadline := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelDeadline()
	time.Sleep(time.Millisecond)

	got := normalizeRunErr(deadlineCtx, killed)
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Errorf("normalizeRunErr(deadline, killed) = %v, want DeadlineExceeded", got)
	}
	if !errors.Is(got, killed) {
		t.Errorf("normalizeRunErr dropped the process error: %v", got)
	}

	got = normalizeRunErr(deadlineCtx, nil)
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Errorf("normalizeRunErr(deadline, nil) = %v, want DeadlineExceeded", got)
	}
	got = normalizeRunErr(deadlineCtx, context.DeadlineExceeded)
	if !errors.Is(got, context.DeadlineExceeded) || got != context.DeadlineExceeded {
		t.Errorf("already-deadline error was rewrapped: %v", got)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	got = normalizeRunErr(canceledCtx, killed)
	if !errors.Is(got, context.Canceled) {
		t.Errorf("normalizeRunErr(canceled, killed) = %v, want Canceled", got)
	}

	live := context.Background()
	if got := normalizeRunErr(live, killed); got != killed {
		t.Errorf("live context changed err: %v", got)
	}
	if got := normalizeRunErr(live, nil); got != nil {
		t.Errorf("live context with nil err = %v, want nil", got)
	}
}
