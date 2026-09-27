// vocab_mgmt.go implements the vocab_manager.py MANAGEMENT command paths on
// Bridge (Phase 9, VOC-01 — parent vocabulary management):
//   - (*Bridge).RunAdd: invokes the `add` subcommand with a list-mode JSON
//     batch of word entries and parses the {"added": N, "skipped": N}
//     result payload (vocab_manager.py:206).
//
// There is NO RunList here: reads go through database.CardRepository
// (D-12 — Go-side reads, zero Python subprocess spawns on the read path).
// Python remains the SOLE WRITER to the cards table (ADR write-separation).
//
// Reuses Bridge.run (Plan 03-03 exec engine: semaphore, process group,
// per-call timeout, logResult) and extractTrailingJSON (Plan 03-02 parser:
// tail-backward-scan to peel the trailing JSON off vocab_manager's mixed
// Warning/JSON stdout -- RESEARCH Pitfall 2), exactly like RunDue/RunReview
// in vocab.go.
//
// argv contract (RESEARCH Pattern 2 / verified against vocab_manager.py:762):
// vocab_manager.py's argparse declares `--db` on the TOP-LEVEL parser, BEFORE
// the subparsers. The bridge MUST therefore emit
//
//	[vocabScript, "--db", dbPath, "add", ...]
//
// with `--db` placed BEFORE the subcommand token; argparse would otherwise
// reject `--db` as an unknown subcommand flag. This is the same ordering
// contract documented at vocab.go:15-24.
package pybridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// addArgs builds the argv slice + cleanup for the vocab_manager.py `add`
// subcommand.
//
// The returned slice is intended to be passed to Bridge.run as the variadic
// `args` tail: the interpreter path is supplied separately by the caller.
// The shape is:
//
//	[vocabScript, "--db", dbPath, "add", "--category", <c>, [--unit <u>],
//	 (--words <json> | --file <path>)]
//
// where `--db` is placed BEFORE the `add` subcommand token because
// vocab_manager.py's argparse declares `--db` on the top-level parser
// (vocab_manager.py:762). The `--category` value is ALWAYS present
// (RunAdd validates it non-empty before calling addArgs); `--unit` is
// omitted when empty (script default "").
//
// Payload selection (copies reviewArgs verbatim, RESEARCH Pitfall 5):
//   - marshalled entries byte length <= addInlineByteCap AND
//     len(opts.Entries) <= addInlineEntryCap -> inline --words <json>,
//     cleanup is a no-op.
//   - Otherwise                            -> write to a temp file via
//     os.CreateTemp("", "hta-add-*.json") and pass --file <path>; the
//     returned cleanup func removes the temp file. Callers MUST defer
//     cleanup.
//
// Entries are ALWAYS marshalled as list-mode JSON (an array of
// {word, meaning, example} objects -- the AddEntry json tags match the
// script's list-mode keys at vocab_manager.py:166-179). Dict mode
// ({"word": "meaning"}) would silently drop the example field (RESEARCH
// Pitfall 4), so it is never used here.
func addArgs(cfg infra.PythonConfig, opts AddOpts) ([]string, func(), error) {
	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "add",
		"--category", opts.Category}
	if opts.Unit != "" {
		args = append(args, "--unit", opts.Unit)
	}

	entries := opts.Entries
	if entries == nil {
		entries = []AddEntry{}
	}
	jsonBytes, err := json.Marshal(entries)
	if err != nil {
		return nil, nil, errors.Wrap(err, "marshal add entries")
	}

	var cleanup func()
	if len(jsonBytes) <= addInlineByteCap && len(entries) <= addInlineEntryCap {
		args = append(args, "--words", string(jsonBytes))
		cleanup = func() {} // no temp file to remove
	} else {
		// Large payload: write to a temp file and pass --file. The temp
		// file lives in the OS temp dir; it contains only word/meaning/
		// example triples -- never secrets (same rationale as the review
		// path, T-03-14).
		tmp, err := os.CreateTemp("", "hta-add-*.json")
		if err != nil {
			return nil, nil, errors.Wrap(err, "create add entries temp file")
		}
		if _, err := tmp.Write(jsonBytes); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			return nil, nil, errors.Wrap(err, "write add entries temp file")
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmp.Name())
			return nil, nil, errors.Wrap(err, "close add entries temp file")
		}
		args = append(args, "--file", tmp.Name())
		cleanup = func() { _ = os.Remove(tmp.Name()) }
	}

	return args, cleanup, nil
}

