// pregenerate.go implements the pregenerate_dictation.py command path on
// Bridge:
//   - (*Bridge).RunPregenerate (PY-04): invokes the `generate` subcommand and
//     parses the line-based mp3 absolute path output via splitNonEmptyLines.
//
// This method reuses Bridge.run (Plan 03-03 exec engine) and splitNonEmptyLines
// (Plan 03-02 parser). It is the SAME SHAPE as RunDue/RunReview (Plan 03-04)
// but with two pregenerate-specific twists:
//
//  1. argv contract differs (RESEARCH Pattern 3, verified against
//     pregenerate_dictation.py:1038-1058 + main:1316-1326): pregenerate has
//     NO global --db flag. The first argv element is the pregenScript
//     filename, immediately followed by the literal subcommand token
//     "generate", then --words (JSON array string), --language, and the
//     optional --words-per-segment / --prefix / --voice flags.
//  2. stdout is line-based mp3 paths, NOT JSON (RESEARCH Pitfall 3). The
//     parser is splitNonEmptyLines, NOT extractTrailingJSON -- never mix the
//     two. Calling json.Unmarshal on pregenerate stdout would fail with
//     "invalid character '/'" (the leading '/' of an absolute path).
//
// fcntl.flock serialization (RESEARCH Pitfall 6, verified against
// pregenerate_dictation.py:1297-1313): pregenerate takes a GLOBAL
// single-instance flock on /tmp/pregenerate_dictation.lock using
// LOCK_EX|LOCK_NB. A second invocation that finds the lock held prints a
// diagnostic to stderr and exits 0 with EMPTY stdout -- silently looking
// like success to a naive caller. Two defences live here:
//   - Bridge.pregenSem (cap=1) serializes concurrent RunPregenerate callers
//     IN GO so they queue rather than race into the Python-side lock.
//   - RunPregenerate detects "exit 0 BUT stdout empty" and returns
//     ErrPythonEmpty -- never a silent zero-path success. (Even when no
//     contention is involved, exit 0 with no paths is a bridge-level
//     contract violation: the script's contract is "exit 0 implies at least
//     one path printed".)
package pybridge

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// pregenArgs builds the argv slice for the pregenerate_dictation.py
// `generate` subcommand.
//
// The returned slice is intended to be passed to Bridge.run as the variadic
// `args` tail (the interpreter path is supplied separately by the caller).
// The shape is:
//
//	[pregenScript, "generate", "--words", <jsonArray>, "--language", <lang>, ...optionalFlags]
//
// Note the absence of any global --db flag: pregenerate_dictation.py's main()
// pops "generate" off argv and parses the rest with a fresh argparse parser
// (verified against pregenerate_dictation.py:1316-1326 + 1038-1058). This is
// different from vocab_manager.py, which declares --db on the top-level
// parser BEFORE the subcommand token (see dueArgs / reviewArgs). Mixing the
// two argv shapes would break either script.
//
// Flag mapping (verified against pregenerate_dictation.py:1043-1051):
//   - opts.Words            -> --words <jsonArray>  (REQUIRED; the whole
//                            list is batched into one call per D-01.)
//   - opts.Language         -> --language <english|chinese>  (REQUIRED)
//   - opts.WordsPerSegment  -> --words-per-segment <int>     (omitted when <= 0)
//   - opts.Prefix           -> --prefix <value>              (omitted when empty)
//   - opts.Voice            -> --voice <value>               (omitted when empty)
//
// Words is json.Marshal'd into a JSON array string (e.g. `["hello","world"]`)
// because that is the form pregenerate_dictation.py's argparse expects
// (verified at pregenerate_dictation.py:1043 -- help text shows the JSON
// array example). The script re-parses it with json.loads internally.
//
// The function performs no I/O and no validation beyond the Marshall check
// -- it is a pure transform of (cfg, opts) into an argv slice, trivially
// testable without a Python interpreter.
func pregenArgs(cfg infra.PythonConfig, opts PregenOpts) ([]string, error) {
	// Words is REQUIRED by the script's argparse; guard against the zero
	// value producing a `--words 'null'` argv that the script would reject
	// with a non-zero exit. Returning an error here lets the caller classify
	// it as a programming mistake rather than a subprocess failure.
	if len(opts.Words) == 0 {
		return nil, errors.New("pregenArgs: opts.Words must not be empty")
	}
	if opts.Language == "" {
		return nil, errors.New("pregenArgs: opts.Language must not be empty")
	}

	wordsJSON, err := json.Marshal(opts.Words)
	if err != nil {
		// json.Marshal on a []string can only fail on non-UTF-8 bytes; this
		// branch is defensive against caller-supplied garbage. Wrap so the
		// caller sees the underlying cause via errors.Is/errors.As.
		return nil, errors.Wrap(err, "marshal pregenerate --words")
	}

	args := []string{cfg.PregenScript, "generate",
		"--words", string(wordsJSON),
		"--language", opts.Language,
	}
	if opts.WordsPerSegment > 0 {
		args = append(args, "--words-per-segment", strconv.Itoa(opts.WordsPerSegment))
	}
	if opts.Prefix != "" {
		args = append(args, "--prefix", opts.Prefix)
	}
	if opts.Voice != "" {
		args = append(args, "--voice", opts.Voice)
	}
	return args, nil
}

