package pybridge

import (
	"errors"
	"strings"
	"testing"
)

// These tests cover the pybridge contract layer declared in 03-02-PLAN.md
// Task 1:
//   - ExitError.Error() format is stable for log scrapers
//   - ExitError.Unwrap() makes errors.Is work through the wrapping
//   - every sentinel error has non-empty text terminated by a period
//     (D-04 -- callers detect via errors.Is, never by string matching,
//     but the period convention matches worker.go so log lines read
//     consistently across packages).
//
// Only Go stdlib testing (D-48 -- no testify/mock). The Bridge
// compile-time interface check (var _ Executor = (*Bridge)(nil)) is
// deferred to Plan 03-03 where Bridge is defined.

// TestExitErrorError verifies the Error() format is the documented
// `python command "<cmd>" exited <code>: <stderr>` shape. Log scrapers
// depend on this stability.
func TestExitErrorError(t *testing.T) {
	e := &ExitError{
		Cmd:      "vocab_manager.py --db /tmp/v.db due --limit 5",
		ExitCode: 2,
		Stderr:   "usage: vocab_manager.py due ...",
	}
	got := e.Error()
	want := `python command "vocab_manager.py --db /tmp/v.db due --limit 5" exited 2: usage: vocab_manager.py due ...`
	if got != want {
		t.Errorf("Error():\n got: %s\nwant: %s", got, want)
	}
}

// TestExitErrorErrorEmptyStderr verifies the format degrades gracefully
// when the subprocess wrote nothing to stderr (e.g. killed by signal
// before flushing). The trailing ": " is preserved so log scrapers can
// still split on the colon.
func TestExitErrorErrorEmptyStderr(t *testing.T) {
	e := &ExitError{
		Cmd:      "pregenerate_dictation.py generate",
		ExitCode: 137, // SIGKILL
		Stderr:   "",
	}
	got := e.Error()
	want := `python command "pregenerate_dictation.py generate" exited 137: `
	if got != want {
		t.Errorf("Error():\n got: %s\nwant: %s", got, want)
	}
}

// TestExitErrorUnwrap proves errors.Is works through ExitError wrapping
// (D-04). Callers can both errors.Is(err, ErrPythonExit) and
// errors.As(err, &exitErr) to recover the structured fields.
func TestExitErrorUnwrap(t *testing.T) {
	e := &ExitError{
		Cmd:      "due",
		ExitCode: 1,
		Stderr:   "boom",
		Err:      ErrPythonExit,
	}
	if !errors.Is(e, ErrPythonExit) {
		t.Errorf("errors.Is(e, ErrPythonExit): got false, want true")
	}
	if unwrapped := errors.Unwrap(e); !errors.Is(unwrapped, ErrPythonExit) {
		t.Errorf("errors.Unwrap: got %v, want ErrPythonExit", unwrapped)
	}
	var asTarget *ExitError
	if !errors.As(e, &asTarget) {
		t.Errorf("errors.As(e, &ExitError): got false, want true")
	}
	if asTarget == nil {
		t.Fatalf("errors.As target is nil")
	}
	if asTarget.ExitCode != 1 {
		t.Errorf("asTarget.ExitCode: got %d, want 1", asTarget.ExitCode)
	}
	if asTarget.Cmd != "due" {
		t.Errorf("asTarget.Cmd: got %q, want %q", asTarget.Cmd, "due")
	}
}

// TestSentinelErrors verifies each sentinel error's text is non-empty and
// does not end with punctuation (ST1005). The text is not load-bearing for
// detection (callers use errors.Is).
func TestSentinelErrors(t *testing.T) {
	cases := []struct {
		err  error
		name string
	}{
		{ErrPythonExit, "ErrPythonExit"},
		{ErrPythonTimeout, "ErrPythonTimeout"},
		{ErrPythonJSONParse, "ErrPythonJSONParse"},
		{ErrPythonEmpty, "ErrPythonEmpty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			if msg == "" {
				t.Errorf("%s.Error(): empty message", tc.name)
			}
			if strings.HasSuffix(msg, ".") || strings.HasSuffix(msg, "\n") {
				t.Errorf("%s.Error(): %q must not end with punctuation", tc.name, msg)
			}
			// Each sentinel MUST be distinguishable via errors.Is so
			// callers can branch on classification without parsing text.
			if !errors.Is(tc.err, tc.err) {
				t.Errorf("%s: errors.Is self-check failed", tc.name)
			}
		})
	}
}

// TestSentinelErrorsAreDistinct proves no two sentinels alias each other
// -- a regression guard against an accidental reassignment that would
// collapse two classifications into one.
func TestSentinelErrorsAreDistinct(t *testing.T) {
	all := []error{ErrPythonExit, ErrPythonTimeout, ErrPythonJSONParse, ErrPythonEmpty}
	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Errorf("sentinel[%d] and sentinel[%d] alias each other", i, j)
			}
		}
	}
}
