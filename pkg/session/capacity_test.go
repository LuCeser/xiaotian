package session_test

import (
	"context"
	"errors"
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

	s1, err := m.NewSession(context.Background(), nil, "conn-a", fsrs.SelectOpts{Limit: 2})
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	s2, err := m.NewSession(context.Background(), nil, "conn-b", fsrs.SelectOpts{Limit: 2})
	if err != nil {
		t.Fatalf("second session: %v", err)
	}
	t.Cleanup(func() {
		m.Push("conn-a", session.SessionEvent{Type: session.EventCancel})
		m.Push("conn-b", session.SessionEvent{Type: session.EventCancel})
	})
	_ = s1
	_ = s2

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
