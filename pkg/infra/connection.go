package infra

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

// Connection-lifecycle sentinel errors. Callers MUST branch on these via
// errors.Is rather than string matching (D-05 rich error handling).
var (
	// ErrTooManyConnections is returned by ConnectionRegistry.Register when the
	// registry is at capacity (D-56, RES-03). Plan 08's HTTP/WS server
	// translates this sentinel into a 429 Too Many Requests response so
	// well-behaved clients can back off.
	ErrTooManyConnections = errors.New("too many concurrent connections")

	// ErrConnectionNotFound is returned by Lookup, Unregister, and Touch when
	// the supplied SessionID does not resolve to an active connection. This is
	// the stateless token-verification miss path (D-65): the caller supplies a
	// token, the registry reports presence/absence.
	ErrConnectionNotFound = errors.New("connection not found")

	// ErrRegistryAlreadyRunning is returned by Start when the cleanup goroutine
	// is already running. Renamed from the more obvious ErrAlreadyRunning to
	// avoid colliding with a sibling symbol expected from Plan 04 (memory.go)
	// — both packages share pkg/infra per D-02, so every exported sentinel
	// must be uniquely named within the package.
	ErrRegistryAlreadyRunning = errors.New("connection registry already running")

	// ErrInvalidThrottleConfig is returned by NewTokenBucket when ratePerSec
	// or burst are non-positive. Throttling is a DoS mitigation (T-01-25) so
	// a non-functional bucket is a fail-closed configuration error.
	ErrInvalidThrottleConfig = errors.New("invalid throttle configuration")
)

// SessionID is the typed session identifier (D-40 domain newtype). It is the
// stateless auth-token surface (D-65): the registry performs only token
// presence/absence verification via map lookup. Cryptographic validation is
// deferred to the Phase 2 caller — the SessionID itself IS the token in
// Phase 1's stateless model.
//
// The underlying string is a canonical UUID v4 (D-58, see newUUIDv4) — 122
// bits of crypto-random entropy from crypto/rand, collision probability
// negligible (T-01-23 mitigation).
type SessionID string

// ConnectionState is the typed connection lifecycle state (D-60, D-40). Three
// states only for Phase 1; future phases may extend the iota without breaking
// callers because every transition is mediated by the registry.
type ConnectionState int

const (
	// StateConnecting is the initial state for an inbound connection that has
	// not yet completed the protocol handshake. Phase 1's registry does not
	// emit this state — every Register call goes straight to StateActive —
	// but the value exists so Phase 2 can introduce a pre-active window
	// without reshaping the type.
	StateConnecting ConnectionState = iota
	// StateActive is the steady-state for a registered connection. Every
	// connection returned by Register enters this state immediately.
	StateActive
	// StateClosing is the draining state for a connection mid-Unregister.
	// Phase 1 transitions StateActive -> <removed> atomically (no visible
	// StateClosing); the value exists for future graceful-drain support.
	StateClosing
)

// String returns the lowercase state name for structured-log fields. Keeps
// log output stable across phases even if the underlying numeric values
// shift.
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

// Connection is the per-session state tracked by the registry. The struct is
// treated as immutable from the caller's perspective: every mutation goes
// through ConnectionRegistry methods (Touch updates LastSeen, Unregister
// removes the entry). Lookup returns a copy by value so callers cannot
// mutate the registry's interior state.
type Connection struct {
	// ID is the SessionID assigned at Register time. Unique within the
	// registry's lifetime (UUID v4 collision probability negligible per
	// D-58/T-01-23).
	ID SessionID

	// State is the current lifecycle state (D-60). Always StateActive for
	// connections returned by Register in Phase 1.
	State ConnectionState

	// CreatedAt is when the Register call completed. Used by stats and by
	// downstream observability code to compute session duration.
	CreatedAt time.Time

	// LastSeen is the most recent activity timestamp, updated by Touch. The
	// cleanup goroutine reaps connections whose time.Since(LastSeen) exceeds
	// SessionConfig.IdleTimeout (D-57, D-59).
	LastSeen time.Time

	// Metadata is an optional caller-supplied tag map. The registry does not
	// interpret these values; they exist so Phase 2's WebSocket upgrade
	// handler can attach protocol-level context (origin, user-agent, sub-protocol)
	// at Register time without expanding the registry's own API.
	Metadata map[string]string
}

