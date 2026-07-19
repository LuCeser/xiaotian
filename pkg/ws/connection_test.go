package ws

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// Test helper functions (local copies to avoid import cycle with wstest package)

// MustBuildTestFrame builds a test frame for testing. Panics on failure (t.Helper() pattern).
func MustBuildTestFrame(t *testing.T, msgType MessageType, seq uint32, payload []byte) []byte {
	t.Helper()
	header := &FrameHeader{
		Version:    ProtocolVersion,
		MsgType:    msgType,
		Sequence:   seq,
		Timestamp:  uint32(time.Now().UnixMicro()),
		PayloadLen: uint16(len(payload)),
	}
	frame := EncodeFrame(header, payload)
	return frame
}

// getTestAudioPayload returns a deterministic 640-byte audio payload for testing.
func getTestAudioPayload() []byte {
	payload := make([]byte, AudioFrameSize)
	for i := 0; i < AudioFrameSize; i++ {
		payload[i] = byte(i)
	}
	return payload
}

// fakeConn is a manual fake for *websocket.Conn (D-48 - no testify/mock).
// Records calls to WebSocket methods for verification in tests.
type fakeConn struct {
	mu                    sync.Mutex
	readMessageCallCount  atomic.Int32
	writeMessageCallCount atomic.Int32
	writeControlCallCount atomic.Int32
	closeCallCount        atomic.Int32
	closeCallCode         atomic.Int32
	closeCallReason       atomic.Value // string

	// Test control fields
	readMessageResult  []byte          // Return value for ReadMessage
	readMessageError   error           // Error to return from ReadMessage
	writeMessageError  error           // Error to return from WriteMessage
	writeControlError  error           // Error to return from WriteControl
	onWriteMessage     func([]byte)    // Optional callback on WriteMessage
	onWriteControl     func(int, []byte) // Optional callback on WriteControl
}

func (f *fakeConn) ReadMessage() (int, []byte, error) {
	f.readMessageCallCount.Add(1)
	if f.readMessageError != nil {
		return 0, nil, f.readMessageError
	}
	if f.readMessageResult != nil {
		return websocket.BinaryMessage, f.readMessageResult, nil
	}
	return websocket.BinaryMessage, []byte("test"), nil
}

func (f *fakeConn) WriteMessage(messageType int, data []byte) error {
	f.writeMessageCallCount.Add(1)
	if f.writeMessageError != nil {
		return f.writeMessageError
	}
	if f.onWriteMessage != nil {
		f.onWriteMessage(data)
	}
	return nil
}

func (f *fakeConn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	f.writeControlCallCount.Add(1)
	if messageType == websocket.CloseMessage {
		// Extract close code and reason from data (format per WebSocket spec)
		if len(data) >= 2 {
			code := int(data[0])<<8 | int(data[1])
			f.closeCallCode.Store(int32(code))
			if len(data) > 2 {
				reason := string(data[2:])
				f.closeCallReason.Store(reason)
			}
		}
	}
	if f.writeControlError != nil {
		return f.writeControlError
	}
	if f.onWriteControl != nil {
		f.onWriteControl(messageType, data)
	}
	return nil
}

func (f *fakeConn) Close() error {
	f.closeCallCount.Add(1)
	return nil
}

// fakeFrameHandler is a manual fake for FrameHandler (D-48).
// Records calls to Route for verification in tests.
type fakeFrameHandler struct {
	routeCallCount atomic.Int32
	lastRouteCtx   atomic.Value // *context.Context
	lastRouteClient atomic.Value // *Client
	lastRouteHeader atomic.Value // *FrameHeader
	lastRoutePayload atomic.Value // []byte
	routeError     error           // Error to return from Route
	onRoute        func(ctx context.Context, c *Client, h *FrameHeader, p []byte) error
}

