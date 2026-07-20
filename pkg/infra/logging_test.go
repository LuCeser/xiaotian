package infra

import (
	"bytes"
	"encoding/json"
	"testing"
)

// parseLogLines splits a newline-delimited buffer of JSON log entries into
// decoded maps. Used by every test in this file to assert on the structured
// output of ZapLogger. Fails the test if any line is not valid JSON.
func parseLogLines(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return out
	}
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("invalid JSON log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// TestStructuredLogging proves ERR-04 (VALIDATION.md contract): a Logger.Info
// call with a single Field produces one JSON line carrying keys "msg",
// "level", "ts", plus the caller-supplied field key/value. The literal test
// name is referenced by 01-VALIDATION.md Per-Task Verification Map and MUST
// resolve to this function.
func TestStructuredLogging(t *testing.T) {
	var buf bytes.Buffer
	logger := newLoggerWithWriter(&buf, LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stderr",
	})
	logger.Info("hello", Field{Key: "user", Value: "alice"})
	if err := logger.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	lines := parseLogLines(t, buf.Bytes())
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 log line, got %d (%q)", len(lines), buf.String())
	}
	line := lines[0]

	if line["msg"] != "hello" {
		t.Errorf("msg field: got %v, want %q", line["msg"], "hello")
	}
	if line["level"] != "info" {
		t.Errorf("level field: got %v, want %q", line["level"], "info")
	}
	ts, ok := line["ts"]
	if !ok {
		t.Errorf("ts key missing from log line: %v", line)
	} else if _, isNum := ts.(float64); !isNum {
		t.Errorf("ts should be a JSON number, got %T (%v)", ts, ts)
	}
	if line["user"] != "alice" {
		t.Errorf("user field: got %v, want %q", line["user"], "alice")
	}
}

// TestLogLevels verifies each of Debug/Info/Warn/Error emits the correct
// "level" field value when format is json (production encoder → lowercase).
func TestLogLevels(t *testing.T) {
	tests := []struct {
		name string
		emit func(l Logger, msg string)
		want string
	}{
		{"debug", func(l Logger, m string) { l.Debug(m) }, "debug"},
		{"info", func(l Logger, m string) { l.Info(m) }, "info"},
		{"warn", func(l Logger, m string) { l.Warn(m) }, "warn"},
		{"error", func(l Logger, m string) { l.Error(m) }, "error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := newLoggerWithWriter(&buf, LogConfig{
				Level:  "debug",
				Format: "json",
				Output: "stderr",
			})
			tc.emit(logger, "body")
			_ = logger.Sync()

			lines := parseLogLines(t, buf.Bytes())
			if len(lines) != 1 {
				t.Fatalf("expected exactly 1 log line, got %d (%q)", len(lines), buf.String())
			}
			if got := lines[0]["level"]; got != tc.want {
				t.Errorf("level: got %v, want %q", got, tc.want)
			}
			if got := lines[0]["msg"]; got != "body" {
				t.Errorf("msg: got %v, want %q", got, "body")
			}
		})
	}
}

// TestContextFieldPropagation proves D-25: a child logger created via With
// inherits its parent's context fields, and a grandchild inherits from BOTH.
// This is what lets request_id flow from the request entrypoint all the way
// into a recovered-panic event emitted deep in the handler chain.
func TestContextFieldPropagation(t *testing.T) {
	var buf bytes.Buffer
	base := newLoggerWithWriter(&buf, LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stderr",
	})

	child := base.With(F("request_id", "r-1"))
	child.Info("first")

	grandchild := child.With(F("op", "fetch"))
	grandchild.Info("second")

	_ = base.Sync()

	lines := parseLogLines(t, buf.Bytes())
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d (%q)", len(lines), buf.String())
	}

	if got := lines[0]["request_id"]; got != "r-1" {
		t.Errorf("line 0 request_id: got %v, want %q", got, "r-1")
	}
	if _, present := lines[0]["op"]; present {
		t.Errorf("line 0 op should be absent (not yet added), got %v", lines[0]["op"])
	}

	if got := lines[1]["request_id"]; got != "r-1" {
		t.Errorf("line 1 request_id: got %v, want %q (must inherit from parent)", got, "r-1")
	}
	if got := lines[1]["op"]; got != "fetch" {
		t.Errorf("line 1 op: got %v, want %q", got, "fetch")
	}
}

