package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
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
