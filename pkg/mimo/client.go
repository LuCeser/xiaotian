package mimo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Client implements the Mimo API client with OpenAI-compatible interface
type mimoClient struct {
	logger     infra.Logger
	httpClient *http.Client
	config     ClientConfig
	mu         sync.RWMutex
	closed     bool
}

// NewClient creates a new Mimo API client with the given configuration
func NewClient(config ClientConfig, logger infra.Logger) (Client, error) {
	if config.APIKey == "" {
		return nil, errors.Wrap(ErrAPIKeyRequired, "API key must be provided")
	}

	// Set defaults
	if config.BaseURL == "" {
		config.BaseURL = "https://api.xiaomimimo.com/v1"
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}
	if config.RetryDelay == 0 {
		config.RetryDelay = 1 * time.Second
	}

	if logger == nil {
		logger = infra.NewNoOpLogger()
	}

	return &mimoClient{
		config: config,
		httpClient: &http.Client{
			Timeout: config.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				IdleConnTimeout:     90 * time.Second,
				DisableKeepAlives:   false,
				MaxConnsPerHost:     20, // Respect 1C1G VPS limits
				MaxIdleConnsPerHost: 5,
			},
		},
		logger: logger,
		closed: false,
	}, nil
}

// Synthesize generates audio from text input
func (c *mimoClient) Synthesize(ctx context.Context, text string, opts ...TTSOption) (DecodedAudioData, error) {
	if c.isClosed() {
		return DecodedAudioData{}, errors.New("client is closed")
	}

	// Build request
	req := c.buildTTSRequest(text, opts...)

	// Execute request with retry
	var lastErr error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			c.logger.Debug("TTS synthesis retry attempt",
				infra.F("component", "mimo.client"),
				infra.F("attempt", attempt),
				infra.F("max_retries", c.config.MaxRetries),
				infra.ErrField(lastErr),
			)

			// Exponential backoff
			delay := c.config.RetryDelay * time.Duration(1<<uint(attempt-1))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return DecodedAudioData{}, errors.Wrap(ErrContextCancelled, "TTS retry cancelled")
			}
		}

		result, err := c.executeTTSRequest(ctx, req)
		if err == nil {
			return result, nil
		}

		lastErr = err

		// Don't retry non-retryable errors
		if !IsRetryable(err) {
			c.logger.Warn("TTS synthesis failed with non-retryable error",
				infra.F("component", "mimo.client"),
				infra.F("text_length", len(text)),
				infra.ErrField(err),
			)
			return DecodedAudioData{}, err
		}

		c.logger.Debug("TTS synthesis attempt failed, will retry",
			infra.F("component", "mimo.client"),
			infra.F("attempt", attempt),
			infra.ErrField(err),
		)
	}

	return DecodedAudioData{}, errors.Wrap(lastErr, "TTS synthesis exhausted retries")
}

// SynthesizeStream generates audio with streaming callback
func (c *mimoClient) SynthesizeStream(ctx context.Context, text string, callback StreamCallback, opts ...TTSOption) (*StreamResult, error) {
	if c.isClosed() {
		return nil, errors.New("client is closed")
	}

	// Build request with streaming enabled
	req := c.buildTTSRequest(text, append(opts, WithTTSStream())...)

	// Execute streaming request. Cumulative duration is tracked here from
	// decoded byte counts — the wire chunks carry no timing information.
	result := &StreamResult{}
	var totalBytes int

	err := c.executeTTSStreamRequest(ctx, req, func(chunk StreamChunk) error {
		totalBytes += base64.StdEncoding.DecodedLen(len(chunk.AudioData))
		chunk.Duration = totalBytes * 1000 / (2 * 24000) // 24kHz PCM16LE mono per docs

		if chunk.IsFinal {
			result.TotalDuration = chunk.Duration
			return nil
		}

		// ChunkCount tracks chunks delivered to the caller's callback; the
		// finish_reason terminator is protocol bookkeeping, not a data chunk
		result.ChunkCount++

		if callback != nil {
			if err := callback(chunk); err != nil {
				return errors.Wrap(err, "stream callback failed")
			}
		}

		return nil
	})

	// Streams that end without an explicit finish_reason chunk ([DONE] or
	// plain EOF) still deserve an accurate total duration
	if err == nil && result.TotalDuration == 0 {
		result.TotalDuration = totalBytes * 1000 / (2 * 24000)
	}

	if err != nil {
		result.Error = err
		return result, errors.Wrap(err, "TTS streaming failed")
	}

	return result, nil
}

