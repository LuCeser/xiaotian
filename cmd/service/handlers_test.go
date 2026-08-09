package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
	"go.uber.org/zap"
)

// TestSendControlFrame_MonotonicSeq verifies D-12: consecutive sendControlFrame
// calls emit strictly increasing Sequence values.
//
// sendControlFrame takes a concrete *ws.Client whose Send() drops into an
// internal buffered channel drained by the unexported writePump, so a
// mock/stub client cannot observe the sent bytes. The test therefore drives a
// real connection: a router handler stashes the server-side *ws.Client on the
// first inbound control frame, then the test calls sendControlFrame three
// times directly and decodes the frames received on the client side.
//
// The package counter is NOT reset here: controlFrameSeq is process-global
// (research A4) and other cmd/service tests may have advanced it. The contract
// under test is strict monotonic increase, not specific starting values.
func TestSendControlFrame_MonotonicSeq(t *testing.T) {
	logger := zap.NewNop()

	hub, err := ws.NewHub(ws.HubConfig{Limit: 10, RegisterBuffer: 32, UnregisterBuffer: 32}, logger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)

	// serverClient receives the server-side *ws.Client of the first connection.
	// The router handler stashes it so the test can call sendControlFrame
	// directly (the same shape ws.Handler expects in production wiring).
	serverClient := make(chan *ws.Client, 1)
	handlers := map[ws.MessageType]ws.Handler{
		ws.MsgTypeControl: func(_ context.Context, c *ws.Client, _ *ws.FrameHeader, _ []byte) error {
			select {
			case serverClient <- c:
			default:
			}
			return nil
		},
	}

	router, err := ws.NewRouter(handlers, ws.RouterConfig{RouteBuffer: 32, WorkerConcurrency: 4}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	server, err := ws.NewServer(hub, router, ws.ServerConfig{
		AllowedOrigins:  []string{"localhost"},
		MaxMessageBytes: 65536,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	testServer := httptest.NewServer(http.HandlerFunc(server.HandleWebSocket))
	defer testServer.Close()
	wsURL := "ws://" + strings.TrimPrefix(testServer.URL, "http://") + "/ws"

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	headers := http.Header{}
	headers.Set("Origin", "http://localhost")
	conn, _, err := websocket.DefaultDialer.DialContext(dialCtx, wsURL, headers)
	if err != nil {
		t.Fatalf("WebSocket upgrade failed: %v", err)
	}
	defer conn.Close()

	// Probe: send one inbound control frame so the router handler stashes the
	// server-side *ws.Client. The probe itself produces no reply.
	probe := ws.EncodeFrame(&ws.FrameHeader{
		Version:  ws.ProtocolVersion,
		MsgType:  ws.MsgTypeControl,
		Sequence: 1,
	}, []byte(`{"type":"test"}`))
	if err := conn.WriteMessage(websocket.BinaryMessage, probe); err != nil {
		t.Fatalf("failed to write probe frame: %v", err)
	}

	var client *ws.Client
	select {
	case client = <-serverClient:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server-side client capture")
	}

	// Three direct sendControlFrame calls with a trivial control payload (D-12).
	for i := 0; i < 3; i++ {
		if err := sendControlFrame(client, ws.MsgTypeControl, map[string]string{"type": "test"}); err != nil {
			t.Fatalf("sendControlFrame call %d failed: %v", i+1, err)
		}
	}

	// Decode the three received frames and assert strict sequence increase.
	seqs := make([]uint32, 3)
	for i := 0; i < 3; i++ {
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("failed to set read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("failed to read reply frame %d: %v", i+1, err)
		}
		header, _, err := ws.DecodeFrame(raw)
		if err != nil {
			t.Fatalf("failed to decode reply frame %d: %v", i+1, err)
		}
		if header.Version != ws.ProtocolVersion {
			t.Errorf("reply frame %d: Version = %d, want ws.ProtocolVersion (%d)", i+1, header.Version, ws.ProtocolVersion)
		}
		seqs[i] = header.Sequence
	}

	if !(seqs[1] > seqs[0]) {
		t.Errorf("D-12 violated: second sequence (%d) must be > first (%d)", seqs[1], seqs[0])
	}
	if !(seqs[2] > seqs[1]) {
		t.Errorf("D-12 violated: third sequence (%d) must be > second (%d)", seqs[2], seqs[1])
	}
}
