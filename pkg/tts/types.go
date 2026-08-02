// Package tts implements the TTS audio streaming layer (Phase 6 D-09). It
// converts pregenerate_dictation.py MP3 output to PCM frames and streams them
// via WebSocket binary frames.
//
// Import path: github.com/xiaotian/home-tutor-agent/pkg/tts
package tts

import (
	"context"
	"errors"
)

// AudioStreamer is the contract the WebSocket handler layer calls to deliver
// audio to a client (Phase 6 D-09). Implementations convert pre-generated MP3
// files to PCM frames and stream them via WebSocket binary frames.
type AudioStreamer interface {
	// StreamWord delivers audio for a single word to the given client.
	// The caller provides the sessionID and wordID to identify which audio
	// to stream, and startSeq sets the initial sequence number for the first
	// frame. Returns an error if the audio cannot be delivered.
	StreamWord(ctx context.Context, client Sender, sessionID string, wordID string, startSeq uint32) error
}

// Sender is the interface for sending binary WebSocket messages (extracted
// from ws.Client.Send so pkg/tts does not import pkg/ws, avoiding circular
// dependency). ws.Client satisfies this interface structurally.
type Sender interface {
	// Send sends a single binary WebSocket message. Returns an error if the
	// send buffer is full or the connection is closed.
	Send(msg []byte) error
}

// PCMData represents decoded PCM audio ready for frame slicing. All PCM audio
// uses 16kHz sample rate, mono (1 channel), 16-bit signed samples, big-endian
// byte order (matching the existing test-interface.html client which reads PCM
// via DataView.getInt16 with littleEndian=false).
type PCMData struct {
	// Samples is the raw PCM byte stream.
	Samples []byte
	// FrameSize is the size of each audio frame in bytes (640 for 20ms@16kHz).
	FrameSize int
}

// ConvertOpts controls the ffmpeg output format when converting MP3 to PCM.
type ConvertOpts struct {
	// SampleRate is the output sample rate in Hz (16000 for 16kHz).
	SampleRate int
	// Channels is the number of audio channels (1 for mono).
	Channels int
	// BigEndian selects big-endian byte order (true) vs little-endian (false).
	// Default true to match the existing client.
	BigEndian bool
}

// Sentinel errors returned by the tts layer. Callers MUST detect them via
// errors.Is, never by string matching (D-04).
var (
	// ErrFFmpegNotFound is returned when ffmpeg is not available on PATH.
	ErrFFmpegNotFound = errors.New("ffmpeg not found on PATH")

	// ErrPCMConversionFailed is returned when ffmpeg exits non-zero or produces
	// no output.
	ErrPCMConversionFailed = errors.New("MP3 to PCM conversion failed")

	// ErrSessionNotFound is returned when a session cache directory does not
	// exist.
	ErrSessionNotFound = errors.New("session not found")

	// ErrWordNotCached is returned when a requested wordID has no PCM file
	// in the session cache.
	ErrWordNotCached = errors.New("word PCM not cached")

	// ErrTTSGenerationFailed is returned when TTS generation fails after
	// retries.
	ErrTTSGenerationFailed = errors.New("TTS generation failed")

	// ErrSendBufferFull is returned when the WebSocket send buffer is full.
	ErrSendBufferFull = errors.New("send buffer full")
)
