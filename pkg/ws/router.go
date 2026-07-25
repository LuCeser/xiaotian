package ws

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// Handler is a business logic processing function
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
	mu       sync.RWMutex
	logger   *zap.Logger
	ctx      context.Context
	cancel   context.CancelFunc

	// overloaded counts frames dropped because a per-type channel was full
	// (ErrRouterOverloaded). Observable server-side backpressure signal (WR-09).
	overloaded atomic.Int32
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

// Route submits a frame to the appropriate handler (FrameHandler interface implementation)
func (r *Router) Route(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
	// Check if router is shutting down first
	select {
	case <-r.ctx.Done():
		return ErrRouterShuttingDown
	default:
	}

	r.mu.RLock()
	_, ok := r.handlers[h.MsgType]
	r.mu.RUnlock()

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
						r.logger.Error("handler panic recovered",
							zap.String("component", "ws.router"),
							zap.String("msg_type", mt.String()),
							zap.Any("recovered", rec),
							zap.Stack("stack"))
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

// Close gracefully shuts down the router
func (r *Router) Close() {
	r.cancel()
	r.wg.Wait() // Wait for all workers to exit (D-12 cleanup)
}