// Caps for the inline-vs-file payload selection in addArgs. Mirror the
// reviewArgs caps (vocab.go:291-294): 4096 bytes is conservative for any
// mainstream platform argv limit; 100 entries backstops pathological
// inputs with many tiny entries. The `add --file` flag already exists on
// the Python side (vocab_manager.py:770).
const (
	addInlineByteCap  = 4096
	addInlineEntryCap = 100
)

// RunAdd executes the vocab_manager.py `add` subcommand and returns the
// parsed {added, skipped} counts (Phase 9, VOC-01). It is the entry point
// for parent vocabulary management writes.
//
// Lifecycle (same shape as RunDue / RunReview):
//  1. Pre-validate opts (category non-empty, 1..100 entries) BEFORE
//     spawning any subprocess -- a caller bug surfaces as a wrapped error
//     without a wasted spawn.
//  2. Build argv + cleanup via addArgs. Large payloads (--words JSON
//     > 4096 bytes OR > 100 entries) automatically use --file backed by a
//     temp file; cleanup removes the file when RunAdd returns.
//  3. Delegate to Bridge.run: semaphore acquire (D-02), per-call timeout
//     (cfg.MgmtTimeout), exec.CommandContext argv slice (no shell,
//     T-03-07), process-group kill (Linux), stdout/stderr capture.
//  4. Defer a semantic logResult call with command="add".
//  5. Classify the outcome into one of the pybridge sentinels:
//     - context.DeadlineExceeded -> ErrPythonTimeout.
//     - exit != 0                -> ExitError wrapping ErrPythonExit.
//     - empty stdout on exit 0   -> ErrPythonEmpty.
//     - extractTrailingJSON fail -> ErrPythonJSONParse.
//     - json.Unmarshal fail      -> ErrPythonJSONParse.
//
// On success the returned AddResult mirrors the {"added": N, "skipped": N}
// JSON object vocab_manager.py prints (vocab_manager.py:206). Skipped
// counts duplicates (UNIQUE(word, category) IntegrityError at
// vocab_manager.py:202-203).
func (b *Bridge) RunAdd(ctx context.Context, opts AddOpts) (AddResult, error) {
	if opts.Category == "" {
		return AddResult{}, errors.New("add: category is required")
	}
	if len(opts.Entries) < 1 || len(opts.Entries) > addInlineEntryCap {
		return AddResult{}, fmt.Errorf("add: entries must be in [1, %d], got %d",
			addInlineEntryCap, len(opts.Entries))
	}

	start := time.Now()
	args, cleanup, argErr := addArgs(b.cfg, opts)
	if argErr != nil {
		// cleanup is nil on argErr -- do NOT defer it. No subprocess was
		// spawned; do NOT invoke logResult (no run() happened, no sem
		// held -- same contract as RunUnlock).
		return AddResult{}, errors.Wrap(argErr, "build add argv")
	}
	defer cleanup()

	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.MgmtTimeout,
		b.cfg.Interpreter, args...)
	defer b.logResult("add", start, stdout, stderr, exitCode, err)

	if err != nil && (errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)) {
		return AddResult{}, errors.Wrap(ErrPythonTimeout, "run add")
	}
	if exitCode != 0 {
		return AddResult{}, &ExitError{
			Cmd:      "add",
			ExitCode: exitCode,
			Stderr:   string(stderr),
			Err:      ErrPythonExit,
		}
	}

	// Empty-success-stdout guard (same rationale as RunDue).
	if len(stdout) == 0 {
		return AddResult{}, errors.Wrap(ErrPythonEmpty, "run add")
	}

	raw, parseErr := extractTrailingJSON(stdout)
	if parseErr != nil {
		return AddResult{}, errors.Wrap(ErrPythonJSONParse, "run add")
	}

	var result AddResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return AddResult{}, errors.Wrap(ErrPythonJSONParse, "run add")
	}
	return result, nil
}
