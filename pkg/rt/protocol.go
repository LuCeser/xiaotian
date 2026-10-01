// Package rt implements the real-time tutor interaction layer for the R0
// minimal loop (ADR-0002): a greenfield /rt WebSocket endpoint plus the
// turn-management contracts built on top of it. The legacy /ws binary frame
// protocol (pkg/ws) is frozen as the RT-10 degraded fallback and is neither
// imported nor extended here.
//
// Wire conventions frozen by this file (the single source of truth; the
// tutor frontend rt-client.js and the mock e2e suite follow it field by
// field):
//
//   - Control messages are JSON carried in WebSocket TextMessage frames.
//   - Audio is bare PCM carried in WebSocket BinaryMessage frames:
//     client uplink is s16le (little-endian signed 16-bit) mono at the
//     sample rate negotiated in session_init — R0 accepts only 16000 Hz,
//     and the client declares it rather than the server assuming the iPad
//     captures at 16 kHz (iOS typically captures at 44.1/48 kHz; the client
//     downsamples before sending).
//   - Server downlink TTS audio sample rate is declared via
//     session_ready.tts.sampleRate (TTSOutputSampleRate).
//
// Message-type JSON keys use snake_case (session_id, turn_id, t_client_ms);
// nested audio-spec keys keep the camelCase shape of the session_init
// example (sampleRate, format, channels).
package rt

import (
	"encoding/json"

	"github.com/pkg/errors"
)

// TTSOutputSampleRate is the sample rate the MiMo TTS pcm16 stream actually
// emits. Per the conclusion documented at pkg/tts/mimo_synth.go, MiMo pins
// pcm16 output at 24 kHz 16-bit mono regardless of the sample rate requested
// in the TTS request, so the transport advertises 24000 in
// session_ready.tts.sampleRate and the client plays downlink audio at that
// rate.
const TTSOutputSampleRate = 24000

// Client → server control message type constants.
const (
	TypeSessionInit = "session_init"
	TypeInterrupt   = "interrupt"
	TypeSentenceAck = "sentence_ack"
	TypePing        = "ping"
	TypeEndSession  = "end_session"
)

// Server → client control message type constants.
const (
	TypeSessionReady    = "session_ready"
	TypeTurnStart       = "turn_start"
	TypeTranscript      = "transcript"
	TypeTeacherSentence = "teacher_sentence"
	TypeAudioStart      = "audio_start"
	TypeAudioEnd        = "audio_end"
	TypeLessonUpdate    = "lesson_update"
	TypeInterruptAck    = "interrupt_ack"
	TypePong            = "pong"
	TypeState           = "state"
	TypeError           = "error"
	TypeSessionEnded    = "session_ended"
)

// StateMsg.Value vocabulary for the student-facing connection state.
const (
	StateListening  = "listening"
	StateProcessing = "processing"
	StateSpeaking   = "speaking"
)

// Error codes carried by ErrorMsg.Code.
const (
	// ErrCodeUnsupportedFormat: session_init audio spec rejected by
	// ValidateAudioInit (R0 only accepts 16000/s16le/1ch).
	ErrCodeUnsupportedFormat = "unsupported_format"
	// ErrCodeVoiceUnavailable: no MIMO_API_KEY configured server-side, the
	// voice path is disabled (RT-10 degraded semantics).
	ErrCodeVoiceUnavailable = "voice_unavailable"
	// ErrCodeProviderFailed: an ASR/LLM/TTS provider call failed.
	ErrCodeProviderFailed = "provider_failed"
	// ErrCodeMicUnavailable: client-reported microphone failure relayed
	// through the session (client-detected, server only relays visibility).
	ErrCodeMicUnavailable = "mic_unavailable"
	// ErrCodeInvalidMessage: unparseable or unknown control message.
	ErrCodeInvalidMessage = "invalid_message"
	// ErrCodeSessionClosed: operation referenced an already-closed session.
	ErrCodeSessionClosed = "session_closed"
)

// Sentinel errors carrying the protocol error codes above. Callers map them
// to ErrorMsg.Code with ErrorCode; identity is preserved through errors.Is
// even after Wrapf.
var (
	// ErrUnsupportedAudioFormat is returned by ValidateAudioInit for any
	// audio spec other than 16000/s16le/1ch (code: unsupported_format).
	ErrUnsupportedAudioFormat = errors.New("rt: unsupported audio format")

	// ErrInvalidControlMessage is returned by ParseControl for malformed
	// JSON or an unknown message type (code: invalid_message).
	ErrInvalidControlMessage = errors.New("rt: invalid control message")
)

// requiredInput pins the only uplink audio configuration R0 accepts. The
// client declares its (post-downsample) capture rate in session_init and the
// server verifies against these values — the rate is negotiated and checked,
// never assumed (13-CONTEXT locked decision; Pitfall 5).
const (
	requiredSampleRate = 16000
	requiredFormat     = "s16le"
	requiredChannels   = 1
)

