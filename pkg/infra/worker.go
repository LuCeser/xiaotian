package infra

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

// Task is the unit of work the WorkerPool executes (D-43 small focused
// interface). A Task receives the pool's Start context so it can honour
// cancellation / timeout per D-45 — long-running Tasks MUST select on
// ctx.Done() to avoid wedging a worker (Pitfall 3).
type Task func(ctx context.Context) error

// Sentinel errors returned by the WorkerPool API. Callers MUST detect
// them via errors.Is, never by string matching (D-04 fail-fast on
// contract violations, observable without parsing log text).
var (
	// ErrWorkerPoolFull is the typed backpressure signal returned by
	// Submit when the bounded queue is full (D-72). Plan 08's server
	// translates it into an HTTP 503 / explicit backpressure frame to
	// the client (D-74 graceful rejection — never a silent drop, never
	// a panic).
	ErrWorkerPoolFull = errors.New("worker pool queue full")

	// ErrWorkerPoolClosed is the typed rejection returned by Submit once
	// Shutdown has closed the task channel. A Submit racing Shutdown gets
	// this error instead of a send-on-closed-channel panic; callers treat
	// it exactly like backpressure (log-and-drop), which during a drain is
	// the correct outcome — the work arrives after the pool committed to
	// finishing only what was already queued.
	ErrWorkerPoolClosed = errors.New("worker pool closed")

	// ErrPoolAlreadyRunning is returned by a second Start call on the
	// same pool. It prevents double-spawning workers off the same
	// WaitGroup, which would race the wg.Add/Done accounting.
	ErrPoolAlreadyRunning = errors.New("worker pool already running")

	// ErrInvalidWorkerConfig is returned by NewWorkerPool when the
	// supplied WorkerConfig fails validation (D-04 fail-fast at
	// construction instead of panicking at Start time).
	ErrInvalidWorkerConfig = errors.New("invalid worker pool configuration")

	// ErrShutdownTimeout is returned by Shutdown when its ctx expires
	// before all in-flight workers have finished draining (D-22). On
	// this error the caller is responsible for cancelling the Start
	// context to force worker termination.
	ErrShutdownTimeout = errors.New("worker pool shutdown timed out")
)

// WorkerPool is a fixed-size pool of worker goroutines fed by a bounded
// queue (D-10). The pool bounds the number of goroutines handling
// in-flight work — Submit never blocks; when the queue is full it
// returns ErrWorkerPoolFull, a typed backpressure signal the caller
// (Plan 08 server) translates into HTTP 503 or a client-facing
// backpressure frame (D-72, D-74).
//
// Component redundancy per D-16: multiple peer workers run
// independently. If one worker stalls inside a Task (Pitfall 3) or
// panics (D-49), the peers continue draining the queue — there is no
// single point of failure inside the pool.
//
// Concurrency contract:
//   - Start MUST be called exactly once. A second call returns
//     ErrPoolAlreadyRunning.
//   - Submit is goroutine-safe; callers may submit from many goroutines.
//   - Submit MAY be called concurrently with Shutdown: the pair is
//     mutually excluded by an internal mutex, and a Submit that loses
//     the race returns ErrWorkerPoolClosed (a typed rejection, same
//     handling as ErrWorkerPoolFull) instead of panicking on
//     send-on-closed-channel.
//   - Shutdown MAY be called more than once and from concurrent
//     goroutines: only the first call closes the task channel; later
//     calls still wait for the same drain and return the same outcome.
//     Restarting still requires constructing a new WorkerPool.
type WorkerPool struct {
	logger Logger
	tasks  chan Task

	// mu guards the tasks channel's lifecycle: Submit's send and
	// Shutdown's close are mutually exclusive under it, so a Submit racing
	// Shutdown observes closed and returns ErrWorkerPoolClosed instead of
	// panicking on send-on-closed-channel. It is never held across a
	// blocking operation (Submit's select has a default; Shutdown only
	// flips the flag and closes), so the lock cannot deadlock or delay the
	// worker's unguarded receives.
	mu     sync.Mutex
	closed bool

	// wg tracks in-flight worker goroutines. Add happens in Start before
	// the goroutine is launched; Done happens via defer in worker. wg.
	// Wait() therefore returns only when every worker has exited.
	wg                sync.WaitGroup
	size              int
	totalProcessed    atomic.Uint64
	totalPanics       atomic.Uint64
	totalBackpressure atomic.Uint64

	// running guards Start against double-spawn. CAS from false→true
	// inside Start; Store(false) inside Shutdown's clean-drain path.
	running atomic.Bool

	// Counters below use sync/atomic value types so Stats() can read
	// them without taking a lock.
	inFlight atomic.Int32 // tasks currently executing
}