// Transcribe converts audio to text
func (c *mimoClient) Transcribe(ctx context.Context, audio []byte, format string, opts ...ASROption) (ASRResponse, error) {
	if c.isClosed() {
		return ASRResponse{}, errors.New("client is closed")
	}

	// Validate audio
	if len(audio) == 0 {
		return ASRResponse{}, errors.Wrap(ErrAudioTooShort, "audio data is empty")
	}

	// Build request
	req := c.buildASRRequest(audio, format, opts...)

	// Execute request with retry
	var lastErr error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			c.logger.Debug("ASR transcription retry attempt",
				infra.F("component", "mimo.client"),
				infra.F("attempt", attempt),
				infra.F("max_retries", c.config.MaxRetries),
				infra.ErrField(lastErr),
			)

			// Exponential backoff
			delay := c.config.RetryDelay * time.Duration(1<<uint(attempt-1))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ASRResponse{}, errors.Wrap(ErrContextCancelled, "ASR retry cancelled")
			}
		}

		result, err := c.executeASRRequest(ctx, req)
		if err == nil {
			return result, nil
		}

		lastErr = err

		// Don't retry non-retryable errors
		if !IsRetryable(err) {
			c.logger.Warn("ASR transcription failed with non-retryable error",
				infra.F("component", "mimo.client"),
				infra.F("audio_size", len(audio)),
				infra.ErrField(err),
			)
			return ASRResponse{}, err
		}

		c.logger.Debug("ASR transcription attempt failed, will retry",
			infra.F("component", "mimo.client"),
			infra.F("attempt", attempt),
			infra.ErrField(err),
		)
	}

	return ASRResponse{}, errors.Wrap(lastErr, "ASR transcription exhausted retries")
}

// Chat runs an LLM chat completion request and returns the parsed response
// (Ticket 03: voice-command intent analysis). Mirrors Transcribe's retry
// contract: retries transient failures up to config.MaxRetries with
// exponential backoff, fails fast on non-retryable errors.
func (c *mimoClient) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (ChatResponse, error) {
	if c.isClosed() {
		return ChatResponse{}, errors.New("client is closed")
	}
	if len(messages) == 0 {
		return ChatResponse{}, errors.Wrap(ErrInvalidResponse, "chat requires at least one message")
	}

	req := c.buildChatRequest(messages, opts...)

	var lastErr error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			c.logger.Debug("chat completion retry attempt",
				infra.F("component", "mimo.client"),
				infra.F("attempt", attempt),
				infra.F("max_retries", c.config.MaxRetries),
				infra.ErrField(lastErr),
			)

			delay := c.config.RetryDelay * time.Duration(1<<uint(attempt-1))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ChatResponse{}, errors.Wrap(ErrContextCancelled, "chat retry cancelled")
			}
		}

		result, err := c.executeChatRequest(ctx, req)
		if err == nil {
			return result, nil
		}

		lastErr = err

		// Don't retry non-retryable errors
		if !IsRetryable(err) {
			c.logger.Warn("chat completion failed with non-retryable error",
				infra.F("component", "mimo.client"),
				infra.F("model", req.Model),
				infra.ErrField(err),
			)
			return ChatResponse{}, err
		}

		c.logger.Debug("chat completion attempt failed, will retry",
			infra.F("component", "mimo.client"),
			infra.F("attempt", attempt),
		)
	}

	return ChatResponse{}, errors.Wrap(lastErr, "chat completion exhausted retries")
}

