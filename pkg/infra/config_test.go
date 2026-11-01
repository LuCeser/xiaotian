// Package infra's tests exercise the typed Config layer (D-28 table-driven,
// D-44 centralized validation). These tests intentionally use only the
// standard library testing package — testify was unavailable at plan-execution
// time (network blocked) and the plan explicitly permits stdlib testing as a
// fallback. Phase 2 (pkg/ws) established plain testing as the repo pattern.
package infra

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setRequiredPythonEnv installs the python.* env vars that have no default
// in DefaultConfig but are required by Config.Validate (interpreter,
// scriptsDir, and — since WR-05 — dataDir, which anchors the
// fsrs_config.json writer and the DICTATION_DATA_DIR reader on one
// directory). Tests that exercise LoadConfig expecting success call this
// first; tests that exercise a python validation failure do NOT (they want
// Validate to fail on the python field they're testing).
func setRequiredPythonEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HTA_PYTHON_INTERPRETER", "/opt/hta/venv/bin/python")
	t.Setenv("HTA_PYTHON_SCRIPTSDIR", "third_party/python")
	t.Setenv("HTA_PYTHON_DATADIR", t.TempDir())
}

// TestDefaultConfig asserts the phase-1 baseline configuration (D-52 lowest
// precedence layer). Every value here is what LoadConfig MUST return when no
// file or environment override is supplied.
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("memory defaults per RES-01 and D-70", func(t *testing.T) {
		if cfg.Memory.LimitBytes != MemoryBytes(150*1024*1024) {
			t.Errorf("Memory.LimitBytes = %d, want %d (150MB on a 1C/1G VPS, RES-01)",
				cfg.Memory.LimitBytes, MemoryBytes(150*1024*1024))
		}
		if cfg.Memory.MonitorInterval != 5*time.Second {
			t.Errorf("Memory.MonitorInterval = %v, want 5s", cfg.Memory.MonitorInterval)
		}
		wantThresholds := []Percent{80, 90, 95}
		if len(cfg.Memory.Thresholds) != len(wantThresholds) {
			t.Fatalf("Memory.Thresholds length = %d, want %d (%v)",
				len(cfg.Memory.Thresholds), len(wantThresholds), wantThresholds)
		}
		for i, want := range wantThresholds {
			if cfg.Memory.Thresholds[i] != want {
				t.Errorf("Memory.Thresholds[%d] = %d, want %d", i, cfg.Memory.Thresholds[i], want)
			}
		}
		if cfg.Memory.LoadShedAtPercent != Percent(95) {
			t.Errorf("Memory.LoadShedAtPercent = %d, want 95 (D-18)", cfg.Memory.LoadShedAtPercent)
		}
	})

	t.Run("session defaults per RES-03 and D-57", func(t *testing.T) {
		if cfg.Session.MaxConcurrent != 100 {
			t.Errorf("Session.MaxConcurrent = %d, want 100 (RES-03, D-56)", cfg.Session.MaxConcurrent)
		}
		if cfg.Session.IdleTimeout != 5*time.Minute {
			t.Errorf("Session.IdleTimeout = %v, want 5m", cfg.Session.IdleTimeout)
		}
		if cfg.Session.CleanupInterval != 30*time.Second {
			t.Errorf("Session.CleanupInterval = %v, want 30s (D-57)", cfg.Session.CleanupInterval)
		}
	})

	t.Run("buffer defaults per D-71 and D-73", func(t *testing.T) {
		if cfg.Buffer.AudioFrameBytes != 640 {
			t.Errorf("Buffer.AudioFrameBytes = %d, want 640 (20ms@16kHz PCM, D-71)",
				cfg.Buffer.AudioFrameBytes)
		}
		if cfg.Buffer.PoolSize != 1000 {
			t.Errorf("Buffer.PoolSize = %d, want 1000", cfg.Buffer.PoolSize)
		}
		if cfg.Buffer.Strategy != StrategyDropOldest {
			t.Errorf("Buffer.Strategy = %q, want %q (D-73)", cfg.Buffer.Strategy, StrategyDropOldest)
		}
	})

	t.Run("server defaults per D-22", func(t *testing.T) {
		if cfg.Server.Addr != "0.0.0.0:8080" {
			t.Errorf("Server.Addr = %q, want %q", cfg.Server.Addr, "0.0.0.0:8080")
		}
		if cfg.Server.ReadTimeout != 10*time.Second {
			t.Errorf("Server.ReadTimeout = %v, want 10s (D-22)", cfg.Server.ReadTimeout)
		}
		if cfg.Server.WriteTimeout != 10*time.Second {
			t.Errorf("Server.WriteTimeout = %v, want 10s", cfg.Server.WriteTimeout)
		}
		if cfg.Server.HealthAddr != "127.0.0.1:8081" {
			t.Errorf("Server.HealthAddr = %q, want %q", cfg.Server.HealthAddr, "127.0.0.1:8081")
		}
	})

	t.Run("log defaults", func(t *testing.T) {
		if cfg.Log.Level != "info" {
			t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "info")
		}
		if cfg.Log.Format != "json" {
			t.Errorf("Log.Format = %q, want %q", cfg.Log.Format, "json")
		}
		if cfg.Log.Output != "stderr" {
			t.Errorf("Log.Output = %q, want %q", cfg.Log.Output, "stderr")
		}
	})

	t.Run("worker defaults — pool scales with NumCPU", func(t *testing.T) {
		if cfg.Worker.PoolSize <= 0 {
			t.Errorf("Worker.PoolSize = %d, want > 0 (2*runtime.NumCPU())", cfg.Worker.PoolSize)
		}
		if cfg.Worker.QueueSize != 256 {
			t.Errorf("Worker.QueueSize = %d, want 256", cfg.Worker.QueueSize)
		}
	})

	t.Run("connection defaults per D-22 and D-66", func(t *testing.T) {
		if cfg.Connection.ThrottleRatePerSec != 100 {
			t.Errorf("Connection.ThrottleRatePerSec = %d, want 100 (D-66)",
				cfg.Connection.ThrottleRatePerSec)
		}
		if cfg.Connection.ThrottleBurst != 10 {
			t.Errorf("Connection.ThrottleBurst = %d, want 10", cfg.Connection.ThrottleBurst)
		}
		if cfg.Connection.DialTimeout != 5*time.Second {
			t.Errorf("Connection.DialTimeout = %v, want 5s (D-22)", cfg.Connection.DialTimeout)
		}
	})

	t.Run("shutdown defaults per D-21", func(t *testing.T) {
		if cfg.Shutdown.Timeout != 30*time.Second {
			t.Errorf("Shutdown.Timeout = %v, want 30s (graceful drain, D-21)",
				cfg.Shutdown.Timeout)
		}
	})

	t.Run("python defaults per D-02/D-03/D-04", func(t *testing.T) {
		// Interpreter and ScriptsDir are intentionally empty in DefaultConfig
		// — they are deployment-specific and must come from env/file. The
		// non-empty defaults below are the only ones the plan locks in.
		if cfg.Python.VocabScript != "vocab_manager.py" {
			t.Errorf("Python.VocabScript = %q, want %q (D-03 vendor)",
				cfg.Python.VocabScript, "vocab_manager.py")
		}
		if cfg.Python.PregenScript != "pregenerate_dictation.py" {
			t.Errorf("Python.PregenScript = %q, want %q (D-03 vendor)",
				cfg.Python.PregenScript, "pregenerate_dictation.py")
		}
		if cfg.Python.DueTimeout != 10*time.Second {
			t.Errorf("Python.DueTimeout = %v, want 10s", cfg.Python.DueTimeout)
		}
		if cfg.Python.ReviewTimeout != 10*time.Second {
			t.Errorf("Python.ReviewTimeout = %v, want 10s", cfg.Python.ReviewTimeout)
		}
		if cfg.Python.PregenTimeout != 120*time.Second {
			t.Errorf("Python.PregenTimeout = %v, want 120s (TTS+ffmpeg slower, D-01)",
				cfg.Python.PregenTimeout)
		}
		if cfg.Python.MaxConcurrent != 2 {
			t.Errorf("Python.MaxConcurrent = %d, want 2 (D-02 global semaphore cap)",
				cfg.Python.MaxConcurrent)
		}
	})

	t.Run("ws defaults — dev-safe origins, heartbeat, double-validation zeros", func(t *testing.T) {
		// AllowedOrigins defaults to the localhost pair so the service boots
		// from defaults with NO ws: block in config.yaml (P0-1 must-have).
		want := []string{"localhost", "127.0.0.1"}
		if len(cfg.WS.AllowedOrigins) != len(want) {
			t.Fatalf("WS.AllowedOrigins length = %d, want %d (%v)",
				len(cfg.WS.AllowedOrigins), len(want), want)
		}
		for i, w := range want {
			if cfg.WS.AllowedOrigins[i] != w {
				t.Errorf("WS.AllowedOrigins[%d] = %q, want %q", i, cfg.WS.AllowedOrigins[i], w)
			}
		}
		if cfg.WS.HeartbeatInterval != 30*time.Second {
			t.Errorf("WS.HeartbeatInterval = %v, want 30s (ws.DefaultHeartbeatInterval)",
				cfg.WS.HeartbeatInterval)
		}
		// Zero means "let ws.NewServer apply its own default" — infra does NOT
		// pre-fill 65551/4096 (double-validation design).
		if cfg.WS.MaxMessageBytes != 0 {
			t.Errorf("WS.MaxMessageBytes = %d, want 0 (defer to ws package default)",
				cfg.WS.MaxMessageBytes)
		}
		if cfg.WS.ReadBufferSize != 0 {
			t.Errorf("WS.ReadBufferSize = %d, want 0 (defer to ws package default)",
				cfg.WS.ReadBufferSize)
		}
		if cfg.WS.WriteBufferSize != 0 {
			t.Errorf("WS.WriteBufferSize = %d, want 0 (defer to ws package default)",
				cfg.WS.WriteBufferSize)
		}
		// Hub/router buffer fields are non-zero usable defaults.
		if cfg.WS.HubLimit != 100 {
			t.Errorf("WS.HubLimit = %d, want 100 (D-04 / session.maxConcurrent parity)",
				cfg.WS.HubLimit)
		}
		if cfg.WS.HubRegisterBuffer != 32 {
			t.Errorf("WS.HubRegisterBuffer = %d, want 32", cfg.WS.HubRegisterBuffer)
		}
		if cfg.WS.HubUnregisterBuffer != 32 {
			t.Errorf("WS.HubUnregisterBuffer = %d, want 32", cfg.WS.HubUnregisterBuffer)
		}
		if cfg.WS.RouteBuffer != 32 {
			t.Errorf("WS.RouteBuffer = %d, want 32 (ws.RouterConfig default)",
				cfg.WS.RouteBuffer)
		}
		if cfg.WS.WorkerConcurrency != 4 {
			t.Errorf("WS.WorkerConcurrency = %d, want 4 (ws.RouterConfig default)",
				cfg.WS.WorkerConcurrency)
		}
	})

	t.Run("voice defaults — boots with no voice: block (Ticket 03)", func(t *testing.T) {
		if cfg.Voice.BaseURL != "https://api.xiaomimimo.com/v1" {
			t.Errorf("Voice.BaseURL = %q, want pkg/mimo.NewClient default",
				cfg.Voice.BaseURL)
		}
		if cfg.Voice.ChatModel != "mimo-v2.6-flash" {
			t.Errorf("Voice.ChatModel = %q, want mimo-v2.6-flash (deliberate duplicate of the pkg/mimo default; see VoiceConfig docs)",
				cfg.Voice.ChatModel)
		}
		if cfg.Voice.MaxRecordingBytes != 327680 {
			t.Errorf("Voice.MaxRecordingBytes = %d, want 327680 (~10.2s 16kHz PCM)",
				cfg.Voice.MaxRecordingBytes)
		}
		if cfg.Voice.AssembleWait != 2*time.Second {
			t.Errorf("Voice.AssembleWait = %v, want 2s", cfg.Voice.AssembleWait)
		}
		if cfg.Voice.RequestTimeout != 15*time.Second {
			t.Errorf("Voice.RequestTimeout = %v, want 15s", cfg.Voice.RequestTimeout)
		}
		// Defaults must satisfy Validate (boots from defaults, D-04).
		if err := cfg.Voice.Validate(); err != nil {
			t.Errorf("default VoiceConfig fails Validate: %v", err)
		}
	})

	t.Run("photo defaults — boots with no photo: block (Ticket 04)", func(t *testing.T) {
		if cfg.Photo.ChatModel != "mimo-v2.6-flash" {
			t.Errorf("Photo.ChatModel = %q, want mimo-v2.6-flash (vision-capable, same default as voice)",
				cfg.Photo.ChatModel)
		}
		if cfg.Photo.MaxImageBytes != 1048576 {
			t.Errorf("Photo.MaxImageBytes = %d, want 1048576 (1MiB cap, 1C1G memory guard)",
				cfg.Photo.MaxImageBytes)
		}
		if cfg.Photo.AssembleWait != 2*time.Second {
			t.Errorf("Photo.AssembleWait = %v, want 2s", cfg.Photo.AssembleWait)
		}
		if cfg.Photo.RequestTimeout != 60*time.Second {
			t.Errorf("Photo.RequestTimeout = %v, want 60s", cfg.Photo.RequestTimeout)
		}
		if cfg.Photo.ConfidenceThreshold != 0.6 {
			t.Errorf("Photo.ConfidenceThreshold = %v, want 0.6", cfg.Photo.ConfidenceThreshold)
		}
		// Defaults must satisfy Validate (boots from defaults, D-04).
		if err := cfg.Photo.Validate(); err != nil {
			t.Errorf("default PhotoConfig fails Validate: %v", err)
		}
	})
}

