package mimo

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// attemptField returns the value of key on a testLogger entry, or nil when
// the field is absent.
func attemptField(entry map[string]interface{}, key string) interface{} {
	fields, _ := entry["fields"].([]infra.Field)
	for _, f := range fields {
		if f.Key == key {
			return f.Value
		}
	}
	return nil
}

// findAttemptEntries returns every log entry with the given msg AND op field.
func findAttemptEntries(l *testLogger, msg, op string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, entry := range l.logs {
		if entry["msg"] != msg {
			continue
		}
		if got, _ := attemptField(entry, "op").(string); got == op {
			out = append(out, entry)
		}
	}
	return out
}

// hangHandler accepts the request and holds it until the caller's context
// dies (bounded, so a wedged test server cannot hang the suite).
func hangHandler(calls *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}
}

// TestChatAttemptLogsParentDeadlineAsTimeout pins issue #94 U2 on the real
// Chat method: a hanging upstream aborted by the CALLER's context deadline
// (shorter than the client Timeout) yields one start info and one
// error_class=timeout warn, attempt stays 0 (no second attempt is started),
// and neither the API key nor the request transcript leaks into any log line.
func TestChatAttemptLogsParentDeadlineAsTimeout(t *testing.T) {
	const (
		apiKey     = "secret-key-must-not-be-logged"
		transcript = "孩子的转写原文不该进日志"
	)
	var calls int32
	server, cleanup := mockTTSServer(hangHandler(&calls))
	defer cleanup()

	logger := newTestLogger()
	client, err := NewClient(ClientConfig{
		APIKey:     apiKey,
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 3,
		RetryDelay: time.Millisecond,
	}, logger)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = client.Chat(ctx, []Message{{Role: "user", Content: transcript}})
	if !errors.Is(err, ErrContextCancelled) {
		t.Fatalf("Chat error = %v, want ErrContextCancelled", err)
	}

	started := findAttemptEntries(logger, "upstream attempt started", opChat)
	if len(started) != 1 {
		t.Fatalf("expected exactly 1 start entry for op=chat, got %d (%+v)", len(started), logger.logs)
	}
	if got := attemptField(started[0], "attempt"); got != 0 {
		t.Errorf("start attempt = %v, want 0", got)
	}
	if got := attemptField(started[0], "level"); started[0]["level"] != "info" {
		t.Errorf("start level = %v, want info", got)
	}

	finished := findAttemptEntries(logger, "upstream attempt finished", opChat)
	if len(finished) != 1 {
		t.Fatalf("expected exactly 1 finish entry for op=chat, got %d (%+v)", len(finished), logger.logs)
	}
	fin := finished[0]
	if fin["level"] != "warn" {
		t.Errorf("finish level = %v, want warn (timeout)", fin["level"])
	}
	if got, _ := attemptField(fin, "error_class").(string); got != classTimeout {
		t.Errorf("finish error_class = %v, want %q", attemptField(fin, "error_class"), classTimeout)
	}
	if got, _ := attemptField(fin, "status").(int); got != 0 {
		t.Errorf("finish status = %v, want 0 (no response)", got)
	}

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("server saw %d requests, want 1 (parent-deadline failures must not retry)", n)
	}
	assertLogsOmit(t, logger, apiKey, transcript)
}

// TestChatAttemptLogsSuccess pins the happy path: exactly one start and one
// finish entry, finish at info with status=200, error_class=ok, and a
// duration_ms field present.
func TestChatAttemptLogsSuccess(t *testing.T) {
	server, cleanup := mockTTSServer(defaultChatHandler)
	defer cleanup()

	logger := newTestLogger()
	client, err := NewClient(ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 1,
		RetryDelay: time.Millisecond,
	}, logger)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	if _, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "好了"}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	started := findAttemptEntries(logger, "upstream attempt started", opChat)
	finished := findAttemptEntries(logger, "upstream attempt finished", opChat)
	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("expected 1 start + 1 finish for op=chat, got %d/%d (%+v)",
			len(started), len(finished), logger.logs)
	}
	fin := finished[0]
	if fin["level"] != "info" {
		t.Errorf("finish level = %v, want info", fin["level"])
	}
	if got, _ := attemptField(fin, "status").(int); got != 200 {
		t.Errorf("finish status = %v, want 200", got)
	}
	if got, _ := attemptField(fin, "error_class").(string); got != classOK {
		t.Errorf("finish error_class = %v, want %q", attemptField(fin, "error_class"), classOK)
	}
	if attemptField(fin, "duration_ms") == nil {
		t.Errorf("finish entry has no duration_ms field: %+v", fin)
	}
}

