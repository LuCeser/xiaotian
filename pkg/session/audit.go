package session

import (
	"context"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// auditCreate submits the Session's initial audit row (database.Session with
// StateIdle) through the WorkerPool (D-06 fire-and-forget). On ErrWorkerPoolFull
// the row is dropped (logged at Warn) — the in-memory Session is the authority
// and the audit is a best-effort Phase-10 replay trail (Pitfall 5: the event
// loop NEVER calls repo.* directly, only through these wrappers).
//
// The returned dbID from repo.Create is not captured back into s.dbID because
// the Task runs asynchronously on a worker goroutine; writing to a field read
// by the event loop would violate the single-writer discipline (D-02). The
// dbID stays 0 for Phase 7; a future phase may add a synchronous init-time
// Create or a channel-based write-back if accurate dbID becomes necessary.
func (s *Session) auditCreate() {
	if s.pool == nil {
		return
	}
	if err := s.pool.Submit(func(ctx context.Context) error {
		_, err := s.repo.Create(ctx, database.Session{
			ConnectionID: s.connID,
			State:        database.StateIdle,
			WordCount:    len(s.wordQueue),
		})
		return err
	}); err != nil {
		s.logger.Warn("session: audit create dropped (pool full)",
			infra.F("component", "session"),
			infra.F("conn_id", s.connID),
			infra.ErrField(err),
		)
	}
}

// auditState submits a state-change audit row via the WorkerPool (D-06). Called
// by the event-loop transition wrapper (Plan 02) on every valid FSM transition.
func (s *Session) auditState(newState database.SessionState) {
	if s.pool == nil {
		return
	}
	if err := s.pool.Submit(func(ctx context.Context) error {
		return s.repo.UpdateState(ctx, s.dbID, newState)
	}); err != nil {
		s.logger.Warn("session: audit state dropped (pool full)",
			infra.F("component", "session"),
			infra.F("conn_id", s.connID),
			infra.F("new_state", newState),
			infra.ErrField(err),
		)
	}
}

// auditEvent submits a session-event audit row via the WorkerPool (D-06).
// Plan 03 extends the signature with a payload parameter; for Plan 01 the
// eventType alone is recorded.
func (s *Session) auditEvent(eventType string) {
	if s.pool == nil {
		return
	}
	if err := s.pool.Submit(func(ctx context.Context) error {
		return s.repo.AppendEvent(ctx, database.SessionEvent{
			SessionID: s.dbID,
			EventType: eventType,
		})
	}); err != nil {
		s.logger.Warn("session: audit event dropped (pool full)",
			infra.F("component", "session"),
			infra.F("conn_id", s.connID),
			infra.F("event_type", eventType),
			infra.ErrField(err),
		)
	}
}