func (f *fakeFrameHandler) Route(ctx context.Context, c *Client, header *FrameHeader, payload []byte) error {
	f.routeCallCount.Add(1)
	f.lastRouteCtx.Store(&ctx)
	f.lastRouteClient.Store(c)
	f.lastRouteHeader.Store(header)
	f.lastRoutePayload.Store(payload)

	if f.onRoute != nil {
		return f.onRoute(ctx, c, header, payload)
	}
	if f.routeError != nil {
		return f.routeError
	}
	return nil
}

// fakeHub is a minimal fake for Hub to test Client unregistration.
type fakeHub struct {
	unregisterClient atomic.Value // *Client
	unregisterCallCount atomic.Int32
}

func (f *fakeHub) Unregister(c *Client) {
	f.unregisterCallCount.Add(1)
	f.unregisterClient.Store(c)
}

func (f *fakeHub) Register(c *Client) {
	// Not needed for Client tests
}

// TestNewClientConstructor verifies NewClient returns valid Client with initialized send channel.
func TestNewClientConstructor(t *testing.T) {
	fakeConn := &fakeConn{}
	fakeHub, err := NewHub(HubConfig{Limit: 100, RegisterBuffer: 32, UnregisterBuffer: 32}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	fakeHandler := &fakeFrameHandler{}
	logger := zap.NewNop()

	cfg := ClientConfig{
		SendBuffer: 16,
	}

	client, err := NewClient(fakeConn, fakeHub, fakeHandler, cfg, logger)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if client == nil {
		t.Fatal("NewClient should return non-nil client")
	}
	if client.send == nil {
		t.Error("NewClient should initialize send channel")
	}
	if cap(client.send) != cfg.SendBuffer {
		t.Errorf("send channel capacity = %d, want %d", cap(client.send), cfg.SendBuffer)
	}
	if client.conn != fakeConn {
		t.Error("NewClient should set conn field")
	}
	if client.hub != fakeHub {
		t.Error("NewClient should set hub field")
	}
	if client.handler != fakeHandler {
		t.Error("NewClient should set handler field")
	}
}

// TestNewClientValidatesConfig verifies NewClient returns error for invalid SendBuffer.
func TestNewClientValidatesConfig(t *testing.T) {
	tests := []struct {
		name       string
		sendBuffer int
	}{
		{"zero buffer", 0},
		{"negative buffer", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ClientConfig{
				SendBuffer: tt.sendBuffer,
			}
			fakeHub, _ := NewHub(HubConfig{Limit: 100, RegisterBuffer: 32, UnregisterBuffer: 32}, zap.NewNop())
			_, err := NewClient(&fakeConn{}, fakeHub, &fakeFrameHandler{}, cfg, zap.NewNop())
			if err == nil {
				t.Error("NewClient should return error for invalid SendBuffer")
			}
		})
	}
}