// buildChatRequest creates a chat completion request from messages and options
func (c *mimoClient) buildChatRequest(messages []Message, opts ...ChatOption) ChatRequest {
	req := ChatRequest{
		Model:    DefaultChatModel,
		Messages: messages,
	}

	// Apply options
	for _, opt := range opts {
		opt(&req)
	}

	return req
}

// executeChatRequest executes a chat completion request and returns the response
func (c *mimoClient) executeChatRequest(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, errors.Wrap(ErrEncodingFailed, "failed to marshal chat request")
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, errors.Wrap(err, "failed to create chat HTTP request")
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return ChatResponse{}, errors.Wrap(ErrContextCancelled, "chat request cancelled")
		}
		return ChatResponse{}, errors.Wrap(ErrNetworkError, err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, c.handleAPIError(resp)
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return ChatResponse{}, errors.Wrap(ErrInvalidResponse, "failed to decode chat response")
	}

	if len(chatResp.Choices) == 0 {
		return ChatResponse{}, errors.Wrap(ErrInvalidResponse, "no choices in chat response")
	}

	return chatResp, nil
}

// ChatVision runs a vision-capable chat completion over one image (Ticket 04:
// photo OCR). The system prompt goes out as a plain-string content message
// and the user turn as an image_url+text parts array — the exact wire shape
// of the official image-understanding examples — with the image encoded as a
// base64 data URI. Mirrors Chat's retry contract.
func (c *mimoClient) ChatVision(ctx context.Context, systemPrompt, userPrompt string, image []byte, mimeType string, opts ...ChatOption) (ChatResponse, error) {
	if c.isClosed() {
		return ChatResponse{}, errors.New("client is closed")
	}
	if len(image) == 0 {
		return ChatResponse{}, errors.Wrap(ErrInvalidResponse, "vision chat requires image bytes")
	}

	req := c.buildChatVisionRequest(systemPrompt, userPrompt, image, mimeType, opts...)

	var lastErr error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			c.logger.Debug("vision chat retry attempt",
				infra.F("component", "mimo.client"),
				infra.F("attempt", attempt),
				infra.F("max_retries", c.config.MaxRetries),
				infra.ErrField(lastErr),
			)

			delay := c.config.RetryDelay * time.Duration(1<<uint(attempt-1))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ChatResponse{}, errors.Wrap(ErrContextCancelled, "vision chat retry cancelled")
			}
		}

		result, err := c.executeChatVisionRequest(ctx, req)
		if err == nil {
			return result, nil
		}

		lastErr = err

		// Don't retry non-retryable errors
		if !IsRetryable(err) {
			c.logger.Warn("vision chat failed with non-retryable error",
				infra.F("component", "mimo.client"),
				infra.F("model", req.Model),
				infra.ErrField(err),
			)
			return ChatResponse{}, err
		}

		c.logger.Debug("vision chat attempt failed, will retry",
			infra.F("component", "mimo.client"),
			infra.F("attempt", attempt),
		)
	}

	return ChatResponse{}, errors.Wrap(lastErr, "vision chat exhausted retries")
}

// buildChatVisionRequest assembles the vision request. ChatOptions are
// applied to a ChatRequest first so the shared option vocabulary
// (WithChatModel/WithMaxTokens/WithTemperature) configures the vision
// request's matching fields; Temperature is dropped because the vision model
// runs in thinking mode server-side (same rationale as WithTemperature's doc).
func (c *mimoClient) buildChatVisionRequest(systemPrompt, userPrompt string, image []byte, mimeType string, opts ...ChatOption) ChatVisionRequest {
	base := ChatRequest{Model: DefaultChatModel}
	for _, opt := range opts {
		opt(&base)
	}

	return ChatVisionRequest{
		Model: base.Model,
		Messages: []VisionMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: []ChatContentPart{
				{
					Type: "image_url",
					ImageURL: &ChatContentPartImage{
						URL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(image),
					},
				},
				{Type: "text", Text: userPrompt},
			}},
		},
		MaxTokens: base.MaxTokens,
	}
}

