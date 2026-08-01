---
phase: quick-260801-ge1
plan: 01
subsystem: websocket-runtime-wiring
tags: [websocket, runtime-wiring, config, infra, p0-1]
status: complete
requirements: [P0-1]
dependency_graph:
  requires:
    - "pkg/ws (Hub/Client/Server/Router — Phase 2 library)"
    - "pkg/infra (Config/DefaultConfig/Validate/bindEnv)"
    - "cmd/service main.go entrypoint (Phase 1 Plan 08)"
  provides:
    - "infra.WSConfig (operational WS config + dev-safe default origins)"
    - "cmd/service public WS listener on cfg.Server.Addr (:8080) with /ws route"
    - "no-op ws.Handler map (MsgTypeAudio/MsgTypeControl) — Phase 6/7 substrate"
    - "CR-01-safe WS shutdown ordering in main.go"
  affects:
    - "cmd/service/main.go (WS construction + publicSrv + shutdown order)"
    - "pkg/infra/config.go (WSConfig struct + DefaultConfig + Validate + bindEnv)"
    - "pkg/infra/logging.go (Zap() accessor for *zap.Logger interop)"
tech_stack:
  added: []
  patterns:
    - "double validation (infra default/override layer + ws semantic-default layer)"
    - "CR-01-safe shutdown ordering (publicSrv → wsServer → server/healthSrv)"
    - "no-op handler substrate (upgrade/hub/heartbeat/decode/dispatch live, business logic deferred)"
key_files:
  created:
    - cmd/service/handlers.go
  modified:
    - pkg/infra/config.go
    - pkg/infra/config_test.go
    - pkg/infra/logging.go
    - config.example.yaml
    - cmd/service/main.go
    - cmd/service/main_test.go
decisions:
  - "WSConfig.AllowedOrigins defaults to [localhost,127.0.0.1] so the service boots with NO ws: block (P0-1 must-have)"
  - "MaxMessageBytes/*BufferSize default to 0 (infra sentinel); main.go bridges MaxMessageBytes 0→ws.DefaultMaxMessageBytes because ws.ServerConfig.Validate rejects <=0 before NewServer's fill branch"
  - "CR-01-safe shutdown order: publicSrv.Shutdown THEN wsServer.Shutdown THEN server/healthSrv, all within cfg.Shutdown.Timeout"
  - "/health + /metrics stay on the separate localhost healthSrv (probe/session traffic separation preserved)"
metrics:
  duration: ~25
  completed: "2026-08-01"
  tasks: 3
  files: 7
---

# Phase quick-260801-ge1 Plan 01: WebSocket P0-1 Runtime Wiring Summary

Wired Phase 2's `pkg/ws` library into the `cmd/service` runtime so the Go service listens on `:8080` and serves the `/ws` WebSocket upgrade route with no-op handlers, closing review finding P0-1.

## What Was Built

### Task A: WSConfig in pkg/infra (2844ec7)

Added `WSConfig` to `pkg/infra/config.go` mirroring the `PythonConfig` operational-fields pattern (no secrets — D-24 invariant). The `WS WSConfig` field on the top-level `Config` carries: `AllowedOrigins`, `HeartbeatInterval`, `MaxMessageBytes`, `ReadBufferSize`, `WriteBufferSize`, `HubLimit`, `HubRegisterBuffer`, `HubUnregisterBuffer`, `RouteBuffer`, `WorkerConcurrency`.

**Double-validation design:** `DefaultConfig().WS.AllowedOrigins = ["localhost","127.0.0.1"]` so the service boots with NO `ws:` block in config.yaml. `MaxMessageBytes`/`ReadBufferSize`/`WriteBufferSize` default to ZERO so the ws package applies its own semantic defaults at construction — infra owns the default/override layer, ws owns the semantics.

`Config.Validate` rejects a wildcard `"*"` origin entry at the infra layer (mirrors `ws.ServerConfig.Validate`, honors D-24). There is intentionally NO "origins required" check — the defaults always win when the operator leaves it empty. `bindEnvForAllLeaves` registers all `ws.*` leaf keys (`HTA_WS_ALLOWEDORIGINS` etc.).