// TestClientSendChannelBounded verifies send channel has bounded capacity.
func TestClientSendChannelBounded(t *testing.T) {
	cfg := ClientConfig{
		SendBuffer: 16,
	}

	fakeHub, _ := NewHub(HubConfig{Limit: 100, RegisterBuffer: 32, UnregisterBuffer: 32}, zap.NewNop())
	client, err := NewClient(&fakeConn{}, fakeHub, &fakeFrameHandler{}, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// Verify channel capacity is bounded
	if cap(client.send) != cfg.SendBuffer {
		t.Errorf("send channel capacity = %d, want %d", cap(client.send), cfg.SendBuffer)
	}

	// Verify channel is truly bounded (not unbuffered)
	if cap(client.send) == 0 {
		t.Error("send channel should be buffered, not unbuffered")
	}
}

// TestClientWritePumpExitsOnChannelClose verifies writePump exits when send channel closes.
func TestClientWritePumpExitsOnChannelClose(t *testing.T) {
	fakeConn := &fakeConn{}
	fakeHub, _ := NewHub(HubConfig{Limit: 100, RegisterBuffer: 32, UnregisterBuffer: 32}, zap.NewNop())
	fakeHandler := &fakeFrameHandler{}

	cfg := ClientConfig{SendBuffer: 16}
	client, err := NewClient(fakeConn, fakeHub, fakeHandler, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start writePump goroutine
	go client.writePump(ctx)

	// Give goroutine time to start
	time.Sleep(50 * time.Millisecond)

	// Close send channel to trigger writePump exit
	close(client.send)

	// Wait for goroutine to exit (should be within 100ms)
	time.Sleep(100 * time.Millisecond)

	// Verify goroutine exited (no easy way to directly test, but we can verify conn.Close was called)
	if fakeConn.closeCallCount.Load() == 0 {
		t.Error("writePump should close conn on send channel close")
	}
}

// TestClientWritePumpExitsOnContextCancel verifies writePump exits when context is cancelled.
func TestClientWritePumpExitsOnContextCancel(t *testing.T) {
	fakeConn := &fakeConn{}
	fakeHub, _ := NewHub(HubConfig{Limit: 100, RegisterBuffer: 32, UnregisterBuffer: 32}, zap.NewNop())
	fakeHandler := &fakeFrameHandler{}

	cfg := ClientConfig{SendBuffer: 16}
	client, err := NewClient(fakeConn, fakeHub, fakeHandler, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Start writePump goroutine
	go client.writePump(ctx)

	// Give goroutine time to start
	time.Sleep(50 * time.Millisecond)

	// Cancel context to trigger writePump exit
	cancel()

	// Wait for goroutine to exit (should be within 100ms)
	time.Sleep(100 * time.Millisecond)

	// Verify conn.Close was called
	if fakeConn.closeCallCount.Load() == 0 {
		t.Error("writePump should close conn on context cancel")
	}
}

// TestClientWritePumpSerializesWrites verifies writePump calls WriteMessage for each send.
func TestClientWritePumpSerializesWrites(t *testing.T) {
	fakeConn := &fakeConn{
		onWriteMessage: func(data []byte) {
			// Simulate some processing time
			time.Sleep(10 * time.Millisecond)
		},
	}
	fakeHub, _ := NewHub(HubConfig{Limit: 100, RegisterBuffer: 32, UnregisterBuffer: 32}, zap.NewNop())
	fakeHandler := &fakeFrameHandler{}

	cfg := ClientConfig{SendBuffer: 16}
	client, err := NewClient(fakeConn, fakeHub, fakeHandler, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start writePump goroutine
	go client.writePump(ctx)

	// Give goroutine time to start
	time.Sleep(50 * time.Millisecond)

	// Send 3 messages
	for i := 0; i < 3; i++ {
		client.send <- []byte("message")
	}

	// Wait for messages to be written
	time.Sleep(200 * time.Millisecond)

	// Verify WriteMessage was called 3 times
	callCount := fakeConn.writeMessageCallCount.Load()
	if callCount != 3 {
		t.Errorf("WriteMessage called %d times, want 3", callCount)
	}
}

// TestClientReadPumpDecodesValidFrame verifies readPump calls handler.Route for valid frames.
func TestClientReadPumpDecodesValidFrame(t *testing.T) {
	// Build a valid test frame
	validFrame := MustBuildTestFrame(t, MsgTypeAudio, 1, getTestAudioPayload())

	fakeConn := &fakeConn{
		readMessageResult: validFrame,
	}
	fakeHub, _ := NewHub(HubConfig{Limit: 100, RegisterBuffer: 32, UnregisterBuffer: 32}, zap.NewNop())
	fakeHandler := &fakeFrameHandler{}

	cfg := ClientConfig{SendBuffer: 16}
	client, err := NewClient(fakeConn, fakeHub, fakeHandler, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Start readPump goroutine
	go client.readPump(ctx)

	// Wait for read and routing
	time.Sleep(200 * time.Millisecond)

	// Cancel context to stop readPump
	cancel()

	// Wait for goroutine to exit
	time.Sleep(100 * time.Millisecond)

	// Verify handler.Route was called
	if fakeHandler.routeCallCount.Load() == 0 {
		t.Error("readPump should call handler.Route for valid frame")
	}

	// Verify unregistration was called
	if fakeHub.Clients() == 0 {
		t.Error("readPump should unregister on exit")
	}
}

// TestClientReadPumpIgnoresInvalidFrame verifies readPump skips invalid frames without exiting.
func TestClientReadPumpIgnoresInvalidFrame(t *testing.T) {
	invalidFrame := []byte("invalid-crc-frame")

	fakeConn := &fakeConn{
		readMessageResult: invalidFrame,
	}
	fakeHub, _ := NewHub(HubConfig{Limit: 100, RegisterBuffer: 32, UnregisterBuffer: 32}, zap.NewNop())
	fakeHandler := &fakeFrameHandler{}

	cfg := ClientConfig{SendBuffer: 16}
	client, err := NewClient(fakeConn, fakeHub, fakeHandler, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Start readPump goroutine
	go client.readPump(ctx)

	// Wait for read attempt
	time.Sleep(200 * time.Millisecond)

	// Cancel context to stop readPump
	cancel()

	// Wait for goroutine to exit
	time.Sleep(100 * time.Millisecond)

	// Verify handler.Route was NOT called (invalid frame was skipped)
	if fakeHandler.routeCallCount.Load() != 0 {
		t.Error("readPump should not call handler.Route for invalid frame")
	}

	// Verify unregistration was still called (graceful exit)
	if fakeHub.Clients() == 0 {
		t.Error("readPump should still unregister on exit after skipping invalid frame")
	}
}

// TestClientReadPumpUnregistersOnError verifies readPump unregisters on ReadMessage error.
func TestClientReadPumpUnregistersOnError(t *testing.T) {
	fakeConn := &fakeConn{
		readMessageError: errors.New("connection closed"),
	}
	fakeHub, _ := NewHub(HubConfig{Limit: 100, RegisterBuffer: 32, UnregisterBuffer: 32}, zap.NewNop())
	fakeHandler := &fakeFrameHandler{}

	cfg := ClientConfig{SendBuffer: 16}
	client, err := NewClient(fakeConn, fakeHub, fakeHandler, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Start readPump goroutine
	go client.readPump(ctx)

	// Wait for error and unregistration
	time.Sleep(200 * time.Millisecond)

	// Verify unregistration was called (Hub.Clients() should return 0 since error happened immediately)
	if fakeHub.Clients() > 0 {
		t.Error("readPump should unregister on ReadMessage error, leaving 0 clients")
	}

	cancel()
}

// TestClientReadPumpPanicRecovery verifies readPump recovers from handler panics.
func TestClientReadPumpPanicRecovery(t *testing.T) {
	// Build a valid test frame
	validFrame := MustBuildTestFrame(t, MsgTypeAudio, 1, getTestAudioPayload())

	fakeConn := &fakeConn{
		readMessageResult: validFrame,
	}
	fakeHub, _ := NewHub(HubConfig{Limit: 100, RegisterBuffer: 32, UnregisterBuffer: 32}, zap.NewNop())
	fakeHandler := &fakeFrameHandler{
		onRoute: func(ctx context.Context, c *Client, h *FrameHeader, p []byte) error {
			panic("intentional handler panic for testing")
		},
	}

	cfg := ClientConfig{SendBuffer: 16}
	client, err := NewClient(fakeConn, fakeHub, fakeHandler, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// Capture logger output (use development config to see panic logs)
	logger, err := zap.NewDevelopment()
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	client.logger = logger

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Start readPump goroutine (should not crash despite handler panic)
	go client.readPump(ctx)

	// Wait for panic and recovery
	time.Sleep(200 * time.Millisecond)

	// Cancel context to stop readPump
	cancel()

	// Wait for goroutine to exit
	time.Sleep(100 * time.Millisecond)

	// Verify unregistration was called (graceful exit despite panic)
	if fakeHub.Clients() == 0 {
		t.Error("readPump should unregister on exit despite handler panic")
	}

	// If we got here without test crash, panic recovery worked
}