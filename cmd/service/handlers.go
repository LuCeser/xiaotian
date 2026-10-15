package main

// This file holds the WebSocket message handlers wired into the runtime by
// main.go. Phase 6 delivers the TTS-aware control frame handlers that
// orchestrate FSRS word selection, TTS batch generation, and audio frame
// streaming.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/grading"
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

// submitAnswersParams are the parameters for a batch answer submission
// (Ticket 05). Answers maps word → the student's written answer for it; the
// map may be partial — unanswered words grade incorrect (classroom
// semantics: a blank is wrong).
type submitAnswersParams struct {
	Answers map[string]string `json:"answers"`
}

// batchWordResult is one word's judgment inside a batch_result frame. The
// FSRS rating ("good"/"hard"/"again") is fully derivable from Correct and
// NearMiss, so it travels only in the server-side UpdateRatings call — not
// on the wire. Diff is the full char-level alignment (Ticket 06) the client
// renders as per-character highlights.
type batchWordResult struct {
	Word     string           `json:"word"`
	Heard    string           `json:"heard"` // "" when the word was left unanswered
	Correct  bool             `json:"correct"`
	NearMiss bool             `json:"near_miss"`
	Diff     []grading.CharOp `json:"diff,omitempty"`
}

// batchResultResponse is the control frame reply to a submit_answers trigger
// (Ticket 05): per-word judgments in dictation-queue order plus summary
// counts. The session_complete frame (SUBMISSION → FEEDBACK) follows.
type batchResultResponse struct {
	Type         string            `json:"type"` // "batch_result"
	Results      []batchWordResult `json:"results"`
	CorrectCount int               `json:"correct_count"`
	Total        int               `json:"total"`
}

