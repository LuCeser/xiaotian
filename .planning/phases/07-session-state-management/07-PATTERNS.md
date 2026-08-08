# Phase 7: Session State Management - Pattern Map

**Mapped:** 2026-08-08
**Files analyzed:** 14 (6 NEW in `pkg/session/`, 8 MODIFY)
**Analogs found:** 14 / 14 (every file has a real in-repo analog)

> **Path correction (from RESEARCH.md):** the real codebase lives under `pkg/` and `cmd/service/`.
> `internal/` and `cmd/xiaotian/` do NOT exist. Every excerpt below was read from the real path this session.
> Module: `github.com/xiaotian/home-tutor-agent`, Go 1.26.

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `pkg/session/types.go` (NEW) | types / interface-seam | declarative | `pkg/tts/types.go` (`Sender` seam + sentinel errors) | **exact** |
| `pkg/session/fsm.go` (NEW) | state-machine / utility | transform | `pkg/infra/connection.go` (`ConnectionState.String()` switch) + `pkg/database/schema.go` (`SessionState` enum) | role-match |
| `pkg/session/session.go` (NEW) | orchestrator / event-loop | event-driven | `pkg/ws/hub.go` (`Hub.Run` channel-select loop) | **exact** |
| `pkg/session/manager.go` (NEW) | registry / background-goroutine | CRUD + sweeper | `pkg/infra/connection.go` (`ConnectionRegistry` + `cleanupLoop`) | **exact** |
| `pkg/session/audit.go` (NEW) | service / fire-and-forget | batch (async) | `pkg/infra/worker.go` (`WorkerPool.Submit`) | **exact** |
| `pkg/session/*_test.go` (NEW) | test | table-driven | `pkg/database/session_repo_test.go` + `pkg/infra/connection_test.go` | role-match |
| `pkg/infra/connection.go` (MODIFY) | metrics-shim | read-only (Stats) | same file: `Stats()` lines 474-488 | **exact** (in-file) |
| `pkg/infra/worker.go` (MODIFY) | — (gets first caller) | — | same file: `Submit` lines 228-240 | **exact** (in-file) |
| `pkg/infra/server.go` (MODIFY) | rewiring site | request-response (Snapshot) | same file: `Snapshot()` lines 316-330 | **exact** (in-file) |
| `pkg/ws/server.go` (MODIFY — verify only) | accept gate | request-response | same file: `HandleWebSocket` lines 200-204 | **exact** (in-file) |
| `pkg/ws/router.go` (MODIFY) | dispatcher | event-driven | same file: `Route`/`frameTask` lines 136-176 | **exact** (in-file) |
| `cmd/service/handlers.go` (MODIFY) | handler / control-frame | event-driven | same file: `ttsHandlers` switch lines 114-150 | **exact** (in-file) |
| `cmd/service/main.go` (MODIFY) | composition root | construction | same file: construction block lines 117-167, 297 | **exact** (in-file) |
| `pkg/infra/config.go` (MODIFY) | config | declarative | same file: `SessionConfig` 399-414, `FSRSConfig.Validate` 310-325, `bindEnvForAllLeaves` 902-968 | **exact** (in-file) |

## Pattern Assignments

---

### `pkg/session/types.go` (NEW — types / interface-seam / declarative)

**Analog:** `pkg/tts/types.go` (the canonical import-cycle seam in this codebase)

**Imports pattern** — copy verbatim shape from `pkg/tts/types.go:8-11`:
```go
import (
	"context"
	"errors"
)
```

**Interface seam (D-05) — copy `tts.Sender` shape verbatim** (`pkg/tts/types.go:24-31`):
```go
// Sender is the interface for sending binary WebSocket messages (extracted
// from ws.Client.Send so pkg/tts does not import pkg/ws, avoiding circular
// dependency). ws.Client satisfies this interface structurally.
type Sender interface {
	// Send sends a single binary WebSocket message. Returns an error if the
	// send buffer is full or the connection is closed.
	Send(msg []byte) error
}
```
→ New `FrameSender interface { Send(msg []byte) error }` must mirror this **exactly**. `*ws.Client` already satisfies it structurally (`pkg/ws/connection.go:256-268` — `Client.Send(msg []byte) error`).

**Sentinel-error declaration style** — copy from `pkg/tts/types.go:55-83`:
```go
// Sentinel errors returned by the tts layer. Callers MUST detect them via
// errors.Is, never by string matching (D-04).
var (
	ErrFFmpegNotFound = errors.New("ffmpeg not found on PATH")
	ErrPCMConversionFailed = errors.New("MP3 to PCM conversion failed")
	// ...
)
```
→ Declare `ErrInvalidTransition`, `ErrNoDueWords`, `ErrSessionClosed` the same way: a single `var (...)` block, each `errors.New("session: <msg>")`, doc comment above mandating `errors.Is`.

**Reuse `database.SessionState` verbatim** — do NOT redeclare. From `pkg/database/schema.go:16-33`:
```go
type SessionState string

const (
	StateIdle         SessionState = "IDLE"
	StateListening    SessionState = "LISTENING"
	StateDictating    SessionState = "DICTATING"
	StateWaitingParent SessionState = "WAITING_PARENT"
	StateFeedback     SessionState = "FEEDBACK"
)
```
→ `pkg/session` imports `pkg/database` for the enum (no cycle: `pkg/database` only imports `pkg/infra`). Optionally add a type alias `type State = database.SessionState` for brevity.

**`EventType` enum — model on `ws.MessageType`** (`pkg/ws/protocol.go:25-56`):
```go
type MessageType uint8

const (
	MsgTypeAudio     MessageType = 0x01
	MsgTypeControl   MessageType = 0x02
	// ...
)

// String returns a human-readable name for the MessageType (D-40)
func (mt MessageType) String() string {
	switch mt {
	case MsgTypeAudio:
		return "audio"
	// ...
	default:
		return fmt.Sprintf("unknown(0x%02x)", uint8(mt))
	}
}
```
→ Define `EventType` (`EventControlFrame`, `EventWordComplete`, `EventTimeout`, `EventCancel`, `EventParentReview`) with a `String()` method in the same switch style.

**Pattern to follow:**
- Domain services are thin structs with methods + sentinel errors + DI constructors (see `NewBridge`, `NewFSRSService`, `NewStreamer`).
- Package doc at top of file: 1 short paragraph naming the package + import path (mirror `pkg/tts/types.go:1-7`).

**Integration seam:** `types.go` is imported by every other file in `pkg/session/`. The `FrameSender` interface is the type accepted by `Session`/`Manager` constructors (so `pkg/session` does NOT import `pkg/ws` for framing — only for the `*ws.Client` pointer type, same as `pkg/tts/streamer.go:11` imports `pkg/ws` for `FrameHeader`).

