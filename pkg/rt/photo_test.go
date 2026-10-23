// photo_test.go — behavior tests for the 13.1-03 photo-check channel:
// the per-session upload assembler (contiguous chunks, byte cap with
// reject+clear, cancel/close sweeping — the Pitfall 5 incident shape), the
// OCR verdict prefill riding ApplyVerdict with manual=false (RT-07), and
// the D-05 submit gate (an OCR-only prefill can never complete the unified
// submission — the human confirmation pass is mandatory).
package rt

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// fakeJPEG builds a JPEG-SOI-prefixed byte blob of n bytes (the vision seam
// is a fake — only the SOI validation ever inspects the content).
func fakeJPEG(n int) []byte {
	b := make([]byte, n)
	b[0], b[1], b[2] = 0xFF, 0xD8, 0xFF
	for i := 3; i < n; i++ {
		b[i] = byte(i)
	}
	return b
}

// fakeOCRReader records every image it receives and replies with a scripted
// string (the OCRReader seam — same shape as ocr.Reader).
type fakeOCRReader struct {
	err     error
	images  [][]byte
	replies []string // consumed per call; the last one repeats
	mu      sync.Mutex
}

func (f *fakeOCRReader) read(_ context.Context, systemPrompt, userPrompt string, imageJPEG []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images = append(f.images, append([]byte(nil), imageJPEG...))
	if f.err != nil {
		return "", f.err
	}
	if len(f.replies) == 0 {
		return "[]", nil
	}
	reply := f.replies[0]
	if len(f.replies) > 1 {
		f.replies = f.replies[1:]
	}
	return reply, nil
}

func (f *fakeOCRReader) imageCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.images)
}

func (f *fakeOCRReader) lastImage() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.images) == 0 {
		return nil
	}
	return f.images[len(f.images)-1]
}

// ttBindPhoto injects the photo-check config into a manager already driven
// into the check stage (white-box, same package as ttDriveToCheck).
func ttBindPhoto(tm *TurnManager, reader *fakeOCRReader, maxBytes int) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.cfg.OCR = reader.read
	if maxBytes > 0 {
		tm.cfg.PhotoMaxBytes = maxBytes
	}
	tm.ocrPipeline = ocr.NewPipeline(ocr.Reader(tm.cfg.OCR), tm.cfg.PhotoConfidence)
}

// TestPhotoRequestTimeoutFollowsConfigDefault (Issue #44): the /rt OCR
// bound must defer to the photo config block's default — never a second
// frozen literal in this package — while an injected value wins as-is.
func TestPhotoRequestTimeoutFollowsConfigDefault(t *testing.T) {
	cards := ttCards("苹果")
	exec := newFakeExecutor(ttLessonID, cards)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(newFakeWords(cards)), nil, nil)
	t.Cleanup(tm.internalClose)
	want := infra.DefaultConfig().Photo.RequestTimeout
	if got := tm.cfg.PhotoRequestTimeout; got != want {
		t.Fatalf("unset PhotoRequestTimeout = %v, want photo config default %v", got, want)
	}
	custom := 42 * time.Second
	cfg := ttConfigWith(newFakeWords(cards), func(c *TurnManagerConfig) {
		c.PhotoRequestTimeout = custom
	})
	tm2 := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, cfg, nil, nil)
	t.Cleanup(tm2.internalClose)
	if got := tm2.cfg.PhotoRequestTimeout; got != custom {
		t.Fatalf("explicit PhotoRequestTimeout = %v, want %v", got, custom)
	}
}

// ttPhotoChunk ships one photo_chunk control frame.
func ttPhotoChunk(t *testing.T, tm *TurnManager, s *captureSender, seq int, chunk []byte, final bool) {
	t.Helper()
	tm.HandleControl(s, mustJSON(t, PhotoChunkMsg{
		Type:  TypePhotoChunk,
		Seq:   seq,
		Data:  base64.StdEncoding.EncodeToString(chunk),
		Final: final,
	}))
}