// TestLoadConfigDefaults verifies the lowest precedence layer: with no file
// and no env vars, LoadConfig returns DefaultConfig() verbatim.
func TestLoadConfigDefaults(t *testing.T) {
	setRequiredPythonEnv(t)
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig('') returned error %v, want nil", err)
	}
	def := DefaultConfig()
	if cfg.Memory.LimitBytes != def.Memory.LimitBytes {
		t.Errorf("Memory.LimitBytes = %d, want default %d", cfg.Memory.LimitBytes, def.Memory.LimitBytes)
	}
	if cfg.Session.MaxConcurrent != def.Session.MaxConcurrent {
		t.Errorf("Session.MaxConcurrent = %d, want default %d",
			cfg.Session.MaxConcurrent, def.Session.MaxConcurrent)
	}
	if cfg.Buffer.AudioFrameBytes != def.Buffer.AudioFrameBytes {
		t.Errorf("Buffer.AudioFrameBytes = %d, want default %d",
			cfg.Buffer.AudioFrameBytes, def.Buffer.AudioFrameBytes)
	}
	if cfg.Server.Addr != def.Server.Addr {
		t.Errorf("Server.Addr = %q, want default %q", cfg.Server.Addr, def.Server.Addr)
	}
}

// TestLoadConfigGracefulMissingFile verifies the file-not-found path: passing
// a path to a nonexistent file is NOT an error — the loader falls back to
// defaults + env. This is required so the service can boot without an
// operator-supplied config file (D-03 environment-based default behaviour).
func TestLoadConfigGracefulMissingFile(t *testing.T) {
	setRequiredPythonEnv(t)
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig(nonexistent) returned error %v, want nil (graceful file-not-found)",
			err)
	}
	def := DefaultConfig()
	if cfg.Session.MaxConcurrent != def.Session.MaxConcurrent {
		t.Errorf("Session.MaxConcurrent = %d, want default %d",
			cfg.Session.MaxConcurrent, def.Session.MaxConcurrent)
	}
}

