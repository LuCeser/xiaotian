package voice

import (
	"encoding/binary"
	"math"
)

// wavHeaderSize is the canonical 44-byte RIFF/WAVE header length written by
// WrapWAV.
const wavHeaderSize = 44

// fitUint16 converts n to uint16. Values outside the uint16 range keep the
// low 16 bits, matching a bare conversion.
func fitUint16(n int) uint16 {
	if n < 0 || n > math.MaxUint16 {
		return uint16(n) //nolint:gosec // G115: value does not fit uint16; keep the low 16 bits
	}
	return uint16(n)
}

// fitUint32 converts n to uint32. Values outside the uint32 range keep the
// low 32 bits, matching a bare conversion.
func fitUint32(n int) uint32 {
	if n < 0 {
		return uint32(n) //nolint:gosec // G115: negative value does not fit uint32; keep the low 32 bits
	}
	if uint64(n) > math.MaxUint32 {
		return uint32(n) //nolint:gosec // G115: value does not fit uint32; keep the low 32 bits
	}
	return uint32(n)
}

// WrapWAV prepends a 44-byte RIFF/WAVE header to raw signed 16-bit
// little-endian PCM samples, producing a self-describing WAV the Mimo ASR
// accepts (its audio input is a data:audio/wav;base64 URL — a bare PCM
// payload with a wav MIME type is not a container).
//
// channels is the channel count (1 for the dictation mic path); sampleRate
// is the capture rate (16000 for the protocol's PCM frames).
func WrapWAV(pcm []byte, sampleRate, channels int) []byte {
	byteRate := sampleRate * channels * 2 // 16-bit samples
	blockAlign := channels * 2

	out := make([]byte, wavHeaderSize+len(pcm))

	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], fitUint32(36+len(pcm)))
	copy(out[8:12], "WAVE")

	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:20], 16) // PCM fmt chunk size
	binary.LittleEndian.PutUint16(out[20:22], 1)  // audio format: PCM
	binary.LittleEndian.PutUint16(out[22:24], fitUint16(channels))
	binary.LittleEndian.PutUint32(out[24:28], fitUint32(sampleRate))
	binary.LittleEndian.PutUint32(out[28:32], fitUint32(byteRate))
	binary.LittleEndian.PutUint16(out[32:34], fitUint16(blockAlign))
	binary.LittleEndian.PutUint16(out[34:36], 16) // bits per sample

	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:44], fitUint32(len(pcm)))
	copy(out[wavHeaderSize:], pcm)

	return out
}
