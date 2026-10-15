// Package wstest provides testing utilities for WebSocket functionality.
package wstest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// MustBuildTestFrame builds a complete binary frame for tests.
// Panics on invalid input — keeps test bodies concise.
// The frame includes a 16-byte header with version, message type, sequence,
// timestamp, and auto-computed CRC-16 checksum.
func MustBuildTestFrame(t *testing.T, msgType ws.MessageType, seq uint32, payload []byte) []byte {
	t.Helper()

	h := &ws.FrameHeader{
		Version:   ws.ProtocolVersion,
		MsgType:   msgType,
		Sequence:  seq,
		Timestamp: uint32(time.Now().UnixMicro()), //nolint:gosec // G115: protocol timestamp keeps the low 32 bits of Unix microseconds
	}

	return ws.EncodeFrame(h, payload)
}

// TestAudioPayload returns a deterministic 640-byte PCM audio fixture.
// The payload consists of sequential byte values (0, 1, 2, ..., 255, 0, 1, ...)
// which provides predictable test data for audio frame testing.
// This matches the 20ms@16kHz PCM audio format (D-09).
func TestAudioPayload() []byte {
	buf := make([]byte, ws.AudioFrameSize)
	for i := 0; i < ws.AudioFrameSize; i++ {
		buf[i] = byte(i)
	}
	return buf
}

// MustDialTestServer establishes a WebSocket connection to a test HTTP server.
// The provided handler should perform WebSocket upgrade using websocket.Upgrader.
// Returns the connection and a cleanup function that closes both the connection
// and the test server. Panics on connection failure — keeps test bodies concise.
//
// Usage:
//
//	upgrader := websocket.Upgrader{}
//	handler := func(w http.ResponseWriter, r *http.Request) {
//	    conn, err := upgrader.Upgrade(w, r, nil)
//	    if err != nil { return }
//	    defer conn.Close()
//	    // Handle connection...
//	}
//	conn, cleanup := MustDialTestServer(t, handler)
//	defer cleanup()
//	// Use conn for WebSocket operations
func MustDialTestServer(t *testing.T, handler http.HandlerFunc) (*websocket.Conn, func()) {
	t.Helper()

	server := httptest.NewServer(handler)
	wsURL := "ws://" + server.Listener.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	// gorilla/websocket uses websocket.DefaultDialer or websocket.Dialer{} struct
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		cancel()
		server.Close()
		t.Fatalf("MustDialTestServer: failed to dial %s: %v", wsURL, err)
	}

	cleanup := func() {
		_ = conn.Close()
		cancel()
		server.Close()
	}

	return conn, cleanup
}
