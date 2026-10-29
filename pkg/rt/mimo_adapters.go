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
	"sort"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
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

// EnsureLessonCards creates the lesson row carrying word cards and the
// classroom's subject (Issue #10) if absent (first content wins). A legacy
// row with empty cards is NOT backfilled — the 13-08 FSM detects that state
// and answers lesson_unavailable.
func (t *LessonTools) EnsureLessonCards(ctx context.Context, lessonID string, cards []lesson.Word, category string) (lesson.Lesson, error) {
	return t.store.EnsureLessonCards(ctx, lessonID, cards, category)
}

// GetLessonState returns the current lesson row (get_lesson_state tool).
func (t *LessonTools) GetLessonState(ctx context.Context, lessonID string) (lesson.Lesson, error) {
	return t.exec.GetLessonState(ctx, lessonID)
}

// EnsurePoemLesson creates the whole-poem 背默 attempt row (Issue #12).
func (t *LessonTools) EnsurePoemLesson(ctx context.Context, lessonID string, poemID int64, lines []string) (lesson.Lesson, error) {
	return t.store.EnsurePoemLesson(ctx, lessonID, poemID, lines)
}

// LatestPoemLessonID returns the poem's most recent attempt row id (Issue
// #12's resume probe).
func (t *LessonTools) LatestPoemLessonID(ctx context.Context, poemID int64) (string, error) {
	return t.store.LatestPoemLessonID(ctx, poemID)
}

// EnsurePassageLesson creates the passage-range 背默 attempt row (Issue #14).
func (t *LessonTools) EnsurePassageLesson(ctx context.Context, lessonID string, passageID, scopeID int64, whole bool, paragraphs []int, texts []string, category string) (lesson.Lesson, error) {
	return t.store.EnsurePassageLesson(ctx, lessonID, passageID, scopeID, whole, paragraphs, texts, category)
}

// LatestPassageLessonID returns the passage RANGE's most recent attempt row
// id (Issue #14's resume probe, keyed by content + range).
func (t *LessonTools) LatestPassageLessonID(ctx context.Context, passageID int64, whole bool, paragraphs []int) (string, error) {
	return t.store.LatestPassageLessonID(ctx, passageID, whole, paragraphs)
}

// AdvanceWord moves the cursor forward one word, idempotently by requestID
// under the expectedVersion optimistic lock (advance_word tool).
func (t *LessonTools) AdvanceWord(ctx context.Context, lessonID string, expectedVersion int, requestID string) (lesson.AdvanceResult, error) {
	return t.exec.AdvanceWord(ctx, lessonID, expectedVersion, requestID)
}

// AdvanceDictation persists behavior and advances one fixed dictation
// target in the executor's single transaction (advance_dictation tool).
func (t *LessonTools) AdvanceDictation(ctx context.Context, in lesson.AdvanceDictationInput) (lesson.AdvanceDictationResult, error) {
	return t.exec.AdvanceDictation(ctx, in)
}

// EnterCheck opens the lesson's check stage (13.1-01, enter_check tool).
func (t *LessonTools) EnterCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, outcomes map[string]lesson.WordOutcome) (lesson.EnterCheckResult, error) {
	return t.exec.EnterCheck(ctx, lessonID, expectedVersion, requestID, outcomes)
}

// ApplyVerdict records one check-stage verdict (13.1-01, check_verdict tool).
func (t *LessonTools) ApplyVerdict(ctx context.Context, lessonID string, expectedVersion int, requestID string, verdict lesson.CheckVerdict) (lesson.ApplyVerdictResult, error) {
	return t.exec.ApplyVerdict(ctx, lessonID, expectedVersion, requestID, verdict)
}

// CompleteCheck runs the unified-submit stage transition (13.1-01,
// check_submit tool) — the FSRS idempotency latch.
func (t *LessonTools) CompleteCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, unchecked bool) (lesson.CompleteCheckResult, error) {
	return t.exec.CompleteCheck(ctx, lessonID, expectedVersion, requestID, unchecked)
}

// ClearUnchecked resets the D-03 unchecked marker (13.1-04, clear_unchecked
// tool) once the correction session recorded its diff.
func (t *LessonTools) ClearUnchecked(ctx context.Context, lessonID string, expectedVersion int, requestID string) (lesson.ClearUncheckedResult, error) {
	return t.exec.ClearUnchecked(ctx, lessonID, expectedVersion, requestID)
}

