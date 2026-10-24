package rt

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// ensureGateExec wraps the REAL lesson executor and parks the first
// EnsureLessonCards (the classroom-init row write) between the close
// attempt's start and the actual SQLite write: the row/goodbye ordering is
// decided right there, on the real Store.
type ensureGateExec struct {
	LessonExecutor

	closeStarted chan struct{} // the test launched the real close
	attempt      chan struct{} // the write position was reached
	written      chan struct{} // the real write returned
	endedAtWrite func() bool   // whether session_ended existed at write completion
	lockHeld     func() bool   // whether the init-write lock is held (probe)

	mu    sync.Mutex
	ended int32 // session_ended count observed when the gated write returned
	held  int32 // 1 when the init-write lock was held during the write
	calls int
}

func (g *ensureGateExec) EnsureLessonCards(ctx context.Context, lessonID string, cards []lesson.Word, category string) (lesson.Lesson, error) {
	g.mu.Lock()
	g.calls++
	first := g.calls == 1
	g.mu.Unlock()
	if !first {
		return g.LessonExecutor.EnsureLessonCards(ctx, lessonID, cards, category)
	}
	close(g.attempt)
	<-g.closeStarted
	if g.lockHeld != nil {
		atomic.StoreInt32(&g.held, btoi(g.lockHeld()))
	}
	l, err := g.LessonExecutor.EnsureLessonCards(ctx, lessonID, cards, category)
	atomic.StoreInt32(&g.ended, btoi(g.endedAtWrite()))
	close(g.written)
	return l, err
}

func btoi(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

// gatingFetchSource wraps a WordSource whose first FetchDueWords runs the
// real close synchronously mid-fetch: the init proceeds only after the
// session is already closed.
type gatingFetchSource struct {
	WordSource
	closer func()
	done   chan struct{}
	once   sync.Once
}

func (g *gatingFetchSource) FetchDueWords(ctx context.Context, limit int, category string) ([]lesson.Word, error) {
	g.once.Do(func() {
		g.closer()
		close(g.done)
	})
	return g.WordSource.FetchDueWords(ctx, limit, category)
}

// ttRealStoreExec builds the production lesson stack over a temp SQLite DB
// (the same seam main.go wires).
func ttRealStoreExec(t *testing.T) LessonExecutor {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "init-write.db"))
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = lesson.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate temp db: %v", err)
	}
	store, err := lesson.NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	exec, err := lesson.NewExecutor(store, nil)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	tools, err := NewLessonTools(store, exec)
	if err != nil {
		t.Fatalf("NewLessonTools: %v", err)
	}
	return tools
}

// TestInitWriteCloseOrdering pins the init-write lock's contract on the real
// Store: a close that arrives first means no row for the dead session, and a
// row write that started first completes strictly ahead of session_ended.
func TestInitWriteCloseOrdering(t *testing.T) {
	t.Run("a row write that started first finishes ahead of session_ended", func(t *testing.T) {
		captured := &captureSender{}
		g := &ensureGateExec{
			LessonExecutor: ttRealStoreExec(t),
			attempt:        make(chan struct{}),
			closeStarted:   make(chan struct{}),
			written:        make(chan struct{}),
			endedAtWrite:   func() bool { return captured.countOf(TypeSessionEnded) > 0 },
			lockHeld:       nil, // armed below, once tm exists
		}
		tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), g,
			ttConfig(newFakeWords(ttCards("苹果"))), NewEventRecorder(&captureLogger{}, "t"), nil)
		g.lockHeld = func() bool { return !tm.initWriteMu.TryLock() }

		initDone := make(chan struct{})
		go func() {
			tm.HandleControl(captured, mustJSON(t, SessionInitMsg{
				Type: TypeSessionInit, Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
			}))
			close(initDone)
		}()
		ttAwaitGate(t, g.attempt, "row write position was not reached")

		closerDone := make(chan struct{})
		go func() {
			tm.handleEndSession(captured)
			close(closerDone)
		}()
		close(g.closeStarted) // the close attempt runs while the write is in flight
		ttAwaitGate(t, g.written, "real row write did not finish")
		ttAwaitGate(t, closerDone, "close did not finish after the write")
		ttAwaitGate(t, initDone, "init did not finish")

		if got := atomic.LoadInt32(&g.held); got != 1 {
			t.Fatal("the row write ran without holding the init-write lock — a close could reorder against it")
		}
		if got := atomic.LoadInt32(&g.ended); got != 0 {
			t.Fatalf("session_ended count observed at write completion = %d, want 0 (the close must block on the init-write lock)", got)
		}
		g.mu.Lock()
		calls := g.calls
		g.mu.Unlock()
		if calls != 1 {
			t.Fatalf("EnsureLessonCards calls = %d, want exactly 1", calls)
		}
		// The row completed ahead of the goodbye; whether the starter's
		// ready also won its own post-write race is free — but if it
		// shipped, it shipped BEFORE the goodbye, and nothing trails it.
		if got := captured.countOf(TypeSessionReady); got > 1 {
			t.Fatalf("session_ready count = %d, want at most 1", got)
		}
		if got := captured.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want 1", got)
		}
		if captured.countOf(TypeSessionReady) > 0 {
			ttReadyBeforeEnd(t, captured, TypeSessionReady)
		}
		ttAssertNothingAfterEnd(t, captured)
	})

	t.Run("a close that arrived first creates no row", func(t *testing.T) {
		captured := &captureSender{}
		g := &ensureGateExec{
			LessonExecutor: ttRealStoreExec(t),
			attempt:        make(chan struct{}),
			closeStarted:   make(chan struct{}),
			written:        make(chan struct{}),
			endedAtWrite:   func() bool { return false },
		}
		tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), g,
			ttConfig(newFakeWords(ttCards("苹果"))), NewEventRecorder(&captureLogger{}, "t"), nil)
		src := &gatingFetchSource{
			WordSource: newFakeWords(ttCards("苹果")),
			closer:     func() { tm.handleEndSession(captured) },
			done:       make(chan struct{}),
		}
		tm.cfg.Words = src

		ttInitWithSender(t, tm, captured) // the gated fetch closes mid-init, synchronously
		ttAwaitGate(t, src.done, "gated fetch was not reached")

		if got := captured.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want 1", got)
		}
		if got := captured.countOf(TypeSessionReady); got != 0 {
			t.Fatalf("session_ready count = %d, want none", got)
		}
		g.mu.Lock()
		calls := g.calls
		g.mu.Unlock()
		if calls != 0 {
			t.Fatalf("EnsureLessonCards calls = %d, want 0: the dead session must leave no row", calls)
		}
		tm.mu.Lock()
		closed := tm.closed
		tm.mu.Unlock()
		if !closed {
			t.Fatal("session is not closed")
		}
	})
}
