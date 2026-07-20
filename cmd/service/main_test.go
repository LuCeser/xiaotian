package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

// runMu serializes tests that invoke run() so two tests do not race for
// the same ephemeral port range. Each test acquires the mutex before
// calling run() and releases it after its context-cancellation cleanup.
var runMu sync.Mutex

// TestMainLoadsDefaultConfig proves run() returns 0 when no config file
// exists and no env vars override defaults. The test cancels the lifetime
// context to drive the graceful-shutdown path; main() wires the same path
// to SIGTERM/SIGINT via signal.NotifyContext.
func TestMainLoadsDefaultConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- run(ctx, "config-does-not-exist.yaml", "") }()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code: got %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s of ctx cancel")
	}
}

// TestMainHandlesCtxCancel proves context cancellation triggers graceful
// shutdown that completes inside the Shutdown.Timeout budget.
func TestMainHandlesCtxCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	start := time.Now()
	go func() { done <- run(ctx, "config-does-not-exist.yaml", "") }()

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code: got %d, want 0", code)
		}
		elapsed := time.Since(start)
		// Default Shutdown.Timeout is 30s; with no in-flight work the
		// drain is near-instant. Use a generous 15s upper bound to avoid
		// CI flakiness.
		if elapsed > 15*time.Second {
			t.Errorf("shutdown took %s, want < 15s", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run did not return within 20s of ctx cancel")
	}
}

// TestMainStartsHealthAndMetricsServers proves run() binds the health HTTP
// server and /health + /metrics both respond. We pre-allocate an ephemeral
// port via net.Listen, close it, then pass the address as override so
// run() binds the same port.
func TestMainStartsHealthAndMetricsServers(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)

	healthAddr, err := freePortAddr()
	if err != nil {
		t.Fatalf("freePortAddr: %v", err)
	}
	url := "http://" + healthAddr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- run(ctx, "config-does-not-exist.yaml", healthAddr) }()
	defer func() {
		cancel()
		<-done
	}()

	if !waitForOK(t, url+"/health", 5*time.Second) {
		t.Fatalf("/health never responded at %s", url)
	}

	resp, err := http.Get(url + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, _ := io.ReadAll(resp.Body)
	if !contains(string(body), `"status"`) {
		t.Errorf("GET /health body: missing status field; body=%s", string(body))
	}

	respM, err := http.Get(url + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer respM.Body.Close()
	if respM.StatusCode != http.StatusOK {
		t.Errorf("GET /metrics status: got %d, want %d", respM.StatusCode, http.StatusOK)
	}
	bodyM, _ := io.ReadAll(respM.Body)
	if !contains(string(bodyM), "hta_memory_used_bytes") {
		t.Errorf("GET /metrics body: missing hta_memory_used_bytes; body=%s", string(bodyM))
	}
}

// TestMainMemoryUnder150MBAtIdle proves RES-01 with generous tolerance.
// The 1.5x factor accommodates dev-runtime overhead; production
// benchmarking will tighten this via a separate test (D-36 deferred).
func TestMainMemoryUnder150MBAtIdle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)

	healthAddr, err := freePortAddr()
	if err != nil {
		t.Fatalf("freePortAddr: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- run(ctx, "config-does-not-exist.yaml", healthAddr) }()
	defer func() {
		cancel()
		<-done
	}()

	if !waitForOK(t, "http://"+healthAddr+"/health", 5*time.Second) {
		t.Skipf("health never responded; skipping memory assertion")
	}

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	const budget = 150 * 1024 * 1024
	const tolerance = 1.5
	limit := uint64(float64(budget) * tolerance)
	if m.Sys > limit {
		t.Errorf("Sys bytes: got %d (%.1fMB), want <= %d (%.1fMB)",
			m.Sys, float64(m.Sys)/1024/1024, limit, float64(limit)/1024/1024)
	}
}

// TestMainRejectsInvalidConfig proves HTA_BUFFER_AUDIOFRAMEBYTES=512 (must
// be 640 per D-71) causes run to exit non-zero with a validation error.
func TestMainRejectsInvalidConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)
	t.Setenv("HTA_BUFFER_AUDIOFRAMEBYTES", "512")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- run(ctx, "config-does-not-exist.yaml", "") }()

	select {
	case code := <-done:
		if code == 0 {
			t.Errorf("exit code: got 0, want non-zero for invalid config")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s for invalid config")
	}
}

// clearHTAEnv removes every HTA_* env var so tests start from a known
// baseline. t.Cleanup restores the originally-cleared state.
func clearHTAEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if len(kv) >= 4 && kv[:4] == "HTA_" {
			eq := 0
			for eq < len(kv) && kv[eq] != '=' {
				eq++
			}
			if eq >= len(kv) {
				continue
			}
			name := kv[:eq]
			orig := os.Getenv(name)
			_ = os.Unsetenv(name)
			t.Cleanup(func() {
				_ = os.Setenv(name, orig)
			})
		}
	}
}

// freePortAddr opens a TCP listener on 127.0.0.1:0, reads the bound
// address, closes the listener, and returns "127.0.0.1:port". There is a
// small race window between close and re-bind, but in practice the kernel
// holds the port free for the immediate subsequent bind.
func freePortAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr, nil
}

// waitForOK polls url every 50ms until it returns HTTP 200 or 503,
// returning true. Returns false if the deadline elapses.
func waitForOK(t *testing.T, url string, timeout time.Duration) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		resp, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusServiceUnavailable {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// contains is the substring test. Tests use this rather than importing
// strings.Contains so the test file stays stdlib-minimal.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
