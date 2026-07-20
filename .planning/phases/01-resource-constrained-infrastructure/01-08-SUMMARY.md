---
phase: 01-resource-constrained-infrastructure
plan: 08
subsystem: infra-server
tags: [server, lifecycle, health, prometheus, entry-point, capstone]
requires:
  - 01-02  # Config / LoadConfig / Validate
  - 01-03  # Logger interface + ZapLogger + RecoveryMiddleware
  - 01-04  # MemoryMonitor + MemSource + LoadShedder
  - 01-05  # AudioBufferPool
  - 01-06  # ConnectionRegistry + TokenBucket
  - 01-07  # WorkerPool
provides:
  - "Server coordinator (pkg/infra.Server) wiring all four infra components"
  - "ServerSnapshot aggregate for health/metrics endpoints"
  - "HealthHandler (JSON /health, 200 ok / 503 unhealthy) + MetricsHandler (Prometheus text /metrics)"
  - "cmd/service entry point (binary bin/service) with signal.NotifyContext shutdown"
  - "config.example.yaml operator reference"
affects:
  - "pkg/infra/memory.go (LoadShedder signature: added active bool)"
  - "pkg/infra/connection.go (ConnectionStats JSON tags for stable health output)"
  - "pkg/infra/worker.go (WorkerPoolStats JSON tags)"
  - "pkg/infra/pool.go (PoolStats JSON tags)"
tech-stack:
  added: []
  patterns:
    - "Coordinator pattern: Server owns lifecycle of MemoryMonitor + AudioBufferPool + ConnectionRegistry + WorkerPool"
    - "Graceful shutdown via signal.NotifyContext + child-context cancellation"
    - "Manual Prometheus text exposition format (v0.0.4) without client_golang dependency"
    - "Testable entry point: run(ctx, configPath, override) so main() handles flags + signal wiring"
key-files:
  created:
    - pkg/infra/server.go
    - pkg/infra/server_test.go
    - pkg/infra/health.go
    - pkg/infra/health_test.go
    - cmd/service/main.go
    - cmd/service/main_test.go
    - config.example.yaml
  modified:
    - pkg/infra/memory.go
    - pkg/infra/memory_test.go
    - pkg/infra/connection.go
    - pkg/infra/worker.go
    - pkg/infra/pool.go
decisions:
  - "D-08-1: Implement Prometheus text exposition format manually instead of using github.com/prometheus/client_golang (network proxy unreachable; spec is stable; smaller binary fits 1C/1G VPS budget)."
  - "D-08-2: run(ctx, configPath, healthAddrOverride) is the testable entry point; main() wraps it with signal.NotifyContext. Tests cancel ctx directly because syscall.Kill(SIGTERM) is unavailable on Windows dev hosts."
  - "D-08-3: LoadShedder signature extended to func(reason string, active bool) so MemoryMonitor can signal both rising-edge (active=true) and falling-edge (active=false) transitions. Server's loadShedding latch clears in lockstep with monitor's shedActive."
  - "D-08-4: Server.Shutdown is best-effort — worker drain timeout is logged but not returned as error. main() is exiting regardless; surfacing the timeout would not change behavior."
  - "D-08-5: Use cfg.Shutdown.Timeout (existing ShutdownConfig from Plan 02) rather than adding a duplicate Shutdown field to ServerConfig as the plan suggested. Avoids redundant state."
metrics:
  duration: 25
  completed: 2026-07-21
  tasks: 3
  files: 12
status: complete
---

# Phase 01 Plan 08: Server Coordinator & Service Entry Point Summary

Assembled the four Wave 3 infra components into a single coordinated Server, exposed health/metrics HTTP endpoints in Prometheus format, and shipped `bin/service` — the runnable binary that closes Phase 1's four success criteria end-to-end.

## What was built

### Task 1: Server coordinator (pkg/infra/server.go)
The Server struct owns pointers to MemoryMonitor, AudioBufferPool, ConnectionRegistry, and WorkerPool. `NewServer(cfg, logger)` validates cfg, builds all four components, and wires MemoryMonitor's LoadShedder callback to a `sync/atomic.Bool` latch on Server. `Start(ctx)` derives cancellable child contexts for the monitor and registry, starts the worker pool, and returns immediately (workers run in background goroutines). `Shutdown(ctx)` is idempotent and best-effort: cancels the child contexts, calls `workers.Shutdown(ctx)`, and logs any timeout without surfacing it as an error. `Snapshot()` returns the aggregate view (memory + pool + connections + workers + loadShedding + uptime) consumed by the health and metrics handlers.

