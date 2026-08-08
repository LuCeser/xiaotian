package session

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// trackingRepo implements database.SessionRepository with atomic counters
// so the audit-via-WorkerPool test can assert Pitfall 5: repo.* is never
// called from the event-loop goroutine — only from pool worker goroutines.
type trackingRepo struct {
	creates atomic.Int64
	updates atomic.Int64
	events  atomic.Int64
}

func (r *trackingRepo) Create(ctx context.Context, s database.Session) (int64, error) {
	r.creates.Add(1)
	return 1, nil
}

func (r *trackingRepo) UpdateState(ctx context.Context, id int64, state database.SessionState) error {
	r.updates.Add(1)
	return nil
}

func (r *trackingRepo) AppendEvent(ctx context.Context, e database.SessionEvent) error {
	r.events.Add(1)
	return nil
}

func (r *trackingRepo) GetByID(ctx context.Context, id int64) (database.Session, error) {
	return database.Session{}, nil
}

var _ database.SessionRepository = (*trackingRepo)(nil)

// TestAuditViaWorkerPool proves D-06 (audit writes flow through pool.Submit,
// the first real Submit caller — P1-3 resolved) and Pitfall 5 (the event-loop
// caller NEVER touches repo.* directly; only pool worker goroutines do).
func TestAuditViaWorkerPool(t *testing.T) {
	// Create a real WorkerPool with capacity but DON'T start it yet.
	// Tasks queue in the buffered channel without executing, proving
	// audit dispatches through pool.Submit and the caller never calls repo.* directly.
	pool, err := infra.NewWorkerPool(
		infra.WorkerConfig{PoolSize: 1, QueueSize: 10},
		infra.NewNoOpLogger(),
	)
	if err != nil {
		t.Fatalf("NewWorkerPool: %v", err)
	}

	repo := &trackingRepo{}
	s := &Session{
		pool:      pool,
		repo:      repo,
		connID:    "test-conn",
		dbID:      42,
		wordQueue: []pybridge.DueCard{{Word: "hello"}},
		logger:    infra.NewNoOpLogger(),
	}

	// Dispatch audit operations — these go through pool.Submit, not direct repo calls.
	s.auditCreate()
	s.auditState(database.StateListening)
	s.auditEvent("control_frame")

	// Pitfall 5: the event-loop caller never touches repo.* directly.
	// Tasks are queued but no workers are running, so all counts MUST be 0.
	if c := repo.creates.Load(); c != 0 {
		t.Errorf("repo.Create called %d times before pool worker started (Pitfall 5 violation)", c)
	}
	if c := repo.updates.Load(); c != 0 {
		t.Errorf("repo.UpdateState called %d times before pool worker started (Pitfall 5 violation)", c)
	}
	if c := repo.events.Load(); c != 0 {
		t.Errorf("repo.AppendEvent called %d times before pool worker started (Pitfall 5 violation)", c)
	}

	// Now start the pool — queued tasks execute on worker goroutines.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatalf("pool.Start: %v", err)
	}

	// Give the worker time to drain the queue.
	time.Sleep(100 * time.Millisecond)
	_ = pool.Shutdown(context.Background())

	// After pool processes, repo IS touched (tasks ran on worker goroutines).
	if c := repo.creates.Load(); c != 1 {
		t.Errorf("repo.Create called %d times, want 1", c)
	}
	if c := repo.updates.Load(); c != 1 {
		t.Errorf("repo.UpdateState called %d times, want 1", c)
	}
	if c := repo.events.Load(); c != 1 {
		t.Errorf("repo.AppendEvent called %d times, want 1", c)
	}
}

// TestAuditViaWorkerPool_WorkerPoolFull verifies D-06 best-effort drop:
// when the pool is full, the audit method logs Warn and returns without panicking.
func TestAuditViaWorkerPool_WorkerPoolFull(t *testing.T) {
	// QueueSize=1: fill it with one task, then the next Submit returns ErrWorkerPoolFull.
	pool, err := infra.NewWorkerPool(
		infra.WorkerConfig{PoolSize: 1, QueueSize: 1},
		infra.NewNoOpLogger(),
	)
	if err != nil {
		t.Fatalf("NewWorkerPool: %v", err)
	}

	// Fill the queue (capacity 1). Don't start the pool so the task stays queued.
	_ = pool.Submit(func(ctx context.Context) error { return nil })

	repo := &trackingRepo{}
	s := &Session{
		pool:      pool,
		repo:      repo,
		connID:    "test-conn",
		dbID:      42,
		wordQueue: []pybridge.DueCard{{Word: "hello"}},
		logger:    infra.NewNoOpLogger(),
	}

	// This Submit should get ErrWorkerPoolFull — auditCreate logs Warn, drops, no panic.
	s.auditCreate()

	if c := repo.creates.Load(); c != 0 {
		t.Errorf("repo.Create called %d times, want 0 (pool full, audit dropped)", c)
	}
}