// Compile-time interface check: the LessonTools adapter satisfies the turn
// manager's classroom seam (13-06 key_link).
var _ LessonExecutor = (*LessonTools)(nil)

// Compile-time signature checks binding pkg/lesson's methods (13-01, plus
// the 13.1-01 check-stage trio) to the LessonExecutor seam — if any of the
// signatures drifts, this file stops compiling instead of failing at wiring
// time.
var (
	_ func(*lesson.Store, context.Context, string, []string) (lesson.Lesson, error)                                                = (*lesson.Store).EnsureLesson
	_ func(*lesson.Store, context.Context, string, []lesson.Word, string) (lesson.Lesson, error)                                   = (*lesson.Store).EnsureLessonCards
	_ func(*lesson.Executor, context.Context, string) (lesson.Lesson, error)                                                       = (*lesson.Executor).GetLessonState
	_ func(*lesson.Executor, context.Context, string, int, string) (lesson.AdvanceResult, error)                                   = (*lesson.Executor).AdvanceWord
	_ func(*lesson.Executor, context.Context, lesson.AdvanceDictationInput) (lesson.AdvanceDictationResult, error)                 = (*lesson.Executor).AdvanceDictation
	_ func(*lesson.Executor, context.Context, string, int, string, map[string]lesson.WordOutcome) (lesson.EnterCheckResult, error) = (*lesson.Executor).EnterCheck
	_ func(*lesson.Executor, context.Context, string, int, string, lesson.CheckVerdict) (lesson.ApplyVerdictResult, error)         = (*lesson.Executor).ApplyVerdict
	_ func(*lesson.Executor, context.Context, string, int, string, bool) (lesson.CompleteCheckResult, error)                       = (*lesson.Executor).CompleteCheck
	_ func(*lesson.Executor, context.Context, string, int, string) (lesson.ClearUncheckedResult, error)                            = (*lesson.Executor).ClearUnchecked
)

// pyfsrsFetchLockHours is the fetch-lock window PyFSRS explicitly passes on
// every RunDue (13-10, T-13-20): 1 hour covers an R0 classroom while
// bounding the impact of a "RunDue succeeded but lesson creation failed"
// leftover lock, and it must NOT be left to the vendored script's 24h
// default. Process-crash residual locks re-enter the queue after this
// window; scoped unlock / durable recovery is deferred to Phase 15.
const pyfsrsFetchLockHours = 1

// PyFSRS adapts pybridge.Executor onto the WordSource seam (13-10): the
// dictation FSM fetches due words via the vocab_manager.py `due`
// subcommand and writes learning records via `review`. This is the ONLY
// place where the pybridge result shapes (DueCard / ReviewResult.Updated)
// are digested — everything above sees lesson.Word and ReviewWriteResult.
type PyFSRS struct {
	exec   pybridge.Executor
	logger infra.Logger
}

