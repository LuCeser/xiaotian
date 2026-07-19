// Package ws provides WebSocket protocol implementation for the home tutor agent.
// This file preserves dependencies for the WebSocket protocol layer.
package ws

import (
	_ "github.com/gorilla/websocket" // Preserved for protocol implementation
	_ "go.uber.org/zap"               // Preserved for logging
	_ "github.com/pkg/errors"         // Preserved for error wrapping
)

// This file ensures dependencies are retained when go mod tidy is run.
// It will be replaced with actual protocol implementation in subsequent tasks.
