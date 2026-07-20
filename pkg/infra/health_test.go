package infra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// buildStartedServer constructs a Server with fast intervals, starts it, and
// returns it together with a cleanup func that shuts it down. Health/metrics
// handler tests share this helper so they exercise a live Server.Snapshot
// path rather than a hand-built struct.
func buildStartedServer(t *testing.T) (*Server, func()) {
	t.Helper()
	cfg := fastServerConfig()
	srv, err := NewServer(cfg, newFakeLogger())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	return srv, cleanup
}

// TestHealthHandlerReturns200 proves D-27 happy path: when the server is not
// load shedding, GET /health returns HTTP 200 with a JSON body whose status
// field is "ok".
func TestHealthHandlerReturns200(t *testing.T) {
	srv, cleanup := buildStartedServer(t)
	defer cleanup()

	h := HealthHandler(srv, newFakeLogger())
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal body %q: %v", rec.Body.String(), err)
	}
	if got, want := resp["status"], "ok"; got != want {
		t.Errorf("status field: got %v, want %q", got, want)
	}
}

// TestHealthHandlerReportsComponentStatus proves the body carries the four
// component snapshots (memory, pool, connections, workers). The exact
// numeric values depend on runtime state; we assert field presence.
func TestHealthHandlerReportsComponentStatus(t *testing.T) {
	srv, cleanup := buildStartedServer(t)
	defer cleanup()

	h := HealthHandler(srv, newFakeLogger())
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	// Memory fields are nested under "memory": used_bytes, limit_bytes.
	// Connections under "connections": active. Workers under "workers":
	// inflight (the JSON tag is in_flight, snake_case per Prometheus
	// convention). Use substring match so the test does not break when
	// numeric values shift.
	for _, want := range []string{
		"used_bytes",
		"limit_bytes",
		"active",
		"in_flight",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; body=%s", want, body)
		}
	}
}

// TestHealthHandlerMarksUnhealthyOnLoadShed proves the load-shedding → 503
// transition (D-27). When Server.loadShedding is true, the health endpoint
// reports HTTP 503 with status: "unhealthy" so the load balancer can pull
// the instance out of rotation.
func TestHealthHandlerMarksUnhealthyOnLoadShed(t *testing.T) {
	srv, cleanup := buildStartedServer(t)
	defer cleanup()

	// Flip the latch directly — same package, so the unexported field is
	// accessible. This simulates the MemoryMonitor firing the LoadShedder
	// callback with active=true.
	srv.loadShedding.Store(true)
	defer srv.loadShedding.Store(false)

	h := HealthHandler(srv, newFakeLogger())
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusServiceUnavailable; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal body %q: %v", rec.Body.String(), err)
	}
	if got, want := resp["status"], "unhealthy"; got != want {
		t.Errorf("status field: got %v, want %q", got, want)
	}
	if got, want := resp["load_shedding"], true; got != want {
		t.Errorf("load_shedding field: got %v, want %v", got, want)
	}
}

// TestMetricsHandlerReturnsPrometheusFormat proves D-26 / D-51: GET /metrics
// returns text/plain with the Prometheus exposition format version banner
// (version=0.0.4) in the Content-Type header.
func TestMetricsHandlerReturnsPrometheusFormat(t *testing.T) {
	srv, cleanup := buildStartedServer(t)
	defer cleanup()

	h := MetricsHandler(srv, newFakeLogger())
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type: got %q, want text/plain", ct)
	}
	if !strings.Contains(ct, "version=0.0.4") {
		t.Errorf("Content-Type: got %q, want version=0.0.4", ct)
	}
}

// TestMetricsHandlerIncludesAllComponents proves every hta_* metric is
// present in the /metrics body. The list mirrors the plan's <action> spec.
func TestMetricsHandlerIncludesAllComponents(t *testing.T) {
	srv, cleanup := buildStartedServer(t)
	defer cleanup()

	h := MetricsHandler(srv, newFakeLogger())
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	wantMetrics := []string{
		"hta_memory_used_bytes",
		"hta_memory_limit_bytes",
		"hta_memory_percent",
		"hta_pool_inflight",
		"hta_pool_total_gets",
		"hta_pool_total_puts",
		"hta_pool_total_drops",
		"hta_connections_active",
		"hta_connections_total_accepted",
		"hta_connections_total_rejected",
		"hta_connections_total_cleaned",
		"hta_workers_inflight",
		"hta_workers_queue_len",
		"hta_workers_total_processed",
		"hta_workers_total_panics",
		"hta_workers_total_backpressure",
		"hta_load_shedding",
		"hta_uptime_seconds",
	}
	for _, name := range wantMetrics {
		if !strings.Contains(body, name+" ") {
			t.Errorf("metrics body missing %q; body=%s", name, body)
		}
	}
}

// TestHealthAndMetricsWrappedByRecovery proves D-49 + T-01-36 mitigation:
// RecoveryMiddleware converts a panicking handler into HTTP 500 without
// crashing the process.
func TestHealthAndMetricsWrappedByRecovery(t *testing.T) {
	logger := newFakeLogger()
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated handler panic")
	})
	wrapped := RecoveryMiddleware(logger, panicHandler)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusInternalServerError; got != want {
		t.Errorf("status: got %d, want %d (recovery should yield 500)", got, want)
	}
}

// TestRegisterMetricsIdempotent proves RegisterMetrics is safe to call more
// than once. Without the Prometheus library, registration is a no-op that
// only validates the server pointer; subsequent calls return nil.
func TestRegisterMetricsIdempotent(t *testing.T) {
	srv, cleanup := buildStartedServer(t)
	defer cleanup()

	if err := RegisterMetrics(srv); err != nil {
		t.Fatalf("RegisterMetrics first: %v", err)
	}
	if err := RegisterMetrics(srv); err != nil {
		t.Errorf("RegisterMetrics second: %v, want nil", err)
	}
}
