// Package ws provides WebSocket connection lifecycle management for the home tutor agent.
// This file implements the Hub - a centralized connection manager with capacity limits
// and graceful client registration/unregistration.
package ws

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	"go.uber.org/zap"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// HubConfig holds configuration for creating a Hub (D-44 centralized validation).
type HubConfig struct {
	// Limit is the maximum number of concurrent connections (D-04, default 100)
	Limit int

	// RegisterBuffer is the capacity of the registration channel (must be bounded)
	RegisterBuffer int

	// UnregisterBuffer is the capacity of the unregistration channel (must be bounded)
	UnregisterBuffer int
}

// Validate checks if the HubConfig is valid (D-44).
func (c HubConfig) Validate() error {
	if c.Limit <= 0 {
		return errors.Wrap(ErrInvalidHubConfig, "limit must be > 0")
	}
	if c.RegisterBuffer <= 0 {
		return errors.Wrap(ErrInvalidHubConfig, "register buffer must be > 0")
	}
	if c.UnregisterBuffer <= 0 {
		return errors.Wrap(ErrInvalidHubConfig, "unregister buffer must be > 0")
	}
	return nil
}

// ErrInvalidHubConfig is returned when HubConfig validation fails.
var ErrInvalidHubConfig = errors.New("invalid hub config")

// Client forward declaration - full definition in connection.go
// This allows Hub to reference Client without circular dependency.

// Hub is the centralized WebSocket connection manager (D-11 thread-safe).
// It manages client registration, unregistration, and enforces capacity limits.
// Hub does NOT depend on pkg/infra (Phase 1 Plan 06 not delivered yet) -
// it maintains a self-contained clients map (RESEARCH.md Open Question 3 resolution).
type Hub struct {
	// register is the channel for client registration requests (D-17 channel routing)
	register chan *Client

	// unregister is the channel for client unregistration requests (D-17)
	unregister chan *Client

	// clients holds the active client connections (D-11 protected by mu)
	clients map[*Client]bool

	// logger is the structured logger (D-25)
	logger *zap.Logger

	// limit is the maximum number of concurrent connections (D-04)
	limit int

	// totalAccepted/totalRejected/totalCleaned track connection lifecycle
	// events for the /metrics endpoint (D-04 option-a Hub-backed metrics).
	// Incremented atomically in Run(); read by Stats().
	totalAccepted atomic.Uint64
	totalRejected atomic.Uint64
	totalCleaned  atomic.Uint64

	// mu protects the clients map (D-11 RWMutex for read-heavy workload)
	mu sync.RWMutex

	// closeMu guards the shutdown flag. Register/Unregister take the read lock to
	// check closed before a non-blocking send; Close takes the write lock to flip
	// the flag. This mirrors Client.CloseSend (CR-04) and is what prevents a
	// send-on-closed-channel race — and, more importantly here, prevents
	// readPump's deferred Unregister from blocking indefinitely once Hub.Run has
	// stopped draining (CR-01). We intentionally do NOT close the register/
	// unregister channels: doing so would make Run receive nil values and busy-loop.
	closeMu sync.RWMutex
	closed  bool
}

// NewHub creates a new Hub with the given configuration (D-42 constructor DI).
// Returns an error if the configuration is invalid (D-44 validation).
// If logger is nil, a no-op logger is used (D-42 nil substitution).
func NewHub(cfg HubConfig, logger *zap.Logger) (*Hub, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	return &Hub{
		register:   make(chan *Client, cfg.RegisterBuffer),
		unregister: make(chan *Client, cfg.UnregisterBuffer),
		clients:    make(map[*Client]bool),
		limit:      cfg.Limit,
		logger:     logger,
	}, nil
}

// Run starts the hub's main event loop (D-12 context-based lifecycle).
// It processes client registration, unregistration, and context cancellation.
// This method blocks until the context is cancelled.
//
// On exit (any path) Run defers Close, which flips the closed flag so that any
// straggler Register/Unregister calls (e.g. from a readPump's defer) are
// short-circuited instead of blocking forever on channels nobody is draining
// (CR-01 goroutine-leak fix).
func (h *Hub) Run(ctx context.Context) {
	defer h.Close()
	for {
		select {
		case c := <-h.register:
			// Handle client registration (D-04 capacity limit)
			h.mu.Lock()
			if len(h.clients) >= h.limit {
				h.mu.Unlock()
				// Reject client at capacity (D-04 graceful rejection)
				h.totalRejected.Add(1) // D-04: track for /metrics
				_ = c.Close(websocket.CloseTryAgainLater, "server full")
				h.logger.Info("hub rejected client at capacity",
					zap.Int("limit", h.limit))
				continue
			}

			// Accept client
			h.clients[c] = true
			h.totalAccepted.Add(1)  // D-04: track for /metrics
			total := len(h.clients) // CR-07: capture under lock; reading after Unlock races future writers
			h.mu.Unlock()
			h.logger.Info("hub registered client",
				zap.Int("total", total))

		case c := <-h.unregister:
			// Handle client unregistration (WS-03 session cleanup)
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				h.totalCleaned.Add(1)   // D-04: track for /metrics
				total := len(h.clients) // CR-07: capture under lock
				h.mu.Unlock()
				// Signal writePump to exit by closing the send channel (close-safe, CR-04)
				c.CloseSend()
				h.logger.Info("hub unregistered client",
					zap.Int("total", total))
			} else {
				// Client already unregistered (idempotent)
				h.mu.Unlock()
			}

		case <-ctx.Done():
			// CR-01: drain any pending unregister so that in-flight disconnects
			// get their CloseSend (writePump-exit) signal. Unregister is already
			// non-blocking, so this drain is belt-and-suspenders for clean teardown.
			h.drainUnregister()
			return
		}
	}
}