// NewPyFSRS composes the FSRS word-source adapter. Returns an error when
// exec is nil (D-47 nil-guard convention, same as NewLessonTools); a nil
// logger is substituted with the silent NoOp logger.
func NewPyFSRS(exec pybridge.Executor, logger infra.Logger) (*PyFSRS, error) {
	if exec == nil {
		return nil, errors.New("rt: pyfsrs requires a non-nil pybridge executor")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &PyFSRS{exec: exec, logger: logger}, nil
}

// FetchDueWords returns up to limit due vocabulary cards from the FSRS
// store in category (Issue #10: "" normalizes to the script's english
// default — same call as before the field existed). A limit <= 0 falls back
// to the FSM's own cap (dueWordsLimit = 10). RunDue is ALWAYS called with an
// explicit FetchLockHours=1 (never the script's 24h default, never a peek):
// the lock serializes one fetch per card for the classroom window while
// bounding leftover locks to one hour. Cards map into lesson.Word
// field-for-field (word/meaning/example — the json tags were aligned
// losslessly in 13-07) and keep the script's order. Zero cards is a legal
// result (no due words today), not an error.
func (p *PyFSRS) FetchDueWords(ctx context.Context, limit int, category string) ([]lesson.Word, error) {
	if limit <= 0 {
		limit = dueWordsLimit
	}
	res, err := p.exec.RunDue(ctx, pybridge.DueOpts{
		Limit:          limit,
		FetchLockHours: pyfsrsFetchLockHours,
		Category:       lesson.NormalizeCategory(category),
	})
	if err != nil {
		return nil, errors.Wrap(err, "rt: pyfsrs fetch due words")
	}
	return dueCardsToWords(res.Cards), nil
}

// PeekDueWords implements the lock-free half of the WordSource seam (Issue
// #55): the same due read as FetchDueWords but with Peek set, so the auto
// subject resolution's presence probe never locks cards away from a live
// classroom. Zero cards is a legal result, not an error.
func (p *PyFSRS) PeekDueWords(ctx context.Context, limit int, category string) ([]lesson.Word, error) {
	if limit <= 0 {
		limit = dueWordsLimit
	}
	res, err := p.exec.RunDue(ctx, pybridge.DueOpts{
		Limit:    limit,
		Peek:     true,
		Category: lesson.NormalizeCategory(category),
	})
	if err != nil {
		return nil, errors.Wrap(err, "rt: pyfsrs peek due words")
	}
	return dueCardsToWords(res.Cards), nil
}

// dueCardsToWords maps pybridge DueCards onto lesson.Word field-for-field
// (word/meaning/example — the json tags were aligned losslessly in 13-07),
// keeping the script's order.
func dueCardsToWords(cards []pybridge.DueCard) []lesson.Word {
	words := make([]lesson.Word, 0, len(cards))
	for _, card := range cards {
		words = append(words, lesson.Word{
			Word:    card.Word,
			Meaning: card.Meaning,
			Example: card.Example,
		})
	}
	return words
}

// SubmitReview writes one review batch (word → good/hard/again/easy rating)
// in category and reports what ACTUALLY happened. An empty results map is a
// no-op: it returns the zero result WITHOUT spawning a python subprocess.
//
// Category (Issue #10, AC4 写错历史): the script's review lookup prefers the
// english column when --category is omitted, so a Chinese classroom's
// ratings MUST ride an explicit chinese category — "" normalizes to english,
// the pre-Issue-#10 behavior.
//
// A nil error NEVER means "everything saved": the returned sets are the
// truth — Requested is the submitted keys, Updated the words the backend
// really refreshed (in the script's reported order), Missing the requested
// words it did not (already reviewed today, absent from the store, ...).
// Partial and zero Updated with a nil error are legal outcomes the 13-08
// completion path narrates honestly ("本次保存 M/N 个" / 记录未保存后缀).
// Exactly one RunReview is issued — no blind batch retry.
//
// Updated words that were never requested, or that repeat, violate the
// provider contract and surface as a provider-contract error (not retried)
// rather than being silently folded into the sets.
func (p *PyFSRS) SubmitReview(ctx context.Context, results map[string]string, category string) (ReviewWriteResult, error) {
	if len(results) == 0 {
		return ReviewWriteResult{}, nil
	}
	res, err := p.exec.RunReview(ctx, pybridge.ReviewOpts{
		Results:  results,
		Category: lesson.NormalizeCategory(category),
	})
	if err != nil {
		return ReviewWriteResult{}, errors.Wrap(err, "rt: pyfsrs submit review")
	}
	requested := make([]string, 0, len(results))
	for word := range results {
		requested = append(requested, word)
	}
	sort.Strings(requested) // deterministic reporting; membership is what matters
	updatedSet := make(map[string]bool, len(res.Updated))
	updated := make([]string, 0, len(res.Updated))
	for _, card := range res.Updated {
		if _, ok := results[card.Word]; !ok {
			return ReviewWriteResult{}, errors.Errorf(
				"rt: pyfsrs review reported an unrequested word %q (provider contract violation)", card.Word)
		}
		if updatedSet[card.Word] {
			return ReviewWriteResult{}, errors.Errorf(
				"rt: pyfsrs review reported word %q twice (provider contract violation)", card.Word)
		}
		updatedSet[card.Word] = true
		updated = append(updated, card.Word)
	}
	missing := make([]string, 0, len(requested)-len(updated))
	for _, word := range requested {
		if !updatedSet[word] {
			missing = append(missing, word)
		}
	}
	p.logger.Info("pyfsrs review write reported",
		infra.F("component", "rt"),
		infra.F("requested", len(requested)),
		infra.F("updated", len(updated)),
		infra.F("missing", len(missing)),
	)
	return ReviewWriteResult{Requested: requested, Updated: updated, Missing: missing}, nil
}

// Compile-time interface check: the PyFSRS adapter satisfies the dictation
// FSM's word seam (13-10 key_link).
var _ WordSource = (*PyFSRS)(nil)
