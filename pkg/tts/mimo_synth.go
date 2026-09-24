package tts

import (
	"context"
	"encoding/binary"

	"github.com/pkg/errors"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// MimoSynthClient is the minimal slice of mimo.Client the MimoWordSynthesizer
// consumes (DICT-03: Mimo is the dictation word audio source, the Volcengine
// pregenerate path becomes the whole-batch fallback). Defined structurally —
// mimo.Client satisfies it by method-set inclusion (proven below) and tests
// fake it with a one-method type — so pkg/tts depends on the smallest
// possible mimo surface. No import cycle: pkg/mimo imports only pkg/infra
// and the stdlib.
type MimoSynthClient interface {
	// Synthesize generates audio from text with the given TTS options.
	Synthesize(ctx context.Context, text string, opts ...mimo.TTSOption) (mimo.DecodedAudioData, error)
}

// Compile-time proof the production mimo client satisfies the narrow seam
// (mimo.NewClient returns exactly this interface type).
var _ MimoSynthClient = (mimo.Client)(nil)

// WordSynthesizer synthesizes ONE dictation word into playback-ready PCM —
// 16kHz mono s16le, FrameSize 640, the exact PCMData semantics of the
// MP3ToPCM pipeline. The Generator uses it as the primary per-word word
// source and keeps the Volcengine whole-batch path as fallback (DICT-03).
type WordSynthesizer interface {
	// SynthesizeWord returns PCM for the bare word — a SINGLE pronunciation.
	// The dictation-standard "word. word." rhythm is the frontend's job (it
	// schedules two plays off the word_complete frame's audio_plays hint), so
	// the audio itself must contain the word exactly once.
	SynthesizeWord(ctx context.Context, word string) (PCMData, error)
}

// MimoWordSynthesizer implements WordSynthesizer on top of the Mimo TTS API
// using mimo.PresetDictationTeacher (VoiceMoLi, pcm16, StyleTeacher).
// Mimo pins pcm16 output at 24kHz 16-bit mono regardless of the requested
// sample rate (see mimo.newDecodedAudio), so SynthesizeWord resamples to the
// streaming pipeline's 16kHz.
type MimoWordSynthesizer struct {
	// client is the Mimo API client behind the narrow seam.
	client MimoSynthClient
	// logger is the structured logger (NoOp-substituted by the constructor).
	logger infra.Logger
}

// NewMimoWordSynthesizer wires the word synthesizer onto a Mimo client.
// Mirrors NewGenerator's dependency validation: a nil client returns an
// error wrapping ErrTTSGenerationFailed; a nil logger becomes
// infra.NewNoOpLogger. The API key lives inside the client only and never
// passes through this layer (and is therefore never logged here).
func NewMimoWordSynthesizer(client MimoSynthClient, logger infra.Logger) (*MimoWordSynthesizer, error) {
	if client == nil {
		return nil, errors.Wrap(ErrTTSGenerationFailed, "mimo client must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &MimoWordSynthesizer{client: client, logger: logger}, nil
}

// SynthesizeWord synthesizes the bare word via PresetDictationTeacher and
// returns 16kHz s16le mono PCMData. The text is the word verbatim — no scene
// narration and no "word. word." pairing (the pregenerate track's doubling
// moved to the frontend scheduler, so one synthesis = one pronunciation).
// Errors are wrapped for errors.Is chains; per-word bytes are transient —
// nothing but the returned PCMData outlives the call (150MB budget).
func (m *MimoWordSynthesizer) SynthesizeWord(ctx context.Context, word string) (PCMData, error) {
	if word == "" {
		return PCMData{}, errors.Wrap(ErrTTSGenerationFailed, "word must not be empty")
	}

	m.logger.Debug("mimo word synthesis starting",
		infra.F("component", "tts.mimo_synth"),
		infra.F("word_length", len(word)),
	)

	audio, err := m.client.Synthesize(ctx, word, mimo.PresetDictationTeacher...)
	if err != nil {
		return PCMData{}, errors.Wrap(err, "mimo word synthesis failed")
	}
	if len(audio.Samples) == 0 {
		return PCMData{}, errors.Wrap(ErrTTSGenerationFailed, "mimo word synthesis returned empty audio")
	}

	switch audio.SampleRate {
	case 16000:
		// Already pipeline-native — pass the bytes through untouched.
		return PCMData{Samples: audio.Samples, FrameSize: 640}, nil
	case 24000:
		// The documented pcm16 reality: 24kHz 16-bit mono regardless of the
		// requested rate. Resample to the pipeline's 16kHz.
		return PCMData{Samples: downsample24kTo16k(audio.Samples), FrameSize: 640}, nil
	default:
		return PCMData{}, errors.Wrapf(ErrUnsupportedSampleRate,
			"mimo returned sample rate %d, want 16000 or 24000", audio.SampleRate)
	}
}

// downsample24kTo16k converts 24kHz mono s16le PCM to 16kHz by linear
// interpolation at the exact 3:2 ratio: output sample j maps to input
// position 1.5*j; integer positions copy through, half positions average the
// two neighbours. Deliberately naive — no anti-alias filter — because speech
// keeps its intelligibility under the mild aliasing a 24k→16k decimation
// introduces, and the 1C1G/150MB budget favours a single-pass allocation
// over filter machinery. Output length is floor(in*2/3) samples; trailing
// input samples a fractional ceiling would need are dropped.
func downsample24kTo16k(pcm []byte) []byte {
	inSamples := len(pcm) / 2 // s16le: 2 bytes per sample; a trailing odd byte is ignored
	outSamples := inSamples * 2 / 3
	out := make([]byte, outSamples*2)

	for j := 0; j < outSamples; j++ {
		num := j * 3 // exact source position is num/2
		base := num / 2
		var v int16
		if num%2 == 0 {
			// Integer position: copy the sample verbatim.
			v = int16(binary.LittleEndian.Uint16(pcm[base*2:]))
		} else {
			// Half position: average the two neighbouring samples. int32
			// arithmetic avoids int16 overflow (e.g. 32767 + 32767).
			a := int32(int16(binary.LittleEndian.Uint16(pcm[base*2:])))
			b := int32(int16(binary.LittleEndian.Uint16(pcm[(base+1)*2:])))
			v = int16((a + b) / 2)
		}
		binary.LittleEndian.PutUint16(out[j*2:], uint16(v))
	}
	return out
}
