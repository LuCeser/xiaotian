package session_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/tts"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// testCfg returns a Config with sane defaults for session init tests. The
// SessionTimeout + FSRS defaults come from infra.DefaultConfig().
func testCfg() infra.Config {
	return infra.DefaultConfig()
}

func threeCards() []pybridge.DueCard {
	return []pybridge.DueCard{
		{ID: 1, Word: "apple"},
		{ID: 2, Word: "banana"},
		{ID: 3, Word: "cherry"},
	}
}

// TestNewSession_SelectDueCards: NewSession calls SelectDueCards, holds the
// word queue, resolves lockCategory/lockUnit, and does NOT unlock on success.
func TestNewSession_SelectDueCards(t *testing.T) {
	fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
	gen := &fakeTTSGenerator{}

	s, err := session.NewSession(
		context.Background(),
		nil,                       // client — not exercised by this test
		"conn-1",                  // connID
		fsrsSvc,                   // fsrsSvc
		gen,                       // gen (TTSSessionGenerator)
		nil,                       // streamer — not exercised
		nil,                       // repo — not exercised (pool is nil → auditCreate no-ops)
		nil,                       // pool — nil → auditCreate no-ops
		nil,                       // hub — not exercised
		nil,                       // logger → NewNoOpLogger substituted
		testCfg(),                 // cfg
		fsrs.SelectOpts{Limit: 3}, // selectOpts
	)
	if err != nil {
		t.Fatalf("NewSession() returned unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("NewSession() returned nil session")
	}

	// Word queue holds all 3 cards.
	if got := s.WordQueueLen(); got != 3 {
		t.Errorf("WordQueueLen() = %d, want 3", got)
	}

	// State is IDLE.
	if got := s.State(); got != database.StateIdle {
		t.Errorf("State() = %s, want %s", got, database.StateIdle)
	}

	// Lock category resolved from empty SelectOpts → cfg.FSRS.DefaultCategory.
	if got, want := s.LockCategory(), testCfg().FSRS.DefaultCategory; got != want {
		t.Errorf("LockCategory() = %q, want %q", got, want)
	}

	// No premature unlock on success.
	if count := fsrsSvc.UnlockCount(); count != 0 {
		t.Errorf("UnlockCount() = %d, want 0 (no unlock on success)", count)
	}

	// TTS generation was called exactly once.
	if count := gen.GenCalls(); count != 1 {
		t.Errorf("GenCalls() = %d, want 1", count)
	}

	// Session has a non-empty ID (TTS/FSRS UUID).
	if s.ID() == "" {
		t.Error("ID() is empty, want a non-empty session UUID")
	}
}

// TestNewSession_EmptyDue: empty SelectDueCards result → ErrNoDueWords (D-02-a).
func TestNewSession_EmptyDue(t *testing.T) {
	fsrsSvc := &fakeFSRSService{dueCards: nil} // empty slice
	gen := &fakeTTSGenerator{}

	s, err := session.NewSession(
		context.Background(),
		nil, "conn-2", fsrsSvc, gen, nil, nil, nil, nil, nil, testCfg(),
		fsrs.SelectOpts{},
	)
	if s != nil {
		t.Errorf("NewSession() returned non-nil session, want nil")
	}
	if !errors.Is(err, session.ErrNoDueWords) {
		t.Errorf("NewSession() error = %v, want ErrNoDueWords", err)
	}

	// TTS generation must NOT be called (we bail before it).
	if count := gen.GenCalls(); count != 0 {
		t.Errorf("GenCalls() = %d, want 0 (no TTS gen on empty due)", count)
	}
}

// TestNewSession_TTSFailure_Unlocks: when GenerateForSession fails, the
// already-acquired FSRS lock is released via a NON-defer UnlockCards call
// (D-10 init path). The unlock scope matches the resolved lockCategory/lockUnit.
func TestNewSession_TTSFailure_Unlocks(t *testing.T) {
	ttsErr := errors.New("simulated TTS failure")
	fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
	gen := &fakeTTSGenerator{genErr: ttsErr}

	s, err := session.NewSession(
		context.Background(),
		nil, "conn-3", fsrsSvc, gen, nil, nil, nil, nil, nil, testCfg(),
		fsrs.SelectOpts{Category: "spanish"},
	)
	if s != nil {
		t.Errorf("NewSession() returned non-nil session, want nil")
	}
	if err == nil {
		t.Fatal("NewSession() returned nil error, want TTS failure wrapped")
	}

	// The lock was released exactly once (D-10 init-path unlock).
	if count := fsrsSvc.UnlockCount(); count != 1 {
		t.Errorf("UnlockCount() = %d, want 1 (non-defer unlock on TTS failure)", count)
	}

	// The unlock scope matches the resolved Category (selectOpts.Category was
	// non-empty → used directly; Pitfall 4 scope replay).
	opts := fsrsSvc.UnlockOpts()
	if opts.Category != "spanish" {
		t.Errorf("UnlockOpts().Category = %q, want %q (resolved scope replay)", opts.Category, "spanish")
	}
}

// TestNewSession_NilGuards: missing required deps return a construction error.
func TestNewSession_NilGuards(t *testing.T) {
	gen := &fakeTTSGenerator{}

	// nil fsrsSvc
	if _, err := session.NewSession(
		context.Background(), nil, "c", nil, gen, nil, nil, nil, nil, nil, testCfg(), fsrs.SelectOpts{},
	); err == nil {
		t.Error("NewSession() with nil fsrsSvc should return error")
	}

	// nil gen
	fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
	if _, err := session.NewSession(
		context.Background(), nil, "c", fsrsSvc, nil, nil, nil, nil, nil, nil, testCfg(), fsrs.SelectOpts{},
	); err == nil {
		t.Error("NewSession() with nil gen should return error")
	}
}

// ---------------------------------------------------------------------------
// Plan 07-02: Event loop + D-10 defer chain tests
// ---------------------------------------------------------------------------

// shortTimeoutCfg returns a Config with very short per-state timeouts for testing.
func shortTimeoutCfg() infra.Config {
	cfg := testCfg()
	cfg.SessionTimeout.IdleStateTimeout = 50 * time.Millisecond
	cfg.SessionTimeout.ListeningStateTimeout = 50 * time.Millisecond
	cfg.SessionTimeout.DictatingStateTimeout = 50 * time.Millisecond
	return cfg
}

// multiWordTimeoutCfg returns a Config with longer timeouts for tests that exercise
// multi-word progression (need time to push events between state transitions).
func multiWordTimeoutCfg() infra.Config {
	cfg := testCfg()
	cfg.SessionTimeout.IdleStateTimeout = 5 * time.Second
	cfg.SessionTimeout.ListeningStateTimeout = 5 * time.Second
	cfg.SessionTimeout.DictatingStateTimeout = 5 * time.Second
	cfg.SessionTimeout.WaitingParentStateTimeout = 5 * time.Second
	return cfg
}

// waitForState polls the session state until it matches want or timeout expires.
func waitForState(t *testing.T, s *session.Session, want database.SessionState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.State() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitForState: state still %q, want %q after %v", s.State(), want, timeout)
}

// waitForLog polls the recordingLogger until a message containing sub has
// been recorded or the timeout expires (same deadline-loop shape as
// waitForState: 2ms interval, hard failure on timeout).
func waitForLog(t *testing.T, lg *recordingLogger, sub string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if lg.hasMessage(sub) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitForLog: no message containing %q within %v", sub, timeout)
}

// waitDone waits for the session goroutine to exit (Done channel closed) or timeout.
func waitDone(t *testing.T, s *session.Session, timeout time.Duration) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(timeout):
		t.Fatalf("waitDone: session did not exit within %v", timeout)
	}
}

