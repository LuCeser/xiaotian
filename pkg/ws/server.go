package ws

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// DefaultMaxMessageBytes is the default WebSocket read limit. It allows the largest
// frame the wire protocol can express (16-byte header + uint16 max payload = 65551),
// keeping the enforced limit in sync with the protocol contract (WR-05).
const DefaultMaxMessageBytes = int64(HeaderSize + 0xFFFF)

// ServerConfig defines server configuration (D-44 centralized validation)
type ServerConfig struct {
	// AllowedOrigins is the Origin whitelist (D-24, forbidden "*" or empty).
	// Entries are matched against the browser's Origin header in two passes:
	// first an exact match (scheme+host+port), then a port-tolerant fallback
	// that strips the port. So "localhost" matches both "http://localhost" and
	// "http://localhost:8080" — a single entry covers any local dev port and
	// LAN iPad testing without per-port churn. For strict port isolation,
	// include the port explicitly (e.g. "tutor.example.com:8443"). Security
	// stays host-whitelist-bound: the port-less origin must still be listed,
	// and D-24's wildcard ban holds (IN-03).
	AllowedOrigins []string
	MaxMessageBytes int64    // Default 65551 (HeaderSize + max uint16 payload; IN-02)
	ReadBufferSize  int      // Default 4096
	WriteBufferSize int      // Default 4096
	HandshakeTimeout time.Duration // Default 10s
	HeartbeatInterval time.Duration // Default DefaultHeartbeatInterval (Plan 02)
	ClientCfg       ClientConfig // Passed to NewClient
}

// Validate checks if the configuration is valid
func (c ServerConfig) Validate() error {
	if len(c.AllowedOrigins) == 0 {
		return errors.Wrap(ErrInvalidServerConfig, "allowed origins required")
	}
	for _, origin := range c.AllowedOrigins {
		if origin == "*" {
			return errors.Wrap(ErrInvalidServerConfig, "wildcard origin forbidden")
		}
	}
	if c.MaxMessageBytes <= 0 {
		return errors.Wrap(ErrInvalidServerConfig, "max message bytes must be > 0")
	}
	// WR-07: NewServer only treats == 0 as "use default"; a negative value passes
	// through unchanged and silently breaks behaviour (e.g. HeartbeatInterval = -1
	// computes readTimeout = -2, falling into the <= 0 fixed-60s fallback while the
	// operator believes they configured heartbeat). Reject negatives explicitly.
	if c.HeartbeatInterval < 0 {
		return errors.Wrap(ErrInvalidServerConfig, "heartbeat interval must be >= 0")
	}
	if c.ReadBufferSize < 0 || c.WriteBufferSize < 0 {
		return errors.Wrap(ErrInvalidServerConfig, "buffer sizes must be >= 0")
	}
	if c.HandshakeTimeout < 0 {
		return errors.Wrap(ErrInvalidServerConfig, "handshake timeout must be >= 0")
	}
	return nil
}

// Pre-defined error types
var (
	ErrInvalidServerConfig = errors.New("invalid server config")
)

// Server handles WebSocket HTTP upgrades
type Server struct {
	upgrader websocket.Upgrader
	hub      *Hub
	handler  FrameHandler
	cfg      ServerConfig
	logger   *zap.Logger
}

// NewServer creates a new server instance (D-42 + D-44)
func NewServer(hub *Hub, handler FrameHandler, cfg ServerConfig, logger *zap.Logger) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "server config validation failed")
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	// Set defaults
	if cfg.ReadBufferSize == 0 {
		cfg.ReadBufferSize = 4096
	}
	if cfg.WriteBufferSize == 0 {
		cfg.WriteBufferSize = 4096
	}
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = DefaultHeartbeatInterval // From Plan 02 (would be defined elsewhere)
	}
	if cfg.MaxMessageBytes == 0 {
		cfg.MaxMessageBytes = DefaultMaxMessageBytes // 65551: header + uint16 max payload (WR-05)
	}

	// Set ClientCfg defaults
	if cfg.ClientCfg.SendBuffer == 0 {
		cfg.ClientCfg.SendBuffer = 16 // Default send buffer size
	}

	// Build origin whitelist for fast lookup
	allowedOrigins := make(map[string]bool)
	for _, origin := range cfg.AllowedOrigins {
		// Add both with and without scheme for flexibility
		allowedOrigins[origin] = true
		// If origin doesn't have scheme, add common schemes
		if !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://") {
			allowedOrigins["http://"+origin] = true
			allowedOrigins["https://"+origin] = true
		}
	}

	server := &Server{
		hub:     hub,
		handler: handler,
		cfg:     cfg,
		logger:  logger,
		upgrader: websocket.Upgrader{
			ReadBufferSize:    cfg.ReadBufferSize,
			WriteBufferSize:   cfg.WriteBufferSize,
			EnableCompression: false, // D-02/D-09: Safari compatibility — with permessage-deflate, Safari 17.6 throws "Protocol error" when a TEXT control frame is followed by a BINARY audio frame (Apple Developer Forum 744018, uWebSockets#1347)
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					// CR-05: browsers always send Origin on cross-origin WebSocket
					// requests, so an empty Origin means a non-browser client (curl,
					// urllib, an attacker's script). Reject it rather than weaken the
					// browser path into a near-return-true (D-24 forbids '*' / return-true).
					return false
				}
				// Exact whitelist match first (scheme+host+port per D-24).
				if allowedOrigins[origin] {
					return true
				}
				// Port-tolerant fallback: browsers append the port to the Origin
				// header on non-default ports (e.g. http://localhost:8080), but
				// whitelist entries conventionally omit it (localhost). Strip the
				// port and retry so a single "localhost" entry covers any local dev
				// port and LAN iPad testing without per-port config churn. Security
				// is preserved: the port-less origin MUST still be whitelisted —
				// this is not a return-true, and D-24's "*" / wildcard ban holds.
				if stripped := stripOriginPort(origin); stripped != origin && allowedOrigins[stripped] {
					return true
				}
				return false
			},
			HandshakeTimeout: cfg.HandshakeTimeout,
		},
	}

	return server, nil
}

