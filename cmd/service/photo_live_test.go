package main

// Live photo-OCR verification against the REAL Mimo vision service
// (Ticket 04 final gap). photo_test.go fakes the ocr.Reader and the judger;
// this file builds the production stack — mimo client + mimoReader
// (ChatVision) + the real pkg/ocr pipeline (real prompt, real JSON parsing)
// — and drives a committed handwriting-style JPEG through the real
// photo_submit handler over the socket:
//
//	testdata/handwriting-apple.jpg     → real vision OCR → judged "apple"
//	testdata/handwriting-piaoliang.jpg → real vision OCR → judged "漂亮"
//
// Gated on MIMO_API_KEY via the shared liveAPIKey loader (voice_live_test.go
// twin); without a key the test skips, so offline runs stay green. The
// judging seam stays faked — the session/FSRS layer is not what this test
// verifies. Each run costs 1 vision call per case.
//
// The fixtures are generated (jittered per-character rendering, see
// .scratch/fixgen); real 6-12 handwriting accuracy remains UAT on-device.

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/grading"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// livePhotoChunkBytes chunks the fixture JPEG at 8KiB so the live path
// exercises multi-frame reassembly (production chunks at 32KiB; the wire
// contract is identical).
const livePhotoChunkBytes = 8192

// TestPhotoOCR_LiveRealVision streams both handwriting fixtures through the
// real router + real Mimo vision OCR and asserts the recognized text judges
// correct. Vision thinking over an image takes well past the offline 5s read
// deadline — reads use 60s.
func TestPhotoOCR_LiveRealVision(t *testing.T) {
	apiKey := liveAPIKey(t)

	client, err := mimo.NewClient(mimo.ClientConfig{
		APIKey:  apiKey,
		BaseURL: infra.DefaultConfig().Voice.BaseURL,
		Timeout: infra.DefaultConfig().Photo.RequestTimeout,
	}, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("live mimo client construction failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Fake judger records the OCR'd transcript; the live assertion compares
	// it against the expected word AFTER the real pipeline ran.
	var judgedText string
	judger := func(connID, transcript string) (answerJudgement, string) {
		judgedText = transcript
		return answerJudgement{Expected: "fixture", Correct: true, Rating: "good"}, ""
	}

	cfg := infra.DefaultConfig().Photo
	inner := mimoReader(client, cfg.ChatModel)
	debugReader := func(ctx context.Context, s, u string, img []byte) (string, error) {
		reply, rerr := inner(ctx, s, u, img)
		t.Logf("RAW REPLY: %q", reply)
		return reply, rerr
	}
	ps := newPhotoService(debugReader, judger, infra.NewNoOpLogger(), cfg)

	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, nil, ps, infra.NewNoOpLogger()))
	defer teardown()

	cases := []struct {
		name     string
		fixture  string
		wantWord string
		lang     string
	}{
		{"english word apple", "testdata/handwriting-apple.jpg", "apple", "en"},
		{"chinese word 漂亮", "testdata/handwriting-piaoliang.jpg", "漂亮", "zh"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jpeg, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatalf("fixture read failed: %v", err)
			}

			for offset, seq := 0, uint32(0); offset < len(jpeg); offset, seq = offset+livePhotoChunkBytes, seq+1 {
				end := offset + livePhotoChunkBytes
				if end > len(jpeg) {
					end = len(jpeg)
				}
				sendImageFrame(t, conn, seq+1, jpeg[offset:end])
			}
			sendControlJSON(t, conn, `{"type":"photo_submit","params":{}}`)

			header, payload := readFrameWithTimeout(t, conn, 60*time.Second)
			if header.MsgType != ws.MsgTypeControl {
				t.Fatalf("reply msg type = %s, want control (got payload %s)", header.MsgType, payload)
			}
			var result photoResultResponse
			if err := json.Unmarshal(payload, &result); err != nil {
				t.Fatalf("photo_result JSON decode: %v", err)
			}

			if result.Error != "" {
				t.Fatalf("live OCR failed: %q (confidence %v)", result.Error, result.Confidence)
			}
			if result.LowConfidence {
				t.Fatalf("live OCR under threshold: text %q confidence %v", result.Text, result.Confidence)
			}
			if !result.Judged {
				t.Fatalf("photo_result not judged: %+v", result)
			}

			got := grading.Normalize(result.Text, tc.lang)
			want := grading.Normalize(tc.wantWord, tc.lang)
			if got != want {
				t.Errorf("live OCR text = %q (normalized %q), want %q", result.Text, got, want)
			}
			if judgedText != result.Text {
				t.Errorf("judger saw %q, want the OCR text %q", judgedText, result.Text)
			}
			t.Logf("live OCR %q → text=%q confidence=%.2f", tc.fixture, result.Text, result.Confidence)
		})
	}
}
