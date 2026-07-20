package infra

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

// fakeMemSource is the test-only MemSource (D-48 manual fake). It is
// goroutine-safe so tests can mutate the reported bytes while the monitor's
// polling goroutine reads them — without this mutex, TestMemoryMonitorLoadShed
// and TestMemoryMonitorStopsOnContextCancel would race under -race.
type fakeMemSource struct {
	mu    sync.Mutex
	bytes uint64
}

// SysBytes implements MemSource.
func (f *fakeMemSource) SysBytes() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bytes
}

// set updates the bytes the source will report on the next SysBytes call.
func (f *fakeMemSource) set(b uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bytes = b
}

// shedCounter is a thread-safe counter for LoadShedder invocations. Each call
// records its reason so tests can assert on both count and the reason code.
//
// count tracks only active=true (rising-edge) events — the canonical
// "load-shed activations" metric. clears tracks active=false (falling-edge)
// events so tests can additionally assert on the close transition.
type shedCounter struct {
	mu      sync.Mutex
	count   int
	clears  int
	reasons []string
}

// fire is the LoadShedder callback closure. It increments count (active=true)
// or clears (active=false) and appends the reason on rising edges; safe to
// call from the monitor goroutine and read from the test goroutine
// simultaneously.
func (s *shedCounter) fire(reason string, active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active {
		s.count++
		s.reasons = append(s.reasons, reason)
	} else {
		s.clears++
	}
}

// get returns the current shed count (rising-edge events only) under the
// lock.
func (s *shedCounter) get() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// getClears returns the current clear count (falling-edge events) under the
// lock. Used by tests that assert on the closed → open transition.
func (s *shedCounter) getClears() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clears
}

// newMonitorBuilder is a tiny DSL for assembling a MemoryMonitor with the
// phase-1 defaults overridden only where a test cares. It keeps every test
// honest about limit / interval / thresholds without repeating 10 lines of
// struct literal at every call site.
type monitorBuilder struct {
	limit      uint64
	thresholds []Percent
	loadShedAt Percent
	interval   time.Duration
}

func newMonitorBuilder() monitorBuilder {
	return monitorBuilder{
		limit:      150 * 1024 * 1024,
		thresholds: []Percent{80, 90, 95},
		loadShedAt: 95,
		interval:   5 * time.Millisecond,
	}
}

func (b monitorBuilder) withLimit(bytes uint64) monitorBuilder {
	b.limit = bytes
	return b
}

func (b monitorBuilder) build(
	t *testing.T,
	src MemSource,
	loadShedder LoadShedder,
	logger Logger,
) *MemoryMonitor {
	t.Helper()
	cfg := MemoryConfig{
		LimitBytes:        MemoryBytes(b.limit),
		MonitorInterval:   b.interval,
		Thresholds:        append([]Percent(nil), b.thresholds...),
		LoadShedAtPercent: b.loadShedAt,
	}
	m, err := NewMemoryMonitor(cfg, src, loadShedder, logger)
	if err != nil {
		t.Fatalf("NewMemoryMonitor: %v", err)
	}
	return m
}

// TestServiceMemoryBudget proves RES-01 (the 150MB memory budget): when
// MemSource reports Sys == LimitBytes, Snapshot().percent == 100. This is
// the literal VALIDATION.md test name for the budget contract —
// `go test -run TestServiceMemoryBudget` must pass for the phase to ship.
func TestServiceMemoryBudget(t *testing.T) {
	const limit = 150 * 1024 * 1024
	src := &fakeMemSource{bytes: limit}
	m := newMonitorBuilder().build(t, src, func(string, bool) {}, newFakeLogger())

	used, lim, pct := m.Snapshot()
	if used != limit {
		t.Errorf("Snapshot used: got %d, want %d", used, limit)
	}
	if lim != limit {
		t.Errorf("Snapshot limit: got %d, want %d", lim, limit)
	}
	if pct != Percent(100) {
		t.Errorf("Snapshot percent: got %d, want 100 (budget exhausted)", pct)
	}
}

