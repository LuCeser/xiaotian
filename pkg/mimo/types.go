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

// Message represents a single message in the TTS conversation. The same type
// carries chat-completion turns, where Role may also be "system" or "tool":
// ToolCalls replays an assistant's tool requests and ToolCallID answers one on
// a role:"tool" turn (Phase 13 RT-03 wire shape). Both are omitted when zero,
// so the TTS wire (which reuses this type via TTSRequest.Messages) never sees
// them.
type Message struct {
	Role    string `json:"role"`    // "user" or "assistant" (chat: "system"/"tool")
	Content string `json:"content"` // Message content
	// ToolCallID identifies which tool call this role:"tool" message answers
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolCalls replays the assistant's requested tool calls back to the model
	// (assistant turn of the tool loop)
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
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
	Usage   Usage    `json:"usage,omitempty"`
}

// Choice contains the generated audio data
type Choice struct {
	Message AudioMessage `json:"message"`
	Index   int          `json:"index"`
}

// AudioMessage contains the base64-encoded audio data
type AudioMessage struct {
	Audio   AudioData `json:"audio,omitempty"`
	Role    string    `json:"role"`
	Content string    `json:"content,omitempty"`
}

// AudioData mirrors the API's audio object verbatim. The service returns only
// id/data/expires_at/transcript — format, sample rate, and duration are NOT
// in the response and must be derived client-side from the bytes (see
// newDecodedAudio).
type AudioData struct {
	// ExpiresAt is the optional expiry timestamp (null for TTS)
	ExpiresAt *int64 `json:"expires_at,omitempty"`
	// Transcript is the optional text transcript (null for TTS)
	Transcript *string `json:"transcript,omitempty"`
	// ID is the server-side audio identifier
	ID string `json:"id,omitempty"`
	// Data contains base64-encoded audio
	Data string `json:"data"`
}

// DecodedAudioData represents decoded audio data ready for playback (client output)
type DecodedAudioData struct {
	// Format is the audio format (e.g., "wav", "pcm16")
	Format string
	// Samples contains raw audio samples
	Samples []byte
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
	// ASROptions carries recognition tuning (top-level "asr_options"; the
	// official Python SDK's extra_body is flattened to the top level of the
	// JSON body, so the wire field is "asr_options", NOT "extra_body")
	ASROptions ASROptions `json:"asr_options,omitempty"`
	// Messages contains the conversation with audio input
	Messages []ASRMessage `json:"messages"`
}

// ASRMessage represents a message in ASR request (OpenAI-compatible format)
type ASRMessage struct {
	Role    string           `json:"role"` // "user"
	Content []MessageContent `json:"content"`
}

// MessageContent represents content within a message (can be text or audio)
type MessageContent struct {
	InputAudio *AudioInput `json:"input_audio,omitempty"`
	Type       string      `json:"type"` // "input_audio"
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
	Index   int               `json:"index"`
}

// ASRMessageContent represents the message content in ASR response
type ASRMessageContent struct {
	Role    string `json:"role"` // "assistant"
	Content string `json:"content"`
}

// DecodedASRText represents the decoded transcription text (convenience struct for clients)
type DecodedASRText struct {
	Text       string        `json:"text"`
	Language   string        `json:"language,omitempty"`
	Dialect    string        `json:"dialect,omitempty"`
	Words      []WordSegment `json:"words,omitempty"`
	Usage      Usage         `json:"usage,omitempty"`
	Confidence float64       `json:"confidence,omitempty"`
	Duration   int           `json:"duration,omitempty"`
}

// WordSegment represents a recognized word with timing information
type WordSegment struct {
	Word       string  `json:"word"`
	StartTime  float64 `json:"start_time"`
	EndTime    float64 `json:"end_time"`
	Confidence float64 `json:"confidence"`
}

// ExtractText extracts the transcription text from ASRResponse (OpenAI-compatible format)
func (r *ASRResponse) ExtractText() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].Message.Content
}

