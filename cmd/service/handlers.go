package main

// This file holds the WebSocket message handlers wired into the runtime by
// main.go. Phase 6 delivers the TTS-aware control frame handlers that
// orchestrate FSRS word selection, TTS batch generation, and audio frame
// streaming.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/tts"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// controlFramePayload represents the JSON schema for control frames.
// The client sends: {"type":"start_session|play_word|select_due_cards","params":{...}}
type controlFramePayload struct {
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params"`
}

// startSessionParams are the parameters for starting a TTS session.
type startSessionParams struct {
	Limit    int    `json:"limit"`
	Category string `json:"category"`
}

// playWordParams are the parameters for playing a word's audio.
type playWordParams struct {
	WordID    string `json:"word_id"`
	SessionID string `json:"session_id"`
}

// selectDueCardsParams are the parameters for FSRS due word queries.
type selectDueCardsParams struct {
	Limit           int    `json:"limit"`
	Category        string `json:"category"`
	Units           string `json:"units"`
	FetchLockHours  int    `json:"fetch_lock_hours"`
}

// sessionReadyResponse is the control frame response for successful session start.
type sessionReadyResponse struct {
	Type      string   `json:"type"`
	SessionID string   `json:"session_id"`
	Words     []string `json:"words"`
}

// selectDueCardsResultResponse is the control frame response for FSRS queries.
type selectDueCardsResultResponse struct {
	Type   string `json:"type"`
	Result struct {
		Cards []pybridge.DueCard `json:"cards"`
	} `json:"result"`
}

// errorResponse is the control frame response for errors.
type errorResponse struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// wordCompleteNotification is sent after all audio frames for a word are delivered.
type wordCompleteNotification struct {
	Type   string `json:"type"`
	WordID string `json:"word_id"`
}

// ttsHandlers returns the per-MsgType handler map handed to ws.NewRouter.
// It composes the FSRS service, TTS generator, and streamer to deliver the
// end-to-end TTS audio streaming pipeline.
func ttsHandlers(fsrsSvc fsrs.FSRSService, gen *tts.Generator, streamer *tts.Streamer, logger infra.Logger) map[ws.MessageType]ws.Handler {
	return map[ws.MessageType]ws.Handler{
		// MsgTypeAudio: inbound PCM frames. Placeholder until Phase 7+
		// processes client microphone audio.
		ws.MsgTypeAudio: func(_ context.Context, _ *ws.Client, _ *ws.FrameHeader, _ []byte) error {
			return nil
		},

		// MsgTypeControl: session control messages for TTS streaming.
		ws.MsgTypeControl: func(ctx context.Context, client *ws.Client, header *ws.FrameHeader, payload []byte) error {
			// Decode the control frame JSON
			var control controlFramePayload
			if err := json.Unmarshal(payload, &control); err != nil {
				logger.Warn("malformed control frame JSON",
					infra.F("error", err),
					infra.F("payload", string(payload)),
				)
				sendErrorFrame(client, "malformed control frame")
				return nil // don't terminate connection
			}

			// Dispatch based on control type
			switch control.Type {
			case "start_session":
				return handleStartSession(ctx, client, control.Params, fsrsSvc, gen, streamer, logger)

			case "play_word":
				return handlePlayWord(ctx, client, control.Params, streamer, logger)

			case "select_due_cards":
				return handleSelectDueCards(ctx, client, control.Params, fsrsSvc, logger)

			default:
				logger.Warn("unknown control type",
					infra.F("type", control.Type),
				)
				sendErrorFrame(client, "unknown control type")
				return nil
			}
		},
	}
}