// answerResultResponse is the control frame response for answer judgment.
type answerResultResponse struct {
	Type     string           `json:"type"`
	WordID   string           `json:"word_id"`
	Correct  bool             `json:"correct"`
	Expected string           `json:"expected"`
	Heard    string           `json:"heard"`
	Rating   string           `json:"rating"`         // "good" | "hard" | "again"
	NearMiss bool             `json:"near_miss"`      // Ticket 06: one-edit-away wrong answer
	Diff     []grading.CharOp `json:"diff,omitempty"` // Ticket 06: char-level alignment
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
// end-to-end TTS audio streaming pipeline. voice may be nil (voice commands
// disabled — no MIMO_API_KEY): the audio route no-ops and voice_command
// rejects with an error frame (see voice.go). photo may be nil the same way
// (see photo.go).
func ttsHandlers(manager *session.Manager, fsrsSvc fsrs.FSRSService, voiceCmd *voiceService, photoCmd *photoService, logger infra.Logger) map[ws.MessageType]ws.Handler {
	return map[ws.MessageType]ws.Handler{
		// MsgTypeAudio: inbound microphone PCM frames (Ticket 03 voice
		// commands). Delegates to the voice pipeline's buffer registry.
		ws.MsgTypeAudio: voiceCmd.handleAudioFrame,

		// MsgTypeImage: inbound photo-answer JPEG chunks (Ticket 04).
		// Delegates to the photo pipeline's buffer registry.
		ws.MsgTypeImage: photoCmd.handlePhotoChunk,

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

			case "submit_answers":
				// Ticket 05: batch submission — grade every queue word at once.
				return handleSubmitAnswers(ctx, client, control.Params, manager, fsrsSvc, logger)

			case "voice_command":
				// Ticket 03: student finished a push-to-talk recording; run
				// ASR + intent analysis and dispatch the FSM verb.
				return voiceCmd.handleVoiceCommand(ctx, client, control.Params)

			case "voice_cancel":
				// Ticket 03: student aborted the recording; discard the
				// buffered audio so it cannot poison the next command.
				return voiceCmd.handleVoiceCancel(ctx, client, control.Params)

			case "photo_submit":
				// Ticket 04: student uploaded a photo of the written answer;
				// run OCR + answer judging and reply photo_result.
				return photoCmd.handlePhotoSubmit(ctx, client, control.Params)

			case "photo_cancel":
				// Ticket 04: student aborted the upload (retake); discard the
				// buffered chunks so they cannot poison the next photo.
				return photoCmd.handlePhotoCancel(ctx, client, control.Params)

			case "photo_ocr":
				// Ticket 05: OCR-only photo read — the batch form fills the
				// word's input with the recognized text, no judging.
				return photoCmd.handlePhotoOCR(ctx, client, control.Params)

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
	// Ticket 07: capacity rejection gets its own friendly message — the
	// service is healthy, the student should just retry shortly.
	if errors.Is(err, session.ErrManagerAtCapacity) {
		sendErrorFrame(client, "现在学习的人有点多，请稍等一分钟再试")
		return nil
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

	// Ticket 06: the per-word completion path reads its summary aloud too
	// (the batch path speaks inside handleSubmitAnswers). good/easy ratings
	// are the correct answers; hard (near-miss) and again are not.
	if s := manager.Lookup(clientConnID(client)); s != nil {
		correct, total := 0, len(p.Ratings)
		for _, r := range p.Ratings {
			if grading.RatingCountsAsCorrect(r) {
				correct++
			}
		}
		s.SpeakFeedback(grading.Encouragement(grading.Percent(correct, total), correct, total), "chinese")
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

	// Completeness guard (Ticket 04 review): an empty transcript is an
	// incomplete submission, not a wrong answer — reject it outright so the
	// client can demand real input instead of recording a spurious "again".
	if strings.TrimSpace(p.Transcript) == "" {
		sendErrorFrame(client, "answer is empty")
		return nil
	}

	connID := clientConnID(client)
	j, reject := judgeAnswer(manager, connID, p.Transcript)
	if reject != "" {
		// Restore the pre-extraction observability: the spoken path logged
		// state mismatches (the photo path logs its own twin below).
		logger.Warn("submit_answer not judged",
			infra.F("conn_id", connID),
			infra.F("reason", reject),
		)
		sendErrorFrame(client, reject)
		return nil
	}

	logger.Info("answer judged",
		infra.F("word", j.Expected),
		infra.F("transcript", p.Transcript),
		infra.F("correct", j.Correct),
		infra.F("rating", j.Rating),
	)

	response := answerResultResponse{
		Type:     "answer_result",
		WordID:   p.WordID,
		Correct:  j.Correct,
		Expected: j.Expected,
		Heard:    p.Transcript,
		Rating:   j.Rating,
		NearMiss: j.NearMiss,
		Diff:     j.Diff,
	}

	if err := sendControlFrame(client, ws.MsgTypeControl, response); err != nil {
		logger.Warn("failed to send answer_result", infra.ErrField(err))
	}

	return nil
}

// answerJudgement is the outcome of comparing a submitted answer against the
// session's current dictation word (shared by the spoken submit_answer path
// and the photo path).
type answerJudgement struct {
	Expected string
	Lang     string // "en" | "zh" (informational)
	Correct  bool
	NearMiss bool
	Diff     []grading.CharOp
	Rating   string // FSRS rating verb ("good"/"hard"/"again")
}

// answerJudger judges a transcript against the connection's current word.
// It returns a non-empty reject message when judging is impossible (no
// active session, wrong state, no current word) — the reply frames surface
// that message. *session.Manager satisfies it via judgeAnswer; tests inject
// a plain closure (mirrors the sessionPusher seam, D-05).
type answerJudger func(connID, transcript string) (answerJudgement, string)

// judgeAnswer performs the session lookup + state guard + word comparison
// shared by the spoken (submit_answer) and photo (photo_submit, Ticket 04)
// answer paths. It returns the judgement and, when judging is impossible, a
// non-empty reject message naming the reason (no active session / not in
// dictating state / no current word). A nil manager is a "no active session"
// — the disabled-mode wiring passes nil and the reply stays graceful.
func judgeAnswer(manager *session.Manager, connID, transcript string) (answerJudgement, string) {
	if manager == nil {
		return answerJudgement{}, "no active session"
	}

	s := manager.Lookup(connID)
	if s == nil {
		return answerJudgement{}, "no active session"
	}

	// Guard: must be in DICTATING state
	if s.State() != database.StateDictating {
		return answerJudgement{}, "not in dictating state"
	}

	// Get the current word from the session
	currentWord := s.CurrentWord()
	if currentWord == "" {
		return answerJudgement{}, "no current word"
	}

	lang := langForCategory(s.Category())
	j := grading.GradeWord(currentWord, transcript, lang)
	return answerJudgement{
		Expected: currentWord,
		Lang:     lang,
		Correct:  j.Correct,
		NearMiss: j.NearMiss,
		Diff:     j.Diff,
		Rating:   j.Rating,
	}, ""
}

// langForCategory maps a session category onto the grading language selector
// (default English — the zh/en split mirrors grading.Normalize's two
// normalization regimes).
func langForCategory(category string) string {
	if category == "chinese" || category == "zh" {
		return "zh"
	}
	return "en"
}

// handleSubmitAnswers serves the Ticket 05 batch submission: validate the
// answers map, grade EVERY queue word (unanswered = incorrect, the classroom
// blank-is-wrong semantic), reply batch_result, submit the ratings to FSRS
// server-side (one call — no client roundtrip), and drive the FSM spine
// DICTATING|WAITING_PARENT → SUBMISSION → FEEDBACK. The reply is sent BEFORE
// the FSRS call and event pushes so a slow Python bridge cannot delay the
// student's feedback.
func handleSubmitAnswers(ctx context.Context, client *ws.Client, params json.RawMessage, manager *session.Manager, fsrsSvc fsrs.FSRSService, logger infra.Logger) error {
	var p submitAnswersParams
	if err := json.Unmarshal(params, &p); err != nil {
		logger.Warn("invalid submit_answers params", infra.ErrField(err))
		sendErrorFrame(client, "invalid submit_answers params")
		return nil
	}

	if manager == nil {
		sendErrorFrame(client, "no active session")
		return nil
	}
	connID := clientConnID(client)
	s := manager.Lookup(connID)
	if s == nil {
		sendErrorFrame(client, "no active session")
		return nil
	}
	// Mirror the FSM's submit_answers edges — the guard keeps the error
	// message helpful; the FSM remains the authority and would drop the
	// event anyway. The DICTATING edge is last-word-only (an early submit
	// would grade never-dictated words); DictationFinished is the FSM's own
	// edge condition, so the mirror cannot drift.
	switch s.State() {
	case database.StateWaitingParent:
	case database.StateDictating:
		if !s.DictationFinished() {
			sendErrorFrame(client, "dictation is not finished yet")
			return nil
		}
	default:
		logger.Warn("submit_answers in wrong state",
			infra.F("conn_id", connID),
			infra.F("state", string(s.State())),
		)
		sendErrorFrame(client, "session is not accepting answers")
		return nil
	}

	// Validation (Ticket 05: all answers validated before any processing):
	// non-empty map, keys limited to this session's queue, non-empty values.
	// First failure rejects the WHOLE submission — partial acceptance would
	// silently strand the rest.
	words := s.Words()
	if len(p.Answers) == 0 {
		sendErrorFrame(client, "answers map is empty")
		return nil
	}
	known := make(map[string]bool, len(words))
	for _, w := range words {
		known[w] = true
	}
	for w, text := range p.Answers {
		if !known[w] {
			sendErrorFrame(client, fmt.Sprintf("unknown word %q", w))
			return nil
		}
		if strings.TrimSpace(text) == "" {
			sendErrorFrame(client, fmt.Sprintf("answer for %q is empty", w))
			return nil
		}
	}

	// Grade in queue order so the client can render results 1..N matching
	// the dictation sequence. Unanswered words grade incorrect/again
	// (blank = wrong, classroom semantics); one-edit-away wrong answers are
	// near-misses — still incorrect, but FSRS "hard" (Ticket 06).
	lang := langForCategory(s.Category())
	response := batchResultResponse{
		Type:    "batch_result",
		Total:   len(words),
		Results: make([]batchWordResult, 0, len(words)),
	}
	ratings := make(map[string]string, len(words))
	inputs := make([]grading.WordInput, 0, len(words))
	for _, w := range words {
		heard := p.Answers[w] // "" when unanswered
		j := grading.GradeWord(w, heard, lang)
		inputs = append(inputs, grading.WordInput{Word: w, Heard: heard, Judgment: j})
		response.Results = append(response.Results, batchWordResult{
			Word: w, Heard: heard, Correct: j.Correct, NearMiss: j.NearMiss, Diff: j.Diff,
		})
		if j.Correct {
			response.CorrectCount++
		}
		ratings[w] = j.Rating
	}

	if err := sendControlFrame(client, ws.MsgTypeControl, response); err != nil {
		logger.Warn("failed to send batch_result", infra.ErrField(err))
	}

	// Ticket 06: analysis report + persistence + trends + spoken feedback.
	// All best-effort after the batch_result — grading itself already landed.
	// History is queried BEFORE persisting this round: the current session's
	// row must not re-enter its own trend list or repeat-miss counts.
	report := grading.Analyze(inputs)
	analysis := buildAnalysisResponse(s.ID(), report)
	if past, err := manager.RecentResults(ctx, trendHistoryLimit); err != nil {
		// History is an enhancement, not a gate — the report ships without it.
		logger.Warn("graded history query failed", infra.ErrField(err))
	} else {
		applyHistory(analysis, past, s.Category())
	}
	if err := sendControlFrame(client, ws.MsgTypeControl, analysis); err != nil {
		logger.Warn("failed to send analysis_result", infra.ErrField(err))
	}
	if payload, err := marshalStoredResult(s.Category(), inputs, report); err == nil {
		if err = s.RecordGradedResult(ctx, payload); err != nil {
			logger.Warn("graded result not persisted",
				infra.F("conn_id", connID),
				infra.ErrField(err),
			)
		}
	} else {
		logger.Warn("stored result marshal failed", infra.ErrField(err))
	}

	// Server-side FSRS submission (one batched call, mirrors what the client
	// did per-word via submit_review). Failure logs and continues — the
	// grading result is already delivered and stranding the student in
	// SUBMISSION over a scheduling hiccup would be worse.
	if _, err := fsrsSvc.UpdateRatings(ctx, ratings, s.Category()); err != nil {
		logger.Warn("batch FSRS ratings update failed",
			infra.F("conn_id", connID),
			infra.F("words", len(ratings)),
			infra.ErrField(err),
		)
	}

	// Ticket 06: read the encouragement aloud. The sentence is Chinese in
	// every session (the product's UI language), so the TTS voice profile is
	// always chinese. Best-effort — Session.SpeakFeedback drops failures.
	s.SpeakFeedback(report.Encouragement, "chinese")

	// FSM spine: the two pushes are order-stable on the session's single
	// event channel — SUBMISSION entry emits the status-bearing progress
	// frame, FEEDBACK entry emits session_complete.
	manager.Push(connID, session.SessionEvent{
		Type:        session.EventControlFrame,
		ControlType: "submit_answers",
	})
	manager.Push(connID, session.SessionEvent{
		Type: session.EventBatchGraded,
	})

	logger.Info("batch answers graded",
		infra.F("conn_id", connID),
		infra.F("submitted", len(p.Answers)),
		infra.F("total", response.Total),
		infra.F("correct", response.CorrectCount),
		infra.F("score", report.Score),
	)
	return nil
}

// sendControlFrame is a helper for sending JSON control frames.
func sendControlFrame(client *ws.Client, msgType ws.MessageType, payload interface{}) error {
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	header := &ws.FrameHeader{
		Version:  ws.ProtocolVersion,
		MsgType:  msgType,
		Sequence: nextControlSeq(),
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
