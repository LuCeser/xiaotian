// turn.go — the per-connection turn manager (13-04 Task 2; pipeline
// re-arranged by the 13-08 dictation pivot): the single owner of session
// state on top of the /rt transport.
//
// Responsibilities (CONTEXT locked decisions, all landed here):
//   - Dictation FSM: the server owns classroom pacing. session_init fetches
//     due words, establishes the lesson row, answers session_ready and the
//     teacher speaks the opening (word announcement included) immediately —
//     zero LLM calls on that path (first-greeting defect fix, RT-04).
//   - Per-turn pipeline: VAD → ASR → intent classification (one Chat call,
//     strict JSON) → FSM dispatch → speakTurn announcement.
//   - Advance path: the immutable TurnTarget captured at classification
//     start feeds AdvanceWord's idempotency key + optimistic lock; first
//     commit / idempotent replay / version conflict are mutually exclusive
//     branches (T-13-15c) — only the first commit records the outcome,
//     sends lesson_update, and narrates the next word or the completion.
//   - Epoch cancellation: a monotonic counter; interrupt bumps it, cancels
//     the in-flight provider context, invalidates every old-epoch playback
//     wait, and trims the FSM short-history deque to what was actually
//     heard (Pitfall 9). Cancelling speech NEVER rolls back an
//     already-committed advance_word.
//   - Sentence-streamed TTS: subtitles first, audio chunks forwarded the
//     moment they arrive (收即播). Bare s16le PCM crosses this layer
//     (AudioChunkFunc); the MiMo wire's encoded-chunk decoding belongs to
//     the 13-06 MimoTTS adapter alone (review M-1).
//   - Playback-completion gating: every sentence registers a pending entry
//     BEFORE its audio_start is queued; the FSM stays speaking (ordinary
//     uplink gated) until the sentence_ack matching the final sentence
//     arrives (T-13-15b). A bounded wait (3..45s from the outstanding
//     scheduled PCM) degrades via ErrorMsg(playback_ack_timeout) — never a
//     fabricated completion (protocol.go sequencing contract).
//   - Half-duplex gating: uplink audio is dropped while the teacher speaks
//     (Pattern 3 — echo countermeasure); interrupts travel the client
//     control channel only.
package rt

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
)

// ErrStaleEpoch is returned by TTS chunk callbacks when the epoch they were
// launched under no longer matches the session's current epoch — the caller
// discards the late output instead of playing it (Pitfall 4: old audio must
// not resurrect).
var ErrStaleEpoch = errors.New("rt: stale epoch")

// LessonExecutor is the classroom-state seam the turn manager depends on
// (13-01). Signatures align verbatim with pkg/lesson's methods so 13-06 can
// bind them with a compile-time check; EnsureLessonCards lives on
// *lesson.Store and the read/write pair on *lesson.Executor, so the
// production implementation is a small adapter over both.
type LessonExecutor interface {
	// EnsureLesson creates the lesson row if absent and returns it (first
	// stored content wins).
	EnsureLesson(ctx context.Context, lessonID string, words []string) (lesson.Lesson, error)
	// GetLessonState returns the current lesson row.
	GetLessonState(ctx context.Context, lessonID string) (lesson.Lesson, error)
	// EnsureLessonCards creates the lesson row carrying word cards and the
	// classroom's subject (Issue #10) if absent and returns it (first stored
	// content wins; a legacy row with empty cards is NOT backfilled — the
	// caller detects it).
	EnsureLessonCards(ctx context.Context, lessonID string, cards []lesson.Word, category string) (lesson.Lesson, error)
	// AdvanceWord moves the cursor forward one word, idempotently by
	// requestID, guarded by expectedVersion.
	AdvanceWord(ctx context.Context, lessonID string, expectedVersion int, requestID string) (lesson.AdvanceResult, error)
	// AdvanceDictation atomically persists the current word's behavior,
	// advances the fixed target, and opens checking on the last word.
	AdvanceDictation(ctx context.Context, in lesson.AdvanceDictationInput) (lesson.AdvanceDictationResult, error)
	// EnterCheck opens the check stage (13.1-01): persists stage='checking'
	// plus the behavior snapshot, idempotently by requestID.
	EnterCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, outcomes map[string]lesson.WordOutcome) (lesson.EnterCheckResult, error)
	// ApplyVerdict upserts one check-stage verdict, idempotently by
	// requestID, and returns the FULL stored verdict list.
	ApplyVerdict(ctx context.Context, lessonID string, expectedVersion int, requestID string, verdict lesson.CheckVerdict) (lesson.ApplyVerdictResult, error)
	// CompleteCheck flips stage checking→submitted — the unified-submit
	// idempotency latch (only its first commit may drive the one FSRS
	// write).
	CompleteCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, unchecked bool) (lesson.CompleteCheckResult, error)
	// ClearUnchecked resets the D-03 unchecked marker (13.1-04) once the
	// correction session confirmed every word and recorded its diff.
	ClearUnchecked(ctx context.Context, lessonID string, expectedVersion int, requestID string) (lesson.ClearUncheckedResult, error)
	// EnsurePoemLesson creates the whole-poem 背默 attempt row (Issue #12):
	// the poem's ordered LINES ride as the cards, the subject is always
	// 语文, poem_id stamps the work; first stored content wins.
	EnsurePoemLesson(ctx context.Context, lessonID string, poemID int64, lines []string) (lesson.Lesson, error)
	// LatestPoemLessonID returns the poem's most recent attempt row id (""
	// when never practiced) — the poem session's resume probe (an in-flight
	// attempt reopens under its own id; a submitted one stays settled).
	LatestPoemLessonID(ctx context.Context, poemID int64) (string, error)
	// EnsurePassageLesson creates the passage-range 背默 attempt row (Issue
	// #14): the range's own ordered PARAGRAPHS ride as the cards and the
	// content + range identity is stamped on the row; first stored content
	// wins.
	EnsurePassageLesson(ctx context.Context, lessonID string, passageID, scopeID int64, whole bool, paragraphs []int, texts []string, category string) (lesson.Lesson, error)
	// LatestPassageLessonID returns the passage RANGE's most recent attempt
	// row id ("" when never practiced) — the passage session's resume probe,
	// keyed by content + range (never the scope row id).
	LatestPassageLessonID(ctx context.Context, passageID int64, whole bool, paragraphs []int) (string, error)
}

// TurnManagerConfig configures one TurnManager.
type TurnManagerConfig struct {
	// Practice is the vocabulary read seam for 「再练一组」 (13.1-05, D-11):
	// random practice picks List the FULL vocabulary through it — never the
	// due fetch (RunDue takes the lock and only returns due words). The
	// production implementation is the CardRepository instance the parent
	// pages already use. Nil Practice degrades practice_init to a coded
	// honest error — never a silent fallback into a due lesson.
	Practice PracticeSource
	// Words is the due-word source (WordSource). Nil answers session_init
	// with lesson_unavailable — the classroom cannot start without it.
	// The 13-10 PyFSRS adapter injects the production implementation.
	Words WordSource
	// Examples is the on-demand example-sentence context seam (Issue #9,
	// ADR-0006): meaning/example intents in the dictation stage resolve the
	// word's textbook background and learned-word sample through it before
	// the one generation call. Nil disables generation — the classification
	// fallback replies still speak honestly (degraded wiring, tests).
	Examples ExampleSource
	// Receipts is the persistent save-receipt store (13.3-01): every real
	// save attempt (check / correction / unchecked_fallback / practice)
	// records a two-phase lesson_save_receipts row — pending before the
	// downstream write, saved/failed with the real word sets after — so
	// 「已提交」 and 「已写入 FSRS」 stay distinguishable across crashes. Nil =
	// receipts disabled (degraded/test constructions): submissions proceed
	// exactly as before, only unrecorded. A receipt write failure is a
	// bypass event, never a submission error (S-05).
	Receipts *lesson.ReceiptStore
	// PracticeStore is the Go-owned practice-record write seam (13.1-05,
	// D-12): practice results land HERE and only here — never review_logs,
	// never the FSRS schedule. A practice session without it answers the
	// same honest degraded error as a nil Practice source.
	PracticeStore *lesson.PracticeStore
	// Poems is the whole-poem read seam (Issue #12): a poem session_init
	// resolves the work (lines, scope verdict) through it. Nil answers the
	// poem init with a coded honest error — never a silent fallback into a
	// due-word lesson.
	Poems PoemSource
	// PoemPractice is the Go-owned whole-poem practice-record write seam
	// (Issue #12): ONE row per 背默 submit, keyed by the poem — never the
	// word-shaped practice_logs, never the FSRS schedule (poems are not
	// cards). Nil means the poem classroom's save path is unwired: the
	// submit then reports the honest zero-saved result.
	PoemPractice PoemPracticeRecorder
	// Passages is the passage read seam (Issue #14): a passage session_init
	// resolves the work (paragraphs, scope verdict, stored ranges) through
	// it. Nil answers the passage init with a coded honest error — never a
	// silent fallback into a due-word lesson.
	Passages PassageSource
	// PassagePractice is the Go-owned passage practice-record write seam
	// (Issue #14): ONE row per 背默 submit, keyed by content + range —
	// never the word-shaped practice_logs, never the FSRS schedule
	// (passages are not cards). Nil means the passage classroom's save path
	// is unwired: the submit then reports the honest zero-saved result.
	PassagePractice PassagePracticeRecorder
	// OCR is the photo-check vision seam (13.1-03): the raw-model-reply
	// func the manager wraps in an ocr.Pipeline. Nil disables the photo
	// check path — the check stage keeps working manual-only (RT-10
	// degraded semantics; main.go injects cmd/service's mimoReader).
	OCR OCRReader
	// LessonID pins the server-owned lesson this session binds to. Empty
	// means derive "r0-YYYYMMDD" on the Beijing calendar (same-day
	// reconnects resume the same lesson).
	LessonID string
	// VADStartGraceMs is the uplink start-grace window in milliseconds
	// after session_ready (0 → 1000ms default): mic-open transients must
	// not cut a phantom turn. A real-device calibration value (13-12).
	VADStartGraceMs int
	// PhotoConfidence is the OCR prefill confidence floor (0 → 0.6,
	// PhotoConfig.ConfidenceThreshold semantics).
	PhotoConfidence float64
	// PhotoMaxBytes caps one assembled photo's decoded size (0 → 1MiB,
	// PhotoConfig.MaxImageBytes semantics; T-13.1-06). Over-cap uploads are
	// rejected AND cleared.
	PhotoMaxBytes int
	// PhotoRequestTimeout bounds one vision OCR call (0 → the photo config
	// block's RequestTimeout default, PhotoConfig.RequestTimeout semantics):
	// a wedged upstream must not pin the OCR goroutine forever. Issue #44:
	// no second frozen literal — the fallback IS the config default.
	PhotoRequestTimeout time.Duration
	// VADEchoTailMs is the post-ack echo tail in milliseconds (0 → 500ms
	// default): ordinary uplink stays gated this long after the VALID
	// playback-completion ack of a teacher announcement. A real-device
	// calibration value (13-12).
	VADEchoTailMs int
	// WordTimeout bounds how long the FSM waits for a per-word confirm
	// before auto-advancing (0 → 60s default). 13-08 defines the field;
	// the timer behavior itself lands in 13-09.
	WordTimeout time.Duration
	// ForceStudent is the 13.3-05 production anti-fat-finger switch (ROADMAP
	// success criterion 6): when true, session_init's client-supplied mode is
	// IGNORED and the classroom always runs the student projection — the
	// mode=test full-reveal behavior disappears. Wired from
	// infra.RTConfig.ForceStudentMode through cmd/service/main.go (rtTurnCfg).
	// Default false keeps the existing development flow untouched (13.3-01
	// checkpoint ① user-confirmed direction). Anti-fat-finger, NOT auth:
	// no PIN, no session, no API gating (consensus decision 3).
	ForceStudent bool
}

// defaultWordTimeout is the per-word confirm wait used when
// TurnManagerConfig.WordTimeout is unset (13-09 arms the timer).
const defaultWordTimeout = 60 * time.Second

// VAD gating window defaults (milliseconds): the session_ready start grace
// and the post-ack echo tail. Both are starting values awaiting real-device
// calibration (13-12) — the WINDOW CONTROL is already ack-driven here.
const (
	defaultVADStartGraceMs = 1000
	defaultVADEchoTailMs   = 500
)

// Announcement purposes — the playback-completion await behaves differently
// for ordinary announcements (timeout → orderly close) versus natural
// endings (timeout → the original lesson_complete/no_due_words reason still
// ships; protocol.go sequencing contract).
const (
	purposeGreeting   = "greeting"     // opening + first word
	purposeAnnounce   = "announce"     // next-word announcement
	purposeChat       = "chat"         // classification reply
	purposeSummary    = "summary"      // completion wrap-up
	purposeNoDue      = "no_due_words" // zero-due explanation
	purposeSubjectAsk = "subject_ask"  // Issue #55: which-subject clarify question
)

