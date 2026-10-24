//go:build !unix

package e2ecorpus

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCacheDirLockReturnsUnsupportedWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	called := false
	result := make(chan error, 1)
	go func() {
		result <- withCacheDirLock(context.Background(), dir, func() error {
			called = true
			return nil
		})
	}()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("withCacheDirLock() error = %v, want explicit unsupported error", err)
		}
		if called {
			t.Fatal("withCacheDirLock() called the protected function without a supported lock")
		}
	case <-time.After(time.Second):
		t.Fatal("withCacheDirLock() blocked instead of returning unsupported")
	}
}
