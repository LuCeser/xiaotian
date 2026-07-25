//go:build linux

// Subprocess integration tests for the Linux-only process-group kill path.
// Behind //go:build linux so Windows/macOS dev hosts skip these (they have no
// sh/kill semantics; they run only the fake-driven unit tests in
// bridge_test.go). The integration test uses only POSIX utilities guaranteed
// to exist on a Linux VPS (sh, sleep) -- no Python interpreter required.
package pybridge

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRunProcessGroupKill drives the Bridge.run Cancel hook with a child
// that spawns its own grandchild (simulating pregenerate_dictation.py
// spawning ffmpeg), then asserts:
//
//   - run returns well within the WaitDelay + timeout budget (proving the
//     process group was actually killed, not left to time out alone)
//   - the returned error matches context.DeadlineExceeded (the parent ctx
//     layer fired)
//
// The grandchild is `sleep 30`. Without setProcessGroup's SIGKILL(-pgid),
// the orphaned sleep would keep its inherited stdout pipe open and cmd.Wait
// would block for the full WaitDelay (3s) instead of returning promptly;
// this test would then flake on the timing assertion.
//
// Uses only `sh -c "sleep 30 & sleep 30"` so the test does not require a
// Python interpreter to exercise the bridge's process-management layer. The
// script name passed to run() is /bin/sh, with the rest of the command as
// argv elements -- the same argv-only shape Bridge uses for real Python
// invocations (T-03-07: argv slice, no shell-string concatenation).
func TestRunProcessGroupKill(t *testing.T) {
	b := &Bridge{
		cfg:    validTestCfg(),
		logger: newFakeLogger(),
		sem:    newSemaphore(1),
	}

	// 200ms per-call timeout. The grandchild sleeps 30s, so the timeout
	// always fires; the question is whether Cancel kills the whole group
	// quickly enough that run returns in <2s (well under the 3s WaitDelay
	// backstop).
	const timeout = 200 * time.Millisecond
	const slack = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	_, _, _, err := b.run(ctx, timeout, "/bin/sh", "-c", "sleep 30 & sleep 30")
	elapsed := time.Since(start)

	if elapsed > timeout+slack {
		t.Errorf("run took %v, want <%v (timeout+slack); process-group kill did not fire promptly",
			elapsed, timeout+slack)
	}
	if err == nil {
		t.Fatalf("run returned nil error, want context.DeadlineExceeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("run error = %v, want errors.Is(err, context.DeadlineExceeded)=true", err)
	}
}
