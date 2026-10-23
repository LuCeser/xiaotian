//go:build unix

package e2ecorpus

import (
	"context"
	"testing"
	"time"
)

// TestCacheDirLockExcludesConcurrentHolder: while one holder runs under the
// cache lock, a second acquirer must block until the first releases — this
// is the cross-process mutual exclusion that makes the whole decide→
// synthesize→publish sequence atomic between concurrent prepare runs
// (PR #63 review：判定缺失之前取得跨进程锁).
func TestCacheDirLockExcludesConcurrentHolder(t *testing.T) {
	dir := t.TempDir()
	inside := make(chan struct{})
	release := make(chan struct{})
	go func() {
		err := withCacheDirLock(context.Background(), dir, func() error {
			close(inside)
			<-release
			return nil
		})
		if err != nil {
			t.Errorf("first holder: %v", err)
		}
	}()
	<-inside // the first holder now holds the lock

	acquired := make(chan error, 1)
	go func() {
		acquired <- withCacheDirLock(context.Background(), dir, func() error { return nil })
	}()
	select {
	case err := <-acquired:
		t.Fatalf("second holder acquired the lock while the first still held it: %v", err)
	case <-time.After(100 * time.Millisecond):
		// still waiting — the exclusion holds
	}

	close(release)
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second holder failed after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second holder still blocked after the first released")
	}
}

// TestCacheDirLockRespectsContext: a waiter must give up when its context is
// cancelled instead of blocking forever on a stuck holder.
func TestCacheDirLockRespectsContext(t *testing.T) {
	dir := t.TempDir()
	inside := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = withCacheDirLock(context.Background(), dir, func() error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	acquired := make(chan error, 1)
	go func() {
		acquired <- withCacheDirLock(ctx, dir, func() error { return nil })
	}()
	time.Sleep(50 * time.Millisecond) // let the waiter enter its lock poll
	cancel()
	select {
	case err := <-acquired:
		if err == nil {
			t.Fatal("waiter proceeded despite a cancelled context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter ignored the cancelled context")
	}
}
