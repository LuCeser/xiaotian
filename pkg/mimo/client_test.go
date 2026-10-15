package mimo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// testLogger is a simple logger for testing
type testLogger struct {
	logs []map[string]interface{}
}

func newTestLogger() *testLogger {
	return &testLogger{logs: make([]map[string]interface{}, 0)}
}

func (l *testLogger) Debug(msg string, fields ...infra.Field) {
	l.logs = append(l.logs, map[string]interface{}{"level": "debug", "msg": msg, "fields": fields})
}

func (l *testLogger) Info(msg string, fields ...infra.Field) {
	l.logs = append(l.logs, map[string]interface{}{"level": "info", "msg": msg, "fields": fields})
}

func (l *testLogger) Warn(msg string, fields ...infra.Field) {
	l.logs = append(l.logs, map[string]interface{}{"level": "warn", "msg": msg, "fields": fields})
}

func (l *testLogger) Error(msg string, fields ...infra.Field) {
	l.logs = append(l.logs, map[string]interface{}{"level": "error", "msg": msg, "fields": fields})
}

func (l *testLogger) Sync() error {
	return nil
}

func (l *testLogger) With(fields ...infra.Field) infra.Logger {
	return l // Simply return self for testing purposes
}

// mockTTSServer creates a mock TTS server for testing
func mockTTSServer(handler http.HandlerFunc) (*httptest.Server, func()) {
	server := httptest.NewServer(handler)
	cleanup := func() { server.Close() }
	return server, cleanup
}

// defaultTTSHandler provides a successful TTS response
func defaultTTSHandler(w http.ResponseWriter, r *http.Request) {
	// Verify authentication
	auth := r.Header.Get("Authorization")
	if auth != "Bearer test-api-key" {
		w.WriteHeader(http.StatusUnauthorized)
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "authentication_failed",
				"message": "Invalid API key",
				"type":    "authentication_error",
			},
		}); err != nil {
			return
		}
		return
	}

	// Verify content type
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Parse request
	var req TTSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Generate mock audio data — a realistic-size PCM16 payload so the
	// client-derived duration math (bytes/2/24kHz) produces non-zero values
	mockAudio := bytes.Repeat([]byte{0x00, 0x01}, 24000) // 1s @ 24kHz 16-bit mono
	audioData := base64.StdEncoding.EncodeToString(mockAudio)

	response := TTSResponse{
		Choices: []Choice{
			{
				Message: AudioMessage{
					Role: "assistant",
					Audio: AudioData{
						Data: audioData,
					},
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}

// defaultASRHandler provides a successful ASR response
func defaultASRHandler(w http.ResponseWriter, r *http.Request) {
	// Verify authentication
	auth := r.Header.Get("Authorization")
	if auth != "Bearer test-api-key" {
		w.WriteHeader(http.StatusUnauthorized)
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "authentication_failed",
				"message": "Invalid API key",
				"type":    "authentication_error",
			},
		}); err != nil {
			return
		}
		return
	}

	// Parse request
	var req ASRRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	response := ASRResponse{
		Choices: []ASRChoice{
			{
				Message: ASRMessageContent{
					Role:    "assistant",
					Content: "这是测试语音识别结果",
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}

// rateLimitHandler simulates rate limiting
func rateLimitHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(http.StatusTooManyRequests)
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    "rate_limit_exceeded",
			"message": "Rate limit exceeded",
			"type":    "rate_limit_error",
		},
	}); err != nil {
		return
	}
}

// serviceUnavailableHandler simulates service outage
func serviceUnavailableHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusServiceUnavailable)
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    "service_unavailable",
			"message": "Service temporarily unavailable",
			"type":    "service_unavailable",
		},
	}); err != nil {
		return
	}
}

// TestNewClient tests client creation
func TestNewClient(t *testing.T) {
	tests := []struct {
		name    string
		config  ClientConfig
		logger  infra.Logger
		wantErr error
	}{
		{
			name: "valid config",
			config: ClientConfig{
				APIKey: "test-api-key",
			},
			logger:  newTestLogger(),
			wantErr: nil,
		},
		{
			name: "missing API key",
			config: ClientConfig{
				APIKey: "",
			},
			logger:  newTestLogger(),
			wantErr: ErrAPIKeyRequired,
		},
		{
			name: "with custom base URL",
			config: ClientConfig{
				APIKey:  "test-api-key",
				BaseURL: "https://custom.api.com",
			},
			logger:  newTestLogger(),
			wantErr: nil,
		},
		{
			name: "with custom timeout",
			config: ClientConfig{
				APIKey:  "test-api-key",
				Timeout: 60 * time.Second,
			},
			logger:  newTestLogger(),
			wantErr: nil,
		},
		{
			name: "with retry config",
			config: ClientConfig{
				APIKey:     "test-api-key",
				MaxRetries: 5,
				RetryDelay: 1 * time.Millisecond,
			},
			logger:  newTestLogger(),
			wantErr: nil,
		},
		{
			name: "nil logger uses noop",
			config: ClientConfig{
				APIKey: "test-api-key",
			},
			logger:  nil,
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.config, tt.logger)

			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("NewClient() expected error %v, got nil", tt.wantErr)
				} else if !errors.Is(err, tt.wantErr) {
					t.Errorf("NewClient() expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Errorf("NewClient() unexpected error: %v", err)
				return
			}

			if client == nil {
				t.Error("NewClient() returned nil client")
			}
		})
	}
}

