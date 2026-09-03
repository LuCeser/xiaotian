package main

// Voice-command wiring tests (Ticket 03): end-to-end through a real router
// + WebSocket connection, using the httptest scaffold pattern from
// handlers_test.go. The ASR is faked at the voice.Transcriber seam (a plain
// closure) and the classifier runs rules-only (voice.NewHybridClassifier(nil))
// so no LLM call is involved.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/voice"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
	"go.uber.org/zap"
)

// captureTranscriber records every WAV it received and replies with a fixed
// transcript (the fake at the voice.Transcriber seam).
type captureTranscriber struct {
	mu         sync.Mutex
	transcript string
	wavs       [][]byte
}

func (c *captureTranscriber) transcribe(_ context.Context, wav []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := make([]byte, len(wav))
	copy(w, wav)
	c.wavs = append(c.wavs, w)
	return c.transcript, nil
}

func (c *captureTranscriber) wavCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.wavs)
}

func (c *captureTranscriber) lastWAV() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.wavs) == 0 {
		return nil
	}
	return c.wavs[len(c.wavs)-1]
}

// recordingPusher records every pushed session event (fake sessionPusher).
type recordingPusher struct {
	events []session.SessionEvent
	byConn map[string]bool
}

func (p *recordingPusher) Push(connID string, ev session.SessionEvent) {
	if p.byConn == nil {
		p.byConn = map[string]bool{}
	}
	p.byConn[connID] = true
	p.events = append(p.events, ev)
}

// dialVoiceTestServer boots a hub + router + ws server around the given
// handler map and dials one client connection. Returns the dialed connection
// and a teardown func.
func dialVoiceTestServer(t *testing.T, handlers map[ws.MessageType]ws.Handler) (*websocket.Conn, func()) {
	t.Helper()
	hub, err := ws.NewHub(ws.HubConfig{Limit: 10, RegisterBuffer: 32, UnregisterBuffer: 32}, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}
	return dialVoiceTestServerOnHub(t, hub, handlers)
}

// dialVoiceTestServerOnHub is dialVoiceTestServer for a caller-supplied hub —
// needed when the test's service objects (e.g. a real session.Manager) must
// share the hub the ws server registers connections on.
func dialVoiceTestServerOnHub(t *testing.T, hub *ws.Hub, handlers map[ws.MessageType]ws.Handler) (*websocket.Conn, func()) {
	t.Helper()
	logger := zap.NewNop()

	hubCtx, hubCancel := context.WithCancel(context.Background())
	go hub.Run(hubCtx)

	router, err := ws.NewRouter(handlers, ws.RouterConfig{RouteBuffer: 32, WorkerConcurrency: 4}, logger)
	if err != nil {
		hubCancel()
		t.Fatalf("failed to create router: %v", err)
	}

	server, err := ws.NewServer(hub, router, ws.ServerConfig{
		AllowedOrigins:  []string{"localhost"},
		MaxMessageBytes: 65536,
	}, logger)
	if err != nil {
		hubCancel()
		router.Close()
		t.Fatalf("failed to create server: %v", err)
	}

	testServer := httptest.NewServer(http.HandlerFunc(server.HandleWebSocket))
	wsURL := "ws://" + strings.TrimPrefix(testServer.URL, "http://") + "/ws"

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	headers := http.Header{}
	headers.Set("Origin", "http://localhost")
	conn, _, err := websocket.DefaultDialer.DialContext(dialCtx, wsURL, headers)
	dialCancel()
	if err != nil {
		hubCancel()
		router.Close()
		testServer.Close()
		t.Fatalf("WebSocket upgrade failed: %v", err)
	}

	return conn, func() {
		conn.Close()
		testServer.Close()
		hubCancel()
		router.Close()
	}
}

// sendAudioFrame writes one inbound 640-byte audio frame (the codec rejects
// any other audio payload size, D-09).
func sendAudioFrame(t *testing.T, conn *websocket.Conn, seq uint32, pcm []byte) {
	t.Helper()
	if len(pcm) != ws.AudioFrameSize {
		t.Fatalf("test bug: audio payload must be exactly %d bytes, got %d", ws.AudioFrameSize, len(pcm))
	}
	frame := ws.EncodeFrame(&ws.FrameHeader{
		Version:  ws.ProtocolVersion,
		MsgType:  ws.MsgTypeAudio,
		Sequence: seq,
	}, pcm)
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatalf("failed to write audio frame %d: %v", seq, err)
	}
}

