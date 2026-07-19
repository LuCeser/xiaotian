package ws

import (
	"encoding/binary"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/crc16"
)

// TestEncodeHeaderRoundTrip tests that any FrameHeader can be encoded and decoded
// with all fields preserved.
func TestEncodeHeaderRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		header FrameHeader
	}{
		{
			name: "audio frame",
			header: FrameHeader{
				Version:    0x01,
				MsgType:    MsgTypeAudio,
				Sequence:   12345,
				Timestamp:  1678901234,
				PayloadLen: 640,
				Reserved:   0,
			},
		},
		{
			name: "control frame",
			header: FrameHeader{
				Version:    0x01,
				MsgType:    MsgTypeControl,
				Sequence:   67890,
				Timestamp:  1678905678,
				PayloadLen: 32,
				Reserved:   123,
			},
		},
		{
			name: "all fields max values",
			header: FrameHeader{
				Version:    0xFF,
				MsgType:    0xFF,
				Sequence:   0xFFFFFFFF,
				Timestamp:  0xFFFFFFFF,
				PayloadLen: 0xFFFF,
				Reserved:   0xFFFF,
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Encode the header
			encoded := EncodeHeader(&c.header)

			// Decode it back
			decoded, err := DecodeHeader(encoded)
			if err != nil {
				t.Fatalf("DecodeHeader failed: %v", err)
			}

			// Verify all fields match
			if decoded.Version != c.header.Version {
				t.Errorf("Version mismatch: got %d, want %d", decoded.Version, c.header.Version)
			}
			if decoded.MsgType != c.header.MsgType {
				t.Errorf("MsgType mismatch: got %d, want %d", decoded.MsgType, c.header.MsgType)
			}
			if decoded.Sequence != c.header.Sequence {
				t.Errorf("Sequence mismatch: got %d, want %d", decoded.Sequence, c.header.Sequence)
			}
			if decoded.Timestamp != c.header.Timestamp {
				t.Errorf("Timestamp mismatch: got %d, want %d", decoded.Timestamp, c.header.Timestamp)
			}
			if decoded.PayloadLen != c.header.PayloadLen {
				t.Errorf("PayloadLen mismatch: got %d, want %d", decoded.PayloadLen, c.header.PayloadLen)
			}
			if decoded.Reserved != c.header.Reserved {
				t.Errorf("Reserved mismatch: got %d, want %d", decoded.Reserved, c.header.Reserved)
			}
			// CRC16 will be computed by EncodeHeader, so we just verify it's non-zero
			if decoded.CRC16 == 0 {
				t.Errorf("CRC16 should not be zero")
			}
		})
	}
}

// TestEncodeHeaderByteOrder verifies that multi-byte fields use big-endian byte order.
func TestEncodeHeaderByteOrder(t *testing.T) {
	header := FrameHeader{
		Version:    0x01,
		MsgType:    MsgTypeAudio,
		Sequence:   0x12345678, // Test pattern
		Timestamp:  0x87654321, // Test pattern
		PayloadLen: 0x1234,     // Test pattern
		Reserved:   0x5678,     // Test pattern
	}

	encoded := EncodeHeader(&header)

	// Verify Sequence (bytes 2-6) is big-endian
	sequence := binary.BigEndian.Uint32(encoded[2:6])
	if sequence != 0x12345678 {
		t.Errorf("Sequence byte order wrong: got 0x%08x, want 0x12345678", sequence)
	}

	// Verify Timestamp (bytes 6-10) is big-endian
	timestamp := binary.BigEndian.Uint32(encoded[6:10])
	if timestamp != 0x87654321 {
		t.Errorf("Timestamp byte order wrong: got 0x%08x, want 0x87654321", timestamp)
	}

	// Verify PayloadLen (bytes 10-12) is big-endian
	payloadLen := binary.BigEndian.Uint16(encoded[10:12])
	if payloadLen != 0x1234 {
		t.Errorf("PayloadLen byte order wrong: got 0x%04x, want 0x1234", payloadLen)
	}

	// Verify Reserved (bytes 12-14) is big-endian
	reserved := binary.BigEndian.Uint16(encoded[12:14])
	if reserved != 0x5678 {
		t.Errorf("Reserved byte order wrong: got 0x%04x, want 0x5678", reserved)
	}
}

