# Phase 7 Design Analysis (pi advisor)

**Dominant architectural shape:** Introduce `pkg/session` as a new orchestration package holding the session state machine, word queue, timeout lifecycle, and a channel-based event loop (modeled on `ws.Hub.Run`). Bind sessions to `ws.Client` (the existing WS connection handle) rather than `infra.ConnectionRegistry` — the Hub already owns client lifecycle, heartbeat, and disconnect cleanup. Keep the DB-backed `database.SessionRepository` for persistence (audit trail, parent review replay) but run state transitions in-memory for hot-path speed. The session event loop serializes all events (control frames, timeouts, TTS completion signals) per session, eliminating mutex contention.

---

## Gray Area 1: Session state machine model

**Options:**
- **A: Typed enum + switch** — `SessionState` as a string enum (already exists in `database.SessionState`) with a `session.transition(event)` method containing a flat switch over `(currentState, event)` pairs. Transition rules live as a method on the Session struct.
- **B: State pattern (per-state handler funcs/structs)** — Each state gets a `SessionStateHandler` interface with `OnEnter`/`OnEvent`/`OnExit` methods. A `StateMachine` dispatches to the current handler.
- **C: External FSM lib** — Import a third-party FSM library (e.g., `looplab/fsm`).

**Codebase grounding:**
- `database/schema.go` already defines `type SessionState string` with 5 constants: `StateIdle`, `StateListening`, `StateDictating`, `StateWaitingParent`, `StateFeedback`. This is the canonical type.
- `infra.ConnectionState` uses int iota with a `String()` method — two styles coexist. The string-enum pattern in `database.SessionState` matches how Go enum sentinels are already consumed throughout the codebase (e.g., `pybridge.Executor` sentinel errors).
- `ws.Router` dispatches via `map[MessageType]Handler` — a functional dispatch map, not a heavy abstraction. The codebase consistently chooses simple, explicit Go over framework magic.
- `fsrs.fsrsService` is a thin orchestration struct. Every "service" in this codebase is a struct with methods, not an abstract framework.

**Recommendation: A** — The string enum already exists, costs nothing, and the 5-state transition table (max ~15 valid transitions) is small enough that a flat switch is more readable than a state-pattern abstraction. A state-per-handler pattern (B) adds ceremony (interface + 5 implementations) for a class of bug (wrong-event-in-wrong-state) that a default `default: return ErrInvalidTransition` switch case catches just as well. An external FSM lib (C) adds a dependency for what is effectively a 15-line switch.

**Decision the user must make:** Confirm that a flat `switch state { case IDLE: switch event { ... } }` transition table in `pkg/session` is acceptable — or do you anticipate more than ~15 transitions that would justify the state-pattern indirection?

---

## Gray Area 2: Session storage & lifecycle

**Options:**
- **A: In-memory map with RWMutex** — `map[string]*Session` guarded by `sync.RWMutex` in a `SessionManager` singleton. Fast, zero serialization overhead, gone on restart.
- **B: DB-backed via SessionRepository** — Every state transition writes to SQLite via `database.SessionRepository.UpdateState()`. Survives restarts.
- **C: Hybrid** — In-memory Session struct for hot-path operations (state transitions, word queue, TTS coordination); `SessionRepository` records lifecycle events and state changes for audit/persistence. The in-memory Session is the authority; the DB is a durable shadow.

**Codebase grounding:**
- `infra.ConnectionRegistry` uses option A exactly: `map[SessionID]*Connection` with `sync.RWMutex`, O(1) lookups. This pattern is proven and tested.
- `database.SessionRepository` already provides `Create`, `UpdateState`, `AppendEvent`, `GetByID`. The `sessions` table schema includes `state TEXT`, `word_count INTEGER`, `metadata TEXT` (JSON extensibility). These were built for Phase 7.
- `ws.Hub` also uses an in-memory `map[*Client]bool` — the codebase has *three* connection/session maps already.
- 150MB budget (RES-01), 100-session limit (RES-03): a `Session` struct with word list (~25 DueCard entries, ~2KB each) is ~50KB per session. 100 sessions = ~5MB. In-memory is well within budget.

