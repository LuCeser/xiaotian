package infra

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// MemSource abstracts the source of memory-usage data so the monitor can be
// tested without manipulating the Go runtime (D-42 constructor DI, D-47 no
// globals). Production code uses NewRuntimeMemSource; tests inject a fake.
//
// The single-method shape keeps the seam minimal: callers that need richer
// metrics (heap alloc, GC pause, num goroutines) will grow a separate
// interface in a later phase. For Phase 1's 150MB budget enforcement, Sys
// is the right single signal — it captures the process's total resident
// footprint including heap, stacks, and metadata (D-13).
type MemSource interface {
	// SysBytes returns the current memory usage in bytes. Implementations
	// MUST be safe to call concurrently with themselves (the monitor polls
	// on a goroutine while Snapshot may be called from the health endpoint).
	SysBytes() uint64
}

// runtimeMemSource is the production MemSource. It calls runtime.ReadMemStats
// (research Pattern 1) on every invocation and returns m.Sys. ReadMemStats
// is stop-the-world — callers should poll on the order of seconds, not
// milliseconds, to avoid starving the mutator. The default MemoryConfig
// interval (5s) is well within the safe range.
type runtimeMemSource struct{}

// SysBytes implements MemSource.
func (runtimeMemSource) SysBytes() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Sys
}

// NewRuntimeMemSource returns the production MemSource backed by
// runtime.ReadMemStats. Exposed so Plan 08 (server main) can construct a
// MemoryMonitor with the real runtime source without reaching into
// unexported types.
func NewRuntimeMemSource() MemSource {
	return runtimeMemSource{}
}

// LoadShedder is the callback the MemoryMonitor invokes when memory usage
// crosses the configured LoadShedAtPercent threshold (D-18). The reason
// argument is a stable machine-readable code (currently always
// "memory_budget_exceeded") that the callback can log, emit as a metric, or
// use as a circuit-breaker label.
//
// The active argument is true on a threshold breach (rising edge — circuit
// breaker trips OPEN) and false on recovery (falling edge — breaker closes
// again). The paired true/false events let the registered callback keep its
// own latch in lockstep with the monitor's internal shedActive flag without
// polling Snapshot() (Plan 08's Server uses this to drive its
// Server.LoadShedding() reflector for the health endpoint).
//
// LoadShedder acts as the trip signal for a circuit breaker (D-15): the
// registered callback (Plan 08's Server load-shed latch) trips OPEN when
// invoked with active=true, rejecting new work until the monitor invokes it
// again with active=false. The hysteresis inside MemoryMonitor (shedActive
// latch) ensures the breaker does not flap between open and closed on every
// poll — the callback fires at most once per transition.
//
// LoadShedder MUST be safe to call from the monitor's polling goroutine.
// The reference implementation in Plan 08 only flips an atomic.Bool — no
// blocking I/O, no allocations.
type LoadShedder func(reason string, active bool)

// ErrAlreadyRunning is returned by MemoryMonitor.Start when the monitor has
// already been started on the same instance. Start is NOT idempotent —
// callers MUST cancel the original context (which stops the goroutine
// within one tick interval) and construct a new monitor to restart.
// Calling Start a second time without cancelling would spawn a second
// polling goroutine and double every tick's work.
var ErrAlreadyRunning = errors.New("memory monitor already running")

// ErrInvalidMemoryConfig is the sentinel error returned (wrapped with
// context) by NewMemoryMonitor when any field of the supplied MemoryConfig
// fails validation. Callers detect validation failures via
// errors.Is(err, infra.ErrInvalidMemoryConfig).
var ErrInvalidMemoryConfig = errors.New("invalid memory configuration")

