package infra

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

// Sentinel errors for Server. Callers MUST detect via errors.Is rather than
// string matching.
var (
	// ErrInvalidServerConfig is returned by NewServer when the supplied
	// Config fails Validate, or when logger is nil. The former is defensive
	// — LoadConfig already validates — but NewServer does not assume the
	// caller went through LoadConfig (tests build literals).
	ErrInvalidServerConfig = errors.New("invalid server configuration")

	// ErrServerAlreadyRunning is returned by a second Start call on the same
	// Server instance. Start is NOT idempotent: a second call would
	// double-spawn monitor / registry / worker goroutines and double-close
	// the lifecycle contexts.
	ErrServerAlreadyRunning = errors.New("server already running")
)

// MemorySnapshot is the runtime view of the memory budget surfaced through
// ServerSnapshot (D-27). It carries the same triple MemoryMonitor.Snapshot
// returns, packaged as a value type so the health endpoint can serialize it
// without importing the monitor's internals.
type MemorySnapshot struct {
	// UsedBytes is the current Sys bytes reported by MemSource.
	UsedBytes uint64 `json:"used_bytes"`

	// LimitBytes is the configured MemoryConfig.LimitBytes (immutable after
	// construction).
	LimitBytes uint64 `json:"limit_bytes"`

	// Percent is UsedBytes / LimitBytes * 100. May exceed 100 if the
	// process overshoots the budget between GC cycles.
	Percent Percent `json:"percent"`
}

// ServerSnapshot is the aggregate point-in-time view of every infra
// component (D-17, D-27). Server.Snapshot produces one of these per call;
// the health endpoint renders it as JSON, the metrics endpoint projects
// the relevant fields onto Prometheus gauges/counters.
//
// Field consistency (D-61): each component's snapshot is taken atomically
// within that component, but the aggregate is NOT transactional across
// components — Server.Snapshot sequentially calls four Snapshot/Stats
// methods. For Phase 1's health/metrics use case this is acceptable: the
// endpoints are observability-only and slight cross-component skew is
// invisible to operators.
type ServerSnapshot struct {
	Memory       MemorySnapshot  `json:"memory"`
	Pool         PoolStats       `json:"pool"`
	Connections  ConnectionStats `json:"connections"`
	Workers      WorkerPoolStats `json:"workers"`
	LoadShedding bool            `json:"load_shedding"`
	Uptime       time.Duration   `json:"uptime"`
}

// Server is the coordinator that owns the lifecycle of every infra
// component (D-17): MemoryMonitor, AudioBufferPool, ConnectionRegistry,
// WorkerPool. It is the single seam through which main.go (Plan 08 Task 3)
// drives the service — main constructs a Server, calls Start, waits for a
// signal, then calls Shutdown.
//
// Lifecycle (D-21):
//
//	srv, err := NewServer(cfg, logger)  // construct, do not start
//	if err != nil { ... }
//	if err := srv.Start(ctx); err != nil { ... }   // spawn goroutines
//	<-ctx.Done()                                // wait for SIGTERM/SIGINT
//	srv.Shutdown(shutdownCtx)                   // drain
//
// Concurrency contract:
//   - NewServer is safe to call from any goroutine; the returned *Server is
//     safe to call Start on exactly once.
//   - Start is NOT idempotent; a second call returns ErrServerAlreadyRunning.
//   - Shutdown IS idempotent; subsequent calls return nil without re-draining.
//   - Snapshot, LoadShedding, and Components are safe for concurrent use
//     across any number of goroutines (they delegate to atomic / mutex
//     guarded component reads).
//
// Single-instance posture (D-20): exactly one Server per process; no
// service discovery, leader election, or peer coordination. The Server
// does not know whether it is the only instance — it does not need to.
type Server struct {
	cfg    Config
	logger Logger

	memMonitor *MemoryMonitor
	pool       *AudioBufferPool
	registry   *ConnectionRegistry
	workers    *WorkerPool

	// startedAt is set by Start; read by Snapshot to compute Uptime.
	startedAt time.Time

	// loadShedding is the latch the MemoryMonitor's LoadShedder callback
	// flips on rising/falling edges. Server.LoadShedding() reads it; the
	// health endpoint surfaces it as the ok/unhealthy signal (D-27).
	loadShedding atomic.Bool

	// memCancel / regCancel stop the MemoryMonitor polling goroutine and
	// the ConnectionRegistry cleanup goroutine respectively. WorkerPool
	// uses a different model (close-channel) so it does not need a cancel.
	memCancel context.CancelFunc
	regCancel context.CancelFunc

	// mu guards the running flag against double-Start / double-Shutdown
	// races. Held briefly; no I/O while held.
	mu sync.Mutex

	// running is true between Start and Shutdown. Guarded by mu.
	running bool

	// connStats is the optional Hub-backed connection-stats provider (D-04
	// option-a). When set, Snapshot reads live Hub counts instead of the
	// soft-deprecated registry. Zero-value nil falls back to registry.Stats()
	// so existing tests that do not call SetConnectionStatsProvider keep working.
	connStats func() ConnectionStats
}

