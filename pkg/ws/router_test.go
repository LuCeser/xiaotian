package ws

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// Compile-time interface implementation check
var _ FrameHandler = (*Router)(nil)

// TestNewRouterValidatesConfig tests that NewRouter validates configuration
func TestNewRouterValidatesConfig(t *testing.T) {
	tests := []struct {
		wantErr error
		name    string
		cfg     RouterConfig
	}{
		{
			name: "zero route buffer",
			cfg: RouterConfig{
				RouteBuffer:       0,
				WorkerConcurrency: 4,
			},
			wantErr: ErrInvalidRouterConfig,
		},
		{
			name: "zero worker concurrency",
			cfg: RouterConfig{
				RouteBuffer:       32,
				WorkerConcurrency: 0,
			},
			wantErr: ErrInvalidRouterConfig,
		},
		{
			name: "negative route buffer",
			cfg: RouterConfig{
				RouteBuffer:       -1,
				WorkerConcurrency: 4,
			},
			wantErr: ErrInvalidRouterConfig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := zap.NewNop()
			_, err := NewRouter(map[MessageType]Handler{}, tt.cfg, logger)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if errors.Is(err, tt.wantErr) {
				t.Logf("correctly got expected error: %v", tt.wantErr)
			}
		})
	}
}

// TestNewRouterAcceptsValidConfig tests that NewRouter accepts valid configuration
func TestNewRouterAcceptsValidConfig(t *testing.T) {
	logger := zap.NewNop()
	router, err := NewRouter(map[MessageType]Handler{}, RouterConfig{
		RouteBuffer:       32,
		WorkerConcurrency: 4,
	}, logger)

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if router == nil {
		t.Fatal("expected non-nil router")
	}
	router.Close()
}