**Recommendation: C (hybrid)** — The in-memory Session struct (in `pkg/session`) is the authoritative runtime state. It holds the word queue, current word index, state, timer handles, and WS client reference. On every state transition, the session event loop calls `sessionRepo.UpdateState()` and `sessionRepo.AppendEvent()` as fire-and-forget (don't block the event loop on DB writes — spawn them on the WorkerPool). This gives: (1) fast in-memory state checks, (2) DB audit trail for Phase 10 parent review, (3) resilience — on restart, `GetByID` can reconstruct session history.

**Decision the user must make:** Do you need sessions to survive a process restart (student mid-dictation resumes)? If yes, the in-memory Session must be reconstructable from DB state + re-fetch words from FSRS. If no, DB persistence can be audit-only (fire-and-forget) and in-memory is sufficient.

---

## Gray Area 3: Session ↔ Connection binding + the "unify connection registry" question (P0-2)

**Options:**
- **A: Bind Session to ws.Client; Phase 7 owns unification** — The new `pkg/session.Session` stores a `*ws.Client` reference. The ws.Hub is the canonical registry of active connections. `infra.ConnectionRegistry` is deprecated and removed from the connection-accept path. Session timeout replaces ConnectionRegistry's idle-timeout cleanup.
- **B: Bind Session via SessionID lookup; defer unification** — Session stores only a `SessionID` string (or `infra.SessionID`). When it needs to send a frame, it calls `registry.Lookup(sessionID)` to get the Connection, then somehow bridges to ws.Client.Send(). Requires a bridging layer and keeps both registries alive.
- **C: Bind Session to infra.ConnectionRegistry; demote ws.Hub** — Session is registered via ConnectionRegistry. Hub becomes a dumb relay that only manages gorilla websocket.Conn lifecycle but delegates identity to ConnectionRegistry.

**Codebase grounding:**
- STATE.md P0-2: "统一连接注册表 — infra.ConnectionRegistry vs ws.Hub 双轨制，需 ADR 拍单一真相源"
- `ws.Client` already has `Send([]byte) error` — the method Session needs for pushing control frames (state notifications, session_ready, errors).
- `ws.Hub` already manages: Register (with capacity limit), Unregister (on disconnect → triggers CloseSend), Clients() count. Hub is already the WS lifecycle authority.
- `infra.ConnectionRegistry` adds: UUID-based SessionID, Touch (idle refresh), Stats, LifecycleHooks, token bucket. Much of this is connection-level, not session-level.
- `main.go` wires BOTH: it constructs `infra.Server` (which owns ConnectionRegistry) AND `ws.Hub` independently. They both enforce a `Limit: 100` but against different maps.
- The TTS `Streamer` already sends frames directly to `ws.Client` through the `Sender` interface.
- `tts.Sender` interface exists specifically to avoid `pkg/tts` importing `pkg/ws` — the same pattern should be used for `pkg/session`.

**Recommendation: A — Phase 7 owns the unification, binds Session to ws.Client.** Rationale: (1) The Session MUST push frames to the client (state change notifications, errors, session_ready). The `ws.Client` is the only object with a `Send()` method. Bridging through ConnectionRegistry would add an indirection layer with no benefit. (2) The Hub already detects disconnection (readPump exits → Unregister → CloseSend). Session cleanup on disconnect is natural if Hub owns the binding. (3) ConnectionRegistry's idle-timeout sweeper can be replaced by the Session's own per-state timeouts (Gray Area 4). (4) ConnectionRegistry's UUID-based SessionID is useful — keep the UUID generation but move it into Session creation (or use ConnectionRegistry purely for ID generation initially).

**Blast radius:** Medium. `infra.ConnectionRegistry` is wired into `infra.Server` and the health/metrics endpoints. The migration path: (1) Phase 7 introduces `pkg/session.Session` with a `*ws.Client` field. (2) The `session.Start()` method calls `ws.Hub.Register(client)` (already non-blocking, returns bool). (3) On disconnect, Hub.Unregister triggers `session.Cancel()`. (4) ConnectionRegistry stays in the health/metrics endpoint but stops being the session-accept gate. (5) A follow-up cleanup phase removes ConnectionRegistry entirely.

**Decision the user must make:** Accept that Phase 7 owns the P0-2 unification — Session binds to ws.Client, ws.Hub is the single source of truth for active connections, and infra.ConnectionRegistry is soft-deprecated (kept for metrics but no longer the session gate). Or defer and have Phase 7 work around the dual-track?

---

## Gray Area 4: Timeout & cleanup strategy (ERR-03)

**Options:**
- **A: Per-state timers** — Each state transition sets a `time.AfterFunc` (or `context.WithTimeout`) that fires a timeout event on the session's event channel. The timer is cleared on the next transition. Dead simple, but requires careful timer cleanup.
- **B: Global sweeper goroutine** — A single ticker (like `ConnectionRegistry.cleanupLoop`) scans all active sessions every N seconds, checks per-state deadlines, and transitions timed-out sessions. Coarse granularity but single goroutine.
- **C: Hybrid (per-session deadline + sweeper backstop)** — Each session has a `deadline time.Time` updated on every state transition. The session's own event loop checks `time.After`. A global sweeper (30s tick) catches sessions that died without clean teardown (e.g., goroutine leak). Per-state timeouts: IDLE=30m, LISTENING=30s, DICTATING=30s, WAITING_PARENT=30m, FEEDBACK=5m.

**Codebase grounding:**
- `ConnectionRegistry.cleanupLoop` is a ticker-based sweeper (option B), checking `time.Since(c.LastSeen) > idleTimeout`. Proven and tested pattern.
- `ws.Heartbeat` uses `time.NewTicker` + `ctx.Done()` — the per-connection goroutine pattern already has ticker lifecycle management.
- `context.Context` is threaded through every function — the codebase already has context-propagation infrastructure for cancellation.
- FSRS fetch-lock interaction: On ANY timeout that ends the session, MUST call `fsrsSvc.UnlockCards()` to release the lock. The `SelectDueCards` call in `handleStartSession` acquires a lock via `--fetch-lock-hours` — currently NEVER released.
- The `infra.WorkerPool` submit path shows the codebase's approach to resource release on failure: `Submit` returns typed errors, never silent drops.

**Recommendation: C (hybrid).** The per-state deadline is stored on the Session struct, updated on every `transition()` call. The session's event loop does:

```go
select {
case event := <-s.events:
    s.handleEvent(event)
case <-time.After(time.Until(s.deadline)):
    s.handleTimeout()
case <-s.ctx.Done():
    s.handleCancel()
}
```

The global sweeper (reuse or mirror `ConnectionRegistry.cleanupLoop`) scans sessions whose `lastHeartbeat` exceeds a 2x deadline threshold — catching sessions whose goroutine leaked. On timeout, the session MUST: (1) transition to a terminal state, (2) call `fsrsSvc.UnlockCards()`, (3) call `hub.Unregister(client)`, (4) call `gen.CleanupSession()` to remove TTS cache.

**Timeout durations (recommended defaults, configurable):**
| State | Timeout | Rationale |
|-------|---------|-----------|
| IDLE | 30 min | Session initialized but student hasn't started. Long enough for parental setup. |
| LISTENING | 30 sec | System is playing TTS audio (~2sec/word). 30s covers replay loops. |
| DICTATING | 30 sec | Student is speaking. 30s is generous for a single word. |
| WAITING_PARENT | 30 min | Parent may review later in the day. Long timeout. |
| FEEDBACK | 5 min | Active parent review session. |

**Decision the user must make:** Are these timeout durations reasonable? Also: on timeout mid-dictation — release the FSRS lock (words return to pool) without committing partial progress? Or commit whatever words were completed?

---

## Gray Area 5: FSRS word-lock integration (DICT-02)

**Options:**
- **A: Lock at session init, unlock on session end/timeout** — `SelectDueCards` with `--fetch-lock-hours` acquires the lock. `UnlockCards` releases on FEEDBACK→IDLE or timeout. Partial dictation on timeout: unlock without committing.
- **B: Lock per-word** — Lock each word individually when it's about to be dictated, release after parent rates it. Finer granularity, more Python subprocess calls.
- **C: Lock at session init, commit partial progress on timeout** — On timeout mid-dictation, call `UpdateRatings` with whatever words were completed (default rating "hard" for unrated), then unlock.

**Codebase grounding:**
- `fsrs.FSRSService.SelectDueCards()` passes `FetchLockHours` to `vocab_manager.py due --fetch-lock-hours N`. The Python script sets `fetch_locked_until = NOW + N hours` on selected cards. The lock prevents another concurrent session from selecting the same words.
- `fsrs.FSRSService.UnlockCards()` calls `vocab_manager.py unlock` which clears `fetch_locked_until`. It's a separate CLI path — not automatic.
- `fsrs.FSRSService.UpdateRatings()` calls `vocab_manager.py review` which updates FSRS parameters and clears the lock as a side effect of the review.
- Currently in `handlers.go`, `handleStartSession` calls `SelectDueCards` (acquires lock) but `handleSubmitReview` calls `UpdateRatings` (which clears locks via review). There's NO timeout path that releases locks.
- The `vocab_manager.py` script's due command already has `--peek` mode (no-side-effects, doesn't set lock) — useful for previews.