// TestClient_Synthesize tests TTS synthesis
func TestClient_Synthesize(t *testing.T) {
	tests := []struct {
		name           string
		handler        http.HandlerFunc
		text           string
		opts           []TTSOption
		wantErr        error
		validateResult func(*testing.T, DecodedAudioData)
	}{
		{
			name:    "successful synthesis",
			handler: defaultTTSHandler,
			text:    "Hello, world!",
			opts:    []TTSOption{},
			wantErr: nil,
			validateResult: func(t *testing.T, result DecodedAudioData) {
				if len(result.Samples) == 0 {
					t.Error("expected non-empty audio samples")
				}
				if result.Format != "pcm16" {
					t.Errorf("expected format 'pcm16', got '%s'", result.Format)
				}
				// pcm16 output is fixed 24kHz 16-bit mono per official docs;
				// the mock payload is 48000 bytes = 1s of audio
				if result.SampleRate != 24000 {
					t.Errorf("expected sample rate 24000, got %d", result.SampleRate)
				}
				if result.Duration != 1000 {
					t.Errorf("expected duration 1000ms, got %d", result.Duration)
				}
			},
		},
		{
			name:    "with custom voice",
			handler: defaultTTSHandler,
			text:    "Test with voice",
			opts:    []TTSOption{WithVoice("茉莉")},
			wantErr: nil,
		},
		{
			name:    "with style instruction",
			handler: defaultTTSHandler,
			text:    "Be encouraging",
			opts:    []TTSOption{WithStyleInstruction("Use a gentle, encouraging tone suitable for elementary students.")},
			wantErr: nil,
		},
		{
			name:    "authentication failure",
			handler: defaultTTSHandler,
			text:    "Test auth",
			opts:    []TTSOption{},
			wantErr: ErrAuthentication,
		},
		{
			name:    "rate limit with retry",
			handler: rateLimitHandler,
			text:    "Test rate limit",
			opts:    []TTSOption{},
			wantErr: ErrRateLimit,
		},
		{
			name:    "service unavailable",
			handler: serviceUnavailableHandler,
			text:    "Test service unavailable",
			opts:    []TTSOption{},
			wantErr: ErrServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			server, cleanup := mockTTSServer(tt.handler)
			defer cleanup()

			// Create client with test server URL
			config := ClientConfig{
				APIKey:     "test-api-key",
				BaseURL:    server.URL,
				Timeout:    5 * time.Second,
				MaxRetries: 2,
				RetryDelay: 100 * time.Millisecond,
			}

			// Override API key for auth failure test
			if tt.name == "authentication failure" {
				config.APIKey = "invalid-api-key"
			}

			logger := newTestLogger()
			client, err := NewClient(config, logger)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			defer client.Close()

			// Execute synthesis
			ctx := context.Background()
			result, err := client.Synthesize(ctx, tt.text, tt.opts...)

			// Validate error
			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("Synthesize() expected error %v, got nil", tt.wantErr)
				} else if !errors.Is(err, tt.wantErr) && !IsRetryable(err) {
					t.Errorf("Synthesize() expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Errorf("Synthesize() unexpected error: %v", err)
				return
			}

			// Validate result
			if tt.validateResult != nil {
				tt.validateResult(t, result)
			}
		})
	}
}

