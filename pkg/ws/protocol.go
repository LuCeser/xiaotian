// Package ws provides WebSocket protocol implementation for the home tutor agent.
// This file implements the binary frame protocol with 16-byte headers and CRC-16-CCITT integrity checks.
package ws

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/xiaotian/home-tutor-agent/pkg/crc16"
)

// Package-level constants (D-05, D-09, D-11)
const (
	// HeaderSize is the fixed size of the frame header in bytes (D-05)
	HeaderSize = 16

	// AudioFrameSize is the size of an audio frame payload in bytes (20ms@16kHz PCM, D-09)
	AudioFrameSize = 640

	// ProtocolVersion is the current protocol version (D-11)
	ProtocolVersion = 0x01
)

// MessageType represents the type of message in a WebSocket frame (D-06, D-40)
type MessageType uint8

// MessageType constants (D-06 extended message type set, ordered by byte value)
const (
	MsgTypeAudio    MessageType = 0x01 // Audio data frame
	MsgTypeControl  MessageType = 0x02 // Control message
	MsgTypeError    MessageType = 0x03 // Error message
	MsgTypeHeartbeat MessageType = 0x04 // Heartbeat/ping
	MsgTypeSession  MessageType = 0x05 // Session management
	MsgTypeStatus   MessageType = 0x06 // Status update
)

// String returns a human-readable name for the MessageType (D-40, structured logging friendly)
func (mt MessageType) String() string {
	switch mt {
	case MsgTypeAudio:
		return "audio"
	case MsgTypeControl:
		return "control"
	case MsgTypeError:
		return "error"
	case MsgTypeHeartbeat:
		return "heartbeat"
	case MsgTypeSession:
		return "session"
	case MsgTypeStatus:
		return "status"
	default:
		return fmt.Sprintf("unknown(0x%02x)", uint8(mt))
	}
}

// FrameHeader represents the 16-byte fixed header of a WebSocket frame (D-05, D-11)
type FrameHeader struct {
	Version    uint8      // Protocol version (D-11)
	MsgType    MessageType // Message type (D-06)
	Sequence   uint32     // Sequence number (D-07, 32-bit consecutive)
	Timestamp  uint32     // Microsecond timestamp (optional)
	PayloadLen uint16     // Payload length in bytes (D-09, variable-length control messages)
	Reserved   uint16     // Reserved for future use (D-05)
	CRC16      uint16     // CRC-16-CCITT checksum (D-08, auto-filled by EncodeHeader)
}

// Sentinel errors for frame validation (D-44 centralized validation)
var (
	// ErrInvalidFrame is returned when a frame has invalid structure or length
	ErrInvalidFrame = errors.New("invalid frame")

	// ErrCRCChecksumFailed is returned when CRC-16 validation fails
	ErrCRCChecksumFailed = errors.New("crc-16 checksum failed")

	// ErrUnknownMessageType is returned when an unknown message type is encountered
	ErrUnknownMessageType = errors.New("unknown message type")

	// ErrUnsupportedVersion is returned when a decoded frame's protocol version is not
	// ProtocolVersion. Checked at the codec boundary so future wire-format changes can
	// be distinguished from legacy frames (WR-07).
	ErrUnsupportedVersion = errors.New("unsupported protocol version")
)

// EncodeHeader encodes a FrameHeader into a 16-byte slice using big-endian byte order (D-10)
// The CRC-16 field is automatically computed and written to bytes 14-15.
func EncodeHeader(h *FrameHeader) []byte {
	buf := make([]byte, HeaderSize)

	// Write fields according to 16-byte layout (D-05, PATTERNS.md Pattern 2)
	buf[0] = h.Version
	buf[1] = byte(h.MsgType)

	// Multi-byte fields use big-endian order (D-10)
	binary.BigEndian.PutUint32(buf[2:6], h.Sequence)
	binary.BigEndian.PutUint32(buf[6:10], h.Timestamp)
	binary.BigEndian.PutUint16(buf[10:12], h.PayloadLen)
	binary.BigEndian.PutUint16(buf[12:14], h.Reserved)

	// Compute CRC-16 over bytes 0-13 (D-08, covers all fields except CRC itself)
	crc := crc16.Checksum(buf[0:14])
	binary.BigEndian.PutUint16(buf[14:16], crc)

	// Update the header's CRC field for logging purposes
	h.CRC16 = crc

	return buf
}

// DecodeHeader decodes a 16-byte slice into a FrameHeader.
// Returns ErrInvalidFrame if the input is too short.
func DecodeHeader(raw []byte) (*FrameHeader, error) {
	if len(raw) < HeaderSize {
		return nil, ErrInvalidFrame
	}

	h := &FrameHeader{
		Version:    raw[0],
		MsgType:    MessageType(raw[1]),
		Sequence:   binary.BigEndian.Uint32(raw[2:6]),
		Timestamp:  binary.BigEndian.Uint32(raw[6:10]),
		PayloadLen: binary.BigEndian.Uint16(raw[10:12]),
		Reserved:   binary.BigEndian.Uint16(raw[12:14]),
		CRC16:      binary.BigEndian.Uint16(raw[14:16]),
	}

	return h, nil
}
