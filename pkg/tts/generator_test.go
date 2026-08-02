package tts

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// fakeExecutor is a manual fake for pybridge.Executor per D-48.
// It returns pre-canned results without spawning a real Python subprocess.
type fakeExecutor struct {
	// pregenerateResult is the result to return from RunPregenerate.
	pregenerateResult pybridge.PregenResult
	// pregenerateError is the error to return from RunPregenerate.
	pregenerateError error
	// calledPregenerate counts how many times RunPregenerate was called.
	calledPregenerate int
	// lastOpts captures the last PregenOpts passed to RunPregenerate.
	lastOpts pybridge.PregenOpts
}

func (f *fakeExecutor) RunDue(ctx context.Context, opts pybridge.DueOpts) (pybridge.DueResult, error) {
	return pybridge.DueResult{}, nil
}

func (f *fakeExecutor) RunReview(ctx context.Context, opts pybridge.ReviewOpts) (pybridge.ReviewResult, error) {
	return pybridge.ReviewResult{}, nil
}

func (f *fakeExecutor) RunPregenerate(ctx context.Context, opts pybridge.PregenOpts) (pybridge.PregenResult, error) {
	f.calledPregenerate++
	f.lastOpts = opts
	if f.pregenerateError != nil {
		return pybridge.PregenResult{}, f.pregenerateError
	}
	return f.pregenerateResult, nil
}

// mockConvertFn is a mock MP3-to-PCM conversion function for testing.
type mockConvertFn struct {
	// pcmData is the PCM data to return on success.
	pcmData PCMData
	// err is the error to return. If set, overrides pcmData.
	err error
	// calledCount tracks how many times the function was called.
	calledCount int
	// failAfterN simulates transient failures by returning err after N successful calls.
	failAfterN int
}

func (m *mockConvertFn) call(mp3Path string, opts ConvertOpts) (PCMData, error) {
	m.calledCount++
	if m.failAfterN > 0 && m.calledCount > m.failAfterN {
		return PCMData{}, m.err
	}
	if m.err != nil {
		return PCMData{}, m.err
	}
	return m.pcmData, nil
}

// Test 1: GenerateForSession with a fake Executor returning 2 MP3 paths and a mock codec converting them produces 2 entries in the result map
func TestGenerateForSession_Success_TwoWords(t *testing.T) {
	exec := &fakeExecutor{
		pregenerateResult: pybridge.PregenResult{
			Paths: []string{"/tmp/word1.mp3", "/tmp/word2.mp3"},
		},
	}

	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	convertMock := &mockConvertFn{
		pcmData: PCMData{
			Samples:   []byte{0x00, 0x01, 0x02, 0x03},
			FrameSize: 640,
		},
	}

	cfg := infra.DefaultConfig().TTS
	logger := infra.NewNoOpLogger()

	generator, err := NewGenerator(exec, cache, logger, cfg)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}

	// Inject the mock convert function
	generator.convertFn = convertMock.call

	ctx := context.Background()
	sessionID := "test-session"
	words := []string{"word1", "word2"}

	result, err := generator.GenerateForSession(ctx, sessionID, words)
	if err != nil {
		t.Fatalf("GenerateForSession failed: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 results, got %d", len(result))
	}

	// Verify the result map has the correct keys (word IDs)
	if _, ok := result["word1"]; !ok {
		t.Error("result missing key 'word1'")
	}
	if _, ok := result["word2"]; !ok {
		t.Error("result missing key 'word2'")
	}

	// Verify the paths are correct
	expectedPath1 := filepath.Join(cache.SessionDir(sessionID), "word1.pcm")
	if result["word1"] != expectedPath1 {
		t.Errorf("expected path %q, got %q", expectedPath1, result["word1"])
	}

	expectedPath2 := filepath.Join(cache.SessionDir(sessionID), "word2.pcm")
	if result["word2"] != expectedPath2 {
		t.Errorf("expected path %q, got %q", expectedPath2, result["word2"])
	}

	// Verify pregenerate was called once
	if exec.calledPregenerate != 1 {
		t.Errorf("expected RunPregenerate to be called once, called %d times", exec.calledPregenerate)
	}

	// Verify PregenOpts was constructed correctly
	if exec.lastOpts.Language != "english" {
		t.Errorf("expected Language 'english', got %q", exec.lastOpts.Language)
	}
	if len(exec.lastOpts.Words) != 2 {
		t.Errorf("expected 2 words, got %d", len(exec.lastOpts.Words))
	}
}