// NewServer constructs a Server from cfg and logger. It validates cfg
// (defensive — LoadConfig already validates, but tests bypass LoadConfig)
// and builds all four components WITHOUT starting any goroutines. Start
// spawns the goroutines; this separation lets tests assert construction
// invariants independently of lifecycle.
//
// logger MUST be non-nil (D-47 — no global logger state). Passing nil
// returns ErrInvalidServerConfig. Components internally substitute
// NoOpLogger for nil, but at the Server level we require the caller to be
// explicit so a forgotten logger argument fails fast.
func NewServer(cfg Config, logger Logger) (*Server, error) {
	if logger == nil {
		return nil, errors.Wrap(ErrInvalidServerConfig, "logger must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(ErrInvalidServerConfig, err.Error())
	}

	// s is referenced inside the loadShedder closure below, so we allocate
	// it first and fill the fields afterward. The closure captures s by
	// pointer; once s.memMonitor is wired in, the loop is closed.
	s := &Server{
		cfg:    cfg,
		logger: logger,
	}

	// LoadShedder wires MemoryMonitor's threshold-crossing signal to
	// Server.loadShedding. active=true trips the latch (health endpoint
	// will report unhealthy); active=false clears it on recovery.
	// Per the LoadShedder contract, this closure must be non-blocking and
	// allocation-free — it runs on the monitor's polling goroutine.
	loadShedder := func(reason string, active bool) {
		s.loadShedding.Store(active)
		if active {
			logger.Warn("load shedding activated",
				F("component", "infra.server"),
				F("reason", reason),
			)
		} else {
			logger.Info("load shedding deactivated",
				F("component", "infra.server"),
				F("reason", reason),
			)
		}
	}

	memSource := NewRuntimeMemSource()
	memMonitor, err := NewMemoryMonitor(cfg.Memory, memSource, loadShedder, logger)
	if err != nil {
		return nil, errors.Wrap(ErrInvalidServerConfig, err.Error())
	}

	pool, err := NewAudioBufferPool(cfg.Buffer, logger)
	if err != nil {
		return nil, errors.Wrap(ErrInvalidServerConfig, err.Error())
	}

	// LifecycleHooks left zero-valued for Phase 1. WebSocket layer (Phase 2)
	// will supply OnConnect/OnDisconnect hooks via a setter when it becomes
	// the caller of ConnectionRegistry.
	registry, err := NewConnectionRegistry(cfg.Session, LifecycleHooks{}, logger)
	if err != nil {
		return nil, errors.Wrap(ErrInvalidServerConfig, err.Error())
	}

	workers, err := NewWorkerPool(cfg.Worker, logger)
	if err != nil {
		return nil, errors.Wrap(ErrInvalidServerConfig, err.Error())
	}

	s.memMonitor = memMonitor
	s.pool = pool
	s.registry = registry
	s.workers = workers
	return s, nil
}

// Start spawns the MemoryMonitor polling goroutine, the ConnectionRegistry
// cleanup goroutine, and the WorkerPool worker goroutines (D-17, D-21). It
// returns immediately — the goroutines run in the background and exit when
// ctx is cancelled or Shutdown is called.
//
// Start is NOT idempotent: a second call returns ErrServerAlreadyRunning
// without spawning additional goroutines. To restart, finish Shutting down
// and construct a new Server.
//
// The supplied ctx is the lifetime context for the monitor and registry
// goroutines. Cancelling it triggers clean exit within one tick interval
// (TestServerShutdownReleasesAllGoroutines proof). The WorkerPool uses a
// separate close-channel signal (see WorkerPool.Shutdown) but also selects
// on ctx so cancellation forces termination even mid-Task.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrServerAlreadyRunning
	}
	s.running = true
	s.startedAt = time.Now()
	s.mu.Unlock()

	// Derive child contexts with cancel so Shutdown can stop the monitor
	// and registry goroutines independently of the WorkerPool's
	// close-channel teardown. WorkerPool reads ctx directly (its worker
	// loop selects on ctx.Done()).
	memCtx, memCancel := context.WithCancel(ctx)
	s.memCancel = memCancel

	if err := s.memMonitor.Start(memCtx); err != nil {
		// Roll back: clear cancel and running flag so the caller can
		// retry after fixing whatever caused the failure.
		memCancel()
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		return errors.Wrap(err, "start memory monitor")
	}
	if err := s.workers.Start(ctx); err != nil {
		memCancel()
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		return errors.Wrap(err, "start worker pool")
	}

	s.logger.Info("server started",
		F("component", "infra.server"),
		F("addr", s.cfg.Server.Addr),
		F("health_addr", s.cfg.Server.HealthAddr),
	)
	return nil
}