// LifecycleHooks is the caller-supplied event sink (D-55). Every field is
// optional — the registry nil-checks each invocation. Hooks fire from the
// goroutine that triggered the event (Register / Unregister / cleanup
// goroutine), so hook implementations MUST be re-entrant if they touch
// shared state.
//
// Threat note (T-01-26 mitigation): the registry emits an Info/Warn log
// entry alongside every hook invocation, so the audit trail exists even
// when a hook is not configured.
type LifecycleHooks struct {
	// OnConnect fires after a successful Register, with the newly-assigned
	// SessionID. It does NOT fire when Register is rejected at capacity
	// (those events go to OnError).
	OnConnect func(SessionID)

	// OnDisconnect fires after a connection is removed, regardless of
	// whether removal was caller-initiated (Unregister) or cleaner-initiated
	// (stale-eviction). The future client-side reconnect manager (D-67)
	// consumes this signal.
	OnDisconnect func(SessionID)

	// OnError fires when an event-level error occurs. In Phase 1 the only
	// trigger is Register-at-capacity; Phase 2 will additionally trigger it
	// from the on-accept protocol validator (D-62). The SessionID argument
	// is the empty string for Register-rejections (no ID was ever assigned).
	OnError func(SessionID, error)
}

// ConnectionStats is the aggregate-metrics snapshot (D-61). Every field is
// point-in-time consistent: Active is sampled under the registry's read
// lock, and the Total* counters are loaded atomically, so the returned
// struct reflects a single moment in registry history.
//
// Plan 08's metrics endpoint renders this struct into Prometheus gauge /
// counter families.
type ConnectionStats struct {
	// Active is the current number of registered connections. Sampled under
	// the registry read lock.
	Active int

	// TotalAccepted is the monotonically-increasing count of successful
	// Register calls since the registry was constructed. Tracked atomically.
	TotalAccepted uint64

	// TotalRejected is the monotonically-increasing count of Register calls
	// rejected at capacity (ErrTooManyConnections). Tracked atomically.
	TotalRejected uint64

	// TotalCleaned is the monotonically-increasing count of connections
	// removed by the stale-eviction goroutine. Caller-initiated Unregister
	// calls do NOT increment this counter. Tracked atomically.
	TotalCleaned uint64
}

// ConnectionRegistry is the bounded in-memory pool of active connections
// (D-54, D-63). The registry IS the connection pool: a fixed-capacity map
// (MaxConcurrent=100 by default per RES-03/D-56) whose entries are reused
// across ticks via Touch rather than reallocated.
//
// Concurrency model:
//   - r.mu (sync.RWMutex) guards r.conns. Register/Unregister/Touch acquire
//     the write lock; Lookup acquires the read lock. O(1) lookup per D-54.
//   - r.running (atomic.Bool) guards Start so a second Start call returns
//     ErrRegistryAlreadyRunning instead of spawning a second cleaner.
//   - r.totalAccepted / totalRejected / totalCleaned (atomic.Uint64) track
//     cumulative metrics without contending on r.mu.
//
// Cancellation: Start(ctx) spawns a cleanup goroutine that exits when ctx
// is cancelled. TestRegistryStopsOnContextCancel asserts this exit happens
// within 200ms to defend against goroutine leaks (T-01-27 mitigation).
type ConnectionRegistry struct {
	// mu guards conns, limit, idleTimeout, cleanupInterval, hooks, and
	// logger. Held for writes during Register/Unregister/Touch/cleanupStale
	// and for reads during Lookup/Stats.
	mu sync.RWMutex

	// conns is the live connection pool. Pointer values so Touch can mutate
	// LastSeen in place; the registry never hands the pointer to callers
	// (Lookup returns a copy by value).
	conns map[SessionID]*Connection

	// limit is the hard cap on len(conns). Set once at construction time;
	// never mutated. Equal to cfg.MaxConcurrent.
	limit int

	// idleTimeout is how long a connection may go without a Touch before the
	// cleaner reaps it (D-57, D-59). Equal to cfg.IdleTimeout.
	idleTimeout time.Duration

	// cleanupInterval is the cadence at which the cleaner sweeps stale
	// connections (D-57). Equal to cfg.CleanupInterval.
	cleanupInterval time.Duration

	// hooks is the caller-supplied event sink (D-55). Zero-value (all-nil)
	// is valid; every invocation is nil-checked.
	hooks LifecycleHooks

	// logger is the structured event sink (D-25, D-47). Defaults to
	// NewNoOpLogger() when the caller passes nil so the registry is always
	// observable. Every Info/Warn entry is tagged with
	// F("component", "infra.connection") so operators can filter infra
	// connection logs from websocket / TTS logs.
	logger Logger

	// Cumulative metric counters. Manipulated only via atomic ops so a hot
	// Register path does not contend on r.mu for stat updates.
	totalAccepted atomic.Uint64
	totalRejected atomic.Uint64
	totalCleaned  atomic.Uint64

	// running guards Start so a second call returns ErrRegistryAlreadyRunning
	// instead of spawning a duplicate cleaner. Set via CompareAndSwap in
	// Start; cleared by the cleanup goroutine's deferred teardown.
	running atomic.Bool
}

