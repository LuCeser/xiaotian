package tts

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// mockSender is a test fake for the Sender interface
type mockSender struct {
	mu       sync.Mutex
	messages [][]byte
	sendErr  error
	closed   bool
}

func (m *mockSender) Send(msg []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClientClosing
	}
	if m.sendErr != nil {
		return m.sendErr
	}
	m.messages = append(m.messages, msg)
	return nil
}

func (m *mockSender) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
}

func (m *mockSender) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.messages)
}

func (m *mockSender) get(index int) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if index < 0 || index >= len(m.messages) {
		return nil
	}
	return m.messages[index]
}

// TestStreamWord_Basic tests that StreamWord sends the correct number of frames
func TestStreamWord_Basic(t *testing.T) {
	cache, err := NewSessionCache(t.TempDir(), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	// Store 1280 bytes of PCM (2 frames at 640 bytes each)
	pcmData := make([]byte, 1280)
	for i := range pcmData {
		pcmData[i] = byte(i % 256)
	}
	err = cache.Store("session1", "word1", pcmData)
	if err != nil {
		t.Fatalf("failed to store PCM: %v", err)
	}

	cfg := infra.TTSConfig{
		SessionDir:        t.TempDir(),
		MaxRetries:        3,
		GenerationTimeout: 120 * time.Second,
		PrefetchCount:     3,
	}

	streamer, err := NewStreamer(cache, infra.NewNoOpLogger(), cfg)
	if err != nil {
		t.Fatalf("failed to create streamer: %v", err)
	}

	sender := &mockSender{}
	ctx := context.Background()

	// Stream from sequence 0
	err = streamer.StreamWord(ctx, sender, "session1", "word1", 0)
	if err != nil {
		t.Fatalf("StreamWord failed: %v", err)
	}

	// Should send 2 frames: 2 audio frames. word_complete is emitted by the
	// session event loop, not the streamer (08-06 dedup).
	count := sender.count()
	if count != 2 {
		t.Errorf("expected 2 frames (2 audio), got %d", count)
	}

	// Verify first 2 frames are audio frames (656 bytes each)
	for i := 0; i < 2; i++ {
		frame := sender.get(i)
		if len(frame) != 656 {
			t.Errorf("frame %d: expected length 656, got %d", i, len(frame))
		}

		// Decode header and verify fields
		header := &ws.FrameHeader{}
		header.Version = frame[0]
		header.MsgType = ws.MessageType(frame[1])
		header.Sequence = binary.BigEndian.Uint32(frame[2:6])
		header.Timestamp = binary.BigEndian.Uint32(frame[6:10])
		header.PayloadLen = binary.BigEndian.Uint16(frame[10:12])

		if header.Version != ws.ProtocolVersion {
			t.Errorf("frame %d: expected version %d, got %d", i, ws.ProtocolVersion, header.Version)
		}
		if header.MsgType != ws.MsgTypeAudio {
			t.Errorf("frame %d: expected msgtype %d, got %d", i, ws.MsgTypeAudio, header.MsgType)
		}
		if header.PayloadLen != 640 {
			t.Errorf("frame %d: expected payloadlen 640, got %d", i, header.PayloadLen)
		}
		// First call to nextSeq returns 1 (D-11 session-level monotonic counter)
		expectedSeq := uint32(i + 1)
		if header.Sequence != expectedSeq {
			t.Errorf("frame %d: expected sequence %d, got %d", i, expectedSeq, header.Sequence)
		}
	}
}

// TestStreamWord_StartSeq tests that startSeq skips earlier frames
func TestStreamWord_StartSeq(t *testing.T) {
	cache, err := NewSessionCache(t.TempDir(), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	// Store 1920 bytes of PCM (3 frames)
	pcmData := make([]byte, 1920)
	err = cache.Store("session1", "word1", pcmData)
	if err != nil {
		t.Fatalf("failed to store PCM: %v", err)
	}

	cfg := infra.TTSConfig{
		SessionDir:        t.TempDir(),
		MaxRetries:        3,
		GenerationTimeout: 120 * time.Second,
		PrefetchCount:     3,
	}

	streamer, err := NewStreamer(cache, infra.NewNoOpLogger(), cfg)
	if err != nil {
		t.Fatalf("failed to create streamer: %v", err)
	}

	sender := &mockSender{}
	ctx := context.Background()

	// Stream from sequence 5 (should skip frames 1-4, but we only have 3 frames total).
	// So we get 0 frames: all audio frames are before sequence 5, and word_complete
	// is now emitted by the session layer, not the streamer (08-06 dedup).
	err = streamer.StreamWord(ctx, sender, "session1", "word1", 5)
	if err != nil {
		t.Fatalf("StreamWord failed: %v", err)
	}

	count := sender.count()
	if count != 0 {
		t.Errorf("expected 0 frames when startSeq=5 with only 3 audio frames total, got %d", count)
	}
}

// TestStreamWord_StartSeqPartial tests that startSeq=2 skips only the first frame
func TestStreamWord_StartSeqPartial(t *testing.T) {
	cache, err := NewSessionCache(t.TempDir(), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	// Store 1920 bytes of PCM (3 frames)
	pcmData := make([]byte, 1920)
	err = cache.Store("session1", "word1", pcmData)
	if err != nil {
		t.Fatalf("failed to store PCM: %v", err)
	}

	cfg := infra.TTSConfig{
		SessionDir:        t.TempDir(),
		MaxRetries:        3,
		GenerationTimeout: 120 * time.Second,
		PrefetchCount:     3,
	}

	streamer, err := NewStreamer(cache, infra.NewNoOpLogger(), cfg)
	if err != nil {
		t.Fatalf("failed to create streamer: %v", err)
	}

	sender := &mockSender{}
	ctx := context.Background()

	// Stream from startSeq=2. D-10: startSeq is 0-indexed, so startSeq=2 means
	// startIndex=2 — only frames[2] is sent (1 audio frame).
	err = streamer.StreamWord(ctx, sender, "session1", "word1", 2)
	if err != nil {
		t.Fatalf("StreamWord failed: %v", err)
	}

	count := sender.count()
	if count != 1 {
		t.Errorf("expected 1 frame (1 audio) when startSeq=2 with 3 audio frames total, got %d", count)
	}

	// First audio frame sent has seq 1 (first call to nextSeq for this session)
	frame := sender.get(0)
	header := &ws.FrameHeader{}
	header.Sequence = binary.BigEndian.Uint32(frame[2:6])
	if header.Sequence != 1 {
		t.Errorf("expected first frame sequence 1, got %d", header.Sequence)
	}
}

// TestStreamWord_ContextCancellation tests that context cancellation stops streaming
func TestStreamWord_ContextCancellation(t *testing.T) {
	cache, err := NewSessionCache(t.TempDir(), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	// Store 1280 bytes of PCM (2 frames)
	pcmData := make([]byte, 1280)
	err = cache.Store("session1", "word1", pcmData)
	if err != nil {
		t.Fatalf("failed to store PCM: %v", err)
	}

	cfg := infra.TTSConfig{
		SessionDir:        t.TempDir(),
		MaxRetries:        3,
		GenerationTimeout: 120 * time.Second,
		PrefetchCount:     3,
	}

	streamer, err := NewStreamer(cache, infra.NewNoOpLogger(), cfg)
	if err != nil {
		t.Fatalf("failed to create streamer: %v", err)
	}

	sender := &mockSender{}
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	err = streamer.StreamWord(ctx, sender, "session1", "word1", 0)
	if err == nil {
		t.Error("expected context error, got nil")
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// Should send zero frames (or very few if cancellation raced the first frame)
	count := sender.count()
	// With immediate cancellation, we expect 0 frames
	// But we allow 1 frame in case of race condition
	if count > 1 {
		t.Errorf("expected 0-1 frames with immediate cancellation, got %d", count)
	}
}

// TestStreamWord_WordNotCached tests that ErrWordNotCached is returned for missing words
func TestStreamWord_WordNotCached(t *testing.T) {
	cache, err := NewSessionCache(t.TempDir(), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	cfg := infra.TTSConfig{
		SessionDir:        t.TempDir(),
		MaxRetries:        3,
		GenerationTimeout: 120 * time.Second,
		PrefetchCount:     3,
	}

	streamer, err := NewStreamer(cache, infra.NewNoOpLogger(), cfg)
	if err != nil {
		t.Fatalf("failed to create streamer: %v", err)
	}

	sender := &mockSender{}
	ctx := context.Background()

	// Try to stream a word that doesn't exist
	err = streamer.StreamWord(ctx, sender, "session1", "nonexistent", 0)
	if err == nil {
		t.Error("expected ErrWordNotCached, got nil")
	} else if !errors.Is(err, ErrWordNotCached) {
		t.Errorf("expected ErrWordNotCached, got %v", err)
	}

	count := sender.count()
	if count != 0 {
		t.Errorf("expected 0 frames for non-cached word, got %d", count)
	}
}

// TestStreamWord_SendBufferFull tests that ErrSendBufferFull is handled gracefully
func TestStreamWord_SendBufferFull(t *testing.T) {
	cache, err := NewSessionCache(t.TempDir(), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	// Store 1280 bytes of PCM (2 frames)
	pcmData := make([]byte, 1280)
	err = cache.Store("session1", "word1", pcmData)
	if err != nil {
		t.Fatalf("failed to store PCM: %v", err)
	}

	cfg := infra.TTSConfig{
		SessionDir:        t.TempDir(),
		MaxRetries:        3,
		GenerationTimeout: 120 * time.Second,
		PrefetchCount:     3,
	}

	streamer, err := NewStreamer(cache, infra.NewNoOpLogger(), cfg)
	if err != nil {
		t.Fatalf("failed to create streamer: %v", err)
	}

	sender := &mockSender{sendErr: ErrSendBufferFull}
	ctx := context.Background()

	// Should not return error despite send failures
	err = streamer.StreamWord(ctx, sender, "session1", "word1", 0)
	if err != nil {
		t.Fatalf("StreamWord failed with ErrSendBufferFull: %v", err)
	}

	// All sends should have been attempted but rejected
	count := sender.count()
	if count != 0 {
		t.Errorf("expected 0 frames sent due to buffer full, got %d", count)
	}
}

// TestStreamWord_ClientClosing tests that ErrClientClosing is handled by stopping streaming
func TestStreamWord_ClientClosing(t *testing.T) {
	cache, err := NewSessionCache(t.TempDir(), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	// Store 1280 bytes of PCM (2 frames)
	pcmData := make([]byte, 1280)
	err = cache.Store("session1", "word1", pcmData)
	if err != nil {
		t.Fatalf("failed to store PCM: %v", err)
	}

	cfg := infra.TTSConfig{
		SessionDir:        t.TempDir(),
		MaxRetries:        3,
		GenerationTimeout: 120 * time.Second,
		PrefetchCount:     3,
	}

	streamer, err := NewStreamer(cache, infra.NewNoOpLogger(), cfg)
	if err != nil {
		t.Fatalf("failed to create streamer: %v", err)
	}

	sender := &mockSender{sendErr: ErrClientClosing}
	ctx := context.Background()

	// Should stop immediately on first send error
	err = streamer.StreamWord(ctx, sender, "session1", "word1", 0)
	if err != nil {
		t.Fatalf("StreamWord failed with ErrClientClosing: %v", err)
	}

	count := sender.count()
	if count != 0 {
		t.Errorf("expected 0 frames sent due to client closing, got %d", count)
	}
}

// TestStreamer_CompileTimeCheck ensures Streamer implements AudioStreamer
func TestStreamer_CompileTimeCheck(t *testing.T) {
	// This test exists only for the compile-time check below
	var _ AudioStreamer = (*Streamer)(nil)
}

// TestNewStreamer_InvalidArgs tests constructor validation
func TestNewStreamer_InvalidArgs(t *testing.T) {
	cfg := infra.TTSConfig{
		SessionDir:        t.TempDir(),
		MaxRetries:        3,
		GenerationTimeout: 120 * time.Second,
		PrefetchCount:     3,
	}

	// Nil cache should fail
	_, err := NewStreamer(nil, infra.NewNoOpLogger(), cfg)
	if err == nil {
		t.Error("expected error for nil cache, got nil")
	}

	// Nil logger should be substituted with NoOpLogger (should succeed)
	cache, _ := NewSessionCache(t.TempDir(), infra.NewNoOpLogger())
	streamer, err := NewStreamer(cache, nil, cfg)
	if err != nil {
		t.Errorf("expected nil logger to be substituted, got error: %v", err)
	}
	if streamer == nil {
		t.Error("expected non-nil streamer with substituted logger")
	}
}

// TestStreamWord_SessionMonotonicSeq verifies D-11: audio frame sequences are
// monotonic across multiple StreamWord calls sharing one sessionID. The second
// call's first frame seq equals the first call's last frame seq plus one.
func TestStreamWord_SessionMonotonicSeq(t *testing.T) {
	cache, err := NewSessionCache(t.TempDir(), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	// Store 1920 bytes of PCM (3 frames at 640 bytes each) for two words
	pcmData := make([]byte, 1920)
	for i := range pcmData {
		pcmData[i] = byte(i % 256)
	}
	err = cache.Store("session1", "word1", pcmData)
	if err != nil {
		t.Fatalf("failed to store word1 PCM: %v", err)
	}
	err = cache.Store("session1", "word2", pcmData)
	if err != nil {
		t.Fatalf("failed to store word2 PCM: %v", err)
	}

	cfg := infra.TTSConfig{
		SessionDir:        t.TempDir(),
		MaxRetries:        3,
		GenerationTimeout: 120 * time.Second,
		PrefetchCount:     3,
	}

	streamer, err := NewStreamer(cache, infra.NewNoOpLogger(), cfg)
	if err != nil {
		t.Fatalf("failed to create streamer: %v", err)
	}

	ctx := context.Background()
	const sessionID = "session1"

	// First StreamWord call — 3 audio frames + 1 status frame.
	sender1 := &mockSender{}
	err = streamer.StreamWord(ctx, sender1, sessionID, "word1", 0)
	if err != nil {
		t.Fatalf("first StreamWord failed: %v", err)
	}
	lastSeq := frameSeq(t, sender1.get(2))

	// Second StreamWord call on the same sessionID — 3 audio frames + 1 status.
	sender2 := &mockSender{}
	err = streamer.StreamWord(ctx, sender2, sessionID, "word2", 0)
	if err != nil {
		t.Fatalf("second StreamWord failed: %v", err)
	}
	firstSeq := frameSeq(t, sender2.get(0))

	// D-11: cross-word monotonicity — second call's first seq == first call's
	// last seq + 1.
	if firstSeq != lastSeq+1 {
		t.Errorf("expected second call first seq %d == first call last seq %d + 1, got %d", firstSeq, lastSeq, lastSeq+1)
	}
}

// TestStreamer_ClearSeqCounter verifies the D-11 teardown hook (Pitfall 5): after
// ClearSeqCounter, a subsequent StreamWord on the same sessionID restarts the
// sequence at 1.
func TestStreamer_ClearSeqCounter(t *testing.T) {
	cache, err := NewSessionCache(t.TempDir(), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	pcm1 := make([]byte, 640)
	cache.Store("session1", "word1", pcm1)

	cfg := infra.TTSConfig{
		SessionDir:        t.TempDir(),
		MaxRetries:        3,
		GenerationTimeout: 120 * time.Second,
		PrefetchCount:     3,
	}

	streamer, err := NewStreamer(cache, infra.NewNoOpLogger(), cfg)
	if err != nil {
		t.Fatalf("failed to create streamer: %v", err)
	}

	ctx := context.Background()
	const sessionID = "session1"

	// First StreamWord — 1 audio frame + 1 status frame.
	sender1 := &mockSender{}
	err = streamer.StreamWord(ctx, sender1, sessionID, "word1", 0)
	if err != nil {
		t.Fatalf("first StreamWord failed: %v", err)
	}

	// Clear the per-session counter (session teardown, Pitfall 5).
	streamer.ClearSeqCounter(sessionID)

	// Second StreamWord on the same sessionID — sequence restarts at 1.
	sender2 := &mockSender{}
	err = streamer.StreamWord(ctx, sender2, sessionID, "word1", 0)
	if err != nil {
		t.Fatalf("second StreamWord failed: %v", err)
	}
	firstSeq := frameSeq(t, sender2.get(0))
	if firstSeq != 1 {
		t.Errorf("expected first frame sequence 1 after ClearSeqCounter, got %d", firstSeq)
	}
}

// frameSeq decodes the Sequence field (bytes 2-6, big-endian) from an encoded
// WS frame.
func frameSeq(t *testing.T, frame []byte) uint32 {
	t.Helper()
	if len(frame) < ws.HeaderSize {
		t.Fatalf("frame too short: %d bytes", len(frame))
	}
	return binary.BigEndian.Uint32(frame[2:6])
}
