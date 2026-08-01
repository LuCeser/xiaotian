package main

// This file holds the WebSocket message handlers wired into the runtime by
// main.go (P0-1). Handlers are deliberately NO-OP this phase: the WS upgrade,
// Hub registration, heartbeat, frame decoding, and Router dispatch are all live
// and end-to-end testable, but real audio playback / session state / TTS / FSRS
// are explicitly deferred to Phase 6/7. Each handler matches the
// ws.Handler signature proven in pkg/ws/integration_test.go (the exact no-op
// closure shape) and simply returns nil so the Router worker pool exercises the
// full dispatch path without mutating any state.

import (
	"context"

	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// noOpHandlers returns the per-MsgType handler map handed to ws.NewRouter.
// Every entry is a placeholder until Phase 6/7 delivers the real audio/session
// logic. MsgTypeAudio and MsgTypeControl are the two types the current client
// protocol emits; unknown types are rejected upstream by ws.Router.Route
// (ErrUnknownMessageType) so they need no entry here.
func noOpHandlers() map[ws.MessageType]ws.Handler {
	return map[ws.MessageType]ws.Handler{
		// MsgTypeAudio: inbound PCM frames. Placeholder until Phase 6 wires the
		// audio buffer pool + playback path.
		ws.MsgTypeAudio: func(_ context.Context, _ *ws.Client, _ *ws.FrameHeader, _ []byte) error {
			return nil
		},
		// MsgTypeControl: session control messages. Placeholder until Phase 7
		// wires the session state machine.
		ws.MsgTypeControl: func(_ context.Context, _ *ws.Client, _ *ws.FrameHeader, _ []byte) error {
			return nil
		},
	}
}
