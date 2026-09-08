package main

// Photo-OCR wiring tests (Ticket 04): end-to-end through a real router +
// WebSocket connection, using the httptest scaffold from voice_test.go. The
// vision API is faked at the ocr.Reader seam (a plain closure) and answer
// judging at the answerJudger seam, so no Mimo call is involved.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// captureReader records every JPEG it received and replies with a fixed
// model reply (the fake at the ocr.Reader seam).
type captureReader struct {
	mu    sync.Mutex
	reply string
	err   error
	jpegs [][]byte
}

func (c *captureReader) read(_ context.Context, _, _ string, jpeg []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j := make([]byte, len(jpeg))
	copy(j, jpeg)
	c.jpegs = append(c.jpegs, j)
	return c.reply, c.err
}

func (c *captureReader) jpegCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.jpegs)
}

func (c *captureReader) lastJPEG() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.jpegs) == 0 {
		return nil
	}
	return c.jpegs[len(c.jpegs)-1]
}

// sendImageFrame writes one inbound variable-size image frame (Ticket 04).
func sendImageFrame(t *testing.T, conn *websocket.Conn, seq uint32, payload []byte) {
	t.Helper()
	frame := ws.EncodeFrame(&ws.FrameHeader{
		Version:  ws.ProtocolVersion,
		MsgType:  ws.MsgTypeImage,
		Sequence: seq,
	}, payload)
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatalf("failed to write image frame %d: %v", seq, err)
	}
}

// testPhotoConfig returns a PhotoConfig for the offline tests. AssembleWait
// keeps the production 2s where the frames actually land (settle exits on
// its 50ms quiet window); tests with NO frames pass a shorter wait so the
// appear-poll does not burn the full window.
func testPhotoConfig(maxBytes int) infra.PhotoConfig {
	cfg := infra.DefaultConfig().Photo
	cfg.RequestTimeout = 2 * time.Second
	if maxBytes > 0 {
		cfg.MaxImageBytes = maxBytes
	}
	return cfg
}

// photoJPEG builds a chunkable fake JPEG: SOI magic prefix + filler.
func photoJPEG(fillByte byte, size int) []byte {
	b := make([]byte, size)
	copy(b, []byte{0xFF, 0xD8, 0xFF, 0xE0})
	for i := 4; i < size; i++ {
		b[i] = fillByte
	}
	return b
}

// TestPhotoSubmit_EndToEnd drives the full pipeline through the real router:
// two streamed image chunks → photo_submit trigger → OCR → judged
// photo_result frame. The image and control routes are separate worker
// pools, so this also covers the straggler settle path for free.
func TestPhotoSubmit_EndToEnd(t *testing.T) {
	rd := &captureReader{reply: `{"text":"apple","confidence":0.92}`}
	judgeCalls := 0
	judger := func(connID, transcript string) (answerJudgement, string) {
		judgeCalls++
		if transcript != "apple" {
			t.Errorf("judger transcript = %q, want apple", transcript)
		}
		return answerJudgement{Expected: "apple", Correct: true, Rating: "good"}, ""
	}
	ps := newPhotoService(rd.read, judger, infra.NewNoOpLogger(), testPhotoConfig(0))

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, nil, ps, infra.NewNoOpLogger()))
	defer teardown()

	// Stream the photo as two chunks (sequence order exercises assembler ordering).
	sendImageFrame(t, conn, 1, photoJPEG(0x11, 32))
	sendImageFrame(t, conn, 2, photoJPEG(0x22, 16))
	sendControlJSON(t, conn, `{"type":"photo_submit","params":{}}`)

	header, payload := readFrame(t, conn)
	if header.MsgType != ws.MsgTypeControl {
		t.Fatalf("reply msg type = %s, want control", header.MsgType)
	}
	var result photoResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("photo_result JSON decode: %v", err)
	}
	if result.Type != "photo_result" {
		t.Fatalf("reply type = %q, want photo_result", result.Type)
	}
	if result.Text != "apple" || result.Confidence != 0.92 {
		t.Errorf("text/confidence = %q/%v, want apple/0.92", result.Text, result.Confidence)
	}
	if result.LowConfidence {
		t.Error("low_confidence = true, want false")
	}
	if result.Error != "" {
		t.Errorf("error = %q, want empty", result.Error)
	}
	if !result.Judged || !result.Correct || result.Expected != "apple" || result.Rating != "good" {
		t.Errorf("judged fields = %+v, want judged/correct/apple/good", result)
	}
	if judgeCalls != 1 {
		t.Errorf("judger called %d times, want 1", judgeCalls)
	}

	// The reader received one JPEG carrying both chunks in order.
	if rd.jpegCount() != 1 {
		t.Fatalf("reader called %d times, want 1", rd.jpegCount())
	}
	jpeg := rd.lastJPEG()
	if len(jpeg) != 48 {
		t.Errorf("JPEG length = %d, want 48", len(jpeg))
	}
	if jpeg[0] != 0xFF || jpeg[1] != 0xD8 || jpeg[2] != 0xFF {
		t.Errorf("JPEG magic missing: % x", jpeg[0:4])
	}
	// Chunk 1 (bytes 0-31): magic + 0x11 filler; chunk 2 (bytes 32-47): its
	// own magic prefix + 0x22 filler (photoJPEG puts the SOI magic in every
	// chunk), so the final filler byte proves chunk 2 sorted after chunk 1.
	if jpeg[4] != 0x11 || jpeg[47] != 0x22 {
		t.Errorf("chunk order wrong: % x ... % x", jpeg[4:6], jpeg[46:48])
	}
}