// TestEventLoop_WordComplete: play_word → LISTENING → D-13 StreamWord completion → DICTATING.
func TestEventLoop_WordComplete(t *testing.T) {
	streamer := &fakeStreamer{}
	hub := &fakeHub{}
	fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
	gen := &fakeTTSGenerator{}
	repo := newFakeSessionRepository()

	s, err := session.NewSession(context.Background(), nil, "c1", fsrsSvc, gen, streamer, repo, nil, hub, nil, shortTimeoutCfg(), fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "play_word"})
	// D-13: fakeStreamer returns instantly, so LISTENING→DICTATING may race
	// past before we observe LISTENING. Asserting DICTATING proves both transitions.
	waitForState(t, s, database.StateDictating, 2*time.Second)

	if streamer.StreamCalls() < 1 {
		t.Errorf("StreamCalls = %d, want >= 1 (D-13 StreamWord spawned)", streamer.StreamCalls())
	}
}

// TestEventLoop_LastWord: 1 word — next_word on the last word drives WAITING_PARENT (not LISTENING).
func TestEventLoop_LastWord(t *testing.T) {
	streamer := &fakeStreamer{}
	hub := &fakeHub{}
	oneCard := []pybridge.DueCard{{ID: 1, Word: "apple"}}
	fsrsSvc := &fakeFSRSService{dueCards: oneCard}
	gen := &fakeTTSGenerator{}
	repo := newFakeSessionRepository()

	s, err := session.NewSession(context.Background(), nil, "c1", fsrsSvc, gen, streamer, repo, nil, hub, nil, shortTimeoutCfg(), fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "play_word"})
	// D-13 races past LISTENING; assert DICTATING (proves play_word→LISTENING→DICTATING).
	waitForState(t, s, database.StateDictating, 2*time.Second)
	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "next_word"})
	waitForState(t, s, database.StateWaitingParent, 2*time.Second)
}

