package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// Manager tracks active Sessions and runs the global sweeper backstop (ERR-03,
// D-08). It mirrors infra.ConnectionRegistry in shape: a map guarded by RWMutex,
// a Start method with a CompareAndSwap guard on an atomic.Bool, and a
// ticker-based sweep goroutine that cancels leaked sessions.
type Manager struct {
	// --- Service deps (immutable after NewManager) ---
	hub      SessionHub
	fsrsSvc  fsrs.FSRSService
	gen      TTSSessionGenerator
	streamer SessionStreamer
	repo     database.SessionRepository
	logger   infra.Logger
	pool     *infra.WorkerPool
	sessions map[string]*Session // keyed by connID
	cfg      infra.Config
	reserved int // capacity slots held by in-flight NewSession constructions (Ticket 07: the cap must hold through the seconds-long construction window, not just at the instant of the check)

	// --- Registry ---
	mu      sync.RWMutex
	running atomic.Bool // CompareAndSwap guard for Start (D-08)
}

// NewManager constructs a Manager with nil-guarded dependencies (D-42 DI shape,
// mirroring NewFSRSService / NewConnectionRegistry). gen and streamer take the
// same narrow interfaces NewSession consumes (D-05): production passes the
// concrete *tts.Generator / *tts.Streamer, which satisfy them structurally —
// callers compile unchanged — and integration tests elsewhere can inject
// fakes without constructing a Python-backed TTS stack.
func NewManager(
	hub *ws.Hub,
	fsrsSvc fsrs.FSRSService,
	gen TTSSessionGenerator,
	streamer SessionStreamer,
	repo database.SessionRepository,
	pool *infra.WorkerPool,
	logger infra.Logger,
	cfg infra.Config,
) (*Manager, error) {
	if hub == nil {
		return nil, errors.New("session: hub must not be nil")
	}
	if fsrsSvc == nil {
		return nil, errors.New("session: fsrsSvc must not be nil")
	}
	if gen == nil {
		return nil, errors.New("session: gen must not be nil")
	}
	if streamer == nil {
		return nil, errors.New("session: streamer must not be nil")
	}
	if repo == nil {
		return nil, errors.New("session: repo must not be nil")
	}
	if pool == nil {
		return nil, errors.New("session: pool must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &Manager{
		hub:      hub,
		fsrsSvc:  fsrsSvc,
		gen:      gen,
		streamer: streamer,
		repo:     repo,
		pool:     pool,
		logger:   logger,
		cfg:      cfg,
		sessions: make(map[string]*Session),
	}, nil
}

// Start spawns the global sweeper goroutine. Returns ErrManagerAlreadyRunning
// if already started (CompareAndSwap guard, mirrors connection.go:407-413).
func (m *Manager) Start(ctx context.Context) error {
	if !m.running.CompareAndSwap(false, true) {
		return ErrManagerAlreadyRunning
	}
	go m.sweep(ctx)
	return nil
}

// sweep is the global sweeper backstop (ERR-03). It ticks at
// cfg.Session.CleanupInterval and cancels sessions whose deadline exceeds 2×
// their state timeout. The sweeper's job is narrow: detect leaked goroutines
// whose run() never returned (e.g. stuck in a Python subprocess). Normally-exited
// sessions are already removed from the map by the onExit callback in the D-10
// defer chain.
func (m *Manager) sweep(ctx context.Context) {
	interval := m.cfg.Session.CleanupInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer m.running.Store(false)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.cleanupLeaked()
		}
	}
}

// cleanupLeaked iterates active sessions under RLock, collects those whose
// deadline exceeds 2× their state timeout, and cancels them outside the lock
// (context.CancelFunc is goroutine-safe). The cancel fires the run-loop's
// ctx.Done arm, which fires the D-10 defer chain.
func (m *Manager) cleanupLeaked() {
	type leakedEntry struct {
		cancel context.CancelFunc
		connID string
	}
	var leaked []leakedEntry

	m.mu.RLock()
	for connID, s := range m.sessions {
		if time.Since(s.Deadline()) > 2*s.timeoutFor(s.State()) {
			leaked = append(leaked, leakedEntry{connID: connID, cancel: s.cancel})
		}
	}
	m.mu.RUnlock()

	if len(leaked) == 0 {
		return
	}

	m.logger.Warn("session: sweeper detected leaked sessions",
		infra.F("component", "session"),
		infra.F("count", len(leaked)),
	)
	for _, ls := range leaked {
		if ls.cancel != nil {
			ls.cancel()
		}
	}
}

