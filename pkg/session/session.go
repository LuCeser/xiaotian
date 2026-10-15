package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/tts"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// eventBufferSize is the capacity of a Session's events channel. A buffered
// channel decouples the Router worker (which pushes events) from the event loop
// (which processes them). T-07-05: a flood drops events (logged) rather than
// blocking the Router worker.
const eventBufferSize = 32

// SessionStreamer is the subset of *tts.Streamer the Session uses for D-13
// word-audio streaming. *tts.Streamer satisfies it structurally. Defined as a
// narrow seam so tests can inject a fake streamer (mirrors tts.Sender, D-05).
type SessionStreamer interface {
	// StreamWord delivers audio for a single word to the given client. The
	// Session owns this call per D-13 option B (server-side side channel).
	StreamWord(ctx context.Context, client tts.Sender, sessionID string, wordID string, startSeq uint32) error
	// ClearSeqCounter removes the streamer's per-session monotonic sequence
	// counter entry (D-11 teardown hook). Called on session exit so the
	// seqCounters map stays bounded under the 100-concurrent budget (RESEARCH
	// Pitfall 5 memory-leak mitigation).
	ClearSeqCounter(sessionID string)
}

// SessionHub is the subset of *ws.Hub the Session uses for lifecycle cleanup.
// *ws.Hub satisfies it structurally. Defined as a narrow seam so tests can
// inject a fake hub (mirrors TTSSessionGenerator, D-05).
type SessionHub interface {
	// Unregister removes the client from the hub. Non-blocking, idempotent
	// per CR-01 (checks closed-flag before sending to the channel).
	Unregister(c *ws.Client)
}

// Session is the in-memory authority for one dictation session's lifecycle
// (D-02, D-06). It binds a WS client to an FSRS lock + TTS cache + FSM state.
//
// Concurrency contract (D-02): the mutable FSM fields (state, deadline) are
// protected by mu. The event-loop goroutine is the sole writer via transition()
// and onTransition(); external readers (sweeper, tests) use the exported
// accessors (State, Deadline) which take RLock. wordQueue and wordIndex are
// accessed only within the event-loop goroutine (single-writer, no mutex
// needed). prevState and repeatPending are likewise touched ONLY on the
// event-loop goroutine: transition() writes them under the write lock, but
// they have no cross-goroutine readers — onTransition() reads (and clears)
// them lock-free on that same goroutine (see onTransition). Unlike
// state/deadline, which DO have cross-goroutine readers via the RLock-taking
// accessors, these two fields never need a lock on the read side.
type Session struct {
	// --- Identity ---
	id     string // TTS/FSRS session UUID (crypto/rand hex v4)
	dbID   int64  // audit row id (best-effort, D-06; may lag behind)
	connID string

	// --- FSM state (protected by mu for cross-goroutine reads) ---
	mu               sync.RWMutex
	state            database.SessionState
	prevState        database.SessionState // Ticket 02: track previous state for resume detection — written ONLY by transition() under mu on the event-loop goroutine; read ONLY by onTransition()'s resume-from-pause check on that same goroutine (D-02 single-writer, same as streamGen) — the unlocked read is race-free by program order, no RLock needed or allowed.
	wordQueue        []pybridge.DueCard
	wordIndex        int
	deadline         time.Time
	currentWordID    string    // Track current word for progress frames (08-06)
	sessionStartTime time.Time // Track session start time for duration calculation (08-06)
	repeatPending    bool      // Ticket 02 DICT-07: flag set by transition, cleared by onTransition — set by transition() under mu (repeat branches), read AND cleared ONLY by onTransition() on the same event-loop goroutine (D-02 single-writer, same as streamGen) — lock-free by program order, no lock needed or allowed.

	// --- FSRS lock scope (captured at NewSession, replayed at exit — Pitfall 4) ---
	lockCategory string
	lockUnit     string
	// lockCardIDs are the IDs SelectDueCards actually locked (one per queue
	// card, Ticket 07). Exit replays them as a SCOPED unlock: a category-wide
	// release would drop OTHER sessions' in-flight locks — two students
	// dictating the same category is the supported multi-user posture.
	lockCardIDs []int64

	// --- Word audio source (captured at NewSession — DICT-03) ---
	// audioSource records which TTS backend generated this session's word
	// audio (tts.SourceMimo = single-pronunciation tracks; tts.SourceVolcengine
	// = paired "word. word." tracks). Written once before run() starts and
	// read only on the event-loop goroutine (sendWordCompleteFrame) plus the
	// AudioSource() accessor from tests post-construction — the NewSession
	// return / manager map insertion establishes happens-before, so no lock
	// is needed (same discipline as wordQueue).
	audioSource tts.AudioSource

	// --- Service dependencies ---
	client   *ws.Client // frame-send target + hub identity (D-05)
	fsrsSvc  fsrs.FSRSService
	gen      TTSSessionGenerator // *tts.Generator satisfies structurally
	streamer SessionStreamer     // *tts.Streamer satisfies structurally
	repo     database.SessionRepository
	pool     *infra.WorkerPool
	hub      SessionHub // *ws.Hub satisfies structurally
	logger   infra.Logger
	cfg      infra.Config

	// --- Event loop ---
	events        chan SessionEvent
	droppedEvents atomic.Int64 // events Push discarded because the buffer was full
	runCtx        context.Context
	cancel        context.CancelFunc
	done          chan struct{}      // closed when run() exits (liveness signal for tests/sweeper)
	streamCancel  context.CancelFunc // cancels in-flight StreamWord (set by spawnStreamWord in the loop goroutine)
	streamGen     uint64             // spawnStreamWord's monotonically increasing stream generation; read/written ONLY on the event-loop goroutine (D-02 single-writer, same as wordQueue/wordIndex — no lock). Spawned goroutines only ever use the by-value copy captured via the go statement.
	onExit        func()             // set by Manager.NewSession for map cleanup
}

