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

	"github.com/go-viper/mapstructure/v2"
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

	// SessionTimeout tunes the per-state FSM timeouts for dictation sessions
	// (Phase 7, D-08/D-09). Sibling to Session (registry concerns stay separate
	// from FSM concerns).
	SessionTimeout SessionTimeoutConfig `json:"sessionTimeout" mapstructure:"sessionTimeout"`

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

	// WS tunes the public WebSocket runtime listener (P0-1). Holds only
	// operational fields mirroring the PythonConfig pattern (no secrets —
	// D-24 invariant). This is the infra default/override layer; ws package
	// constructors own the semantic defaults and re-validate (double
	// validation). AllowedOrigins defaults to the localhost pair so the
	// service boots with NO ws: block in config.yaml.
	WS WSConfig `json:"ws" mapstructure:"ws"`

	// Database tunes the SQLite storage layer (Phase 4). Holds only the
	// operational path; pragmas (WAL, busy_timeout, foreign_keys) are NOT
	// configurable here — they are correctness-critical and owned by
	// database.Open (D-09). The value SHOULD match PythonConfig.VocabDBPath
	// so Go and Python hit the same file (D-03). Empty path defaults to
	// $HOME/.home-tutor-agent/vocab.db via database.Open.
	Database DatabaseConfig `json:"database" mapstructure:"database"`

	// FSRS tunes the FSRS orchestration layer (Phase 5, D-03). Holds only
	// operational fields — default word count, fetch-lock duration, unit and
	// category scope — and NEVER credentials (D-24 no-secret invariant,
	// enforced by TestFSRSConfigHasNoSecretFields). These defaults flow into
	// FSRSService.SelectDueCards when the caller's SelectOpts fields are zero,
	// so the service boots and serves a sensible session with NO fsrs: block
	// in config.yaml.
	FSRS FSRSConfig `json:"fsrs" mapstructure:"fsrs"`

	// TTS tunes the TTS audio streaming layer (Phase 6, D-10). Holds only
	// operational fields — session cache directory, retry counts, timeouts,
	// and prefetch depth — and NEVER credentials (D-24 no-secret invariant,
	// enforced by TestTTSConfigHasNoSecretFields). These defaults flow into
	// tts constructors when the caller's option fields are zero, so the
	// service boots and serves a sensible session with NO tts: block in
	// config.yaml.
	TTS TTSConfig `json:"tts" mapstructure:"tts"`

	// Voice tunes the voice-command pipeline (Phase 8.2 / Ticket 03): Mimo
	// ASR + LLM intent analysis over streamed microphone audio. Operational
	// fields only — the MIMO_API_KEY secret is deliberately absent and read
	// from the live environment by the consuming code (D-05/D-24, same
	// invariant TestTTSConfigHasNoSecretFields enforces for TTS).
	Voice VoiceConfig `json:"voice" mapstructure:"voice"`

	// Photo tunes the photo-answer OCR pipeline (Phase 8.2 / Ticket 04):
	// streamed photo chunks reassembled into one JPEG, read through the Mimo
	// vision API, judged against the current dictation word. Shares the
	// voice pipeline's mimo client (voice.baseURL and the env-only
	// MIMO_API_KEY), so PhotoConfig carries no endpoint or secret of its own.
	Photo PhotoConfig `json:"photo" mapstructure:"photo"`

	// RT tunes the /rt realtime tutor layer (Phase 13, RT-01/RT-10):
	// the natural-voice WebSocket endpoint's transport governance and the
	// R0 fixed test lesson. Operational fields only — the MIMO_API_KEY
	// secret stays environment-only (D-24 no-secret invariant). Defaults
	// boot the service with NO rt: block in config.yaml; 0-value buffer /
	// message fields defer to rt.NewHandler's semantic defaults (double
	// validation, mirroring the WS pattern).
	RT RTConfig `json:"rt" mapstructure:"rt"`
}

// TLSConfig configures HTTPS termination on the public listener (Phase 13,
// RT-01). The two fields are paired: BOTH must be set to enable TLS; an
// absent server.tls block (nil pointer) leaves the listener on plain HTTP
// (the localhost dev path). The certificate is the owner's existing asset —
// paths point at deployment-side files and never enter git.
type TLSConfig struct {
	// CertFile is the path to the PEM certificate chain (e.g.
	// /etc/letsencrypt/live/<domain>/fullchain.pem). Empty when TLS is
	// disabled; MUST be non-empty iff KeyFile is non-empty.
	CertFile string `json:"certFile" mapstructure:"certFile"`

	// KeyFile is the path to the PEM private key (e.g.
	// /etc/letsencrypt/live/<domain>/privkey.pem). MUST be non-empty iff
	// CertFile is non-empty.
	KeyFile string `json:"keyFile" mapstructure:"keyFile"`
}

