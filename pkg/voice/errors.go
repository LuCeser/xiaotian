package voice

import "errors"

// Sentinel errors for the voice pipeline. Callers MUST detect them via
// errors.Is, never by string matching (mirrors the tts/session/mimo
// conventions).
var (
	// errNilChatter is returned when an LLMClassifier is used without a
	// Chatter seam. Wire the hybrid classifier without the LLM instead.
	errNilChatter = errors.New("voice: llm classifier has no chatter seam")

	// errUnparseableLLMReply is returned when the model reply contains no
	// extractable action JSON. Informational: the intent comes back as
	// ActionNone so the dictation loop keeps going.
	errUnparseableLLMReply = errors.New("voice: unparseable llm reply")

	// ErrAudioOverflow is returned by Assembler.Add when a chunk would push
	// the recording past the configured byte cap (1C1G memory guard).
	ErrAudioOverflow = errors.New("voice: recording exceeds max bytes")
)