// TestNewRouter_RoutesPopulatedBeforeWorkers is a regression test for the P0
// "concurrent map read and map write" race in NewRouter. The previous
// single-loop implementation populated router.routes AND started worker
// goroutines in the same iteration, so with >=2 message types a worker from
// iteration 1 could read r.routes while iteration 2 wrote it — a fatal Go map
// sanity check that crashed the process. The fix populates all routes first,
// then starts all workers.
func TestNewRouter_RoutesPopulatedBeforeWorkers(t *testing.T) {
	trivialHandler := func(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
		return nil
	}

	handlers := map[MessageType]Handler{
		MsgTypeAudio:   trivialHandler,
		MsgTypeControl: trivialHandler,
		MsgTypeStatus:  trivialHandler,
	}

	logger := zap.NewNop()
	router, err := NewRouter(handlers, RouterConfig{
		RouteBuffer:       8,
		WorkerConcurrency: 4,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	// All routes populated — this is the deterministic guard that would fail if
	// someone re-merged the loops into one pass.
	if len(router.routes) != 3 {
		t.Fatalf("expected 3 routes populated, got %d", len(router.routes))
	}

	// Each route channel must exist and be non-nil.
	for _, mt := range []MessageType{MsgTypeAudio, MsgTypeControl, MsgTypeStatus} {
		ch, ok := router.routes[mt]
		if !ok || ch == nil {
			t.Fatalf("route channel for message type %s missing or nil (ok=%v)", mt.String(), ok)
		}
	}

	// Smoke that Route accepts a frame into each type's channel.
	ctx := context.Background()
	fakeClient := &Client{}
	for _, mt := range []MessageType{MsgTypeAudio, MsgTypeControl, MsgTypeStatus} {
		header := &FrameHeader{MsgType: mt}
		if err := router.Route(ctx, fakeClient, header, []byte{0x01}); err != nil {
			t.Fatalf("Route failed for message type %s: %v", mt.String(), err)
		}
	}
}

// TestRouterImplementsFrameHandler tests that Router implements FrameHandler interface
func TestRouterImplementsFrameHandler(t *testing.T) {
	// Compile-time check already done above, this is a runtime sanity check
	logger := zap.NewNop()
	router, err := NewRouter(map[MessageType]Handler{}, RouterConfig{
		RouteBuffer:       32,
		WorkerConcurrency: 4,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	// The assignment is the implementation check. A type assertion reads the
	// interface value without comparing it to nil (a concrete *Router is
	// never a nil interface).
	var handler FrameHandler = router
	got, ok := handler.(*Router)
	if !ok || got != router {
		t.Fatal("FrameHandler should be the router")
	}
}

// TestRouterRouteAudio tests routing audio messages
func TestRouterRouteAudio(t *testing.T) {
	callCount := atomic.Int32{}
	// recvMu guards receivedHeader/receivedPayload: the handler runs on a
	// worker goroutine while this test goroutine reads them back (a bare
	// read races the handler's write under -race, issue #26).
	var recvMu sync.Mutex
	var receivedHeader *FrameHeader
	var receivedPayload []byte

	audioHandler := func(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
		callCount.Add(1)
		recvMu.Lock()
		receivedHeader = h
		receivedPayload = payload
		recvMu.Unlock()
		return nil
	}

	handlers := map[MessageType]Handler{
		MsgTypeAudio: audioHandler,
	}

	logger := zap.NewNop()
	router, err := NewRouter(handlers, RouterConfig{
		RouteBuffer:       32,
		WorkerConcurrency: 4,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	ctx := context.Background()
	fakeClient := &Client{} // Minimal fake client
	testHeader := &FrameHeader{MsgType: MsgTypeAudio}
	testPayload := []byte{1, 2, 3, 4}

	err = router.Route(ctx, fakeClient, testHeader, testPayload)
	if err != nil {
		t.Fatalf("route failed: %v", err)
	}

	// Give time for async handler execution
	time.Sleep(100 * time.Millisecond)

	recvMu.Lock()
	gotHeader, gotPayload := receivedHeader, receivedPayload
	recvMu.Unlock()

	if callCount.Load() != 1 {
		t.Errorf("expected handler called 1 time, got %d", callCount.Load())
	}
	if gotHeader == nil {
		t.Error("expected header to be received")
	}
	if gotPayload == nil || len(gotPayload) != 4 {
		t.Errorf("expected payload to be received with 4 bytes, got %v", gotPayload)
	}
}

// TestRouterRouteControl tests routing control messages
func TestRouterRouteControl(t *testing.T) {
	callCount := atomic.Int32{}

	controlHandler := func(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
		callCount.Add(1)
		return nil
	}

	handlers := map[MessageType]Handler{
		MsgTypeControl: controlHandler,
	}

	logger := zap.NewNop()
	router, err := NewRouter(handlers, RouterConfig{
		RouteBuffer:       32,
		WorkerConcurrency: 4,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	ctx := context.Background()
	fakeClient := &Client{}
	testHeader := &FrameHeader{MsgType: MsgTypeControl}
	testPayload := []byte{5, 6, 7, 8}

	err = router.Route(ctx, fakeClient, testHeader, testPayload)
	if err != nil {
		t.Fatalf("route failed: %v", err)
	}

	// Give time for async handler execution
	time.Sleep(100 * time.Millisecond)

	if callCount.Load() != 1 {
		t.Errorf("expected handler called 1 time, got %d", callCount.Load())
	}
}

// TestRouterRouteUnknownType tests routing unknown message types
func TestRouterRouteUnknownType(t *testing.T) {
	handler := func(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
		return nil
	}

	handlers := map[MessageType]Handler{
		MsgTypeAudio: handler,
	}

	logger := zap.NewNop()
	router, err := NewRouter(handlers, RouterConfig{
		RouteBuffer:       32,
		WorkerConcurrency: 4,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	ctx := context.Background()
	fakeClient := &Client{}
	testHeader := &FrameHeader{MsgType: MsgTypeError} // Not registered
	testPayload := []byte{1, 2, 3}

	err = router.Route(ctx, fakeClient, testHeader, testPayload)
	if err == nil {
		t.Fatal("expected error for unknown message type, got nil")
	}

	if !errors.Is(err, ErrUnknownMessageType) {
		t.Errorf("expected ErrUnknownMessageType, got: %v", err)
	}
}

// TestRouterRoutePanicRecovery tests that handler panics are recovered
func TestRouterRoutePanicRecovery(t *testing.T) {
	panicHandler := func(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
		panic("intentional handler panic")
	}

	handlers := map[MessageType]Handler{
		MsgTypeAudio: panicHandler,
	}

	logger := zap.NewNop()
	router, err := NewRouter(handlers, RouterConfig{
		RouteBuffer:       32,
		WorkerConcurrency: 4,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	ctx := context.Background()
	fakeClient := &Client{}
	testHeader := &FrameHeader{MsgType: MsgTypeAudio}
	testPayload := []byte{1, 2, 3}

	// This should not panic (the panic is recovered in the worker)
	err = router.Route(ctx, fakeClient, testHeader, testPayload)
	if err != nil {
		t.Fatalf("route should not error even with handler panic, got: %v", err)
	}

	// Give time for async handler execution and panic recovery
	time.Sleep(100 * time.Millisecond)

	// Router should still be functional
	testHeader2 := &FrameHeader{MsgType: MsgTypeAudio}
	testPayload2 := []byte{4, 5, 6}

	err = router.Route(ctx, fakeClient, testHeader2, testPayload2)
	if err != nil {
		t.Fatalf("router should still work after panic, got: %v", err)
	}
}

// TestRouterRouteSerial tests that sequential Route calls trigger handlers in order
func TestRouterRouteSerial(t *testing.T) {
	callOrder := atomic.Int32{}
	// orderMu guards order: the handler appends on the worker goroutine
	// while this test goroutine later reads the slice back (issue #26).
	var orderMu sync.Mutex
	order := []int32{}

	handler := func(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
		seq := callOrder.Add(1)
		orderMu.Lock()
		order = append(order, seq)
		orderMu.Unlock()
		return nil
	}

	handlers := map[MessageType]Handler{
		MsgTypeAudio: handler,
	}

	logger := zap.NewNop()
	router, err := NewRouter(handlers, RouterConfig{
		RouteBuffer:       32,
		WorkerConcurrency: 1, // Single worker for strict ordering
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	ctx := context.Background()
	fakeClient := &Client{}

	// Send 10 messages sequentially
	for i := 0; i < 10; i++ {
		testHeader := &FrameHeader{MsgType: MsgTypeAudio}
		testPayload := []byte{byte(i)}
		err = router.Route(ctx, fakeClient, testHeader, testPayload)
		if err != nil {
			t.Fatalf("route failed for message %d: %v", i, err)
		}
	}

	// Give time for async handler execution
	time.Sleep(200 * time.Millisecond)

	orderMu.Lock()
	got := append([]int32(nil), order...)
	orderMu.Unlock()

	if len(got) != 10 {
		t.Errorf("expected 10 handler calls, got %d", len(got))
	}

	// Verify order is sequential (1,2,3,...10)
	for i := 0; i < 10; i++ {
		if got[i] != int32(i+1) {
			t.Errorf("expected order[%d]=%d, got %d", i, i+1, got[i])
		}
	}
}

// TestRouterRouteDropOldestOnBackpressure tests backpressure signal
func TestRouterRouteDropOldestOnBackpressure(t *testing.T) {
	slowHandler := func(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
		time.Sleep(10 * time.Millisecond) // Simulate slow processing
		return nil
	}

	handlers := map[MessageType]Handler{
		MsgTypeAudio: slowHandler,
	}

	// Small buffer to trigger backpressure
	logger := zap.NewNop()
	router, err := NewRouter(handlers, RouterConfig{
		RouteBuffer:       2, // Small buffer
		WorkerConcurrency: 1, // Single worker processes sequentially
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	defer router.Close()

	ctx := context.Background()
	fakeClient := &Client{}

	overloadedCount := 0
	successCount := 0

	// Rapidly send many messages - faster than worker can process
	for i := 0; i < 10; i++ {
		testHeader := &FrameHeader{MsgType: MsgTypeAudio}
		testPayload := []byte{byte(i)}
		err = router.Route(ctx, fakeClient, testHeader, testPayload)

		if errors.Is(err, ErrRouterOverloaded) {
			overloadedCount++
		} else if err == nil {
			successCount++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	// With slow handler and small buffer, we should get some ErrRouterOverloaded
	if overloadedCount == 0 {
		t.Errorf("expected at least one ErrRouterOverloaded, got %d overloaded out of %d total", overloadedCount, successCount+overloadedCount)
	}

	t.Logf("Backpressure test: %d succeeded, %d overloaded (backpressure working)", successCount, overloadedCount)
}

// TestRouterRouteContextCancel tests that router respects context cancellation
func TestRouterRouteContextCancel(t *testing.T) {
	handler := func(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
		return nil
	}

	handlers := map[MessageType]Handler{
		MsgTypeAudio: handler,
	}

	logger := zap.NewNop()
	router, err := NewRouter(handlers, RouterConfig{
		RouteBuffer:       32,
		WorkerConcurrency: 4,
	}, logger)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	// Cancel router context
	router.Close()

	ctx := context.Background()
	fakeClient := &Client{}
	testHeader := &FrameHeader{MsgType: MsgTypeAudio}
	testPayload := []byte{1, 2, 3}

	err = router.Route(ctx, fakeClient, testHeader, testPayload)
	if err == nil {
		t.Fatal("expected error after router closed, got nil")
	}

	if !errors.Is(err, ErrRouterShuttingDown) {
		t.Errorf("expected ErrRouterShuttingDown, got: %v", err)
	}
}
