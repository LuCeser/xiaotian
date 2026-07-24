// Package infra provides the foundational infrastructure for the Home Tutor
// Agent Go service: typed configuration, memory budgeting, connection
// limiting, structured logging, and resource lifecycle.
//
// Config is loaded once at startup (D-19). Runtime reload is intentionally
// unsupported in Phase 1 — operators restart the process to pick up new
// values.
package infra

import (
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/viper"
)

// Package-scoped constants (D-38). These are unexported because they encode
// in-package conventions; operators interact with the service through env
// vars and config-file keys, not through these identifiers.
const (
	// componentName is the value used for the structured log field "component"
	// (D-25). Every log line emitted by this package is tagged with this
	// value so operators can filter infra logs from websocket / TTS logs.
	componentName = "infra"

	// envPrefix is the Viper env-var prefix. Combined with the dotted field
	// path (e.g. "memory.limitBytes") it produces env vars like
	// HTA_MEMORY_LIMITBYTES. Chosen to be short and unambiguous (Home Tutor
	// Agent) so it does not collide with other services on the same VPS.
	envPrefix = "HTA"

	// defaultConfigFile is the file name LoadConfig probes when the caller
	// passes an empty path. Resolved relative to the process CWD.
	defaultConfigFile = "config.yaml"

	// StrategyDropOldest is the only buffer backpressure strategy supported
	// in Phase 1 (D-73). When the audio buffer pool is exhausted the oldest
	// queued frame is dropped to make room for the newest one, prioritising
	// liveness over completeness.
	StrategyDropOldest = "drop-oldest"
)

// ErrConfigInvalid is the sentinel error returned (wrapped with context) by
// LoadConfig and Config.Validate when any field fails validation (D-04, D-44).
// Callers MUST use errors.Is(err, infra.ErrConfigInvalid) to detect validation
// failures rather than string matching.
var ErrConfigInvalid = errors.New("configuration invalid")

// MemoryBytes is a domain newtype for byte counts (D-40). Carries units in
// its type so callers cannot accidentally assign a session count to a memory
// limit. The underlying width is uint64 to express multi-GB budgets without
// overflow even though Phase 1's VPS caps Memory.LimitBytes at 150MB.
type MemoryBytes uint64

// Percent is a domain newtype for percentage values in the closed range
// [0, 100] (D-40). Used for memory thresholds and load-shed triggers so a
// raw int "95" is not confused with a session count or a byte count.
type Percent int

// Config is the typed configuration for the Go service (D-53). Loaded once
// at process startup by LoadConfig; runtime mutation is unsupported (D-19).
//
// Secrets (API tokens, database credentials, TLS keys) are read by the
// consuming package directly from environment variables via os.Getenv,
// never through this struct. This struct holds operational configuration
// only (D-24) — there is intentionally no field named Token, Secret,
// Password, or APIKey.
type Config struct {
	// Memory budgets the Go process heap (D-13, D-18, RES-01).
	Memory MemoryConfig `json:"memory" mapstructure:"memory"`

	// Session bounds the active session count (D-56, RES-03).
	Session SessionConfig `json:"session" mapstructure:"session"`

	// Buffer tunes the audio buffer pool (D-69, D-71, D-73).
	Buffer BufferConfig `json:"buffer" mapstructure:"buffer"`

	// Server tunes the HTTP/WS listener (D-22).
	Server ServerConfig `json:"server" mapstructure:"server"`

	// Log tunes structured logging output (D-25, D-34).
	Log LogConfig `json:"log" mapstructure:"log"`

	// Worker tunes the worker pool (D-10).
	Worker WorkerConfig `json:"worker" mapstructure:"worker"`

	// Connection tunes outbound connection throttling (D-22, D-66).
	Connection ConnectionConfig `json:"connection" mapstructure:"connection"`

	// Shutdown tunes graceful drain on SIGTERM/SIGINT (D-21).
	Shutdown ShutdownConfig `json:"shutdown" mapstructure:"shutdown"`

	// Python tunes the pkg/pybridge subprocess layer (Phase 3, D-03/D-04/D-05).
	// Holds only operational configuration: interpreter path, vendored script
	// paths, runtime data/cache directories, subprocess timeouts, and the
	// global concurrency cap. Secrets (VOLCENGINE_TTS_*) are deliberately NOT
	// modelled here — the bridge reads them via os.Getenv per D-05 and the
	// package's own "no secret fields" rule (see Config doc comment above).
	Python PythonConfig `json:"python" mapstructure:"python"`
}