// RTConfig tunes the /rt realtime tutor layer (Phase 13). Defaults make the
// service boot with NO rt: block: the localhost origin pair, a 65551-byte
// message cap, 10s handshake / 30s heartbeat cadence, an EMPTY LessonID
// (the FSM derives the daily "r0-YYYYMMDD" lesson), and the 60s per-word
// timeout. Words are never configured here — they always come from the FSRS
// due fetch through the PyFSRS adapter (13-10 pivot; the retired
// rt.lessonWords key is ignored when old configs still carry it).
// Buffer-size and message-cap zeros defer to rt.NewHandler's own semantic
// defaults when left at 0 (double validation, WS pattern).
type RTConfig struct {
	// AllowedOrigins is the /rt Origin whitelist. Defaults to the localhost
	// pair ["localhost", "127.0.0.1"]; the wildcard "*" is rejected by
	// Validate. Production deployments MUST list the certificate's domain
	// (e.g. "tutor.example.com") — rt matches host(+port) like ws does.
	AllowedOrigins []string `json:"allowedOrigins" mapstructure:"allowedOrigins"`

	// MaxMessageBytes caps a single /rt WebSocket read. Default 65551
	// (matching the /ws cap). 0 defers to rt.NewHandler's default.
	MaxMessageBytes int64 `json:"maxMessageBytes" mapstructure:"maxMessageBytes"`

	// ReadBufferSize is the gorilla read buffer size. Default 0 →
	// rt.NewHandler fills its own default.
	ReadBufferSize int `json:"readBufferSize" mapstructure:"readBufferSize"`

	// WriteBufferSize is the gorilla write buffer size. Default 0 →
	// rt.NewHandler fills its own default.
	WriteBufferSize int `json:"writeBufferSize" mapstructure:"writeBufferSize"`

	// HandshakeTimeout bounds the /rt WebSocket upgrade handshake. Default
	// 10s. 0 defers to rt.NewHandler's default.
	HandshakeTimeout time.Duration `json:"handshakeTimeout" mapstructure:"handshakeTimeout"`

	// HeartbeatInterval is the /rt ping cadence. Default 30s (matching
	// ws.DefaultHeartbeatInterval). 0 defers to rt.NewHandler's default.
	HeartbeatInterval time.Duration `json:"heartbeatInterval" mapstructure:"heartbeatInterval"`

	// LessonID is the server-side lesson the /rt session binds to. When
	// non-empty it is used as-is (a dedicated classroom / test lesson);
	// when empty the server derives the daily lesson id "r0-YYYYMMDD" on
	// the Beijing calendar, so same-day sessions resume the same lesson.
	// Default: empty (daily derivation).
	LessonID string `json:"lessonId" mapstructure:"lessonId"`

	// WordTimeoutMs is the per-word writing timer in milliseconds: when it
	// elapses the FSM auto-advances to the next word. Default 60000 (60s);
	// the knob exists so real-device runs (13-12) can shorten it for
	// quicker verification. 0 means "use the default"; a negative value or
	// anything above 3600000 (1h) is rejected by Validate.
	WordTimeoutMs int `json:"wordTimeoutMs" mapstructure:"wordTimeoutMs"`

	// ForceStudentMode is the 13.3-05 production anti-fat-finger switch
	// (ROADMAP success criterion 6): when true, the server FORCES the
	// student projection for every /rt session — the client-supplied
	// session_init mode (including "test") is ignored at the
	// normalizeSessionMode adjudication point (rt.TurnManagerConfig.
	// ForceStudent, to which this key is wired verbatim through
	// cmd/service/main.go). Default false = permissive: existing dev
	// configs (no key) keep the full-reveal mode=test flow unchanged — the
	// Go zero value is the backward-compatible default (13.3-01 checkpoint ①
	// user-confirmed direction, 2026-10-10). The family entry point
	// (config.local.yaml, untracked) sets this true. This key is an
	// ANTI-FAT-FINGER measure, NOT an authentication/authorization
	// mechanism — no PIN, no session, no API gating (consensus decision 3).
	ForceStudentMode bool `json:"forceStudentMode" mapstructure:"forceStudentMode"`
}