// sendControlJSON writes one inbound control frame with the given JSON payload.
func sendControlJSON(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()
	frame := ws.EncodeFrame(&ws.FrameHeader{
		Version: ws.ProtocolVersion,
		MsgType: ws.MsgTypeControl,
	}, []byte(payload))
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatalf("failed to write control frame: %v", err)
	}
}

// readFrame reads one server frame and returns its decoded header + payload.
func readFrame(t *testing.T, conn *websocket.Conn) (*ws.FrameHeader, []byte) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read reply frame: %v", err)
	}
	header, payload, err := ws.DecodeFrame(raw)
	if err != nil {
		t.Fatalf("failed to decode reply frame: %v", err)
	}
	return header, payload
}

// testVoiceConfig returns a fast VoiceConfig for tests (short settle bounds).
func testVoiceConfig(maxBytes int) infra.VoiceConfig {
	cfg := infra.DefaultConfig().Voice
	cfg.AssembleWait = 300 * time.Millisecond
	cfg.RequestTimeout = 2 * time.Second
	if maxBytes > 0 {
		cfg.MaxRecordingBytes = maxBytes
	}
	return cfg
}

// TestVoiceCommand_RuleFastPath drives the full pipeline end-to-end through
// the real router: two streamed audio chunks → voice_command trigger →
// rule-classified transcript → FSM push + voice_result frame. The audio and
// control routes are separate worker pools, so this also covers the straggler
// settle path for free.
func TestVoiceCommand_RuleFastPath(t *testing.T) {
	tr := &captureTranscriber{transcript: "写好了"}
	pusher := &recordingPusher{}
	vs := newVoiceService(tr.transcribe, voice.NewHybridClassifier(nil), pusher,
		infra.NewNoOpLogger(), testVoiceConfig(0))

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, vs, infra.NewNoOpLogger()))
	defer teardown()

	// Stream two chunks (sequence order exercises assembler ordering).
	sendAudioFrame(t, conn, 1, make([]byte, ws.AudioFrameSize))
	sendAudioFrame(t, conn, 2, make([]byte, ws.AudioFrameSize))
	sendControlJSON(t, conn, `{"type":"voice_command","params":{}}`)

	header, payload := readFrame(t, conn)
	if header.MsgType != ws.MsgTypeControl {
		t.Fatalf("reply msg type = %s, want control", header.MsgType)
	}
	var result voiceResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("voice_result JSON decode: %v", err)
	}
	if result.Type != "voice_result" {
		t.Fatalf("reply type = %q, want voice_result", result.Type)
	}
	if result.Action != string(voice.ActionNextWord) {
		t.Errorf("action = %q, want next_word (写好了 is a rule fast path)", result.Action)
	}
	if result.Source != "rule" {
		t.Errorf("source = %q, want rule", result.Source)
	}
	if result.Transcript != "写好了" {
		t.Errorf("transcript = %q, want 写好了", result.Transcript)
	}

	// The FSM verb was pushed exactly once for the connection.
	if len(pusher.events) != 1 {
		t.Fatalf("pushed %d events, want 1: %+v", len(pusher.events), pusher.events)
	}
	ev := pusher.events[0]
	if ev.Type != session.EventControlFrame || ev.ControlType != "next_word" {
		t.Errorf("pushed event = %+v, want control frame next_word", ev)
	}
	if len(pusher.byConn) != 1 {
		t.Errorf("push targeted %d connections, want 1", len(pusher.byConn))
	}

	// The transcriber received one WAV carrying both chunks in order.
	if tr.wavCount() != 1 {
		t.Fatalf("transcriber called %d times, want 1", tr.wavCount())
	}
	wav := tr.lastWAV()
	if len(wav) != 44+2*ws.AudioFrameSize {
		t.Errorf("WAV length = %d, want %d", len(wav), 44+2*ws.AudioFrameSize)
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Errorf("WAV magic missing: % x", wav[0:12])
	}
}

