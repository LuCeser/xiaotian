package ws

import (
	"testing"
)

// TestComputeCRC16_CCITTStandardVector tests the standard CRC-16-CCITT test vector.
func TestComputeCRC16_CCITTStandardVector(t *testing.T) {
	// Standard test vector from ITU-T V.41: "123456789" -> 0x2189
	data := []byte("123456789")
	result := ComputeCRC16(data)
	if result != 0x2189 {
		t.Errorf("ComputeCRC16([]byte(\"123456789\")) = 0x%04x, want 0x2189", result)
	}
}

// TestValidateFrameValidFrame tests that valid frames pass validation.
func TestValidateFrameValidFrame(t *testing.T) {
	header := &FrameHeader{
		Version:    0x01,
		MsgType:    MsgTypeAudio,
		Sequence:   12345,
		Timestamp:  1678901234,
		PayloadLen: 640,
		Reserved:   0,
	}

	encoded := EncodeFrame(header, []byte{})

	if !ValidateFrame(encoded) {
		t.Error("ValidateFrame(valid frame) returned false, want true")
	}
}

// TestValidateFrameCorruptedByte tests that corrupting any header byte fails validation.
func TestValidateFrameCorruptedByte(t *testing.T) {
	header := &FrameHeader{
		Version:    0x01,
		MsgType:    MsgTypeAudio,
		Sequence:   12345,
		Timestamp:  1678901234,
		PayloadLen: 640,
		Reserved:   0,
	}

	encoded := EncodeFrame(header, []byte{})

	// Test corrupting each byte in header[0:14] (all bytes except CRC field)
	for i := 0; i < 14; i++ {
		// Create a corrupted copy
		corrupted := make([]byte, len(encoded))
		copy(corrupted, encoded)
		corrupted[i] ^= 0xFF // Flip all bits

		if ValidateFrame(corrupted) {
			t.Errorf("ValidateFrame(corrupted byte at index %d) returned true, want false", i)
		}
	}
}

// TestValidateFrameTooShort tests that short frames fail validation.
func TestValidateFrameTooShort(t *testing.T) {
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
			if ValidateFrame(tt.data) {
				t.Error("ValidateFrame(short frame) returned true, want false")
			}
		})
	}
}

// TestEncodeFrameRoundTrip_Audio tests audio frame round-trip encoding/decoding.
func TestEncodeFrameRoundTrip_Audio(t *testing.T) {
	header := &FrameHeader{
		Version:    0x01,
		MsgType:    MsgTypeAudio,
		Sequence:   12345,
		Timestamp:  1678901234,
		PayloadLen: 640,
		Reserved:   0,
	}

	// Create deterministic 640-byte payload
	payload := make([]byte, AudioFrameSize)
	for i := 0; i < AudioFrameSize; i++ {
		payload[i] = byte(i % 256)
	}

	encoded := EncodeFrame(header, payload)
	decodedHeader, decodedPayload, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame() error = %v", err)
	}

	// Verify header matches
	if decodedHeader.Version != header.Version ||
		decodedHeader.MsgType != header.MsgType ||
		decodedHeader.Sequence != header.Sequence ||
		decodedHeader.Timestamp != header.Timestamp ||
		decodedHeader.PayloadLen != header.PayloadLen ||
		decodedHeader.Reserved != header.Reserved {
		t.Error("Decoded header does not match original header")
	}

	// Verify payload matches
	if len(decodedPayload) != len(payload) {
		t.Errorf("Decoded payload length = %d, want %d", len(decodedPayload), len(payload))
	}
	for i := range payload {
		if decodedPayload[i] != payload[i] {
			t.Errorf("Payload byte %d mismatch: got 0x%02x, want 0x%02x", i, decodedPayload[i], payload[i])
		}
	}
}

