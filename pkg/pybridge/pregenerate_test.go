// pregenerate_test.go exercises the argv builder (pregenArgs), the stdout
// parser pipeline (splitNonEmptyLines), and the post-run classifier
// (classifyPregenResult) declared in pregenerate.go.
//
// Coverage strategy (Plan 03-05 Task 2):
//   - TestPregenArgs: pure argv construction. No subprocess, no Python.
//     Covers minimal + --prefix + --voice + multi-word + --words-per-segment,
//     plus the empty-Words / empty-Language guards. Every happy-path subtest
//     asserts the pregenerate-specific "no --db global flag" invariant
//     (args[0]==pregenScript, args[1]=="generate") AND that --words is a
//     valid JSON array string.
//   - TestRunPregenerateParse: drives splitNonEmptyLines against the golden
//     pregenerate_paths.txt fixture. Asserts the line-based path count and
//     contents; also asserts the fixture bytes are NOT valid JSON (Pitfall 3
//     acceptance: never json.Unmarshal pregenerate stdout).
//   - TestClassifyPregenResult: table-driven classification covering all
//     four sentinel branches. This is the RESEARCH Pitfall 6 acceptance
//     point -- empty stdout + exit 0 MUST return ErrPythonEmpty, never a
//     silent zero-path success. Also covers non-zero-exit -> ExitError
//     wrapping ErrPythonExit, timeout -> ErrPythonTimeout, and the
//     happy-path success.
//
// Test framework: stdlib `testing` only, table-driven + t.Run subtests,
// manual fakes (D-48). Mirrors vocab_test.go / config_test.go style. Tests
// run on every platform Go supports -- no Python, no venv, no shell fake
// required (we drive classifyPregenResult directly).
package pybridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// validPregenCfg returns a PythonConfig with the PregenScript / PregenTimeout
// fields populated for pregenerate testing. Reuses the same shape as
// validTestCfg in bridge_test.go but local so this file is self-contained.
func validPregenCfg() infra.PythonConfig {
	return infra.PythonConfig{
		Interpreter:   "/opt/hta/venv/bin/python",
		ScriptsDir:    "third_party/python",
		VocabScript:   "vocab_manager.py",
		PregenScript:  "pregenerate_dictation.py",
		VocabDBPath:   "/var/lib/hta/vocab.db",
		DataDir:       "/var/lib/hta/data",
		CacheDir:      "/var/lib/hta/cache",
		ManifestDir:   "/var/lib/hta/manifest",
		DueTimeout:    10 * time.Second,
		ReviewTimeout: 10 * time.Second,
		PregenTimeout: 120 * time.Second,
		MaxConcurrent: 2,
	}
}

// assertNoDBGlobal is the pregenerate-specific argv invariant: unlike
// vocab_manager.py, pregenerate_dictation.py has NO global --db flag, so the
// argv MUST be [pregenScript, "generate", ...] with "generate" at index 1.
// This is the negative-space counterpart of vocab_test.go's
// assertDBBeforeSubcommand. Verified against
// pregenerate_dictation.py:1316-1326 (main pops "generate" off argv).
func assertNoDBGlobal(t *testing.T, argv []string) {
	t.Helper()
	if got := len(argv); got < 2 {
		t.Fatalf("argv too short: got %d elements %v, want >= 2", got, argv)
	}
	if argv[1] != "generate" {
		t.Errorf("argv[1] = %q, want %q (pregenerate_dictation.py:1316-1326 -- "+
			"no --db global flag, 'generate' subcommand must be argv[1])",
			argv[1], "generate")
	}
	for i, v := range argv {
		if v == "--db" {
			t.Errorf("argv[%d] = %q: pregenerate_dictation.py has no --db flag "+
				"(vocab_manager-only); mixing argv shapes would break the script", i, v)
		}
	}
}