`config.example.yaml` gained a documented `ws:` block explaining: (1) the block is optional (defaults boot); (2) wildcard forbidden (D-24); (3) port required for non-default ports (IN-03); (4) zero-valued fields defer to ws package defaults.

TDD: RED (tests fail to compile — `cfg.WS` undefined) → GREEN (WSConfig implemented, all assertions pass).

### Task B: WS runtime wiring in main.go (de9f0f4)

Created `cmd/service/handlers.go` defining `noOpHandlers()` returning `map[ws.MessageType]ws.Handler` keyed by `ws.MsgTypeAudio` and `ws.MsgTypeControl` — each a closure that returns nil (the exact shape proven in `pkg/ws/integration_test.go:33-42`). Documented as Phase 6/7 placeholders: the WS upgrade, Hub registration, heartbeat, frame decode, and Router dispatch all go live; real audio/session logic is deferred.

Extended `cmd/service/main.go` `run()` to construct `ws.Hub`, `ws.Router` (with no-op handlers), and `ws.Server` from `cfg.WS`, mapping to the three ws config structs. Starts `go hub.Run(ctx)`, builds a public mux carrying ONLY `/ws` (`publicMux.HandleFunc("/ws", wsServer.HandleWebSocket)`), and starts `publicSrv` on `cfg.Server.Addr` with the same timeout hygiene as `healthSrv`.

**Shutdown ordering (CR-01-safe):** `publicSrv.Shutdown(shutdownCtx)` → `wsServer.Shutdown(shutdownCtx)` → `server.Shutdown` → `healthSrv.Shutdown`, all within `cfg.Shutdown.Timeout`. This matches the documented contract in `pkg/ws/server.go Server.Shutdown`: stop accepting new connections first, then close the Hub+Router so no HandleWebSocket goroutine blocks on channels being torn down.

`/health` and `/metrics` remain on the dedicated localhost `healthSrv` — untouched.

### Task C: WS upgrade + no-leak integration tests (bfb24d3)

Added two tests to `cmd/service/main_test.go`:

- **`TestMainWSPublicRouteUpgradesAndShutsDownCleanly`**: acquires `runMu`, points the public listener at an ephemeral port via `HTA_SERVER_ADDR`, polls-dials `ws://<ephemeral>/ws?user_id=t&device=t` with `Origin: http://localhost` (default origins accept it) until HTTP 101, records goroutine baseline, closes the client conn, cancels ctx, asserts exit 0, then asserts goroutine delta `<= 2` (CR-01 no-leak, same tolerance as `pkg/ws` TestIntegration_ServerShutdownCleansAllGoroutines). Skips gracefully on a flaky port race.
- **`TestMainBootsWithNoWSConfigBlock`**: boots `run()` with `config-does-not-exist.yaml` + no `HTA_WS_*` env, asserts exit 0 on ctx cancel — the runtime proof of the "boots from defaults" guarantee.

## Deviations from Plan

### Rule 3 — Blocking: infra.Logger / *zap.Logger type mismatch

- **Found during:** Task B (compile error)
- **Issue:** The plan's Task B action passed `logger` (type `infra.Logger`, an interface) directly to `ws.NewHub`/`NewRouter`/`NewServer`, which take `*zap.Logger`. The infra `ZapLogger.l` field is unexported (T-01-09 invariant) with no accessor.
- **Fix:** Added a `Zap() *zap.Logger` concrete method on `*ZapLogger` in `pkg/infra/logging.go` (does not expand the Logger interface, so no other implementer breaks). main.go type-asserts `logger.(*infra.ZapLogger)` and falls back to `zap.NewNop()` for the test/no-op path.
- **Files modified:** `pkg/infra/logging.go` (added `Zap()` method), `cmd/service/main.go` (wsLogger bridge)
- **Commit:** de9f0f4

### Rule 1 — Bug: ws.ServerConfig.Validate rejects MaxMessageBytes <= 0 before NewServer's fill