**Recommendation: A.** Lock at session init, unlock on session end or timeout — WITHOUT committing partial progress. Rationale: (1) If the student didn't complete the dictation, the words should return to the due pool so they appear in the next session. The student needs to redo them. (2) Committing "hard" ratings for incomplete words (option C) would incorrectly train the FSRS algorithm. (3) Per-word locking (option B) would require N Python subprocess calls for a 25-word session — excessive on the 1C/1G VPS (PythonConfig.MaxConcurrent=2).

For WAITING_PARENT → FEEDBACK: Phase 7 does NOT own FSRS parameter updates — Phase 10 (Parent Feedback System) does. Phase 7 provides the infrastructure: the session holds the word list, records which words were completed, and at FEEDBACK→IDLE calls `UnlockCards`. Phase 10 will call `UpdateRatings` based on parent input. Phase 7 can optionally do a "fast path" commit at FEEDBACK→IDLE calling `UpdateRatings` with a default rating for words that were dictated but not rated by the parent.

**Decision the user must make:** (1) Confirm: on timeout mid-dictation, release the lock without committing any progress. (2) Should Phase 7 implement a default-rating commit at session end (WAITING_PARENT→FEEDBACK→IDLE), or defer ALL rating to Phase 10?

---

## Gray Area 6: TTS streaming ↔ state machine coordination

