package pybridge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// These tests cover the D-02 global bounded semaphore declared in
// 03-02-PLAN.md Task 2. Mirrors worker_test.go's waitOrTimeout idiom
// (no testify/mock -- D-48 manual fakes and stdlib testing only).
//
// Contract under test:
//   - newSemaphore(n) creates a semaphore with n slots
//   - acquire blocks until a slot is free or ctx expires
//   - on ctx expiry acquire returns ctx.Err() (DeadlineExceeded or
//     Canceled)
//   - release returns a slot, unblocking a queued acquire
//   - newSemaphore(<=0) panics (programmer error, not runtime)

// TestSemaphoreAcquireRelease proves the basic slot accounting: with N=2
// the first two acquires succeed immediately, the third blocks until a
// release OR its ctx expires.
func TestSemaphoreAcquireRelease(t *testing.T) {
	sem := newSemaphore(2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sem.acquire(ctx); err != nil {
		t.Fatalf("acquire[1]: %v", err)
	}
	if err := sem.acquire(ctx); err != nil {
		t.Fatalf("acquire[2]: %v", err)
	}

	// Third acquire must block -- give it a short ctx so it fails fast.
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer shortCancel()
	start := time.Now()
	err := sem.acquire(shortCtx)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire[3] on full semaphore: got %v, want context.DeadlineExceeded", err)
	}
	// The blocked acquire MUST have waited for (close to) the ctx
	// timeout, not returned immediately -- that is the contract
	// difference vs worker.go Submit's non-blocking default branch.
	if elapsed < 30*time.Millisecond {
		t.Errorf("acquire[3] returned after %v, want it to block >= 30ms", elapsed)
	}

	// Release one slot; the next acquire should now succeed promptly.
	sem.release()
	if err := sem.acquire(ctx); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}

	// Clean up the held slots.
	sem.release()
	sem.release()
}

// TestSemaphoreAcquireBlockedThenReleased proves a blocked acquire is
// unblocked when a peer releases its slot -- the queue ordering is FIFO
// on the channel send buffer.
func TestSemaphoreAcquireBlockedThenReleased(t *testing.T) {
	sem := newSemaphore(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sem.acquire(ctx); err != nil {
		t.Fatalf("initial acquire: %v", err)
	}

	var acquired atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := sem.acquire(ctx); err != nil {
			t.Errorf("blocked acquire: %v", err)
			return
		}
		acquired.Store(true)
		sem.release()
	}()

	// Give the blocked acquire a moment to park on the channel.
	time.Sleep(20 * time.Millisecond)
	if acquired.Load() {
		t.Fatalf("blocked acquire returned before release")
	}

	sem.release()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("blocked acquire did not complete within 500ms of release")
	}
	if !acquired.Load() {
		t.Errorf("blocked acquire never succeeded after release")
	}
}

// TestSemaphoreAcquireOnCancelledCtx proves acquire honours an
// already-cancelled ctx and returns context.Canceled WITHOUT consuming
// a slot -- a subsequent live acquire must still succeed immediately.
func TestSemaphoreAcquireOnCancelledCtx(t *testing.T) {
	sem := newSemaphore(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sem.acquire(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("acquire on cancelled ctx: got %v, want context.Canceled", err)
	}

	// Slot must still be available: a fresh acquire on a live ctx
	// should succeed immediately.
	liveCtx, liveCancel := context.WithCancel(context.Background())
	defer liveCancel()
	if err := sem.acquire(liveCtx); err != nil {
		t.Errorf("acquire after cancelled attempt: %v", err)
	}
	sem.release()
}

// TestSemaphoreZeroCapPanics proves the defensive panic on a non-positive
// capacity. A zero-cap semaphore would block every acquire forever
// (nothing can release a token), so the constructor treats it as a
// programmer error rather than producing a silently-broken primitive.
func TestSemaphoreZeroCapPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("newSemaphore(0): expected panic, got none")
		}
	}()
	_ = newSemaphore(0)
}

// TestSemaphoreNegativeCapPanics is the same defensive contract for n<0.
func TestSemaphoreNegativeCapPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("newSemaphore(-1): expected panic, got none")
		}
	}()
	_ = newSemaphore(-1)
}

// TestSemaphoreConcurrentAcquirers proves N+M concurrent acquirers
// against a cap-N semaphore never panic, never deadlock, and never
// exceed N in-flight -- race-clean under `go test -race`.
func TestSemaphoreConcurrentAcquirers(t *testing.T) {
	const cap = 2
	const total = 8
	sem := newSemaphore(cap)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var inFlight, maxInFlight atomic.Int32
	release := make(chan struct{})
	done := make(chan error, total)

	for i := 0; i < total; i++ {
		go func() {
			if err := sem.acquire(ctx); err != nil {
				done <- err
				return
			}
			cur := inFlight.Add(1)
			for {
				old := maxInFlight.Load()
				if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
					break
				}
			}
			<-release
			inFlight.Add(-1)
			sem.release()
			done <- nil
		}()
	}

	// Let the acquirers park on the semaphore.
	time.Sleep(30 * time.Millisecond)
	close(release)

	for i := 0; i < total; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("acquirer %d: %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("acquirer %d did not complete within 2s (deadlock?)", i)
		}
	}

	if got := maxInFlight.Load(); got > cap {
		t.Errorf("max in-flight: got %d, want <= %d (cap exceeded)", got, cap)
	}
}