// PythonConfig tunes the Go ↔ Python subprocess bridge (Phase 3, D-03/D-04).
// It carries only operational fields; TTS credentials are intentionally absent
// — see the Config struct doc comment and D-05. The TestPythonConfigHasNoSecretFields
// reflect guard enforces this invariant automatically.
type PythonConfig struct {
	// Interpreter is the absolute path to the venv's python executable used
	// to spawn vocab_manager.py / pregenerate_dictation.py (D-04). Example:
	// "/opt/hta/venv/bin/python". Empty is rejected by Validate.
	Interpreter string `json:"interpreter" mapstructure:"interpreter"`

	// ScriptsDir is the directory holding the vendored Python scripts
	// (D-03 vendor). Typically "third_party/python" relative to the process
	// CWD. Empty is rejected by Validate.
	ScriptsDir string `json:"scriptsDir" mapstructure:"scriptsDir"`

	// VocabScript is the filename of the vocab_manager script inside
	// ScriptsDir. Default "vocab_manager.py". Empty is rejected by Validate.
	VocabScript string `json:"vocabScript" mapstructure:"vocabScript"`

	// PregenScript is the filename of the pregenerate_dictation script inside
	// ScriptsDir. Default "pregenerate_dictation.py". Empty is rejected by
	// Validate.
	PregenScript string `json:"pregenScript" mapstructure:"pregenScript"`

	// VocabDBPath is the runtime SQLite DB path (env DICTATION_DB_PATH) used
	// by vocab_manager for FSRS state (D-09/D-10). Forwarded to the subprocess
	// via its --db global argument by the bridge.
	VocabDBPath string `json:"vocabDBPath" mapstructure:"vocabDBPath"`

	// DataDir is the runtime data directory (env DICTATION_DATA_DIR) used by
	// the Python scripts for non-cache persistent state.
	DataDir string `json:"dataDir" mapstructure:"dataDir"`

	// CacheDir is the runtime cache directory (env DICTATION_CACHE_DIR) where
	// pregenerate_dictation writes mp3 files (D-06). Forwarded to the
	// subprocess via env.
	CacheDir string `json:"cacheDir" mapstructure:"cacheDir"`

	// ManifestDir is the runtime manifest directory (env DICTATION_MANIFEST_DIR)
	// where pregenerate_dictation emits its generated-audio manifest.
	ManifestDir string `json:"manifestDir" mapstructure:"manifestDir"`

	// EnvFile is the path to the non-secret .env file (env DICTATION_ENV_FILE)
	// the subprocess may load for DICTATION_* path overrides. VOLCENGINE_TTS_*
	// secrets are NOT placed here — they come from the live process environment
	// per D-05.
	EnvFile string `json:"envFile" mapstructure:"envFile"`

	// DueTimeout caps the wall-clock duration of a vocab_manager.py due call.
	// Units: seconds (parsed from strings like "10s"). Default 10s.
	DueTimeout time.Duration `json:"dueTimeout" mapstructure:"dueTimeout"`

	// ReviewTimeout caps the wall-clock duration of a vocab_manager.py review
	// call. Units: seconds. Default 10s.
	ReviewTimeout time.Duration `json:"reviewTimeout" mapstructure:"reviewTimeout"`

	// PregenTimeout caps the wall-clock duration of a pregenerate_dictation.py
	// generate call. Default 120s — TTS + ffmpeg are slower than vocab_manager
	// and the call batches an entire word list (D-01).
	PregenTimeout time.Duration `json:"pregenTimeout" mapstructure:"pregenTimeout"`

	// MaxConcurrent is the global bounded-semaphore cap on simultaneously
	// running Python subprocesses (D-02). Prevents 50-100 concurrent sessions
	// from fork-bombing the 1C/1G VPS. Default 2. MUST be in [1, 4].
	MaxConcurrent int `json:"maxConcurrent" mapstructure:"maxConcurrent"`
}