Supporting change in `pkg/infra/memory.go`: `LoadShedder` signature extended to `func(reason string, active bool)`. MemoryMonitor's recovery branch now invokes the callback with `active=false` so downstream latches clear in lockstep. Existing memory tests preserved via shedCounter that counts active=true events separately from clears.

### Task 2: Health & metrics handlers (pkg/infra/health.go)
- `HealthHandler(server, logger) http.Handler` — renders ServerSnapshot as pretty JSON; HTTP 200 with `"status":"ok"` when not shedding, HTTP 503 with `"status":"unhealthy"` when `Server.LoadShedding()` is true. Body exposes only counts/bytes (T-01-34 mitigation).
- `MetricsHandler(server, logger) http.Handler` — renders 18 `hta_*` metrics in Prometheus text exposition format v0.0.4 directly from ServerSnapshot. Covers memory (used/limit/percent), pool (inflight/gets/puts/drops), connections (active/accepted/rejected/cleaned), workers (inflight/queue_len/processed/panics/backpressure), and aggregate (load_shedding/uptime_seconds). Content-Type is `text/plain; version=0.0.4; charset=utf-8`.
- `RegisterMetrics(server) error` — nil-safe no-op retained for API stability with main.go and future migration to the Prometheus library when network access is available.

JSON tag additions to ConnectionStats, WorkerPoolStats, and PoolStats for snake_case output consistent with the Prometheus naming convention across the JSON surface.

### Task 3: cmd/service entry point + config.example.yaml
`run(ctx, configPath, healthAddrOverride) int` is the testable entry point. `main()` parses `-config` and `-health-addr` flags, builds a context via `signal.NotifyContext(SIGTERM, SIGINT)`, and delegates to `run`. Flow:

1. Bootstrap logger writes plain stderr lines for pre-config errors.
2. `infra.LoadConfig(configPath)` — missing file silently skipped; malformed YAML or validation failure is fatal (exit 1).
3. `infra.NewLogger(cfg.Log)` — production structured logger; defer Sync.
4. `infra.NewServer(cfg, logger)` — defensive `cfg.Validate()` runs again inside.
5. `infra.RegisterMetrics(server)` — nil-safe.
6. `server.Start(ctx)` — spawns monitor/registry/workers.
7. `http.Server{Addr: cfg.Server.HealthAddr, Handler: healthMux}` in a goroutine; healthMux wraps both handlers in `RecoveryMiddleware`.
8. `<-ctx.Done()` — blocks until SIGTERM/SIGINT (production) or test cancel.
9. `context.WithTimeout(context.Background(), cfg.Shutdown.Timeout)` bounds graceful drain. Both `server.Shutdown` and `healthSrv.Shutdown` called inside this budget.

`config.example.yaml` documents all eight top-level sections (memory, session, buffer, server, log, worker, connection, shutdown) with the Phase 1 defaults: 150MB budget (RES-01), 100 sessions (RES-03), drop-oldest (RES-04), 30s shutdown timeout.

## Verification

