package ws

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestNewHubValidatesLimit verifies NewHub returns error for non-positive limits.
func TestNewHubValidatesLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit int
	}{
		{"zero limit", 0},
		{"negative limit", -1},
		{"large negative", -100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := HubConfig{
				Limit:          tt.limit,
				RegisterBuffer: 32,
				UnregisterBuffer: 32,
			}

			hub, err := NewHub(cfg, zap.NewNop())
			if err == nil {
				t.Error("NewHub should return error for invalid limit, got nil")
			}
			if hub != nil {
				t.Error("NewHub should return nil hub for invalid limit")
			}
		})
	}
}

// TestNewHubAcceptsPositiveLimit verifies NewHub succeeds with positive limit.
func TestNewHubAcceptsPositiveLimit(t *testing.T) {
	cfg := HubConfig{
		Limit:          100,
		RegisterBuffer: 32,
		UnregisterBuffer: 32,
	}

	hub, err := NewHub(cfg, zap.NewNop())
	if err != nil {
		t.Errorf("NewHub failed with positive limit: %v", err)
	}
	if hub == nil {
		t.Error("NewHub should return non-nil hub for valid config")
	}
}

// TestHubRegisterAddsClient verifies Register adds a client to the hub.
func TestHubRegisterAddsClient(t *testing.T) {
	cfg := HubConfig{
		Limit:          100,
		RegisterBuffer: 32,
		UnregisterBuffer: 32,
	}

	hub, _ := NewHub(cfg, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	// Create a minimal fake client
	client := &Client{
		send:      make(chan []byte, 16),
		closeOnce: sync.Once{},
		conn:      &fakeConnForHeartbeat{},
		hub:       hub,
		handler:   nil,
		logger:    zap.NewNop(),
	}

	hub.Register(client)

	// Wait for registration to process
	time.Sleep(50 * time.Millisecond)

	if hub.Clients() != 1 {
		t.Errorf("Expected 1 client after Register, got %d", hub.Clients())
	}
}

// TestHubUnregisterRemovesClient verifies Unregister removes a client from the hub.
func TestHubUnregisterRemovesClient(t *testing.T) {
	cfg := HubConfig{
		Limit:          100,
		RegisterBuffer: 32,
		UnregisterBuffer: 32,
	}

	hub, _ := NewHub(cfg, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	client := &Client{
		send:      make(chan []byte, 16),
		closeOnce: sync.Once{},
		conn:      &fakeConnForHeartbeat{},
		hub:       hub,
		handler:   nil,
		logger:    zap.NewNop(),
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	hub.Unregister(client)
	time.Sleep(50 * time.Millisecond)

	if hub.Clients() != 0 {
		t.Errorf("Expected 0 clients after Unregister, got %d", hub.Clients())
	}
}

// TestHubRejectsAtCapacity verifies hub rejects clients when at capacity limit.
func TestHubRejectsAtCapacity(t *testing.T) {
	cfg := HubConfig{
		Limit:          2,
		RegisterBuffer: 32,
		UnregisterBuffer: 32,
	}

	hub, _ := NewHub(cfg, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	// Create two fake clients that should be accepted
	client1 := &Client{
		send:      make(chan []byte, 16),
		closeOnce: sync.Once{},
		conn:      &fakeConnForHeartbeat{},
		hub:       hub,
		handler:   nil,
		logger:    zap.NewNop(),
	}
	client2 := &Client{
		send:      make(chan []byte, 16),
		closeOnce: sync.Once{},
		conn:      &fakeConnForHeartbeat{},
		hub:       hub,
		handler:   nil,
		logger:    zap.NewNop(),
	}

	// Third client should be rejected
	client3 := &Client{
		send:      make(chan []byte, 16),
		closeOnce: sync.Once{},
		conn:      &fakeConnForHeartbeat{},
		hub:       hub,
		handler:   nil,
		logger:    zap.NewNop(),
	}

	hub.Register(client1)
	hub.Register(client2)
	hub.Register(client3)

	// Wait for all registrations to process
	time.Sleep(100 * time.Millisecond)

	if hub.Clients() != 2 {
		t.Errorf("Expected 2 clients at capacity, got %d", hub.Clients())
	}

	// Verify third client was rejected by checking total count remains at limit
	// (Third client should not have been added)
}

// TestHubStopsOnContextCancel verifies hub.Run goroutine exits when context is cancelled.
func TestHubStopsOnContextCancel(t *testing.T) {
	cfg := HubConfig{
		Limit:          100,
		RegisterBuffer: 32,
		UnregisterBuffer: 32,
	}

	hub, _ := NewHub(cfg, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())

	// Measure goroutine count before starting hub.Run
	baseGoroutines := runtime.NumGoroutine()

	go hub.Run(ctx)

	// Wait for the hub.Run goroutine to actually start. A fixed time.Sleep is
	// scheduler-sensitive and flakes under full-suite load: the assertion below
	// fired intermittently because the goroutine hadn't been scheduled within
	// 50ms. Polling for the count to rise (with a deadline) keeps the test's
	// intent — prove hub.Run blocks — while removing the timing fragility.
	started := waitForGoroutineIncrease(baseGoroutines, 2*time.Second)
	if !started {
		t.Error("Expected goroutine count to increase after starting hub.Run")
	}

	// Cancel context to trigger shutdown
	cancel()

	// Wait for the goroutine to exit (should be within 200ms per D-12). Poll
	// instead of a fixed sleep so cleanup under load is observed reliably.
	waitForGoroutineDrain(baseGoroutines+1, 2*time.Second)

	// Verify goroutine count returned to baseline
	finalGoroutines := runtime.NumGoroutine()
	if finalGoroutines > baseGoroutines+1 { // +1 for test tolerance
		t.Errorf("Expected goroutine count to return to baseline after cancel, got %d (baseline %d)", finalGoroutines, baseGoroutines)
	}
}

// TestHubRegisterChannelBounded verifies register channel has bounded capacity.
func TestHubRegisterChannelBounded(t *testing.T) {
	cfg := HubConfig{
		Limit:          100,
		RegisterBuffer: 32,
		UnregisterBuffer: 32,
	}

	hub, _ := NewHub(cfg, zap.NewNop())

	// Check that register channel has bounded capacity (not nil and not unbounded)
	if hub.register == nil {
		t.Error("register channel should not be nil")
	}

	// The channel capacity should be RegisterBuffer (32)
	capacity := cap(hub.register)
	if capacity != cfg.RegisterBuffer {
		t.Errorf("register channel capacity = %d, want %d", capacity, cfg.RegisterBuffer)
	}

	// Verify channel is truly bounded by attempting to fill it
	// Start Hub.Run to process registrations
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	// Send more clients than the channel capacity - should eventually all succeed
	// since Hub.Run is processing them
	successCount := atomic.Int32{}
	for i := 0; i < 100; i++ {
		go func() {
			client := &Client{
				send:      make(chan []byte, 16),
				closeOnce: sync.Once{},
				conn:      &fakeConnForHeartbeat{},
				hub:       hub,
				handler:   nil,
				logger:    zap.NewNop(),
			}
			hub.Register(client)
			successCount.Add(1)
		}()
	}

	// Wait for all sends to complete (they should succeed since Hub.Run is draining)
	time.Sleep(200 * time.Millisecond)
	finalCount := successCount.Load()
	if finalCount != 100 {
		t.Errorf("Expected all 100 sends to succeed with Hub.Run processing, got %d", finalCount)
	}
}

// TestHubConcurrentRegisterUnregister verifies concurrent register/unregister is race-free.
func TestHubConcurrentRegisterUnregister(t *testing.T) {
	cfg := HubConfig{
		Limit:          100,
		RegisterBuffer: 32,
		UnregisterBuffer: 32,
	}

	hub, _ := NewHub(cfg, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	// Launch many concurrent register/unregister operations
	for i := 0; i < 100; i++ {
		go func() {
			client := &Client{
				send:      make(chan []byte, 16),
				closeOnce: sync.Once{},
				conn:      &fakeConnForHeartbeat{},
				hub:       hub,
				handler:   nil,
				logger:    zap.NewNop(),
			}
			hub.Register(client)
			time.Sleep(time.Duration(i%10) * time.Millisecond)
			hub.Unregister(client)
		}()
	}

	// Wait for all operations to complete
	time.Sleep(500 * time.Millisecond)

	// If we got here without data race, the test passes
	// Run with -race to detect issues
}

// waitForGoroutineIncrease polls until the live goroutine count rises above
// baseline, returning true once it does or false on timeout. Replaces a fixed
// time.Sleep before asserting "hub.Run started a goroutine" — the sleep was
// scheduler-sensitive and flaked under full-suite load.
func waitForGoroutineIncrease(baseline int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		runtime.Gosched()
		if runtime.NumGoroutine() > baseline {
			return true
		}
	}
	return false
}

// waitForGoroutineDrain polls until the live goroutine count drops to target
// or below, or the timeout elapses. Replaces a fixed sleep before asserting
// "hub.Run exited after cancel" for the same scheduler-fragility reason.
func waitForGoroutineDrain(target int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		runtime.Gosched()
		if runtime.NumGoroutine() <= target {
			return
		}
	}
}