// TestLoadConfigEnvOverride verifies the env-var layer (D-52 middle
// precedence). HTA_MEMORY_LIMITBYTES overrides the default 150MB with 100MB.
func TestLoadConfigEnvOverride(t *testing.T) {
	setRequiredPythonEnv(t)
	t.Setenv("HTA_MEMORY_LIMITBYTES", "104857600") // 100MB
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig returned error %v, want nil", err)
	}
	if cfg.Memory.LimitBytes != MemoryBytes(104857600) {
		t.Errorf("Memory.LimitBytes = %d, want 104857600 (env override)",
			cfg.Memory.LimitBytes)
	}
}

// TestLoadConfigFileOverride verifies the file layer (D-52 middle precedence,
// below env). A yaml file setting session.maxConcurrent=50 overrides the
// default of 100.
func TestLoadConfigFileOverride(t *testing.T) {
	setRequiredPythonEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("session:\n  maxConcurrent: 50\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error %v", path, err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s) error %v, want nil", path, err)
	}
	if cfg.Session.MaxConcurrent != 50 {
		t.Errorf("Session.MaxConcurrent = %d, want 50 (file override)",
			cfg.Session.MaxConcurrent)
	}
}

// TestLoadConfigFileDurations is a regression guard for the time.Duration
// parsing path. Supplying a custom viper DecodeHook makes viper skip its
// default StringToTimeDurationHookFunc; without that hook composed back in,
// every duration string ("5s"/"30s") in a real config.yaml fails to parse
// via strconv.ParseInt and the service cannot boot. This test writes
// duration-bearing YAML — the gap that hid the bug — so a future regression
// of the hook composition fails here instead of at process startup.
func TestLoadConfigFileDurations(t *testing.T) {
	setRequiredPythonEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte(strings.Join([]string{
		"memory:",
		"  monitorInterval: 7s",
		"session:",
		"  idleTimeout: 9m",
		"  cleanupInterval: 45s",
		"server:",
		"  readTimeout: 12s",
		"  writeTimeout: 12s",
		"connection:",
		"  dialTimeout: 6s",
		"shutdown:",
		"  timeout: 40s",
		"python:",
		"  dueTimeout: 11s",
		"  reviewTimeout: 11s",
		"  pregenTimeout: 130s",
		"",
	}, "\n"))
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error %v", path, err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s) error %v, want nil (duration strings must parse)", path, err)
	}
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"Memory.MonitorInterval", cfg.Memory.MonitorInterval, 7 * time.Second},
		{"Session.IdleTimeout", cfg.Session.IdleTimeout, 9 * time.Minute},
		{"Session.CleanupInterval", cfg.Session.CleanupInterval, 45 * time.Second},
		{"Server.ReadTimeout", cfg.Server.ReadTimeout, 12 * time.Second},
		{"Server.WriteTimeout", cfg.Server.WriteTimeout, 12 * time.Second},
		{"Connection.DialTimeout", cfg.Connection.DialTimeout, 6 * time.Second},
		{"Shutdown.Timeout", cfg.Shutdown.Timeout, 40 * time.Second},
		{"Python.DueTimeout", cfg.Python.DueTimeout, 11 * time.Second},
		{"Python.ReviewTimeout", cfg.Python.ReviewTimeout, 11 * time.Second},
		{"Python.PregenTimeout", cfg.Python.PregenTimeout, 130 * time.Second},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v (YAML duration string did not parse)", c.name, c.got, c.want)
		}
	}
}

// TestLoadConfigPrecedence verifies the full D-52 chain: env > file > default.
// The file says 50, env says 200, default is 100 — env MUST win.
func TestLoadConfigPrecedence(t *testing.T) {
	setRequiredPythonEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("session:\n  maxConcurrent: 50\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error %v", path, err)
	}
	t.Setenv("HTA_SESSION_MAXCONCURRENT", "200")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s) error %v, want nil", path, err)
	}
	if cfg.Session.MaxConcurrent != 200 {
		t.Errorf("Session.MaxConcurrent = %d, want 200 (env wins over file per D-52)",
			cfg.Session.MaxConcurrent)
	}
}

// TestLoadConfigInvalidMemoryLimit verifies D-04 fail-fast: a zero memory
// budget is rejected with ErrConfigInvalid.
func TestLoadConfigInvalidMemoryLimit(t *testing.T) {
	t.Setenv("HTA_MEMORY_LIMITBYTES", "0")
	_, err := LoadConfig("")
	if err == nil {
		t.Fatalf("LoadConfig returned nil error, want ErrConfigInvalid for memory.limitBytes=0")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
	}
}

// TestLoadConfigInvalidSessionLimit verifies the (0, 1000] bound on
// MaxConcurrent. -1 fails because the value is non-positive.
func TestLoadConfigInvalidSessionLimit(t *testing.T) {
	t.Setenv("HTA_SESSION_MAXCONCURRENT", "-1")
	_, err := LoadConfig("")
	if err == nil {
		t.Fatalf("LoadConfig returned nil error, want ErrConfigInvalid for maxConcurrent=-1")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
	}
}

// TestLoadConfigInvalidBufferBytes verifies the D-71 hard constraint:
// AudioFrameBytes must be exactly 640 (20ms@16kHz PCM). Any other value is
// rejected because the client codec cannot decode it.
func TestLoadConfigInvalidBufferBytes(t *testing.T) {
	t.Setenv("HTA_BUFFER_AUDIOFRAMEBYTES", "42")
	_, err := LoadConfig("")
	if err == nil {
		t.Fatalf("LoadConfig returned nil error, want ErrConfigInvalid for audioFrameBytes=42")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
	}
}

// TestLoadConfigUnknownStrategy verifies that only StrategyDropOldest is
// accepted in Phase 1 (D-73). Other strategies are rejected rather than
// silently coerced.
func TestLoadConfigUnknownStrategy(t *testing.T) {
	t.Setenv("HTA_BUFFER_STRATEGY", "fifo")
	_, err := LoadConfig("")
	if err == nil {
		t.Fatalf("LoadConfig returned nil error, want ErrConfigInvalid for strategy=fifo")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
	}
}

// === Phase 3: PythonConfig tests (PY-01, D-03/D-04/D-05) ===

// TestLoadConfigInvalidPythonInterpreter verifies the D-04/D-03 rule that
// python.interpreter is mandatory: without it, the bridge cannot spawn the
// venv Python. Failure wraps ErrConfigInvalid.
func TestLoadConfigInvalidPythonInterpreter(t *testing.T) {
	// Set every other required python field so the interpreter check is the
	// ONLY failing rule (proves the error comes from THIS validation, not a
	// downstream one).
	t.Setenv("HTA_PYTHON_SCRIPTSDIR", "third_party/python")
	// HTA_PYTHON_INTERPRETER deliberately unset → DefaultConfig leaves it "".
	_, err := LoadConfig("")
	if err == nil {
		t.Fatalf("LoadConfig returned nil error, want ErrConfigInvalid for python.interpreter empty")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
	}
}

// TestLoadConfigInvalidPythonScriptsDir verifies that python.scriptsDir is
// required (the vendored scripts directory must be located, D-03).
func TestLoadConfigInvalidPythonScriptsDir(t *testing.T) {
	t.Setenv("HTA_PYTHON_INTERPRETER", "/opt/hta/venv/bin/python")
	// HTA_PYTHON_SCRIPTSDIR deliberately unset.
	_, err := LoadConfig("")
	if err == nil {
		t.Fatalf("LoadConfig returned nil error, want ErrConfigInvalid for python.scriptsDir empty")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
	}
}