// NewSession constructs a Session by acquiring the FSRS fetch-lock, generating
// TTS audio, and recording the audit row. It does NOT spawn the event-loop
// goroutine — the caller (Manager.NewSession, or the handler in Plan 03) calls
// s.Start(connCtx) to begin the loop.
//
// Init sequence (RESEARCH §"FSRS Lock Integration", DICT-02, D-11):
//  1. SelectDueCards — acquire the fetch-lock + word queue.
//  2. Empty result → ErrNoDueWords (D-02-a).
//  3. Generate TTS UUID + GenerateForSession (before the event loop, Pitfall 3).
//  4. On TTS failure → non-defer UnlockCards (D-10 init path; the defer chain
//     is not installed yet because run() has not started).
//  5. Build the struct + audit Create (fire-and-forget via pool.Submit, D-06).
//
// The client parameter may be nil in tests that do not exercise frame sending
// or hub interaction; production always passes a live *ws.Client.
func NewSession(
	ctx context.Context,
	client *ws.Client,
	connID string,
	fsrsSvc fsrs.FSRSService,
	gen TTSSessionGenerator,
	streamer SessionStreamer,
	repo database.SessionRepository,
	pool *infra.WorkerPool,
	hub SessionHub,
	logger infra.Logger,
	cfg infra.Config,
	selectOpts fsrs.SelectOpts,
) (*Session, error) {
	// Nil-guard the deps the init path actually uses (D-42 DI).
	if fsrsSvc == nil {
		return nil, errors.New("session: fsrsSvc must not be nil")
	}
	if gen == nil {
		return nil, errors.New("session: gen must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}

	// --- Step 1: SelectDueCards — acquire the FSRS fetch-lock + word queue. ---
	cards, err := fsrsSvc.SelectDueCards(ctx, selectOpts)
	if err != nil {
		return nil, errors.Wrap(err, "session: select due cards")
	}

	// Resolve the effective Category/Unit the same way fsrsService.SelectDueCards
	// does (Pitfall 4: UnlockCards is Category+Unit scoped, not card-ID). These
	// resolved values are replayed verbatim at exit (releaseFSRSLock).
	resolvedCategory := selectOpts.Category
	if resolvedCategory == "" {
		resolvedCategory = cfg.FSRS.DefaultCategory
	}
	resolvedUnit := ""
	if len(selectOpts.Units) > 0 {
		resolvedUnit = selectOpts.Units[0]
	} else if cfg.FSRS.DefaultUnit != "" {
		resolvedUnit = cfg.FSRS.DefaultUnit
	}

	// --- Step 2: Empty result → ErrNoDueWords (D-02-a). ---
	if len(cards) == 0 {
		return nil, ErrNoDueWords
	}

	// --- Step 3: TTS UUID + GenerateForSessionWithSource (Pitfall 3: before the loop). ---
	// The card IDs captured here are the lock scope replayed on BOTH unlock
	// paths (Ticket 07): the TTS-failure init path below and the D-10 exit
	// defer chain.
	// DICT-03: the WithSource variant also reports which backend produced
	// the audio; the session remembers it so word_complete frames can carry
	// the audio_plays hint (single-pronunciation mimo tracks need the
	// frontend to schedule the double play, paired volcengine tracks do not).
	cardIDs := make([]int64, len(cards))
	sessionID := newSessionID()
	words := make([]string, len(cards))
	for i, c := range cards {
		words[i] = c.Word
		cardIDs[i] = int64(c.ID)
	}
	// The paths map is intentionally discarded — the streamer reads the cache
	// by (sessionID, wordID), so only the source annotation travels forward.
	_, audioSource, err := gen.GenerateForSessionWithSource(ctx, sessionID, words)
	if err != nil {
		// --- Step 4: TTS failure — non-defer UnlockCards (D-10 init path). ---
		// The defer chain is NOT installed yet (run() hasn't started), so the
		// unlock must happen explicitly here. Using the caller's ctx is correct
		// because it is still live at init time (unlike the deferred unlock in
		// run(), which needs a fresh ctx — Pitfall 1). Scoped to the card IDs
		// this session locked (Ticket 07) — a category sweep would drop other
		// sessions' locks.
		if _, unlockErr := fsrsSvc.UnlockCards(ctx, fsrs.UnlockOpts{
			Category: resolvedCategory,
			Unit:     resolvedUnit,
			IDs:      cardIDs,
		}); unlockErr != nil {
			logger.Error("session: failed to release FSRS lock after TTS failure",
				infra.F("component", "session"),
				infra.F("category", resolvedCategory),
				infra.F("unit", resolvedUnit),
				infra.ErrField(unlockErr),
			)
		}
		return nil, errors.Wrap(err, "session: generate TTS")
	}

	// --- Step 5: Build the Session struct. ---
	s := &Session{
		id:               sessionID,
		connID:           connID,
		state:            database.StateIdle,
		wordQueue:        cards,
		wordIndex:        0,
		deadline:         time.Now().Add(cfg.SessionTimeout.IdleStateTimeout),
		lockCategory:     resolvedCategory,
		lockUnit:         resolvedUnit,
		lockCardIDs:      cardIDs,
		audioSource:      audioSource,
		client:           client,
		fsrsSvc:          fsrsSvc,
		gen:              gen,
		streamer:         streamer,
		repo:             repo,
		pool:             pool,
		hub:              hub,
		logger:           logger,
		cfg:              cfg,
		events:           make(chan SessionEvent, eventBufferSize),
		currentWordID:    "",         // Initialize current word ID tracker (08-06)
		sessionStartTime: time.Now(), // Track session start for progress frames (08-06)
	}

	// --- Step 6: Audit Create (synchronous to capture dbID). ---
	// FIX: Make Create synchronous to capture the returned database ID into s.dbID.
	// The original fire-and-forget approach left s.dbID at 0, causing
	// "update state for session 0: session not found" errors in subsequent
	// database operations (auditState, auditEvent).
	if s.repo != nil {
		dbID, err := s.repo.Create(ctx, database.Session{
			ConnectionID: s.connID,
			State:        database.StateIdle,
			WordCount:    len(s.wordQueue),
		})
		if err != nil {
			s.logger.Error("session: audit create failed",
				infra.F("component", "session"),
				infra.F("conn_id", s.connID),
				infra.ErrField(err),
			)
			// Continue anyway - session can function without audit trail
		} else {
			s.dbID = dbID // Store the database ID for subsequent operations
			s.logger.Debug("session: audit create succeeded",
				infra.F("component", "session"),
				infra.F("conn_id", s.connID),
				infra.F("db_id", dbID),
			)
		}
	}

	// Do NOT spawn go s.run(ctx) — the caller does via Start(ctx).
	return s, nil
}

// ---------------------------------------------------------------------------
// Lifecycle: Start, run (event loop), Push
// ---------------------------------------------------------------------------

// Start derives a cancellable context from ctx and spawns the run goroutine.
// Per Pitfall 6: ctx MUST be the per-connection context so that a WS disconnect
// cancels the session immediately (firing the D-10 defer chain).
func (s *Session) Start(ctx context.Context) {
	s.done = make(chan struct{})
	s.runCtx, s.cancel = context.WithCancel(ctx)
	go s.run(s.runCtx)
}

// run is the single-writer event-loop goroutine (D-02). It mirrors ws.Hub.Run:
// a for-select on the events channel, a per-state deadline timer, and ctx.Done.
//
// The D-10 defer chain is registered at the top of run() in LIFO order so it
// fires on EVERY exit path (normal return, timeout, ctx cancel, panic):
//
//	registration order (LIFO execution):
//	  1. close(done)            — runs LAST (after recovery)
//	  2. panic recovery         — catches unrecovered panics → logged
//	  3. onExit (map cleanup)   — removes the session from the Manager map
//	  4. releaseFSRSLock        — fresh-ctx UnlockCards (Pitfall 1)
//	  5. cleanupTTS             — removes TTS cache dir
//	  6. unregisterFromHub      — runs FIRST (hub identity cleanup)
func (s *Session) run(ctx context.Context) {
	// 1. close(done) — registered FIRST → runs LAST.
	defer func() {
		if s.done != nil {
			close(s.done)
		}
	}()

	// 2. Panic recovery — without this an unrecovered panic in the session
	// goroutine crashes the entire process. The three D-10 defers below still
	// fire during panic unwinding, so D-10's lock release holds; but the
	// process dies before that matters. Recovery turns a fatal process crash
	// into a logged + cleaned-up single-session failure (mirrors ws.readPump).
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("session: run panic recovered",
				infra.F("component", "session"),
				infra.F("session_id", s.id),
				infra.F("panic", r),
				infra.F("stack", string(debug.Stack())),
			)
		}
	}()

	// 3. onExit (map cleanup) — runs after the D-10 cleanup so the session is
	// fully torn down before map removal.
	defer func() {
		if s.onExit != nil {
			s.onExit()
		}
	}()

	// 4-6. D-10 cleanup chain (reverse registration = forward execution).
	defer s.releaseFSRSLock()   // runs THIRD
	defer s.cleanupTTS()        // runs SECOND
	defer s.unregisterFromHub() // runs FIRST

	for {
		select {
		case ev := <-s.events:
			// Timeout/Cancel are loop-level signals — exit immediately (D-10
			// defer chain fires). transition() returns nil for these but the
			// loop handles them here rather than delegating.
			if ev.Type == EventTimeout || ev.Type == EventCancel {
				return
			}
			// 260823-sdx: drop stale WordComplete events. A superseded stream
			// (cancelled by a repeat/resume/next_word re-spawn) may still
			// complete and push its event; its generation no longer matches
			// s.streamGen, so it is a stale artifact of an expected race —
			// skip it rather than let it drive the FSM at the wrong moment
			// (e.g. cutting a new word's LISTENING short into DICTATING).
			// Current-generation events are unaffected, including a
			// WordComplete arriving while already DICTATING or PAUSED (the
			// replay-completion self-loops in the FSM accept these, and
			// handleEvent sends the word_complete frame after the replayed
			// audio has streamed).
			if ev.Type == EventWordComplete && ev.StreamGen != s.streamGen {
				s.logger.Debug("session: dropping stale word_complete event",
					infra.F("component", "session"),
					infra.F("session_id", s.id),
					infra.F("word_id", ev.WordID),
					infra.F("event_stream_gen", ev.StreamGen),
					infra.F("current_stream_gen", s.streamGen),
				)
				continue
			}
			s.handleEvent(ev)

		// Per-state deadline timer (D-08). Go 1.23+ GC-safe per Pitfall 2.
		// time.After is re-evaluated on every loop iteration so an onTransition
		// deadline update is reflected immediately.
		case <-time.After(time.Until(s.Deadline())):
			// PAUSED state: no timeout — the student controls when to resume.
			// Reset the deadline so the timer doesn't keep firing.
			if s.State() == database.StatePaused {
				s.mu.Lock()
				s.deadline = time.Now().Add(s.cfg.SessionTimeout.PausedStateTimeout)
				s.mu.Unlock()
				continue
			}
			// DICTATING timeout means the student didn't answer in time: skip
			// the word on this goroutine and keep the session alive. Pushing
			// the skip back onto s.events would block the only consumer when
			// the buffer is full, and leaving the deadline in the past makes
			// time.After fire again immediately. Any other state timeout
			// terminates the session (existing behavior).
			if s.State() == database.StateDictating {
				s.logger.Debug("session: dictating timeout, skipping word",
					infra.F("component", "session"),
					infra.F("session_id", s.id),
				)
				s.skipTimedOutWord()
				continue
			}
			return

		case <-ctx.Done():
			return
		}
	}
}

