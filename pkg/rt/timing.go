package rt

import (
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Server-side timing event vocabulary — the server half of the REQUIREMENTS
// "必须记录" list. Client-side events (page_ready / auth_complete /
// first_audio_played / interrupt_start / interrupt_stop) are recorded by the
// frontend (13-05) on the client clock; the two JSONL streams are aligned by
// turn/epoch event IDs, never by subtracting cross-machine clocks.
//
// Values are frozen identifiers (they land in JSONL evidence files), not
// display strings.
const (
	// EventConnected: /rt connection upgraded and session bound.
	EventConnected = "connected"
	// EventSpeechStarted: VAD detected speech onset (server).
	EventSpeechStarted = "speech_started"
	// EventSpeechEnded: VAD closed an utterance (server).
	EventSpeechEnded = "speech_ended"
	// EventFirstAudioReceived: first TTS chunk of a turn arrived from the
	// provider (server side of the RT-04 sentence-end→first-audio path).
	EventFirstAudioReceived = "first_audio_received"
	// EventInterruptHandled: an interrupt took effect (epoch bumped, TTS
	// cancelled). Wire value is the REQUIREMENTS vocabulary's "interrupt";
	// the constant name records that this is the handled-acknowledgement
	// event, not the client's interrupt_start detection.
	EventInterruptHandled = "interrupt"
	// EventWordAnnounced: the teacher announced a word and the client
	// confirmed actual playback of that announcement (13-08 FSM).
	EventWordAnnounced = "word_announced"
	// EventWordAdvanced: the FIRST commit of one advance_word landed
	// (13-08); idempotent replays and version conflicts never emit it.
	EventWordAdvanced = "word_advanced"
	// EventTimeoutFired: the 60s per-word countdown DROVE an advance (13-09)
	// — emitted by the first commit only; a timeout that lost the race to a
	// confirm replays idempotently and never claims it advanced anything.
	EventTimeoutFired = "timeout_fired"
	// EventIntentClassified: the classification Chat produced a whitelisted
	// intent (13-08). Fields carry the intent label only — never the
	// transcript text (T-13-06b boundary). Issue #56: the dictation-stage
	// emission also correlates the turn (turn_id) and the captured target
	// (word_index, version) with the MODEL's intent (model_intent) beside
	// the final one, so a confirmation phrase the classifier misjudged —
	// and any later correction of it — stays diagnosable from the evidence
	// stream alone; a collapsed classification omits model_intent.
	EventIntentClassified = "intent_classified"
	// EventLessonComplete: the lesson's natural completion sequence ran
	// (summary played/acknowledged or degraded, session ended).
	EventLessonComplete = "lesson_complete"
	// EventReviewWritten: the review write returned; fields carry the real
	// requested/updated/missing counts (13-08 honest completion) plus the
	// U-9 hint accounting aggregates hint_words/hint_total (2026-10-06).
	EventReviewWritten = "review_written"
	// EventHintGiven: the teacher granted one hint request for a word
	// (U-9, 2026-10-06). Fields carry word_index and hint_count (the Nth
	// hint for that word) — count-class values only, never word text or
	// transcript content (T-13-06b boundary). Frozen JSONL identifier.
	EventHintGiven = "hint_given"
	// EventPlaybackAckTimeout: the bounded wait for a playback-completion
	// ack expired (13-08); fields carry the purpose and the waited ms.
	EventPlaybackAckTimeout = "playback_ack_timeout"
	// EventSentenceAckRejected: a sentence_ack was rejected (empty id,
	// unknown/stale, or duplicate) — sanitized diagnostic only, zero state
	// side effects (T-13-15b).
	EventSentenceAckRejected = "sentence_ack_rejected"
	// EventSessionEnd: session released (explicit end, idle, or failure).
	EventSessionEnd = "session_end"
	// EventError: a visible failure occurred (RT-10 evidence).
	EventError = "error"
	// EventSubjectAsk (Issue #55): an auto word classroom found BOTH
	// subjects holding due words and the clarify question went out; the
	// retry=true field marks a re-ask after an unusable spoken answer.
	EventSubjectAsk = "subject_ask"
	// EventSubjectPicked (Issue #55): the child's clarify answer (via
	// button or voice) bound the round's subject. Fields carry the picked
	// category and the channel only — never the transcript text.
	EventSubjectPicked = "subject_picked"
)

// EventRecorder emits the server-side timing event stream for one session
// as structured rt_event log entries. All events for a session share one
// process anchor so their t_mono_ns values are mutually comparable.
type EventRecorder struct {
	// anchor is the in-process monotonic reference fixed at construction.
	// time.Since(anchor) is monotonic and immune to wall-clock steps (NTP),
	// unlike time.Now().UnixNano().
	anchor    time.Time
	logger    infra.Logger
	sessionID string
}

// NewEventRecorder builds the session's event recorder. A nil logger is
// replaced with the no-op logger (dependency-injection zero value, D-42), so
// callers constructing recorders in tests never need a logger to avoid a
// panic.
func NewEventRecorder(logger infra.Logger, sessionID string) *EventRecorder {
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &EventRecorder{
		logger:    logger,
		sessionID: sessionID,
		anchor:    time.Now(),
	}
}

// SessionID returns the session id every Emit of this recorder stamps —
// the one id a caller outside the rt_event stream (e.g. Issue #94 U5's
// thinking-timeout Warn) needs to correlate its entry with the stream.
func (r *EventRecorder) SessionID() string {
	return r.sessionID
}

// MonoNs returns nanoseconds elapsed since the recorder's anchor. This is a
// MONOTONIC, in-process relative timestamp:
//
//   - It never goes backwards and is unaffected by NTP steps or manual clock
//     changes (it is time.Since, not UnixNano).
//   - It shares its anchor with every rt_event t_mono_ns and with the
//     PongMsg.t_server_mono_ns stamp, so pong-based clock-offset estimation
//     aligns with the event stream.
//   - It is NOT comparable across machines or processes. Cross-machine
//     alignment uses event IDs plus the client's per-session clock_offset
//     estimate (RTT/2 against pong); never subtract a client timestamp from
//     a server one directly.
func (r *EventRecorder) MonoNs() int64 {
	return time.Since(r.anchor).Nanoseconds()
}

// Emit logs one rt_event entry carrying the event name, the session ID, and
// the monotonic t_mono_ns stamp, followed by the caller's fields.
//
// Redaction boundary (T-13-06b): fields may only carry event-name / ID /
// duration / version / count class values — turn_id, epoch, sentence_id,
// duration_ms, version, and the like. Never pass transcript text, audio
// payloads, or credential-bearing values; the rt_event stream is retained as
// desensitized evidence, and this method is the single choke point it flows
// through.
func (r *EventRecorder) Emit(event string, fields ...infra.Field) {
	all := make([]infra.Field, 0, 3+len(fields))
	all = append(all,
		infra.F("event", event),
		infra.F("session_id", r.sessionID),
		infra.F("t_mono_ns", r.MonoNs()),
	)
	all = append(all, fields...)
	r.logger.Info("rt_event", all...)
}
