package infra

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeLoggerState is the shared mutable record buffer that every fakeLogger
// derived from the same root writes into. Sharing the buffer between parent
// and child loggers means a test can pass `fake.With(F("request_id",...))`
// into RecoveryMiddleware and still observe the recovered-panic entry via
// the original fake reference (the test's seam).
type fakeLoggerState struct {
	msgs []loggedMsg
}

// loggedMsg is the decoded form of one Logger call. Fields is the fully
// accumulated context — parent's With-fields followed by the call-site
// fields — so tests can assert on context propagation (D-25).
type loggedMsg struct {
	Level  string
	Msg    string
	Fields []Field
}

// fakeLogger is the manual fake (D-48) used by middleware tests. It does
// not pull in any mocking framework; it records every call into a slice
// the test can inspect directly.
type fakeLogger struct {
	state  *fakeLoggerState
	fields []Field
}

// newFakeLogger returns a fakeLogger with its own fresh record buffer.
func newFakeLogger() *fakeLogger {
	return &fakeLogger{state: &fakeLoggerState{}}
}

// record appends a captured entry with the receiver's accumulated context
// fields prepended to the per-call fields. The slice copies prevent aliasing
// when later With(...) calls mutate the receiver's fields slice.
func (f *fakeLogger) record(level, msg string, fs []Field) {
	combined := append([]Field{}, f.fields...)
	combined = append(combined, fs...)
	f.state.msgs = append(f.state.msgs, loggedMsg{Level: level, Msg: msg, Fields: combined})
}

func (f *fakeLogger) Debug(m string, fs ...Field) { f.record("debug", m, fs) }
func (f *fakeLogger) Info(m string, fs ...Field)  { f.record("info", m, fs) }
func (f *fakeLogger) Warn(m string, fs ...Field)  { f.record("warn", m, fs) }
func (f *fakeLogger) Error(m string, fs ...Field) { f.record("error", m, fs) }

// With returns a child fakeLogger that shares the parent's record buffer
// and inherits its accumulated context fields. This mirrors ZapLogger.With's
// semantics so RecoveryMiddleware's caller code is agnostic to which
// implementation it received.
func (f *fakeLogger) With(fs ...Field) Logger {
	child := &fakeLogger{state: f.state}
	child.fields = append(append([]Field{}, f.fields...), fs...)
	return child
}

func (f *fakeLogger) Sync() error { return nil }

// findField returns the first Field with the given key in the slice and a
// boolean indicating presence. Used by middleware tests to assert that a
// specific context field (e.g. recovered_panic, request_id) was emitted.
func findField(fields []Field, key string) (any, bool) {
	for _, f := range fields {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

// TestRecoveryMiddlewareHandlesPanic proves D-49: a panic inside the wrapped
// handler does not escape to the test (or, in production, to net/http's
// ServeMux which would crash the process). The middleware recovers and
// writes HTTP 500.
func TestRecoveryMiddlewareHandlesPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped RecoveryMiddleware: %v", r)
		}
	}()

	logger := newFakeLogger()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})
	wrapped := RecoveryMiddleware(logger, next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// TestRecoveryMiddlewareLogsPanic proves the recovered-panic event is
// emitted at Error level with msg "recovered panic" and the three required
// fields: recovered_panic=true, error=<panic value>, stack=<runtime stack>.
func TestRecoveryMiddlewareLogsPanic(t *testing.T) {
	logger := newFakeLogger()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	})
	wrapped := RecoveryMiddleware(logger, next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	wrapped.ServeHTTP(rec, req)

	var recovered *loggedMsg
	for i := range logger.state.msgs {
		m := &logger.state.msgs[i]
		if m.Level == "error" && m.Msg == "recovered panic" {
			recovered = m
			break
		}
	}
	if recovered == nil {
		t.Fatalf("no 'recovered panic' error entry emitted; msgs=%+v", logger.state.msgs)
	}

	rp, ok := findField(recovered.Fields, "recovered_panic")
	if !ok {
		t.Errorf("recovered_panic field missing: %+v", recovered.Fields)
	} else if rp != true {
		t.Errorf("recovered_panic: got %v, want true", rp)
	}

	errVal, ok := findField(recovered.Fields, "error")
	if !ok {
		t.Errorf("error field missing: %+v", recovered.Fields)
	} else if errVal != "kaboom" {
		t.Errorf("error: got %v, want 'kaboom'", errVal)
	}

	stackVal, ok := findField(recovered.Fields, "stack")
	if !ok {
		t.Errorf("stack field missing: %+v", recovered.Fields)
	} else if s, isStr := stackVal.(string); !isStr || s == "" {
		t.Errorf("stack should be a non-empty string, got %T (%v)", stackVal, stackVal)
	}
}

