// vad_test.go — behavior tests for the energy VAD segmenter (13-04 Task 1).
//
// All fixtures are synthetic 16 kHz s16le mono PCM (the only negotiated
// uplink format, protocol.go): sine bursts for speech, zeros for silence.
// Durations are frame-aligned (multiples of 30 ms) so every expectation is
// exact byte arithmetic rather than an approximation.
package rt

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

const (
	// vadTestFrameMs mirrors DefaultVADConfig().FrameMs.
	vadTestFrameMs = 30
	// vadTestFrameBytes: 30 ms @ 16 kHz s16le mono = 480 samples = 960 bytes.
	vadTestFrameBytes = 960
	// vadSinePeriod samples per cycle → 12 exact cycles per 480-sample frame,
	// so a full frame's RMS is exactly amplitude/√2 (no phase leakage).
	vadSinePeriod = 40
)

// vadSine synthesizes ms milliseconds of a sine wave at the given int16
// amplitude, little-endian s16le mono.
func vadSine(amplitude float64, ms int) []byte {
	samples := ms * requiredSampleRate / 1000
	buf := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		s := math.Sin(2*math.Pi*float64(i)/vadSinePeriod) * amplitude
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(int16(math.Round(s)))) //nolint:gosec // G115: s16le PCM bit pattern, not a secret range check
	}
	return buf
}

// vadSilence synthesizes ms milliseconds of digital silence.
func vadSilence(ms int) []byte {
	return make([]byte, ms*requiredSampleRate/1000*2)
}

// vadFeedAll feeds pcm through v in four-frame chunks and collects every
// closed segment.
func vadFeedAll(v *VAD, pcm []byte) [][]byte {
	var segments [][]byte
	step := vadTestFrameBytes * 4
	for off := 0; off < len(pcm); off += step {
		end := off + step
		if end > len(pcm) {
			end = len(pcm)
		}
		if seg, closed := v.Feed(pcm[off:end]); closed {
			segments = append(segments, seg)
		}
	}
	return segments
}

// vadFeedOdd feeds pcm in deliberately non-frame-aligned chunk sizes,
// exercising the partial-frame remainder path (no sample may be lost).
func vadFeedOdd(v *VAD, pcm []byte) [][]byte {
	var segments [][]byte
	sizes := []int{1, vadTestFrameBytes, 7, 1913, 319, vadTestFrameBytes * 10}
	for off, i := 0, 0; off < len(pcm); i++ {
		n := sizes[i%len(sizes)]
		if off+n > len(pcm) {
			n = len(pcm) - off
		}
		if seg, closed := v.Feed(pcm[off : off+n]); closed {
			segments = append(segments, seg)
		}
		off += n
	}
	return segments
}

// allZero reports whether every byte of b is zero.
func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// TestVADDefaultConfig anchors the seven tuning fields and the 30 s
// max-segment default (Pitfall 6: a 30 s cap keeps ASR DataURLs far below
// the 10 MB limit).
func TestVADDefaultConfig(t *testing.T) {
	cfg := DefaultVADConfig()
	if cfg.StartRMS != 0.02 || cfg.StopRMS != 0.012 {
		t.Errorf("RMS thresholds = (%v, %v), want (0.02, 0.012)", cfg.StartRMS, cfg.StopRMS)
	}
	if cfg.FrameMs != 30 {
		t.Errorf("FrameMs = %d, want 30", cfg.FrameMs)
	}
	if cfg.StartFrames != 3 {
		t.Errorf("StartFrames = %d, want 3", cfg.StartFrames)
	}
	if cfg.HangoverMs != 700 {
		t.Errorf("HangoverMs = %d, want 700", cfg.HangoverMs)
	}
	if cfg.PreRollMs != 300 {
		t.Errorf("PreRollMs = %d, want 300", cfg.PreRollMs)
	}
	if cfg.MaxSegmentMs != 30000 {
		t.Errorf("MaxSegmentMs = %d, want 30000", cfg.MaxSegmentMs)
	}
}