// MemoryConfig bounds the process heap. LoadConfig validates that LimitBytes
// fits the 1C/1G VPS envelope and that Thresholds are ascending and within
// LoadShedAtPercent.
type MemoryConfig struct {
	// LimitBytes is the maximum bytes the Go service may allocate before
	// load shedding triggers. Default 150MB on a 1C/1G VPS (RES-01),
	// leaving headroom for the OS and the Python sidecar. Units: bytes.
	LimitBytes MemoryBytes `json:"limitBytes" mapstructure:"limitBytes"`

	// MonitorInterval is how often the runtime is sampled against
	// LimitBytes. Units: seconds (parsed from strings like "5s").
	// Default 5s.
	MonitorInterval time.Duration `json:"monitorInterval" mapstructure:"monitorInterval"`

	// Thresholds are the percentage-of-budget alert thresholds (D-70).
	// Default [80, 90, 95] (warn / escalate / critical). MUST be ascending
	// and every element MUST be in (0, 100]. Units: percent.
	Thresholds []Percent `json:"thresholds" mapstructure:"thresholds"`

	// LoadShedAtPercent is the percentage at which new sessions are
	// rejected (D-18). Default 95. MUST be >= max(Thresholds). Units:
	// percent.
	LoadShedAtPercent Percent `json:"loadShedAtPercent" mapstructure:"loadShedAtPercent"`
}

// SessionConfig bounds the active session count and cleanup cadence.
type SessionConfig struct {
	// MaxConcurrent is the hard cap on active sessions. Default 100
	// (RES-03, D-56). When the cap is reached, new connections receive
	// HTTP 429 Too Many Requests. Sanity ceiling is 1000 — anything higher
	// is rejected as a misconfiguration on a 1C/1G VPS.
	MaxConcurrent int `json:"maxConcurrent" mapstructure:"maxConcurrent"`

	// IdleTimeout is how long a connection may be idle before the cleaner
	// reaps it. Units: seconds. Default 5m.
	IdleTimeout time.Duration `json:"idleTimeout" mapstructure:"idleTimeout"`

	// CleanupInterval is how often the cleaner sweeps stale connections
	// (D-57). Units: seconds. Default 30s.
	CleanupInterval time.Duration `json:"cleanupInterval" mapstructure:"cleanupInterval"`
}

// BufferConfig tunes the audio buffer pool. AudioFrameBytes is non-negotiable
// (640 = 20ms @ 16kHz PCM); the other fields are tunable.
type BufferConfig struct {
	// AudioFrameBytes is the size of one PCM frame. MUST be 640 (20ms of
	// 16kHz mono PCM) per D-71. Non-negotiable for codec compatibility
	// with the client. Units: bytes.
	AudioFrameBytes int `json:"audioFrameBytes" mapstructure:"audioFrameBytes"`

	// PoolSize is the number of frames pre-allocated in the sync.Pool
	// (D-69). Default 1000. Larger pools reduce GC pressure at the cost of
	// resident memory.
	PoolSize int `json:"poolSize" mapstructure:"poolSize"`

	// Strategy selects the backpressure strategy when the pool is
	// exhausted (D-73). Only StrategyDropOldest is supported in Phase 1.
	Strategy string `json:"strategy" mapstructure:"strategy"`
}

// ServerConfig tunes the HTTP/WS listener and the localhost-only health
// endpoint. Timeouts are mandatory (D-22) to prevent slowloris-style
// resource exhaustion on the resource-constrained VPS.
type ServerConfig struct {
	// Addr is the public service listen address (host:port). Default
	// "0.0.0.0:8080".
	Addr string `json:"addr" mapstructure:"addr"`

	// ReadTimeout caps slowloris-style slow reads. Units: seconds.
	// Default 10s (D-22).
	ReadTimeout time.Duration `json:"readTimeout" mapstructure:"readTimeout"`

	// WriteTimeout caps stalled writes. Units: seconds. Default 10s.
	WriteTimeout time.Duration `json:"writeTimeout" mapstructure:"writeTimeout"`

	// HealthAddr is the localhost-only health/metrics listener, kept
	// separate from the public Addr so probing traffic does not compete
	// with session traffic. Default "127.0.0.1:8081".
	HealthAddr string `json:"healthAddr" mapstructure:"healthAddr"`
}

