---
phase: 07-session-state-management
plan: 02
subsystem: infra
tags: [concurrency, event-loop, defer-chain, goroutine-leak, session-lifecycle, go]

# Dependency graph
requires:
  - phase: 07-session-state-management/01
    provides: pkg/session skeleton (Session struct, NewSession init, FSM transition(), Manager stub, audit stubs)
  - phase: 02-websocket-communication-layer
    provides: ws.Hub.Run pattern (for-select event loop mirror), ws.Hub.Unregister (idempotent)
  - phase: 06-tts-pipeline
    provides: tts.Streamer.StreamWord, tts.Generator.CleanupSession
provides:
  - Session.run() event loop — goroutine-per-session (D-02), for-select on events/time.After(deadline)/ctx.Done
  - D-10 exit defer chain — 6-defer LIFO: close(done) + panic-recovery + onExit + releaseFSRSLock + cleanupTTS + unregisterFromHub
  - D-13 StreamWord-completion side channel — server-owned TTS kickoff pushes WordComplete on return
  - releaseFSRSLock() with fresh context (Pitfall 1 avoidance) — UnlockCards survives cancelled run-ctx
  - Manager full implementation — map registry, CompareAndSwap Start, global sweeper backstop (deadline > 2× state timeout)
  - No-rating-commit invariant (D-11/D-12) — UpdateRatings never called on any exit path
  - SessionStreamer + SessionHub interface seams for testability
  - Exported accessors: State() (RLock-protected), Deadline() (RLock-protected), Done() (<-chan struct{})
affects: [07-03]

# Actuals
actuals:
  tokens: 18000
  tasks: 3
  commits: 2

# Tech tracking
tech-stack:
  added: []
  patterns: [defer-chain-LIFO-ordering, fresh-context-in-defer, goroutine-per-session-event-loop, CompareAndSwap-start-guard, deadline-driven-timeout, sweeper-backstop, interface-seam-for-testability, polling-based-test-assertion]

key-files:
  modified:
    - pkg/session/session.go
    - pkg/session/fsm.go
    - pkg/session/manager.go
    - pkg/session/fakes_test.go
    - pkg/session/session_test.go
  created:
    - pkg/session/manager_test.go

key-decisions:
  - "SessionStreamer interface: *tts.Streamer is concrete; defined narrow interface (StreamWord with tts.Sender param) so tests inject a fake streamer (same D-05/D-01 seam pattern as FrameSender/TTSSessionGenerator)"
  - "SessionHub interface: *ws.Hub is concrete; defined narrow interface (Unregister only) so tests inject a fake hub for exit-defer assertion"
  - "mu sync.RWMutex on Session protecting state+deadline: transition() takes WLock, State()/Deadline() take RLock. Enables polling-based test assertions that are race-free even without -race (CGO unavailable on this Windows machine)"
  - "done chan struct{} closed by the FIRST-registered defer in run() (runs LAST): gives tests a deterministic exit signal via Done() accessor"
  - "D-10 defer chain is 6 defers in LIFO order: close(done)→panic-recovery→onExit→releaseFSRSLock→cleanupTTS→unregisterFromHub. Execution order: unregister→cleanupTTS→releaseFSRSLock→onExit→panic-recovery→close(done)"
  - "D-13 StreamWord goroutine uses context.WithCancel(s.runCtx) so disconnect cancels the in-flight stream; on return pushes WordComplete non-blocking with runCtx.Done guard"
  - "fakeStreamer returns instantly, so LISTENING state races past before polling catches it — tests assert DICTATING directly (proves both IDLE→LISTENING and LISTENING→DICTATING)"

patterns-established:
  - "Fresh-context-in-defer: deferred cleanup that calls external services MUST use context.WithTimeout(Background,...) because the run-loop ctx is cancelled before defer fires (Pitfall 1)"
  - "Sweeper-backstop with onExit callback: sweeper detects leaked goroutines (deadline > 2× timeout) but normally-exited sessions are removed by an onExit defer callback — the sweeper's job stays narrow"
  - "Polling-based test assertion over shared state: when event-loop goroutine writes and test goroutine reads, use RLock/WLock + poll loop (2ms interval) instead of sleep-based timing"