// Push sends an event onto the session's events channel. A full buffer
// drops the event and counts it (T-07-05: a flood must not block the
// router worker). A cancelled session returns without blocking and
// without counting a drop. Push before Start is a no-op.
func (s *Session) Push(ev SessionEvent) {
	if s.runCtx == nil {
		return
	}
	select {
	case <-s.runCtx.Done():
		return
	default:
	}
	select {
	case s.events <- ev:
		return
	default:
	}
	select {
	case <-s.runCtx.Done():
		return
	default:
	}
	n := s.droppedEvents.Add(1)
	s.logger.Warn("session: dropping event, buffer full",
		infra.F("component", "session"),
		infra.F("session_id", s.id),
		infra.F("dropped", n),
		infra.F("event", ev.Type.String()),
	)
}

// DroppedCount reports how many events Push has discarded because the
// buffer was full.
func (s *Session) DroppedCount() int64 {
	return s.droppedEvents.Load()
}

// skipTimedOutWord advances past the current dictating word on the event
// loop itself. A successful next_word transition resets the deadline in
// onTransition. A rejected transition leaves the deadline in the past, so
// move it forward here; otherwise time.Until of a past deadline is <= 0
// and the timer fires again on the next iteration.
func (s *Session) skipTimedOutWord() {
	s.handleEvent(SessionEvent{Type: EventControlFrame, ControlType: "next_word"})
	if s.Deadline().After(time.Now()) {
		return
	}
	timeout := s.timeoutFor(s.State())
	s.mu.Lock()
	s.deadline = time.Now().Add(timeout)
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Event processing
// ---------------------------------------------------------------------------

// handleEvent processes a single non-exit event through the FSM. Invalid
// transitions are logged and dropped (the session continues — mirrors the
// "skip malformed, don't kick" philosophy at connection.go:186-189).
func (s *Session) handleEvent(ev SessionEvent) {
	if err := s.transition(ev); err != nil {
		if errors.Is(err, ErrInvalidTransition) {
			s.logger.Warn("session: dropping invalid transition event",
				infra.F("component", "session"),
				infra.F("session_id", s.id),
				infra.F("event", ev.Type.String()),
				infra.F("control_type", ev.ControlType),
				infra.ErrField(err),
			)
			return
		}
		s.logger.Error("session: transition error",
			infra.F("component", "session"),
			infra.F("session_id", s.id),
			infra.ErrField(err),
		)
		return
	}
	// 2026-09-14 UAT fix: word_complete is EVENT-owned, not state-entry-owned.
	// The old onTransition(DICTATING) send fired on resume/repeat BEFORE the
	// re-stream produced any audio (the frontend dropped it on an empty
	// rolling buffer), and the re-stream's own completion was then an invalid
	// DICTATING transition — replayed audio was orphaned client-side and the
	// UI stayed dead until the DICTATING timeout force-advanced. Sending on
	// the EVENT covers both the normal LISTENING→DICTATING completion and the
	// DICTATING/PAUSED replay self-loops, always after frames have streamed.
	if ev.Type == EventWordComplete {
		wordID := ev.WordID
		if wordID == "" {
			s.mu.RLock()
			wordID = s.currentWordID
			s.mu.RUnlock()
		}
		if wordID != "" {
			s.sendWordCompleteFrame(wordID)
		}
	}
	s.onTransition()
}

// onTransition is called after a successful FSM transition. It resets the
// per-state deadline (D-08), fires the state-change audit (D-06), and spawns
// the D-13 StreamWord side channel if the new state is LISTENING.
// Additionally sends progress frames for real-time feedback (08-06).
// Ticket 02: handles repeat (re-spawn StreamWord) and PAUSED state.
func (s *Session) onTransition() {
	s.mu.Lock()
	newState := s.state
	// Compute deadline without holding the lock for the full duration.
	// timeoutFor may call CurrentWord() which needs RLock, so compute
	// the timeout separately.
	s.mu.Unlock()

	timeout := s.timeoutFor(newState)

	s.mu.Lock()
	s.deadline = time.Now().Add(timeout)
	s.mu.Unlock()

	s.auditState(newState)

	// 08-06: Emit progress_update when entering LISTENING (starting word streaming)
	if newState == database.StateListening {
		s.sendProgressFrame() // Notify client that word streaming is starting
	}

	// 08-06: progress display on entering DICTATING. word_complete itself is
	// EVENT-owned now (handleEvent sends it when an EventWordComplete
	// transition succeeds — see the note there); sending it here fired on
	// resume/repeat re-streams before any audio existed.
	if newState == database.StateDictating {
		s.mu.RLock()
		wordID := s.currentWordID
		s.mu.RUnlock()
		if wordID != "" {
			s.sendProgressFrame() // Update progress display
		}
	}

	// 08-06: Emit session_complete when entering FEEDBACK (all words completed)
	if newState == database.StateFeedback {
		s.sendSessionCompleteFrame() // Notify client that session completed
	}

	// Ticket 05: WAITING_PARENT entry now emits a progress frame too — it
	// carries the status field, which is how the batch-submission UI (and the
	// per-word mode's auto-ratings submitter) learn the session reached the
	// all-words-dictated point. SUBMISSION entry does the same so the client
	// can show the grading-in-progress indicator.
	if newState == database.StateWaitingParent || newState == database.StateSubmission {
		s.sendProgressFrame()
	}

	// Ticket 02 DICT-07: Notify client when entering PAUSED state.
	if newState == database.StatePaused {
		s.sendPausedFrame()
	}

	// D-13: on entering LISTENING, the Session owns the StreamWord call. On its
	// return, a goroutine pushes WordComplete onto the events channel, driving
	// the LISTENING→DICTATING system transition.
	if newState == database.StateListening {
		s.spawnStreamWord()
	}

	// Ticket 02 DICT-07: on repeat (DICTATING or PAUSED), re-spawn StreamWord
	// for the same word. The FSM does NOT change state on repeat, so
	// onTransition fires with the same state — we detect repeat by checking
	// if the current state is DICTATING or PAUSED and re-play the word.
	// This is triggered by the repeat event which passes through the FSM
	// without state change, so we need a different detection mechanism.
	// The repeat event sets a flag that onTransition reads and clears.
	//
	// Lock-free read/clear of repeatPending — DO NOT add a lock here
	// (D-02 single-writer):
	//   (a) Invariant: this function and transition() run sequentially on the
	//   same event-loop goroutine (run() → handleEvent() calls transition()
	//   first, then onTransition() on success), so transition()'s writes
	//   happen-before these reads by program order, and no other goroutine
	//   touches repeatPending — the unlocked read and the clear-write below
	//   are race-free. mu guards these writes only incidentally, as part of
	//   transition()'s critical section; it is NOT the source of read-side
	//   safety.
	//   (b) Never lock this block: spawnStreamWord(), called right below,
	//   acquires s.mu itself — wrapping this block in s.mu would self-deadlock
	//   (sync.RWMutex is not reentrant).
	//   (c) Regression warning: this function once deadlocked holding the
	//   write lock across timeoutFor → CurrentWord() (which takes RLock); the
	//   established fix pattern is the release → compute → re-lock sequence at
	//   the top of this function — never reintroduce nested acquisition of
	//   s.mu.
	if s.repeatPending {
		s.repeatPending = false
		if newState == database.StateDictating || newState == database.StatePaused {
			s.spawnStreamWord()
		}
	}

	// Ticket 02 DICT-07: on resume from PAUSED, re-spawn StreamWord for the
	// current word (the word audio replays from the beginning).
	// prevState: same single-goroutine invariant as repeatPending above —
	// unlocked read, no lock needed or allowed.
	if newState == database.StateDictating && s.prevState == database.StatePaused {
		s.spawnStreamWord()
	}
}

// SpeakFeedback generates and streams the spoken encouragement sentence
// (Ticket 06) — "final scores and encouraging feedback read aloud via TTS".
// Best-effort: generation or streaming failures log and drop, because the
// visual report has already been delivered by the time this runs.
//
// It runs on its own goroutine so the pybridge subprocess cannot block the
// caller (the WS handler that graded the answers), and is bounded by the
// session's own lifetime: the ctx derives from runCtx (cancelled on session
// exit — disconnect, sweep, or a new session on the same connection), plus a
// 30s cap so a stuck subprocess cannot outlive the FEEDBACK window.
//
// runCtx is read without s.mu here: it is written exactly once in Start(),
// BEFORE Manager.NewSession inserts the session into the registry map, and
// callers reach this method through Manager.Lookup (RLock) — that lock pair
// establishes the happens-before edge (D-02 discipline).
func (s *Session) SpeakFeedback(text, language string) {
	if s.gen == nil || s.streamer == nil {
		return
	}

	runCtx := s.runCtx
	if runCtx == nil {
		runCtx = context.Background()
	}

	go func() {
		ctx, cancel := context.WithTimeout(runCtx, 30*time.Second)
		defer cancel()

		if _, err := s.gen.GenerateText(ctx, s.id, text, language); err != nil {
			s.logger.Warn("session: feedback TTS generation failed",
				infra.F("component", "session"),
				infra.F("session_id", s.id),
				infra.F("language", language),
				infra.ErrField(err),
			)
			return
		}
		if err := s.streamer.StreamWord(ctx, s.client, s.id, text, 0); err != nil {
			s.logger.Warn("session: feedback TTS stream failed",
				infra.F("component", "session"),
				infra.F("session_id", s.id),
				infra.ErrField(err),
			)
		}
	}()
}

// RecordGradedResult persists a batch_graded event (JSON payload — per-word
// results + counts, see cmd/service/analysis.go) against the session's audit
// row so trends and parent review can query it later (Ticket 06).
//
// Deliberate deviation from the auditEvent pattern (D-06/Pitfall 5): the
// audit wrappers route through the WorkerPool because the EVENT LOOP must
// never block on SQLite. This method is called from the WS handler goroutine
// (a router worker), not the event loop, and is a synchronous single insert —
// the same weight class as the FSRS UpdateRatings subprocess call the grading
// handler already makes. Synchronous on purpose: the write lands before the
// connection answers, so the row exists by the time any later history query
// runs. Best-effort contract: the caller logs failures and continues, the
// student's grading result is already delivered.
func (s *Session) RecordGradedResult(ctx context.Context, payload string) error {
	if s.repo == nil {
		return nil // audit-less mode (tests) — nothing to write to
	}
	if s.dbID == 0 {
		return errors.New("session: no audit row to record graded result against")
	}
	return s.repo.AppendEvent(ctx, database.SessionEvent{
		SessionID: s.dbID,
		EventType: "batch_graded",
		Payload:   payload,
	})
}

// spawnStreamWord kicks off the D-13 server-side side channel: StreamWord runs
// in a goroutine, and on its return pushes WordComplete onto the events channel.
// An in-flight stream (from a replay request) is cancelled first.
func (s *Session) spawnStreamWord() {
	if s.streamer == nil {
		return
	}
	// Cancel any in-flight stream (e.g. a replay request replaced the current word).
	if s.streamCancel != nil {
		s.streamCancel()
	}

	// Get current word for progress tracking (08-06)
	var wordID string
	if s.wordIndex >= 0 && s.wordIndex < len(s.wordQueue) {
		wordID = s.wordQueue[s.wordIndex].Word
		s.mu.Lock()
		s.currentWordID = wordID // Track current word for completion frames (08-06)
		s.mu.Unlock()
	}

	streamCtx, cancel := context.WithCancel(s.runCtx)
	s.streamCancel = cancel

	// 260823-sdx: stamp this spawn with a fresh stream generation. A superseded
	// stream (cancelled here or by a later re-spawn) may still complete and push
	// its WordComplete; the event loop drops events whose generation no longer
	// matches s.streamGen. This field is only touched on the event-loop
	// goroutine; the goroutine below only uses the by-value copy `gen`.
	s.streamGen++
	gen := s.streamGen

	go func() {
		defer cancel()
		err := s.streamer.StreamWord(streamCtx, s.client, s.id, wordID, 0)
		if err != nil {
			s.logger.Warn("session: stream word completed with error",
				infra.F("component", "session"),
				infra.F("session_id", s.id),
				infra.F("word_id", wordID),
				infra.ErrField(err),
			)
		}
		// Push WordComplete regardless of error — the event loop decides the
		// next state. A stream error does NOT terminate the session.
		select {
		case s.events <- SessionEvent{Type: EventWordComplete, WordID: wordID, StreamGen: gen}:
		case <-s.runCtx.Done():
		}
	}()
}

// ---------------------------------------------------------------------------
// D-10 exit defer chain methods
// ---------------------------------------------------------------------------

// releaseFSRSLock releases the FSRS fetch-lock acquired at NewSession (D-10).
//
// Pitfall 1: the run-loop ctx is already cancelled by the time this defer fires
// (disconnect/timeout/shutdown all cancel it). Using it would make pybridge
// return context.Canceled immediately and the lock would never release. So we
// construct a FRESH context.
//
// On UnlockCards error: log at Error but do NOT panic — the fetch-lock has a
// 24h TTL (FetchLockHours) so it self-expires.
func (s *Session) releaseFSRSLock() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Scoped release (Ticket 07): only the cards this session locked. A
	// category-wide sweep would also drop OTHER active sessions' locks in
	// the same category — two students sharing a category is the supported
	// multi-user posture, and clobbering a peer's lock could hand its words
	// to a third session mid-dictation. With no captured IDs there is
	// nothing this session locked — skip entirely rather than fall back to
	// a sweep (empty IDs currently can't happen — ErrNoDueWords guards —
	// but a future path must not silently re-enable the clobber).
	if len(s.lockCardIDs) == 0 {
		s.logger.Warn("session: no card IDs captured, skipping FSRS unlock",
			infra.F("component", "session"),
			infra.F("session_id", s.id),
		)
		return
	}
	n, err := s.fsrsSvc.UnlockCards(ctx, fsrs.UnlockOpts{
		Category: s.lockCategory,
		Unit:     s.lockUnit,
		IDs:      s.lockCardIDs,
	})
	if err != nil {
		s.logger.Error("session: failed to release FSRS lock (will self-expire via 24h TTL)",
			infra.F("component", "session"),
			infra.F("session_id", s.id),
			infra.F("category", s.lockCategory),
			infra.F("unit", s.lockUnit),
			infra.F("card_ids", len(s.lockCardIDs)),
			infra.ErrField(err),
		)
		return
	}
	s.logger.Debug("session: released FSRS lock",
		infra.F("component", "session"),
		infra.F("session_id", s.id),
		infra.F("category", s.lockCategory),
		infra.F("unlocked", n),
	)
}

