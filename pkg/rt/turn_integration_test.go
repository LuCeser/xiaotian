// turn_integration_test.go — in-process whole-chain integration (13-04
// Task 3): a real httptest /rt server + gorilla dialer + fakes, driving the
// binary→VAD→ASR→LLM(tool loop)→TTS→binary round trip over the actual
// websocket transport (review Suggestion 8). Assembly-class defects — sender
// concurrency, binary→VAD→provider→SendAudio routing — regress here instead
// of relying on `var _` checks and hand simulation.
package rt

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// itOrigin is the whitelisted Origin these tests dial with.
const itOrigin = "http://origin.test"

// itServer mounts one /rt handler backed by the given fakes and executor.
func itServer(t *testing.T, asr ASRProvider, llm LLMProvider, tts TTSSynthesizer, exec LessonExecutor) *httptest.Server {
	t.Helper()
	h, err := NewHandler(
		ServerConfig{
			AllowedOrigins:    []string{itOrigin},
			HeartbeatInterval: 50 * time.Millisecond,
		},
		func() SessionManager {
			return NewTurnManager(asr, llm, tts, exec,
				TurnManagerConfig{
					LessonID:        "it-lesson",
					LessonWords:     []string{"苹果", "香蕉", "橘子"},
					MaxToolLoops:    3,
					HistoryMaxTurns: 20,
				},
				NewEventRecorder(infra.NewNoOpLogger(), "it"),
				infra.NewNoOpLogger())
		},
		infra.NewNoOpLogger(),
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.HandleWebSocket))
	t.Cleanup(srv.Close)
	return srv
}

// itDial dials the /rt endpoint with the whitelisted Origin.
func itDial(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	header := http.Header{}
	header.Set("Origin", itOrigin)
	u := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := d.Dial(u, header)
	if err != nil {
		t.Fatalf("dial /rt: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// itMsg is one received websocket message; typ is the control type for text
// frames ("" for binary).
type itMsg struct {
	mt  int
	typ string
	raw []byte
}

// itProbe types a received message (typ is "" for binary frames).
func itProbe(mt int, data []byte) itMsg {
	m := itMsg{mt: mt, raw: data}
	if mt == websocket.TextMessage {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &probe) == nil {
			m.typ = probe.Type
		}
	}
	return m
}

// itRead reads messages until match returns true. One overall deadline
// governs the wait; gorilla forbids reads after ANY read error (even a
// deadline timeout), so an error is terminal here — no retry loop.
func itRead(t *testing.T, conn *websocket.Conn, timeout time.Duration, match func(itMsg) bool) itMsg {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("itRead: no matching message within %v: %v", timeout, err)
		}
		m := itProbe(mt, data)
		if match(m) {
			return m
		}
	}
}

// itDrainUntilTimeout collects every message arriving inside the window and
// then returns. Reads are only retried after SUCCESSFUL reads (gorilla's
// sticky readErr panics on a post-error read); the closing timeout error is
// the expected exit.
func itDrainUntilTimeout(t *testing.T, conn *websocket.Conn, window time.Duration) []itMsg {
	t.Helper()
	var seen []itMsg
	deadline := time.Now().Add(window)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return seen
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return seen // window elapsed with nothing in flight
			}
			t.Fatalf("itDrainUntilTimeout: connection failed inside the quiet window: %v", err)
		}
		seen = append(seen, itProbe(mt, data))
	}
}

// itSendText writes one JSON control frame.
func itSendText(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal control: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write control: %v", err)
	}
}