// TestEventLoop_MultiWordFlow: exercises the full 2-word dictation cycle:
// play_word → LISTENING → DICTATING → next_word → LISTENING → DICTATING → next_word (last) → WAITING_PARENT.
func TestEventLoop_MultiWordFlow(t *testing.T) {
	streamer := &fakeStreamer{}
	hub := &fakeHub{}
	twoCards := []pybridge.DueCard{{ID: 1, Word: "apple"}, {ID: 2, Word: "banana"}}
	fsrsSvc := &fakeFSRSService{dueCards: twoCards}
	gen := &fakeTTSGenerator{}
	repo := newFakeSessionRepository()

	s, err := session.NewSession(context.Background(), nil, "c1", fsrsSvc, gen, streamer, repo, nil, hub, nil, multiWordTimeoutCfg(), fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	// Step 1: play_word → IDLE → LISTENING → (D-13) DICTATING
	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "play_word"})
	waitForState(t, s, database.StateDictating, 2*time.Second)
	time.Sleep(10 * time.Millisecond) // let event loop settle after D-13 StreamWord completes

	// Step 2: next_word (first word done) → DICTATING → LISTENING → (D-13) DICTATING
	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "next_word"})
	waitForState(t, s, database.StateDictating, 2*time.Second)
	time.Sleep(10 * time.Millisecond) // let event loop settle after D-13 StreamWord completes

	// Step 3: next_word (last word) → DICTATING → WAITING_PARENT
	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "next_word"})
	waitForState(t, s, database.StateWaitingParent, 2*time.Second)

	// Verify StreamWord was called for each word (at least 2 calls)
	if streamer.StreamCalls() < 2 {
		t.Errorf("StreamCalls = %d, want >= 2 (two words streamed)", streamer.StreamCalls())
	}
}