- **Found during:** Task C verification
- **Issue:** The plan's design ("MaxMessageBytes default 0 → ws.NewServer fills 65551") cannot work: `ws.ServerConfig.Validate()` rejects `MaxMessageBytes <= 0` and runs BEFORE `NewServer`'s own `if cfg.MaxMessageBytes == 0 { fill default }` branch — making that branch dead code for MaxMessageBytes. (`ReadBufferSize`/`WriteBufferSize` are unaffected: Validate only rejects `< 0` for those, so 0 passes and the fill branch works.)
- **Fix:** In `cmd/service/main.go`, bridge the semantic default at the wiring layer: `if cfg.WS.MaxMessageBytes == 0 { maxMsg = ws.DefaultMaxMessageBytes }` before constructing `serverCfg`. This preserves infra's 0-sentinel design and lets ws.ServerConfig.Validate pass. Task A's assertion that `DefaultConfig().WS.MaxMessageBytes == 0` is unchanged.
- **Files modified:** `cmd/service/main.go`
- **Commit:** bfb24d3

## Verification Results

All phase-level checks pass:

```
go build ./...  → BUILD_OK
go vet ./...    → VET_OK
go test ./... -count=1:
  ok  github.com/xiaotian/home-tutor-agent/cmd/service   2.050s
  ok  github.com/xiaotian/home-tutor-agent/pkg/infra     1.510s
  ok  github.com/xiaotian/home-tutor-agent/pkg/pybridge  0.603s
  ok  github.com/xiaotian/home-tutor-agent/pkg/ws        5.640s
```

Targeted Task C tests (proving the WS upgrade + no-leak contracts):

```
=== RUN   TestMainWSPublicRouteUpgradesAndShutsDownCleanly
--- PASS: TestMainWSPublicRouteUpgradesAndShutsDownCleanly (0.25s)
=== RUN   TestMainBootsWithNoWSConfigBlock
--- PASS: TestMainBootsWithNoWSConfigBlock (0.30s)
```

Manual smoke test (built binary, config.yaml with NO ws: block):

```
public server listening  addr=0.0.0.0:8080   ← service listens on :8080
health server listening  addr=127.0.0.1:8081  ← /health + /metrics intact
curl http://127.0.0.1:8081/health → {"status":"ok"}
gorilla/websocket Dial ws://127.0.0.1:8080/ws?user_id=t&device=t (Origin http://localhost)
  → UPGRADE_OK: HTTP 101 Switching Protocols   ← WS upgrade succeeds with default origins
hub registered client   total=1                ← end-to-end Hub registration
hub unregistered client total=0                ← clean disconnect
```

## Known Stubs

The no-op handlers in `cmd/service/handlers.go` are INTENTIONAL stubs by design (not defects). `ws.MsgTypeAudio` and `ws.MsgTypeControl` handlers both `return nil`. The plan explicitly defers real audio playback, session state, TTS, and FSRS logic to Phase 6/7. The WS upgrade, Hub registration, heartbeat, frame decoding, and Router dispatch are all live and end-to-end testable; only the handler business logic is a no-op. This plan ships the runtime substrate, not the dictation business logic.

## P0-1 Closure

P0-1 ("WS layer not wired into runtime; service does not listen on :8080") is closed: `go run ./cmd/service` now boots a real WebSocket server listening on `cfg.Server.Addr` (default :8080) with `/ws` live, bootable from defaults, with verifiable graceful shutdown and no goroutine leak.

## Self-Check: PASSED

All created/modified files exist on disk:
- FOUND: pkg/infra/config.go
- FOUND: pkg/infra/config_test.go
- FOUND: pkg/infra/logging.go
- FOUND: config.example.yaml
- FOUND: cmd/service/handlers.go
- FOUND: cmd/service/main.go
- FOUND: cmd/service/main_test.go

All three task commits verified in git log:
- 2844ec7 feat(infra): add WSConfig with default origins for runtime WS wiring
- de9f0f4 feat(service): wire WebSocket runtime on :8080 with no-op handlers
- bfb24d3 test(service): WS upgrade + clean shutdown integration test
