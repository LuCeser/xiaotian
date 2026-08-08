# Phase 7: Session State Management - Research

**Researched:** 2026-08-08
**Domain:** Go backend — session orchestration, channel-based concurrency, FSRS lock lifecycle, WS/TTS coordination
**Confidence:** HIGH

## Summary

Phase 7 introduces a single new package, `pkg/session`, that becomes the **orchestration seam** binding the existing leaf services (`pkg/ws`, `pkg/fsrs`, `pkg/tts`, `pkg/database`, `pkg/infra`) into a working dictation session. The implementation reuses **four already-proven codebase patterns verbatim** — the `ws.Hub.Run` channel-select event loop, the `tts.Sender` import-cycle seam, the `infra.ConnectionRegistry.cleanupLoop` sweeper, and the `infra.WorkerPool` task queue — so the risk is integration-wiring risk, not novel-algorithm risk. No new external libraries are introduced (D-03 explicitly rejects an FSM library); every component is Go stdlib + existing in-repo packages.

The single most important discovery from reading the actual code: **the P0-2 "dual-track connection registry" is already half-resolved.** `ws.Hub` is *already* the WS-accept gate (`HandleWebSocket` calls `hub.Register`, never `ConnectionRegistry.Register`), and `ConnectionRegistry.Register()` has **zero production callers** — only test code calls it. So D-04's "remove ConnectionRegistry from the accept path" is *already done*. The genuine D-04 work that remains is narrower and lower-risk than CONTEXT.md/DESIGN-ANALYSIS.md imply: (1) feed Hub connection counts into the `ServerSnapshot.Connections` metric (currently always reports `Active: 0`), and (2) stop the dead `ConnectionRegistry` cleanup goroutine from sweeping an empty map. The one-way blast radius is therefore limited to the metrics contract, not the accept path.

The two genuine one-way invariants the planner must encode as explicit tasks are **D-10** (every exit path runs `UnlockCards` + `CleanupSession` + `hub.Unregister` via a single deferred chain in the event loop) and **D-11** (lock-at-init / unlock-on-exit without committing partial progress). The FSRS `UnlockCards` API is **category+unit scoped, not card-ID scoped** — the Session must remember the exact `Category`/`Unit` used at `SelectDueCards` time and replay them at unlock; this is a non-obvious constraint the planner must not miss.

**Primary recommendation:** Build `pkg/session` as a `Session` struct + `Manager` registry, with `Session.run(ctx)` mirroring `Hub.Run` exactly (`select` on `events chan SessionEvent` + `time.After(time.Until(deadline))` + `ctx.Done()`), a flat `switch(state, event)` FSM table, and a `defer` chain that unconditionally releases the FSRS lock + TTS cache + WS registration. Wire DB audit writes through `infra.WorkerPool.Submit` (its first real caller). Add a metrics shim so Hub counts replace the dead registry counts.

## Critical Path Correction (read before planning)

The CONTEXT.md `<canonical_refs>` and the DESIGN-ANALYSIS.md reference integration points under **`internal/`** and **`cmd/xiaotian/`**. **These paths do not exist.** The real codebase lives under `pkg/` and `cmd/service/`. Every code citation below uses the real paths. The planner and executor MUST use the real paths:

| Referenced (wrong) | Real path |
|---|---|
| `internal/ws/hub.go` | `pkg/ws/hub.go` |
| `internal/ws/client.go` | `pkg/ws/connection.go` |
| `internal/ws/router.go` | `pkg/ws/router.go` |
| `internal/infra/connection_registry.go` | `pkg/infra/connection.go` |
| `internal/infra/worker_pool.go` | `pkg/infra/worker.go` |
| `internal/fsrs/` | `pkg/fsrs/service.go` |
| `internal/tts/streamer.go` | `pkg/tts/streamer.go` |
| `internal/database/` | `pkg/database/` |
| `cmd/xiaotian/main.go` | `cmd/service/main.go` |

Module path (go.mod line 1, verified): `github.com/xiaotian/home-tutor-agent`, Go 1.26. `[VERIFIED: go.mod]`

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

**Architecture: package & dominant shape**
- **D-01:** Introduce a new `pkg/session` package that owns the session state machine, per-session word queue, timeout lifecycle, and a channel-based event loop modeled on `ws.Hub.Run`. The Session struct holds: current state, word queue + current word index, per-state deadline, timer handles, `*ws.Client` reference, and FSRS/TTS/DB service handles. — **Reversibility:** costly.
- **D-02:** Concurrency model = **one goroutine per session** running an event loop (`select` on an `events chan SessionEvent` + deadline timer + `ctx.Done()`). The event loop is the ONLY writer to session state — no mutex needed on Session fields. Modeled exactly on `ws.Hub.Run`. Event types: `ControlFrame`, `WordComplete`, `Timeout`, `Cancel`. ~100 goroutines at the 100-session limit is well within Go's budget.
- **D-03:** FSM model = **flat typed-enum `switch(state, event)`** transition table in `pkg/session`. The 5-state machine has ~15 valid transitions. Reuses the already-defined `database.SessionState` string enum.

**Connection binding & P0-2 unification**
- **D-04:** **Phase 7 owns the P0-2 unification.** Session stores a `*ws.Client` reference. `ws.Hub` becomes the single source of truth for active connections. `infra.ConnectionRegistry` is **soft-deprecated**: removed from the WS-upgrade accept path, kept temporarily for the health/metrics endpoint (a shim feeds Hub stats into the existing `ServerSnapshot.Connections` field). **The planner should emit a `checkpoint:decision` before the task that removes ConnectionRegistry from the accept path.** — **Reversibility:** one-way.
- **D-05:** `pkg/session` must NOT import `pkg/ws` directly for framing — use the same interface-seam pattern as `tts.Sender` (a `FrameSender` interface with `Send([]byte) error`) to avoid the import cycle. `ws.Client` satisfies it structurally.

**Session storage**
- **D-06:** **Hybrid storage: in-memory Session struct is the authority; DB is audit-only.** `database.SessionRepository` records lifecycle events and state changes via `Create` / `UpdateState` / `AppendEvent` as **fire-and-forget** (spawned on `infra.WorkerPool` — this also gives the currently-unused WorkerPool a purpose, addressing P1-3). The in-memory Session is NOT reconstructed from DB on restart.
- **D-07:** **No restart resilience required.** Sessions do NOT survive a process restart. DB persistence is audit/trail only.

**Timeout & cleanup (ERR-03)**
- **D-08:** **Hybrid timeout strategy.** Each session carries a per-state `deadline time.Time` updated on every `transition()`. The session event loop selects on `time.After(time.Until(deadline))`. A **global sweeper goroutine** (mirror `ConnectionRegistry.cleanupLoop`, ~30s tick) is a backstop that catches sessions whose goroutine leaked (lastHeartbeat > 2× deadline).
- **D-09:** Per-state timeout defaults (configurable via config, not hardcoded): IDLE=30min, LISTENING=30s, DICTATING=30s, WAITING_PARENT=30min, FEEDBACK=5min.
- **D-10:** **Critical invariant — every session exit path (normal completion, timeout, disconnect, error) MUST release the FSRS lock.** The session event loop's `defer` calls `fsrsSvc.UnlockCards()`, TTS cleanup (`gen.CleanupSession`), and `hub.Unregister(client)`.

**FSRS word-lock integration (DICT-02)**
- **D-11:** **Lock at session init, unlock on session end/timeout — WITHOUT committing partial progress.** `SelectDueCards` (with `FetchLockHours`) acquires the lock at session start. On any exit, `UnlockCards` releases it. Committing default "hard" ratings for incomplete words is explicitly REJECTED.
- **D-12:** **All rating/feedback logic is deferred to Phase 10.** Phase 7 provides the lock/unlock infrastructure and holds the word list + completion records, but does NOT auto-commit any default rating at FEEDBACK→IDLE.

**TTS ↔ state machine coordination**
- **D-13:** **System-driven state transitions via the `word_complete` status frame.** `tts.Streamer.StreamWord()` already emits a `word_complete` JSON status frame on completion — the session event loop intercepts it and transitions LISTENING→DICTATING automatically. Client control frames: `play_word` (start/replay), `next_word` (advance: DICTATING→LISTENING for next word). The session owns the word queue and current index. `ws.MsgTypeSession` (0x05, currently unused) is available for session-specific control messages.

