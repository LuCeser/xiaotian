package rt

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/pkg/errors"
)

// roundTrip marshals msg, parses it back through ParseControl, and asserts
// the result has the same dynamic type as want (a zero value of the expected
// struct). It returns the decoded value for field-level assertions.
func roundTrip(t *testing.T, msg any, want any) any {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal %T: %v", msg, err)
	}
	got, err := ParseControl(data)
	if err != nil {
		t.Fatalf("ParseControl(%T): %v", msg, err)
	}
	if gotType, wantType := fmt.Sprintf("%T", got), fmt.Sprintf("%T", want); gotType != wantType {
		t.Fatalf("ParseControl returned %s, want %s", gotType, wantType)
	}
	return got
}

// wireKeys unmarshals the marshaled form of msg into a generic map so tests
// can assert exact JSON field names (the frozen wire contract).
func wireKeys(t *testing.T, msg any) map[string]any {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal %T: %v", msg, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %T to map: %v", msg, err)
	}
	return m
}

func wantString(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("wire JSON missing key %q (got keys from %v)", key, m)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("wire JSON key %q = %T, want string", key, v)
	}
	return s
}

func wantNumber(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("wire JSON missing key %q (got keys from %v)", key, m)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("wire JSON key %q = %T, want number", key, v)
	}
	return f
}

func wantObject(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("wire JSON missing key %q (got keys from %v)", key, m)
	}
	o, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("wire JSON key %q = %T, want object", key, v)
	}
	return o
}

func TestProtocolSessionInitRoundTrip(t *testing.T) {
	msg := SessionInitMsg{
		Type:  TypeSessionInit,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}
	got := roundTrip(t, msg, SessionInitMsg{})
	m := got.(SessionInitMsg)
	if m.Type != TypeSessionInit {
		t.Errorf("type = %q, want %q", m.Type, TypeSessionInit)
	}
	if m.Audio != msg.Audio {
		t.Errorf("audio = %+v, want %+v", m.Audio, msg.Audio)
	}

	// Wire field names frozen per the session_init example:
	// {"type":"session_init","audio":{"sampleRate":16000,"format":"s16le","channels":1}}
	w := wireKeys(t, msg)
	if got := wantString(t, w, "type"); got != "session_init" {
		t.Errorf("wire type = %q, want session_init", got)
	}
	audio := wantObject(t, w, "audio")
	if got := wantNumber(t, audio, "sampleRate"); got != 16000 {
		t.Errorf("wire audio.sampleRate = %v, want 16000", got)
	}
	if got := wantString(t, audio, "format"); got != "s16le" {
		t.Errorf("wire audio.format = %q, want s16le", got)
	}
	if got := wantNumber(t, audio, "channels"); got != 1 {
		t.Errorf("wire audio.channels = %v, want 1", got)
	}
}

func TestProtocolInterruptRoundTrip(t *testing.T) {
	msg := InterruptMsg{
		Type:           TypeInterrupt,
		Epoch:          7,
		LastSentenceID: "s-3",
		PlayedUntilMs:  1420,
	}
	got := roundTrip(t, msg, InterruptMsg{}).(InterruptMsg)
	if got != msg {
		t.Errorf("round trip = %+v, want %+v", got, msg)
	}
	w := wireKeys(t, msg)
	for _, key := range []string{"epoch", "last_sentence_id", "played_until_ms"} {
		if _, ok := w[key]; !ok {
			t.Errorf("interrupt wire JSON missing key %q", key)
		}
	}
}