// NewSession constructs a Session via the package-level NewSession, sets up its
// lifecycle fields (done channel, runCtx/cancel, onExit callback), inserts it
// into the registry, and spawns the event-loop goroutine.
//
// Per Pitfall 6: ctx MUST be the per-connection context so disconnect cancels
// the session immediately.
func (m *Manager) NewSession(
	ctx context.Context,
	client *ws.Client,
	connID string,
	selectOpts fsrs.SelectOpts,
) (*Session, error) {
	// Capacity gate (Ticket 07): reserve the slot under the WRITE lock before
	// the seconds-long construction (FSRS select + TTS pregen) — a check-then-
	// act read lock would let MaxConcurrent simultaneous callers all pass and
	// overshoot the cap. Slots free automatically: the reservation converts
	// into a registry entry on success and is released on failure; the entry
	// itself is removed by onExit on every exit path (D-10).
	m.mu.Lock()
	active := len(m.sessions) + m.reserved
	if active >= m.cfg.Session.MaxConcurrent {
		m.mu.Unlock()
		m.logger.Warn("session: rejected, manager at capacity",
			infra.F("component", "session"),
			infra.F("conn_id", connID),
			infra.F("active", active),
			infra.F("max", m.cfg.Session.MaxConcurrent),
		)
		return nil, ErrManagerAtCapacity
	}
	m.reserved++
	m.mu.Unlock()
	// On any failure path the reservation must be handed back; on success the
	// deferred commit converts it into the registry entry (after `go run` is
	// spawned, so the sweeper's RLock establishes happens-before with the
	// lifecycle fields set below).
	var s *Session
	commit := false
	defer func() {
		m.mu.Lock()
		m.reserved--
		if commit {
			m.sessions[connID] = s
		}
		m.mu.Unlock()
	}()

	var err error
	s, err = NewSession(
		ctx, client, connID,
		m.fsrsSvc, m.gen, m.streamer, m.repo, m.pool,
		m.hub, m.logger, m.cfg, selectOpts,
	)
	if err != nil {
		return nil, err
	}

	// Set up lifecycle fields BEFORE inserting into the map so the sweeper's
	// RLock establishes happens-before with these writes.
	s.done = make(chan struct{})
	s.runCtx, s.cancel = context.WithCancel(ctx) //nolint:gosec // G118: cancel is stored on the session and invoked by the sweeper
	s.onExit = func() {
		m.removeSession(connID, s)
		// D-11 teardown: clear the streamer's per-session sequence counter on
		// every session exit so seqCounters stays bounded under the
		// 100-concurrent budget (RESEARCH Pitfall 5). Keyed by s.ID() — the same
		// session ID passed to StreamWord. Synchronous and non-blocking like
		// removeSession.
		m.streamer.ClearSeqCounter(s.ID())
	}

	// Convert the reservation into the registry entry (the deferred commit
	// runs after this return).
	commit = true

	go s.run(s.runCtx)
	return s, nil
}

// Lookup returns the active Session for the given connID, or nil if not found.
func (m *Manager) Lookup(connID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[connID]
}

// ActiveCount returns the number of live sessions (the capacity gauge the
// NewSession gate checks; tests use it to assert the registry drained).
func (m *Manager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// RecentResults returns the most recent graded sessions (newest first) from
// the session repository — the Ticket 06 history feed for trend and
// repeat-miss analysis in the grading handler.
func (m *Manager) RecentResults(ctx context.Context, limit int) ([]database.SessionResult, error) {
	return m.repo.ListRecentResults(ctx, limit)
}

// Push looks up the session for connID and pushes the event onto its channel.
// No-op if the session is not found (the caller may log this at a higher layer).
func (m *Manager) Push(connID string, ev SessionEvent) {
	s := m.Lookup(connID)
	if s != nil {
		s.Push(ev)
	}
}

// removeSession removes the session from the registry — but only if the
// entry still IS this session (Ticket 07): a same-connID restart replaces the
// map entry, and the OLD session's exit must not delete the NEW one. Called
// from the session's onExit callback in the D-10 defer chain (fired when
// run() exits), so the entry is removed the instant the session terminates —
// regardless of exit cause.
func (m *Manager) removeSession(connID string, s *Session) {
	m.mu.Lock()
	if m.sessions[connID] == s {
		delete(m.sessions, connID)
	}
	m.mu.Unlock()
}