// stripOriginPort removes the port from an Origin header value, preserving the
// scheme and host. It is used by CheckOrigin's port-tolerant fallback. If the
// origin has no port or cannot be parsed, it is returned unchanged.
//
// Examples:
//
//	"http://localhost:8080"  → "http://localhost"
//	"https://10.0.0.1:443"   → "https://10.0.0.1"
//	"http://localhost"        → "http://localhost"  (unchanged)
//	"null"                    → "null"             (unchanged)
func stripOriginPort(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return origin
	}
	if u.Port() == "" {
		return origin
	}
	u.Host = u.Hostname()
	return u.String()
}

// HandleWebSocket handles HTTP → WebSocket upgrade (D-01, WS-01)
func (s *Server) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Attempt WebSocket upgrade
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Warn("ws upgrade failed", zap.Error(err))
		return // gorilla has already written error response
	}

	// Set read limit (T-02-12 oversized DoS protection)
	conn.SetReadLimit(s.cfg.MaxMessageBytes)

	// CR-02/CR-03: idle-timeout + heartbeat. SetReadDeadline sets an absolute time,
	// not an idle timeout — setting it once at upgrade kills every connection at a
	// fixed age. Refresh it on each pong instead: Heartbeat (launched below) sends
	// PINGs every HeartbeatInterval, a healthy client's pong rolls the deadline
	// forward, and two consecutive missed pongs mark the connection dead.
	readTimeout := s.cfg.HeartbeatInterval * 2
	if readTimeout <= 0 {
		readTimeout = 60 * time.Second
	}
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})

	// Extract handshake parameters (D-12)
	userID := r.URL.Query().Get("user_id")
	device := r.URL.Query().Get("device")

	// Create client
	client, err := NewClient(conn, s.hub, s.handler, s.cfg.ClientCfg, s.logger.With(
		zap.String("user_id", userID),
		zap.String("device", device),
	))
	if err != nil {
		s.logger.Error("new client failed", zap.Error(err))
		conn.Close()
		return
	}

	// Register with hub (will be processed asynchronously). CR-01: Register is
	// non-blocking; if the hub is shutting down or saturated it returns false and
	// we reject the connection here rather than blocking the HTTP handler
	// goroutine — which previously pinned a goroutine forever once Hub.Run had
	// stopped draining during shutdown.
	if !s.hub.Register(client) {
		s.logger.Warn("hub rejected registration: hub shutting down or saturated")
		_ = client.Close(websocket.CloseTryAgainLater, "server full")
		return
	}

	// Start client pumps with connection lifecycle
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Create a channel to track connection completion
	done := make(chan struct{})

	// Start pumps in goroutines
	go func() {
		client.readPump(ctx)
		close(done) // Signal that readPump has exited
	}()

	go client.writePump(ctx)

	// CR-03: keep the connection alive with PING/PONG (mitigates Safari background-tab
	// silent drops, RESEARCH.md Pitfall 2, and refreshes the read deadline above).
	// gorilla/websocket permits one concurrent WriteMessage writer (writePump) plus any
	// number of WriteControl callers, so running Heartbeat concurrently is safe (WR-06).
	if s.cfg.HeartbeatInterval > 0 {
		go Heartbeat(ctx, conn, s.cfg.HeartbeatInterval, s.logger)
	}

	// Wait for connection to close (blocking until readPump exits)
	<-done

	// Cleanup
	conn.Close()
	s.logger.Debug("connection handler exiting")
}

// Shutdown gracefully tears down the WebSocket subsystem (WR-09).
//
// There was previously no single entry point for shutdown; the caller had to
// know the tribal ordering (HTTP server -> Hub -> Router) to avoid the CR-01
// goroutine leak. Getting it wrong — which TestIntegration_ServerShutdownCleansAllGoroutines
// itself did (hubCancel before testServer.Close) — triggers the leak.
//
// Required caller ordering to avoid the CR-01 leak:
//  1. http.Server.Shutdown(ctx) — stop accepting and wait for in-flight
//     HandleWebSocket goroutines. MUST happen first, so no new
//     HandleWebSocket goroutines start while the Hub is being torn down.
//  2. Server.Shutdown(ctx) — this method: closes the Hub (short-circuits any
//     straggler Register/Unregister so their goroutines exit without blocking)
//     and then closes the Router (stops the worker pool with a bounded wait,
//     WR-02).
//
// Idempotent. The ctx argument is reserved for a future bounded-wait / nested
// http.Server.Shutdown when Server grows an *http.Server field; today the Router
// bounds its own wait (WR-02).
func (s *Server) Shutdown(ctx context.Context) error {
	_ = ctx // reserved for future bounded shutdown
	// Hub.Close flips the closed flag so any in-flight or future HandleWebSocket
	// goroutines exit without blocking on the hub channels (CR-01).
	s.hub.Close()
	// Close the router/handler if it exposes Close() error. *Router does (WR-02);
	// the type assertion keeps Shutdown decoupled from the concrete Router type.
	if closer, ok := s.handler.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
