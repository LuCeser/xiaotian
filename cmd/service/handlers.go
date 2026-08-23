package main

// This file holds the WebSocket message handlers wired into the runtime by
// main.go. Phase 6 delivers the TTS-aware control frame handlers that
// orchestrate FSRS word selection, TTS batch generation, and audio frame
// streaming.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// controlFrameSeq is an atomic monotonic counter for control-frame Sequence
// values (D-12). It replaces the previously hardcoded Sequence: 1 so that
// consecutive sendControlFrame calls never collide on the same sequence — the
// client relies on monotonic sequences for ordering/dedup. A package-level
// global is acceptable here (research A4): sendControlFrame is a free function
// with no session handle, and control frames are rare, so process-global
// monotonicity is sufficient.
var controlFrameSeq uint32

// nextControlSeq returns the next control-frame sequence number. It is a
// package-level function var (PATTERNS.md testability seam) so tests could
// swap it; production always uses atomic.AddUint32 on controlFrameSeq.
var nextControlSeq = func() uint32 { return atomic.AddUint32(&controlFrameSeq, 1) }

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
	Limit          int    `json:"limit"`
	Category       string `json:"category"`
	Units          string `json:"units"`
	FetchLockHours int    `json:"fetch_lock_hours"`
}

// unlockCardsParams are the parameters for clearing FSRS fetch locks.
type unlockCardsParams struct {
	Category string `json:"category"`
	Unit     string `json:"unit"`
}

// sessionReadyResponse is the control frame response for successful session start.
type sessionReadyResponse struct {
	Type      string   `json:"type"`
	SessionID string   `json:"session_id"`
	Words     []string `json:"words"`
	Category  string   `json:"category"`
}

// selectDueCardsResultResponse is the control frame response for FSRS queries.
type selectDueCardsResultResponse struct {
	Type   string `json:"type"`
	Result struct {
		Cards []pybridge.DueCard `json:"cards"`
	} `json:"result"`
}

// unlockResultResponse is the control frame response for fetch-lock clears.
type unlockResultResponse struct {
	Type   string `json:"type"`
	Result struct {
		Unlocked int `json:"unlocked"`
	} `json:"result"`
}

// submitReviewParams are the parameters for submitting dictation review ratings.
// Ratings maps word string → FSRS rating ("again"/"hard"/"good"/"easy").
type submitReviewParams struct {
	Ratings  map[string]string `json:"ratings"`
	Category string            `json:"category"`
}

// submitAnswerParams are the parameters for submitting a student's spoken answer.
type submitAnswerParams struct {
	WordID     string `json:"word_id"`
	Transcript string `json:"transcript"`
}

// answerResultResponse is the control frame response for answer judgment.
type answerResultResponse struct {
	Type     string `json:"type"`
	WordID   string `json:"word_id"`
	Correct  bool   `json:"correct"`
	Expected string `json:"expected"`
	Heard    string `json:"heard"`
	Rating   string `json:"rating"` // "good" or "again"
}

// submitReviewResultResponse wraps the FSRS ReviewResult for the client.
type submitReviewResultResponse struct {
	Type   string                `json:"type"`
	Result pybridge.ReviewResult `json:"result"`
}