// TestServiceMemoryBudgetSub16 proves the percent math is well-formed at
// low usage: 16MB over 150MB == 10% (well under any threshold).
func TestServiceMemoryBudgetSub16(t *testing.T) {
	const limit = 150 * 1024 * 1024
	const used = 16 * 1024 * 1024
	src := &fakeMemSource{bytes: used}
	m := newMonitorBuilder().build(t, src, func(string, bool) {}, newFakeLogger())

	_, _, pct := m.Snapshot()
	if pct != Percent(10) {
		t.Errorf("percent: got %d, want 10 (16MB / 150MB)", pct)
	}
}

// TestMemoryMonitorThresholdAlerts proves D-70: at 80% / 90% / 95% usage the
// monitor emits Warn log entries with the matching threshold field. Drives
// tick() synchronously so we do not depend on ticker timing.
func TestMemoryMonitorThresholdAlerts(t *testing.T) {
	const limit = 100 * 1024 * 1024 // 100MB → easy mental math
	src := &fakeMemSource{}
	logger := newFakeLogger()
	m := newMonitorBuilder().
		withLimit(limit).
		build(t, src, func(string, bool) {}, logger)

	// Drive tick() through 10 → 80 → 90 → 95 percent. At each step above a
	// threshold, the monitor emits a Warn whose threshold field equals every
	// crossed value (so 90% emits BOTH threshold=80 and threshold=90).
	for _, frac := range []float64{0.10, 0.80, 0.90, 0.95} {
		src.set(uint64(frac * float64(limit)))
		m.tick()
	}

	saw80, saw90, saw95 := false, false, false
	for _, msg := range logger.state.msgs {
		if msg.Level != "warn" || msg.Msg != "memory threshold exceeded" {
			continue
		}
		v, ok := findField(msg.Fields, "threshold")
		if !ok {
			continue
		}
		switch v {
		case 80:
			saw80 = true
		case 90:
			saw90 = true
		case 95:
			saw95 = true
		}
	}
	if !saw80 {
		t.Errorf("expected Warn with threshold=80; msgs=%+v", logger.state.msgs)
	}
	if !saw90 {
		t.Errorf("expected Warn with threshold=90; msgs=%+v", logger.state.msgs)
	}
	if !saw95 {
		t.Errorf("expected Warn with threshold=95; msgs=%+v", logger.state.msgs)
	}
}

// TestMemoryMonitorLoadShed proves D-18 + hysteresis (T-01-17): when usage
// crosses LoadShedAtPercent, the LoadShedder fires EXACTLY ONCE — subsequent
// ticks while still over the threshold do not re-fire. This is the
// circuit-breaker open/half-open/closed semantics from D-15.
func TestMemoryMonitorLoadShed(t *testing.T) {
	const limit = 100 * 1024 * 1024
	src := &fakeMemSource{}
	counter := &shedCounter{}
	m := newMonitorBuilder().
		withLimit(limit).
		build(t, src, counter.fire, newFakeLogger())

	// 10% — no shed.
	src.set(uint64(0.10 * float64(limit)))
	m.tick()
	if got := counter.get(); got != 0 {
		t.Fatalf("after 10%% tick: shedCount=%d, want 0", got)
	}

	// 96% — shed fires once.
	src.set(uint64(0.96 * float64(limit)))
	m.tick()
	if got := counter.get(); got != 1 {
		t.Fatalf("after first 96%% tick: shedCount=%d, want 1", got)
	}

	// Two more ticks at 96% — already active, no additional shed.
	src.set(uint64(0.96 * float64(limit)))
	m.tick()
	src.set(uint64(0.96 * float64(limit)))
	m.tick()

	if got := counter.get(); got != 1 {
		t.Errorf("after three 96%% ticks: shedCount=%d, want 1 (hysteresis)", got)
	}
	if len(counter.reasons) != 1 || counter.reasons[0] != "memory_budget_exceeded" {
		t.Errorf("reasons: got %+v, want [memory_budget_exceeded]", counter.reasons)
	}
}