- `go build -o bin/service ./cmd/service` — produces 12MB binary.
- `./bin/service -health-addr 127.0.0.1:18091` — serves `/health` (200, JSON with all four component snapshots) and `/metrics` (200, Prometheus format with `hta_`-prefixed metrics).
- `go test -count=1 -p 1 -timeout 180s ./...` — clean across cmd/service, pkg/infra, pkg/ws.
- `pkg/infra/server_test.go`: 10 subtests (construction, lifecycle, shutdown idempotency, goroutine release, load-shed wiring, snapshot, double-Start rejection, invalid config, nil logger).
- `pkg/infra/health_test.go`: 8 subtests (200 happy path, component fields, 503 unhealthy, Prometheus Content-Type, full metric coverage, panic recovery, RegisterMetrics idempotency).
- `cmd/service/main_test.go`: 5 integration tests (default-config boot, ctx-cancel shutdown, /health + /metrics round trip, RES-01 memory budget <150MB×1.5, invalid-config rejection).
- `TestMainMemoryUnder150MBAtIdle` passes: Sys ≈ 6.5MB at idle (well under 150MB; the 1.5× tolerance accommodates dev-runtime overhead).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking issue] Prometheus client library unreachable; implemented text exposition format manually**
- **Found during:** Task 2 dependency install
- **Issue:** `go get github.com/prometheus/client_golang` failed — the Go module proxy (proxy.golang.org) is unreachable from this build environment (network timeout).
- **Fix:** Implemented Prometheus exposition format v0.0.4 directly in `pkg/infra/health.go` via `writeMetric(b, name, help, type, value)` helper. All 18 metrics emit `# HELP`, `# TYPE`, and value lines per spec. Content-Type header matches (`text/plain; version=0.0.4; charset=utf-8`). `RegisterMetrics` retained as nil-safe no-op for API stability and future migration.
- **Files modified:** pkg/infra/health.go (created)
- **Tradeoff:** Smaller binary (no client_golang tree), fewer deps, but we lose Prometheus's label/collector machinery. For Phase 1's 18 unlabeled metrics this is a clean fit; if Phase 3 needs labeled histograms we can revisit.
- **Commit:** a0e7702

**2. [Rule 3 — Cross-platform compatibility] Test signal mechanism changed from SIGTERM to context cancellation**
- **Found during:** Task 3 RED phase
- **Issue:** The plan's test design used `syscall.Kill(syscall.Getpid(), syscall.SIGTERM)` to drive graceful shutdown. `syscall.Kill` with SIGTERM is unsupported on Windows (dev host); the build failed at compile time.
- **Fix:** Changed `run` signature to `run(ctx, configPath, healthAddrOverride) int` — tests cancel the lifetime context directly. `main()` wires `signal.NotifyContext(SIGTERM, SIGINT)` to produce the same ctx, so production behavior is unchanged.
- **Files modified:** cmd/service/main.go, cmd/service/main_test.go
- **Commit:** a8f60fe

**3. [Rule 3 — Code consistency] JSON tags added to ConnectionStats, WorkerPoolStats, PoolStats**
- **Found during:** Task 2 GREEN phase
- **Issue:** HealthHandler test asserted snake_case JSON keys (`active`, `in_flight`), but only PoolStats had JSON tags and even those were PascalCase. Without tags, Go's default encoder emits PascalCase field names — inconsistent with the Prometheus `hta_*` snake_case convention used elsewhere.
- **Fix:** Added `json:"..."` snake_case tags to every field of ConnectionStats (connection.go), WorkerPoolStats (worker.go), and PoolStats (pool.go).
- **Files modified:** pkg/infra/connection.go, pkg/infra/worker.go, pkg/infra/pool.go
- **Commit:** a0e7702 (ConnectionStats, WorkerPoolStats); a8f60fe (PoolStats)

**4. [Rule 1 — Bug] TestServerSnapshot Uptime assertion too strict on Windows**
- **Found during:** Task 1 GREEN phase
- **Issue:** TestServerSnapshot asserted `Uptime > 0`, but Windows `time.Now()` has ~1ms granularity. Snapshot called immediately after Start can return exactly `0s` duration.
- **Fix:** Relaxed assertion to `Uptime >= 0` with a comment explaining Windows clock granularity.
- **Files modified:** pkg/infra/server_test.go
- **Commit:** 0edebac

### Plan-Level Adjustments

**A. ServerConfig.Shutdown field not added (use existing cfg.Shutdown.Timeout)**
- Plan Task 3 action said to add `Shutdown time.Duration` to ServerConfig. Plan 02 already shipped a separate `ShutdownConfig` struct with a `Timeout` field (cfg.Shutdown.Timeout, default 30s). Adding a duplicate field to ServerConfig would create two sources of truth.
- **Decision D-08-5:** Use the existing `cfg.Shutdown.Timeout` in main.go. No config.go change required.

**B. TDD gate for Task 3 collapsed into single commit**
- Plan TDD discipline calls for separate RED (test-only) and GREEN (implementation) commits per task. Tasks 1 and 2 followed this strictly. Task 3's RED phase required iterating on the test design to resolve the cross-platform signal-handling issue; by the time the tests compiled cleanly, the implementation was already in place to validate them against. The single commit `a8f60fe` contains both test and implementation.
- Tests were genuinely RED before GREEN: the test file failed to compile until main.go existed. The functional outcome (failing → passing) is preserved; only the commit boundary differs.

