// Package livetest is the single explicit gate for tests that call real
// external providers (MIMO today). A key being present on the machine is NOT
// permission to spend quota: live tests run only when SwitchEnv explicitly
// turns them on, and even then a missing credential is a skip, not a failure.
// The gate never reads .env implicitly — credentials come from the real
// environment (CI secrets, workflow dispatch, manual export) only.
package livetest

import (
	"os"
	"strconv"
	"testing"
)

// SwitchEnv is the environment variable that explicitly enables real-provider
// tests. Any value strconv.ParseBool accepts as true ("1", "true", "T", ...)
// enables them; unset, empty or false keeps every live test skipped.
const SwitchEnv = "XIAOTIAN_LIVE_TESTS"

// KeyEnv is the environment variable holding the MIMO API key. Tests must
// never log or echo its value.
const KeyEnv = "MIMO_API_KEY"

// Enabled reports whether live tests are explicitly switched on.
func Enabled() bool {
	on, err := strconv.ParseBool(os.Getenv(SwitchEnv))
	return err == nil && on
}

// RequireKey gates a live test on the explicit switch plus a non-empty
// credential, skipping with an actionable reason otherwise, and returns the
// key when the test may run.
func RequireKey(t testing.TB, keyEnv string) string {
	t.Helper()
	if reason := gate(testing.Short(), Enabled(), os.Getenv(keyEnv), keyEnv); reason != "" {
		t.Skip(reason)
		return ""
	}
	return os.Getenv(keyEnv)
}

// gate is the pure skip decision behind RequireKey: "" means the test may
// run. Key values never leak into the reason.
func gate(short, on bool, key, keyEnv string) string {
	switch {
	case short:
		return "live test requires real services; skipped in -short mode"
	case !on:
		return "live tests disabled: set " + SwitchEnv + "=1 to enable real-provider calls (they cost quota)"
	case key == "":
		return SwitchEnv + "=1 but " + keyEnv + " is not set — live test skipped (missing credentials)"
	default:
		return ""
	}
}