// TestEventLoop_Timeout: no events → per-state deadline fires → defer chain runs → lock released.
func TestEventLoop_Timeout(t *testing.T) {
	fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
	gen := &fakeTTSGenerator{}

	s, err := session.NewSession(context.Background(), nil, "c1", fsrsSvc, gen, nil, nil, nil, nil, nil, shortTimeoutCfg(), fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	waitDone(t, s, 5*time.Second)

	if fsrsSvc.UnlockCount() != 1 {
		t.Errorf("UnlockCount = %d, want 1 (FSRS lock released on timeout)", fsrsSvc.UnlockCount())
	}
}

// TestEventLoop_StreamWordCompletion: on IDLE→LISTENING transition, spawnStreamWord calls StreamWord.
func TestEventLoop_StreamWordCompletion(t *testing.T) {
	streamer := &fakeStreamer{}
	hub := &fakeHub{}
	fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
	gen := &fakeTTSGenerator{}
	repo := newFakeSessionRepository()

	s, err := session.NewSession(context.Background(), nil, "c1", fsrsSvc, gen, streamer, repo, nil, hub, nil, shortTimeoutCfg(), fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "play_word"})
	// D-13 races past LISTENING; just verify StreamWord was invoked after a settle period.
	time.Sleep(50 * time.Millisecond)

	if streamer.StreamCalls() < 1 {
		t.Errorf("StreamCalls = %d, want >= 1 (D-13 StreamWord invoked on LISTENING entry)", streamer.StreamCalls())
	}
}

// TestExitDefer_Timeout: timeout exit → all three D-10 cleanup methods fire.
func TestExitDefer_Timeout(t *testing.T) {
	streamer := &fakeStreamer{}
	hub := &fakeHub{}
	fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
	gen := &fakeTTSGenerator{}
	repo := newFakeSessionRepository()

	s, err := session.NewSession(context.Background(), &ws.Client{}, "c1", fsrsSvc, gen, streamer, repo, nil, hub, nil, shortTimeoutCfg(), fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	waitDone(t, s, 5*time.Second)

	if fsrsSvc.UnlockCount() != 1 {
		t.Errorf("UnlockCount = %d, want 1", fsrsSvc.UnlockCount())
	}
	if gen.CleanupCalls() != 1 {
		t.Errorf("CleanupCalls = %d, want 1", gen.CleanupCalls())
	}
	if hub.UnregisterCount() != 1 {
		t.Errorf("UnregisterCount = %d, want 1", hub.UnregisterCount())
	}
}

// TestExitDefer_Cancel: ctx cancel exit → all three D-10 cleanup methods fire.
func TestExitDefer_Cancel(t *testing.T) {
	streamer := &fakeStreamer{}
	hub := &fakeHub{}
	fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
	gen := &fakeTTSGenerator{}
	repo := newFakeSessionRepository()

	s, err := session.NewSession(context.Background(), &ws.Client{}, "c1", fsrsSvc, gen, streamer, repo, nil, hub, nil, shortTimeoutCfg(), fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	cancel()
	waitDone(t, s, 5*time.Second)

	if fsrsSvc.UnlockCount() != 1 {
		t.Errorf("UnlockCount = %d, want 1", fsrsSvc.UnlockCount())
	}
	if gen.CleanupCalls() != 1 {
		t.Errorf("CleanupCalls = %d, want 1", gen.CleanupCalls())
	}
	if hub.UnregisterCount() != 1 {
		t.Errorf("UnregisterCount = %d, want 1", hub.UnregisterCount())
	}
}

// TestExitDefer_NormalCompletion: full FSM lifecycle → cancel → all three D-10 cleanups fire.
func TestExitDefer_NormalCompletion(t *testing.T) {
	streamer := &fakeStreamer{}
	hub := &fakeHub{}
	oneCard := []pybridge.DueCard{{ID: 1, Word: "apple"}}
	fsrsSvc := &fakeFSRSService{dueCards: oneCard}
	gen := &fakeTTSGenerator{}
	repo := newFakeSessionRepository()

	s, err := session.NewSession(context.Background(), &ws.Client{}, "c1", fsrsSvc, gen, streamer, repo, nil, hub, nil, shortTimeoutCfg(), fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "play_word"})
	// D-13 races past LISTENING; assert DICTATING directly.
	waitForState(t, s, database.StateDictating, 2*time.Second)
	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "next_word"})
	waitForState(t, s, database.StateWaitingParent, 2*time.Second)
	s.Push(session.SessionEvent{Type: session.EventParentReview})
	waitForState(t, s, database.StateFeedback, 2*time.Second)

	cancel()
	waitDone(t, s, 5*time.Second)

	if fsrsSvc.UnlockCount() != 1 {
		t.Errorf("UnlockCount = %d, want 1", fsrsSvc.UnlockCount())
	}
	if gen.CleanupCalls() != 1 {
		t.Errorf("CleanupCalls = %d, want 1", gen.CleanupCalls())
	}
	if hub.UnregisterCount() != 1 {
		t.Errorf("UnregisterCount = %d, want 1", hub.UnregisterCount())
	}
}

