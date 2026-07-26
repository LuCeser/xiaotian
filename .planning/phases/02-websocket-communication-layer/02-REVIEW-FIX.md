---
phase: 02-websocket-communication-layer
fixed: 2026-07-26T00:00:00Z
source_review: .planning/phases/02-websocket-communication-layer/02-REVIEW.md
fix_scope: all
findings_in_scope: 16
fixed: 16
skipped: 0
iteration: 1
status: all_fixed
auto_mode: false
---

# Phase 02: Code Review Fix Report

**Fixed at:** 2026-07-26
**Source review:** `.planning/phases/02-websocket-communication-layer/02-REVIEW.md`
**Iteration:** 1

**Summary:**
- Findings in scope: 16 (1 Critical, 9 Warning, 6 Info — `fix_scope: all`)
- Fixed: 16
- Skipped: 0
- Status: **all_fixed**

All fixes were verified with `go build ./...`, `go vet ./pkg/ws/...`, and `go test -count=1 ./pkg/ws/... ./internal/wstest/...` (passes consistently across multiple runs). `-race` was unavailable in this environment (requires cgo, which is disabled).

All 16 commits are atomic (one per finding) and now live on `main` (fast-forwarded from `gsd-reviewfix/02-12697`). They follow the repo's `fix(02): <id> <description>` convention.

## Deviations from the review's literal suggestions (with rationale)

- **CR-01** — The review suggested `close(h.register)` / `close(h.unregister)` inside `Hub.Close`. A **closed-flag-only** approach (no physical channel close) was implemented instead, guarded by a `sync.RWMutex`, which is the explicit alternative the review named ("use a closed-flag guard like `Client.CloseSend`"). Rationale: closing the channels while `Hub.Run` is still selecting on them would make `Run` receive nil values and busy-loop; the flag alone prevents the send-on-closed panic and the blocking-send leak, and the channels are reclaimed by GC with the Hub. Also added `defer h.Close()` in `Run` so the flag always flips on exit.
- **WR-09** — `Server.Shutdown` closes the router via an `interface{ Close() error }` type assertion rather than importing the concrete `*Router`, keeping `Server` decoupled from the router type. This required landing WR-02 (which changed `Router.Close` to return `error`) before WR-09 so the assertion actually matches.

## Fixed Issues

### CR-01: Hub goroutine leak on shutdown
**Files modified:** `pkg/ws/hub.go`, `pkg/ws/server.go`
**Commit:** `39e65bd`
Made `Register` genuinely non-blocking (returns `bool`, `select`/`default` under `closeMu`), `Unregister` non-blocking (drops + warns under `closeMu`), added `Hub.Close` (idempotent flag flip) and `defer h.Close()` in `Run`, added `drainUnregister` on `ctx.Done()` so in-flight writePumps get their `CloseSend` signal. `Server.HandleWebSocket` now rejects the connection when `Register` reports false.

### WR-01: Router worker panic circuit breaker
**Files modified:** `pkg/ws/router.go`
**Commit:** `0147679`
Added a router-wide `panicCount`; worker stops dequeuing once it exceeds `routerPanicThreshold` (8). `zap.Stack` (which calls the expensive `runtime.Stack`) is now captured only on the first panic; subsequent panics log a cheap count line.

### WR-02: Bound `Router.Close` with a shutdown timeout
**Files modified:** `pkg/ws/router.go`
**Commit:** `23fa9ce`
`Close()` now returns `error` and bounds `wg.Wait` at `routerShutdownTimeout` (5s) via a `select` on a `done` channel / `time.After`. Existing callers that ignore the return still compile.

### WR-03: `Client.Close` returns the WriteControl error
**Files modified:** `pkg/ws/connection.go`
**Commit:** `4bebc17`
Returns the `WriteControl` error (or `conn.Close` error if `WriteControl` succeeded) instead of always `nil`, so the capacity-rejection path knows whether the close frame was actually delivered.

### WR-04: Log `writePump` write-failure exits
**Files modified:** `pkg/ws/connection.go`
**Commit:** `a8706d2`
Both write-failure exit paths now log at `Info` with a `timeout` hint (`errors.Is(err, os.ErrDeadlineExceeded)`) so a slow/dropped client and the write-deadline kill are diagnosable. Added `os` import.

### WR-05: Raise heartbeat ping-failure log to `Info`
**Files modified:** `pkg/ws/heartbeat.go`
**Commit:** `12966ea`
`Debug` → `Info`, so the primary stale-connection signal (and the documented Safari background-tab silent drop) survives a default production zap config.

