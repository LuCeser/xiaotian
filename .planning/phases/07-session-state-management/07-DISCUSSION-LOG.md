# Phase 7: Session State Management - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-08-08
**Phase:** 7-Session State Management
**Method:** Design analysis by `pi` advisor (dispatched via Orca orchestration) reading the live codebase + planning docs, producing `07-DESIGN-ANALYSIS.md`. User confirmed all 8 recommendations.
**Areas discussed:** FSM model, session storage, connection binding (P0-2), timeout strategy, FSRS word-lock granularity, TTS↔state coordination, concurrency model, timeout defaults

---

## FSM Model

| Option | Description | Selected |
|--------|-------------|----------|
| A. Flat typed-enum switch | `switch(state, event)` table on `database.SessionState` enum (~15 transitions), default returns ErrInvalidTransition | ✓ |
| B. State-pattern abstraction | Per-state struct/handler objects with Enter/Exit/Handle methods | |
| C. External FSM library | e.g. looplab/fsm declarative transitions | |

**User's choice:** A (flat switch) — accepts pi recommendation. Smallest machine, most readable, reuses existing enum.

---

## Session Storage

| Option | Description | Selected |
|--------|-------------|----------|
| A. In-memory authority, DB audit-only | Session struct is the source of truth; DB records lifecycle events fire-and-forget | ✓ |
| B. DB authority, reload on access | Every state change persisted, session reconstructed from DB | |
| C. Hybrid with restart resilience | In-memory + DB + reconstruction on process restart | |

**User's choice:** A (in-memory authority, no restart resilience). Follows project's 快速反馈周期优先 philosophy; mid-dictation resume across restarts not required for v1.

---

## Connection Binding (P0-2 unification)

| Option | Description | Selected |
|--------|-------------|----------|
| A. Phase 7 owns unification | Session binds `*ws.Client`; `ws.Hub` = single source of truth; `infra.ConnectionRegistry` soft-deprecated (removed from accept path, kept for metrics) | ✓ |
| B. Defer to later phase | Keep dual-track, add mapping layer in Phase 7 | |
| C. Full removal of ConnectionRegistry | Delete ConnectionRegistry entirely now | |

**User's choice:** A — Phase 7 owns the unification. **One-way decision** (changes infra.Server accept-path contract); planner must emit `checkpoint:decision` before the task removing ConnectionRegistry from the accept path. Follow-up cleanup removes it entirely.

---

## Timeout Strategy (ERR-03)

| Option | Description | Selected |
|--------|-------------|----------|
| A. Hybrid: per-state deadline + global sweeper backstop | Event loop selects on per-state deadline timer; global sweeper goroutine (~30s tick) catches leaked sessions | ✓ |
| B. Per-state deadline only | No sweeper; rely solely on event-loop timer | |
| C. Global sweeper only | Single sweeper scanning all sessions periodically | |

**User's choice:** A (hybrid). Mirrors `ConnectionRegistry.cleanupLoop` for the backstop.

---

## Timeout Defaults

| Option | Description | Selected |
|--------|-------------|----------|
| A. pi's proposed defaults (configurable) | IDLE=30min, LISTENING=30s, DICTATING=30s, WAITING_PARENT=30min, FEEDBACK=5min | ✓ |
| B. Tighter defaults | Halve all durations | |
| C. Single global timeout | One duration for all states | |

**User's choice:** A — accept pi's defaults, exposed via config (not hardcoded).

---

## FSRS Word-Lock Granularity (DICT-02)

| Option | Description | Selected |
|--------|-------------|----------|
| A. Lock-at-init, unlock-on-exit (no partial commit) | `SelectDueCards` acquires lock at start; on any exit `UnlockCards` releases; timed-out words return to due pool (no default rating committed) | ✓ |
| B. Per-word locking | Lock each word individually as it's presented | |
| C. Lock-at-init + commit default "hard" ratings on timeout | Default-rate incomplete words at session end | |

**User's choice:** A. Per-word (B) too expensive (N Python subprocess calls on 1C/1G VPS). Default-rating (C) explicitly rejected — would incorrectly train FSRS. All rating logic deferred to Phase 10.

---

## TTS ↔ State Machine Coordination

| Option | Description | Selected |
|--------|-------------|----------|
| A. System-driven auto-transition via `word_complete` frame | `tts.Streamer` already emits `word_complete`; event loop intercepts it and drives LISTENING→DICTATING. Client sends high-level intents (play_word/next_word) | ✓ |
| B. Client-driven transitions | Client sends explicit `word_done` frames; server reacts | |
| C. Mixed | Some transitions client-driven, some system-driven | |

**User's choice:** A (system-driven). Simpler for young students; client sends intents, not low-level transitions. `ws.MsgTypeSession` (0x05, currently unused) available for session control messages.

---

## Concurrency Model

| Option | Description | Selected |
|--------|-------------|----------|
| A. Goroutine-per-session event loop | One goroutine per session, `select` on events chan + deadline + ctx.Done; event loop is sole writer (no mutex) | ✓ |
| B. Shared map + RWMutex | Single registry with mutex-protected sessions | |
| C. Actor framework | Full actor model (e.g. protoactor-go) | |

**User's choice:** A. Mirrors `ws.Hub.Run` pattern. ~100 goroutines at the 100-session limit is within Go's budget.

---

## Phase Ordering

| Option | Description | Selected |
|--------|-------------|----------|
| A. Phase 7 before Phase 07.1 | Establish session layer first; 07.1 (iOS WS audio) builds on it | ✓ |
| B. Phase 07.1 before Phase 7 | Fix iOS audio first | |

**User's choice:** A. Phase 07.1 depends on Phase 7 (per ROADMAP).

---

## Claude's Discretion

- Exact struct field layout and method signatures in `pkg/session` (follow existing `NewBridge`/`NewFSRSService` conventions).
- Whether the global sweeper reuses `ConnectionRegistry.cleanupLoop` code or is a fresh goroutine.
- Test strategy (table-driven transition tests mirroring existing test style).

## Deferred Ideas

- **Per-word FSRS locking** — finer granularity but too expensive on 1C/1G VPS (MaxConcurrent=2 Python subprocesses).
- **Session restart resilience / reconstruction from DB** — v1 does not require mid-dictation resume across process restarts.
- **Default-rating fast-path commit at session end** — deferred to Phase 10 (parent feedback owns all ratings).
- **Client-driven state transitions** (explicit `word_done` frames) — deferred; system-driven auto-transition is the v1 choice. Revisit if parent-controlled sessions need it.
