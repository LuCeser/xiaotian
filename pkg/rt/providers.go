package rt

import (
	"context"

	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// ASRProvider converts one utterance of uplink audio into text (ADR-0002:
// each provider layer is replaceable behind a narrow interface).
//
// pcm is bare little-endian signed 16-bit mono PCM at the sample rate
// negotiated in session_init (R0: 16000 Hz). Provider wire details — MiMo ASR
// takes a base64 WAV DataURL — stop at the adapter (13-06 mimo_adapters);
// wrapping pcm into a WAV container is the adapter's job, not the caller's.
type ASRProvider interface {
	// Transcribe converts one complete utterance (VAD-segmented upstream)
	// to text. ctx cancels the provider HTTP call.
	Transcribe(ctx context.Context, pcm []byte) (string, error)
}

// LLMProvider runs one LLM chat completion. The signature is deliberately
// aligned with mimo.Client.Chat so a pkg/mimo client satisfies this
// interface directly and mock providers inject at the same seam. Tool
// definitions and thinking-mode options flow through the existing
// mimo.ChatOption vocabulary; the tool loop itself lives in the turn
// manager (13-04), not here.
type LLMProvider interface {
	// Chat runs one completion over messages. ctx cancels the provider
	// HTTP call.
	Chat(ctx context.Context, messages []mimo.Message, opts ...mimo.ChatOption) (mimo.ChatResponse, error)
}

// AudioChunkFunc receives one chunk of synthesized teacher audio. pcm is
// bare s16le bytes — no container, no encoding; the sample rate is declared
// by the implementation and advertised to the client via
// session_ready.tts.sampleRate (TTSOutputSampleRate). Provider wire details
// such as MiMo's base64 pcm16 SSE chunks are digested by the adapter
// (13-06); base64 never crosses this interface. isFinal marks the last
// chunk of the synthesis. Returning a non-nil error aborts the stream.
type AudioChunkFunc func(pcm []byte, isFinal bool) error

// TTSSynthesizer synthesizes one teacher sentence to streamed audio. The
// signature mirrors mimo.Client.SynthesizeStream except that the callback is
// rt's own AudioChunkFunc (bare PCM) instead of the provider's wire chunk
// type, so the turn manager never sees provider wire formats.
type TTSSynthesizer interface {
	// SynthesizeStream streams audio chunks for text through onAudio as
	// they arrive (first chunk plays immediately; the client never waits
	// for the whole sentence). ctx cancels generation (interrupt path).
	SynthesizeStream(ctx context.Context, text string, onAudio AudioChunkFunc, opts ...mimo.TTSOption) (*mimo.StreamResult, error)
}

// OCRReader is the photo-check vision seam (13.1-03): it receives the fixed
// system + user prompts and the raw JPEG bytes, and returns the raw model
// reply — the exact shape of ocr.Reader (the production adapter is
// cmd/service's mimoReader, shared with the legacy photo pipeline; tests
// inject a plain closure). A nil OCRReader disables the photo-check path:
// the check stage keeps working manual-only (RT-10 degraded semantics).
type OCRReader func(ctx context.Context, systemPrompt, userPrompt string, imageJPEG []byte) (string, error)
