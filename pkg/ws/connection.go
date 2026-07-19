// Package ws provides WebSocket connection lifecycle management for the home tutor agent.
// This file implements the Client - a per-connection I/O manager with read/write pumps
// and panic recovery (D-16, D-49).
package ws

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// WebSocketConn is the interface required by Client for WebSocket operations.
// This abstraction allows testing with fake connections and decouples from gorilla/websocket.
type WebSocketConn interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	WriteControl(int, []byte, time.Time) error
	Close() error
}

// FrameHandler is the interface for handling decoded WebSocket frames (D-19 decoupling).
// Plan 03 Router implements this interface to route frames to business logic handlers.
// This design avoids circular dependencies between connection management and routing.
type FrameHandler interface {
	// Route processes a decoded frame and routes it to the appropriate business logic handler.
	// Returns an error if routing or processing fails; errors are logged but don't terminate the connection.
	Route(ctx context.Context, c *Client, header *FrameHeader, payload []byte) error
}

// ClientConfig holds configuration for creating a Client (D-44 centralized validation).
type ClientConfig struct {
	// SendBuffer is the capacity of the send channel (must be bounded to prevent OOM)
	// Default 16 messages, ~16KB per connection (RESEARCH.md Pitfall 5)
	SendBuffer int
}

// Validate checks if the ClientConfig is valid (D-44).
func (c ClientConfig) Validate() error {
	if c.SendBuffer <= 0 {
		return errors.Wrap(ErrInvalidClientConfig, "send buffer must be > 0")
	}
	return nil
}

// ErrInvalidClientConfig is returned when ClientConfig validation fails.
var ErrInvalidClientConfig = errors.New("invalid client config")

// Client represents a WebSocket client connection with independent read/write pumps (D-16).
// It extends the minimal skeleton defined in hub.go to include full connection lifecycle management.
type Client struct {
	// conn is the underlying WebSocket connection (interface for testability)
	conn WebSocketConn

	// hub is the centralized connection manager for registration/unregistration
	hub *Hub

	// handler processes decoded frames (decoupled from connection management)
	handler FrameHandler

	// send is the channel for outgoing messages (bounded, must be written by writePump only)
	// Handler logic writes to this channel; writePump serializes actual WebSocket writes
	send chan []byte

	// logger is the structured logger (D-25)
	logger *zap.Logger

	// closeOnce ensures Close is called only once (prevents double-close panics)
	closeOnce sync.Once
}

// NewClient creates a new Client with the given configuration (D-42 constructor DI).
// Returns an error if the configuration is invalid (D-44 validation).
// If logger is nil, a no-op logger is used (D-42 nil substitution).
func NewClient(conn WebSocketConn, hub *Hub, handler FrameHandler, cfg ClientConfig, logger *zap.Logger) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	return &Client{
		conn:    conn,
		hub:     hub,
		handler: handler,
		send:    make(chan []byte, cfg.SendBuffer),
		logger:  logger,
	}, nil
}

// readPump reads messages from WebSocket and routes decoded frames to the handler (Pattern 3).
// goroutine-per-connection model (D-16) prevents I/O serialization across connections.
// Must be run as a goroutine.
//
// Guarantees cleanup on any exit path:
// - Always unregisters from hub (WS-03 session cleanup)
// - Recovers from panics (D-49 panic recovery)
// - Respects context cancellation (D-12 lifecycle)
func (c *Client) readPump(ctx context.Context) {
	// WS-03: Unregister from hub on any exit path (error, panic, or context cancel)
	defer func() {
		c.hub.Unregister(c)
	}()

	// D-49: Recover from panics in handler.Route to prevent connection termination
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("readPump panic recovered",
				zap.Any("recovered", r),
				zap.Stack("stack"))
		}
	}()

	for {
		select {
		default:
			// Blocking read from WebSocket (returns when connection closes or error occurs)
			msgType, data, err := c.conn.ReadMessage()
			if err != nil {
				// Connection closed or read error - exit goroutine normally
				// Hub.Unregister will be called by defer
				return
			}
			_ = msgType // We only process binary messages (our custom protocol)

			// Decode frame using Plan 01 protocol
			header, payload, err := DecodeFrame(data)
			if err != nil {
				// Skip malformed frames but don't disconnect (avoid single-frame踢连接)
				// Malicious frames can't kick off legitimate connections
				c.logger.Warn("frame decode failed, skipping", zap.Error(err))
				continue
			}

			// Route decoded frame to handler (non-blocking via separate goroutine in writePump)
			if err := c.handler.Route(ctx, c, header, payload); err != nil {
				// Handler errors are logged but don't terminate the connection
				c.logger.Warn("handler route failed", zap.Error(err))
			}

		case <-ctx.Done():
			// D-12: Context cancelled - exit goroutine gracefully
			return
		}
	}
}

// writePump writes messages from the send channel to WebSocket (Pattern 3).
// Serializes all writes to prevent concurrent write panics (RESEARCH.md Pitfall 3).
// Must be run as a goroutine.
//
// Guarantees cleanup on any exit path:
// - Always closes the WebSocket connection (triggers readPump exit via ReadMessage error)
// - Respects context cancellation (D-12 lifecycle)
func (c *Client) writePump(ctx context.Context) {
	// Close WebSocket on exit to trigger readPump termination
	defer func() {
		_ = c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				// Channel closed by Hub (via Unregister) - exit goroutine
				return
			}

			// Serial write to WebSocket (only one goroutine writes to conn)
			if err := c.conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
				// Write error - exit goroutine (connection likely broken)
				return
			}

		case <-ctx.Done():
			// D-12: Context cancelled - exit goroutine gracefully
			return
		}
	}
}

// Close gracefully closes the client connection with a status code and reason.
// Uses closeOnce to prevent double-close panics (can be called by Hub on rejection).
// Sends a WebSocket close control frame with a 1-second write deadline.
func (c *Client) Close(code int, reason string) error {
	c.closeOnce.Do(func() {
		// Send close frame with timeout
		_ = c.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(code, reason),
			time.Now().Add(time.Second),
		)
		// Close the underlying connection
		_ = c.conn.Close()
	})
	return nil
}