// WorkerPoolStats is the point-in-time snapshot returned by Stats. Each
// field is observed atomically; the snapshot is consistent within a
// field but NOT across fields (e.g. InFlight and QueueLen can be read
// at slightly different instants). Callers that need a transactional
// snapshot must take one externally (none of the Phase 1 callers do).
type WorkerPoolStats struct {
	InFlight          int32  `json:"in_flight"`
	QueueCapacity     int    `json:"queue_capacity"`
	QueueLen          int    `json:"queue_len"`
	TotalProcessed    uint64 `json:"total_processed"`
	TotalPanics       uint64 `json:"total_panics"`
	TotalBackpressure uint64 `json:"total_backpressure"`
}

// NewWorkerPool constructs a WorkerPool with the supplied config.
// PoolSize and QueueSize MUST both be > 0; on failure the returned
// error wraps ErrInvalidWorkerConfig (D-04 fail-fast at construction
// instead of at Start).
//
// If logger is nil, NewNoOpLogger() is substituted so callers can pass
// nil from contexts that have nothing to log (D-42 dependency-injection
// zero value). This avoids a nil-pointer panic on the first backpressure
// / panic log line.
//
// NewWorkerPool does not spawn workers; call Start to do so.
func NewWorkerPool(cfg WorkerConfig, logger Logger) (*WorkerPool, error) {
	if cfg.PoolSize <= 0 {
		return nil, fmt.Errorf("%w: poolSize must be > 0, got %d",
			ErrInvalidWorkerConfig, cfg.PoolSize)
	}
	if cfg.QueueSize <= 0 {
		return nil, fmt.Errorf("%w: queueSize must be > 0, got %d",
			ErrInvalidWorkerConfig, cfg.QueueSize)
	}
	if logger == nil {
		logger = NewNoOpLogger()
	}
	return &WorkerPool{
		tasks:  make(chan Task, cfg.QueueSize),
		size:   cfg.PoolSize,
		logger: logger,
	}, nil
}

// Start spawns exactly size worker goroutines, each selecting on
// ctx.Done() and the task channel. It is idempotent in the failure
// sense: a second call returns ErrPoolAlreadyRunning without spawning
// additional workers.
//
// The supplied ctx is the lifetime context for the workers. Cancelling
// it causes every worker to exit on its next loop iteration (Pitfall 1
// mitigation: no goroutine leak). Cancelling ctx does NOT close the
// task channel — Shutdown does that.
//
// Start returns nil on success. Errors:
//   - ErrPoolAlreadyRunning: Start was already called on this pool.
func (p *WorkerPool) Start(ctx context.Context) error {
	if !p.running.CompareAndSwap(false, true) {
		return ErrPoolAlreadyRunning
	}
	for i := 0; i < p.size; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}
	return nil
}

// worker is the per-goroutine loop. It exits on either ctx.Done() or
// the task channel being closed (Shutdown). Per-task recover() lives
// inside runTask so a panic on one Task does not kill this worker — the
// loop continues to the next iteration and the pool keeps draining
// (D-16 component redundancy, D-49 panic recovery).
func (p *WorkerPool) worker(ctx context.Context, id int) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case task, ok := <-p.tasks:
			if !ok {
				// Channel closed by Shutdown. Drain is complete;
				// exit cleanly so wg.Wait() unblocks.
				return
			}
			p.runTask(ctx, task, id)
		}
	}
}