// Shutdown gracefully drains every component (D-21). It is idempotent: a
// second call returns nil without re-draining. The order is deliberate:
//
//  1. Cancel mem and reg contexts — stops the polling / cleanup goroutines
//     within one tick interval.
//  2. Call workers.Shutdown(ctx) — closes the task channel and waits for
//     in-flight Tasks to finish. ctx bounds the wait; on expiry
//     ErrShutdownTimeout is logged but NOT returned, because main.go's
//     process-exit path treats best-effort drain as success.
//
// The supplied ctx should be bounded (e.g. cfg.Shutdown.Timeout) so a
// wedged Task cannot keep the process alive past the systemd grace period.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	s.mu.Unlock()

	// Stop monitor + registry goroutines. These are ticker-driven loops
	// with short intervals; cancel takes effect within one tick.
	if s.memCancel != nil {
		s.memCancel()
	}
	if s.regCancel != nil {
		s.regCancel()
	}

	// Drain workers. Best-effort: a timeout is logged but does not fail
	// Shutdown — the caller (main.go) is exiting anyway.
	if err := s.workers.Shutdown(ctx); err != nil {
		s.logger.Warn("worker pool shutdown timed out",
			F("component", "infra.server"),
			ErrField(err),
		)
	}

	s.logger.Info("server stopped",
		F("component", "infra.server"),
		F("uptime_ms", time.Since(s.startedAt).Milliseconds()),
	)
	return nil
}

// Snapshot returns the aggregate view of every component (D-27). Safe for
// concurrent use; called by the health endpoint on every GET /health and
// by the metrics collector on every Prometheus scrape.
func (s *Server) Snapshot() ServerSnapshot {
	used, limit, pct := s.memMonitor.Snapshot()
	return ServerSnapshot{
		Memory: MemorySnapshot{
			UsedBytes:  used,
			LimitBytes: limit,
			Percent:    pct,
		},
		Pool:         s.pool.Stats(),
		Connections:  s.connectionStats(),
		Workers:      s.workers.Stats(),
		LoadShedding: s.loadShedding.Load(),
		Uptime:       time.Since(s.startedAt),
	}
}

// connectionStats returns the Hub-backed connection counts when a provider
// has been registered via SetConnectionStatsProvider (D-04 option-a), else
// falls back to the soft-deprecated registry so existing tests keep working.
func (s *Server) connectionStats() ConnectionStats {
	if s.connStats != nil {
		return s.connStats()
	}
	return s.registry.Stats()
}

// SetConnectionStatsProvider wires a Hub-backed connection-stats provider
// (D-04 option-a). After this call, Snapshot reads live Hub connection counts
// instead of the dead registry's always-0 map. Call after Hub construction,
// before the first /metrics scrape (see main.go composition root).
func (s *Server) SetConnectionStatsProvider(fn func() ConnectionStats) {
	s.connStats = fn
}

// LoadShedding reports whether the MemoryMonitor's threshold has been
// breached and not yet recovered. Surfaced separately from Snapshot so
// health-check code can branch without allocating a full snapshot.
func (s *Server) LoadShedding() bool {
	return s.loadShedding.Load()
}

// Components exposes the four infra components by pointer. The health and
// metrics handlers (Task 2) use this to attach themselves to the live
// components without re-wiring construction. Callers MUST NOT call
// Start/Shutdown on the returned components — Server owns their lifecycles.
func (s *Server) Components() (mem *MemoryMonitor, p *AudioBufferPool, reg *ConnectionRegistry, w *WorkerPool) {
	return s.memMonitor, s.pool, s.registry, s.workers
}