// TestTurnOverConnHappyPath drives the whole chain over a real websocket:
// session_init → loud sine uplink → VAD close → fake ASR → LLM final text →
// per-sentence streaming TTS → binary audio back — asserting subtitle-first
// ordering, byte-exact PCM passthrough, and the return to listening.
func TestTurnOverConnHappyPath(t *testing.T) {
	const (
		finalText  = "你好，我们开始听写吧。"
		chunkCount = 2
		chunkSize  = 320
		asrText    = "老师好"
	)
	asr := newFakeASR(asrScript{text: asrText})
	llm := newFakeLLM(llmScript{wire: `{"choices":[{"index":0,"message":{"role":"assistant","content":"` + finalText + `"},"finish_reason":"stop"}]}`})
	tts := newFakeTTS(chunkCount, chunkSize)
	// A small per-chunk delay models real provider latency: the subtitle and
	// audio_start control frames are queued before the first chunk, the way
	// a real wire behaves (with zero delay the pump's control/audio channel
	// race can deliver PCM before audio_start).
	tts.delay = 20 * time.Millisecond
	exec := newFakeExecutor("it-lesson", []string{"苹果", "香蕉", "橘子"})
	srv := itServer(t, asr, llm, tts, exec)
	conn := itDial(t, srv)

	// Handshake.
	itSendText(t, conn, SessionInitMsg{
		Type:  TypeSessionInit,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	})
	ready := itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeSessionReady })
	var readyMsg SessionReadyMsg
	if err := json.Unmarshal(ready.raw, &readyMsg); err != nil {
		t.Fatalf("session_ready decode: %v", err)
	}
	if readyMsg.Lesson.LessonID != "it-lesson" || len(readyMsg.Lesson.Words) != 3 {
		t.Errorf("session_ready.lesson = %+v, want it-lesson with 3 words", readyMsg.Lesson)
	}

	// One utterance of loud speech then silence, in 30 ms binary frames.
	loud := vadSine(12000, 30)
	quiet := vadSilence(30)
	for i := 0; i < 50; i++ { // 1.5 s of speech
		if err := conn.WriteMessage(websocket.BinaryMessage, loud); err != nil {
			t.Fatalf("write speech frame: %v", err)
		}
	}
	for i := 0; i < 30; i++ { // 0.9 s of silence closes the segment
		if err := conn.WriteMessage(websocket.BinaryMessage, quiet); err != nil {
			t.Fatalf("write silence frame: %v", err)
		}
	}

	// Collect the turn's messages in arrival order.
	var (
		sawTranscript     string
		teacherIdx        = -1
		audioStartIdx     = -1
		msgIdx            = 0
		binFrames         [][]byte
		sawListeningEnd   bool
		sentenceBeforeAud bool
	)
	deadline := time.Now().Add(10 * time.Second)
	for !sawListeningEnd && time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("happy-path read failed at message %d: %v", msgIdx, err)
		}
		idx := msgIdx
		msgIdx++
		m := itProbe(mt, data)
		switch {
		case m.mt == websocket.BinaryMessage && audioStartIdx >= 0:
			cp := append([]byte(nil), data...)
			binFrames = append(binFrames, cp)
		case m.mt == websocket.TextMessage:
			switch m.typ {
			case TypeTranscript:
				var tm TranscriptMsg
				_ = json.Unmarshal(data, &tm)
				sawTranscript = tm.Text
			case TypeTeacherSentence:
				teacherIdx = idx
			case TypeAudioStart:
				audioStartIdx = idx
			case TypeState:
				var sm StateMsg
				_ = json.Unmarshal(data, &sm)
				if sm.Value == StateListening && audioStartIdx >= 0 {
					sawListeningEnd = true
				}
			}
		}
	}
	if teacherIdx >= 0 && audioStartIdx >= 0 && teacherIdx < audioStartIdx {
		sentenceBeforeAud = true
	}

	if sawTranscript != asrText {
		t.Errorf("transcript = %q, want %q", sawTranscript, asrText)
	}
	if teacherIdx < 0 {
		t.Fatal("no teacher_sentence received")
	}
	if audioStartIdx < 0 {
		t.Fatal("no audio_start received")
	}
	if !sentenceBeforeAud {
		t.Error("teacher_sentence did not precede audio_start (subtitle-first violated)")
	}
	if !sawListeningEnd {
		t.Fatal("never returned to listening after the turn")
	}
	// Byte-exact passthrough: the fake TTS emits chunkCount chunks of the
	// sawtooth pattern byte(i%251).
	if len(binFrames) != chunkCount {
		t.Fatalf("binary audio frames = %d, want %d", len(binFrames), chunkCount)
	}
	for i, f := range binFrames {
		if len(f) != chunkSize {
			t.Errorf("frame %d len = %d, want %d", i, len(f), chunkSize)
			continue
		}
		for j, b := range f {
			if b != byte(j%251) {
				t.Errorf("frame %d byte %d = %d, want %d (PCM mutated in transit)", i, j, b, j%251)
				break
			}
		}
	}
	// One sentence, no tool calls: no lesson_update on this path.
	if llm.callCount() != 1 {
		t.Errorf("llm calls = %d, want 1 (no tool loop in the happy fixture)", llm.callCount())
	}
}

// TestTurnOverConnInterruptMidStream interrupts during TTS playback: the ack
// bumps the epoch and NO binary audio frame arrives afterwards — late chunks
// are dropped, old audio never resurrects (Pitfall 4 / RT-05 server half).
func TestTurnOverConnInterruptMidStream(t *testing.T) {
	asr := newFakeASR(asrScript{text: "停一下"})
	llm := newFakeLLM(llmScript{wire: `{"choices":[{"index":0,"message":{"role":"assistant","content":"这是一句比较长的话，慢慢说给你听。"},"finish_reason":"stop"}]}`})
	tts := newFakeTTS(8, 320) // 8 chunks x 50 ms: enough stream to interrupt
	tts.delay = 50 * time.Millisecond
	exec := newFakeExecutor("it-lesson", []string{"苹果"})
	srv := itServer(t, asr, llm, tts, exec)
	conn := itDial(t, srv)

	itSendText(t, conn, SessionInitMsg{
		Type:  TypeSessionInit,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	})
	itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeSessionReady })

	// Trigger the turn.
	loud := vadSine(12000, 30)
	quiet := vadSilence(30)
	for i := 0; i < 50; i++ {
		_ = conn.WriteMessage(websocket.BinaryMessage, loud)
	}
	for i := 0; i < 30; i++ {
		_ = conn.WriteMessage(websocket.BinaryMessage, quiet)
	}

	// Wait for the FIRST binary audio frame, then interrupt immediately.
	itRead(t, conn, 10*time.Second, func(m itMsg) bool { return m.mt == websocket.BinaryMessage })
	itSendText(t, conn, InterruptMsg{Type: TypeInterrupt, Epoch: 0, LastSentenceID: "t1-s0", PlayedUntilMs: 250})

	ack := itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeInterruptAck })
	var ackMsg InterruptAckMsg
	if err := json.Unmarshal(ack.raw, &ackMsg); err != nil {
		t.Fatalf("interrupt_ack decode: %v", err)
	}
	if ackMsg.NewEpoch <= ackMsg.OldEpoch {
		t.Errorf("epoch = old %d -> new %d, want new > old", ackMsg.OldEpoch, ackMsg.NewEpoch)
	}
	if ackMsg.LastSentenceID != "t1-s0" {
		t.Errorf("last_sentence_id echo = %q, want t1-s0", ackMsg.LastSentenceID)
	}

	// Nothing binary may arrive in the next second (control text such as
	// the post-interrupt state message is allowed).
	rest := itDrainUntilTimeout(t, conn, 1*time.Second)
	for _, m := range rest {
		if m.mt == websocket.BinaryMessage {
			t.Fatalf("binary frame arrived after interrupt: %d bytes — old audio resurrected", len(m.raw))
		}
	}
}