// cleanupTTS removes the session's TTS cache directory (D-10). Idempotent —
// safe to call from a defer even if the directory was already removed.
func (s *Session) cleanupTTS() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.gen.CleanupSession(ctx, s.id); err != nil {
		s.logger.Error("session: failed to cleanup TTS cache",
			infra.F("component", "session"),
			infra.F("session_id", s.id),
			infra.ErrField(err),
		)
		return
	}
	s.logger.Debug("session: cleaned up TTS cache",
		infra.F("component", "session"),
		infra.F("session_id", s.id),
	)
}

// unregisterFromHub removes the client from the WS hub (D-10). Non-blocking
// and idempotent per CR-01. Nil-safe for tests that pass nil hub/client.
func (s *Session) unregisterFromHub() {
	if s.hub == nil || s.client == nil {
		return
	}
	s.hub.Unregister(s.client)
	s.logger.Debug("session: unregistered from hub",
		infra.F("component", "session"),
		infra.F("session_id", s.id),
	)
}

// ---------------------------------------------------------------------------
// Exported read-only accessors (for tests, the sweeper, and Plan 03 handlers)
// ---------------------------------------------------------------------------

// ID returns the session UUID (used for the session_ready response frame and
// TTS cache scoping).
func (s *Session) ID() string { return s.id }