// errorResponse is the control frame response for errors.
type errorResponse struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ttsHandlers returns the per-MsgType handler map handed to ws.NewRouter.
// It composes the FSRS service, TTS generator, and streamer to deliver the
// end-to-end TTS audio streaming pipeline.
func ttsHandlers(manager *session.Manager, fsrsSvc fsrs.FSRSService, logger infra.Logger) map[ws.MessageType]ws.Handler {
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
				return handleStartSession(ctx, client, control.Params, manager, logger)

			case "play_word":
				return handlePlayWord(ctx, client, control.Params, manager, logger)

			case "next_word":
				return handleNextWord(ctx, client, control.Params, manager, logger)

			case "pause":
				return handlePause(ctx, client, control.Params, manager, logger)

			case "resume":
				return handleResume(ctx, client, control.Params, manager, logger)

			case "repeat":
				return handleRepeat(ctx, client, control.Params, manager, logger)

			case "select_due_cards":
				return handleSelectDueCards(ctx, client, control.Params, fsrsSvc, logger)

			case "submit_review":
				return handleSubmitReview(ctx, client, control.Params, manager, fsrsSvc, logger)

			case "unlock_cards":
				return handleUnlockCards(ctx, client, control.Params, fsrsSvc, logger)

			case "submit_answer":
				return handleSubmitAnswer(ctx, client, control.Params, manager, logger)

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

// handleStartSession delegates to the session Manager which handles FSRS
// lock acquisition, TTS generation, and session lifecycle internally.
func handleStartSession(ctx context.Context, client *ws.Client, params json.RawMessage, manager *session.Manager, logger infra.Logger) error {
	var p startSessionParams
	if err := json.Unmarshal(params, &p); err != nil {
		logger.Warn("invalid start_session params", infra.ErrField(err))
		sendErrorFrame(client, "invalid start_session params")
		return nil
	}

	// Enhanced diagnostic logging before session creation
	logger.Info("attempting session creation",
		infra.F("client_id", clientConnID(client)),
		infra.F("limit", p.Limit),
		infra.F("category", p.Category),
	)

	s, err := manager.NewSession(ctx, client, clientConnID(client), fsrs.SelectOpts{
		Limit:    p.Limit,
		Category: p.Category,
	})

	// Log session creation outcome with details
	if err != nil {
		// Error case handled in the next block
		logger.Warn("session creation returned error",
			infra.F("client_id", clientConnID(client)),
			infra.F("request_params", fmt.Sprintf("limit=%d, category=%s", p.Limit, p.Category)),
		)
	} else {
		logger.Info("session creation succeeded",
			infra.F("session_id", s.ID()),
			infra.F("client_id", clientConnID(client)),
			infra.F("words_count", len(s.Words())),
		)
	}
	if err != nil {
		// Enhanced error logging with detailed diagnostics
		logger.Error("session creation failed",
			infra.F("client_id", clientConnID(client)),
			infra.F("limit", p.Limit),
			infra.F("category", p.Category),
			infra.F("error_type", fmt.Sprintf("%T", err)),
			infra.F("error_message", err.Error()),
			infra.ErrField(err),
		)

		// Send specific error message to client
		errorMsg := "session creation failed"
		if err.Error() != "" {
			// Provide more specific error for common cases
			switch {
			case err.Error() == "no due words":
				errorMsg = "no available words for dictation - please add vocabulary first"
			case strings.Contains(err.Error(), "fetch lock"):
				errorMsg = "vocabulary database is locked - please try again"
			default:
				errorMsg = fmt.Sprintf("session creation failed: %s", err.Error())
			}
		}

		sendErrorFrame(client, errorMsg)
		return nil
	}

	// Send session_ready response with the server-generated session ID and words.
	response := sessionReadyResponse{
		Type:      "session_ready",
		SessionID: s.ID(),
		Words:     s.Words(),
		Category:  s.Category(),
	}
	if err := sendControlFrame(client, ws.MsgTypeControl, response); err != nil {
		logger.Warn("failed to send session_ready response",
			infra.F("session", s.ID()),
			infra.ErrField(err),
		)
	}

	return nil
}

// handlePlayWord pushes a play_word event onto the session's event channel.
// The session event loop (Plan 02) owns the StreamWord call per D-13.
func handlePlayWord(ctx context.Context, client *ws.Client, params json.RawMessage, manager *session.Manager, logger infra.Logger) error {
	var p playWordParams
	if err := json.Unmarshal(params, &p); err != nil {
		logger.Warn("invalid play_word params", infra.ErrField(err))
		sendErrorFrame(client, "invalid play_word params")
		return nil
	}

	manager.Push(clientConnID(client), session.SessionEvent{
		Type:        session.EventControlFrame,
		ControlType: "play_word",
		WordID:      p.WordID,
	})

	return nil
}

// handleNextWord pushes a next_word event onto the session's event channel.
// Per the FSM (Phase 7, fsm.go), DICTATING + next_word advances to the next
// word in the server-owned queue (LISTENING); the event loop's onTransition
// then spawns StreamWord for the new wordIndex. This is the multi-word
// sequential-playback path that was missing in 07-03's handler rewiring.
//
// WordID is advisory — the session's wordIndex is authoritative because the
// queue order comes from SelectDueCards (D-13: the session owns the word queue
// and current index). The client sends high-level intents, not low-level
// transitions.
func handleNextWord(ctx context.Context, client *ws.Client, params json.RawMessage, manager *session.Manager, logger infra.Logger) error {
	var p playWordParams
	if err := json.Unmarshal(params, &p); err != nil {
		logger.Warn("invalid next_word params", infra.ErrField(err))
		sendErrorFrame(client, "invalid next_word params")
		return nil
	}

	manager.Push(clientConnID(client), session.SessionEvent{
		Type:        session.EventControlFrame,
		ControlType: "next_word",
		WordID:      p.WordID,
	})

	return nil
}

// handlePause pushes a pause event onto the session's event channel.
// Ticket 02 DICT-07: student pauses dictation mid-word.
func handlePause(ctx context.Context, client *ws.Client, params json.RawMessage, manager *session.Manager, logger infra.Logger) error {
	manager.Push(clientConnID(client), session.SessionEvent{
		Type:        session.EventControlFrame,
		ControlType: "pause",
	})
	return nil
}

// handleResume pushes a resume event onto the session's event channel.
// Ticket 02 DICT-07: student resumes from pause.
func handleResume(ctx context.Context, client *ws.Client, params json.RawMessage, manager *session.Manager, logger infra.Logger) error {
	manager.Push(clientConnID(client), session.SessionEvent{
		Type:        session.EventControlFrame,
		ControlType: "resume",
	})
	return nil
}

// handleRepeat pushes a repeat event onto the session's event channel.
// Ticket 02 DICT-07: student requests the current word to be replayed.
func handleRepeat(ctx context.Context, client *ws.Client, params json.RawMessage, manager *session.Manager, logger infra.Logger) error {
	manager.Push(clientConnID(client), session.SessionEvent{
		Type:        session.EventControlFrame,
		ControlType: "repeat",
	})
	return nil
}

// clientConnID derives a stable connection identifier from the client pointer.
// Each WebSocket connection has a unique *ws.Client instance, so the pointer
// address serves as a connection-scoped ID for the session registry.
func clientConnID(client *ws.Client) string {
	return fmt.Sprintf("%p", client)
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

// handleSubmitReview submits dictation ratings to FSRS for scheduling updates.
// Delegates to fsrsSvc.UpdateRatings which calls vocab_manager.py review subcommand.
func handleSubmitReview(ctx context.Context, client *ws.Client, params json.RawMessage, manager *session.Manager, fsrsSvc fsrs.FSRSService, logger infra.Logger) error {
	var p submitReviewParams
	if err := json.Unmarshal(params, &p); err != nil {
		logger.Warn("invalid submit_review params", infra.ErrField(err))
		sendErrorFrame(client, "invalid submit_review params")
		return nil
	}

	if len(p.Ratings) == 0 {
		sendErrorFrame(client, "ratings map is empty")
		return nil
	}

	result, err := fsrsSvc.UpdateRatings(ctx, p.Ratings, p.Category)
	if err != nil {
		logger.Warn("submit_review failed",
			infra.F("words", len(p.Ratings)),
			infra.ErrField(err),
		)
		sendErrorFrame(client, "FSRS review update failed")
		return nil
	}

	response := submitReviewResultResponse{
		Type:   "submit_review_result",
		Result: result,
	}

	if err := sendControlFrame(client, ws.MsgTypeControl, response); err != nil {
		logger.Warn("failed to send submit_review_result", infra.ErrField(err))
	}

	// Push EventParentReview to drive WAITING_PARENT→FEEDBACK transition, which triggers session_complete frame.
	manager.Push(clientConnID(client), session.SessionEvent{
		Type: session.EventParentReview,
	})

	return nil
}

// handleUnlockCards clears FSRS fetch locks via fsrsSvc.UnlockCards
// (WebSocket test interface support).
func handleUnlockCards(ctx context.Context, client *ws.Client, params json.RawMessage, fsrsSvc fsrs.FSRSService, logger infra.Logger) error {
	var p unlockCardsParams
	if err := json.Unmarshal(params, &p); err != nil {
		logger.Warn("invalid unlock_cards params", infra.ErrField(err))
		sendErrorFrame(client, "invalid unlock_cards params")
		return nil
	}

	n, err := fsrsSvc.UnlockCards(ctx, fsrs.UnlockOpts{
		Category: p.Category,
		Unit:     p.Unit,
	})
	if err != nil {
		logger.Warn("unlock_cards failed", infra.ErrField(err))
		sendErrorFrame(client, "unlock failed")
		return nil
	}

	response := unlockResultResponse{
		Type: "unlock_result",
	}
	response.Result.Unlocked = n

	if err := sendControlFrame(client, ws.MsgTypeControl, response); err != nil {
		logger.Warn("failed to send unlock_result", infra.ErrField(err))
	}

	return nil
}

// handleSubmitAnswer processes a student's spoken answer, compares it to the
// expected word, and returns the judgment. Does NOT push FSM events — the
// client sends next_word separately to advance the session.
func handleSubmitAnswer(ctx context.Context, client *ws.Client, params json.RawMessage, manager *session.Manager, logger infra.Logger) error {
	var p submitAnswerParams
	if err := json.Unmarshal(params, &p); err != nil {
		logger.Warn("invalid submit_answer params", infra.ErrField(err))
		sendErrorFrame(client, "invalid submit_answer params")
		return nil
	}

	// Look up the session for this connection
	connID := clientConnID(client)
	s := manager.Lookup(connID)
	if s == nil {
		sendErrorFrame(client, "no active session")
		return nil
	}

	// Guard: must be in DICTATING state
	if s.State() != database.StateDictating {
		logger.Warn("submit_answer in wrong state",
			infra.F("state", string(s.State())),
			infra.F("conn_id", connID),
		)
		sendErrorFrame(client, "not in dictating state")
		return nil
	}

	// Get the current word from the session
	currentWord := s.CurrentWord()
	if currentWord == "" {
		sendErrorFrame(client, "no current word")
		return nil
	}

	// Determine language from session config (default to english)
	lang := "en"
	if s.Category() == "chinese" || s.Category() == "zh" {
		lang = "zh"
	}

	// Compare
	correct, rating := compareAnswer(currentWord, p.Transcript, lang)

	logger.Info("answer judged",
		infra.F("word", currentWord),
		infra.F("transcript", p.Transcript),
		infra.F("correct", correct),
		infra.F("rating", rating),
	)

	response := answerResultResponse{
		Type:     "answer_result",
		WordID:   p.WordID,
		Correct:  correct,
		Expected: currentWord,
		Heard:    p.Transcript,
		Rating:   rating,
	}

	if err := sendControlFrame(client, ws.MsgTypeControl, response); err != nil {
		logger.Warn("failed to send answer_result", infra.ErrField(err))
	}

	return nil
}

// compareAnswer compares a student's spoken transcript against the expected word.
// Returns (correct, rating). For now uses simple normalized string comparison.
// Language-aware: en strips non-alphanumeric, zh strips punctuation/whitespace only.
func compareAnswer(expected, transcript, lang string) (bool, string) {
	normExpected := normalizeForCompare(expected, lang)
	normTranscript := normalizeForCompare(transcript, lang)
	if normExpected == normTranscript {
		return true, "good"
	}
	return false, "again"
}

// normalizeForCompare normalizes a word for comparison based on language.
func normalizeForCompare(word, lang string) string {
	w := strings.ToLower(strings.TrimSpace(word))
	if lang == "zh" || lang == "zh-CN" || lang == "chinese" {
		// Chinese: strip punctuation and whitespace only (preserve CJK chars)
		w = strings.ReplaceAll(w, " ", "")
		w = strings.ReplaceAll(w, "，", "")
		w = strings.ReplaceAll(w, "。", "")
		w = strings.ReplaceAll(w, "！", "")
		w = strings.ReplaceAll(w, "？", "")
		w = strings.ReplaceAll(w, ",", "")
		w = strings.ReplaceAll(w, ".", "")
		w = strings.ReplaceAll(w, "!", "")
		w = strings.ReplaceAll(w, "?", "")
		return w
	}
	// English: strip non-alphanumeric
	var b strings.Builder
	for _, r := range w {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sendControlFrame is a helper for sending JSON control frames.
func sendControlFrame(client *ws.Client, msgType ws.MessageType, payload interface{}) error {
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	header := &ws.FrameHeader{
		Version:    ws.ProtocolVersion,
		MsgType:    msgType,
		Sequence:   nextControlSeq(),
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