**Phase ordering**
- **D-14:** **Phase 7 precedes Phase 07.1** (iOS WebSocket audio fix). 07.1 depends on Phase 7.

### Claude's Discretion
- Exact internal struct field layout and method signatures in `pkg/session` — follow existing codebase conventions (struct + methods, sentinel errors, DI via constructor like `NewBridge`/`NewFSRSService`).
- Whether the global sweeper reuses `ConnectionRegistry.cleanupLoop` code or is a fresh goroutine in `pkg/session`.
- Test strategy (table-driven transition tests mirroring existing test style).

### Deferred Ideas (OUT OF SCOPE)
- **Per-word FSRS locking** (pi option B, Gray Area 5) — too many Python subprocess calls on 1C/1G VPS (MaxConcurrent=2).
- **Session restart resilience / reconstruction from DB** — explicitly deferred; v1 does not require mid-dictation resume across restarts.
- **Default-rating fast-path commit at session end** — deferred to Phase 10.
- **Client-driven state transitions** (explicit `word_done` frames) — deferred; system-driven auto-transition is the v1 choice.
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| DICT-02 | System initializes session with selected due words | `fsrs.FSRSService.SelectDueCards` acquires the fetch-lock (§ FSRS Lock Integration); Session init calls it then holds the `[]pybridge.DueCard` as the word queue (§ Architecture Patterns / Session struct) |
| DICT-04 | System maintains session state (IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK) | Flat `switch(state, event)` FSM reusing `database.SessionState` enum (§ FSM Transition Table); ~15 enumerated transitions |
| ERR-03 | System implements timeout-based session cleanup | Hybrid timeout: per-state `deadline` in event loop `time.After` arm + global sweeper backstop (§ Timeout Lifecycle); D-10 defer guarantees no FSRS-lock/TTS/goroutine leak |

Supporting constraints (not deliverables of this phase but bounding it):
- **FSRS-03** (fetch-lock pattern, already implemented Phase 5) — Phase 7 consumes `SelectDueCards`/`UnlockCards`; does not modify FSRS.
- **RES-01** (150MB budget) — in-memory Session ≈ 50KB × 100 = ~5MB (§ Resource Budget).
- **RES-03** (100-session limit) — enforced by `ws.Hub.Limit` (already 100); Session creation must check `hub.Clients()` before spawning.
</phase_requirements>

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Session state machine + transitions | `pkg/session` (new) | — | D-01/D-03: new orchestration seam owns lifecycle |
| Word queue + current index | `pkg/session` (in-memory) | `database` (audit) | D-06: in-memory is authority; DB audit-only |
| Per-state timeout enforcement | `pkg/session` event loop | global sweeper (backstop) | D-08: event loop `time.After` is primary; sweeper catches leaks |
| FSRS lock acquire / release | `pkg/fsrs` (leaf) | `pkg/session` (caller) | D-11: Session calls SelectDueCards at init, UnlockCards at exit |
| TTS streaming kickoff + completion signal | `pkg/tts` (leaf) | `pkg/session` (intercepts `word_complete`) | D-13: Streamer emits frame; Session event loop intercepts → transition |
| TTS cache cleanup | `pkg/tts.Generator.CleanupSession` | `pkg/session` (defer caller) | D-10: Session's exit defer calls it |
| WS frame push to client | `pkg/ws.Client.Send` | — | D-04/D-05: Session holds `*ws.Client` via `FrameSender` seam |
| WS connection registry (source of truth) | `pkg/ws.Hub` | — | D-04: Hub already the accept gate; ConnectionRegistry soft-deprecated |
| DB audit writes (fire-and-forget) | `pkg/database.SessionRepository` | `pkg/infra.WorkerPool` (executor) | D-06: WorkerPool gets its first real `Submit` caller |
| Connection-count metric | `pkg/ws.Hub.Clients()` | `pkg/infra.Server.Snapshot` (shim) | D-04: replace dead `registry.Stats()` with Hub-backed counts |

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Go stdlib (`time`, `context`, `sync`, `sync/atomic`, `encoding/json`) | go1.26 | Event-loop select, `time.After` deadline, `context.Context` cancellation, `sync.Once` exit guard | Already the codebase's entire concurrency substrate `[VERIFIED: go.mod]` |

### Supporting (in-repo, reused — NOT installed)
| Package | Purpose | Why Reused |
|---------|---------|------------|
| `pkg/ws` (`Hub`, `Client`, `MsgType*`, `FrameHeader`) | WS lifecycle + frame protocol | D-04: Hub is the connection source of truth; `Client.Send` is the push primitive `[VERIFIED: pkg/ws/hub.go, connection.go]` |
| `pkg/fsrs` (`FSRSService`) | FSRS lock/unlock/select | D-11: leaf service, called by Session init/exit `[VERIFIED: pkg/fsrs/service.go]` |
| `pkg/tts` (`Streamer`, `Generator`, `Sender`) | TTS streaming + cleanup; `Sender` seam pattern | D-05/D-10/D-13: Streamer emits `word_complete`; `CleanupSession` for exit defer; `Sender` is the template for `FrameSender` `[VERIFIED: pkg/tts/streamer.go, generator.go, types.go]` |
| `pkg/database` (`SessionRepository`, `SessionState`) | DB audit + canonical state enum | D-03/D-06: reuses enum verbatim; audit-only writes `[VERIFIED: pkg/database/schema.go, session_repo.go]` |
| `pkg/infra` (`WorkerPool`, `Logger`, `Config`) | DB-audit executor + logging + timeout config | D-06: WorkerPool first caller; `infra.Logger` is the DI logger `[VERIFIED: pkg/infra/worker.go, logging.go]` |
| `pkg/pybridge` (`DueCard`) | Word queue element type | D-01: word queue is `[]pybridge.DueCard` `[VERIFIED: pkg/pybridge/executor.go:97-107]` |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Flat `switch(state, event)` FSM (D-03) | `looplab/fsm` external lib | Rejected by D-03: ~15 transitions is too small to justify a dependency; default case catches invalid transitions |
| Flat switch | State-pattern (per-state handler interface) | Rejected by D-03: 5 interface impls is ceremony for a 15-line switch |

**Installation:** None. Phase 7 adds zero external dependencies. No `go.mod` / `go.sum` changes required.

## Package Legitimacy Audit

**Not applicable.** Phase 7 installs no external packages. All code is Go stdlib + existing in-repo packages under `pkg/`. D-03 explicitly rejects an external FSM library. The planner must NOT add any `require` entries to `go.mod`.

## Architecture Patterns

### System Architecture Diagram

```
                    ┌──────────────────────────────────────────────────────────┐
                    │                   Client (iPad Safari)                    │
                    │  sends control frames: start_session, play_word,         │
                    │  next_word  ────────┐                                   │
                    └─────────────────────┼──────────────────────────────────┘
                                          │ WS binary frames (MsgTypeControl 0x02)
                                          ▼
              ┌────────────────────────────────────────────────────┐
              │           pkg/ws (existing, leaf)                   │
              │  HandleWebSocket ──► Hub.Register (accept gate)     │
              │  readPump ──► Router.Route(MsgType) ──► Handler     │
              │  Client.Send([]byte) ◄── frame push (FrameSender)   │
              └───────────────┬───────────────────────▲────────────┘
                              │ control frame          │ state/error frames
                              ▼                        │ (via FrameSender.Send)
              ┌────────────────────────────────────────────────────┐
              │           pkg/session (NEW — orchestration seam)    │
              │                                                     │
              │  Manager ──map[connID]*Session                       │
              │     │                                               │
              │     │ NewSession(): SelectDueCards (LOCK) ──► fsrs   │
              │     │              GenerateForSession ────► tts      │
              │     │              SessionRepo.Create (audit) ─►wp   │
              │     ▼                                               │
              │  Session.run(ctx)  ◄── events chan SessionEvent     │
              │   select {                                          │
              │     case event := <-events:  FSM switch(state,event)│
              │     case <-time.After(Until(deadline)): timeout     │
              │     case <-ctx.Done():          cancel              │
              │   }                                                 │
              │   defer { UnlockCards; CleanupSession; hub.Unreg }  │
              │     │ LISTENING ──► StreamWord (goroutine) ──► tts  │
              │     │ word_complete frame ──► events chan (intercept)│
              │     │ audit: UpdateState/AppendEvent ──► WorkerPool │
              └───┬───────────────┬───────────────────┬─────────────┘
                  │               │                   │
                  ▼               ▼                   ▼
        ┌─────────────┐  ┌─────────────────┐  ┌────────────────┐
        │  pkg/fsrs   │  │   pkg/tts       │  │ pkg/database   │
        │ SelectDue   │  │ StreamWord      │  │ SessionRepo    │
        │ UnlockCards │  │ word_complete   │  │ (audit-only)   │
        │ (Phase 10:  │  │ CleanupSession  │  │                │
        │  UpdateRat.)│  │                 │  │                │
        └──────┬──────┘  └────────┬────────┘  └────────▲───────┘
               │                  │                    │ fire-and-forget
               ▼                  ▼                    │ via Submit
        ┌──────────────────────────────────┐    ┌──────┴────────┐
        │  Python subprocess (vocab_mgr.py │    │ pkg/infra     │
        │  due/unlock/review)              │    │ WorkerPool    │
        └──────────────────────────────────┘    │ (P1-3 → first │
                                                │  real caller) │
                                                └───────────────┘

  Global backstop:  pkg/session sweeper goroutine (~30s tick)
                    scans Manager for leaked sessions (deadline > 2× expired)
```