// RunPregenerate executes pregenerate_dictation.py's `generate` subcommand
// and returns the list of mp3 absolute paths the script wrote to CacheDir
// (PY-04). It is the entry point for TTS audio generation in later phases
// (Phase 6 codec layer, Phase 7 session orchestration).
//
// Lifecycle:
//  1. Build argv via pregenArgs (cfg.PregenScript + "generate" + --words JSON
//     array + --language + optional flags). NO global --db; pregenerate has
//     its own argv shape distinct from vocab_manager.
//  2. Acquire Bridge.pregenSem (cap=1) BEFORE b.run so concurrent
//     RunPregenerate callers QUEUE in Go rather than race into the
//     Python-side fcntl.flock (RESEARCH Pitfall 6: a second Python instance
//     that finds the lock held exits 0 with EMPTY stdout, looking like
//     silent success to a naive caller). The general D-02 semaphore inside
//     b.run still applies on top of this -- pregenSem is an additional
//     constraint, not a replacement.
//  3. Defer pregenSem.release() so the lock is always returned even on
//     b.run failure.
//  4. Delegate to Bridge.run: general sem acquire, per-call timeout
//     (cfg.PregenTimeout), exec.CommandContext argv slice (no shell,
//     T-03-07), process-group kill (Linux -- pregenerate spawns ffmpeg as a
//     grandchild so this prevents orphan ffmpeg Pitfall 1), stdout/stderr
//     capture. run() emits the raw subprocess log entry via logResult.
//  5. Defer a semantic logResult call with command="pregenerate" so
//     operators can filter pregenerate-path entries in the log stream
//     (SC-4 field whitelist).
//  6. Classify the outcome into one of the pybridge sentinels:
//     - context.DeadlineExceeded -> ErrPythonTimeout (subprocess took too
//       long; the process group was SIGKILLed by run()'s Cancel hook).
//     - exit != 0                -> ExitError wrapping ErrPythonExit
//       (pregenerate/ffmpeg/Volcengine error; stderr carries the script's
//       diagnostic).
//     - empty stdout on exit 0   -> ErrPythonEmpty (RESEARCH Pitfall 6
//       acceptance: empty-success-stdout is the signature of flock
//       contention OR a script bug; either way the bridge MUST NOT return
//       a silent zero-path success -- surface as a typed error so callers
//       can retry or surface upstream).
//     - splitNonEmptyLines yields the path list. (No ErrPythonJSONParse
//       here: pregenerate stdout is line-based, not JSON -- Pitfall 3.)
//
// On success the returned PregenResult.Paths mirrors the line-based stdout
// the script emits at pregenerate_dictation.py:1147-1148
// (`for p in audio_files: print(os.path.abspath(p))`).
//
// Path validation note (T-03-06 / T-03-17): splitNonEmptyLines intentionally
// does NOT validate paths are inside cfg.CacheDir. The script's own
// _prefix_is_safe (pregenerate_dictation.py:1031-1035) is the primary
// defence against path traversal via --prefix. Phase 6 is the seam where
// mp3 bytes get read; that is where a CacheDir-prefix check belongs. Phase
// 3 deliberately produces paths only (D-06/D-08).
func (b *Bridge) RunPregenerate(ctx context.Context, opts PregenOpts) (PregenResult, error) {
	args, argErr := pregenArgs(b.cfg, opts)
	if argErr != nil {
		// pregenArgs only fails on programmer error (empty Words/Language)
		// or json.Marshal failure. No subprocess was spawned; do NOT invoke
		// logResult (no run() happened, nosem held).
		return PregenResult{}, errors.Wrap(argErr, "build pregenerate argv")
	}

	// Dedicated cap=1 serialization for pregenerate (RESEARCH Pitfall 6).
	// Acquired BEFORE b.run so queueing happens outside b.run's general-sem
	// slot too -- otherwise a queued caller would hold a general-sem token
	// while waiting on pregenSem, starving other Python paths (due/review).
	if err := b.pregenSem.acquire(ctx); err != nil {
		return PregenResult{}, errors.Wrap(err, "pregenerate serialize")
	}
	defer b.pregenSem.release()

	start := time.Now()
	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.PregenTimeout,
		b.cfg.Interpreter, args...)
	defer b.logResult("pregenerate", start, stdout, stderr, exitCode, err)

	return classifyPregenResult(stdout, stderr, exitCode, err)
}