// ttShipPhoto ships jpeg in 32KiB chunks, final flag on the last one.
func ttShipPhoto(t *testing.T, tm *TurnManager, s *captureSender, jpeg []byte) {
	t.Helper()
	const chunkN = 32768
	seq := 0
	for off := 0; off < len(jpeg); off += chunkN {
		end := off + chunkN
		if end > len(jpeg) {
			end = len(jpeg)
		}
		seq++
		ttPhotoChunk(t, tm, s, seq, jpeg[off:end], end == len(jpeg))
	}
}

// ttLastOcrEntries decodes the latest ocr_result's entries.
func ttLastOcrEntries(t *testing.T, s *captureSender) []map[string]any {
	t.Helper()
	got := s.controlsOf(t, TypeOcrResult)
	if len(got) == 0 {
		t.Fatal("no ocr_result message on the wire")
	}
	raw, ok := got[len(got)-1]["entries"].([]any)
	if !ok {
		t.Fatalf("ocr_result entries = %v, want an array", got[len(got)-1]["entries"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		out = append(out, e.(map[string]any))
	}
	return out
}

// ttLastVerdicts decodes the latest check_verdicts echo into
// word_index → {correct, manual}.
func ttLastVerdicts(t *testing.T, s *captureSender) map[int]map[string]bool {
	t.Helper()
	got := s.controlsOf(t, TypeCheckVerdicts)
	if len(got) == 0 {
		t.Fatal("no check_verdicts echo on the wire")
	}
	raw, ok := got[len(got)-1]["verdicts"].([]any)
	if !ok {
		t.Fatalf("verdicts = %v, want an array", got[len(got)-1]["verdicts"])
	}
	out := map[int]map[string]bool{}
	for _, v := range raw {
		m := v.(map[string]any)
		idx := int(m["word_index"].(float64))
		out[idx] = map[string]bool{"correct": m["correct"] == true, "manual": m["manual"] == true}
	}
	return out
}

// ttCheckStageStillOpen asserts the tutor is still inside the check stage.
func ttCheckStageStillOpen(t *testing.T, tm *TurnManager) {
	t.Helper()
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.tutor == nil || !tm.tutor.checking {
		t.Fatal("the check stage closed; it must stay open until the gated submit")
	}
}

// ---------------------------------------------------------------------------
// TestPhotoAssembler — the upload buffer discipline
// ---------------------------------------------------------------------------

func TestPhotoAssembler(t *testing.T) {
	t.Run("sequential chunks assemble and reach OCR exactly once", func(t *testing.T) {
		cards := ttCards("苹果", "香蕉")
		tm, s, log, _ := ttDriveToCheck(t, cards, newFakeWords(cards))
		reader := &fakeOCRReader{replies: []string{
			`[{"text":"苹果","confidence":0.95},{"text":"香蕉","confidence":0.9}]`,
		}}
		ttBindPhoto(tm, reader, 0)

		jpeg := fakeJPEG(100000)
		ttShipPhoto(t, tm, s, jpeg)

		if !waitFor(t, 5*time.Second, func() bool { return reader.imageCount() == 1 }) {
			t.Fatalf("OCR calls = %d, want 1", reader.imageCount())
		}
		if got := reader.lastImage(); !bytes.Equal(got, jpeg) {
			t.Fatalf("reassembled photo = %d bytes, want the original %d", len(got), len(jpeg))
		}
		if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeOcrResult) == 1 }) {
			t.Fatal("ocr_result never arrived after the completed upload")
		}

		// Privacy prohibition P2 (T-13.1-08): the photo bytes exist ONLY in
		// the in-memory buffer and the one OCR call — they never enter the
		// event stream (or any log line the recorder captured).
		b64 := base64.StdEncoding.EncodeToString(jpeg)
		for _, e := range log.snapshot() {
			for _, f := range e.fields {
				if s, ok := f.Value.(string); ok && strings.Contains(s, b64[:64]) {
					t.Fatalf("image bytes leaked into the event stream: %s=%q", f.Key, s)
				}
			}
		}
	})

	t.Run("over-cap rejects the upload AND clears the buffer", func(t *testing.T) {
		cards := ttCards("苹果")
		tm, s, _, _ := ttDriveToCheck(t, cards, newFakeWords(cards))
		reader := &fakeOCRReader{replies: []string{`[{"text":"苹果","confidence":0.9}]`}}
		ttBindPhoto(tm, reader, 4096) // tiny cap so two 3KiB chunks overflow it

		ttPhotoChunk(t, tm, s, 1, fakeJPEG(3000), false)
		ttPhotoChunk(t, tm, s, 2, fakeJPEG(3000), true) // 6000 > 4096: reject + clear
		if got := s.countOf(TypeError); got == 0 {
			t.Fatal("no coded error answered the over-cap upload")
		}

		// The buffer is GONE: a fresh upload starting at seq 1 assembles only
		// its own bytes — the stale first chunk must not leak into it.
		fresh := fakeJPEG(2000)
		ttPhotoChunk(t, tm, s, 1, fresh, true)
		if !waitFor(t, 5*time.Second, func() bool { return reader.imageCount() == 1 }) {
			t.Fatal("the fresh upload never reached OCR")
		}
		if got := reader.lastImage(); !bytes.Equal(got, fresh) {
			t.Fatalf("OCR image = %d bytes, want only the fresh 2000", len(got))
		}
	})

	t.Run("out-of-order and duplicate sequence chunks are rejected", func(t *testing.T) {
		cards := ttCards("苹果")
		tm, s, _, _ := ttDriveToCheck(t, cards, newFakeWords(cards))
		reader := &fakeOCRReader{replies: []string{`[{"text":"苹果","confidence":0.9}]`}}
		ttBindPhoto(tm, reader, 0)

		first := fakeJPEG(2000)
		second := fakeJPEG(1500)

		ttPhotoChunk(t, tm, s, 2, fakeJPEG(1000), false) // seq 2 with no seq 1
		if got := s.countOf(TypeError); got == 0 {
			t.Fatal("no error for a chunk arriving before seq 1")
		}

		ttPhotoChunk(t, tm, s, 1, first, false) // upload A starts
		ttPhotoChunk(t, tm, s, 2, fakeJPEG(1000), false)
		ttPhotoChunk(t, tm, s, 2, fakeJPEG(700), false) // duplicate seq 2
		if got := s.countOf(TypeError); got < 2 {
			t.Fatalf("error frames = %d, want ≥2 (the duplicate was absorbed)", got)
		}

		ttPhotoChunk(t, tm, s, 1, second, false) // seq 1 RESETS: upload B
		ttPhotoChunk(t, tm, s, 2, fakeJPEG(1000), true)

		if !waitFor(t, 5*time.Second, func() bool { return reader.imageCount() == 1 }) {
			t.Fatalf("OCR calls = %d, want 1", reader.imageCount())
		}
		want := append(append([]byte(nil), second...), fakeJPEG(1000)...)
		if got := reader.lastImage(); !bytes.Equal(got, want) {
			t.Fatalf("OCR image = %d bytes, want only upload B's %d", len(got), len(want))
		}
	})

	t.Run("cancel clears the buffer (retake discipline)", func(t *testing.T) {
		cards := ttCards("苹果")
		tm, s, log, _ := ttDriveToCheck(t, cards, newFakeWords(cards))
		reader := &fakeOCRReader{replies: []string{`[{"text":"苹果","confidence":0.9}]`}}
		ttBindPhoto(tm, reader, 0)

		ttPhotoChunk(t, tm, s, 1, fakeJPEG(2000), false)
		tm.HandleControl(s, mustJSON(t, PhotoCancelMsg{Type: TypePhotoCancel}))
		if got := ttEventCount(t, log, EventPhotoCanceled); got != 1 {
			t.Fatalf("photo_canceled events = %d, want 1", got)
		}

		fresh := fakeJPEG(1500)
		ttPhotoChunk(t, tm, s, 1, fresh, true)
		if !waitFor(t, 5*time.Second, func() bool { return reader.imageCount() == 1 }) {
			t.Fatal("the retaken photo never reached OCR")
		}
		if got := reader.lastImage(); !bytes.Equal(got, fresh) {
			t.Fatalf("OCR image = %d bytes, want only the retaken 1500", len(got))
		}
	})

	t.Run("session close sweeps the buffer", func(t *testing.T) {
		cards := ttCards("苹果")
		tm, s, _, _ := ttDriveToCheck(t, cards, newFakeWords(cards))
		reader := &fakeOCRReader{replies: []string{`[{"text":"苹果","confidence":0.9}]`}}
		ttBindPhoto(tm, reader, 0)

		ttPhotoChunk(t, tm, s, 1, fakeJPEG(2000), false)
		errsBefore := s.countOf(TypeError)
		tm.Close(nil) // the transport read loop exited

		ttPhotoChunk(t, tm, s, 2, fakeJPEG(1000), true)
		if got := s.countOf(TypeError); got != errsBefore {
			t.Fatal("the post-close photo frame was answered — session_ended is terminal")
		}
		time.Sleep(150 * time.Millisecond)
		if got := reader.imageCount(); got != 0 {
			t.Fatalf("OCR calls after close = %d, want 0 (buffer must be swept)", got)
		}
	})

	t.Run("nil OCR degrades honestly and the manual path keeps working", func(t *testing.T) {
		cards := ttCards("苹果")
		tm, s, _, _ := ttDriveToCheck(t, cards, newFakeWords(cards))
		// NO ttBindPhoto: no OCR reader configured server-side.

		ttPhotoChunk(t, tm, s, 1, fakeJPEG(2000), false)
		errs := s.controlsOf(t, TypeError)
		if len(errs) == 0 {
			t.Fatal("no degraded ErrorMsg answered the photo chunk")
		}
		last := errs[len(errs)-1]
		if last["code"] != ErrCodePhotoUnavailable {
			t.Fatalf("error code = %v, want %s", last["code"], ErrCodePhotoUnavailable)
		}
		if last["message"] != photoUnavailableNotice {
			t.Fatalf("error message = %v, want the fixed child-friendly wording", last["message"])
		}

		// The check stage keeps working manual-only (RT-10 degraded family).
		tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: "req-manual-0", WordIndex: 0, Correct: true,
		}))
		if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeCheckVerdicts) > 0 }) {
			t.Fatal("the manual verdict echo never arrived (degraded path broke the check stage)")
		}
		if vs := ttLastVerdicts(t, s); len(vs) != 1 || !vs[0]["manual"] {
			t.Fatalf("manual verdict echo = %v, want word 0 manual", vs)
		}
	})
}

