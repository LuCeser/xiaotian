// Package ws provides WebSocket protocol implementation for the home tutor agent.
// This file implements frame validation, encoding, and decoding with CRC-16 integrity checks.
package ws

import (
	"encoding/binary"
	"math"

	"github.com/xiaotian/home-tutor-agent/pkg/crc16"
)

// payloadLenField is the uint16 payload length stored in a frame header.
// Lengths above 65535 keep the low 16 bits, matching a bare conversion.
func payloadLenField(n int) uint16 {
	if n < 0 || n > math.MaxUint16 {
		return uint16(n) //nolint:gosec // G115: protocol length is uint16; keep the low 16 bits
	}
	return uint16(n)
}

// ComputeCRC16 computes the CRC-16-CCITT checksum of data (RESEARCH.md Don't Hand-Roll).
// Uses the local pkg/crc16 implementation (stdlib hash/crc16 not available in Go 1.26).
func ComputeCRC16(data []byte) uint16 {
	return crc16.Checksum(data)
}

// ValidateFrame checks if a frame has a valid CRC-16 checksum (RESEARCH.md lines 456-463).
// Returns false for frames shorter than HeaderSize or with incorrect CRC.
//
// WR-01 limitation: the CRC covers only the 16-byte header (bytes 0-13), NOT the payload.
// This is by design (PATTERNS.md Pattern 2) to keep per-frame CPU bounded on the 1C/1G VPS,
// but it means single-bit payload corruption — e.g. a flipped PCM sample in a 640-byte audio
// frame — passes validation silently. Audio integrity is therefore the application layer's
// responsibility. If end-to-end payload integrity is ever required, extend the CRC to cover
// the payload or add a payload MAC at a higher layer.
func ValidateFrame(data []byte) bool {
	if len(data) < HeaderSize {
		return false
	}

	// Extract expected CRC from bytes 14-15 (big-endian)
	expectedCRC := binary.BigEndian.Uint16(data[14:16])

	// Compute actual CRC over bytes 0-13 (all fields except CRC itself)
	actualCRC := ComputeCRC16(data[0:14])

	// Compare CRC values
	return expectedCRC == actualCRC
}

// EncodeFrame encodes a complete frame with header and payload (PATTERNS.md lines 180-186).
// Automatically sets PayloadLen and computes CRC-16.
//
// WR-04: the caller's FrameHeader is NOT mutated. PayloadLen and CRC16 are computed into
// a local copy, so encoding the same header twice with different payloads produces correct,
// independent frames and the caller's stored header keeps its original values.
func EncodeFrame(h *FrameHeader, payload []byte) []byte {
	// Work on a local copy so the caller's header is not mutated (WR-04).
	local := *h
	local.PayloadLen = payloadLenField(len(payload)) // D-09 auto-fill

	// Encode header (computes and sets CRC on the local copy only)
	header := EncodeHeader(&local)

	// Allocate buffer for header + payload
	buf := make([]byte, HeaderSize+len(payload))

	// Copy header and payload
	copy(buf[:HeaderSize], header)
	copy(buf[HeaderSize:], payload)

	return buf
}

// DecodeFrame decodes a complete frame, returning header and payload.
// Returns ErrInvalidFrame for short frames.
// Returns ErrCRCChecksumFailed if CRC validation fails (T-02-01 anti-tampering).
// Returns ErrInvalidFrame for payload length mismatches.
func DecodeFrame(raw []byte) (*FrameHeader, []byte, error) {
	// Check minimum length first
	if len(raw) < HeaderSize {
		return nil, nil, ErrInvalidFrame
	}

	// Validate CRC (T-02-01 anti-tampering)
	if !ValidateFrame(raw) {
		return nil, nil, ErrCRCChecksumFailed
	}

	// Decode header
	h, err := DecodeHeader(raw)
	if err != nil {
		return nil, nil, err
	}

	// WR-07: reject frames whose protocol version we don't speak, at the codec boundary.
	// Without this a frame carrying version 0xFF plus a known MsgType is silently accepted
	// and dispatched, making future wire-format changes indistinguishable from legacy frames.
	if h.Version != ProtocolVersion {
		return nil, nil, ErrUnsupportedVersion
	}

	// Extract payload (starts after 16-byte header)
	payload := raw[HeaderSize:]

	// For audio frames, enforce fixed 640-byte size (D-09)
	if h.MsgType == MsgTypeAudio && len(payload) != AudioFrameSize {
		return nil, nil, ErrInvalidFrame
	}

	// Verify declared payload length matches actual payload length
	if int(h.PayloadLen) != len(payload) {
		return nil, nil, ErrInvalidFrame
	}

	return h, payload, nil
}
