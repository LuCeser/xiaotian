package pybridge

import "context"

// semaphore is a capacity-N token channel implementing the D-02 global
// bounded semaphore. It caps the number of simultaneously running Python
// subprocesses so 50-100 concurrent sessions cannot fork-bomb the 1C/1G
// VPS (RESEARCH Pattern 5 / Pitfall 7). The cap N is sourced from
// PythonConfig.MaxConcurrent at Bridge construction (Plan 03-03).
//
// This is the blocking variant of worker.go's Submit select idiom: where
// Submit returns ErrWorkerPoolFull immediately on a full queue (D-72
// non-blocking backpressure), semaphore.acquire BLOCKS until either a
// slot frees or ctx expires. The bridge plumbs acquire/release around
// each subprocess call so excess callers queue rather than fail.
//
// Unexported because this is an internal concurrency primitive of the
// bridge; external callers interact with it indirectly via Executor.
type semaphore chan struct{}

// newSemaphore constructs a semaphore with the supplied capacity.
//
// n MUST be > 0. A non-positive capacity would make acquire block
// forever (nothing could ever release a token), so the constructor
// panics on a bad n -- this is a programmer error, not a runtime
// condition, and PythonConfig.Validate already enforces MaxConcurrent
// in [1, 4] upstream. The panic is the defensive second line.
func newSemaphore(n int) semaphore {
	if n <= 0 {
		panic("pybridge: semaphore capacity must be > 0")
	}
	return make(semaphore, n)
}

// acquire blocks until a slot is available or ctx expires. On ctx
// expiry it returns ctx.Err() (context.DeadlineExceeded or
// context.Canceled) so the caller can translate the wait into a
// backpressure signal or client-facing error.
//
// If ctx is ALREADY cancelled at the call site, acquire fails fast and
// returns ctx.Err() WITHOUT consuming a slot. Without the explicit
// fast-path check below, an already-done ctx and a free slot would race
// in the blocking select -- Go picks either branch non-deterministically,
// so acquire would sometimes succeed on a cancelled ctx, which violates
// the Go "ctx already done => no side effect" convention.
//
// Every successful acquire MUST be paired with exactly one release,
// typically via `defer sem.release()` immediately after acquire returns
// nil. Failing to release leaks a slot permanently.
//
// The select idiom mirrors pkg/infra/worker.go Submit (lines 228-240),
// but with the default branch replaced by <-ctx.Done() so callers queue
// instead of being rejected immediately (RESEARCH Pattern 5).
func (s semaphore) acquire(ctx context.Context) error {
	// Fast-path: honour an already-done ctx before racing on a slot.
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	select {
	case s <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release returns one slot to the pool. Calling release on a semaphore
// with no in-flight acquires blocks forever -- it would be a programmer
// error (release without a matching acquire); the bridge always pairs
// them with defer.
func (s semaphore) release() {
	<-s
}