// Test 2: GenerateForSession stores PCM files in SessionCache at paths {sessionID}/{wordID}.pcm verifiable via cache.Load
func TestGenerateForSession_StoresInCache(t *testing.T) {
	exec := &fakeExecutor{
		pregenerateResult: pybridge.PregenResult{
			Paths: []string{"/tmp/word1.mp3"},
		},
	}

	cacheDir := t.TempDir()
	cache, err := NewSessionCache(cacheDir, nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	convertMock := &mockConvertFn{
		pcmData: PCMData{
			Samples:   []byte{0xAA, 0xBB, 0xCC, 0xDD},
			FrameSize: 640,
		},
	}

	cfg := infra.DefaultConfig().TTS
	logger := infra.NewNoOpLogger()

	generator, err := NewGenerator(exec, cache, logger, cfg)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}

	generator.convertFn = convertMock.call

	ctx := context.Background()
	sessionID := "test-session"
	words := []string{"word1"}

	result, err := generator.GenerateForSession(ctx, sessionID, words)
	if err != nil {
		t.Fatalf("GenerateForSession failed: %v", err)
	}

	// Verify the file was stored and can be loaded
	stored, err := cache.Load(sessionID, "word1")
	if err != nil {
		t.Fatalf("cache.Load failed: %v", err)
	}

	// Verify the stored data matches what we wrote
	expectedPCM := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	if len(stored) != len(expectedPCM) {
		t.Errorf("expected %d bytes, got %d", len(expectedPCM), len(stored))
	}

	// Verify the path in the result map
	expectedPath := filepath.Join(cache.SessionDir(sessionID), "word1.pcm")
	if result["word1"] != expectedPath {
		t.Errorf("expected path %q, got %q", expectedPath, result["word1"])
	}
}

// Test 3: GenerateForSession with a fake Executor returning ErrPythonTimeout produces an empty result map and a non-nil error wrapping ErrTTSGenerationFailed
func TestGenerateForSession_PregenerateFails(t *testing.T) {
	exec := &fakeExecutor{
		pregenerateError: pybridge.ErrPythonTimeout,
	}

	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	convertMock := &mockConvertFn{
		pcmData: PCMData{
			Samples:   []byte{0x00},
			FrameSize: 640,
		},
	}

	cfg := infra.DefaultConfig().TTS
	logger := infra.NewNoOpLogger()

	generator, err := NewGenerator(exec, cache, logger, cfg)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}

	generator.convertFn = convertMock.call

	ctx := context.Background()
	sessionID := "test-session"
	words := []string{"word1"}

	result, err := generator.GenerateForSession(ctx, sessionID, words)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, ErrTTSGenerationFailed) {
		t.Errorf("expected ErrTTSGenerationFailed, got %v", err)
	}

	if len(result) != 0 {
		t.Errorf("expected empty result map, got %d entries", len(result))
	}
}

// Test 4: GenerateForSession with a fake Executor returning 3 paths but the codec failing on the 2nd path still generates and caches words 1 and 3 (the 2nd is skipped after retries)
// NOTE: Full retry logic is implemented in Task 2. For now, this test uses a selective mock that succeeds on word1 and word3 but fails on word2.
func TestGenerateForSession_CodecFailsOnSecondWord(t *testing.T) {
	exec := &fakeExecutor{
		pregenerateResult: pybridge.PregenResult{
			Paths: []string{"/tmp/word1.mp3", "/tmp/word2.mp3", "/tmp/word3.mp3"},
		},
	}

	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	// Use a selective mock that succeeds on specific paths, fails on others
	callCount := 0
	convertMock := func(mp3Path string, opts ConvertOpts) (PCMData, error) {
		callCount++
		// Fail only on word2 (second call)
		if callCount == 2 {
			return PCMData{}, ErrPCMConversionFailed
		}
		// Succeed on word1 and word3
		return PCMData{
			Samples:   []byte{0x00, 0x01},
			FrameSize: 640,
		}, nil
	}

	cfg := infra.DefaultConfig().TTS
	logger := infra.NewNoOpLogger()

	generator, err := NewGenerator(exec, cache, logger, cfg)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}

	generator.convertFn = convertMock

	ctx := context.Background()
	sessionID := "test-session"
	words := []string{"word1", "word2", "word3"}

	result, err := generator.GenerateForSession(ctx, sessionID, words)
	if err != nil {
		t.Fatalf("GenerateForSession failed: %v", err)
	}

	// Should have 2 successful results (word1 and word3)
	if len(result) != 2 {
		t.Errorf("expected 2 results (word1 and word3), got %d", len(result))
	}

	// Verify word1 and word3 are in the result
	if _, ok := result["word1"]; !ok {
		t.Error("result missing key 'word1' (should have succeeded)")
	}
	if _, ok := result["word2"]; ok {
		t.Error("result has key 'word2' (should have been skipped)")
	}
	if _, ok := result["word3"]; !ok {
		t.Error("result missing key 'word3' (should have succeeded)")
	}

	// Verify word1 and word3 can be loaded from cache
	_, err = cache.Load(sessionID, "word1")
	if err != nil {
		t.Errorf("word1 should be cached, got error: %v", err)
	}

	_, err = cache.Load(sessionID, "word2")
	if err == nil {
		t.Error("word2 should NOT be cached (conversion failed)")
	}

	_, err = cache.Load(sessionID, "word3")
	if err != nil {
		t.Errorf("word3 should be cached, got error: %v", err)
	}
}

