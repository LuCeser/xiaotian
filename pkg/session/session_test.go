package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
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