// ThinkingConfig controls MiMo's thinking mode on a chat completion request.
// mimo-v2.5 ships with thinking ENABLED by default (extra reasoning_content
// output and a server-forced temperature 1.0 — a large slice of first-response
// latency); realtime callers must send {"type":"disabled"} explicitly to turn
// it off (13-RESEARCH Pitfall 1 / RT-04).
type ThinkingConfig struct {
	// Type is "enabled" or "disabled"
	Type string `json:"type"`
}

// ToolDef declares one function tool offered to the model on a chat
// completion (OpenAI-style wire shape, Phase 13 RT-03).
type ToolDef struct {
	// Type is always "function" in the current MiMo API
	Type string `json:"type"`
	// Function carries the function declaration
	Function ToolFunctionDef `json:"function"`
}

// ToolFunctionDef describes a tool's name, purpose, and JSON Schema parameters.
type ToolFunctionDef struct {
	// Name is the function the model should call (e.g. "advance_word")
	Name string `json:"name"`
	// Description tells the model when to call the function
	Description string `json:"description,omitempty"`
	// Parameters is the JSON Schema for the function arguments, kept as raw
	// JSON so callers pass the schema through verbatim
	Parameters json.RawMessage `json:"parameters,omitempty"`
	// Strict requests schema-conformant arguments (the model may still
	// hallucinate — validate before executing; 13-RESEARCH Pitfall 2)
	Strict bool `json:"strict,omitempty"`
}

