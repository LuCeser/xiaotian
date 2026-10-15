package tts

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// fakeMimoSynth is a one-method fake for the MimoSynthClient seam: canned
// DecodedAudioData or error, plus capture of the text and options so tests
// can assert the bare-word contract and the applied preset.
type fakeMimoSynth struct {
	mu       sync.Mutex
	audio    mimo.DecodedAudioData
	err      error
	calls    int
	lastText string
	lastOpts []mimo.TTSOption
}

func (f *fakeMimoSynth) Synthesize(_ context.Context, text string, opts ...mimo.TTSOption) (mimo.DecodedAudioData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastText = text
	f.lastOpts = opts
	if f.err != nil {
		return mimo.DecodedAudioData{}, f.err
	}
	return f.audio, nil
}

func (f *fakeMimoSynth) snapshot() (int, string, []mimo.TTSOption) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.lastText, f.lastOpts
}

// leSamples packs int16 values as little-endian s16le PCM bytes.
func leSamples(vals []int16) []byte {
	out := make([]byte, len(vals)*2)
	for i, v := range vals {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v)) //nolint:gosec // G115: s16le PCM bit pattern, not a numeric range check
	}
	return out
}

// leUnpack reads the s16le PCM bytes back into int16 values.
func leUnpack(b []byte) []int16 {
	vals := make([]int16, len(b)/2)
	for i := range vals {
		vals[i] = int16(binary.LittleEndian.Uint16(b[i*2:])) //nolint:gosec // G115: s16le PCM sample; the high bit is the sign
	}
	return vals
}

// TestDownsample24kTo16k_LengthMath: 3:2 decimation produces floor(in*2/3)
// samples — 24000 samples (48000 bytes) at 24kHz must yield exactly 16000
// samples (32000 bytes) at 16kHz, the invariant the streaming pipeline's
// 20ms frame math depends on.
func TestDownsample24kTo16k_LengthMath(t *testing.T) {
	cases := []struct {
		name       string
		inSamples  int
		wantSample int
	}{
		{"one second of 24kHz", 24000, 16000},
		{"three samples", 3, 2},
		{"four samples", 4, 2},
		{"two samples", 2, 1},
		{"single sample degenerates to empty", 1, 0},
		{"empty input", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := downsample24kTo16k(leSamples(make([]int16, tc.inSamples)))
			if got := len(out) / 2; got != tc.wantSample {
				t.Errorf("out samples = %d, want %d", got, tc.wantSample)
			}
		})
	}
}

// TestDownsample24kTo16k_RampValues: a linear ramp survives decimation with
// exact expected values — integer source positions (even output index in
// 3:2 terms) copy verbatim; half positions average the two neighbours. For
// input[i] = i the output must be floor(1.5*j) at every output index j
// (truncating integer division in the average accounts for the .5).
func TestDownsample24kTo16k_RampValues(t *testing.T) {
	const n = 600 // divisible by 3 → every output index is exercised
	in := make([]int16, n)
	for i := range in {
		in[i] = int16(i)
	}
	out := leUnpack(downsample24kTo16k(leSamples(in)))
	if len(out) != n*2/3 {
		t.Fatalf("out length = %d, want %d", len(out), n*2/3)
	}
	for j, got := range out {
		want := int16(j * 3 / 2) // floor(1.5*j)
		if got != want {
			t.Fatalf("out[%d] = %d, want %d", j, got, want)
		}
	}
}

// TestDownsample24kTo16k_SineValues: a full-scale 24kHz sine decimates to a
// sine at 16kHz within the interpolation error bound — every output sample
// must stay within one amplitude step of the ideal 16kHz sine. This catches
// sign/endianness bugs a ramp cannot (negative lobes, symmetry).
func TestDownsample24kTo16k_SineValues(t *testing.T) {
	const n = 480 // 20ms of 24kHz sine — well within one period for the phase math below
	const amp = 12000.0
	const freq = 220.0 // Hz, safely below the 8kHz Nyquist of both rates
	in := make([]int16, n)
	for i := range in {
		in[i] = int16(amp * math.Sin(2*math.Pi*freq*float64(i)/24000.0))
	}
	out := leUnpack(downsample24kTo16k(leSamples(in)))
	if len(out) == 0 {
		t.Fatal("empty output")
	}
	for j, got := range out {
		want := amp * math.Sin(2*math.Pi*freq*float64(j)/16000.0)
		if diff := math.Abs(float64(got) - want); diff > amp*math.Pi*freq/16000.0+1 {
			// Interpolation error bound for band-limited-ish signals scales
			// with the derivative (amplitude * 2πf/f_s); +1 for quantization.
			t.Fatalf("out[%d] = %d, want ≈ %.1f (diff %.1f)", j, got, want, diff)
		}
	}
}