// TestVoiceCancel_DiscardsRecording is the regression test for the canceled-
// recording poisoning bug: the client aborts mid-recording (voice_cancel)
// and starts a NEW recording whose sequence numbers restart at 1. Without
// the server-side Drop the assembler would absorb the new chunks as
// duplicates of the canceled ones and transcribe the stale audio; with it,
// the second command's WAV must contain ONLY the second recording's bytes.
func TestVoiceCancel_DiscardsRecording(t *testing.T) {
	tr := &captureTranscriber{transcript: "重读"}
	pusher := &recordingPusher{}
	vs := newVoiceService(tr.transcribe, voice.NewHybridClassifier(nil), pusher,
		infra.NewNoOpLogger(), testVoiceConfig(0))

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, vs, infra.NewNoOpLogger()))
	defer teardown()

	// First (canceled) recording: one chunk, then the client aborts.
	sendAudioFrame(t, conn, 1, bytesFill(0xAA))
	sendControlJSON(t, conn, `{"type":"voice_cancel","params":{}}`)

	// Wait for the cancel ack BEFORE streaming the next recording: audio and
	// control frames cross router routes, so recording 2's chunks could
	// otherwise be processed first and wiped by the Drop.
	ackHeader, ackPayload := readFrame(t, conn)
	if ackHeader.MsgType != ws.MsgTypeControl || !strings.Contains(string(ackPayload), "voice_cancel_ok") {
		t.Fatalf("voice_cancel ack = %s %s, want control voice_cancel_ok", ackHeader.MsgType, ackPayload)
	}

	// Second recording: sequence restarts at 1, two chunks of 0x55.
	sendAudioFrame(t, conn, 1, bytesFill(0x55))
	sendAudioFrame(t, conn, 2, bytesFill(0x55))
	sendControlJSON(t, conn, `{"type":"voice_command","params":{}}`)

	header, payload := readFrame(t, conn)
	if header.MsgType != ws.MsgTypeControl {
		t.Fatalf("reply msg type = %s, want control", header.MsgType)
	}
	var result voiceResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("voice_result JSON decode: %v", err)
	}
	if result.Action != string(voice.ActionRepeat) {
		t.Errorf("action = %q, want repeat (重读)", result.Action)
	}

	// The transcribed WAV must carry ONLY the second recording: no 0xAA
	// payload byte may survive (44-byte header + 2×640 PCM).
	wav := tr.lastWAV()
	if len(wav) != 44+2*ws.AudioFrameSize {
		t.Fatalf("WAV length = %d, want %d (canceled chunk leaked?)", len(wav), 44+2*ws.AudioFrameSize)
	}
	for i, b := range wav[44:] {
		if b != 0x55 {
			t.Fatalf("WAV payload byte %d = 0x%02x, want 0x55 — canceled recording leaked into the new one", i, b)
		}
	}
}

// bytesFill builds an AudioFrameSize payload filled with one byte value.
func bytesFill(v byte) []byte {
	b := make([]byte, ws.AudioFrameSize)
	for i := range b {
		b[i] = v
	}
	return b
}

// TestVoiceCommand_NotACommand: a transcript that is not a command yields
// action "none", no FSM push, and still a voice_result frame (the client
// shows the fallback hint from it).
func TestVoiceCommand_NotACommand(t *testing.T) {
	tr := &captureTranscriber{transcript: "苹果"} // not a command
	pusher := &recordingPusher{}
	vs := newVoiceService(tr.transcribe, voice.NewHybridClassifier(nil), pusher,
		infra.NewNoOpLogger(), testVoiceConfig(0))

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, vs, infra.NewNoOpLogger()))
	defer teardown()

	sendAudioFrame(t, conn, 1, make([]byte, ws.AudioFrameSize))
	sendControlJSON(t, conn, `{"type":"voice_command","params":{}}`)

	_, payload := readFrame(t, conn)
	var result voiceResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("voice_result JSON decode: %v", err)
	}
	if result.Action != string(voice.ActionNone) {
		t.Errorf("action = %q, want none", result.Action)
	}
	if len(pusher.events) != 0 {
		t.Errorf("pushed %+v, want no events for a non-command", pusher.events)
	}
}

