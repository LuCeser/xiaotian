package e2ecorpus

import (
	"encoding/binary"
	"fmt"
	"time"
)

// WAVInfo is a parsed RIFF/WAVE payload: the real `data` chunk bytes plus
// the format taken from the `fmt ` chunk.
type WAVInfo struct {
	PCM           []byte
	SampleRate    int
	Channels      int
	BitsPerSample int
	Duration      time.Duration
}

// ParseWAV walks a RIFF/WAVE stream chunk by chunk to find the real `data`
// and `fmt ` chunks — it never assumes a fixed 44-byte header. Extra chunks
// (LIST, fact, …) are skipped, and odd-sized chunks consume their RIFF pad
// byte. Anything that is not plain 16-bit PCM errors out: a corrupt or
// foreign file must be rejected, not guessed at (计划第 4 节：WAV 解析查找
// 真实 data 块，不假定固定 44 字节头).
func ParseWAV(b []byte) (WAVInfo, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return WAVInfo{}, fmt.Errorf("wav: not a RIFF/WAVE stream (%d bytes)", len(b))
	}
	var info WAVInfo
	haveFmt, haveData := false, false
	for pos := 12; pos+8 <= len(b); {
		id := string(b[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(b[pos+4 : pos+8])) //nolint:gosec // G115: chunk size from a local buffer; truncation checked below
		body := pos + 8
		if size < 0 || body+size > len(b) {
			return WAVInfo{}, fmt.Errorf("wav: chunk %q truncated (size %d, %d bytes left)", id, size, len(b)-body)
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return WAVInfo{}, fmt.Errorf("wav: fmt chunk too small (%d bytes)", size)
			}
			if format := binary.LittleEndian.Uint16(b[body:]); format != 1 {
				return WAVInfo{}, fmt.Errorf("wav: format %d, want plain PCM (1)", format)
			}
			info.Channels = int(binary.LittleEndian.Uint16(b[body+2:]))   //nolint:gosec // G115: u16 fits int
			info.SampleRate = int(binary.LittleEndian.Uint32(b[body+4:])) //nolint:gosec // G115: sample rates fit int
			info.BitsPerSample = int(binary.LittleEndian.Uint16(b[body+14:]))
			haveFmt = true
		case "data":
			if size%2 != 0 {
				return WAVInfo{}, fmt.Errorf("wav: odd data size %d — 16-bit PCM must be even", size)
			}
			info.PCM = b[body : body+size]
			haveData = true
		}
		pos = body + size
		if size%2 == 1 {
			pos++ // RIFF pads odd-sized chunks back to an even offset
		}
	}
	switch {
	case !haveFmt:
		return WAVInfo{}, fmt.Errorf("wav: no fmt chunk")
	case !haveData:
		return WAVInfo{}, fmt.Errorf("wav: no data chunk")
	case info.BitsPerSample != 16:
		return WAVInfo{}, fmt.Errorf("wav: %d bits per sample, want 16", info.BitsPerSample)
	case info.Channels < 1:
		return WAVInfo{}, fmt.Errorf("wav: %d channels", info.Channels)
	case info.SampleRate < 1:
		return WAVInfo{}, fmt.Errorf("wav: %d Hz sample rate — duration would divide by zero", info.SampleRate)
	case len(info.PCM) == 0:
		return WAVInfo{}, fmt.Errorf("wav: empty data chunk")
	}
	frames := int64(len(info.PCM) / (info.Channels * 2))
	info.Duration = time.Duration(frames * int64(time.Second) / int64(info.SampleRate))
	return info, nil
}

// BuildWAV wraps s16le mono PCM in a canonical WAV byte stream with the
// given sample rate. The inverse of ParseWAV for the files this package
// writes: the 16kHz injection WAVs and test fixtures.
func BuildWAV(pcm []byte, sampleRate int) []byte {
	out := make([]byte, 44+len(pcm))
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+len(pcm))) //nolint:gosec // G115: callers pass in-memory audio
	copy(out[8:], "WAVE")
	copy(out[12:], "fmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)                   // fmt chunk size
	binary.LittleEndian.PutUint16(out[20:], 1)                    // plain PCM
	binary.LittleEndian.PutUint16(out[22:], 1)                    // mono
	binary.LittleEndian.PutUint32(out[24:], uint32(sampleRate))   //nolint:gosec // G115: sample rates fit u32
	binary.LittleEndian.PutUint32(out[28:], uint32(sampleRate)*2) //nolint:gosec // G115: byte rate = rate × 2 bytes
	binary.LittleEndian.PutUint16(out[32:], 2)                    // block align: 2 bytes per frame
	binary.LittleEndian.PutUint16(out[34:], 16)                   // bits per sample
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm))) //nolint:gosec // G115: callers pass in-memory audio
	copy(out[44:], pcm)
	return out
}
