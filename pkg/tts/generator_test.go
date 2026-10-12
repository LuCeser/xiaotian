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

func (f *fakeExecutor) RunUnlock(_ context.Context, _ pybridge.UnlockOpts) (pybridge.UnlockResult, error) {
	return pybridge.UnlockResult{}, nil
}

func (f *fakeExecutor) RunAdd(_ context.Context, _ pybridge.AddOpts) (pybridge.AddResult, error) {
	return pybridge.AddResult{}, nil
}

func (f *fakeExecutor) RunEdit(_ context.Context, _ pybridge.EditOpts) (pybridge.EditResult, error) {
	return pybridge.EditResult{}, nil
}
func (f *fakeExecutor) RunTextbookCreate(context.Context, pybridge.TextbookCreateOpts) (pybridge.TextbookMutationResult, error) {
	return pybridge.TextbookMutationResult{}, nil
}

func (f *fakeExecutor) RunTextbookUpdate(context.Context, pybridge.TextbookUpdateOpts) (pybridge.TextbookMutationResult, error) {
	return pybridge.TextbookMutationResult{}, nil
}

func (f *fakeExecutor) RunAssignWords(context.Context, pybridge.AssignWordsOpts) (pybridge.AssignWordsResult, error) {
	return pybridge.AssignWordsResult{}, nil
}

func (f *fakeExecutor) RunUnassignWords(context.Context, pybridge.UnassignWordsOpts) (pybridge.UnassignWordsResult, error) {
	return pybridge.UnassignWordsResult{}, nil
}

func (f *fakeExecutor) RunSetParticipation(context.Context, pybridge.SetParticipationOpts) (pybridge.SetParticipationResult, error) {
	return pybridge.SetParticipationResult{}, nil
}

func (f *fakeExecutor) RunRenameUnit(_ context.Context, _ pybridge.RenameUnitOpts) (pybridge.RenameUnitResult, error) {
	return pybridge.RenameUnitResult{}, nil
}