// executeChatVisionRequest posts the vision request to /chat/completions and
// decodes the (shared) ChatResponse shape.
func (c *mimoClient) executeChatVisionRequest(ctx context.Context, req ChatVisionRequest) (ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, errors.Wrap(ErrEncodingFailed, "failed to marshal vision chat request")
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, errors.Wrap(err, "failed to create vision chat HTTP request")
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return ChatResponse{}, errors.Wrap(ErrContextCancelled, "vision chat request cancelled")
		}
		return ChatResponse{}, errors.Wrap(ErrNetworkError, err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, c.handleAPIError(resp)
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return ChatResponse{}, errors.Wrap(ErrInvalidResponse, "failed to decode vision chat response")
	}

	if len(chatResp.Choices) == 0 {
		return ChatResponse{}, errors.Wrap(ErrInvalidResponse, "no choices in vision chat response")
	}

	return chatResp, nil
}

// HealthCheck verifies API connectivity
func (c *mimoClient) HealthCheck(ctx context.Context) error {
	if c.isClosed() {
		return errors.New("client is closed")
	}

	c.logger.Debug("Mimo API health check starting",
		infra.F("component", "mimo.client"),
	)

	// Create a minimal TTS request to test connectivity
	req := c.buildTTSRequest("test", WithVoice("冰糖"))

	// Short timeout for health check
	healthCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := c.executeTTSRequest(healthCtx, req)
	if err != nil {
		c.logger.Warn("Mimo API health check failed",
			infra.F("component", "mimo.client"),
			infra.ErrField(err),
		)
		return errors.Wrap(err, "API health check failed")
	}

	c.logger.Debug("Mimo API health check passed",
		infra.F("component", "mimo.client"),
	)

	return nil
}

// Close releases client resources
func (c *mimoClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	c.httpClient.CloseIdleConnections()

	c.logger.Debug("Mimo client closed",
		infra.F("component", "mimo.client"),
	)

	return nil
}

// isClosed checks if the client is closed
func (c *mimoClient) isClosed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.closed
}

// buildTTSRequest creates a TTS request from text and options
func (c *mimoClient) buildTTSRequest(text string, opts ...TTSOption) TTSRequest {
	req := TTSRequest{
		Model: "mimo-v2.5-tts",
		Messages: []Message{
			{Role: "assistant", Content: text},
		},
		Audio: AudioConfig{
			Format:     "pcm16",
			Voice:      "冰糖",  // Default to children's voice
			SampleRate: 16000, // Match existing TTS pipeline
		},
		Stream: false,
	}

	// Apply options
	for _, opt := range opts {
		opt(&req)
	}

	return req
}

// buildASRRequest creates an ASR request from audio and options (OpenAI-compatible format)
func (c *mimoClient) buildASRRequest(audio []byte, format string, opts ...ASROption) ASRRequest {
	// Get MIME type for the audio format
	mimeType := c.getMimeType(format)

	// Encode audio to base64 with Data URL format
	encoded := base64.StdEncoding.EncodeToString(audio)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, encoded)

	req := ASRRequest{
		Model: "mimo-v2.5-asr",
		Messages: []ASRMessage{
			{
				Role: "user",
				Content: []MessageContent{
					{
						Type: "input_audio",
						InputAudio: &AudioInput{
							Data: dataURL,
						},
					},
				},
			},
		},
		ASROptions: ASROptions{
			Language: "zh", // Default to Chinese; override via WithASRLanguage
		},
	}

	// Apply options
	for _, opt := range opts {
		opt(&req)
	}

	return req
}

// getMimeType returns the MIME type for a given audio format
func (c *mimoClient) getMimeType(format string) string {
	switch format {
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "pcm16":
		return "audio/wav" // Default to wav for pcm16
	default:
		return "audio/wav" // Default to wav
	}
}

