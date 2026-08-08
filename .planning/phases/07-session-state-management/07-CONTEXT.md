# Phase 7: Session State Management - Context

**Gathered:** 2026-08-08
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 7 delivers the **complete session lifecycle** — the orchestration layer that turns the isolated components (WebSocket from Phase 2, FSRS from Phase 5, TTS from Phase 6) into a real, working dictation session flow. Concretely it delivers:

1. A `pkg/session` package holding the session state machine, word queue, timeout lifecycle, and channel-based event loop.
2. The 5-state lifecycle: `IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK` (requirement DICT-04), with transitions driven by user actions, TTS completion signals, and timeouts.
3. Session initialization that acquires due words from FSRS (requirement DICT-02).
4. Timeout-based cleanup that removes stale sessions and releases FSRS locks without resource leaks (requirement ERR-03).
5. **Resolves P0-2** — unifies the dual-track connection registry by binding Session to `ws.Client` and making `ws.Hub` the single source of truth for active connections (soft-deprecating `infra.ConnectionRegistry`).

**Out of scope:** Parent rating/feedback UI logic belongs to Phase 10 (Parent Feedback System). FSRS parameter *update from parent ratings* is Phase 10's job; Phase 7 only provides the lock/unlock infrastructure and holds the word list. The iOS WebSocket audio fix is Phase 07.1 (after Phase 7).

</domain>

<decisions>
## Implementation Decisions

### Architecture: package & dominant shape
- **D-01:** Introduce a new `pkg/session` package that owns the session state machine, per-session word queue, timeout lifecycle, and a channel-based event loop modeled on `ws.Hub.Run`. The Session struct holds: current state, word queue + current word index, per-state deadline, timer handles, `*ws.Client` reference, and FSRS/TTS/DB service handles. — **Reversibility:** costly — `pkg/session` becomes the central orchestration seam; undo touches every control-frame handler and the main wiring.
- **D-02:** Concurrency model = **one goroutine per session** running an event loop (`select` on an `events chan SessionEvent` + deadline timer + `ctx.Done()`). The event loop is the ONLY writer to session state — no mutex needed on Session fields. Modeled exactly on `ws.Hub.Run`. Event types: `ControlFrame`, `WordComplete`, `Timeout`, `Cancel`. ~100 goroutines at the 100-session limit is well within Go's budget. — **Reversibility:** reversible.
- **D-03:** FSM model = **flat typed-enum `switch(state, event)`** transition table in `pkg/session`. The 5-state machine has ~15 valid transitions — small enough that a flat switch (with `default: return ErrInvalidTransition`) is more readable than a state-pattern abstraction or external FSM lib. Reuses the already-defined `database.SessionState` string enum (`StateIdle`/`StateListening`/`StateDictating`/`StateWaitingParent`/`StateFeedback`).

### Connection binding & P0-2 unification
- **D-04:** **Phase 7 owns the P0-2 unification.** Session stores a `*ws.Client` reference (the only object with a `Send([]byte) error` method, needed to push control/state frames). `ws.Hub` becomes the single source of truth for active connections. `infra.ConnectionRegistry` is **soft-deprecated**: removed from the WS-upgrade accept path (no longer the session gate), but kept temporarily for the health/metrics endpoint (a shim can feed Hub stats into the existing `ServerSnapshot.Connections` field). Follow-up cleanup removes ConnectionRegistry entirely. — **Reversibility:** one-way — removes ConnectionRegistry from the connection-accept path and changes the published contract of `infra.Server`; undo requires re-wiring the accept path and restoring ConnectionRegistry as the session gate. **The planner should emit a `checkpoint:decision` before the task that removes ConnectionRegistry from the accept path.**
- **D-05:** `pkg/session` must NOT import `pkg/ws` directly for framing — use the same interface-seam pattern as `tts.Sender` (a `FrameSender` interface with `Send([]byte) error`) to avoid the import cycle. `ws.Client` satisfies it structurally.

### Session storage
- **D-06:** **Hybrid storage: in-memory Session struct is the authority; DB is audit-only.** `database.SessionRepository` records lifecycle events and state changes via `Create` / `UpdateState` / `AppendEvent` as **fire-and-forget** (spawned on `infra.WorkerPool` — this also gives the currently-unused WorkerPool a purpose, addressing P1-3). The in-memory Session is NOT reconstructed from DB on restart.
- **D-07:** **No restart resilience required.** Sessions do NOT survive a process restart — a student mid-dictation loses the session and re-initiates. DB persistence is audit/trail only (feeds Phase 10 parent review replay). This keeps the implementation minimal per the project's 快速反馈周期优先 philosophy.