// State returns the current FSM state (read by the sweeper and tests). RLock-
// protected so it is race-free against the event loop's WLock in transition().
func (s *Session) State() database.SessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Deadline returns the per-state deadline (read by the sweeper). RLock-protected
// so it is race-free against onTransition's WLock.
func (s *Session) Deadline() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deadline
}

// CurrentWord returns the word string currently being dictated.
// Returns "" if no word is active. Thread-safe (takes RLock).
func (s *Session) CurrentWord() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.wordIndex >= 0 && s.wordIndex < len(s.wordQueue) {
		return s.wordQueue[s.wordIndex].Word
	}
	return ""
}

// Category returns the session's FSRS category.
func (s *Session) Category() string {
	return s.lockCategory
}

// AudioSource returns the TTS backend that generated this session's word
// audio (DICT-03): tts.SourceMimo when the Mimo per-word path produced it,
// tts.SourceVolcengine for the pregenerate path (including fallbacks). Set
// once at NewSession.
func (s *Session) AudioSource() tts.AudioSource { return s.audioSource }

// Done returns a channel that is closed when run() exits. Tests and the sweeper
// use it to detect session liveness without polling.
func (s *Session) Done() <-chan struct{} { return s.done }

// WordQueueLen returns the number of words in the queue.
func (s *Session) WordQueueLen() int { return len(s.wordQueue) }

