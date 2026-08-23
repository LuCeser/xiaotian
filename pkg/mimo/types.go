// Package mimo provides integration with Xiaomi Mimo speech services (ASR & TTS).
// It implements OpenAI-compatible API clients for text-to-speech and speech recognition
// with authentication, error handling, and streaming support optimized for the
// Home Tutor Agent's 1C1G VPS constraints.
//
// Import path: github.com/xiaotian/home-tutor-agent/pkg/mimo
package mimo

import (
	"context"
	"encoding/json"
	"time"
)

// TTSRequest represents a text-to-speech synthesis request to Xiaomi Mimo API.
type TTSRequest struct {
	// Model specifies the TTS model to use (e.g., "mimo-v2.5-tts")
	Model string `json:"model"`
	// Messages contains the conversation for TTS with optional style instructions
	Messages []Message `json:"messages"`
	// Audio configures output audio format and voice
	Audio AudioConfig `json:"audio"`
	// Stream enables SSE streaming for real-time audio generation
	Stream bool `json:"stream,omitempty"`
}

// Message represents a single message in the TTS conversation
type Message struct {
	Role    string `json:"role"`    // "user" or "assistant"
	Content string `json:"content"` // Message content
}

// AudioConfig configures TTS audio output parameters
type AudioConfig struct {
	// Format specifies output format: "wav", "pcm16", "mp3"
	Format string `json:"format"`
	// Voice selects the voice for synthesis (e.g., "冰糖", "茉莉", "Mia")
	Voice string `json:"voice"`
	// SampleRate sets audio sample rate in Hz (default: 24000)
	SampleRate int `json:"sample_rate,omitempty"`
}

// TTSResponse represents the response from a TTS synthesis request
type TTSResponse struct {
	Choices []Choice `json:"choices"`
	Usage   Usage     `json:"usage,omitempty"`
}

// Choice contains the generated audio data
type Choice struct {
	Message AudioMessage `json:"message"`
	Index   int          `json:"index"`
}

// AudioMessage contains the base64-encoded audio data
type AudioMessage struct {
	Role    string    `json:"role"`
	Content string    `json:"content,omitempty"`
	Audio   AudioData `json:"audio,omitempty"`
}

// AudioData mirrors the API's audio object verbatim. The service returns only
// id/data/expires_at/transcript — format, sample rate, and duration are NOT
// in the response and must be derived client-side from the bytes (see
// newDecodedAudio).
type AudioData struct {
	// ID is the server-side audio identifier
	ID string `json:"id,omitempty"`
	// Data contains base64-encoded audio
	Data string `json:"data"`
	// ExpiresAt is the optional expiry timestamp (null for TTS)
	ExpiresAt *int64 `json:"expires_at,omitempty"`
	// Transcript is the optional text transcript (null for TTS)
	Transcript *string `json:"transcript,omitempty"`
}

// DecodedAudioData represents decoded audio data ready for playback (client output)
type DecodedAudioData struct {
	// Samples contains raw audio samples
	Samples []byte
	// Format is the audio format (e.g., "wav", "pcm16")
	Format string
	// SampleRate is the sample rate in Hz
	SampleRate int
	// Duration is the audio duration in milliseconds
	Duration int
}

