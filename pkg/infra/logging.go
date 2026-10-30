package infra

import (
	"fmt"
	"io"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Field is the structured-logging field type (D-25, D-40). It is a small
// domain value carried across the Logger interface boundary so that callers
// (and tests, and fakes) do not need to import go.uber.org/zap to emit a
// log entry. Each Field maps to a single key/value pair in the structured
// output.
//
// Use the F helper to construct fields; use ErrField for error values.
type Field struct {

	// Value is the field payload. Any type is accepted; zap.Any encoding
	// rules apply (primitives encode natively, structs encode via
	// reflection). String values are passed through sanitizeString before
	// emission to defend against log injection (T-01-09).
	Value any
	// Key is the field name. Convention (D-25): lowercase snake_case, e.g.
	// "request_id", "component", "operation", "duration_ms".
	Key string
}

// F is the short constructor for a Field. Used everywhere a log call needs a
// key/value pair: `logger.Info("loaded", F("component", "infra"), F("ms", 42))`.
func F(key string, value any) Field {
	return Field{Key: key, Value: value}
}

// ErrField is the convention for logging an error as a structured field. The
// resulting Field carries the error's message text under key "error". Using
// this consistently lets operators filter logs by `error=` regardless of
// which subsystem emitted them.
func ErrField(err error) Field {
	return Field{Key: "error", Value: err.Error()}
}

// Logger is the structured logging contract every Wave 3+ package accepts as
// a constructor parameter (D-42 constructor DI, D-47 no global state). The
// interface is intentionally minimal (SRP, D-43): five severity levels, one
// context-binding method, one Sync method. Everything else (sampling,
// hooks, output routing) is an implementation detail owned by the concrete
// logger.
//
// Implementations:
//   - *ZapLogger: production JSON logger, built by NewLogger
//   - nopLogger (returned by NewNoOpLogger): silent zero value for tests
//
// Context propagation (D-25): With(...) returns a child Logger that injects
// the supplied fields into every subsequent entry. This is how request_id,
// component, operation, etc. flow from the entrypoint down into deep
// handler code without thread-local state.
type Logger interface {
	// Debug emits a log entry at debug level. Filtered out when the logger's
	// configured level is info/warn/error.
	Debug(msg string, fields ...Field)

	// Info emits a log entry at info level. Default production level.
	Info(msg string, fields ...Field)

	// Warn emits a log entry at warn level. Used for degraded-but-operational
	// conditions (e.g. approaching memory threshold).
	Warn(msg string, fields ...Field)

	// Error emits a log entry at error level. Used for recovered panics,
	// failed operations, and other conditions that need operator attention.
	Error(msg string, fields ...Field)

	// With returns a child Logger that includes the supplied fields on every
	// subsequent entry. Fields accumulate: child.With(b).Info(...) inherits
	// both the parent's context fields and the new ones.
	With(fields ...Field) Logger

	// Sync flushes any buffered log entries to the underlying writer. Must
	// be called before process exit. Returns nil for unbuffered writers and
	// for the NoOpLogger.
	Sync() error
}

// ZapLogger is the production Logger implementation, backed by a
// *go.uber.org/zap.Logger. It is the only Logger implementation that
// produces output; tests that do not need output use NewNoOpLogger instead.
//
// The embedded *zap.Logger is unexported so callers cannot accidentally
// bypass the Field-sanitization layer (T-01-09) by calling zap methods
// directly.
type ZapLogger struct {
	// l is the underlying zap logger. Delegated to for every severity method.
	// When With is called, l is replaced with a child zap logger that has
	// the new fields baked in.
	l *zap.Logger

	// closer closes file-backed log output when non-nil. stderr/stdout and
	// test buffers leave this nil. Declared before the slice so the
	// pointer-bearing fields pack together (govet fieldalignment: 32
	// pointer bytes instead of 48).
	closer io.Closer

	// baseFields is the accumulated context fields for this logger
	// instance. Tracked alongside l's internal state so that introspection
	// (and future features like a Metrics hook) can see the logical context
	// without decoding zap's internal representation. Mirrors D-25 context
	// propagation.
	baseFields []Field
}

// ensure ZapLogger satisfies Logger at compile time.
var _ Logger = (*ZapLogger)(nil)

// NewLogger builds a production Logger from LogConfig (D-19 startup-only).
// The returned Logger writes structured JSON (when cfg.Format == "json") or
// human-readable console output (when cfg.Format == "console") to the
// configured sink: "stderr", "stdout", or a file path (issue #94 — opened
// create+append, 0644, NOT mirrored to stderr). An unopenable path returns
// an error so startup fails loudly instead of silently falling back to
// stderr while the operator tails an empty file.
//
// On success the first emitted entry is an info "logger ready" carrying the
// effective level/format/output — one boot line that says where the rest of
// the log went.
//
// cfg.Level is parsed via zapcore.ParseLevel; on failure the returned error
// wraps the underlying parse error so the caller can branch. This is
// defense-in-depth on top of Config.Validate — NewLogger does not assume
// the caller ran Validate.
func NewLogger(cfg LogConfig) (Logger, error) {
	// Re-validate level here even though Config.Validate covers it; this
	// guards callers that construct a LogConfig literal without going
	// through LoadConfig (notably tests).
	if _, err := zapcore.ParseLevel(cfg.Level); err != nil {
		return nil, fmt.Errorf("parse log level %q: %w", cfg.Level, err)
	}
	w, err := selectWriter(cfg.Output)
	if err != nil {
		return nil, fmt.Errorf("open log output %q: %w", cfg.Output, err)
	}
	var closer io.Closer
	if cfg.Output != "stdout" && cfg.Output != "stderr" && cfg.Output != "" {
		if f, ok := w.(*os.File); ok {
			closer = f
		}
	}
	logger := newLoggerWithWriter(w, cfg, closer)
	logger.Info("logger ready",
		F("level", cfg.Level),
		F("format", cfg.Format),
		F("output", cfg.Output),
	)
	return logger, nil
}

// NewNoOpLogger returns a Logger that discards every entry. It is the
// dependency-injection zero value (D-42): tests construct components with
// `infra.NewNoOpLogger()` so they do not need to import zap or capture
// output. It is also safe to use in production code paths that genuinely
// have nothing to log (rare; prefer explicit cfg.Level="error" for
// quiet-by-default production logging).
func NewNoOpLogger() Logger {
	return nopLogger{}
}

// newLoggerWithWriter is the test-injectable seam. Production callers go
// through NewLogger, which picks stderr/stdout based on cfg.Output; tests
// pass a *bytes.Buffer so they can inspect the emitted JSON directly. This
// function never returns an error — invalid level strings fall back to
// info-level with the assumption that Config.Validate (or NewLogger's
// pre-check) has already rejected them.
func newLoggerWithWriter(w io.Writer, cfg LogConfig, closer io.Closer) Logger {
	level := zapcore.InfoLevel
	if parsed, err := zapcore.ParseLevel(cfg.Level); err == nil {
		level = parsed
	}

	var encoder zapcore.Encoder
	if cfg.Format == "console" {
		encoder = zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
	} else {
		// Default to JSON for any non-console format including the empty
		// string; Config.Validate restricts the legal values, but tests
		// construct LogConfig literals so we cannot assume validation ran.
		encoder = zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	}

	core := zapcore.NewCore(encoder, zapcore.AddSync(w), level)
	// AddCallerSkip(1) is set so that if a caller later opts into caller
	// info via zap.AddCaller(), the reported caller is the infra.Logger
	// invocation site rather than this wrapper's delegation line.
	zl := zap.New(core, zap.AddCallerSkip(1))
	return &ZapLogger{l: zl, closer: closer}
}

// selectWriter resolves cfg.Output to an io.Writer following the contract
// documented in LogConfig.Output: "stderr" (or empty) → os.Stderr; "stdout"
// → os.Stdout; any other value is a file path, opened O_CREATE|O_APPEND
// (0644) so restarts keep prior history. File-open errors are returned, not
// swallowed: routing logs to a path the process cannot write must abort
// startup rather than quietly keep writing stderr (issue #94).
func selectWriter(output string) (io.Writer, error) {
	switch output {
	case "stdout":
		return os.Stdout, nil
	case "stderr", "":
		return os.Stderr, nil
	default:
		return os.OpenFile(output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	}
}

// Debug implements Logger.
func (z *ZapLogger) Debug(msg string, fields ...Field) {
	z.l.Debug(msg, toZapFields(fields)...)
}

// Info implements Logger.
func (z *ZapLogger) Info(msg string, fields ...Field) {
	z.l.Info(msg, toZapFields(fields)...)
}

// Warn implements Logger.
func (z *ZapLogger) Warn(msg string, fields ...Field) {
	z.l.Warn(msg, toZapFields(fields)...)
}

// Error implements Logger.
func (z *ZapLogger) Error(msg string, fields ...Field) {
	z.l.Error(msg, toZapFields(fields)...)
}

// With implements Logger. Returns a child ZapLogger whose underlying zap
// logger has the supplied fields baked in (so every subsequent entry
// includes them) and whose baseFields slice reflects the accumulated
// context. The receiver is not mutated.
func (z *ZapLogger) With(fields ...Field) Logger {
	zapFields := toZapFields(fields)
	accumulated := make([]Field, 0, len(z.baseFields)+len(fields))
	accumulated = append(accumulated, z.baseFields...)
	accumulated = append(accumulated, fields...)
	child := &ZapLogger{
		l:          z.l.With(zapFields...),
		baseFields: accumulated,
		closer:     z.closer,
	}
	return child
}

// Sync implements Logger.
func (z *ZapLogger) Sync() error {
	return z.l.Sync()
}

// Close releases file-backed log output when this logger owns the handle.
// Call Sync before Close on process exit. stderr/stdout loggers are no-ops.
func (z *ZapLogger) Close() error {
	if z.closer == nil {
		return nil
	}
	return z.closer.Close()
}

// Zap returns the underlying *zap.Logger for interop with packages (e.g.
// pkg/ws) whose API takes *zap.Logger directly. Callers logging through the
// infra.Logger interface keep T-01-09 Field sanitization; this accessor is
// ONLY for handing the configured, level-filtered logger to a
// *zap.Logger-typed dependency injection point. It does not bypass Field
// sanitization for infra's own log calls — those still go through the
// interface methods.
func (z *ZapLogger) Zap() *zap.Logger {
	return z.l
}

// toZapFields converts the package-local Field slice into the zap.Field
// slice zap's logger methods require. String values are sanitized to defend
// against log injection (T-01-09); non-string values pass through to
// zap.Any which encodes them via reflection.
func toZapFields(fields []Field) []zap.Field {
	if len(fields) == 0 {
		return nil
	}
	out := make([]zap.Field, 0, len(fields))
	for _, f := range fields {
		// zap.Any dispatches on the dynamic type for primitives; for
		// strings we route through zap.String directly so we can apply
		// sanitizeString before zap sees the value. This is the only
		// place sanitization happens — every severity method goes
		// through this path.
		if s, ok := f.Value.(string); ok {
			out = append(out, zap.String(f.Key, sanitizeString(s)))
			continue
		}
		out = append(out, zap.Any(f.Key, f.Value))
	}
	return out
}

// sanitizeString replaces raw newline, carriage-return, and tab bytes with
// their literal escape representations (e.g. 0x0A → "\\n", two characters).
// This is the T-01-09 mitigation: even though zap's JSON encoder escapes
// these bytes itself, applying our own sanitization makes the contract
// independent of the encoder's behavior and prevents log injection across
// any future output path.
//
// Non-string values bypass this function entirely; structures with
// string-typed fields rely on zap's reflection-based encoder for safe
// escaping.
func sanitizeString(s string) string {
	if !strings.ContainsAny(s, "\n\r\t") {
		return s
	}
	// strings.Replacer allocates only on Replace; the constructor is cheap.
	// Using NewReplacer instead of chained Replace avoids quadratic behavior
	// when all three control characters are present.
	r := strings.NewReplacer(
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return r.Replace(s)
}

// nopLogger is the silent Logger returned by NewNoOpLogger. Every method is
// a no-op. With returns the receiver unchanged because there is nothing to
// accumulate. This is the dependency-injection zero value (D-42).
type nopLogger struct{}

// ensure nopLogger satisfies Logger at compile time.
var _ Logger = nopLogger{}

// Debug implements Logger.
func (nopLogger) Debug(string, ...Field) {}

// Info implements Logger.
func (nopLogger) Info(string, ...Field) {}

// Warn implements Logger.
func (nopLogger) Warn(string, ...Field) {}

// Error implements Logger.
func (nopLogger) Error(string, ...Field) {}

// With implements Logger. Returns the receiver (nopLogger is a value type
// with no state) so callers can chain without allocation.
func (n nopLogger) With(...Field) Logger { return n }

// Sync implements Logger. Always returns nil.
func (nopLogger) Sync() error { return nil }