// AudioSpec describes one audio direction's PCM parameters. It is echoed by
// the server in session_ready to confirm the negotiated uplink values. Field
// names follow the session_init wire example (camelCase inside the audio
// object).
type AudioSpec struct {
	// SampleRate is the sample rate in Hz (uplink R0: 16000).
	SampleRate int `json:"sampleRate"`
	// Format is the PCM format ("s16le" — bare little-endian signed 16-bit).
	Format string `json:"format"`
	// Channels is the channel count (R0: 1, mono).
	Channels int `json:"channels"`
}

// TTSSpec declares the server downlink TTS audio parameters in
// session_ready.tts. Only the sample rate varies (format is s16le mono by
// construction of the pcm16 stream), so the object carries exactly one
// field.
type TTSSpec struct {
	// SampleRate is the downlink TTS PCM sample rate (TTSOutputSampleRate).
	SampleRate int `json:"sampleRate"`
}

// LessonView is the lesson snapshot embedded in session_ready. It is a
// read-only projection of the server-owned lesson state (13-01); lesson
// writes only happen through tool execution.
type LessonView struct {
	// LessonID identifies the lesson ("id" on the wire per the
	// session_ready example).
	LessonID string `json:"id"`
	// Version is the lesson state version (optimistic-concurrency counter).
	Version int `json:"version"`
	// CurrentIndex is the index of the word the student is currently on.
	CurrentIndex int `json:"current_index"`
	// Words lists the lesson's words in order (R0: 3 fixed words).
	Words []string `json:"words"`
}

// SessionInitMsg is the first client → server message. The client declares
// the audio format it will uplink (post-downsample); the server validates
// with ValidateAudioInit and answers with SessionReadyMsg or an
// unsupported_format ErrorMsg.
type SessionInitMsg struct {
	Type  string    `json:"type"`
	Audio AudioSpec `json:"audio"`
}

// InterruptMsg reports a client-detected interruption while the teacher was
// speaking. Detection is client-side (playback-time local energy) because
// the server cannot distinguish speaker echo from a real voice (Pattern 3).
// Epoch is the epoch the client was playing; PlayedUntilMs/LastSentenceID
// describe the actually-played position so history can be trimmed to what
// the student really heard (Pitfall 9).
type InterruptMsg struct {
	Type           string `json:"type"`
	Epoch          uint64 `json:"epoch"`
	LastSentenceID string `json:"last_sentence_id"`
	PlayedUntilMs  int64  `json:"played_until_ms"`
}

// SentenceAckMsg confirms the client actually started/finished playing one
// teacher sentence. Acks are the ground truth for "actually played" history
// trimming and RT-04 first-audible measurement.
type SentenceAckMsg struct {
	Type       string `json:"type"`
	SentenceID string `json:"sentence_id"`
	Epoch      uint64 `json:"epoch"`
}

// PingMsg is a clock-offset estimation probe (RT-04). The client may send it
// at any time; the server replies immediately with PongMsg echoing
// t_client_ms alongside the server's monotonic nanosecond stamp. The client
// estimates clock offset as RTT/2-aligned against t_server_mono_ns and never
// subtracts cross-machine clocks directly.
type PingMsg struct {
	Type      string `json:"type"`
	TClientMs int64  `json:"t_client_ms"`
}

// EndSessionMsg is the client's explicit goodbye. The server releases the
// session (RT-10/SC7 server half) and answers SessionEndedMsg.
type EndSessionMsg struct {
	Type string `json:"type"`
}

// SessionReadyMsg answers a valid SessionInitMsg. Audio echoes the confirmed
// uplink values; TTS declares the downlink sample rate; Lesson carries the
// initial lesson snapshot.
type SessionReadyMsg struct {
	Type      string     `json:"type"`
	SessionID string     `json:"session_id"`
	Audio     AudioSpec  `json:"audio"`
	TTS       TTSSpec    `json:"tts"`
	Lesson    LessonView `json:"lesson"`
}

// TurnStartMsg announces a new turn (one student utterance → one teacher
// response cycle) and carries the epoch cancellation token for everything
// that follows in the turn.
type TurnStartMsg struct {
	Type   string `json:"type"`
	TurnID uint64 `json:"turn_id"`
	Epoch  uint64 `json:"epoch"`
}

// TranscriptMsg relays the recognized student speech back as text.
type TranscriptMsg struct {
	Type   string `json:"type"`
	TurnID uint64 `json:"turn_id"`
	Text   string `json:"text"`
}

// TeacherSentenceMsg sends one teacher sentence's text BEFORE its audio
// chunks (subtitle-first), so captions render ahead of playback.
type TeacherSentenceMsg struct {
	Type       string `json:"type"`
	TurnID     uint64 `json:"turn_id"`
	Epoch      uint64 `json:"epoch"`
	SentenceID string `json:"sentence_id"`
	Text       string `json:"text"`
}

// AudioStartMsg announces the first audio chunk of one sentence (subtitles
// already delivered via TeacherSentenceMsg).
type AudioStartMsg struct {
	Type       string `json:"type"`
	TurnID     uint64 `json:"turn_id"`
	Epoch      uint64 `json:"epoch"`
	SentenceID string `json:"sentence_id"`
}