// ---------------------------------------------------------------------------
// TestOcrVerdictPrefill — OCR judgement → ApplyVerdict(manual=false) (RT-07)
// ---------------------------------------------------------------------------

func TestOcrVerdictPrefill(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	tm, s, log, _ := ttDriveToCheck(t, cards, newFakeWords(cards))
	// OCR reads 苹菓 for 苹果 (one substituted character → near-miss) and
	// 小狗 for 香蕉 (a different word entirely → plain wrong).
	reader := &fakeOCRReader{replies: []string{
		`[{"text":"苹菓","confidence":0.9},{"text":"小狗","confidence":0.85}]`,
	}}
	ttBindPhoto(tm, reader, 0)

	ttShipPhoto(t, tm, s, fakeJPEG(2000))
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeOcrResult) == 1 }) {
		t.Fatal("ocr_result never arrived")
	}

	entries := ttLastOcrEntries(t, s)
	if len(entries) != 2 {
		t.Fatalf("ocr_result entries = %d, want 2", len(entries))
	}
	e0, e1 := entries[0], entries[1]
	if e0["word_index"].(float64) != 0 || e0["ocr_text"] != "苹菓" || e0["correct"] != false || e0["near_miss"] != true {
		t.Errorf("entry0 = %v, want word_index 0 / 苹菓 / correct false / near_miss true", e0)
	}
	if e1["word_index"].(float64) != 1 || e1["ocr_text"] != "小狗" || e1["correct"] != false || e1["near_miss"] != false {
		t.Errorf("entry1 = %v, want word_index 1 / 小狗 / correct false / near_miss false", e1)
	}

	// The prefill rode the executor's verdict path with manual=false —
	// the authoritative echo says so (RT-07 single render source).
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeCheckVerdicts) > 0 }) {
		t.Fatal("check_verdicts echo never arrived after the prefill")
	}
	vs := ttLastVerdicts(t, s)
	if len(vs) != 2 || vs[0] == nil || vs[1] == nil {
		t.Fatalf("verdicts = %v, want prefills for both indexes", vs)
	}
	if vs[0]["manual"] || vs[1]["manual"] {
		t.Errorf("prefills arrived manual: %v — OCR must never satisfy the human gate", vs)
	}
	if vs[0]["correct"] || vs[1]["correct"] {
		t.Errorf("verdicts = %v, want both wrong (near-miss is still wrong)", vs)
	}

	// The ocr_completed event carries counts only — never word text.
	if got := ttEventCount(t, log, EventOcrCompleted); got != 1 {
		t.Errorf("ocr_completed events = %d, want 1", got)
	}
	if got := ttEventField(t, log, EventOcrCompleted, "recognized"); len(got) != 1 || got[0] != 2 {
		t.Errorf("ocr_completed recognized = %v, want [2]", got)
	}
	if got := ttEventField(t, log, EventOcrCompleted, "total"); len(got) != 1 || got[0] != 2 {
		t.Errorf("ocr_completed total = %v, want [2]", got)
	}

	// A later manual flip OVERWRITES the prefill (D-06①: the submitted
	// rating follows the human verdict, never the OCR guess).
	tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
		Type: TypeCheckVerdict, RequestID: "req-manual-0", WordIndex: 0, Correct: true,
	}))
	if !waitFor(t, 5*time.Second, func() bool {
		vs := ttLastVerdicts(t, s)
		return vs[0] != nil && vs[0]["manual"] && vs[0]["correct"]
	}) {
		t.Fatalf("manual flip never overwrote the prefill: %v", ttLastVerdicts(t, s))
	}
	if vs := ttLastVerdicts(t, s); vs[1]["manual"] {
		t.Errorf("word 1 became manual without any human action: %v", vs)
	}
}