// Words returns the word strings from the session's word queue. Used by
// the composition-root handler to populate the session_ready response.
func (s *Session) Words() []string {
	words := make([]string, len(s.wordQueue))
	for i, card := range s.wordQueue {
		words[i] = card.Word
	}
	return words
}

// onLastWord reports whether the dictation cursor sits on the queue's final
// word — the edge condition the FSM applies to next_word (→ WAITING_PARENT)
// and submit_answers. Callers must hold s.mu (transition does).
func (s *Session) onLastWord() bool {
	return s.wordIndex >= len(s.wordQueue)-1
}

// DictationFinished reports whether the dictation has reached its final word
// — the same edge condition the FSM applies to submit_answers, so
// handler-side guards mirror the FSM exactly instead of re-encoding it.
// Thread-safe (takes RLock).
func (s *Session) DictationFinished() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.onLastWord()
}

// ---------------------------------------------------------------------------
// Progress frame emission (08-06: Backend Progress Frame Emission)
// ---------------------------------------------------------------------------

// sendProgressFrame sends a progress_update control frame to the client (08-06).
func (s *Session) sendProgressFrame() {
	if s.client == nil {
		return // No client in tests
	}

	s.mu.RLock()
	currentWord := s.wordIndex + 1 // 1-indexed for display
	completedWords := s.wordIndex  // Words fully completed
	totalWords := len(s.wordQueue)
	sessionID := s.id
	status := s.state // Ticket 05: FSM status echo (see _applyProgressUpdate)
	s.mu.RUnlock()

	// Build progress frame JSON. Field names use camelCase to match the
	// frontend's _applyProgressUpdate parser (websocket-client.js).
	progressMsg := map[string]interface{}{
		"type":           "progress_update",
		"session_id":     sessionID,
		"currentWord":    currentWord,
		"completedWords": completedWords,
		"totalWords":     totalWords,
		"status":         string(status),
	}

	jsonBytes, err := json.Marshal(progressMsg)
	if err != nil {
		s.logger.Warn("session: failed to marshal progress frame",
			infra.F("session_id", s.id),
			infra.ErrField(err),
		)
		return
	}

	// Send as control frame
	header := &ws.FrameHeader{
		Version:    ws.ProtocolVersion,
		MsgType:    ws.MsgTypeControl,
		Sequence:   0, // TODO: atomic counter for monotonic sequences
		PayloadLen: uint16(len(jsonBytes)),
	}
	frame := ws.EncodeFrame(header, jsonBytes)

	if err := s.client.Send(frame); err != nil {
		s.logger.Warn("session: failed to send progress frame",
			infra.F("session_id", s.id),
			infra.ErrField(err),
		)
	}
}