// TestMemoryMonitorLoadShedClears proves the hysteresis reset path: when
// usage drops below LoadShedAtPercent, shedActive clears; if usage then
// rises above the threshold again, LoadShedder fires a second time. This
// is the closed → open transition of the circuit breaker.
func TestMemoryMonitorLoadShedClears(t *testing.T) {
	const limit = 100 * 1024 * 1024
	src := &fakeMemSource{}
	counter := &shedCounter{}
	m := newMonitorBuilder().
		withLimit(limit).
		build(t, src, counter.fire, newFakeLogger())

	// 96% — shed (count=1).
	src.set(uint64(0.96 * float64(limit)))
	m.tick()
	// 50% — below threshold, latch clears (no shed fire).
	src.set(uint64(0.50 * float64(limit)))
	m.tick()
	// 96% — shed fires again (count=2).
	src.set(uint64(0.96 * float64(limit)))
	m.tick()

	if got, want := counter.get(), 2; got != want {
		t.Errorf("after clear-and-refire: shedCount=%d, want %d", got, want)
	}
}

// TestMemoryMonitorNoLoadShedBelowThreshold proves the negative case: usage
// just below LoadShedAtPercent never triggers the LoadShedder, even across
// many ticks. This guards against off-by-one errors in the >= comparison.
func TestMemoryMonitorNoLoadShedBelowThreshold(t *testing.T) {
	const limit = 100 * 1024 * 1024
	src := &fakeMemSource{}
	counter := &shedCounter{}
	m := newMonitorBuilder().
		withLimit(limit).
		build(t, src, counter.fire, newFakeLogger())

	// 94% — below LoadShedAtPercent (95). No shed.
	src.set(uint64(0.94 * float64(limit)))
	for i := 0; i < 5; i++ {
		m.tick()
	}
	if got := counter.get(); got != 0 {
		t.Errorf("below-threshold ticks shed: got %d, want 0", got)
	}
}

// TestMemoryMonitorStopsOnContextCancel proves D-12 (context-based resource
// cleanup) and Pitfall 1 (no goroutine leak). After ctx.Cancel, the polling
// goroutine exits within one tick interval; runtime.NumGoroutine returns to
// baseline (plus a small slack for test framework goroutines).
func TestMemoryMonitorStopsOnContextCancel(t *testing.T) {
	// Allow the runtime to settle so the baseline is stable.
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &fakeMemSource{}
	m := newMonitorBuilder().
		withLimit(150*1024*1024).
		build(t, src, func(string, bool) {}, newFakeLogger())

	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let the polling goroutine run a few ticks so we know it is alive.
	time.Sleep(50 * time.Millisecond)

	cancel()

	// Poll for the goroutine to exit. 200ms is generous for a 5ms tick.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+1 {
			return // pass
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("goroutine leak: baseline=%d, now=%d (want <=%d within 200ms)",
		baseline, runtime.NumGoroutine(), baseline+1)
}

// TestMemoryMonitorStartIdempotent proves Start is not idempotent: a second
// call returns ErrAlreadyRunning. The contract is "construct a new monitor
// to restart" — double-Start would otherwise spawn a second polling
// goroutine and double every tick's work.
func TestMemoryMonitorStartIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &fakeMemSource{}
	m := newMonitorBuilder().build(t, src, func(string, bool) {}, newFakeLogger())

	if err := m.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	err := m.Start(ctx)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second Start: got %v, want ErrAlreadyRunning (errors.Is)", err)
	}
}