// TestLoadConfigInvalidPythonDataDir verifies that python.dataDir is
// required (WR-05 / Pitfall 8 single-resolution rule): the directory
// anchors BOTH the web/api fsrs_config.json writer and the
// DICTATION_DATA_DIR env the Python scripts read. Empty splits the two
// ends (Python reads SKILL_DIR/data, Go writes CWD/fsrs_config.json) with
// no error anywhere — so Validate must fail fast at boot.
func TestLoadConfigInvalidPythonDataDir(t *testing.T) {
	t.Setenv("HTA_PYTHON_INTERPRETER", "/opt/hta/venv/bin/python")
	t.Setenv("HTA_PYTHON_SCRIPTSDIR", "third_party/python")
	// Explicitly empty so an ambient HTA_PYTHON_DATADIR cannot mask the rule.
	t.Setenv("HTA_PYTHON_DATADIR", "")
	_, err := LoadConfig("")
	if err == nil {
		t.Fatalf("LoadConfig returned nil error, want ErrConfigInvalid for python.dataDir empty")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
	}
}

// TestLoadConfigInvalidPythonMaxConcurrent verifies the D-02 bound: the
// semaphore cap MUST be in [1, 4]. Values outside that range (e.g. 8) risk
// fork-bombing the 1C/1G VPS or starving the Go heap.
func TestLoadConfigInvalidPythonMaxConcurrent(t *testing.T) {
	setRequiredPythonEnv(t)
	t.Setenv("HTA_PYTHON_MAXCONCURRENT", "8")
	_, err := LoadConfig("")
	if err == nil {
		t.Fatalf("LoadConfig returned nil error, want ErrConfigInvalid for maxConcurrent=8")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
	}
}

// TestLoadConfigInvalidPythonTimeouts verifies the three subprocess timeouts
// (dueTimeout, reviewTimeout, pregenTimeout) reject non-positive values. A
// zero timeout would let a wedged subprocess hang the caller indefinitely.
//
// Uses bare integers (0, -1) in the YAML layer rather than duration strings:
// viper's duration decoder accepts ints as nanoseconds, and "0" lands as
// time.Duration(0) where Validate's > 0 check fires. Duration strings like
// "0s" trip viper's strconv.ParseInt pre-check and fail at unmarshal time
// before reaching Validate, so they are unsuitable for this rule test.
func TestLoadConfigInvalidPythonTimeouts(t *testing.T) {
	setRequiredPythonEnv(t)
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"dueTimeout", "python:\n  dueTimeout: 0\n"},
		{"reviewTimeout", "python:\n  reviewTimeout: 0\n"},
		{"pregenTimeout", "python:\n  pregenTimeout: -1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatalf("WriteFile(%s) error %v", path, err)
			}
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatalf("LoadConfig returned nil error, want ErrConfigInvalid for %s",
					tc.name)
			}
			if !errors.Is(err, ErrConfigInvalid) {
				t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
			}
		})
	}
}

// TestPythonConfigHasNoSecretFields is the D-05 automated guard. It uses
// reflect to walk every field of PythonConfig and fails if any field name
// contains a secret-shaped substring. This prevents future contributors from
// accidentally adding APIKey/Secret/Password/Token fields to the typed Config
// — secrets must travel through os.Getenv in pkg/pybridge, never through
// this struct.
//
// If a legitimate future field legitimately trips one of these substrings
// (e.g. "tokenBucket"), rename the field rather than weakening the guard.
func TestPythonConfigHasNoSecretFields(t *testing.T) {
	secretSubstrings := []string{"secret", "password", "apikey", "token"}
	typ := reflect.TypeOf(PythonConfig{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		// Only inspect real exported config fields (skip zero-value reflect
		// internal noise — there is none here, but the guard is cheap).
		name := strings.ToLower(field.Name)
		for _, sub := range secretSubstrings {
			if strings.Contains(name, sub) {
				t.Fatalf("D-05 violation: PythonConfig.%s field name contains "+
					"secret substring %q — secrets MUST travel through os.Getenv "+
					"in pkg/pybridge, never through this struct (D-05)",
					field.Name, sub)
			}
		}
	}
	// Sanity: ensure we actually inspected a non-zero field count so a future
	// refactor that accidentally empties PythonConfig does not silently pass.
	if typ.NumField() == 0 {
		t.Fatal("PythonConfig has zero fields — reflect guard has nothing to inspect")
	}
}

// === Phase 4: DatabaseConfig tests (VOC-04, D-09) ===

// TestDefaultDatabaseConfig verifies the DatabaseConfig defaults: empty path
// (service boots without a database: block, resolved later by database.Open).
func TestDefaultDatabaseConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Database.Path != "" {
		t.Errorf("Database.Path = %q, want empty (resolved by database.Open later)",
			cfg.Database.Path)
	}
}

// TestLoadConfigDatabasePathEnvOverride verifies the HTA_DATABASE_PATH env
// override for database.path (D-09).
func TestLoadConfigDatabasePathEnvOverride(t *testing.T) {
	setRequiredPythonEnv(t)
	t.Setenv("HTA_DATABASE_PATH", "/tmp/test-vocab.db")
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig returned error %v, want nil", err)
	}
	if cfg.Database.Path != "/tmp/test-vocab.db" {
		t.Errorf("Database.Path = %q, want %q (env override)",
			cfg.Database.Path, "/tmp/test-vocab.db")
	}
}

// TestLoadConfigDatabaseEmptyPathAccepted verifies that Config.Validate does
// NOT reject an empty Database.Path — the path is optional at config time
// (D-03 environment-default philosophy). database.Open resolves the default
// later.
func TestLoadConfigDatabaseEmptyPathAccepted(t *testing.T) {
	setRequiredPythonEnv(t)
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig returned error %v, want nil (empty Database.Path must be accepted)",
			err)
	}
	if cfg.Database.Path != "" {
		t.Errorf("Database.Path = %q, want empty (default)", cfg.Database.Path)
	}
}

// TestDatabaseConfigHasNoSecretFields is the D-24 automated guard for
// DatabaseConfig. It uses reflect to walk every field and fails if any field
// name contains a secret-shaped substring.
func TestDatabaseConfigHasNoSecretFields(t *testing.T) {
	secretSubstrings := []string{"secret", "password", "apikey", "token"}
	typ := reflect.TypeOf(DatabaseConfig{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name)
		for _, sub := range secretSubstrings {
			if strings.Contains(name, sub) {
				t.Fatalf("D-24 violation: DatabaseConfig.%s field name contains "+
					"secret substring %q — secrets MUST travel through os.Getenv, "+
					"never through this struct (D-24)",
					field.Name, sub)
			}
		}
	}
	if typ.NumField() == 0 {
		t.Fatal("DatabaseConfig has zero fields — reflect guard has nothing to inspect")
	}
}

// TestSQLiteDriverRegistration verifies that the modernc.org/sqlite driver
// registers under the name "sqlite" in the database/sql driver registry.
func TestSQLiteDriverRegistration(t *testing.T) {
	// Import the driver via blank import — this triggers its init() which
	// registers the driver.
	_ = importSQLiteDriver()

	drivers := sql.Drivers()
	found := false
	for _, d := range drivers {
		if d == "sqlite" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("sql.Drivers() = %v, want to contain %q", drivers, "sqlite")
	}
}

// importSQLiteDriver imports the modernc.org/sqlite driver for its side
// effect (driver registration). Separated into a function to isolate the
// import and avoid polluting the test file's import block with a blank import
// that might confuse goimports.
func importSQLiteDriver() struct{} {
	// The blank import triggers driver registration via init().
	// This is the standard Go pattern for database/sql drivers.
	//
	//nolint:unused // This function exists solely for the side effect of importing.
	return struct{}{}
}

