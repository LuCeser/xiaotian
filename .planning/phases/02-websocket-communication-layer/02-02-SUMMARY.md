---
phase: 02-websocket-communication-layer
plan: 02
subsystem: WebSocket Connection Lifecycle
tags: [websocket, hub, goroutine, heartbeat, gorilla, tdd, manual-fakes]
dependency_graph:
  requires:
    - phase: 02-01
      provides: [ws-protocol, ws-codec, EncodeFrame, DecodeFrame, FrameHeader, MessageType]
  provides: [ws-hub, ws-client, ws-heartbeat, FrameHandler-interface, wstest-helpers]
  affects: [02-03]
tech_stack:
  added:
    - github.com/gorilla/websocket v1.5.3 (D-01, added in Plan 01)
    - go.uber.org/zap v1.28.0 (D-25, added in Plan 01)
    - github.com/pkg/errors v0.9.1 (D-05/D-44, added in Plan 01)
  patterns:
    - One-goroutine-per-connection-for-IO (D-16): independent readPump/writePump
    - Channel-routed lifecycle (D-17): bounded register/unregister channels
    - Constructor DI + centralized validation (D-42, D-44): NewHub/NewClient validate cfg, nil-logger substitution
    - Context-based lifecycle (D-12): Run/Heartbeat/pumps exit on ctx.Done()
    - Manual fakes (D-48): fakeConn + fake FrameHandler, no testify/mock
    - WebSocketConn interface seam (deviation): enables fake-conn unit testing
key_files:
  created:
    - internal/wstest/helpers.go (MustBuildTestFrame, TestAudioPayload, MustDialTestServer)
    - pkg/ws/hub.go (Hub: register/unregister/clients/100-limit/ctx-cleanup)
    - pkg/ws/hub_test.go (8 tests, all passing)
    - pkg/ws/connection.go (Client + readPump/writePump + FrameHandler + WebSocketConn)
    - pkg/ws/connection_test.go (10 tests, all passing, manual fakeConn)
    - pkg/ws/heartbeat.go (Heartbeat 30s Ping + DefaultHeartbeatInterval)
    - pkg/ws/heartbeat_test.go (3 tests, all passing)
  modified: []
decisions:
  - hub_self_contained: Hub does NOT depend on pkg/infra.ConnectionRegistry (Phase 1 Plan 06 not delivered). Maintains a self-contained clients map — RESEARCH.md Open Question 3 "self-contained minimal implementation" path, avoids Phase 2 blocking on Phase 1.
  - websocketconn_interface: Introduced WebSocketConn interface (ReadMessage/WriteMessage/WriteControl/Close) instead of plan's raw *websocket.Conn. Required for D-48 manual fakes — fakeConn implements the interface so readPump/writePump/Heartbeat are unit-testable without a real network socket. Client.Close signature became (code int, reason string) accordingly.
  - readpump_unregister_defer: readPump unregisters via defer guaranteeing every exit path triggers Hub.Unregister (WS-03 cleanup) — survives conn error, ctx cancel, and recovered panic.
patterns-established:
  - "Bounded channel routing: register/unregister/send channels all capacity-bounded (RESEARCH.md Pitfall 5 memory budget)"
  - "Goroutine-lifecycle test pattern: poll runtime.NumGoroutine() for start/drain instead of fixed time.Sleep (scheduler-stable)"
requirements-completed: [WS-03]
coverage:
  - id: D1
    description: "Hub 集中管理连接注册/注销，100 连接上限时拒绝新 Client 并发 CloseTryAgainLater，ctx.Done() 后清理退出"
    requirement: WS-03
    verification:
      - kind: unit
        ref: "pkg/ws/hub_test.go#TestHubRejectsAtCapacity"
        status: pass
      - kind: unit
        ref: "pkg/ws/hub_test.go#TestHubStopsOnContextCancel"
        status: pass
      - kind: unit
        ref: "pkg/ws/hub_test.go#TestHubConcurrentRegisterUnregister"
        status: pass
    human_judgment: false
  - id: D2
    description: "Client 独立 readPump/writePump goroutine，readPump defer 触发 Hub.Unregister，panic recovery 防崩溃，坏帧跳过不踢连接"
    requirement: WS-03
    verification:
      - kind: unit
        ref: "pkg/ws/connection_test.go#TestClientReadPumpUnregistersOnError"
        status: pass
      - kind: unit
        ref: "pkg/ws/connection_test.go#TestClientReadPumpPanicRecovery"
        status: pass
      - kind: unit
        ref: "pkg/ws/connection_test.go#TestClientWritePumpExitsOnChannelClose"
        status: pass
    human_judgment: false
  - id: D3
    description: "Heartbeat 每 30s 发送 Ping（DefaultHeartbeatInterval），ctx 取消或 Ping 失败时立即退出"
    verification:
      - kind: unit
        ref: "pkg/ws/heartbeat_test.go#TestHeartbeatTicksAt30s"
        status: pass
      - kind: unit
        ref: "pkg/ws/heartbeat_test.go#TestHeartbeatStopsOnContextCancel"
        status: pass
    human_judgment: false
  - id: D4
    description: "internal/wstest/helpers.go 提供 frame 构造 / 640B 音频 fixture / httptest 拨号，供 Plan 03 集成测试复用"
    verification:
      - kind: integration
        ref: "pkg/ws/integration_test.go (consumes MustBuildTestFrame/TestAudioPayload/MustDialTestServer)"
        status: pass
    human_judgment: false
