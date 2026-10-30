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
	TypeSessionInit  = "session_init"
	TypeInterrupt    = "interrupt"
	TypeSentenceAck  = "sentence_ack"
	TypePing         = "ping"
	TypeEndSession   = "end_session"
	TypeReadWord     = "read_word"     // re-read a selected announced position
	TypeCheckVerdict = "check_verdict" // 13.1-01: one screen 对/错 verdict
	TypeCheckSubmit  = "check_submit"  // 13.1-01: the unified-submit request
	TypePhotoChunk   = "photo_chunk"   // 13.1-03: one base64 image chunk (check stage)
	TypePhotoCancel  = "photo_cancel"  // 13.1-03: discard the in-flight upload
	TypeSubjectPick  = "subject_pick"  // Issue #55: the child answered the subject clarify (button)
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
	TypeCheckStarted    = "check_started"  // 13.1-01: the check stage opened (D-10 unmask)
	TypeCheckVerdicts   = "check_verdicts" // 13.1-01: authoritative verdict echo (RT-07)
	TypeReviewResult    = "review_result"  // 13.1-01: honest unified-submit result (S-05)
	TypeWordRevealed    = "word_revealed"  // 13.1-01: one word's plaintext reveal
	TypeOcrResult       = "ocr_result"     // 13.1-03: per-word OCR recognition entries
	TypeSubjectAsk      = "subject_ask"    // Issue #55: both subjects have due words — ask the child
)

// StateMsg.Value vocabulary for the student-facing connection state.
const (
	StateListening  = "listening"
	StateProcessing = "processing"
	StateSpeaking   = "speaking"
	// StatePaused: the pause command has suspended the per-word timer and
	// playback, but VAD keeps listening for the resume command. Carried by
	// StateMsg.Value. (Qualified as rt.StatePaused — the frozen database
	// StatePaused = "PAUSED" constant belongs to another package and flow.)
	StatePaused = "paused"
	// StateGrading: the check panel is open; uplink is gated after the bridge.
	StateGrading = "grading"
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
	// ErrCodeLessonUnavailable: session_init could not initialize the FSRS
	// due-word fetch / classroom row (e.g. legacy lessons row with empty
	// cards, no due words with the lesson unusable). The classroom cannot
	// start; the frontend must surface an honest failure.
	ErrCodeLessonUnavailable = "lesson_unavailable"
	// ErrCodePlaybackAckTimeout: the server finished SENDING one scheduled
	// announcement but did not receive a matching actual-playback-completion
	// ack (sentence_ack) within the bounded wait derived from the total
	// duration of the outstanding scheduled PCM. This code is carried ONLY
	// by ErrorMsg.Code — it is never a SessionEndedMsg.Reason and is never
	// evidence that audio was actually played. The frontend must present an
	// honest notice and keep the scheduled audio until it drains naturally
	// or a bounded cleanup stops it.
	ErrCodePlaybackAckTimeout = "playback_ack_timeout"
	// ErrCodePhotoUnavailable: no OCR reader configured server-side, the
	// photo-check path is disabled (13.1-03; RT-10 degraded semantics — the
	// check stage keeps working manual-only).
	ErrCodePhotoUnavailable = "photo_unavailable"
	// ErrCodeDuplicateSubmit: an ABSORBED duplicate submission (13.1-08) —
	// a stage-latch replay or a cross-connection write race whose latch
	// branch fired (the duplicate= field names which: check_submit_latch /
	// correction_submit_latch / check_submit_conflict /
	// practice_submit_conflict / practice_submit_latch /
	// enter_check_submitted). Diagnostic taxonomy, NOT a provider fault:
	// provider_failed keeps meaning a real ASR/LLM/TTS/store failure, so
	// 13-13 acceptance can grep error rates per code without counting
	// absorbed duplicates as failures. In-session frame-level duplicates
	// (the reviewSeen gate's duplicate=check_submit / correction_submit /
	// practice_submit markers) stay invalid_message — they are client
	// protocol violations, not absorbed races.
	ErrCodeDuplicateSubmit = "duplicate_submit"
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

	// ErrLessonUnavailable is returned when classroom initialization fails
	// during session_init — FSRS due-word fetch or the lessons row cannot
	// support a lesson (code: lesson_unavailable).
	ErrLessonUnavailable = errors.New("rt: lesson unavailable")

	// ErrPlaybackAckTimeout is returned when the bounded wait for a
	// matching playback-completion ack expires after the server sent out a
	// scheduled announcement (code: playback_ack_timeout). It reports the
	// server STOPPED WAITING — never that playback completed.
	ErrPlaybackAckTimeout = errors.New("rt: playback completion ack timed out")
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
	// Format is the PCM format ("s16le" — bare little-endian signed 16-bit).
	Format string `json:"format"`
	// SampleRate is the sample rate in Hz (uplink R0: 16000).
	SampleRate int `json:"sampleRate"`
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
	// Words lists the lesson's words in order (R0: 3 fixed words).
	Words []string `json:"words"`
	// Version is the lesson state version (optimistic-concurrency counter).
	Version int `json:"version"`
	// CurrentIndex is the index of the word the student is currently on.
	CurrentIndex int `json:"current_index"`
}