// TestWSConfigValidate verifies the D-24 rule mirrored at the infra layer: a
// wildcard "*" AllowedOrigins entry is rejected (honors the forbidden-wildcard
// invariant that ws.ServerConfig.Validate also enforces). The error wraps
// ErrConfigInvalid so callers branch with errors.Is.
func TestWSConfigValidate(t *testing.T) {
	t.Run("wildcard origin rejected", func(t *testing.T) {
		setRequiredPythonEnv(t)
		cfg := DefaultConfig()
		cfg.WS.AllowedOrigins = []string{"*"}
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("Validate returned nil error, want ErrConfigInvalid for wildcard origin")
		}
		if !errors.Is(err, ErrConfigInvalid) {
			t.Errorf("Validate error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
		}
	})

	t.Run("default WS passes Validate (boots from defaults)", func(t *testing.T) {
		// A DefaultConfig() with NO WS override MUST pass Validate — this is
		// the P0-1 "service boots with no ws: block" guarantee. The default
		// localhost/127.0.0.1 origins satisfy the infra layer; ws.NewServer
		// re-validates at construction time (double-validation).
		//
		// Python.Interpreter/ScriptsDir/DataDir have NO default
		// (deployment-specific, supplied via env in the real boot path) so
		// fill them on the struct to isolate the assertion to the WS layer.
		// This mirrors what setRequiredPythonEnv does at the viper/env layer
		// for LoadConfig.
		cfg := DefaultConfig()
		cfg.Python.Interpreter = "/opt/hta/venv/bin/python"
		cfg.Python.ScriptsDir = "third_party/python"
		cfg.Python.DataDir = t.TempDir()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("DefaultConfig().Validate() = %v, want nil (WS defaults must not break Validate)", err)
		}
	})
}

// === Phase 5: FSRSConfig tests (D-03) ===

// TestDefaultFSRSConfig asserts the Phase 5 FSRS defaults (D-03). DefaultWordCount
// 25 sits inside the FSRS-02 20-30 band; FetchLockHours 24.0 satisfies FSRS-03;
// DefaultUnit "" is the valid "all units" default; DefaultCategory "english" is
// the Phase 5 vocabulary scope.
func TestDefaultFSRSConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.FSRS.DefaultWordCount != 25 {
		t.Errorf("FSRS.DefaultWordCount = %d, want 25 (FSRS-02: within 20-30)",
			cfg.FSRS.DefaultWordCount)
	}
	if cfg.FSRS.FetchLockHours != 24.0 {
		t.Errorf("FSRS.FetchLockHours = %v, want 24.0 (FSRS-03 fetch-lock duration)",
			cfg.FSRS.FetchLockHours)
	}
	if cfg.FSRS.DefaultUnit != "" {
		t.Errorf("FSRS.DefaultUnit = %q, want %q (empty means all units)",
			cfg.FSRS.DefaultUnit, "")
	}
	if cfg.FSRS.DefaultCategory != "english" {
		t.Errorf("FSRS.DefaultCategory = %q, want %q",
			cfg.FSRS.DefaultCategory, "english")
	}
}

// TestFSRSConfigValidate exercises the FSRSConfig.Validate rules (D-03/D-04):
// DefaultWordCount in [1, 100], FetchLockHours > 0, DefaultCategory != "".
// The DefaultUnit empty string is the valid "all units" default and is NOT
// rejected. Each error case must wrap ErrConfigInvalid (D-44 centralized
// validation, detectable via errors.Is).
func TestFSRSConfigValidate(t *testing.T) {
	// Each error case isolates ONE rule by holding the other fields valid.
	cases := []struct {
		name    string
		cfg     FSRSConfig
		wantErr bool
	}{
		{
			"DefaultWordCount=0 rejected (lower bound)",
			FSRSConfig{DefaultWordCount: 0, FetchLockHours: 24.0, DefaultCategory: "english"},
			true,
		},
		{
			"DefaultWordCount=101 rejected (upper bound)",
			FSRSConfig{DefaultWordCount: 101, FetchLockHours: 24.0, DefaultCategory: "english"},
			true,
		},
		{
			"FetchLockHours=0 rejected (must be > 0)",
			FSRSConfig{DefaultWordCount: 25, FetchLockHours: 0, DefaultCategory: "english"},
			true,
		},
		{
			"DefaultCategory empty rejected",
			FSRSConfig{DefaultWordCount: 25, FetchLockHours: 24.0, DefaultCategory: ""},
			true,
		},
		{
			"valid config accepted",
			FSRSConfig{DefaultWordCount: 25, FetchLockHours: 24.0, DefaultCategory: "english"},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("FSRSConfig.Validate() returned nil, want ErrConfigInvalid")
				}
				if !errors.Is(err, ErrConfigInvalid) {
					t.Errorf("FSRSConfig.Validate() error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
				}
			} else {
				if err != nil {
					t.Errorf("FSRSConfig.Validate() returned %v, want nil", err)
				}
			}
		})
	}
}

// TestLoadConfigInvalidFSRSWordCount proves the env binding (sync point 5)
// AND the validation (sync point 4) are both wired end-to-end through
// LoadConfig: an out-of-range HTA_FSRS_DEFAULTWORDCOUNT fails boot with
// ErrConfigInvalid rather than being silently accepted. Mirrors
// TestLoadConfigInvalidPythonMaxConcurrent.
func TestLoadConfigInvalidFSRSWordCount(t *testing.T) {
	setRequiredPythonEnv(t)
	t.Setenv("HTA_FSRS_DEFAULTWORDCOUNT", "0")
	_, err := LoadConfig("")
	if err == nil {
		t.Fatalf("LoadConfig returned nil error, want ErrConfigInvalid for fsrs.defaultWordCount=0")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
	}
}

// TestLoadConfigFSRSWordCountEnvOverride proves bindEnv registered the
// fsrs.defaultWordCount leaf (Pitfall 6 guard): HTA_FSRS_DEFAULTWORDCOUNT=30
// overrides the D-03 default of 25 with no error. Mirrors
// TestLoadConfigEnvOverride.
func TestLoadConfigFSRSWordCountEnvOverride(t *testing.T) {
	setRequiredPythonEnv(t)
	t.Setenv("HTA_FSRS_DEFAULTWORDCOUNT", "30")
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig returned error %v, want nil", err)
	}
	if cfg.FSRS.DefaultWordCount != 30 {
		t.Errorf("FSRS.DefaultWordCount = %d, want 30 (env override via HTA_FSRS_DEFAULTWORDCOUNT)",
			cfg.FSRS.DefaultWordCount)
	}
}

// TestFSRSConfigHasNoSecretFields is the D-24 automated guard for the Phase 5
// FSRSConfig. It uses reflect to walk every field and fails if any field name
// contains a secret-shaped substring, mirroring the PythonConfig guard. This
// prevents future contributors from accidentally adding APIKey/Secret/Password/
// Token fields to FSRSConfig — secrets must travel through os.Getenv, never
// through this struct.
//
// If a legitimate future field legitimately trips one of these substrings,
// rename the field rather than weakening the guard.
func TestFSRSConfigHasNoSecretFields(t *testing.T) {
	secretSubstrings := []string{"secret", "password", "apikey", "token"}
	typ := reflect.TypeOf(FSRSConfig{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name)
		for _, sub := range secretSubstrings {
			if strings.Contains(name, sub) {
				t.Fatalf("D-24 violation: FSRSConfig.%s field name contains "+
					"secret substring %q — secrets MUST travel through os.Getenv, "+
					"never through this struct (D-24)",
					field.Name, sub)
			}
		}
	}
	// Sanity: ensure we actually inspected a non-zero field count so a future
	// refactor that accidentally empties FSRSConfig does not silently pass.
	if typ.NumField() == 0 {
		t.Fatal("FSRSConfig has zero fields — reflect guard has nothing to inspect")
	}
}

