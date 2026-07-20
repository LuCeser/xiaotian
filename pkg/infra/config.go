// Package infra provides the foundational infrastructure for the Home Tutor
// Agent Go service: typed configuration, memory budgeting, connection
// limiting, structured logging, and resource lifecycle.
//
// Config is loaded once at startup (D-19). Runtime reload is intentionally
// unsupported in Phase 1 — operators restart the process to pick up new
// values.
package infra

import (
	"runtime"
	"time"

	"github.com/pkg/errors"
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
	}
}