func (f *fakeExecutor) RunDeleteWords(_ context.Context, _ pybridge.DeleteWordsOpts) (pybridge.DeleteWordsResult, error) {
	return pybridge.DeleteWordsResult{}, nil
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

// Test 4: GenerateForSession with a fake Executor returning 3 paths but the codec failing on the 2nd path returns ErrTTSGenerationFailed
// (not all words have audio — the session must fail instead of silently skipping word2).
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

	// Mock that tracks which MP3 path is being converted and fails consistently on word2
	convertMock := func(mp3Path string, opts ConvertOpts) (PCMData, error) {
		// Fail on word2 (all retries)
		if mp3Path == "/tmp/word2.mp3" {
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
	// Should return ErrTTSGenerationFailed because word2 has no audio
	if err == nil {
		t.Fatal("expected error when not all words have audio, got nil")
	}
	if !errors.Is(err, ErrTTSGenerationFailed) {
		t.Errorf("expected ErrTTSGenerationFailed, got %v", err)
	}

	// Result map should have 2 entries (word1 and word3 were cached before the error)
	if len(result) != 2 {
		t.Errorf("expected 2 results (word1 and word3 cached before error), got %d", len(result))
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
			Paths: []string{"/tmp/apple.mp3", "/tmp/banana.mp3", "/tmp/cherry.mp3"},
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

// Task 2 Tests: Retry-skip logic + error classification + session cleanup helper

// Test 1: retryConvert with convertFn failing ErrFFmpegNotFound on all 3 attempts returns ErrTTSGenerationFailed
// (not all words have audio — the session must fail instead of silently skipping).
func TestRetryConvert_FFmpegNotFound_NoRetry(t *testing.T) {
	exec := &fakeExecutor{
		pregenerateResult: pybridge.PregenResult{
			Paths: []string{"/tmp/word1.mp3"},
		},
	}

	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	// Mock that always returns ErrFFmpegNotFound
	convertMock := func(mp3Path string, opts ConvertOpts) (PCMData, error) {
		return PCMData{}, ErrFFmpegNotFound
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
	words := []string{"word1"}

	result, err := generator.GenerateForSession(ctx, sessionID, words)
	// Should return ErrTTSGenerationFailed because word1 has no audio
	if err == nil {
		t.Fatal("expected error when word has no audio, got nil")
	}
	if !errors.Is(err, ErrTTSGenerationFailed) {
		t.Errorf("expected ErrTTSGenerationFailed, got %v", err)
	}

	// Result should be empty (word was skipped)
	if len(result) != 0 {
		t.Errorf("expected empty result (word skipped), got %d entries", len(result))
	}
}

// Test 2: retryConvert with convertFn failing ErrPCMConversionFailed on first 2 attempts but succeeding on 3rd returns the PCM data successfully
func TestRetryConvert_PCMConversionFailed_RetriesSucceed(t *testing.T) {
	exec := &fakeExecutor{
		pregenerateResult: pybridge.PregenResult{
			Paths: []string{"/tmp/word1.mp3"},
		},
	}

	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	// Mock that fails twice then succeeds
	callCount := 0
	convertMock := func(mp3Path string, opts ConvertOpts) (PCMData, error) {
		callCount++
		if callCount <= 2 {
			return PCMData{}, ErrPCMConversionFailed
		}
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
	words := []string{"word1"}

	result, err := generator.GenerateForSession(ctx, sessionID, words)
	if err != nil {
		t.Fatalf("GenerateForSession failed: %v", err)
	}

	// Should have 1 successful result
	if len(result) != 1 {
		t.Errorf("expected 1 result (after retries), got %d", len(result))
	}

	// Verify the word was cached
	_, err = cache.Load(sessionID, "word1")
	if err != nil {
		t.Errorf("word1 should be cached after successful retries, got error: %v", err)
	}

	// Should have called convertFn 3 times (2 failures + 1 success)
	if callCount != 3 {
		t.Errorf("expected 3 calls (2 retries + 1 success), got %d", callCount)
	}
}

// Test 3: retryConvert with convertFn failing context.DeadlineExceeded on all attempts returns ErrTTSGenerationFailed
// (not all words have audio — the session must fail instead of silently skipping).
func TestRetryConvert_Timeout_SkipsAfterRetries(t *testing.T) {
	exec := &fakeExecutor{
		pregenerateResult: pybridge.PregenResult{
			Paths: []string{"/tmp/word1.mp3"},
		},
	}

	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	// Mock that always times out
	convertMock := func(mp3Path string, opts ConvertOpts) (PCMData, error) {
		return PCMData{}, context.DeadlineExceeded
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
	words := []string{"word1"}

	result, err := generator.GenerateForSession(ctx, sessionID, words)
	// Should return ErrTTSGenerationFailed because word1 has no audio
	if err == nil {
		t.Fatal("expected error when word has no audio, got nil")
	}
	if !errors.Is(err, ErrTTSGenerationFailed) {
		t.Errorf("expected ErrTTSGenerationFailed, got %v", err)
	}

	// Result should be empty (word was skipped after retries)
	if len(result) != 0 {
		t.Errorf("expected empty result (word skipped after retries), got %d entries", len(result))
	}

	// Verify the word was NOT cached
	_, err = cache.Load(sessionID, "word1")
	if err == nil {
		t.Error("word1 should NOT be cached (all retries failed)")
	}
}

// Test 4: GenerateForSession logs an error with word_id and error_type fields for skipped words
func TestRetryConvert_LogsWarningForSkippedWords(t *testing.T) {
	exec := &fakeExecutor{
		pregenerateResult: pybridge.PregenResult{
			Paths: []string{"/tmp/word1.mp3"},
		},
	}

	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	// Mock that always fails
	convertMock := func(mp3Path string, opts ConvertOpts) (PCMData, error) {
		return PCMData{}, ErrPCMConversionFailed
	}

	// Use a logger that captures warnings and errors
	var loggedWarnings []string
	var loggedErrors []string
	logger := &captureLogger{
		warnFunc: func(msg string, fields ...infra.Field) {
			loggedWarnings = append(loggedWarnings, msg)
		},
		errorFunc: func(msg string, fields ...infra.Field) {
			loggedErrors = append(loggedErrors, msg)
		},
	}

	cfg := infra.DefaultConfig().TTS

	generator, err := NewGenerator(exec, cache, logger, cfg)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}

	generator.convertFn = convertMock

	ctx := context.Background()
	sessionID := "test-session"
	words := []string{"word1"}

	_, err = generator.GenerateForSession(ctx, sessionID, words)
	// Should return error because word1 has no audio
	if err == nil {
		t.Fatal("expected error when word has no audio, got nil")
	}

	// Should have logged at least one warning for the skipped word
	if len(loggedWarnings) == 0 {
		t.Error("expected warning log for skipped word, got none")
	}

	// Should have logged at least one error for incomplete generation
	if len(loggedErrors) == 0 {
		t.Error("expected error log for incomplete TTS generation, got none")
	}
}

// Test 5: CleanupSession method on Generator calls cache.Cleanup(sessionID) and returns nil on success
func TestCleanupSession_Success(t *testing.T) {
	exec := &fakeExecutor{}
	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	cfg := infra.DefaultConfig().TTS
	logger := infra.NewNoOpLogger()

	generator, err := NewGenerator(exec, cache, logger, cfg)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}

	ctx := context.Background()
	sessionID := "test-session"

	err = generator.CleanupSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("CleanupSession failed: %v", err)
	}
}

// Test 6: CleanupSession is idempotent (calling twice does not error)
func TestCleanupSession_Idempotent(t *testing.T) {
	exec := &fakeExecutor{}
	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}

	cfg := infra.DefaultConfig().TTS
	logger := infra.NewNoOpLogger()

	generator, err := NewGenerator(exec, cache, logger, cfg)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}

	ctx := context.Background()
	sessionID := "test-session"

	// First cleanup
	err = generator.CleanupSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("first CleanupSession failed: %v", err)
	}

	// Second cleanup (should not error)
	err = generator.CleanupSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("second CleanupSession failed (should be idempotent): %v", err)
	}
}

// captureLogger is a test helper that captures log calls for verification
type captureLogger struct {
	warnFunc  func(msg string, fields ...infra.Field)
	errorFunc func(msg string, fields ...infra.Field)
}

func (l *captureLogger) Debug(msg string, fields ...infra.Field) {
	// Ignore debug logs
}

func (l *captureLogger) Info(msg string, fields ...infra.Field) {
	// Ignore info logs
}

func (l *captureLogger) Warn(msg string, fields ...infra.Field) {
	if l.warnFunc != nil {
		l.warnFunc(msg, fields...)
	}
}

func (l *captureLogger) Error(msg string, fields ...infra.Field) {
	if l.errorFunc != nil {
		l.errorFunc(msg, fields...)
	}
}

func (l *captureLogger) With(fields ...infra.Field) infra.Logger {
	return l
}

func (l *captureLogger) Sync() error {
	return nil
}