// Validate enforces the RTConfig rules (D-04 fail-fast): origins non-empty
// with no wildcard "*", WordTimeoutMs in range (0 → default 60000; negative
// or above 3600000 rejected), and — when LessonID is set — a lightweight
// sanity check (≤64 runes, no whitespace, no path separators). An empty
// LessonID is legal and means the daily "r0-YYYYMMDD" derivation. On any
// failure it returns an error wrapping ErrConfigInvalid so callers branch
// with errors.Is(err, infra.ErrConfigInvalid).
func (c RTConfig) Validate() error {
	if len(c.AllowedOrigins) == 0 {
		return errors.Wrap(ErrConfigInvalid,
			"rt.allowedOrigins must not be empty (set the certificate domain for production)")
	}
	for _, origin := range c.AllowedOrigins {
		if origin == "*" {
			return errors.Wrap(ErrConfigInvalid,
				"rt.allowedOrigins must not contain wildcard \"*\"")
		}
	}
	if c.WordTimeoutMs < 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("rt.wordTimeoutMs must not be negative, got %d", c.WordTimeoutMs))
	}
	if c.WordTimeoutMs > 3600000 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("rt.wordTimeoutMs must not exceed 3600000 (1h), got %d", c.WordTimeoutMs))
	}
	if c.LessonID != "" {
		if len(c.LessonID) > 64 {
			return errors.Wrap(ErrConfigInvalid,
				fmt.Sprintf("rt.lessonId must not exceed 64 characters, got %d", len(c.LessonID)))
		}
		if strings.IndexFunc(c.LessonID, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
		}) >= 0 {
			return errors.Wrap(ErrConfigInvalid,
				"rt.lessonId must not contain whitespace")
		}
		if strings.ContainsAny(c.LessonID, `/\`) {
			return errors.Wrap(ErrConfigInvalid,
				"rt.lessonId must not contain path separators")
		}
	}
	return nil
}

// VoiceConfig tunes the voice-command pipeline (Phase 8.2, Ticket 03). It
// carries only operational fields; MIMO_API_KEY stays environment-only per
// the D-24 no-secret invariant. Every field has a working default so the
// service boots with NO voice: block in config.yaml.
type VoiceConfig struct {
	// BaseURL is the Mimo API endpoint root (no trailing slash). Default
	// "https://api.xiaomimimo.com/v1" — same default as pkg/mimo.NewClient;
	// duplicated here so the infra layer stays the operator-facing surface.
	BaseURL string `json:"baseUrl" mapstructure:"baseUrl"`

	// ChatModel is the LLM used for intent classification of voice commands
	// that miss the keyword fast path. Default "mimo-v2.5" (the -pro variant
	// costs more latency than a 4-way classification justifies).
	ChatModel string `json:"chatModel" mapstructure:"chatModel"`

	// MaxRecordingBytes caps one voice-command recording's accumulated PCM
	// (memory guard on the 1C1G VPS). Default 327680 bytes ≈ 10.2s of
	// 16kHz 16-bit mono — far beyond any "好了" utterance. MUST be > 0.
	MaxRecordingBytes int `json:"maxRecordingBytes" mapstructure:"maxRecordingBytes"`

	// AssembleWait bounds how long the voice_command handler waits for the
	// final streamed audio chunks to land after the trigger arrives (the
	// audio and control routes are separate worker pools, so the trigger can
	// be dequeued first). Default 2s. MUST be > 0.
	AssembleWait time.Duration `json:"assembleWait" mapstructure:"assembleWait"`

	// RequestTimeout bounds the WHOLE Mimo API sequence (ASR transcribe,
	// then the LLM chat fallback when the keyword fast path misses) served
	// under one voice command — one context spans both calls, so a slow ASR
	// eats into the LLM's budget rather than extending the total. Default
	// 15s, sized on live observation: ASR ~2s + a mimo-v2.5 thinking-model
	// classification ~8s ≈ 10s, leaving retry headroom; bounded so a wedged
	// upstream cannot pin router workers (WR-02 bounds Close, not individual
	// handlers). MUST be > 0.
	RequestTimeout time.Duration `json:"requestTimeout" mapstructure:"requestTimeout"`
}

// Validate enforces the VoiceConfig field ranges (D-04 fail-fast). On any
// failure it returns an error wrapping ErrConfigInvalid so callers branch
// with errors.Is(err, infra.ErrConfigInvalid).
func (c VoiceConfig) Validate() error {
	if c.BaseURL == "" {
		return errors.Wrap(ErrConfigInvalid, "voice.baseUrl is required")
	}
	if c.ChatModel == "" {
		return errors.Wrap(ErrConfigInvalid, "voice.chatModel is required")
	}
	if c.MaxRecordingBytes <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("voice.maxRecordingBytes must be > 0, got %d", c.MaxRecordingBytes))
	}
	if c.AssembleWait <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("voice.assembleWait must be > 0, got %v", c.AssembleWait))
	}
	if c.RequestTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("voice.requestTimeout must be > 0, got %v", c.RequestTimeout))
	}
	return nil
}

// PhotoConfig tunes the photo-answer OCR pipeline (Phase 8.2, Ticket 04).
// It carries only operational fields; the mimo endpoint and MIMO_API_KEY
// come from the shared voice pipeline wiring (env-only per the D-24
// no-secret invariant). Every field has a working default so the service
// boots with NO photo: block in config.yaml.
type PhotoConfig struct {
	// ChatModel is the vision-capable LLM used for OCR. Default
	// "mimo-v2.5" — the official image-understanding examples run this
	// model, keeping voice/ photo/ on one deployment shape.
	ChatModel string `json:"chatModel" mapstructure:"chatModel"`

	// MaxImageBytes caps one photo's reassembled JPEG size (memory guard on
	// the 1C1G VPS — the chunk assembler spoils any upload past the cap).
	// Default 1048576 (1MiB). The web controller downscales to ~768KiB before
	// chunking, so 1MiB leaves decode headroom. MUST be > 0.
	MaxImageBytes int `json:"maxImageBytes" mapstructure:"maxImageBytes"`

	// AssembleWait bounds how long the photo_submit handler waits for the
	// final streamed chunks after the trigger arrives (image and control
	// routes are separate worker pools, so the trigger can be dequeued
	// first). Default 2s. MUST be > 0.
	AssembleWait time.Duration `json:"assembleWait" mapstructure:"assembleWait"`

	// RequestTimeout bounds the Mimo vision OCR call served under one
	// photo_submit. Default 20s — a thinking model over an image takes
	// noticeably longer than the text-only voice classification (15s);
	// bounded so a wedged upstream cannot pin router workers. MUST be > 0.
	RequestTimeout time.Duration `json:"requestTimeout" mapstructure:"requestTimeout"`

	// ConfidenceThreshold is the OCR confidence floor: results below it
	// report low-confidence to the client, which offers the manual-input
	// fallback prefilled with the best guess. Default 0.6. MUST be in (0, 1)
	// — 0 would auto-accept garbage, 1 would always demand manual input.
	ConfidenceThreshold float64 `json:"confidenceThreshold" mapstructure:"confidenceThreshold"`
}

// Validate enforces the PhotoConfig field ranges (D-04 fail-fast). On any
// failure it returns an error wrapping ErrConfigInvalid so callers branch
// with errors.Is(err, infra.ErrConfigInvalid).
func (c PhotoConfig) Validate() error {
	if c.ChatModel == "" {
		return errors.Wrap(ErrConfigInvalid, "photo.chatModel is required")
	}
	if c.MaxImageBytes <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("photo.maxImageBytes must be > 0, got %d", c.MaxImageBytes))
	}
	if c.AssembleWait <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("photo.assembleWait must be > 0, got %v", c.AssembleWait))
	}
	if c.RequestTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("photo.requestTimeout must be > 0, got %v", c.RequestTimeout))
	}
	if c.ConfidenceThreshold <= 0 || c.ConfidenceThreshold >= 1 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("photo.confidenceThreshold must be in (0, 1), got %v", c.ConfidenceThreshold))
	}
	return nil
}

// DatabaseConfig tunes the SQLite storage layer (Phase 4). Holds only the
// operational path; pragmas (WAL, busy_timeout, foreign_keys) are NOT
// configurable here — they are correctness-critical and owned by
// database.Open (D-09). The value SHOULD match PythonConfig.VocabDBPath
// so Go and Python hit the same file (D-03). Empty path defaults to
// $HOME/.home-tutor-agent/vocab.db via database.Open.
type DatabaseConfig struct {
	// Path is the absolute or relative path to the SQLite database file.
	// Default "" (empty) — database.Open resolves to
	// $HOME/.home-tutor-agent/vocab.db when empty. The value SHOULD match
	// PythonConfig.VocabDBPath so Go and Python access the same file (D-03).
	Path string `json:"path" mapstructure:"path"`
}

// WSConfig tunes the Go service's WebSocket runtime (P0-1). It carries only
// operational fields; the wildcard origin "*" is forbidden at BOTH this layer
// and ws.ServerConfig.Validate (D-24). DefaultConfig supplies dev-safe origins
// [localhost, 127.0.0.1] so the service boots without an operator-supplied ws:
// block — there is intentionally NO "origins required" check here (the defaults
// always win when the operator leaves it empty).
//
// The MaxMessageBytes / *BufferSize fields default to ZERO (not 65551/4096) so
// ws.NewServer applies its own semantic defaults via its "treat 0 as
// use-default" branch. This keeps infra as the default/override layer and ws as
// the semantic-default layer (double validation per the P0-1 design decision).
type WSConfig struct {
	// AllowedOrigins is the Origin whitelist (D-24). Default ["localhost",
	// "127.0.0.1"]. The wildcard "*" is rejected by Validate. ws.ServerConfig
	// matches scheme+host+port (IN-03), so operators MUST include the port for
	// non-default ports (e.g. "tutor.example.com:8080").
	AllowedOrigins []string `json:"allowedOrigins" mapstructure:"allowedOrigins"`

	// HeartbeatInterval is the WS ping cadence (D-14). Default 30s, equal to
	// ws.DefaultHeartbeatInterval.
	HeartbeatInterval time.Duration `json:"heartbeatInterval" mapstructure:"heartbeatInterval"`

	// MaxMessageBytes caps a single WS read. Default 0 → ws.NewServer fills
	// DefaultMaxMessageBytes (65551). Infra does NOT pre-fill 65551.
	MaxMessageBytes int64 `json:"maxMessageBytes" mapstructure:"maxMessageBytes"`

	// ReadBufferSize is the gorilla read buffer size. Default 0 → ws.NewServer
	// fills 4096.
	ReadBufferSize int `json:"readBufferSize" mapstructure:"readBufferSize"`

	// WriteBufferSize is the gorilla write buffer size. Default 0 →
	// ws.NewServer fills 4096.
	WriteBufferSize int `json:"writeBufferSize" mapstructure:"writeBufferSize"`

	// HubLimit is the max concurrent WS connections (D-04). Default 100,
	// matching SessionConfig.MaxConcurrent.
	HubLimit int `json:"hubLimit" mapstructure:"hubLimit"`

	// HubRegisterBuffer is the Hub register channel capacity. Default 32.
	HubRegisterBuffer int `json:"hubRegisterBuffer" mapstructure:"hubRegisterBuffer"`

	// HubUnregisterBuffer is the Hub unregister channel capacity. Default 32.
	HubUnregisterBuffer int `json:"hubUnregisterBuffer" mapstructure:"hubUnregisterBuffer"`

	// RouteBuffer is the per-MsgType Router channel capacity. Default 32
	// (ws.RouterConfig default).
	RouteBuffer int `json:"routeBuffer" mapstructure:"routeBuffer"`

	// WorkerConcurrency is the per-MsgType Router worker count. Default 4
	// (ws.RouterConfig default).
	WorkerConcurrency int `json:"workerConcurrency" mapstructure:"workerConcurrency"`
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
	// the Python scripts for non-cache persistent state. It is ALSO the
	// anchor for the web/api layer's fsrs_config.json writer (Phase 9,
	// Pitfall 8 single-resolution rule: Go writer and Python reader must
	// resolve THE SAME directory). Empty is rejected by Validate — an empty
	// value silently splits the two ends (env not forwarded → Python reads
	// SKILL_DIR/data; Go writes CWD/fsrs_config.json).
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

	// MgmtTimeout caps the wall-clock duration of a vocab_manager.py
	// management call (add/edit/rename-unit/delete-words — Phase 9, VOC-01).
	// Units: seconds (parsed from strings like "10s"). Default 10s, matching
	// DueTimeout: these are in-process fast SQLite operations.
	MgmtTimeout time.Duration `json:"mgmtTimeout" mapstructure:"mgmtTimeout"`

	// MaxConcurrent is the global bounded-semaphore cap on simultaneously
	// running Python subprocesses (D-02). Prevents 50-100 concurrent sessions
	// from fork-bombing the 1C/1G VPS. Default 2. MUST be in [1, 4].
	MaxConcurrent int `json:"maxConcurrent" mapstructure:"maxConcurrent"`
}

// FSRSConfig tunes the FSRS orchestration layer (Phase 5, D-03). It carries
// only operational fields — word counts, durations, and scope labels — and is
// intentionally devoid of credentials; the TestFSRSConfigHasNoSecretFields
// reflect guard enforces the D-24 no-secret invariant automatically. These
// values are the defaults Plan 05-02's NewFSRSService reads when SelectOpts
// leaves a field at its zero value.
type FSRSConfig struct {
	// DefaultWordCount is the number of words a dictation session pulls when
	// the caller does not specify a count (FSRS-02). Default 25, sitting in
	// the 20-30 band. MUST be in [1, 100] so a session never returns zero
	// words nor an unbounded batch on the resource-constrained VPS.
	DefaultWordCount int `json:"defaultWordCount" mapstructure:"defaultWordCount"`

	// FetchLockHours is how long a single fetch result is considered fresh
	// before a re-fetch is permitted (FSRS-03). Default 24.0 (hours). MUST be
	// > 0 so the lock cannot be disabled (which would allow re-fetch storms).
	FetchLockHours float64 `json:"fetchLockHours" mapstructure:"fetchLockHours"`

	// DefaultUnit is the unit/lesson scope used when the caller does not pin a
	// unit. The empty string is the valid "all units" default — it is NOT
	// rejected by Validate. Default "".
	DefaultUnit string `json:"defaultUnit" mapstructure:"defaultUnit"`

	// DefaultCategory is the vocabulary category scope (e.g. "english").
	// Default "english" (Phase 5 scope). MUST be non-empty so SelectDueCards
	// always narrows to a real category rather than returning an unscoped set.
	DefaultCategory string `json:"defaultCategory" mapstructure:"defaultCategory"`
}

// Validate enforces the FSRSConfig field ranges (D-03/D-04 fail-fast). On any
// failure it returns an error wrapping ErrConfigInvalid so callers branch with
// errors.Is(err, infra.ErrConfigInvalid). NewFSRSService in Plan 05-02 also
// calls this directly, so the method is independently callable from
// Config.Validate.
func (c FSRSConfig) Validate() error {
	if c.DefaultWordCount < 1 || c.DefaultWordCount > 100 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("fsrs.defaultWordCount must be in [1, 100], got %d",
				c.DefaultWordCount))
	}
	if c.FetchLockHours <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("fsrs.fetchLockHours must be > 0, got %v",
				c.FetchLockHours))
	}
	if c.DefaultCategory == "" {
		return errors.Wrap(ErrConfigInvalid, "fsrs.defaultCategory is required")
	}
	return nil
}

// SessionTimeoutConfig tunes the per-state FSM timeouts for dictation sessions
// (Phase 7, D-08/D-09). Each field bounds how long a Session may remain in the
// corresponding state before the event loop exits and the D-10 defer chain
// fires. It is a sibling to SessionConfig (registry/connection concerns) so
// FSM tuning never interacts with the connection-count bounds.
type SessionTimeoutConfig struct {
	// IdleStateTimeout bounds the IDLE state (default 30m). MUST be > 0.
	IdleStateTimeout time.Duration `json:"idleStateTimeout" mapstructure:"idleStateTimeout"`
	// ListeningStateTimeout bounds the LISTENING state (default 30s). MUST be > 0.
	ListeningStateTimeout time.Duration `json:"listeningStateTimeout" mapstructure:"listeningStateTimeout"`
	// DictatingStateTimeout bounds the DICTATING state (default 30s). MUST be > 0.
	DictatingStateTimeout time.Duration `json:"dictatingStateTimeout" mapstructure:"dictatingStateTimeout"`
	// PausedStateTimeout bounds the PAUSED state (default 5m; Ticket 02
	// DICT-07 — the student controls when to resume). MUST be > 0.
	PausedStateTimeout time.Duration `json:"pausedStateTimeout" mapstructure:"pausedStateTimeout"`
	// WaitingParentStateTimeout bounds the WAITING_PARENT state (default 30m). MUST be > 0.
	WaitingParentStateTimeout time.Duration `json:"waitingParentStateTimeout" mapstructure:"waitingParentStateTimeout"`
	// FeedbackStateTimeout bounds the FEEDBACK state (default 5m). MUST be > 0.
	FeedbackStateTimeout time.Duration `json:"feedbackStateTimeout" mapstructure:"feedbackStateTimeout"`
}

// Validate enforces that every per-state timeout is strictly positive (D-04
// fail-fast). On any failure it returns an error wrapping ErrConfigInvalid so
// callers branch with errors.Is(err, infra.ErrConfigInvalid). Independently
// callable from Config.Validate (mirrors FSRSConfig.Validate).
func (c SessionTimeoutConfig) Validate() error {
	if c.IdleStateTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("session.idleStateTimeout must be > 0, got %v", c.IdleStateTimeout))
	}
	if c.ListeningStateTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("session.listeningStateTimeout must be > 0, got %v", c.ListeningStateTimeout))
	}
	if c.DictatingStateTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("session.dictatingStateTimeout must be > 0, got %v", c.DictatingStateTimeout))
	}
	if c.PausedStateTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("session.pausedStateTimeout must be > 0, got %v", c.PausedStateTimeout))
	}
	if c.WaitingParentStateTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("session.waitingParentStateTimeout must be > 0, got %v", c.WaitingParentStateTimeout))
	}
	if c.FeedbackStateTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("session.feedbackStateTimeout must be > 0, got %v", c.FeedbackStateTimeout))
	}
	return nil
}

// TTSConfig tunes the TTS audio streaming layer (Phase 6, D-10). It carries
// only operational fields — session cache directory, retry counts, timeouts,
// and prefetch depth — and is intentionally devoid of credentials; the
// TestTTSConfigHasNoSecretFields reflect guard enforces the D-24 no-secret
// invariant automatically. These values are the defaults Plan 06-01's tts
// constructors read when option fields are zero.
type TTSConfig struct {
	// SessionDir is the base directory for session PCM caches (D-02, D-08).
	// Each session gets a subdirectory under this base. Default "/tmp/tts_sessions".
	// MUST be non-empty.
	SessionDir string `json:"sessionDir" mapstructure:"sessionDir"`

	// MaxRetries is the per-word TTS generation retry count (D-05). Default 3.
	// MUST be in [1, 10] to prevent infinite retry loops.
	MaxRetries int `json:"maxRetries" mapstructure:"maxRetries"`

	// GenerationTimeout is the single-word generation timeout (D-05). Default
	// 120s (matches PythonConfig.PregenTimeout). MUST be > 0.
	GenerationTimeout time.Duration `json:"generationTimeout" mapstructure:"generationTimeout"`

	// PrefetchCount is the number of upcoming words to pre-load into memory
	// (D-04). Default 2. MUST be in [0, 10]; zero disables prefetch.
	PrefetchCount int `json:"prefetchCount" mapstructure:"prefetchCount"`
}

// Validate enforces the TTSConfig field ranges (D-10 fail-fast). On any
// failure it returns an error wrapping ErrConfigInvalid so callers branch with
// errors.Is(err, infra.ErrConfigInvalid).
func (c TTSConfig) Validate() error {
	if c.SessionDir == "" {
		return errors.Wrap(ErrConfigInvalid, "tts.sessionDir is required")
	}
	if c.MaxRetries < 1 || c.MaxRetries > 10 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("tts.maxRetries must be in [1, 10], got %d", c.MaxRetries))
	}
	if c.GenerationTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("tts.generationTimeout must be > 0, got %v", c.GenerationTimeout))
	}
	if c.PrefetchCount < 0 || c.PrefetchCount > 10 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("tts.prefetchCount must be in [0, 10], got %d", c.PrefetchCount))
	}
	return nil
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

	// StaticDir is the filesystem directory served as static files for the
	// student web interface (Phase 8, WEB-02/WEB-04). Empty (the default)
	// DISABLES static file serving — the service boots without it, mirroring
	// the Database.Path "empty = disabled" precedent. When non-empty, main.go
	// validates the directory exists at boot and mounts an http.FileServer at
	// StaticRoute. Relative paths resolve against the process CWD, so
	// production deployments typically set an absolute path
	// (e.g. "/opt/hta/pkg/web/static").
	StaticDir string `json:"staticDir" mapstructure:"staticDir"`

	// StaticRoute is the URL prefix at which the static file server is mounted
	// (Phase 8). Default "/". MUST start with "/" and MUST NOT collide with
	// the "/ws" WebSocket upgrade path. Mounting at "/" coexists with "/ws"
	// because Go's ServeMux matches the longer, more specific "/ws" pattern
	// first. Only consulted when StaticDir is non-empty.
	StaticRoute string `json:"staticRoute" mapstructure:"staticRoute"`

	// TLS opts the public listener into HTTPS termination (Phase 13, RT-01).
	// nil (the default and the absence of a server.tls block) keeps the
	// listener on plain HTTP — the localhost dev path. When non-nil BOTH
	// CertFile and KeyFile must be set (see TLSConfig); a yaml `tls: {}`
	// empty block is rejected by Validate rather than silently disabling
	// TLS (L-7: no silent downgrade on the trusted entrance).
	TLS *TLSConfig `json:"tls" mapstructure:"tls"`
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
		SessionTimeout: SessionTimeoutConfig{
			IdleStateTimeout:          30 * time.Minute,
			ListeningStateTimeout:     30 * time.Second,
			DictatingStateTimeout:     30 * time.Second,
			PausedStateTimeout:        5 * time.Minute,
			WaitingParentStateTimeout: 30 * time.Minute,
			FeedbackStateTimeout:      5 * time.Minute,
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
			// Phase 8: static interface serving is DISABLED by default (empty
			// dir). Operators opt in by setting server.staticDir; the route
			// then defaults to "/" so the student interface loads at the
			// service root.
			StaticDir:   "",
			StaticRoute: "/",
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
			MgmtTimeout:   10 * time.Second,
			MaxConcurrent: 2,
		},
		WS: WSConfig{
			// Dev-safe origins so the service boots with NO ws: block in
			// config.yaml (P0-1 must-have). Operators MUST set production
			// origins; the wildcard "*" is rejected at both layers (D-24).
			AllowedOrigins:      []string{"localhost", "127.0.0.1"},
			HeartbeatInterval:   30 * time.Second, // == ws.DefaultHeartbeatInterval
			MaxMessageBytes:     0,                // 0 → ws.NewServer fills 65551
			ReadBufferSize:      0,                // 0 → ws.NewServer fills 4096
			WriteBufferSize:     0,                // 0 → ws.NewServer fills 4096
			HubLimit:            100,              // D-04 / session.maxConcurrent parity
			HubRegisterBuffer:   32,
			HubUnregisterBuffer: 32,
			RouteBuffer:         32, // ws.RouterConfig default
			WorkerConcurrency:   4,  // ws.RouterConfig default
		},
		Database: DatabaseConfig{
			Path: "", // empty → database.Open resolves to $HOME/.home-tutor-agent/vocab.db
		},
		FSRS: FSRSConfig{
			// FSRS-02 default word count sits in the 20-30 band so a session
			// with no explicit count serves a sensible batch.
			DefaultWordCount: 25,
			// FSRS-03 fetch-lock: a fetched word set is fresh for 24h before a
			// re-fetch is permitted.
			FetchLockHours:  24.0,
			DefaultUnit:     "", // empty = all units (valid default)
			DefaultCategory: "english",
		},
		TTS: TTSConfig{
			// D-02: session-level cache under /tmp/tts_sessions/{sessionID}/
			SessionDir: "/tmp/tts_sessions",
			// D-05: 3 retries per word before skipping
			MaxRetries: 3,
			// D-05: 120s timeout matches PythonConfig.PregenTimeout
			GenerationTimeout: 120 * time.Second,
			// D-04: prefetch next 2 words to eliminate network latency
			PrefetchCount: 2,
		},
		Voice: VoiceConfig{
			// Same default as pkg/mimo.NewClient — duplicated so operators
			// see the surface at the infra layer (see VoiceConfig docs).
			BaseURL: "https://api.xiaomimimo.com/v1",
			// mimo-v2.5: intent classification is a 4-way choice; -pro's
			// extra capability buys latency we do not want per command.
			ChatModel: "mimo-v2.5",
			// ~10.2s of 16kHz 16-bit mono — generous for a spoken command.
			MaxRecordingBytes: 327680,
			// Audio and control routes are separate worker pools; the
			// trigger can be dequeued before the last audio chunks.
			AssembleWait: 2 * time.Second,
			// Bounded per-call so a wedged upstream cannot pin workers.
			RequestTimeout: 15 * time.Second,
		},
		Photo: PhotoConfig{
			// Vision-capable per the official image-understanding docs; same
			// deployment shape as the voice intent classifier.
			ChatModel: "mimo-v2.5",
			// 1MiB cap — the client re-encodes to ~768KiB max, leaving
			// headroom. Bounds orphaned-buffer memory on the 1C1G VPS.
			MaxImageBytes: 1048576,
			// Same separate-routes rationale as voice.assembleWait.
			AssembleWait: 2 * time.Second,
			// Vision thinking over an image runs slower than text-only
			// classification (voice: 15s); 20s with headroom.
			RequestTimeout: 20 * time.Second,
			// Below 0.6 the client offers manual input prefilled with the
			// best guess rather than auto-judging.
			ConfidenceThreshold: 0.6,
		},
		RT: RTConfig{
			// Dev-safe origins (same pair as ws); production MUST set the
			// certificate domain. The wildcard "*" is rejected by Validate.
			AllowedOrigins: []string{"localhost", "127.0.0.1"},
			// Matches the /ws read cap; 0 would also defer to rt.NewHandler.
			MaxMessageBytes: 65551,
			// Buffer sizes stay 0 → rt.NewHandler fills its own defaults
			// (double validation, WS pattern).
			ReadBufferSize: 0,
			WriteBufferSize: 0,
			// 10s handshake (TLS+upgrade on a slow iPad radio), 30s ping
			// cadence matching ws.DefaultHeartbeatInterval.
			HandshakeTimeout:  10 * time.Second,
			HeartbeatInterval: 30 * time.Second,
			// LessonID empty → the FSM derives the daily "r0-YYYYMMDD"
			// lesson on the Beijing calendar (13-10 pivot: words come from
			// the FSRS due fetch, the fixed three-word lesson is retired).
			LessonID: "",
			// 60s per-word confirm wait; the knob exists for real-device
			// calibration runs to shorten.
			WordTimeoutMs: 60000,
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
// Working-directory requirement (Phase 8, G-08-2):
//   - configPath is resolved relative to the process CWD. The service MUST
//     be started from the repository root (use `make run` or ./run.ps1) for
//     a relative path like "config.yaml" to resolve.
//   - If the file is NOT found, LoadConfig silently falls back to
//     DefaultConfig, whose Server.StaticDir is "" — that DISABLES the
//     student web interface. The service boots and serves debug test pages
//     instead, with CSS/JS assets returning 404. This is an operational
//     concern (not a code bug): see docs/DEPLOYMENT.md for startup procedures.
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
	//
	// Supplying a custom DecodeHook makes viper's internal v.decodeHook
	// non-nil, which makes viper SKIP its default composed hook chain
	// (StringToTimeDurationHookFunc, StringToSliceHookFunc). The decodeHook
	// below only owns MemoryBytes/Percent; without the defaults composed
	// back in, every time.Duration field ("5s"/"30s" in config.yaml) fails
	// to parse via strconv.ParseInt and the service cannot boot. We compose
	// the standard hooks back so duration + slice parsing works as the
	// decodeHook comment promises.
	if err := v.Unmarshal(&cfg, viper.DecodeHook(
		mapstructure.ComposeDecodeHookFunc(
			decodeHook,
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(" "),
		))); err != nil {
		return Config{}, errors.Wrap(err, "unmarshalling config")
	}

	// L-7 (Phase 13): viper's AllKeys-based unmarshal drops an empty
	// `server.tls: {}` yaml block entirely — it has no leaf keys, so the
	// *TLSConfig pointer stays nil and TLS would be silently disabled.
	// IsSet still sees the block; fail fast instead of downgrading the
	// trusted entrance (RT-01). A tls block with any real leaf key
	// reaches the struct and is checked by Config.Validate's pair rules.
	if cfg.Server.TLS == nil && v.IsSet("server.tls") {
		return Config{}, errors.Wrap(ErrConfigInvalid,
			"server.tls present but certFile/keyFile empty — remove the tls block to disable TLS (no silent HTTP downgrade)")
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

	// Phase 8 static interface (WEB-02/WEB-04). StaticDir empty means
	// "static serving disabled" (Database.Path precedent), so validation is
	// only consulted when the operator opts in. When set, the route MUST be a
	// valid absolute path that does not shadow the /ws upgrade. Directory
	// EXISTENCE is intentionally NOT checked here — it is CWD-relative and
	// would break DefaultConfig-based tests and cross-CWD boots; main.go
	// fails fast on a missing directory at startup (Rule 2: fail-fast at boot).
	if c.Server.StaticDir != "" {
		if c.Server.StaticRoute == "" {
			return errors.Wrap(ErrConfigInvalid,
				"server.staticRoute is required when server.staticDir is set")
		}
		if !strings.HasPrefix(c.Server.StaticRoute, "/") {
			return errors.Wrap(ErrConfigInvalid,
				fmt.Sprintf("server.staticRoute must start with \"/\", got %q",
					c.Server.StaticRoute))
		}
		if c.Server.StaticRoute == "/ws" || strings.HasPrefix(c.Server.StaticRoute, "/ws/") {
			return errors.Wrap(ErrConfigInvalid,
				"server.staticRoute must not collide with the /ws WebSocket upgrade path")
		}
	}

	// Phase 13 TLS three-state (L-7, RT-01). nil = disabled (plain HTTP,
	// the localhost dev path). Non-nil demands BOTH paths set: an empty
	// pair (`tls: {}` in yaml) or a half pair is a hard error so the
	// trusted entrance never silently downgrades to HTTP.
	if c.Server.TLS != nil {
		if c.Server.TLS.CertFile == "" && c.Server.TLS.KeyFile == "" {
			return errors.Wrap(ErrConfigInvalid,
				"server.tls present but certFile/keyFile empty — remove the tls block to disable TLS (no silent HTTP downgrade)")
		}
		if c.Server.TLS.CertFile == "" || c.Server.TLS.KeyFile == "" {
			return errors.Wrap(ErrConfigInvalid,
				"server.tls.certFile and server.tls.keyFile must be set as a pair")
		}
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
	if c.Python.MgmtTimeout <= 0 {
		return errors.Wrap(ErrConfigInvalid, "python.mgmtTimeout must be > 0")
	}
	// WR-05 (Pitfall 8): DataDir anchors BOTH the web/api fsrs_config.json
	// writer and the DICTATION_DATA_DIR env the Python scripts read. When
	// empty, Bridge.childEnv silently withholds the env (Python falls back
	// to SKILL_DIR/data) while Go writes CWD/fsrs_config.json — the parent's
	// saved settings never reach the scheduler, with no error anywhere.
	if c.Python.DataDir == "" {
		return errors.Wrap(ErrConfigInvalid,
			"python.dataDir must not be empty (anchors fsrs_config.json writer and DICTATION_DATA_DIR reader on one directory)")
	}

	// WS origin whitelist (D-24). The wildcard "*" is forbidden at this layer
	// to mirror ws.ServerConfig.Validate; both layers reject it so a slip at
	// either boundary is caught. There is intentionally NO "origins required"
	// check here — DefaultConfig always supplies the localhost pair, so an
	// operator-supplied empty list simply means the defaults win and the
	// service still boots. The semantics-level "origins required" enforcement
	// happens inside ws.ServerConfig.Validate, which main.go still invokes.
	for _, origin := range c.WS.AllowedOrigins {
		if origin == "*" {
			return errors.Wrap(ErrConfigInvalid,
				"ws.allowedOrigins must not contain wildcard \"*\" (D-24)")
		}
	}

	// FSRS orchestration (Phase 5, D-03). FSRSConfig.Validate enforces field
	// ranges so a malformed HTA_FSRS_* value fails boot rather than producing
	// silent misconfiguration (D-04 fail-fast). Delegated to the sub-struct so
	// NewFSRSService can call the same validator independently.
	if err := c.FSRS.Validate(); err != nil {
		return err
	}

	// TTS audio streaming (Phase 6, D-10). TTSConfig.Validate enforces field
	// ranges so a malformed HTA_TTS_* value fails boot rather than producing
	// silent misconfiguration (D-04 fail-fast). Delegated to the sub-struct so
	// tts constructors can call the same validator independently.
	if err := c.TTS.Validate(); err != nil {
		return err
	}

	// Session FSM timeouts (Phase 7, D-09). SessionTimeoutConfig.Validate
	// enforces positive durations so a malformed HTA_SESSIONTIMEOUT_* value
	// fails boot rather than producing an instant-timeout session (D-04
	// fail-fast). Delegated to the sub-struct so it is independently callable.
	if err := c.SessionTimeout.Validate(); err != nil {
		return err
	}

	// Voice-command pipeline (Phase 8.2, Ticket 03). VoiceConfig.Validate
	// enforces field ranges so a malformed HTA_VOICE_* value fails boot
	// rather than producing an unbounded recording buffer (D-04 fail-fast).
	if err := c.Voice.Validate(); err != nil {
		return err
	}

	// Photo-answer OCR pipeline (Phase 8.2, Ticket 04). PhotoConfig.Validate
	// enforces field ranges so a malformed HTA_PHOTO_* value fails boot
	// rather than producing an unbounded photo buffer (D-04 fail-fast).
	if err := c.Photo.Validate(); err != nil {
		return err
	}

	// /rt realtime tutor layer (Phase 13). RTConfig.Validate enforces the
	// origin whitelist shape and the fixed test lesson bounds so a malformed
	// HTA_RT_* value fails boot rather than producing an open or unusable
	// realtime endpoint (D-04 fail-fast).
	if err := c.RT.Validate(); err != nil {
		return err
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
		// Phase 8 static interface (WEB-02/WEB-04). Pitfall 6: every leaf
		// MUST be bound explicitly or HTA_SERVER_STATIC* env vars are inert.
		"server.staticDir",
		"server.staticRoute",
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
		"python.mgmtTimeout",
		"python.maxConcurrent",
		"ws.allowedOrigins",
		"ws.heartbeatInterval",
		"ws.maxMessageBytes",
		"ws.readBufferSize",
		"ws.writeBufferSize",
		"ws.hubLimit",
		"ws.hubRegisterBuffer",
		"ws.hubUnregisterBuffer",
		"ws.routeBuffer",
		"ws.workerConcurrency",
		// Phase 5 FSRS orchestration (D-03). Pitfall 6: every leaf MUST be
		// bound explicitly or HTA_FSRS_* env vars are silently inert.
		"fsrs.defaultWordCount",
		"fsrs.fetchLockHours",
		"fsrs.defaultUnit",
		"fsrs.defaultCategory",
		// Phase 6 TTS audio streaming (D-10). Pitfall 6: every leaf MUST be
		// bound explicitly or HTA_TTS_* env vars are silently inert.
		"tts.sessionDir",
		"tts.maxRetries",
		"tts.generationTimeout",
		"tts.prefetchCount",
		// Phase 8.2 voice-command pipeline (Ticket 03). Pitfall 6: every leaf
		// MUST be bound explicitly or HTA_VOICE_* env vars are silently inert.
		"voice.baseUrl",
		"voice.chatModel",
		"voice.maxRecordingBytes",
		"voice.assembleWait",
		"voice.requestTimeout",
		// Phase 8.2 photo-answer OCR pipeline (Ticket 04). Same Pitfall 6:
		// every leaf MUST be bound explicitly or HTA_PHOTO_* env vars are
		// silently inert.
		"photo.chatModel",
		"photo.maxImageBytes",
		"photo.assembleWait",
		"photo.requestTimeout",
		"photo.confidenceThreshold",
		// Phase 7 session FSM per-state timeouts (D-09). Pitfall 6: every leaf
		// MUST be bound explicitly or HTA_SESSIONTIMEOUT_* env vars are silently
		// inert. Keys mirror the mapstructure path of SessionTimeoutConfig
		// (Config.SessionTimeout -> sessionTimeout.*).
		"sessionTimeout.idleStateTimeout",
		"sessionTimeout.listeningStateTimeout",
		"sessionTimeout.dictatingStateTimeout",
		"sessionTimeout.pausedStateTimeout",
		"sessionTimeout.waitingParentStateTimeout",
		"sessionTimeout.feedbackStateTimeout",
		// Database path (Phase 4).
		"database.path",
		// Phase 13 RT realtime layer + TLS (RT-01/RT-10). Pitfall 6: every
		// leaf MUST be bound explicitly or HTA_SERVER_TLS_*/HTA_RT_* env
		// vars are silently inert.
		"server.tls.certFile",
		"server.tls.keyFile",
		"rt.allowedOrigins",
		"rt.maxMessageBytes",
		"rt.readBufferSize",
		"rt.writeBufferSize",
		"rt.handshakeTimeout",
		"rt.heartbeatInterval",
		"rt.lessonId",
		"rt.wordTimeoutMs",
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
