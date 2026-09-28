package mimo

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// skipIntegration is set to false to enable integration tests when MIMO_API_KEY is provided
const skipIntegration = false

// TestMain loads the repo-root .env into this test process's environment
// before any test runs, so `go test` picks up MIMO_API_KEY the same way the
// dev workflow expects. Precedence: real environment always wins — .env only
// fills variables that are unset (D-05: secrets travel via live env; this
// loader never overrides and never logs key material).
func TestMain(m *testing.M) {
	// Go tests run with cwd = package dir (pkg/mimo), so the repo root is ../..
	loadDotEnv("../../.env")
	os.Exit(m.Run())
}

// loadDotEnv reads a minimal KEY=VALUE .env file into os.Environment.
// It tolerates comments (#), blank lines, and surrounding quotes, and skips
// any key that is already set in the environment. Missing file is not an
// error — getTestAPIKey's t.Skip handles the no-key case.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue // live environment takes precedence over .env
		}
		os.Setenv(key, value)
	}
}

// getTestAPIKey retrieves the API key from environment or skips the test
func getTestAPIKey(t *testing.T) string {
	apiKey := os.Getenv("MIMO_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: MIMO_API_KEY not set. Set MIMO_API_KEY environment variable to enable integration tests.")
	}
	return apiKey
}

// getTestBaseURL returns the base URL for testing (default to production)
func getTestBaseURL() string {
	baseURL := os.Getenv("MIMO_BASE_URL")
	if baseURL == "" {
		return "https://api.xiaomimimo.com/v1"
	}
	return baseURL
}

