package database

import (
	"context"
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

func TestSessionRepository(t *testing.T) {
	t.Run("NewSessionRepository with nil db returns error", func(t *testing.T) {
		_, err := NewSessionRepository(nil, infra.NewNoOpLogger())
		if err == nil {
			t.Fatal("NewSessionRepository(nil, logger) expected error, got nil")
		}
	})

	t.Run("NewSessionRepository with nil logger substitutes no-op", func(t *testing.T) {
		db := newTestDB(t)
		repo, err := NewSessionRepository(db, nil)
		if err != nil {
			t.Fatalf("NewSessionRepository(db, nil) error = %v", err)
		}
		if repo == nil {
			t.Fatal("NewSessionRepository(db, nil) returned nil repo")
		}
	})

	t.Run("Create with valid session returns id > 0", func(t *testing.T) {
		db := newTestDB(t)
		repo, _ := NewSessionRepository(db, infra.NewNoOpLogger())

		id, err := repo.Create(context.Background(), Session{
			ConnectionID: "conn-abc",
			State:        StateIdle,
			WordCount:    10,
			Metadata:     `{"key":"value"}`,
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if id <= 0 {
			t.Errorf("Create() returned invalid id: %d, want > 0", id)
		}
	})

	t.Run("Create with empty ConnectionID is rejected", func(t *testing.T) {
		db := newTestDB(t)
		repo, _ := NewSessionRepository(db, infra.NewNoOpLogger())

		_, err := repo.Create(context.Background(), Session{
			ConnectionID: "",
			State:        StateIdle,
		})
		if err == nil {
			t.Fatal("Create with empty ConnectionID expected error, got nil")
		}
		if !errors.Is(err, ErrEmptyConnectionID) {
			t.Errorf("Create() error = %v, want errors.Is ErrEmptyConnectionID", err)
		}
	})

	t.Run("GetByID returns session created by Create", func(t *testing.T) {
		db := newTestDB(t)
		repo, _ := NewSessionRepository(db, infra.NewNoOpLogger())

		created, err := repo.Create(context.Background(), Session{
			ConnectionID: "conn-xyz",
			State:        StateIdle,
			WordCount:    5,
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		got, err := repo.GetByID(context.Background(), created)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if got.ID != created {
			t.Errorf("GetByID().ID = %d, want %d", got.ID, created)
		}
		if got.ConnectionID != "conn-xyz" {
			t.Errorf("GetByID().ConnectionID = %q, want %q", got.ConnectionID, "conn-xyz")
		}
		if got.State != StateIdle {
			t.Errorf("GetByID().State = %q, want %q", got.State, StateIdle)
		}
		if got.WordCount != 5 {
			t.Errorf("GetByID().WordCount = %d, want 5", got.WordCount)
		}
	})

	t.Run("GetByID for missing id returns not-found sentinel", func(t *testing.T) {
		db := newTestDB(t)
		repo, _ := NewSessionRepository(db, infra.NewNoOpLogger())

		_, err := repo.GetByID(context.Background(), 99999)
		if err == nil {
			t.Fatal("GetByID(99999) expected error, got nil")
		}
		if !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("GetByID(99999) error = %v, want errors.Is ErrSessionNotFound", err)
		}
	})

	t.Run("UpdateState transitions session state", func(t *testing.T) {
		db := newTestDB(t)
		repo, _ := NewSessionRepository(db, infra.NewNoOpLogger())

		id, _ := repo.Create(context.Background(), Session{
			ConnectionID: "conn-update",
			State:        StateIdle,
		})

		err := repo.UpdateState(context.Background(), id, StateListening)
		if err != nil {
			t.Fatalf("UpdateState() error = %v", err)
		}

		got, err := repo.GetByID(context.Background(), id)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if got.State != StateListening {
			t.Errorf("GetByID().State = %q, want %q", got.State, StateListening)
		}
	})

	t.Run("UpdateState for missing session returns not-found", func(t *testing.T) {
		db := newTestDB(t)
		repo, _ := NewSessionRepository(db, infra.NewNoOpLogger())

		err := repo.UpdateState(context.Background(), 99999, StateDictating)
		if err == nil {
			t.Fatal("UpdateState(99999, ...) expected error, got nil")
		}
		if !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("UpdateState() error = %v, want errors.Is ErrSessionNotFound", err)
		}
	})

	t.Run("AppendEvent inserts event linked to session", func(t *testing.T) {
		db := newTestDB(t)
		repo, _ := NewSessionRepository(db, infra.NewNoOpLogger())

		id, _ := repo.Create(context.Background(), Session{
			ConnectionID: "conn-event",
			State:        StateIdle,
		})

		err := repo.AppendEvent(context.Background(), SessionEvent{
			SessionID: id,
			EventType: "state_transition",
			Payload:   `{"from":"IDLE","to":"LISTENING"}`,
		})
		if err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}

		// Verify event exists by querying directly
		var count int
		err = db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM session_events WHERE session_id = ? AND event_type = ?",
			id, "state_transition",
		).Scan(&count)
		if err != nil {
			t.Fatalf("query session_events: %v", err)
		}
		if count != 1 {
			t.Errorf("session_events count = %d, want 1", count)
		}
	})

	t.Run("multiple operations on same session", func(t *testing.T) {
		db := newTestDB(t)
		repo, _ := NewSessionRepository(db, infra.NewNoOpLogger())

		id, _ := repo.Create(context.Background(), Session{
			ConnectionID: "conn-multi",
			State:        StateIdle,
		})

		// Transition through states
		states := []SessionState{StateListening, StateDictating, StateWaitingParent, StateFeedback}
		for _, state := range states {
			if err := repo.UpdateState(context.Background(), id, state); err != nil {
				t.Fatalf("UpdateState(%s) error = %v", state, err)
			}
		}

		// Verify final state
		got, err := repo.GetByID(context.Background(), id)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if got.State != StateFeedback {
			t.Errorf("GetByID().State = %q, want %q", got.State, StateFeedback)
		}

		// Append multiple events
		for i := 0; i < 3; i++ {
			if err := repo.AppendEvent(context.Background(), SessionEvent{
				SessionID: id,
				EventType: "test_event",
				Payload:   "test",
			}); err != nil {
				t.Fatalf("AppendEvent() iteration %d error = %v", i, err)
			}
		}

		// Verify events count
		var count int
		err = db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM session_events WHERE session_id = ?", id,
		).Scan(&count)
		if err != nil {
			t.Fatalf("query count: %v", err)
		}
		if count != 3 {
			t.Errorf("events count = %d, want 3", count)
		}
	})
}