// ---------------------------------------------------------------------------
// TestOcrSubmitRequiresManualConfirmation — the D-05 gate
// ---------------------------------------------------------------------------

func TestOcrSubmitRequiresManualConfirmation(t *testing.T) {
	cards := ttCards("苹果")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	tm, s, _, exec := ttDriveToCheck(t, cards, words)
	// OCR prefills CORRECT for the only word — the strongest case for the
	// zero-human-action submit the gate must refuse.
	reader := &fakeOCRReader{replies: []string{`[{"text":"苹果","confidence":0.95}]`}}
	ttBindPhoto(tm, reader, 0)
	ttShipPhoto(t, tm, s, fakeJPEG(2000))
	if !waitFor(t, 5*time.Second, func() bool {
		return s.countOf(TypeOcrResult) == 1 && s.countOf(TypeCheckVerdicts) > 0
	}) {
		t.Fatal("the OCR prefill never landed")
	}

	// Immediate check_submit: refused — ErrorMsg, no stage transition, no
	// SubmitReview.
	errBefore := s.countOf(TypeError)
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-ocr-submit"}))
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeError) > errBefore }) {
		t.Fatal("the zero-human-action submit was not refused with an ErrorMsg")
	}
	if got := s.countOf(TypeReviewResult); got != 0 {
		t.Fatalf("review_result messages = %d, want 0", got)
	}
	if got := len(words.submissions()); got != 0 {
		t.Fatalf("SubmitReview calls = %d, want 0 (the gate must fire before the write)", got)
	}
	// The prefill already used the executor; the refused submit must NOT
	// have added a complete_check call.
	for _, c := range exec.checkCalls {
		if c.tool == "complete_check" {
			t.Fatal("complete_check ran for a submit the gate should have refused")
		}
	}
	ttCheckStageStillOpen(t, tm)

	// The human confirmation pass: a manual flip that DISAGREES with the OCR
	// prefill (the student actually wrote it wrong) — the submitted rating
	// must follow the human verdict (D-06①).
	tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
		Type: TypeCheckVerdict, RequestID: "req-manual-0", WordIndex: 0, Correct: false,
	}))
	if !waitFor(t, 5*time.Second, func() bool {
		vs := ttLastVerdicts(t, s)
		return vs[0] != nil && vs[0]["manual"] && !vs[0]["correct"]
	}) {
		t.Fatalf("manual flip never landed: %v", ttLastVerdicts(t, s))
	}

	result := ttSubmitCheck(t, tm, s)
	if result["updated"].(float64) != 1 {
		t.Errorf("review_result.updated = %v, want 1", result["updated"])
	}
	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1", len(subs))
	}
	if subs[0]["苹果"] != "again" {
		t.Errorf("submitted rating = %v, want 苹果:again (人工改判为准 — the manual verdict overrides the OCR prefill)", subs[0])
	}
}

