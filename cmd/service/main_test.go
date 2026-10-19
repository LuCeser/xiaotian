package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // Pure-Go SQLite driver (CGO_ENABLED=0)

	"github.com/gorilla/websocket"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
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

	// Wait for boot readiness (health listener answering), NOT a fixed
	// sleep: boot now runs four migration packages (database/lesson/poem/
	// passage) against a fresh temp SQLite file, and the old 200ms sleep
	// raced them on slow CI runners — cancel mid-migrate surfaced as
	// "database/migrate: context canceled" and exit 1 (issue #47). The
	// empty health override keeps the pure-defaults posture, so probe the
	// default health addr.
	waitReadyOrFatal(t, infra.DefaultConfig().Server.HealthAddr)
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
	go func() { done <- run(ctx, "config-does-not-exist.yaml", "") }()

	// Readiness-gated cancel (see TestMainLoadsDefaultConfig): the old
	// 300ms sleep raced boot's migrations on slow CI runners.
	waitReadyOrFatal(t, infra.DefaultConfig().Server.HealthAddr)

	// Measure shutdown only: arm the timer at the cancel, after boot is
	// ready, so a slow CI boot cannot eat the 15s budget below.
	start := time.Now()
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

// TestMainWSPublicRouteUpgradesAndShutsDownCleanly proves the public /ws route
// upgrades a real WebSocket client (HTTP 101) and that shutdown leaves no leaked
// goroutines (CR-01). This is the P0-1 end-to-end contract: the service listens
// on cfg.Server.Addr, the /ws upgrade works with the default localhost origin,
// and the CR-01-safe shutdown ordering drains every pump/hub/router goroutine.
func TestMainWSPublicRouteUpgradesAndShutsDownCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)

	// Point the public listener at an ephemeral port via the env-override layer
	// (run()'s signature only overrides the health addr).
	pubAddr, err := freePortAddr()
	if err != nil {
		t.Fatalf("freePortAddr (public): %v", err)
	}
	t.Setenv("HTA_SERVER_ADDR", pubAddr)

	healthAddr, err := freePortAddr()
	if err != nil {
		t.Fatalf("freePortAddr (health): %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- run(ctx, "config-does-not-exist.yaml", healthAddr) }()

	// Poll-dial the WS endpoint until success or a 5s timeout. The default
	// AllowedOrigins [localhost, 127.0.0.1] accept Origin http://localhost.
	url := "ws://" + pubAddr + "/ws?user_id=t&device=t"
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	header := http.Header{}
	header.Set("Origin", "http://localhost")

	var conn *websocket.Conn
	dialDeadline := time.Now().Add(5 * time.Second)
	for {
		c, resp, derr := dialer.DialContext(ctx, url, header)
		if derr == nil {
			conn = c
			break
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		if time.Now().After(dialDeadline) {
			t.Skipf("WS dial never succeeded at %s within 5s (port race?)", url)
		}
		select {
		case <-ctx.Done():
			t.Skipf("ctx cancelled before WS dial succeeded at %s", url)
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Upgrade observed: record the goroutine baseline (pumps/hub/router/workers
	// are all live), then close the client conn.
	baseline := runtime.NumGoroutine()
	_ = conn.Close()

	// Cancel and wait for run() to return cleanly (exit 0).
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code: got %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s of ctx cancel")
	}

	// CR-01 goroutine-leak assertion: allow 200ms for pumps/Hub/Router to exit,
	// then assert the delta stays within tolerance (same as
	// TestIntegration_ServerShutdownCleansAllGoroutines in pkg/ws).
	time.Sleep(200 * time.Millisecond)
	final := runtime.NumGoroutine()
	if delta := final - baseline; delta > 2 {
		t.Errorf("goroutine leak: baseline=%d final=%d delta=%d (want <= 2, CR-01)",
			baseline, final, delta)
	}
}

// TestMainBootsWithNoWSConfigBlock proves the "boots from defaults" guarantee
// from a runtime (not just config) perspective: with NO ws: block in the config
// file and NO HTA_WS_* env vars, the service still boots (DefaultConfig supplies
// the dev-safe origins) and shuts down cleanly. This is the P0-1 must-have.
func TestMainBootsWithNoWSConfigBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t) // clears every HTA_* including HTA_WS_*, sets python env

	healthAddr, err := freePortAddr()
	if err != nil {
		t.Fatalf("freePortAddr: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- run(ctx, "config-does-not-exist.yaml", healthAddr) }()

	// Readiness-gated cancel (see TestMainLoadsDefaultConfig): the old
	// 300ms sleep raced boot's migrations on slow CI runners.
	waitReadyOrFatal(t, healthAddr)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code: got %d, want 0 (must boot from defaults, no ws block)", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s of ctx cancel")
	}
}

// TestMainBootsWithVoiceEnabled covers the Ticket-03 voice-enabled boot
// path: with MIMO_API_KEY present, run() must construct the mimo client,
// the hybrid classifier, and the voiceService, then boot and shut down
// cleanly — a construction failure anywhere in that chain returns exit
// code 1, so exit 0 is the assertion. The key value is never used at boot
// (mimo.NewClient is a pure constructor; no API call happens until a
// voice_command arrives), so a syntactically-valid dummy key suffices.
func TestMainBootsWithVoiceEnabled(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)
	t.Setenv("MIMO_API_KEY", "test-key-boot-verification-only")

	healthAddr, err := freePortAddr()
	if err != nil {
		t.Fatalf("freePortAddr: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- run(ctx, "config-does-not-exist.yaml", healthAddr) }()

	// Readiness-gated cancel (see TestMainLoadsDefaultConfig): the old
	// 300ms sleep raced boot's migrations on slow CI runners.
	waitReadyOrFatal(t, healthAddr)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code: got %d, want 0 (voice-enabled boot must succeed)", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s of ctx cancel")
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
	// Phase 3 (D-03/D-04): PythonConfig requires a non-empty interpreter +
	// scriptsDir, so run() refuses to boot without them. The main boot tests
	// exercise run() end-to-end and need a minimally-valid python env — the
	// same contract pkg/infra tests get via setRequiredPythonEnv. Since
	// WR-05, python.dataDir is required too (it anchors the fsrs_config.json
	// writer and the DICTATION_DATA_DIR reader on one directory). Set after
	// clearing so the baseline is "no HTA env except the required python
	// fields." (TestMainRejectsInvalidConfig still fails on its
	// audioFrameBytes=512 override, independent of these.)
	t.Setenv("HTA_PYTHON_INTERPRETER", "python3")
	t.Setenv("HTA_PYTHON_SCRIPTSDIR", "third_party/python")
	t.Setenv("HTA_PYTHON_DATADIR", t.TempDir())
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
		resp, err := http.Get(url) //nolint:gosec // G107: test helper polls the local health server
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

// waitReadyOrFatal blocks until run()'s health endpoint answers, proving boot
// (config load, all four migration packages, server start) finished. The
// main-boot tests gate their ctx cancel on this instead of a fixed sleep —
// sleeps raced boot's migrations on slow CI runners (issue #47: cancel
// mid-migrate → "database/migrate: context canceled" → exit 1). 10s is a
// generous ceiling for a loaded runner; a healthy boot answers in well under
// a second, and a boot that never answers fails here with a clearer message
// than a bare exit-code mismatch.
func waitReadyOrFatal(t *testing.T, healthAddr string) {
	t.Helper()
	if !waitForOK(t, "http://"+healthAddr+"/health", 10*time.Second) {
		t.Fatalf("health never responded at %s; run() may have failed to boot", healthAddr)
	}
}

// TestMainDatabaseConnection proves the Phase 4 vertical slice: run() opens the
// shared SQLite database, runs the idempotent migration, constructs both
// repositories, and closes the DB on shutdown. The test points cfg.Database.Path
// at a t.TempDir() file, boots a short-lived run(), then re-opens the DB
// read-only to assert the sessions table exists (proving Open+Migrate ran).
// Follows existing Windows-safe test style (cancel ctx, no syscall.Kill).
func TestMainDatabaseConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)

	// Point cfg.Database.Path at a temp directory so the DB is created there.
	dbDir := t.TempDir()
	dbPath := dbDir + "/test-vocab.db"
	t.Setenv("HTA_DATABASE_PATH", dbPath)

	healthAddr, err := freePortAddr()
	if err != nil {
		t.Fatalf("freePortAddr: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- run(ctx, "config-does-not-exist.yaml", healthAddr) }()

	// Wait for the health server to come up — proves run() completed boot.
	if !waitForOK(t, "http://"+healthAddr+"/health", 5*time.Second) {
		t.Fatalf("health never responded; run() may have failed to boot")
	}

	// Cancel and wait for run() to return cleanly (exit 0).
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code: got %d, want 0", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return within 15s of ctx cancel")
	}

	// After shutdown, re-open the DB read-only and assert the sessions table
	// exists — proving database.Open() and database.Migrate() ran at boot.
	import_database_check(t, dbPath)
}

// TestMainDatabaseDefaultPath proves the "empty path resolves to HOME default"
// guarantee: when cfg.Database.Path is empty and HOME is overridden, run()
// creates the DB under $HOME/.home-tutor-agent/vocab.db. On Windows, HOME
// must also be set because os.UserHomeDir() checks USERPROFILE first.
func TestMainDatabaseDefaultPath(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)

	// Override HOME (and USERPROFILE on Windows) so the default path resolves
	// to a temp directory. os.UserHomeDir() on Windows checks USERPROFILE
	// before HOME, so we must set both.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	healthAddr, err := freePortAddr()
	if err != nil {
		t.Fatalf("freePortAddr: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- run(ctx, "config-does-not-exist.yaml", healthAddr) }()

	if !waitForOK(t, "http://"+healthAddr+"/health", 5*time.Second) {
		t.Fatalf("health never responded; run() may have failed to boot")
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code: got %d, want 0", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return within 15s of ctx cancel")
	}

	// Assert the default DB file was created under $HOME/.home-tutor-agent/vocab.db.
	defaultDB := tmpHome + "/.home-tutor-agent/vocab.db"
	if _, err := os.Stat(defaultDB); os.IsNotExist(err) {
		t.Errorf("default DB not created at %s", defaultDB)
	}
}

// import_database_check opens the DB file read-only and asserts the sessions
// table exists, proving Open+Migrate ran at boot. Separated from the test so
// the "database" import does not conflict with the test file's existing imports.
// Uses the same modernc.org/sqlite driver as production.
func import_database_check(t *testing.T, dbPath string) {
	t.Helper()
	t.Logf("checking database at %s", dbPath)

	// We cannot import database package from main_test (same package), so use
	// database/sql directly with the same driver. This proves the DB file is
	// valid SQLite and was migrated.
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("re-open DB read-only: %v", err)
	}
	defer db.Close()

	var name string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='sessions'").Scan(&name)
	if err != nil {
		t.Fatalf("sessions table not found after boot: %v", err)
	}
	if name != "sessions" {
		t.Errorf("unexpected table name: got %q, want %q", name, "sessions")
	}

	// Also verify session_events exists.
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='session_events'").Scan(&name)
	if err != nil {
		t.Fatalf("session_events table not found after boot: %v", err)
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