// TestProtocolSentenceAckCompletionRoundTrip asserts the sentence_ack wire
// contract keeps its frozen two-field identity (sentence_id + epoch) — the
// message is the ACTUAL PLAYBACK COMPLETION acknowledgement (sent only after
// the matching audio_end and the last scheduled source's onended), never a
// playback-start signal. Field shape must not change; runtime identity
// gating belongs to 13-08, per-sentence browser scheduling to 13-11.
func TestProtocolSentenceAckCompletionRoundTrip(t *testing.T) {
	msg := SentenceAckMsg{Type: TypeSentenceAck, SentenceID: "s-1", Epoch: 4}
	got := roundTrip(t, msg, SentenceAckMsg{}).(SentenceAckMsg)
	if got != msg {
		t.Errorf("round trip = %+v, want %+v", got, msg)
	}
	if got.Epoch != msg.Epoch || got.SentenceID != msg.SentenceID {
		t.Errorf("completion ack identity = epoch %d/sentence %q, want epoch %d/sentence %q",
			got.Epoch, got.SentenceID, msg.Epoch, msg.SentenceID)
	}
	w := wireKeys(t, msg)
	for _, key := range []string{"sentence_id", "epoch"} {
		if _, ok := w[key]; !ok {
			t.Errorf("sentence_ack wire JSON missing key %q", key)
		}
	}
}

func TestProtocolEndSessionRoundTrip(t *testing.T) {
	msg := EndSessionMsg{Type: TypeEndSession}
	got := roundTrip(t, msg, EndSessionMsg{}).(EndSessionMsg)
	if got != msg {
		t.Errorf("round trip = %+v, want %+v", got, msg)
	}
}

// TestProtocolPingPongRoundTrip covers the RT-04 clock-offset probe pair:
// the ping carries t_client_ms; the pong echoes it verbatim alongside the
// server monotonic stamp t_server_mono_ns (same anchor as rt_event
// t_mono_ns).
func TestProtocolPingPongRoundTrip(t *testing.T) {
	ping := PingMsg{Type: TypePing, TClientMs: 1727774400123}
	gotPing := roundTrip(t, ping, PingMsg{}).(PingMsg)
	if gotPing != ping {
		t.Errorf("ping round trip = %+v, want %+v", gotPing, ping)
	}

	// Server-side response: echo t_client_ms, stamp monotonic ns.
	pong := PongMsg{Type: TypePong, TClientMs: ping.TClientMs, TServerMonoNs: 987654321}
	gotPong := roundTrip(t, pong, PongMsg{}).(PongMsg)
	if gotPong != pong {
		t.Errorf("pong round trip = %+v, want %+v", gotPong, pong)
	}
	if gotPong.TClientMs != ping.TClientMs {
		t.Errorf("pong t_client_ms = %d, want echo of ping %d", gotPong.TClientMs, ping.TClientMs)
	}

	// Exact wire field names for the offset-estimation pair.
	pw := wireKeys(t, ping)
	if _, ok := pw["t_client_ms"]; !ok {
		t.Error("ping wire JSON missing key t_client_ms")
	}
	ow := wireKeys(t, pong)
	if _, ok := ow["t_client_ms"]; !ok {
		t.Error("pong wire JSON missing key t_client_ms")
	}
	if _, ok := ow["t_server_mono_ns"]; !ok {
		t.Error("pong wire JSON missing key t_server_mono_ns")
	}
}

