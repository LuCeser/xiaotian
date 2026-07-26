package ws

import (
	"context"
	"net/http"
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
	AllowedOrigins  []string // Origin whitelist (D-24, forbidden "*" or empty)
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
			EnableCompression: false, // D-02: Safari compatibility (CLAUDE.md target platform constraint overrides literal "compression enabled")
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					// CR-05: browsers always send Origin on cross-origin WebSocket
					// requests, so an empty Origin means a non-browser client (curl,
					// urllib, an attacker's script). Reject it rather than weaken the
					// browser path into a near-return-true (D-24 forbids '*' / return-true).
					return false
				}
				// Check against whitelist
				return allowedOrigins[origin]
			},
			HandshakeTimeout: cfg.HandshakeTimeout,
		},
	}

	return server, nil
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