// runTask executes one Task with per-task panic recovery. inFlight is
// incremented before the Task runs and decremented after it returns or
// panics. On panic, an Error entry is emitted with recovered_panic=true
// and a stack trace; totalPanics is incremented. On normal return
// (whether the Task returned nil or an error), totalProcessed is
// incremented — panics do NOT count as processed.
//
// Task errors are logged at Warn level. They are expected Task outcomes
// (e.g. upstream throttle, bad input), not infrastructure failures.
// Panics are logged at Error level because they indicate a bug in the
// Task and need operator attention.
func (p *WorkerPool) runTask(ctx context.Context, task Task, workerID int) {
	p.inFlight.Add(1)
	defer p.inFlight.Add(-1)

	func() {
		defer func() {
			if r := recover(); r != nil {
				p.totalPanics.Add(1)
				p.logger.Error("worker recovered task panic",
					F("component", "infra.worker"),
					F("worker_id", workerID),
					F("recovered_panic", true),
					F("error", fmt.Sprintf("%v", r)),
					F("stack", string(debug.Stack())),
				)
			}
		}()
		if err := task(ctx); err != nil {
			p.logger.Warn("task returned error",
				F("component", "infra.worker"),
				F("worker_id", workerID),
				ErrField(err),
			)
		}
		p.totalProcessed.Add(1)
	}()
}

// Submit enqueues a Task. It never blocks: when the bounded queue is
// full it returns ErrWorkerPoolFull immediately (D-72 backpressure
// signal), and once Shutdown has closed the task channel it returns
// ErrWorkerPoolClosed. The caller (Plan 08 server) translates either
// typed error into an HTTP 503 or an explicit backpressure frame to the
// client (D-74 graceful rejection — never a silent drop, never a panic).
//
// The mutex-held select has a default, so Submit cannot block on a full
// queue OR on Shutdown holding the lock; workers receive from p.tasks
// without touching the lock, so a busy worker never keeps Submit waiting.
func (p *WorkerPool) Submit(task Task) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrWorkerPoolClosed
	}
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

// Shutdown initiates a graceful drain (D-21): it closes the task channel
// (signalling workers that no more work is coming) and waits for either
// all workers to exit OR ctx to expire. On clean drain it returns nil;
// on ctx expiry it returns ErrShutdownTimeout (D-22).
//
// Shutdown is safe to call concurrently with Submit and safe to call
// more than once: only the first caller closes the channel (later ones
// skip the close), and every caller waits on the same worker WaitGroup.
// To "restart" a pool, construct a new one.
//
// Shutdown does NOT cancel the Start context. If Shutdown returns
// ErrShutdownTimeout, in-flight workers may still be running; the caller
// should cancel the Start context to force termination. On a clean
// drain (nil return), all workers have exited before Shutdown returns.
func (p *WorkerPool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.tasks)
	}
	p.mu.Unlock()
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		p.running.Store(false)
		return nil
	case <-ctx.Done():
		return ErrShutdownTimeout
	}
}

// Stats returns a point-in-time snapshot of pool counters. The snapshot
// is consistent within each field but not across fields. QueueCapacity
// is the original cfg.QueueSize (channel cap, constant for the pool's
// lifetime); QueueLen is the number of Tasks currently buffered.
func (p *WorkerPool) Stats() WorkerPoolStats {
	return WorkerPoolStats{
		InFlight:          p.inFlight.Load(),
		QueueCapacity:     cap(p.tasks),
		QueueLen:          len(p.tasks),
		TotalProcessed:    p.totalProcessed.Load(),
		TotalPanics:       p.totalPanics.Load(),
		TotalBackpressure: p.totalBackpressure.Load(),
	}
}