// TestEncodeFrameRoundTrip_Control tests control frame round-trip with variable payload.
func TestEncodeFrameRoundTrip_Control(t *testing.T) {
	header := &FrameHeader{
		Version:    0x01,
		MsgType:    MsgTypeControl,
		Sequence:   67890,
		Timestamp:  1678905678,
		PayloadLen: 32,
		Reserved:   123,
	}

	// Create 32-byte payload
	payload := make([]byte, 32)
	for i := 0; i < 32; i++ {
		payload[i] = byte(i * 2)
	}

	encoded := EncodeFrame(header, payload)
	decodedHeader, decodedPayload, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame() error = %v", err)
	}

	// Verify header matches
	if decodedHeader.Version != header.Version ||
		decodedHeader.MsgType != header.MsgType ||
		decodedHeader.Sequence != header.Sequence ||
		decodedHeader.Timestamp != header.Timestamp ||
		decodedHeader.PayloadLen != header.PayloadLen ||
		decodedHeader.Reserved != header.Reserved {
		t.Error("Decoded header does not match original header")
	}

	// Verify payload matches
	if len(decodedPayload) != len(payload) {
		t.Errorf("Decoded payload length = %d, want %d", len(decodedPayload), len(payload))
	}
	for i := range payload {
		if decodedPayload[i] != payload[i] {
			t.Errorf("Payload byte %d mismatch: got 0x%02x, want 0x%02x", i, decodedPayload[i], payload[i])
		}
	}
}

// TestEncodeFrameSetsPayloadLen tests that EncodeFrame sets PayloadLen correctly.
func TestEncodeFrameSetsPayloadLen(t *testing.T) {
	header := &FrameHeader{
		Version:  0x01,
		MsgType:  MsgTypeAudio,
		Sequence: 12345,
	}

	tests := []struct {
		name          string
		payloadLen    int
		expectedLen   uint16
	}{
		{"zero payload", 0, 0},
		{"small payload", 32, 32},
		{"audio payload", AudioFrameSize, AudioFrameSize},
		{"max payload", 65535, 65535},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := make([]byte, tt.payloadLen)
			encoded := EncodeFrame(header, payload)

			// Extract PayloadLen from encoded header (bytes 10-12)
			// This is a bit-endian uint16
			payloadLen := uint16(encoded[10])<<8 | uint16(encoded[11])
			if payloadLen != tt.expectedLen {
				t.Errorf("PayloadLen = %d, want %d", payloadLen, tt.expectedLen)
			}
		})
	}
}

// TestDecodeFrameCRCChecksumFailed tests that corrupted frames return CRC error.
func TestDecodeFrameCRCChecksumFailed(t *testing.T) {
	header := &FrameHeader{
		Version:    0x01,
		MsgType:    MsgTypeAudio,
		Sequence:   12345,
		Timestamp:  1678901234,
		PayloadLen: 640,
		Reserved:   0,
	}

	encoded := EncodeFrame(header, make([]byte, 640))

	// Corrupt a byte in the header (but not in CRC field)
	encoded[5] ^= 0xFF

	_, _, err := DecodeFrame(encoded)
	if err != ErrCRCChecksumFailed {
		t.Errorf("DecodeFrame(corrupted frame) error = %v, want %v", err, ErrCRCChecksumFailed)
	}
}

// TestDecodeFrameTooShort tests that short frames return error.
func TestDecodeFrameTooShort(t *testing.T) {
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
			_, _, err := DecodeFrame(tt.data)
			if err != ErrInvalidFrame {
				t.Errorf("DecodeFrame(short frame) error = %v, want %v", err, ErrInvalidFrame)
			}
		})
	}
}

// TestDecodeFrameAudioSizeMismatch tests that audio frames with wrong payload size fail.
func TestDecodeFrameAudioSizeMismatch(t *testing.T) {
	header := &FrameHeader{
		Version:    0x01,
		MsgType:    MsgTypeAudio,
		Sequence:   12345,
		Timestamp:  1678901234,
		PayloadLen: 320, // Wrong size (should be 640)
		Reserved:   0,
	}

	encoded := EncodeFrame(header, make([]byte, 320))

	_, _, err := DecodeFrame(encoded)
	if err != ErrInvalidFrame {
		t.Errorf("DecodeFrame(audio frame with wrong size) error = %v, want %v", err, ErrInvalidFrame)
	}
}

// TestDecodeFramePayloadLenMismatch tests that payload length mismatches fail.
func TestDecodeFramePayloadLenMismatch(t *testing.T) {
	header := &FrameHeader{
		Version:    0x01,
		MsgType:    MsgTypeControl,
		Sequence:   12345,
		Timestamp:  1678901234,
		PayloadLen: 32,
		Reserved:   0,
	}

	encoded := EncodeFrame(header, make([]byte, 32))

	// Corrupt the PayloadLen field to create mismatch
	encoded[10] ^= 0xFF

	_, _, err := DecodeFrame(encoded)
	if err != ErrInvalidFrame {
		t.Errorf("DecodeFrame(payload length mismatch) error = %v, want %v", err, ErrInvalidFrame)
	}
}