// MemoryMonitor polls a MemSource on a configurable interval, evaluates
// usage against alert thresholds, and triggers a LoadShedder callback when
// usage crosses LoadShedAtPercent (D-18). It is the runtime component of
// RES-01 (150MB budget enforcement) and the circuit breaker (D-15) that
// downstream callers (Plan 06 connection limiter, Plan 08 worker pool)
// consult before accepting new work.
//
// Thread safety (D-11): shedActive and running are guarded by mu. Snapshot
// reads source (which MUST be goroutine-safe) and limit (immutable after
// construction) without holding mu — safe because those reads do not
// depend on mutable monitor state.
//
// Lifecycle (D-12): Start(ctx) spawns exactly one goroutine that selects
// on ctx.Done() and ticker.C. Cancelling ctx triggers ticker.Stop() and
// goroutine return within one tick interval (TestMemoryMonitorStopsOnContextCancel
// proves there is no leak — Pitfall 1).
type MemoryMonitor struct {
	// source is the MemSource consulted on every tick. Immutable after
	// construction; MUST be goroutine-safe.
	source MemSource

	// limit is the memory budget in bytes (from MemoryConfig.LimitBytes).
	// Immutable after construction. Used as the denominator of the percent
	// calculation; guaranteed > 0 by NewMemoryMonitor validation.
	limit uint64

	// thresholds are the percent-of-budget alert thresholds (D-70), stored
	// as a defensive ascending-sorted copy. Immutable after construction.
	// Every tick emits one Warn log entry per threshold that pct crosses.
	thresholds []Percent

	// loadShedAt is the percent-of-budget at which LoadShedder fires.
	// Guaranteed >= max(thresholds) by NewMemoryMonitor validation so the
	// shed signal always arrives at or after the most severe alert.
	loadShedAt Percent

	// loadShedder is the callback invoked on threshold crossing. Required
	// non-nil by NewMemoryMonitor so callers cannot accidentally construct
	// a monitor that silently swallows the overload signal.
	loadShedder LoadShedder

	// interval is the polling cadence. Immutable after construction;
	// guaranteed > 0 by validation.
	interval time.Duration

	// logger receives Warn entries on threshold crossings and Info entries
	// on lifecycle events (started / stopped / shed cleared). Every entry
	// is tagged with F("component", "infra.memory") per D-25.
	logger Logger

	// mu guards the two mutable flags below. Held briefly; no I/O while
	// holding it. The LoadShedder callback is invoked AFTER releasing mu
	// so a slow callback cannot block the polling goroutine's next tick.
	mu sync.Mutex

	// shedActive is the hysteresis latch (T-01-17). Once LoadShedder has
	// fired, shedActive stays true until usage drops below loadShedAt,
	// preventing flap on every poll.
	shedActive bool

	// running guards double-Start. Set true by Start; never reset to false
	// (construct a new monitor to restart).
	running bool
}

// NewMemoryMonitor constructs a MemoryMonitor from cfg, source, loadShedder,
// and logger. It performs full validation of cfg (mirroring Config.Validate
// but applied to a standalone MemoryConfig — this guard lets tests construct
// monitors with hand-built configs without running LoadConfig).
//
// Validation rules (each failure wraps ErrInvalidMemoryConfig):
//   - source non-nil
//   - loadShedder non-nil (require explicit callback for clarity)
//   - LimitBytes > 0
//   - MonitorInterval > 0
//   - Thresholds non-empty, strictly ascending, every value in (0, 100]
//   - LoadShedAtPercent in (0, 100] and >= max(Thresholds)
//   - logger: nil is accepted and substituted with NewNoOpLogger()
//
// On any failure returns (nil, error). The returned error chain always
// satisfies errors.Is(err, ErrInvalidMemoryConfig).
func NewMemoryMonitor(
	cfg MemoryConfig,
	source MemSource,
	loadShedder LoadShedder,
	logger Logger,
) (*MemoryMonitor, error) {
	if source == nil {
		return nil, errors.Wrap(ErrInvalidMemoryConfig, "source must not be nil")
	}
	if loadShedder == nil {
		return nil, errors.Wrap(ErrInvalidMemoryConfig, "loadShedder must not be nil")
	}
	if cfg.LimitBytes == 0 {
		return nil, errors.Wrap(ErrInvalidMemoryConfig, "limitBytes must be > 0")
	}
	if cfg.MonitorInterval <= 0 {
		return nil, errors.Wrap(ErrInvalidMemoryConfig, "monitorInterval must be > 0")
	}
	if err := validateMonitorThresholds(cfg.Thresholds); err != nil {
		return nil, err
	}
	if cfg.LoadShedAtPercent <= 0 || cfg.LoadShedAtPercent > 100 {
		return nil, errors.Wrap(ErrInvalidMemoryConfig,
			"loadShedAtPercent must be in (0, 100]")
	}

	// Defensive copy + sort (T-01-16 defense-in-depth). The validation
	// above already enforced strict ascending on the caller's slice, so
	// sort.Slice is a no-op on valid input. The copy + sort runs anyway so
	// a future caller that bypasses validation (or a test that injects an
	// unsorted slice after the check is relaxed) cannot corrupt the
	// monitor's internal state.
	thresholds := append([]Percent(nil), cfg.Thresholds...)
	sort.Slice(thresholds, func(i, j int) bool { return thresholds[i] < thresholds[j] })

	maxThreshold := thresholds[len(thresholds)-1]
	if cfg.LoadShedAtPercent < maxThreshold {
		return nil, errors.Wrap(ErrInvalidMemoryConfig,
			fmt.Sprintf("loadShedAtPercent=%d must be >= max(thresholds)=%d",
				cfg.LoadShedAtPercent, maxThreshold))
	}

	if logger == nil {
		logger = NewNoOpLogger()
	}

	return &MemoryMonitor{
		source:      source,
		limit:       uint64(cfg.LimitBytes),
		thresholds:  thresholds,
		loadShedAt:  cfg.LoadShedAtPercent,
		loadShedder: loadShedder,
		interval:    cfg.MonitorInterval,
		logger:      logger,
	}, nil
}