// TestPhotoSubmit_LowConfidence: a below-threshold OCR result replies
// photo_result with low_confidence=true and the best-guess text, and never
// invokes the judger (the client offers manual input prefilled instead).
func TestPhotoSubmit_LowConfidence(t *testing.T) {
	rd := &captureReader{reply: `{"text":"appl?","confidence":0.4}`}
	judgeCalls := 0
	judger := func(connID, transcript string) (answerJudgement, string) {
		judgeCalls++
		return answerJudgement{}, ""
	}
	ps := newPhotoService(rd.read, judger, infra.NewNoOpLogger(), testPhotoConfig(0))

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, nil, ps, infra.NewNoOpLogger()))
	defer teardown()

	sendImageFrame(t, conn, 1, photoJPEG(0x33, 64))
	sendControlJSON(t, conn, `{"type":"photo_submit","params":{}}`)

	header, payload := readFrame(t, conn)
	if header.MsgType != ws.MsgTypeControl {
		t.Fatalf("reply msg type = %s, want control", header.MsgType)
	}
	var result photoResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("photo_result JSON decode: %v", err)
	}
	if !result.LowConfidence {
		t.Error("low_confidence = true expected")
	}
	if result.Text != "appl?" || result.Confidence != 0.4 {
		t.Errorf("best guess = %q/%v, want appl?/0.4", result.Text, result.Confidence)
	}
	if result.Judged {
		t.Error("judged = true, want false on low confidence")
	}
	if judgeCalls != 0 {
		t.Errorf("judger called %d times, want 0", judgeCalls)
	}
}

// TestPhotoSubmit_JudgeRejects: OCR succeeds but the session guard rejects
// (no session / wrong state) — the reason lands in photo_result.error so the
// client can show it with the manual fallback.
func TestPhotoSubmit_JudgeRejects(t *testing.T) {
	rd := &captureReader{reply: `{"text":"apple","confidence":0.9}`}
	judger := func(connID, transcript string) (answerJudgement, string) {
		return answerJudgement{}, "not in dictating state"
	}
	ps := newPhotoService(rd.read, judger, infra.NewNoOpLogger(), testPhotoConfig(0))

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, nil, ps, infra.NewNoOpLogger()))
	defer teardown()

	sendImageFrame(t, conn, 1, photoJPEG(0x44, 64))
	sendControlJSON(t, conn, `{"type":"photo_submit","params":{}}`)

	header, payload := readFrame(t, conn)
	if header.MsgType != ws.MsgTypeControl {
		t.Fatalf("reply msg type = %s, want control (not an error frame)", header.MsgType)
	}
	var result photoResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("photo_result JSON decode: %v", err)
	}
	if result.Error != "not in dictating state" {
		t.Errorf("error = %q, want not in dictating state", result.Error)
	}
	if result.Judged {
		t.Error("judged = true, want false when the judger rejects")
	}
}

