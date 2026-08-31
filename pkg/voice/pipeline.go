package voice

import (
	"context"

	"github.com/pkg/errors"
)

// pcmSampleRate and pcmChannels pin the recording format the wire protocol
// already fixes elsewhere (16-byte header, 640-byte audio frames =
// 20ms@16kHz 16-bit mono). The pipeline is the one place that renders the
// PCM into a container, so the numbers live here.
const (
	pcmSampleRate = 16000
	pcmChannels   = 1
)

// Transcriber is the seam the pipeline uses to reach an ASR API: it receives
// a self-describing WAV (44-byte RIFF header + PCM payload) and returns the
// raw transcript. cmd/service adapts the mimo client to this shape, keeping
// pkg/voice free of the mimo dependency (same pattern as the Chatter seam).
type Transcriber func(ctx context.Context, wav []byte) (string, error)

// Pipeline runs one voice command end-to-end: wrap the accumulated PCM as
// WAV, transcribe it, classify the transcript. The handler layer owns
// buffering (Buffers/Assembler) and session dispatch (ControlVerb); the
// pipeline owns the API composition.
type Pipeline struct {
	transcribe Transcriber
	classifier Classifier
}

// NewPipeline builds the pipeline. A nil Transcriber or nil Classifier
// yields a pipeline whose Process calls return errors — wire real
// implementations (the hybrid classifier works rules-only, see
// NewHybridClassifier).
func NewPipeline(transcribe Transcriber, classifier Classifier) *Pipeline {
	return &Pipeline{transcribe: transcribe, classifier: classifier}
}

// Process converts one recording's PCM (signed 16-bit LE, 16kHz mono) into a
// command Intent. Error contract mirrors the classifiers': a non-nil error
// rides alongside an ActionNone intent and is informational — callers show
// the button fallback and keep the dictation loop alive. Empty PCM fails
// fast with ErrEmptyRecording (no wasted ASR round trip); an empty
// transcript classifies as ActionNone without consulting the LLM.
func (p *Pipeline) Process(ctx context.Context, pcm []byte) (Intent, error) {
	if len(pcm) == 0 {
		return Intent{Action: ActionNone}, ErrEmptyRecording
	}
	if p.transcribe == nil || p.classifier == nil {
		return Intent{Action: ActionNone}, errNilSeam
	}

	wav := WrapWAV(pcm, pcmSampleRate, pcmChannels)
	transcript, err := p.transcribe(ctx, wav)
	if err != nil {
		return Intent{Action: ActionNone, Transcript: transcript},
			errors.Wrap(err, "voice: transcribe failed")
	}
	// Silence (or punctuation-only noise) is not a command and must never
	// cost an LLM call on the interactive loop.
	if NormalizeTranscript(transcript) == "" {
		return Intent{Action: ActionNone, Transcript: transcript, Source: "rule"}, nil
	}
	return p.classifier.Classify(ctx, transcript)
}