// validateMonitorThresholds is the input-validation gate for the thresholds
// slice. It enforces: non-empty, strictly ascending, every value in (0, 100].
// Mirrors Config.validateThresholds but wraps ErrInvalidMemoryConfig so
// callers can distinguish a monitor-construction failure from a config-load
// failure.
func validateMonitorThresholds(ts []Percent) error {
	if len(ts) == 0 {
		return errors.Wrap(ErrInvalidMemoryConfig, "thresholds must not be empty")
	}
	prev := Percent(0)
	for i, p := range ts {
		if p <= 0 || p > 100 {
			return errors.Wrap(ErrInvalidMemoryConfig,
				fmt.Sprintf("thresholds[%d]=%d must be in (0, 100]", i, p))
		}
		if p <= prev {
			return errors.Wrap(ErrInvalidMemoryConfig,
				fmt.Sprintf("thresholds must be strictly ascending; %d followed by %d", prev, p))
		}
		prev = p
	}
	return nil
}

// Start spawns the polling goroutine. The goroutine samples the source every
// interval and calls tick() to evaluate thresholds and update the load-shed
// latch.
//
// Start is NOT idempotent: a second call on the same instance returns
// ErrAlreadyRunning without spawning a second goroutine. To restart, cancel
// the original context (goroutine exits within one tick interval) and
// construct a new monitor.
//
// The goroutine exits cleanly when ctx is cancelled (D-12 resource cleanup).
func (m *MemoryMonitor) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}
	m.running = true
	m.mu.Unlock()

	m.logger.Info("memory monitor started",
		F("component", "infra.memory"),
		F("limit_bytes", m.limit),
		F("interval_ms", m.interval.Milliseconds()),
		F("loadShedAt", int(m.loadShedAt)),
	)

	go m.loop(ctx)
	return nil
}

// loop is the polling goroutine body. Lives in its own function so the
// deferred ticker.Stop() cleanup is scoped to the goroutine.
func (m *MemoryMonitor) loop(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.logger.Info("memory monitor stopped", F("component", "infra.memory"))
			return
		case <-ticker.C:
			m.tick()
		}
	}
}

// tick samples the source once, evaluates thresholds, and updates the
// load-shed latch. Called from the polling goroutine; tests also call it
// directly to drive the monitor synchronously without waiting for the
// ticker (TestMemoryMonitorThresholdAlerts, TestMemoryMonitorLoadShed).
//
// tick is safe to call concurrently with Snapshot, but NOT with itself —
// only the polling goroutine should call it. If two ticks overlapped, the
// shedActive latch could race and the LoadShedder could double-fire.
func (m *MemoryMonitor) tick() {
	used := m.source.SysBytes()
	// limit > 0 is enforced at construction; no divide-by-zero possible.
	// Cast through float64 so 16MB/150MB rounds to 10 (not 0 as int div would).
	pct := Percent(float64(used) / float64(m.limit) * 100)

	// Threshold alerts: emit one Warn per crossed threshold. Because the
	// thresholds slice is sorted ascending, the loop walks every threshold
	// <= pct — typically 0, 1, 2, or 3 entries depending on load.
	for _, threshold := range m.thresholds {
		if pct >= threshold {
			m.logger.Warn("memory threshold exceeded",
				F("threshold", int(threshold)),
				F("used_bytes", used),
				F("limit_bytes", m.limit),
				F("percent", int(pct)),
				F("component", "infra.memory"),
			)
		}
	}

	// Load-shed evaluation. The latch update and the callback invocation
	// are deliberately separated by the unlock/release boundary so a slow
	// callback cannot extend the critical section.
	if pct >= m.loadShedAt {
		m.mu.Lock()
		wasActive := m.shedActive
		if !wasActive {
			m.shedActive = true
		}
		m.mu.Unlock()
		if !wasActive {
			// Circuit breaker trips OPEN. Plan 08's callback will reject
			// new work until shedActive clears in the recovery branch
			// below.
			m.loadShedder("memory_budget_exceeded", true)
		}
		return
	}

	// Recovery branch: pct dropped below loadShedAt. If we were previously
	// shedding, clear the latch so a future breach can re-fire (closed →
	// open transition). Fire the callback with active=false so downstream
	// latches (Plan 08's Server.LoadShedding()) clear in lockstep.
	m.mu.Lock()
	wasActive := m.shedActive
	if wasActive {
		m.shedActive = false
	}
	m.mu.Unlock()
	if wasActive {
		m.logger.Info("load shed cleared",
			F("component", "infra.memory"),
			F("percent", int(pct)),
		)
		m.loadShedder("memory_budget_exceeded", false)
	}
}

// Snapshot returns the current memory state for the health/metrics endpoint
// (Plan 08). Calls source.SysBytes() exactly once; safe to call concurrently
// with the polling goroutine as long as source is goroutine-safe (every
// MemSource implementation in this package is).
//
// Returns:
//   - used: bytes currently reported by MemSource
//   - limit: the configured LimitBytes (immutable)
//   - percent: used / limit * 100 (Percent in [0, 100+] — can exceed 100 if
//     the process overshoots the budget between GC cycles)
func (m *MemoryMonitor) Snapshot() (used, limit uint64, percent Percent) {
	used = m.source.SysBytes()
	limit = m.limit
	percent = Percent(float64(used) / float64(limit) * 100)
	return
}