// TestVoiceCommand_Disabled: with no voice service configured (no
// MIMO_API_KEY), voice_command rejects with an error frame instead of a
// voice_result, and audio frames are silently dropped (no crash).
func TestVoiceCommand_Disabled(t *testing.T) {
	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, nil, infra.NewNoOpLogger()))
	defer teardown()

	sendAudioFrame(t, conn, 1, make([]byte, ws.AudioFrameSize))
	sendControlJSON(t, conn, `{"type":"voice_command","params":{}}`)

	header, payload := readFrame(t, conn)
	if header.MsgType != ws.MsgTypeError {
		t.Fatalf("reply msg type = %s, want error", header.MsgType)
	}
	var errResp struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &errResp); err != nil {
		t.Fatalf("error frame JSON decode: %v", err)
	}
	if errResp.Message != "voice commands unavailable" {
		t.Errorf("error message = %q, want voice commands unavailable", errResp.Message)
	}
}

// TestVoiceCommand_EmptyRecording: a trigger with no streamed audio still
// replies voice_result (action none) — the client shows the retry/fallback
// hint rather than hanging.
func TestVoiceCommand_EmptyRecording(t *testing.T) {
	tr := &captureTranscriber{transcript: "写好了"}
	pusher := &recordingPusher{}
	vs := newVoiceService(tr.transcribe, voice.NewHybridClassifier(nil), pusher,
		infra.NewNoOpLogger(), testVoiceConfig(0))

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, vs, infra.NewNoOpLogger()))
	defer teardown()

	sendControlJSON(t, conn, `{"type":"voice_command","params":{}}`)

	_, payload := readFrame(t, conn)
	var result voiceResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("voice_result JSON decode: %v", err)
	}
	if result.Action != string(voice.ActionNone) {
		t.Errorf("action = %q, want none for empty recording", result.Action)
	}
	if tr.wavCount() != 0 {
		t.Errorf("transcriber called on empty recording, want 0 calls")
	}
	if len(pusher.events) != 0 {
		t.Errorf("pushed %+v, want no events", pusher.events)
	}
}

// TestHandleAudioFrame_OverflowSpoilsRecording: exceeding the byte cap drops
// the whole recording buffer (a follow-up trigger reports an empty recording,
// not a truncated one) and tells the client.
func TestHandleAudioFrame_OverflowSpoilsRecording(t *testing.T) {
	tr := &captureTranscriber{transcript: "好了"}
	pusher := &recordingPusher{}
	// Cap fits exactly one 640-byte chunk.
	vs := newVoiceService(tr.transcribe, voice.NewHybridClassifier(nil), pusher,
		infra.NewNoOpLogger(), testVoiceConfig(ws.AudioFrameSize))

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, vs, infra.NewNoOpLogger()))
	defer teardown()

	sendAudioFrame(t, conn, 1, make([]byte, ws.AudioFrameSize)) // fits
	sendAudioFrame(t, conn, 2, make([]byte, ws.AudioFrameSize)) // overflows

	// The overflow error frame is best-effort; read it and verify.
	header, payload := readFrame(t, conn)
	if header.MsgType != ws.MsgTypeError {
		t.Fatalf("reply msg type = %s, want error", header.MsgType)
	}
	if !strings.Contains(string(payload), "recording too long") {
		t.Errorf("error payload = %s, want recording too long", payload)
	}

	// The spoiled recording was dropped: the trigger answers action none
	// without an ASR call.
	sendControlJSON(t, conn, `{"type":"voice_command","params":{}}`)
	_, payload = readFrame(t, conn)
	var result voiceResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("voice_result JSON decode: %v", err)
	}
	if result.Action != string(voice.ActionNone) {
		t.Errorf("action = %q, want none after overflow spoiled the recording", result.Action)
	}
	if tr.wavCount() != 0 {
		t.Errorf("transcriber called %d times, want 0 (recording was spoiled)", tr.wavCount())
	}
}
