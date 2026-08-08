package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"runtime/debug"
	"sync"
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
// narrow seam so tests can inject a fake streamer (mirrors FrameSender, D-05).
type SessionStreamer interface {
	// StreamWord delivers audio for a single word to the given client. The
	// Session owns this call per D-13 option B (server-side side channel).
	StreamWord(ctx context.Context, client tts.Sender, sessionID string, wordID string, startSeq uint32) error
}

// SessionHub is the subset of *ws.Hub the Session uses for lifecycle cleanup.
// *ws.Hub satisfies it structurally. Defined as a narrow seam so tests can
// inject a fake hub (mirrors FrameSender, D-05).
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
// needed).
type Session struct {
	// --- Identity ---
	id     string // TTS/FSRS session UUID (crypto/rand hex v4)
	dbID   int64  // audit row id (best-effort, D-06; may lag behind)
	connID string

	// --- FSM state (protected by mu for cross-goroutine reads) ---
	mu        sync.RWMutex
	state     database.SessionState
	wordQueue []pybridge.DueCard
	wordIndex int
	deadline  time.Time

	// --- FSRS lock scope (captured at NewSession, replayed at exit — Pitfall 4) ---
	lockCategory string
	lockUnit     string

	// --- Service dependencies ---
	client   *ws.Client // the FrameSender target + hub identity (D-05)
	fsrsSvc  fsrs.FSRSService
	gen      TTSSessionGenerator // *tts.Generator satisfies structurally
	streamer SessionStreamer     // *tts.Streamer satisfies structurally
	repo     database.SessionRepository
	pool     *infra.WorkerPool
	hub      SessionHub // *ws.Hub satisfies structurally
	logger   infra.Logger
	cfg      infra.Config

	// --- Event loop ---
	events       chan SessionEvent
	runCtx       context.Context
	cancel       context.CancelFunc
	done         chan struct{}      // closed when run() exits (liveness signal for tests/sweeper)
	streamCancel context.CancelFunc // cancels in-flight StreamWord (set by spawnStreamWord in the loop goroutine)
	onExit       func()             // set by Manager.NewSession for map cleanup
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

	// --- Step 3: TTS UUID + GenerateForSession (Pitfall 3: before the loop). ---
	sessionID := newSessionID()
	words := make([]string, len(cards))
	for i, c := range cards {
		words[i] = c.Word
	}
	if _, err := gen.GenerateForSession(ctx, sessionID, words); err != nil {
		// --- Step 4: TTS failure — non-defer UnlockCards (D-10 init path). ---
		// The defer chain is NOT installed yet (run() hasn't started), so the
		// unlock must happen explicitly here. Using the caller's ctx is correct
		// because it is still live at init time (unlike the deferred unlock in
		// run(), which needs a fresh ctx — Pitfall 1).
		if _, unlockErr := fsrsSvc.UnlockCards(ctx, fsrs.UnlockOpts{
			Category: resolvedCategory,
			Unit:     resolvedUnit,
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
		id:           sessionID,
		connID:       connID,
		state:        database.StateIdle,
		wordQueue:    cards,
		wordIndex:    0,
		deadline:     time.Now().Add(cfg.SessionTimeout.IdleStateTimeout),
		lockCategory: resolvedCategory,
		lockUnit:     resolvedUnit,
		client:       client,
		fsrsSvc:      fsrsSvc,
		gen:          gen,
		streamer:     streamer,
		repo:         repo,
		pool:         pool,
		hub:          hub,
		logger:       logger,
		cfg:          cfg,
		events:       make(chan SessionEvent, eventBufferSize),
	}

	// --- Step 6: Audit Create (fire-and-forget via pool.Submit, D-06). ---
	s.auditCreate()

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
			s.handleEvent(ev)

		// Per-state deadline timer (D-08). Go 1.23+ GC-safe per Pitfall 2.
		// time.After is re-evaluated on every loop iteration so an onTransition
		// deadline update is reflected immediately.
		case <-time.After(time.Until(s.Deadline())):
			return

		case <-ctx.Done():
			return
		}
	}
}

// Push sends an event onto the session's events channel. Non-blocking w/ a
// ctx.Done guard so a caller cannot block forever if the session has exited.
func (s *Session) Push(ev SessionEvent) {
	select {
	case s.events <- ev:
	case <-s.runCtx.Done():
	}
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
	s.onTransition()
}

// onTransition is called after a successful FSM transition. It resets the
// per-state deadline (D-08), fires the state-change audit (D-06), and spawns
// the D-13 StreamWord side channel if the new state is LISTENING.
func (s *Session) onTransition() {
	s.mu.Lock()
	s.deadline = time.Now().Add(s.timeoutFor(s.state))
	newState := s.state
	s.mu.Unlock()

	s.auditState(newState)

	// D-13: on entering LISTENING, the Session owns the StreamWord call. On its
	// return, a goroutine pushes WordComplete onto the events channel, driving
	// the LISTENING→DICTATING system transition.
	if newState == database.StateListening {
		s.spawnStreamWord()
	}
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

	wordID := s.wordQueue[s.wordIndex].Word
	streamCtx, cancel := context.WithCancel(s.runCtx)
	s.streamCancel = cancel

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
		case s.events <- SessionEvent{Type: EventWordComplete, WordID: wordID}:
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

	n, err := s.fsrsSvc.UnlockCards(ctx, fsrs.UnlockOpts{
		Category: s.lockCategory,
		Unit:     s.lockUnit,
	})
	if err != nil {
		s.logger.Error("session: failed to release FSRS lock (will self-expire via 24h TTL)",
			infra.F("component", "session"),
			infra.F("session_id", s.id),
			infra.F("category", s.lockCategory),
			infra.F("unit", s.lockUnit),
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

// LockCategory returns the resolved FSRS lock category captured at NewSession
// (Pitfall 4 verification).
func (s *Session) LockCategory() string { return s.lockCategory }

// LockUnit returns the resolved FSRS lock unit captured at NewSession.
func (s *Session) LockUnit() string { return s.lockUnit }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// timeoutFor returns the per-state timeout from cfg.SessionTimeout (D-08, D-09).
// Called by the event loop (onTransition) to reset the deadline on each
// transition, and by the sweeper (cleanupLeaked) to compute the 2× threshold.
// Only reads cfg (immutable) — no lock needed.
func (s *Session) timeoutFor(state database.SessionState) time.Duration {
	switch state {
	case database.StateIdle:
		return s.cfg.SessionTimeout.IdleStateTimeout
	case database.StateListening:
		return s.cfg.SessionTimeout.ListeningStateTimeout
	case database.StateDictating:
		return s.cfg.SessionTimeout.DictatingStateTimeout
	case database.StateWaitingParent:
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