// executeTTSRequest executes a TTS request and returns audio data
func (c *mimoClient) executeTTSRequest(ctx context.Context, req TTSRequest) (DecodedAudioData, error) {
	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return DecodedAudioData{}, errors.Wrap(ErrEncodingFailed, "failed to marshal TTS request")
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return DecodedAudioData{}, errors.Wrap(err, "failed to create TTS HTTP request")
	}

	// Set headers
	httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	// Execute request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return DecodedAudioData{}, errors.Wrap(ErrContextCancelled, "TTS request cancelled")
		}
		return DecodedAudioData{}, errors.Wrap(ErrNetworkError, err.Error())
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return DecodedAudioData{}, c.handleAPIError(resp)
	}

	// Parse response
	var ttsResp TTSResponse
	if err = json.NewDecoder(resp.Body).Decode(&ttsResp); err != nil {
		return DecodedAudioData{}, errors.Wrap(ErrInvalidResponse, "failed to decode TTS response")
	}

	// Validate response
	if len(ttsResp.Choices) == 0 {
		return DecodedAudioData{}, errors.Wrap(ErrInvalidResponse, "no audio data in TTS response")
	}

	choice := ttsResp.Choices[0]
	audioMsg := choice.Message

	// Decode base64 audio
	audioBytes, err := base64.StdEncoding.DecodeString(audioMsg.Audio.Data)
	if err != nil {
		return DecodedAudioData{}, errors.Wrap(ErrDecodingFailed, "failed to decode base64 audio")
	}

	// The API response carries only the audio bytes (no format/sample_rate/
	// duration), so derive playback metadata from the bytes and the requested
	// format.
	return newDecodedAudio(audioBytes, req.Audio.Format), nil
}

// newDecodedAudio derives playback metadata from the audio bytes themselves.
// WAV: sample rate and duration come from the RIFF header. pcm16: the official
// docs pin the output at 24kHz 16-bit mono, so duration = bytes/2/24000.
func newDecodedAudio(samples []byte, format string) DecodedAudioData {
	d := DecodedAudioData{Samples: samples, Format: format}
	if format == "wav" {
		if sr, ms, ok := parseWAV(samples); ok {
			d.SampleRate = sr
			d.Duration = ms
			return d
		}
	}
	// pcm16 (and unrecognized formats): fixed 24kHz 16-bit mono per docs
	d.SampleRate = 24000
	d.Duration = len(samples) * 1000 / (2 * 24000)
	return d
}

// parseWAV extracts sample rate and duration from a RIFF/WAVE header by
// walking the chunk list. Returns ok=false for malformed input so callers
// can fall back to the pcm16 assumption.
func parseWAV(b []byte) (sampleRate, durationMs int, ok bool) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0, 0, false
	}
	var byteRate, dataSize int
	pos := 12
	for pos+8 <= len(b) {
		id := string(b[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(b[pos+4 : pos+8]))
		body := pos + 8
		if size < 0 || body+size > len(b) {
			break // truncated/corrupt chunk
		}
		switch id {
		case "fmt ":
			if size >= 16 {
				sampleRate = int(binary.LittleEndian.Uint32(b[body+4 : body+8]))
				byteRate = int(binary.LittleEndian.Uint32(b[body+8 : body+12]))
			}
		case "data":
			dataSize = size
		}
		pos = body + size + (size & 1) // chunks are word-aligned
	}
	if sampleRate <= 0 || byteRate <= 0 || dataSize <= 0 {
		return 0, 0, false
	}
	return sampleRate, dataSize * 1000 / byteRate, true
}

// executeTTSStreamRequest executes a streaming TTS request
func (c *mimoClient) executeTTSStreamRequest(ctx context.Context, req TTSRequest, callback func(StreamChunk) error) error {
	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return errors.Wrap(ErrEncodingFailed, "failed to marshal TTS stream request")
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return errors.Wrap(err, "failed to create TTS stream HTTP request")
	}

	// Set headers
	httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	// Execute request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return errors.Wrap(ErrContextCancelled, "TTS stream request cancelled")
		}
		return errors.Wrap(ErrNetworkError, err.Error())
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return c.handleAPIError(resp)
	}

	// Parse SSE stream
	return c.parseSSEStream(resp.Body, callback)
}

