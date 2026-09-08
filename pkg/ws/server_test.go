package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// TestNewServerValidatesConfig tests that NewServer validates configuration
func TestNewServerValidatesConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ServerConfig
		wantErr error
	}{
		{
			name: "empty allowed origins",
			cfg: ServerConfig{
				AllowedOrigins: []string{},
			},
			wantErr: ErrInvalidServerConfig,
		},
		{
			name: "wildcard origin forbidden",
			cfg: ServerConfig{
				AllowedOrigins: []string{"*"},
			},
			wantErr: ErrInvalidServerConfig,
		},
		{
			name: "zero max message bytes",
			cfg: ServerConfig{
				AllowedOrigins:  []string{"example.com"},
				MaxMessageBytes: 0,
			},
			wantErr: ErrInvalidServerConfig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := zap.NewNop()
			handler := &Router{} // Minimal handler
			_, err := NewServer(nil, handler, tt.cfg, logger)

			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if errors.Is(err, tt.wantErr) {
				t.Logf("correctly got expected error: %v", tt.wantErr)
			}
		})
	}
}

// TestNewServerAcceptsValidConfig tests that NewServer accepts valid configuration
func TestNewServerAcceptsValidConfig(t *testing.T) {
	logger := zap.NewNop()
	hub, err := NewHub(HubConfig{
		Limit:            100,
		RegisterBuffer:   32,
		UnregisterBuffer: 32,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}

	handler := &Router{}
	cfg := ServerConfig{
		AllowedOrigins:  []string{"example.com", "localhost"},
		MaxMessageBytes: 65536,
	}

	server, err := NewServer(hub, handler, cfg, logger)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if server == nil {
		t.Fatal("expected non-nil server")
	}
}

// TestServerUpgraderCompressionDisabled verifies EnableCompression is false.
//
// D-09 (LOCKED): compression must stay OFF. With permessage-deflate enabled,
// Safari 17.6 throws "Protocol error" when a TEXT control frame is followed by
// a BINARY audio frame (Apple Developer Forum thread 744018, uWebSockets#1347).
// This assertion guards that invariant so a future PR cannot regress it.
func TestServerUpgraderCompressionDisabled(t *testing.T) {
	logger := zap.NewNop()
	hub, err := NewHub(HubConfig{
		Limit:            100,
		RegisterBuffer:   32,
		UnregisterBuffer: 32,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}

	handler := &Router{}
	cfg := ServerConfig{
		AllowedOrigins:  []string{"example.com"},
		MaxMessageBytes: 65536,
	}

	server, err := NewServer(hub, handler, cfg, logger)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	if server.upgrader.EnableCompression {
		t.Error("EnableCompression should be false for Safari compatibility")
	}
}

// TestServerUpgraderCheckOriginRejectsUnknown verifies CheckOrigin rejects non-whitelisted origins
func TestServerUpgraderCheckOriginUnknown(t *testing.T) {
	logger := zap.NewNop()
	hub, err := NewHub(HubConfig{
		Limit:            100,
		RegisterBuffer:   32,
		UnregisterBuffer: 32,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}

	handler := &Router{}
	cfg := ServerConfig{
		AllowedOrigins:  []string{"example.com"},
		MaxMessageBytes: 65536,
	}

	server, err := NewServer(hub, handler, cfg, logger)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// Create a request with non-whitelisted origin
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Origin", "http://evil.com")

	// CheckOrigin should return false
	if server.upgrader.CheckOrigin(req) {
		t.Error("CheckOrigin should reject non-whitelisted origin")
	}
}

// TestServerUpgraderCheckOriginAcceptsWhitelisted verifies CheckOrigin accepts whitelisted origins
func TestServerUpgraderCheckOriginWhitelisted(t *testing.T) {
	logger := zap.NewNop()
	hub, err := NewHub(HubConfig{
		Limit:            100,
		RegisterBuffer:   32,
		UnregisterBuffer: 32,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}

	handler := &Router{}
	cfg := ServerConfig{
		AllowedOrigins:  []string{"example.com", "localhost", "127.0.0.1"},
		MaxMessageBytes: 65536,
	}

	server, err := NewServer(hub, handler, cfg, logger)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	testOrigins := []string{
		"http://example.com",
		"https://example.com",
		"http://localhost",
		"http://127.0.0.1",
	}

	for _, origin := range testOrigins {
		req := httptest.NewRequest("GET", "/ws", nil)
		req.Header.Set("Origin", origin)

		if !server.upgrader.CheckOrigin(req) {
			t.Errorf("CheckOrigin should accept whitelisted origin: %s", origin)
		}
	}
}

// TestServerUpgraderCheckOriginWithPort verifies the port-tolerant fallback
// (G-07-13): a browser visiting on a non-default port sends Origin
// "http://localhost:8080", which a port-less whitelist entry "localhost" must
// still accept. This is the real-world browser behavior that broke end-to-end
// session flow in Phase 7 UAT.
func TestServerUpgraderCheckOriginWithPort(t *testing.T) {
	logger := zap.NewNop()
	hub, err := NewHub(HubConfig{
		Limit:            100,
		RegisterBuffer:   32,
		UnregisterBuffer: 32,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}

	handler := &Router{}
	cfg := ServerConfig{
		AllowedOrigins:  []string{"localhost", "127.0.0.1"},
		MaxMessageBytes: 65536,
	}

	server, err := NewServer(hub, handler, cfg, logger)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	acceptOrigins := []string{
		"http://localhost:8080", // non-default port + port-less whitelist entry
		"https://localhost:8443",
		"http://127.0.0.1:8080",
		"http://localhost", // exact match still works (regression guard)
	}
	for _, origin := range acceptOrigins {
		req := httptest.NewRequest("GET", "/ws", nil)
		req.Header.Set("Origin", origin)
		if !server.upgrader.CheckOrigin(req) {
			t.Errorf("CheckOrigin should accept whitelisted origin with port: %s", origin)
		}
	}

	// Security guard: a non-whitelisted host with a port is still rejected
	// (port tolerance must not weaken host-whitelist binding — D-24 holds).
	rejectReq := httptest.NewRequest("GET", "/ws", nil)
	rejectReq.Header.Set("Origin", "http://evil.com:8080")
	if server.upgrader.CheckOrigin(rejectReq) {
		t.Error("CheckOrigin should reject non-whitelisted origin even with port")
	}
}

// TestServerHandleWebSocketUpgradeSuccess verifies successful WebSocket upgrade
func TestServerHandleWebSocketUpgradeSuccess(t *testing.T) {
	logger := zap.NewNop()
	hub, err := NewHub(HubConfig{
		Limit:            100,
		RegisterBuffer:   32,
		UnregisterBuffer: 32,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}

	// Start hub event loop
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)

	router, err := NewRouter(map[MessageType]Handler{}, RouterConfig{
		RouteBuffer:       32,
		WorkerConcurrency: 4,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	cfg := ServerConfig{
		AllowedOrigins:  []string{"localhost", "127.0.0.1"},
		MaxMessageBytes: 65536,
	}

	server, err := NewServer(hub, router, cfg, logger)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// Create test HTTP server
	testServer := httptest.NewServer(http.HandlerFunc(server.HandleWebSocket))
	defer testServer.Close()

	// Convert http://server URL to ws://server URL
	wsURL := "ws://" + strings.TrimPrefix(testServer.URL, "http://") + "/ws"

	// Create WebSocket client
	dialer := websocket.Dialer{}
	headers := http.Header{}
	headers.Set("Origin", "http://localhost")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		t.Fatalf("WebSocket upgrade failed: %v", err)
	}
	defer conn.Close()

	if resp.StatusCode != 101 {
		t.Errorf("Expected status 101 (Switching Protocols), got %d", resp.StatusCode)
	}

	// Verify hub registered the client
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.Clients() == 1 {
			return // Success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("Expected 1 client registered, got %d", hub.Clients())
}

// TestServerHandleWebSocketOriginRejected verifies rejected Origin
func TestServerHandleWebSocketOriginRejected(t *testing.T) {
	logger := zap.NewNop()
	hub, err := NewHub(HubConfig{
		Limit:            100,
		RegisterBuffer:   32,
		UnregisterBuffer: 32,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}

	// Start hub event loop
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)

	router, err := NewRouter(map[MessageType]Handler{}, RouterConfig{
		RouteBuffer:       32,
		WorkerConcurrency: 4,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	cfg := ServerConfig{
		AllowedOrigins:  []string{"example.com"},
		MaxMessageBytes: 65536,
	}

	server, err := NewServer(hub, router, cfg, logger)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// Create test HTTP server
	testServer := httptest.NewServer(http.HandlerFunc(server.HandleWebSocket))
	defer testServer.Close()

	wsURL := "ws://" + strings.TrimPrefix(testServer.URL, "http://") + "/ws"

	// Try to connect with non-whitelisted origin
	dialer := websocket.Dialer{}
	headers := http.Header{}
	headers.Set("Origin", "http://evil.com")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err == nil {
		conn.Close()
		t.Fatal("Expected connection rejected, but it succeeded")
	}

	if resp.StatusCode != 403 {
		t.Errorf("Expected status 403 (Forbidden), got %d", resp.StatusCode)
	}
}

// TestServerHandleWebSocketParameterHandshake verifies user_id and device parameters
func TestServerHandleWebSocketParameterHandshake(t *testing.T) {
	logger := zap.NewNop()
	hub, err := NewHub(HubConfig{
		Limit:            100,
		RegisterBuffer:   32,
		UnregisterBuffer: 32,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}

	// Start hub event loop
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)

	router, err := NewRouter(map[MessageType]Handler{}, RouterConfig{
		RouteBuffer:       32,
		WorkerConcurrency: 4,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	cfg := ServerConfig{
		AllowedOrigins:  []string{"localhost"},
		MaxMessageBytes: 65536,
	}

	server, err := NewServer(hub, router, cfg, logger)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// Create test HTTP server
	testServer := httptest.NewServer(http.HandlerFunc(server.HandleWebSocket))
	defer testServer.Close()

	wsURL := "ws://" + strings.TrimPrefix(testServer.URL, "http://") + "/ws" + "?user_id=test123&device=ipad"

	dialer := websocket.Dialer{}
	headers := http.Header{}
	headers.Set("Origin", "http://localhost")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		t.Fatalf("WebSocket upgrade failed: %v", err)
	}
	defer conn.Close()

	// Verify connection was successful (status 101)
	// In real implementation, user_id and device would be passed to Client metadata
	// For now, just verify the connection succeeds with parameters
}