func TestProtocolSessionReadyRoundTrip(t *testing.T) {
	msg := SessionReadyMsg{
		Type:      TypeSessionReady,
		SessionID: "sess-42",
		Audio:     AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
		TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
		Lesson: LessonView{
			LessonID:     "L1",
			Version:      1,
			CurrentIndex: 0,
			Words:        []string{"apple", "banana", "cherry"},
		},
	}
	got := roundTrip(t, msg, SessionReadyMsg{}).(SessionReadyMsg)
	if got.Type != msg.Type || got.SessionID != msg.SessionID || got.Audio != msg.Audio {
		t.Errorf("session_ready header round trip = %+v, want %+v", got, msg)
	}
	if got.TTS.SampleRate != TTSOutputSampleRate {
		t.Errorf("tts.sampleRate = %d, want TTSOutputSampleRate %d", got.TTS.SampleRate, TTSOutputSampleRate)
	}
	if got.Lesson.LessonID != msg.Lesson.LessonID || got.Lesson.Version != msg.Lesson.Version ||
		got.Lesson.CurrentIndex != msg.Lesson.CurrentIndex || len(got.Lesson.Words) != len(msg.Lesson.Words) {
		t.Errorf("lesson round trip = %+v, want %+v", got.Lesson, msg.Lesson)
	}
	for i, w := range msg.Lesson.Words {
		if got.Lesson.Words[i] != w {
			t.Errorf("lesson words[%d] = %q, want %q", i, got.Lesson.Words[i], w)
		}
	}

	// Wire shape: session_ready.tts.sampleRate declares the downlink rate;
	// lesson object keys per the session_ready example.
	w := wireKeys(t, msg)
	if _, ok := w["session_id"]; !ok {
		t.Error("session_ready wire JSON missing key session_id")
	}
	tts := wantObject(t, w, "tts")
	if got := wantNumber(t, tts, "sampleRate"); got != TTSOutputSampleRate {
		t.Errorf("session_ready.tts.sampleRate = %v, want %d", got, TTSOutputSampleRate)
	}
	lesson := wantObject(t, w, "lesson")
	if got := wantString(t, lesson, "id"); got != "L1" {
		t.Errorf("session_ready.lesson.id = %q, want L1", got)
	}
	if got := wantNumber(t, lesson, "version"); got != 1 {
		t.Errorf("session_ready.lesson.version = %v, want 1", got)
	}
}

func TestProtocolTurnStartRoundTrip(t *testing.T) {
	msg := TurnStartMsg{Type: TypeTurnStart, TurnID: 3, Epoch: 9}
	got := roundTrip(t, msg, TurnStartMsg{}).(TurnStartMsg)
	if got != msg {
		t.Errorf("round trip = %+v, want %+v", got, msg)
	}
}

func TestProtocolTranscriptRoundTrip(t *testing.T) {
	msg := TranscriptMsg{Type: TypeTranscript, TurnID: 3, Text: "这个单词什么意思"}
	got := roundTrip(t, msg, TranscriptMsg{}).(TranscriptMsg)
	if got != msg {
		t.Errorf("round trip = %+v, want %+v", got, msg)
	}
}

func TestProtocolTeacherSentenceRoundTrip(t *testing.T) {
	msg := TeacherSentenceMsg{Type: TypeTeacherSentence, TurnID: 3, Epoch: 9, SentenceID: "s-1", Text: "我们来写第一个词。"}
	got := roundTrip(t, msg, TeacherSentenceMsg{}).(TeacherSentenceMsg)
	if got != msg {
		t.Errorf("round trip = %+v, want %+v", got, msg)
	}
	w := wireKeys(t, msg)
	for _, key := range []string{"turn_id", "epoch", "sentence_id", "text"} {
		if _, ok := w[key]; !ok {
			t.Errorf("teacher_sentence wire JSON missing key %q", key)
		}
	}
}

func TestProtocolAudioStartEndRoundTrip(t *testing.T) {
	start := AudioStartMsg{Type: TypeAudioStart, TurnID: 3, Epoch: 9, SentenceID: "s-1"}
	gotStart := roundTrip(t, start, AudioStartMsg{}).(AudioStartMsg)
	if gotStart != start {
		t.Errorf("audio_start round trip = %+v, want %+v", gotStart, start)
	}
	end := AudioEndMsg{Type: TypeAudioEnd, TurnID: 3, Epoch: 9, SentenceID: "s-1"}
	gotEnd := roundTrip(t, end, AudioEndMsg{}).(AudioEndMsg)
	if gotEnd != end {
		t.Errorf("audio_end round trip = %+v, want %+v", gotEnd, end)
	}
}

