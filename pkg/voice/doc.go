// Package voice implements the voice-command pipeline for the dictation
// tutor (Ticket 03): reassembling streamed microphone audio, wrapping it as
// WAV, and classifying the transcribed utterance into one of the four core
// student commands (好了 / 重读 / 暂停 / 不会写) via a rule fast-path plus an
// optional LLM fallback for varied natural expressions.
//
// The package deliberately has no dependency on pkg/mimo: the LLM classifier
// takes a Chatter func seam and the handler layer adapts the mimo client to
// it, mirroring the FrameSender/SessionStreamer narrow-seam pattern used by
// pkg/session (D-05).
package voice
