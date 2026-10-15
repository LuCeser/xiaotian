package tts

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/pkg/errors"
)

// MP3ToPCM converts an MP3 file to raw PCM using ffmpeg as a subprocess
// (Phase 3 D-08: mp3-to-PCM conversion is Phase 6 work). Returns PCMData
// with FrameSize=640 (20ms@16kHz). Uses exec.CommandContext with a timeout
// and returns ErrFFmpegNotFound if ffmpeg is not on PATH.
func MP3ToPCM(mp3Path string, opts ConvertOpts) (PCMData, error) {
	// Look for ffmpeg on PATH first
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return PCMData{}, errors.Wrap(err, ErrFFmpegNotFound.Error())
	}

	// Create a context with timeout for the subprocess
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Build ffmpeg argv (never shell string per T-03-07 tampering mitigation)
	formatFlag := "s16le"
	codecFlag := "pcm_s16le"
	if opts.BigEndian {
		formatFlag = "s16be"
		codecFlag = "pcm_s16be"
	}

	argv := []string{
		ffmpegPath,
		"-i", mp3Path,
		"-f", formatFlag,
		"-codec:a", codecFlag,
		"-ar", fmt.Sprintf("%d", opts.SampleRate),
		"-ac", fmt.Sprintf("%d", opts.Channels),
		"-", // Write to stdout
	}

	// Create command with context timeout
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: argument vector passed to ffmpeg, not a shell; the binary comes from LookPath

	// Pipe stdout to a buffer for PCM data
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	// Pipe stderr to a separate buffer for error diagnostics
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Run the command
	if err := cmd.Run(); err != nil {
		return PCMData{}, errors.Wrap(err, ErrPCMConversionFailed.Error()+": "+stderr.String())
	}

	// Return the PCM data with FrameSize=640 (matches ws.AudioFrameSize)
	return PCMData{
		Samples:   stdout.Bytes(),
		FrameSize: 640,
	}, nil
}

// SliceToFrames splits a PCM byte slice into consecutive frameSize-byte chunks.
// The last chunk is right-padded with zero bytes if the PCM length is not a
// multiple of frameSize (ensures every frame is exactly frameSize bytes for the
// WS protocol). Empty input returns an empty (non-nil) slice.
func SliceToFrames(pcm []byte, frameSize int) [][]byte {
	if len(pcm) == 0 {
		return [][]byte{} // Non-nil empty slice
	}

	frameCount := (len(pcm) + frameSize - 1) / frameSize // Ceiling division
	frames := make([][]byte, frameCount)

	for i := 0; i < frameCount; i++ {
		start := i * frameSize
		end := start + frameSize

		if end >= len(pcm) {
			// Last frame: pad with zeros
			frame := make([]byte, frameSize)
			copy(frame, pcm[start:])
			frames[i] = frame
		} else {
			// Full frame
			frames[i] = pcm[start:end:end] // Use full capacity for slice
		}
	}

	return frames
}
