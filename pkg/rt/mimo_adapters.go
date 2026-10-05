// Package rt: mimo_adapters.go binds the MiMo provider client to the three
// replaceable provider interfaces (ADR-0002: provider swap cost stays inside
// the adapter layer). This is the ONLY place in the /rt chain where provider
// wire details — the ASR base64 WAV DataURL container and the TTS pcm16 SSE
// chunks' base64 encoding — are digested (13-03 interface contract, review
// M-1): everything above sees bare PCM and plain text.
package rt

import (
	"context"
	"encoding/base64"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/voice"
)

// rtASRSampleRate is the /rt uplink sample rate fixed by session_init
// validation (16kHz s16le mono); the WAV container handed to MiMo ASR must
// declare it.
const rtASRSampleRate = 16000

// MimoASR adapts mimo's whole-utterance ASR call to ASRProvider. The VAD
// upstream segments utterances; each segment arrives as bare 16kHz s16le
// PCM and is wrapped into a WAV container here (pkg/voice.WrapWAV — the
// container is a provider wire detail, not a transport concern).
type MimoASR struct {
	client mimo.Client
}

// NewMimoASR wraps a mimo.Client as an ASRProvider.
func NewMimoASR(client mimo.Client) *MimoASR {
	return &MimoASR{client: client}
}

// Transcribe converts one utterance of bare PCM to text (zh, per the R0
// Chinese tutor scope).
func (a *MimoASR) Transcribe(ctx context.Context, pcm []byte) (string, error) {
	wav := voice.WrapWAV(pcm, rtASRSampleRate, 1)
	resp, err := a.client.Transcribe(ctx, wav, "wav", mimo.WithASRLanguage("zh"))
	if err != nil {
		return "", err
	}
	return resp.ExtractText(), nil
}

// Compile-time interface check (13-03 key_link).
var _ ASRProvider = (*MimoASR)(nil)

// MimoLLM adapts mimo's Chat call to LLMProvider. The signature is already
// aligned (13-03), so Chat is a pass-through that injects the deployment's
// chat model; tool definitions and thinking-mode options flow through the
// mimo.ChatOption vocabulary owned by the turn manager (13-04).
type MimoLLM struct {
	client mimo.Client
	model  string
}

// NewMimoLLM wraps a mimo.Client as an LLMProvider pinned to model.
func NewMimoLLM(client mimo.Client, model string) *MimoLLM {
	return &MimoLLM{client: client, model: model}
}

// Chat runs one completion, transparently passing opts through.
func (l *MimoLLM) Chat(ctx context.Context, messages []mimo.Message, opts ...mimo.ChatOption) (mimo.ChatResponse, error) {
	opts = append(opts, mimo.WithChatModel(l.model))
	return l.client.Chat(ctx, messages, opts...)
}

// Compile-time interface check (13-03 key_link).
var _ LLMProvider = (*MimoLLM)(nil)

// MimoTTS adapts mimo's streaming SynthesizeStream to TTSSynthesizer. MiMo's
// SSE chunks carry base64-encoded pcm16; the callback here decodes them to
// bare s16le bytes before handing them up via AudioChunkFunc — the single
// base64 decode point of the whole /rt chain (review M-1 ruling: 13-03's
// interface face and 13-04's turn.go are base64-free by contract).
type MimoTTS struct {
	client mimo.Client
}

// NewMimoTTS wraps a mimo.Client as a TTSSynthesizer.
func NewMimoTTS(client mimo.Client) *MimoTTS {
	return &MimoTTS{client: client}
}