// TestMimoWordSynthesizer_NilClient: the constructor validates like
// NewGenerator — nil client is an ErrTTSGenerationFailed wrap.
func TestMimoWordSynthesizer_NilClient(t *testing.T) {
	if _, err := NewMimoWordSynthesizer(nil, nil); !errors.Is(err, ErrTTSGenerationFailed) {
		t.Errorf("err = %v, want ErrTTSGenerationFailed wrap", err)
	}
}

// TestMimoWordSynthesizer_EmptyWord: the empty word is rejected before any
// API call.
func TestMimoWordSynthesizer_EmptyWord(t *testing.T) {
	fake := &fakeMimoSynth{}
	s, err := NewMimoWordSynthesizer(fake, nil)
	if err != nil {
		t.Fatalf("constructor failed: %v", err)
	}
	if _, err := s.SynthesizeWord(context.Background(), ""); !errors.Is(err, ErrTTSGenerationFailed) {
		t.Errorf("err = %v, want ErrTTSGenerationFailed wrap", err)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Errorf("Synthesize calls = %d, want 0", calls)
	}
}

// TestMimoWordSynthesizer_Passthrough16k: 16kHz audio passes through
// byte-for-byte (the pipeline-native branch).
func TestMimoWordSynthesizer_Passthrough16k(t *testing.T) {
	samples := leSamples([]int16{100, -200, 300, -400, 500})
	fake := &fakeMimoSynth{audio: mimo.DecodedAudioData{
		Samples: samples, Format: "pcm16", SampleRate: 16000, Duration: 5 * 1000 / 16000,
	}}
	s, err := NewMimoWordSynthesizer(fake, nil)
	if err != nil {
		t.Fatalf("constructor failed: %v", err)
	}
	pcm, err := s.SynthesizeWord(context.Background(), "apple")
	if err != nil {
		t.Fatalf("SynthesizeWord failed: %v", err)
	}
	if string(pcm.Samples) != string(samples) {
		t.Error("16kHz audio was not passed through byte-for-byte")
	}
	if pcm.FrameSize != 640 {
		t.Errorf("FrameSize = %d, want 640 (matches MP3ToPCM output)", pcm.FrameSize)
	}
}

// TestMimoWordSynthesizer_Downsample24k: the documented pcm16 reality is
// 24kHz — the synthesizer must decimate to 16kHz with the 3:2 length math.
func TestMimoWordSynthesizer_Downsample24k(t *testing.T) {
	in := make([]int16, 3000) // 125ms @ 24kHz → 2000 samples @ 16kHz
	for i := range in {
		in[i] = int16(i)
	}
	fake := &fakeMimoSynth{audio: mimo.DecodedAudioData{
		Samples: leSamples(in), Format: "pcm16", SampleRate: 24000,
	}}
	s, err := NewMimoWordSynthesizer(fake, nil)
	if err != nil {
		t.Fatalf("constructor failed: %v", err)
	}
	pcm, err := s.SynthesizeWord(context.Background(), "banana")
	if err != nil {
		t.Fatalf("SynthesizeWord failed: %v", err)
	}
	if got := len(pcm.Samples) / 2; got != 2000 {
		t.Errorf("out samples = %d, want 2000", got)
	}
	if pcm.FrameSize != 640 {
		t.Errorf("FrameSize = %d, want 640", pcm.FrameSize)
	}
	// Ramp values survive exactly (see the ramp test for the floor(1.5j) rule).
	got := leUnpack(pcm.Samples)
	if got[1] != 1 || got[2] != 3 || got[3] != 4 { // floor(1.5), floor(3), floor(4.5)
		t.Errorf("ramp values wrong: got %v, want [0 1 3 4 ...] prefix", got[:4])
	}
}

