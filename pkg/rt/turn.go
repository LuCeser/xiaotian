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
	// EnsureLessonCards creates the lesson row carrying word cards if
	// absent and returns it (first stored content wins; a legacy row with
	// empty cards is NOT backfilled — the caller detects it).
	EnsureLessonCards(ctx context.Context, lessonID string, cards []lesson.Word) (lesson.Lesson, error)
	// AdvanceWord moves the cursor forward one word, idempotently by
	// requestID, guarded by expectedVersion.
	AdvanceWord(ctx context.Context, lessonID string, expectedVersion int, requestID string) (lesson.AdvanceResult, error)
}

// TurnManagerConfig configures one TurnManager.
type TurnManagerConfig struct {
	// LessonID pins the server-owned lesson this session binds to. Empty
	// means derive "r0-YYYYMMDD" on the Beijing calendar (same-day
	// reconnects resume the same lesson).
	LessonID string
	// Words is the due-word source (WordSource). Nil answers session_init
	// with lesson_unavailable — the classroom cannot start without it.
	// The 13-10 PyFSRS adapter injects the production implementation.
	Words WordSource
	// WordTimeout bounds how long the FSM waits for a per-word confirm
	// before auto-advancing (0 → 60s default). 13-08 defines the field;
	// the timer behavior itself lands in 13-09.
	WordTimeout time.Duration
}

// defaultWordTimeout is the per-word confirm wait used when
// TurnManagerConfig.WordTimeout is unset (13-09 arms the timer).
const defaultWordTimeout = 60 * time.Second

// Announcement purposes — the playback-completion await behaves differently
// for ordinary announcements (timeout → orderly close) versus natural
// endings (timeout → the original lesson_complete/no_due_words reason still
// ships; protocol.go sequencing contract).
const (
	purposeGreeting = "greeting"      // opening + first word
	purposeAnnounce = "announce"      // next-word announcement
	purposeChat     = "chat"          // classification reply
	purposeSummary  = "summary"       // completion wrap-up
	purposeNoDue    = "no_due_words"  // zero-due explanation
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
	purpose    string
	pcmSamples int // scheduled PCM sample count (s16le mono: bytes/2)
	done       chan struct{}
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
	Epoch           uint64
	FinalSentenceID string
	SentenceIDs     []string
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
// is only touched from the read loop and needs no lock. Provider calls
// (ASR/LLM/TTS) always happen OUTSIDE mu; their results re-acquire it.
// SendControl/SendAudio are non-blocking queue enqueues, so calling them
// under mu is safe and makes "epoch check + send" atomic with respect to
// interrupt handling — after HandleControl(interrupt) returns, no further
// chunk or subtitle of the interrupted announcement can be emitted.
type TurnManager struct {
	asr      ASRProvider
	llm      LLMProvider
	tts      TTSSynthesizer
	exec     LessonExecutor
	cfg      TurnManagerConfig
	recorder *EventRecorder
	logger   infra.Logger
	session  string

	mu sync.Mutex
	// state is one of StateListening/StateProcessing/StateSpeaking, or ""
	// before session_init / after close.
	state string
	// closed suppresses duplicate session_end emissions when the transport
	// calls Close after an end_session already closed the session.
	closed bool
	// epoch is the monotonic cancellation token. In-flight provider work
	// remembers the epoch it launched under; on mismatch it aborts.
	epoch uint64
	// turnCancel cancels the current turn's/announcement's provider context.
	turnCancel context.CancelFunc
	// turnID counts turns and announcements (sentence-id namespace).
	turnID uint64
	// tutor is the dictation FSM state; nil until a lesson session starts.
	tutor *DictationTutor
	// turnSentences are the sentences sent for the current announcement, in
	// order (the interrupt-trim source).
	turnSentences []sentenceInfo
	// acked marks sentence ids the client confirmed playing, for the trim.
	acked map[string]bool
	// pendingPlayback maps epoch/sentence_id → the outstanding completion
	// wait (T-13-15b).
	pendingPlayback map[string]*pendingPlayback
	// playbackPending marks an announcement awaiting its final ack; uplink
	// stays gated while it holds.
	playbackPending bool
	// droppedUplink counts audio chunks dropped by half-duplex gating.
	droppedUplink int

	// vad is fed from the read loop only (see concurrency note).
	vad *VAD
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
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	if recorder == nil {
		recorder = NewEventRecorder(logger, "")
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
	default:
		// Server→client types parse but are not accepted from a client;
		// the transport only moves bytes — this is the enforcement point.
		tm.sendError(s, ErrCodeInvalidMessage, "该消息类型不能由客户端发送", true)
	}
}

// handleSessionInit validates the negotiated audio format, then establishes
// the classroom under the per-lesson init lock: one creator runs the due
// fetch; legacy-empty and already-completed rows take their explicit paths.
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
	if tm.cfg.Words == nil {
		// T-13-14: fixed human wording; the adapter is wired by 13-10.
		tm.sendError(s, ErrCodeLessonUnavailable, lessonUnavailableNotice, false)
		return
	}

	lessonID := resolveLessonID(tm.cfg.LessonID, time.Now())
	unlock := lockLessonInit(lessonID)
	defer unlock()

	ctx := context.Background()
	l, err := tm.exec.GetLessonState(ctx, lessonID)
	if err != nil && !errors.Is(err, lesson.ErrLessonNotFound) {
		tm.failLessonInit(s)
		return
	}

	if errors.Is(err, lesson.ErrLessonNotFound) {
		// Fresh lesson: this process instance is the single creator (init
		// lock held) — fetch due words now.
		cards, ferr := tm.cfg.Words.FetchDueWords(ctx, dueWordsLimit)
		if ferr != nil {
			tm.failLessonInit(s)
			return
		}
		if len(cards) == 0 {
			tm.startNoDueSession(s, lessonID)
			return
		}
		l, err = tm.exec.EnsureLessonCards(ctx, lessonID, cards)
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
	case l.CurrentIndex >= len(l.Cards):
		tm.startCompletedSession(s, l)
	default:
		tm.startDictationSession(s, l)
	}
}

