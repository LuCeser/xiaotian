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

// ServerConfig defines server configuration (D-44 centralized validation)
type ServerConfig struct {
	AllowedOrigins  []string // Origin whitelist (D-24, forbidden "*" or empty)
	MaxMessageBytes int64    // Default 65536 (64KB, RESEARCH.md Anti-Pattern)
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
		cfg.MaxMessageBytes = 65536 // Default 64KB
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
					return true // Allow requests with no Origin header
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

	// Set initial read deadline
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))

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

	// Register with hub (will be processed asynchronously)
	s.hub.Register(client)

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

	// Wait for connection to close (blocking until readPump exits)
	<-done

	// Cleanup
	conn.Close()
	s.logger.Debug("connection handler exiting")
}