// NewConnectionRegistry constructs a registry bounded by cfg.MaxConcurrent
// with stale-eviction driven by cfg.IdleTimeout and cfg.CleanupInterval
// (D-42 constructor DI, D-44 centralized validation). The returned registry
// is safe for concurrent use immediately; call Start to enable the
// background cleanup goroutine.
//
// Validation:
//   - cfg.MaxConcurrent > 0 (production default 100 per RES-03/D-56).
//   - cfg.IdleTimeout > 0 and cfg.CleanupInterval > 0.
//
// logger may be nil — the constructor substitutes NewNoOpLogger() in that
// case so the registry is always observable (D-47 no global state, but
// always non-nil logger). hooks may be the zero value — every invocation
// is nil-checked.
func NewConnectionRegistry(cfg SessionConfig, hooks LifecycleHooks, logger Logger) (*ConnectionRegistry, error) {
	if cfg.MaxConcurrent <= 0 {
		return nil, errors.Wrap(ErrConfigInvalid, "session.maxConcurrent must be > 0 for ConnectionRegistry")
	}
	if cfg.IdleTimeout <= 0 {
		return nil, errors.Wrap(ErrConfigInvalid, "session.idleTimeout must be > 0 for ConnectionRegistry")
	}
	if cfg.CleanupInterval <= 0 {
		return nil, errors.Wrap(ErrConfigInvalid, "session.cleanupInterval must be > 0 for ConnectionRegistry")
	}
	if logger == nil {
		logger = NewNoOpLogger()
	}
	return &ConnectionRegistry{
		conns:           make(map[SessionID]*Connection),
		limit:           cfg.MaxConcurrent,
		idleTimeout:     cfg.IdleTimeout,
		cleanupInterval: cfg.CleanupInterval,
		hooks:           hooks,
		logger:          logger,
	}, nil
}

// Register adds a new connection to the registry (D-63 pool add). Returns
// ErrTooManyConnections (translatable to HTTP 429 by Plan 08's server)
// when the registry is at capacity — active connections are NEVER evicted
// to make room for a new one (D-64 binary priority: active beats new).
//
// The newly-assigned SessionID is a UUID v4 generated via newUUIDv4 (D-58,
// 122 bits of crypto-random entropy — collision probability negligible per
// T-01-23 mitigation).
//
// Lifecycle:
//  1. On success: increments totalAccepted, invokes OnConnect(id), logs
//     Info "connection registered".
//  2. On capacity rejection: increments totalRejected, invokes OnError if
//     non-nil, logs Warn "connection rejected (at capacity)". OnConnect is
//     NOT invoked (testified by TestRegisterRejectsAtCapacity).
func (r *ConnectionRegistry) Register() (*Connection, error) {
	r.mu.Lock()
	if len(r.conns) >= r.limit {
		active := len(r.conns)
		r.mu.Unlock()
		r.totalRejected.Add(1)
		if r.hooks.OnError != nil {
			r.hooks.OnError("", ErrTooManyConnections)
		}
		r.logger.Warn("connection rejected (at capacity)",
			F("component", "infra.connection"),
			F("limit", r.limit),
			F("active", active),
			ErrField(ErrTooManyConnections),
		)
		return nil, errors.Wrap(ErrTooManyConnections, "register")
	}

	idStr, err := newUUIDv4()
	if err != nil {
		// Crypto-random failure is exceptional and unrecoverable; surface it
		// rather than synthesizing a deterministic ID. Does not count as a
		// rejection for metrics purposes (the slot was available).
		r.mu.Unlock()
		return nil, errors.Wrap(err, "generate session id")
	}
	id := SessionID(idStr)
	now := time.Now()
	conn := &Connection{
		ID:        id,
		State:     StateActive,
		CreatedAt: now,
		LastSeen:  now,
	}
	r.conns[id] = conn
	r.mu.Unlock()

	r.totalAccepted.Add(1)
	if r.hooks.OnConnect != nil {
		r.hooks.OnConnect(id)
	}
	r.logger.Info("connection registered",
		F("component", "infra.connection"),
		F("session_id", string(id)),
	)
	return conn, nil
}

