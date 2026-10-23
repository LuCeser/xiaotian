package e2ecorpus

import (
	"encoding/binary"
	"math"
	"testing"
)

// sampleAt reads sample i from an s16le mono buffer.
func sampleAt(t *testing.T, pcm []byte, i int) int16 {
	t.Helper()
	return int16(binary.LittleEndian.Uint16(pcm[i*2:])) //nolint:gosec // G115: s16le sample; the high bit is the sign
}

// putSample writes sample i of an s16le mono buffer.
func putSample(pcm []byte, i int, v int16) {
	binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v)) //nolint:gosec // G115: s16le bit pattern
}

// TestResamplePCM16DownsampleRatio: 24kHz→16kHz must produce exactly 2/3 as
// many samples, and the duration they encode must be unchanged.
func TestResamplePCM16DownsampleRatio(t *testing.T) {
	const inSamples = 2400 // 100ms at 24kHz
	in := make([]byte, inSamples*2)
	for i := 0; i < inSamples; i++ {
		putSample(in, i, int16(math.Sin(float64(i)/8)*8000))
	}
	out, err := ResamplePCM16(in, 24000, 16000)
	if err != nil {
		t.Fatalf("ResamplePCM16: %v", err)
	}
	const wantSamples = inSamples * 16000 / 24000
	if got := len(out) / 2; got != wantSamples {
		t.Fatalf("output has %d samples, want %d (1600 = 100ms at 16kHz)", got, wantSamples)
	}
}

// TestResamplePCM16PreservesSilenceAndLevel: constant input (including
// silence) must stay constant — a broken interpolation would drift.
func TestResamplePCM16PreservesSilenceAndLevel(t *testing.T) {
	for _, level := range []int16{0, 1000, -5000} {
		in := make([]byte, 480*2) // 20ms at 24kHz
		for i := 0; i < len(in)/2; i++ {
			putSample(in, i, level)
		}
		out, err := ResamplePCM16(in, 24000, 16000)
		if err != nil {
			t.Fatalf("ResamplePCM16: %v", err)
		}
		for i := 0; i < len(out)/2; i++ {
			if got := sampleAt(t, out, i); got != level {
				t.Fatalf("constant input %d became %d at output sample %d", level, got, i)
			}
		}
	}
}

// TestResamplePCM16RampStaysMonotonic: a rising ramp must not get scrambled.
func TestResamplePCM16RampStaysMonotonic(t *testing.T) {
	const n = 240
	in := make([]byte, n*2)
	for i := 0; i < n; i++ {
		putSample(in, i, int16(i*40))
	}
	out, err := ResamplePCM16(in, 24000, 16000)
	if err != nil {
		t.Fatalf("ResamplePCM16: %v", err)
	}
	prev := int16(math.MinInt16)
	for i := 0; i < len(out)/2; i++ {
		if v := sampleAt(t, out, i); v < prev {
			t.Fatalf("ramp broke at output sample %d: %d after %d", i, v, prev)
		} else {
			prev = v
		}
	}
}

func TestResamplePCM16SameRateAndErrors(t *testing.T) {
	in := []byte{0x01, 0x02, 0x03, 0x04}
	same, err := ResamplePCM16(in, 16000, 16000)
	if err != nil {
		t.Fatalf("same-rate resample: %v", err)
	}
	if string(same) != string(in) {
		t.Errorf("same-rate output %v != input %v", same, in)
	}
	if _, err := ResamplePCM16(in, 0, 16000); err == nil {
		t.Error("zero source rate accepted, want error")
	}
	if _, err := ResamplePCM16(in, 24000, -1); err == nil {
		t.Error("negative target rate accepted, want error")
	}
	if _, err := ResamplePCM16(in[:3], 24000, 16000); err == nil {
		t.Error("odd byte count accepted, want error")
	}
}