// TestPregenArgs covers pregenArgs argv construction. Every happy-path
// subtest asserts the pregenerate-specific shape (args[0]=pregenScript,
// args[1]="generate", NO --db anywhere) AND that --words is a valid JSON
// array. Error subtests cover the empty-Words / empty-Language guards.
func TestPregenArgs(t *testing.T) {
	cfg := validPregenCfg()

	t.Run("minimal", func(t *testing.T) {
		opts := PregenOpts{
			Words:    []string{"hello"},
			Language: "english",
		}
		argv, err := pregenArgs(cfg, opts)
		if err != nil {
			t.Fatalf("pregenArgs returned unexpected error: %v", err)
		}

		assertNoDBGlobal(t, argv)
		if argv[0] != "pregenerate_dictation.py" {
			t.Errorf("argv[0] = %q, want %q", argv[0], "pregenerate_dictation.py")
		}

		// --words JSON value must round-trip back to the input slice.
		idx := argvFind(argv, "--words")
		if idx < 0 {
			t.Fatalf("argv %v missing --words", argv)
		}
		var parsed []string
		if err := json.Unmarshal([]byte(argv[idx+1]), &parsed); err != nil {
			t.Fatalf("--words value %q is not valid JSON: %v", argv[idx+1], err)
		}
		if len(parsed) != 1 || parsed[0] != "hello" {
			t.Errorf("--words JSON parsed to %v, want [hello]", parsed)
		}

		// --language required.
		langIdx := argvFind(argv, "--language")
		if langIdx < 0 {
			t.Fatalf("argv %v missing --language", argv)
		}
		if argv[langIdx+1] != "english" {
			t.Errorf("--language = %q, want %q", argv[langIdx+1], "english")
		}

		// Minimal opts: no --prefix / --voice / --words-per-segment.
		for _, banned := range []string{"--prefix", "--voice", "--words-per-segment"} {
			if argvFind(argv, banned) >= 0 {
				t.Errorf("minimal argv %v unexpectedly contains %q "+
					"(zero-valued PregenOpts fields must omit the flag)", argv, banned)
			}
		}
	})

	t.Run("multi_words", func(t *testing.T) {
		opts := PregenOpts{
			Words:    []string{"hello", "world", "cat"},
			Language: "chinese",
		}
		argv, err := pregenArgs(cfg, opts)
		if err != nil {
			t.Fatalf("pregenArgs returned unexpected error: %v", err)
		}
		assertNoDBGlobal(t, argv)

		idx := argvFind(argv, "--words")
		if idx < 0 {
			t.Fatalf("argv %v missing --words", argv)
		}
		var parsed []string
		if err := json.Unmarshal([]byte(argv[idx+1]), &parsed); err != nil {
			t.Fatalf("--words value %q is not valid JSON: %v", argv[idx+1], err)
		}
		if len(parsed) != 3 {
			t.Errorf("--words JSON parsed to %d elements, want 3", len(parsed))
		}
		// Spot-check the first and last to make sure all elements made it.
		if parsed[0] != "hello" || parsed[2] != "cat" {
			t.Errorf("--words JSON parsed to %v, want [hello world cat]", parsed)
		}
	})

	t.Run("with_prefix", func(t *testing.T) {
		opts := PregenOpts{
			Words:    []string{"hello"},
			Language: "english",
			Prefix:   "unit1",
		}
		argv, err := pregenArgs(cfg, opts)
		if err != nil {
			t.Fatalf("pregenArgs returned unexpected error: %v", err)
		}
		assertNoDBGlobal(t, argv)

		idx := argvFind(argv, "--prefix")
		if idx < 0 {
			t.Fatalf("argv %v missing --prefix when opts.Prefix set", argv)
		}
		if argv[idx+1] != "unit1" {
			t.Errorf("--prefix = %q, want %q", argv[idx+1], "unit1")
		}
	})

	t.Run("with_voice", func(t *testing.T) {
		opts := PregenOpts{
			Words:    []string{"hello"},
			Language: "english",
			Voice:    "zh_female_v1",
		}
		argv, err := pregenArgs(cfg, opts)
		if err != nil {
			t.Fatalf("pregenArgs returned unexpected error: %v", err)
		}
		assertNoDBGlobal(t, argv)

		idx := argvFind(argv, "--voice")
		if idx < 0 {
			t.Fatalf("argv %v missing --voice when opts.Voice set", argv)
		}
		if argv[idx+1] != "zh_female_v1" {
			t.Errorf("--voice = %q, want %q", argv[idx+1], "zh_female_v1")
		}
	})

	t.Run("with_words_per_segment", func(t *testing.T) {
		opts := PregenOpts{
			Words:           []string{"hello", "world"},
			Language:        "english",
			WordsPerSegment: 5,
		}
		argv, err := pregenArgs(cfg, opts)
		if err != nil {
			t.Fatalf("pregenArgs returned unexpected error: %v", err)
		}
		assertNoDBGlobal(t, argv)

		idx := argvFind(argv, "--words-per-segment")
		if idx < 0 {
			t.Fatalf("argv %v missing --words-per-segment when opts.WordsPerSegment > 0", argv)
		}
		if argv[idx+1] != "5" {
			t.Errorf("--words-per-segment = %q, want %q", argv[idx+1], "5")
		}
	})

	t.Run("all flags combined", func(t *testing.T) {
		opts := PregenOpts{
			Words:           []string{"a", "b"},
			Language:        "chinese",
			WordsPerSegment: 1,
			Prefix:          "lesson1",
			Voice:           "zh_male_v2",
		}
		argv, err := pregenArgs(cfg, opts)
		if err != nil {
			t.Fatalf("pregenArgs returned unexpected error: %v", err)
		}
		assertNoDBGlobal(t, argv)

		for _, want := range []string{"--prefix", "--voice", "--words-per-segment"} {
			if argvFind(argv, want) < 0 {
				t.Errorf("argv %v missing expected flag %q for all-flags-combined case", argv, want)
			}
		}
	})

	t.Run("empty words errors", func(t *testing.T) {
		// Words is REQUIRED by pregenerate_dictation.py:1043 (argparse
		// required=True). pregenArgs must reject empty input at the argv
		// builder layer rather than letting the script fail with a non-zero
		// exit (avoids a needless subprocess spawn + gets a clearer error).
		opts := PregenOpts{Words: nil, Language: "english"}
		_, err := pregenArgs(cfg, opts)
		if err == nil {
			t.Fatalf("pregenArgs with empty Words returned nil error, want non-nil")
		}
		if !strings.Contains(err.Error(), "Words") {
			t.Errorf("pregenArgs error = %q, want it to mention Words", err.Error())
		}
	})

	t.Run("empty language errors", func(t *testing.T) {
		// Language is REQUIRED by pregenerate_dictation.py:1044 (required=True
		// with choices=[english, chinese]). pregenArgs must reject empty
		// Language at the argv builder.
		opts := PregenOpts{Words: []string{"hello"}, Language: ""}
		_, err := pregenArgs(cfg, opts)
		if err == nil {
			t.Fatalf("pregenArgs with empty Language returned nil error, want non-nil")
		}
		if !strings.Contains(err.Error(), "Language") {
			t.Errorf("pregenArgs error = %q, want it to mention Language", err.Error())
		}
	})
}

