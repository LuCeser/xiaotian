package e2ecorpus

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// riffHeader is the 12-byte RIFF/WAVE prefix; the size field is patched by
// buildTestWAV.
var riffHeader = []byte("RIFF\x00\x00\x00\x00WAVE")

// appendChunk appends a RIFF chunk (id + payload) to b, adding the pad byte
// odd-sized payloads require.
func appendChunk(b []byte, id string, payload []byte) []byte {
	var head [8]byte
	copy(head[:4], id)
	binary.LittleEndian.PutUint32(head[4:], uint32(len(payload))) //nolint:gosec // G115: test payload size
	b = append(append(b, head[:]...), payload...)
	if len(payload)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

// fmtChunk builds a standard 16-byte PCM fmt chunk.
func fmtChunk(sampleRate, channels, bits int) []byte {
	c := make([]byte, 16)
	binary.LittleEndian.PutUint16(c[0:], 1)
	binary.LittleEndian.PutUint16(c[2:], uint16(channels))   //nolint:gosec // G115: test value
	binary.LittleEndian.PutUint32(c[4:], uint32(sampleRate)) //nolint:gosec // G115: test sample rate fits u32
	binary.LittleEndian.PutUint16(c[14:], uint16(bits))      //nolint:gosec // G115: test value
	return c
}

// buildTestWAV assembles a WAV stream with arbitrary extra chunks between
// fmt and data, so tests can prove parsing does not assume the standard
// 44-byte header.
func buildTestWAV(sampleRate, channels int, pcm []byte, extra ...string) []byte {
	b := append([]byte(nil), riffHeader...)
	b = appendChunk(b, "fmt ", fmtChunk(sampleRate, channels, 16))
	for _, id := range extra {
		b = appendChunk(b, id, []byte("whatever payload"))
	}
	b = appendChunk(b, "data", pcm)
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8)) //nolint:gosec // G115: test buffer size
	return b
}

func TestParseWAVRoundTrip(t *testing.T) {
	pcm := make([]byte, 4800) // 2400 samples = 100ms at 24kHz mono
	for i := range pcm {
		pcm[i] = byte(i)
	}
	info, err := ParseWAV(BuildWAV(pcm, 24000))
	if err != nil {
		t.Fatalf("ParseWAV(BuildWAV): %v", err)
	}
	if len(info.PCM) != len(pcm) || info.SampleRate != 24000 || info.Channels != 1 || info.BitsPerSample != 16 {
		t.Fatalf("parsed %+v does not match what was built", info)
	}
	if want := 100 * time.Millisecond; info.Duration != want {
		t.Errorf("duration = %v, want %v", info.Duration, want)
	}
}

// TestParseWAVNonStandardItem: extra chunks before data (one of them with an
// odd payload and pad byte) push the data chunk well past byte 44 — the
// parser must still find the real payload (非标准 RIFF 头).
func TestParseWAVNonStandardItem(t *testing.T) {
	pcm := []byte{1, 2, 3, 4, 5, 6} // 3 frames
	info, err := ParseWAV(buildTestWAV(24000, 1, pcm, "LIST", "odd"))
	if err != nil {
		t.Fatalf("ParseWAV with LIST + odd chunks: %v", err)
	}
	if string(info.PCM) != string(pcm) {
		t.Errorf("PCM = %v, want the 6 payload bytes (data does not start at byte 44 here)", info.PCM)
	}
	if info.SampleRate != 24000 {
		t.Errorf("sample rate = %d, want 24000", info.SampleRate)
	}
}

// truncatedDataWAV declares a 100-byte data chunk but carries only 2.
func truncatedDataWAV() []byte {
	b := appendChunk(append([]byte(nil), riffHeader...), "fmt ", fmtChunk(24000, 1, 16))
	var head [8]byte
	copy(head[:4], "data")
	binary.LittleEndian.PutUint32(head[4:], 100) //nolint:gosec // G115: deliberately oversized
	return append(append(b, head[:]...), 1, 0)
}

func TestParseWAVRejects(t *testing.T) {
	cases := []struct {
		name string
		want string
		wav  []byte
	}{
		{"not RIFF", "not a RIFF", []byte(strings.Repeat("OGG-ish junk", 5))},
		{"too short", "not a RIFF", riffHeader[:8]},
		{"no fmt chunk", "no fmt", appendChunk(append([]byte(nil), riffHeader...), "data", []byte{1, 0, 2, 0})},
		{"no data chunk", "no data", appendChunk(append([]byte(nil), riffHeader...), "fmt ", fmtChunk(24000, 1, 16))},
		{"truncated data", "truncated", truncatedDataWAV()},
		{"odd data size", "odd data", appendChunk(appendChunk(append([]byte(nil), riffHeader...),
			"fmt ", fmtChunk(24000, 1, 16)), "data", []byte{1, 0, 3})},
		{"eight-bit samples", "16", appendChunk(appendChunk(append([]byte(nil), riffHeader...),
			"fmt ", fmtChunk(24000, 1, 8)), "data", []byte{1, 2, 3, 4})},
		{"empty data", "empty", buildTestWAV(24000, 1, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWAV(tc.wav)
			if err == nil {
				t.Fatal("ParseWAV succeeded, want rejection")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
