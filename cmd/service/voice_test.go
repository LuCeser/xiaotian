package main

// Voice-command wiring tests (Ticket 03): end-to-end through a real router
// + WebSocket connection, using the httptest scaffold pattern from
// handlers_test.go. The mimo client is faked at the interface seam (only
// Transcribe is exercised by the voice path) and the classifier runs
// rules-only (voice.NewHybridClassifier(nil)) so no LLM call is involved.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/voice"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
	"go.uber.org/zap"
)

// fakeMimoClient implements mimo.Client for the voice path: Transcribe
// records the WAV it received and replies with a fixed transcript. Every
// other method is a stub — the voice handlers never call them.
type fakeMimoClient struct {
	transcript string
	err        error
	wavs       [][]byte
}

func (f *fakeMimoClient) Synthesize(context.Context, string, ...mimo.TTSOption) (mimo.DecodedAudioData, error) {
	return mimo.DecodedAudioData{}, nil
}

func (f *fakeMimoClient) SynthesizeStream(context.Context, string, mimo.StreamCallback, ...mimo.TTSOption) (*mimo.StreamResult, error) {
	return nil, nil
}

func (f *fakeMimoClient) Transcribe(_ context.Context, audio []byte, _ string, _ ...mimo.ASROption) (mimo.ASRResponse, error) {
	w := make([]byte, len(audio))
	copy(w, audio)
	f.wavs = append(f.wavs, w)
	if f.err != nil {
		return mimo.ASRResponse{}, f.err
	}
	return mimo.ASRResponse{Choices: []mimo.ASRChoice{{Message: mimo.ASRMessageContent{Content: f.transcript}}}}, nil
}

func (f *fakeMimoClient) Chat(context.Context, []mimo.Message, ...mimo.ChatOption) (mimo.ChatResponse, error) {
	return mimo.ChatResponse{}, nil
}

func (f *fakeMimoClient) HealthCheck(context.Context) error { return nil }
func (f *fakeMimoClient) Close() error                      { return nil }

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
	logger := zap.NewNop()

	hub, err := ws.NewHub(ws.HubConfig{Limit: 10, RegisterBuffer: 32, UnregisterBuffer: 32}, logger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}
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
	fakeMimo := &fakeMimoClient{transcript: "写好了"}
	pusher := &recordingPusher{}
	vs := newVoiceService(fakeMimo, voice.NewHybridClassifier(nil), pusher,
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
	if len(fakeMimo.wavs) != 1 {
		t.Fatalf("transcriber called %d times, want 1", len(fakeMimo.wavs))
	}
	wav := fakeMimo.wavs[0]
	if len(wav) != 44+2*ws.AudioFrameSize {
		t.Errorf("WAV length = %d, want %d", len(wav), 44+2*ws.AudioFrameSize)
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Errorf("WAV magic missing: % x", wav[0:12])
	}
}

// TestVoiceCommand_NotACommand: a transcript that is not a command yields
// action "none", no FSM push, and still a voice_result frame (the client
// shows the fallback hint from it).
func TestVoiceCommand_NotACommand(t *testing.T) {
	fakeMimo := &fakeMimoClient{transcript: "苹果"} // not a command
	pusher := &recordingPusher{}
	vs := newVoiceService(fakeMimo, voice.NewHybridClassifier(nil), pusher,
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
	fakeMimo := &fakeMimoClient{transcript: "写好了"}
	pusher := &recordingPusher{}
	vs := newVoiceService(fakeMimo, voice.NewHybridClassifier(nil), pusher,
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
	if len(fakeMimo.wavs) != 0 {
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
	fakeMimo := &fakeMimoClient{transcript: "好了"}
	pusher := &recordingPusher{}
	// Cap fits exactly one 640-byte chunk.
	vs := newVoiceService(fakeMimo, voice.NewHybridClassifier(nil), pusher,
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
	if len(fakeMimo.wavs) != 0 {
		t.Errorf("transcriber called %d times, want 0 (recording was spoiled)", len(fakeMimo.wavs))
	}
}
