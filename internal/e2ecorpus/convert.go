package e2ecorpus

import (
	"encoding/binary"
	"fmt"
)

// ResamplePCM16 linearly resamples 16-bit LE mono PCM between sample rates.
// This is the 24kHz→16kHz leg of the /rt injection path (计划第 4 节：直接
// 注入前转换为 16 kHz、单声道、s16le). Linear interpolation keeps the tool
// dependency-free; the algorithm version rides in the cache key via
// ConvertVersion, so any change here invalidates converted inputs.
func ResamplePCM16(in []byte, from, to int) ([]byte, error) {
	if from <= 0 || to <= 0 {
		return nil, fmt.Errorf("resample: invalid rates %d→%d", from, to)
	}
	if len(in)%2 != 0 {
		return nil, fmt.Errorf("resample: odd input byte count %d", len(in))
	}
	if from == to {
		return append([]byte(nil), in...), nil
	}
	ratio := float64(from) / float64(to)
	n := len(in) / 2
	outN := int(float64(n) / ratio)
	out := make([]byte, outN*2)
	for j := 0; j < outN; j++ {
		src := float64(j) * ratio
		i0 := int(src)
		frac := src - float64(i0)
		s0 := int16(binary.LittleEndian.Uint16(in[i0*2:])) //nolint:gosec // G115: s16le sample; the high bit is the sign
		var s1 int16
		if (i0+1)*2+2 <= len(in) {
			s1 = int16(binary.LittleEndian.Uint16(in[(i0+1)*2:])) //nolint:gosec // G115: s16le sample; the high bit is the sign
		} else {
			s1 = s0 // last sample holds until the end
		}
		v := int16(float64(s0) + (float64(s1)-float64(s0))*frac)
		binary.LittleEndian.PutUint16(out[j*2:], uint16(v)) //nolint:gosec // G115: s16le bit pattern, not a range check
	}
	return out, nil
}