// Test 5: GenerateForSession with an empty word list returns an empty map and nil error (no subprocess spawned)
func TestGenerateForSession_EmptyWordList(t *testing.T) {
	exec := &fakeExecutor{
		pregenerateResult: pybridge.PregenResult{
			Paths: []string{},
		},
	}

	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	convertMock := &mockConvertFn{
		pcmData: PCMData{
			Samples:   []byte{0x00},
			FrameSize: 640,
		},
	}

	cfg := infra.DefaultConfig().TTS
	logger := infra.NewNoOpLogger()

	generator, err := NewGenerator(exec, cache, logger, cfg)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}

	generator.convertFn = convertMock.call

	ctx := context.Background()
	sessionID := "test-session"
	words := []string{}

	result, err := generator.GenerateForSession(ctx, sessionID, words)
	if err != nil {
		t.Fatalf("GenerateForSession failed: %v", err)
	}

	if len(result) != 0 {
		t.Errorf("expected empty result map, got %d entries", len(result))
	}

	// Verify RunPregenerate was NOT called
	if exec.calledPregenerate != 0 {
		t.Errorf("expected RunPregenerate NOT to be called, called %d times", exec.calledPregenerate)
	}
}

// Test 6: GenerateForSession constructs PregenOpts with Words set to the input word list and Language set to "english"
func TestGenerateForSession_PregenOpts(t *testing.T) {
	exec := &fakeExecutor{
		pregenerateResult: pybridge.PregenResult{
			Paths: []string{"/tmp/word1.mp3"},
		},
	}

	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	convertMock := &mockConvertFn{
		pcmData: PCMData{
			Samples:   []byte{0x00},
			FrameSize: 640,
		},
	}

	cfg := infra.DefaultConfig().TTS
	logger := infra.NewNoOpLogger()

	generator, err := NewGenerator(exec, cache, logger, cfg)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}

	generator.convertFn = convertMock.call

	ctx := context.Background()
	sessionID := "test-session"
	words := []string{"apple", "banana", "cherry"}

	_, err = generator.GenerateForSession(ctx, sessionID, words)
	if err != nil {
		t.Fatalf("GenerateForSession failed: %v", err)
	}

	// Verify PregenOpts was constructed correctly
	if exec.lastOpts.Language != "english" {
		t.Errorf("expected Language 'english', got %q", exec.lastOpts.Language)
	}

	if len(exec.lastOpts.Words) != 3 {
		t.Errorf("expected 3 words, got %d", len(exec.lastOpts.Words))
	}

	expectedWords := []string{"apple", "banana", "cherry"}
	for i, word := range exec.lastOpts.Words {
		if word != expectedWords[i] {
			t.Errorf("word[%d]: expected %q, got %q", i, expectedWords[i], word)
		}
	}
}

// Test 7: NewGenerator with a nil Executor returns ErrTTSGenerationFailed
func TestNewGenerator_NilExecutor(t *testing.T) {
	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	cfg := infra.DefaultConfig().TTS
	logger := infra.NewNoOpLogger()

	_, err = NewGenerator(nil, cache, logger, cfg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, ErrTTSGenerationFailed) {
		t.Errorf("expected ErrTTSGenerationFailed, got %v", err)
	}
}
