//go:build !unix

package e2ecorpus

import (
	"context"
	"fmt"
	"os"
)

// lockCacheFile refuses to run on platforms without the flock primitive:
// silently proceeding would reintroduce the concurrent-publish corruption
// the lock exists to prevent.
func lockCacheFile(context.Context, *os.File) error {
	return fmt.Errorf("e2ecorpus: cross-process %s locking is unsupported on this platform", cacheLockName)
}

func unlockCacheFile(*os.File) error { return nil }
