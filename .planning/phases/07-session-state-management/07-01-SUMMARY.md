---
phase: 07-session-state-management
plan: 01
subsystem: infra
tags: [fsm, go, session-lifecycle, fsrs-lock, config]

# Dependency graph
requires:
  - phase: 05-fsrs-core-implementation
    provides: FSRSService interface (SelectDueCards/UnlockCards), SelectOpts/UnlockOpts
  - phase: 06-tts-pipeline
    provides: tts.Generator (GenerateForSession/CleanupSession), tts.Streamer (StreamWord)
  - phase: 02-websocket-communication-layer
    provides: ws.Hub, ws.Client, ws.MessageType enum pattern
  - phase: 04-database-schema-design
    provides: database.SessionState constants, SessionRepository interface, Session/SessionEvent structs
provides:
  - pkg/session package skeleton (doc.go, types.go, fsm.go, session.go, manager.go stub, audit.go)
  - FrameSender interface seam (D-05) — import-cycle-safe Sender satisfied structurally by *ws.Client
  - TTSSessionGenerator interface seam — narrow seam for testability (fake injection)
  - EventType enum (5 values) + SessionEvent struct + State type alias
  - Sentinel errors: ErrInvalidTransition, ErrNoDueWords, ErrSessionClosed, ErrManagerAlreadyRunning
  - FSM transition() table — flat switch(state,event), ~15 transitions, D-03
  - Session struct + NewSession init path — FSRS lock acquisition, lock-scope capture (D-11), TTS-fail non-defer unlock (D-10 init)
  - infra.SessionTimeoutConfig — 5 per-state timeouts wired into config validation + env binding (D-09)
  - Shared test fakes: fakeFSRSService, fakeSessionRepository, fakeFrameSender, fakeTTSGenerator
affects: [07-02, 07-03]

# Actuals
actuals:
  tokens: 14000
  tasks: 3
  commits: 2

# Tech tracking
tech-stack:
  added: []
  patterns: [interface-seam-for-testability, flat-switch-fsm, sentinel-error-with-errors.Is, non-defer-unlock-on-init-failure]

key-files:
  created:
    - pkg/session/doc.go
    - pkg/session/types.go
    - pkg/session/fsm.go
    - pkg/session/session.go
    - pkg/session/manager.go
    - pkg/session/audit.go
    - pkg/session/fakes_test.go
    - pkg/session/fsm_test.go
    - pkg/session/session_test.go
    - pkg/session/types_test.go
  modified:
    - pkg/infra/config.go

key-decisions:
  - "TTSSessionGenerator interface seam: *tts.Generator is concrete with private fields; defined narrow interface in types.go so tests can inject a fake (same pattern as FrameSender, D-05/D-01)"
  - "Exported accessors (ID/State/WordQueueLen/LockCategory/LockUnit) on Session: fields are all unexported; external tests (package session_test) and Plan 03 handlers need them"
  - "fsm_test.go is internal (package session); session_test.go is external (package session_test): internal tests access unexported transition()+fields directly, external tests verify via fakes+accessors"
  - "NewSession nil-guards ONLY fsrsSvc + gen (the deps the init path uses); streamer/hub/repo/pool/client may be nil for tests"
  - "auditCreate/auditState/auditEvent are no-op when pool==nil (early return) so tests with nil pool work"
  - "dbID stays 0: async repo.Create via pool.Submit can't safely write back to s.dbID (D-02 single-writer); audit is best-effort (D-06)"

patterns-established:
  - "Interface seam for import-cycle avoidance: FrameSender/TTSSessionGenerator defined in pkg/session, satisfied structurally by external concrete types"
  - "Non-defer unlock on init failure: when a resource is acquired before the run-loop defer chain is installed, failure must release on a non-defer path (D-10 init sub-invariant)"
  - "Flat typed-enum switch for FSM: switch(state){ switch(event) } — NOT state-pattern interface, NOT external FSM library (D-03)"

requirements-completed: [DICT-02, DICT-04]

# Coverage metadata
coverage:
  - id: D1
    description: "pkg/session package skeleton exports FrameSender, EventType, SessionEvent, sentinels, Session, NewSession, transition(), Manager stub, audit methods"
    requirement: DICT-04
    verification:
      - kind: unit
        ref: "pkg/session/types_test.go#TestTypes"
        status: pass
    human_judgment: false
  - id: D2
    description: "FSM transition() table covers all user/system-driven transitions + invalid-pair rejection with wrapped ErrInvalidTransition"
    requirement: DICT-04
    verification:
      - kind: unit
        ref: "pkg/session/fsm_test.go#TestTransition"
        status: pass
      - kind: unit
        ref: "pkg/session/fsm_test.go#TestTransition_Invalid"
        status: pass
      - kind: unit
        ref: "pkg/session/fsm_test.go#TestTransition_LastWord"
        status: pass
    human_judgment: false
  - id: D3
    description: "NewSession acquires FSRS fetch-lock via SelectDueCards, captures lockCategory/lockUnit for exit replay (D-11), releases on TTS failure via non-defer UnlockCards (D-10 init), returns ErrNoDueWords on empty (D-02-a)"
    requirement: DICT-02
    verification:
      - kind: unit
        ref: "pkg/session/session_test.go#TestNewSession_SelectDueCards"
        status: pass
      - kind: unit
        ref: "pkg/session/session_test.go#TestNewSession_EmptyDue"
        status: pass
      - kind: unit
        ref: "pkg/session/session_test.go#TestNewSession_TTSFailure_Unlocks"
        status: pass
    human_judgment: false
  - id: D4
    description: "SessionTimeoutConfig with 5 per-state timeouts wired into Config.Validate() + 5 env bindings (D-09)"
    requirement: DICT-04
    verification:
      - kind: unit
        ref: "go build ./pkg/infra/... succeeds (SessionTimeoutConfig compiles + validates)"
        status: pass
    human_judgment: false