// TestFieldSanitization proves T-01-09 mitigation: a field value containing
// raw newline and carriage-return bytes is emitted without leaking those raw
// control characters into the log stream. zap's JSON encoder escapes \n and
// \r inside string values; sanitizeString adds a defense-in-depth layer that
// runs BEFORE zap sees the value. Either way, the contract is "no raw \n / \r
// bytes appear inside the JSON payload of a single log entry". zap appends
// exactly one trailing line separator per entry, so the expected raw \n
// count for one log call is 1, and the expected raw \r count is 0.
func TestFieldSanitization(t *testing.T) {
	var buf bytes.Buffer
	logger := newLoggerWithWriter(&buf, LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stderr",
	})
	malicious := "line1\nline2\rinjected"
	logger.Info("sanitized", F("payload", malicious))
	_ = logger.Sync()

	out := buf.Bytes()
	newlineCount := bytes.Count(out, []byte{'\n'})
	crCount := bytes.Count(out, []byte{'\r'})

	if newlineCount != 1 {
		t.Errorf("expected exactly 1 trailing newline, got %d — raw newline leaked into JSON value (log injection): %q",
			newlineCount, out)
	}
	if crCount != 0 {
		t.Errorf("expected 0 raw CR bytes, got %d — carriage return leaked into JSON value: %q",
			crCount, out)
	}

	// Additionally, the parsed payload value must equal the input (round-trip
	// through JSON decode): sanitization escapes the bytes for transport, it
	// does not mutate the logical value.
	lines := parseLogLines(t, out)
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}
	if got := lines[0]["payload"]; got != malicious {
		t.Errorf("payload round-trip: got %v, want %q", got, malicious)
	}
}

// TestNoOpLogger proves the test-only NoOpLogger is panic-free and produces
// no output. It is the dependency-injection zero value (D-42) used by every
// downstream package's tests (and by cmd/service/main_test.go per Plan 08)
// so tests do not have to pull in zap.
func TestNoOpLogger(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NoOpLogger panicked: %v", r)
		}
	}()

	logger := NewNoOpLogger()
	logger.Debug("d", F("k", "v"))
	logger.Info("i", F("k", "v"))
	logger.Warn("w", F("k", "v"))
	logger.Error("e", F("k", "v"))

	child := logger.With(F("ctx", "request-id"))
	child.Info("child-msg")
	child.With(F("more", "x")).Error("grandchild-msg")

	if err := logger.Sync(); err != nil {
		t.Errorf("NoOpLogger.Sync returned non-nil error: %v", err)
	}
}

// TestLevelFiltering proves D-34 configurability: a logger built with level
// "warn" silently drops Debug and Info calls while admitting Warn and Error.
func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := newLoggerWithWriter(&buf, LogConfig{
		Level:  "warn",
		Format: "json",
		Output: "stderr",
	})

	logger.Debug("debug-should-drop")
	logger.Info("info-should-drop")
	logger.Warn("warn-passes")
	logger.Error("error-passes")
	_ = logger.Sync()

	lines := parseLogLines(t, buf.Bytes())
	if len(lines) != 2 {
		t.Fatalf("expected exactly 2 log lines (warn + error), got %d (%q)",
			len(lines), buf.String())
	}
	for i, l := range lines {
		level, _ := l["level"].(string)
		if level == "debug" || level == "info" {
			t.Errorf("line %d level %q leaked through warn filter: %v", i, level, l)
		}
	}
	// Sanity-check the two surviving levels.
	gotLevels := []string{
		lines[0]["level"].(string),
		lines[1]["level"].(string),
	}
	wantLevels := []string{"warn", "error"}
	for i := range wantLevels {
		if gotLevels[i] != wantLevels[i] {
			t.Errorf("line %d level: got %q, want %q", i, gotLevels[i], wantLevels[i])
		}
	}
}