// TestPhotoSubmit_NotJPEG: reassembled bytes without the JPEG SOI signature
// fail before any vision call.
func TestPhotoSubmit_NotJPEG(t *testing.T) {
	rd := &captureReader{reply: `{"text":"apple","confidence":0.9}`}
	ps := newPhotoService(rd.read, func(connID, transcript string) (answerJudgement, string) {
		t.Error("judger must not run for a non-JPEG upload")
		return answerJudgement{}, ""
	}, infra.NewNoOpLogger(), testPhotoConfig(0))

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, nil, ps, infra.NewNoOpLogger()))
	defer teardown()

	sendImageFrame(t, conn, 1, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})
	sendControlJSON(t, conn, `{"type":"photo_submit","params":{}}`)

	header, payload := readFrame(t, conn)
	if header.MsgType != ws.MsgTypeControl {
		t.Fatalf("reply msg type = %s, want control", header.MsgType)
	}
	var result photoResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("photo_result JSON decode: %v", err)
	}
	if result.Error == "" {
		t.Error("error = empty, want a non-empty OCR failure reason")
	}
	if rd.jpegCount() != 0 {
		t.Errorf("reader called %d times, want 0 (validation fails first)", rd.jpegCount())
	}
}

// TestPhotoSubmit_Empty: a trigger with no streamed image replies
// photo_result with an error (the client shows the retry hint rather than
// hanging).
func TestPhotoSubmit_Empty(t *testing.T) {
	rd := &captureReader{reply: `{"text":"apple","confidence":0.9}`}
	// No image ever lands; shorten the appear-poll so the test is fast.
	cfg := testPhotoConfig(0)
	cfg.AssembleWait = 100 * time.Millisecond
	ps := newPhotoService(rd.read, func(connID, transcript string) (answerJudgement, string) {
		t.Error("judger must not run for an empty upload")
		return answerJudgement{}, ""
	}, infra.NewNoOpLogger(), cfg)

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, nil, ps, infra.NewNoOpLogger()))
	defer teardown()

	sendControlJSON(t, conn, `{"type":"photo_submit","params":{}}`)

	_, payload := readFrame(t, conn)
	var result photoResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("photo_result JSON decode: %v", err)
	}
	if result.Error == "" {
		t.Error("error = empty, want a no-photo reason")
	}
	if rd.jpegCount() != 0 {
		t.Errorf("reader called %d times, want 0", rd.jpegCount())
	}
}

// TestPhotoSubmit_Disabled: with no photo service configured (no
// MIMO_API_KEY), photo_submit rejects with an error frame and image frames
// are silently dropped (no crash).
func TestPhotoSubmit_Disabled(t *testing.T) {
	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, nil, nil, infra.NewNoOpLogger()))
	defer teardown()

	sendImageFrame(t, conn, 1, photoJPEG(0x55, 64))
	sendControlJSON(t, conn, `{"type":"photo_submit","params":{}}`)

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
	if errResp.Message != "photo submission unavailable" {
		t.Errorf("error message = %q, want photo submission unavailable", errResp.Message)
	}
}

// TestPhotoChunk_OverflowSpoilsUpload: exceeding the byte cap drops the whole
// image buffer (a follow-up submit reports no photo, not a truncated one)
// and tells the client.
func TestPhotoChunk_OverflowSpoilsUpload(t *testing.T) {
	rd := &captureReader{reply: `{"text":"apple","confidence":0.9}`}
	judger := func(connID, transcript string) (answerJudgement, string) {
		return answerJudgement{}, ""
	}
	// Cap fits exactly one 64-byte chunk; the overflow drops the buffer, so
	// the follow-up submit's appear-poll cannot see a re-created entry.
	cfg := testPhotoConfig(64)
	cfg.AssembleWait = 100 * time.Millisecond
	ps := newPhotoService(rd.read, judger, infra.NewNoOpLogger(), cfg)

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, nil, ps, infra.NewNoOpLogger()))
	defer teardown()

	sendImageFrame(t, conn, 1, photoJPEG(0x66, 64)) // fits
	sendImageFrame(t, conn, 2, photoJPEG(0x77, 64)) // overflows

	header, payload := readFrame(t, conn)
	if header.MsgType != ws.MsgTypeError {
		t.Fatalf("reply msg type = %s, want error", header.MsgType)
	}
	if !strings.Contains(string(payload), "photo too large") {
		t.Errorf("error payload = %s, want photo too large", payload)
	}

	// The spoiled upload was dropped: the trigger answers an error result
	// without a vision call.
	sendControlJSON(t, conn, `{"type":"photo_submit","params":{}}`)
	_, payload = readFrame(t, conn)
	var result photoResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("photo_result JSON decode: %v", err)
	}
	if result.Error == "" {
		t.Error("error = empty, want a no-photo reason after overflow spoiled the upload")
	}
	if rd.jpegCount() != 0 {
		t.Errorf("reader called %d times, want 0 (upload was spoiled)", rd.jpegCount())
	}
}