// TestClient_Transcribe tests ASR transcription
func TestClient_Transcribe(t *testing.T) {
	tests := []struct {
		name           string
		handler        http.HandlerFunc
		audioData      []byte
		format         string
		opts           []ASROption
		wantErr        error
		validateResult func(*testing.T, ASRResponse)
	}{
		{
			name:      "successful transcription",
			handler:   defaultASRHandler,
			audioData: []byte("mock_audio_samples"),
			format:    "pcm16",
			opts:      []ASROption{},
			wantErr:   nil,
			validateResult: func(t *testing.T, result ASRResponse) {
				if result.ExtractText() != "这是测试语音识别结果" {
					t.Errorf("expected text '这是测试语音识别结果', got '%s'", result.ExtractText())
				}
				if result.Choices[0].Message.Role != "assistant" {
					t.Errorf("expected role 'assistant', got '%s'", result.Choices[0].Message.Role)
				}
			},
		},
		{
			name:      "with language option",
			handler:   defaultASRHandler,
			audioData: []byte("mock_audio"),
			format:    "wav",
			opts:      []ASROption{WithASRLanguage("en")},
			wantErr:   nil,
		},
		{
			name:      "empty audio data",
			handler:   defaultASRHandler,
			audioData: []byte{},
			format:    "pcm16",
			opts:      []ASROption{},
			wantErr:   ErrAudioTooShort,
		},
		{
			name:      "authentication failure",
			handler:   defaultASRHandler,
			audioData: []byte("mock_audio"),
			format:    "pcm16",
			opts:      []ASROption{},
			wantErr:   ErrAuthentication,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			server, cleanup := mockTTSServer(tt.handler)
			defer cleanup()

			// Create client with test server URL
			config := ClientConfig{
				APIKey:     "test-api-key",
				BaseURL:    server.URL,
				Timeout:    5 * time.Second,
				MaxRetries: 2,
				RetryDelay: 100 * time.Millisecond,
			}

			// Override API key for auth failure test
			if tt.name == "authentication failure" {
				config.APIKey = "invalid-api-key"
			}

			logger := newTestLogger()
			client, err := NewClient(config, logger)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			defer client.Close()

			// Execute transcription
			ctx := context.Background()
			result, err := client.Transcribe(ctx, tt.audioData, tt.format, tt.opts...)

			// Validate error
			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("Transcribe() expected error %v, got nil", tt.wantErr)
				} else if !errors.Is(err, tt.wantErr) && !IsRetryable(err) {
					t.Errorf("Transcribe() expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Errorf("Transcribe() unexpected error: %v", err)
				return
			}

			// Validate result
			if tt.validateResult != nil {
				tt.validateResult(t, result)
			}
		})
	}
}

// TestClient_TranscribeEmptyChoices verifies the sentinel split for
// well-formed-but-empty ASR answers: an HTTP 200 response whose JSON decodes
// successfully but carries an empty choices array is the provider's
// legitimate "no speech" answer and must surface as ErrEmptyTranscription —
// NOT as ErrInvalidResponse (which is reserved for corrupted/unparsable
// responses).
func TestClient_TranscribeEmptyChoices(t *testing.T) {
	emptyChoicesHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		response := ASRResponse{Choices: []ASRChoice{}}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			return
		}
	}

	server, cleanup := mockTTSServer(emptyChoicesHandler)
	defer cleanup()

	config := ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 2,
		RetryDelay: 100 * time.Millisecond,
	}

	logger := newTestLogger()
	client, err := NewClient(config, logger)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	_, err = client.Transcribe(context.Background(), []byte("mock_audio"), "pcm16")
	if err == nil {
		t.Fatal("Transcribe() expected error for empty choices, got nil")
	}
	if !errors.Is(err, ErrEmptyTranscription) {
		t.Errorf("Transcribe() error = %v, want errors.Is(err, ErrEmptyTranscription)", err)
	}
	if errors.Is(err, ErrInvalidResponse) {
		t.Errorf("Transcribe() error = %v, empty choices must NOT match ErrInvalidResponse", err)
	}
}