// drainUnregister processes any buffered unregister requests without blocking.
// Called from Run on ctx.Done() so writePumps partnered with disconnecting
// clients receive their CloseSend signal during shutdown (CR-01).
func (h *Hub) drainUnregister() {
	for {
		select {
		case c := <-h.unregister:
			if c == nil {
				continue
			}
			c.CloseSend()
		default:
			return
		}
	}
}

// Register sends a client registration request to the hub (D-17 channel routing).
//
// Genuinely non-blocking (CR-01): it returns false if the hub is shutting down
// (Close has run) or the register channel is saturated. The caller must reject
// the connection on false — otherwise the client would be silently dropped or,
// under the old blocking semantics, the caller would pin a goroutine forever.
// The doc comments previously claimed "non-blocking" but the channel send was
// blocking once the buffer filled; this implementation now honours the claim.
func (h *Hub) Register(c *Client) bool {
	h.closeMu.RLock()
	defer h.closeMu.RUnlock()
	if h.closed {
		return false
	}
	select {
	case h.register <- c:
		return true
	default:
		return false // saturated — shed; caller should reject the connection
	}
}

// Unregister sends a client unregistration request to the hub (D-17 channel routing).
//
// Genuinely non-blocking (CR-01): if the hub is shutting down (Close has run) or
// the unregister channel is saturated, the request is dropped and a warning is
// logged. This is critical because readPump calls Unregister in a defer on every
// exit path; a blocking send here (as the old implementation had) pins the
// goroutine forever once Hub.Run stops draining during shutdown — the exact leak
// that took down deploys on the 1C/1G VPS. The dropped client is reclaimed by GC
// once its pumps exit.
func (h *Hub) Unregister(c *Client) {
	h.closeMu.RLock()
	defer h.closeMu.RUnlock()
	if h.closed {
		return
	}
	select {
	case h.unregister <- c:
	default:
		h.logger.Warn("unregister dropped: hub saturated or shutting down")
	}
}

// Close shuts the hub down (CR-01). After Close returns, Register reports false
// and Unregister is a no-op, so callers running concurrently with shutdown (a
// readPump's deferred Unregister, an in-flight HandleWebSocket) can never block
// on the register/unregister channels. Idempotent.
//
// Close does NOT physically close the register/unregister channels: Run reads
// from them, and closing them would make Run receive nil values. The closed flag
// under closeMu is the entire mechanism — the channels are reclaimed by GC once
// the Hub is unreachable. Run defers Close on exit, so callers normally do not
// need to invoke it directly; Server.Shutdown calls it to short-circuit
// stragglers before they reach the channels.
func (h *Hub) Close() {
	h.closeMu.Lock()
	defer h.closeMu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
}

// isClosed reports whether Close has flipped the shutdown flag. It is the
// synchronized read side of the closeMu-guarded field — polling h.closed
// directly would race Run's deferred Close write (issue #26).
func (h *Hub) isClosed() bool {
	h.closeMu.RLock()
	defer h.closeMu.RUnlock()
	return h.closed
}

// Clients returns the current number of active clients (D-11 thread-safe).
func (h *Hub) Clients() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Stats returns a snapshot of the hub's connection lifecycle counters (D-04
// option-a Hub-backed metrics). Active is sampled under the read lock; the
// monotonic counters are read atomically. Called by Server.Snapshot via the
// connection-stats provider wired in main.go.
func (h *Hub) Stats() infra.ConnectionStats {
	h.mu.RLock()
	active := len(h.clients)
	h.mu.RUnlock()
	return infra.ConnectionStats{
		Active:        active,
		TotalAccepted: h.totalAccepted.Load(),
		TotalRejected: h.totalRejected.Load(),
		TotalCleaned:  h.totalCleaned.Load(),
	}
}
