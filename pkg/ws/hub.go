// Package ws provides WebSocket connection lifecycle management for the home tutor agent.
// This file implements the Hub - a centralized connection manager with capacity limits
// and graceful client registration/unregistration.
package ws

import (
	"context"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	"go.uber.org/zap"
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

	// mu protects the clients map (D-11 RWMutex for read-heavy workload)
	mu sync.RWMutex

	// limit is the maximum number of concurrent connections (D-04)
	limit int

	// logger is the structured logger (D-25)
	logger *zap.Logger
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
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case c := <-h.register:
			// Handle client registration (D-04 capacity limit)
			h.mu.Lock()
			if len(h.clients) >= h.limit {
				h.mu.Unlock()
				// Reject client at capacity (D-04 graceful rejection)
				_ = c.Close(websocket.CloseTryAgainLater, "server full")
				h.logger.Info("hub rejected client at capacity",
					zap.Int("limit", h.limit))
				continue
			}

			// Accept client
			h.clients[c] = true
			h.mu.Unlock()
			h.logger.Info("hub registered client",
				zap.Int("total", len(h.clients)))

		case c := <-h.unregister:
			// Handle client unregistration (WS-03 session cleanup)
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				h.mu.Unlock()
				// Signal writePump to exit by closing send channel
				close(c.send)
				h.logger.Info("hub unregistered client",
					zap.Int("total", len(h.clients)))
			} else {
				// Client already unregistered (idempotent)
				h.mu.Unlock()
			}

		case <-ctx.Done():
			// Context cancelled - exit goroutine (D-12 resource cleanup)
			return
		}
	}
}

// Register sends a client registration request to the hub (D-17 channel routing).
// This is a non-blocking operation - the request is queued in the register channel.
func (h *Hub) Register(c *Client) {
	h.register <- c
}

// Unregister sends a client unregistration request to the hub (D-17 channel routing).
// This is a non-blocking operation - the request is queued in the unregister channel.
func (h *Hub) Unregister(c *Client) {
	h.unregister <- c
}

// Clients returns the current number of active clients (D-11 thread-safe).
func (h *Hub) Clients() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