// TestReleaseFSRSLock_FreshContext: deferred UnlockCards receives a LIVE ctx (Pitfall 1).
func TestReleaseFSRSLock_FreshContext(t *testing.T) {
	fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
	gen := &fakeTTSGenerator{}

	s, err := session.NewSession(context.Background(), nil, "c1", fsrsSvc, gen, nil, nil, nil, nil, nil, shortTimeoutCfg(), fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	cancel() // cancel runCtx → defer chain → releaseFSRSLock with FRESH ctx
	waitDone(t, s, 5*time.Second)

	if err := fsrsSvc.UnlockCtxErr(); err != nil {
		t.Errorf("UnlockCtxErr = %v, want nil (fresh ctx not cancelled — Pitfall 1)", err)
	}
}

// TestExit_NoRatingCommit: exit must NOT call UpdateRatings (D-11/D-12).
func TestExit_NoRatingCommit(t *testing.T) {
	fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
	gen := &fakeTTSGenerator{}

	s, err := session.NewSession(context.Background(), nil, "c1", fsrsSvc, gen, nil, nil, nil, nil, nil, shortTimeoutCfg(), fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	cancel()
	waitDone(t, s, 5*time.Second)

	if fsrsSvc.UpdateRatingsCount() != 0 {
		t.Errorf("UpdateRatingsCount = %d, want 0 (D-11/D-12 no partial-progress commit)", fsrsSvc.UpdateRatingsCount())
	}
}

// ---------------------------------------------------------------------------
// 260823-sdx: stale WordComplete race (repeat/next_word interleave)
// ---------------------------------------------------------------------------

// gatedStreamer is a session.SessionStreamer fake whose StreamWord calls block
// on a per-call gate until the test releases them (260823-sdx). It deliberately
// does NOT select on ctx.Done(): that reproduces the worst case where cancel
// has already been called on an in-flight stream but the stream still finishes
// and its goroutine pushes a WordComplete event — the stale-event race under
// test. Without that "ignore the cancellation" behavior the race cannot be
// made deterministic.
type gatedStreamer struct {
	mu       sync.Mutex
	words    []string        // wordID of each StreamWord call, in call order
	gates    []chan struct{} // gate[i] blocks call i until closed
	returned []chan struct{} // returned[i] is closed when call i returns
	released []bool          // makes release idempotent (cleanup re-releases)
}

func (g *gatedStreamer) StreamWord(ctx context.Context, client tts.Sender, sessionID string, wordID string, startSeq uint32) error {
	g.mu.Lock()
	gate := make(chan struct{})
	ret := make(chan struct{})
	g.words = append(g.words, wordID)
	g.gates = append(g.gates, gate)
	g.returned = append(g.returned, ret)
	g.released = append(g.released, false)
	g.mu.Unlock()

	// Deliberately NOT selecting on ctx.Done() — see the type comment. An
	// already-cancelled stream that still completes and pushes WordComplete is
	// exactly the timing this fake must reproduce.
	<-gate
	close(ret)
	return nil
}

// ClearSeqCounter satisfies the SessionStreamer interface (D-11 teardown hook).
// No-op for tests.
func (g *gatedStreamer) ClearSeqCounter(sessionID string) {
	// no-op
}

// release closes the i-th call's gate, letting that StreamWord call return.
// Idempotent: closing an already-released gate is a no-op, so t.Cleanup can
// safely re-release every gate after the test released some of them.
func (g *gatedStreamer) release(i int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if i >= 0 && i < len(g.gates) && !g.released[i] {
		g.released[i] = true
		close(g.gates[i])
	}
}

// waitReturned blocks until the i-th StreamWord call has returned, failing the
// test after a 2s timeout.
func (g *gatedStreamer) waitReturned(t *testing.T, i int) {
	t.Helper()
	g.mu.Lock()
	ret := g.returned[i]
	g.mu.Unlock()
	select {
	case <-ret:
	case <-time.After(2 * time.Second):
		t.Fatalf("waitReturned: StreamWord call %d did not return within 2s", i)
	}
}

// calls returns how many StreamWord calls have been registered so far.
func (g *gatedStreamer) calls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.words)
}

