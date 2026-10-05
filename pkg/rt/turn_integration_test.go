// turn_integration_test.go — in-process whole-chain integration harness: a
// real httptest /rt server + gorilla dialer + fakes, driving the
// binary→VAD→ASR→classify→FSM dispatch→TTS→binary round trip over the
// actual websocket transport. The dictation-flow integration cases
// (TestDictationOverConn*) land in 13-08 Task 3; the helpers here are the
// shared 13-04 harness (review Suggestion 8).
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

// itServer mounts one /rt handler backed by the given fakes, executor, and
// word source.
func itServer(t *testing.T, asr ASRProvider, llm LLMProvider, tts TTSSynthesizer, exec LessonExecutor, words WordSource) *httptest.Server {
	t.Helper()
	h, err := NewHandler(
		ServerConfig{
			AllowedOrigins:    []string{itOrigin},
			HeartbeatInterval: 50 * time.Millisecond,
		},
		func() SessionManager {
			return NewTurnManager(asr, llm, tts, exec,
				TurnManagerConfig{
					LessonID: "it-lesson",
					Words:    words,
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

// itSendUtterance writes one loud-speech + silence sequence that closes a
// VAD segment (one turn).
func itSendUtterance(t *testing.T, conn *websocket.Conn) {
	t.Helper()
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
}
