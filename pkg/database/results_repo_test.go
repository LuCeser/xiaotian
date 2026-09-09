package database

import (
	"context"
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// seedGraded inserts a session and a batch_graded event carrying payload,
// returning the session id. Interleaves a state-audit event so the query must
// filter by event type, not just join.
func seedGraded(t *testing.T, repo SessionRepository, payload string) int64 {
	t.Helper()
	id, err := repo.Create(context.Background(), Session{ConnectionID: "conn-r", State: StateFeedback})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.UpdateState(context.Background(), id, StateFeedback); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	if err := repo.AppendEvent(context.Background(), SessionEvent{SessionID: id, EventType: "state_changed"}); err != nil {
		t.Fatalf("AppendEvent(noise): %v", err)
	}
	if err := repo.AppendEvent(context.Background(), SessionEvent{SessionID: id, EventType: "batch_graded", Payload: payload}); err != nil {
		t.Fatalf("AppendEvent(batch_graded): %v", err)
	}
	return id
}

// TestListRecentResults: newest-first graded sessions with their payloads;
// ungraded sessions never appear; the limit truncates.
func TestListRecentResults(t *testing.T) {
	db := newTestDB(t)
	repo, _ := NewSessionRepository(db, infra.NewNoOpLogger())

	// An ungraded session that must stay invisible.
	if _, err := repo.Create(context.Background(), Session{ConnectionID: "conn-x", State: StateDictating}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	id1 := seedGraded(t, repo, `{"correct_count":3,"total":4}`)
	id2 := seedGraded(t, repo, `{"correct_count":4,"total":4}`)
	id3 := seedGraded(t, repo, `{"correct_count":1,"total":4}`)

	results, err := repo.ListRecentResults(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecentResults: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	// Newest first (highest event id first).
	if results[0].SessionID != id3 || results[1].SessionID != id2 || results[2].SessionID != id1 {
		t.Errorf("order = [%d %d %d], want newest-first [%d %d %d]",
			results[0].SessionID, results[1].SessionID, results[2].SessionID, id3, id2, id1)
	}
	if results[0].Payload != `{"correct_count":1,"total":4}` {
		t.Errorf("payload = %q", results[0].Payload)
	}
	if results[0].CreatedAt == "" {
		t.Error("CreatedAt empty")
	}

	// Limit truncates to the most recent.
	results, err = repo.ListRecentResults(context.Background(), 2)
	if err != nil {
		t.Fatalf("ListRecentResults(2): %v", err)
	}
	if len(results) != 2 || results[0].SessionID != id3 || results[1].SessionID != id2 {
		t.Errorf("limited results = %+v, want the 2 newest", results)
	}

	// limit <= 0 returns nothing rather than everything.
	results, err = repo.ListRecentResults(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListRecentResults(0): %v", err)
	}
	if len(results) != 0 {
		t.Errorf("ListRecentResults(0) = %d results, want 0", len(results))
	}
}

// A database error surfaces wrapped (not swallowed into an empty slice).
func TestListRecentResults_DBError(t *testing.T) {
	db := newTestDB(t)
	repo, _ := NewSessionRepository(db, infra.NewNoOpLogger())
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := repo.ListRecentResults(context.Background(), 5); err == nil || errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want a real query error", err)
	}
}