// Unregister removes a connection by ID (D-63 pool remove). Caller-initiated
// removal — the cleaner's stale-eviction path goes through cleanupStale
// instead. Returns ErrConnectionNotFound when the ID is not registered
// (stateless token miss per D-65).
//
// OnDisconnect fires AFTER the connection is removed from the map and the
// write lock released, so the hook can re-enter the registry (e.g. to call
// Stats) without deadlock.
func (r *ConnectionRegistry) Unregister(id SessionID) error {
	r.mu.Lock()
	if _, ok := r.conns[id]; !ok {
		r.mu.Unlock()
		return errors.Wrap(ErrConnectionNotFound, "unregister")
	}
	delete(r.conns, id)
	r.mu.Unlock()

	if r.hooks.OnDisconnect != nil {
		r.hooks.OnDisconnect(id)
	}
	r.logger.Info("connection unregistered",
		F("component", "infra.connection"),
		F("session_id", string(id)),
	)
	return nil
}

// Lookup resolves a SessionID to its Connection (D-54 O(1) map lookup, D-65
// stateless token verification). Returns a copy by value so callers cannot
// mutate the registry's interior state — every mutation goes through
// Unregister or Touch. Returns ErrConnectionNotFound when the ID is not
// registered.
func (r *ConnectionRegistry) Lookup(id SessionID) (Connection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.conns[id]
	if !ok {
		return Connection{}, errors.Wrap(ErrConnectionNotFound, "lookup")
	}
	return *c, nil
}

// Touch refreshes the connection's LastSeen timestamp to time.Now() (D-63
// pool reuse — Touch is how an active connection avoids stale-eviction
// without re-registering). Returns ErrConnectionNotFound when the ID is not
// registered.
//
// Callers SHOULD call Touch on every observed activity (incoming frame,
// heartbeat, etc.) so the cleaner's idleTimeout gate reflects real
// liveness rather than connection-open time.
func (r *ConnectionRegistry) Touch(id SessionID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.conns[id]
	if !ok {
		return errors.Wrap(ErrConnectionNotFound, "touch")
	}
	c.LastSeen = time.Now()
	return nil
}

// Start spawns the stale-connection cleanup goroutine (D-57). The goroutine
// ticks at cleanupInterval and reaps connections whose time.Since(LastSeen)
// exceeds idleTimeout. Returns ErrRegistryAlreadyRunning on a second call
// without cancelling the first.
//
// Cancellation: pass a context whose cancellation signals cleaner exit. On
// ctx.Done() the goroutine returns within one tick; TestRegistryStopsOnContextCancel
// asserts the exit happens within 200ms. The r.running flag is cleared on
// goroutine exit, so a fresh Start can succeed after ctx cancel.
func (r *ConnectionRegistry) Start(ctx context.Context) error {
	if !r.running.CompareAndSwap(false, true) {
		return errors.Wrap(ErrRegistryAlreadyRunning, "start")
	}
	go r.cleanupLoop(ctx)
	return nil
}

// cleanupLoop is the goroutine body spawned by Start. Exits on ctx.Done()
// and clears r.running on the way out so a subsequent Start can succeed.
// Separated from cleanupStale so ctx cancellation is checked between ticks
// without holding any lock.
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

// cleanupStale is one sweep of the stale-eviction policy (D-57, D-59). It
// acquires the write lock, builds the list of IDs whose LastSeen exceeds
// idleTimeout, removes them in place, releases the lock, then fires
// OnDisconnect for each removed ID OUTSIDE the lock (so the hook cannot
// deadlock the registry).
//
// OnDisconnect firing for cleaner-removed connections is what lets the
// future client-side reconnect manager (D-67) observe server-side eviction.
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
	r.logger.Warn("stale connections cleaned",
		F("component", "infra.connection"),
		F("count", len(removed)),
	)
	if r.hooks.OnDisconnect != nil {
		for _, id := range removed {
			r.hooks.OnDisconnect(id)
		}
	}
}

// Stats returns a point-in-time snapshot of aggregate metrics (D-61).
// Active is sampled under the read lock; the Total* counters are loaded
// atomically. Plan 08's /metrics endpoint renders this struct into
// Prometheus gauge/counter families.
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

// newUUIDv4 returns a freshly-generated canonical UUID v4 string (D-58).
// Implemented with crypto/rand from the standard library so this package
// does not need to add github.com/google/uuid as a module dependency —
// important under the parallel-execution constraint that forbids touching
// go.mod/go.sum while sibling plans (memory.go, pool.go, worker.go) add
// their own files concurrently.
//
// The 122 bits of entropy (16 bytes minus 2 version/variant bits) come from
// the OS CSPRNG via crypto/rand, satisfying the T-01-23 collision-mitigation
// requirement. The output is formatted in the canonical 8-4-4-4-12 hex
// shape so callers can validate via the standard UUID v4 regex.
func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.Wrap(err, "read crypto-random bytes for uuid v4")
	}
	// Version 4: high nibble of byte 6 is 0100.
	b[6] = (b[6] & 0x0f) | 0x40
	// Variant: high bits of byte 8 are 10 (RFC 4122).
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
