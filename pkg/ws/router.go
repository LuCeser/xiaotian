package ws

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// routerShutdownTimeout bounds how long Router.Close waits for in-flight workers
// before giving up (WR-02). r.cancel() only fires ctx.Done(); it cannot interrupt
// a handler blocked on a slow downstream (TTS API, DB, time.Sleep). Without a
// bound, a single stuck handler blocks clean process exit on the constrained VPS
// and may force SIGKILL, losing in-flight work.
const routerShutdownTimeout = 5 * time.Second

// routerPanicThreshold is the per-router handler-panic count at which the worker
// circuit breaker trips and stops dequeuing tasks (WR-01). Prevents CPU/log
// amplification from a repeatedly-panicking handler on the 1C VPS.
const routerPanicThreshold int32 = 8

// Handler is a business logic processing function.
//
// WR-08: the ctx passed to a Handler is the PER-CONNECTION context (readPump's
// ctx), NOT the Router's lifetime context. Consequences callers must know:
//   - Router.Close / r.cancel() does NOT interrupt an in-flight Handler; only
//     the connection context cancelling (client disconnect, server shutdown)
//     does. WR-02's bounded Close wait is therefore the only defence against a
//     Handler blocked on a slow downstream.
//   - A Handler that needs to observe the Router's lifetime (e.g. to flush
//     state on router shutdown) cannot do so via ctx today — it would need a
//     separate signal. File as a follow-up if a handler requires it.
//   - After a client disconnects its ctx is cancelled, so queued tasks for that
//     client become no-ops; they still occupy the channel and a worker still
//     dequeues them. Benign, but the cause of the bounded queue drain on close.
type Handler func(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error

// RouterConfig defines router configuration
type RouterConfig struct {
	RouteBuffer       int // per-type handler channel capacity (default 32)
	WorkerConcurrency int // per-type concurrent worker count (default 4)
}

// Validate checks if the configuration is valid
func (c RouterConfig) Validate() error {
	if c.RouteBuffer <= 0 {
		return errors.Wrap(ErrInvalidRouterConfig, "route buffer must be > 0")
	}
	if c.WorkerConcurrency <= 0 {
		return errors.Wrap(ErrInvalidRouterConfig, "worker concurrency must be > 0")
	}
	return nil
}

// Pre-defined error types
var (
	ErrInvalidRouterConfig = errors.New("invalid router config")
	ErrRouterOverloaded    = errors.New("router overloaded")    // D-73 drop-oldest signal
	ErrRouterShuttingDown  = errors.New("router shutting down") // Context cancellation
)

// Router dispatches decoded frames to per-MsgType handlers via Go channels
type Router struct {
	routes   map[MessageType]chan frameTask
	handlers map[MessageType]Handler
	wg       sync.WaitGroup
	logger   *zap.Logger
	ctx      context.Context
	cancel   context.CancelFunc

	// overloaded counts frames dropped because a per-type channel was full
	// (ErrRouterOverloaded). Observable server-side backpressure signal (WR-09).
	overloaded atomic.Int32

	// panicCount is a router-wide tally of recovered handler panics used by the
	// worker circuit breaker (WR-01). On a 1C VPS an unthrottled
	// panic->recover->panic loop pins the single core and emits GB of stack
	// traces (zap.Stack calls runtime.Stack, which is very expensive). Once the
	// count exceeds routerPanicThreshold the workers stop dequeuing so a single
	// buggy/malicious route cannot amplify. The counter is shared across all
	// routes/workers: conservative, but adequate for a 1C budget.
	panicCount atomic.Int32
}

// frameTask represents a frame to be processed
type frameTask struct {
	ctx     context.Context
	client  *Client
	header  *FrameHeader
	payload []byte
}

// NewRouter creates a new router instance (D-42 + D-44)
func NewRouter(handlers map[MessageType]Handler, cfg RouterConfig, logger *zap.Logger) (*Router, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "router config validation failed")
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	ctx, cancel := context.WithCancel(context.Background())

	router := &Router{
		routes:   make(map[MessageType]chan frameTask),
		handlers: handlers,
		logger:   logger,
		ctx:      ctx,
		cancel:   cancel,
	}

	// Create channels for each message type and start workers
	for msgType := range handlers {
		router.routes[msgType] = make(chan frameTask, cfg.RouteBuffer)

		// Start WorkerConcurrency workers per message type (D-16 worker pool for logic)
		for i := 0; i < cfg.WorkerConcurrency; i++ {
			router.wg.Add(1)
			go router.worker(msgType, handlers[msgType])
		}
	}

	return router, nil
}

