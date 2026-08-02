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

	// Call RunPregenerate to batch-generate MP3 files
	pregenResult, err := g.exec.RunPregenerate(ctx, pybridge.PregenOpts{
		Words:     words,
		Language: "english",
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

		// Convert with retry (will be implemented in Task 2)
		pcmData, err := g.convertFn(mp3Path, ConvertOpts{
			SampleRate: 16000,
			Channels:   1,
			BigEndian:  true,
		})

		if err != nil {
			// For now, just log and skip (retry logic added in Task 2)
			g.logger.Warn("word conversion failed, skipping",
				infra.F("component", "tts.generator"),
				infra.F("word_id", wordID),
				infra.ErrField(err),
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