**Trace the primary use case** (one word dictated): Client sends `start_session` → Router delivers to session Handler → `Manager.NewSession` calls `SelectDueCards` (locks) + `GenerateForSession` + creates `Session`, spawns `Session.run` goroutine in IDLE → Client sends `play_word` → routed to Session.events → FSM transitions IDLE→LISTENING → Session spawns `StreamWord` goroutine → StreamWord sends audio frames then `word_complete` status frame → Session intercepts `word_complete` (via the FrameSender wrapper or a side channel) → pushes `WordComplete` event → FSM transitions LISTENING→DICTATING → Client sends `next_word` → FSM transitions DICTATING→LISTENING (next word) or DICTATING→WAITING_PARENT (last word) → ... → on any exit, the deferred chain releases the FSRS lock.

### Recommended Project Structure
```
pkg/session/
├── doc.go              # package doc; orchestration-seam rationale
├── types.go            # SessionState reuse alias, SessionEvent/EventType, FrameSender interface, sentinel errors
├── fsm.go              # transition() switch(state,event) table + ErrInvalidTransition
├── session.go          # Session struct + NewSession() + run() event loop + defer exit chain
├── manager.go          # Manager (map registry) + NewSession + sweeper goroutine
├── audit.go            # WorkerPool.Submit wrappers for Create/UpdateState/AppendEvent
└── *_test.go           # table-driven FSM tests, event-loop tests, goroutine-leak tests
```
`cmd/service/main.go` + `cmd/service/handlers.go` are the **rewiring sites** (D-04, control-frame routing into Session.events).

### Pattern 1: Goroutine-per-session event loop (mirror `ws.Hub.Run`)
**What:** Each `Session` runs one goroutine that is the sole writer to session state. No mutex on Session fields. `[VERIFIED: pkg/ws/hub.go:110-157 — Hub.Run is the canonical model]`
**When to use:** Always — D-02 locks this.
**Example:**
```go
// Shape mirrors ws.Hub.Run (pkg/ws/hub.go:110) exactly.
// Source: pkg/ws/hub.go:110-157 [VERIFIED codebase pattern]
func (s *Session) run(ctx context.Context) {
    // D-10: the single exit path. defer runs on normal completion, timeout,
    // disconnect (ctx cancel), and panic. Order is LIFO: Unregister first,
    // then TTS cleanup, then FSRS unlock (unlock last so the words return
    // to the due pool only after we've stopped touching them).
    defer s.hub.Unregister(s.client)        // ws.Hub.Unregister(c *Client) [VERIFIED: hub.go:207]
    defer s.gen.CleanupSession(...)          // tts.Generator.CleanupSession [VERIFIED: generator.go:204]
    defer s.releaseFSRSLock()                // fsrs.UnlockCards(ctx, opts)  [VERIFIED: service.go:174]

    for {
        select {
        case ev := <-s.events:
            if err := s.handleEvent(ev); err != nil {
                s.logger.Warn("session event error", infra.ErrField(err))
                return // exit → defer chain fires
            }
        case <-time.After(time.Until(s.deadline)): // D-08: GC-safe on Go 1.23+ [CITED: Go stdlib]
            s.handleTimeout()
            return
        case <-ctx.Done():
            return // disconnect/shutdown → defer chain fires
        }
    }
}
```

### Pattern 2: `FrameSender` import-cycle seam (copy `tts.Sender`)
**What:** `pkg/session` must not import `pkg/ws` for framing (would risk a cycle if `pkg/ws` ever routes to session). Define a local interface satisfied structurally by `*ws.Client`. `[VERIFIED: pkg/tts/types.go:27-31 — tts.Sender is the exact template]`
```go
// Source: pkg/tts/types.go:27-31 (the tts.Sender seam) [VERIFIED codebase pattern]
// FrameSender is the interface for pushing binary WS frames back to the client.
// *ws.Client satisfies it structurally (Client.Send([]byte) error, connection.go:256).
// pkg/session never imports pkg/ws for framing — only for the *Client pointer type
// passed in at construction (the pointer type alone does not create a framing cycle
// because session does not call ws.EncodeFrame/ws.FrameHeader directly; if it must
// build frames, hand the raw []byte to the client via a helper in cmd/service).
type FrameSender interface {
    Send(msg []byte) error
}
```
**Note on the import boundary:** `pkg/session` storing a `*ws.Client` field still imports `pkg/ws` for the *type*. This is acceptable (same as `pkg/tts` importing `pkg/ws` for `ws.FrameHeader`/`ws.MsgTypeAudio` in streamer.go:11). The `FrameSender` seam matters when pushing *arbitrary* frames without coupling session to the wire encoding. If session needs to build frames, prefer passing pre-built `[]byte` from `cmd/service/handlers.go` (the composition root) rather than calling `ws.EncodeFrame` inside `pkg/session`.

### Pattern 3: Flat typed-enum FSM (D-03)
**What:** A method on `*Session` containing a nested `switch(state){ switch(event){...} }`. Invalid `(state,event)` pairs hit `default: return ErrInvalidTransition`. Reuses `database.SessionState` verbatim. `[VERIFIED: pkg/database/schema.go:20-33 — enum values]`

### Pattern 4: Hybrid timeout (per-state deadline + sweeper backstop)
**What:** Event-loop `time.After(time.Until(deadline))` is the primary timeout; a separate `Manager`-level goroutine (mirroring `infra.ConnectionRegistry.cleanupLoop`) sweeps every ~30s as a leak backstop. `[VERIFIED: pkg/infra/connection.go:419-436 — cleanupLoop ticker+ctx.Done pattern]`

### Pattern 5: Fire-and-forget DB audit via WorkerPool
**What:** Each state transition submits a `Task` to `infra.WorkerPool` that calls `SessionRepository.UpdateState`/`AppendEvent`. The event loop never blocks on DB. Gives WorkerPool its first real caller (P1-3). `[VERIFIED: pkg/infra/worker.go:228-240 — Submit(task Task) error, non-blocking, ErrWorkerPoolFull]`
```go
// Source: pkg/infra/worker.go:228 [VERIFIED signature]
// Submit is non-blocking; on ErrWorkerPoolFull we log + drop the audit row
// (audit is best-effort; the in-memory Session is the authority per D-06).
func (s *Session) auditState(newState database.SessionState) {
    err := s.pool.Submit(func(ctx context.Context) error {
        return s.repo.UpdateState(ctx, s.dbID, newState) // [VERIFIED: session_repo.go:139]
    })
    if err != nil {
        s.logger.Warn("session audit dropped (worker pool full)", infra.ErrField(err))
    }
}
```

### Anti-Patterns to Avoid
- **Mutex on Session state (D-02 option B):** Holding a lock across `StreamWord` (a multi-second TTS subprocess goroutine spawn) or `UnlockCards` (a Python subprocess call) is a deadlock/latency hazard. The event loop serializes writes — no mutex.
- **Committing default ratings on timeout (D-11 option C):** Would poison FSRS training. Unlock-only.
- **Calling `ConnectionRegistry.Register` on the accept path:** It is already NOT called — keep it that way. Do not "re-wire" it back in.
- **Per-word `UnlockCards` calls (D-11 option B):** N Python subprocess calls per session on a MaxConcurrent=2 bridge — fork-bomb on the 1C VPS.
- **Hand-rolling a timer manager with `time.AfterFunc`:** The `time.After` in the select arm is GC-safe on Go 1.26 (Go 1.23+ change) and far simpler. `[CITED: Go stdlib — go doc time.After]`