// SynthesizeStream streams one teacher sentence's audio through onAudio as
// bare s16le chunks. isFinal and error semantics pass through unchanged; the
// dictation-teacher preset (voice/format/sample-rate/style) is attached
// here so the turn manager stays preset-agnostic.
func (t *MimoTTS) SynthesizeStream(ctx context.Context, text string, onAudio AudioChunkFunc, opts ...mimo.TTSOption) (*mimo.StreamResult, error) {
	callback := func(chunk mimo.StreamChunk) error {
		pcm, err := base64.StdEncoding.DecodeString(chunk.AudioData)
		if err != nil {
			return errors.Wrap(err, "rt: decode mimo tts chunk base64")
		}
		return onAudio(pcm, chunk.IsFinal)
	}
	all := make([]mimo.TTSOption, 0, len(opts)+len(mimo.PresetDictationTeacher))
	all = append(all, mimo.PresetDictationTeacher...)
	all = append(all, opts...)
	return t.client.SynthesizeStream(ctx, text, callback, all...)
}

// Compile-time interface check (13-03 key_link).
var _ TTSSynthesizer = (*MimoTTS)(nil)

// LessonTools binds pkg/lesson onto the LessonExecutor seam: EnsureLesson
// lives on *lesson.Store while the read/write tool pair lives on
// *lesson.Executor (13-01 split), so the production LessonExecutor is this
// small adapter over both — exactly what rt.LessonExecutor's godoc
// anticipates. The method-expression checks below pin pkg/lesson's
// signatures to the seam at compile time (13-06 key_link).
type LessonTools struct {
	store *lesson.Store
	exec  *lesson.Executor
}

// NewLessonTools composes the classroom-tool adapter. Returns an error when
// either dependency is nil (D-47 nil-guard convention).
func NewLessonTools(store *lesson.Store, exec *lesson.Executor) (*LessonTools, error) {
	if store == nil || exec == nil {
		return nil, errors.New("rt: lesson tools require non-nil store and executor")
	}
	return &LessonTools{store: store, exec: exec}, nil
}

// EnsureLesson creates the lesson row if absent (first content wins).
func (t *LessonTools) EnsureLesson(ctx context.Context, lessonID string, words []string) (lesson.Lesson, error) {
	return t.store.EnsureLesson(ctx, lessonID, words)
}

// EnsureLessonCards creates the lesson row carrying word cards if absent
// (first content wins). A legacy row with empty cards is NOT backfilled —
// the 13-08 FSM detects that state and answers lesson_unavailable.
func (t *LessonTools) EnsureLessonCards(ctx context.Context, lessonID string, cards []lesson.Word) (lesson.Lesson, error) {
	return t.store.EnsureLessonCards(ctx, lessonID, cards)
}

// GetLessonState returns the current lesson row (get_lesson_state tool).
func (t *LessonTools) GetLessonState(ctx context.Context, lessonID string) (lesson.Lesson, error) {
	return t.exec.GetLessonState(ctx, lessonID)
}

// AdvanceWord moves the cursor forward one word, idempotently by requestID
// under the expectedVersion optimistic lock (advance_word tool).
func (t *LessonTools) AdvanceWord(ctx context.Context, lessonID string, expectedVersion int, requestID string) (lesson.AdvanceResult, error) {
	return t.exec.AdvanceWord(ctx, lessonID, expectedVersion, requestID)
}

// Compile-time interface check: the LessonTools adapter satisfies the turn
// manager's classroom seam (13-06 key_link).
var _ LessonExecutor = (*LessonTools)(nil)

// Compile-time signature checks binding pkg/lesson's methods (13-01) to the
// LessonExecutor seam — if any of the three signatures drifts, this file
// stops compiling instead of failing at wiring time.
var (
	_ func(*lesson.Store, context.Context, string, []string) (lesson.Lesson, error) = (*lesson.Store).EnsureLesson
	_ func(*lesson.Store, context.Context, string, []lesson.Word) (lesson.Lesson, error) = (*lesson.Store).EnsureLessonCards
	_ func(*lesson.Executor, context.Context, string) (lesson.Lesson, error)        = (*lesson.Executor).GetLessonState
	_ func(*lesson.Executor, context.Context, string, int, string) (lesson.AdvanceResult, error) = (*lesson.Executor).AdvanceWord
)
