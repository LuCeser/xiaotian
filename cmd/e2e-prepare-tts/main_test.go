package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/internal/livetest"
)

// TestSkipReason pins the command's gate to the same rule table as the live
// tests: no explicit switch → no synthesis even with a key on the machine;
// switch on but no credential → skip with the reason. Values are never
// echoed by these messages.
func TestSkipReason(t *testing.T) {
	t.Run("switch off skips despite key", func(t *testing.T) {
		t.Setenv(livetest.SwitchEnv, "0")
		t.Setenv(livetest.KeyEnv, "some-key-value")
		msg := skipReason()
		if !strings.Contains(msg, livetest.SwitchEnv) {
			t.Errorf("message %q does not tell how to enable", msg)
		}
	})

	t.Run("switch on without key skips", func(t *testing.T) {
		t.Setenv(livetest.SwitchEnv, "1")
		t.Setenv(livetest.KeyEnv, "")
		msg := skipReason()
		if !strings.Contains(msg, livetest.KeyEnv) {
			t.Errorf("message %q does not name the missing credential", msg)
		}
	})

	t.Run("switch on with key runs", func(t *testing.T) {
		t.Setenv(livetest.SwitchEnv, "1")
		t.Setenv(livetest.KeyEnv, "some-key-value")
		if msg := skipReason(); msg != "" {
			t.Errorf("skipReason() = %q, want empty", msg)
		}
	})
}

func TestResolveCacheDir(t *testing.T) {
	t.Run("flag wins", func(t *testing.T) {
		t.Setenv("XIAOTIAN_E2E_AUDIO_DIR", "/from/env")
		if got, err := resolveCacheDir("/from/flag"); err != nil || got != "/from/flag" {
			t.Fatalf("resolveCacheDir(flag) = %q, %v; want /from/flag", got, err)
		}
	})

	t.Run("env fallback", func(t *testing.T) {
		t.Setenv("XIAOTIAN_E2E_AUDIO_DIR", "/from/env")
		if got, err := resolveCacheDir(""); err != nil || got != "/from/env" {
			t.Fatalf("resolveCacheDir(env) = %q, %v; want /from/env", got, err)
		}
	})

	t.Run("user cache default stays outside the repo", func(t *testing.T) {
		t.Setenv("XIAOTIAN_E2E_AUDIO_DIR", "")
		got, err := resolveCacheDir("")
		if err != nil {
			t.Fatalf("resolveCacheDir(default): %v", err)
		}
		if !filepath.IsAbs(got) {
			t.Fatalf("default cache dir %q is not an absolute outside-repo path", got)
		}
		if !strings.Contains(got, "xiaotian") {
			t.Fatalf("default cache dir %q does not live under a xiaotian dir", got)
		}
	})
}