// SessionInitMsg is the first client → server message. The client declares
// the audio format it will uplink (post-downsample); the server validates
// with ValidateAudioInit and answers with SessionReadyMsg or an
// unsupported_format ErrorMsg. Mode carries the page's UI mode (13.1 D-08:
// "student" default, "test" keeps the full-reveal behavior); an absent or
// invalid value normalizes to student.
//
// Practice=true (13.1-05, D-11) requests a PRACTICE session instead of the
// daily due lesson: random words from the full vocabulary minus today's
// dictated/practiced words, the same three-stage flow, results recorded to
// the Go-owned practice_logs only — never the FSRS schedule (D-12).
//
// Category (Issue #10) selects the classroom's subject: "chinese" opens a
// 语文词语 classroom (due fetch, review writes, practice picks and the
// lesson row all scope to the chinese vocabulary category); absent or
// "english" keeps the English classroom — the pre-Issue-#10 default, so
// existing pages that never send the field land exactly where they did.
//
// Poem (Issue #12) requests a WHOLE-POEM 背默 classroom for the poems.id it
// names instead of the due-word lesson: the poem's ordered lines become the
// task and the check rows, never a per-line dictation (the teacher does NOT
// read the poem aloud), and the submit records the whole-poem scope. A
// non-positive value (absent) keeps the word classroom — the same
// deny-by-default shape as Practice/Category.
//
// Passage/PassageScope (Issue #14) request a PASSAGE 背默 classroom for the
// passages.id and the passage_scopes.id the parent picked: the RANGE's own
// paragraphs (整篇 or the explicit paragraph set, in the original's own
// order) become the task and the check rows, and the submit records exactly
// that content + range identity (第 2、3 段掌握不算全文掌握). A non-positive
// passage (absent) keeps the word classroom; a passage without a scope id is
// refused honestly — a passage classroom ALWAYS names its range.
type SessionInitMsg struct {
	Type         string    `json:"type"`
	Mode         string    `json:"mode,omitempty"`
	Category     string    `json:"category,omitempty"`
	Audio        AudioSpec `json:"audio"`
	Practice     bool      `json:"practice,omitempty"`
	Poem         int64     `json:"poem,omitempty"`
	Passage      int64     `json:"passage,omitempty"`
	PassageScope int64     `json:"passage_scope,omitempty"`
}

// Classroom category values (Issue #10). The literals match the vocabulary
// category enum (D-06) and lesson.NormalizeCategory's canonical pair.
const (
	CategoryEnglish = "english"
	CategoryChinese = "chinese"
)

// CategoryAuto (Issue #55) is the explicit "no subject declared" request.
// The server NEVER lets a model guess the subject: with only ONE subject
// holding due words the classroom opens under that subject directly; with
// BOTH subjects holding due words the server asks the CHILD which one to
// dictate (the subject_ask question + screen buttons; the spoken or tapped
// answer binds the round) and the round then runs under the picked subject
// only; with neither subject due, the english classroom opens and ends
// through the ordinary no-due path. An explicit english/chinese declaration
// skips all of this — the user's word wins. Absent/off-enum values keep the
// pre-#55 deny-by-default english landing.
const CategoryAuto = "auto"