**Options:**
- **A: State transitions driven by TTS completion signals** — The `Streamer.StreamWord()` already sends a `word_complete` status frame at the end. The session event loop intercepts this signal and transitions LISTENING→DICTATING. "Next word" is a client control frame that transitions DICTATING→LISTENING (for the next word).
- **B: State transitions driven by client control frames only** — The client sends explicit `word_finished_playing` / `word_finished_speaking` control frames. The server doesn't infer state from TTS completion.
- **C: Timer-based auto-advance** — After `StreamWord()` completes, the state machine automatically starts a "listening window" timer, then auto-advances to DICTATING.

**Codebase grounding:**
- `tts.Streamer.StreamWord()` already sends a `word_complete` status frame (JSON `{"type":"word_complete","word_id":"..."}`) via `ws.MsgTypeStatus`. This is the natural "TTS audio finished" signal.
- `handlePlayWord` fires `StreamWord` in a goroutine — the handler returns immediately, not waiting for streaming to complete. The session state machine needs a callback or channel from the streaming goroutine.
- `ws.Router` routes frames by MsgType via channel-based dispatch. The session event loop can receive events from multiple sources: client control frames, TTS completion, timeouts.
- `ws.MsgTypeSession` (0x05) is defined but unused — Phase 7 can use this for session-specific control messages.
- The existing `playWordParams` struct includes `session_id` and `word_id` — the client already identifies the session.

**Recommendation: A.** The `word_complete` status frame (already sent by Streamer) is the natural signal to transition LISTENING→DICTATING. The session event loop should be the recipient of this event. Implementation:

1. The session's event loop holds a reference to the streamer.
2. On `PLAY_WORD` control frame → the session calls `streamer.StreamWord()` in a goroutine, with a callback channel.
3. When StreamWord completes (or the context is done), it sends a `WordComplete` event to the session's event channel.
4. The session transitions LISTENING → DICTATING and starts the per-word dictation timer.

For "replay word": the client sends another `play_word` control frame with the same word_id. The session stays in LISTENING, cancels any in-progress stream, and restarts.

For "next word": the client sends a `next_word` control frame. The session transitions DICTATING → LISTENING (advances word index, starts streaming next word).

The key design point: the session owns the word queue (from `SelectDueCards`) and the current word index. The client sends `play_word`, `next_word`, `replay_word` control frames; the session manages the state transitions.

**Decision the user must make:** Should `word_complete` auto-transition (system-driven, option A), or should the client explicitly send a `word_done` frame (client-driven, option B)? Auto-transition is simpler for young students but less flexible for parent-controlled sessions.

---

## Gray Area 7: Concurrency model

**Options:**
- **A: One goroutine per session with event channel** — Each session has a `go s.run()` goroutine that selects on an `events chan SessionEvent`. All state mutations are serialized by the event loop. No mutex needed on Session state. Modeled on `ws.Hub.Run`.
- **B: Mutex on Session struct** — A `sync.Mutex` guards the Session's mutable fields. Handlers acquire the lock, check state, transition, release. Simpler startup (no goroutine spawn) but every handler is a critical section.