---

### `pkg/session/fsm.go` (NEW — state-machine / transform)

**Analog:** `pkg/database/schema.go` (the enum) + `pkg/infra/connection.go` (the switch style). **No FSM exists in the repo yet** — this is the first; D-03 explicitly mandates a flat `switch(state, event)` over a library.

**Switch-on-typed-enum style** — copy from `pkg/infra/connection.go:77-88` (`ConnectionState.String`):
```go
func (s ConnectionState) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateActive:
		return "active"
	case StateClosing:
		return "closing"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}
```

**Pattern to follow** — flat `switch(state){ switch(event){...} }` (D-03), ~15 valid transitions, `default` returns wrapped sentinel:
```go
// Skeleton (shape mirrors the codebase's typed-enum switch):
func (s *Session) transition(ev SessionEvent) error {
	switch s.state {
	case database.StateIdle:
		switch ev.Type {
		case EventControlFrame: // play_word → LISTENING
			// ...
		default:
			return fmt.Errorf("%w: state=%s event=%v", ErrInvalidTransition, s.state, ev.Type)
		}
	case database.StateListening:
		switch ev.Type {
		case EventWordComplete: // → DICTATING (D-13)
			// ...
		// ...
		default:
			return fmt.Errorf("%w: state=%s event=%v", ErrInvalidTransition, s.state, ev.Type)
		}
	// ... 3 more states
	default:
		return fmt.Errorf("%w: state=%s event=%v", ErrInvalidTransition, s.state, ev.Type)
	}
	return nil
}
```

**Error-wrapping convention** — `fmt.Errorf("%w: ...", ErrInvalidTransition, ...)` so callers can `errors.Is(err, ErrInvalidTransition)`. This mirrors `pkg/fsrs/service.go:100-101`:
```go
return nil, fmt.Errorf("fsrs config validation failed: %w (sentinel %w)",
    err, ErrFSRSInvalid)
```