// TestVADSilenceNoSegment: five seconds of pure silence must never open or
// close a segment (ordinary silence is writing time, never a turn).
func TestVADSilenceNoSegment(t *testing.T) {
	v := NewVAD(DefaultVADConfig())
	segments := vadFeedAll(v, vadSilence(5000))
	if len(segments) != 0 {
		t.Fatalf("silence produced %d segments, want 0", len(segments))
	}
	if seg, closed := v.Feed(vadSilence(60)); closed || seg != nil {
		t.Errorf("final Feed = (%v, %v), want (nil, false)", seg, closed)
	}
}

// TestVADSpeechThenSilenceCloses: silence lead-in + one speech burst + long
// silence yields exactly one closed segment that
//   - begins with the pre-roll ring's silence samples (nothing clipped), and
//   - includes the hangover tail (700 ms of trailing silence inside the
//     segment).
//
// Frame math (30 ms frames, defaults): 17 silence frames, then speech frames
// 17..40. Frames 17-19 satisfy StartFrames → segment opens with the 10-frame
// ring (frames 10..19 = 210 ms silence + 90 ms speech). Speech frames 20..40
// append (630 ms). Silence frames 41.. count down the 700 ms hangover; the
// 24th quiet frame (frame 64) closes the segment: 10+21+24 = 55 frames =
// 1650 ms = 52800 bytes. (Plan prose "700 ms speech + 800 ms silence ≈ 1.5 s"
// is realized frame-aligned as 720 ms + 810 ms.)
func TestVADSpeechThenSilenceCloses(t *testing.T) {
	v := NewVAD(DefaultVADConfig())
	input := append(append(vadSilence(510), vadSine(12000, 720)...), vadSilence(810)...)

	segments := vadFeedAll(v, input)
	if len(segments) != 1 {
		t.Fatalf("segments = %d, want exactly 1", len(segments))
	}
	seg := segments[0]
	if got := len(seg); got != 55*vadTestFrameBytes {
		t.Errorf("segment length = %d bytes (%.0f ms), want %d (1650 ms)",
			got, float64(got)/float64(requiredSampleRate)/2*1000, 55*vadTestFrameBytes)
	}
	// Pre-roll: the first 7 frames (210 ms) of the segment are the ring's
	// silence lead-in — assert a safe 200 ms of leading zeros.
	if !allZero(seg[:200*requiredSampleRate/1000*2]) {
		t.Error("segment head is not pre-roll silence; first samples must be zero")
	}
	// And speech really is in there: frames 10..30 (the burst) are non-zero.
	if allZero(seg[10*vadTestFrameBytes : 30*vadTestFrameBytes]) {
		t.Error("segment middle is all zero; speech burst missing from segment")
	}
}

// TestVADHardCutAtMax: with MaxSegmentMs dialed down to 2 s, continuous
// speech hard-cuts at the cap and later audio re-accumulates into a second
// segment.
//
// Math: open at frame 2 (ring 90 ms); segmentMs reaches 2010 on frame 66 →
// segment 1 = frames 0..66 = 67 frames = 2010 ms. Loud frames 67..99
// re-trigger (ring 67..69), accumulate to 990 ms, then the trailing silence
// closes via hangover: segment 2 = 3+30+24 = 57 frames = 1710 ms.
func TestVADHardCutAtMax(t *testing.T) {
	cfg := DefaultVADConfig()
	cfg.MaxSegmentMs = 2000
	v := NewVAD(cfg)

	input := append(vadSine(12000, 3000), vadSilence(810)...)
	segments := vadFeedAll(v, input)
	if len(segments) != 2 {
		t.Fatalf("segments = %d, want 2 (hard cut + re-accumulation)", len(segments))
	}
	if got := len(segments[0]); got != 67*vadTestFrameBytes {
		t.Errorf("segment 1 = %d bytes (%.0f ms), want %d (2010 ms hard cut)",
			got, float64(got)/float64(requiredSampleRate)/2*1000, 67*vadTestFrameBytes)
	}
	if got := len(segments[1]); got != 57*vadTestFrameBytes {
		t.Errorf("segment 2 = %d bytes (%.0f ms), want %d (1710 ms re-accumulated)",
			got, float64(got)/float64(requiredSampleRate)/2*1000, 57*vadTestFrameBytes)
	}
}