// Usage tracks token consumption for the request
type Usage struct {
	TotalTokens      int `json:"total_tokens"`
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// ASRRequest represents a speech recognition request to Xiaomi Mimo API (OpenAI-compatible format)
type ASRRequest struct {
	// Model specifies the ASR model (e.g., "mimo-v2.5-asr")
	Model string `json:"model"`
	// Messages contains the conversation with audio input
	Messages []ASRMessage `json:"messages"`
	// ASROptions carries recognition tuning (top-level "asr_options"; the
	// official Python SDK's extra_body is flattened to the top level of the
	// JSON body, so the wire field is "asr_options", NOT "extra_body")
	ASROptions ASROptions `json:"asr_options,omitempty"`
}

// ASRMessage represents a message in ASR request (OpenAI-compatible format)
type ASRMessage struct {
	Role    string             `json:"role"` // "user"
	Content []MessageContent   `json:"content"`
}

// MessageContent represents content within a message (can be text or audio)
type MessageContent struct {
	Type       string      `json:"type"` // "input_audio"
	InputAudio *AudioInput `json:"input_audio,omitempty"`
}

// AudioInput represents input audio data with Data URL format
type AudioInput struct {
	// Data contains Data URL formatted audio (e.g., "data:audio/wav;base64,xxx")
	Data string `json:"data"`
}

// ASROptions configures speech recognition behavior (top-level asr_options).
// Only language is documented by the official API; undocumented fields risk
// HTTP 400 "Param Incorrect", so they are intentionally absent.
type ASROptions struct {
	// Language configures recognition language (options: "auto", "zh", "en"; unset = auto-detect)
	Language string `json:"language,omitempty"`
}

// ASRResponse represents the response from an ASR request (OpenAI-compatible format)
type ASRResponse struct {
	Choices []ASRChoice `json:"choices"`
	Usage   Usage       `json:"usage,omitempty"`
}

// ASRChoice represents a choice in ASR response
type ASRChoice struct {
	Message ASRMessageContent `json:"message"`
	Index   int                `json:"index"`
}

// ASRMessageContent represents the message content in ASR response
type ASRMessageContent struct {
	Role    string `json:"role"` // "assistant"
	Content string `json:"content"`
}

// DecodedASRText represents the decoded transcription text (convenience struct for clients)
type DecodedASRText struct {
	Text      string        `json:"text"`
	Language  string        `json:"language,omitempty"`
	Dialect   string        `json:"dialect,omitempty"`
	Confidence float64      `json:"confidence,omitempty"`
	Duration  int           `json:"duration,omitempty"`
	Words     []WordSegment `json:"words,omitempty"`
	Usage     Usage         `json:"usage,omitempty"`
}

// WordSegment represents a recognized word with timing information
type WordSegment struct {
	Word      string  `json:"word"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	Confidence float64 `json:"confidence"`
}

// ExtractText extracts the transcription text from ASRResponse (OpenAI-compatible format)
func (r *ASRResponse) ExtractText() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].Message.Content
}

// StreamChunk represents a chunk of streaming audio data delivered to the
// caller's callback. The wire format is OpenAI-style chat.completion.chunk
// (choices[].delta.audio.data + finish_reason); UnmarshalJSON maps that shape
// into these client-facing fields. Duration is cumulative and filled in by
// the client from decoded byte counts — the wire never carries it.
type StreamChunk struct {
	// AudioData contains base64-encoded audio chunk ("" on the final chunk)
	AudioData string
	// IsFinal indicates if this is the last chunk (finish_reason == "stop")
	IsFinal bool
	// Duration provides cumulative audio duration in milliseconds so far
	Duration int
}

// ClientConfig configures the Mimo API client
type ClientConfig struct {
	// APIKey is the authentication token for Mimo API
	APIKey string
	// BaseURL is the API endpoint (default: "https://api.xiaomimimo.com/v1")
	BaseURL string
	// Timeout is the HTTP request timeout (default: 30s)
	Timeout time.Duration
	// MaxRetries is the number of retry attempts for transient failures
	MaxRetries int
	// RetryDelay is the initial delay between retries
	RetryDelay time.Duration
	// EnableStreaming enables SSE streaming for TTS
	EnableStreaming bool
}

// StreamCallback is called for each audio chunk during streaming TTS
type StreamCallback func(chunk StreamChunk) error

// StreamResult represents the result of a streaming TTS operation
type StreamResult struct {
	// TotalDuration is the total audio duration in milliseconds
	TotalDuration int
	// ChunkCount is the number of chunks received
	ChunkCount int
	// Error contains any error that occurred during streaming
	Error error
}


// Client is the interface for Mimo service operations
type Client interface {
	// Synthesize generates audio from text input
	Synthesize(ctx context.Context, text string, opts ...TTSOption) (DecodedAudioData, error)

	// SynthesizeStream generates audio with streaming callback
	SynthesizeStream(ctx context.Context, text string, callback StreamCallback, opts ...TTSOption) (*StreamResult, error)

	// Transcribe converts audio to text
	Transcribe(ctx context.Context, audio []byte, format string, opts ...ASROption) (ASRResponse, error)

	// HealthCheck verifies API connectivity
	HealthCheck(ctx context.Context) error

	// Close releases client resources
	Close() error
}

// TTSOption configures TTS synthesis behavior
type TTSOption func(*TTSRequest)

// ASROption configures ASR recognition behavior
type ASROption func(*ASRRequest)

// WithVoice sets the TTS voice
func WithVoice(voice string) TTSOption {
	return func(req *TTSRequest) {
		req.Audio.Voice = voice
	}
}

// WithAudioFormat sets the TTS audio format
func WithAudioFormat(format string) TTSOption {
	return func(req *TTSRequest) {
		req.Audio.Format = format
	}
}

// WithSampleRate sets the TTS sample rate
func WithSampleRate(rate int) TTSOption {
	return func(req *TTSRequest) {
		req.Audio.SampleRate = rate
	}
}

// WithTTSStream enables streaming for TTS
func WithTTSStream() TTSOption {
	return func(req *TTSRequest) {
		req.Stream = true
	}
}

// WithStyleInstruction adds a style instruction for TTS
func WithStyleInstruction(instruction string) TTSOption {
	return func(req *TTSRequest) {
		req.Messages = append([]Message{{Role: "user", Content: instruction}}, req.Messages...)
	}
}

// WithASRLanguage sets the ASR recognition language (top-level asr_options.language)
func WithASRLanguage(lang string) ASROption {
	return func(req *ASRRequest) {
		req.ASROptions.Language = lang
	}
}

// UnmarshalJSON maps an OpenAI-style chat.completion.chunk SSE payload onto
// StreamChunk. Expected wire shape:
//
//	{"choices":[{"index":0,"delta":{"role":"assistant",
//	  "audio":{"id":"...","data":"<base64>"}},
//	  "finish_reason":null}]}
//
// The terminating chunk carries finish_reason:"stop" and usually no audio.
func (s *StreamChunk) UnmarshalJSON(data []byte) error {
	var wire struct {
		Choices []struct {
			Delta struct {
				Audio struct {
					ID   string `json:"id"`
					Data string `json:"data"`
				} `json:"audio"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	if len(wire.Choices) > 0 {
		choice := wire.Choices[0]
		s.AudioData = choice.Delta.Audio.Data
		s.IsFinal = choice.FinishReason != nil && *choice.FinishReason == "stop"
	}
	return nil
}