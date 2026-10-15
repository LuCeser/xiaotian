package ws

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// fakeConnForHeartbeat is a specialized fake for Heartbeat testing.
// Focuses on WriteControl behavior (Ping/Close messages).
type fakeConnForHeartbeat struct {
	writeControlError      error
	writeControlCallCount  atomic.Int32
	lastControlMessageType atomic.Int32
}

func (f *fakeConnForHeartbeat) WriteControl(messageType int, data []byte, deadline time.Time) error {
	f.writeControlCallCount.Add(1)
	if messageType < math.MinInt32 || messageType > math.MaxInt32 {
		return errors.New("message type out of int32 range")
	}
	f.lastControlMessageType.Store(int32(messageType))
	if f.writeControlError != nil {
		return f.writeControlError
	}
	return nil
}

func (f *fakeConnForHeartbeat) ReadMessage() (int, []byte, error) {
	return 0, []byte("test"), nil
}

func (f *fakeConnForHeartbeat) WriteMessage(int, []byte) error {
	return nil
}

// SetWriteDeadline satisfies WebSocketConn; unused by Heartbeat's WriteControl path.
func (f *fakeConnForHeartbeat) SetWriteDeadline(time.Time) error {
	return nil
}

func (f *fakeConnForHeartbeat) Close() error {
	return nil
}

// TestHeartbeatTicksAtInterval verifies Heartbeat sends Ping at specified interval.
// Uses short tick (50ms) instead of 30s for fast test execution.
func TestHeartbeatTicksAtInterval(t *testing.T) {
	fakeConn := &fakeConnForHeartbeat{}
	logger := zap.NewNop()

	// Use short interval for fast test
	interval := 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Start Heartbeat goroutine
	go Heartbeat(ctx, fakeConn, interval, logger)

	// Wait for at least 2 ticks
	time.Sleep(150 * time.Millisecond)

	// Verify Ping was sent at least twice
	callCount := fakeConn.writeControlCallCount.Load()
	if callCount < 2 {
		t.Errorf("Heartbeat sent %d pings, want at least 2", callCount)
	}

	// Verify messages were Ping messages
	lastType := fakeConn.lastControlMessageType.Load()
	if lastType != int32(websocket.PingMessage) {
		t.Errorf("Last control message type = %d, want PingMessage (%d)", lastType, websocket.PingMessage)
	}
}

// TestHeartbeatStopsOnContextCancel verifies Heartbeat exits when context is cancelled.
func TestHeartbeatStopsOnContextCancel(t *testing.T) {
	fakeConn := &fakeConnForHeartbeat{}
	logger := zap.NewNop()

	interval := 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())

	// Start Heartbeat goroutine
	go Heartbeat(ctx, fakeConn, interval, logger)

	// Give goroutine time to start
	time.Sleep(50 * time.Millisecond)

	// Cancel context to trigger exit
	cancel()

	// Wait for goroutine to exit (should be within 100ms)
	time.Sleep(100 * time.Millisecond)

	// Count calls before and after cancel should be similar (no new calls after cancel)
	callCount := fakeConn.writeControlCallCount.Load()

	// Wait longer and verify no new calls
	time.Sleep(100 * time.Millisecond)
	newCallCount := fakeConn.writeControlCallCount.Load()

	if newCallCount > callCount+1 { // Allow at most 1 more call (race window)
		t.Errorf("Heartbeat continued after context cancel: %d calls before, %d after", callCount, newCallCount)
	}
}

// TestHeartbeatExitsOnPingError verifies Heartbeat exits immediately when Ping write fails.
func TestHeartbeatExitsOnPingError(t *testing.T) {
	fakeConn := &fakeConnForHeartbeat{
		writeControlError: errors.New("connection broken"),
	}
	logger := zap.NewNop()

	interval := 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Start Heartbeat goroutine
	go Heartbeat(ctx, fakeConn, interval, logger)

	// Wait for first tick and error
	time.Sleep(100 * time.Millisecond)

	// Verify Ping was attempted
	callCount := fakeConn.writeControlCallCount.Load()
	if callCount == 0 {
		t.Error("Heartbeat should attempt at least one Ping before error")
	}

	// Verify no further calls after error (Heartbeat should have exited)
	initialCallCount := callCount
	time.Sleep(100 * time.Millisecond)
	finalCallCount := fakeConn.writeControlCallCount.Load()

	if finalCallCount > initialCallCount {
		t.Errorf("Heartbeat continued after Ping error: %d calls after error", finalCallCount-initialCallCount)
	}
}
