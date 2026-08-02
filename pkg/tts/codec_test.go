package tts

import (
	"os/exec"
	"testing"
)

// TestSliceToFrames_1280Bytes_Returns2Frames tests SliceToFrames with a 1280-byte
// PCM input (exactly 2 * 640) returns exactly 2 frames.
func TestSliceToFrames_1280Bytes_Returns2Frames(t *testing.T) {
	pcm := make([]byte, 1280)
	frames := SliceToFrames(pcm, 640)

	if len(frames) != 2 {
		t.Fatalf("SliceToFrames(1280 bytes, 640) returned %d frames, want 2", len(frames))
	}
	if len(frames[0]) != 640 {
		t.Errorf("Frame 0 length = %d, want 640", len(frames[0]))
	}
	if len(frames[1]) != 640 {
		t.Errorf("Frame 1 length = %d, want 640", len(frames[1]))
	}
}

// TestSliceToFrames_641Bytes_Returns2Frames tests SliceToFrames with 641 bytes
// (1 byte over 640) returns 2 frames with the last frame padded to 640 bytes.
func TestSliceToFrames_641Bytes_Returns2Frames(t *testing.T) {
	pcm := make([]byte, 641)
	// Set the last byte to a non-zero value so we can verify padding
	pcm[640] = 0xFF

	frames := SliceToFrames(pcm, 640)

	if len(frames) != 2 {
		t.Fatalf("SliceToFrames(641 bytes, 640) returned %d frames, want 2", len(frames))
	}
	if len(frames[0]) != 640 {
		t.Errorf("Frame 0 length = %d, want 640", len(frames[0]))
	}
	if len(frames[1]) != 640 {
		t.Errorf("Frame 1 length = %d, want 640 (padded)", len(frames[1]))
	}
	// Verify the last frame is padded with zeros
	if frames[1][639] != 0 {
		t.Errorf("Frame 1 last byte = %d, want 0 (padding)", frames[1][639])
	}
}

// TestSliceToFrames_Empty_ReturnsEmptySlice tests SliceToFrames with empty input
// returns an empty (non-nil) slice and does not panic.
func TestSliceToFrames_Empty_ReturnsEmptySlice(t *testing.T) {
	pcm := []byte{}
	frames := SliceToFrames(pcm, 640)

	if frames == nil {
		t.Fatal("SliceToFrames(empty) returned nil, want non-nil empty slice")
	}
	if len(frames) != 0 {
		t.Errorf("SliceToFrames(empty) returned %d frames, want 0", len(frames))
	}
}

// TestMP3ToPCM_FFmpegNotFound_ReturnsErrFFmpegNotFound tests that MP3ToPCM
// returns ErrFFmpegNotFound when ffmpeg is not on PATH.
func TestMP3ToPCM_FFmpegNotFound_ReturnsErrFFmpegNotFound(t *testing.T) {
	// Skip this test if ffmpeg is actually installed
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		t.Skip("ffmpeg is installed, skipping ErrFFmpegNotFound test")
	}

	opts := ConvertOpts{
		SampleRate: 16000,
		Channels:   1,
		BigEndian:  true,
	}

	_, err := MP3ToPCM("/nonexistent.mp3", opts)

	if err == nil {
		t.Fatal("MP3ToPCM with ffmpeg not on PATH returned nil error, want ErrFFmpegNotFound")
	}
}