// handleStartSession orchestrates FSRS selection, TTS generation, and prefetch.
func handleStartSession(ctx context.Context, client *ws.Client, params json.RawMessage, fsrsSvc fsrs.FSRSService, gen *tts.Generator, streamer *tts.Streamer, logger infra.Logger) error {
	var p startSessionParams
	if err := json.Unmarshal(params, &p); err != nil {
		logger.Warn("invalid start_session params", infra.ErrField(err))
		sendErrorFrame(client, "invalid start_session params")
		return nil
	}

	// Select due words via FSRS
	cards, err := fsrsSvc.SelectDueCards(ctx, fsrs.SelectOpts{
		Limit:    p.Limit,
		Category: p.Category,
	})
	if err != nil || len(cards) == 0 {
		logger.Warn("FSRS selection failed or empty",
			infra.ErrField(err),
			infra.F("cards", len(cards)),
		)
		sendErrorFrame(client, "no due words found")
		return nil
	}

	// Extract word strings
	words := make([]string, len(cards))
	for i, card := range cards {
		words[i] = card.Word
	}

	// Generate session ID (Phase 7 will provide real session IDs)
	sessionID := fmt.Sprintf("session-%d", time.Now().UnixMilli())

	// Generate TTS audio for all words in the session
	_, err = gen.GenerateForSession(ctx, sessionID, words)
	if err != nil {
		logger.Error("TTS generation failed",
			infra.F("session", sessionID),
			infra.ErrField(err),
		)
		sendErrorFrame(client, "TTS generation failed")
		return nil
	}

	// Prefetch the first N words for faster playback
	firstWords := make([]string, 0, 10) // default prefetch count
	for i := 0; i < len(cards) && i < 10; i++ {
		firstWords = append(firstWords, cards[i].Word)
	}
	if len(firstWords) > 0 {
		streamer.PrefetchNext(sessionID, firstWords)
	}

	// Send session_ready response
	response := sessionReadyResponse{
		Type:      "session_ready",
		SessionID: sessionID,
		Words:     words,
	}
	if err := sendControlFrame(client, ws.MsgTypeControl, response); err != nil {
		logger.Warn("failed to send session_ready response",
			infra.F("session", sessionID),
			infra.ErrField(err),
		)
	}

	return nil
}

// handlePlayWord streams audio frames for a single word.
func handlePlayWord(ctx context.Context, client *ws.Client, params json.RawMessage, streamer *tts.Streamer, logger infra.Logger) error {
	var p playWordParams
	if err := json.Unmarshal(params, &p); err != nil {
		logger.Warn("invalid play_word params", infra.ErrField(err))
		sendErrorFrame(client, "invalid play_word params")
		return nil
	}

	// Stream asynchronously so the handler returns immediately (frees Router worker)
	go func() {
		if err := streamer.StreamWord(ctx, client, p.SessionID, p.WordID, 0); err != nil {
			logger.Warn("audio streaming failed",
				infra.F("session", p.SessionID),
				infra.F("word", p.WordID),
				infra.ErrField(err),
			)
		}
	}()

	return nil
}

// handleSelectDueCards queries FSRS for due words (test interface support).
func handleSelectDueCards(ctx context.Context, client *ws.Client, params json.RawMessage, fsrsSvc fsrs.FSRSService, logger infra.Logger) error {
	var p selectDueCardsParams
	if err := json.Unmarshal(params, &p); err != nil {
		logger.Warn("invalid select_due_cards params", infra.ErrField(err))
		sendErrorFrame(client, "invalid select_due_cards params")
		return nil
	}

	cards, err := fsrsSvc.SelectDueCards(ctx, fsrs.SelectOpts{
		Limit:          p.Limit,
		Category:       p.Category,
		Units:          []string{p.Units},
		FetchLockHours: float64(p.FetchLockHours),
	})
	if err != nil {
		logger.Warn("select_due_cards failed", infra.ErrField(err))
		sendErrorFrame(client, "FSRS query failed")
		return nil
	}

	response := selectDueCardsResultResponse{
		Type: "select_due_cards_result",
	}
	response.Result.Cards = cards

	if err := sendControlFrame(client, ws.MsgTypeControl, response); err != nil {
		logger.Warn("failed to send select_due_cards_result", infra.ErrField(err))
	}

	return nil
}

// sendControlFrame is a helper for sending JSON control frames.
func sendControlFrame(client *ws.Client, msgType ws.MessageType, payload interface{}) error {
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	header := &ws.FrameHeader{
		Version:    1,
		MsgType:    msgType,
		Sequence:   1, // TODO: per-handler atomic counter
		PayloadLen: uint16(len(jsonBytes)),
	}

	frame := ws.EncodeFrame(header, jsonBytes)
	return client.Send(frame)
}

// sendErrorFrame sends an error response to the client.
func sendErrorFrame(client *ws.Client, message string) {
	response := errorResponse{
		Type:    "error",
		Message: message,
	}
	if err := sendControlFrame(client, ws.MsgTypeError, response); err != nil {
		// Log but don't fail - error frames are best-effort
		fmt.Printf("failed to send error frame: %v\n", err)
	}
}