# Metrics
duration: ~35min
completed: 2026-08-08
status: complete
---

# Plan 07-01: pkg/session skeleton + FSM + NewSession init Summary

**pkg/session package with flat-switch FSM transition table, FSRS-lock-acquiring NewSession init path, and per-state timeout config wired into infra**

## Performance

- **Duration:** ~35 min
- **Tasks:** 3
- **Files modified:** 11 (10 created, 1 modified)

## Accomplishments
- pkg/session package established as the orchestration seam binding ws/fsrs/tts/database/infra, with import-cycle-safe interface seams (FrameSender, TTSSessionGenerator)
- Flat switch(state,event) FSM transition table encoding all ~15 user/system-driven transitions (D-03), rejecting invalid pairs with wrapped ErrInvalidTransition
- NewSession init path acquires the FSRS fetch-lock, captures lockCategory/lockUnit for exit replay (Pitfall 4), generates TTS synchronously (Pitfall 3), and releases the lock via non-defer UnlockCards on TTS failure (D-10 init path)
- infra.SessionTimeoutConfig with 5 per-state timeouts wired into config validation + env binding (D-09)

## Task Commits

Each task was committed atomically:

1. **Task 1: pkg/session package skeleton (types + config + fakes)** - `efe1b57` (feat)
2. **Task 2+3: FSM transition table + Session init path + manager/audit stubs** - `b056ee6` (feat)

## Files Created/Modified
- `pkg/session/doc.go` - Package doc (orchestration seam)
- `pkg/session/types.go` - FrameSender, TTSSessionGenerator, EventType, SessionEvent, State alias, sentinel errors
- `pkg/session/fsm.go` - transition(ev) flat switch(state,event) table (D-03)
- `pkg/session/session.go` - Session struct + NewSession init path + accessors + timeoutFor/newSessionID helpers
- `pkg/session/manager.go` - Compilable Manager stub (Plan 02 fills map+sweeper)
- `pkg/session/audit.go` - pool.Submit wrappers for Create/UpdateState/AppendEvent (D-06)
- `pkg/session/fakes_test.go` - Shared fakes: fakeFSRSService, fakeSessionRepository, fakeFrameSender, fakeTTSGenerator
- `pkg/session/types_test.go` - TestTypes (EventType, String, sentinels, FrameSender)
- `pkg/session/fsm_test.go` - TestTransition/TestTransition_Invalid/TestTransition_LastWord/TestTransition_TimeoutCancelReturnNil (internal package)
- `pkg/session/session_test.go` - TestNewSession_SelectDueCards/EmptyDue/TTSFailure_Unlocks/NilGuards
- `pkg/infra/config.go` - SessionTimeoutConfig struct + Validate() + Config.SessionTimeout field + DefaultConfig + env bindings

## Decisions Made
- Added TTSSessionGenerator interface seam (not in plan) for testability — *tts.Generator is concrete with private fields
- Exported accessors (ID/State/WordQueueLen/LockCategory/LockUnit) for external tests + Plan 03 handlers
- fsm_test.go internal (package session) to access unexported transition()+fields; session_test.go external
- NewSession nil-guards only fsrsSvc+gen; audit methods no-op when pool==nil

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Correctness] env binding keys use `sessionTimeout.*` not plan's literal `session.*`**
- **Found during:** Task 1 (config.go env binding)
- **Issue:** Plan specified env keys `session.idleStateTimeout` etc., but the Config struct field is tagged `mapstructure:"sessionTimeout"`. Under mapstructure, binding keys MUST match the field path `sessionTimeout.*` or the binding is INERT (values silently never load — a Pitfall 6-class bug).
- **Fix:** Used `sessionTimeout.idleStateTimeout` etc. (env vars `HTA_SESSIONTIMEOUT_*`). The acceptance grep for `"session.idleStateTimeout"` returns 0 by design.
- **Files modified:** pkg/infra/config.go
- **Verification:** `grep -c "sessionTimeout.idleStateTimeout" pkg/infra/config.go` returns 1
- **Committed in:** efe1b57 (Task 1 commit)

**2. [Rule 1 - Testability] TTSSessionGenerator interface seam added**
- **Found during:** Task 3 (NewSession TTS-failure test)
- **Issue:** Plan assumed a fake tts.Generator, but *tts.Generator is concrete with private fields — un-fakeable from external tests.
- **Fix:** Defined narrow TTSSessionGenerator interface in types.go; Session.gen field uses the interface. *tts.Generator satisfies it structurally.
- **Files modified:** pkg/session/types.go, pkg/session/session.go, pkg/session/fakes_test.go (fakeTTSGenerator)
- **Verification:** TestNewSession_TTSFailure_Unlocks passes with fakeTTSGenerator
- **Committed in:** b056ee6 (Task 2+3 commit)

---

**Total deviations:** 2 auto-fixed (both Rule 1 correctness/testability)
**Impact on plan:** Both deviations necessary for correctness (env binding) and testability (TTS fake). No scope creep.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required. Env vars HTA_SESSIONTIMEOUT_* are optional (defaults: 30m/30s/30s/30m/5m per D-09).

## Next Phase Readiness
- pkg/session seams (types, FSM, Session init, Manager stub, audit stubs) stable for Plan 07-02 to build on
- Plan 07-02 implements: Session.run() event loop (goroutine-per-session D-02), D-10 exit defer chain (fresh-ctx UnlockCards), Manager full (map+sweeper+goroutine-leak test)
- Session.runCtx/cancel/onExit fields already present (set by Plan 02's Start method)

---
*Phase: 07-session-state-management*
*Completed: 2026-08-08*