// TestPhotoCancel_DiscardsUpload mirrors the voice-cancel regression: the
// client aborts mid-upload (photo_cancel) and restarts with sequence numbers
// from 1. Without the server-side Drop the assembler would absorb the new
// chunks as duplicates and OCR the stale photo.
func TestPhotoCancel_DiscardsUpload(t *testing.T) {
	rd := &captureReader{reply: `{"text":"apple","confidence":0.95}`}
	judger := func(connID, transcript string) (answerJudgement, string) {
		return answerJudgement{Expected: "apple", Correct: true, Rating: "good"}, ""
	}
	ps := newPhotoService(rd.read, judger, infra.NewNoOpLogger(), testPhotoConfig(0))

	// Wrap the image route so the test can wait for the canceled chunk to
	// LAND before sending the cancel (handler-return means landed; see the
	// voice twin for the ordering rationale).
	handlers := ttsHandlers(nil, nil, nil, ps, infra.NewNoOpLogger())
	landed := make(chan struct{}, 1)
	imageHandler := handlers[ws.MsgTypeImage]
	handlers[ws.MsgTypeImage] = func(ctx context.Context, c *ws.Client, h *ws.FrameHeader, p []byte) error {
		err := imageHandler(ctx, c, h, p)
		select {
		case landed <- struct{}{}:
		default:
		}
		return err
	}

	conn, teardown := dialVoiceTestServer(t, handlers)
	defer teardown()

	// First (canceled) upload: one chunk of 0xAA.
	sendImageFrame(t, conn, 1, photoJPEG(0xAA, 64))
	select {
	case <-landed:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled chunk never landed in the server buffer")
	}

	sendControlJSON(t, conn, `{"type":"photo_cancel","params":{}}`)

	// Wait for the ack BEFORE streaming the next upload (cross-route race).
	ackHeader, ackPayload := readFrame(t, conn)
	if ackHeader.MsgType != ws.MsgTypeControl || !strings.Contains(string(ackPayload), "photo_cancel_ok") {
		t.Fatalf("photo_cancel ack = %s %s, want control photo_cancel_ok", ackHeader.MsgType, ackPayload)
	}

	// Second upload: sequence restarts at 1, chunk of 0x55.
	sendImageFrame(t, conn, 1, photoJPEG(0x55, 48))
	sendControlJSON(t, conn, `{"type":"photo_submit","params":{}}`)

	_, payload := readFrame(t, conn)
	var result photoResultResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("photo_result JSON decode: %v", err)
	}
	if !result.Judged {
		t.Fatalf("photo_result not judged: %+v", result)
	}

	// The OCR'd JPEG must carry ONLY the second upload.
	jpeg := rd.lastJPEG()
	if len(jpeg) != 48 {
		t.Fatalf("JPEG length = %d, want 48 (canceled chunk leaked?)", len(jpeg))
	}
	for i, b := range jpeg[4:] {
		if b != 0x55 {
			t.Fatalf("JPEG byte %d = 0x%02x, want 0x55 — canceled upload leaked into the new one", i, b)
		}
	}
}

// TestMimoReader_AdaptsChatVision pins the mimo→ocr.Reader adapter wiring
// contract at the type level (the live behavior is covered by the gated live
// test).
func TestMimoReader_IsReaderSeam(t *testing.T) {
	var r ocr.Reader = mimoReader(nil, "mimo-v2.5")
	if r == nil {
		t.Fatal("mimoReader returned nil")
	}
}