func TestProtocolLessonUpdateRoundTrip(t *testing.T) {
	msg := LessonUpdateMsg{Type: TypeLessonUpdate, LessonID: "L1", Version: 2, CurrentIndex: 1, RequestID: "call_abc"}
	got := roundTrip(t, msg, LessonUpdateMsg{}).(LessonUpdateMsg)
	if got != msg {
		t.Errorf("round trip = %+v, want %+v", got, msg)
	}
	w := wireKeys(t, msg)
	for _, key := range []string{"lesson_id", "version", "current_index", "request_id"} {
		if _, ok := w[key]; !ok {
			t.Errorf("lesson_update wire JSON missing key %q", key)
		}
	}
}

func TestProtocolInterruptAckRoundTrip(t *testing.T) {
	msg := InterruptAckMsg{Type: TypeInterruptAck, OldEpoch: 9, NewEpoch: 10, LastSentenceID: "s-2", PlayedUntilMs: 800}
	got := roundTrip(t, msg, InterruptAckMsg{}).(InterruptAckMsg)
	if got != msg {
		t.Errorf("round trip = %+v, want %+v", got, msg)
	}
	w := wireKeys(t, msg)
	for _, key := range []string{"old_epoch", "new_epoch", "last_sentence_id", "played_until_ms"} {
		if _, ok := w[key]; !ok {
			t.Errorf("interrupt_ack wire JSON missing key %q", key)
		}
	}
}

func TestProtocolStateRoundTrip(t *testing.T) {
	for _, value := range []string{StateListening, StateProcessing, StateSpeaking, StatePaused} {
		msg := StateMsg{Type: TypeState, Value: value}
		got := roundTrip(t, msg, StateMsg{}).(StateMsg)
		if got != msg {
			t.Errorf("round trip = %+v, want %+v", got, msg)
		}
	}
}

func TestProtocolErrorRoundTrip(t *testing.T) {
	msg := ErrorMsg{Type: TypeError, Code: ErrCodeUnsupportedFormat, Message: "want 16000Hz s16le 1ch", Recoverable: false}
	got := roundTrip(t, msg, ErrorMsg{}).(ErrorMsg)
	if got != msg {
		t.Errorf("round trip = %+v, want %+v", got, msg)
	}
	w := wireKeys(t, msg)
	for _, key := range []string{"code", "message", "recoverable"} {
		if _, ok := w[key]; !ok {
			t.Errorf("error wire JSON missing key %q", key)
		}
	}
}

func TestProtocolSessionEndedRoundTrip(t *testing.T) {
	msg := SessionEndedMsg{Type: TypeSessionEnded, Reason: "client_end"}
	got := roundTrip(t, msg, SessionEndedMsg{}).(SessionEndedMsg)
	if got != msg {
		t.Errorf("round trip = %+v, want %+v", got, msg)
	}
}

func TestProtocolUnknownTypeRejected(t *testing.T) {
	_, err := ParseControl([]byte(`{"type":"bogus","x":1}`))
	if err == nil {
		t.Fatal("ParseControl accepted unknown type, want error")
	}
	if !errors.Is(err, ErrInvalidControlMessage) {
		t.Errorf("err = %v, want ErrInvalidControlMessage", err)
	}
	if code := ErrorCode(err); code != ErrCodeInvalidMessage {
		t.Errorf("ErrorCode = %q, want %q", code, ErrCodeInvalidMessage)
	}
}

func TestProtocolMissingTypeRejected(t *testing.T) {
	if _, err := ParseControl([]byte(`{}`)); err == nil {
		t.Fatal("ParseControl accepted message without type, want error")
	}
}

func TestProtocolMalformedJSONRejected(t *testing.T) {
	if _, err := ParseControl([]byte(`{"type":`)); err == nil {
		t.Fatal("ParseControl accepted malformed JSON, want error")
	}
}