// failLessonInit is the single classroom-init failure egress: a coded,
// fixed-wording ErrorMsg plus the server-side error event (T-13-14).
func (tm *TurnManager) failLessonInit(s Sender) {
	tm.recorder.Emit(EventError, infra.F("code", ErrCodeLessonUnavailable))
	tm.sendError(s, ErrCodeLessonUnavailable, lessonUnavailableNotice, false)
}

// startNoDueSession answers session_ready with an empty word table, lets
// the teacher explain (zero due words), and ends the session only after the
// explanation's actual playback ack (or its bounded timeout).
func (tm *TurnManager) startNoDueSession(s Sender, lessonID string) {
	tm.mu.Lock()
	tm.state = StateListening
	tm.mu.Unlock()

	_ = s.SendControl(SessionReadyMsg{
		Type:      TypeSessionReady,
		SessionID: tm.session,
		Audio:     AudioSpec{SampleRate: requiredSampleRate, Format: requiredFormat, Channels: requiredChannels},
		TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
		Lesson:    LessonView{LessonID: lessonID, Words: []string{}},
	})
	tm.recorder.Emit(EventConnected, infra.F("session_id", tm.session))

	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	tm.turnCancel = cancel
	tm.mu.Unlock()
	go tm.runNaturalEnding(s, ctx, cancel, noDueWordsText(), purposeNoDue, ReasonNoDueWords)
}

// startCompletedSession restores the completion state for an
// already-finished lesson: session_ready then session_ended(lesson_complete)
// — no summary replay, no second SubmitReview.
func (tm *TurnManager) startCompletedSession(s Sender, l lesson.Lesson) {
	tm.mu.Lock()
	tm.state = StateListening
	tm.mu.Unlock()

	_ = s.SendControl(SessionReadyMsg{
		Type:      TypeSessionReady,
		SessionID: tm.session,
		Audio:     AudioSpec{SampleRate: requiredSampleRate, Format: requiredFormat, Channels: requiredChannels},
		TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
		Lesson:    lessonViewOf(l),
	})
	tm.recorder.Emit(EventConnected, infra.F("session_id", tm.session))
	_ = s.SendControl(SessionEndedMsg{Type: TypeSessionEnded, Reason: ReasonLessonComplete})
	tm.internalClose()
}

