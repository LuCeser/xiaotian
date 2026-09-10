package session_test

// Ticket 07 load shape: 50 concurrent sessions through the REAL Manager + FSM
// + sweeper (fakes at the FSRS/TTS/streamer seams), each driven to FEEDBACK
// and out, asserting the multi-user invariants: every session completes, the
// registry fully drains, the streamer's per-session seq counters all clear,
// and per-session audit rows stay distinct. A loose heap guard documents the
// 150MB budget rather than hard-failing on CI variance.

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
	"go.uber.org/zap"
)

// loadFSRS hands every session two distinct words (IDs unique per session so
// the scoped unlock contract is exercised across the fleet).
type loadFSRS struct {
	mu sync.Mutex
}

func (f *loadFSRS) SelectDueCards(_ context.Context, _ fsrs.SelectOpts) ([]pybridge.DueCard, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := time.Now().UnixNano()
	return []pybridge.DueCard{
		{ID: int(n % 100000), Word: "apple"},
		{ID: int((n + 1) % 100000), Word: "banana"},
	}, nil
}

func (f *loadFSRS) UpdateRatings(_ context.Context, _ map[string]string, _ string) (pybridge.ReviewResult, error) {
	return pybridge.ReviewResult{}, nil
}

func (f *loadFSRS) UnlockCards(_ context.Context, _ fsrs.UnlockOpts) (int, error) {
	return 2, nil
}

// countingStreamer counts ClearSeqCounter calls — the Pitfall 5 leak guard.
type countingStreamer struct {
	fakeStreamer
	mu      sync.Mutex
	cleared int
}

func (s *countingStreamer) ClearSeqCounter(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleared++
}

func (s *countingStreamer) clearedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleared
}

// recordingLoadRepo records distinct connIDs of Create calls.
type recordingLoadRepo struct {
	fakeSessionRepository

	createdConns map[string]bool
}

func (r *recordingLoadRepo) Create(_ context.Context, s database.Session) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCnt++
	if r.createdConns == nil {
		r.createdConns = make(map[string]bool)
	}
	r.createdConns[s.ConnectionID] = true
	return int64(r.createCnt), nil
}

func (r *recordingLoadRepo) createdCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.createdConns)
}

// TestLoad_50ConcurrentSessions: the ticket's 50-concurrent acceptance
// against the Go-side machinery. Each goroutine starts a session, drives
// IDLE→LISTENING→DICTATING→WAITING_PARENT→FEEDBACK through the FSM, then
// cancels; the test waits for the registry to drain fully.
func TestLoad_50ConcurrentSessions(t *testing.T) {
	const n = 50

	logger := infra.NewNoOpLogger()
	cfg := testCfg()
	cfg.Session.MaxConcurrent = n + 10 // cap above the fleet for this test
	cfg.Session.CleanupInterval = 100 * time.Millisecond

	hub, err := ws.NewHub(ws.HubConfig{Limit: n + 10, RegisterBuffer: n, UnregisterBuffer: n}, zap.NewNop())
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	pool, err := infra.NewWorkerPool(cfg.Worker, logger)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Shutdown(context.Background()) })

	streamer := &countingStreamer{}
	repo := &recordingLoadRepo{}
	manager, err := session.NewManager(hub, &loadFSRS{}, &fakeTTSGenerator{}, streamer, repo, pool, logger, cfg)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	svcCtx, svcCancel := context.WithCancel(context.Background())
	t.Cleanup(svcCancel)
	go hub.Run(svcCtx)
	go manager.Start(svcCtx)

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			connID := "load-conn-" + itoa(i)
			s, err := manager.NewSession(context.Background(), nil, connID, fsrs.SelectOpts{Limit: 2})
			if err != nil {
				t.Errorf("session %d: %v", i, err)
				return
			}
			// Drive the spine: play → DICTATING(word 1) → next_word →
			// DICTATING(word 2) → next_word (last) → WAITING_PARENT →
			// parent review → FEEDBACK. Waits pin state AND current word —
			// polling state alone can't tell word 1's DICTATING from word
			// 2's (both fake words stream instantly), and a next_word sent
			// into the wrong one is dropped by the FSM.
			s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "play_word"})
			waitLoadWord(t, s, database.StateDictating, "apple", connID)
			s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "next_word"})
			waitLoadWord(t, s, database.StateDictating, "banana", connID)
			s.Push(session.SessionEvent{Type: session.EventControlFrame, ControlType: "next_word"})
			waitLoadState(t, s, database.StateWaitingParent, connID)
			s.Push(session.SessionEvent{Type: session.EventParentReview})
			waitLoadState(t, s, database.StateFeedback, connID)
			s.Push(session.SessionEvent{Type: session.EventCancel})
		}(i)
	}
	wg.Wait()

	// Registry must fully drain (every session exited + onExit fired).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if manager.ActiveCount() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := manager.ActiveCount(); got != 0 {
		t.Errorf("registry did not drain: %d sessions still active", got)
	}

	// Every session cleared its streamer seq counter (Pitfall 5 leak guard).
	if got := streamer.clearedCount(); got != n {
		t.Errorf("ClearSeqCounter calls = %d, want %d (leak under load)", got, n)
	}

	// Audit rows stayed per-connection distinct.
	if got := repo.createdCount(); got != n {
		t.Errorf("audit Create calls = %d, want %d", got, n)
	}

	elapsed := time.Since(start)
	t.Logf("load: %d sessions to FEEDBACK + drained in %v", n, elapsed)

	// Loose heap documentation for the 150MB budget: sessions alone must be
	// far under it (this excludes audio buffers — 50MB pool — and the rest of
	// the service, so the guard sits at a small fraction).
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("load: HeapAlloc = %.1f MB", float64(ms.HeapAlloc)/(1<<20))
	if ms.HeapAlloc > 100<<20 {
		t.Errorf("HeapAlloc = %d MB after load, unexpectedly high vs the 150MB service budget", ms.HeapAlloc>>20)
	}
}

// waitLoadState polls a session until it reaches want (bounded; failure names
// the connID).
func waitLoadState(t *testing.T, s *session.Session, want database.SessionState, connID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.State() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("%s: never reached %s (at %s)", connID, want, s.State())
}

// waitLoadWord pins state AND current word — the load test's fake streams
// complete instantly, so consecutive words pass through the same states and
// only the word disambiguates them (ticket-05 lesson).
func waitLoadWord(t *testing.T, s *session.Session, want database.SessionState, word, connID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.State() == want && s.CurrentWord() == word {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("%s: never reached %s at %s (at %s/%s)", connID, want, word, s.State(), s.CurrentWord())
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
