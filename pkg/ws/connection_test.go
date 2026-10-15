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

// waitFor polls cond until it returns true or the timeout elapses, returning whether
// cond ever held. Replaces fixed time.Sleep waits for deterministic synchronization
// (IN-01) and makes the unregistration assertions below scheduler-robust under -race.
func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// MustBuildTestFrame builds a test frame for testing. Panics on failure (t.Helper() pattern).
func MustBuildTestFrame(t *testing.T, msgType MessageType, seq uint32, payload []byte) []byte {
	t.Helper()
	header := &FrameHeader{
		Version:   ProtocolVersion,
		MsgType:   msgType,
		Sequence:  seq,
		Timestamp: uint32(time.Now().UnixMicro()), //nolint:gosec // G115: protocol timestamp keeps the low 32 bits of Unix microseconds
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
	writeMessageError error             // Error to return from WriteMessage
	closeCallReason   atomic.Value      // string
	readMessageError  error             // Error to return from ReadMessage
	writeControlError error             // Error to return from WriteControl
	onWriteMessage    func([]byte)      // Optional callback on WriteMessage
	onWriteControl    func(int, []byte) // Optional callback on WriteControl

	// Test control fields
	readMessageResult []byte // Return value for ReadMessage
	// mu guards the non-atomic control fields below (readMessageResult/Error,
	// writeMessageError, writeControlError, onWriteMessage, onWriteControl), which are
	// set by the test goroutine and read inside ReadMessage/WriteMessage/WriteControl
	// running on the pump goroutines. WR-10: previously declared but unused, leaving
	// latent data races under -race.
	mu                    sync.Mutex
	readMessageCallCount  atomic.Int32
	writeMessageCallCount atomic.Int32
	writeControlCallCount atomic.Int32
	closeCallCount        atomic.Int32
	closeCallCode         atomic.Int32
}

func (f *fakeConn) ReadMessage() (int, []byte, error) {
	f.readMessageCallCount.Add(1)
	f.mu.Lock()
	result := f.readMessageResult
	err := f.readMessageError
	f.mu.Unlock()
	if err != nil {
		return 0, nil, err
	}
	if result != nil {
		return websocket.BinaryMessage, result, nil
	}
	return websocket.BinaryMessage, []byte("test"), nil
}

func (f *fakeConn) WriteMessage(messageType int, data []byte) error {
	f.writeMessageCallCount.Add(1)
	f.mu.Lock()
	err := f.writeMessageError
	cb := f.onWriteMessage
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if cb != nil {
		cb(data)
	}
	return nil
}

func (f *fakeConn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	f.writeControlCallCount.Add(1)
	if messageType == websocket.CloseMessage {
		// Extract close code and reason from data (format per WebSocket spec)
		if len(data) >= 2 {
			code := int32(data[0])<<8 | int32(data[1])
			f.closeCallCode.Store(code)
			if len(data) > 2 {
				reason := string(data[2:])
				f.closeCallReason.Store(reason)
			}
		}
	}
	f.mu.Lock()
	err := f.writeControlError
	cb := f.onWriteControl
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if cb != nil {
		cb(messageType, data)
	}
	return nil
}

// SetWriteDeadline satisfies the WebSocketConn interface. writePump sets a write
// deadline before each WriteMessage (CR-06); the fake does not model network
// backpressure, so the deadline is accepted and ignored.
func (f *fakeConn) SetWriteDeadline(deadline time.Time) error {
	return nil
}

func (f *fakeConn) Close() error {
	f.closeCallCount.Add(1)
	return nil
}

// fakeFrameHandler is a manual fake for FrameHandler (D-48).
// Records calls to Route for verification in tests.
type fakeFrameHandler struct {
	lastRouteCtx     atomic.Value // *context.Context
	lastRouteClient  atomic.Value // *Client
	lastRouteHeader  atomic.Value // *FrameHeader
	lastRoutePayload atomic.Value // []byte
	routeError       error        // Error to return from Route
	onRoute          func(ctx context.Context, c *Client, h *FrameHeader, p []byte) error
	routeCallCount   atomic.Int32
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

	// Close send channel to trigger writePump exit (use the close-safe API, CR-04)
	client.CloseSend()

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

	// Hub outlives the read pump so it can drain the unregister after readPump exits.
	// A single shared ctx would cancel Hub.Run at the same instant as readPump, racing
	// the unregister and leaving the client in the map (the latent flake WR-02 exposed).
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go fakeHub.Run(hubCtx)

	// Register client with Hub
	fakeHub.Register(client)
	if !waitFor(t, 200*time.Millisecond, "client registration", func() bool {
		return fakeHub.Clients() == 1
	}) {
		t.Fatalf("client should be registered before readPump starts, got %d", fakeHub.Clients())
	}

	readCtx, readCancel := context.WithCancel(context.Background())
	defer readCancel()

	// Start readPump goroutine
	go client.readPump(readCtx)

	// Wait for read and routing
	time.Sleep(100 * time.Millisecond)

	// Verify handler.Route was called
	if fakeHandler.routeCallCount.Load() == 0 {
		t.Error("readPump should call handler.Route for valid frame")
	}

	// Stop readPump only; the hub keeps running to process the unregister.
	readCancel()

	// WR-02: verify unregistration completed on exit. The previous assertion fired when
	// Clients()==0 — exactly when unregistration HAD happened — so it proved the opposite
	// of its message. Inverted, and polled so it is robust to scheduling jitter.
	if !waitFor(t, 200*time.Millisecond, "readPump unregisters on exit", func() bool {
		return fakeHub.Clients() == 0
	}) {
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

	// Hub outlives the read pump so it can drain the unregister after readPump exits.
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go fakeHub.Run(hubCtx)

	// Register client with Hub
	fakeHub.Register(client)
	if !waitFor(t, 200*time.Millisecond, "client registration", func() bool {
		return fakeHub.Clients() == 1
	}) {
		t.Fatalf("client should be registered before readPump starts, got %d", fakeHub.Clients())
	}

	readCtx, readCancel := context.WithCancel(context.Background())
	defer readCancel()

	// Start readPump goroutine
	go client.readPump(readCtx)

	// Wait for read attempt
	time.Sleep(100 * time.Millisecond)

	// Verify handler.Route was NOT called (invalid frame was skipped)
	if fakeHandler.routeCallCount.Load() != 0 {
		t.Error("readPump should not call handler.Route for invalid frame")
	}

	// Stop readPump only; the hub keeps running to process the unregister.
	readCancel()

	// WR-02: verify unregistration still completed (graceful exit after skipping the
	// invalid frame). Previously asserted the wrong polarity (fired when Clients()==0).
	if !waitFor(t, 200*time.Millisecond, "readPump unregisters after skipping invalid frame", func() bool {
		return fakeHub.Clients() == 0
	}) {
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
	defer cancel()

	// WR-03: the hub event loop must run and the client must be registered before
	// readPump starts. The previous version never called Run/Register, so Clients()
	// was always 0 and the assertion passed trivially even if the defer were removed.
	go fakeHub.Run(ctx)
	fakeHub.Register(client)

	if !waitFor(t, 200*time.Millisecond, "client registration", func() bool {
		return fakeHub.Clients() == 1
	}) {
		t.Fatalf("client should be registered before readPump starts, got %d clients", fakeHub.Clients())
	}

	// readPump errors on the first ReadMessage and unregisters via its defer.
	go client.readPump(ctx)

	if !waitFor(t, 200*time.Millisecond, "client unregistration on read error", func() bool {
		return fakeHub.Clients() == 0
	}) {
		t.Error("readPump should unregister on ReadMessage error, leaving 0 clients")
	}
}

// TestClientReadPumpPanicRecovery verifies readPump recovers from handler panics
// and still unregisters cleanly on exit.
//
// IN-04: the previous version reused a single ctx for both the Hub and readPump
// and asserted via fixed time.Sleep. Under CR-01 the hub's ctx.Done and the
// readPump defer race: if Hub.Run exits (and Close flips the closed flag) before
// the deferred Unregister reaches the channel, the unregister is dropped and the
// client stays in the map — making the test flaky. The other readPump tests were
// already fixed (WR-02) to give the hub its own longer-lived ctx and to poll via
// waitFor; this test now follows the same pattern.
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

	// Hub outlives the read pump so it can drain the unregister after readPump exits.
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go fakeHub.Run(hubCtx)

	// Register client with Hub
	fakeHub.Register(client)
	if !waitFor(t, 200*time.Millisecond, "client registration", func() bool {
		return fakeHub.Clients() == 1
	}) {
		t.Fatalf("client should be registered before readPump starts, got %d", fakeHub.Clients())
	}

	readCtx, readCancel := context.WithCancel(context.Background())
	defer readCancel()

	// Start readPump goroutine (should not crash despite handler panic)
	go client.readPump(readCtx)

	// Wait for the handler to be invoked at least once (it panics, readPump recovers).
	if !waitFor(t, 500*time.Millisecond, "handler invoked (panic recovered)", func() bool {
		return fakeHandler.routeCallCount.Load() >= 1
	}) {
		t.Fatal("readPump should invoke (and recover from) the panicking handler at least once")
	}

	// Stop readPump only; the hub keeps running to process the unregister.
	readCancel()

	// Verify unregistration completed on exit despite the earlier handler panic.
	if !waitFor(t, 200*time.Millisecond, "readPump unregisters after panic recovery", func() bool {
		return fakeHub.Clients() == 0
	}) {
		t.Error("readPump should unregister on exit despite handler panic")
	}

	// If we got here without test crash, panic recovery worked
}
