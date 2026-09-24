package session

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/tts"
)

// ---------------------------------------------------------------------------
// Inline test fakes (package session — can access unexported Session fields).
// ---------------------------------------------------------------------------

// testFSRS implements fsrs.FSRSService for manager tests. Only UnlockCards
// is exercised (via the D-10 defer chain); SelectDueCards is never called
// because these tests construct *Session via struct literals, not NewSession.
type testFSRS struct {
	unlockCalled atomic.Int64
	unlockCtxErr error
}

func (f *testFSRS) SelectDueCards(_ context.Context, _ fsrs.SelectOpts) ([]pybridge.DueCard, error) {
	return nil, nil
}

func (f *testFSRS) UpdateRatings(_ context.Context, _ map[string]string, _ string) (pybridge.ReviewResult, error) {
	return pybridge.ReviewResult{}, nil
}

func (f *testFSRS) UnlockCards(ctx context.Context, _ fsrs.UnlockOpts) (int, error) {
	f.unlockCalled.Add(1)
	f.unlockCtxErr = ctx.Err()
	return 0, nil
}

// testGen implements TTSSessionGenerator for manager tests.
type testGen struct {
	cleanupCalled atomic.Int64
}

func (g *testGen) GenerateForSession(_ context.Context, _ string, _ []string) (map[string]string, error) {
	return nil, nil
}

// GenerateForSessionWithSource satisfies the widened DICT-03 seam.
func (g *testGen) GenerateForSessionWithSource(_ context.Context, _ string, _ []string) (map[string]string, tts.AudioSource, error) {
	return nil, tts.SourceVolcengine, nil
}

func (g *testGen) GenerateText(_ context.Context, _, _, _ string) (string, error) {
	return "/fake/feedback.pcm", nil
}

func (g *testGen) CleanupSession(_ context.Context, _ string) error {
	g.cleanupCalled.Add(1)
	return nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestManager_Sweeper: cleanupLeaked cancels sessions whose deadline exceeds
// 2× the state timeout (the leak-defense backstop, ERR-03).
func TestManager_Sweeper(t *testing.T) {
	cfg := infra.DefaultConfig()
	cfg.SessionTimeout.IdleStateTimeout = 1 * time.Millisecond // tiny threshold: 2× = 2ms
	var cancelled atomic.Bool

	m := &Manager{
		cfg:      cfg,
		sessions: make(map[string]*Session),
		logger:   infra.NewNoOpLogger(),
	}

	s := &Session{
		connID:   "leaked",
		state:    database.StateIdle,
		deadline: time.Now().Add(-1 * time.Hour), // long expired
		cfg:      cfg,
		cancel:   func() { cancelled.Store(true) },
	}
	m.sessions["leaked"] = s

	m.cleanupLeaked()

	if !cancelled.Load() {
		t.Error("cleanupLeaked() should have called cancel on the leaked session")
	}
}

// TestManager_Sweeper_NotLeaked: active sessions are NOT cancelled by the sweeper.
func TestManager_Sweeper_NotLeaked(t *testing.T) {
	cfg := infra.DefaultConfig()
	var cancelled atomic.Bool

	m := &Manager{
		cfg:      cfg,
		sessions: make(map[string]*Session),
		logger:   infra.NewNoOpLogger(),
	}

	s := &Session{
		connID:   "active",
		state:    database.StateIdle,
		deadline: time.Now().Add(1 * time.Hour), // not expired
		cfg:      cfg,
		cancel:   func() { cancelled.Store(true) },
	}
	m.sessions["active"] = s

	m.cleanupLeaked()

	if cancelled.Load() {
		t.Error("cleanupLeaked() should NOT have called cancel on an active session")
	}
}

// TestManager_Lifecycle_OnExit: onExit callback removes the session from the
// Manager map when run() exits, and the D-10 defer chain fires.
func TestManager_Lifecycle_OnExit(t *testing.T) {
	cfg := infra.DefaultConfig()
	cfg.SessionTimeout.IdleStateTimeout = 50 * time.Millisecond

	fsrsSvc := &testFSRS{}
	gen := &testGen{}

	m := &Manager{
		cfg:      cfg,
		sessions: make(map[string]*Session),
		logger:   infra.NewNoOpLogger(),
	}

	ctx, cancel := context.WithCancel(context.Background())

	s := &Session{
		id:       "lifecycle",
		connID:   "c1",
		state:    database.StateIdle,
		deadline: time.Now().Add(50 * time.Millisecond),
		fsrsSvc:  fsrsSvc,
		gen:      gen,
		logger:   infra.NewNoOpLogger(),
		cfg:      cfg,
		events:   make(chan SessionEvent, 32),
		done:     make(chan struct{}),
		runCtx:   ctx,
		cancel:   cancel,
		// Ticket 07: exit replays a card-ID-scoped unlock; a hand-built
		// session carries one card so the unlock path fires (production
		// sessions always have ≥1 — ErrNoDueWords guards the empty queue).
		lockCardIDs: []int64{1},
	}
	// onExit assigned after construction: the pointer-compare guard needs the
	// finished `s` (self-reference inside the literal is not in scope).
	s.onExit = func() { m.removeSession("c1", s) }
	m.sessions["c1"] = s

	go s.run(ctx)

	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not exit within 5s")
	}

	if m.Lookup("c1") != nil {
		t.Error("Lookup should return nil after session exited (onExit removes from map)")
	}
	if got := fsrsSvc.unlockCalled.Load(); got != 1 {
		t.Errorf("UnlockCards should be called once on exit, got %d", got)
	}
	if got := gen.cleanupCalled.Load(); got != 1 {
		t.Errorf("CleanupSession should be called once on exit, got %d", got)
	}
}

// TestSession_NoGoroutineLeak: no goroutine remains after a full init→run→exit
// lifecycle (NumGoroutine diffing, ERR-03).
func TestSession_NoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	cfg := infra.DefaultConfig()
	cfg.SessionTimeout.IdleStateTimeout = 50 * time.Millisecond

	fsrsSvc := &testFSRS{}
	gen := &testGen{}

	s := &Session{
		id:       "leak-test",
		connID:   "c1",
		state:    database.StateIdle,
		deadline: time.Now().Add(50 * time.Millisecond),
		fsrsSvc:  fsrsSvc,
		gen:      gen,
		logger:   infra.NewNoOpLogger(),
		cfg:      cfg,
		events:   make(chan SessionEvent, 32),
		done:     make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.runCtx = ctx
	s.cancel = cancel

	go s.run(ctx)

	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not exit within 5s")
	}

	// Allow spawned goroutines (e.g. spawnStreamWord) to drain.
	time.Sleep(100 * time.Millisecond)

	after := runtime.NumGoroutine()
	diff := after - before
	if diff > 1 {
		t.Errorf("goroutine leak: before=%d, after=%d, diff=%d (want <=1)", before, after, diff)
	}
}