// TestChatAttemptLogCarriesContextIDs pins the ctx propagation: ids placed
// via ContextWithLogIDs appear on BOTH entries; without them the
// session_id/turn_id fields are empty strings, not missing keys.
func TestChatAttemptLogCarriesContextIDs(t *testing.T) {
	server, cleanup := mockTTSServer(defaultChatHandler)
	defer cleanup()

	logger := newTestLogger()
	client, err := NewClient(ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 0,
	}, logger)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	if _, err := client.Chat(ContextWithLogIDs(context.Background(), "sess-1", "turn-9"),
		[]Message{{Role: "user", Content: "好了"}}); err != nil {
		t.Fatalf("Chat with ids: %v", err)
	}
	for _, msg := range []string{"upstream attempt started", "upstream attempt finished"} {
		if got := loggedStringField(t, logger, msg, "session_id"); got != "sess-1" {
			t.Errorf("%s session_id = %q, want sess-1", msg, got)
		}
		if got := loggedStringField(t, logger, msg, "turn_id"); got != "turn-9" {
			t.Errorf("%s turn_id = %q, want turn-9", msg, got)
		}
	}

	// Drop the first call's entries so findLog cannot match them when
	// asserting the "no ids" shape (empty string, not a missing key).
	logger.logs = nil
	if _, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "好了"}}); err != nil {
		t.Fatalf("Chat without ids: %v", err)
	}
	for _, msg := range []string{"upstream attempt started", "upstream attempt finished"} {
		if got := loggedStringField(t, logger, msg, "session_id"); got != "" {
			t.Errorf("%s session_id = %q, want empty string", msg, got)
		}
	}
}

// TestTranscribeAttemptLogsParentDeadlineAsTimeout locks the same timeout
// contract on the ASR entrypoint (Transcribe); TTS and vision share the
// identical loop helper.
func TestTranscribeAttemptLogsParentDeadlineAsTimeout(t *testing.T) {
	var calls int32
	server, cleanup := mockTTSServer(hangHandler(&calls))
	defer cleanup()

	logger := newTestLogger()
	client, err := NewClient(ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 3,
		RetryDelay: time.Millisecond,
	}, logger)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	audio := bytes.Repeat([]byte{0x00, 0x01}, 320)
	_, err = client.Transcribe(ctx, audio, "pcm16")
	if !errors.Is(err, ErrContextCancelled) {
		t.Fatalf("Transcribe error = %v, want ErrContextCancelled", err)
	}

	if got := len(findAttemptEntries(logger, "upstream attempt started", opASR)); got != 1 {
		t.Fatalf("expected 1 asr start entry, got %d", got)
	}
	finished := findAttemptEntries(logger, "upstream attempt finished", opASR)
	if len(finished) != 1 {
		t.Fatalf("expected 1 asr finish entry, got %d", len(finished))
	}
	if finished[0]["level"] != "warn" {
		t.Errorf("asr finish level = %v, want warn", finished[0]["level"])
	}
	if got, _ := attemptField(finished[0], "error_class").(string); got != classTimeout {
		t.Errorf("asr finish error_class = %v, want %q", attemptField(finished[0], "error_class"), classTimeout)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}

// flushOKHangBodyHandler sends HTTP 200 headers then blocks on the body so the
// client times out while reading (not while dialing).
func flushOKHangBodyHandler(calls *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}
}