// TestProtocolPausedAndReasonLiterals pins the exact wire literals of the
// 13-07 contract vocabulary: the paused state value, the two new error
// codes, and the two natural-end SessionEndedMsg reasons. playback_ack_timeout
// must exist ONLY as an error code — it is deliberately absent from the
// reason vocabulary.
func TestProtocolPausedAndReasonLiterals(t *testing.T) {
	if got := StatePaused; got != "paused" {
		t.Errorf("StatePaused = %q, want %q", got, "paused")
	}
	if got := ErrCodeLessonUnavailable; got != "lesson_unavailable" {
		t.Errorf("ErrCodeLessonUnavailable = %q, want %q", got, "lesson_unavailable")
	}
	if got := ErrCodePlaybackAckTimeout; got != "playback_ack_timeout" {
		t.Errorf("ErrCodePlaybackAckTimeout = %q, want %q", got, "playback_ack_timeout")
	}
	if got := ReasonLessonComplete; got != "lesson_complete" {
		t.Errorf("ReasonLessonComplete = %q, want %q", got, "lesson_complete")
	}
	if got := ReasonNoDueWords; got != "no_due_words" {
		t.Errorf("ReasonNoDueWords = %q, want %q", got, "no_due_words")
	}
	if ReasonPlaybackAckTimeoutExists() {
		t.Error("playback_ack_timeout leaked into the SessionEndedMsg.Reason vocabulary")
	}
}

// ReasonPlaybackAckTimeoutExists is a compile-time guard documenting the
// freeze: playback_ack_timeout is an ErrorMsg.Code, never a
// SessionEndedMsg.Reason constant. It always returns false; if someone later
// adds a Reason constant with the playback_ack_timeout value, update this
// function to return true and the test above fails.
func ReasonPlaybackAckTimeoutExists() bool {
	return ReasonLessonComplete == ErrCodePlaybackAckTimeout ||
		ReasonNoDueWords == ErrCodePlaybackAckTimeout
}

// TestErrorCode maps the protocol sentinels (bare and wrapped) to their
// stable ErrorMsg.Code values, including the two 13-07 codes and the
// provider_failed fallback.
func TestErrorCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unsupported format", ErrUnsupportedAudioFormat, ErrCodeUnsupportedFormat},
		{"invalid control message", ErrInvalidControlMessage, ErrCodeInvalidMessage},
		{"lesson unavailable", ErrLessonUnavailable, ErrCodeLessonUnavailable},
		{"lesson unavailable wrapped", errors.Wrap(ErrLessonUnavailable, "session_init: due fetch failed"), ErrCodeLessonUnavailable},
		{"playback ack timeout", ErrPlaybackAckTimeout, ErrCodePlaybackAckTimeout},
		{"playback ack timeout wrapped", errors.Wrap(ErrPlaybackAckTimeout, "bounded wait expired"), ErrCodePlaybackAckTimeout},
		{"unknown falls back", errors.New("some other failure"), ErrCodeProviderFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorCode(tc.err); got != tc.want {
				t.Errorf("ErrorCode(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestProtocolValidateAudioInitMatrix covers the negotiation matrix: the one
// legal R0 configuration and three rejection classes (wrong rate / wrong
// format / wrong channel count).
func TestProtocolValidateAudioInitMatrix(t *testing.T) {
	cases := []struct {
		name    string
		spec    AudioSpec
		wantErr bool
	}{
		{"legal 16000 s16le mono", AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1}, false},
		{"48000 rejected", AudioSpec{SampleRate: 48000, Format: "s16le", Channels: 1}, true},
		{"f32le rejected", AudioSpec{SampleRate: 16000, Format: "f32le", Channels: 1}, true},
		{"stereo rejected", AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 2}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAudioInit(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateAudioInit(%+v) = nil, want error", tc.spec)
				}
				if !errors.Is(err, ErrUnsupportedAudioFormat) {
					t.Errorf("err = %v, want ErrUnsupportedAudioFormat", err)
				}
				if code := ErrorCode(err); code != ErrCodeUnsupportedFormat {
					t.Errorf("ErrorCode = %q, want %q", code, ErrCodeUnsupportedFormat)
				}
			} else if err != nil {
				t.Fatalf("ValidateAudioInit(%+v) = %v, want nil", tc.spec, err)
			}
		})
	}
}
