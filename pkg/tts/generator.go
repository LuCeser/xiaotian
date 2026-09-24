package tts

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// AudioSource identifies which TTS backend produced a session's word audio.
// The session layer threads it into the word_complete control frame so the
// frontend knows how many times to play each track (see
// session.WordCompleteAudioPlays).
type AudioSource string

const (
	// SourceMimo marks audio synthesized per-word by the Mimo TTS client —
	// a SINGLE bare-word pronunciation; the frontend schedules the
	// dictation-standard double play.
	SourceMimo AudioSource = "mimo"

	// SourceVolcengine marks audio from the Volcengine whole-batch
	// pregenerate path — paired "word. word." tracks; the frontend plays
	// each track once. Also the reported source whenever no word
	// synthesizer is attached or the Mimo path fell back.
	SourceVolcengine AudioSource = "volcengine"
)

// Generator orchestrates batch TTS audio generation for a dictation session.
// It calls the Python bridge's RunPregenerate to batch-generate MP3 files,
// converts each MP3 to PCM using the injected convertFn, and stores results
// in a SessionCache. When a WordSynthesizer is attached (SetWordSynthesizer),
// it is the PRIMARY per-word source (DICT-03: Mimo) and RunPregenerate
// degrades to the whole-batch fallback.
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
	// synth is the OPTIONAL direct word synthesizer (Mimo). nil = the
	// generator is Volcengine-only (backward compatible with every existing
	// constructor call site). Written once during boot wiring via
	// SetWordSynthesizer, before the first session exists, so it needs no
	// lock — GenerateForSession calls only happen after wiring completes.
	synth WordSynthesizer
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

// SetWordSynthesizer attaches the optional PRIMARY word synthesizer (Mimo).
// Call once during boot wiring, before the first GenerateForSession — the
// field is deliberately lock-free because sessions only exist after wiring
// completes. Passing nil detaches and restores Volcengine-only behaviour.
func (g *Generator) SetWordSynthesizer(s WordSynthesizer) {
	g.synth = s
}

// GenerateForSession generates TTS audio for the given word list. Backward
// compatible delegate for GenerateForSessionWithSource that discards the
// source annotation (pre-DICT-03 callers and tests keep their signature).
func (g *Generator) GenerateForSession(ctx context.Context, sessionID string, words []string) (map[string]string, error) {
	paths, _, err := g.GenerateForSessionWithSource(ctx, sessionID, words)
	return paths, err
}

// GenerateForSessionWithSource generates TTS audio for the given word list
// and reports WHICH backend produced it.
//
// Source selection (DICT-03): when a word synthesizer is attached, every
// word synthesizes through it SEQUENTIALLY — one bare pronunciation per
// word, resampled to 16kHz, stored in the session cache — and the result is
// annotated SourceMimo. The FIRST failure (synthesis, resample, or cache
// write) logs a warning with the word and error, and the whole batch falls
// back to the existing RunPregenerate path, annotated SourceVolcengine.
// Discarding the partial Mimo cache entries is deliberately unnecessary:
// the fallback re-Stores every word under the same sessionID and cache keys,
// and SessionCache.Store writes via os.WriteFile, which truncates — a
// successful fallback leaves no mixed Mimo/Volcengine files behind.
//
// If the word list is empty, returns an empty map, SourceVolcengine, and no
// error without calling RunPregenerate (unchanged contract). If
// RunPregenerate fails, returns an empty map and an error wrapping
// ErrTTSGenerationFailed. Individual word conversion failures are retried up
// to MaxRetries; words that exhaust retries are skipped without aborting the
// entire session (D-05).
func (g *Generator) GenerateForSessionWithSource(ctx context.Context, sessionID string, words []string) (map[string]string, AudioSource, error) {
	// Guard: empty word list → no subprocess spawn, no synth calls (same as
	// pregenArgs in pregenerate.go).
	if len(words) == 0 {
		return make(map[string]string), SourceVolcengine, nil
	}

	if g.synth != nil {
		paths, err := g.generateViaSynth(ctx, sessionID, words)
		if err == nil {
			g.logger.Info("dictation words synthesized via mimo",
				infra.F("component", "tts.generator"),
				infra.F("session_id", sessionID),
				infra.F("word_count", len(paths)),
			)
			return paths, SourceMimo, nil
		}
		// generateViaSynth already logged the failing word at Warn. Fall
		// through to the whole-batch path; it overwrites any partial cache
		// entries (see the Store-overwrite note in the doc comment above).
	}

	paths, err := g.generateViaPregenerate(ctx, sessionID, words)
	return paths, SourceVolcengine, err
}