// startDictationSession binds the FSM to the lesson row and immediately
// speaks the opening greeting (first greeting + first word) — the RT-04
// structure where first-audible latency contains only the FSRS fetch and
// the TTS first packet, no LLM call.
func (tm *TurnManager) startDictationSession(s Sender, l lesson.Lesson) {
	tm.mu.Lock()
	tm.tutor = newDictationTutor(l)
	tm.state = StateListening
	first := tm.tutor.words[tm.tutor.currentIndex]
	total := len(tm.tutor.words)
	tm.mu.Unlock()

	_ = s.SendControl(SessionReadyMsg{
		Type:      TypeSessionReady,
		SessionID: tm.session,
		Audio:     AudioSpec{SampleRate: requiredSampleRate, Format: requiredFormat, Channels: requiredChannels},
		TTS:       TTSSpec{SampleRate: TTSOutputSampleRate},
		Lesson:    lessonViewOf(l),
	})
	tm.recorder.Emit(EventConnected, infra.F("session_id", tm.session))

	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	tm.turnCancel = cancel
	tm.mu.Unlock()
	go tm.runAnnouncement(s, ctx, cancel, greetingText(total, first.Word), purposeGreeting, func() {
		tm.announceAcked(s)
	})
}

// lessonViewOf projects a lesson row into the wire LessonView.
func lessonViewOf(l lesson.Lesson) LessonView {
	words := make([]string, 0, len(l.Cards))
	for _, c := range l.Cards {
		words = append(words, c.Word)
	}
	return LessonView{LessonID: l.LessonID, Version: l.Version, CurrentIndex: l.CurrentIndex, Words: words}
}

// announceAcked is the ack-armed callback of word announcements: mark the
// announced word event and arm the (13-09) per-word timer slot.
func (tm *TurnManager) announceAcked(s Sender) {
	tm.mu.Lock()
	idx := -1
	if tm.tutor != nil && tm.tutor.currentIndex < len(tm.tutor.words) {
		idx = tm.tutor.currentIndex
	}
	tm.mu.Unlock()
	if idx >= 0 {
		tm.recorder.Emit(EventWordAnnounced, infra.F("word_index", idx))
	}
	tm.armWordTimer()
}

// armWordTimer arms the per-word confirm timer after a word announcement's
// valid playback ack (WordTimeout countdown → auto-advance with the
// timeout outcome). 13-08 ships the armed-slot placeholder only: the FSM
// callback point is exercised by tests; the timer behavior itself is 13-09
// work (plan-locked split).
func (tm *TurnManager) armWordTimer() {
	// placeholder — implemented in 13-09
}

// runAnnouncement speaks one FSM announcement and settles the FSM only
// after its valid final playback ack — TTS send completion and audio_end
// NEVER restore listening (uplink stays gated through the ack; 13-09 adds
// the 500ms echo tail afterwards). Timeout degrades per purpose: ordinary
// announcements orderly-close; natural endings still ship their original
// reason first.
func (tm *TurnManager) runAnnouncement(s Sender, ctx context.Context, cancel context.CancelFunc, text, purpose string, onAck func()) {
	defer cancel()

	tm.mu.Lock()
	epoch := tm.epoch
	turnID := tm.nextTurnIDLocked()
	tm.mu.Unlock()

	ref := tm.speakTurn(s, turnID, epoch, text, purpose, ctx, cancel)
	completed, timedOut := tm.awaitPlaybackCompletion(s, ref, purpose)
	if !completed {
		if timedOut {
			tm.orderlyClose()
		}
		return
	}
	tm.settleListening(s)
	if onAck != nil {
		onAck()
	}
}