// LogConfig tunes structured logging output (D-25, D-34). The actual logger
// is constructed by Plan 03 — this struct only carries its configuration.
type LogConfig struct {
	// Level is one of {debug, info, warn, error}. Default "info".
	Level string `json:"level" mapstructure:"level"`

	// Format is one of {json, console}. Default "json" (machine-parseable
	// for log aggregators).
	Format string `json:"format" mapstructure:"format"`

	// Output is the log destination: "stderr", "stdout", or a file path.
	// Default "stderr" so systemd / container runtimes capture logs.
	Output string `json:"output" mapstructure:"output"`
}

// WorkerConfig tunes the worker pool (D-10). Worker goroutines process
// inbound session work items off a bounded queue.
type WorkerConfig struct {
	// PoolSize is the number of worker goroutines. Default
	// 2*runtime.NumCPU() — on a 1C VPS that evaluates to 2, providing
	// minimal parallelism without over-subscribing the single core.
	PoolSize int `json:"poolSize" mapstructure:"poolSize"`

	// QueueSize is the bounded queue depth feeding the worker pool.
	// Default 256. When full, new work items are rejected with
	// backpressure signalling (D-72).
	QueueSize int `json:"queueSize" mapstructure:"queueSize"`
}

// ConnectionConfig tunes outbound connection throttling (D-22, D-66).
type ConnectionConfig struct {
	// ThrottleRatePerSec is the token-bucket refill rate (D-66). Default
	// 100 tokens/sec. Units: tokens per second.
	ThrottleRatePerSec int `json:"throttleRatePerSec" mapstructure:"throttleRatePerSec"`

	// ThrottleBurst is the token bucket burst capacity. Default 10.
	// Units: tokens.
	ThrottleBurst int `json:"throttleBurst" mapstructure:"throttleBurst"`

	// DialTimeout caps outbound dial latency. Units: seconds. Default 5s
	// (D-22) so a wedged upstream fails fast rather than queuing.
	DialTimeout time.Duration `json:"dialTimeout" mapstructure:"dialTimeout"`
}

// ShutdownConfig tunes graceful drain on SIGTERM/SIGINT (D-21). When the
// process receives a termination signal it stops accepting new work and
// waits up to Timeout for in-flight requests to complete before exiting.
type ShutdownConfig struct {
	// Timeout is the maximum time allowed for in-flight requests to
	// complete before the process force-exits. Units: seconds. Default
	// 30s — long enough for a dictation frame to finish, short enough to
	// satisfy Kubernetes / systemd termination grace periods.
	Timeout time.Duration `json:"timeout" mapstructure:"timeout"`
}

// DefaultConfig returns the populated zero-value Config with every field set
// to its phase-1 default. It is the source of truth for the "no config file,
// no env vars" baseline behavior, and the starting point for LoadConfig's
// layered override chain (defaults < file < env per D-52).
//
// Mutating the returned struct does not affect subsequent calls — every call
// returns a fresh value with no shared mutable state.
func DefaultConfig() Config {
	return Config{
		Memory: MemoryConfig{
			LimitBytes:        MemoryBytes(150 * 1024 * 1024),
			MonitorInterval:   5 * time.Second,
			Thresholds:        []Percent{80, 90, 95},
			LoadShedAtPercent: 95,
		},
		Session: SessionConfig{
			MaxConcurrent:   100,
			IdleTimeout:     5 * time.Minute,
			CleanupInterval: 30 * time.Second,
		},
		Buffer: BufferConfig{
			AudioFrameBytes: 640,
			PoolSize:        1000,
			Strategy:        StrategyDropOldest,
		},
		Server: ServerConfig{
			Addr:         "0.0.0.0:8080",
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			HealthAddr:   "127.0.0.1:8081",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
			Output: "stderr",
		},
		Worker: WorkerConfig{
			PoolSize:  2 * runtime.NumCPU(),
			QueueSize: 256,
		},
		Connection: ConnectionConfig{
			ThrottleRatePerSec: 100,
			ThrottleBurst:      10,
			DialTimeout:        5 * time.Second,
		},
		Shutdown: ShutdownConfig{
			Timeout: 30 * time.Second,
		},
		Python: PythonConfig{
			VocabScript:   "vocab_manager.py",
			PregenScript:  "pregenerate_dictation.py",
			DueTimeout:    10 * time.Second,
			ReviewTimeout: 10 * time.Second,
			PregenTimeout: 120 * time.Second,
			MaxConcurrent: 2,
		},
	}
}

