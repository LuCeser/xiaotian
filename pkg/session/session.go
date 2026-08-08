package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

// Session is the in-memory authority for one dictation session's lifecycle
// (D-02, D-06). It binds a WS client to an FSRS lock + TTS cache + FSM state.
//
// Concurrency contract (D-02): all mutable fields (state, wordQueue, wordIndex,
// deadline) are written ONLY by the event-loop goroutine (run, implemented in
// Plan 02). The single-writer discipline means NO mutex is needed on these
// fields. External readers (the sweeper, tests) use the exported accessors.
type Session struct {
	// --- Identity ---
	id     string // TTS/FSRS session UUID (crypto/rand hex v4)
	dbID   int64  // audit row id (best-effort, D-06; may lag behind)
	connID string

	// --- FSM state (single-writer: the event loop goroutine, D-02) ---
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
	streamer *tts.Streamer
	repo     database.SessionRepository
	pool     *infra.WorkerPool
	hub      *ws.Hub
	logger   infra.Logger
	cfg      infra.Config

	// --- Event loop (Plan 02 fills run/cancel; declared here for struct stability) ---
	events chan SessionEvent
	runCtx context.Context
	cancel context.CancelFunc
	onExit func() // set by Manager.NewSession for map cleanup (Plan 02)
}

// NewSession constructs a Session by acquiring the FSRS fetch-lock, generating
// TTS audio, and recording the audit row. It does NOT spawn the event-loop
// goroutine — the caller (Manager.NewSession in Plan 02, or the handler in
// Plan 03) calls s.Start(connCtx) to begin the loop.
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
	streamer *tts.Streamer,
	repo database.SessionRepository,
	pool *infra.WorkerPool,
	hub *ws.Hub,
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
	// resolved values are replayed verbatim at exit (releaseFSRSLock, Plan 02).
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
		// Plan 02, which needs a fresh ctx — Pitfall 1).
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

	// Do NOT spawn go s.run(ctx) — that is Plan 02's responsibility.
	return s, nil
}

// ---------------------------------------------------------------------------
// Exported read-only accessors (for tests, the sweeper, and Plan 03 handlers)
// ---------------------------------------------------------------------------

// ID returns the session UUID (used for the session_ready response frame and
// TTS cache scoping).
func (s *Session) ID() string { return s.id }

// State returns the current FSM state (read by the sweeper and tests).
func (s *Session) State() database.SessionState { return s.state }

// WordQueueLen returns the number of words in the queue.
func (s *Session) WordQueueLen() int { return len(s.wordQueue) }

// LockCategory returns the resolved FSRS lock category captured at NewSession
// (Pitfall 4 verification).
func (s *Session) LockCategory() string { return s.lockCategory }

// LockUnit returns the resolved FSRS lock unit captured at NewSession.
func (s *Session) LockUnit() string { return s.lockUnit }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// timeoutFor returns the per-state timeout from cfg.SessionTimeout (D-08, D-09).
// Called by the event loop (Plan 02) to reset the deadline on each transition.
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
