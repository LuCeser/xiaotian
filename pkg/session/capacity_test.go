package session_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
	"go.uber.org/zap"
)

// bootCapManager builds a live Manager whose cfg caps sessions at cap, with
// an FSRS fake that always has words and the standard test fakes for the
// other seams. Sessions idle until EventCancel, so the registry fills as
// sessions start.
func bootCapManager(t *testing.T, cap int) *session.Manager {
	t.Helper()
	cfg := testCfg()
	cfg.Session.MaxConcurrent = cap
	fk := &fakeFSRSService{dueCards: []pybridge.DueCard{
		{ID: 1, Word: "apple"}, {ID: 2, Word: "banana"},
	}}
	pool, err := infra.NewWorkerPool(cfg.Worker, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("worker pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Shutdown(context.Background()) })
	// Real ws.Hub (NewManager takes the concrete type; the Session-level
	// seam still gets exercised through it — Unregister is non-blocking).
	hub, err := ws.NewHub(ws.HubConfig{Limit: 10, RegisterBuffer: 8, UnregisterBuffer: 8}, zap.NewNop())
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	m, err := session.NewManager(hub, fk, &fakeTTSGenerator{}, &fakeStreamer{},
		newFakeSessionRepository(), pool, infra.NewNoOpLogger(), cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// TestManagerCapacity_RejectsBeyondCap: the (n+1)-th concurrent session is
// rejected with ErrManagerAtCapacity and nothing is locked from FSRS.
func TestManagerCapacity_RejectsBeyondCap(t *testing.T) {
	m := bootCapManager(t, 2)

	s1, err1 := m.NewSession(context.Background(), nil, "conn-a", fsrs.SelectOpts{Limit: 2})
	_, err2 := m.NewSession(context.Background(), nil, "conn-b", fsrs.SelectOpts{Limit: 2})
	if err1 != nil || err2 != nil {
		t.Fatalf("seed sessions: %v / %v", err1, err2)
	}
	_ = s1 // both must stay registered; cancels ride the Cleanup pushes below
	t.Cleanup(func() {
		m.Push("conn-a", session.SessionEvent{Type: session.EventCancel})
		m.Push("conn-b", session.SessionEvent{Type: session.EventCancel})
	})

	if _, err := m.NewSession(context.Background(), nil, "conn-c", fsrs.SelectOpts{Limit: 2}); !errors.Is(err, session.ErrManagerAtCapacity) {
		t.Fatalf("NewSession beyond cap err = %v, want ErrManagerAtCapacity", err)
	}
	// The rejected session is not in the registry.
	if s := m.Lookup("conn-c"); s != nil {
		t.Error("rejected session registered")
	}
}

// TestManagerCapacity_FreesOnExit: after one session exits (onExit removes
// its registry entry), a new session is admitted again.
func TestManagerCapacity_FreesOnExit(t *testing.T) {
	m := bootCapManager(t, 1)

	if _, err := m.NewSession(context.Background(), nil, "conn-a", fsrs.SelectOpts{Limit: 2}); err != nil {
		t.Fatalf("first session: %v", err)
	}
	if _, err := m.NewSession(context.Background(), nil, "conn-b", fsrs.SelectOpts{Limit: 2}); !errors.Is(err, session.ErrManagerAtCapacity) {
		t.Fatalf("second session err = %v, want ErrManagerAtCapacity", err)
	}

	// Cancel → run() exits → D-10 chain → onExit removes the map entry.
	s := m.Lookup("conn-a")
	if s == nil {
		t.Fatal("conn-a session missing")
	}
	m.Push("conn-a", session.SessionEvent{Type: session.EventCancel})
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not exit after cancel")
	}

	if _, err := m.NewSession(context.Background(), nil, "conn-b", fsrs.SelectOpts{Limit: 2}); err != nil {
		t.Fatalf("session after freeing a slot: %v", err)
	}
}

// TestManagerCapacity_ConcurrentStartsStayAtCap: N simultaneous NewSession
// calls at cap N-1 admit exactly N-1 — the reservation closes the TOCTOU
// window a plain check-then-act gate leaves open (Ticket 07 review).
func TestManagerCapacity_ConcurrentStartsStayAtCap(t *testing.T) {
	const tries = 8
	m := bootCapManager(t, tries-1)

	var wg sync.WaitGroup
	var admitted atomic.Int32
	for i := 0; i < tries; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := m.NewSession(context.Background(), nil, "race-"+strconv.Itoa(i), fsrs.SelectOpts{Limit: 2}); err == nil {
				admitted.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := admitted.Load(); got != tries-1 {
		t.Errorf("admitted %d of %d concurrent starts at cap %d, want exactly %d",
			got, tries, tries-1, tries-1)
	}
}

// TestManager_SameConnRestartKeepsNewSession: a second start on the same
// connID replaces the registry entry; the OLD session's later exit must not
// delete the NEW session's entry (review finding: onExit used an
// unconditional delete, corrupting the registry, undercounting capacity and
// blinding the sweeper).
func TestManager_SameConnRestartKeepsNewSession(t *testing.T) {
	m := bootCapManager(t, 10)

	s1, err := m.NewSession(context.Background(), nil, "conn-x", fsrs.SelectOpts{Limit: 2})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	s2, err := m.NewSession(context.Background(), nil, "conn-x", fsrs.SelectOpts{Limit: 2})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if m.Lookup("conn-x") != s2 {
		t.Fatal("restart did not replace the registry entry")
	}

	// The old session exits — the new one must survive. Drive s1's exit
	// directly (its own event channel), not the manager's Push (which now
	// targets s2, the current entry).
	s1.Push(session.SessionEvent{Type: session.EventCancel})
	select {
	case <-s1.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("old session did not exit")
	}
	if got := m.Lookup("conn-x"); got != s2 {
		t.Fatalf("old session's exit removed the new session's entry (got %v)", got)
	}
	if got := m.ActiveCount(); got != 1 {
		t.Errorf("ActiveCount = %d, want 1 (only the restarted session)", got)
	}
}