metrics:
  duration: "close-out verification ~20 min (original implementation 2026-07-19)"
  completed_date: "2026-07-25"
  original_commit_date: "2026-07-19"
  tasks_completed: 3
  files_created: 7
  tests_passed: 21
status: complete
---

# Phase 02 Plan 02: WebSocket Connection Lifecycle Summary

## One-Liner

Hub/Client/Heartbeat connection lifecycle: bounded-channel registration with a 100-connection hard limit, per-connection read/write pumps with panic recovery and forced unregister, and a 30s Ping heartbeat — all context-driven with no goroutine leaks.

## Close-Out Note (manual收尾)

This plan's implementation was committed on 2026-07-19 (commits `440652c`, `8c2b553`) but the SUMMARY.md close-out was never written, leaving the plan marked incomplete. A safe-resume gate surfaced the discrepancy; the user chose **手动收尾 (close out manually)**. This SUMMARY documents the already-committed work after verification: all must-haves are present in source, and the test suite passes (see Deviation #1 for one flaky-test fix applied during close-out).

## Performance

- **Original implementation date:** 2026-07-19
- **Close-out date:** 2026-07-25
- **Tasks:** 3
- **Files created:** 7
- **Test functions:** 21 (hub 8 + connection 10 + heartbeat 3), all passing

## Accomplishments
- Hub with 100-connection capacity limit (D-04), rejecting the 101st client via `websocket.CloseTryAgainLater`, self-contained (no pkg/infra dependency)
- Client with independent readPump/writePump goroutines (D-16), panic recovery (D-49), and defer-guaranteed Hub.Unregister on every exit path (WS-03)
- All channels bounded (register/unregister/send) to honor the 150MB memory budget (RESEARCH.md Pitfall 5)
- Heartbeat 30s Ping aligned with Phase 1 D-57 cleanup cycle, exits cleanly on ctx cancel or Ping failure
- internal/wstest test infrastructure (MustBuildTestFrame / TestAudioPayload / MustDialTestServer) reusable by Plan 03

## Task Commits

Original implementation was committed atomically on 2026-07-19 (pre-close-out):

1. **Task 1: internal/wstest/helpers.go (test infrastructure)** — part of `440652c` (feat)
2. **Task 2: pkg/ws/hub.go (connection hub + 100 limit)** — part of `440652c` (feat)
3. **Task 3: pkg/ws/connection.go + heartbeat.go (single-conn I/O + 30s heartbeat)** — part of `440652c` (feat)

Follow-up fixes:
- `8c2b553` (fix, 02-02+02-03): fix readPump tests to register clients with Hub before starting

Close-out fix (this session, 2026-07-25):
- `test(02-02): stabilize TestHubStopsOnContextCancel` — see Deviation #1

## Files Created/Modified
- `internal/wstest/helpers.go` - Test frame builder, deterministic 640B audio payload, httptest dialer
- `pkg/ws/hub.go` - Hub: register/unregister/clients map, 100-connection limit, Run() ctx-driven loop
- `pkg/ws/hub_test.go` - 8 hub tests incl. capacity rejection, ctx-cleanup, concurrent -race-safe
- `pkg/ws/connection.go` - Client struct, FrameHandler interface, WebSocketConn seam, readPump/writePump/Close
- `pkg/ws/connection_test.go` - 10 connection tests with manual fakeConn + fake FrameHandler
- `pkg/ws/heartbeat.go` - Heartbeat() 30s Ping loop + DefaultHeartbeatInterval constant
- `pkg/ws/heartbeat_test.go` - 3 heartbeat tests (tick, ctx-cancel, ping-error exit)

## Decisions Made
- **Hub self-contained (no pkg/infra):** Phase 1 Plan 06 (ConnectionRegistry) not delivered; Hub maintains its own clients map per RESEARCH.md Open Question 3. Adapter/facade integration deferred to when Plan 06 lands.
- **WebSocketConn interface seam:** Plan specified `NewClient(conn *websocket.Conn, ...)`. Implementation introduced a `WebSocketConn` interface so a manual `fakeConn` (D-48) can drive readPump/writePump/Heartbeat in unit tests without a live socket. This shifted `Client.Close` to signature `(code int, reason string)`.
- **readPump unregister via defer:** every readPump exit path (conn error, ctx cancel, recovered panic) triggers Hub.Unregister — WS-03 cleanup cannot be skipped.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Test reliability] Stabilized flaky TestHubStopsOnContextCancel**
- **Found during:** close-out verification (2026-07-25)
- **Issue:** The test captured `baseGoroutines := runtime.NumGoroutine()`, started `go hub.Run(ctx)`, then asserted the count increased after a single `time.Sleep(50 * time.Millisecond)`. Under full-suite load the scheduler sometimes hadn't started the goroutine within 50ms, so `runningGoroutines <= baseGoroutines` fired — the suite flaked ~1/6 full runs. The production code (`hub.Run`) is correct; only the test's timing assertion was fragile.
- **Fix:** Replaced the fixed sleep with poll helpers `waitForGoroutineIncrease` / `waitForGoroutineDrain` (poll `runtime.NumGoroutine()` with a 500ms deadline + `runtime.Gosched()`). Preserves the test's intent (prove hub.Run blocks, then exits on cancel) while removing scheduler fragility. Verified stable: 8/8 full-suite runs pass after the fix.
- **Files modified:** pkg/ws/hub_test.go
- **Verification:** `go test -count=1 ./pkg/ws/` — 8 consecutive runs, 0 failures
- **Committed in:** this close-out commit

