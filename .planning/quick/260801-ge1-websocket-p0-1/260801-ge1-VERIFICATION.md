---
phase: quick-260801-ge1
verified: 2026-08-01T04:27:23Z
status: passed
score: 6/6 must-haves verified
behavior_unverified: 0
overrides_applied: 0
---

# Quick Task 260801-ge1: WebSocket P0-1 Runtime Wiring — Verification Report

**Task Goal:** 接入 WebSocket 运行时(P0-1)—— main.go 监听 :8080 注册 /ws, 把 Phase 2 的 ws 库接入运行时, handlers 用 no-op(业务逻辑留 Phase 6/7)。
**Verified:** 2026-08-01T04:27:23Z
**Status:** passed
**Re-verification:** No — initial verification

## Goal Achievement

### Observable Truths

| #   | Truth | Status | Evidence |
| --- | ----- | ------ | -------- |
| 1 | go run ./cmd/service boots and listens on cfg.Server.Addr (default :8080) with the /ws route registered | VERIFIED | `main.go:221-227` builds `publicSrv` on `cfg.Server.Addr` (default `0.0.0.0:8080` from `DefaultConfig` `config.go:398`); `main.go:220` registers `publicMux.HandleFunc("/ws", wsServer.HandleWebSocket)`. `TestMainBootsWithNoWSConfigBlock` boots `run()` and asserts exit 0; its log output shows `public server listening addr=0.0.0.0:8080`. |
| 2 | A config.yaml with NO ws: block still boots — DefaultConfig supplies ws.allowedOrigins=[localhost,127.0.0.1] | VERIFIED | `config.go:432` sets `AllowedOrigins: []string{"localhost","127.0.0.1"}`. infra `Validate` (`config.go:673-678`) only rejects `"*"`, does NOT require origins. `TestMainBootsWithNoWSConfigBlock` proves runtime boot with `config-does-not-exist.yaml` + no `HTA_WS_*` env (exit 0). `TestWSConfigValidate/default WS passes Validate` confirms `DefaultConfig().Validate()==nil`. |
| 3 | A WebSocket client can upgrade successfully at /ws (HTTP 101) using the default localhost origin | VERIFIED | `TestMainWSPublicRouteUpgradesAndShutsDownCleanly` (`main_test.go:223`) dials `ws://<ephemeral>/ws?user_id=t&device=t` with `Origin: http://localhost`; it passes and the log shows `hub registered client total=1` — only reachable on a successful HTTP 101 upgrade (a failed dial `t.Skipf`s rather than passing). |
| 4 | infra.WSConfig.Validate rejects a wildcard origin entry (D-24), matching ws.ServerConfig semantics | VERIFIED | `config.go:673-678` rejects `"*"`, wrapping `ErrConfigInvalid`. `TestWSConfigValidate/wildcard origin rejected` passes via `errors.Is(err, ErrConfigInvalid)`. ws layer mirrors at `server.go:43-46`. |
| 5 | Graceful shutdown follows CR-01-safe order: publicSrv.Shutdown THEN wsServer.Shutdown, within cfg.Shutdown.Timeout, with no goroutine leak | VERIFIED | `main.go:282-305` calls `publicSrv.Shutdown(shutdownCtx)` (282) BEFORE `wsServer.Shutdown(shutdownCtx)` (288), both within `shutdownCtx` (cfg.Shutdown.Timeout, `main.go:273`). `TestMainWSPublicRouteUpgradesAndShutsDownCleanly` exercises the full shutdown path and asserts goroutine `delta <= 2` (`main_test.go:300-303`) — passes. |
| 6 | go build ./... && go vet ./... && go test ./... all pass | VERIFIED | `go build ./...` → BUILD_OK; `go vet ./...` → VET_OK; `go test ./... -count=1` → all packages green on re-run (cmd/service, pkg/infra, pkg/pybridge, pkg/ws). See Anti-Patterns / Probe notes for one pre-existing flake in an unmodified package. |

**Score:** 6/6 truths verified (0 present, behavior-unverified)