// UI mode values carried by SessionInitMsg.Mode (13.1 D-08). Anything else —
// including an absent field — is student mode: the iPad bookmark must never
// land the child on a full-reveal word table by accident.
const (
	ModeStudent = "student"
	ModeTest    = "test"
)

// InterruptMsg reports a client-detected interruption while the teacher was
// speaking. Detection is client-side (playback-time local energy) because
// the server cannot distinguish speaker echo from a real voice (Pattern 3).
// Epoch is the epoch the client was playing; PlayedUntilMs/LastSentenceID
// describe the actually-played position so history can be trimmed to what
// the student really heard (Pitfall 9).
type InterruptMsg struct {
	Type           string `json:"type"`
	LastSentenceID string `json:"last_sentence_id"`
	Epoch          uint64 `json:"epoch"`
	PlayedUntilMs  int64  `json:"played_until_ms"`
}

// SentenceAckMsg confirms the ACTUAL PLAYBACK COMPLETION of one teacher
// sentence. It is a completion acknowledgement, never a playback-start
// signal: the client sends it only after (a) the audio_end matching this
// epoch+sentence_id has been seen AND (b) the onended callback of the last
// AudioBufferSourceNode scheduled for that sentence has fired. The
// epoch+sentence_id pair is the identity key and its wire shape is frozen;
// runtime identity validation (only accept acks for the currently
// outstanding sentence set) belongs to the 13-08 FSM, and per-sentence
// independent scheduling in the browser belongs to 13-11. Acks are the
// ground truth for "actually played" history trimming, natural-completion
// sequencing (SessionEndedMsg reasons), and RT-04 measurement.
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

// SubjectAskMsg (Issue #55) is the server's subject clarify question: an
// auto (unspecified-subject) word classroom where BOTH subjects hold due
// words asks the CHILD which one to dictate instead of guessing. Question
// carries the same text the teacher speaks; the page also renders two pick
// buttons. The classroom has NOT started yet — no lesson row exists until
// the answer arrives (subject_pick control or the spoken answer), and the
// round then runs under the picked subject only.
type SubjectAskMsg struct {
	Type     string `json:"type"`
	Question string `json:"question"`
}

// SubjectPickMsg (Issue #55) is the page's answer to SubjectAskMsg: the
// subject button the child tapped. Category must be exactly english or
// chinese (ParseControl rejects anything else); the picked subject binds
// the round exactly like an explicit declaration.
type SubjectPickMsg struct {
	Type     string `json:"type"`
	Category string `json:"category"`
}

// SessionReadyMsg answers a valid SessionInitMsg. Audio echoes the confirmed
// uplink values; TTS declares the downlink sample rate; Lesson carries the
// initial lesson snapshot. Category echoes the session's subject
// ("english"/"chinese", Issue #10) — the page's language-dependent surfaces
// (speech-recognition locale, word display) key off it, and a reconnect to
// an existing row echoes the ROW's subject, which is what the row was
// created under.
//
// Poem (Issue #12) is non-nil only for a whole-poem 背默 classroom: the
// practiced work's identity the page renders (title/author/line count) —
// the poem's TEXT itself never rides here; Lesson.Words stays masked (""
// per line) until check_started, in every mode.
//
// Passage (Issue #14) is non-nil only for a passage 背默 classroom: the
// practiced work's identity plus the RANGE actually practiced (整篇 or the
// 1-based paragraph set in original order). The passage's TEXT never rides
// here either; Lesson.Words stays masked ("" per paragraph) until
// check_started, in every mode.
type SessionReadyMsg struct {
	Poem      *PoemTaskView    `json:"poem,omitempty"`
	Passage   *PassageTaskView `json:"passage,omitempty"`
	Type      string           `json:"type"`
	SessionID string           `json:"session_id"`
	Category  string           `json:"category,omitempty"`
	Audio     AudioSpec        `json:"audio"`
	Lesson    LessonView       `json:"lesson"`
	TTS       TTSSpec          `json:"tts"`
}

// PoemTaskView is the whole-poem 背默 task's identity on session_ready
// (Issue #12): WHAT the child is writing from memory, never the text
// itself. Method names the practice way (练习方式) so the page and the
// parent records share one vocabulary.
type PoemTaskView struct {
	Title     string `json:"title"`
	Author    string `json:"author,omitempty"`
	Method    string `json:"method"`
	ID        int64  `json:"id"`
	LineCount int    `json:"line_count"`
}

