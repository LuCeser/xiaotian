package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
)

// wavDataLen converts a payload length to the WAV data-size field.
func wavDataLen(n int) (uint32, bool) {
	if n < 0 {
		return 0, false
	}
	if uint64(n) > math.MaxUint32 {
		return 0, false
	}
	return uint32(n), true
}

// stubClassifier records the transcripts it saw and replies from a table.
type stubClassifier struct {
	byTranscript map[string]Intent
	fallback     Intent
	calls        []string
}

func (s *stubClassifier) Classify(_ context.Context, transcript string) (Intent, error) {
	s.calls = append(s.calls, transcript)
	if it, ok := s.byTranscript[transcript]; ok {
		return it, nil
	}
	return s.fallback, nil
}

// captureTranscriber records the WAV bytes it received and replies from a
// table (empty table entry ⇒ empty transcript).
type captureTranscriber struct {
	err   error
	reply string
	wavs  [][]byte
}

func (c *captureTranscriber) transcribe(_ context.Context, wav []byte) (string, error) {
	w := make([]byte, len(wav))
	copy(w, wav)
	c.wavs = append(c.wavs, w)
	if c.err != nil {
		return "", c.err
	}
	return c.reply, nil
}

// validWAVHeader checks the 44-byte RIFF/WAVE header WrapWAV produces:
// magic strings, PCM format, mono 16kHz 16-bit, and data size matching the
// payload length.
func validWAVHeader(wav []byte, payloadLen int) bool {
	if len(wav) != 44+payloadLen {
		return false
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return false
	}
	if binary.LittleEndian.Uint32(wav[16:20]) != 16 { // fmt chunk size
		return false
	}
	if binary.LittleEndian.Uint16(wav[20:22]) != 1 { // PCM
		return false
	}
	if binary.LittleEndian.Uint16(wav[22:24]) != 1 { // mono
		return false
	}
	if binary.LittleEndian.Uint32(wav[24:28]) != 16000 { // sample rate
		return false
	}
	if binary.LittleEndian.Uint16(wav[34:36]) != 16 { // bits per sample
		return false
	}
	dataLen, ok := wavDataLen(payloadLen)
	if !ok || binary.LittleEndian.Uint32(wav[40:44]) != dataLen {
		return false
	}
	return true
}

// TestPipeline_Process_WAVContract pins the ASR wire contract: the
// transcriber seam receives a self-describing WAV (44-byte RIFF header,
// PCM 16kHz mono) carrying the PCM payload verbatim — not bare PCM, which
// the Mimo ASR audio input rejects.
func TestPipeline_Process_WAVContract(t *testing.T) {
	tr := &captureTranscriber{reply: "好了"}
	cl := &stubClassifier{
		byTranscript: map[string]Intent{"好了": {Action: ActionNextWord, Source: "rule"}},
	}
	p := NewPipeline(tr.transcribe, cl)

	pcm := []byte{0x01, 0x02, 0x03, 0x04} // arbitrary PCM payload
	if _, err := p.Process(context.Background(), pcm); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(tr.wavs) != 1 {
		t.Fatalf("transcriber called %d times, want 1", len(tr.wavs))
	}
	if !validWAVHeader(tr.wavs[0], len(pcm)) {
		t.Errorf("transcriber received non-contract WAV: % x", tr.wavs[0][:44])
	}
	if got := tr.wavs[0][44:]; string(got) != string(pcm) {
		t.Errorf("WAV payload = % x, want PCM verbatim % x", got, pcm)
	}
}

// TestPipeline_Process_ClassifiesTranscript verifies the transcript from the
// seam flows to the classifier and the classified Intent is returned
// unchanged (the pipeline adds no classification logic of its own).
func TestPipeline_Process_ClassifiesTranscript(t *testing.T) {
	want := Intent{Action: ActionPause, Transcript: "停一下", Confidence: 0.9, Source: "llm"}
	tr := &captureTranscriber{reply: "停一下"}
	cl := &stubClassifier{byTranscript: map[string]Intent{"停一下": want}}
	p := NewPipeline(tr.transcribe, cl)

	got, err := p.Process(context.Background(), []byte{0x00, 0x01})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if got != want {
		t.Errorf("Process intent = %+v, want %+v", got, want)
	}
}

// TestPipeline_Process_EmptyTranscript: ASR that heard nothing yields
// ActionNone WITHOUT consulting the classifier — silence must never cost an
// LLM call on the interactive loop.
func TestPipeline_Process_EmptyTranscript(t *testing.T) {
	tr := &captureTranscriber{reply: "   "} // whitespace-only transcript
	cl := &stubClassifier{}
	p := NewPipeline(tr.transcribe, cl)

	got, err := p.Process(context.Background(), []byte{0x00})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if got.Action != ActionNone {
		t.Errorf("action = %q, want none", got.Action)
	}
	if len(cl.calls) != 0 {
		t.Errorf("classifier called %d time(s) on empty transcript, want 0", len(cl.calls))
	}
}

// TestPipeline_Process_EmptyRecording: a trigger with no accumulated audio
// is a caller bug / silent recording — surfaced as ErrEmptyRecording, not a
// wasted ASR round trip.
func TestPipeline_Process_EmptyRecording(t *testing.T) {
	tr := &captureTranscriber{reply: "好了"}
	p := NewPipeline(tr.transcribe, &stubClassifier{})

	got, err := p.Process(context.Background(), nil)
	if !errors.Is(err, ErrEmptyRecording) {
		t.Fatalf("Process(nil) error = %v, want ErrEmptyRecording", err)
	}
	if got.Action != ActionNone {
		t.Errorf("action = %q, want none", got.Action)
	}
	if len(tr.wavs) != 0 {
		t.Errorf("transcriber called on empty recording, want 0 calls")
	}
}

// TestPipeline_Process_TranscriberError: an ASR failure returns ActionNone
// plus the error — callers show the fallback UI and keep the dictation loop
// alive; the error is informational.
func TestPipeline_Process_TranscriberError(t *testing.T) {
	asrErr := errors.New("asr unavailable")
	tr := &captureTranscriber{err: asrErr}
	cl := &stubClassifier{}
	p := NewPipeline(tr.transcribe, cl)

	got, err := p.Process(context.Background(), []byte{0x00, 0x01})
	if err == nil || !strings.Contains(err.Error(), "asr unavailable") {
		t.Fatalf("Process error = %v, want wrapped asr unavailable", err)
	}
	if got.Action != ActionNone {
		t.Errorf("action = %q, want none", got.Action)
	}
	if len(cl.calls) != 0 {
		t.Errorf("classifier called despite ASR failure, want 0 calls")
	}
}