// wordAt returns the wordID of the i-th StreamWord call.
func (g *gatedStreamer) wordAt(i int) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.words[i]
}

var _ session.SessionStreamer = (*gatedStreamer)(nil)

// waitForStreamCalls polls the gatedStreamer until at least n StreamWord calls
// have been registered (2ms interval, 2s timeout). Needed because repeat does
// not change the observable state — the spawn side effect is the only signal.
func waitForStreamCalls(t *testing.T, g *gatedStreamer, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g.calls() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitForStreamCalls: %d StreamWord calls registered, want >= %d within 2s", g.calls(), n)
}

// TestEventLoop_Repeat_StaleWordCompleteDropped reproduces the 260823-sdx
// race: in DICTATING, repeat spawns a replacement stream (gen2); next_word then
// supersedes it again (gen3) and cancels gen2 — but the cancelled gen2 still
// completes and pushes a stale WordComplete. The event loop must DROP that
// stale event (session stays LISTENING) instead of letting it prematurely drive
// LISTENING→DICTATING, which would skip the new word's listening phase.
//
// NOTE: the assertion is deliberately on the mid-sequence state (after the
// stale event, before releasing gen3) — the FINAL state is DICTATING with or
// without the fix (a WordComplete arriving while already DICTATING is an
// invalid transition and gets dropped), so only the intermediate state
// discriminates.
func TestEventLoop_Repeat_StaleWordCompleteDropped(t *testing.T) {
	gs := &gatedStreamer{}
	hub := &fakeHub{}
	twoCards := []pybridge.DueCard{{ID: 1, Word: "apple"}, {ID: 2, Word: "banana"}}
	fsrsSvc := &fakeFSRSService{dueCards: twoCards}
	gen := &fakeTTSGenerator{}
	repo := newFakeSessionRepository()
	lg := &recordingLogger{}

	s, err := session.NewSession(context.Background(), nil, "c1", fsrsSvc, gen, gs, repo, nil, hub, lg, multiWordTimeoutCfg(), fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	// Release every registered gate on cleanup so blocked StreamWord
	// goroutines don't pile up if the test fails early.
	t.Cleanup(func() {
		for i := 0; i < gs.calls(); i++ {
			gs.release(i)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	// Step 1: play_word → LISTENING spawns gen1 (apple), blocked on its gate.
	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "play_word"})
	waitForStreamCalls(t, gs, 1)
	gs.release(0)
	gs.waitReturned(t, 0)
	waitForState(t, s, database.StateDictating, 2*time.Second)

	// Step 2: repeat in DICTATING spawns gen2 (same word), blocked on its gate.
	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "repeat"})
	waitForStreamCalls(t, gs, 2)

	// Step 3: next_word → LISTENING (wordIndex=1) spawns gen3 (banana) and
	// cancels gen2 — which the fake deliberately ignores, so gen2 stays
	// blocked on its own gate with an already-cancelled context.
	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "next_word"})
	waitForState(t, s, database.StateListening, 2*time.Second)
	waitForStreamCalls(t, gs, 3)

	// Step 4: let the cancelled gen2 finish — its goroutine pushes a stale
	// WordComplete onto the events channel; then poll until the event loop
	// has consumed AND dropped it. The drop path's debug log is the only
	// synchronous signal (emitted on the event-loop goroutine), so
	// consumption is proven before the Step 5 assertions — no wall clock.
	gs.release(1)
	gs.waitReturned(t, 1)
	waitForLog(t, lg, "dropping stale word_complete", 2*time.Second)

	// Step 5 (discriminating assertion): the stale WordComplete(gen2) must be
	// dropped — still LISTENING, still on the second word.
	if got, want := s.State(), database.StateListening; got != want {
		t.Errorf("stale WordComplete drove a transition: state=%v, want=%v", got, want)
	}
	if got, want := s.CurrentWord(), "banana"; got != want {
		t.Errorf("CurrentWord() = %q, want %q (stale event must not advance the word)", got, want)
	}

	// Step 6: the current-generation WordComplete(gen3) drives
	// LISTENING→DICTATING normally.
	gs.release(2)
	gs.waitReturned(t, 2)
	waitForState(t, s, database.StateDictating, 2*time.Second)

	// Exactly three streams were spawned: apple (play), apple (repeat),
	// banana (next_word) — the stale gen2 must not have triggered a re-spawn.
	if got := gs.calls(); got != 3 {
		t.Errorf("StreamWord calls = %d, want 3", got)
	}
	for i, want := range []string{"apple", "apple", "banana"} {
		if got := gs.wordAt(i); got != want {
			t.Errorf("wordAt(%d) = %q, want %q", i, got, want)
		}
	}

	cancel()
	waitDone(t, s, 2*time.Second)
}