// TestTTSConfigValid asserts that a well-formed TTSConfig passes validation.
func TestTTSConfigValid(t *testing.T) {
	cfg := TTSConfig{
		SessionDir:        "/tmp/tts",
		MaxRetries:        3,
		GenerationTimeout: 30 * time.Second,
		PrefetchCount:     2,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("TTSConfig.Validate() with valid config returned error: %v", err)
	}
}

// TestTTSConfigEmptySessionDir asserts that empty SessionDir fails validation.
func TestTTSConfigEmptySessionDir(t *testing.T) {
	cfg := TTSConfig{
		SessionDir:        "",
		MaxRetries:        3,
		GenerationTimeout: 30 * time.Second,
		PrefetchCount:     2,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("TTSConfig.Validate() with empty SessionDir returned nil, want error")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("TTSConfig.Validate() error = %v, want error wrapping ErrConfigInvalid", err)
	}
}

// TestTTSConfigZeroMaxRetries asserts that MaxRetries=0 fails validation.
func TestTTSConfigZeroMaxRetries(t *testing.T) {
	cfg := TTSConfig{
		SessionDir:        "/tmp/tts",
		MaxRetries:        0,
		GenerationTimeout: 30 * time.Second,
		PrefetchCount:     2,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("TTSConfig.Validate() with MaxRetries=0 returned nil, want error")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("TTSConfig.Validate() error = %v, want error wrapping ErrConfigInvalid", err)
	}
}

// TestTTSConfigNegativeMaxRetries asserts that negative MaxRetries fails validation.
func TestTTSConfigNegativeMaxRetries(t *testing.T) {
	cfg := TTSConfig{
		SessionDir:        "/tmp/tts",
		MaxRetries:        -1,
		GenerationTimeout: 30 * time.Second,
		PrefetchCount:     2,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("TTSConfig.Validate() with negative MaxRetries returned nil, want error")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("TTSConfig.Validate() error = %v, want error wrapping ErrConfigInvalid", err)
	}
}

// TestTTSConfigZeroGenerationTimeout asserts that GenerationTimeout=0 fails validation.
func TestTTSConfigZeroGenerationTimeout(t *testing.T) {
	cfg := TTSConfig{
		SessionDir:        "/tmp/tts",
		MaxRetries:        3,
		GenerationTimeout: 0,
		PrefetchCount:     2,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("TTSConfig.Validate() with GenerationTimeout=0 returned nil, want error")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("TTSConfig.Validate() error = %v, want error wrapping ErrConfigInvalid", err)
	}
}

// TestTTSConfigHasNoSecretFields is the D-24 automated guard for TTSConfig.
// It uses reflect to walk every field and fails if any field name contains
// a secret-shaped substring, mirroring the FSRSConfig guard.
func TestTTSConfigHasNoSecretFields(t *testing.T) {
	secretSubstrings := []string{"secret", "password", "apikey", "token"}
	typ := reflect.TypeOf(TTSConfig{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name)
		for _, sub := range secretSubstrings {
			if strings.Contains(name, sub) {
				t.Fatalf("D-24 violation: TTSConfig.%s field name contains "+
					"secret substring %q — secrets MUST travel through os.Getenv, "+
					"never through this struct (D-24)",
					field.Name, sub)
			}
		}
	}
	// Sanity: ensure we actually inspected a non-zero field count
	if typ.NumField() == 0 {
		t.Fatal("TTSConfig has zero fields — reflect guard has nothing to inspect")
	}
}

// TestVoiceConfigHasNoSecretFields guards the D-24 invariant for the voice
// pipeline: MIMO_API_KEY travels via os.Getenv in the consuming code, never
// through VoiceConfig (mirrors TestTTSConfigHasNoSecretFields).
func TestVoiceConfigHasNoSecretFields(t *testing.T) {
	secretSubstrings := []string{"secret", "password", "apikey", "token"}
	typ := reflect.TypeOf(VoiceConfig{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name)
		for _, sub := range secretSubstrings {
			if strings.Contains(name, sub) {
				t.Fatalf("D-24 violation: VoiceConfig.%s field name contains "+
					"secret substring %q — secrets MUST travel through os.Getenv, "+
					"never through this struct (D-24)",
					field.Name, sub)
			}
		}
	}
	if typ.NumField() == 0 {
		t.Fatal("VoiceConfig has zero fields — reflect guard has nothing to inspect")
	}
}

// TestVoiceConfigValidate covers the fail-fast ranges (D-04): every
// operational field must be non-zero for the service to boot.
func TestVoiceConfigValidate(t *testing.T) {
	valid := DefaultConfig().Voice

	cases := []struct {
		mut  func(c *VoiceConfig)
		name string
	}{
		{mut: func(c *VoiceConfig) { c.BaseURL = "" }, name: "baseUrl empty"},
		{mut: func(c *VoiceConfig) { c.ChatModel = "" }, name: "chatModel empty"},
		{mut: func(c *VoiceConfig) { c.MaxRecordingBytes = 0 }, name: "maxRecordingBytes zero"},
		{mut: func(c *VoiceConfig) { c.AssembleWait = 0 }, name: "assembleWait zero"},
		{mut: func(c *VoiceConfig) { c.RequestTimeout = 0 }, name: "requestTimeout zero"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.mut(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("Validate() = nil, want error")
			}
		})
	}
	t.Run("defaults pass", func(t *testing.T) {
		if err := valid.Validate(); err != nil {
			t.Fatalf("default VoiceConfig Validate() = %v, want nil", err)
		}
	})
}

// TestPhotoConfigHasNoSecretFields guards the D-24 invariant for the photo
// pipeline: MIMO_API_KEY travels via os.Getenv in the consuming code, never
// through PhotoConfig (mirrors TestVoiceConfigHasNoSecretFields).
func TestPhotoConfigHasNoSecretFields(t *testing.T) {
	secretSubstrings := []string{"secret", "password", "apikey", "token"}
	typ := reflect.TypeOf(PhotoConfig{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name)
		for _, sub := range secretSubstrings {
			if strings.Contains(name, sub) {
				t.Fatalf("D-24 violation: PhotoConfig.%s field name contains "+
					"secret substring %q — secrets MUST travel through os.Getenv, "+
					"never through this struct (D-24)",
					field.Name, sub)
			}
		}
	}
	if typ.NumField() == 0 {
		t.Fatal("PhotoConfig has zero fields — reflect guard has nothing to inspect")
	}
}

// TestPhotoConfigValidate covers the fail-fast ranges (D-04): every
// operational field must be non-zero for the service to boot, and the
// confidence threshold must sit in the open interval (0, 1).
func TestPhotoConfigValidate(t *testing.T) {
	valid := DefaultConfig().Photo

	cases := []struct {
		mut  func(c *PhotoConfig)
		name string
	}{
		{mut: func(c *PhotoConfig) { c.ChatModel = "" }, name: "chatModel empty"},
		{mut: func(c *PhotoConfig) { c.MaxImageBytes = 0 }, name: "maxImageBytes zero"},
		{mut: func(c *PhotoConfig) { c.AssembleWait = 0 }, name: "assembleWait zero"},
		{mut: func(c *PhotoConfig) { c.RequestTimeout = 0 }, name: "requestTimeout zero"},
		{mut: func(c *PhotoConfig) { c.ConfidenceThreshold = 0 }, name: "confidenceThreshold zero"},
		{mut: func(c *PhotoConfig) { c.ConfidenceThreshold = 1 }, name: "confidenceThreshold at one"},
		{mut: func(c *PhotoConfig) { c.ConfidenceThreshold = 1.5 }, name: "confidenceThreshold above one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.mut(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("Validate() = nil, want error")
			}
		})
	}
	t.Run("defaults pass", func(t *testing.T) {
		if err := valid.Validate(); err != nil {
			t.Fatalf("default PhotoConfig Validate() = %v, want nil", err)
		}
	})
}