## FSM Transition Table

The 5 states reuse `database.SessionState` verbatim (`"IDLE"/"LISTENING"/"DICTATING"/"WAITING_PARENT"/"FEEDBACK"`, `[VERIFIED: pkg/database/schema.go:22-33]`). Event taxonomy (D-02 + D-13):

| Event | Source | Drives |
|---|---|---|
| `ControlFrame{play_word}` | client (Router) | user-driven |
| `ControlFrame{next_word}` | client (Router) | user-driven |
| `WordComplete{wordID}` | system (TTS Streamer `word_complete` frame intercepted) | system-driven (D-13) |
| `Timeout` | system (per-state deadline fires) | timeout-driven |
| `Cancel` | system (ctx.Done — disconnect/shutdown) | lifecycle |
| `ParentReview` | client/parent (Phase 10 hook; Phase 7 allows the transition but does no rating work) | user-driven |

### Enumerated ~15 transitions

| # | From | Event | To | Notes |
|---|------|-------|----|-------|
| 1 | (none) | `NewSession` | `IDLE` | SelectDueCards (LOCK) + GenerateForSession + SessionRepo.Create. D-11 lock acquired here. |
| 2 | `IDLE` | `play_word` | `LISTENING` | Advance word index to 0; spawn `StreamWord` goroutine for first word. |
| 3 | `IDLE` | `Timeout` | terminal | 30min; defer chain → cleanup. |
| 4 | `LISTENING` | `WordComplete` | `DICTATING` | D-13 system-driven auto-transition. TTS finished; student may speak. |
| 5 | `LISTENING` | `play_word` (same word) | `LISTENING` | Replay: cancel in-flight stream, restart StreamWord. Stays in state. |
| 6 | `LISTENING` | `Timeout` | terminal | 30s; defer chain → cleanup. |
| 7 | `DICTATING` | `next_word` (more words) | `LISTENING` | Advance word index; spawn StreamWord for next word. |
| 8 | `DICTATING` | `next_word` (last word) | `WAITING_PARENT` | Word queue exhausted; dictation done. No rating commit (D-12). |
| 9 | `DICTATING` | `Timeout` | terminal | 30s mid-word; defer chain → cleanup (no partial commit, D-11). |
| 10 | `WAITING_PARENT` | `ParentReview` | `FEEDBACK` | Phase 10 owns the rating work; Phase 7 only transitions. |
| 11 | `WAITING_PARENT` | `Timeout` | terminal | 30min; defer chain → cleanup. |
| 12 | `FEEDBACK` | `session_end` / `Timeout` | terminal | 5min or explicit end; defer chain → cleanup. No default rating (D-12). |
| 13 | ANY active | `Cancel` (ctx.Done) | terminal | Disconnect/shutdown; defer chain → cleanup. |
| 14 | ANY active | `Timeout` (sweeper backstop) | terminal | Sweeper detects leaked goroutine (deadline > 2× expired); force cleanup. |
| 15 | `DICTATING` | `play_word` | `LISTENING` | (Edge) re-listen to current word mid-dictation. Optional; planner may include or defer. |

**Invalid-transition handling:** every `(state, event)` pair not listed above returns a sentinel:
```go
var ErrInvalidTransition = errors.New("session: invalid state transition")
// default branch in transition() switch:
//   return fmt.Errorf("%w: state=%s event=%v", ErrInvalidTransition, s.state, ev)
```
The event loop logs the invalid transition (Warn) but does **not** terminate the session for a benign client mis-order (e.g., `next_word` arriving while in IDLE); it drops the event. This mirrors the codebase's "skip malformed frames, don't kick the connection" philosophy (`[VERIFIED: pkg/ws/connection.go:186-189 — frame decode failed, skipping]`).

