package tts

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// Streamer implements the AudioStreamer interface (Phase 6 Plan 03). It reads
// PCM bytes from SessionCache, slices them into frames, encodes them as WS
// binary messages, and streams them to clients via Sender.Send with frame pacing.
type Streamer struct {
	// cache is the PCM file storage (Plan 06-01)
	cache *SessionCache
	// logger is the structured logger
	logger infra.Logger
	// cfg holds TTS configuration (prefetch count, timeouts)
	cfg infra.TTSConfig
	// prefetch is an in-memory cache of upcoming words' PCM (D-04 prefetch)
	prefetch    map[string][]byte
	prefetchMu  sync.RWMutex
}

// Compile-time interface check
var _ AudioStreamer = (*Streamer)(nil)

// NewStreamer creates a new Streamer with the given cache, logger, and config.
// Validates cache is non-nil. Substitutes NewNoOpLogger for nil logger.
func NewStreamer(cache *SessionCache, logger infra.Logger, cfg infra.TTSConfig) (*Streamer, error) {
	if cache == nil {
		return nil, errors.New("tts streamer: cache must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &Streamer{
		cache:    cache,
		logger:   logger,
		cfg:      cfg,
		prefetch: make(map[string][]byte),
	}, nil
}

// StreamWord delivers audio for a single word to the given client.
// It reads PCM bytes from cache (or prefetch), slices them into frames,
// encodes each as a WS binary frame, and sends via Sender.Send with 20ms
// frame pacing. The startSeq parameter enables D-06 breakpoint resume.
func (s *Streamer) StreamWord(ctx context.Context, client Sender, sessionID string, wordID string, startSeq uint32) error {
	// 1. Load PCM bytes: first check prefetch map, then cache
	var pcm []byte

	// Check prefetch map first (D-04)
	s.prefetchMu.RLock()
	if cached, ok := s.prefetch[wordID]; ok {
		pcm = cached
		s.prefetchMu.RUnlock()
	} else {
		s.prefetchMu.RUnlock()
		// Not in prefetch, load from disk
		var err error
		pcm, err = s.cache.Load(sessionID, wordID)
		if err != nil {
			if errors.Is(err, ErrWordNotCached) {
				return ErrWordNotCached
			}
			return errors.Wrap(err, "tts streamer: load pcm")
		}
	}

	// 2. Slice PCM into frames
	frames := SliceToFrames(pcm, ws.AudioFrameSize)
	if len(frames) == 0 {
		return nil // No audio to stream
	}

	// 3. Create a 20ms ticker for frame pacing (50 frames/sec = 16kHz playback rate)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	// 4. Loop over frames starting from startSeq
	// Skip frames before startSeq (D-06 breakpoint resume)
	// startSeq=0 means send all frames, startSeq=2 means start from frame 2 (skip 0,1)
	startIndex := int(startSeq)
	if startIndex < 0 {
		startIndex = 0
	}
	// If startSeq > 0, convert to 0-indexed frame index
	// startSeq=2 means start from frame index 1 (second frame), so startIndex = startSeq - 1
	// BUT startSeq=0 means start from frame index 0 (first frame)
	if startSeq > 0 {
		startIndex = int(startSeq - 1)
	}

	for i := startIndex; i < len(frames); i++ {
		// 4a. Wait for ticker OR context cancellation
		// First frame sends immediately (no ticker wait) for minimal latency
		if i == startIndex {
			// Don't wait for first frame - send immediately
		} else {
			select {
			case <-ticker.C:
				// Time to send next frame
			case <-ctx.Done():
				// Context cancelled - stop streaming
				return ctx.Err()
			}
		}

		// 4b. Build the WS frame header
		currentSeq := uint32(i + 1) // Sequences are 1-indexed
		header := ws.FrameHeader{
			Version:    ws.ProtocolVersion,
			MsgType:    ws.MsgTypeAudio,
			Sequence:   currentSeq,
			Timestamp:  uint32(time.Now().UnixMicro()),
			PayloadLen: uint16(len(frames[i])),
			Reserved:   0,
		}

		// 4c. Encode frame and send
		encodedFrame := ws.EncodeFrame(&header, frames[i])
		if err := client.Send(encodedFrame); err != nil {
			if errors.Is(err, ErrSendBufferFull) {
				// Drop frame and continue (D-73 drop-oldest semantics)
				s.logger.Warn("tts streamer: send buffer full, dropping frame",
					infra.F("word_id", wordID),
					infra.F("sequence", currentSeq))
				continue
			}
			if errors.Is(err, ErrClientClosing) {
				// Client disconnected - stop immediately
				return nil
			}
			// Other errors - stop streaming
			return errors.Wrap(err, "tts streamer: send frame")
		}
	}

	// 5. Send end-of-word notification frame
	s.sendWordComplete(ctx, client, wordID)

	return nil
}

// sendWordComplete sends a status frame indicating audio has finished for this word.
func (s *Streamer) sendWordComplete(ctx context.Context, client Sender, wordID string) {
	// Build JSON payload
	payload := map[string]string{
		"type":   "word_complete",
		"word_id": wordID,
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		s.logger.Warn("tts streamer: failed to marshal word_complete payload",
			infra.F("word_id", wordID),
			infra.F("error", err))
		return
	}

	// Build status frame
	header := ws.FrameHeader{
		Version:    ws.ProtocolVersion,
		MsgType:    ws.MsgTypeStatus,
		Sequence:   0, // Status frames don't need sequence numbers
		Timestamp:  uint32(time.Now().UnixMicro()),
		PayloadLen: uint16(len(jsonData)),
		Reserved:   0,
	}

	encodedFrame := ws.EncodeFrame(&header, jsonData)
	if err := client.Send(encodedFrame); err != nil {
		// Status frame send is informational - log and continue
		if !errors.Is(err, ErrSendBufferFull) && !errors.Is(err, ErrClientClosing) {
			s.logger.Warn("tts streamer: failed to send word_complete status",
				infra.F("word_id", wordID),
				infra.F("error", err))
		}
	}
}

// PrefetchNext pre-loads upcoming words' PCM into memory (D-04).
// Best-effort: failures are logged but do not error.
func (s *Streamer) PrefetchNext(sessionID string, wordIDs []string) {
	s.prefetchMu.Lock()
	defer s.prefetchMu.Unlock()

	for _, wordID := range wordIDs {
		// Skip if already prefetched
		if _, exists := s.prefetch[wordID]; exists {
			continue
		}

		// Load from cache
		pcm, err := s.cache.Load(sessionID, wordID)
		if err != nil {
			// Best-effort: log and continue
			s.logger.Debug("tts streamer: prefetch skipped",
				infra.F("word_id", wordID),
				infra.F("error", err))
			continue
		}

		// Store in prefetch map
		s.prefetch[wordID] = pcm
	}
}

// ClearPrefetch removes words from the prefetch map.
func (s *Streamer) ClearPrefetch(wordIDs ...string) {
	s.prefetchMu.Lock()
	defer s.prefetchMu.Unlock()

	for _, wordID := range wordIDs {
		delete(s.prefetch, wordID)
	}
}
