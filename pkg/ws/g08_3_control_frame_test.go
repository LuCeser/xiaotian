package ws_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	ws "github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// TestG08_3_ControlFrameBinaryAccepted_TextDropped is the regression guard for
// G-08-3. The student interface previously sent JSON as WebSocket TEXT frames,
// which the server silently dropped at the read boundary
// ("non-binary ws message ignored") — the entire student session flow was dead.
// The fix makes the frontend send Phase-2 binary frames.
//
// This test pins the contract in both directions:
//  1. a TEXT frame carrying the start_session JSON MUST be dropped at the ws
//     boundary and never reach the control handler (the bug);
//  2. the SAME JSON wrapped in a Phase-2 binary control frame MUST reach the
//     control handler with its payload decoded intact.
//
// If this test ever fails, the frontend↔backend wire contract has drifted and
// the student session flow is broken again.
func TestG08_3_ControlFrameBinaryAccepted_TextDropped(t *testing.T) {
	hubCfg := ws.HubConfig{Limit: 10, RegisterBuffer: 32, UnregisterBuffer: 32}
	routerCfg := ws.RouterConfig{RouteBuffer: 8, WorkerConcurrency: 1}
	serverCfg := ws.ServerConfig{AllowedOrigins: []string{"localhost", "127.0.0.1"}, MaxMessageBytes: 65536}

	logger := zap.NewNop()

	var (
		mu          sync.Mutex
		gotPayload  []byte
		handlerDone = make(chan struct{}, 2)
	)

	// Recording control handler: captures the decoded payload and signals done.
	handlers := map[ws.MessageType]ws.Handler{
		ws.MsgTypeControl: func(_ context.Context, _ *ws.Client, _ *ws.FrameHeader, payload []byte) error {
			mu.Lock()
			gotPayload = append(gotPayload[:0], payload...)
			mu.Unlock()
			select {
			case handlerDone <- struct{}{}:
			default:
			}
			return nil
		},
	}

	hub, err := ws.NewHub(hubCfg, logger)
	if err != nil {
		t.Fatalf("new hub: %v", err)
	}
	router, err := ws.NewRouter(handlers, routerCfg, logger)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	server, err := ws.NewServer(hub, router, serverCfg, logger)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	hubCtx, hubCancel := context.WithCancel(context.Background())
	go hub.Run(hubCtx)
	defer func() {
		hubCancel()
		router.Close()
		time.Sleep(50 * time.Millisecond)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", server.HandleWebSocket)
	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	wsURL := "ws://" + testServer.Listener.Addr().String() + "/ws"
	dialer := websocket.Dialer{}
	headers := http.Header{}
	headers.Set("Origin", "http://localhost")

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	conn, resp, err := dialer.DialContext(dialCtx, wsURL, headers)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if resp.StatusCode != 101 {
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}
	time.Sleep(100 * time.Millisecond) // let hub register the client

	// The start_session payload, identical in both the buggy and fixed clients.
	startJSON, _ := json.Marshal(map[string]interface{}{
		"type":   "start_session",
		"params": map[string]interface{}{"limit": 30},
	})

	// --- 1. Buggy path: send as a TEXT frame. Must be dropped, handler silent. ---
	if err := conn.WriteMessage(websocket.TextMessage, startJSON); err != nil {
		t.Fatalf("write text frame: %v", err)
	}
	select {
	case <-handlerDone:
		t.Fatal("G-08-3 REGRESSION: control handler fired for a TEXT frame — text frames must be dropped at the ws boundary")
	case <-time.After(250 * time.Millisecond):
		// expected: handler NOT called
	}

	// --- 2. Fixed path: send as a Phase-2 binary control frame. Must arrive. ---
	frame := ws.EncodeFrame(&ws.FrameHeader{
		Version: ws.ProtocolVersion,
		MsgType: ws.MsgTypeControl,
	}, startJSON)
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatalf("write binary frame: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("control handler not invoked for binary-framed start_session (timeout) — G-08-3 not fixed")
	}

	mu.Lock()
	payload := gotPayload
	mu.Unlock()

	var decoded struct {
		Type   string          `json:"type"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("handler payload not valid JSON: %v (raw=%q)", err, string(payload))
	}
	if decoded.Type != "start_session" {
		t.Fatalf("expected type=start_session, got %q (raw=%q)", decoded.Type, string(payload))
	}
	var params struct {
		Limit int `json:"limit"`
	}
	if err := json.Unmarshal(decoded.Params, &params); err != nil {
		t.Fatalf("invalid params: %v", err)
	}
	if params.Limit != 30 {
		t.Fatalf("expected limit=30, got %d", params.Limit)
	}

	t.Logf("G-08-3 contract held: text frame dropped, binary-framed start_session reached handler with payload %q", string(payload))
}