// runNaturalEnding speaks a natural-ending announcement (no-due words, or
// the completion summary) and finishes with session_ended carrying the
// ORIGINAL reason — after the valid ack, or after the bounded ack timeout
// (ErrorMsg first, reason second — the protocol.go degraded order; never a
// fabricated completion and never a timeout reason).
func (tm *TurnManager) runNaturalEnding(s Sender, ctx context.Context, cancel context.CancelFunc, text, purpose, reason string) {
	defer cancel()

	tm.mu.Lock()
	epoch := tm.epoch
	turnID := tm.nextTurnIDLocked()
	tm.mu.Unlock()

	ref := tm.speakTurn(s, turnID, epoch, text, purpose, ctx, cancel)
	completed, timedOut := tm.awaitPlaybackCompletion(s, ref, purpose)
	if !completed && !timedOut {
		// Interrupted or disconnected: cancel the wait, never message.
		tm.internalClose()
		return
	}
	if purpose == purposeSummary {
		tm.recorder.Emit(EventLessonComplete, infra.F("reason", reason))
	}
	_ = s.SendControl(SessionEndedMsg{Type: TypeSessionEnded, Reason: reason})
	tm.internalClose()
}

// handleInterrupt is the epoch bump + provider cancellation + short-history
// trim (Pitfall 9 on the FSM deque) + old-epoch playback-wait invalidation.
func (tm *TurnManager) handleInterrupt(s Sender, m InterruptMsg) {
	tm.mu.Lock()
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

	if tm.state != "" && !tm.closed {
		tm.state = StateListening
	}
	tm.mu.Unlock()

	tm.vad.Reset()

	tm.recorder.Emit(EventInterruptHandled,
		infra.F("old_epoch", oldEpoch),
		infra.F("new_epoch", newEpoch),
		infra.F("last_sentence_id", m.LastSentenceID),
		infra.F("played_until_ms", m.PlayedUntilMs),
	)
	_ = s.SendControl(InterruptAckMsg{
		Type:           TypeInterruptAck,
		OldEpoch:       oldEpoch,
		NewEpoch:       newEpoch,
		LastSentenceID: m.LastSentenceID,
		PlayedUntilMs:  m.PlayedUntilMs,
	})
	_ = s.SendControl(StateMsg{Type: TypeState, Value: StateListening})
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
// recorder's anchor with every rt_event t_mono_ns (13-03 protocol).
func (tm *TurnManager) handlePing(s Sender, m PingMsg) {
	_ = s.SendControl(PongMsg{
		Type:          TypePong,
		TClientMs:     m.TClientMs,
		TServerMonoNs: tm.recorder.MonoNs(),
	})
}

// handleEndSession is the explicit goodbye (RT-10/SC7 server half).
func (tm *TurnManager) handleEndSession(s Sender) {
	_ = s.SendControl(SessionEndedMsg{Type: TypeSessionEnded, Reason: "client_end"})
	tm.recorder.Emit(EventSessionEnd, infra.F("session_id", tm.session))
	tm.internalClose()
}

// internalClose cancels in-flight work, invalidates playback waits, and
// resets the FSM without emitting the session_end event again. It also
// cancels any playback-ack wait (异常断线取消等待).
func (tm *TurnManager) internalClose() {
	tm.mu.Lock()
	if tm.turnCancel != nil {
		tm.turnCancel()
	}
	for _, p := range tm.pendingPlayback {
		p.cancelWait()
	}
	tm.pendingPlayback = map[string]*pendingPlayback{}
	tm.playbackPending = false
	tm.state = ""
	tm.closed = true
	tm.mu.Unlock()
	tm.vad.Reset()
}

// orderlyClose is the bounded-timeout degraded ending of an ordinary
// announcement: the ErrorMsg(playback_ack_timeout) already went out in the
// await; this closes the session in order — no fabricated completion, no
// timer arm, no word advance, and no new timeout end reason.
func (tm *TurnManager) orderlyClose() {
	tm.internalClose()
}

// Close implements SessionManager: the transport read loop exited for any
// reason (disconnect, deadline, read error). Idempotent — an end_session
// path already closed the session.
func (tm *TurnManager) Close(Sender) {
	tm.mu.Lock()
	already := tm.closed
	tm.mu.Unlock()
	if already {
		return
	}
	tm.recorder.Emit(EventSessionEnd, infra.F("session_id", tm.session))
	tm.internalClose()
}

// ---------------------------------------------------------------------------
// SessionManager: audio path
// ---------------------------------------------------------------------------

// IngestAudio is the half-duplex gate (Pattern 3): while the teacher speaks
// — INCLUDING the playback-pending window from the first audio_start until
// the valid final ack (TTS completion and audio_end never lift the gate) —
// uplink audio is dropped and counted. In listening state the VAD segments
// the stream; each closed utterance starts one turn goroutine. Interrupt
// control messages always bypass this gate (they travel HandleControl).
func (tm *TurnManager) IngestAudio(s Sender, data []byte) {
	tm.mu.Lock()
	state := tm.state
	tm.mu.Unlock()
	if state != StateListening {
		tm.mu.Lock()
		tm.droppedUplink++
		tm.mu.Unlock()
		return
	}
	segment, closed := tm.vad.Feed(data)
	if !closed {
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
	if tm.state != StateListening || tm.closed || tm.tutor == nil {
		tm.mu.Unlock()
		return
	}
	epoch := tm.epoch
	turnID := tm.nextTurnIDLocked()
	tm.state = StateProcessing
	ctx, cancel := context.WithCancel(context.Background())
	tm.turnCancel = cancel
	// Immutable advance target captured BEFORE classification (T-13-15c):
	// a late confirm may only ever act on this word/version pair.
	target := tm.tutor.turnTarget()
	tm.mu.Unlock()
	defer cancel()

	tm.recorder.Emit(EventSpeechStarted, infra.F("turn_id", turnID))
	tm.recorder.Emit(EventSpeechEnded, infra.F("turn_id", turnID))
	_ = s.SendControl(StateMsg{Type: TypeState, Value: StateProcessing})
	_ = s.SendControl(TurnStartMsg{Type: TypeTurnStart, TurnID: turnID, Epoch: epoch})

	transcript, err := tm.asr.Transcribe(ctx, segment)
	if err != nil {
		tm.failTurn(s, epoch, ErrCodeProviderFailed, "语音识别失败，请再对我说一遍")
		return
	}
	if transcript == "" {
		// Nothing usable recognized: back to listening. (13-09 refines the
		// silent tolerance of legal-empty ASR results.)
		tm.settleListening(s)
		return
	}
	_ = s.SendControl(TranscriptMsg{Type: TypeTranscript, TurnID: turnID, Text: transcript})
	tm.mu.Lock()
	tm.tutor.appendUserRound(transcript)
	tm.mu.Unlock()

	intent, reply := tm.classifyTurn(ctx, transcript, target)
	if !tm.epochCurrent(epoch) {
		return // interrupted mid-classification: abort the whole turn silently
	}
	tm.dispatchTurn(s, ctx, cancel, turnID, epoch, intent, reply, target)
}

// dispatchTurn routes a whitelisted intent into the FSM. Only confirm drives
// pacing; chat (and repeat/slow/hint/meaning/pause/resume, whose dedicated
// behaviors 13-09/13-10 fill) speaks the classification reply.
func (tm *TurnManager) dispatchTurn(s Sender, ctx context.Context, cancel context.CancelFunc, turnID, epoch uint64, intent, reply string, target TurnTarget) {
	switch intent {
	case intentConfirm:
		tm.confirmTarget(s, ctx, cancel, turnID, epoch, reply, target)
	default:
		tm.runAnnouncement(s, ctx, cancel, reply, purposeChat, nil)
	}
}

// confirmTarget advances the classroom through the target captured at
// classification start. The three AdvanceWord outcomes are mutually
// exclusive; NONE of them re-interprets a late confirm against the
// post-return currentIndex (T-13-15c):
//
//   - first commit (Idempotent=false, nil): record outcome, lesson_update,
//     word_advanced event, next-word announcement or completion;
//   - idempotent hit (Idempotent=true, nil): align the local cursor to the
//     FIRST stored result — no outcome write, no message, no narration, no
//     completion;
//   - version conflict: read-back alignment ONLY — no retry write, no
//     side effects.
func (tm *TurnManager) confirmTarget(s Sender, ctx context.Context, cancel context.CancelFunc, turnID, epoch uint64, reply string, target TurnTarget) {
	tm.mu.Lock()
	lessonID := tm.tutor.lessonID
	valid := !tm.tutor.completed && target.WordIndex >= 0 && target.WordIndex < len(tm.tutor.words)
	tm.mu.Unlock()
	if !valid {
		// Completed or out-of-range target: pacing owns the cursor — this
		// confirm degrades to a plain chat reply.
		tm.runAnnouncement(s, ctx, cancel, reply, purposeChat, nil)
		return
	}

	requestID := fmt.Sprintf("%s-advance-%d", lessonID, target.WordIndex)
	res, err := tm.exec.AdvanceWord(ctx, lessonID, target.ExpectedVersion, requestID)
	if err != nil {
		var conflict *lesson.ConflictError
		if errors.As(err, &conflict) {
			// Conflict: read-back sync only. No retry write, no narration,
			// no completion — the store already holds the truth.
			if l, gerr := tm.exec.GetLessonState(ctx, lessonID); gerr == nil {
				tm.mu.Lock()
				tm.tutor.align(l.CurrentIndex, l.Version)
				tm.mu.Unlock()
			}
			tm.settleListening(s)
			return
		}
		tm.failTurn(s, epoch, ErrCodeProviderFailed, "课堂进度更新失败，请再试一次")
		return
	}
	if res.Idempotent {
		// Duplicate confirm: align to the first result and do NOTHING else.
		tm.mu.Lock()
		tm.tutor.align(res.NewIndex, res.NewVersion)
		tm.mu.Unlock()
		tm.settleListening(s)
		return
	}

	// First commit: outcome + wire update + narration (or completion).
	tm.mu.Lock()
	tm.tutor.align(res.NewIndex, res.NewVersion)
	tm.tutor.commitOutcome(target.WordIndex, outcomeConfirmed)
	finished := res.NewIndex >= len(tm.tutor.words)
	tm.mu.Unlock()

	_ = s.SendControl(LessonUpdateMsg{
		Type:         TypeLessonUpdate,
		LessonID:     lessonID,
		Version:      res.NewVersion,
		CurrentIndex: res.NewIndex,
		RequestID:    requestID,
	})
	tm.recorder.Emit(EventWordAdvanced,
		infra.F("word_index", target.WordIndex),
		infra.F("new_index", res.NewIndex),
		infra.F("version", res.NewVersion),
	)

	if finished {
		tm.enterCompletion(s, ctx, cancel, epoch)
		return
	}

	tm.mu.Lock()
	word := tm.tutor.words[res.NewIndex]
	text := announceText(res.NewIndex+1, len(tm.tutor.words), word.Word)
	tm.mu.Unlock()
	tm.runAnnouncement(s, ctx, cancel, text, purposeAnnounce, func() {
		tm.announceAcked(s)
	})
}

// enterCompletion runs the wrap-up: reachable only from the first-commit
// branch (the completed flag is set under mu below, and a duplicate confirm
// of the same word replays idempotently — it can never re-enter).
// Order is 先执行后播报: the review write actually executes first; the
// spoken summary then reports the REAL result sets; session_ended
// (lesson_complete) waits for the summary's valid playback ack.
func (tm *TurnManager) enterCompletion(s Sender, ctx context.Context, cancel context.CancelFunc, epoch uint64) {
	if !tm.epochCurrent(epoch) {
		return
	}
	tm.mu.Lock()
	tm.tutor.markCompleted()
	total := len(tm.tutor.words)
	confirmed, timedOutN, hinted := tm.tutor.outcomeCounts()
	results := tm.tutor.ratings()
	tm.mu.Unlock()

	summary := completionSummaryText(total, confirmed, timedOutN, hinted)
	if len(results) > 0 {
		wres, err := tm.cfg.Words.SubmitReview(ctx, results)
		switch {
		case err != nil:
			// Honest failure: the record was NOT saved; the student hears
			// that — never a narrated success (transparency prohibition).
			tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
			summary += reviewFailedSuffix()
		default:
			// Real result sets decide the reported success: requested vs
			// updated vs missing, never "nil error = full success". A
			// partial or zero update ships the same event with the real
			// counts plus a server-side error marker and an honest
			// "saved M/N" spoken note — and the batch is NEVER retried
			// blindly (S-05).
			tm.recorder.Emit(EventReviewWritten,
				infra.F("requested", len(wres.Requested)),
				infra.F("updated", len(wres.Updated)),
				infra.F("missing", len(wres.Missing)),
			)
			if updated := len(wres.Updated); updated < len(results) {
				tm.recorder.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
				summary += fmt.Sprintf("本次保存 %d/%d 个。", updated, len(results))
			}
		}
	}

	tm.runNaturalEnding(s, ctx, cancel, summary, purposeSummary, ReasonLessonComplete)
}

// speakTurn streams the text sentence by sentence: subtitle first, audio
// chunks forwarded the instant they arrive. Each sentence's pendingPlayback
// entry is registered in the SAME locked section that queues its
// audio_start — an ack can never outrun its pending entry (T-13-15b). The
// FSM stays speaking/playback-pending on return; the caller awaits the
// final ack via awaitPlaybackCompletion.
func (tm *TurnManager) speakTurn(s Sender, turnID, epoch uint64, text, purpose string, ctx context.Context, cancel context.CancelFunc) PlaybackRef {
	text = truncateRunes(text, maxReplyRunes)
	sentences := splitSentences(text)

	tm.mu.Lock()
	if tm.epoch != epoch {
		// Stale before the first sound: nothing registered, nothing spoken.
		tm.mu.Unlock()
		return PlaybackRef{}
	}
	if tm.tutor != nil {
		tm.tutor.recordTeacherReply(text)
	}
	tm.turnSentences = nil
	tm.acked = map[string]bool{}
	tm.state = StateSpeaking
	tm.playbackPending = true
	tm.mu.Unlock()

	_ = s.SendControl(StateMsg{Type: TypeState, Value: StateSpeaking})

	var ref PlaybackRef
	ref.Epoch = epoch
	for i, sentence := range sentences {
		sentID := fmt.Sprintf("t%d-s%d", turnID, i)

		// Subtitle first (字幕先行) + pending registration BEFORE
		// audio_start is queued — atomic against interrupt handling: after
		// an interrupt is acknowledged, no further subtitle/chunk of the
		// interrupted announcement can go out.
		tm.mu.Lock()
		if tm.epoch != epoch {
			tm.mu.Unlock()
			return ref
		}
		tm.pendingPlayback[pendingKey(epoch, sentID)] = &pendingPlayback{purpose: purpose, done: make(chan struct{})}
		tm.turnSentences = append(tm.turnSentences, sentenceInfo{id: sentID, text: sentence})
		_ = s.SendControl(TeacherSentenceMsg{
			Type: TypeTeacherSentence, TurnID: turnID, Epoch: epoch, SentenceID: sentID, Text: sentence,
		})
		_ = s.SendControl(AudioStartMsg{Type: TypeAudioStart, TurnID: turnID, Epoch: epoch, SentenceID: sentID})
		tm.mu.Unlock()

		// pcm arrives as bare s16le bytes (AudioChunkFunc); the wire
		// encoding is digested by the 13-06 adapter, never here (M-1).
		var sentSamples int
		_, err := tm.tts.SynthesizeStream(ctx, sentence, func(pcm []byte, isFinal bool) error {
			tm.mu.Lock()
			if tm.epoch != epoch {
				tm.mu.Unlock()
				cancel() // late chunk: abort the provider stream too
				return ErrStaleEpoch
			}
			sentSamples += len(pcm) / 2 // s16le mono: 2 bytes per sample
			err := s.SendAudio(pcm)
			tm.mu.Unlock()
			return err
		})
		if err != nil && !errors.Is(err, ErrStaleEpoch) && tm.epochCurrent(epoch) {
			// A genuine provider failure on a still-live announcement.
			tm.failTurn(s, epoch, ErrCodeProviderFailed, "老师的声音出了点问题，请再说一遍")
			return ref
		}

		tm.mu.Lock()
		if p, ok := tm.pendingPlayback[pendingKey(epoch, sentID)]; ok {
			p.pcmSamples = sentSamples
		}
		stale := tm.epoch != epoch
		if !stale {
			_ = s.SendControl(AudioEndMsg{Type: TypeAudioEnd, TurnID: turnID, Epoch: epoch, SentenceID: sentID})
		}
		tm.mu.Unlock()
		if stale {
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
// timer arm, no word advance.
func (tm *TurnManager) awaitPlaybackCompletion(s Sender, ref PlaybackRef, purpose string) (completed, timedOut bool) {
	if ref.FinalSentenceID == "" {
		return false, false // nothing scheduled (stale before the first sound)
	}
	tm.mu.Lock()
	fin, ok := tm.pendingPlayback[pendingKey(ref.Epoch, ref.FinalSentenceID)]
	remaining := 0
	for _, sid := range ref.SentenceIDs {
		if p, ok := tm.pendingPlayback[pendingKey(ref.Epoch, sid)]; ok && !p.completed {
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
		tm.recorder.Emit(EventPlaybackAckTimeout,
			infra.F("purpose", purpose),
			infra.F("wait_ms", wait.Milliseconds()),
		)
		_ = s.SendControl(ErrorMsg{
			Type:        TypeError,
			Code:        ErrCodePlaybackAckTimeout,
			Message:     playbackAckTimeoutNotice,
			Recoverable: true,
		})
		return false, true
	}
}

// settleListening returns the FSM to listening after a valid final ack —
// the ONLY place an announcement hands control back (13-09 then adds the
// echo tail).
func (tm *TurnManager) settleListening(s Sender) {
	tm.mu.Lock()
	if tm.state == "" || tm.closed {
		tm.mu.Unlock()
		return
	}
	tm.state = StateListening
	tm.playbackPending = false
	tm.mu.Unlock()
	_ = s.SendControl(StateMsg{Type: TypeState, Value: StateListening})
}

// failTurn reports a turn-level failure and returns the FSM to listening —
// but only if the turn is still current: an interrupted turn must not emit a
// spurious error on top of the interrupt ack. Any playback waits the failed
// announcement left behind are invalidated.
func (tm *TurnManager) failTurn(s Sender, epoch uint64, code, message string) {
	if !tm.epochCurrent(epoch) {
		return
	}
	tm.sendError(s, code, message, true)
	tm.mu.Lock()
	for _, p := range tm.pendingPlayback {
		p.cancelWait()
	}
	tm.pendingPlayback = map[string]*pendingPlayback{}
	tm.playbackPending = false
	tm.state = StateListening
	tm.mu.Unlock()
	_ = s.SendControl(StateMsg{Type: TypeState, Value: StateListening})
}

// sendError is the single error egress: coded ErrorMsg + error event.
func (tm *TurnManager) sendError(s Sender, code, message string, recoverable bool) {
	_ = s.SendControl(ErrorMsg{Type: TypeError, Code: code, Message: message, Recoverable: recoverable})
	tm.recorder.Emit(EventError, infra.F("code", code))
}

// ---------------------------------------------------------------------------
// Small locked helpers
// ---------------------------------------------------------------------------

func (tm *TurnManager) epochCurrent(launch uint64) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.epoch == launch
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