// WordCompleteAudioPlays returns the audio_plays hint carried by the
// word_complete control frame (DICT-03). Mimo word audio is a SINGLE bare
// pronunciation, so the frontend schedules the dictation-standard double
// play (2); the Volcengine pregenerate tracks are already paired
// "word. word.", so the frontend plays once (1). Unknown/zero sources map
// to 1 — the conservative choice: never double-play an already-paired track.
func WordCompleteAudioPlays(source tts.AudioSource) int {
	if source == tts.SourceMimo {
		return 2
	}
	return 1
}

// sendWordCompleteFrame sends a word_complete control frame to the client (08-06).
// The payload carries the additive audio_plays field (DICT-03) — clients
// that ignore it behave exactly as before (volcengine behaviour).
func (s *Session) sendWordCompleteFrame(wordID string) {
	if s.client == nil {
		return // No client in tests
	}

	// Build word_complete frame JSON
	wordCompleteMsg := map[string]interface{}{
		"type":        "word_complete",
		"session_id":  s.id,
		"word_id":     wordID,
		"audio_plays": WordCompleteAudioPlays(s.audioSource),
	}

	jsonBytes, err := json.Marshal(wordCompleteMsg)
	if err != nil {
		s.logger.Warn("session: failed to marshal word_complete frame",
			infra.F("session_id", s.id),
			infra.F("word_id", wordID),
			infra.ErrField(err),
		)
		return
	}

	// Send as control frame
	header := &ws.FrameHeader{
		Version:    ws.ProtocolVersion,
		MsgType:    ws.MsgTypeControl,
		Sequence:   0, // TODO: atomic counter for monotonic sequences
		PayloadLen: uint16(len(jsonBytes)),
	}
	frame := ws.EncodeFrame(header, jsonBytes)

	if err := s.client.Send(frame); err != nil {
		s.logger.Warn("session: failed to send word_complete frame",
			infra.F("session_id", s.id),
			infra.F("word_id", wordID),
			infra.ErrField(err),
		)
	}
}