### Timeout & cleanup (ERR-03)
- **D-08:** **Hybrid timeout strategy.** Each session carries a per-state `deadline time.Time` updated on every `transition()`. The session event loop selects on `time.After(time.Until(deadline))`. A **global sweeper goroutine** (mirror `ConnectionRegistry.cleanupLoop`, ~30s tick) is a backstop that catches sessions whose goroutine leaked (lastHeartbeat > 2× deadline).
- **D-09:** Per-state timeout defaults (configurable via config, not hardcoded): IDLE=30min, LISTENING=30s, DICTATING=30s, WAITING_PARENT=30min, FEEDBACK=5min.
- **D-10:** **Critical invariant — every session exit path (normal completion, timeout, disconnect, error) MUST release the FSRS lock.** The session event loop's `defer` calls `fsrsSvc.UnlockCards()`, TTS cleanup (`gen.CleanupSession`), and `hub.Unregister(client)`.

### FSRS word-lock integration (DICT-02)
- **D-11:** **Lock at session init, unlock on session end/timeout — WITHOUT committing partial progress.** `SelectDueCards` (with `FetchLockHours`) acquires the lock at session start. On any exit, `UnlockCards` releases it. A student who times out mid-dictation gets the words back in the due pool for the next session (they must redo them). Committing default "hard" ratings for incomplete words is explicitly REJECTED — it would incorrectly train the FSRS algorithm.
- **D-12:** **All rating/feedback logic is deferred to Phase 10.** Phase 7 provides the lock/unlock infrastructure and holds the word list + completion records, but does NOT auto-commit any default rating at FEEDBACK→IDLE. Phase 10 (Parent Feedback System) owns `UpdateRatings` based on parent input.

### TTS ↔ state machine coordination
- **D-13:** **System-driven state transitions via the `word_complete` status frame.** `tts.Streamer.StreamWord()` already emits a `word_complete` JSON status frame on completion — the session event loop intercepts it and transitions LISTENING→DICTATING automatically. Client control frames: `play_word` (start/replay), `next_word` (advance: DICTATING→LISTENING for next word). The session owns the word queue and current index; the client sends high-level intents, not low-level transitions. Simpler for young students. `ws.MsgTypeSession` (0x05, currently unused) is available for session-specific control messages.

### Phase ordering
- **D-14:** **Phase 7 precedes Phase 07.1** (iOS WebSocket audio fix). 07.1 depends on Phase 7 (per ROADMAP). Both touch the control-frame handler layer but with different concerns; doing Phase 7 first establishes the session layer that 07.1's audio work builds on.

### Claude's Discretion
- Exact internal struct field layout and method signatures in `pkg/session` — follow existing codebase conventions (struct + methods, sentinel errors, DI via constructor like `NewBridge`/`NewFSRSService`).
- Whether the global sweeper reuses `ConnectionRegistry.cleanupLoop` code or is a fresh goroutine in `pkg/session`.
- Test strategy (table-driven transition tests mirroring existing test style).

### Folded Todos
- **P0-2 统一连接注册表** (`todos/pending/2026-07-25-unify-connection-registry-adr.md`) — folded INTO Phase 7 scope. Resolved by D-04: Session binds ws.Client, Hub becomes single source of truth, ConnectionRegistry soft-deprecated. An ADR should be produced during planning/execution to record the unification decision.
- **P1-3 WorkerPool 建而不用** (`todos/pending/2026-07-25-resolve-workerpool-unused.md`) — folded INTO Phase 7 scope. Resolved by D-06: the fire-and-forget DB audit writes are submitted to `infra.WorkerPool`, giving it a real Submit caller.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Design analysis (produced this phase)
- `.planning/phases/07-session-state-management/07-DESIGN-ANALYSIS.md` — pi advisor's full codebase-grounded trade-off analysis for all 7 gray areas. **The single richest source of design rationale — read first.**

### Requirements & roadmap
- `.planning/REQUIREMENTS.md` — DICT-02 (session init with due words), DICT-04 (5-state lifecycle), ERR-03 (timeout cleanup); also FSRS-03 (fetch-lock pattern, already implemented in Phase 5), RES-01 (150MB budget), RES-03 (100-session limit).
- `.planning/ROADMAP.md` §Phase 7 — goal, depends-on (Phase 2, 5), success criteria.
- `.planning/STATE.md` — pending todos P0-2 (unify connection registry) and P1-3 (WorkerPool unused), both folded into this phase.

### Project philosophy
- `.planning/CONTEXT.md` — 最小端到端流程 + 快速反馈周期优先 (Fast Feedback Cycle First). Phase 7 is THE phase that proves the end-to-end dictation flow.