// maxReplyRunes bounds an LLM reply before it enters TTS and subtitles
// (T-13-15: untrusted model output is length-capped at one choke point).
const maxReplyRunes = 500

// lessonInitLocks serializes session-init classroom initialization per
// lesson id across this process's concurrent connections: with the lock
// held, a second connection's GetLessonState sees the row the first creator
// wrote, so only ONE creator runs the due fetch — two simultaneous
// connections must not each walk off a batch of unused words.
var lessonInitLocks sync.Map // lesson id → *sync.Mutex

func lockLessonInit(lessonID string) func() {
	v, _ := lessonInitLocks.LoadOrStore(lessonID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// sessionCounter disambiguates session ids within one process.
var sessionCounter atomic.Uint64

// sentenceInfo is one spoken sentence of the current announcement, kept so
// an interrupt can trim the FSM short history to the sentences actually
// played (Pitfall 9).
type sentenceInfo struct {
	id   string
	text string
}

// pendingPlayback tracks one scheduled sentence awaiting its
// actual-playback-completion ack (T-13-15b). The entry is registered BEFORE
// the sentence's audio_start is queued, so an ack can never outrun its
// pending entry; done is closed exactly once — with completed set on a real
// ack, left false on interrupt/disconnect cancellation (waiters wake and
// re-check either way; the sticky completed bit means a final ack that
// arrives before the await call is never lost).
type pendingPlayback struct {
	done       chan struct{}
	purpose    string
	pcmSamples int // scheduled PCM sample count (s16le mono: bytes/2)
	completed  bool
}

// finish marks the pending completed (a valid ack consumed it).
// Caller holds tm.mu.
func (p *pendingPlayback) finish() {
	if !p.completed {
		p.completed = true
		close(p.done)
	}
}

// cancelWait wakes the awaiter without completion (interrupt / disconnect).
// Caller holds tm.mu.
func (p *pendingPlayback) cancelWait() {
	if !p.completed {
		close(p.done)
	}
}

func pendingKey(epoch uint64, sentenceID string) string {
	return fmt.Sprintf("%d/%s", epoch, sentenceID)
}

// PlaybackRef identifies one multi-sentence announcement for the
// playback-completion wait: the final sentence's identity plus every
// sentence scheduled in the announcement (their still-unacked PCM samples
// drive the bounded wait — never the last short sentence alone).
type PlaybackRef struct {
	FinalSentenceID string
	SentenceIDs     []string
	Epoch           uint64
}

// Playback ack wait bounds: derived from all outstanding scheduled PCM plus
// margin, clamped to a bounded band (plan: 3..45s).
const (
	playbackAckMargin  = 3 * time.Second
	playbackAckMinWait = 3 * time.Second
	playbackAckMaxWait = 45 * time.Second
)

// playbackAckTimeout derives the bounded wait for one announcement.
func playbackAckTimeout(pcmSamples int) time.Duration {
	d := playbackAckMargin
	if pcmSamples > 0 {
		d += time.Duration(pcmSamples) * time.Second / TTSOutputSampleRate
	}
	if d < playbackAckMinWait {
		d = playbackAckMinWait
	}
	if d > playbackAckMaxWait {
		d = playbackAckMaxWait
	}
	return d
}

// TurnManager implements SessionManager for one /rt connection.
//
// Concurrency: HandleControl/IngestAudio run on the transport read loop;
// processTurn, greeting/completion goroutines (and their TTS callbacks) run
// on separate goroutines. mu guards every field below — including all
// DictationTutor state (single-lock discipline, see dictation.go). The VAD
// is fed from the read loop but Reset also runs on close paths driven from
// announcement goroutines, so the VAD carries its own leaf mutex (vad.go).
// Provider calls (ASR/LLM/TTS) always happen OUTSIDE mu; their results
// re-acquire it. SendControl/SendAudio are non-blocking queue enqueues, so
// calling them under mu is safe and makes "epoch check + send" atomic with
// respect to interrupt handling — after HandleControl(interrupt) returns,
// no further chunk or subtitle of the interrupted announcement can be
// emitted.
type TurnManager struct {
	// echoTailUntil gates ordinary uplink through the echo tail that starts
	// at the valid playback ack.
	echoTailUntil time.Time

	// sessionReadyAt stamps when session_ready went out; ordinary uplink is
	// dropped until the start grace elapses (mic-open transients).
	sessionReadyAt time.Time
	// lastPlaybackCompletedAt is the moment the last VALID playback ack
	// landed. Written ONLY by the ack-driven settle path — never from a
	// speakTurn return or an audio_end send.
	lastPlaybackCompletedAt time.Time
	llm                     LLMProvider
	tts                     TTSSynthesizer
	exec                    LessonExecutor

	// sender is the connection's write half, retained at session_init for
	// announcements that have no request context behind them — the 13-09
	// timeout auto-advance. It lives exactly as long as the session.
	sender Sender
	logger infra.Logger
	asr    ASRProvider

	// ocrPipeline wraps cfg.OCR for the check pass; nil when photo check is
	// disabled (no MIMO_API_KEY).
	ocrPipeline *ocr.Pipeline
	recorder    *EventRecorder

	// vad is fed from the read loop; its own mutex covers the Reset calls
	// that arrive from close paths on other goroutines (see concurrency note).
	vad *VAD
	// turnCancel cancels the current turn's/announcement's provider context.
	turnCancel context.CancelFunc
	// wordTimer is the live countdown handle; nil when disarmed.
	wordTimer *time.Timer
	// tutor is the dictation FSM state; nil until a lesson session starts.
	tutor *DictationTutor
	// timerFactory builds the countdown timers (test seam); nil means
	// time.AfterFunc.
	timerFactory func(time.Duration, func()) *time.Timer
	// subjectTakeInterleave (test seam, PR #60 review; nil in production)
	// fires between takeSubjectAsk's pendingAsk consumption and its epoch
	// bump. The deterministic race tests probe the lock discipline from
	// here — a TryLock on tm.mu succeeds only if the implementation dropped
	// the hold between the two steps, and the probe then runs the ask's
	// OLD-epoch timeout close synchronously in that gap, so a split
	// implementation turns those tests red.
	subjectTakeInterleave func()
	// dispatchInterleave is a narrow test probe after classification's
	// liveness check and before intent dispatch.
	dispatchInterleave func()
	// turnAnnouncementInterleave is a narrow test probe immediately before
	// an ordinary classroom announcement checks its original epoch.
	turnAnnouncementInterleave func()
	// starterInterleave gates a registered starter task before it runs.
	starterInterleave func(func())
	// speakTurnInterleave and speakRegistrationInterleave are narrow test
	// probes around speakTurn's initial registration.
	speakTurnInterleave         func()
	speakRegistrationInterleave func()
	// settleInterleave is a narrow test probe between an announcement's
	// valid final ack (await returned) and its protected settle.
	settleInterleave func()
	// checkPresentationInterleave is a narrow test probe after EnterCheck (or
	// its conflict read-back) returns and before stored state is presented.
	checkPresentationInterleave func()
	// checkResumeInterleave fires after resume IO and before panel binding.
	checkResumeInterleave func()
	// checkSubmitPresentationInterleave is a narrow test probe after a review
	// save completes and before its result is presented.
	checkSubmitPresentationInterleave func()
	// checkSubmitSummaryInterleave probes the gap after review_result is
	// accepted and before the ending announcement starts.
	checkSubmitSummaryInterleave func()
	// closeEntryInterleave (test seam, issue #69 备选一 步骤 8; nil in
	// production) fires inside the unified close entry after the init-write
	// lock is taken and BEFORE tm.mu — the last window in which a racing
	// check-stage presentation can still land ahead of the claim's
	// eligibility/snapshot decision. The deterministic race tests probe the
	// claim's one-critical-section contract from here.
	closeEntryInterleave func()

	// photo is the in-flight upload's assembler buffer; nil when idle.
	// PRIVACY P2: the decoded chunks live ONLY here until the one OCR call
	// consumes them; close/cancel/over-cap/new-upload all discard.
	photo           *photoBuffer
	acked           map[string]bool
	pendingPlayback map[string]*pendingPlayback
	session         string
	// state is one of StateListening/StateProcessing/StateSpeaking, or ""
	// before session_init / after close.
	state string
	// mode is the page-supplied UI mode (13.1 D-08, normalized to
	// student/test at session_init); plan 02's masking consumes it.
	mode string
	// category is the classroom's subject (Issue #10, normalized to
	// english/chinese at session_init from the init message — and from the
	// lesson ROW on every resume/correction/completed reconnect, so a
	// post-hoc correction submits its review under the subject the lesson
	// was actually taught in). Everything subject-scoped reads it: due
	// fetch, review write, practice pick, no-due copy, session_ready echo.
	// (例句生成 stays English-scoped by design — the ExampleResolver
	// grounds on English cards and a 语文 word degrades honestly to the
	// classification fallback, per AC2's 不强制英语式例句.)
	category string
	// pendingAsk (Issue #55): the parked session_init of an auto word
	// classroom whose TWO subjects both hold due words — the clarify is
	// out, and the child's answer (subject_pick or spoken 语文/英语)
	// binds category and resumes through continueSessionInit. Non-nil ONLY
	// while the ask is live; the classroom has no lesson row yet.
	pendingAsk *SessionInitMsg
	// pendingAdvance is the one accepted dictation write that later turns
	// and reconnect reconciliation must wait for before reading a target.
	pendingAdvance *pendingDictationAdvance
	// turnSentences are the sentences sent for the current announcement, in
	// order (the interrupt-trim source).
	turnSentences []sentenceInfo
	cfg           TurnManagerConfig

	// timerGen is the monotonic per-word timer generation. EVERY
	// arm/reset/stop increments it, so a callback already queued before the
	// reset is dead on arrival even with the same epoch and word index
	// (T-13-16).
	timerGen uint64
	// droppedUplink counts audio chunks dropped by half-duplex gating.
	droppedUplink int
	// turnID counts turns and announcements (sentence-id namespace).
	turnID uint64
	// epoch is the monotonic cancellation token. In-flight provider work
	// remembers the epoch it launched under; on mismatch it aborts.
	epoch uint64
	// ocrRuns disambiguates prefill idempotency keys across retakes (a
	// replayed key would pin the FIRST reading and drop the retake's).
	ocrRuns uint64

	// initWriteMu serializes the classroom-init ROW WRITES (EnsureLessonCards
	// in continueSessionInit and handlePracticeInit) against every close
	// path, which must take it BEFORE marking closed. Fixed order:
	// initWriteMu → mu — the starters hold it around the short DB write only
	// (never while taking mu), so a close that arrives mid-write blocks until
	// the row materializes ahead of session_ended, and a close that arrives
	// first lets the init's re-check refuse with no row at all (#68).
	initWriteMu sync.Mutex
	mu          sync.Mutex
	// playbackPending marks an announcement awaiting its final ack; uplink
	// stays gated while it holds.
	playbackPending bool
	// closed suppresses duplicate session_end emissions when the transport
	// calls Close after an end_session already closed the session.
	closed bool
	// closeOwned marks the close path that won the ONE-TIME D-03 fallback
	// ownership (uncheckedCloseFallback). Guarded by mu.
	closeOwned bool
}

// Compile-time check: TurnManager satisfies the transport's SessionManager
// contract (13-03 key_link).
var _ SessionManager = (*TurnManager)(nil)

// NewTurnManager builds a turn manager. A nil asr/llm/tts still constructs —
// the session then answers session_init with ErrCodeVoiceUnavailable (RT-10
// degraded semantics: no MIMO_API_KEY configured server-side; 13-06 uses
// this). A nil Words answers lesson_unavailable (T-13-14: honest failure
// until the 13-10 adapter is wired). A nil logger becomes the no-op logger
// and a nil recorder a no-session recorder (D-42).
func NewTurnManager(asr ASRProvider, llm LLMProvider, tts TTSSynthesizer, exec LessonExecutor, cfg TurnManagerConfig, recorder *EventRecorder, logger infra.Logger) *TurnManager {
	if cfg.WordTimeout <= 0 {
		cfg.WordTimeout = defaultWordTimeout
	}
	if cfg.VADStartGraceMs <= 0 {
		cfg.VADStartGraceMs = defaultVADStartGraceMs
	}
	if cfg.VADEchoTailMs <= 0 {
		cfg.VADEchoTailMs = defaultVADEchoTailMs
	}
	if cfg.PhotoMaxBytes <= 0 {
		cfg.PhotoMaxBytes = defaultPhotoMaxBytes
	}
	if cfg.PhotoConfidence <= 0 {
		cfg.PhotoConfidence = defaultPhotoConfidence
	}
	if cfg.PhotoRequestTimeout <= 0 {
		// Issue #44: the fallback references the photo config block's own
		// default instead of a second frozen literal here, so the /rt photo
		// path follows the config (20s today, 60s once #48 lands).
		cfg.PhotoRequestTimeout = infra.DefaultConfig().Photo.RequestTimeout
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	if recorder == nil {
		recorder = NewEventRecorder(logger, "")
	}
	// The photo-check pipeline wraps the raw reader seam (13.1-03); the
	// identical underlying func type converts explicitly.
	var ocrPipeline *ocr.Pipeline
	if cfg.OCR != nil {
		ocrPipeline = ocr.NewPipeline(ocr.Reader(cfg.OCR), cfg.PhotoConfidence)
	}
	return &TurnManager{
		asr:             asr,
		llm:             llm,
		tts:             tts,
		exec:            exec,
		cfg:             cfg,
		recorder:        recorder,
		logger:          logger,
		session:         fmt.Sprintf("rt-%d", sessionCounter.Add(1)),
		acked:           map[string]bool{},
		pendingPlayback: map[string]*pendingPlayback{},
		vad:             NewVAD(DefaultVADConfig()),
		ocrPipeline:     ocrPipeline,
	}
}

// ---------------------------------------------------------------------------
// SessionManager: control path
// ---------------------------------------------------------------------------

// HandleControl dispatches one JSON control payload (the transport hands raw
// TextMessage bodies; parse failures answer a coded ErrorMsg).
func (tm *TurnManager) HandleControl(s Sender, data []byte) {
	msg, err := ParseControl(data)
	if err != nil {
		tm.sendError(s, ErrCodeInvalidMessage, "无法识别的控制消息", true)
		return
	}
	switch m := msg.(type) {
	case SessionInitMsg:
		tm.handleSessionInit(s, m)
	case InterruptMsg:
		tm.handleInterrupt(s, m)
	case SentenceAckMsg:
		tm.handleSentenceAck(m)
	case PingMsg:
		tm.handlePing(s, m)
	case EndSessionMsg:
		tm.handleEndSession(s)
	case ReadWordMsg:
		tm.handleReadWord(s, m)
	case CheckVerdictMsg:
		tm.handleCheckVerdict(s, m) // 13.1-01 check stage (check.go)
	case CheckSubmitMsg:
		tm.handleCheckSubmit(s, m) // 13.1-01 unified submit (check.go)
	case PhotoChunkMsg:
		tm.handlePhotoChunk(s, m) // 13.1-03 photo check (photo.go)
	case PhotoCancelMsg:
		tm.handlePhotoCancel(s, m) // 13.1-03 retake discipline (photo.go)
	case SubjectPickMsg:
		tm.handleSubjectPick(s, m) // Issue #55: the clarify's button answer
	default:
		// Server→client types parse but are not accepted from a client;
		// the transport only moves bytes — this is the enforcement point.
		tm.sendError(s, ErrCodeInvalidMessage, "该消息类型不能由客户端发送", true)
	}
}

// handleSessionInit validates the negotiated audio format, then establishes
// the classroom under the per-lesson init lock: one creator runs the due
// fetch; legacy-empty and already-completed rows take their explicit paths.
// A practice=true init (13.1-05) branches off BEFORE the due-fetch flow —
// practice never touches FetchDueWords nor the r0-YYYYMMDD namespace.
func (tm *TurnManager) handleSessionInit(s Sender, m SessionInitMsg) {
	if err := ValidateAudioInit(m.Audio); err != nil {
		tm.sendError(s, ErrCodeUnsupportedFormat, "仅支持 16000Hz、s16le、单声道的上行音频", true)
		return
	}
	if tm.asr == nil || tm.llm == nil || tm.tts == nil {
		// RT-10 degraded semantics: the voice path is disabled server-side.
		tm.sendError(s, ErrCodeVoiceUnavailable, "服务器未配置语音服务密钥，语音模式不可用", false)
		return
	}
	if tm.exec == nil {
		tm.sendError(s, ErrCodeProviderFailed, "服务器课堂模块未就绪", false)
		return
	}

	// Retain the write half for server-initiated announcements that have no
	// request context behind them (the 13-09 timeout auto-advance), the
	// page's UI mode (13.1 D-08: "test" keeps today's full-reveal behavior;
	// absent/invalid → student — the iPad bookmark must never leak words)
	// and the classroom's subject (Issue #10: "chinese" opens the 语文
	// classroom; Issue #55: "auto" enters the child-asks clarify flow;
	// absent/invalid → english, the pre-#10 landing).
	// 13.3-05: the production force switch short-circuits the SAME single
	// adjudication point — when cfg.ForceStudent is set, the client-supplied
	// mode is ignored and every session runs the student projection (the
	// mode=test full-reveal behavior cannot exist server-side). Absent, the
	// normalizeSessionMode semantics below are untouched.
	tm.mu.Lock()
	tm.sender = s
	if tm.cfg.ForceStudent {
		tm.mode = ModeStudent
	} else {
		tm.mode = normalizeSessionMode(m.Mode)
	}
	tm.category = sessionCategoryFromWire(m.Category)
	tm.mu.Unlock()

	// Issue #55: an unspecified subject on a WORD classroom (dictation or
	// practice — never the 背默 branches, whose subject is the work's own)
	// is settled by the CHILD, never a model: the one subject holding due
	// words opens directly; both subjects holding due words asks the child
	// and the classroom parks until the answer; neither holds due words
	// keeps the english landing and its ordinary no-due ending. A declared
	// english/chinese never reaches the ask — the user's specification is
	// final (AC2).
	if m.Poem == 0 && m.Passage == 0 && tm.sessionCategory() == CategoryAuto {
		resolved, ask := tm.resolveAutoSubject()
		if ask {
			tm.startSubjectAsk(s, m)
			return
		}
		tm.mu.Lock()
		tm.category = resolved
		tm.mu.Unlock()
	}

	tm.continueSessionInit(s, m)
}

// continueSessionInit runs everything session_init does once the classroom's
// subject is settled: the practice / poem / passage branches, then the
// due-word classroom itself (row resume vs. fresh fetch). It is ALSO the
// resume point after a subject clarify answer (Issue #55) — an auto
// classroom whose two subjects both hold due words asks the child first,
// then re-enters here under the picked subject with the ORIGINAL init
// message, so the answered round binds exactly like an explicit declaration.
func (tm *TurnManager) continueSessionInit(s Sender, m SessionInitMsg) {
	if m.Practice {
		tm.handlePracticeInit(s)
		return
	}

	// Issue #12: a whole-poem 背默 classroom branches BEFORE the due-word
	// flow — poems never enter FetchDueWords (they are not cards), and the
	// poem row's own resume/correction branches live in handlePoemInit.
	if m.Poem > 0 {
		tm.handlePoemInit(s, m.Poem)
		return
	}

	// Issue #14: a passage-range 背默 classroom branches the same way — the
	// selected paragraphs never enter FetchDueWords, and the range's own
	// resume/correction branches live in handlePassageInit.
	if m.Passage > 0 {
		tm.handlePassageInit(s, m.Passage, m.PassageScope)
		return
	}

	if tm.cfg.Words == nil {
		// T-13-14: fixed human wording; the adapter is wired by 13-10.
		tm.sendError(s, ErrCodeLessonUnavailable, lessonUnavailableNotice, false)
		return
	}

	category := tm.sessionCategory()
	lessonID := lessonIDForCategory(tm.cfg.LessonID, time.Now(), category)

	unlock := lockLessonInit(lessonID)
	defer unlock()

	ctx := context.Background()
	l, err := tm.exec.GetLessonState(ctx, lessonID)
	if err != nil && !errors.Is(err, lesson.ErrLessonNotFound) {
		tm.failLessonInit(s)
		return
	}

	if err == nil {
		// Existing row: the ROW is the subject authority from here on — a
		// reconnect echoes and reviews under the subject the lesson was
		// actually created under, not whatever the page re-declared.
		tm.mu.Lock()
		tm.category = lesson.NormalizeCategory(l.Category)
		tm.mu.Unlock()
	} else {
		// Fresh lesson: this process instance is the single creator (init
		// lock held) — fetch due words now, scoped to the same subject.
		cards, ferr := tm.cfg.Words.FetchDueWords(ctx, dueWordsLimit, category)
		if ferr != nil {
			tm.failLessonInit(s)
			return
		}
		if len(cards) == 0 {
			tm.startNoDueSession(s, lessonID)
			return
		}
		// #68 / path B: the row write is serialized against every close path
		// by the dedicated init-write lock (order: initWriteMu → mu). A
		// close that arrived first marks closed and this re-check refuses —
		// no row for a dead session; a write that started first completes
		// BEFORE the close can mark anything (the close blocks on
		// initWriteMu), so the row never materializes behind session_ended's
		// back. The write itself runs without tm.mu.
		tm.initWriteMu.Lock()
		if !tm.stillOpen() {
			tm.initWriteMu.Unlock()
			return
		}
		l, err = tm.exec.EnsureLessonCards(ctx, lessonID, cards, category)
		tm.initWriteMu.Unlock()
		if err != nil {
			// RunDue succeeded but the lesson row did not materialize; the
			// 13-10 fetch lock (1h) may linger — recorded honestly below.
			// R0 does NOT pretend this is recoverable in-process.
			tm.failLessonInit(s)
			return
		}
	}

	switch {
	case len(l.Cards) == 0:
		// Legacy empty cards (e.g. r0-test rows created before cards
		// existed): INSERT OR IGNORE is not a replacement mechanism (13-07
		// frozen boundary). Visible failure — never a re-RunDue, never a
		// fake backfill.
		tm.failLessonInit(s)
	case l.Stage == lesson.StageChecking:
		// 13.1-04 (Pitfall 2): the check never finished and the close
		// fallback never landed — reopen the check panel from the row.
		tm.startCheckResume(s, l)
	case l.Stage == lesson.StageSubmitted && l.Unchecked:
		// 13.1-04 (D-03): the fallback rated this lesson unchecked —
		// reopen as the post-hoc correction instead of short-circuiting.
		tm.startCheckCorrection(s, l)
	case l.CurrentIndex >= len(l.Cards):
		// Legacy + normally completed rows short-circuit exactly as before
		// (stage first, index second — the new branches never swallow them).
		tm.startCompletedSession(s, l)
	default:
		tm.startDictationSession(s, l, false)
	}
}

// failLessonInit is the single classroom-init failure egress: a coded,
// fixed-wording ErrorMsg plus the server-side error event (T-13-14). PR #60
// review: a session that closed under the init IO gets no failure message
// either — nothing follows its session_ended.
func (tm *TurnManager) failLessonInit(s Sender) {
	if !tm.applySessionEffect(func() error {
		return s.SendControl(ErrorMsg{
			Type: TypeError, Code: ErrCodeLessonUnavailable, Message: lessonUnavailableNotice, Recoverable: false,
		})
	}) {
		return
	}
	tm.recorder.Emit(EventError, infra.F("code", ErrCodeLessonUnavailable))
}

// normalizeSessionMode normalizes the page-supplied UI mode (13.1 D-08):
// only the exact "test" value selects test mode; anything else — including
// an absent field — is student mode. Stored on the TurnManager at
// session_init; 13.1-01 has no visible difference beyond the check panel
// (the masking itself lands in 13.1-02).
func normalizeSessionMode(mode string) string {
	if mode == ModeTest {
		return ModeTest
	}
	return ModeStudent
}

// normalizeSessionCategory was widened into sessionCategoryFromWire
// (subject.go, Issue #55): the deny-by-default english landing for
// absent/off-enum values is unchanged — "auto" is the one addition.

// startNoDueSession answers session_ready with an empty word table, lets
// the teacher explain (zero due words — with the D-13 days variant when the
// vocabulary's due data names the next review), and ends the session only
// after the explanation's actual playback ack (or its bounded timeout).
func (tm *TurnManager) startNoDueSession(s Sender, lessonID string) {
	// sessionCategory/lessonViewFor take tm.mu themselves: read them before
	// the effect (both are session-constant after session_init).
	category := tm.sessionCategory()
	if !tm.applySessionEffect(func() error {
		// PR #60 review: the session may have closed under the init IO that
		// led here — a closed session gets no session_ready and no spoken
		// explanation (nothing may follow its session_ended); the bind and
		// the enqueue are one hold, so the close can no longer land between
		// them either.
		tm.state = StateListening
		tm.sessionReadyAt = time.Now()
		return s.SendControl(SessionReadyMsg{
			Type:      TypeSessionReady,
			SessionID: tm.session,
			Audio:     AudioSpec{SampleRate: requiredSampleRate, Format: requiredFormat, Channels: requiredChannels},
			TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
			Lesson:    LessonView{LessonID: lessonID, Words: []string{}},
			Category:  category,
		})
	}) {
		return
	}
	tm.recorder.Emit(EventConnected, infra.F("session_id", tm.session))

	ctx, cancel := context.WithCancel(context.Background())
	var epoch uint64
	if !tm.applySessionEffect(func() error {
		tm.turnCancel = cancel
		epoch = tm.epoch
		return nil
	}) {
		cancel()
		return
	}
	go tm.runStarterTask(func() {
		tm.runNaturalEndingAt(s, epoch, ctx, cancel, tm.noDueWordsTextForNow(), purposeNoDue, ReasonNoDueWords)
	})
}

// startCompletedSession restores the completion state for an
// already-finished lesson: session_ready then session_ended(lesson_complete)
// — no summary replay, no second SubmitReview.
func (tm *TurnManager) startCompletedSession(s Sender, l lesson.Lesson) {
	// Locked helpers read before the effect (session-constant values).
	view, category := tm.lessonViewFor(l), tm.sessionCategory()
	if !tm.applySessionEffect(func() error {
		// PR #60 review: same gate as the other starters — a closed session
		// replays neither session_ready nor the completion session_ended;
		// the bind and the enqueue are one hold.
		tm.state = StateListening
		tm.sessionReadyAt = time.Now()
		return s.SendControl(SessionReadyMsg{
			Type:      TypeSessionReady,
			SessionID: tm.session,
			Audio:     AudioSpec{SampleRate: requiredSampleRate, Format: requiredFormat, Channels: requiredChannels},
			TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
			Lesson:    view,
			Category:  category,
		})
	}) {
		return
	}
	tm.recorder.Emit(EventConnected, infra.F("session_id", tm.session))
	// The completion goodbye rides the ONE-TIME terminal close: a goodbye
	// that raced in after session_ready already closed the session, and no
	// second session_ended may ship.
	tm.tryTerminalClose(s, ReasonLessonComplete)
}

// startDictationSession binds the FSM to the lesson row and immediately
// speaks the opening greeting (first greeting + first word) — the RT-04
// structure where first-audible latency contains only the FSRS fetch and
// the TTS first packet, no LLM call. practice (13.1-05) flips only the
// submit-path flag; pacing, the check stage and the D-05 gate are identical.
func (tm *TurnManager) startDictationSession(s Sender, l lesson.Lesson, practice bool) {
	// Locked helpers read before the effect (session-constant values).
	view, category := tm.lessonViewFor(l), tm.sessionCategory()
	var first lesson.Word
	var total int
	var mode string
	if !tm.applySessionEffect(func() error {
		// PR #60 review: the subject-picked classroom's init runs on the
		// answer turn's goroutine, so an end_session may have closed the
		// session under the fetch/row IO — the closed check shares the hold
		// that binds the tutor, so a closed session binds nothing, sends no
		// session_ready and never greets; the bind and the enqueue are one
		// hold, so the close can no longer land between them either.
		tm.tutor = newDictationTutor(l)
		tm.tutor.practice = practice
		tm.state = StateListening
		tm.sessionReadyAt = time.Now()
		first = tm.tutor.words[tm.tutor.currentIndex]
		total = len(tm.tutor.words)
		mode = tm.mode
		return s.SendControl(SessionReadyMsg{
			Type:      TypeSessionReady,
			SessionID: tm.session,
			Audio:     AudioSpec{SampleRate: requiredSampleRate, Format: requiredFormat, Channels: requiredChannels},
			TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
			Lesson:    view,
			Category:  category,
		})
	}) {
		return
	}
	tm.recorder.Emit(EventConnected, infra.F("session_id", tm.session))

	ctx, cancel := context.WithCancel(context.Background())
	var epoch uint64
	if !tm.applySessionEffect(func() error {
		// A close that landed after the session_ready send still gets no
		// greeting: the entry gates (runAnnouncementDisplayAt) kill the
		// goroutine even if it launches, but refusing here is the fast path.
		tm.turnCancel = cancel
		epoch = tm.epoch
		return nil
	}) {
		cancel()
		return
	}
	spoken := greetingText(total, first.Word)
	display := spoken
	if mode != ModeTest {
		// 13.1-02 student masking (D-10): the greeting subtitle drops the
		// first word's text; the TTS input stays the raw greeting.
		display = greetingMaskedText(total)
	}
	go tm.runStarterTask(func() {
		tm.runAnnouncementDisplayAt(s, epoch, ctx, cancel, spoken, display, purposeGreeting, func() {
			tm.announceAckedLocked()
		})
	})
}

func (tm *TurnManager) runStarterTask(task func()) {
	if tm.starterInterleave != nil {
		tm.starterInterleave(task)
		return
	}
	task()
}

// lessonViewOf projects a lesson row into the wire LessonView (test mode and
// the full-reveal shape: every word's plaintext).
func lessonViewOf(l lesson.Lesson) LessonView {
	words := make([]string, 0, len(l.Cards))
	for _, c := range l.Cards {
		words = append(words, c.Word)
	}
	return LessonView{LessonID: l.LessonID, Version: l.Version, CurrentIndex: l.CurrentIndex, Words: words}
}

// lessonViewStudent hides every answer until the whole dictation is done,
// including completed positions restored by a mid-lesson reconnect.
func lessonViewStudent(l lesson.Lesson) LessonView {
	words := make([]string, 0, len(l.Cards))
	for _, c := range l.Cards {
		if l.CurrentIndex >= len(l.Cards) {
			words = append(words, c.Word)
		} else {
			words = append(words, "")
		}
	}
	return LessonView{LessonID: l.LessonID, Version: l.Version, CurrentIndex: l.CurrentIndex, Words: words}
}

// sessionMode returns the normalized UI mode (ModeStudent before session_init
// stores one — the absent case must never leak words, D-08).
func (tm *TurnManager) sessionMode() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.mode
}

// sessionCategory returns the classroom's subject (CategoryEnglish before
// session_init stores one — the same pre-#10 default as the wire field).
func (tm *TurnManager) sessionCategory() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.category
}

// lessonViewFor projects a lesson row through the session's mode: student
// masks undictated words (D-09); test keeps the full reveal.
func (tm *TurnManager) lessonViewFor(l lesson.Lesson) LessonView {
	// Issue #12/#14: a 背默 row (poem or passage) masks its original in
	// EVERY mode until the check stage — the task is writing from MEMORY,
	// so even mode=test must not hand the child the text (unlike a word
	// lesson, where test mode is a debugging affordance).
	// lessonViewStudent's pre-check shape ("" per line/paragraph) is exactly
	// that projection.
	if (l.PoemID > 0 || l.PassageID > 0) && l.Stage != lesson.StageChecking && l.Stage != lesson.StageSubmitted {
		return lessonViewStudent(l)
	}
	if tm.sessionMode() == ModeTest {
		return lessonViewOf(l)
	}
	return lessonViewStudent(l)
}

// replyDisplay returns the SCREEN copy of an LLM reply: in student mode the
// reply passes through maskTranscript (a leaking hint/meaning/chat reply is
// masked on screen) while TTS still speaks it as generated; test mode and
// pre-session turns return it unchanged.
func (tm *TurnManager) replyDisplay(reply string) string {
	if reply == "" {
		return reply
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.tutor == nil {
		return reply
	}
	// Issue #12/#14: a 背默 classroom (poem or passage) masks LLM replies
	// in EVERY mode pre-check — a chatty reply quoting the original would
	// defeat the 背默 task. Word classrooms keep the pre-13.1 shape: test
	// mode returns the reply as-is.
	recitePreCheck := tm.tutor.recitation() && !tm.tutor.checking
	if tm.mode == ModeTest && !recitePreCheck {
		return reply
	}
	return maskTranscript(reply, tm.tutor.maskedWords())
}

// announceAckedLocked is the word announcement's ack post-processing: the
// announced-word event and the (13-09) per-word timer arm. Caller holds
// tm.mu — it runs INSIDE the settle effect, so the arm reads the epoch the
// ack belonged to instead of re-reading whatever the FSM moved on to.
func (tm *TurnManager) announceAckedLocked() {
	if tm.tutor != nil && tm.tutor.currentIndex < len(tm.tutor.words) {
		tm.recorder.Emit(EventWordAnnounced, infra.F("word_index", tm.tutor.currentIndex))
	}
	tm.armWordTimerLocked()
}

// armWordTimer arms the per-word confirm countdown; the implementation
// (generation bump + full TurnTarget capture + epoch-stamped callback)
// lives in dictation.go next to the rest of the 13-09 timer logic.
// stopWordTimer suspends it (the pause command).

// runAnnouncement speaks one FSM announcement whose display text equals its
// spoken text (non-word-bearing announcements) and settles the FSM only
// after its valid final playback ack — TTS send completion and audio_end
// NEVER restore listening (uplink stays gated through the ack; 13-09 adds
// the 500ms echo tail afterwards). Timeout degrades per purpose: ordinary
// announcements orderly-close; natural endings still ship their original
// reason first.
func (tm *TurnManager) runAnnouncement(s Sender, ctx context.Context, cancel context.CancelFunc, text, purpose string, onAck func()) {
	tm.runAnnouncementDisplay(s, ctx, cancel, text, text, purpose, onAck)
}

func (tm *TurnManager) runAnnouncementAt(s Sender, epoch uint64, ctx context.Context, cancel context.CancelFunc, text, purpose string, onAck func()) {
	tm.runTurnAnnouncementDisplayAt(s, epoch, ctx, cancel, text, text, purpose, onAck)
}

// runAnnouncementDisplay is runAnnouncement with SEPARATE spoken and display
// texts (13.1-02 student masking): spoken feeds TTS, the LLM history, and the
// interrupt trim; display rides the teacher_sentence subtitle only.
func (tm *TurnManager) runAnnouncementDisplay(s Sender, ctx context.Context, cancel context.CancelFunc, spoken, display, purpose string, onAck func()) {
	tm.runAnnouncementDisplayAt(s, tm.currentEpoch(), ctx, cancel, spoken, display, purpose, onAck)
}

func (tm *TurnManager) runTurnAnnouncementDisplayAt(s Sender, epoch uint64, ctx context.Context, cancel context.CancelFunc, spoken, display, purpose string, onAck func()) {
	if tm.turnAnnouncementInterleave != nil {
		tm.turnAnnouncementInterleave()
	}
	tm.runAnnouncementDisplayAt(s, epoch, ctx, cancel, spoken, display, purpose, onAck)
}

// currentEpoch snapshots the FSM epoch under the lock.
func (tm *TurnManager) currentEpoch() uint64 {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.epoch
}

// runAnnouncementDisplayAt is runAnnouncementDisplay with the epoch FIXED by
// the caller instead of snapshot at goroutine entry (PR #59 review): the
// subject ask parks its epoch BEFORE its goroutine is launched, and this
// entry check refuses an announcement whose epoch has since been bumped —
// a pick processed before the ask goroutine first runs must find the ask
// dead, not resurrected under the NEW epoch. speakTurn mutates playback
// state, registers waits and ships subtitles BEFORE any TTS call, so a
// cancelled context alone cannot stop a late ask from clobbering the new
// classroom's bookkeeping; only the epoch comparison can. The closed refusal
// (PR #60 review) rides the same hold: an announcement goroutine launched
// just before an end_session but scheduled after it must die here too — a
// close does not bump the epoch, so the epoch comparison alone would pass it.
func (tm *TurnManager) runAnnouncementDisplayAt(s Sender, epoch uint64, ctx context.Context, cancel context.CancelFunc, spoken, display, purpose string, onAck func()) {
	defer cancel()

	tm.mu.Lock()
	if tm.epoch != epoch || tm.closed {
		// Stale before the first sound: nothing registered, nothing spoken.
		tm.mu.Unlock()
		return
	}
	turnID := tm.nextTurnIDLocked()
	tm.mu.Unlock()
	if tm.speakTurnInterleave != nil {
		tm.speakTurnInterleave()
	}

	ref := tm.speakTurn(s, turnID, epoch, spoken, display, purpose, ctx, cancel)
	completed, timedOut := tm.awaitPlaybackCompletion(s, ref, purpose)
	if !completed {
		if timedOut {
			tm.orderlyCloseAt(epoch)
		}
		return
	}
	if tm.settleInterleave != nil {
		tm.settleInterleave()
	}
	// The settle, and the onAck work it licenses (timer arms, word events),
	// ride the SAME epoch gate as ONE effect: a close or interrupt landing
	// between the final ack and this processing suppresses the lot — no
	// listening revival, no timer armed at a re-read epoch.
	tm.settleAnnouncementAt(s, epoch, onAck)
}

// runNaturalEnding speaks a natural-ending announcement (no-due words, or
// the completion summary) and finishes with session_ended carrying the
// ORIGINAL reason — after the valid ack, or after the bounded ack timeout
// (ErrorMsg first, reason second — the protocol.go degraded order; never a
// fabricated completion and never a timeout reason). PR #60 review: a
// no-due/explanation goroutine scheduled only after the session closed dies
// at entry — no sentences, no ack-timeout theater, no second session_ended.
func (tm *TurnManager) runNaturalEnding(s Sender, ctx context.Context, cancel context.CancelFunc, text, purpose, reason string) {
	tm.mu.Lock()
	if tm.closed {
		tm.mu.Unlock()
		cancel()
		return
	}
	epoch := tm.epoch
	tm.mu.Unlock()
	tm.runNaturalEndingAt(s, epoch, ctx, cancel, text, purpose, reason)
}

func (tm *TurnManager) runNaturalEndingAt(s Sender, epoch uint64, ctx context.Context, cancel context.CancelFunc, text, purpose, reason string) {
	defer cancel()
	var turnID uint64
	if !tm.applyTurnAt(epoch, func() error {
		turnID = tm.nextTurnIDLocked()
		return nil
	}) {
		return
	}

	ref := tm.speakTurn(s, turnID, epoch, text, text, purpose, ctx, cancel)
	completed, timedOut := tm.awaitPlaybackCompletion(s, ref, purpose)
	if !completed && !timedOut {
		// Interrupted or disconnected: cancel the wait, never message — and
		// only if this ending's epoch still owns the FSM: a summary wait
		// that outlived an interrupt must not close the classroom that
		// replaced it (orderlyCloseAt's epoch gate).
		tm.orderlyCloseAt(epoch)
		return
	}
	if purpose == purposeSummary {
		tm.recorder.Emit(EventLessonComplete, infra.F("reason", reason))
	}
	// The goodbye rides the ONE-TIME terminal close at this ending's own
	// epoch: a racing client goodbye (or an interrupt that bumped the
	// epoch) already closed the session, and exactly one session_ended can
	// ever ship.
	tm.tryTerminalCloseAt(s, epoch, reason)
}

// handleInterrupt is the epoch bump + provider cancellation + short-history
// trim (Pitfall 9 on the FSM deque) + old-epoch playback-wait invalidation.
// issue #69 备选一 步骤 10: a CLOSED session stays silent — no epoch bump, no
// ack, no state message; nothing may follow session_ended. On a live
// session the state change and the reply enqueues ride ONE lock hold: a
// close racing the interrupt either wins tm.mu first (this handler then
// finds closed set and refuses outright) or lands behind the enqueued
// replies — interrupt_ack and the state message can never trail
// session_ended on the wire.
func (tm *TurnManager) handleInterrupt(s Sender, m InterruptMsg) {
	tm.mu.Lock()
	if tm.closed {
		tm.mu.Unlock()
		return
	}
	oldEpoch := tm.epoch
	tm.epoch++
	newEpoch := tm.epoch
	if tm.turnCancel != nil {
		tm.turnCancel()
	}

	// Played = this announcement's acked sentences ∪ the interrupt's
	// last_sentence_id (Pitfall 9: the next turn must only know what the
	// student actually heard).
	played := make(map[string]bool, len(tm.acked)+1)
	for id := range tm.acked {
		played[id] = true
	}
	if m.LastSentenceID != "" {
		played[m.LastSentenceID] = true
	}
	var kept []string
	for _, si := range tm.turnSentences {
		if played[si.id] {
			kept = append(kept, si.text)
		}
	}
	if tm.tutor != nil {
		tm.tutor.trimTeacherReply(kept)
	}
	tm.turnSentences = nil
	tm.acked = map[string]bool{}

	// Invalidate every old-epoch playback wait: close the wait channels so
	// awaiters wake and observe the stale epoch (interrupt beats ack).
	for _, p := range tm.pendingPlayback {
		p.cancelWait()
	}
	tm.pendingPlayback = map[string]*pendingPlayback{}
	tm.playbackPending = false

	if tm.state != "" {
		tm.state = StateListening
	}
	effective := tm.effectiveClientStateLocked()
	_ = s.SendControl(InterruptAckMsg{
		Type:           TypeInterruptAck,
		OldEpoch:       oldEpoch,
		NewEpoch:       newEpoch,
		LastSentenceID: m.LastSentenceID,
		PlayedUntilMs:  m.PlayedUntilMs,
	})
	_ = s.SendControl(StateMsg{Type: TypeState, Value: effective})
	tm.mu.Unlock()

	tm.vad.Reset()

	tm.recorder.Emit(EventInterruptHandled,
		infra.F("old_epoch", oldEpoch),
		infra.F("new_epoch", newEpoch),
		infra.F("last_sentence_id", m.LastSentenceID),
		infra.F("played_until_ms", m.PlayedUntilMs),
	)
}

// handleSentenceAck records one sentence the client actually played — the
// only valid completion evidence (T-13-15b): the ack must carry a non-empty
// sentence id, match the pending entry registered before that sentence's
// audio_start, and belong to the announcement's pending set. Unknown, stale
// and duplicate acks emit a sanitized diagnostic and have ZERO state side
// effects.
func (tm *TurnManager) handleSentenceAck(m SentenceAckMsg) {
	if m.SentenceID == "" {
		tm.recorder.Emit(EventSentenceAckRejected, infra.F("reason", "empty_sentence_id"))
		return
	}
	tm.mu.Lock()
	p, ok := tm.pendingPlayback[pendingKey(m.Epoch, m.SentenceID)]
	if !ok {
		tm.mu.Unlock()
		tm.recorder.Emit(EventSentenceAckRejected,
			infra.F("reason", "unknown_or_stale"),
			infra.F("epoch", m.Epoch))
		return
	}
	tm.acked[m.SentenceID] = true
	if p.completed {
		tm.mu.Unlock()
		tm.recorder.Emit(EventSentenceAckRejected,
			infra.F("reason", "duplicate"),
			infra.F("epoch", m.Epoch))
		return
	}
	p.finish()
	tm.mu.Unlock()
}

// handlePing answers the clock-offset probe. TServerMonoNs shares the
// recorder's anchor with every rt_event t_mono_ns (13-03 protocol). The
// reply rides the session effect: a session that already shipped
// session_ended is never answered past its terminal message — a ping that
// races the goodbye loses, and session_ended stays the wire's last word.
func (tm *TurnManager) handlePing(s Sender, m PingMsg) {
	pong := PongMsg{
		Type:          TypePong,
		TClientMs:     m.TClientMs,
		TServerMonoNs: tm.recorder.MonoNs(),
	}
	tm.applySessionEffect(func() error { return s.SendControl(pong) })
}

// closeClaim reports one unified-close attempt (issue #69 备选一 步骤 6/8):
// Owned is true only for the single caller whose critical section flipped
// the session closed. When the closing path carries the D-03 fallback
// (takeFallback — the goodbye and the transport close), the SAME critical
// section captured the unchecked-fallback snapshot: whatever state the
// claim observed is the state the closed marking finalized, so a check
// stage presented inside the close's old two-lock window can no longer
// slip past the fallback decision. Every other path that raced the close
// owns nothing — nothing to send, nothing to persist.
type closeClaim struct {
	fallback    uncheckedCloseSnapshot
	owned       bool
	hasFallback bool
}

// claimCloseLocked is the single ownership point of the session's close:
// eligibility (already closed, and for the epoch-gated flavors a stale
// epoch), the D-03 fallback snapshot (takeFallback: the classroom still
// holds an unsubmitted check stage) and the closed marking complete in ONE
// critical section, so the decision and the marking can never split.
// Caller holds initWriteMu AND tm.mu (fixed order initWriteMu → tm.mu).
// Everything that can block — persistence, logs, the goodbye enqueue, the
// VAD reset — runs outside, after the claim: a failed notification must
// never reopen, delay or skip the close.
func (tm *TurnManager) claimCloseLocked(epoch uint64, gateEpoch, takeFallback bool) closeClaim {
	if tm.closed || (gateEpoch && tm.epoch != epoch) {
		return closeClaim{}
	}
	c := closeClaim{owned: true}
	if takeFallback && !tm.closeOwned && tm.tutor != nil && tm.tutor.checking {
		tm.closeOwned = true
		t := tm.tutor
		c.fallback = uncheckedCloseSnapshot{
			lessonID: t.lessonID,
			version:  t.version,
			category: t.category,
			poem:     t.poem != nil,
			words:    append([]lesson.Word(nil), t.words...),
		}
		c.hasFallback = true
	}
	tm.internalCloseLocked()
	return c
}

// closeSessionAt is the unified close entry every close path funnels
// through: initWriteMu → tm.mu → claimCloseLocked, then the transport
// finalization (VAD reset; the ONE session_ended when a reason is asked
// for) runs lock-free. A failed session_ended enqueue is logged and
// swallowed — the session is already closed at that point, the close is
// not undoable, and the caller still learns it owns the close.
func (tm *TurnManager) closeSessionAt(epoch uint64, gateEpoch, takeFallback bool, reason string, s Sender) closeClaim {
	tm.initWriteMu.Lock()
	if tm.closeEntryInterleave != nil {
		// Narrow test probe (issue #69 备选一 步骤 8): fires with the
		// init-write lock held and tm.mu NOT yet taken — the exact window
		// where a check-stage presentation can still slip in ahead of the
		// claim's decision.
		tm.closeEntryInterleave()
	}
	tm.mu.Lock()
	c := tm.claimCloseLocked(epoch, gateEpoch, takeFallback)
	tm.mu.Unlock()
	tm.initWriteMu.Unlock()
	if !c.owned {
		return c
	}
	tm.vad.Reset()
	if s != nil {
		if err := s.SendControl(SessionEndedMsg{Type: TypeSessionEnded, Reason: reason}); err != nil {
			tm.logger.Warn("session_ended enqueue failed", infra.ErrField(err))
		}
	}
	return c
}

// tryTerminalClose claims the session's ONE terminal close and ships the
// single session_ended: the winner's claim (closeSessionAt) marks the
// session closed under tm.mu FIRST (the lesson_update pattern — every
// lock-disciplined effect that lost the race dies on the closed flag, and
// none can queue behind the goodbye), losers send nothing. Epoch-free: the
// client's goodbye closes whatever turn is live. The D-03 fallback
// ownership is NOT decided here — only the goodbye (handleEndSession) and
// the transport close (Close) carry it; these callers have either already
// left the check stage or never showed it.
func (tm *TurnManager) tryTerminalClose(s Sender, reason string) bool {
	return tm.closeSessionAt(0, false, false, reason, s).owned
}

// tryTerminalCloseAt is tryTerminalClose gated on the ending's OWN epoch: a
// stale ending — a summary whose wait outlived an interrupt that bumped the
// epoch — must not close (or goodbye) the classroom that replaced it.
func (tm *TurnManager) tryTerminalCloseAt(s Sender, epoch uint64, reason string) bool {
	return tm.closeSessionAt(epoch, true, false, reason, s).owned
}

// handleEndSession is the explicit goodbye (RT-10/SC7 server half). The
// close owns the D-03 fallback (13.1-04) ONCE, decided in the claim's ONE
// critical section together with the closed marking (issue #69 备选一
// 步骤 8: the old shape snapshotted in its own earlier lock hold, so a
// check stage presented in between closed the session with the fallback
// silently missed). The goodbye ships after the marking, and only then
// does the lock-free persistence run — every in-flight callback that
// returns during that write finds closed set and stays silent, and the
// dead tutor is never touched again. PR #61 review: closed is set BEFORE
// session_ended ships — the flag (under tm.mu) is what every advance-side
// gate checks, and the lesson_update enqueue in advanceTarget shares that
// lock, so a racing advance's wire output lands strictly before the
// goodbye, or (when it lost the race) is suppressed entirely.
// session_ended stays the terminal control message.
func (tm *TurnManager) handleEndSession(s Sender) {
	if c := tm.closeSessionAt(0, false, true, ReasonClientEnd, s); c.owned {
		tm.recorder.Emit(EventSessionEnd, infra.F("session_id", tm.session))
		if c.hasFallback {
			tm.persistUncheckedFallback(c.fallback)
		}
	}
}

// internalClose cancels in-flight work, invalidates playback waits,
// suspends the per-word timer, and resets the FSM without emitting the
// session_end event again. It also cancels any playback-ack wait (异常断线
// 取消等待) and sweeps the photo assembler (13.1-03 privacy P2: the child's
// photo never outlives its session).
func (tm *TurnManager) internalClose() {
	tm.closeSessionAt(0, false, false, "", nil)
}

// internalCloseLocked is internalClose's locked half (caller holds tm.mu),
// shared with the epoch-gated orderlyCloseAt so the epoch check and the
// close itself are one atomic step.
func (tm *TurnManager) internalCloseLocked() {
	if tm.turnCancel != nil {
		tm.turnCancel()
	}
	for _, p := range tm.pendingPlayback {
		p.cancelWait()
	}
	tm.pendingPlayback = map[string]*pendingPlayback{}
	tm.playbackPending = false
	tm.stopWordTimerLocked()
	tm.state = ""
	tm.closed = true
	tm.photo = nil
	tm.pendingAsk = nil
}

// orderlyCloseAt is the bounded-timeout degraded ending of an ordinary
// announcement: the ErrorMsg(playback_ack_timeout) already went out in the
// await; this closes the session in order — no fabricated completion, no
// timer arm, no word advance, and no new timeout end reason — gated on the
// announcement's OWN epoch (PR #59 review): a timeout reported by an
// announcement the FSM has since moved past — an ask terminated by a
// subject pick — must never close the classroom that replaced it. The gate
// and the close are one lock hold, so the interleaving "timeout fires →
// pick bumps the epoch and opens the new classroom → the stale close
// lands" cannot happen.
func (tm *TurnManager) orderlyCloseAt(epoch uint64) {
	tm.closeSessionAt(epoch, true, false, "", nil)
}

// Close implements SessionManager: the transport read loop exited for any
// reason (disconnect, deadline, read error). Idempotent — an end_session
// path already closed the session. The D-03 fallback (13.1-04) rides the
// same ONE-TIME close ownership as handleEndSession, decided in the claim's
// ONE critical section (issue #69 备选一 步骤 8): whoever's claim flipped
// the closed flag also decided — and owns — the fallback; the loser does
// nothing. No session_ended ships here (the transport is gone).
func (tm *TurnManager) Close(Sender) {
	if c := tm.closeSessionAt(0, false, true, "", nil); c.owned {
		tm.recorder.Emit(EventSessionEnd, infra.F("session_id", tm.session))
		if c.hasFallback {
			tm.persistUncheckedFallback(c.fallback)
		}
	}
}

// ---------------------------------------------------------------------------
// SessionManager: audio path
// ---------------------------------------------------------------------------

// IngestAudio is the half-duplex gate (Pattern 3) with the 13-09 silence
// windows. Ordinary uplink is dropped (and counted) while
//
//   - the teacher's announcement awaits its valid final playback ack —
//     from the first audio_start through the ack; TTS send completion and
//     audio_end NEVER lift this gate;
//   - the echo tail runs — VADEchoTailMs after the valid ack
//     (lastPlaybackCompletedAt is set by that ack and ONLY by it);
//   - the start grace runs — VADStartGraceMs after session_ready
//     (mic-open transients must not cut a phantom turn);
//   - the session is not in listening (voice state; the classroom pause bit
//     is an overlay on the client-effective state and does not gate uplink —
//     the resume command must be recognizable while paused).
//
// Interrupt control messages always bypass this gate (they travel
// HandleControl and are honored immediately).
func (tm *TurnManager) IngestAudio(s Sender, data []byte) {
	tm.mu.Lock()
	now := time.Now()
	ordinaryGated := tm.playbackPending || now.Before(tm.echoTailUntil) ||
		(!tm.sessionReadyAt.IsZero() &&
			now.Sub(tm.sessionReadyAt) < time.Duration(tm.cfg.VADStartGraceMs)*time.Millisecond)
	state := tm.state
	closed := tm.closed
	if ordinaryGated || state != StateListening || closed {
		tm.droppedUplink++
	}
	tm.mu.Unlock()
	if ordinaryGated || state != StateListening || closed {
		return
	}
	segment, segClosed := tm.vad.Feed(data)
	if !segClosed {
		return
	}
	// Utterances shorter than two frames (60 ms) are nose breaths, not
	// speech — do not spend an ASR call on them.
	if len(segment) < 2*960 {
		return
	}
	go tm.processTurn(s, segment)
}

// processTurn runs one utterance → ASR → intent classification → FSM
// dispatch cycle on its own goroutine (IngestAudio launches it). The epoch
// captured at entry governs every provider call and the announcement; any
// mismatch aborts the whole turn without issuing further calls.
func (tm *TurnManager) processTurn(s Sender, segment []byte) {
	tm.mu.Lock()
	// Issue #55: while the subject clarify is parked the classroom has no
	// tutor yet — the utterance is the child's ANSWER to the clarify and
	// flows through that branch below, never the lesson FSM.
	asking := tm.pendingAsk != nil
	if tm.state != StateListening || tm.closed || (tm.tutor == nil && !asking) {
		tm.mu.Unlock()
		return
	}
	// 13.1-06 (D-06②): the check stage has a VOICE input now — per-word
	// verdict commands classify through the CHECK prompt and converge on the
	// same ApplyVerdict write as the screen buttons (RT-07). The advance
	// target below must NOT be captured for the check stage: the cursor sits
	// at len(words) there and turnTarget would panic. The no-target marker
	// (WordIndex -1) also covers a classroom whose cursor ran past the end
	// without the checking bit (an EnterCheck write failure leaves
	// index=len) — turnTarget must never be asked for a word that does not
	// exist, and an invalid-marker target can never drive an advance.
	checking := tm.tutor != nil && tm.tutor.checking
	epoch := tm.epoch
	turnID := tm.nextTurnIDLocked()
	ctx, cancel := context.WithCancel(context.Background())
	target := TurnTarget{WordIndex: -1}
	if !checking && tm.tutor != nil &&
		tm.tutor.currentIndex >= 0 && tm.tutor.currentIndex < len(tm.tutor.words) {
		// Immutable advance target captured BEFORE classification
		// (T-13-15c): a late confirm may only ever act on this word/version
		// pair.
		target = tm.tutor.turnTarget()
	}
	// Issue #56: the turn's task context (waiting on the current word,
	// pause, stage) is snapshot in the SAME locked section, before the
	// processing flip — classification judges the classroom as it stood
	// when the child spoke, never mid-turn state.
	turnCtx := tm.captureDictationTurnContextLocked(turnID, epoch, checking, target)
	started := tm.applyTurnLocked(epoch, func() error {
		if err := s.SendControl(StateMsg{Type: TypeState, Value: StateProcessing}); err != nil {
			return err
		}
		if err := s.SendControl(TurnStartMsg{Type: TypeTurnStart, TurnID: turnID, Epoch: epoch}); err != nil {
			return err
		}
		tm.state = StateProcessing
		tm.turnCancel = cancel
		return nil
	})
	tm.mu.Unlock()
	if !started {
		cancel()
		return
	}
	defer cancel()

	tm.recorder.Emit(EventSpeechStarted, infra.F("turn_id", turnID))
	tm.recorder.Emit(EventSpeechEnded, infra.F("turn_id", turnID))

	attempts := 1
	transcript, err := tm.asr.Transcribe(ctx, segment)
	if errors.Is(err, mimo.ErrEmptyTranscription) {
		// The provider's legitimate "nothing spoken" answer (13-07 sentinel):
		// silence is not a fault — no user-visible error, no classification,
		// and the per-word countdown is NOT reset (hearing nothing is not an
		// interaction). A server-side marker event only.
		tm.recorder.Emit(EventSpeechEnded, infra.F("turn_id", turnID), infra.F("empty", true))
		// Silence is not playback: the recovery lifts no playback-completion
		// time and starts no echo tail (resumeListeningAt).
		tm.resumeListeningAt(s, epoch)
		return
	}
	if err != nil && errors.Is(err, mimo.ErrInvalidResponse) {
		// The ASR half of the invalid-response discipline (13-08 built the
		// LLM half): same ctx, at most ONE retry; the failure count lands
		// in the event fields when it still fails.
		if ctx.Err() != nil || !tm.epochCurrent(epoch) {
			return
		}
		attempts = 2
		transcript, err = tm.asr.Transcribe(ctx, segment)
	}
	if err != nil {
		tm.failTurn(s, epoch, ErrCodeProviderFailed, "语音识别失败，请再对我说一遍", infra.F("attempts", attempts))
		return
	}
	if transcript == "" {
		// Nothing usable recognized: back to listening — without inventing a
		// playback-completion time. (13-09 refines the silent tolerance of
		// legal-empty ASR results.)
		tm.resumeListeningAt(s, epoch)
		return
	}
	if asking {
		// Issue #55: the child's answer to the subject clarify. Shown
		// verbatim (no tutor yet means no masking set), then bound by
		// keyword — never an LLM classification. The verbatim transcript
		// still goes through the turn effect entry: a session that ended
		// while ASR was out must not receive a stale transcript.
		if !tm.applyTurnAt(epoch, func() error {
			return s.SendControl(TranscriptMsg{Type: TypeTranscript, TurnID: turnID, Text: transcript})
		}) {
			return
		}
		tm.handleSubjectAnswerTurn(s, transcript, epoch, ctx, cancel)
		return
	}

	// Transcript desensitization (13.1-02 D-10, Pitfall 4 red line): the
	// OUTBOUND copy masks still-unrevealed words; the history and the
	// classification input keep the raw transcript. In the check stage the
	// masked set is empty by construction (nothing re-masks after
	// check_started) — the display copy is the raw transcript either way.
	// Issue #12/#14: a 背默 classroom (poem or passage) masks in EVERY mode
	// pre-check — the child reciting the original back must not put it on
	// screen before the check reveals it.
	if !tm.applyTurnAt(epoch, func() error {
		display := transcript
		tm.tutor.appendUserRound(transcript)
		if tm.mode != ModeTest || (tm.tutor.recitation() && !tm.tutor.checking) {
			display = maskTranscript(transcript, tm.tutor.maskedWords())
		}
		return s.SendControl(TranscriptMsg{Type: TypeTranscript, TurnID: turnID, Text: display})
	}) {
		return
	}

	if checking {
		// 13.1-06: the check stage's classification runs against the CHECK
		// prompt (Pitfall 8) and dispatches verdicts through the SAME
		// ApplyVerdict path as the screen buttons (RT-07).
		tm.classifyCheckTurn(s, ctx, cancel, turnID, epoch, transcript)
		return
	}

	// Issue #12/#14: a 背默 WRITING stage classifies through its own prompt
	// (the payload's Index drives the line/paragraph hint) and dispatches
	// through its own branch table — never the per-word pacing switch below.
	tm.mu.Lock()
	reciteMode := tm.tutor.recitation()
	isPoemTask := tm.tutor.poem != nil
	tm.mu.Unlock()
	if reciteMode {
		var p intentPayload
		if isPoemTask {
			p = tm.classifyPoemTurn(ctx, transcript)
		} else {
			p = tm.classifyPassageTurn(ctx, transcript)
		}
		if !tm.epochCurrent(epoch) {
			return
		}
		if isPoemTask {
			tm.dispatchPoemTurn(s, ctx, cancel, turnID, epoch, p)
		} else {
			tm.dispatchPassageTurn(s, ctx, cancel, turnID, epoch, p)
		}
		return
	}

	intent, reply := tm.classifyTurn(ctx, transcript, turnCtx)
	if !tm.epochCurrent(epoch) {
		return // interrupted mid-classification: abort the whole turn silently
	}
	if tm.dispatchInterleave != nil {
		tm.dispatchInterleave()
	}
	tm.dispatchTurn(s, ctx, cancel, turnID, epoch, intent, reply, turnCtx.target)
}

// dispatchTurn routes a whitelisted intent into the FSM. The switch covers
// the nine-value dictation intent vocabulary EXHAUSTIVELY — there is no
// fall-through-to-chat branch left (13-09 Task 1 source contract): confirm
// drives pacing, pause/resume own the classroom pause bit, repeat/slow
// re-read the current word from templates, hint/meaning/example/chat speak
// the classification reply — meaning/example first augment it with the
// on-demand example sentence (Issue #9, example.go). Every
// non-confirm/non-pause reply re-arms the per-word countdown after ITS
// valid final ack (the 13-09 timer's arm-after-reply-ack rule); pause stops
// it instead.
func (tm *TurnManager) dispatchTurn(s Sender, ctx context.Context, cancel context.CancelFunc, turnID, epoch uint64, intent, reply string, target TurnTarget) {
	switch intent {
	case intentConfirm:
		paused := false
		if !tm.applyTurnAt(epoch, func() error {
			paused = tm.tutor != nil && tm.tutor.paused
			return nil
		}) {
			return
		}
		if paused {
			// Gentle rejection while paused: pacing stays suspended — no
			// advance, no lesson_update, no outcome write; the classroom
			// keeps showing paused after the reply settles.
			tm.runAnnouncementAt(s, epoch, ctx, cancel, confirmPausedRejectText(), purposeChat, nil)
			return
		}
		tm.confirmTarget(s, ctx, cancel, turnID, epoch, reply, target)
	case intentRepeat:
		tm.speakCurrentWordReading(s, ctx, cancel, epoch, repeatText, repeatMaskedText)
	case intentSlow:
		tm.speakCurrentWordReading(s, ctx, cancel, epoch, slowReadText, slowMaskedText)
	case intentHint:
		// U-9 ruling (2026-10-06): every hint request increments the word's
		// hint count and pre-records the hint outcome. The pre-record is
		// DESIGNED to be overwritten by the word's first confirm/timeout
		// commit; the COUNT lives in perWordHints and survives that
		// overwrite — the final rating is wordRating(outcome, hints) at
		// completion. The event carries count-class fields only
		// (word_index + hint_count; T-13-06b boundary) and is emitted
		// outside the lock.
		n := 0
		if !tm.applyTurnAt(epoch, func() error {
			if tm.tutor != nil && tm.tutor.currentIndex == target.WordIndex &&
				tm.tutor.version == target.ExpectedVersion &&
				target.WordIndex >= 0 && target.WordIndex < len(tm.tutor.words) {
				n = tm.tutor.recordHint(target.WordIndex)
			}
			return nil
		}) {
			return
		}
		if n > 0 {
			tm.recorder.Emit(EventHintGiven,
				infra.F("word_index", target.WordIndex),
				infra.F("hint_count", n),
			)
		}
		// LLM replies mask on screen in student mode (a leaking hint is still
		// masked); TTS speaks the reply as generated (D-10).
		tm.runTurnAnnouncementDisplayAt(s, epoch, ctx, cancel, reply, tm.replyDisplay(reply), purposeChat, tm.armWordTimerLocked)
	case intentMeaning, intentExample:
		// Issue #9 (ADR-0006): in the DICTATION stage these two intents
		// may augment the classification reply with one on-demand example
		// sentence. The check stage reaches this same case through
		// checkDispatch with WordIndex=-1 — the guard keeps check-stage
		// asks on the stored-information path (no generation there).
		if target.WordIndex >= 0 {
			augmented, proceed := tm.exampleReply(ctx, epoch, intent, target, reply)
			if !proceed {
				return // interrupted mid-generation: abort silently
			}
			reply = augmented
		}
		tm.runTurnAnnouncementDisplayAt(s, epoch, ctx, cancel, reply, tm.replyDisplay(reply), purposeChat, tm.armWordTimerLocked)
	case intentPause:
		if !tm.applyTurnAt(epoch, func() error {
			if tm.tutor != nil {
				tm.tutor.paused = true
			}
			tm.stopWordTimerLocked()
			return nil
		}) {
			return
		}
		tm.runAnnouncementAt(s, epoch, ctx, cancel, pauseAckText(), purposeChat, nil)
	case intentResume:
		idx, word, mode := 0, "", ""
		if !tm.applyTurnAt(epoch, func() error {
			mode = tm.mode
			if tm.tutor != nil && tm.tutor.currentIndex >= 0 && tm.tutor.currentIndex < len(tm.tutor.words) {
				tm.tutor.paused = false
				idx = tm.tutor.currentIndex + 1
				word = tm.tutor.words[tm.tutor.currentIndex].Word
			}
			return nil
		}) {
			return
		}
		spoken := resumeAckText(idx, word)
		display := spoken
		if mode != ModeTest {
			display = resumeMaskedText(idx)
		}
		tm.runTurnAnnouncementDisplayAt(s, epoch, ctx, cancel, spoken, display, purposeChat, tm.armWordTimerLocked)
	case intentChat:
		tm.runTurnAnnouncementDisplayAt(s, epoch, ctx, cancel, reply, tm.replyDisplay(reply), purposeChat, tm.armWordTimerLocked)
	}
}

// speakCurrentWordReading announces a template re-reading (repeat/slow) of
// the CURRENT word — the cursor never moves. The template SPEAKS the word
// (voice is the answer channel, D-10); the masked variant rides the subtitle
// in student mode. Re-arms the per-word countdown after the reading's valid
// final ack.
func (tm *TurnManager) speakCurrentWordReading(s Sender, ctx context.Context, cancel context.CancelFunc, epoch uint64, template func(string) string, masked func(int) string) {
	word, mode, idx := "", "", 0
	if !tm.applyTurnAt(epoch, func() error {
		mode = tm.mode
		if tm.tutor != nil && tm.tutor.currentIndex >= 0 && tm.tutor.currentIndex < len(tm.tutor.words) {
			word = tm.tutor.words[tm.tutor.currentIndex].Word
			idx = tm.tutor.currentIndex + 1
		}
		return nil
	}) {
		return
	}
	spoken := template(word)
	display := spoken
	if mode != ModeTest {
		display = masked(idx)
	}
	tm.runTurnAnnouncementDisplayAt(s, epoch, ctx, cancel, spoken, display, purposeChat, tm.armWordTimerLocked)
}

// confirmTarget advances the classroom through the target captured at
// classification start (outcome=confirmed). All advance outcomes funnel
// through advanceTarget, shared with the 13-09 timeout path — the three
// AdvanceWord branches stay mutually exclusive whatever drove them.
func (tm *TurnManager) confirmTarget(s Sender, ctx context.Context, cancel context.CancelFunc, turnID, epoch uint64, reply string, target TurnTarget) {
	tm.advanceTarget(s, ctx, cancel, epoch, target, outcomeConfirmed, reply, nil)
}

// advanceTarget drives one word advance through the captured target. NONE
// of the branches re-interprets a late driver against the post-return
// currentIndex (T-13-15c):
//
//   - first commit (Idempotent=false, nil): record outcome (optionally fire
//     onFirstCommit — the timeout's EventTimeoutFired), lesson_update,
//     word_advanced event, next-word announcement or completion;
//   - idempotent hit (Idempotent=true, nil): align the local cursor to the
//     FIRST stored result — no outcome write, no message, no narration, no
//     completion;
//   - version conflict: read-back alignment ONLY — no retry write, no
//     side effects.
//
// An invalid target (completed lesson, out of range) degrades to the plain
// chat reply when one exists (confirm) and silently returns otherwise
// (timeout).
func (tm *TurnManager) advanceTarget(s Sender, ctx context.Context, cancel context.CancelFunc, epoch uint64, target TurnTarget, outcome, reply string, onFirstCommit func()) {
	tm.mu.Lock()
	lessonID := tm.tutor.lessonID
	// Issue #56 / PR #61 review: a dead driver refuses the advance at BOTH
	// ends of the executor call, and "dead" is all three of closed, completed
	// and STALE EPOCH. The entry gate re-validates the epoch under the lock
	// because processTurn's post-classification check can pass and an
	// interrupt can still land BEFORE the executor is reached — a late
	// confirm in that window would otherwise commit the advance to the store
	// (the post-return check suppresses messages but can never undo a write).
	current := tm.epoch == epoch && !tm.closed
	valid := current && !tm.tutor.completed &&
		target.WordIndex >= 0 && target.WordIndex < len(tm.tutor.words)
	tm.mu.Unlock()
	if !valid {
		// Completed or out-of-range target: pacing owns the cursor — a
		// confirm degrades to a plain chat reply; a timeout just stops. A
		// stale or closed turn never speaks the degraded reply: its classroom
		// has moved on (runAnnouncement's own gate would kill it anyway).
		if reply != "" && current {
			tm.runAnnouncementAt(s, epoch, ctx, cancel, reply, purposeChat, nil)
		}
		return
	}

	requestID := fmt.Sprintf("%s-advance-%d", lessonID, target.WordIndex)
	res, err := tm.exec.AdvanceWord(ctx, lessonID, target.ExpectedVersion, requestID)
	// PR #61 review: the close (or an interrupt — same predicate) may land
	// UNDER the executor call. The store commit stands — it is the durable
	// truth a reconnect reads — but nothing after the return may touch the
	// dead session: no tutor mutation, no lesson_update, no check stage, no
	// error message.
	if !tm.epochCurrent(epoch) {
		return
	}
	if err != nil {
		var conflict *lesson.ConflictError
		if errors.As(err, &conflict) {
			// Conflict: read-back sync only. No retry write, no narration,
			// no completion — the store already holds the truth.
			if l, gerr := tm.exec.GetLessonState(ctx, lessonID); gerr == nil {
				tm.mu.Lock()
				if tm.epoch == epoch && !tm.closed {
					tm.tutor.align(l.CurrentIndex, l.Version)
				}
				tm.mu.Unlock()
			}
			tm.settleListeningAt(s, epoch)
			return
		}
		tm.failTurn(s, epoch, ErrCodeProviderFailed, "课堂进度更新失败，请再试一次")
		return
	}
	if res.Idempotent {
		// A duplicate driver (late confirm after a timeout, or the
		// reverse): align to the first result and do NOTHING else — the
		// first outcome is never overwritten.
		tm.mu.Lock()
		if tm.epoch == epoch && !tm.closed {
			tm.tutor.align(res.NewIndex, res.NewVersion)
		}
		tm.mu.Unlock()
		tm.settleListeningAt(s, epoch)
		return
	}

	// First commit: outcome + wire update + narration (or completion). The
	// liveness re-check, the tutor mutation, and the lesson_update enqueue
	// share ONE lock hold (the speakTurn pattern — atomic against the close
	// paths): a close that races this window either sets closed first (the
	// branch dies here) or blocks on tm.mu until the update is queued, and
	// the close paths send session_ended only AFTER closed is set — so
	// lesson_update can never trail the goodbye on the wire.
	tm.mu.Lock()
	if tm.epoch != epoch || tm.closed {
		tm.mu.Unlock()
		return
	}
	tm.tutor.align(res.NewIndex, res.NewVersion)
	tm.tutor.commitOutcome(target.WordIndex, outcome)
	finished := res.NewIndex >= len(tm.tutor.words)
	_ = s.SendControl(LessonUpdateMsg{
		Type:         TypeLessonUpdate,
		LessonID:     lessonID,
		Version:      res.NewVersion,
		CurrentIndex: res.NewIndex,
		RequestID:    requestID,
	})
	tm.mu.Unlock()
	if onFirstCommit != nil {
		onFirstCommit()
	}
	// Progress carries positions only. Answers arrive together in check_started.
	tm.recorder.Emit(EventWordAdvanced,
		infra.F("word_index", target.WordIndex),
		infra.F("new_index", res.NewIndex),
		infra.F("version", res.NewVersion),
	)

	if finished {
		// 13.1-01 (D-01): the last word's first commit enters the CHECK
		// stage instead of writing ratings — the unified FSRS write moves
		// behind check_submit (check.go).
		tm.enterCheck(s, ctx, cancel, epoch)
		return
	}

	tm.mu.Lock()
	word := tm.tutor.words[res.NewIndex]
	total := len(tm.tutor.words)
	mode := tm.mode
	tm.mu.Unlock()
	spoken := announceText(res.NewIndex+1, total, word.Word)
	display := spoken
	if mode != ModeTest {
		// 13.1-02 student masking (D-10): the announcement subtitle is the
		// masked position; TTS still announces the word itself.
		display = announceMaskedText(res.NewIndex+1, total)
	}
	tm.runTurnAnnouncementDisplayAt(s, epoch, ctx, cancel, spoken, display, purposeAnnounce, func() {
		tm.announceAckedLocked()
	})
}

// enterCompletion was REPLACED by the check stage (13.1-01, D-01): the last
// word's first advance commit now calls enterCheck (check.go) — the unified
// FSRS write happens only after check_submit, the battle summary speaks
// after that write, and the completionSummaryText wording retired with the
// write-at-completion model (the D-02/D-04 summary is checkedSummaryText).
// The behavior data (outcomeCounts/hintAccounting) survives inside
// EventReviewSubmitted's fields.

// speakTurn streams the announcement sentence by sentence: subtitle first,
// audio chunks forwarded the instant they arrive. spoken drives TTS, the LLM
// history, and the interrupt trim; display (13.1-02 student masking) rides
// the teacher_sentence subtitle only — the pair is per-sentence aligned, and
// any count mismatch falls back to the spoken text. Each sentence's
// pendingPlayback entry is registered in the SAME locked section that queues
// its audio_start — an ack can never outrun its pending entry (T-13-15b).
// The FSM stays speaking/playback-pending on return; the caller awaits the
// final ack via awaitPlaybackCompletion.
func (tm *TurnManager) speakTurn(s Sender, turnID, epoch uint64, spoken, display, purpose string, ctx context.Context, cancel context.CancelFunc) PlaybackRef {
	spoken = truncateRunes(spoken, maxReplyRunes)
	display = truncateRunes(display, maxReplyRunes)
	spokenSents := splitSentences(spoken)
	displaySents := splitSentences(display)
	if len(displaySents) != len(spokenSents) {
		// Masking must never change the sentence count; a mismatching display
		// falls back to the spoken text rather than corrupting the stream.
		displaySents = spokenSents
	}

	if !tm.applyTurnAt(epoch, func() error {
		if tm.tutor != nil {
			// RAW: the LLM context never sees the masked copies (Pitfall 4).
			tm.tutor.recordTeacherReply(spoken)
		}
		tm.turnSentences = nil
		tm.acked = map[string]bool{}
		tm.state = StateSpeaking
		tm.playbackPending = true
		return s.SendControl(StateMsg{Type: TypeState, Value: StateSpeaking})
	}) {
		return PlaybackRef{}
	}
	if tm.speakRegistrationInterleave != nil {
		tm.speakRegistrationInterleave()
	}

	var ref PlaybackRef
	ref.Epoch = epoch
	for i, sentence := range spokenSents {
		sentID := fmt.Sprintf("t%d-s%d", turnID, i)

		// Subtitle first (字幕先行) + pending registration BEFORE
		// audio_start is queued — atomic against interrupt handling: after
		// an interrupt is acknowledged, no further subtitle/chunk of the
		// interrupted announcement can go out. The closed flag rides the
		// same check (epochCurrent's predicate): a close does not bump the
		// epoch, so the comparison alone would let a sentence of a dead
		// session register and ship (PR #61 review).
		if !tm.applyTurnAt(epoch, func() error {
			tm.pendingPlayback[pendingKey(epoch, sentID)] = &pendingPlayback{purpose: purpose, done: make(chan struct{})}
			tm.turnSentences = append(tm.turnSentences, sentenceInfo{id: sentID, text: sentence})
			if err := s.SendControl(TeacherSentenceMsg{
				Type: TypeTeacherSentence, TurnID: turnID, Epoch: epoch, SentenceID: sentID, Text: displaySents[i],
			}); err != nil {
				return err
			}
			return s.SendControl(AudioStartMsg{Type: TypeAudioStart, TurnID: turnID, Epoch: epoch, SentenceID: sentID})
		}) {
			return ref
		}

		// pcm arrives as bare s16le bytes (AudioChunkFunc); the wire
		// encoding is digested by the 13-06 adapter, never here (M-1).
		var sentSamples int
		_, err := tm.tts.SynthesizeStream(ctx, sentence, func(pcm []byte, isFinal bool) error {
			if !tm.applyTurnAt(epoch, func() error {
				sentSamples += len(pcm) / 2 // s16le mono: 2 bytes per sample
				return s.SendAudio(pcm)
			}) {
				// Late chunk (the turn lost its epoch or the session closed)
				// or a dead audio queue: abort the provider stream too — no
				// partial sentence keeps flowing at a dead identity.
				cancel()
				return ErrStaleEpoch
			}
			return nil
		})
		if err != nil && !errors.Is(err, ErrStaleEpoch) && tm.epochCurrent(epoch) {
			// A genuine provider failure on a still-live announcement.
			tm.failTurn(s, epoch, ErrCodeProviderFailed, "老师的声音出了点问题，请再说一遍")
			return ref
		}

		// The sample count and audio_end ride ONE turn effect: a late
		// audio_end is refused whole, so a dead turn's pending entry never
		// carries a phantom sample count either.
		if !tm.applyTurnAt(epoch, func() error {
			if p, ok := tm.pendingPlayback[pendingKey(epoch, sentID)]; ok {
				p.pcmSamples = sentSamples
			}
			return s.SendControl(AudioEndMsg{Type: TypeAudioEnd, TurnID: turnID, Epoch: epoch, SentenceID: sentID})
		}) {
			return ref // interrupted mid-announcement: the interrupt branch trims
		}
		ref.SentenceIDs = append(ref.SentenceIDs, sentID)
		ref.FinalSentenceID = sentID
	}
	return ref
}

// awaitPlaybackCompletion waits, bounded, for the announcement's FINAL
// sentence's actual-playback ack. The wait derives from ALL still-unacked
// scheduled PCM of the announcement plus margin (clamped 3..45s) — never
// from the last short sentence alone. On timeout ONLY the timeout is
// reported (ErrorMsg code=playback_ack_timeout, recoverable) plus the
// desensitized event: no fabricated ack, no lastPlaybackCompletedAt, no
// timer arm, no word advance. The timeout RESULT rides the announcement's
// own epoch through the unified turn entry: a close or interrupt that
// cancels the wait first wins outright, and one racing the timer branch
// suppresses the notice instead of shipping it past session_ended.
func (tm *TurnManager) awaitPlaybackCompletion(s Sender, ref PlaybackRef, purpose string) (completed, timedOut bool) {
	if ref.FinalSentenceID == "" {
		return false, false // nothing scheduled (stale before the first sound)
	}
	tm.mu.Lock()
	fin, ok := tm.pendingPlayback[pendingKey(ref.Epoch, ref.FinalSentenceID)]
	remaining := 0
	for _, sid := range ref.SentenceIDs {
		if p, found := tm.pendingPlayback[pendingKey(ref.Epoch, sid)]; found && !p.completed {
			remaining += p.pcmSamples
		}
	}
	tm.mu.Unlock()
	if !ok {
		return false, false // pending gone (interrupt cleared it) — stale
	}

	wait := playbackAckTimeout(remaining)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-fin.done:
		tm.mu.Lock()
		done := fin.completed
		tm.mu.Unlock()
		return done, false
	case <-timer.C:
		// The timeout observation is logged regardless (desensitized
		// evidence); the RESULT — the degraded-playback notice and the
		// timed-out report — applies only while the announcement's own
		// epoch still owns the FSM: a close or interrupt that won the race
		// to this branch must not ship a playback complaint for a session
		// that already ended or moved on.
		tm.recorder.Emit(EventPlaybackAckTimeout,
			infra.F("purpose", purpose),
			infra.F("wait_ms", wait.Milliseconds()),
		)
		if !tm.applyTurnAt(ref.Epoch, func() error {
			return s.SendControl(ErrorMsg{
				Type:        TypeError,
				Code:        ErrCodePlaybackAckTimeout,
				Message:     playbackAckTimeoutNotice,
				Recoverable: true,
			})
		}) {
			return false, false
		}
		return false, true
	}
}

// settleListeningAt returns the FSM to listening after a valid final ack —
// the ONLY place an announcement hands control back, and the ONLY place the
// echo tail starts: lastPlaybackCompletedAt is stamped here and ordinary
// uplink stays gated through VADEchoTailMs. The broadcast carries the
// EFFECTIVE client state: while the classroom pause bit holds, the student
// sees paused, never a silent resume to listening (13-09).
//
// The settle is gated on the ANNOUNCEMENT'S OWN epoch, atomically with the
// state update (PR #59 review): a stale announcer — the ask whose final ack
// landed just before a pick bumped the epoch and opened the new classroom —
// must not lift the fresh greeting's playbackPending gate or start an echo
// tail over it. The state change and the state message ride ONE turn effect
// (the lesson_update pattern): a close that races the settle either wins
// the lock and refuses it outright or blocks until the state message is
// queued — the message can never trail session_ended on the wire. Returns
// whether the settle actually took effect (callers gate their onAck work on
// it).
func (tm *TurnManager) settleListeningAt(s Sender, epoch uint64) bool {
	var value string
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.state == "" {
		return false
	}
	return tm.applyTurnLocked(epoch, func() error {
		tm.state = StateListening
		tm.playbackPending = false
		now := time.Now()
		tm.lastPlaybackCompletedAt = now
		tm.echoTailUntil = now.Add(time.Duration(tm.cfg.VADEchoTailMs) * time.Millisecond)
		value = tm.effectiveClientStateLocked()
		return s.SendControl(StateMsg{Type: TypeState, Value: value})
	})
}

// resumeListeningAt returns the FSM to listening after a turn that produced
// nothing to play back (an ASR empty transcription, an unusable transcript):
// the same protected shape as settleListeningAt, but NO playback-completion
// stamp and no echo tail — silence must never masquerade as played audio,
// because the 13-09 playback-relative gates key on the real thing.
func (tm *TurnManager) resumeListeningAt(s Sender, epoch uint64) bool {
	var value string
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.state == "" {
		return false
	}
	return tm.applyTurnLocked(epoch, func() error {
		tm.state = StateListening
		tm.playbackPending = false
		value = tm.effectiveClientStateLocked()
		return s.SendControl(StateMsg{Type: TypeState, Value: value})
	})
}

// settleAnnouncementAt is the announcement's protected ACK post-processing:
// the settle and the onAck work it licenses (word events, timer arms) run
// as ONE turn effect on the announcement's own epoch. onAck executes INSIDE
// the effect — it must not take tm.mu (the locked timer/arm helpers), which
// is what keeps the arm from re-reading an epoch the FSM has moved on to.
func (tm *TurnManager) settleAnnouncementAt(s Sender, epoch uint64, onAck func()) bool {
	var value string
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.state == "" {
		return false
	}
	return tm.applyTurnLocked(epoch, func() error {
		tm.state = StateListening
		tm.playbackPending = false
		now := time.Now()
		tm.lastPlaybackCompletedAt = now
		tm.echoTailUntil = now.Add(time.Duration(tm.cfg.VADEchoTailMs) * time.Millisecond)
		value = tm.effectiveClientStateLocked()
		if err := s.SendControl(StateMsg{Type: TypeState, Value: value}); err != nil {
			return err
		}
		if onAck != nil {
			onAck()
		}
		return nil
	})
}

// failTurn reports a turn-level failure and returns the FSM to listening —
// but only if the turn is still current: an interrupted turn must not emit a
// spurious error on top of the interrupt ack. The error message, the
// playback-wait invalidation, the listening recovery and the state message
// ride ONE turn effect on the failing turn's own epoch: a close that races
// the report either refuses it outright or lands behind the accepted pair —
// the failure notice and the listening state can never trail session_ended.
// Optional fields ride the error event (e.g. the retry-attempts count of
// the invalid-response discipline), which is recorded only when the report
// was actually accepted.
func (tm *TurnManager) failTurn(s Sender, epoch uint64, code, message string, fields ...infra.Field) {
	var value string
	if !tm.applyTurnAt(epoch, func() error {
		for _, p := range tm.pendingPlayback {
			p.cancelWait()
		}
		tm.pendingPlayback = map[string]*pendingPlayback{}
		tm.playbackPending = false
		tm.state = StateListening
		value = tm.effectiveClientStateLocked()
		if err := s.SendControl(ErrorMsg{Type: TypeError, Code: code, Message: message, Recoverable: true}); err != nil {
			return err
		}
		return s.SendControl(StateMsg{Type: TypeState, Value: value})
	}) {
		return
	}
	all := make([]infra.Field, 0, 1+len(fields))
	all = append(all, infra.F("code", code))
	all = append(all, fields...)
	tm.recorder.Emit(EventError, all...)
}

// sendError is the single error egress: coded ErrorMsg + error event.
// issue #69 备选一 步骤 10: the enqueue rides the session effect — a
// session that already shipped session_ended answers NOTHING past its
// terminal message (a parse failure, a server-only frame, a read_word or a
// poem/passage/practice degradation landing after the goodbye stays
// silent). The optional fields are desensitized event annotations only
// (attempts, codes) — never transcript text (T-13-06b); the event records
// only when the reply actually shipped, sendOpenError's discipline.
func (tm *TurnManager) sendError(s Sender, code, message string, recoverable bool, fields ...infra.Field) {
	if !tm.applySessionEffect(func() error {
		return s.SendControl(ErrorMsg{Type: TypeError, Code: code, Message: message, Recoverable: recoverable})
	}) {
		return
	}
	all := make([]infra.Field, 0, 1+len(fields))
	all = append(all, infra.F("code", code))
	all = append(all, fields...)
	tm.recorder.Emit(EventError, all...)
}

// sendOpenError is sendError's session-gated sibling for replies to inbound
// frames whose admission is the frame itself: the coded answer rides
// applySessionEffect, so a session that already shipped session_ended is
// never answered past its terminal message — the reply races the goodbye
// (or a timeout close) and loses. The event records only when the reply
// actually shipped, failTurn's discipline.
func (tm *TurnManager) sendOpenError(s Sender, code, message string, recoverable bool, fields ...infra.Field) {
	if !tm.applySessionEffect(func() error {
		return s.SendControl(ErrorMsg{Type: TypeError, Code: code, Message: message, Recoverable: recoverable})
	}) {
		return
	}
	all := make([]infra.Field, 0, 1+len(fields))
	all = append(all, infra.F("code", code))
	all = append(all, fields...)
	tm.recorder.Emit(EventError, all...)
}

// ---------------------------------------------------------------------------
// Small locked helpers
// ---------------------------------------------------------------------------

// epochCurrent reports whether a turn launched at the given epoch may still
// act: the epoch must match AND the session must be open. A close does NOT
// bump the epoch (unlike an interrupt), so the closed flag rides here — PR
// #61 review: the epoch comparison alone waved late provider returns through
// after session_ended already shipped; folding the flag into the one
// predicate every turn gate shares closes that class at the root.
func (tm *TurnManager) epochCurrent(launch uint64) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.epoch == launch && !tm.closed
}

// nextTurnIDLocked allocates the next turn/announcement identity (sentence
// ids derive from it). Caller holds tm.mu.
func (tm *TurnManager) nextTurnIDLocked() uint64 {
	tm.turnID++
	return tm.turnID
}

// truncateRunes caps text to n runes (T-13-15: reply length bound before
// TTS and subtitles).
func truncateRunes(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	return string(r[:n])
}

// splitSentences breaks teacher text into speakable sentences. Chinese
// terminators 。！？；, newlines, and ASCII .!? each end a sentence (the
// terminator stays with its sentence); whitespace between sentences is
// dropped; text with no terminator is returned as one sentence (兜底单句).
// Empty/blank input yields nil.
func splitSentences(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out []string
	start := 0
	for i, r := range text {
		if !isSentenceBreak(r) {
			continue
		}
		end := i + len(string(r))
		if seg := strings.TrimSpace(text[start:end]); seg != "" {
			out = append(out, seg)
		}
		start = end
	}
	if start < len(text) {
		if seg := strings.TrimSpace(text[start:]); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func isSentenceBreak(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '.', '!', '?', '\n':
		return true
	}
	return false
}
