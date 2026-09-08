package main

// Voice-command wiring (Phase 8.2, Ticket 03): routes streamed microphone
// audio frames into the pkg/voice assembler registry, and serves the
// voice_command control trigger end-to-end — assemble → WAV → Mimo ASR →
// intent classification → session FSM dispatch, with a voice_result frame
// back to the client on every path (recognized, not-recognized, or failed)
// so the UI can always fall back to the button controls.

import (
	"context"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/voice"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// voiceBufferIdleTTL bounds how long an abandoned per-connection recording
// buffer survives its last audio frame (voice.Buffers janitor). There is no
// router-level disconnect hook, so this is the only reclamation path for a
// client that disconnects mid-recording; 30s keeps a dropped iPad offline
// while capping orphaned memory on the 1C1G VPS.
const voiceBufferIdleTTL = 30 * time.Second

// settlePollInterval is how often settlePCM re-checks the assembler length
// while waiting for straggler audio chunks.
const settlePollInterval = 50 * time.Millisecond

// sessionPusher is the narrow seam voiceService needs from the session
// Manager (mirrors session.FrameSender, D-05): push a control event onto the
// connection's session. *session.Manager satisfies it structurally, tests
// inject a recording fake.
type sessionPusher interface {
	Push(connID string, ev session.SessionEvent)
}

// voiceService carries the voice-command pipeline dependencies for the ws
// handlers. A nil *voiceService means voice is disabled (no MIMO_API_KEY):
// the audio-frame handler becomes a no-op and voice_command rejects with an
// error frame — the button controls remain the full fallback.
type voiceService struct {
	buffers        *voice.Buffers
	pipeline       *voice.Pipeline
	sessions       sessionPusher
	logger         infra.Logger
	assembleWait   time.Duration
	requestTimeout time.Duration
}

// newVoiceService composes the voice pipeline from the pkg/voice seams.
// transcribe is the Transcriber seam (mimoTranscriber adapts the production
// client; tests inject a plain closure); classifier is injected so tests can
// run rules-only — production passes voice.NewHybridClassifier.
func newVoiceService(transcribe voice.Transcriber, classifier voice.Classifier, sessions sessionPusher, logger infra.Logger, cfg infra.VoiceConfig) *voiceService {
	return &voiceService{
		buffers:        voice.NewBuffers(cfg.MaxRecordingBytes, voiceBufferIdleTTL),
		pipeline:       voice.NewPipeline(transcribe, classifier),
		sessions:       sessions,
		logger:         logger,
		assembleWait:   cfg.AssembleWait,
		requestTimeout: cfg.RequestTimeout,
	}
}

// mimoTranscriber adapts the mimo ASR call onto the voice.Transcriber seam,
// keeping the mimo dependency out of the service layer's test surface.
func mimoTranscriber(mc mimo.Client) voice.Transcriber {
	return func(ctx context.Context, wav []byte) (string, error) {
		resp, err := mc.Transcribe(ctx, wav, "wav")
		if err != nil {
			return "", err
		}
		return resp.ExtractText(), nil
	}
}

// mimoChatter adapts the mimo Chat call onto the voice.Chatter seam. The
// system prompt + user transcript shape matches what LLMClassifier sends.
func mimoChatter(mc mimo.Client, model string) voice.Chatter {
	return func(ctx context.Context, systemPrompt, userText string) (string, error) {
		resp, err := mc.Chat(ctx, []mimo.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userText},
		}, mimo.WithChatModel(model))
		if err != nil {
			return "", err
		}
		return resp.Text(), nil
	}
}

// handleAudioFrame buffers one inbound microphone PCM chunk (MsgTypeAudio).
// Nil receiver (voice disabled) drops the frame — the mic UI is hidden in
// that mode, so stray frames are logged at debug, not punished.
func (vs *voiceService) handleAudioFrame(_ context.Context, client *ws.Client, header *ws.FrameHeader, payload []byte) error {
	if vs == nil {
		return nil
	}
	connID := clientConnID(client)
	if err := vs.buffers.Add(connID, header.Sequence, payload); err != nil {
		// Cap exceeded: the recording is spoiled. Drop the whole entry so a
		// follow-up voice_command reports an empty recording rather than a
		// half-assembled one, and tell the client to stop.
		vs.buffers.Drop(connID)
		vs.logger.Warn("voice recording overflowed cap",
			infra.F("conn_id", connID),
			infra.ErrField(err),
		)
		sendErrorFrame(client, "recording too long")
	}
	return nil
}

