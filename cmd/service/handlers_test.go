package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// TestSendControlFrame_MonotonicSeq verifies D-12: consecutive sendControlFrame
// calls emit strictly increasing Sequence values.
//
// sendControlFrame takes a concrete *ws.Client whose Send() drops into an
// internal buffered channel drained by the unexported writePump, so a
// mock/stub client cannot observe the sent bytes. The test therefore drives a
// real connection: a router handler stashes the server-side *ws.Client on the
// first inbound control frame, then the test calls sendControlFrame three
// times directly and decodes the frames received on the client side.
//
// The package counter is NOT reset here: controlFrameSeq is process-global
// (research A4) and other cmd/service tests may have advanced it. The contract
// under test is strict monotonic increase, not specific starting values.
func TestSendControlFrame_MonotonicSeq(t *testing.T) {
	// serverClient receives the server-side *ws.Client of the first connection.
	// The router handler stashes it so the test can call sendControlFrame
	// directly (the same shape ws.Handler expects in production wiring).
	serverClient := make(chan *ws.Client, 1)
	handlers := map[ws.MessageType]ws.Handler{
		ws.MsgTypeControl: func(_ context.Context, c *ws.Client, _ *ws.FrameHeader, _ []byte) error {
			select {
			case serverClient <- c:
			default:
			}
			return nil
		},
	}

	// Shared hub/router/server/dial scaffold (voice_test.go).
	conn, teardown := dialVoiceTestServer(t, handlers)
	defer teardown()

	// Probe: send one inbound control frame so the router handler stashes the
	// server-side *ws.Client. The probe itself produces no reply.
	probe := ws.EncodeFrame(&ws.FrameHeader{
		Version:  ws.ProtocolVersion,
		MsgType:  ws.MsgTypeControl,
		Sequence: 1,
	}, []byte(`{"type":"test"}`))
	if err := conn.WriteMessage(websocket.BinaryMessage, probe); err != nil {
		t.Fatalf("failed to write probe frame: %v", err)
	}

	var client *ws.Client
	select {
	case client = <-serverClient:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server-side client capture")
	}

	// Three direct sendControlFrame calls with a trivial control payload (D-12).
	for i := 0; i < 3; i++ {
		if err := sendControlFrame(client, ws.MsgTypeControl, map[string]string{"type": "test"}); err != nil {
			t.Fatalf("sendControlFrame call %d failed: %v", i+1, err)
		}
	}

	// Decode the three received frames and assert strict sequence increase.
	seqs := make([]uint32, 3)
	for i := 0; i < 3; i++ {
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("failed to set read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("failed to read reply frame %d: %v", i+1, err)
		}
		header, _, err := ws.DecodeFrame(raw)
		if err != nil {
			t.Fatalf("failed to decode reply frame %d: %v", i+1, err)
		}
		if header.Version != ws.ProtocolVersion {
			t.Errorf("reply frame %d: Version = %d, want ws.ProtocolVersion (%d)", i+1, header.Version, ws.ProtocolVersion)
		}
		seqs[i] = header.Sequence
	}

	if !(seqs[1] > seqs[0]) {
		t.Errorf("D-12 violated: second sequence (%d) must be > first (%d)", seqs[1], seqs[0])
	}
	if !(seqs[2] > seqs[1]) {
		t.Errorf("D-12 violated: third sequence (%d) must be > second (%d)", seqs[2], seqs[1])
	}
}

// TestSubmitAnswer_EmptyTranscriptRejected: an empty/whitespace transcript
// is an incomplete submission, not a wrong answer (Ticket 04 review) — the
// server rejects it with an error frame BEFORE any session lookup or
// judgment, so no spurious "again" rating can be recorded.
func TestSubmitAnswer_EmptyTranscriptRejected(t *testing.T) {
	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, nil, nil, infra.NewNoOpLogger()))
	defer teardown()

	for i, transcript := range []string{"", "   "} {
		sendControlJSON(t, conn, `{"type":"submit_answer","params":{"word_id":"w1","transcript":"`+transcript+`"}}`)

		header, payload := readFrame(t, conn)
		if header.MsgType != ws.MsgTypeError {
			t.Fatalf("case %d: reply msg type = %s, want error", i, header.MsgType)
		}
		var errResp struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(payload, &errResp); err != nil {
			t.Fatalf("case %d: error frame JSON decode: %v", i, err)
		}
		if errResp.Message != "answer is empty" {
			t.Errorf("case %d: error message = %q, want answer is empty", i, errResp.Message)
		}
	}
}