// PassageTaskView is the passage 背默 task's identity on session_ready
// (Issue #14): WHAT the child is writing from memory and WHICH range —
// never the text itself. Paragraphs carries the 1-based ORIGINAL paragraph
// indexes in original order (equal to [1..ParagraphCount] when Whole), so
// the page and the parent records label the practiced rows by the text's
// own numbering, never by a re-derived sequence.
type PassageTaskView struct {
	Title          string `json:"title"`
	Author         string `json:"author,omitempty"`
	Method         string `json:"method"`
	Paragraphs     []int  `json:"paragraphs"`
	ID             int64  `json:"id"`
	ScopeID        int64  `json:"scope_id"`
	ParagraphCount int    `json:"paragraph_count"`
	Whole          bool   `json:"whole"`
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
	Text   string `json:"text"`
	TurnID uint64 `json:"turn_id"`
}

// TeacherSentenceMsg sends one teacher sentence's text BEFORE its audio
// chunks (subtitle-first), so captions render ahead of playback.
type TeacherSentenceMsg struct {
	Type       string `json:"type"`
	SentenceID string `json:"sentence_id"`
	Text       string `json:"text"`
	TurnID     uint64 `json:"turn_id"`
	Epoch      uint64 `json:"epoch"`
}

// AudioStartMsg announces the first audio chunk of one sentence (subtitles
// already delivered via TeacherSentenceMsg).
type AudioStartMsg struct {
	Type       string `json:"type"`
	SentenceID string `json:"sentence_id"`
	TurnID     uint64 `json:"turn_id"`
	Epoch      uint64 `json:"epoch"`
}

// AudioEndMsg announces the last audio chunk of one sentence. Same shape as
// AudioStartMsg.
type AudioEndMsg struct {
	Type       string `json:"type"`
	SentenceID string `json:"sentence_id"`
	TurnID     uint64 `json:"turn_id"`
	Epoch      uint64 `json:"epoch"`
}

// LessonUpdateMsg reports a committed lesson-state write (after tool
// execution) — the progress/UI consistency half of RT-07. RequestID is the
// idempotency key the tool executed under (the MiMo tool_call id).
type LessonUpdateMsg struct {
	Type         string `json:"type"`
	LessonID     string `json:"lesson_id"`
	RequestID    string `json:"request_id"`
	Version      int    `json:"version"`
	CurrentIndex int    `json:"current_index"`
}

// InterruptAckMsg confirms an interruption took effect: the invalidated
// epoch, the new epoch, and the played position the server recorded from the
// client's report.
type InterruptAckMsg struct {
	Type           string `json:"type"`
	LastSentenceID string `json:"last_sentence_id"`
	OldEpoch       uint64 `json:"old_epoch"`
	NewEpoch       uint64 `json:"new_epoch"`
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
// processing / speaking / paused).
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

// SessionEndedMsg.Reason vocabulary for NATURAL classroom endings (the
// state-machine dictation-tutor loop, 2026-10-03 pivot; 13.1-05 adds the
// practice ending). Explicit end_session, idle timeout, or fatal error keep
// using their existing reason strings.
const (
	// ReasonLessonComplete: the wrap-up summary has been produced and the
	// learning record written after the last word completed.
	ReasonLessonComplete = "lesson_complete"
	// ReasonNoDueWords: zero FSRS due words; the teacher explained that and
	// ended the session.
	ReasonNoDueWords = "no_due_words"
	// ReasonPracticeComplete: a PRACTICE set (13.1-05, D-11) finished its
	// three-stage flow; the results went to practice_logs only, never the
	// FSRS schedule (D-12). The practice ending is the ONLY ending that
	// carries this reason.
	ReasonPracticeComplete = "practice_complete"
)

// SessionEndedMsg is the server's final message before closing the
// connection (explicit end_session, idle timeout, fatal error, or a natural
// classroom end carrying ReasonLessonComplete / ReasonNoDueWords /
// ReasonPracticeComplete).
//
// Sequencing contract for natural endings: the SessionEndedMsg must be sent
// only after the matching playback-completion sentence_ack has been received
// for the final announcement. If that bounded wait times out, the server
// first sends ErrorMsg(code=playback_ack_timeout) and THEN sends the
// SessionEndedMsg still carrying the original lesson_complete/no_due_words/
// practice_complete reason. That degraded order means only "the server
// stopped waiting" — it is never evidence that the announcement was actually
// played, and playback_ack_timeout itself is an ErrorMsg.Code, never a
// SessionEndedMsg.Reason.
type SessionEndedMsg struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// ---------------------------------------------------------------------------
// Check-stage messages (13.1-01): the three-stage classroom's wire contract.
// The tutor page and the mock e2e suite mirror these field-for-field.
// ---------------------------------------------------------------------------

// CheckVerdictMsg is one screen verdict (client → server): the student (or
// the e2e harness) tapped 对/错 for WordIndex. RequestID is the
// CLIENT-SUPPLIED idempotency key — unique per mutation intent (a legitimate
// 对→错→对 flip sends fresh ids; only network retries reuse one). The server
// echoes the authoritative list via CheckVerdictsMsg; the client never
// renders local optimistic state (RT-07).
type CheckVerdictMsg struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	WordIndex int    `json:"word_index"`
	Correct   bool   `json:"correct"`
}

