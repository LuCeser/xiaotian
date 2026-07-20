package infra

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// prometheusContentType is the Content-Type header value Prometheus
// requires for the v0.0.4 text exposition format. Scrapers (Prometheus,
// Grafana Agent, vmagent) key off this header to select the parser.
const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

// healthResponse is the JSON body returned by HealthHandler. Field tags
// are snake_case to match the Prometheus / logging convention used
// throughout pkg/infra.
type healthResponse struct {
	Status       string          `json:"status"`
	Uptime       string          `json:"uptime"`
	LoadShedding bool            `json:"load_shedding"`
	Memory       MemorySnapshot  `json:"memory"`
	Pool         PoolStats       `json:"pool"`
	Connections  ConnectionStats `json:"connections"`
	Workers      WorkerPoolStats `json:"workers"`
}

// HealthHandler returns an http.Handler that renders the server's
// point-in-time snapshot as JSON (D-27). When Server.LoadShedding is true
// the response is HTTP 503 with status "unhealthy" so the load balancer
// can pull the instance out of rotation; otherwise HTTP 200 with "ok".
//
// Threat mitigations:
//   - T-01-34 Information Disclosure: the response exposes only counts and
//     bytes, never session payloads or task arguments.
//   - T-01-36 DoS via handler panic: callers MUST wrap the returned
//     handler in RecoveryMiddleware (see main.go).
//
// The handler does not retain the request past ServeHTTP's return; safe
// for concurrent use.
func HealthHandler(server *Server, logger Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := server.Snapshot()
		status := "ok"
		httpStatus := http.StatusOK
		if snap.LoadShedding {
			status = "unhealthy"
			httpStatus = http.StatusServiceUnavailable
		}

		resp := healthResponse{
			Status:       status,
			Uptime:       snap.Uptime.String(),
			LoadShedding: snap.LoadShedding,
			Memory:       snap.Memory,
			Pool:         snap.Pool,
			Connections:  snap.Connections,
			Workers:      snap.Workers,
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(httpStatus)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(resp); err != nil {
			logger.Warn("health handler: encode response failed",
				F("component", "infra.health"),
				ErrField(err),
			)
		}
	})
}