// AudioEndMsg announces the last audio chunk of one sentence. Same shape as
// AudioStartMsg.
type AudioEndMsg struct {
	Type       string `json:"type"`
	TurnID     uint64 `json:"turn_id"`
	Epoch      uint64 `json:"epoch"`
	SentenceID string `json:"sentence_id"`
}

// LessonUpdateMsg reports a committed lesson-state write (after tool
// execution) — the progress/UI consistency half of RT-07. RequestID is the
// idempotency key the tool executed under (the MiMo tool_call id).
type LessonUpdateMsg struct {
	Type         string `json:"type"`
	LessonID     string `json:"lesson_id"`
	Version      int    `json:"version"`
	CurrentIndex int    `json:"current_index"`
	RequestID    string `json:"request_id"`
}

// InterruptAckMsg confirms an interruption took effect: the invalidated
// epoch, the new epoch, and the played position the server recorded from the
// client's report.
type InterruptAckMsg struct {
	Type           string `json:"type"`
	OldEpoch       uint64 `json:"old_epoch"`
	NewEpoch       uint64 `json:"new_epoch"`
	LastSentenceID string `json:"last_sentence_id"`
	PlayedUntilMs  int64  `json:"played_until_ms"`
}

// PongMsg answers PingMsg. TClientMs echoes the probe's t_client_ms
// verbatim; TServerMonoNs is the server's monotonic stamp sharing the same
// process anchor as every rt_event t_mono_ns (see EventRecorder.MonoNs).
// The client derives a per-session clock-offset estimate from
// t_client_ms/t_server_mono_ns plus RTT/2; cross-machine clocks are never
// subtracted directly.
type PongMsg struct {
	Type          string `json:"type"`
	TClientMs     int64  `json:"t_client_ms"`
	TServerMonoNs int64  `json:"t_server_mono_ns"`
}

// StateMsg announces the student-facing connection state (listening /
// processing / speaking).
type StateMsg struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// ErrorMsg reports a recoverable or fatal condition with a stable machine
// code (the ErrCode* constants). Message is operator/diagnostic text and
// must not leak credentials or internal paths.
type ErrorMsg struct {
	Type        string `json:"type"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	Recoverable bool   `json:"recoverable"`
}

// SessionEndedMsg is the server's final message before closing the
// connection (explicit end_session, idle timeout, or fatal error).
type SessionEndedMsg struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// ParseControl decodes one control message (the payload of a WebSocket
// TextMessage frame) into its concrete message struct. It recognizes the
// full type vocabulary so both directions round-trip in tests and conformance
// harnesses; which types a given peer may actually send is enforced by the
// session manager (pkg/rt turn manager), not by the transport ("transport
// only moves bytes"). Malformed JSON, a missing type, or an unknown type
// return an error wrapping ErrInvalidControlMessage. Oversized messages are
// rejected earlier by the connection read limit (conn.go), not here.
func ParseControl(data []byte) (any, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
	}
	switch probe.Type {
	case TypeSessionInit:
		var m SessionInitMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeInterrupt:
		var m InterruptMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeSentenceAck:
		var m SentenceAckMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypePing:
		var m PingMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeEndSession:
		var m EndSessionMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeSessionReady:
		var m SessionReadyMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeTurnStart:
		var m TurnStartMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeTranscript:
		var m TranscriptMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeTeacherSentence:
		var m TeacherSentenceMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeAudioStart:
		var m AudioStartMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeAudioEnd:
		var m AudioEndMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeLessonUpdate:
		var m LessonUpdateMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeInterruptAck:
		var m InterruptAckMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypePong:
		var m PongMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeState:
		var m StateMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeError:
		var m ErrorMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeSessionEnded:
		var m SessionEndedMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	default:
		return nil, errors.Wrapf(ErrInvalidControlMessage, "unknown type %q", probe.Type)
	}
}

// ValidateAudioInit checks a session_init audio declaration against the only
// uplink configuration R0 supports: 16000 Hz s16le mono. Anything else
// returns an error wrapping ErrUnsupportedAudioFormat (code
// unsupported_format) so the session manager can answer with a precise
// ErrorMsg instead of misinterpreting audio. The sample rate is declared by
// the client and verified here — never assumed (Pitfall 5).
func ValidateAudioInit(a AudioSpec) error {
	if a.SampleRate != requiredSampleRate || a.Format != requiredFormat || a.Channels != requiredChannels {
		return errors.Wrapf(ErrUnsupportedAudioFormat,
			"got %dHz format=%q channels=%d, want %dHz %s %dch",
			a.SampleRate, a.Format, a.Channels,
			requiredSampleRate, requiredFormat, requiredChannels)
	}
	return nil
}

// ErrorCode maps a protocol-layer error to its ErrorMsg.Code value, so the
// session manager turns every ParseControl/ValidateAudioInit failure into a
// coded ErrorMsg instead of string matching. Unknown errors map to
// provider_failed (the generic server-side failure code).
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrUnsupportedAudioFormat):
		return ErrCodeUnsupportedFormat
	case errors.Is(err, ErrInvalidControlMessage):
		return ErrCodeInvalidMessage
	default:
		return ErrCodeProviderFailed
	}
}