// CheckSubmitMsg requests the unified FSRS submission (client → server).
// The server submits at most once — the checking→submitted stage transition
// is the idempotency latch; a replayed submit re-serves the stored result
// without a second SubmitReview (T-13.1-01).
type CheckSubmitMsg struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

// CheckStartedReason vocabulary for CheckStartedMsg.Reason.
const (
	// CheckStartedReasonDictationDone: every word was dictated; the check
	// stage opens right after the last advance commit (the D-10 unmask
	// point — this message carries the full plaintext list).
	CheckStartedReasonDictationDone = "dictation_done"
	// CheckStartedReasonResume: a session reconnected into a lesson whose
	// check stage never finished (13.1-04 recovery path).
	CheckStartedReasonResume = "resume"
	// CheckStartedReasonCorrection: a stored unchecked lesson reopens for
	// the post-hoc photo correction (13.1-04).
	CheckStartedReasonCorrection = "correction"
	// CheckStartedReasonPoemDone: the whole-poem 背默 task finished its
	// writing stage — the student said 写完了 and the panel opens against
	// the poem's own ordered LINES (Issue #12; the same D-10 unmask point:
	// before this message the poem's text never reached the client).
	CheckStartedReasonPoemDone = "poem_done"
	// CheckStartedReasonPassageDone: the passage-range 背默 task finished
	// its writing stage — the panel opens against the RANGE's own ordered
	// PARAGRAPHS only (Issue #14; the same D-10 unmask point, and the rows
	// never include paragraphs outside the practiced range).
	CheckStartedReasonPassageDone = "passage_done"
)

// CheckStartedMsg opens the check stage (server → client). Words is the
// FULL plaintext list — the unmask point (D-10): before this message the
// student-facing surfaces must not carry undictated word text. The client
// renders one check row per entry.
type CheckStartedMsg struct {
	Type   string   `json:"type"`
	Reason string   `json:"reason"`
	Words  []string `json:"words"`
}

// WireVerdict is one verdict of the authoritative echo list.
type WireVerdict struct {
	WordIndex int  `json:"word_index"`
	Correct   bool `json:"correct"`
	Manual    bool `json:"manual"`
}

// CheckVerdictsMsg carries the FULL stored verdict list (server → client) —
// the single render source for the check panel (RT-07): every screen state
// derives from this echo, never from local optimistic state.
type CheckVerdictsMsg struct {
	Type     string        `json:"type"`
	Verdicts []WireVerdict `json:"verdicts"`
}

// ReviewResultMsg reports the unified FSRS submission honestly (server →
// client, S-05): Requested/Updated/Missing are the REAL set sizes — partial
// and zero updates accompany a successful write, and the client must never
// fabricate full success when Updated < Requested. Unchecked marks the D-03
// behavior-fallback path (13.1-04) — always false for a checked submission.
type ReviewResultMsg struct {
	Type      string `json:"type"`
	Requested int    `json:"requested"`
	Updated   int    `json:"updated"`
	Missing   int    `json:"missing"`
	Unchecked bool   `json:"unchecked"`
}

