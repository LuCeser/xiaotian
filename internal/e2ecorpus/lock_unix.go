//go:build unix

package e2ecorpus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockPollInterval is how often a blocked prepare run retries the advisory
// lock; each retry also re-checks ctx so a stuck holder cannot wedge the
// command past its timeout.
const lockPollInterval = 100 * time.Millisecond

// lockCacheFile takes an exclusive flock on the open lock file, waiting in
// non-blocking retries until it is free or ctx is done. flock is per open
// file description, so it excludes other goroutines and other processes
// alike, and the kernel releases it even if the holder crashes.
func lockCacheFile(ctx context.Context, f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("e2ecorpus: lock %s: %w", cacheLockName, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("e2ecorpus: another prepare run holds %s: %w", cacheLockName, ctx.Err())
		case <-time.After(lockPollInterval):
		}
	}
}

// unlockCacheFile releases the lock; on a stale fd the close that follows
// releases it anyway, so the error is advisory only.
func unlockCacheFile(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("e2ecorpus: unlock %s: %w", cacheLockName, err)
	}
	return nil
}