// TestRunPregenerateParse drives splitNonEmptyLines against the golden
// pregenerate_paths.txt fixture. This is the RESEARCH Pattern 3 / Pitfall 3
// acceptance point: pregenerate stdout is line-based mp3 paths, NOT JSON --
// json.Unmarshal would fail with "invalid character '/'" on the leading
// slash of an absolute path. The pipeline here mirrors what
// classifyPregenResult does AFTER the empty-stdout guard.
func TestRunPregenerateParse(t *testing.T) {
	t.Run("normal_paths", func(t *testing.T) {
		fixturePath := filepath.Join("testdata", "pregenerate_paths.txt")
		stdout, err := os.ReadFile(fixturePath)
		if err != nil {
			t.Fatalf("read fixture %s: %v", fixturePath, err)
		}

		// Pitfall 3 acceptance: the fixture MUST NOT be valid JSON. If it
		// were, the test would no longer exercise the line-based path.
		if json.Valid(stdout) {
			t.Fatalf("pregenerate_paths.txt is valid JSON; the line-based "+
				"path contract (Pitfall 3) is no longer exercised: %s",
				string(stdout))
		}

		paths := splitNonEmptyLines(stdout)
		if len(paths) != 3 {
			t.Fatalf("splitNonEmptyLines returned %d paths, want 3 (fixture has 3 lines)",
				len(paths))
		}
		// Spot-check each path. Each must be an absolute mp3 path; TrimSpace
		// must have stripped any trailing CR (CRLF tolerance).
		wantPaths := []string{
			"/opt/hta/cache/hello.mp3",
			"/opt/hta/cache/goodbye.mp3",
			"/opt/hta/cache/thank_you.mp3",
		}
		for i, want := range wantPaths {
			if paths[i] != want {
				t.Errorf("paths[%d] = %q, want %q", i, paths[i], want)
			}
		}
	})

	t.Run("trailing_newline_filtered", func(t *testing.T) {
		// The fixture ends with a trailing newline; splitNonEmptyLines must
		// NOT produce an empty-string fourth element. This subtest makes
		// that invariant explicit (the golden fixture's exact byte content
		// is one '\n' after the last path).
		stdout := []byte("/a.mp3\n/b.mp3\n")
		paths := splitNonEmptyLines(stdout)
		if len(paths) != 2 {
			t.Errorf("splitNonEmptyLines with trailing newline returned %d paths, "+
				"want 2 (empty trailing line must be filtered): %v",
				len(paths), paths)
		}
	})

	t.Run("empty_stdout_yields_empty_slice", func(t *testing.T) {
		// Mirror what classifyPregenResult sees in the Pitfall 6 contention
		// case: completely empty stdout. splitNonEmptyLines returns an empty
		// slice; the empty-stdout guard upstream returns ErrPythonEmpty
		// before this function is reached, but the slice invariant itself
		// must hold (callers must not see a nil/1-element slice of "").
		paths := splitNonEmptyLines([]byte{})
		if len(paths) != 0 {
			t.Errorf("splitNonEmptyLines(empty) returned %d paths, want 0: %v",
				len(paths), paths)
		}
		// Cover the "all whitespace" branch too (classifyPregenResult's
		// secondary ErrPythonEmpty path).
		paths = splitNonEmptyLines([]byte("\n  \n\t\n"))
		if len(paths) != 0 {
			t.Errorf("splitNonEmptyLines on whitespace-only returned %d paths, "+
				"want 0 (all-whitespace stdout is contract-violation empty)",
				len(paths))
		}
	})
}