// TestMimoWordSynthesizer_UnsupportedRate: anything but 16k/24k surfaces as
// ErrUnsupportedSampleRate (detectable via errors.Is).
func TestMimoWordSynthesizer_UnsupportedRate(t *testing.T) {
	fake := &fakeMimoSynth{audio: mimo.DecodedAudioData{
		Samples: []byte{0, 0}, Format: "wav", SampleRate: 44100,
	}}
	s, err := NewMimoWordSynthesizer(fake, nil)
	if err != nil {
		t.Fatalf("constructor failed: %v", err)
	}
	if _, err := s.SynthesizeWord(context.Background(), "cherry"); !errors.Is(err, ErrUnsupportedSampleRate) {
		t.Errorf("err = %v, want ErrUnsupportedSampleRate", err)
	}
}

// TestMimoWordSynthesizer_EmptyAudio: a success response with zero bytes is
// a failure, not an empty cache file that would silently pass the
// "all words have audio" count invariant.
func TestMimoWordSynthesizer_EmptyAudio(t *testing.T) {
	fake := &fakeMimoSynth{audio: mimo.DecodedAudioData{SampleRate: 24000}}
	s, err := NewMimoWordSynthesizer(fake, nil)
	if err != nil {
		t.Fatalf("constructor failed: %v", err)
	}
	if _, err := s.SynthesizeWord(context.Background(), "word"); !errors.Is(err, ErrTTSGenerationFailed) {
		t.Errorf("err = %v, want ErrTTSGenerationFailed wrap", err)
	}
}

// TestMimoWordSynthesizer_BareWordAndPreset: the DICT-03 single-pronunciation
// contract — the synthesized text is the bare word (no "word. word." pairing,
// the frontend schedules the double play) and the options carry
// PresetDictationTeacher (VoiceMoLi, pcm16, StyleTeacher).
func TestMimoWordSynthesizer_BareWordAndPreset(t *testing.T) {
	fake := &fakeMimoSynth{audio: mimo.DecodedAudioData{
		Samples: leSamples([]int16{1, 2, 3}), Format: "pcm16", SampleRate: 24000,
	}}
	s, err := NewMimoWordSynthesizer(fake, nil)
	if err != nil {
		t.Fatalf("constructor failed: %v", err)
	}
	if _, err := s.SynthesizeWord(context.Background(), "苹果"); err != nil {
		t.Fatalf("SynthesizeWord failed: %v", err)
	}
	calls, text, opts := fake.snapshot()
	if calls != 1 {
		t.Errorf("Synthesize calls = %d, want 1 (one bare-word call)", calls)
	}
	if text != "苹果" {
		t.Errorf("synthesized text = %q, want the bare word %q", text, "苹果")
	}

	// Replay the captured options onto a default-shaped request and assert
	// the PresetDictationTeacher fields landed.
	req := mimo.TTSRequest{
		Model: "mimo-v2.5-tts",
		Messages: []mimo.Message{
			{Role: "assistant", Content: text},
		},
		Audio: mimo.AudioConfig{Format: "pcm16", Voice: "冰糖", SampleRate: 16000},
	}
	for _, opt := range opts {
		opt(&req)
	}
	if req.Audio.Voice != mimo.VoiceMoLi {
		t.Errorf("voice = %q, want %q (PresetDictationTeacher)", req.Audio.Voice, mimo.VoiceMoLi)
	}
	if req.Audio.Format != mimo.FormatPCM16 {
		t.Errorf("format = %q, want %q", req.Audio.Format, mimo.FormatPCM16)
	}
	if req.Audio.SampleRate != mimo.SampleRate16kHz {
		t.Errorf("sample rate = %d, want %d", req.Audio.SampleRate, mimo.SampleRate16kHz)
	}
	// StyleTeacher prepends a user style message ahead of the word itself.
	if len(req.Messages) != 2 || req.Messages[0].Role != "user" || req.Messages[1].Content != "苹果" {
		t.Errorf("messages = %+v, want [style user message, assistant word]", req.Messages)
	}
}

// TestMimoWordSynthesizer_APIErrorWraps: client errors propagate wrapped, so
// the generator's fallback sees a non-nil error chain.
func TestMimoWordSynthesizer_APIErrorWraps(t *testing.T) {
	sentinel := mimo.ErrNetworkError
	fake := &fakeMimoSynth{err: sentinel}
	s, err := NewMimoWordSynthesizer(fake, nil)
	if err != nil {
		t.Fatalf("constructor failed: %v", err)
	}
	_, err = s.SynthesizeWord(context.Background(), "word")
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want errors.Is(err, mimo.ErrNetworkError)", err)
	}
}