// executeASRRequest executes an ASR request and returns transcription
func (c *mimoClient) executeASRRequest(ctx context.Context, req ASRRequest) (ASRResponse, error) {
	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return ASRResponse{}, errors.Wrap(ErrEncodingFailed, "failed to marshal ASR request")
	}

	// Create HTTP request (use /chat/completions endpoint for OpenAI-compatible API)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ASRResponse{}, errors.Wrap(err, "failed to create ASR HTTP request")
	}

	// Set headers
	httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	// Execute request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return ASRResponse{}, errors.Wrap(ErrContextCancelled, "ASR request cancelled")
		}
		return ASRResponse{}, errors.Wrap(ErrNetworkError, err.Error())
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return ASRResponse{}, c.handleAPIError(resp)
	}

	// Parse response (OpenAI-compatible format)
	var asrResp ASRResponse
	if err := json.NewDecoder(resp.Body).Decode(&asrResp); err != nil {
		return ASRResponse{}, errors.Wrap(ErrInvalidResponse, "failed to decode ASR response")
	}

	// Validate response. An empty choices array is a well-formed
	// "no speech" answer from the provider (distinct from a corrupted
	// response above): surface it as ErrEmptyTranscription so callers can
	// tolerate it as an empty transcription instead of a fault.
	if len(asrResp.Choices) == 0 {
		return ASRResponse{}, errors.Wrap(ErrEmptyTranscription, "no transcription in ASR response")
	}

	return asrResp, nil
}

// handleAPIError converts HTTP error responses to typed errors
func (c *mimoClient) handleAPIError(resp *http.Response) error {
	// Read error response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(ErrNetworkError, fmt.Sprintf("HTTP %d: failed to read error body", resp.StatusCode))
	}

	// Parse error JSON
	var apiResp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		// Fallback to plain text
		return NewAPIError(
			"unknown",
			string(body),
			"unknown",
			resp.StatusCode,
			ErrInvalidResponse,
		)
	}

	// Map error codes to sentinel errors
	apiErr := NewAPIError(
		apiResp.Error.Code,
		apiResp.Error.Message,
		apiResp.Error.Type,
		resp.StatusCode,
		nil,
	)

	// Check for rate limiting with retry-after
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		return NewRetryableError(apiErr, retryAfter)
	}

	return apiErr
}

// parseSSEStream parses Server-Sent Events stream
func (c *mimoClient) parseSSEStream(reader io.Reader, callback func(StreamChunk) error) error {
	scanner := newSSEReader(reader)

	for {
		line, err := scanner.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			return errors.Wrap(ErrStreamInterrupted, err.Error())
		}

		if line == "" {
			continue // Empty line between events
		}

		// Parse SSE format: "data: {...}"
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// OpenAI-compatible streams terminate with the [DONE] sentinel
		if data == "[DONE]" {
			break
		}

		// Parse JSON chunk
		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			c.logger.Warn("failed to parse SSE chunk",
				infra.F("component", "mimo.client"),
				infra.F("data", data),
				infra.ErrField(err),
			)
			continue
		}

		// Call callback
		if err := callback(chunk); err != nil {
			return err
		}

		// Check for final chunk
		if chunk.IsFinal {
			break
		}
	}

	return nil
}

// sseReader helps parse Server-Sent Events
type sseReader struct {
	reader *bufio.Reader
}

func newSSEReader(reader io.Reader) *sseReader {
	return &sseReader{
		reader: bufio.NewReader(reader),
	}
}

func (r *sseReader) ReadLine() (string, error) {
	line, err := r.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(line, "\n"), nil
}

// parseRetryAfter parses Retry-After header value
func parseRetryAfter(value string) int {
	if value == "" {
		return 1 // Default 1 second
	}

	var seconds int
	if _, err := fmt.Sscanf(value, "%d", &seconds); err != nil {
		return 1
	}

	if seconds < 1 {
		return 1
	}
	if seconds > 60 {
		return 60 // Cap at 60 seconds
	}

	return seconds
}