// TestEventLoop_DictatingTimeoutSkipsWithoutSpin: a dictating deadline
// advances to the next word on the event loop itself. The new deadline
// stays in the future, so the timer does not skip every remaining word
// in one burst, and the session stays alive.
func TestEventLoop_DictatingTimeoutSkipsWithoutSpin(t *testing.T) {
	streamer := &fakeStreamer{}
	hub := &fakeHub{}
	twoCards := []pybridge.DueCard{{ID: 1, Word: "apple"}, {ID: 2, Word: "banana"}}
	fsrsSvc := &fakeFSRSService{dueCards: twoCards}
	gen := &fakeTTSGenerator{}
	repo := newFakeSessionRepository()

	cfg := testCfg()
	cfg.SessionTimeout.IdleStateTimeout = 5 * time.Second
	cfg.SessionTimeout.ListeningStateTimeout = 5 * time.Second
	cfg.SessionTimeout.DictatingStateTimeout = 120 * time.Millisecond
	cfg.SessionTimeout.WaitingParentStateTimeout = 5 * time.Second

	s, err := session.NewSession(context.Background(), nil, "c1", fsrsSvc, gen, streamer, repo, nil, hub, nil, cfg, fsrs.SelectOpts{})
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "play_word"})
	waitForState(t, s, database.StateDictating, 2*time.Second)
	if got := s.CurrentWord(); got != "apple" {
		t.Fatalf("CurrentWord() = %q, want apple", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.State() == database.StateDictating && s.CurrentWord() == "banana" {
			break
		}
		if s.State() == database.StateWaitingParent {
			t.Fatal("both words were skipped before banana was dictated; the deadline refired immediately")
		}
		select {
		case <-s.Done():
			t.Fatal("session exited on a dictating timeout")
		default:
		}
		time.Sleep(2 * time.Millisecond)
	}
	if s.State() != database.StateDictating || s.CurrentWord() != "banana" {
		t.Fatalf("state=%s word=%q, want DICTATING banana", s.State(), s.CurrentWord())
	}
	if !s.Deadline().After(time.Now().Add(50 * time.Millisecond)) {
		t.Fatalf("deadline %v is not ahead of now; the timeout would refire immediately", s.Deadline())
	}
	select {
	case <-s.Done():
		t.Fatal("session exited after skipping one word")
	default:
	}
}