// Route submits a frame to the appropriate handler (FrameHandler interface
// implementation). The caller's ctx (readPump's per-connection ctx) is stored
// in the frameTask and later passed to the handler as-is (WR-08): handlers run
// under the CONNECTION context, not the Router's. See Handler doc for the
// cancellation semantics this implies.
func (r *Router) Route(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
	// IN-06: tests construct bare &Router{} (server_test.go) for config-validation
	// paths, leaving ctx/cancel as nil zero values. Reading from a nil ctx's
	// Done() would nil-deref. Treat an uninitialized router as "shutting down".
	if r.ctx == nil {
		return ErrRouterShuttingDown
	}
	// Check if router is shutting down first
	select {
	case <-r.ctx.Done():
		return ErrRouterShuttingDown
	default:
	}

	// IN-05: handlers is populated once in NewRouter and never mutated, so the
	// map is safe to read concurrently without a lock (Go permits concurrent map
	// reads when there are no writers). The previous RLock/RUnlock guarded
	// nothing and added overhead on the readPump hot path.
	_, ok := r.handlers[h.MsgType]

	if !ok {
		return errors.Wrap(ErrUnknownMessageType, fmt.Sprintf("msg type 0x%02x", h.MsgType))
	}

	task := frameTask{
		ctx:     ctx,
		client:  c,
		header:  h,
		payload: payload,
	}

	select {
	case r.routes[h.MsgType] <- task:
		return nil // Successfully queued
	case <-r.ctx.Done():
		return ErrRouterShuttingDown
	default:
		r.overloaded.Add(1) // WR-09: observable server-side backpressure counter
		return ErrRouterOverloaded // D-73 drop-oldest backpressure
	}
}

// OverloadedCount returns the number of frames dropped because a per-type channel was
// full (ErrRouterOverloaded). Exposed so the backpressure signal can be observed
// server-side — in tests and monitoring — rather than inferred from client write
// errors, which never reflect router-level drops (WR-09).
func (r *Router) OverloadedCount() int64 {
	return int64(r.overloaded.Load())
}

// worker processes frames from the channel (D-16 worker pool for logic)
func (r *Router) worker(mt MessageType, handler Handler) {
	defer r.wg.Done()

	for {
		// WR-01: circuit breaker. If handlers (router-wide) have panicked beyond
		// routerPanicThreshold, stop dequeuing this route so a buggy/malicious
		// handler cannot pin the single CPU core in a panic->recover->panic loop.
		// readPump, by contrast, exits the whole connection on first panic — much
		// smaller blast radius; this is the router-side equivalent.
		if r.panicCount.Load() > routerPanicThreshold {
			r.logger.Error("router worker stopping: handler panic threshold exceeded",
				zap.String("component", "ws.router"),
				zap.String("msg_type", mt.String()),
				zap.Int32("panics", r.panicCount.Load()),
				zap.Int32("threshold", routerPanicThreshold))
			return
		}
		select {
		case task, ok := <-r.routes[mt]:
			if !ok {
				// Channel closed
				return
			}
			// Execute handler with panic recovery (D-49)
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						// WR-01: zap.Stack calls runtime.Stack (very expensive). A
						// panicking handler can fire on every frame from a streaming
						// client, so capture the full stack only on the first panic;
						// subsequent ones log a cheap count line. The circuit breaker
						// above then stops the loop once the threshold is crossed.
						count := r.panicCount.Add(1)
						if count == 1 {
							r.logger.Error("handler panic recovered (first occurrence, full stack)",
								zap.String("component", "ws.router"),
								zap.String("msg_type", mt.String()),
								zap.Any("recovered", rec),
								zap.Stack("stack"))
						} else {
							r.logger.Error("handler panic recovered",
								zap.String("component", "ws.router"),
								zap.String("msg_type", mt.String()),
								zap.Any("recovered", rec),
								zap.Int32("panic_count", count))
						}
					}
				}()

				if err := handler(task.ctx, task.client, task.header, task.payload); err != nil {
					r.logger.Warn("handler returned error",
						zap.String("msg_type", mt.String()),
						zap.Error(err))
				}
			}() // Handler must complete before worker receives next task - this creates real backpressure
		case <-r.ctx.Done():
			return
		}
	}
}

// Close gracefully shuts down the router.
//
// WR-02: bounds the worker wait at routerShutdownTimeout. r.cancel() only fires
// ctx.Done(); it cannot interrupt a handler blocked on a slow downstream, so an
// unbounded wg.Wait() could stall process exit indefinitely on the constrained
// VPS. Returns nil if all workers exited cleanly, or an error if the timeout
// elapsed (workers may still be running — the caller should treat this as "give
// up and let the process exit"). Callers that previously ignored the return value
// continue to compile.
func (r *Router) Close() error {
	// IN-06: guard against the zero-value Router (bare &Router{} constructed in
	// tests for config validation), where cancel is nil. Calling a nil CancelFunc
	// panics.
	if r.cancel != nil {
		r.cancel()
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(routerShutdownTimeout):
		r.logger.Error("router close timed out waiting for workers",
			zap.Duration("timeout", routerShutdownTimeout))
		return errors.New("router shutdown timed out")
	}
}