### WR-06: Validate Heartbeat interval at the boundary
**Files modified:** `pkg/ws/heartbeat.go`
**Commit:** `ef57380`
`Heartbeat` is exported and runs in its own goroutine with no recover wrapper; a non-positive interval would panic `time.NewTicker` and crash the process. Added an `if interval <= 0 { warn; return }` guard.

### WR-07: Validate HeartbeatInterval, buffer sizes, handshake timeout
**Files modified:** `pkg/ws/server.go`
**Commit:** `ae4a3c3`
Extended `ServerConfig.Validate` to reject negative `HeartbeatInterval`, `ReadBufferSize`, `WriteBufferSize`, `HandshakeTimeout` (which previously passed through `== 0` defaulting and silently broke behaviour).

### WR-08: Document handler context semantics
**Files modified:** `pkg/ws/router.go`
**Commit:** `6778043`
Documented on the `Handler` type and `Route` method that handlers run under the per-connection context, not the Router's. The full per-task dual-parent context fix would spawn a goroutine per task (expensive on a 1C VPS); WR-02's bounded `Close` is the mitigation.

### WR-09: Coordinated `Server.Shutdown`
**Files modified:** `pkg/ws/server.go`
**Commit:** `8986332`
Added `Server.Shutdown(ctx) error` that closes the Hub (short-circuits stragglers per CR-01) then the Router (bounded wait per WR-02) via an `interface{ Close() error }` assertion, and documents the required HTTP-first shutdown ordering.

### IN-01: Remove dead `fakeHub` test type
**Files modified:** `pkg/ws/connection_test.go`
**Commit:** `8653548`
Deleted the never-instantiated `fakeHub` struct and its methods; every variable named `fakeHub` is a real `*Hub`.

### IN-02: Fix stale `MaxMessageBytes` default comment
**Files modified:** `pkg/ws/server.go`
**Commit:** `4d1474b`
`Default 65536` → `Default 65551 (HeaderSize + max uint16 payload)`.

### IN-03: Document origin-whitelist port gotcha
**Files modified:** `pkg/ws/server.go`
**Commit:** `c27bd61`
Expanded the `AllowedOrigins` field doc to state operators must include the port for non-default ports.

### IN-04: Stabilize `TestClientReadPumpPanicRecovery` under CR-01
**Files modified:** `pkg/ws/connection_test.go`
**Commit:** `04945d1`
Gave the hub its own longer-lived ctx (decoupled from readPump) and switched to the `waitFor` polling helper. Under CR-01 the hub's `ctx.Done` races the readPump defer; the shared-ctx version dropped the unregister non-deterministically. Mirrors the WR-02 fix already applied to the sibling readPump tests.

### IN-05: Drop unused `Router.mu`
**Files modified:** `pkg/ws/router.go`
**Commit:** `1901127`
`handlers` is immutable after `NewRouter`, so the `RWMutex` guarded nothing and added overhead on the readPump hot path. Reads the map directly.

### IN-06: Guard Router `Close`/`Route` against the zero value
**Files modified:** `pkg/ws/router.go`
**Commit:** `82e49f0`
Tests construct bare `&Router{}`, leaving `ctx`/`cancel` nil. `Route` now returns `ErrRouterShuttingDown` when `ctx == nil`; `Close` skips `cancel` when it is nil.

---

## Notes for the human reviewer

1. The previously-flaky `TestHubStopsOnContextCancel` (scheduler-sensitive `waitForGoroutineIncrease`, passes in isolation, occasionally failed under full-suite load) was passing consistently across the fixer's runs; it is a pre-existing condition unrelated to these fixes.
2. One orphan temp directory (`C:/Users/fortu/AppData/Local/Temp/sv-02-reviewfix-DH5ls0`) remained on disk because Windows holds a go-build-cache file handle on it. Git state is fully clean (worktree deregistered, temp branch deleted, sentinel gone); the directory is reclaimable once the handle releases.

## Verification

- `go build ./...` — OK
- `go vet ./pkg/ws/...` — clean
- `go test -count=1 ./pkg/ws/... ./internal/wstest/...` — passes consistently across multiple runs
- `-race` unavailable in this environment (requires cgo, disabled) — recommend running locally before ship

**Fixer:** gsd-code-fixer
**Iteration:** 1