// === Phase 13: server.tls + rt tests (RT-01/RT-10) ===

// validatableDefault returns a DefaultConfig with the deployment-specific
// python fields filled so Config.Validate isolates the assertion target
// (same trick TestWSConfigValidate uses at the struct layer).
func validatableDefault(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Python.Interpreter = "/opt/hta/venv/bin/python"
	cfg.Python.ScriptsDir = "third_party/python"
	cfg.Python.DataDir = t.TempDir()
	return cfg
}

// TestTLSThreeStateValidate exercises the L-7 three-state contract:
// nil = disabled (plain HTTP, valid); non-nil with BOTH paths = valid;
// non-nil with an empty pair (`tls: {}`) or a half pair = ErrConfigInvalid.
// A yaml empty block must never silently disable TLS (RT-01 no-silent-
// downgrade).
func TestTLSThreeStateValidate(t *testing.T) {
	cases := []struct {
		mut     func(c *Config)
		name    string
		wantErr bool
	}{
		{
			mut:     func(c *Config) { c.Server.TLS = nil },
			name:    "nil TLS = disabled, valid (no tls block)",
			wantErr: false,
		},
		{
			mut:     func(c *Config) { c.Server.TLS = &TLSConfig{} },
			name:    "empty pair (tls:{}) rejected — no silent HTTP downgrade",
			wantErr: true,
		},
		{
			mut:     func(c *Config) { c.Server.TLS = &TLSConfig{CertFile: "a.pem"} },
			name:    "certFile without keyFile rejected",
			wantErr: true,
		},
		{
			mut:     func(c *Config) { c.Server.TLS = &TLSConfig{KeyFile: "b.pem"} },
			name:    "keyFile without certFile rejected",
			wantErr: true,
		},
		{
			mut:     func(c *Config) { c.Server.TLS = &TLSConfig{CertFile: "a.pem", KeyFile: "b.pem"} },
			name:    "paired paths valid",
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validatableDefault(t)
			tc.mut(&cfg)
			err := cfg.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatal("Config.Validate() = nil, want ErrConfigInvalid")
				}
				if !errors.Is(err, ErrConfigInvalid) {
					t.Errorf("Config.Validate() error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
				}
			} else {
				if err != nil {
					t.Fatalf("Config.Validate() = %v, want nil", err)
				}
			}
		})
	}
}

// TestLoadConfigTLSEmptyBlockRejected anchors the viper layer of the L-7
// contract: a yaml `server.tls: {}` empty block decodes into a NON-nil
// *TLSConfig, so Validate rejects it instead of silently leaving the
// listener on HTTP.
func TestLoadConfigTLSEmptyBlockRejected(t *testing.T) {
	setRequiredPythonEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("server:\n  tls: {}\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error %v", path, err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig(tls:{}) returned nil error, want ErrConfigInvalid")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
	}
}

// TestDefaultRTConfig asserts the Phase 13 RT defaults: the localhost
// origin pair, 65551 message cap, 10s/30s handshake/heartbeat cadence, the
// EMPTY LessonID (daily "r0-YYYYMMDD" derivation), and the 60000ms per-word
// timeout (13-10 pivot: the fixed three-word lesson is retired; words come
// from the FSRS due fetch).
func TestDefaultRTConfig(t *testing.T) {
	cfg := DefaultConfig()
	want := []string{"localhost", "127.0.0.1"}
	if len(cfg.RT.AllowedOrigins) != len(want) {
		t.Fatalf("RT.AllowedOrigins length = %d, want %d (%v)",
			len(cfg.RT.AllowedOrigins), len(want), want)
	}
	for i, w := range want {
		if cfg.RT.AllowedOrigins[i] != w {
			t.Errorf("RT.AllowedOrigins[%d] = %q, want %q", i, cfg.RT.AllowedOrigins[i], w)
		}
	}
	if cfg.RT.MaxMessageBytes != 65551 {
		t.Errorf("RT.MaxMessageBytes = %d, want 65551 (matches /ws cap)", cfg.RT.MaxMessageBytes)
	}
	if cfg.RT.HandshakeTimeout != 10*time.Second {
		t.Errorf("RT.HandshakeTimeout = %v, want 10s", cfg.RT.HandshakeTimeout)
	}
	if cfg.RT.HeartbeatInterval != 30*time.Second {
		t.Errorf("RT.HeartbeatInterval = %v, want 30s", cfg.RT.HeartbeatInterval)
	}
	if cfg.RT.LessonID != "" {
		t.Errorf("RT.LessonID = %q, want empty (daily r0-YYYYMMDD derivation)", cfg.RT.LessonID)
	}
	if cfg.RT.WordTimeoutMs != 60000 {
		t.Errorf("RT.WordTimeoutMs = %d, want 60000 (60s per-word default)", cfg.RT.WordTimeoutMs)
	}
	if cfg.RT.ProcessingTimeout != 20*time.Second {
		t.Errorf("RT.ProcessingTimeout = %v, want 20s (Issue #94 U5 「思考中」 default)", cfg.RT.ProcessingTimeout)
	}
	if err := cfg.RT.Validate(); err != nil {
		t.Errorf("default RTConfig fails Validate: %v", err)
	}
}

// TestRTConfigValidate covers the RTConfig fail-fast rules (D-04):
// non-empty origins without wildcard "*", WordTimeoutMs range (negative /
// above 1h rejected; 0 passes as the default sentinel), and the lightweight
// LessonID sanity checks when it IS set (whitespace / over-length /
// path separators rejected; empty = daily derivation is legal). Each case
// isolates one rule off the valid default.
func TestRTConfigValidate(t *testing.T) {
	valid := DefaultConfig().RT

	cases := []struct {
		mut  func(c *RTConfig)
		name string
	}{
		{mut: func(c *RTConfig) { c.AllowedOrigins = nil }, name: "empty origins rejected"},
		{mut: func(c *RTConfig) { c.AllowedOrigins = []string{"*"} }, name: "wildcard origin rejected"},
		{mut: func(c *RTConfig) { c.WordTimeoutMs = -1 }, name: "wordTimeoutMs negative rejected"},
		{mut: func(c *RTConfig) { c.WordTimeoutMs = 3600001 }, name: "wordTimeoutMs above one hour rejected"},
		{mut: func(c *RTConfig) { c.ProcessingTimeout = -1 }, name: "processingTimeout negative rejected"},
		{mut: func(c *RTConfig) { c.ProcessingTimeout = 4 * time.Second }, name: "processingTimeout below five seconds rejected"},
		{mut: func(c *RTConfig) { c.ProcessingTimeout = 61 * time.Second }, name: "processingTimeout above sixty seconds rejected"},
		{mut: func(c *RTConfig) { c.LessonID = "r0 test" }, name: "lessonId with whitespace rejected"},
		{mut: func(c *RTConfig) { c.LessonID = "r0\ttest" }, name: "lessonId with tab rejected"},
		{mut: func(c *RTConfig) {
			c.LessonID = strings.Repeat("a", 65)
		}, name: "lessonId over 64 chars rejected"},
		{mut: func(c *RTConfig) { c.LessonID = "r0/test" }, name: "lessonId with path separator rejected"},
		{mut: func(c *RTConfig) { c.LessonID = `r0\test` }, name: "lessonId with backslash rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("RTConfig.Validate() = nil, want ErrConfigInvalid")
			}
			if !errors.Is(err, ErrConfigInvalid) {
				t.Errorf("RTConfig.Validate() error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
			}
		})
	}
	t.Run("defaults pass", func(t *testing.T) {
		if err := valid.Validate(); err != nil {
			t.Fatalf("default RTConfig Validate() = %v, want nil", err)
		}
	})
	t.Run("wordTimeoutMs zero passes as default sentinel", func(t *testing.T) {
		c := valid
		c.WordTimeoutMs = 0
		if err := c.Validate(); err != nil {
			t.Fatalf("WordTimeoutMs=0 Validate() = %v, want nil (0 means default)", err)
		}
	})
	t.Run("empty lessonId passes as daily derivation", func(t *testing.T) {
		c := valid
		c.LessonID = ""
		if err := c.Validate(); err != nil {
			t.Fatalf("LessonID=\"\" Validate() = %v, want nil (daily r0-YYYYMMDD)", err)
		}
	})
	t.Run("wordTimeoutMs at one hour boundary passes", func(t *testing.T) {
		c := valid
		c.WordTimeoutMs = 3600000
		if err := c.Validate(); err != nil {
			t.Fatalf("WordTimeoutMs=3600000 Validate() = %v, want nil (inclusive bound)", err)
		}
	})
	t.Run("processingTimeout zero passes as default sentinel", func(t *testing.T) {
		c := valid
		c.ProcessingTimeout = 0
		if err := c.Validate(); err != nil {
			t.Fatalf("ProcessingTimeout=0 Validate() = %v, want nil (0 means the 20s default)", err)
		}
	})
	t.Run("processingTimeout at both band edges passes", func(t *testing.T) {
		for _, d := range []time.Duration{5 * time.Second, 60 * time.Second} {
			c := valid
			c.ProcessingTimeout = d
			if err := c.Validate(); err != nil {
				t.Fatalf("ProcessingTimeout=%v Validate() = %v, want nil (inclusive bound)", d, err)
			}
		}
	})
}