// generateViaSynth synthesizes each word through the attached
// WordSynthesizer, one at a time. Sequential on purpose: the Mimo client
// already owns retry/backoff, and per-word transient bytes keep the 150MB
// memory budget flat (no batch buffering). Preserves the GenerateForSession
// invariant — the returned map covers every word, or an error is returned.
func (g *Generator) generateViaSynth(ctx context.Context, sessionID string, words []string) (map[string]string, error) {
	result := make(map[string]string, len(words))
	for _, word := range words {
		pcm, err := g.synth.SynthesizeWord(ctx, word)
		if err != nil {
			g.logger.Warn("mimo word synthesis failed — falling back to batch TTS",
				infra.F("component", "tts.generator"),
				infra.F("word", word),
				infra.ErrField(err),
			)
			return nil, err
		}
		if err := g.cache.Store(sessionID, word, pcm.Samples); err != nil {
			g.logger.Warn("failed to cache mimo PCM — falling back to batch TTS",
				infra.F("component", "tts.generator"),
				infra.F("word", word),
				infra.ErrField(err),
			)
			return nil, err
		}
		result[word] = g.cache.Path(sessionID, word)
	}
	return result, nil
}

// generateViaPregenerate is the pre-DICT-03 whole-batch path: RunPregenerate
// batch-generates MP3 files (Volcengine on the Python side), each MP3 is
// converted to PCM, and results land in the session cache. Returns a map of
// wordID -> cache path, erroring unless every word has audio.
func (g *Generator) generateViaPregenerate(ctx context.Context, sessionID string, words []string) (map[string]string, error) {
	result := make(map[string]string)

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
		GenerationMode: "bare",
	})
	if err != nil {
		g.logger.Error("TTS batch generation failed",
			infra.F("component", "tts.generator"),
			infra.ErrField(err),
		)
		return result, ErrTTSGenerationFailed
	}

	// Validate path count matches word count — a mismatch means the Python
	// script returned fewer audio files than requested (partial TTS failure,
	// flock contention, or --words-per-segment batching). Without this check,
	// words beyond the path count silently get no audio (StreamWord returns
	// ErrWordNotCached, spawnStreamWord pushes WordComplete anyway, session
	// advances with no audio).
	//
	// Returning an error here prevents the "only first word plays" bug:
	// the session fails to initialize instead of silently advancing through
	// uncached words with no audio.
	if len(pregenResult.Paths) < len(words) {
		g.logger.Error("TTS pregenerate returned fewer paths than words — aborting session",
			infra.F("component", "tts.generator"),
			infra.F("path_count", len(pregenResult.Paths)),
			infra.F("word_count", len(words)),
			infra.F("missing_words", words[len(pregenResult.Paths):]),
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
		result[wordID] = g.cache.Path(sessionID, wordID)
	}

	// Final validation: all words must have audio. If any word was skipped
	// due to codec failure (retryConvert exhausted retries), the result map
	// will have fewer entries than the input word list. Returning an error
	// here prevents the "only first word plays" bug: the session fails to
	// initialize instead of silently advancing through uncached words.
	if len(result) < len(words) {
		// Collect missing words for diagnostic logging
		cached := make(map[string]bool, len(result))
		for k := range result {
			cached[k] = true
		}
		var missing []string
		for _, w := range words {
			if !cached[w] {
				missing = append(missing, w)
			}
		}
		g.logger.Error("TTS generation incomplete — not all words have audio",
			infra.F("component", "tts.generator"),
			infra.F("cached_count", len(result)),
			infra.F("word_count", len(words)),
			infra.F("missing_words", missing),
		)
		return result, ErrTTSGenerationFailed
	}

	return result, nil
}

// GenerateText synthesizes one arbitrary spoken text — not a dictation word —
// into the session cache, returning its cache path. This is the spoken-
// feedback path (Ticket 06): the encouragement sentence is passed as-is and
// the text itself is the cache key (SessionCache hashes non-identifier keys
// into safe filenames). language selects the TTS voice profile
// ("english"|"chinese") — the word pipeline always synthesizes English, so
// feedback needs its own entry point to speak Chinese sentences.
//
// Unlike GenerateForSession this does NOT hard-fail the session on error —
// callers treat spoken feedback as best-effort.
func (g *Generator) GenerateText(ctx context.Context, sessionID, text, language string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.Wrap(ErrTTSGenerationFailed, "tts.generator: empty feedback text")
	}
	if language == "" {
		language = "english"
	}

	pregenResult, err := g.exec.RunPregenerate(ctx, pybridge.PregenOpts{
		Words:           []string{text},
		Language:        language,
		WordsPerSegment: 1,
		// "bare" = the text verbatim plus trailing silence, no intro/scene
		// narration — exactly what a feedback sentence wants.
		GenerationMode: "bare",
	})
	if err != nil {
		g.logger.Error("TTS feedback generation failed",
			infra.F("component", "tts.generator"),
			infra.F("language", language),
			infra.ErrField(err),
		)
		return "", ErrTTSGenerationFailed
	}
	if len(pregenResult.Paths) == 0 {
		g.logger.Error("TTS feedback generation returned no paths",
			infra.F("component", "tts.generator"),
		)
		return "", ErrTTSGenerationFailed
	}

	pcmData, err := g.retryConvert(ctx, pregenResult.Paths[0])
	if err != nil {
		return "", errors.Wrap(err, "tts feedback convert")
	}
	if err := g.cache.Store(sessionID, text, pcmData.Samples); err != nil {
		return "", errors.Wrap(err, "tts feedback cache")
	}
	return g.cache.Path(sessionID, text), nil
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