### Codebase integration points (read before implementing)
- `internal/database/schema.go` — `type SessionState string` + 5 state constants (canonical enum). `sessions` table: `state TEXT`, `word_count INTEGER`, `metadata TEXT`.
- `internal/database/` (SessionRepository) — `Create`, `UpdateState`, `AppendEvent`, `GetByID` already built for Phase 7.
- `internal/ws/hub.go` — `Hub.Run` (the event-loop pattern to mirror), `Register`/`Unregister`/`Clients()`.
- `internal/ws/client.go` — `Client.Send([]byte) error` (the frame-push method Session needs).
- `internal/ws/router.go` — `map[MessageType]Handler` dispatch; `MsgTypeSession` (0x05) unused and available.
- `internal/tts/streamer.go` — `Streamer.StreamWord()` emits `word_complete` status frame; `Sender` interface (the import-cycle seam pattern to copy for `FrameSender`).
- `internal/fsrs/` (FSRSService) — `SelectDueCards` (acquires fetch-lock), `UnlockCards` (releases), `UpdateRatings` (Phase 10 only).
- `internal/infra/connection_registry.go` — `cleanupLoop` (sweeper pattern), `map[SessionID]*Connection` + RWMutex (storage pattern). To be soft-deprecated per D-04.
- `internal/infra/worker_pool.go` — currently unused; becomes the DB-audit-write executor per D-06.
- `cmd/xiaotian/main.go` (or current wiring location) — where infra.Server (owns ConnectionRegistry) and ws.Hub are both currently constructed; the rewiring site for D-04.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `database.SessionState` enum + `SessionRepository` (Create/UpdateState/AppendEvent/GetByID) — built for Phase 7, ready to use.
- `ws.Hub.Run` channel-select event loop — the canonical concurrency pattern; mirror it in `pkg/session`.
- `ws.Client.Send()` — the frame-push primitive Session needs.
- `tts.Streamer.StreamWord()` + its `word_complete` frame — the TTS-completion signal, already emitted.
- `tts.Sender` interface — the import-cycle seam pattern; copy as `FrameSender` for pkg/session.
- `infra.ConnectionRegistry.cleanupLoop` — the sweeper-goroutine pattern for timeout backstop.
- `infra.WorkerPool` — give it a real caller (fire-and-forget DB writes), resolving P1-3.

### Established Patterns
- Domain services are thin structs with methods + sentinel errors + DI constructors (`NewBridge`, `NewFSRSService`) — `pkg/session` follows the same shape.
- Concurrency is channel-based, not mutex-heavy (`ws.Hub`, `ws.Router.worker`, `infra.WorkerPool`).
- String-enum sentinels over int iota where the value is user/DB-facing (`database.SessionState`).
- `context.Context` is threaded through every function for cancellation.

### Integration Points
- WS upgrade handler — currently calls `ConnectionRegistry.Register`; must switch to `Hub.Register` + create a `pkg/session.Session` (D-04).
- Control-frame router — session control frames (`play_word`, `next_word`) route into the Session event channel instead of being handled inline.
- `handleStartSession` (handlers.go) — currently calls `SelectDueCards` (acquires lock) with no release path; the session event loop's `defer` adds the release.
- health/metrics endpoint — currently reads `ConnectionRegistry.Stats()`; add a Hub→snapshot shim so metrics survive the soft-deprecation.

</code_context>

<specifics>
## Specific Ideas

- The dominant architectural intent (from pi + user): a single `pkg/session` package that is the **orchestration seam** — it owns the event loop, the word queue, the timeouts, and the lifecycle of FSRS locks + TTS streaming + WS frames. Everything else (ws, fsrs, tts, database) stays a leaf service that Session coordinates. Keep it minimal and end-to-end-runnable per the project's fast-feedback philosophy.
- The unification (D-04) is the one genuinely one-way decision and deserves an ADR during this phase.

</specifics>

<deferred>
## Deferred Ideas

- **Per-word FSRS locking** (pi option B, Gray Area 5) — finer lock granularity but N Python subprocess calls per session is too expensive on the 1C/1G VPS (MaxConcurrent=2). Deferred; lock-at-init/unlock-on-end is the v1 choice.
- **Session restart resilience / reconstruction from DB** — explicitly deferred; v1 does not require mid-dictation resume across process restarts.
- **Default-rating fast-path commit at session end** — deferred to Phase 10 (parent feedback owns all ratings).
- **Client-driven state transitions** (explicit `word_done` frames) — deferred; system-driven auto-transition is the v1 choice for student simplicity. Can revisit if parent-controlled sessions need it.

### Reviewed Todos (not folded)
- **仓库卫生批处理** (`todos/pending/2026-07-25-repo-hygiene-readme-gitignore-cleanup.md`) — reviewed, not folded. Out of Phase 7 scope (README/gitignore/binary cleanup is repo hygiene, not session logic).

</deferred>

---

*Phase: 7-Session State Management*
*Context gathered: 2026-08-08 (via orca-dispatched pi design advisor + user confirmation)*