**Integration seam:** called from `Session.handleEvent` inside the event loop (`session.go`). Returns a sentinel on invalid transitions; the event loop logs at Warn but does NOT terminate (mirrors `pkg/ws/connection.go:186-189` "frame decode failed, skipping" philosophy — drop, don't kick).

---

### `pkg/session/session.go` (NEW — orchestrator / event-driven event-loop)

**Analog:** `pkg/ws/hub.go:110-157` — `Hub.Run` is THE canonical channel-select event loop in this repo. D-02 mandates: "Modeled exactly on `ws.Hub.Run`."

**THE pattern to copy — `Hub.Run` channel-select** (`pkg/ws/hub.go:110-157`):
```go
func (h *Hub) Run(ctx context.Context) {
	defer h.Close()
	for {
		select {
		case c := <-h.register:
			// Handle client registration (D-04 capacity limit)
			h.mu.Lock()
			if len(h.clients) >= h.limit {
				h.mu.Unlock()
				_ = c.Close(websocket.CloseTryAgainLater, "server full")
				h.logger.Info("hub rejected client at capacity",
					zap.Int("limit", h.limit))
				continue
			}
			h.clients[c] = true
			// ...
			h.mu.Unlock()

		case c := <-h.unregister:
			// Handle client unregistration (WS-03 session cleanup)
			// ...

		case <-ctx.Done():
			h.drainUnregister()
			return
		}
	}
}
```
→ New `Session.run(ctx)` mirrors this exactly: `for { select { case ev := <-s.events: ...; case <-time.After(time.Until(s.deadline)): ...; case <-ctx.Done(): return } }`. **No mutex on Session fields** — the event loop is the sole writer (D-02).

**Exit-defer invariant (D-10) — model on `Client.readPump` defer chain** (`pkg/ws/connection.go:127-131`):
```go
func (c *Client) readPump(ctx context.Context) {
	// WS-03: Unregister from hub on any exit path (error, panic, or context cancel)
	defer func() {
		c.hub.Unregister(c)
	}()

	// D-49: Recover from panics in handler.Route to prevent connection termination
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("readPump panic recovered",
				zap.Any("recovered", r),
				zap.Stack("stack"))
		}
	}()
	// ...
}
```
→ `Session.run` registers three defers in LIFO order so the run chain is: FSRS unlock LAST (registered first), TTS cleanup, then `hub.Unregister` (registered last, runs first). See RESEARCH.md §"The exit defer chain (D-10)".

**Constructor DI style — copy `NewFSRSService` shape** (`pkg/fsrs/service.go:88-107`):
```go
type fsrsService struct {
	exec   pybridge.Executor
	logger infra.Logger
	cfg    infra.FSRSConfig
}

func NewFSRSService(exec pybridge.Executor, logger infra.Logger, cfg infra.FSRSConfig) (FSRSService, error) {
	if exec == nil {
		return nil, errors.Wrap(ErrFSRSInvalid, "pybridge.Executor must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("fsrs config validation failed: %w (sentinel %w)",
			err, ErrFSRSInvalid)
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &fsrsService{exec: exec, logger: logger, cfg: cfg}, nil
}
```
→ `NewSession(...)` follows the same shape: nil-guard each dep, call `cfg.Validate()`, substitute `infra.NewNoOpLogger()` for nil logger, return `(*Session, error)`. Compile-time interface check: `var _ X = (*Y)(nil)` (see `pkg/fsrs/service.go:78`, `pkg/database/session_repo.go:48`).

**D-11 lock lifecycle — call signatures** (verified from `pkg/fsrs/service.go:113, 174`):
```go
// Acquire at init:
cards, err := fsrsSvc.SelectDueCards(ctx, fsrs.SelectOpts{
	Limit: opts.Limit, Category: opts.Category, Units: opts.Units,
	FetchLockHours: opts.FetchLockHours, // <=0 → cfg.FSRS.FetchLockHours (24.0)
})

// Release at exit (CATEGORY+UNIT scoped, NOT card-ID — Pitfall 4):
n, err := fsrsSvc.UnlockCards(ctx, fsrs.UnlockOpts{
	Category: s.lockCategory, // remember from SelectOpts||cfg.FSRS.DefaultCategory
	Unit:     s.lockUnit,
})
```
→ Store `lockCategory`/`lockUnit` on the Session at init; replay verbatim at unlock. The deferred unlock MUST use a **fresh** `context.WithTimeout(context.Background(), 10*time.Second)` (Pitfall 1 — the run-loop ctx is cancelled by the time defer fires; `pybridge.Bridge.run` selects on `ctx.Done()`).

**TTS kickoff — Session spawns StreamWord itself** (`pkg/tts/streamer.go:53` signature):
```go
func (s *Streamer) StreamWord(ctx context.Context, client Sender, sessionID string, wordID string, startSeq uint32) error
```
→ Per RESEARCH D-13 recommendation (B): Session owns StreamWord invocation; on its return, push `WordComplete{wordID}` onto `s.events`. `handlePlayWord` in `cmd/service/handlers.go` becomes a thin event-pusher (no direct `streamer.StreamWord` call).

**Cleanup at exit** — `tts.Generator.CleanupSession(ctx, sessionID)` (`pkg/tts/generator.go:204-220`); it is idempotent and safe to call from defer.

**Integration seam:** `Manager.NewSession()` constructs a `*Session`, calls `SelectDueCards` + `GenerateForSession` synchronously (before `go s.run(ctx)` — Pitfall 3), then spawns the goroutine. The `ctx` MUST derive from the per-connection ctx (`HandleWebSocket`'s `context.WithCancel(r.Context())` at `pkg/ws/server.go:207`) so disconnect cancels the session immediately (Pitfall 6).

---

### `pkg/session/manager.go` (NEW — registry + background-sweeper goroutine)

**Analog:** `pkg/infra/connection.go` — `ConnectionRegistry` is the verbatim model: a map+RWMutex registry, a `Start(ctx)`-spawned ticker goroutine, atomic counters, and a `Stats()` snapshot.

**Registry struct + constructor — copy `ConnectionRegistry` shape** (`pkg/infra/connection.go:191-272`):
```go
type ConnectionRegistry struct {
	mu              sync.RWMutex
	conns           map[SessionID]*Connection
	limit           int
	idleTimeout     time.Duration
	cleanupInterval time.Duration
	hooks           LifecycleHooks
	logger          Logger
	totalAccepted   atomic.Uint64
	totalRejected   atomic.Uint64
	totalCleaned    atomic.Uint64
	running         atomic.Bool
}

func NewConnectionRegistry(cfg SessionConfig, hooks LifecycleHooks, logger Logger) (*ConnectionRegistry, error) {
	if cfg.MaxConcurrent <= 0 {
		return nil, errors.Wrap(ErrConfigInvalid, "session.maxConcurrent must be > 0 for ConnectionRegistry")
	}
	// ... more validation ...
	if logger == nil {
		logger = NewNoOpLogger()
	}
	return &ConnectionRegistry{
		conns:           make(map[SessionID]*Connection),
		limit:           cfg.MaxConcurrent,
		// ...
	}, nil
}
```
→ `Manager` mirrors this: `mu sync.RWMutex`, `sessions map[string]*Session` (key = connID or session UUID), `limit int` (from `cfg.WS.HubLimit`), `running atomic.Bool`, `logger infra.Logger`. Constructor `NewManager(hub, fsrsSvc, gen, streamer, repo, pool, logger, cfg)` validates each dep, substitutes no-op logger.

**THE sweeper pattern — copy `cleanupLoop` verbatim** (`pkg/infra/connection.go:419-436`):
```go
func (r *ConnectionRegistry) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cleanupInterval)
	defer func() {
		ticker.Stop()
		r.running.Store(false)
	}()
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("connection cleanup goroutine exiting",
				F("component", "infra.connection"),
			)
			return
		case <-ticker.C:
			r.cleanupStale()
		}
	}
}
```
→ `Manager.sweep(ctx)` mirrors this: `time.NewTicker(30*time.Second)`, `defer ticker.Stop()`, `defer m.running.Store(false)`, `select { case <-ctx.Done(): return; case <-ticker.C: m.cleanupLeaked() }`.

**Start spawns the goroutine (CompareAndSwap guard)** (`pkg/infra/connection.go:407-413`):
```go
func (r *ConnectionRegistry) Start(ctx context.Context) error {
	if !r.running.CompareAndSwap(false, true) {
		return errors.Wrap(ErrRegistryAlreadyRunning, "start")
	}
	go r.cleanupLoop(ctx)
	return nil
}
```
→ `Manager.Start(ctx)` identical pattern; define `ErrManagerAlreadyRunning = errors.New("session manager already running")`.

**cleanupStale iteration under lock, hooks OUTSIDE lock** (`pkg/infra/connection.go:446-472`):
```go
func (r *ConnectionRegistry) cleanupStale() {
	now := time.Now()
	var removed []SessionID
	r.mu.Lock()
	for id, c := range r.conns {
		if now.Sub(c.LastSeen) > r.idleTimeout {
			delete(r.conns, id)
			removed = append(removed, id)
		}
	}
	r.mu.Unlock()
	if len(removed) == 0 {
		return
	}
	r.totalCleaned.Add(uint64(len(removed)))
	r.logger.Warn("stale connections cleaned", ...)
	// hooks fire OUTSIDE the lock
}
```
→ `Manager.cleanupLeaked()` mirrors: build list of leaked session IDs under `mu.Lock()`, release lock, then for each call `session.Cancel()` (which cancels its run-loop ctx, firing the D-10 defer chain).

**Stats snapshot pattern** (`pkg/infra/connection.go:474-488`):
```go
func (r *ConnectionRegistry) Stats() ConnectionStats {
	r.mu.RLock()
	active := len(r.conns)
	r.mu.RUnlock()
	return ConnectionStats{
		Active:        active,
		TotalAccepted: r.totalAccepted.Load(),
		// ...
	}
}
```

**Integration seam:** `cmd/service/main.go` constructs `Manager` after the hub/router (line ~297, near `ttsHandlers(...)`), calls `manager.Start(ctx)` in a goroutine alongside `go hub.Run(ctx)` (line 316). Manager.NewSession is invoked from the reworked `handleStartSession` handler.

---

### `pkg/session/audit.go` (NEW — service / fire-and-forget / async)

**Analogs:**
- `pkg/infra/worker.go:228-240` — `WorkerPool.Submit` (the executor, gets its first real caller per D-06 / P1-3).
- `pkg/database/session_repo.go:31-45` — `SessionRepository` interface (the operations to wrap).

**THE Submit signature** (`pkg/infra/worker.go:228-240`):
```go
// Submit enqueues a Task. It never blocks: when the bounded queue is
// full it returns ErrWorkerPoolFull immediately (D-72 backpressure signal).
func (p *WorkerPool) Submit(task Task) error {
	select {
	case p.tasks <- task:
		return nil
	default:
		p.totalBackpressure.Add(1)
		p.logger.Warn("worker pool backpressure (queue full)",
			F("component", "infra.worker"),
			F("queue_capacity", cap(p.tasks)),
		)
		return ErrWorkerPoolFull
	}
}
```
where `type Task func(ctx context.Context) error` (`pkg/infra/worker.go:16`).

**Wrapped audit pattern (D-06)** — each call submits a Task and drops on `ErrWorkerPoolFull`:
```go
func (s *Session) auditState(newState database.SessionState) {
	err := s.pool.Submit(func(ctx context.Context) error {
		return s.repo.UpdateState(ctx, s.dbID, newState) // [VERIFIED: session_repo.go:139]
	})
	if err != nil {
		s.logger.Warn("session audit dropped (worker pool full)", infra.ErrField(err))
	}
}
```

**The wrapped operations** (`pkg/database/session_repo.go:31-45`):
```go
type SessionRepository interface {
	Create(ctx context.Context, session Session) (int64, error)
	UpdateState(ctx context.Context, id int64, state SessionState) error
	AppendEvent(ctx context.Context, event SessionEvent) error
	GetByID(ctx context.Context, id int64) (Session, error)
}
```

**Pattern to follow:**
- NEVER call `repo.*` directly from the event loop (Pitfall 5 — SQLite WAL contention blocks the loop). Always go through `pool.Submit`.
- On `ErrWorkerPoolFull`, log at Warn and drop — the in-memory Session is the authority (D-06); a dropped audit row is a gap in the Phase-10 replay trail, not a correctness loss.
- Wrap each of `Create`, `UpdateState`, `AppendEvent` in its own method (`auditCreate`, `auditState`, `auditEvent`).

**Integration seam:** `Session` holds a `pool *infra.WorkerPool` field, injected via `NewSession`. `audit.go` is imported only by `session.go`'s `transition()` and exit path. This file gives `infra.WorkerPool` its first real `Submit` caller (resolves P1-3).

---

### `pkg/session/*_test.go` (NEW — table-driven tests + fakes)

**Analogs:**
- `pkg/database/session_repo_test.go:1-90` — `t.Run` subtests + `errors.Is` assertions + `newTestDB(t)` helper.
- `pkg/infra/connection_test.go` — registry/sweeper table tests (lines 270-540: `MaxConcurrent`/`IdleTimeout`/`CleanupInterval` config literals; sweeper timing assertions).
- `pkg/ws/hub_test.go` — event-loop / Register / Unregister tests.

**Test shape — copy `session_repo_test.go:11-62`**:
```go
func TestSessionRepository(t *testing.T) {
	t.Run("NewSessionRepository with nil db returns error", func(t *testing.T) {
		_, err := NewSessionRepository(nil, infra.NewNoOpLogger())
		if err == nil {
			t.Fatal("NewSessionRepository(nil, logger) expected error, got nil")
		}
	})

	t.Run("Create with empty ConnectionID is rejected", func(t *testing.T) {
		// ...
		_, err := repo.Create(context.Background(), Session{ConnectionID: "", State: StateIdle})
		if !errors.Is(err, ErrEmptyConnectionID) {
			t.Errorf("Create() error = %v, want errors.Is ErrEmptyConnectionID", err)
		}
	})
}
```

**Table-driven transition tests** (model on `connection_test.go` config-validation table at lines 651-666):
```go
tests := []struct {
	name      string
	cfg       SessionConfig
	wantError bool
}{
	{name: "MaxConcurrent zero", cfg: SessionConfig{MaxConcurrent: 0, ...}, wantError: true},
	// ...
}
for _, tt := range tests {
	t.Run(tt.name, func(t *testing.T) {
		// ...
	})
}
```
→ FSM transition table test: `tests := []struct{ name string; from State; event Event; want State; wantErr error }{...}` covering all ~15 enumerated transitions + 1 invalid case asserting `errors.Is(err, ErrInvalidTransition)`.

**Fakes:** define `fakeFSRSService`, `fakeSessionRepository`, `fakeFrameSender` in `package session_test` (or a `internal_test.go` / `testing.go` file). Fakes implement the interface (`fsrs.FSRSService`, `database.SessionRepository`, `FrameSender`) with configurable return values + call counters. Mirror the structural-satisfaction check at the bottom: `var _ fsrs.FSRSService = (*fakeFSRSService)(nil)`.

**Pattern to follow:**
- `package session_test` (external test package) — imports `pkg/session` as a consumer would.
- Use `infra.NewNoOpLogger()` everywhere (D-42 zero value).
- For goroutine-leak tests: either add `go.uber.org/goleak` (`goleak.VerifyNone(t)` in `TestMain`) OR use `runtime.NumGoroutine()` before/after diffing (RESEARCH planner's discretion).
- For timeout tests: use very short durations (e.g. 50ms) and `<-ctx.Done()` assertions with a test-level `context.WithTimeout` so a bug does not hang the suite (mirror `connection_test.go:497-520` which uses `IdleTimeout: 50ms`, `CleanupInterval: 20ms`).

**Integration seam:** `*_test.go` files live alongside source in `pkg/session/`. Run via `go test ./pkg/session/...`. Fakes can be shared via a `testing.go` file if multiple test files need them.

---

### `pkg/infra/connection.go` (MODIFY — metrics shim + stop dead cleanup goroutine)

**Analog (in-file):** the existing `Stats()` method and `cleanupLoop` — both stay, the *source* of `Stats().Active` changes (D-04 rewiring per RESEARCH.md §"The D-04 Connection Unification").

**Existing `Stats()` to rewire** (`pkg/infra/connection.go:474-488`):
```go
func (r *ConnectionRegistry) Stats() ConnectionStats {
	r.mu.RLock()
	active := len(r.conns)
	r.mu.RUnlock()
	return ConnectionStats{
		Active:        active,
		TotalAccepted: r.totalAccepted.Load(),
		TotalRejected: r.totalRejected.Load(),
		TotalCleaned:  r.totalCleaned.Load(),
	}
}
```

**RESEARCH finding (Critical Path Correction):** `ConnectionRegistry.Register()` already has **zero production callers** — `HandleWebSocket` (`pkg/ws/server.go:200`) calls `hub.Register`, NOT `registry.Register`. So the "remove from accept path" half of D-04 is already done. The genuine D-04 work here is narrow:
1. **Metrics shim:** feed Hub connection counts into `ServerSnapshot.Connections.Active` (currently always reports 0 because the registry map is empty). See RESEARCH D-04 step 2.
2. **Stop the dead cleanup goroutine** OR leave it sweeping an empty map harmlessly (RESEARCH D-04 step 3 — planner's discretion).

**Two viable rewiring approaches (RESEARCH §D-04 Metrics counters gap):**
- **(a) Minimal:** `Stats().Active = 0` forever; Total* stay 0. Acceptable for v1.
- **(b) Add atomic counters to `Hub`** (RECOMMENDED): add `totalAccepted/totalRejected atomic.Uint64` to `Hub` (mirroring `connection.go:227-229`), increment in `Run` on accept/reject/unregister. Hub gains a `Stats() ConnectionStats` method. `Server.Snapshot()` reads Hub stats instead of registry stats. Smallest change that preserves the published `/metrics` contract.

**Pattern to follow if (b):** copy the atomic-counter block from `connection.go:225-229` into `pkg/ws/hub.go`:
```go
// Cumulative metric counters. Manipulated only via atomic ops so a hot
// Register path does not contend on r.mu for stat updates.
totalAccepted atomic.Uint64
totalRejected atomic.Uint64
totalCleaned  atomic.Uint64
```
And increment them in `Hub.Run` (`pkg/ws/hub.go:117-148`) at the accept-reject/unregister branches.

**Integration seam:**
- If option (b): `pkg/ws/hub.go` gets `Stats()`; `pkg/infra/server.go:325` (`Connections: s.registry.Stats()`) is replaced with the Hub-backed provider (see server.go entry below).
- The dead `s.registry.Start(regCtx)` call at `pkg/infra/server.go:242` can be removed (sweeper sweeps an empty map) — OR left; planner's discretion. Removing is cleaner and stops one goroutine.

**checkpoint:decision** — emit this before the task that changes the metrics contract (per CONTEXT.md D-04 + RESEARCH.md).

---

### `pkg/infra/worker.go` (MODIFY — gets first real `Submit` caller)

**Analog (in-file):** `Submit` at lines 228-240 — already correct, no signature change needed.

**Pattern to follow:** No change to this file. The first real caller is `pkg/session/audit.go` (see above). This entry is in PATTERNS.md only to flag that the dependency-edge `pkg/session → pkg/infra` is created here, and that `Submit`'s documented contract (non-blocking, returns `ErrWorkerPoolFull`, MUST NOT be called after `Shutdown`) is what audit.go relies on.

**Integration seam:** `cmd/service/main.go` already constructs and starts the WorkerPool (`infra.NewServer` → `NewWorkerPool` → `Start` at `server.go:187, 250`). `session.Manager` receives the `*WorkerPool` via `server.Components()` (`pkg/infra/server.go:343-345`):
```go
func (s *Server) Components() (mem *MemoryMonitor, p *AudioBufferPool, reg *ConnectionRegistry, w *WorkerPool) {
	return s.memMonitor, s.pool, s.registry, s.workers
}
```

---

### `pkg/infra/server.go` (MODIFY — Snapshot rewiring site)

**Analog (in-file):** the existing `Snapshot()` method — lines 316-330.

**Current `Snapshot()`** (`pkg/infra/server.go:316-330`):
```go
func (s *Server) Snapshot() ServerSnapshot {
	used, limit, pct := s.memMonitor.Snapshot()
	return ServerSnapshot{
		Memory: MemorySnapshot{
			UsedBytes:  used,
			LimitBytes: limit,
			Percent:    pct,
		},
		Pool:         s.pool.Stats(),
		Connections:  s.registry.Stats(),   // ← THIS line: replace with Hub-backed provider
		Workers:      s.workers.Stats(),
		LoadShedding: s.loadShedding.Load(),
		Uptime:       time.Since(s.startedAt),
	}
}
```

**The shape to preserve** (`pkg/infra/server.go:56-63`):
```go
type ServerSnapshot struct {
	Memory       MemorySnapshot  `json:"memory"`
	Pool         PoolStats       `json:"pool"`
	Connections  ConnectionStats `json:"connections"`  // ← keep this struct shape; health.go/metrics untouched
	Workers      WorkerPoolStats `json:"workers"`
	LoadShedding bool            `json:"load_shedding"`
	Uptime       time.Duration   `json:"uptime"`
}
```
→ Keep `ConnectionStats` struct shape so `pkg/infra/health.go:117-125` (the `hta_connections_*` metrics) is untouched. Only the *source* of the values changes.

**Pattern to follow — additive DI:** Either
- add a new field `connStats func() ConnectionStats` to `Server` and a setter `Server.SetConnectionStatsProvider(fn)`, called from `cmd/service/main.go` after the hub is constructed (line ~296) with `func() ConnectionStats { return hubConnectionStats(hub) }`; OR
- add a `*ws.Hub` field to `Server` + constructor arg (more invasive — changes `NewServer` signature, breaks `pkg/infra/server_test.go` literals).

**Recommendation (RESEARCH option b):** the setter approach. `Snapshot()` calls `s.connStats()` if set, else falls back to `s.registry.Stats()`. This is reversible (D-04 soft-deprecation) and keeps `NewServer` test-friendly.

**Integration seam:** `cmd/service/main.go` between hub construction (line ~296) and `server.Start(ctx)` (line 233) — call `server.SetConnectionStatsProvider(...)` passing a closure that reads `hub.Stats()`. Health/metrics handlers (`pkg/infra/health.go`, `pkg/infra/middleware.go`) require NO change.

---

### `pkg/ws/server.go` (MODIFY — verify only; accept path already unified)

**Analog (in-file):** `HandleWebSocket` lines 154-235 — already calls `hub.Register` (line 200), never `ConnectionRegistry.Register`.

**The accept-gate call to confirm/keep** (`pkg/ws/server.go:200-204`):
```go
// Register with hub (will be processed asynchronously). CR-01: Register is
// non-blocking; if the hub is shutting down or saturated it returns false and
// we reject the connection here rather than blocking the HTTP handler
// goroutine — which previously pinned a goroutine forever once Hub.Run had
// stopped draining during shutdown.
if !s.hub.Register(client) {
	s.logger.Warn("hub rejected registration: hub shutting down or saturated")
	_ = client.Close(websocket.CloseTryAgainLater, "server full")
	return
}
```

**Per-connection ctx creation — the Session's ctx parent** (`pkg/ws/server.go:207`):
```go
ctx, cancel := context.WithCancel(r.Context())
defer cancel()
```
→ This `ctx` (passed to `readPump`/`writePump`/Router/Handlers) is the parent the Session's `run()` ctx MUST derive from, so a client disconnect cancels the session immediately (Pitfall 6 / RESEARCH Open Question 1). The control-frame handler receives this ctx as its first arg (`pkg/ws/router.go:40, 136` — Handler signature `func(ctx context.Context, c *Client, ...)`; `Route` stores caller ctx in `frameTask.ctx`).

**Pattern to follow:** Do NOT add a `ConnectionRegistry.Register` call here — it is intentionally NOT the accept gate (D-04). Session creation is wired from the `handleStartSession` handler (in `cmd/service/handlers.go`), which receives the per-connection ctx via the Router. This file likely needs ZERO source changes — listed for the planner to verify.

**Integration seam:** the session-creation hook lives in `cmd/service/handlers.go` (the MsgTypeControl handler), not here. `HandleWebSocket` already does the right thing.

---

### `pkg/ws/router.go` (MODIFY — route session control frames)

**Analog (in-file):** the existing `Route` + `frameTask` + per-MsgType worker — already does the dispatch. The MsgTypeControl handler (in `cmd/service/handlers.go`) is where session frames are routed.

**THE dispatch to extend** (`pkg/ws/router.go:136-176`):
```go
func (r *Router) Route(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
	// ...
	_, ok := r.handlers[h.MsgType]
	if !ok {
		return errors.Wrap(ErrUnknownMessageType, fmt.Sprintf("msg type 0x%02x", h.MsgType))
	}
	task := frameTask{ctx: ctx, client: c, header: h, payload: payload}
	select {
	case r.routes[h.MsgType] <- task:
		return nil
	case <-r.ctx.Done():
		return ErrRouterShuttingDown
	default:
		r.overloaded.Add(1)
		return ErrRouterOverloaded
	}
}
```

**`MsgTypeSession` (0x05) availability** (`pkg/ws/protocol.go:34`):
```go
MsgTypeSession  MessageType = 0x05 // Session management
```
→ Currently unused. RESEARCH recommends keeping `play_word`/`next_word` on `MsgTypeControl` (0x02) for v1 (client already sends them there) and reserving `MsgTypeSession` for future frames. Planner's discretion.

**Pattern to follow:** No structural change to `router.go` itself. The change is in `cmd/service/handlers.go`: the MsgTypeControl handler's `switch control.Type` rewrites `start_session`/`play_word` to push events onto the session's `events` channel (via `Manager.Lookup(connID).Push(...)`) instead of calling `streamer.StreamWord` directly.

**Integration seam:** the `handlers` map built by `ttsHandlers(...)` in `cmd/service/handlers.go:105-152` — this is the place that gains a `Manager` dependency and routes session control frames into it.

---

### `cmd/service/handlers.go` (MODIFY — rewire handlers to push events)

**Analog (in-file):** the current MsgTypeControl switch + `handleStartSession` + `handlePlayWord`.

**Current handler map construction** (`cmd/service/handlers.go:105-152`):
```go
func ttsHandlers(fsrsSvc fsrs.FSRSService, gen *tts.Generator, streamer *tts.Streamer, logger infra.Logger) map[ws.MessageType]ws.Handler {
	return map[ws.MessageType]ws.Handler{
		ws.MsgTypeAudio: func(_ context.Context, _ *ws.Client, _ *ws.FrameHeader, _ []byte) error {
			return nil
		},
		ws.MsgTypeControl: func(ctx context.Context, client *ws.Client, header *ws.FrameHeader, payload []byte) error {
			var control controlFramePayload
			if err := json.Unmarshal(payload, &control); err != nil {
				// ... log + sendErrorFrame ...
				return nil
			}
			switch control.Type {
			case "start_session":
				return handleStartSession(ctx, client, control.Params, fsrsSvc, gen, streamer, logger)
			case "play_word":
				return handlePlayWord(ctx, client, control.Params, streamer, logger)
			// ...
			}
		},
	}
}
```
→ Add a `manager *session.Manager` parameter to `ttsHandlers(...)`. Update the call site in `main.go:297`.

**Current `handleStartSession` to rewire** (`cmd/service/handlers.go:155-222`):
```go
func handleStartSession(ctx context.Context, client *ws.Client, params json.RawMessage, fsrsSvc fsrs.FSRSService, gen *tts.Generator, streamer *tts.Streamer, logger infra.Logger) error {
	// ...
	cards, err := fsrsSvc.SelectDueCards(ctx, fsrs.SelectOpts{Limit: p.Limit, Category: p.Category})
	// ...
	sessionID := fmt.Sprintf("session-%d", time.Now().UnixMilli())  // ← replace with real UUID
	_, err = gen.GenerateForSession(ctx, sessionID, words)
	// ...
}
```
→ Replace the body with: `manager.NewSession(ctx, client, connID, selectOpts)` — which does SelectDueCards + GenerateForSession + SessionRepo.Create + `go session.run(ctx)` internally (the Session struct owns all of this per D-01). The `sessionID := fmt.Sprintf(...)` (line 186) becomes a real UUID via `infra.newUUIDv4` (private — expose a helper or use `crypto/rand` directly; RESEARCH §State of the Art flags this).

**Current `handlePlayWord` to rewire** (`cmd/service/handlers.go:225-245`):
```go
func handlePlayWord(ctx context.Context, client *ws.Client, params json.RawMessage, streamer *tts.Streamer, logger infra.Logger) error {
	var p playWordParams
	// ...
	go func() {
		if err := streamer.StreamWord(ctx, client, p.SessionID, p.WordID, 0); err != nil {
			// ...
		}
	}()
	return nil
}
```
→ Replace the goroutine with `manager.Lookup(connID).Push(session.Event{Type: EventControlFrame, ...})`. The Session event loop spawns `StreamWord` itself (D-13 option B per RESEARCH).

**Pattern to follow:**
- Keep the JSON-decode + `sendErrorFrame` shape (lines 117-124) — it is the established "don't kick the connection on a malformed frame" philosophy.
- Handlers stay thin: decode → push event → return nil. All orchestration moves into `pkg/session`.
- The `sendControlFrame`/`sendErrorFrame` helpers (lines 349-377) stay; Session pushes frames via `FrameSender.Send` (raw `[]byte` built by `ws.EncodeFrame`).

**Integration seam:** `ttsHandlers(...)` signature gains `manager *session.Manager`. The `cmd/service/main.go:297` call site is updated to pass it.

---

### `cmd/service/main.go` (MODIFY — composition root: construct Manager)

**Analog (in-file):** the existing leaf-service construction block — `pyBridge`, `fsrsSvc`, `gen`, `streamer`, `sessionRepo` are all built here with the same DI shape.

**THE construction pattern to extend** (`cmd/service/main.go:117-167`):
```go
pyBridge, err := pybridge.NewBridge(cfg.Python, logger)
if err != nil {
	logger.Error("pybridge construction failed",
		infra.F("component", "infra.main"),
		infra.ErrField(err),
	)
	return 1
}

fsrsSvc, err := fsrs.NewFSRSService(pyBridge, logger, cfg.FSRS)
if err != nil {
	logger.Error("fsrs service construction failed", /* ... */)
	return 1
}

cache, err := tts.NewSessionCache(cfg.TTS.SessionDir, logger)
// ...
gen, err := tts.NewGenerator(pyBridge, cache, logger, cfg.TTS)
// ...
streamer, err := tts.NewStreamer(cache, logger, cfg.TTS)
// ...
```
→ Add the Manager construction here, after `sessionRepo` (line 214) and after `server` (line 101, to access `server.Components()` for the WorkerPool):

```go
// Phase 7 (session orchestration): construct the session Manager — the
// orchestration seam binding ws.Hub + fsrs + tts + database + worker pool.
_, _, _, workers := server.Components()  // [VERIFIED: server.go:343]
manager, err := session.NewManager(hub, fsrsSvc, gen, streamer, sessionRepo, workers, logger, cfg)
if err != nil {
	logger.Error("session manager construction failed",
		infra.F("component", "infra.main"), infra.ErrField(err))
	return 1
}
go manager.Start(ctx)  // spawns the global sweeper goroutine
```

**THE D-04 metrics rewiring site** — between hub construction (line ~296) and `server.Start(ctx)` (line 233). Per RESEARCH option (b), call the new setter on `server`:
```go
// After hub is constructed, before server.Start:
server.SetConnectionStatsProvider(func() infra.ConnectionStats {
	return hubConnectionStats(hub)  // reads hub.Stats() — see pkg/infra/connection.go entry
})
```
(Note: ordering — `server` is built at line 101, `hub` at line 289. So the setter call goes AFTER line 296, BEFORE `server.Start(ctx)` at line 233... but 233 < 296. The planner must either move `server.Start(ctx)` later, OR build the hub earlier, OR pass the hub to NewServer. RESEARCH flags this as the integration-ordering question — see RESEARCH §"What D-04 actually requires" step 1.)

**Handler-map construction to update** (`cmd/service/main.go:297`):
```go
router, err := ws.NewRouter(ttsHandlers(fsrsSvc, gen, streamer, logger), routerCfg, wsLogger)
```
→ Becomes `ttsHandlers(fsrsSvc, gen, streamer, manager, logger)`.

**Pattern to follow:**
- Every constructor follows `NewX(deps..., logger, cfg) (*X, error)` with nil-guard + `cfg.Validate()` + no-op-logger substitution (see `NewFSRSService`, `NewGenerator`, `NewStreamer`).
- Every error path logs `infra.F("component", "infra.main")` + `infra.ErrField(err)` and `return 1`.
- Import block (lines 16-37) gains `"github.com/xiaotian/home-tutor-agent/pkg/session"`.

**Integration seam:** `cmd/service/main.go` is the only file that imports BOTH `pkg/session` AND `pkg/ws`/`pkg/fsrs`/`pkg/tts`/`pkg/database`/`pkg/infra` — it is the composition root. `pkg/session` itself must NOT import `cmd/service`.

---

### `pkg/infra/config.go` (MODIFY — add per-state timeout config + env bindings)

**Analogs (in-file):**
- `SessionConfig` struct (lines 399-414) — the sibling to extend OR the model for a new `SessionTimeoutConfig`.
- `FSRSConfig` struct + `Validate()` (lines 282-325) — the model for a sub-config with its own `Validate()` method called by `Config.Validate()`.
- `bindEnvForAllLeaves` (lines 902-968) — where new env keys are registered.

**Existing `SessionConfig` to extend** (`pkg/infra/config.go:399-414`):
```go
// SessionConfig bounds the active session count and cleanup cadence.
type SessionConfig struct {
	MaxConcurrent    int           `json:"maxConcurrent" mapstructure:"maxConcurrent"`
	IdleTimeout      time.Duration `json:"idleTimeout" mapstructure:"idleTimeout"`
	CleanupInterval  time.Duration `json:"cleanupInterval" mapstructure:"cleanupInterval"`
}
```
→ Option A: extend this struct with `IdleStateTimeout`, `ListeningStateTimeout`, `DictatingStateTimeout`, `WaitingParentStateTimeout`, `FeedbackStateTimeout time.Duration`. Option B: add a sibling `SessionTimeoutConfig` struct (cleaner — keeps registry concerns separate from FSM timeouts). RESEARCH A3 recommends B; planner's discretion.

**THE sub-config-Validate pattern to model** (`pkg/infra/config.go:305-325`):
```go
// Validate enforces the FSRSConfig field ranges (D-03/D-04 fail-fast). On any
// failure it returns an error wrapping ErrConfigInvalid so callers branch with
// errors.Is(err, infra.ErrConfigInvalid). NewFSRSService in Plan 05-02 also
// calls this directly, so the method is independently callable from
// Config.Validate.
func (c FSRSConfig) Validate() error {
	if c.DefaultWordCount < 1 || c.DefaultWordCount > 100 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("fsrs.defaultWordCount must be in [1, 100], got %d", c.DefaultWordCount))
	}
	if c.FetchLockHours <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("fsrs.fetchLockHours must be > 0, got %v", c.FetchLockHours))
	}
	// ...
	return nil
}
```
→ New `SessionTimeoutConfig.Validate()` mirrors this: each timeout MUST be > 0, wrap `ErrConfigInvalid`, return `fmt.Sprintf` with the offending value.

**Config.Validate call site to add the new check** (`pkg/infra/config.go:845-855`):
```go
// FSRS orchestration (Phase 5, D-03). FSRSConfig.Validate enforces field
// ranges so a malformed HTA_FSRS_* value fails boot rather than producing
// silent misconfiguration (D-04 fail-fast). Delegated to the sub-struct so
// NewFSRSService can call the same validator independently.
if err := c.FSRS.Validate(); err != nil {
	return err
}
if err := c.TTS.Validate(); err != nil {
	return err
}
// ← ADD: if err := c.SessionTimeout.Validate(); err != nil { return err }
```

**THE env-binding list to extend** (`pkg/infra/config.go:902-968`):
```go
func bindEnvForAllLeaves(v *viper.Viper) {
	keys := []string{
		// ...
		"session.maxConcurrent",
		"session.idleTimeout",
		"session.cleanupInterval",
		// ...
		"fsrs.defaultWordCount",
		"fsrs.fetchLockHours",
		// ...
		"tts.sessionDir",
		// ...
		"database.path",
		// ← ADD: "session.idleStateTimeout", "session.listeningStateTimeout",
		//        "session.dictatingStateTimeout", "session.waitingParentStateTimeout",
		//        "session.feedbackStateTimeout"
	}
	for _, key := range keys {
		_ = v.BindEnv(key)
	}
}
```
→ Pitfall 6 (documented at `config.go:950-951` for FSRS): every leaf MUST be bound explicitly or `HTA_SESSION_*` env vars are silently inert. Add the 5 new keys with a Phase 7 comment block mirroring the Phase 5/6 comments.

**Default values** — populate in `DefaultConfig()` (the function that builds the default Config; the Session defaults are at `config.go.backup:470-472` showing `MaxConcurrent: 100, IdleTimeout: 5m, CleanupInterval: 30s`). Add D-09 defaults: IDLE=30min, LISTENING=30s, DICTATING=30s, WAITING_PARENT=30min, FEEDBACK=5min.

**Integration seam:** `cfg.SessionTimeout` (or extended `cfg.Session`) is read by `session.NewManager(...)` and threaded into each `Session` for the per-state `deadline` calculation (`session.go` `transition()` per RESEARCH §Timeout Lifecycle).

---

## Shared Patterns

### Constructor DI (apply to ALL new `pkg/session/` files)
**Source:** `pkg/fsrs/service.go:88-107` (`NewFSRSService`), `pkg/tts/streamer.go:34-47` (`NewStreamer`), `pkg/database/session_repo.go:60-68` (`NewSessionRepository`).
**Apply to:** `NewSession`, `NewManager`.
**Shape:**
```go
func NewX(dep1 Dep1, dep2 Dep2, logger infra.Logger, cfg infra.XConfig) (*X, error) {
	if dep1 == nil {
		return nil, errors.Wrap(ErrXInvalid, "dep1 must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("x config validation failed: %w (sentinel %w)", err, ErrXInvalid)
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &X{dep1: dep1, dep2: dep2, logger: logger, cfg: cfg}, nil
}
```
Plus a compile-time check: `var _ Interface = (*impl)(nil)`.

### Sentinel errors via `errors.Is` (apply to ALL new `pkg/session/` files)
**Source:** `pkg/tts/types.go:55-83`, `pkg/infra/connection.go:14-40`, `pkg/infra/worker.go:21-44`, `pkg/database/session_repo.go:14-22`.
**Apply to:** `pkg/session/types.go`.
**Shape:** a single `var (...)` block, each `ErrX = errors.New("session: <msg>")`, doc comment above mandating `errors.Is`. Callers wrap with `errors.Wrap(ErrX, "context")` or `fmt.Errorf("%w: ...", ErrX, ...)`.

### Logging via `infra.Logger` + `infra.F` / `infra.ErrField` (apply to ALL new files)
**Source:** `pkg/infra/logging.go:34-44` (`F`, `ErrField`), `pkg/fsrs/service.go:142-145` (usage).
**Apply to:** every log call in `pkg/session/`.
**Shape:**
```go
s.logger.Warn("session event error",
	infra.F("component", "session"),
	infra.F("session_id", s.id),
	infra.F("state", s.state),
	infra.ErrField(err),
)
```
Never import `go.uber.org/zap` directly in `pkg/session/` — the `infra.Logger` interface is the seam (T-01-09 sanitization flows through it). The `*ws.Client`/Hub take `*zap.Logger` directly (legacy from Phase 2); bridge at the composition root via `infra.ZapLogger.Zap()` (see `cmd/service/main.go:284-287`).

### Channel-select concurrency (apply to `session.go` + `manager.go`)
**Source:** `pkg/ws/hub.go:110-157` (`Hub.Run`), `pkg/infra/worker.go:163-178` (`WorkerPool.worker`), `pkg/infra/connection.go:419-436` (`cleanupLoop`), `pkg/ws/connection.go:127-198` (`readPump`).
**Apply to:** `Session.run`, `Manager.sweep`.
**Shape:**
```go
for {
	select {
	case x := <-ch:
		// handle
	case <-ctx.Done():
		return  // ALWAYS have a ctx.Done arm
	case <-time.After(...):  // session only
		// timeout
	}
}
```
Defers registered BEFORE the loop fire on every exit path (D-10).

### `context.Context` threaded through every function (apply to ALL new files)
**Source:** every analog file — `Hub.Run(ctx)`, `FSRSService.SelectDueCards(ctx, ...)`, `Streamer.StreamWord(ctx, ...)`, `WorkerPool.Submit(task)` (Task is `func(ctx context.Context) error`).
**Apply to:** every public method in `pkg/session/`.
**Shape:** first param is `ctx context.Context`; long-running operations select on `ctx.Done()`; the deferred unlock uses a FRESH `context.WithTimeout(context.Background(), ...)` (Pitfall 1).

### Frame building via `ws.EncodeFrame` at the composition root (apply to `cmd/service/handlers.go`)
**Source:** `cmd/service/handlers.go:349-365` (`sendControlFrame`), `pkg/tts/streamer.go:114-127` (frame build inside Streamer).
**Apply to:** any frame Session needs to push back to the client.
**Shape:** build `ws.FrameHeader{Version: ws.ProtocolVersion, MsgType: ..., PayloadLen: uint16(len(payload))}`, call `ws.EncodeFrame(&header, payload)`, pass the `[]byte` to `FrameSender.Send`. If `pkg/session` must build frames, prefer doing it in `cmd/service/handlers.go` (the composition root) and handing pre-built `[]byte` to Session, to keep `pkg/session` decoupled from the wire encoding (RESEARCH D-05 note).

## No Analog Found

**None.** Every file in scope has at least one close in-repo analog. The four novel pieces — the FSM table, the per-state deadline logic, the FrameSender seam, the Session struct itself — are all compositions of patterns that already exist (typed-enum switch, `time.After`, `tts.Sender`, `Hub.Run`). Per RESEARCH §"Don't Hand-Roll": "Every 'hard' problem in this phase already has a solved, tested implementation in the repo. Phase 7 is a *composition* phase, not an invention phase."

## Metadata

**Analog search scope:** `pkg/ws/`, `pkg/infra/`, `pkg/fsrs/`, `pkg/tts/`, `pkg/database/`, `pkg/pybridge/`, `cmd/service/`.
**Files scanned:** 14 source files + 4 test files (hub_test.go, connection_test.go, session_repo_test.go, server_test.go referenced via grep).
**Pattern extraction date:** 2026-08-08.
**Source confidence:** HIGH — every excerpt quoted above was read from the real `pkg/`/`cmd/service/` path this session; no `internal/` or `cmd/xiaotian/` paths were used (per RESEARCH.md Critical Path Correction).

## PATTERNS COMPLETE