// LoadConfig resolves the operational configuration using the D-52 layered
// precedence: defaults < config file < environment variables. It then runs
// Config.Validate and returns the final, validated Config.
//
// Startup-only contract (D-19): this function MUST be called exactly once at
// process boot. There is no reload API — operators restart the process to
// pick up new values.
//
// File handling:
//   - If configPath is empty, no file is loaded.
//   - If configPath points to a missing file, the file is silently skipped
//     (the env layer and defaults still apply). This lets the service boot
//     without an operator-supplied config file.
//   - If configPath points to a malformed file (bad YAML, wrong schema
//     shape), the error is wrapped and returned — silent misconfiguration
//     would be worse than a noisy boot.
//
// Env handling:
//   - Prefix is HTA (Home Tutor Agent). Examples: HTA_MEMORY_LIMITBYTES,
//     HTA_SESSION_MAXCONCURRENT, HTA_BUFFER_STRATEGY.
//   - Every leaf field is bound explicitly via BindEnv (T-01-06 mitigation)
//     so we never depend on AutomaticEnv's heuristic.
//
// Validate handling:
//   - On any validation failure, returns Config{} and an error that wraps
//     ErrConfigInvalid (detectable via errors.Is).
func LoadConfig(configPath string) (Config, error) {
	cfg := DefaultConfig()

	v := viper.New()

	// Layer 1 (middle precedence): config file. Skip silently on
	// missing-file so the service can boot without an operator-supplied
	// config (D-03 environment-default).
	if configPath != "" {
		if _, statErr := os.Stat(configPath); statErr == nil {
			v.SetConfigFile(configPath)
			if err := v.ReadInConfig(); err != nil {
				var notFoundErr viper.ConfigFileNotFoundError
				if !errors.As(err, &notFoundErr) {
					return Config{}, errors.Wrap(err, "reading config file")
				}
			}
		}
	}

	// Layer 2 (highest precedence): environment variables. Prefix HTA +
	// dotted key with "." -> "_" yields HTA_MEMORY_LIMITBYTES etc.
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	bindEnvForAllLeaves(v)

	// Unmarshal over the defaults already in cfg. Viper only overrides
	// keys it has a value for; everything else is preserved.
	if err := v.Unmarshal(&cfg, viper.DecodeHook(decodeHook)); err != nil {
		return Config{}, errors.Wrap(err, "unmarshalling config")
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, errors.Wrap(err, "config validation failed")
	}
	return cfg, nil
}