// WordRevealedMsg carries one word's plaintext reveal (server → client, the
// per-word unmask primitive the student-mode masking of 13.1-02 consumes).
type WordRevealedMsg struct {
	Type      string `json:"type"`
	Word      string `json:"word"`
	WordIndex int    `json:"word_index"`
}

// ---------------------------------------------------------------------------
// Photo-check messages (13.1-03): the D-05 photo upload channel. The image
// rides the CONTROL channel as base64 JSON chunks (32KiB raw per chunk —
// under the 65551-byte read limit) so the audio BINARY channel stays
// untouched (Pitfall 5). The tutor page and the mock e2e suite mirror these
// field-for-field.
// ---------------------------------------------------------------------------

// PhotoChunkMsg is one base64 image chunk (client → server). Seq is 1-based
// and contiguous per upload; a first chunk with seq 1 RESETS any buffered
// upload (the retake contract). Final marks the last chunk — the server runs
// the OCR check pass immediately, there is no separate submit message.
type PhotoChunkMsg struct {
	Type  string `json:"type"`
	Data  string `json:"data"` // base64 of the raw chunk bytes
	Seq   int    `json:"seq"`
	Final bool   `json:"final"` // last chunk of this upload
}

// PhotoCancelMsg discards the in-flight upload (client → server): the
// student is retaking or abandoning the photo. The assembler buffer clears
// unconditionally — without this, the NEXT upload's sequence numbers (which
// restart at 1) would be absorbed as duplicates and the wrong photo OCR'd
// (the cmd/service photo_cancel incident, Pitfall 5).
type PhotoCancelMsg struct {
	Type string `json:"type"`
}

// OcrResultEntry is one word's OCR recognition shown next to its check row.
// Correct/NearMiss mirror the GradeWord booleans the prefill used — the
// verdicts themselves still ride check_verdicts (the single render source).
type OcrResultEntry struct {
	OcrText   string `json:"ocr_text"`
	WordIndex int    `json:"word_index"`
	Correct   bool   `json:"correct"`
	NearMiss  bool   `json:"near_miss"`
}

// OcrResultMsg reports what the vision model recognized per word (server →
// client), right after the prefill it produced. Entries cover only the
// positions a non-empty reading exists for; the rest stay for manual
// confirmation.
type OcrResultMsg struct {
	Type    string           `json:"type"`
	Entries []OcrResultEntry `json:"entries"`
}

// ReadWordMsg requests audio only. CurrentIndex rejects clicks queued against
// an older lesson position; the selected index never changes lesson progress.
type ReadWordMsg struct {
	Type         string `json:"type"`
	WordIndex    int    `json:"word_index"`
	CurrentIndex int    `json:"current_index"`
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
	case TypeReadWord:
		var fields struct {
			WordIndex    *int `json:"word_index"`
			CurrentIndex *int `json:"current_index"`
		}
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		if fields.WordIndex == nil || fields.CurrentIndex == nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, "read_word requires word_index and current_index")
		}
		return ReadWordMsg{Type: probe.Type, WordIndex: *fields.WordIndex, CurrentIndex: *fields.CurrentIndex}, nil
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
	case TypeSubjectPick:
		var m SubjectPickMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		if m.Category != CategoryEnglish && m.Category != CategoryChinese {
			return nil, errors.Wrap(ErrInvalidControlMessage, "subject_pick requires category english or chinese")
		}
		return m, nil
	case TypeSubjectAsk:
		var m SubjectAskMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeCheckVerdict:
		var m CheckVerdictMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeCheckSubmit:
		var m CheckSubmitMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypePhotoChunk:
		var m PhotoChunkMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypePhotoCancel:
		var m PhotoCancelMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeOcrResult:
		var m OcrResultMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeCheckStarted:
		var m CheckStartedMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeCheckVerdicts:
		var m CheckVerdictsMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeReviewResult:
		var m ReviewResultMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Wrap(ErrInvalidControlMessage, err.Error())
		}
		return m, nil
	case TypeWordRevealed:
		var m WordRevealedMsg
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
	case errors.Is(err, ErrLessonUnavailable):
		return ErrCodeLessonUnavailable
	case errors.Is(err, ErrPlaybackAckTimeout):
		return ErrCodePlaybackAckTimeout
	default:
		return ErrCodeProviderFailed
	}
}
