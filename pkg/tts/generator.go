package tts

import (
	"context"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// Generator orchestrates batch TTS audio generation for a dictation session.
// It calls the Python bridge's RunPregenerate to batch-generate MP3 files,
// converts each MP3 to PCM using the injected convertFn, and stores results
// in a SessionCache.
type Generator struct {
	// exec is the Python bridge executor that runs pregenerate_dictation.py
	exec pybridge.Executor
	// cache stores PCM files per session
	cache *SessionCache
	// convertFn is the MP3-to-PCM conversion function (injected for testing)
	convertFn func(string, ConvertOpts) (PCMData, error)
	// logger is the structured logger
	logger infra.Logger
	// cfg holds TTS configuration (retries, timeouts)
	cfg infra.TTSConfig
}

// NewGenerator creates a new Generator with the given dependencies.
// Returns ErrTTSGenerationFailed (wrapped) if exec is nil.
func NewGenerator(exec pybridge.Executor, cache *SessionCache, logger infra.Logger, cfg infra.TTSConfig) (*Generator, error) {
	if exec == nil {
		return nil, errors.Wrap(ErrTTSGenerationFailed, "pybridge.Executor must not be nil")
	}
	if cache == nil {
		return nil, errors.New("SessionCache must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}

	return &Generator{
		exec:      exec,
		cache:     cache,
		convertFn: MP3ToPCM, // Default to the real MP3ToPCM function
		logger:    logger,
		cfg:       cfg,
	}, nil
}

// GenerateForSession generates TTS audio for the given word list.
// It calls RunPregenerate to batch-generate MP3 files, converts each MP3
// to PCM, and stores results in the session cache. Returns a map of
// wordID -> cache path for successfully generated words.
//
// If the word list is empty, returns an empty map without calling RunPregenerate.
// If RunPregenerate fails, returns an empty map and an error wrapping ErrTTSGenerationFailed.
// Individual word conversion failures are retried up to MaxRetries; words that exhaust
// retries are skipped without aborting the entire session (D-05).
func (g *Generator) GenerateForSession(ctx context.Context, sessionID string, words []string) (map[string]string, error) {
	result := make(map[string]string)

	// Guard: empty word list → no subprocess spawn (same as pregenArgs in pregenerate.go)
	if len(words) == 0 {
		return result, nil
	}

	// Call RunPregenerate to batch-generate MP3 files.
	// WordsPerSegment:1 is REQUIRED — pregenerate_dictation.py concatenates
	// `words_per_segment` words (+ "number N" intros + silence) into ONE mp3 per
	// segment, and returns one path per segment (NOT per word). Below we map
	// paths[i] -> words[i] positionally, so each segment must contain exactly one
	// word for the mapping to stay correct. The default ("all words in one
	// segment") would make the first word's cache hold the entire dictation track.
	pregenResult, err := g.exec.RunPregenerate(ctx, pybridge.PregenOpts{
		Words:           words,
		Language:        "english",
		WordsPerSegment: 1,
		// "bare" emits one bare-word track per word (no scene/intro narration),
		// so paths[i] <-> words[i] maps to the bare word the per-word Play
		// button should play. Kept separate from the full-dictation "fragments"
		// mode which adds opening/number/closing narration.
		GenerationMode:  "bare",
	})
	if err != nil {
		g.logger.Error("TTS batch generation failed",
			infra.F("component", "tts.generator"),
			infra.ErrField(err),
		)
		return result, ErrTTSGenerationFailed
	}

	// Convert each MP3 to PCM and store in cache
	// Word IDs come from the input word list (position-based mapping)
	for i, mp3Path := range pregenResult.Paths {
		// Derive wordID from the word list at the same index
		// If index exceeds words length (shouldn't happen), use basename
		wordID := ""
		if i < len(words) {
			wordID = words[i]
		} else {
			// Fallback: use basename without extension
			// This handles edge cases where the path list is longer than words
			wordID = filepath.Base(mp3Path)
			// Remove .mp3 extension if present
			if len(wordID) > 4 && wordID[len(wordID)-4:] == ".mp3" {
				wordID = wordID[:len(wordID)-4]
			}
		}

		// Convert with retry using retryConvert
		pcmData, err := g.retryConvert(ctx, mp3Path)
		if err != nil {
			// Log warning for skipped word (exhausted retries or non-retryable error)
			g.logger.Warn("word generation failed after retries, skipping",
				infra.F("component", "tts.generator"),
				infra.F("word_id", wordID),
				infra.F("error_type", err.Error()),
			)
			continue
		}

		// Store in cache
		if err := g.cache.Store(sessionID, wordID, pcmData.Samples); err != nil {
			g.logger.Warn("failed to cache PCM",
				infra.F("component", "tts.generator"),
				infra.F("word_id", wordID),
				infra.ErrField(err),
			)
			continue
		}

		// Add to result map
		result[wordID] = filepath.Join(g.cache.SessionDir(sessionID), wordID+".pcm")
	}

	return result, nil
}

// retryConvert attempts to convert an MP3 file to PCM with retries.
// It loops up to MaxRetries times, classifying errors to determine retryability:
// - ErrFFmpegNotFound: returns immediately (infrastructure failure, not retryable)
// - context.DeadlineExceeded: retries with logging
// - Other errors: retries with logging
// Returns the last error wrapped in ErrTTSGenerationFailed after all retries exhausted.
func (g *Generator) retryConvert(ctx context.Context, mp3Path string) (PCMData, error) {
	var lastErr error

	// Retry counter starts at 1 for natural log messages ("attempt 1/3", "attempt 2/3", etc.)
	for attempt := 1; attempt <= g.cfg.MaxRetries; attempt++ {
		// Attempt conversion
		pcmData, err := g.convertFn(mp3Path, ConvertOpts{
			SampleRate: 16000,
			Channels:   1,
			BigEndian:  false, // s16le — matches frontend getInt16(offset, true)
		})

		if err == nil {
			// Success! Return immediately
			return pcmData, nil
		}

		lastErr = err

		// Classify error to determine retryability
		if errors.Is(err, ErrFFmpegNotFound) {
			// Infrastructure failure - not retryable
			g.logger.Error("ffmpeg not found on PATH, aborting",
				infra.F("component", "tts.generator"),
				infra.F("attempt", attempt),
				infra.ErrField(err),
			)
			return PCMData{}, err
		}

		if errors.Is(err, context.DeadlineExceeded) {
			// Timeout - log and retry
			g.logger.Debug("TTS generation attempt timed out",
				infra.F("component", "tts.generator"),
				infra.F("attempt", attempt),
				infra.F("max_retries", g.cfg.MaxRetries),
			)
			// Continue to next retry
		} else {
			// Other error (e.g., ErrPCMConversionFailed) - log and retry
			g.logger.Warn("TTS generation attempt failed",
				infra.F("component", "tts.generator"),
				infra.F("attempt", attempt),
				infra.F("max_retries", g.cfg.MaxRetries),
				infra.ErrField(err),
			)
			// Continue to next retry
		}
	}

	// All retries exhausted
	return PCMData{}, errors.Wrap(lastErr, "word generation exhausted retries")
}

// CleanupSession removes the session's PCM cache directory.
// It delegates to cache.Cleanup and logs the operation. This gives the session
// lifecycle (Phase 7) a single entry point to clean up TTS resources.
// Idempotent: calling multiple times on the same sessionID is safe.
func (g *Generator) CleanupSession(ctx context.Context, sessionID string) error {
	g.logger.Info("cleaning up TTS session",
		infra.F("component", "tts.generator"),
		infra.F("session_id", sessionID),
	)

	if err := g.cache.Cleanup(sessionID); err != nil {
		g.logger.Error("failed to cleanup session",
			infra.F("component", "tts.generator"),
			infra.F("session_id", sessionID),
			infra.ErrField(err),
		)
		return err
	}

	return nil
}
