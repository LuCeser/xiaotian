// Package ws provides WebSocket protocol implementation for the home tutor agent.
// This file implements frame validation, encoding, and decoding with CRC-16 integrity checks.
package ws

import (
	"encoding/binary"

	"github.com/xiaotian/home-tutor-agent/pkg/crc16"
)

// ComputeCRC16 computes the CRC-16-CCITT checksum of data (RESEARCH.md Don't Hand-Roll).
// Uses the local pkg/crc16 implementation (stdlib hash/crc16 not available in Go 1.26).
func ComputeCRC16(data []byte) uint16 {
	return crc16.Checksum(data)
}

// ValidateFrame checks if a frame has a valid CRC-16 checksum (RESEARCH.md lines 456-463).
// Returns false for frames shorter than HeaderSize or with incorrect CRC.
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
// Automatically sets h.PayloadLen and computes CRC-16.
func EncodeFrame(h *FrameHeader, payload []byte) []byte {
	// Set PayloadLen to actual payload length (D-09 auto-fill)
	h.PayloadLen = uint16(len(payload))

	// Encode header (this computes and sets CRC)
	header := EncodeHeader(h)

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