// TestEncodeHeaderSize verifies that EncodeHeader returns exactly 16 bytes.
func TestEncodeHeaderSize(t *testing.T) {
	header := FrameHeader{
		Version:  0x01,
		MsgType:  MsgTypeAudio,
		Sequence: 1,
	}

	encoded := EncodeHeader(&header)
	if len(encoded) != HeaderSize {
		t.Errorf("EncodeHeader returned %d bytes, want %d", len(encoded), HeaderSize)
	}
}

// TestEncodeHeaderCRCComputed verifies that CRC-16 is computed correctly.
func TestEncodeHeaderCRCComputed(t *testing.T) {
	header := FrameHeader{
		Version:    0x01,
		MsgType:    MsgTypeAudio,
		Sequence:   12345,
		Timestamp:  1678901234,
		PayloadLen: 640,
		Reserved:   0,
	}

	encoded := EncodeHeader(&header)

	// Extract CRC from encoded header (bytes 14-16)
	encodedCRC := binary.BigEndian.Uint16(encoded[14:16])

	// Compute expected CRC from bytes 0-14
	expectedCRC := crc16.Checksum(encoded[0:14])

	if encodedCRC != expectedCRC {
		t.Errorf("CRC mismatch: got 0x%04x, want 0x%04x", encodedCRC, expectedCRC)
	}

	// Also verify header.CRC16 field was updated
	if header.CRC16 != expectedCRC {
		t.Errorf("Header CRC16 field not updated: got 0x%04x, want 0x%04x", header.CRC16, expectedCRC)
	}
}

// TestMessageTypeValues verifies all MessageType constants have correct values.
func TestMessageTypeValues(t *testing.T) {
	tests := []struct {
		name     string
		msgType  MessageType
		expected uint8
	}{
		{"MsgTypeAudio", MsgTypeAudio, 0x01},
		{"MsgTypeControl", MsgTypeControl, 0x02},
		{"MsgTypeError", MsgTypeError, 0x03},
		{"MsgTypeHeartbeat", MsgTypeHeartbeat, 0x04},
		{"MsgTypeSession", MsgTypeSession, 0x05},
		{"MsgTypeStatus", MsgTypeStatus, 0x06},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if uint8(tt.msgType) != tt.expected {
				t.Errorf("%s = %d, want %d", tt.name, tt.msgType, tt.expected)
			}
		})
	}
}

// TestMessageTypeString verifies the String() method returns human-readable names.
func TestMessageTypeString(t *testing.T) {
	tests := []struct {
		msgType  MessageType
		expected string
	}{
		{MsgTypeAudio, "audio"},
		{MsgTypeControl, "control"},
		{MsgTypeError, "error"},
		{MsgTypeHeartbeat, "heartbeat"},
		{MsgTypeSession, "session"},
		{MsgTypeStatus, "status"},
		{0xFF, "unknown(0xff)"}, // Unknown type
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := tt.msgType.String()
			if result != tt.expected {
				t.Errorf("String() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestProtocolVersionConstant verifies the protocol version is 0x01.
func TestProtocolVersionConstant(t *testing.T) {
	if ProtocolVersion != 0x01 {
		t.Errorf("ProtocolVersion = %d, want 1", ProtocolVersion)
	}
}

// TestDecodeHeaderTooShort verifies that DecodeHeader returns error for short input.
func TestDecodeHeaderTooShort(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"one byte", []byte{0x01}},
		{"partial header", make([]byte, HeaderSize-1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeHeader(tt.data)
			if err != ErrInvalidFrame {
				t.Errorf("DecodeHeader() error = %v, want %v", err, ErrInvalidFrame)
			}
		})
	}
}

// TestFrameHeaderFields verifies that FrameHeader has all required fields.
func TestFrameHeaderFields(t *testing.T) {
	header := FrameHeader{}

	// This test is mostly compile-time verification, but we check field existence
	// by assigning to each field
	header.Version = 0x01
	header.MsgType = MsgTypeAudio
	header.Sequence = 12345
	header.Timestamp = 1678901234
	header.PayloadLen = 640
	header.Reserved = 0
	header.CRC16 = 0x1234

	// Verify no panics (struct is valid)
	if header.Version != 0x01 {
		t.Error("Version field assignment failed")
	}
}