// TestChatAttemptLogsBodyReadTimeoutAfterHeaders pins Codex P2 #2: after the
// upstream flushes HTTP 200, a client Timeout on body read is error_class
// timeout at warn level with status=200, not decode/error.
func TestChatAttemptLogsBodyReadTimeoutAfterHeaders(t *testing.T) {
	var calls int32
	server, cleanup := mockTTSServer(flushOKHangBodyHandler(&calls))
	defer cleanup()

	logger := newTestLogger()
	client, err := NewClient(ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    150 * time.Millisecond,
		MaxRetries: 0,
		RetryDelay: time.Millisecond,
	}, logger)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	c := client.(*mimoClient)
	c.config.MaxRetries = 0

	_, err = client.Chat(context.Background(), []Message{{Role: "user", Content: "你好"}})
	if err == nil {
		t.Fatal("Chat: want timeout error")
	}

	finished := findAttemptEntries(logger, "upstream attempt finished", opChat)
	if len(finished) != 1 {
		t.Fatalf("expected 1 finish entry, got %d (%+v)", len(finished), logger.logs)
	}
	fin := finished[0]
	if fin["level"] != "warn" {
		t.Errorf("finish level = %v, want warn (body read timeout)", fin["level"])
	}
	if got, _ := attemptField(fin, "error_class").(string); got != classTimeout {
		t.Errorf("error_class = %v, want %q", got, classTimeout)
	}
	if got, _ := attemptField(fin, "status").(int); got != 200 {
		t.Errorf("status = %v, want 200 (headers received)", got)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("server saw %d requests, want 1 (no retry on timeout)", n)
	}
}

// TestChatAttemptLogsEncodingFailureStatusZero pins Codex P2 #3: JSON encode
// failures never send a request; logged status must be 0, not 200.
func TestChatAttemptLogsEncodingFailureStatusZero(t *testing.T) {
	server, cleanup := mockTTSServer(defaultChatHandler)
	defer cleanup()

	logger := newTestLogger()
	client, err := NewClient(ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 0,
	}, logger)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	_, err = client.Chat(context.Background(), []Message{{Role: "user", Content: "x"}},
		WithTemperature(math.NaN()))
	if !errors.Is(err, ErrEncodingFailed) {
		t.Fatalf("Chat error = %v, want ErrEncodingFailed", err)
	}

	finished := findAttemptEntries(logger, "upstream attempt finished", opChat)
	if len(finished) != 1 {
		t.Fatalf("expected 1 finish entry, got %d", len(finished))
	}
	if got, _ := attemptField(finished[0], "status").(int); got != 0 {
		t.Errorf("status = %v, want 0 (no HTTP response)", got)
	}
	if got, _ := attemptField(finished[0], "error_class").(string); got != classDecode {
		t.Errorf("error_class = %v, want %q", got, classDecode)
	}
}

// flush503HangBodyHandler returns 503 then blocks on the error body read.
func flush503HangBodyHandler(calls *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}
}

// TestChatAttemptLogsHTTPErrorStatusWhenBodyReadTimesOut pins Codex P2 #3:
// when status 503 was received but reading the error body times out, the log
// still records status=503.
func TestChatAttemptLogsHTTPErrorStatusWhenBodyReadTimesOut(t *testing.T) {
	var calls int32
	server, cleanup := mockTTSServer(flush503HangBodyHandler(&calls))
	defer cleanup()

	logger := newTestLogger()
	client, err := NewClient(ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    150 * time.Millisecond,
		MaxRetries: 0,
	}, logger)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	c := client.(*mimoClient)
	c.config.MaxRetries = 0

	_, err = client.Chat(context.Background(), []Message{{Role: "user", Content: "你好"}})
	if err == nil {
		t.Fatal("Chat: want error")
	}

	finished := findAttemptEntries(logger, "upstream attempt finished", opChat)
	if len(finished) != 1 {
		t.Fatalf("expected 1 finish entry, got %d", len(finished))
	}
	if got, _ := attemptField(finished[0], "status").(int); got != http.StatusServiceUnavailable {
		t.Errorf("status = %v, want %d", got, http.StatusServiceUnavailable)
	}
}
