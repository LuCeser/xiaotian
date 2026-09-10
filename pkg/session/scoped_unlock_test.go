package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
)

// TestSessionExit_ScopedUnlock: the D-10 defer chain releases the session's
// own card IDs — student A finishing must not drop student B's in-flight
// locks (the multi-user isolation contract, Ticket 07). The shared fake
// captures the last UnlockOpts; the assertions pin IDs over category sweep.
func TestSessionExit_ScopedUnlock(t *testing.T) {
	fk := &fakeFSRSService{}
	fk.dueCards = []pybridge.DueCard{
		{ID: 11, Word: "apple"}, {ID: 12, Word: "banana"}, {ID: 13, Word: "cherry"},
	}
	s, err := session.NewSession(
		context.Background(), nil, "conn-scoped",
		fk, &fakeTTSGenerator{}, nil,
		nil, nil, nil, infra.NewNoOpLogger(), testCfg(),
		fsrs.SelectOpts{Limit: 3},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Start under a cancellable ctx; cancel drives the exit so the D-10
	// defer chain (and its scoped unlock) fires promptly.
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	cancel()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not exit")
	}

	if got := fk.UnlockCount(); got != 1 {
		t.Fatalf("UnlockCards calls = %d, want 1", got)
	}
	opts := fk.UnlockOpts()
	if len(opts.IDs) != 3 {
		t.Fatalf("unlock IDs = %v, want the session's three card IDs", opts.IDs)
	}
	want := []int64{11, 12, 13}
	for i := range want {
		if opts.IDs[i] != want[i] {
			t.Errorf("unlock IDs = %v, want %v (a category sweep would clobber other sessions' locks)", opts.IDs, want)
			break
		}
	}
}