// sendSessionCompleteFrame sends a session_complete control frame to the client (08-06).
func (s *Session) sendSessionCompleteFrame() {
	if s.client == nil {
		return // No client in tests
	}

	durationMs := time.Since(s.sessionStartTime).Milliseconds()

	// Build session_complete frame JSON. Field names use camelCase to match
	// the frontend's _applySessionComplete parser (websocket-client.js).
	sessionCompleteMsg := map[string]interface{}{
		"type":       "session_complete",
		"session_id": s.id,
		"totalWords": len(s.wordQueue),
		"durationMs": durationMs,
	}

	jsonBytes, err := json.Marshal(sessionCompleteMsg)
	if err != nil {
		s.logger.Warn("session: failed to marshal session_complete frame",
			infra.F("session_id", s.id),
			infra.ErrField(err),
		)
		return
	}

	// Send as control frame
	header := &ws.FrameHeader{
		Version:    ws.ProtocolVersion,
		MsgType:    ws.MsgTypeControl,
		Sequence:   0, // TODO: atomic counter for monotonic sequences
		PayloadLen: uint16(len(jsonBytes)),
	}
	frame := ws.EncodeFrame(header, jsonBytes)

	if err := s.client.Send(frame); err != nil {
		s.logger.Warn("session: failed to send session_complete frame",
			infra.F("session_id", s.id),
			infra.ErrField(err),
		)
	}
}

// sendPausedFrame sends a paused control frame to the client (Ticket 02 DICT-07).
// Like the other send*Frame helpers, the frame header is sent with Sequence 0;
// TODO: atomic counter for monotonic sequences.
func (s *Session) sendPausedFrame() {
	if s.client == nil {
		return // No client in tests
	}

	s.mu.RLock()
	sessionID := s.id
	s.mu.RUnlock()

	pausedMsg := map[string]interface{}{
		"type":       "paused",
		"session_id": sessionID,
	}

	jsonBytes, err := json.Marshal(pausedMsg)
	if err != nil {
		s.logger.Warn("session: failed to marshal paused frame",
			infra.F("session_id", s.id),
			infra.ErrField(err),
		)
		return
	}

	header := &ws.FrameHeader{
		Version:    ws.ProtocolVersion,
		MsgType:    ws.MsgTypeControl,
		Sequence:   0, // TODO: atomic counter for monotonic sequences
		PayloadLen: uint16(len(jsonBytes)),
	}
	frame := ws.EncodeFrame(header, jsonBytes)

	if err := s.client.Send(frame); err != nil {
		s.logger.Warn("session: failed to send paused frame",
			infra.F("session_id", s.id),
			infra.ErrField(err),
		)
	}
}

// LockCategory returns the resolved FSRS lock category captured at NewSession
// (Pitfall 4 verification).
func (s *Session) LockCategory() string { return s.lockCategory }

// LockUnit returns the resolved FSRS lock unit captured at NewSession.
func (s *Session) LockUnit() string { return s.lockUnit }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// dictatingGrace is the safety margin added to the adaptive DICTATING
// timeout. 2026-09-14 UAT race fix: the frontend paces next_word with the
// SAME AdaptiveTimeout formula (frontend batch gap starts at word_complete
// receipt; the backend deadline starts at emission). With identical
// durations the backend won by milliseconds, self-advanced the session, and
// streamed word N+1 while the client still routed audio under word N —
// every word after the first played silent. With the margin the client's
// pacing timer is authoritative; the backend timeout remains the
// dead-client backstop, just later.
const dictatingGrace = 10 * time.Second

// timeoutFor returns the per-state timeout from cfg.SessionTimeout (D-08, D-09).
// For DICTATING state, returns the adaptive timeout based on the current word's
// length (Ticket 02 DICT-06: 8-15 seconds). For PAUSED state, returns the
// paused-state timeout configured on cfg.SessionTimeout (default 5m) since the
// student controls when to resume.
// Called by the event loop (onTransition) to reset the deadline on each
// transition, and by the sweeper (cleanupLeaked) to compute the 2× threshold.
func (s *Session) timeoutFor(state database.SessionState) time.Duration {
	switch state {
	case database.StateIdle:
		return s.cfg.SessionTimeout.IdleStateTimeout
	case database.StateListening:
		return s.cfg.SessionTimeout.ListeningStateTimeout
	case database.StateDictating:
		// Ticket 02 DICT-06: use adaptive timeout based on current word length
		// (plus dictatingGrace), but respect the config timeout as an upper
		// bound. This ensures tests with short DictatingStateTimeout still work
		// while production gets the full adaptive window plus margin.
		word := s.CurrentWord()
		if word != "" {
			adaptive := AdaptiveTimeout(word) + dictatingGrace
			if adaptive < s.cfg.SessionTimeout.DictatingStateTimeout {
				return adaptive
			}
			return s.cfg.SessionTimeout.DictatingStateTimeout
		}
		return s.cfg.SessionTimeout.DictatingStateTimeout
	case database.StatePaused:
		// Ticket 02 DICT-07: no dictation timeout while paused.
		return s.cfg.SessionTimeout.PausedStateTimeout
	case database.StateWaitingParent:
		return s.cfg.SessionTimeout.WaitingParentStateTimeout
	case database.StateSubmission:
		// Ticket 05: SUBMISSION is a transient pass-through (grading is a
		// synchronous comparison pass), so the waiting-parent budget amply
		// covers it — a dedicated config knob would be knob-per-state noise.
		return s.cfg.SessionTimeout.WaitingParentStateTimeout
	case database.StateFeedback:
		return s.cfg.SessionTimeout.FeedbackStateTimeout
	default:
		return s.cfg.SessionTimeout.IdleStateTimeout
	}
}

// newSessionID generates a random hex UUID v4 string for TTS/FSRS scoping.
// Mirrors the crypto/rand pattern at connection.go:501. On the (impossible)
// rand.Read failure, falls back to a timestamp-based ID so the caller never
// receives an empty string.
func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b)
}