// ToolCall is one tool invocation requested by the model. It appears both in
// assistant responses (the model asking to call a tool) and in assistant
// messages replayed back on the next request of the tool loop.
type ToolCall struct {
	// ID identifies this call; echo it back as the role:"tool" ToolCallID
	ID string `json:"id"`
	// Type is always "function" in the current MiMo API
	Type string `json:"type"`
	// Function names the call and carries its raw JSON argument string
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction pairs the requested function name with its arguments.
// Arguments stays a raw string — the official docs warn it may be invalid
// JSON, so parsing/validation is deferred to the tool executor (T-13-05).
type ToolCallFunction struct {
	// Name is the function to execute
	Name string `json:"name"`
	// Arguments is the model-produced JSON argument string (unvalidated)
	Arguments string `json:"arguments"`
}

// ChatRequest represents an LLM chat completion request to the Xiaomi Mimo
// API (OpenAI-compatible /chat/completions, Ticket 03 — voice-command intent
// analysis).
type ChatRequest struct {
	// ToolChoice influences tool usage. MiMo only honors the string "auto" —
	// structured values are silently stripped server-side and behave as auto
	// (13-RESEARCH Pitfall 7), so WithToolChoiceAuto is the only meaningful
	// setter.
	ToolChoice any `json:"tool_choice,omitempty"`
	// Thinking controls the mimo-v2.5 thinking mode. It is ENABLED by default
	// server-side and must be explicitly disabled for latency-sensitive
	// realtime turns (Pitfall 1) — see WithThinkingDisabled.
	Thinking *ThinkingConfig `json:"thinking,omitempty"`
	// Model specifies the LLM to use (e.g. "mimo-v2.5", "mimo-v2.5-pro")
	Model string `json:"model"`
	// Messages carries the conversation (system prompt + user turns)
	Messages []Message `json:"messages"`
	// Tools offers function tools to the model (Phase 13 RT-03 tool loop).
	// Omitted when unset so legacy chat request bodies stay unchanged on the
	// wire.
	Tools []ToolDef `json:"tools,omitempty"`
	// MaxTokens caps the completion length (0 = server default)
	MaxTokens int `json:"max_tokens,omitempty"`
	// Temperature tunes sampling (0 = omitted; mimo-v2.5 thinking mode
	// ignores it server-side anyway per the official docs)
	Temperature float64 `json:"temperature,omitempty"`
}

// ChatMessageContent mirrors an assistant message in a chat completion
// response. Thinking models may populate ReasoningContent with the chain of
// thought while the final answer lands in Content; tool-calling turns carry
// the requested calls in ToolCalls with an empty Content.
type ChatMessageContent struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// ChatResponse represents the response from an LLM chat completion request.
type ChatResponse struct {
	Choices []struct {
		FinishReason string             `json:"finish_reason"`
		Message      ChatMessageContent `json:"message"`
		Index        int                `json:"index"`
	} `json:"choices"`
	Usage Usage `json:"usage,omitempty"`
}

// Text returns the response's answer text: Choices[0].Message.Content, or the
// reasoning content when the model returned an empty content (mirrors
// ASRResponse.ExtractText's convenience contract).
func (r *ChatResponse) Text() string {
	if len(r.Choices) == 0 {
		return ""
	}
	if r.Choices[0].Message.Content != "" {
		return r.Choices[0].Message.Content
	}
	return r.Choices[0].Message.ReasoningContent
}

// ToolCallsRequested reports whether the model responded by requesting tool
// calls (finish_reason == "tool_calls" on the first choice). The realtime
// turn manager uses this to branch between answering and executing tools.
func (r *ChatResponse) ToolCallsRequested() bool {
	return len(r.Choices) > 0 && r.Choices[0].FinishReason == "tool_calls"
}

// FirstToolCalls returns the tool calls requested by the first choice, or nil
// when there are none (no choices or a plain text answer).
func (r *ChatResponse) FirstToolCalls() []ToolCall {
	if len(r.Choices) == 0 {
		return nil
	}
	return r.Choices[0].Message.ToolCalls
}

// ChatContentPart is one part of a multipart chat message content
// (Ticket 04 — photo OCR). Exactly one payload field is set per Type: Text
// for "text", ImageURL for "image_url".
type ChatContentPart struct {
	// ImageURL carries the image reference when Type == "image_url". The URL
	// may be public https or a base64 data URI ("data:image/jpeg;base64,...")
	// per the official image-understanding docs.
	ImageURL *ChatContentPartImage `json:"image_url,omitempty"`
	// Type is the part kind: "text" or "image_url"
	Type string `json:"type"`
	// Text carries the part text when Type == "text"
	Text string `json:"text,omitempty"`
}

// ChatContentPartImage holds the image reference of an "image_url" content
// part.
type ChatContentPartImage struct {
	// URL is the image location: https URL or data URI
	URL string `json:"url"`
}

// ChatVisionRequest represents a vision-capable LLM chat completion request
// (Ticket 04 — photo OCR). Same endpoint and option vocabulary as
// ChatRequest; only the user message content differs (image + text parts).
type ChatVisionRequest struct {
	// Model specifies the vision-capable LLM (default: mimo-v2.5)
	Model string `json:"model"`
	// Messages carries the conversation. ChatVision builds a system message
	// (plain string content) plus a user message (image_url + text parts) —
	// the exact wire shape of the official image-understanding examples.
	Messages []VisionMessage `json:"messages"`
	// MaxTokens caps the completion length (0 = server default)
	MaxTokens int `json:"max_tokens,omitempty"`
	// Thinking controls the model's thinking mode. It is ENABLED BY DEFAULT
	// server-side, which spends the whole token budget on reasoning_content
	// and returns an empty content — vision callers must send
	// {"type":"disabled"} explicitly (issue #42).
	Thinking *ThinkingConfig `json:"thinking,omitempty"`
	// MaxCompletionTokens is the OFFICIAL budget field (the OpenAI-compatible
	// endpoint documents max_completion_tokens, not max_tokens). It covers
	// reasoning + visible output together. 0 = omitted = server default.
	MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
}

// VisionMessage is one message of a ChatVisionRequest. Content is
// polymorphic on the wire — a plain string for system turns, a
// []ChatContentPart array for the vision user turn — so it is typed `any`
// here. ChatVision constructs both shapes itself; callers never build one.
type VisionMessage struct {
	// Content is either string (system) or []ChatContentPart (user vision)
	Content any `json:"content"`
	// Role is "system" or "user"
	Role string `json:"role"`
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
	// Error contains any error that occurred during streaming
	Error error
	// TotalDuration is the total audio duration in milliseconds
	TotalDuration int
	// ChunkCount is the number of chunks received
	ChunkCount int
}

// Client is the interface for Mimo service operations
type Client interface {
	// Synthesize generates audio from text input
	Synthesize(ctx context.Context, text string, opts ...TTSOption) (DecodedAudioData, error)

	// SynthesizeStream generates audio with streaming callback
	SynthesizeStream(ctx context.Context, text string, callback StreamCallback, opts ...TTSOption) (*StreamResult, error)

	// Transcribe converts audio to text
	Transcribe(ctx context.Context, audio []byte, format string, opts ...ASROption) (ASRResponse, error)

	// Chat runs an LLM chat completion and returns the response (Ticket 03:
	// voice-command intent analysis).
	Chat(ctx context.Context, messages []Message, opts ...ChatOption) (ChatResponse, error)

	// ChatVision runs a vision-capable chat completion over one image and
	// returns the response (Ticket 04: photo OCR). The image is sent as a
	// base64 data URI per the official image-understanding wire format.
	ChatVision(ctx context.Context, systemPrompt, userPrompt string, image []byte, mimeType string, opts ...ChatOption) (ChatResponse, error)

	// HealthCheck verifies API connectivity
	HealthCheck(ctx context.Context) error

	// Close releases client resources
	Close() error
}

// TTSOption configures TTS synthesis behavior
type TTSOption func(*TTSRequest)

// ASROption configures ASR recognition behavior
type ASROption func(*ASRRequest)

// ChatOption configures LLM chat completion behavior
type ChatOption func(*ChatRequest)

// DefaultChatModel is the default LLM for chat completions. mimo-v2.5 (not
// -pro) keeps per-command intent classification fast and cheap on the 1C1G
// VPS budget; override per call via WithChatModel.
const DefaultChatModel = "mimo-v2.5"

// WithChatModel sets the chat completion model (e.g. "mimo-v2.5-pro")
func WithChatModel(model string) ChatOption {
	return func(req *ChatRequest) {
		req.Model = model
	}
}

// WithMaxTokens caps the completion length
func WithMaxTokens(n int) ChatOption {
	return func(req *ChatRequest) {
		req.MaxTokens = n
	}
}

// WithTemperature sets sampling temperature (mimo-v2.5 thinking mode forces
// 1.0 server-side per the official docs — this is a no-op there)
func WithTemperature(t float64) ChatOption {
	return func(req *ChatRequest) {
		req.Temperature = t
	}
}

// WithTools offers function tools on a chat completion (Phase 13 RT-03 tool
// loop wire shape).
func WithTools(tools []ToolDef) ChatOption {
	return func(req *ChatRequest) {
		req.Tools = tools
	}
}

// WithToolChoiceAuto sets tool_choice to the string "auto" — the only value
// the MiMo backend honors (structured tool_choice values are silently
// stripped server-side; 13-RESEARCH Pitfall 7).
func WithToolChoiceAuto() ChatOption {
	return func(req *ChatRequest) {
		req.ToolChoice = "auto"
	}
}

// WithThinkingDisabled turns off mimo-v2.5's default-enabled thinking mode
// (no reasoning_content, lower first-response latency — Pitfall 1 / RT-04).
func WithThinkingDisabled() ChatOption {
	return func(req *ChatRequest) {
		req.Thinking = &ThinkingConfig{Type: "disabled"}
	}
}

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
			FinishReason *string `json:"finish_reason"`
			Delta        struct {
				Audio struct {
					ID   string `json:"id"`
					Data string `json:"data"`
				} `json:"audio"`
			} `json:"delta"`
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
