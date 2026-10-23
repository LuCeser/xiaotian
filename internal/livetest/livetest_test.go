package livetest

import (
	"strings"
	"testing"
)

// fakeKeyEnv is a placeholder credential variable so the tests never touch
// the real MIMO_API_KEY (present on dev machines) and never gate on anything
// but the switch logic itself.
const fakeKeyEnv = "LIVETEST_FAKE_KEY"

func TestEnabled(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"yes", false}, // not a ParseBool value — strict on purpose
		{"garbage", false},
		{"1", true},
		{"true", true},
		{"T", true},
	}
	for _, tc := range cases {
		t.Setenv(SwitchEnv, tc.value)
		if got := Enabled(); got != tc.want {
			t.Errorf("Enabled() with %s=%q = %v, want %v", SwitchEnv, tc.value, got, tc.want)
		}
	}
}

func TestGate(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		wantInMsg string
		short     bool
		on        bool
		wantRun   bool
	}{
		{name: "short mode always skips", short: true, on: true, key: "k", wantInMsg: "-short"},
		{name: "switch off skips even with key", short: false, on: false, key: "k", wantInMsg: SwitchEnv},
		{name: "switch on without key skips for credentials", short: false, on: true, key: "", wantInMsg: fakeKeyEnv},
		{name: "switch on with key runs", short: false, on: true, key: "k", wantRun: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := gate(tc.short, tc.on, tc.key, fakeKeyEnv)
			if tc.wantRun {
				if reason != "" {
					t.Fatalf("gate() = %q, want run", reason)
				}
				return
			}
			if reason == "" {
				t.Fatal("gate() allowed the test to run, want a skip reason")
			}
			if !strings.Contains(reason, tc.wantInMsg) {
				t.Errorf("gate() = %q, want it to mention %q", reason, tc.wantInMsg)
			}
		})
	}
}

// TestRequireKey_RunsWhenEnabled covers the happy path end to end: switch on
// plus a set credential returns the credential value.
func TestRequireKey_RunsWhenEnabled(t *testing.T) {
	t.Setenv(SwitchEnv, "1")
	t.Setenv(fakeKeyEnv, "secret-value")
	if got := RequireKey(t, fakeKeyEnv); got != "secret-value" {
		t.Fatalf("RequireKey() = %q, want the credential value", got)
	}
}

// TestRequireKey_SkipsWhenDisabled asserts the guard actually skips: if the
// skip were missing, the Fatal line below would fail the test.
func TestRequireKey_SkipsWhenDisabled(t *testing.T) {
	t.Setenv(SwitchEnv, "0")
	t.Setenv(fakeKeyEnv, "present-but-irrelevant")
	RequireKey(t, fakeKeyEnv)
	t.Fatal("RequireKey should have skipped while live tests are disabled")
}

// TestRequireKey_SkipsWithoutKey: enabled but no credential → skip, not fail.
func TestRequireKey_SkipsWithoutKey(t *testing.T) {
	t.Setenv(SwitchEnv, "1")
	t.Setenv(fakeKeyEnv, "")
	RequireKey(t, fakeKeyEnv)
	t.Fatal("RequireKey should have skipped when the credential is missing")
}