// TestClient_TranscribeInvalidJSON verifies the complementary branch of the
// sentinel split: an HTTP 200 response whose body fails to decode is a
// corrupted response and must surface as ErrInvalidResponse — NOT as
// ErrEmptyTranscription.
func TestClient_TranscribeInvalidJSON(t *testing.T) {
	invalidJSONHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [not valid json`))
	}

	server, cleanup := mockTTSServer(invalidJSONHandler)
	defer cleanup()

	config := ClientConfig{
		APIKey:     "test-api-key",
		BaseURL:    server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 2,
		RetryDelay: 100 * time.Millisecond,
	}

	logger := newTestLogger()
	client, err := NewClient(config, logger)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	_, err = client.Transcribe(context.Background(), []byte("mock_audio"), "pcm16")
	if err == nil {
		t.Fatal("Transcribe() expected error for invalid JSON, got nil")
	}
	if !errors.Is(err, ErrInvalidResponse) {
		t.Errorf("Transcribe() error = %v, want errors.Is(err, ErrInvalidResponse)", err)
	}
	if errors.Is(err, ErrEmptyTranscription) {
		t.Errorf("Transcribe() error = %v, invalid JSON must NOT match ErrEmptyTranscription", err)
	}
}

// TestClient_HealthCheck tests API health check
func TestClient_HealthCheck(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr error
	}{
		{
			name:    "healthy service",
			handler: defaultTTSHandler,
			wantErr: nil,
		},
		{
			name:    "service unavailable",
			handler: serviceUnavailableHandler,
			wantErr: ErrServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			server, cleanup := mockTTSServer(tt.handler)
			defer cleanup()

			// Create client
			config := ClientConfig{
				APIKey:  "test-api-key",
				BaseURL: server.URL,
				Timeout: 5 * time.Second,
			}

			logger := newTestLogger()
			client, err := NewClient(config, logger)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			defer client.Close()

			// Execute health check
			ctx := context.Background()
			err = client.HealthCheck(ctx)

			// Validate error
			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("HealthCheck() expected error %v, got nil", tt.wantErr)
				} else if !errors.Is(err, tt.wantErr) {
					t.Errorf("HealthCheck() expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Errorf("HealthCheck() unexpected error: %v", err)
			}
		})
	}
}

// TestClient_Close tests client cleanup
func TestClient_Close(t *testing.T) {
	config := ClientConfig{
		APIKey: "test-api-key",
	}

	logger := newTestLogger()
	client, err := NewClient(config, logger)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Close client
	err = client.Close()
	if err != nil {
		t.Errorf("Close() unexpected error: %v", err)
	}

	// Try to use closed client
	ctx := context.Background()
	_, err = client.Synthesize(ctx, "test", WithVoice("冰糖"))
	if err == nil {
		t.Error("expected error when using closed client, got nil")
	}

	// Double close should be safe
	err = client.Close()
	if err != nil {
		t.Errorf("second Close() unexpected error: %v", err)
	}
}

// TestTTSOptions tests TTS option functions
func TestTTSOptions(t *testing.T) {
	req := TTSRequest{
		Messages: []Message{{Role: "assistant", Content: "test"}},
		Audio:    AudioConfig{Format: "pcm16", Voice: "default"},
	}

	// Test WithVoice
	WithVoice("茉莉")(&req)
	if req.Audio.Voice != "茉莉" {
		t.Errorf("WithVoice() expected voice '茉莉', got '%s'", req.Audio.Voice)
	}

	// Test WithAudioFormat
	WithAudioFormat("wav")(&req)
	if req.Audio.Format != "wav" {
		t.Errorf("WithAudioFormat() expected format 'wav', got '%s'", req.Audio.Format)
	}

	// Test WithSampleRate
	WithSampleRate(24000)(&req)
	if req.Audio.SampleRate != 24000 {
		t.Errorf("WithSampleRate() expected sample rate 24000, got %d", req.Audio.SampleRate)
	}

	// Test WithTTSStream
	WithTTSStream()(&req)
	if !req.Stream {
		t.Error("WithTTSStream() expected stream=true, got false")
	}

	// Test WithStyleInstruction
	initialMsgCount := len(req.Messages)
	WithStyleInstruction("Be encouraging")(&req)
	if len(req.Messages) != initialMsgCount+1 {
		t.Errorf("WithStyleInstruction() expected message count %d, got %d", initialMsgCount+1, len(req.Messages))
	}
	if req.Messages[0].Content != "Be encouraging" {
		t.Errorf("WithStyleInstruction() expected instruction 'Be encouraging', got '%s'", req.Messages[0].Content)
	}
}

// TestASROptions tests ASR option functions
func TestASROptions(t *testing.T) {
	req := ASRRequest{
		ASROptions: ASROptions{},
	}

	// Test WithASRLanguage
	WithASRLanguage("en")(&req)
	if req.ASROptions.Language != "en" {
		t.Errorf("WithASRLanguage() expected language 'en', got '%s'", req.ASROptions.Language)
	}

	// Switching language replaces the previous value
	WithASRLanguage("zh")(&req)
	if req.ASROptions.Language != "zh" {
		t.Errorf("WithASRLanguage() expected language 'zh', got '%s'", req.ASROptions.Language)
	}
}

// TestIsRetryable tests retryable error detection
func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"rate limit error", ErrRateLimit, true},
		{"service unavailable", ErrServiceUnavailable, true},
		{"network error", ErrNetworkError, true},
		{"retryable error wrapper", NewRetryableError(errors.New("temp error"), 5), true},
		{"authentication error", ErrAuthentication, false},
		{"invalid request", ErrInvalidRequest, false},
		{"context cancelled", ErrContextCancelled, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsRetryable(tt.err)
			if result != tt.expected {
				t.Errorf("IsRetryable() for %v expected %v, got %v", tt.err, tt.expected, result)
			}
		})
	}
}

// TestGetRetryDelay tests retry delay extraction
func TestGetRetryDelay(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected int
	}{
		{"retryable error with delay", NewRetryableError(errors.New("temp"), 10), 10},
		{"retryable error with zero delay", NewRetryableError(errors.New("temp"), 0), 0},
		{"non-retryable error", ErrInvalidRequest, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetRetryDelay(tt.err)
			if result != tt.expected {
				t.Errorf("GetRetryDelay() for %v expected %d, got %d", tt.err, tt.expected, result)
			}
		})
	}
}