## TDD Gate Compliance

| Task | RED commit | GREEN commit | Status |
|------|-----------|--------------|--------|
| 1 | 7634907 (test: server_test.go + LoadShedder signature) | 0edebac (feat: server.go) | Compliant |
| 2 | 00a3b71 (test: health_test.go) | a0e7702 (feat: health.go + JSON tags) | Compliant |
| 3 | (collapsed into GREEN commit a8f60fe) | a8f60fe (feat: main.go + main_test.go) | Combined — see Deviation B above |

All three tasks have a `feat(01-08):` commit that adds production code and a corresponding test file that exercises it. The RED→GREEN progression is verifiable per-task; only Task 3's commit boundary is non-canonical.

## Phase 1 Success Criteria Closure

| Criterion | How Plan 08 closes it | Proof |
|-----------|----------------------|-------|
| 1. Go service starts within 150MB | Server wires MemoryMonitor; runtime.ReadMemStats drives the budget check | TestMainMemoryUnder150MBAtIdle: Sys ≈ 6.5MB at idle |
| 2. Enforces 100 concurrent session limit | ConnectionRegistry enforces; surfaced via ServerSnapshot.Connections | ConnectionRegistry from Plan 06, exercised in server_test.go |
| 3. Drop-oldest strategy | AudioBufferPool enforces; surfaced via ServerSnapshot.Pool | AudioBufferPool from Plan 05, surfaced in /metrics as `hta_pool_total_drops` |
| 4. Structured logging captures all events | ZapLogger from Plan 03 injected into every component via Server | Every component logs with F("component","infra.*") tags; visible in test output |

Plus the design decisions: D-17 (Server coordinates all four), D-20 (single-instance — no service discovery code), D-21 (graceful drain via signal.NotifyContext + ctx-cancellation chain), D-26/D-51 (Prometheus metrics), D-27 (detailed health), D-29 (integration tests), D-42/D-47 (constructor DI, no globals).

## Known Stubs

None. Every data path in server.go, health.go, and main.go is wired to real component snapshots. No placeholder text, no TODO markers, no mock data sources in production code.

## Threat Flags

No new threat surface beyond the plan's `<threat_model>`. All six T-01-33 through T-01-38 entries have their mitigations in place:
- T-01-33 (abrupt SIGTERM): signal.NotifyContext + Server.Shutdown drain — proven by TestServerGracefulShutdown.
- T-01-34 (health endpoint info leak): HealthHandler body exposes only counts/bytes — verified in TestHealthHandlerReportsComponentStatus.
- T-01-35 (metrics endpoint capacity leak): accepted; localhost-only binding by default.
- T-01-36 (handler panic DoS): RecoveryMiddleware wraps both handlers — proven by TestHealthAndMetricsWrappedByRecovery.
- T-01-37 (goroutine leak): every component honors ctx — proven by TestServerShutdownReleasesAllGoroutines.
- T-01-38 (config flag injection): LoadConfig returns descriptive error — proven by TestMainRejectsInvalidConfig.

## Self-Check: PASSED

Created files exist:
- pkg/infra/server.go: FOUND
- pkg/infra/server_test.go: FOUND
- pkg/infra/health.go: FOUND
- pkg/infra/health_test.go: FOUND
- cmd/service/main.go: FOUND
- cmd/service/main_test.go: FOUND
- config.example.yaml: FOUND

Commits exist:
- 7634907 (Task 1 RED): FOUND
- 0edebac (Task 1 GREEN): FOUND
- 00a3b71 (Task 2 RED): FOUND
- a0e7702 (Task 2 GREEN): FOUND
- a8f60fe (Task 3 RED+GREEN): FOUND

Binary builds:
- `go build -o bin/service ./cmd/service` produces a 12MB executable that serves `/health` (200, JSON) and `/metrics` (200, Prometheus text).

Full test suite:
- `go test -count=1 -p 1 -timeout 180s ./...` — clean across cmd/service, pkg/infra, pkg/ws.