// ---------------------------------------------------------------------------
// Wire contract — the photo message family (mirror discipline)
// ---------------------------------------------------------------------------

func TestProtocolPhotoRoundTrip(t *testing.T) {
	chunk := PhotoChunkMsg{Type: TypePhotoChunk, Seq: 2, Data: "aGVsbG8=", Final: true}
	got := roundTrip(t, chunk, PhotoChunkMsg{}).(PhotoChunkMsg)
	if got != chunk {
		t.Errorf("round trip = %+v, want %+v", got, chunk)
	}
	w := wireKeys(t, chunk)
	if got := wantString(t, w, "type"); got != "photo_chunk" {
		t.Errorf("wire type = %q, want photo_chunk", got)
	}
	if got := wantNumber(t, w, "seq"); got != 2 {
		t.Errorf("wire seq = %v, want 2", got)
	}
	if got := wantString(t, w, "data"); got != "aGVsbG8=" {
		t.Errorf("wire data = %q, want the base64 payload", got)
	}
	if w["final"] != true {
		t.Errorf("wire final = %v, want true", w["final"])
	}

	cancel := PhotoCancelMsg{Type: TypePhotoCancel}
	gotCancel := roundTrip(t, cancel, PhotoCancelMsg{}).(PhotoCancelMsg)
	if gotCancel != cancel {
		t.Errorf("round trip = %+v, want %+v", gotCancel, cancel)
	}
	if got := wantString(t, wireKeys(t, cancel), "type"); got != "photo_cancel" {
		t.Errorf("wire type = %q, want photo_cancel", got)
	}

	res := OcrResultMsg{Type: TypeOcrResult, Entries: []OcrResultEntry{
		{WordIndex: 1, OcrText: "aple", Correct: false, NearMiss: true},
	}}
	gotRes := roundTrip(t, res, OcrResultMsg{}).(OcrResultMsg)
	if len(gotRes.Entries) != 1 || gotRes.Entries[0] != res.Entries[0] {
		t.Errorf("round trip = %+v, want %+v", gotRes, res)
	}
	wr := wireKeys(t, res)
	if got := wantString(t, wr, "type"); got != "ocr_result" {
		t.Errorf("wire type = %q, want ocr_result", got)
	}
	entry := wr["entries"].([]any)[0].(map[string]any)
	if got := wantNumber(t, entry, "word_index"); got != 1 {
		t.Errorf("wire entries[0].word_index = %v, want 1", got)
	}
	if got := wantString(t, entry, "ocr_text"); got != "aple" {
		t.Errorf("wire entries[0].ocr_text = %q, want aple", got)
	}
	if entry["correct"] != false || entry["near_miss"] != true {
		t.Errorf("wire entries[0] flags = %v/%v, want correct false / near_miss true", entry["correct"], entry["near_miss"])
	}
}