// Validate enforces every D-44 centralized validation rule. On any failure
// it returns an error that wraps ErrConfigInvalid so callers can branch with
// errors.Is(err, infra.ErrConfigInvalid).
//
// Rules (each maps to a row in the threat register):
//   - T-01-04 / T-01-07: numeric ranges (memory, sessions, workers).
//   - D-71: AudioFrameBytes is codec-locked at 640.
//   - D-73: only StrategyDropOldest supported in Phase 1.
//   - D-70: thresholds ascending and bounded.
//   - D-04: fail-fast at startup, never at runtime.
func (c Config) Validate() error {
	if c.Memory.LimitBytes == 0 {
		return errors.Wrap(ErrConfigInvalid, "memory.limitBytes must be > 0")
	}
	if c.Memory.LimitBytes > MemoryBytes(1024*1024*1024) {
		return errors.Wrap(ErrConfigInvalid,
			"memory.limitBytes cannot exceed 1GB on a 1C/1G VPS")
	}
	if c.Memory.MonitorInterval <= 0 {
		return errors.Wrap(ErrConfigInvalid, "memory.monitorInterval must be > 0")
	}
	if err := validateThresholds(c.Memory.Thresholds); err != nil {
		return err
	}
	if c.Memory.LoadShedAtPercent <= 0 || c.Memory.LoadShedAtPercent > 100 {
		return errors.Wrap(ErrConfigInvalid,
			"memory.loadShedAtPercent must be in (0, 100]")
	}
	if len(c.Memory.Thresholds) > 0 {
		maxThreshold := c.Memory.Thresholds[len(c.Memory.Thresholds)-1]
		if c.Memory.LoadShedAtPercent < maxThreshold {
			return errors.Wrap(ErrConfigInvalid,
				"memory.loadShedAtPercent must be >= max(thresholds)")
		}
	}

	if c.Session.MaxConcurrent <= 0 || c.Session.MaxConcurrent > 1000 {
		return errors.Wrap(ErrConfigInvalid,
			"session.maxConcurrent must be in (0, 1000]")
	}
	if c.Session.IdleTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid, "session.idleTimeout must be > 0")
	}
	if c.Session.CleanupInterval <= 0 {
		return errors.Wrap(ErrConfigInvalid, "session.cleanupInterval must be > 0")
	}

	if c.Buffer.AudioFrameBytes != 640 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf(
				"buffer.audioFrameBytes must be 640 (20ms@16kHz PCM), got %d",
				c.Buffer.AudioFrameBytes))
	}
	if c.Buffer.PoolSize <= 0 {
		return errors.Wrap(ErrConfigInvalid, "buffer.poolSize must be > 0")
	}
	if c.Buffer.Strategy != StrategyDropOldest {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf(
				"buffer.strategy must be %q, got %q",
				StrategyDropOldest, c.Buffer.Strategy))
	}

	if c.Server.Addr == "" {
		return errors.Wrap(ErrConfigInvalid, "server.addr is required")
	}
	if c.Server.ReadTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid, "server.readTimeout must be > 0")
	}
	if c.Server.WriteTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid, "server.writeTimeout must be > 0")
	}
	if c.Server.HealthAddr == "" {
		return errors.Wrap(ErrConfigInvalid, "server.healthAddr is required")
	}

	if !isValidLogLevel(c.Log.Level) {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("log.level must be one of debug/info/warn/error, got %q",
				c.Log.Level))
	}
	if c.Log.Format != "json" && c.Log.Format != "console" {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("log.format must be json or console, got %q", c.Log.Format))
	}
	if c.Log.Output == "" {
		return errors.Wrap(ErrConfigInvalid, "log.output is required")
	}

	if c.Worker.PoolSize <= 0 {
		return errors.Wrap(ErrConfigInvalid, "worker.poolSize must be > 0")
	}
	if c.Worker.QueueSize <= 0 {
		return errors.Wrap(ErrConfigInvalid, "worker.queueSize must be > 0")
	}

	if c.Connection.ThrottleRatePerSec <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			"connection.throttleRatePerSec must be > 0")
	}
	if c.Connection.ThrottleBurst <= 0 {
		return errors.Wrap(ErrConfigInvalid, "connection.throttleBurst must be > 0")
	}
	if c.Connection.DialTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid, "connection.dialTimeout must be > 0")
	}

	if c.Shutdown.Timeout <= 0 {
		return errors.Wrap(ErrConfigInvalid, "shutdown.timeout must be > 0")
	}

	// Python bridge operational fields (D-03/D-04/D-05). Note that secret
	// values (VOLCENGINE_TTS_*) are intentionally absent from PythonConfig;
	// their presence is enforced by TestPythonConfigHasNoSecretFields.
	if c.Python.Interpreter == "" {
		return errors.Wrap(ErrConfigInvalid, "python.interpreter is required")
	}
	if c.Python.ScriptsDir == "" {
		return errors.Wrap(ErrConfigInvalid, "python.scriptsDir is required")
	}
	if c.Python.VocabScript == "" {
		return errors.Wrap(ErrConfigInvalid, "python.vocabScript is required")
	}
	if c.Python.PregenScript == "" {
		return errors.Wrap(ErrConfigInvalid, "python.pregenScript is required")
	}
	if c.Python.MaxConcurrent < 1 || c.Python.MaxConcurrent > 4 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("python.maxConcurrent must be in [1, 4], got %d",
				c.Python.MaxConcurrent))
	}
	if c.Python.DueTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid, "python.dueTimeout must be > 0")
	}
	if c.Python.ReviewTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid, "python.reviewTimeout must be > 0")
	}
	if c.Python.PregenTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid, "python.pregenTimeout must be > 0")
	}

	return nil
}