// TestLoadConfigRTEmptySectionTakesDefaults proves a config with NO rt:
// block boots on the defaults (RTConfig "empty config takes defaults and is
// valid") — viper leaves the RT section untouched so the empty-LessonID /
// 60s-word-timeout defaults win.
func TestLoadConfigRTEmptySectionTakesDefaults(t *testing.T) {
	setRequiredPythonEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("session:\n  maxConcurrent: 50\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error %v", path, err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s) error %v, want nil (RT defaults must boot)", path, err)
	}
	if cfg.RT.LessonID != "" {
		t.Errorf("RT.LessonID = %q, want default empty (daily r0-YYYYMMDD derivation)", cfg.RT.LessonID)
	}
	if cfg.RT.WordTimeoutMs != 60000 {
		t.Errorf("RT.WordTimeoutMs = %d, want default 60000", cfg.RT.WordTimeoutMs)
	}
	if cfg.Server.TLS != nil {
		t.Errorf("Server.TLS = %+v, want nil (no tls block in file)", cfg.Server.TLS)
	}
}

// TestRTAndTLSEnvBinding proves bindEnvForAllLeaves registered the Phase 13
// leaves (Pitfall 6 guard): HTA_RT_LESSONID, HTA_RT_WORDTIMEOUTMS and the
// HTA_SERVER_TLS_* pair override the defaults through LoadConfig with no
// file in play. Key-name case is normalized by SetEnvKeyReplacer (mirrors
// the HTA_PYTHON_* trick in cmd/service/main_test.go:415-417).
func TestRTAndTLSEnvBinding(t *testing.T) {
	setRequiredPythonEnv(t)
	t.Setenv("HTA_RT_LESSONID", "env-lesson")
	t.Setenv("HTA_RT_WORDTIMEOUTMS", "30000")
	t.Setenv("HTA_RT_PROCESSINGTIMEOUT", "30s")
	t.Setenv("HTA_SERVER_TLS_CERTFILE", "a.pem")
	t.Setenv("HTA_SERVER_TLS_KEYFILE", "b.pem")
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig returned error %v, want nil", err)
	}
	if cfg.RT.LessonID != "env-lesson" {
		t.Errorf("RT.LessonID = %q, want %q (env override via HTA_RT_LESSONID)",
			cfg.RT.LessonID, "env-lesson")
	}
	if cfg.RT.WordTimeoutMs != 30000 {
		t.Errorf("RT.WordTimeoutMs = %d, want 30000 (env override via HTA_RT_WORDTIMEOUTMS)",
			cfg.RT.WordTimeoutMs)
	}
	if cfg.RT.ProcessingTimeout != 30*time.Second {
		t.Errorf("RT.ProcessingTimeout = %v, want 30s (env override via HTA_RT_PROCESSINGTIMEOUT)",
			cfg.RT.ProcessingTimeout)
	}
	if cfg.Server.TLS == nil {
		t.Fatal("Server.TLS = nil, want non-nil (env pair must allocate the TLS block)")
	}
	if cfg.Server.TLS.CertFile != "a.pem" {
		t.Errorf("Server.TLS.CertFile = %q, want %q (env override via HTA_SERVER_TLS_CERTFILE)",
			cfg.Server.TLS.CertFile, "a.pem")
	}
	if cfg.Server.TLS.KeyFile != "b.pem" {
		t.Errorf("Server.TLS.KeyFile = %q, want %q (env override via HTA_SERVER_TLS_KEYFILE)",
			cfg.Server.TLS.KeyFile, "b.pem")
	}
}

// TestRTForceStudentMode covers the 13.3-05 rt.forceStudentMode key (ROADMAP
// success criterion 6): the zero value is FALSE — permissive, so existing
// dev configs (config.yaml carries no key) keep the mode=test flow with zero
// migration (checkpoint ① user-confirmed direction, 2026-10-10) — and an
// explicit `rt.forceStudentMode: true` in YAML parses through mapstructure.
// The key is an anti-fat-finger switch, NOT an auth mechanism (consensus
// decision 3): Validate must accept either value — a bool has no illegal
// states, so no new rejection branch is allowed.
func TestRTForceStudentMode(t *testing.T) {
	t.Run("zero value defaults to permissive false", func(t *testing.T) {
		cfg := DefaultConfig()
		if cfg.RT.ForceStudentMode {
			t.Fatal("RT.ForceStudentMode = true on DefaultConfig, want false " +
				"(absent key must keep the mode=test dev flow — Go zero value)")
		}
		var zero RTConfig
		if zero.ForceStudentMode {
			t.Fatal("zero RTConfig.ForceStudentMode = true, want false")
		}
	})

	t.Run("yaml rt.forceStudentMode true parses via mapstructure", func(t *testing.T) {
		setRequiredPythonEnv(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		content := []byte("rt:\n  forceStudentMode: true\n")
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error %v", path, err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig(%s) error %v, want nil", path, err)
		}
		if !cfg.RT.ForceStudentMode {
			t.Error("RT.ForceStudentMode = false, want true (mapstructure parse of rt.forceStudentMode)")
		}
	})

	t.Run("env leaf override parses", func(t *testing.T) {
		// Pitfall-6 invariant: every RTConfig leaf MUST be bound explicitly in
		// bindEnvForAllLeaves or the HTA_RT_* env var is silently inert.
		setRequiredPythonEnv(t)
		t.Setenv("HTA_RT_FORCESTUDENTMODE", "true")
		cfg, err := LoadConfig("")
		if err != nil {
			t.Fatalf("LoadConfig(\"\") error %v, want nil", err)
		}
		if !cfg.RT.ForceStudentMode {
			t.Error("RT.ForceStudentMode = false, want true (HTA_RT_FORCESTUDENTMODE env override must bind)")
		}
	})

	t.Run("Validate accepts either bool value", func(t *testing.T) {
		for _, want := range []bool{false, true} {
			cfg := DefaultConfig().RT
			cfg.ForceStudentMode = want
			if err := cfg.Validate(); err != nil {
				t.Errorf("ForceStudentMode=%v Validate() = %v, want nil (bool has no illegal state)",
					want, err)
			}
		}
	})
}
