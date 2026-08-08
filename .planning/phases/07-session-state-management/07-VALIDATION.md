---
phase: 7
slug: session-state-management
# status lifecycle: draft (seeded by plan-phase) → validated (set by validate-phase §6)
# audit-milestone §5.5 distinguishes NOT-VALIDATED (draft) from PARTIAL (validated + nyquist_compliant: false) (#2117)
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-08-08
---

# Phase 7 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Derived from `07-RESEARCH.md` §Validation Architecture (the authoritative requirement→test map).

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go testing (`go test`) + table-driven tests; recommended `go.uber.org/goleak` for goroutine-leak assertions (or `runtime.NumGoroutine()` diffing to avoid the dep — planner's discretion) |
| **Config file** | none — standard `go test`; tests live alongside source as `*_test.go` |
| **Quick run command** | `go test ./pkg/session/... ./pkg/infra/... ./pkg/ws/...` |
| **Full suite command** | `go test ./...` |
| **Estimated runtime** | ~15-30 seconds (session unit tests are fast; no network in unit layer) |

---

## Sampling Rate

- **After every task commit:** Run `go test ./pkg/session/... ./pkg/infra/... ./pkg/ws/...`
- **After every plan wave:** Run `go test ./...`
- **Before `/gsd-verify-work`:** Full suite must be green + manual end-to-end smoke (open `test-interface.html`, start session, play word, observe state transitions in server logs)
- **Max feedback latency:** 30 seconds

---

## Per-Task Verification Map

> Seeded from RESEARCH.md §Validation Architecture. Task IDs are provisional — the planner assigns final `{plan}-{wave}-{seq}` IDs. The planner MUST keep these test commands attached to the corresponding implementation tasks.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 7-W0-types | 01 | 0 | DICT-04/D-13 | — | N/A | unit (stub) | `go test ./pkg/session/ -run TestTypes` | ❌ W0 | ⬜ pending |
| 7-01-fsm | 01 | 1 | DICT-04 | — | N/A | unit (table-driven) | `go test ./pkg/session/ -run TestTransition -x` | ❌ W0 | ⬜ pending |
| 7-01-fsm-invalid | 01 | 1 | DICT-04 | — | invalid transition must NOT mutate state | unit (table-driven) | `go test ./pkg/session/ -run TestTransition_Invalid -x` | ❌ W0 | ⬜ pending |
| 7-02-init-due | 01 | 1 | DICT-02 | — | N/A | unit | `go test ./pkg/session/ -run TestNewSession_SelectDueCards -x` | ❌ W0 | ⬜ pending |
| 7-02-init-ttsfail | 01 | 1 | DICT-02/D-10 | — | init failure releases FSRS lock | unit | `go test ./pkg/session/ -run TestNewSession_TTSFailure_Unlocks -x` | ❌ W0 | ⬜ pending |
| 7-03-wordcomplete | 01 | 2 | DICT-04/D-13 | — | N/A | unit | `go test ./pkg/session/ -run TestEventLoop_WordComplete -x` | ❌ W0 | ⬜ pending |
| 7-03-lastword | 01 | 2 | DICT-04 | — | N/A | unit | `go test ./pkg/session/ -run TestEventLoop_LastWord -x` | ❌ W0 | ⬜ pending |
| 7-03-streamword | 01 | 2 | D-13 | — | N/A | unit | `go test ./pkg/session/ -run TestEventLoop_StreamWordCompletion -x` | ❌ W0 | ⬜ pending |
| 7-04-timeout | 01 | 2 | ERR-03 | — | N/A | unit (short timeouts) | `go test ./pkg/session/ -run TestEventLoop_Timeout -x` | ❌ W0 | ⬜ pending |
| 7-04-sweeper | 01 | 2 | ERR-03 | — | leaked session cancelled | unit | `go test ./pkg/session/ -run TestManager_Sweeper -x` | ❌ W0 | ⬜ pending |
| 7-04-noleak | 01 | 2 | ERR-03 | — | no goroutine leak across lifecycle | unit + goleak | `go test ./pkg/session/ -run TestSession_NoGoroutineLeak -x` | ❌ W0 | ⬜ pending |
| 7-05-exitdefer | 01 | 2 | D-10 | — | every exit path: UnlockCards + TTS cleanup + hub.Unregister | unit (subtests per exit path) | `go test ./pkg/session/ -run TestExitDefer -x` | ❌ W0 | ⬜ pending |
| 7-05-freshctx | 01 | 2 | D-10/D-11 | — | deferred unlock uses fresh ctx (survives run-ctx cancel) | unit | `go test ./pkg/session/ -run TestReleaseFSRSLock_FreshContext -x` | ❌ W0 | ⬜ pending |
| 7-05-norating | 01 | 2 | D-11/D-12 | — | exit does NOT call UpdateRatings | unit | `go test ./pkg/session/ -run TestExit_NoRatingCommit -x` | ❌ W0 | ⬜ pending |
| 7-06-metrics | 01 | 3 | D-04 | — | Hub counts feed ServerSnapshot.Connections | unit | `go test ./pkg/infra/ -run TestSnapshot_HubConnections -x` | ❌ W0 | ⬜ pending |
| 7-07-audit | 01 | 3 | D-06 | — | DB audit writes via WorkerPool.Submit | unit | `go test ./pkg/session/ -run TestAuditViaWorkerPool -x` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

> The `pkg/session` package does not exist yet — Wave 0 creates the package skeleton + test fakes so all subsequent tasks can build against stable seams.

- [ ] `pkg/session/types.go` — `SessionEvent`, `EventType`, `FrameSender` interface, sentinel errors (`ErrInvalidTransition`, `ErrNoDueWords`)
- [ ] `pkg/session/fsm.go` — `transition()` table
- [ ] `pkg/session/session.go` — `Session` struct + `run()` event loop + exit defer
- [ ] `pkg/session/manager.go` — `Manager` registry + sweeper
- [ ] `pkg/session/audit.go` — WorkerPool wrappers
- [ ] `pkg/session/*_test.go` — shared fakes (fake `FSRSService`, fake `SessionRepository`, fake `FrameSender`) + the tests above
- [ ] Framework add: `go.uber.org/goleak` to `go.mod` (the ONLY new dependency this phase should consider; verify legitimacy) — OR `runtime.NumGoroutine()` diffing to avoid the dep. Planner's discretion.
- [ ] Config: per-state timeout config (new `infra.SessionTimeoutConfig` or extend `SessionConfig`) + env bindings in `bindEnvForAllLeaves` (`[VERIFIED: config.go:902]`)

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| End-to-end dictation smoke (real TTS + WS) | DICT-02/DICT-04 | requires live TTS API + WS client + audio; unit layer uses fakes | Open `test-interface.html` (or equivalent) → start session → play word → verify server logs show IDLE→LISTENING→DICTATING transitions → let it time out → verify stale session removed + FSRS lock released |
| iPad Safari WS connectivity | ERR-03 (resource cleanup under real disconnect) | requires physical iPad + Safari; Phase 07.1 owns the iOS audio fix | Connect from iPad, start session, force-close Safari tab, verify sweeper removes the orphaned session within ~2× deadline |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references (pkg/session package + fakes)
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