// TestRecoveryMiddlewarePreservesHealthyHandler proves the middleware is
// transparent when no panic occurs: the handler's status code, body, and
// side effects all pass through, and no spurious log entry is emitted.
func TestRecoveryMiddlewarePreservesHealthyHandler(t *testing.T) {
	logger := newFakeLogger()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	wrapped := RecoveryMiddleware(logger, next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "ok"; got != want {
		t.Errorf("body: got %q, want %q", got, want)
	}
	if len(logger.state.msgs) != 0 {
		t.Errorf("healthy handler should not produce log entries; got %+v", logger.state.msgs)
	}
}

// TestRecoveryMiddlewareEmits5xxNot200 is a focused regression test for the
// exact-status contract: a recovered panic MUST return 500, not 200. The
// information-disclosure threat T-01-10 relies on this — a 200 response
// would lead clients to believe nothing went wrong.
func TestRecoveryMiddlewareEmits5xxNot200(t *testing.T) {
	logger := newFakeLogger()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("explode")
	})
	wrapped := RecoveryMiddleware(logger, next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want exactly %d", rec.Code, http.StatusInternalServerError)
	}
	if rec.Code == http.StatusOK {
		t.Errorf("status must NOT be 200 on a recovered panic")
	}
}

// TestRecoveryMiddlewareBodyDoesNotLeakPanic verifies T-01-10 mitigation:
// the response body for a recovered panic is a generic "internal server
// error" string, never the panic value or any part of the stack trace.
// Echoing the panic value would leak internal state to clients.
func TestRecoveryMiddlewareBodyDoesNotLeakPanic(t *testing.T) {
	logger := newFakeLogger()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret-internal-token-42")
	})
	wrapped := RecoveryMiddleware(logger, next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	wrapped.ServeHTTP(rec, req)

	body := rec.Body.String()
	if containsSubstr(body, "secret-internal-token-42") {
		t.Errorf("response body leaked panic value: %q", body)
	}
	if containsSubstr(body, "goroutine") {
		t.Errorf("response body leaked stack trace keyword 'goroutine': %q", body)
	}
}

// TestRecoveryMiddlewareIncludesRequestID proves D-25 context propagation:
// when the caller pre-populates the logger with a request_id field via
// With(...), the recovered-panic event inherits that field. This is what
// lets operators correlate a 500 response to the originating request.
func TestRecoveryMiddlewareIncludesRequestID(t *testing.T) {
	baseLogger := newFakeLogger()
	childLogger := baseLogger.With(F("request_id", "r-abc"))

	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("bang")
	})
	wrapped := RecoveryMiddleware(childLogger, next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	wrapped.ServeHTTP(rec, req)

	if len(baseLogger.state.msgs) != 1 {
		t.Fatalf("expected 1 log entry on shared fake state, got %d: %+v",
			len(baseLogger.state.msgs), baseLogger.state.msgs)
	}
	entry := baseLogger.state.msgs[0]

	rid, ok := findField(entry.Fields, "request_id")
	if !ok {
		t.Errorf("request_id field missing from recovered event: %+v", entry.Fields)
	} else if rid != "r-abc" {
		t.Errorf("request_id: got %v, want 'r-abc'", rid)
	}

	// The recovered_panic field must STILL be present alongside the
	// inherited request_id — context propagation must not displace the
	// recovery-specific fields.
	if rp, ok := findField(entry.Fields, "recovered_panic"); !ok || rp != true {
		t.Errorf("recovered_panic=true missing or wrong alongside request_id: %+v", entry.Fields)
	}
}

// containsSubstr is a tiny strings.Contains shim kept local to avoid pulling
// in the strings package on top of net/http and testing.
func containsSubstr(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