Behavior-dependent truths (shutdown no-leak invariant in #5, listen/upgrade in #1/#3) are all exercised by passing behavioral integration tests in `cmd/service/main_test.go`, so none are left PRESENT_BEHAVIOR_UNVERIFIED.

### Required Artifacts

| Artifact | Expected | Status | Details |
| -------- | -------- | ------ | ------- |
| `pkg/infra/config.go` | WSConfig struct + DefaultConfig + Validate + bindEnv ws.* keys | VERIFIED | `WSConfig` (127-167); `Config.WS` field (113); `DefaultConfig().WS` (428-442); wildcard Validate (673-678); `bindEnvForAllLeaves` registers all 10 `ws.*` keys (763-772). |
| `pkg/infra/config_test.go` | WSConfig defaults + wildcard-rejection tests | VERIFIED | `TestDefaultConfig/ws defaults` (167-217) asserts origins/heartbeat/zeros/buffers; `TestWSConfigValidate` (559-590) covers wildcard rejection + default-passes. |
| `config.example.yaml` | ws: block documenting defaults + D-24 | VERIFIED | `ws:` block (129-173) documents: block optional, wildcard forbidden (D-24), port required (IN-03), zero-valued fields defer to ws defaults. |
| `cmd/service/handlers.go` | no-op handlers for MsgTypeAudio/MsgTypeControl | VERIFIED | `noOpHandlers()` (23-36) returns `map[ws.MessageType]ws.Handler` for both types; each closure `return nil`. Only imports `context` + `ws` — no audio/session/TTS/FSRS logic. |
| `cmd/service/main.go` | hub/router/wsServer construction + publicSrv on cfg.Server.Addr + /ws registration + CR-01-safe shutdown | VERIFIED | cfg.WS→3 ws config structs (149-176); `wsLogger` bridge (182-185); `ws.NewHub/NewRouter/NewServer` (187-210); `go hub.Run(ctx)` (214); publicMux + /ws (219-220); publicSrv on cfg.Server.Addr (221-239); CR-01 shutdown order (282-305). |
| `cmd/service/main_test.go` | WS upgrade integration test + goroutine-leak assertion | VERIFIED | `TestMainWSPublicRouteUpgradesAndShutsDownCleanly` (223-304) + `TestMainBootsWithNoWSConfigBlock` (310-340); both pass. |
| `pkg/infra/logging.go` | (deviation) `Zap()` accessor on `*ZapLogger` | VERIFIED | `Zap() *zap.Logger` method (236-238); concrete method, NOT on Logger interface — nopLogger/other implementers unaffected. |

### Key Link Verification

| From | To | Via | Status | Details |
| ---- | --- | --- | ------ | ------- |
| main.go | wsServer from cfg.WS | infra WSConfig → ws.ServerConfig + ws.HubConfig + ws.RouterConfig mapping; ws.ServerConfig.Validate re-runs | WIRED | `main.go:149-210`; `ws.NewServer` calls `cfg.Validate()` at `server.go:83`. `*ws.Router` satisfies `FrameHandler` via its `Route` method (`router.go:136` ↔ `connection.go:30-34`). |
| main.go | /ws registration | `mux.HandleFunc("/ws", wsServer.HandleWebSocket)` on publicSrv at cfg.Server.Addr | WIRED | `main.go:219-220`, `221-227`. |
| main.go | CR-01 shutdown ordering | publicSrv.Shutdown BEFORE wsServer.Shutdown | WIRED | `main.go:282` (publicSrv) → `288` (wsServer) → `294` (server) → `300` (healthSrv). |
| /health + /metrics | dedicated healthSrv | NOT merged into public mux | WIRED | publicMux carries ONLY /ws (`main.go:219`); /health + /metrics on healthMux/healthSrv (`main.go:244-265`). |

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
| -------- | ------- | ------ | ------ |
| Whole repo compiles | `go build ./...` | BUILD_OK | PASS |
| No vet warnings | `go vet ./...` | VET_OK | PASS |
| WS upgrade HTTP 101 + no-leak | `go test ./cmd/service/ -run TestMainWSPublicRouteUpgradesAndShutsDownCleanly -count=1 -v` | `--- PASS (0.26s)`; log `hub registered client total=1` proves upgrade | PASS |
| Boots with no ws: block | `go test ./cmd/service/ -run TestMainBootsWithNoWSConfigBlock -count=1 -v` | `--- PASS (0.30s)`; exit 0 | PASS |
| WSConfig defaults + wildcard rejection | `go test ./pkg/infra/ -run 'TestDefaultConfig|TestWSConfigValidate' -count=1 -v` | all subtests PASS | PASS |
| Full suite (re-run) | `go test ./... -count=1` | all packages ok | PASS |

### Probe Execution

No phase-declared probes (`scripts/*/tests/probe-*.sh`) or PASS markers asserted by this plan. Phase-level checks are the go build/vet/test commands above, all green.

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
| ----------- | ----------- | ----------- | ------ | -------- |
| P0-1 | 260801-ge1-PLAN.md | WS layer wired into runtime; service listens on :8080 with /ws | SATISFIED | Truths 1-6 + artifacts + key links above. `go run ./cmd/service` now boots a real WS server on cfg.Server.Addr with /ws live, bootable from defaults, CR-01-safe shutdown, no goroutine leak. |

No orphaned requirements found.

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
| ---- | ---- | ------- | -------- | ------ |
| `cmd/service/main.go` | 143 | "Phase 6/7 placeholders" (comment) | Info | Intentional — documents the deliberate no-op handler stance (plan objective). Not a defect. |
| `cmd/service/handlers.go` | 19,26,30 | "placeholder" (comments) | Info | Intentional — handlers are the documented Phase 6/7 substrate. Each handler `return nil`. |

No blocker-level debt markers (TBD/FIXME/XXX) in any modified file. The two "placeholder" references are the plan's explicit design ("Handlers are deliberately no-op"), not incomplete work.

**Pre-existing flake observed (INFO, not a gap):** On the first `go test ./... -count=1`, `pkg/ws`::`TestHubStopsOnContextCancel` failed once (`hub_test.go:210`, "Expected goroutine count to increase after starting hub.Run"). This test lives in `pkg/ws`, which this task did NOT modify, and its own comments (`hub_test.go:203-207`) document it as scheduler-sensitive ("flakes under full-suite load"). It passes 3/3 in isolation and passed on the full-suite re-run. Not a regression and out of scope for P0-1.

### Deviation Review (executor reported 2)

**Deviation (a) — infra.Logger / *zap.Logger type mismatch (Rule 3, blocking):** REAL and correctly resolved. `pkg/ws` constructors take `*zap.Logger` directly (`server.go:82`, `hub.go`, `router.go:98`), but `infra.Logger` is an interface with an unexported `l *zap.Logger` field (T-01-09 invariant, no accessor prior). Fix: added `Zap() *zap.Logger` concrete method on `*ZapLogger` (`logging.go:236-238`) — does NOT expand the `Logger` interface, so `nopLogger` and any other implementer are unaffected. `main.go:182-185` type-asserts `logger.(*infra.ZapLogger)` with a `zap.NewNop()` fallback for the test/no-op path. No new problem introduced; the configured level/format/output are preserved through the accessor.

**Deviation (b) — MaxMessageBytes 0→DefaultMaxMessageBytes bridge (Rule 1, bug):** REAL and correctly resolved. `ws.ServerConfig.Validate()` rejects `MaxMessageBytes <= 0` (`server.go:48-50`) and runs inside `NewServer` (`server.go:83`) BEFORE the `if cfg.MaxMessageBytes == 0 { fill }` branch (`server.go:104-106`) — so the fill branch is dead code for MaxMessageBytes when called via the public constructor. Without the bridge, `wsServer` construction would fail at boot with the infra default of 0. Fix: `main.go:166-169` bridges `if maxMsg == 0 { maxMsg = ws.DefaultMaxMessageBytes }` before building `serverCfg`. This preserves infra's 0-sentinel design (Task A's assertion `DefaultConfig().WS.MaxMessageBytes == 0` still holds) while letting `ws.ServerConfig.Validate` pass. `ReadBufferSize`/`WriteBufferSize` are unaffected because their Validate only rejects `< 0` (`server.go:58`), so 0 passes and the fill branch works. No new problem introduced.

### Scope Boundary Check

handlers.go is genuinely no-op: it imports only `context` and `github.com/xiaotian/home-tutor-agent/pkg/ws`; both `MsgTypeAudio` and `MsgTypeControl` closures `return nil`. No audio/session/TTS/FSRS/pybridge logic is present (grep matches are all in doc-comments). The public mux carries only `/ws`; `/health` and `/metrics` remain on the dedicated localhost `healthSrv`. Scope is respected.

### Gaps Summary

No gaps. All 6 must-have truths are verified against the actual source, all 7 artifacts exist and are substantive and wired, all 4 key links are connected, both deviations are real/necessary/correctly implemented, and requirement P0-1 is satisfied. The only test instability observed is a pre-existing goroutine-scheduling flake in the unmodified `pkg/ws` package (documented as such in its own source comments), which is out of scope for this quick task.

---

_Verified: 2026-08-01T04:27:23Z_
_Verifier: Claude (gsd-verifier)_
