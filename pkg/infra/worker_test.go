package infra

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests cover the WorkerPool contract declared in
// 01-07-PLAN.md:
//   - D-10 fixed pool size (workers spawned == cfg.PoolSize)
//   - D-12 / Pitfall 1 workers honour ctx cancellation (no leak)
//   - D-16 component redundancy (peers keep draining after one task panics)
//   - D-21 graceful drain on Shutdown
//   - D-22 Shutdown ctx bounds the drain wait
//   - D-45 tasks receive the pool's Start ctx
//   - D-49 per-task panic recovery inside the worker loop
//   - D-72 / D-74 Submit returns ErrWorkerPoolFull instead of blocking
//
// They reuse the manual fakeLogger / findField helpers defined in
// middleware_test.go (same package). Run with `go test -race`.

// waitOrTimeout waits for wg up to timeout, failing the test (via
// t.Fatalf) if it does not complete. The what string is included in the
// failure message. Used by tests that need to await task completion
// without risking an indefinite hang.
func waitOrTimeout(t *testing.T, wg *sync.WaitGroup, timeout time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("%s within %v", what, timeout)
	}
}

// TestWorkerPoolProcessesTasks proves D-10 dispatch: a pool with size=2
// runs submitted tasks and Stats().TotalProcessed reflects the count
// after they complete.
func TestWorkerPoolProcessesTasks(t *testing.T) {
	cfg := WorkerConfig{PoolSize: 2, QueueSize: 10}
	pool, err := NewWorkerPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewWorkerPool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var counter atomic.Int64
	var wg sync.WaitGroup
	wg.Add(5)
	for i := 0; i < 5; i++ {
		if err := pool.Submit(func(ctx context.Context) error {
			counter.Add(1)
			wg.Done()
			return nil
		}); err != nil {
			t.Errorf("Submit[%d]: %v", i, err)
			wg.Done()
		}
	}

	waitOrTimeout(t, &wg, 1*time.Second, "tasks did not complete")

	if got, want := counter.Load(), int64(5); got != want {
		t.Errorf("counter: got %d, want %d", got, want)
	}
	if got, want := pool.Stats().TotalProcessed, uint64(5); got != want {
		t.Errorf("TotalProcessed: got %d, want %d", got, want)
	}

	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer sCancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestWorkerPoolBackpressureSignal proves D-72 / D-74: Submit returns
// ErrWorkerPoolFull immediately when the bounded queue is full, instead
// of blocking. The Plan 08 server translates this typed error into an
// HTTP 503 / explicit backpressure frame.
//
// Determinism note (deviation from plan text): the plan's "queue=2,
// submit 3, 3rd fails" description is off-by-one given worker pull
// semantics — a single worker would pick up task1 immediately, leaving
// room for task2 and task3 in the buffer. To make the test deterministic
// we use QueueSize=1 (so capacity = 1 in-flight + 1 queued = 2) and
// pre-block the worker with a sentinel before filling the buffer. The
// contract being proven is identical: a full queue yields an immediate
// typed backpressure error.
func TestWorkerPoolBackpressureSignal(t *testing.T) {
	cfg := WorkerConfig{PoolSize: 1, QueueSize: 1}
	pool, err := NewWorkerPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewWorkerPool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = pool.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	blockCh := make(chan struct{})
	workerStarted := make(chan struct{})

	// Sentinel: blocks the single worker. Close workerStarted once
	// running so the test knows the worker is now in-flight on this
	// task and the buffer is empty.
	if err = pool.Submit(func(ctx context.Context) error {
		close(workerStarted)
		<-blockCh
		return nil
	}); err != nil {
		t.Fatalf("sentinel Submit: %v", err)
	}
	<-workerStarted

	// Buffer is empty (worker pulled the sentinel). Fill it (cap=1).
	if err = pool.Submit(func(ctx context.Context) error {
		<-blockCh
		return nil
	}); err != nil {
		t.Fatalf("buffer-fill Submit: %v", err)
	}

	// Queue is now full. Submit must return ErrWorkerPoolFull immediately.
	start := time.Now()
	err = pool.Submit(func(ctx context.Context) error {
		<-blockCh
		return nil
	})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrWorkerPoolFull) {
		t.Errorf("overflow Submit: got %v, want ErrWorkerPoolFull", err)
	}
	if elapsed > 10*time.Millisecond {
		t.Errorf("overflow Submit blocked: took %v, want < 10ms", elapsed)
	}
	if got := pool.Stats().TotalBackpressure; got != 1 {
		t.Errorf("TotalBackpressure: got %d, want 1", got)
	}

	// Release the worker and drain cleanly.
	close(blockCh)
	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer sCancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestWorkerPoolStopsOnContextCancel proves Pitfall 1 mitigation: when
// the Start ctx is cancelled every worker goroutine exits within a short
// grace period. runtime.NumGoroutine returns to baseline — no leak.
func TestWorkerPoolStopsOnContextCancel(t *testing.T) {
	// Force GC so the baseline is stable; otherwise the previous test's
	// deferred-clean-up goroutines can lift the count momentarily.
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	cfg := WorkerConfig{PoolSize: 10, QueueSize: 10}
	pool, err := NewWorkerPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewWorkerPool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := pool.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the runtime a moment to schedule the 10 workers.
	time.Sleep(20 * time.Millisecond)

	cancel()

	// Poll up to 200ms (well beyond the 100ms acceptance bar in the plan
	// — generous on a contended CI runner).
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > baseline {
		t.Errorf("NumGoroutine after cancel: got %d, baseline %d (workers leaked)", got, baseline)
	}
}

// TestWorkerPoolRecoversTaskPanic proves D-49 + D-16: a panicking task
// is recovered, the worker keeps running, subsequent tasks still execute
// (component redundancy — peers and the surviving worker keep draining).
// The recovered-panic event is emitted at Error level with
// recovered_panic=true and component=infra.worker.
func TestWorkerPoolRecoversTaskPanic(t *testing.T) {
	logger := newFakeLogger()
	cfg := WorkerConfig{PoolSize: 1, QueueSize: 10}
	pool, err := NewWorkerPool(cfg, logger)
	if err != nil {
		t.Fatalf("NewWorkerPool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := pool.Submit(func(ctx context.Context) error {
		panic("boom")
	}); err != nil {
		t.Fatalf("panic-task Submit: %v", err)
	}

	// The worker survives (per-task recover). This normal task must run.
	var counter atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	if err := pool.Submit(func(ctx context.Context) error {
		counter.Add(1)
		wg.Done()
		return nil
	}); err != nil {
		t.Fatalf("post-panic Submit: %v", err)
	}
	waitOrTimeout(t, &wg, 1*time.Second, "post-panic task did not run")

	if got := counter.Load(); got != 1 {
		t.Errorf("post-panic counter: got %d, want 1 (worker died?)", got)
	}

	// Locate the recovered-panic entry. It is emitted at Error level.
	var recovered *loggedMsg
	for i := range logger.state.msgs {
		m := &logger.state.msgs[i]
		if m.Level == "error" && m.Msg == "worker recovered task panic" {
			recovered = m
			break
		}
	}
	if recovered == nil {
		t.Fatalf("no 'worker recovered task panic' entry; msgs=%+v", logger.state.msgs)
	}
	if rp, ok := findField(recovered.Fields, "recovered_panic"); !ok || rp != true {
		t.Errorf("recovered_panic=true missing or wrong: %+v", recovered.Fields)
	}
	if c, ok := findField(recovered.Fields, "component"); !ok || c != "infra.worker" {
		t.Errorf("component=infra.worker missing or wrong: %+v", recovered.Fields)
	}
	if e, ok := findField(recovered.Fields, "error"); !ok || e != "boom" {
		t.Errorf("error='boom' missing or wrong: %+v", recovered.Fields)
	}
	if s, ok := findField(recovered.Fields, "stack"); !ok {
		t.Errorf("stack field missing: %+v", recovered.Fields)
	} else if sStr, isStr := s.(string); !isStr || sStr == "" {
		t.Errorf("stack should be a non-empty string, got %T (%v)", s, s)
	}
	if got := pool.Stats().TotalPanics; got != 1 {
		t.Errorf("TotalPanics: got %d, want 1", got)
	}

	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer sCancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestWorkerPoolGracefulDrain proves D-21: Shutdown closes the task
// channel and waits for in-flight tasks to finish (within the shutdown
// ctx budget). All queued tasks complete before Shutdown returns nil.
func TestWorkerPoolGracefulDrain(t *testing.T) {
	cfg := WorkerConfig{PoolSize: 3, QueueSize: 10}
	pool, err := NewWorkerPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewWorkerPool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = pool.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var counter atomic.Int64
	for i := 0; i < 3; i++ {
		if err = pool.Submit(func(ctx context.Context) error {
			time.Sleep(50 * time.Millisecond)
			counter.Add(1)
			return nil
		}); err != nil {
			t.Errorf("Submit[%d]: %v", i, err)
		}
	}

	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 1*time.Second)
	err = pool.Shutdown(shutdownCtx)
	sCancel()
	if err != nil {
		t.Errorf("Shutdown: got %v, want nil (clean drain)", err)
	}
	if got := counter.Load(); got != 3 {
		t.Errorf("counter after drain: got %d, want 3", got)
	}
}

// TestWorkerPoolShutdownTimeout proves D-22: Shutdown returns
// ErrShutdownTimeout when its ctx expires before in-flight tasks
// complete. The task honours ctx (via select) so the test goroutine
// itself does not leak past the function return.
func TestWorkerPoolShutdownTimeout(t *testing.T) {
	cfg := WorkerConfig{PoolSize: 1, QueueSize: 10}
	pool, err := NewWorkerPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewWorkerPool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = pool.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err = pool.Submit(func(taskCtx context.Context) error {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-taskCtx.Done():
		}
		return nil
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer sCancel()
	err = pool.Shutdown(shutdownCtx)
	if !errors.Is(err, ErrShutdownTimeout) {
		t.Errorf("Shutdown: got %v, want ErrShutdownTimeout", err)
	}
}

// TestWorkerPoolStartIdempotent proves a second Start returns
// ErrPoolAlreadyRunning without spawning additional workers.
func TestWorkerPoolStartIdempotent(t *testing.T) {
	cfg := WorkerConfig{PoolSize: 1, QueueSize: 1}
	pool, err := NewWorkerPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewWorkerPool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := pool.Start(ctx); !errors.Is(err, ErrPoolAlreadyRunning) {
		t.Errorf("second Start: got %v, want ErrPoolAlreadyRunning", err)
	}

	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer sCancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestWorkerPoolConcurrentSubmit proves Submit is goroutine-safe and
// the pool bounds in-flight work (capacity = size + queue). 50 concurrent
// goroutines Submit; the successful count cannot exceed capacity; the
// remainder get ErrWorkerPoolFull. No panic, race-clean.
func TestWorkerPoolConcurrentSubmit(t *testing.T) {
	cfg := WorkerConfig{PoolSize: 4, QueueSize: 20}
	pool, err := NewWorkerPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewWorkerPool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// releaseCh holds every accepted task in-flight until the test
	// releases it. This guarantees the pool stays at capacity while the
	// 50 Submit calls land, so the backpressure math is deterministic.
	releaseCh := make(chan struct{})
	var successCount, failCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := pool.Submit(func(ctx context.Context) error {
				<-releaseCh
				return nil
			})
			switch {
			case err == nil:
				successCount.Add(1)
			case errors.Is(err, ErrWorkerPoolFull):
				failCount.Add(1)
			default:
				t.Errorf("unexpected Submit error: %v", err)
			}
		}()
	}

	waitOrTimeout(t, &wg, 2*time.Second, "concurrent submits did not return")

	total := successCount.Load() + failCount.Load()
	if total != 50 {
		t.Errorf("total submits: got %d, want 50", total)
	}
	capacity := int64(cfg.PoolSize + cfg.QueueSize)
	if got := successCount.Load(); got > capacity {
		t.Errorf("successCount: got %d, want <= %d (pool capacity)", got, capacity)
	}

	close(releaseCh)
	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer sCancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestWorkerPoolRejectsInvalidConfig proves D-04 fail-fast: bad config
// at construction time returns an error wrapping ErrInvalidWorkerConfig
// instead of producing a pool that would panic at Start.
func TestWorkerPoolRejectsInvalidConfig(t *testing.T) {
	if _, err := NewWorkerPool(WorkerConfig{PoolSize: 0, QueueSize: 10}, NewNoOpLogger()); !errors.Is(err, ErrInvalidWorkerConfig) {
		t.Errorf("PoolSize=0: got %v, want ErrInvalidWorkerConfig", err)
	}
	if _, err := NewWorkerPool(WorkerConfig{PoolSize: 2, QueueSize: 0}, NewNoOpLogger()); !errors.Is(err, ErrInvalidWorkerConfig) {
		t.Errorf("QueueSize=0: got %v, want ErrInvalidWorkerConfig", err)
	}
	if _, err := NewWorkerPool(WorkerConfig{PoolSize: -1, QueueSize: 10}, NewNoOpLogger()); !errors.Is(err, ErrInvalidWorkerConfig) {
		t.Errorf("PoolSize=-1: got %v, want ErrInvalidWorkerConfig", err)
	}
}

// TestWorkerPoolSubstitutesNilLogger proves the DI zero-value contract:
// a nil Logger is silently replaced by NewNoOpLogger so callers that
// have nothing to log can pass nil without panicking on the first
// backpressure / panic log line.
func TestWorkerPoolSubstitutesNilLogger(t *testing.T) {
	// QueueSize=5 gives enough headroom that the post-panic Submit
	// cannot race the worker's pull of the panic-task and trip the
	// backpressure path.
	pool, err := NewWorkerPool(WorkerConfig{PoolSize: 1, QueueSize: 5}, nil)
	if err != nil {
		t.Fatalf("NewWorkerPool with nil logger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Trigger a panic recovery path — with nil logger this would
	// dereference a nil pointer if NewWorkerPool did not substitute.
	var ran atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	if err := pool.Submit(func(ctx context.Context) error {
		panic("ignored")
	}); err != nil {
		t.Fatalf("panic-task Submit: %v", err)
	}
	if err := pool.Submit(func(ctx context.Context) error {
		ran.Store(true)
		wg.Done()
		return nil
	}); err != nil {
		t.Fatalf("post-panic Submit: %v", err)
	}
	waitOrTimeout(t, &wg, 1*time.Second, "post-panic task did not run")
	if !ran.Load() {
		t.Errorf("post-panic task did not run; worker died from nil logger?")
	}
	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer sCancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestWorkerPoolStatsSnapshot proves Stats returns a consistent shape
// (zero values right after construction) and QueueCapacity matches
// cfg.QueueSize. Other counters are exercised by the behaviour tests.
func TestWorkerPoolStatsSnapshot(t *testing.T) {
	pool, err := NewWorkerPool(WorkerConfig{PoolSize: 2, QueueSize: 5}, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewWorkerPool: %v", err)
	}
	s := pool.Stats()
	if s.QueueCapacity != 5 {
		t.Errorf("QueueCapacity: got %d, want 5", s.QueueCapacity)
	}
	if s.QueueLen != 0 {
		t.Errorf("QueueLen: got %d, want 0", s.QueueLen)
	}
	if s.InFlight != 0 {
		t.Errorf("InFlight: got %d, want 0", s.InFlight)
	}
	if s.TotalProcessed != 0 || s.TotalPanics != 0 || s.TotalBackpressure != 0 {
		t.Errorf("counters not zero on fresh pool: %+v", s)
	}
}