// TestVADShortGapMerged: two speech bursts separated by 300 ms of silence
// (below the 700 ms hangover) merge into ONE segment.
//
// Math: ring opens at frame 12 (300 ms); speech 13..23 (+330 ms); gap
// 24..33 (+300 ms, hangover only 300 of 700 ms spent); speech 34..47
// (+420 ms, hangover reset); tail silence closes after 24 quiet frames
// (+720 ms) → 69 frames = 2070 ms.
func TestVADShortGapMerged(t *testing.T) {
	v := NewVAD(DefaultVADConfig())
	input := append(append(append(append(
		vadSilence(300),
		vadSine(12000, 420)...),
		vadSilence(300)...),
		vadSine(12000, 420)...),
		vadSilence(900)...)

	segments := vadFeedAll(v, input)
	if len(segments) != 1 {
		t.Fatalf("segments = %d, want 1 (300 ms gap merged by hangover)", len(segments))
	}
	if got := len(segments[0]); got != 69*vadTestFrameBytes {
		t.Errorf("merged segment = %d bytes (%.0f ms), want %d (2070 ms)",
			got, float64(got)/float64(requiredSampleRate)/2*1000, 69*vadTestFrameBytes)
	}
}

// TestVADRMSFrameCalc: a full frame of a known-amplitude sine has RMS
// amplitude/√2 normalized to [-1,1); tolerance 5 % absorbs int16 rounding.
func TestVADRMSFrameCalc(t *testing.T) {
	for _, amp := range []float64{12000, 3000} {
		frame := vadSine(amp, vadTestFrameMs)
		want := amp / math.Sqrt2 / 32768
		got := FrameRMS(frame)
		if math.Abs(got-want)/want >= 0.05 {
			t.Errorf("FrameRMS(amp %.0f) = %v, want ~%v (±5 %%)", amp, got, want)
		}
	}
	if got := FrameRMS(vadSilence(vadTestFrameMs)); got != 0 {
		t.Errorf("FrameRMS(silence) = %v, want 0", got)
	}
	if got := FrameRMS(nil); got != 0 {
		t.Errorf("FrameRMS(nil) = %v, want 0", got)
	}
}

// TestVADFeedArbitraryChunkSizes: feeding the same stream in odd,
// non-frame-aligned chunks must yield byte-identical segments — the
// no-sample-loss invariant for partial-frame remainders.
func TestVADFeedArbitraryChunkSizes(t *testing.T) {
	input := append(append(vadSilence(510), vadSine(12000, 720)...), vadSilence(810)...)

	aligned := vadFeedAll(NewVAD(DefaultVADConfig()), input)
	odd := vadFeedOdd(NewVAD(DefaultVADConfig()), input)

	if len(aligned) != 1 || len(odd) != 1 {
		t.Fatalf("segments: aligned=%d odd=%d, want 1 and 1", len(aligned), len(odd))
	}
	if !bytes.Equal(aligned[0], odd[0]) {
		t.Errorf("odd-chunk segment differs: aligned len=%d odd len=%d", len(aligned[0]), len(odd[0]))
	}
}

// TestVADReset: after Reset the VAD behaves like a fresh instance — a
// previously-open segment is discarded and detection restarts cleanly
// (interrupt/session-end reuse).
func TestVADReset(t *testing.T) {
	v := NewVAD(DefaultVADConfig())
	// Open a segment with speech and leave it unclosed (no trailing silence).
	_, _ = v.Feed(vadSine(12000, 300))

	v.Reset()

	// After reset: a fresh silence+speech+silence run closes exactly one
	// segment identical to a virgin VAD's output.
	input := append(append(vadSilence(510), vadSine(12000, 720)...), vadSilence(810)...)
	resetSegs := vadFeedAll(v, input)
	freshSegs := vadFeedAll(NewVAD(DefaultVADConfig()), input)

	if len(resetSegs) != 1 || len(freshSegs) != 1 {
		t.Fatalf("segments: reset=%d fresh=%d, want 1 and 1", len(resetSegs), len(freshSegs))
	}
	if !bytes.Equal(resetSegs[0], freshSegs[0]) {
		t.Error("post-Reset segment differs from a fresh VAD's segment")
	}
}