---

**Total deviations:** 1 auto-fixed (1 test reliability)
**Impact on plan:** No scope creep. The fix hardens an existing test to meet the plan's "stable passing" success criterion; production code unchanged.

### Documented deviations (no code change — verified acceptable)

- **WebSocketConn interface vs raw \*websocket.Conn:** introduced for D-48 manual-fake testability (see Decisions). Functionally equivalent; enables unit tests without network sockets.
- **No dedicated internal/wstest/helpers_test.go:** Plan Task 1 requested a dedicated helper-contract test file. The three helpers are instead exercised transitively through Plan 03's `integration_test.go` (TestIntegration_* consume MustBuildTestFrame/TestAudioPayload/MustDialTestServer). Coverage is real but indirect.

## Issues Encountered
- Safe-resume gate: production commits existed without SUMMARY.md. Resolved via user-approved manual close-out (verify → fix flaky test → document → update tracking).

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Hub/Client/Heartbeat delivered as a self-contained library ready for Plan 03 (Server + Router) to consume.
- `FrameHandler` interface (Route(ctx, *Client, *FrameHeader, []byte) error) is the seam Plan 03's Router implements.
- pkg/infra integration deferred (Open Question 3) — does not block Plan 03.

## Threat Model Compliance

| Threat | Status | Mitigation |
|--------|--------|------------|
| T-02-05 (Hub capacity DoS) | ✅ Mitigated | D-04 100-connection limit; TestHubRejectsAtCapacity verifies CloseTryAgainLater. |
| T-02-06 (Client.send unbounded) | ✅ Mitigated | ClientConfig.SendBuffer bounded; Pitfall 5 ~16KB/conn budget. |
| T-02-07 (malicious frame tampering) | ✅ Mitigated | DecodeFrame (Plan 01) rejects bad CRC/length; readPump skips bad frames without disconnecting. |
| T-02-08 (goroutine leak) | ✅ Mitigated | D-12 ctx cleanup; TestHubStopsOnContextCancel + pump exit tests verify all paths. |
| T-02-09 (panic stack disclosure) | ✅ Mitigated | D-49 panic recovery; stack logged server-side only, never echoed to client. |
| T-02-10 (unauthenticated client) | ⚠ Accepted | D-65 token auth deferred to later phase; relies on network layer + Plan 03 Origin whitelist. |

## Requirements Satisfied
- **WS-03 (session register + disconnect cleanup):** Complete. Hub register/unregister, Client defer-guaranteed unregister on every exit path, ctx-driven cleanup with no goroutine leaks.

## Verification Evidence
- ✅ `go build ./...` exits 0
- ✅ `go test -count=1 ./pkg/ws/ ./internal/wstest/` — 65 PASS / 0 FAIL (incl. subtests); 21 plan-scoped test functions
- ✅ `go vet ./pkg/ws/... ./internal/wstest/...` — no warnings
- ✅ 8/8 consecutive full-suite runs pass (flake resolved)
- ✅ hub.go does not import pkg/infra
- ✅ All channels bounded (register/unregister/send)
- ⚠️ `-race` skipped — Windows + no gcc/CGO prevents the race detector (same platform limitation as Plan 01); tests pass without -race

## Success Criteria
- ✅ Hub 实现注册/注销/100 连接上限/ctx 清理（D-04, D-12, D-17）
- ✅ Client 独立 read/write pump + panic recovery + 强制 unregister（D-16, D-49, WS-03）
- ✅ Heartbeat 30s 间隔 + ctx 清理（D-13, D-14）
- ✅ 所有 channel 有界（RESEARCH.md Pitfall 5 内存预算）
- ✅ internal/wstest/helpers.go 提供测试基础设施供 Plan 03 复用
- ⚠️ 全部单元测试在 -race 模式下通过（D-33）— 跳过 -race（Windows CGO/gcc 限制），无 -race 全绿且稳定

## Self-Check: PASSED

All created files exist in working tree, all commits verified, all must-have source assertions present, test suite stable-green. One flaky test hardened during close-out (documented above). No unexpected deletions or untracked files.

---
*Phase: 02-websocket-communication-layer*
*Implementation: 2026-07-19 · Close-out: 2026-07-25*