// TestIntegration_Chat_RealAPI tests LLM chat completion with the real API
// (Ticket 03: intent-analysis path). Skipped without MIMO_API_KEY.
func TestIntegration_Chat_RealAPI(t *testing.T) {
	if skipIntegration {
		t.Skip("Skipping integration tests (set MIMO_API_KEY to enable)")
	}

	apiKey := getTestAPIKey(t)
	config := ClientConfig{
		APIKey:     apiKey,
		BaseURL:    getTestBaseURL(),
		Timeout:    60 * time.Second,
		MaxRetries: 2,
		RetryDelay: 1 * time.Millisecond,
	}
	client, err := NewClient(config, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	messages := []Message{
		{
			Role: "system",
			Content: "你是听写课堂的命令识别器。把学生说的话归类为以下动作之一：" +
				"next_word(写完了/好了), repeat(再读一遍), pause(暂停), resume(继续), " +
				"cannot_write(不会写), none(其他)。只输出 JSON：{\"action\":\"...\",\"confidence\":0.0}",
		},
		{Role: "user", Content: "老师我写好了"},
	}

	resp, err := client.Chat(context.Background(), messages, WithMaxTokens(512))
	if err != nil {
		t.Fatalf("Chat() failed: %v", err)
	}

	text := resp.Text()
	if text == "" {
		t.Fatal("expected non-empty chat response text")
	}
	t.Logf("chat response: %s", text)

	// The classifier contract only needs an "action" token somewhere in the
	// response; verify the model followed the JSON instruction loosely.
	if !strings.Contains(text, "action") {
		t.Errorf("response does not contain \"action\": %s", text)
	}
}

// TestIntegration_Synthesize_RealAPI tests TTS synthesis with real API
func TestIntegration_Synthesize_RealAPI(t *testing.T) {
	if skipIntegration {
		t.Skip("Skipping integration tests (set MIMO_API_KEY to enable)")
	}

	apiKey := getTestAPIKey(t)
	baseURL := getTestBaseURL()

	// Create client
	config := ClientConfig{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		Timeout:    60 * time.Second,
		MaxRetries: 3,
		RetryDelay: 1 * time.Millisecond,
	}

	logger := infra.NewNoOpLogger()
	client, err := NewClient(config, logger)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	tests := []struct {
		name           string
		text           string
		opts           []TTSOption
		validateResult func(*testing.T, DecodedAudioData)
		wantErr        bool
	}{
		{
			name: "basic synthesis with default voice",
			text: "你好，世界！",
			opts: []TTSOption{},
			validateResult: func(t *testing.T, result DecodedAudioData) {
				if len(result.Samples) == 0 {
					t.Error("expected non-empty audio samples")
				}
				if result.Format != "pcm16" {
					t.Errorf("expected format 'pcm16', got '%s'", result.Format)
				}
				// pcm16 output is fixed 24kHz per official docs; duration is
				// derived from the byte count client-side
				if result.SampleRate != 24000 {
					t.Errorf("expected sample rate 24000, got %d", result.SampleRate)
				}
				if result.Duration <= 0 {
					t.Errorf("expected positive duration, got %d", result.Duration)
				}
			},
			wantErr: false,
		},
		{
			name: "synthesis with children voice",
			text: "小朋友们好，我们来学习吧！",
			opts: []TTSOption{
				WithVoice("冰糖"),
				WithStyleInstruction("Use a gentle, encouraging tone suitable for elementary students."),
			},
			validateResult: func(t *testing.T, result DecodedAudioData) {
				if len(result.Samples) == 0 {
					t.Error("expected non-empty audio samples")
				}
				if result.Duration <= 0 {
					t.Errorf("expected positive duration, got %d", result.Duration)
				}
			},
			wantErr: false,
		},
		{
			name: "synthesis with different voice",
			text: "This is a test with English voice.",
			opts: []TTSOption{
				WithVoice("Mia"),
				WithAudioFormat("wav"),
			},
			validateResult: func(t *testing.T, result DecodedAudioData) {
				if len(result.Samples) == 0 {
					t.Error("expected non-empty audio samples")
				}
				if result.Format != "wav" {
					t.Errorf("expected format 'wav', got '%s'", result.Format)
				}
			},
			wantErr: false,
		},
		{
			name: "synthesis with custom sample rate",
			text: "Custom sample rate test.",
			opts: []TTSOption{
				WithSampleRate(24000),
			},
			validateResult: func(t *testing.T, result DecodedAudioData) {
				if result.SampleRate != 24000 {
					t.Errorf("expected sample rate 24000, got %d", result.SampleRate)
				}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := client.Synthesize(ctx, tt.text, tt.opts...)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if tt.validateResult != nil {
				tt.validateResult(t, result)
			}

			t.Logf("Synthesis successful: %d bytes, format=%s, sample_rate=%d, duration=%dms",
				len(result.Samples), result.Format, result.SampleRate, result.Duration)
		})
	}
}

// TestIntegration_SynthesizeStream_RealAPI tests streaming TTS with real API
func TestIntegration_SynthesizeStream_RealAPI(t *testing.T) {
	if skipIntegration {
		t.Skip("Skipping integration tests (set MIMO_API_KEY to enable)")
	}

	apiKey := getTestAPIKey(t)
	baseURL := getTestBaseURL()

	// Create client
	config := ClientConfig{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		Timeout:    60 * time.Second,
		MaxRetries: 3,
		RetryDelay: 2 * time.Second,
	}

	logger := infra.NewNoOpLogger()
	client, err := NewClient(config, logger)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	text := "这是一个流式测试，同学们请注意听讲！"
	chunkCount := 0

	callback := func(chunk StreamChunk) error {
		chunkCount++
		_ = chunk.Duration // Acknowledge duration is being tracked

		if chunk.AudioData != "" {
			t.Logf("Received chunk %d: %d bytes, cumulative_duration=%dms, is_final=%v",
				chunkCount, len(chunk.AudioData), chunk.Duration, chunk.IsFinal)
		}

		return nil
	}

	result, err := client.SynthesizeStream(ctx, text, callback,
		WithVoice("冰糖"),
		WithStyleInstruction("Use a clear, teacher-like tone suitable for classroom instruction."),
	)

	if err != nil {
		t.Errorf("unexpected streaming error: %v", err)
		return
	}

	if chunkCount == 0 {
		t.Error("expected at least one chunk, got zero")
	}

	if result == nil {
		t.Error("expected stream result, got nil")
		return
	}

	if result.ChunkCount != chunkCount {
		t.Errorf("expected chunk count %d, got %d", chunkCount, result.ChunkCount)
	}

	if result.TotalDuration <= 0 {
		t.Errorf("expected positive total duration, got %d", result.TotalDuration)
	}

	t.Logf("Streaming completed: %d chunks, total_duration=%dms",
		result.ChunkCount, result.TotalDuration)
}

// ttsRoundTripAudio synthesizes text via the real TTS API and returns WAV
// bytes suitable for feeding straight back into ASR. The official ASR only
// accepts wav/mp3, so the dictation round-trip is tested with real audio
// rather than synthetic bytes (which the server rejects with 400).
func ttsRoundTripAudio(t *testing.T, client Client, text string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	audio, err := client.Synthesize(ctx, text,
		WithVoice("冰糖"),
		WithAudioFormat("wav"),
	)
	if err != nil {
		t.Fatalf("ttsRoundTripAudio: synthesis failed: %v", err)
	}
	if len(audio.Samples) == 0 {
		t.Fatal("ttsRoundTripAudio: synthesis returned empty audio")
	}
	return audio.Samples
}

// TestIntegration_Transcribe_RealAPI tests ASR transcription with real API
func TestIntegration_Transcribe_RealAPI(t *testing.T) {
	if skipIntegration {
		t.Skip("Skipping integration tests (set MIMO_API_KEY to enable)")
	}

	apiKey := getTestAPIKey(t)
	baseURL := getTestBaseURL()

	// Create client
	config := ClientConfig{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		Timeout:    60 * time.Second,
		MaxRetries: 3,
		RetryDelay: 2 * time.Second,
	}

	logger := infra.NewNoOpLogger()
	client, err := NewClient(config, logger)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	tests := []struct {
		name           string
		audioData      []byte
		format         string
		opts           []ASROption
		validateResult func(*testing.T, ASRResponse)
		wantErr        bool
	}{
		{
			name:      "Chinese transcription round-trip from TTS",
			audioData: ttsRoundTripAudio(t, client, "小朋友们好，我们来听写吧"),
			format:    "wav",
			opts: []ASROption{
				WithASRLanguage("zh"),
			},
			validateResult: func(t *testing.T, result ASRResponse) {
				text := result.ExtractText()
				if text == "" {
					t.Error("expected non-empty transcription text")
				}
				if len(result.Choices) == 0 {
					t.Error("expected at least one choice in response")
				}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := client.Transcribe(ctx, tt.audioData, tt.format, tt.opts...)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if tt.validateResult != nil {
				tt.validateResult(t, result)
			}

			t.Logf("Transcription successful: text='%s', choices=%d",
				result.ExtractText(), len(result.Choices))

			if len(result.Choices) > 0 {
				t.Logf("Response structure: role='%s', content_length=%d",
					result.Choices[0].Message.Role,
					len(result.Choices[0].Message.Content))
			}
		})
	}
}

// TestIntegration_HealthCheck_RealAPI tests API health check with real API
func TestIntegration_HealthCheck_RealAPI(t *testing.T) {
	if skipIntegration {
		t.Skip("Skipping integration tests (set MIMO_API_KEY to enable)")
	}

	apiKey := getTestAPIKey(t)
	baseURL := getTestBaseURL()

	// Create client
	config := ClientConfig{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		Timeout:    10 * time.Second,
		MaxRetries: 1,
		RetryDelay: 1 * time.Second,
	}

	logger := infra.NewNoOpLogger()
	client, err := NewClient(config, logger)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = client.HealthCheck(ctx)
	if err != nil {
		t.Errorf("health check failed: %v", err)
	}

	t.Log("API health check passed")
}

// TestIntegration_EndToEnd_RealAPI tests complete dictation workflow
func TestIntegration_EndToEnd_RealAPI(t *testing.T) {
	if skipIntegration {
		t.Skip("Skipping integration tests (set MIMO_API_KEY to enable)")
	}

	apiKey := getTestAPIKey(t)
	baseURL := getTestBaseURL()

	// Create client
	config := ClientConfig{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		Timeout:    60 * time.Second,
		MaxRetries: 3,
		RetryDelay: 2 * time.Second,
	}

	logger := infra.NewNoOpLogger()
	client, err := NewClient(config, logger)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	t.Log("=== End-to-End Dictation Workflow Test ===")

	ctx := context.Background()

	// Step 1: Teacher speaks the word (TTS, wav so ASR can consume it)
	t.Log("Step 1: Generating teacher audio for word 'apple'")
	teacherAudio, err := client.Synthesize(ctx, "Please spell the word: apple",
		WithVoice("茉莉"),
		WithAudioFormat("wav"),
		WithStyleInstruction("Use a clear, encouraging teacher voice."),
	)
	if err != nil {
		t.Errorf("failed to generate teacher audio: %v", err)
		return
	}
	t.Logf("Generated teacher audio: %d bytes, duration=%dms", len(teacherAudio.Samples), teacherAudio.Duration)

	// Step 2: Student listens and speaks the word back. Simulated by feeding
	// the teacher's own audio to ASR — real wav bytes, real recognition.
	t.Log("Step 2: Transcribing student speech (TTS→ASR round-trip)")
	transcription, err := client.Transcribe(ctx, teacherAudio.Samples, "wav",
		WithASRLanguage("en"),
	)
	if err != nil {
		t.Errorf("failed to transcribe student speech: %v", err)
		return
	}
	t.Logf("Student transcription: '%s' (choices: %d)", transcription.ExtractText(), len(transcription.Choices))

	// Step 4: Generate feedback based on transcription
	t.Log("Step 4: Generating teacher feedback")
	var feedback string
	text := transcription.ExtractText()
	if strings.Contains(strings.ToLower(text), "apple") {
		feedback = "Great job! You spelled the word correctly!"
	} else {
		feedback = "Let's try again. Listen carefully to the word: apple."
	}

	// Step 5: Generate feedback audio
	t.Log("Step 5: Generating feedback audio")
	feedbackAudio, err := client.Synthesize(ctx, feedback,
		WithVoice("冰糖"),
		WithStyleInstruction("Use a supportive, encouraging tone suitable for young learners."),
	)
	if err != nil {
		t.Errorf("failed to generate feedback audio: %v", err)
		return
	}
	t.Logf("Generated feedback audio: %d bytes, duration=%dms", len(feedbackAudio.Samples), feedbackAudio.Duration)

	t.Log("=== End-to-End Workflow Test Completed Successfully ===")
}

// TestIntegration_ConcurrentRequests_RealAPI tests concurrent request handling
func TestIntegration_ConcurrentRequests_RealAPI(t *testing.T) {
	if skipIntegration {
		t.Skip("Skipping integration tests (set MIMO_API_KEY to enable)")
	}

	apiKey := getTestAPIKey(t)
	baseURL := getTestBaseURL()

	// Create client
	config := ClientConfig{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		Timeout:    60 * time.Second,
		MaxRetries: 3,
		RetryDelay: 2 * time.Second,
	}

	logger := infra.NewNoOpLogger()
	client, err := NewClient(config, logger)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	// Test with 5 concurrent requests
	concurrency := 5
	words := []string{"apple", "banana", "orange", "grape", "mango"}

	errChan := make(chan error, concurrency)
	resultChan := make(chan string, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(index int, word string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			audio, err := client.Synthesize(ctx, fmt.Sprintf("Word %d: %s", index+1, word),
				WithVoice("冰糖"),
			)

			if err != nil {
				errChan <- fmt.Errorf("request %d failed: %v", index, err)
				return
			}

			resultChan <- fmt.Sprintf("request %d: %d bytes, duration=%dms",
				index, len(audio.Samples), audio.Duration)
		}(i, words[i])
	}

	// Collect results
	successCount := 0
	errorCount := 0

	for i := 0; i < concurrency; i++ {
		select {
		case result := <-resultChan:
			successCount++
			t.Logf("Concurrent request succeeded: %s", result)
		case err := <-errChan:
			errorCount++
			t.Logf("Concurrent request failed: %v", err)
		case <-time.After(45 * time.Second):
			t.Fatalf("concurrent test timeout after 45 seconds")
		}
	}

	t.Logf("Concurrent requests completed: %d succeeded, %d failed", successCount, errorCount)

	if successCount == 0 {
		t.Error("all concurrent requests failed")
	}
}

// TestIntegration_ErrorHandling_RealAPI tests error scenarios with real API
func TestIntegration_ErrorHandling_RealAPI(t *testing.T) {
	if skipIntegration {
		t.Skip("Skipping integration tests (set MIMO_API_KEY to enable)")
	}

	baseURL := getTestBaseURL()

	tests := []struct {
		name    string
		config  ClientConfig
		wantErr error
	}{
		{
			name: "invalid API key",
			config: ClientConfig{
				APIKey:  "invalid_key_test_12345",
				BaseURL: baseURL,
				Timeout: 10 * time.Second,
			},
			wantErr: ErrAuthentication,
		},
		{
			name: "empty audio data",
			config: ClientConfig{
				APIKey:  getTestAPIKey(t),
				BaseURL: baseURL,
				Timeout: 10 * time.Second,
			},
			wantErr: ErrAudioTooShort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "empty audio data" {
				// This test needs a valid client
				config := ClientConfig{
					APIKey:  getTestAPIKey(t),
					BaseURL: baseURL,
					Timeout: 10 * time.Second,
				}
				logger := infra.NewNoOpLogger()
				client, err := NewClient(config, logger)
				if err != nil {
					t.Fatalf("failed to create client: %v", err)
				}
				defer client.Close()

				ctx := context.Background()
				_, err = client.Transcribe(ctx, []byte{}, "pcm16")
				if err == nil {
					t.Error("expected error for empty audio, got nil")
				} else if !errors.Is(err, ErrAudioTooShort) {
					t.Errorf("expected ErrAudioTooShort, got %v", err)
				}
				return
			}

			// Test invalid API key
			logger := infra.NewNoOpLogger()
			client, err := NewClient(tt.config, logger)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			defer client.Close()

			ctx := context.Background()
			_, err = client.Synthesize(ctx, "test")
			if err == nil {
				t.Error("expected error for invalid API key, got nil")
			} else if !errors.Is(err, ErrAuthentication) && !IsRetryable(err) {
				t.Errorf("expected authentication error, got %v", err)
			}
		})
	}
}