// validateThresholds checks the D-70 invariant: thresholds MUST be ascending
// and every element MUST be in the open-closed range (0, 100].
func validateThresholds(ts []Percent) error {
	if len(ts) == 0 {
		return errors.Wrap(ErrConfigInvalid, "memory.thresholds must not be empty")
	}
	prev := Percent(0)
	for i, p := range ts {
		if p <= 0 || p > 100 {
			return errors.Wrap(ErrConfigInvalid,
				fmt.Sprintf(
					"memory.thresholds[%d]=%d must be in (0, 100]", i, p))
		}
		if p <= prev {
			return errors.Wrap(ErrConfigInvalid,
				fmt.Sprintf(
					"memory.thresholds must be strictly ascending; %d followed by %d",
					prev, p))
		}
		prev = p
	}
	return nil
}

// isValidLogLevel is the allowlist for Log.Level.
func isValidLogLevel(level string) bool {
	switch level {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}

// bindEnvForAllLeaves registers explicit env bindings for every leaf field
// of Config. This is the T-01-06 mitigation: relying on AutomaticEnv alone
// is brittle because viper's case-handling can silently miss keys whose
// struct tag contains uppercase letters. BindEnv removes the ambiguity.
//
// Keys MUST match the mapstructure tag path exactly (lowercased by viper
// internally), and env var names are derived from SetEnvPrefix + the dotted
// key with "." replaced by "_" via SetEnvKeyReplacer.
func bindEnvForAllLeaves(v *viper.Viper) {
	keys := []string{
		"memory.limitBytes",
		"memory.monitorInterval",
		"memory.thresholds",
		"memory.loadShedAtPercent",
		"session.maxConcurrent",
		"session.idleTimeout",
		"session.cleanupInterval",
		"buffer.audioFrameBytes",
		"buffer.poolSize",
		"buffer.strategy",
		"server.addr",
		"server.readTimeout",
		"server.writeTimeout",
		"server.healthAddr",
		"log.level",
		"log.format",
		"log.output",
		"worker.poolSize",
		"worker.queueSize",
		"connection.throttleRatePerSec",
		"connection.throttleBurst",
		"connection.dialTimeout",
		"shutdown.timeout",
		"python.interpreter",
		"python.scriptsDir",
		"python.vocabScript",
		"python.pregenScript",
		"python.vocabDBPath",
		"python.dataDir",
		"python.cacheDir",
		"python.manifestDir",
		"python.envFile",
		"python.dueTimeout",
		"python.reviewTimeout",
		"python.pregenTimeout",
		"python.maxConcurrent",
	}
	for _, key := range keys {
		_ = v.BindEnv(key)
	}
}

// decodeHook is the viper DecodeHook passed to Unmarshal. It bridges the
// small gap between viper's string-typed env values and our two domain
// newtypes (MemoryBytes, Percent) and the slice type []Percent. Anything we
// do not own passes through untouched so viper's default hooks (duration
// parsing, slice splitting) still run.
func decodeHook(from, to reflect.Type, data interface{}) (interface{}, error) {
	switch to {
	case reflect.TypeOf(MemoryBytes(0)):
		// viper/cast hands us uint64 for numeric strings.
		if u, ok := data.(uint64); ok {
			return MemoryBytes(u), nil
		}
		if i, ok := data.(int); ok {
			if i < 0 {
				return nil, fmt.Errorf("MemoryBytes cannot be negative, got %d", i)
			}
			return MemoryBytes(i), nil
		}
		if f, ok := data.(float64); ok {
			if f < 0 {
				return nil, fmt.Errorf("MemoryBytes cannot be negative, got %f", f)
			}
			return MemoryBytes(f), nil
		}
	case reflect.TypeOf(Percent(0)):
		if i, ok := data.(int); ok {
			return Percent(i), nil
		}
		if f, ok := data.(float64); ok {
			return Percent(f), nil
		}
	case reflect.TypeOf([]Percent{}):
		switch v := data.(type) {
		case []int:
			out := make([]Percent, len(v))
			for i, x := range v {
				out[i] = Percent(x)
			}
			return out, nil
		case []float64:
			out := make([]Percent, len(v))
			for i, x := range v {
				out[i] = Percent(x)
			}
			return out, nil
		case []interface{}:
			out := make([]Percent, len(v))
			for i, x := range v {
				switch xv := x.(type) {
				case int:
					out[i] = Percent(xv)
				case float64:
					out[i] = Percent(xv)
				default:
					return nil, fmt.Errorf("thresholds element %d has unsupported type %T", i, x)
				}
			}
			return out, nil
		}
	}
	return data, nil
}
