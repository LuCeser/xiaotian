package ws

import (
	"context"
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
				Limit:            tt.limit,
				RegisterBuffer:   32,
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
		Limit:            100,
		RegisterBuffer:   32,
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
		Limit:            100,
		RegisterBuffer:   32,
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
		Limit:            100,
		RegisterBuffer:   32,
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
		Limit:            2,
		RegisterBuffer:   32,
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

// TestHubStopsOnContextCancel verifies hub.Run exits when its context is cancelled.
//
// This test proves the same intent the old goroutine-count assertions tried to
// prove ("hub.Run started" / "hub.Run exited after cancel") using deterministic
// functional signals instead of runtime.NumGoroutine(), which is scheduler-
// sensitive and flaked under full-suite concurrent load on Windows. The three
// signals here are observable regardless of OS scheduling:
//
//  1. Liveness: Register a probe client, observe hub.Clients()==1. Only Run's
//     select-loop register branch increments the count, so a rising count is
//     functional proof Run is consuming its register channel.
//  2. Exit: poll hub.closed. Close is deferred inside Run (hub.go: defer
//     h.Close()), so closed==true deterministically proves Run has returned —
//     i.e. its goroutine exited (no CR-01 leak).
//  3. Shutdown: Register returns false on a closed hub (behavioural, not timing).
//
// The probe is a zero-value &Client{}: the Hub accept path only stores the
// pointer as a map key (h.clients[c] = true) and never dereferences it, so a
// bare struct is safe.
func TestHubStopsOnContextCancel(t *testing.T) {
	cfg := HubConfig{
		Limit:            100,
		RegisterBuffer:   32,
		UnregisterBuffer: 32,
	}

	hub, _ := NewHub(cfg, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	// Liveness proof: a probe client lands in the clients map only through
	// Run's select-loop register branch. If this count rises, Run is live.
	probe := &Client{}
	if !hub.Register(probe) {
		t.Fatal("Register(probe) returned false before cancel; hub not live or saturated")
	}
	if !waitFor(t, 2*time.Second, "hub.Clients() == 1 after Register(probe)", func() bool { return hub.Clients() == 1 }) {
		t.Fatalf("hub.Clients() == 1 after Register(probe) (timed out after %v)", 2*time.Second)
	}

	// Exit proof: Close is deferred inside Run, so closed==true means Run has
	// returned and its goroutine has exited (no CR-01 leak).
	cancel()
	if !waitFor(t, 2*time.Second, "hub.closed == true after cancel()", func() bool { return hub.closed }) {
		t.Fatalf("hub.closed == true after cancel() (timed out after %v)", 2*time.Second)
	}

	// Functional shutdown proof: a closed hub short-circuits Register.
	if hub.Register(&Client{}) {
		t.Error("Register on closed hub returned true; want false")
	}
}

// TestHubRegisterChannelBounded verifies register channel has bounded capacity.
func TestHubRegisterChannelBounded(t *testing.T) {
	cfg := HubConfig{
		Limit:            100,
		RegisterBuffer:   32,
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
		Limit:            100,
		RegisterBuffer:   32,
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

// TestHubStopsOnContextCancel relies on the package-level waitFor helper
// (defined in connection_test.go) for deterministic condition polling. That
// helper replaces the old scheduler-fragile runtime.NumGoroutine polls
// (waitForGoroutineIncrease / waitForGoroutineDrain, now deleted): it waits on
// a behaviour-based condition rather than a goroutine count, removing the OS-
// scheduler timing dependence that caused the full-suite flake.

// TestHub_Stats verifies the D-04 metrics counters (atomic.Uint64) track
// accept/reject/unregister correctly. The Hub is the single source of truth
// for connection lifecycle after D-04 option-a soft-deprecated
// ConnectionRegistry from the metrics path.
func TestHub_Stats(t *testing.T) {
	cfg := HubConfig{
		Limit:            2,
		RegisterBuffer:   32,
		UnregisterBuffer: 32,
	}

	hub, _ := NewHub(cfg, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	// Register two clients (fills capacity).
	client1 := &Client{
		send:      make(chan []byte, 16),
		closeOnce: sync.Once{},
		conn:      &fakeConnForHeartbeat{},
		hub:       hub,
		logger:    zap.NewNop(),
	}
	client2 := &Client{
		send:      make(chan []byte, 16),
		closeOnce: sync.Once{},
		conn:      &fakeConnForHeartbeat{},
		hub:       hub,
		logger:    zap.NewNop(),
	}
	hub.Register(client1)
	hub.Register(client2)

	if !waitFor(t, 2*time.Second, "2 clients accepted", func() bool { return hub.Clients() == 2 }) {
		t.Fatalf("Expected 2 clients, got %d", hub.Clients())
	}

	// Third client should be rejected at capacity limit.
	client3 := &Client{
		send:      make(chan []byte, 16),
		closeOnce: sync.Once{},
		conn:      &fakeConnForHeartbeat{},
		hub:       hub,
		logger:    zap.NewNop(),
	}
	hub.Register(client3)

	if !waitFor(t, 2*time.Second, "TotalRejected == 1", func() bool { return hub.Stats().TotalRejected == 1 }) {
		t.Fatalf("Expected TotalRejected == 1, got %d", hub.Stats().TotalRejected)
	}

	// Unregister client1.
	hub.Unregister(client1)
	if !waitFor(t, 2*time.Second, "TotalCleaned == 1", func() bool { return hub.Stats().TotalCleaned == 1 }) {
		t.Fatalf("Expected TotalCleaned == 1, got %d", hub.Stats().TotalCleaned)
	}

	// Final snapshot: Active==1, TotalAccepted==2, TotalRejected==1, TotalCleaned==1.
	stats := hub.Stats()
	if stats.Active != 1 {
		t.Errorf("Expected Active == 1, got %d", stats.Active)
	}
	if stats.TotalAccepted != 2 {
		t.Errorf("Expected TotalAccepted == 2, got %d", stats.TotalAccepted)
	}
	if stats.TotalRejected != 1 {
		t.Errorf("Expected TotalRejected == 1, got %d", stats.TotalRejected)
	}
	if stats.TotalCleaned != 1 {
		t.Errorf("Expected TotalCleaned == 1, got %d", stats.TotalCleaned)
	}
}