**Codebase grounding:**
- `ws.Hub.Run` — single goroutine with `select` on channels (`register`, `unregister`, `ctx.Done()`). This is the canonical concurrency model in the codebase. All Client map mutations happen inside this single goroutine — no mutex needed for the map itself.
- `ws.Client` — per-connection goroutines: readPump (reads WebSocket), writePump (writes to WebSocket). Client state (`sendClosed`, `closeOnce`) uses mutex guards because two goroutines access it.
- `ws.Router.worker` — per-MsgType worker goroutines processing from a channel. Channel-based serialization is the dominant pattern.
- `infra.ConnectionRegistry` — mutex-guarded map (option B pattern). But ConnectionRegistry has many concurrent callers (HTTP handler, cleanup goroutine, health endpoint); sessions have ONE logical writer (the student's WebSocket connection).
- `infra.WorkerPool` — channel-based task queue. Again, channels are the dominant mechanism.
- Resource budget: 100 sessions × 1 goroutine = 100 goroutines for session event loops. The codebase already has ~300 goroutines at steady state (readPump + writePump per connection + Hub.Run + Router workers). Adding 100 more is well within Go's goroutine budget.

**Recommendation: A.** The session event loop mirrors `ws.Hub.Run` exactly — a pattern already tested and documented in this codebase. A single goroutine per session serializes all events: control frames, TTS completion signals, timeouts, and cancellation. The event loop is the ONLY writer to session state — no mutex needed. This avoids the classic mutex bug: forgetting to unlock, holding the lock during I/O (TTS subprocess, DB write), deadlocks from re-entrant calls.

The event types for the channel:
```go
type SessionEvent struct {
    Type    SessionEventType  // ControlFrame, WordComplete, Timeout, Cancel
    Payload interface{}        // *ws.FrameHeader + []byte, wordID string, etc.
}
```

The Router delivers control frames to the session's event channel instead of processing them directly. The session event loop calls handlers, manages timeouts, and coordinates TTS streaming.

**Decision the user must make:** Accept the goroutine-per-session model? On the 1C VPS with 100 sessions, this adds ~100 goroutines (Go handles 100K+ goroutines comfortably). Or would you prefer the mutex approach (option B) to minimize goroutine count?

---

## Cross-cutting concerns

### Dependency between #2 and #3
If Phase 7 owns the unification (#3, option A — Session binds to ws.Client), then Session storage (#2) naturally becomes in-memory + DB audit. The ws.Hub already tracks active connections; adding a parallel in-memory Session map creates a second tracking slot per connection. This is acceptable — Hub tracks connection liveness, Session tracks dictation state. They serve different lifecycles.

### The P0-2 unification recommendation and its blast radius
**Recommendation: Phase 7 OWNS the unification.** The blast radius:
1. **Removed from accept path:** `infra.ConnectionRegistry.Register()` is no longer called on WS upgrade. The hub's `Register` is the single gate.
2. **Kept for metrics:** `ConnectionRegistry` stays for now — the health endpoint reads `registry.Stats()`. Phase 7 can add a shim that feeds Hub stats into the existing `ServerSnapshot.Connections` field.
3. **Session timeout replaces idle timeout:** The session event loop's per-state deadlines replace ConnectionRegistry's flat idle timeout. Hub still handles dead-connection detection (heartbeat failure → readPump exit → Unregister).
4. **WorkerPool unused (P1-5):** The `infra.WorkerPool` is constructed but never used (STATE.md P1-3). Phase 7 could use it for fire-and-forget DB writes (`AppendEvent`), giving it a purpose. Or leave it deferred.

### FSRS lock release on all exit paths
This is the critical cross-cutting invariant: every session exit path (normal completion, timeout, disconnect, error) MUST call `fsrsSvc.UnlockCards()`. The session event loop's `defer` should handle this:

```go
func (s *Session) run() {
    defer func() {
        s.releaseFSRSLock()
        s.cleanupTTS()
        s.hub.Unregister(s.client)
    }()
    // event loop
}
```

---

## Open questions for the user

1. **Restart resilience:** Should sessions survive a process restart? If yes, the in-memory Session must be reconstructable from DB state — this affects the hybrid storage design (#2).

2. **FSRS unlock without COMMIT:** On timeout mid-dictation, unlock without committing any progress — is this the right behavior? The alternative is committing partial progress with default "hard" ratings.

3. **Default rating commit at session end:** Should Phase 7 auto-commit a rating (e.g., "good") for completed words at FEEDBACK→IDLE, or defer ALL rating logic to Phase 10?

4. **word_complete auto-transition:** Should TTS completion automatically advance the state machine (LISTENING→DICTATING), or should the client explicitly control every transition?

5. **Timeout durations:** Are 30s for LISTENING/DICTATING, 30min for IDLE/WAITING_PARENT, and 5min for FEEDBACK reasonable defaults?

6. **Phase ordering:** Phase 07.1 (iOS WebSocket audio fix) is INSERTED after Phase 7 in the roadmap — should Phase 7 proceed before or after 07.1? They touch different concerns but both modify the control-frame handler layer.
