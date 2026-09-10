package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
)

// TTSSessionGenerator is the subset of *tts.Generator the Session uses for its
// lifecycle (batch-generate at init, cleanup at exit) plus the spoken-feedback
// synthesis (Ticket 06). *tts.Generator satisfies this interface structurally.
// Defined as a narrow seam so tests can inject a fake generator (mirrors
// tts.Sender, D-05/D-01) without constructing a real *tts.Generator with
// private pybridge internals.
type TTSSessionGenerator interface {
	// GenerateForSession batch-generates PCM audio for the word list.
	GenerateForSession(ctx context.Context, sessionID string, words []string) (map[string]string, error)
	// GenerateText synthesizes one arbitrary spoken text (the encouragement
	// sentence) into the session cache — see SpeakFeedback (Ticket 06).
	GenerateText(ctx context.Context, sessionID, text, language string) (string, error)
	// CleanupSession removes the session's TTS cache directory (idempotent).
	CleanupSession(ctx context.Context, sessionID string) error
}

// EventType represents the kind of event pushed onto a Session's event channel
// (Phase 7). Modeled on ws.MessageType (uint8 enum + String() switch).
type EventType uint8

const (
	// EventControlFrame is a client control frame (play_word / next_word),
	// carrying the verb in ControlType and the target in WordID.
	EventControlFrame EventType = 0x01
	// EventWordComplete signals that TTS streaming for the current word
	// finished (D-13 system-driven LISTENING→DICTATING transition).
	EventWordComplete EventType = 0x02
	// EventTimeout signals a per-state deadline exceeded (handled by the event
	// loop, not the FSM table). The loop exits and the D-10 defer chain fires.
	EventTimeout EventType = 0x03
	// EventCancel signals a client-driven cancel. The loop exits and the D-10
	// defer chain fires.
	EventCancel EventType = 0x04
	// EventParentReview is the parent-review event driving WAITING_PARENT→FEEDBACK.
	EventParentReview EventType = 0x05
	// EventBatchGraded signals that a batch answer submission has been
	// graded, driving SUBMISSION→FEEDBACK (Ticket 05). System-driven: pushed
	// by the submit_answers handler after the batch_result frame went out.
	EventBatchGraded EventType = 0x06
)

// String returns a human-readable name for the EventType (structured-logging
// friendly, mirrors ws.MessageType.String()).
func (e EventType) String() string {
	switch e {
	case EventControlFrame:
		return "control_frame"
	case EventWordComplete:
		return "word_complete"
	case EventTimeout:
		return "timeout"
	case EventCancel:
		return "cancel"
	case EventParentReview:
		return "parent_review"
	case EventBatchGraded:
		return "batch_graded"
	default:
		return fmt.Sprintf("unknown(0x%02x)", uint8(e))
	}
}

// SessionEvent is the event pushed onto a Session's events channel. WordID
// carries the target word for EventControlFrame (play_word) and
// EventWordComplete; ControlType carries the verb ("play_word" / "next_word")
// for EventControlFrame; Payload is the raw JSON params if any. StreamGen
// carries the stream generation stamped by spawnStreamWord; it is meaningful
// only for EventWordComplete (a system-internal event, never client input —
// client frames cannot inject this field). The event loop compares it against
// the Session's current generation to drop stale WordComplete events pushed by
// superseded (cancelled but still completing) streams.
type SessionEvent struct {
	Type        EventType
	WordID      string
	ControlType string
	Payload     json.RawMessage
	StreamGen   uint64
}

// State is a convenience alias for database.SessionState so the FSM and event
// loop can write StateIdle / StateListening without the database package prefix
// (no import cycle: database only imports infra).
type State = database.SessionState

// Sentinel errors returned by the session layer. Callers MUST detect them via
// errors.Is, never by string matching (D-04, mirrors the tts/database
// conventions). Each is wrapped at the call site so the sentinel stays
// reachable through the error chain.
var (
	// ErrInvalidTransition is returned by the FSM transition() table for an
	// (state, event) pair that is not a valid user/system-driven transition.
	ErrInvalidTransition = errors.New("session: invalid state transition")

	// ErrNoDueWords is returned by NewSession when SelectDueCards returns an
	// empty slice (D-02-a: empty means none due, not a SelectDueCards error).
	ErrNoDueWords = errors.New("session: no due words")

	// ErrSessionClosed is returned when operating on a Session whose event loop
	// has exited.
	ErrSessionClosed = errors.New("session: closed")

	// ErrManagerAlreadyRunning is returned by Manager.Start when the sweeper is
	// already running (CompareAndSwap guard).
	ErrManagerAlreadyRunning = errors.New("session: manager already running")

	// ErrManagerAtCapacity is returned by Manager.NewSession when the number
	// of active sessions already equals cfg.Session.MaxConcurrent (Ticket 07:
	// graceful degradation at the capacity limit — the student gets a friendly
	// error instead of joining a degraded, oversubscribed service).
	ErrManagerAtCapacity = errors.New("session: at capacity")
)