// MetricsHandler returns an http.Handler that renders Prometheus exposition
// format (v0.0.4) text from Server.Snapshot (D-26, D-51). The handler is
// self-contained — it does NOT use the prometheus/client_golang library
// because (a) the proxy is unreachable from this build environment and (b)
// the binary is leaner without it, which matters on the 1C/1G VPS. The
// output format is identical from a scraper's perspective: same
// Content-Type, same metric names, same value encoding.
//
// All metric names carry the hta_ (Home Tutor Agent) prefix per Prometheus
// naming convention so they group together in dashboards and do not
// collide with the standard process_/go_ families.
//
// Deviation note: the plan called for github.com/prometheus/client_golang
// but network access to the Go module proxy is unavailable. The text
// exposition format is a stable spec (https://prometheus.io/docs/instrumenting/exposition_formats/)
// and we implement the subset required for our 18 metrics. All functional
// acceptance criteria (text/plain, version=0.0.4, hta_* prefix) are met.
func MetricsHandler(server *Server, logger Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := server.Snapshot()

		w.Header().Set("Content-Type", prometheusContentType)
		w.WriteHeader(http.StatusOK)

		var b strings.Builder
		// Memory metrics.
		writeMetric(&b, "hta_memory_used_bytes", "Current Sys bytes reported by runtime.ReadMemStats",
			"gauge", float64(snap.Memory.UsedBytes))
		writeMetric(&b, "hta_memory_limit_bytes", "Configured memory budget (MemoryConfig.LimitBytes)",
			"gauge", float64(snap.Memory.LimitBytes))
		writeMetric(&b, "hta_memory_percent", "Used bytes / limit bytes * 100",
			"gauge", float64(snap.Memory.Percent))

		// Pool metrics.
		writeMetric(&b, "hta_pool_inflight", "Audio buffers currently handed out via Get",
			"gauge", float64(snap.Pool.InFlight))
		writeMetric(&b, "hta_pool_total_gets", "Cumulative AudioBufferPool.Get calls since construction",
			"counter", float64(snap.Pool.TotalGets))
		writeMetric(&b, "hta_pool_total_puts", "Cumulative AudioBufferPool.Put calls (validated) since construction",
			"counter", float64(snap.Pool.TotalPuts))
		writeMetric(&b, "hta_pool_total_drops", "Cumulative AudioBufferPool overflow drops (RES-04 health metric)",
			"counter", float64(snap.Pool.TotalDrops))

		// Connection metrics.
		writeMetric(&b, "hta_connections_active", "Currently registered connections",
			"gauge", float64(snap.Connections.Active))
		writeMetric(&b, "hta_connections_total_accepted", "Cumulative successful Register calls",
			"counter", float64(snap.Connections.TotalAccepted))
		writeMetric(&b, "hta_connections_total_rejected", "Cumulative Register calls rejected at capacity (429)",
			"counter", float64(snap.Connections.TotalRejected))
		writeMetric(&b, "hta_connections_total_cleaned", "Cumulative stale-connection evictions by the cleanup goroutine",
			"counter", float64(snap.Connections.TotalCleaned))

		// Worker metrics.
		writeMetric(&b, "hta_workers_inflight", "Tasks currently executing inside WorkerPool workers",
			"gauge", float64(snap.Workers.InFlight))
		writeMetric(&b, "hta_workers_queue_len", "Tasks currently buffered in the worker queue",
			"gauge", float64(snap.Workers.QueueLen))
		writeMetric(&b, "hta_workers_total_processed", "Cumulative tasks completed (excludes panics)",
			"counter", float64(snap.Workers.TotalProcessed))
		writeMetric(&b, "hta_workers_total_panics", "Cumulative task panics recovered by WorkerPool.runTask",
			"counter", float64(snap.Workers.TotalPanics))
		writeMetric(&b, "hta_workers_total_backpressure", "Cumulative Submit calls rejected with ErrWorkerPoolFull",
			"counter", float64(snap.Workers.TotalBackpressure))

		// Aggregate metrics.
		loadShed := 0.0
		if snap.LoadShedding {
			loadShed = 1.0
		}
		writeMetric(&b, "hta_load_shedding", "1 when the server is load shedding (memory over threshold), 0 otherwise",
			"gauge", loadShed)
		writeMetric(&b, "hta_uptime_seconds", "Seconds since Server.Start",
			"gauge", snap.Uptime.Seconds())

		if _, err := w.Write([]byte(b.String())); err != nil {
			logger.Warn("metrics handler: write failed",
				F("component", "infra.health"),
				ErrField(err),
			)
		}
	})
}

// writeMetric appends one metric entry in Prometheus text exposition
// format. The format per metric is:
//
//	# HELP <name> <help>
//	# TYPE <name> <type>
//	<name> <value>
//
// We do not emit labels (every metric here is unlabeled). Values are
// rendered with %g so integers stay integers (Prometheus accepts both
// integer and float literals for gauge/counter).
func writeMetric(b *strings.Builder, name, help, typ string, value float64) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s %s\n", name, typ)
	fmt.Fprintf(b, "%s %g\n", name, value)
}

// RegisterMetrics is a no-op in the manual-implementation path. The plan
// called for registering an infraCollector on Prometheus's default
// registry; without the library, MetricsHandler reads Server.Snapshot
// directly on each scrape, so there is no registration step.
//
// The function is retained so main.go can call it unconditionally and so
// future migration to prometheus/client_golang (when network access is
// restored) only needs to fill in the body. Returns nil for any non-nil
// server.
func RegisterMetrics(server *Server) error {
	if server == nil {
		return fmt.Errorf("RegisterMetrics: server must not be nil")
	}
	return nil
}