// voiceResultResponse is the control frame reply to a voice_command trigger,
// sent on every path (recognized, not a command, or failed) so the client UI
// can show what was heard and offer the button fallback.
type voiceResultResponse struct {
	Type       string  `json:"type"` // "voice_result"
	Action     string  `json:"action"`
	Transcript string  `json:"transcript"`
	Source     string  `json:"source"` // "rule" | "llm" | ""
	Confidence float64 `json:"confidence"`
}

// handleVoiceCommand serves the voice_command control trigger: wait for any
// straggler audio chunks, run the pipeline, dispatch the FSM verb, reply.
// Nil receiver (voice disabled) rejects with an error frame.
func (vs *voiceService) handleVoiceCommand(ctx context.Context, client *ws.Client, _ []byte) error {
	if vs == nil {
		sendErrorFrame(client, "voice commands unavailable")
		return nil
	}
	connID := clientConnID(client)

	pcm := vs.settlePCM(connID)

	// Bound the ASR + LLM calls so a wedged upstream cannot pin router
	// workers (WR-02 bounds Close, not individual handlers).
	callCtx, cancel := context.WithTimeout(ctx, vs.requestTimeout)
	intent, err := vs.pipeline.Process(callCtx, pcm)
	cancel()

	// The recording is consumed either way — never let one command's audio
	// bleed into the next.
	vs.buffers.Drop(connID)

	if err != nil {
		vs.logger.Warn("voice command processing failed",
			infra.F("conn_id", connID),
			infra.F("pcm_bytes", len(pcm)),
			infra.ErrField(err),
		)
	}
	if verb, ok := intent.Action.ControlVerb(); ok {
		vs.sessions.Push(connID, session.SessionEvent{
			Type:        session.EventControlFrame,
			ControlType: verb,
		})
		vs.logger.Info("voice command dispatched",
			infra.F("conn_id", connID),
			infra.F("action", string(intent.Action)),
			infra.F("verb", verb),
			infra.F("source", intent.Source),
			infra.F("transcript", intent.Transcript),
		)
	}

	return sendControlFrame(client, ws.MsgTypeControl, voiceResultResponse{
		Type:       "voice_result",
		Action:     string(intent.Action),
		Transcript: intent.Transcript,
		Source:     intent.Source,
		Confidence: intent.Confidence,
	})
}

// voiceCancelOKResponse acks a voice_cancel so the client (and tests) can
// sequence a restart AFTER the discard. The ack matters because audio and
// control frames travel separate router routes: a fresh recording's chunks
// can be processed before the cancel, and a blind Drop would then wipe them
// too. The production client fire-and-forgets (a human re-press outlasts the
// control route's drain time), but the ordering hazard is real and the ack
// makes it closable.
type voiceCancelOKResponse struct {
	Type string `json:"type"` // "voice_cancel_ok"
}

// handleVoiceCancel discards the connection's in-flight recording (the
// client aborted its push-to-talk: browser cancelled the gesture, page went
// to background, or the client-side byte cap tripped). Without this the
// assembler keeps the canceled chunks and the NEXT recording's sequence
// numbers (which restart at 1) are absorbed as duplicates — Assemble would
// then sort the stale audio ahead of the fresh audio and transcribe the
// wrong command. Nil receiver is a no-op: with voice disabled there is no
// buffer to discard.
func (vs *voiceService) handleVoiceCancel(_ context.Context, client *ws.Client, _ []byte) error {
	if vs == nil {
		return nil
	}
	connID := clientConnID(client)
	vs.buffers.Drop(connID)
	vs.logger.Debug("voice recording canceled",
		infra.F("conn_id", connID),
	)
	return sendControlFrame(client, ws.MsgTypeControl, voiceCancelOKResponse{Type: "voice_cancel_ok"})
}

// settlePCM waits for the connection's audio before assembly. The audio and
// control routes are separate worker pools, so the voice_command trigger can
// be dequeued BEFORE the recording's chunks — even before the FIRST one, in
// which case no assembler exists yet. Both cases are handled by polling:
// first for the buffer to appear at all, then until one interval passes with
// no length growth (audio streams at 50 frames/s, so 50ms of silence means
// the stream is done). Bounded by assembleWait; returns nil only when no
// audio landed in the whole window (a genuinely silent recording).
func (vs *voiceService) settlePCM(connID string) []byte {
	deadline := time.Now().Add(vs.assembleWait)

	asm := vs.buffers.Get(connID)
	for asm == nil && time.Now().Before(deadline) {
		time.Sleep(settlePollInterval)
		asm = vs.buffers.Get(connID)
	}
	if asm == nil {
		return nil
	}

	lastLen := asm.Len()
	for time.Now().Before(deadline) {
		time.Sleep(settlePollInterval)
		l := asm.Len()
		if l == lastLen {
			break
		}
		lastLen = l
	}
	return asm.Assemble()
}