requirements-completed: [DICT-04, ERR-03]

# Coverage metadata
coverage:
  - id: D5
    description: "Session.run() event loop processes events through the FSM, drives D-13 system transitions, and exits on timeout/cancel"
    requirement: DICT-04
    verification:
      - kind: unit
        ref: "pkg/session/session_test.go#TestEventLoop_WordComplete"
        status: pass
      - kind: unit
        ref: "pkg/session/session_test.go#TestEventLoop_LastWord"
        status: pass
      - kind: unit
        ref: "pkg/session/session_test.go#TestEventLoop_Timeout"
        status: pass
    human_judgment: false
  - id: D6
    description: "D-10 defer chain fires on every exit path (normal/timeout/cancel) with UnlockCards + CleanupSession + hub.Unregister"
    requirement: ERR-03
    verification:
      - kind: unit
        ref: "pkg/session/session_test.go#TestExitDefer_Timeout"
        status: pass
      - kind: unit
        ref: "pkg/session/session_test.go#TestExitDefer_Cancel"
        status: pass
      - kind: unit
        ref: "pkg/session/session_test.go#TestExitDefer_NormalCompletion"
        status: pass
    human_judgment: false
  - id: D7
    description: "releaseFSRSLock uses fresh context — UnlockCards receives a live ctx even when run-ctx is cancelled (Pitfall 1)"
    requirement: ERR-03
    verification:
      - kind: unit
        ref: "pkg/session/session_test.go#TestReleaseFSRSLock_FreshContext"
        status: pass
    human_judgment: false
  - id: D8
    description: "No exit path calls UpdateRatings — no partial-progress commit (D-11/D-12)"
    requirement: ERR-03
    verification:
      - kind: unit
        ref: "pkg/session/session_test.go#TestExit_NoRatingCommit"
        status: pass
    human_judgment: false
  - id: D9
    description: "Manager sweeper cancels leaked sessions (deadline > 2× state timeout) and no goroutine leaks across the lifecycle"
    requirement: ERR-03
    verification:
      - kind: unit
        ref: "pkg/session/manager_test.go#TestManager_Sweeper"
        status: pass
      - kind: unit
        ref: "pkg/session/manager_test.go#TestSession_NoGoroutineLeak"
        status: pass
    human_judgment: false

# Metrics
duration: ~45min
completed: 2026-08-08
status: complete
---

# Plan 07-02: Session event loop + D-10 defer chain + Manager sweeper Summary

**Full Session lifecycle implementation — goroutine-per-session event loop, 6-defer D-10 exit chain with fresh-context unlock, Manager registry with global sweeper backstop**

## Performance

- **Duration:** ~45 min
- **Tasks:** 3
- **Files modified:** 6 (5 modified, 1 created)

## Accomplishments
- Session.run() event loop mirrors Hub.Run (for-select on events/time.After(deadline)/ctx.Done), processing FSM transitions and driving D-13 StreamWord-completion side channel
- D-10 exit defer chain (6 defers in LIFO) fires on every exit path (normal/timeout/cancel/panic) — hub.Unregister → cleanupTTS → releaseFSRSLock → onExit → panic-recovery → close(done)
- releaseFSRSLock uses a fresh context.WithTimeout(Background, 10s) so UnlockCards survives the cancelled run-loop ctx (Pitfall 1)
- No exit path calls UpdateRatings (D-11/D-12) — verified by TestExit_NoRatingCommit
- Manager full implementation: map registry, CompareAndSwap Start guard, global sweeper that cancels sessions with deadline > 2× state timeout (ERR-03 leak backstop)
- onExit callback removes the map entry the instant run() exits — sweeper's job stays narrow (detect stuck goroutines only)
- No goroutine leaks across the full Session lifecycle (TestSession_NoGoroutineLeak with NumGoroutine diffing)

## Task Commits

1. **Task 1+2+3: Session event loop + D-10 defer chain + Manager sweeper** - `01f823f` (feat)

