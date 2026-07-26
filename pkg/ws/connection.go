// Package ws provides WebSocket connection lifecycle management for the home tutor agent.
// This file implements the Client - a per-connection I/O manager with read/write pumps
// and panic recovery (D-16, D-49).
package ws

import (
	"context"
	"os"
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
	SetWriteDeadline(time.Time) error
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

// Sentinel errors for Client.Send (CR-04).
var (
	// ErrSendBufferFull is returned by Send when the bounded send channel is full.
	// Non-blocking: callers should shed load (consistent with the router's drop-oldest semantics, D-73).
	ErrSendBufferFull = errors.New("client send buffer full")
	// ErrClientClosing is returned by Send when the client is shutting down and the
	// send channel has been (or is being) closed by CloseSend.
	ErrClientClosing = errors.New("client is closing")
)

// defaultWriteTimeout bounds how long a single WebSocket write may block, protecting
// against slow/frozen clients pinning goroutines under the 150MB memory budget (CR-06).
const defaultWriteTimeout = 10 * time.Second

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

	// sendMu guards send-channel close safety. Send takes the read lock to check
	// sendClosed before a non-blocking send; CloseSend takes the write lock to flip
	// sendClosed and close the channel atomically. This makes Send safe to call
	// even when Hub.Unregister races it (CR-04).
	sendMu     sync.RWMutex
	sendClosed bool
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

	// CR-01: a blocked ReadMessage cannot be interrupted by ctx alone. Spawn a watcher
	// that closes the underlying conn when ctx is cancelled — on a real *websocket.Conn
	// this unblocks the in-flight read within milliseconds instead of waiting for the
	// read deadline. stop is closed on normal exit so the watcher does not leak.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.conn.Close()
		case <-stop:
		}
	}()

	for {
		// Cheap, non-blocking cancellation check. Catches ctx cancellation for
		// transports whose ReadMessage returns immediately (e.g. test fakes that do
		// not honor Close). For real blocking reads, the watcher above handles it.
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Blocking read from WebSocket (returns when connection closes or error occurs)
		msgType, data, err := c.conn.ReadMessage()
		if err != nil {
			// Connection closed or read error - exit goroutine normally
			// Hub.Unregister will be called by defer
			return
		}

		// WR-08: enforce the binary-message invariant. Our custom frame protocol runs
		// over BinaryMessage; TextMessage is rejected at the boundary rather than fed
		// to DecodeFrame (which would only fail CRC and be silently dropped).
		if msgType != websocket.BinaryMessage {
			c.logger.Warn("non-binary ws message ignored",
				zap.Int("msg_type", msgType))
			continue
		}

		// Decode frame using Plan 01 protocol
		header, payload, err := DecodeFrame(data)
		if err != nil {
			// Skip malformed frames but don't disconnect (avoid single-frame踢连接)
			// Malicious frames can't kick off legitimate connections
			c.logger.Warn("frame decode failed, skipping", zap.Error(err))
			continue
		}

		// Route decoded frame to handler
		if err := c.handler.Route(ctx, c, header, payload); err != nil {
			// Handler errors are logged but don't terminate the connection
			c.logger.Warn("handler route failed", zap.Error(err))
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
				// Channel closed by Hub (via CloseSend/Unregister) - exit goroutine
				return
			}

			// CR-06: set a write deadline so a slow/frozen client (tiny TCP receive
			// window, never ACKs) cannot pin this goroutine indefinitely under the
			// 150MB budget. A timed-out write exits the pump and reclaims resources.
			_ = c.conn.SetWriteDeadline(time.Now().Add(defaultWriteTimeout))

			// Serial write to WebSocket (only one goroutine writes to conn)
			if err := c.conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
				// WR-04: surface write-failure exits so a slow/dropped client is
				// diagnosable in production logs instead of looking like a clean
				// shutdown. The CR-06 write-deadline expiry is the expected
				// slow-client-kill mechanism — logging it here is the only signal
				// of that path firing.
				c.logger.Info("writePump exit: write failed",
					zap.Error(err),
					zap.Bool("timeout", errors.Is(err, os.ErrDeadlineExceeded)))
				return
			}

		case <-ctx.Done():
			// D-12: Context cancelled - exit goroutine gracefully
			return
		}
	}
}

// Send queues a message for asynchronous write to the client. It is the only
// sanctioned way for handlers to push data (e.g. audio/TTS frames) back to the
// client (CR-04) — writePump remains the single writer to the underlying conn.
//
// Non-blocking and safe to call concurrently:
//   - ErrSendBufferFull: the bounded send channel is full; shed load (drop-oldest, D-73).
//   - ErrClientClosing:  the client is shutting down and the send channel is closed.
//
// The sendClosed flag + RWMutex make the check-then-send atomic with CloseSend, so
// Send never panics on a closed channel even if Hub.Unregister races it.
func (c *Client) Send(msg []byte) error {
	c.sendMu.RLock()
	defer c.sendMu.RUnlock()
	if c.sendClosed {
		return ErrClientClosing
	}
	select {
	case c.send <- msg:
		return nil
	default:
		return ErrSendBufferFull
	}
}

// CloseSend closes the send channel, signaling writePump to exit. It is the
// close-safe counterpart to Send and is called by Hub on Unregister. Idempotent.
func (c *Client) CloseSend() {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.sendClosed {
		return
	}
	c.sendClosed = true
	close(c.send)
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