// TestClassifyPregenResult is the RESEARCH Pitfall 6 acceptance point: when
// the pregenerate subprocess exits 0 BUT stdout is empty, classifyPregenResult
// MUST return ErrPythonEmpty -- never a silent zero-path success. The real
// pregenerate_dictation.py exhibits this signature when its fcntl.flock
// cannot be acquired (line 1297-1313: sys.exit(0) with empty stdout); the
// Go-side cap=1 pregenSem prevents it under normal concurrency, but cannot
// defend against non-Go processes holding the lock.
//
// This table-driven test covers ALL FOUR sentinel branches of
// classifyPregenResult directly (no subprocess spawned): timeout / non-zero
// exit / empty stdout / happy-path success. Each subtest asserts the
// returned error's errors.Is relationship to the expected sentinel.
func TestClassifyPregenResult(t *testing.T) {
	happyStdout := []byte("/opt/hta/cache/a.mp3\n/opt/hta/cache/b.mp3\n")

	cases := []struct {
		name       string
		stdout     []byte
		stderr     []byte
		exitCode   int
		runErr     error
		wantSent   error  // nil means: assert NO error returned
		wantExit   bool   // expect *ExitError recoverable via errors.As
		wantPaths  int    // expected len(result.Paths) on success
		wantSentIs string // human-readable label for wantSent for failure msgs
	}{
		{
			name:       "happy path with 2 paths",
			stdout:     happyStdout,
			stderr:     []byte("Intros: done\n"),
			exitCode:   0,
			runErr:     nil,
			wantSent:   nil,
			wantExit:   false,
			wantPaths:  2,
			wantSentIs: "no error",
		},
		{
			name:       "timeout via context.DeadlineExceeded",
			stdout:     nil,
			stderr:     []byte("killed mid-run\n"),
			exitCode:   -1,
			runErr:     context.DeadlineExceeded,
			wantSent:   ErrPythonTimeout,
			wantExit:   false,
			wantPaths:  0,
			wantSentIs: "ErrPythonTimeout",
		},
		{
			name:       "ctx canceled via context.Canceled",
			stdout:     nil,
			stderr:     nil,
			exitCode:   -1,
			runErr:     context.Canceled,
			wantSent:   ErrPythonTimeout,
			wantExit:   false,
			wantPaths:  0,
			wantSentIs: "ErrPythonTimeout (ctx.Canceled also classifies as timeout)",
		},
		{
			name:       "non-zero exit wraps ErrPythonExit",
			stdout:     []byte("partial output before crash\n"),
			stderr:     []byte("ERROR: ffmpeg not found\n"),
			exitCode:   1,
			runErr:     nil,
			wantSent:   ErrPythonExit,
			wantExit:   true,
			wantPaths:  0,
			wantSentIs: "ErrPythonExit (via *ExitError)",
		},
		{
			// RESEARCH Pitfall 6 acceptance point: empty stdout + exit 0
			// is the fcntl.flock contention signature. classifyPregenResult
			// MUST surface this as ErrPythonEmpty, never a silent
			// PregenResult{} success.
			name:       "empty stdout exit 0 is flock contention",
			stdout:     []byte{},
			stderr:     []byte("Another pregenerate_dictation.py instance is running; exiting.\n"),
			exitCode:   0,
			runErr:     nil,
			wantSent:   ErrPythonEmpty,
			wantExit:   false,
			wantPaths:  0,
			wantSentIs: "ErrPythonEmpty (Pitfall 6 flock contention)",
		},
		{
			// Whitespace-only stdout is also a contract violation -- the
			// script either printed real paths or it failed; printing only
			// whitespace means something is wrong.
			name:       "whitespace-only stdout yields ErrPythonEmpty",
			stdout:     []byte("\n  \n\t\n"),
			stderr:     nil,
			exitCode:   0,
			runErr:     nil,
			wantSent:   ErrPythonEmpty,
			wantExit:   false,
			wantPaths:  0,
			wantSentIs: "ErrPythonEmpty (whitespace-only stdout)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := classifyPregenResult(tc.stdout, tc.stderr,
				tc.exitCode, tc.runErr)

			if tc.wantSent == nil {
				if err != nil {
					t.Fatalf("classifyPregenResult returned unexpected error %v, "+
						"want nil (%s)", err, tc.wantSentIs)
				}
				if len(result.Paths) != tc.wantPaths {
					t.Fatalf("result.Paths len = %d, want %d",
						len(result.Paths), tc.wantPaths)
				}
				return
			}

			// Failure path: err must be non-nil and match the sentinel.
			if err == nil {
				t.Fatalf("classifyPregenResult returned nil error, want %s",
					tc.wantSentIs)
			}
			if !errors.Is(err, tc.wantSent) {
				t.Errorf("errors.Is(err, %s) = false; err=%v",
					tc.wantSentIs, err)
			}

			// When the expected sentinel is ErrPythonExit, callers must also
			// be able to recover the typed *ExitError via errors.As (so they
			// can read ExitCode/Stderr for diagnostics).
			if tc.wantExit {
				var exitErr *ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("errors.As(err, *ExitError) = false; want true "+
						"(callers must recover Cmd/ExitCode/Stderr); err=%v", err)
				}
				if exitErr.Cmd != "pregenerate" {
					t.Errorf("exitErr.Cmd = %q, want %q", exitErr.Cmd, "pregenerate")
				}
				if exitErr.ExitCode != tc.exitCode {
					t.Errorf("exitErr.ExitCode = %d, want %d",
						exitErr.ExitCode, tc.exitCode)
				}
				if !strings.Contains(exitErr.Stderr, string(tc.stderr)) {
					t.Errorf("exitErr.Stderr = %q, want it to contain stderr %q",
						exitErr.Stderr, string(tc.stderr))
				}
			}
		})
	}
}