## Files Modified/Created
- `pkg/session/session.go` - Full rewrite: Start/run/Push/handleEvent/onTransition/spawnStreamWord/releaseFSRSLock/cleanupTTS/unregisterFromHub + SessionStreamer/SessionHub interfaces + mu/done/streamCancel fields
- `pkg/session/fsm.go` - transition() wrapped in mu.Lock for -race cleanliness
- `pkg/session/manager.go` - Full rewrite: map registry + atomic.Bool + sweep/cleanupLeaked/NewSession/Lookup/Push/removeSession
- `pkg/session/fakes_test.go` - Added fakeStreamer, fakeHub, CleanupCalls to fakeTTSGenerator
- `pkg/session/session_test.go` - Added event loop tests (WordComplete, LastWord, Timeout, StreamWordCompletion) + exit defer tests (Timeout, Cancel, NormalCompletion) + FreshContext + NoRatingCommit
- `pkg/session/manager_test.go` - NEW: TestManager_Sweeper, TestManager_Sweeper_NotLeaked, TestManager_Lifecycle_OnExit, TestSession_NoGoroutineLeak

## Decisions Made
- Added SessionStreamer + SessionHub interface seams (not in plan) for testability — concrete types (*tts.Streamer, *ws.Hub) have private fields
- mu sync.RWMutex on Session: enables polling-based test assertions that are race-free without CGO (unavailable on this Windows machine)
- done chan struct{} closed by first-registered defer (runs last): deterministic exit signal for tests via Done()
- D-13 StreamWord goroutine uses context.WithCancel(runCtx) so disconnect cancels in-flight stream

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Test correctness] D-13 StreamWord races past LISTENING before polling observes it**
- **Found during:** Task 1 (event loop tests)
- **Issue:** fakeStreamer returns instantly, so the D-13 goroutine pushes WordComplete and transitions LISTENING→DICTATING before the test's 2ms polling loop catches the intermediate LISTENING state.
- **Fix:** Removed intermediate waitForState(LISTENING) calls. Tests assert DICTATING directly (which proves both IDLE→LISTENING and LISTENING→DICTATING transitions happened) + StreamCalls>=1 (proves StreamWord was invoked).
- **Files modified:** pkg/session/session_test.go (TestEventLoop_WordComplete, LastWord, StreamWordCompletion, ExitDefer_NormalCompletion)
- **Verification:** All 26 tests green

**2. [Rule 1 - Test correctness] TestManager_Sweeper boundary precision**
- **Found during:** Task 3 (manager sweeper test)
- **Issue:** The sweeper check `time.Since(deadline) > 2*timeoutFor(state)` was borderline when both sides were ~60 minutes (30m default × 2 = 60m ≈ 1h expired deadline).
- **Fix:** Set cfg.SessionTimeout.IdleStateTimeout = 1ms so 2× = 2ms, easily exceeded by the 1-hour-old deadline.
- **Files modified:** pkg/session/manager_test.go (TestManager_Sweeper)
- **Verification:** TestManager_Sweeper green

---

**Total deviations:** 2 auto-fixed (both Rule 1 test correctness — timing-sensitive assertions adjusted to match real async behavior)
**Impact on plan:** No scope creep. Both fixes make tests match actual runtime semantics.

## Issues Encountered
- CGO_ENABLED=1 fails (no gcc on this Windows machine), so `-race` flag unavailable. Race safety is handled structurally via sync.RWMutex on state+deadline fields.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- pkg/session is fully functional (event loop + exit defer + Manager + sweeper) — ready for Plan 07-03 composition-root wiring
- Plan 07-03 wires: D-04 metrics shim (Hub.Stats → ServerSnapshot), D-06 WorkerPool audit wiring (first real Submit caller), handleStartSession/handlePlayWord delegation to Manager
- **Pre-existing bug flag:** `cmd/service` integration test has `concurrent map read and map write` in pkg/ws/router.go:205 (out of scope for Phase 07). Plan 07-03's final `go test ./...` may fail on this regardless of Phase 07 correctness.

---
*Phase: 07-session-state-management*
*Completed: 2026-08-08*