**`terminal` is not a stored state** — it is the event loop returning, which fires the D-10 `defer` chain. The DB `state` column is left at whatever the last audited state was (audit-only, D-06); optionally a final `UpdateState` to a sentinel like `"CLOSED"` can be added if Phase 10 replay needs it (planner's discretion).

## The D-04 Connection Unification — Precise Rewiring (READ CAREFULLY)

This is the one one-way decision. The planner must emit `checkpoint:decision` before the task that changes the metrics contract. **But the accept-path half is already done** — verified by reading the actual code:

### Current state (verified)
1. **Accept path (ALREADY unified):** `pkg/ws/server.go:200` `HandleWebSocket` calls `s.hub.Register(client)`. It **never** calls `ConnectionRegistry.Register`. `[VERIFIED: pkg/ws/server.go:154-204]`
2. **`ConnectionRegistry.Register()` has ZERO production callers.** A repo-wide grep for `registry.Register|\.Register\(\)` found matches **only in `pkg/infra/connection_test.go`** (test code). `[VERIFIED: grep across *.go]`
3. **The registry is still constructed + its cleanup goroutine started:** `pkg/infra/server.go:182` `NewConnectionRegistry(...)`, `server.go:242` `s.registry.Start(regCtx)`. The cleanup goroutine ticks every 30s sweeping an **empty map** (`Stats().Active` is always 0). `[VERIFIED: pkg/infra/server.go:182, 242, 325]`
4. **Metrics read the dead registry:** `pkg/infra/server.go:325` `Connections: s.registry.Stats()` feeds `ServerSnapshot.Connections`, which `pkg/infra/health.go:119-125` renders as `connections_active` / `connections_total_accepted` gauges/counters. So `/metrics` and `/health` report `Active: 0` even with live WS clients. `[VERIFIED: pkg/infra/health.go:119-125]`

### What D-04 actually requires (the rewiring)
| Step | File | Change | Risk |
|---|---|---|---|
| 1 | `cmd/service/main.go` (after `hub` constructed) | Pass `hub` into `infra.Server` (new setter or constructor arg) so `Snapshot()` can read `hub.Clients()`. **OR** add a `ConnectionStatsProvider` interface that Hub satisfies. | Low — additive |
| 2 | `pkg/infra/server.go:325` | Replace `s.registry.Stats()` with the Hub-backed provider. Keep `ConnectionStats` struct shape so health.go/metrics are untouched. | Medium — changes `Snapshot()` semantics; the Total* counters (accepted/rejected/cleaned) need new sources (Hub doesn't track them today — either add atomic counters to Hub or accept that only `Active` is meaningful v1). |
| 3 | `pkg/infra/server.go:242` | Stop starting the dead registry cleanup goroutine (`s.registry.Start(regCtx)`), OR leave it (it sweeps an empty map harmlessly). Planner's discretion — stopping it is cleaner. | Low |
| 4 | Session creation site | When a session is created, it is already covered by `hub.Register` (the client is registered at WS upgrade). Session just holds the `*ws.Client`. **No new registration call.** | None |
| 5 | (Follow-up, NOT blocking Phase 7) | Remove `ConnectionRegistry` entirely in a later cleanup. Phase 7 keeps it soft-deprecated. | — |

**Metrics counters gap (call out for planner):** `Hub` currently exposes only `Clients() int` (active count, `[VERIFIED: pkg/ws/hub.go:241-245]`). It does NOT track `TotalAccepted`/`TotalRejected`/`TotalCleaned`. Three options for the planner:
- **(a) Minimal:** Snapshot.Connections reports `Active = hub.Clients()`, leaves Total* at 0 (or drops them). Acceptable for v1.
- **(b) Add atomic counters to Hub:** increment in `Run` on accept/reject/unregister. Small change to `pkg/ws/hub.go`. Preserves the metrics contract fully.
- **(c) Keep registry counting via a Hub hook:** wire `Hub`'s accept/reject into `LifecycleHooks`. More moving parts.

**Recommendation:** Option (b) — add `totalAccepted/totalRejected atomic.Uint64` to `Hub` (mirrors `ConnectionRegistry`'s own counters at `connection.go:227-229`) and a `Hub.Stats() ConnectionStats` method. This is the smallest change that preserves the published `/metrics` contract and is reversible.

### FSRS Lock Integration (DICT-02 / D-11)

**Verified signatures (`[VERIFIED: pkg/fsrs/service.go]`):**
```go
SelectDueCards(ctx context.Context, opts SelectOpts) ([]pybridge.DueCard, error)
//   SelectOpts{ Limit int; Category string; Units []string; FetchLockHours float64 }
//   Zero-value opts fall back to cfg.FSRS defaults. Empty slice + nil err = all locked (D-02-a).
UnlockCards(ctx context.Context, opts UnlockOpts) (int, error)
//   UnlockOpts{ Category string; Unit string }   <-- CATEGORY+UNIT scoped, NOT card-ID scoped
```

**Critical constraint (non-obvious):** `UnlockCards` takes **Category + Unit**, not card IDs. The Python `unlock` subcommand (`[VERIFIED: pkg/pybridge/vocab.go:111-120]`) clears `fetch_locked_until` for ALL cards matching the scope. Therefore the Session MUST remember the exact `Category` (and `Unit`, if pinned) used at `SelectDueCards` and replay them at exit. If two concurrent sessions share a category, unlocking one releases the other's lock too — but D-11's lock-at-init/unlock-on-exit is per-session, so this is acceptable for v1 (concurrent same-category sessions are rare for a single-family tutor; the fetch-lock itself prevents word overlap at selection time).

**Session init sequence (D-11):**
```go
// 1. Acquire lock + words
cards, err := fsrsSvc.SelectDueCards(ctx, fsrs.SelectOpts{ /* caller limit/category */ })
// FetchLockHours defaults to cfg.FSRS.FetchLockHours (24.0) [VERIFIED: service.go:130-133]
if err != nil { return nil, err }
if len(cards) == 0 { return nil, ErrNoDueWords } // D-02-a: empty=none due, not error

// 2. Remember the lock scope for unlock-at-exit
s.lockCategory = <the Category actually used> // resolve from SelectOpts||cfg.FSRS.DefaultCategory
s.lockUnit     = <the Unit actually used>     // resolve from SelectOpts||cfg.FSRS.DefaultUnit

// 3. Generate TTS, create DB audit row, spawn event loop...
```

**Session exit sequence (D-10 defer, runs on EVERY exit):**
```go
func (s *Session) releaseFSRSLock() {
    // Use a fresh context: the run() ctx is already cancelled by the time defer fires
    // (disconnect/shutdown). UnlockCards spawns a Python subprocess; it needs a live ctx.
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // cfg.Python.DueTimeout
    defer cancel()
    n, err := s.fsrsSvc.UnlockCards(ctx, fsrs.UnlockOpts{
        Category: s.lockCategory,
        Unit:     s.lockUnit,
    })
    if err != nil {
        s.logger.Error("FSRS unlock failed on session exit — words remain locked until expiry",
            infra.F("category", s.lockCategory), infra.ErrField(err))
        // Do NOT panic: the fetch-lock has a TTL (FetchLockHours=24h) so it self-expires.
        // Logging is the correct response; the words return to the pool after TTL.
    }
}
```
**Pitfall flagged below (Pitfall 1):** the deferred unlock must use a **fresh context**, not the run-loop's (already-cancelled) ctx.

### Timeout Lifecycle (D-08)

**Per-state deadline (primary):** updated inside `transition()` on every state change.
```go
// D-09 defaults — MUST be configurable, not hardcoded.
// [VERIFIED: no existing config block holds these — SessionConfig is registry-oriented (config.go:400)]
// Planner: add a new config struct, e.g. infra.SessionTimeoutConfig, OR extend an existing block.
var defaultStateTimeout = map[database.SessionState]time.Duration{
    database.StateIdle:         30 * time.Minute,
    database.StateListening:    30 * time.Second,
    database.StateDictating:    30 * time.Second,
    database.StateWaitingParent: 30 * time.Minute,
    database.StateFeedback:     5 * time.Minute,
}

func (s *Session) transition(newState database.SessionState) {
    s.state = newState
    s.deadline = time.Now().Add(s.timeoutFor(newState)) // D-08: refresh on every transition
    s.auditState(newState)                              // D-06: fire-and-forget via WorkerPool
}
```
**`time.After(time.Until(deadline))` semantics:** On Go 1.26 (≥1.23), the GC reclaims unreferenced unstopped timers, so recreating the `time.After` each loop iteration is leak-free. If `deadline` is in the past, the channel fires immediately and the timeout arm wins the next select. `[CITED: Go stdlib — go doc time.After]`

**Global sweeper (backstop):** mirrors `ConnectionRegistry.cleanupLoop` `[VERIFIED: pkg/infra/connection.go:419-436]`.
```go
// Manager.sweep runs as a goroutine; catches sessions whose event-loop goroutine
// leaked (deadline long past, no ctx cancel observed).
func (m *Manager) sweep(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second) // D-08: ~30s, mirrors cfg.Session.CleanupInterval
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            m.cleanupLeaked() // iterate sessions, force-cancel those with deadline > 2×expired
        }
    }
}
```
The sweeper's job is narrow: detect a session whose `time.Since(deadline) > 2 × stateTimeout` AND whose goroutine is not observed exiting, then call its `Cancel()`. The event loop's own `time.After` is the normal timeout path; the sweeper is purely a leak defense (e.g., a goroutine stuck in a `StreamWord` subprocess call that ignores ctx).

### DB Audit via WorkerPool (D-06 / P1-3)

`infra.WorkerPool.Submit(Task)` is the executor (`[VERIFIED: pkg/infra/worker.go:228]`, `Task = func(ctx context.Context) error`). Today it has **zero production callers** (P1-3) — Phase 7 gives it its first. The wiring:
```go
// cmd/service/main.go: pass server.Components() workers (or a typed accessor) into the Manager.
mem, pool, reg, workers := server.Components() // [VERIFIED: server.go:343]
// workers is *infra.WorkerPool, already Start()-ed by server.Start(ctx).
manager := session.NewManager(hub, fsrsSvc, gen, streamer, sessionRepo, workers, logger, cfg)
```
**On `ErrWorkerPoolFull`:** log + drop the audit row. Per D-06 the in-memory Session is the authority; a dropped audit row means a gap in the Phase-10 replay trail, not a correctness loss. Never block the event loop on a DB write.

### TTS `word_complete` Interception (D-13)

**Current behavior (verified):** `tts.Streamer.StreamWord` calls `s.sendWordComplete(ctx, client, wordID)` at the end (`[VERIFIED: pkg/tts/streamer.go:145, 151-184]`), which sends a `MsgTypeStatus` (0x06) frame with JSON `{"type":"word_complete","word_id":"..."}` **directly to the client**. Today nothing intercepts it; `handlePlayWord` (`handlers.go:225`) fires `StreamWord` in a goroutine and returns immediately.

**D-13 interception design (two viable approaches — planner picks):**
- **(A) Status-frame round-trip:** The client receives `word_complete` and... the server also needs it. So either the client echoes a control frame back (rejected — adds client complexity for young students), OR:
- **(B) Server-side side channel (RECOMMENDED):** `Session` spawns `StreamWord` itself (not the handler). Give `StreamWord` a completion callback / result channel. When `StreamWord` returns, the spawning goroutine pushes a `WordComplete{wordID}` event onto `s.events`. The status frame still goes to the client (harmless — the client can use it for UI), but the **state transition is driven by the server-side return**, not by parsing the outbound frame.
- **(C) Wrap the `FrameSender`:** Wrap `*ws.Client` in a `frameTap` that inspects outgoing `MsgTypeStatus` frames for `word_complete` and enqueues an event. More coupling; rejected unless (B) is infeasible.

**Recommendation: (B).** The Session owns `StreamWord` invocation (it's the orchestration seam), so it naturally knows when streaming completes. `handlePlayWord` becomes a thin router that pushes a `ControlFrame{play_word}` event onto the session's channel instead of calling `streamer.StreamWord` directly. This keeps `pkg/tts` a pure leaf (no change to streamer.go) and centralizes all TTS kickoff in the event loop.

**`MsgTypeSession` (0x05):** `[VERIFIED: pkg/ws/protocol.go:34 — defined, unused]`. Available for session control frames. Planner's discretion whether `play_word`/`next_word` stay on `MsgTypeControl` (0x02, current) or migrate to `MsgTypeSession`. **Recommendation: keep on `MsgTypeControl` for v1** (client already sends them there per handlers.go); reserve `MsgTypeSession` for future session-specific frames. Migrating is a client-contract change better deferred.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| FSM engine | state-pattern interface + 5 handler impls, or external lib | Flat `switch(state,event)` (D-03) | ~15 transitions; default case catches invalid; readable in one screen |
| Timer manager | `time.AfterFunc` map + manual Stop bookkeeping | `time.After(time.Until(deadline))` in the select arm | GC reclaims unreferenced timers on Go 1.23+ (`[CITED: go doc time.After]`); recreating per-iteration is free |
| Per-session mutex | `sync.Mutex` guarding Session fields | Single-writer event loop (D-02) | Holding a lock across TTS/Python subprocess calls deadlocks; event loop serializes for free |
| Goroutine-leak detector | custom runtime.NumGoroutine polling | Global sweeper mirroring `cleanupLoop` + `go.uber.org/goleak` in tests | Sweeper is the runtime backstop; goleak is the test assertion `[VERIFIED: cleanupLoop pattern at connection.go:419]` |
| Connection registry | a new session→conn map | Reuse `ws.Hub` as source of truth (D-04) | Hub already tracks clients; ConnectionRegistry is dead |
| DB write queue | a new buffered channel + worker goroutine | `infra.WorkerPool.Submit` (D-06) | Already built, already started, currently unused (P1-3) |

**Key insight:** Every "hard" problem in this phase already has a solved, tested implementation in the repo. Phase 7 is a *composition* phase, not an invention phase. The risk is in wiring, not design.

## Resource Budget

| Item | Per-session | 100 sessions | Budget (150MB) | Source |
|------|-------------|--------------|----------------|--------|
| Session struct (state, deadline, word queue) | ~50KB | ~5MB | 3.3% | DESIGN-ANALYSIS Gray Area 2; `[]pybridge.DueCard` ≈ 25 × ~2KB |
| Event-loop goroutine | ~8KB stack | ~800KB | 0.5% | Go goroutine stack minimum |
| TTS PCM prefetch (per session) | ~2 words × ~150KB | bounded by PrefetchCount=2 | — | `[VERIFIED: TTSConfig.PrefetchCount default 2, config.go:602]` |
| DB audit rows (audit-only) | ~5 rows × ~200B | transient | negligible | SQLite WAL `[VERIFIED: database layer]` |

**Verdict:** Well within 150MB. The 100-session RES-03 limit is enforced by `ws.Hub.Limit` (already 100, `[VERIFIED: hub.go:65, config.go:575 HubLimit:100]`). Session creation must additionally check `hub.Clients() < limit` before spawning (defense-in-depth; the Hub already rejects at capacity).

## Common Pitfalls

### Pitfall 1: Deferred FSRS unlock uses the cancelled run-loop context
**What goes wrong:** The D-10 `defer` calls `UnlockCards(s.ctx, ...)` where `s.ctx` is the event-loop context — which is **already cancelled** by the time the defer fires (disconnect/shutdown/timeout all cancel it). `UnlockCards` → `RunUnlock` → `Bridge.run` checks `ctx.Err()` and returns `ErrPythonTimeout`/`context.Canceled` immediately. The lock is never released; words stay locked for 24h.
**Why it happens:** `pybridge.Bridge.run` selects on `ctx.Done()` (`[VERIFIED: pkg/pybridge/vocab.go:159-162]`). A cancelled ctx aborts the subprocess before it runs.
**How to avoid:** The `releaseFSRSLock` defer MUST construct a fresh `context.WithTimeout(context.Background(), cfg.Python.DueTimeout)`. Same for `CleanupSession` if it ever blocks (it doesn't — it's `os.RemoveAll`, but use a fresh ctx anyway for uniformity).
**Warning signs:** `/metrics` shows `connections_total_cleaned` incrementing but FSRS `due` queries keep returning fewer words over time (locks never expiring early).

### Pitfall 2: `time.After` in a loop looks like a leak (it isn't, on Go 1.23+)
**What goes wrong:** Reviewers flag `case <-time.After(time.Until(s.deadline))` inside the `for {}` as a timer leak (the pre-1.23 warning).
**Why it happens:** Stale mental model. Go 1.23 changed `time.After` so the GC reclaims unreferenced unstopped timers.
**How to avoid:** Cite `go doc time.After` (Go 1.23+ note) in a code comment. The project is on Go 1.26 (`[VERIFIED: go.mod]`), so this is safe. Do NOT "optimize" to a manual `time.NewTicker` — that reintroduces Stop-bookkeeping complexity.
**Warning signs:** A reviewer/plan-checker demands `time.NewTimer` + `Stop`. Push back with the Go version.

### Pitfall 3: Holding the (absent) lock across TTS generation blocks the event loop
**What goes wrong:** If `GenerateForSession` (a Python subprocess, up to `PregenTimeout`=120s, `[VERIFIED: config.go:268,601]`) runs **inside** the event loop, the loop cannot process `Cancel`/`Timeout` for 2 minutes — a disconnecting student's session hangs.
**Why it happens:** Tempting to do init synchronously in `run()`.
**How to avoid:** Do `SelectDueCards` + `GenerateForSession` in `NewSession()` **before** spawning the `run()` goroutine (the event loop starts only after init completes and the lock is held). The `IDLE` 30min timeout then covers the init window. Alternatively, run init in a goroutine and only enter the event loop on success — but then a failed init must release the lock in a non-defer path. **Recommendation: synchronous init before `go s.run()`.**
**Warning signs:** A 2-minute window where `Cancel` events queue but don't process.

### Pitfall 4: Unlocking by card-ID instead of category+unit
**What goes wrong:** Implementer assumes `UnlockCards` takes a list of card IDs (natural assumption). It actually takes `UnlockOpts{Category, Unit}` and clears ALL locked cards in that scope (`[VERIFIED: pkg/fsrs/service.go:174-191, pkg/pybridge/vocab.go:111-120]`).
**Why it happens:** The FSRS service interface name suggests per-card granularity.
**How to avoid:** Store `lockCategory`/`lockUnit` on the Session at init; replay them verbatim at unlock. Document the category-scope semantics in the Session struct doc comment.
**Warning signs:** Two concurrent same-category sessions interfere; or unlock releases more cards than expected.

### Pitfall 5: DB audit write blocks the event loop on SQLite contention
**What goes wrong:** Calling `SessionRepository.UpdateState` synchronously in `transition()` blocks the event loop when SQLite WAL contends (Python sidecar also writes the same DB).
**Why it happens:** Direct call instead of WorkerPool submission.
**How to avoid:** D-06 mandates fire-and-forget via `WorkerPool.Submit`. Never call `repo.*` directly from the event loop. The in-memory Session is the authority.

### Pitfall 6: Session goroutine outlives the WS client
**What goes wrong:** On disconnect, `readPump` exits and `hub.Unregister(c)` fires (`[VERIFIED: connection.go:129-131]`), but the Session goroutine keeps running until its `time.After` deadline (up to 30min in IDLE/WAITING_PARENT) — holding the FSRS lock the whole time.
**Why it happens:** The Session's ctx is not bound to the connection's ctx.
**How to avoid:** Bind the Session's context to the connection lifecycle. `cmd/service/main.go`/handlers must pass the **per-connection ctx** (from `HandleWebSocket`'s `context.WithCancel(r.Context())`, `[VERIFIED: server.go:207]`) as the parent of the Session's ctx, so disconnect cancels the Session immediately. This is the linchpin of D-10's "disconnect" exit path.
**Warning signs:** FSRS locks held for full 24h after a student closes the tab.

## Code Examples

Verified signatures the planner/executor will call (all `[VERIFIED]` against source read this session):

### Create a session (init path)
```go
// Composes: fsrs.SelectDueCards (lock), tts.Generator.GenerateForSession,
// database.SessionRepository.Create (audit), then spawns the event loop.
func NewSession(
    ctx context.Context,
    client *ws.Client,                 // FrameSender target [VERIFIED: connection.go:256 Send]
    connID string,                     // for DB audit row (database.Session.ConnectionID)
    fsrsSvc fsrs.FSRSService,          // [VERIFIED: service.go:31 interface]
    gen *tts.Generator,                // [VERIFIED: generator.go:16, NewGenerator:31]
    streamer *tts.Streamer,            // [VERIFIED: streamer.go:17, NewStreamer:34]
    repo database.SessionRepository,   // [VERIFIED: session_repo.go:31 interface]
    pool *infra.WorkerPool,            // [VERIFIED: worker.go:68, Submit:228]
    hub *ws.Hub,                       // for Unregister on exit [VERIFIED: hub.go:207]
    logger infra.Logger,
    selectOpts fsrs.SelectOpts,
    timeouts sessionTimeoutConfig,
) (*Session, error) {
    // 1. LOCK + fetch words
    cards, err := fsrsSvc.SelectDueCards(ctx, selectOpts) // [VERIFIED: service.go:113]
    if err != nil { return nil, err }
    if len(cards) == 0 { return nil, ErrNoDueWords }

    // 2. Generate TTS (blocking, before event loop — see Pitfall 3)
    words := dueCardsToWords(cards) // card.Word is the TTS wordID + FSRS rating key
    sessionID := uuidV4()           // NOT the DB int64 id; this is the TTS/FSRS scope key
    if _, err := gen.GenerateForSession(ctx, sessionID, words); err != nil { // [VERIFIED: generator.go:60]
        // Lock already acquired — must release on this error path (non-defer!)
        _, _ = fsrsSvc.UnlockCards(ctx, fsrs.UnlockOpts{Category: resolvedCategory(selectOpts), Unit: resolvedUnit(selectOpts)})
        return nil, err
    }

    // 3. Audit row (fire-and-forget via WorkerPool — D-06)
    var dbID int64
    auditErr := pool.Submit(func(ctx context.Context) error {
        id, err := repo.Create(ctx, database.Session{ // [VERIFIED: session_repo.go:72]
            ConnectionID: connID, State: database.StateIdle, WordCount: len(cards),
        })
        dbID = id // note: race-free only because Submit is synchronous-ish in tests; in prod, dbID is best-effort
        return err
    })
    // ...

    // 4. Build session, spawn event loop with conn-bound ctx
    s := &Session{ /* state=IDLE, deadline=now+30m, wordQueue=cards, ... */ }
    go s.run(ctx) // [VERIFIED pattern: hub.go:110]
    return s, nil
}
```

### The exit defer chain (D-10 — the critical invariant)
```go
func (s *Session) run(ctx context.Context) {
    // LIFO defer order: last registered = first to run.
    // Register FSRS unlock FIRST so it runs LAST (after WS + TTS teardown).
    defer s.releaseFSRSLock()      // fresh ctx; UnlockCards(Category, Unit) — Pitfall 1
    defer s.cleanupTTS()           // gen.CleanupSession(ctx, sessionID) [VERIFIED: generator.go:204]
    defer s.hub.Unregister(s.client) // [VERIFIED: hub.go:207 — non-blocking, idempotent]
    // ...event loop...
}
```

## Validation Architecture

`workflow.nyquist_validation` is `true` (`[VERIFIED: .planning/config.json:11]`). The project uses table-driven Go tests with `go test`. No separate test config file; tests live alongside source as `*_test.go`. The codebase has no e2e/CI test runner beyond `go test ./...`.

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go testing + `github.com/pkg/errors` + (recommended) `go.uber.org/goleak` for goroutine-leak assertions |
| Config file | none — standard `go test` |
| Quick run command | `go test ./pkg/session/... ./pkg/infra/... ./pkg/ws/...` |
| Full suite command | `go test ./...` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| DICT-02 | Session init calls SelectDueCards and holds the word queue; empty-due returns ErrNoDueWords | unit | `go test ./pkg/session/ -run TestNewSession_SelectDueCards -x` | ❌ Wave 0 |
| DICT-02 | Init failure (TTS gen error) releases the FSRS lock (non-defer path) | unit | `go test ./pkg/session/ -run TestNewSession_TTSFailure_Unlocks -x` | ❌ Wave 0 |
| DICT-04 | All ~15 FSM transitions return correct newState | unit (table-driven) | `go test ./pkg/session/ -run TestTransition -x` | ❌ Wave 0 |
| DICT-04 | Invalid (state,event) returns ErrInvalidTransition and does not change state | unit (table-driven) | `go test ./pkg/session/ -run TestTransition_Invalid -x` | ❌ Wave 0 |
| DICT-04 | `word_complete` event drives LISTENING→DICTATING (D-13) | unit | `go test ./pkg/session/ -run TestEventLoop_WordComplete -x` | ❌ Wave 0 |
| DICT-04 | `next_word` on last word drives DICTATING→WAITING_PARENT | unit | `go test ./pkg/session/ -run TestEventLoop_LastWord -x` | ❌ Wave 0 |
| ERR-03 | Per-state deadline fires Timeout event → terminal | unit (short timeouts) | `go test ./pkg/session/ -run TestEventLoop_Timeout -x` | ❌ Wave 0 |
| ERR-03 | Global sweeper cancels a leaked session (deadline > 2× expired) | unit | `go test ./pkg/session/ -run TestManager_Sweeper -x` | ❌ Wave 0 |
| ERR-03 | No goroutine leak across full session lifecycle (init→run→exit) | unit + goleak | `go test ./pkg/session/ -run TestSession_NoGoroutineLeak -x` | ❌ Wave 0 |
| D-10 | Exit defer calls UnlockCards + CleanupSession + hub.Unregister on every exit path (normal/timeout/cancel/error) | unit (subtests per exit path) | `go test ./pkg/session/ -run TestExitDefer -x` | ❌ Wave 0 |
| D-10 | Deferred unlock uses a fresh context (Pitfall 1) — UnlockCards succeeds even when run-ctx cancelled | unit | `go test ./pkg/session/ -run TestReleaseFSRSLock_FreshContext -x` | ❌ Wave 0 |
| D-11 | Unlock-on-exit does NOT call UpdateRatings (no partial commit) | unit | `go test ./pkg/session/ -run TestExit_NoRatingCommit -x` | ❌ Wave 0 |
| D-13 | Session intercepts StreamWord completion via server-side side channel (option B) | unit | `go test ./pkg/session/ -run TestEventLoop_StreamWordCompletion -x` | ❌ Wave 0 |
| D-04 | Hub counts feed ServerSnapshot.Connections (metrics shim) | unit | `go test ./pkg/infra/ -run TestSnapshot_HubConnections -x` | ❌ Wave 0 |
| D-06 | DB audit writes go through WorkerPool.Submit (first real caller) | unit | `go test ./pkg/session/ -run TestAuditViaWorkerPool -x` | ❌ Wave 0 |

### Success Criteria → Test Map

| Criterion | Validation |
|---|---|
| 1. Session initializes with selected due words | DICT-02 tests above + integration: spin a fake `FSRSService` returning canned `[]DueCard`, assert Session.wordQueue length + lock acquired |
| 2. 5-state lifecycle maintained | DICT-04 table-driven transition tests cover every enumerated transition |
| 3. Transitions driven by user actions + timeouts | DICT-04 + ERR-03 event-loop tests inject `ControlFrame`/`WordComplete`/`Timeout` events and assert state changes |
| 4. Timeout cleanup removes stale sessions without leaks | ERR-03 sweeper test + goleak test; assert `UnlockCards` called once per exit, FSRS lock released, no goroutine remains |

### Sampling Rate
- **Per task commit:** `go test ./pkg/session/... ./pkg/infra/... ./pkg/ws/...` (fast, session + touched packages)
- **Per wave merge:** `go test ./...` (full suite)
- **Phase gate:** Full suite green + manual end-to-end smoke (open `test-interface.html`, start session, play word, observe state transitions in server logs) before `/gsd-verify-work`

### Wave 0 Gaps
- [ ] `pkg/session/types.go` — `SessionEvent`, `EventType`, `FrameSender` interface, sentinel errors (`ErrInvalidTransition`, `ErrNoDueWords`)
- [ ] `pkg/session/fsm.go` — `transition()` table
- [ ] `pkg/session/session.go` — `Session` struct + `run()` + exit defer
- [ ] `pkg/session/manager.go` — `Manager` registry + sweeper
- [ ] `pkg/session/audit.go` — WorkerPool wrappers
- [ ] `pkg/session/*_test.go` — all tests above + shared fakes (fake `FSRSService`, fake `SessionRepository`, fake `FrameSender`)
- [ ] Framework add: `go.uber.org/goleak` to `go.mod` (the ONLY new dependency this phase should consider; verify legitimacy) — **OR** use `runtime.NumGoroutine()` diffing to avoid the dependency. Planner's discretion.
- [ ] Config: add per-state timeout config (new `infra.SessionTimeoutConfig` or extend `SessionConfig`) + env bindings in `bindEnvForAllLeaves` (`[VERIFIED: config.go:902]`)

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `time.After` leaks timers (pre-1.23 warning) | GC reclaims unreferenced timers | Go 1.23 | `time.After(time.Until(deadline))` in a select loop is leak-free; no manual `NewTimer`+`Stop` needed `[CITED: go doc time.After]` |
| `ConnectionRegistry` as accept gate (Phase 1 design intent) | `ws.Hub` as accept gate (Phase 2/6 wiring reality) | Phase 2→6 wiring | D-04's accept-path work is already done; only metrics rewiring remains |
| Manual mutex on session state | Single-writer event loop | D-02 (this phase) | No lock contention; mirrors `ws.Hub.Run` |

**Deprecated/outdated in this codebase:**
- `infra.ConnectionRegistry` — soft-deprecated this phase (D-04). Accept path no longer uses it (already true); metrics shim replaces its `Stats()` source. Full removal deferred to a follow-up cleanup phase.
- The `sessionID := fmt.Sprintf("session-%d", time.Now().UnixMilli())` in `handleStartSession` (`[VERIFIED: handlers.go:186]`) — replace with a real UUID (reuse `infra.newUUIDv4` pattern at `connection.go:501`, or `crypto/rand` directly) for FSRS-scope uniqueness.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | The `word_complete` interception uses server-side side-channel (option B), not client echo or frame-tap (D-13) | TTS Interception | If `StreamWord` cannot easily expose completion to the Session, option C (frame-tap wrapper) adds coupling; low impact |
| A2 | Hub gains `totalAccepted/totalRejected` atomic counters for the metrics shim (option b) | D-04 Rewiring | If rejected, `/metrics` Total* counters report 0 in v1; acceptable degradation |
| A3 | Per-state timeouts live in a new `infra.SessionTimeoutConfig` (or extended `SessionConfig`), env-bound via `bindEnvForAllLeaves` | Timeout Lifecycle | Exact struct name is planner's discretion; if misplaced, env vars won't bind (caught at boot by Validate) |
| A4 | `go.uber.org/goleak` is added for goroutine-leak tests (only new dep) | Validation | If rejected, use `runtime.NumGoroutine()` diffing (slightly flakier); no correctness impact |
| A5 | Session's ctx parent is the per-connection ctx from `HandleWebSocket` (so disconnect cancels Session) | Pitfall 6 | If wrong, sessions leak until deadline; HIGH impact — planner MUST verify the ctx plumbing in handlers.go rework |

**Note:** All code signatures, struct fields, method names, file paths, and line ranges cited as `[VERIFIED]` were read from source this session. The only `[CITED]` claim is Go stdlib `time.After` semantics (confirmed via `go doc`).

## Open Questions

1. **Connection ctx plumbing (Pitfall 6 / A5) — planner must resolve**
   - What we know: `HandleWebSocket` creates `ctx, cancel := context.WithCancel(r.Context())` (`server.go:207`) and passes it to `readPump`/`writePump`. The Router passes the connection ctx to Handlers (`router.go:136`).
   - What's unclear: How does the Session receive this ctx? The session-control handler (reworked `handleStartSession`) receives `ctx` as a Handler arg — but that's the Router-worker ctx, which may outlive the connection. Need to verify whether `readPump`'s ctx cancel propagates to in-flight Router tasks (it should, since Router stores the caller's ctx in `frameTask` per `router.go:160`). **Recommendation:** the Session's `run()` ctx should derive from the same ctx the control-frame handler received, so disconnect cancels it. Planner: trace `readPump` ctx → Router frameTask.ctx → Handler ctx → Session.NewSession(ctx) and confirm the cancel propagates.

2. **DB session ID vs runtime session UUID**
   - What we know: `database.Session.ID` is `int64` (autoincrement); the runtime needs a string UUID for TTS session dir + FSRS scope.
   - Recommendation: Session holds both — `dbID int64` (for audit `UpdateState`/`AppendEvent`) and `sessionID string` (UUID, for TTS/FSRS scope + logging). They are independent; the DB row is audit-only (D-06).

3. **`Metadata` JSON column usage**
   - What we know: `database.Session.Metadata` is a JSON string column (`schema.go:42`). Built for Phase 7 extensibility.
   - Recommendation: store `{"session_uuid":"...","lock_category":"...","lock_unit":"...","word_count":N}` for Phase-10 replay traceability. Planner's discretion.

## Environment Availability

This phase has no NEW external dependencies beyond the existing Go toolchain and the (already-running) Python venv. No environment audit needed beyond confirming the build works.

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | build + test | ✓ | go1.26.0 windows/amd64 `[VERIFIED]` | — |
| Python venv (for FSRS subprocess) | runtime FSRS lock/unlock | ✓ (Phase 3/5 verified) | — | Tests use fake `FSRSService` — no Python needed for unit tests |
| SQLite (modernc.org/sqlite) | DB audit (already wired Phase 4) | ✓ | v1.55.0 `[CITED: STATE.md]` | — |

**Missing dependencies with no fallback:** none.
**Missing dependencies with fallback:** none.

## Security Domain

This phase does **not** introduce new input-validation surfaces (no new HTTP endpoints, no new untrusted-input parsers). It consumes already-validated WS frames (`pkg/ws` already rejects non-binary, oversized, bad-CRC frames at the boundary `[VERIFIED: connection.go:177-190]`). Session control frames (`play_word`/`next_word`) are JSON-decoded with the existing `controlFramePayload` pattern (`handlers.go:23`). No new ASVS categories beyond what Phase 2/6 already enforce.

`security_enforcement` is enabled (absent in config = enabled), but this phase's threat surface is unchanged from Phase 6. The relevant invariants:
- **Resource exhaustion (RES-01/RES-03):** Session count bounded by `Hub.Limit`=100; per-session memory ~50KB (§ Resource Budget).
- **Goroutine leak (T-01-27):** D-10 defer + sweeper backstop + goleak tests.
- **FSRS lock starvation:** `UnlockCards` failure is logged (not fatal) because the 24h TTL self-recovers (Pitfall 1).

No hand-rolled cryptography, auth, or session-token logic in this phase.

## Sources

### Primary (HIGH confidence)
- **Codebase source read this session** (all `[VERIFIED]` citations):
  - `pkg/ws/hub.go`, `connection.go`, `router.go`, `server.go`, `protocol.go` — event-loop + framing patterns
  - `pkg/infra/connection.go` (ConnectionRegistry + cleanupLoop), `worker.go` (WorkerPool), `server.go` (Snapshot wiring), `config.go` (config structs + env binding), `logging.go` (Logger interface)
  - `pkg/fsrs/service.go` — SelectDueCards/UnlockCards signatures + lock semantics
  - `pkg/tts/streamer.go` (StreamWord + word_complete), `generator.go` (GenerateForSession + CleanupSession), `types.go` (Sender seam)
  - `pkg/database/schema.go` (SessionState enum), `session_repo.go` (SessionRepository interface)
  - `pkg/pybridge/executor.go` (DueCard shape, Executor interface), `vocab.go` (unlockArgs category+unit scope)
  - `cmd/service/main.go`, `handlers.go` — current wiring + handler integration points
  - `go.mod` — module path, Go 1.26

### Secondary (MEDIUM confidence)
- `go doc time.After` (Go 1.23+ GC note) — `[CITED]`
- `.planning/phases/07-session-state-management/07-DESIGN-ANALYSIS.md` — pi advisor's gray-area analysis (used to confirm, not to discover; all claims re-verified against source)

### Tertiary (LOW confidence)
- None. No WebSearch or training-knowledge claims are presented as verified. The few `[ASSUMED]` items are in the Assumptions Log.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — Go stdlib only; all signatures verified against source
- Architecture: HIGH — every pattern (Hub.Run, cleanupLoop, WorkerPool, Sender seam) already exists and is tested in this repo
- FSM transitions: HIGH — states verified from `database.SessionState`; transitions derived from D-13 + D-09 + the schema.go doc comment
- Rewiring (D-04): HIGH — the surprising "accept-path already done" finding is grep-verified (zero prod `Register()` callers)
- Pitfalls: HIGH — each grounded in a specific verified code path

**Research date:** 2026-08-08
**Valid until:** 2026-09-08 (stable — no external dependencies; codebase-grounded)

## RESEARCH COMPLETE