// TestNewMemoryMonitorValidation covers every constructor error path. Each
// subtest feeds one invalid input and asserts the wrapped sentinel.
func TestNewMemoryMonitorValidation(t *testing.T) {
	validCfg := MemoryConfig{
		LimitBytes:        MemoryBytes(150 * 1024 * 1024),
		MonitorInterval:   5 * time.Millisecond,
		Thresholds:        []Percent{80, 90, 95},
		LoadShedAtPercent: 95,
	}
	src := &fakeMemSource{}
	shedder := func(string, bool) {}

	t.Run("nil_source", func(t *testing.T) {
		_, err := NewMemoryMonitor(validCfg, nil, shedder, newFakeLogger())
		if !errors.Is(err, ErrInvalidMemoryConfig) {
			t.Errorf("nil source: got %v, want ErrInvalidMemoryConfig", err)
		}
	})
	t.Run("nil_loadShedder", func(t *testing.T) {
		_, err := NewMemoryMonitor(validCfg, src, nil, newFakeLogger())
		if !errors.Is(err, ErrInvalidMemoryConfig) {
			t.Errorf("nil loadShedder: got %v, want ErrInvalidMemoryConfig", err)
		}
	})
	t.Run("zero_limit", func(t *testing.T) {
		cfg := validCfg
		cfg.LimitBytes = 0
		_, err := NewMemoryMonitor(cfg, src, shedder, newFakeLogger())
		if !errors.Is(err, ErrInvalidMemoryConfig) {
			t.Errorf("zero limit: got %v, want ErrInvalidMemoryConfig", err)
		}
	})
	t.Run("zero_interval", func(t *testing.T) {
		cfg := validCfg
		cfg.MonitorInterval = 0
		_, err := NewMemoryMonitor(cfg, src, shedder, newFakeLogger())
		if !errors.Is(err, ErrInvalidMemoryConfig) {
			t.Errorf("zero interval: got %v, want ErrInvalidMemoryConfig", err)
		}
	})
	t.Run("empty_thresholds", func(t *testing.T) {
		cfg := validCfg
		cfg.Thresholds = nil
		_, err := NewMemoryMonitor(cfg, src, shedder, newFakeLogger())
		if !errors.Is(err, ErrInvalidMemoryConfig) {
			t.Errorf("empty thresholds: got %v, want ErrInvalidMemoryConfig", err)
		}
	})
	t.Run("threshold_out_of_range", func(t *testing.T) {
		cfg := validCfg
		cfg.Thresholds = []Percent{80, 90, 150}
		_, err := NewMemoryMonitor(cfg, src, shedder, newFakeLogger())
		if !errors.Is(err, ErrInvalidMemoryConfig) {
			t.Errorf("threshold=150: got %v, want ErrInvalidMemoryConfig", err)
		}
	})
	t.Run("thresholds_unsorted", func(t *testing.T) {
		cfg := validCfg
		cfg.Thresholds = []Percent{90, 80, 95}
		_, err := NewMemoryMonitor(cfg, src, shedder, newFakeLogger())
		if !errors.Is(err, ErrInvalidMemoryConfig) {
			t.Errorf("unsorted thresholds: got %v, want ErrInvalidMemoryConfig", err)
		}
	})
	t.Run("loadShed_below_max_threshold", func(t *testing.T) {
		cfg := validCfg
		cfg.LoadShedAtPercent = 50
		_, err := NewMemoryMonitor(cfg, src, shedder, newFakeLogger())
		if !errors.Is(err, ErrInvalidMemoryConfig) {
			t.Errorf("loadShed < maxThreshold: got %v, want ErrInvalidMemoryConfig", err)
		}
	})
	t.Run("nil_logger_accepted", func(t *testing.T) {
		// nil logger is NOT an error — substituted with NoOpLogger.
		m, err := NewMemoryMonitor(validCfg, src, shedder, nil)
		if err != nil {
			t.Errorf("nil logger: got %v, want nil (NoOpLogger substituted)", err)
		}
		if m == nil {
			t.Error("nil logger: returned monitor is nil")
		}
	})
	t.Run("happy_path", func(t *testing.T) {
		m, err := NewMemoryMonitor(validCfg, src, shedder, newFakeLogger())
		if err != nil {
			t.Errorf("happy path: got %v, want nil", err)
		}
		if m == nil {
			t.Error("happy path: returned monitor is nil")
		}
	})
}

// TestNewRuntimeMemSource proves the production source returns a positive,
// non-zero byte count — the runtime always has SOME resident memory. This
// is a smoke test; it cannot assert an exact value because Sys depends on
// the test binary's heap.
func TestNewRuntimeMemSource(t *testing.T) {
	src := NewRuntimeMemSource()
	got := src.SysBytes()
	if got == 0 {
		t.Error("runtimeMemSource.SysBytes() = 0; want > 0 (runtime always resident)")
	}
}