// classifyPregenResult is the pure post-run classifier for RunPregenerate,
// extracted to a top-level function so the four sentinel branches
// (ErrPythonTimeout / ErrPythonExit / ErrPythonEmpty / success) are testable
// without spawning a Python subprocess -- Windows dev hosts have no venv, so
// the table-driven unit tests in pregenerate_test.go drive this function
// directly with fixture bytes + synthetic exit codes.
//
// Classification rules (in order -- first match wins):
//  1. runErr is context.DeadlineExceeded/Canceled -> ErrPythonTimeout
//     (b.run's Cancel hook already SIGKILLed the process group).
//  2. exitCode != 0                              -> *ExitError wrapping
//     ErrPythonExit (FSRS/ffmpeg/Volcengine error; stderr carries the
//     script's diagnostic).
//  3. len(stdout) == 0                           -> ErrPythonEmpty (RESEARCH
//     Pitfall 6: empty-success-stdout is the signature of fcntl.flock
//     contention OR a script bug; never a silent zero-path success).
//  4. splitNonEmptyLines yields 0 paths          -> ErrPythonEmpty (whitespace-
//     only stdout is still a contract violation).
//  5. otherwise                                  -> PregenResult{Paths} with
//     the parsed line-based mp3 paths (NEVER json.Unmarshal -- Pitfall 3).
//
// The function performs no I/O. stderr is only consumed inside the *ExitError
// wrapper; it is never logged here (logResult handles the structured-log
// side; classifyPregenResult only classifies).
func classifyPregenResult(stdout, stderr []byte, exitCode int, runErr error) (PregenResult, error) {
	if runErr != nil && (errors.Is(runErr, context.DeadlineExceeded) ||
		errors.Is(runErr, context.Canceled)) {
		return PregenResult{}, errors.Wrap(ErrPythonTimeout, "run pregenerate")
	}
	if exitCode != 0 {
		return PregenResult{}, &ExitError{
			Cmd:      "pregenerate",
			ExitCode: exitCode,
			Stderr:   string(stderr),
			Err:      ErrPythonExit,
		}
	}

	// Empty-success-stdout guard (RESEARCH Pitfall 6 acceptance point).
	// pregenerate_dictation.py's _acquire_global_lock (line 1297-1313) calls
	// sys.exit(0) when it cannot acquire the flock -- the process exits 0
	// with EMPTY stdout. The cap=1 pregenSem in RunPregenerate prevents this
	// under normal Go-side concurrency, but cannot defend against a non-Go
	// process (manual operator invocation, stale process from a previous
	// deploy) holding the lock. Detect the signature here and surface it as
	// ErrPythonEmpty rather than a silent zero-path success. Also catches
	// "script bug: exited 0 but printed nothing" -- still a bridge-level
	// contract violation.
	if len(stdout) == 0 {
		return PregenResult{}, errors.Wrap(ErrPythonEmpty,
			"pregenerate returned empty stdout (likely flock contention)")
	}

	// RESEARCH Pattern 3 / Pitfall 3: pregenerate stdout is line-based mp3
	// absolute paths, NOT JSON. Never json.Unmarshal here -- the leading '/'
	// of an absolute path would fail JSON parsing. splitNonEmptyLines is the
	// dedicated parser for this contract (Plan 03-02).
	paths := splitNonEmptyLines(stdout)
	if len(paths) == 0 {
		// splitNonEmptyLines trims and drops empty lines. If stdout was
		// non-empty (passed the guard above) but ALL lines were whitespace,
		// the script still produced no usable paths -- treat the same as
		// empty stdout.
		return PregenResult{}, errors.Wrap(ErrPythonEmpty,
			"pregenerate stdout had no non-empty path lines")
	}
	return PregenResult{Paths: paths}, nil
}
