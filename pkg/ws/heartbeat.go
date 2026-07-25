// Package ws provides WebSocket heartbeat functionality for the home tutor agent.
// This file implements periodic Ping messages to detect and prevent stale connections.
package ws

import (
	"context"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// DefaultHeartbeatInterval is the default interval between Ping messages (D-14).
// Set to 30 seconds to align with Phase 1 D-57 cleanup cycle.
const DefaultHeartbeatInterval = 30 * time.Second

// Heartbeat sends periodic Ping messages to keep the connection alive and detect stale connections.
// Must be run as a goroutine. Stops when context is cancelled or Ping write fails.
//
// Interval behavior (RESEARCH.md lines 423-441):
// - Sends Ping message every `interval` duration
// - Uses deadline of interval/2 for each Ping write (avoids blocking longer than one tick)
// - Exits immediately on Ping write failure (connection likely broken)
// - Exits on context cancellation (D-12 lifecycle)
//
// WR-06 concurrency note: Heartbeat sends PING frames via WriteControl, which gorilla/websocket
// explicitly permits to run alongside a single WriteMessage writer. It is therefore safe to run
// concurrently with writePump on the same connection — the only constraint is at most one
// WriteMessage writer at a time, and writePump is the sole data writer. Server launches one
// Heartbeat goroutine per connection (CR-03); the pongs it elicits refresh that connection's
// read deadline (CR-02).
func Heartbeat(ctx context.Context, c WebSocketConn, interval time.Duration, logger *zap.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// D-12: Context cancelled - exit goroutine gracefully
			return

		case <-ticker.C:
			// Send Ping with deadline (interval/2 ensures we don't block longer than one cycle)
			deadline := time.Now().Add(interval / 2)
			if err := c.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				// Ping failed - connection is broken or remote is unreachable
				// Exit goroutine and let readPump detect connection failure via Hub.Unregister
				logger.Debug("heartbeat ping failed, exiting", zap.Error(err))
				return
			}
		}
	}
}