// vocab.go implements the vocab_manager.py command paths on Bridge:
//   - (*Bridge).RunDue      (PY-02): invokes the `due` subcommand and parses
//     the JSON array of due cards via extractTrailingJSON.
//   - (*Bridge).RunReview   (PY-03): invokes the `review` subcommand with the
//     caller-supplied rating map (4 ratings forwarded verbatim -- RESEARCH
//     Pitfall 4) and parses the {"updated": [...]} JSON payload.
//   - (*Bridge).RunUnlock   (PY-05): invokes the `unlock` subcommand to clear
//     FSRS fetch locks and parses the {"unlocked": N} JSON payload.
//
// Both methods reuse Bridge.run (Plan 03-03 exec engine: semaphore, process
// group, per-call timeout, logResult) and extractTrailingJSON (Plan 03-02
// parser: tail-backward-scan to peel the trailing JSON off vocab_manager's
// mixed Warning/JSON stdout -- RESEARCH Pitfall 2).
//
// argv contract (RESEARCH Pattern 2 / verified against vocab_manager.py:718):
// vocab_manager.py's argparse declares `--db` on the TOP-LEVEL parser, BEFORE
// the subparsers. The bridge MUST therefore emit
//
//	[vocabScript, "--db", dbPath, <subcommand>, ...]
//
// with `--db` placed BEFORE the subcommand token; argparse would otherwise
// reject `--db` as an unknown subcommand flag (RESEARCH lines 541-556). This
// ordering is unique to vocab_manager.py and does NOT apply to
// pregenerate_dictation.py (which has no global --db flag).
package pybridge

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// MaxDueLimit is the upper bound on --limit. It matches
// FSRSConfig.DefaultWordCount's validated range [1, 100] and
// vocab_manager.py MAX_CARD_LIMIT.
const MaxDueLimit = 100

// MaxDesignateLimit is the single-session cap on Issue #80 指定默写
// selection (AC-11 单次最多 100 个). It must stay equal to
// vocab_manager.py MAX_DESIGNATE_LIMIT; the script enforces it inside the
// BEGIN IMMEDIATE transaction and reports the real total back.
const MaxDesignateLimit = 100

// MaxFetchLockHours is the upper bound on --fetch-lock-hours: ten
// 366-day years. That stays far below datetime's year-9999 ceiling and
// still covers the 24h server default. vocab_manager.py
// MAX_FETCH_LOCK_HOURS must stay equal to this value.
const MaxFetchLockHours = 24 * 366 * 10

// WholeLockHours converts a configured hour count into the whole hours
// forwarded to vocab_manager.py. Non-positive, NaN, and negative-infinity
// inputs return 0 (the flag is omitted). A positive fraction below 1
// becomes 1 so the server lock is not replaced by the script default.
// Values above MaxFetchLockHours, including +Inf, are clamped.
func WholeLockHours(hours float64) float64 {
	n, ok := integerLockHours(hours)
	if !ok {
		return 0
	}
	return float64(n)
}

func integerLockHours(hours float64) (int, bool) {
	if math.IsNaN(hours) || hours <= 0 || math.IsInf(hours, -1) {
		return 0, false
	}
	if math.IsInf(hours, 1) || hours >= float64(MaxFetchLockHours) {
		return MaxFetchLockHours, true
	}
	n := int(hours) // hours is in (0, MaxFetchLockHours); truncate toward zero
	if n < 1 {
		n = 1
	}
	return n, true
}

func clampDueLimit(limit int) (int, bool) {
	if limit <= 0 {
		return 0, false
	}
	if limit > MaxDueLimit {
		limit = MaxDueLimit
	}
	return limit, true
}

// dueArgs builds the argv slice for the vocab_manager.py `due` subcommand.
//
// The returned slice is intended to be passed to Bridge.run as the variadic
// `args` tail: the interpreter path is supplied separately by the caller.
// The shape is:
//
//	[vocabScript, "--db", dbPath, "due", ...dueFlags]
//
// where `--db` is placed BEFORE the `due` subcommand token because
// vocab_manager.py's argparse declares `--db` on the top-level parser
// (verified against vocab_manager.py:718). Every DueOpts field maps to at
// most one CLI flag; a zero-valued field means "omit the flag" so the
// script default applies.
//
// Flag mapping (verified against vocab_manager.py:729-736):
//   - opts.Category   -> --category <value>
//   - opts.Limit      -> --limit <int>            (omitted when <= 0; clamped to MaxDueLimit)
//   - opts.Units      -> --units <comma-joined>   (omitted when empty)
//   - opts.Exclude    -> --exclude <comma-joined> (omitted when empty)
//   - opts.Peek       -> --peek (boolean flag, omitted when false)
//   - opts.FetchLockHours -> --fetch-lock-hours <int> (omitted when <= 0;
//     fractional hours truncate toward zero, with a floor of 1 and a
//     ceiling of MaxFetchLockHours)
//
// The function performs no I/O and no validation beyond the zero-value
// checks -- it is a pure transform of (cfg, opts) into an argv slice, which
// makes it trivially testable without a Python interpreter.
func dueArgs(cfg infra.PythonConfig, opts DueOpts) []string {
	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "due"}
	if opts.Category != "" {
		args = append(args, "--category", opts.Category)
	}
	if n, ok := clampDueLimit(opts.Limit); ok {
		args = append(args, "--limit", strconv.Itoa(n))
	}
	if len(opts.Units) > 0 {
		args = append(args, "--units", strings.Join(opts.Units, ","))
	}
	if len(opts.Exclude) > 0 {
		args = append(args, "--exclude", strings.Join(opts.Exclude, ","))
	}
	if opts.Peek {
		args = append(args, "--peek")
	}
	if n, ok := integerLockHours(opts.FetchLockHours); ok {
		args = append(args, "--fetch-lock-hours", strconv.Itoa(n))
	}
	return args
}

// unlockArgs builds the argv slice for the vocab_manager.py `unlock`
// subcommand.
//
// The returned slice is intended to be passed to Bridge.run as the variadic
// `args` tail: the interpreter path is supplied separately by the caller.
// The shape is:
//
//	[vocabScript, "--db", dbPath, "unlock", ...unlockFlags]
//
// where `--db` is placed BEFORE the `unlock` subcommand token because
// vocab_manager.py's argparse declares `--db` on the top-level parser (same
// contract as dueArgs). Every UnlockOpts field maps to at most one CLI flag;
// a zero-valued field means "omit the flag".
//
// Flag mapping:
//   - opts.IDs      -> --ids <json>       (scoped per-card release; wins over
//     the category flags when non-empty — Ticket 07 session isolation)
//   - opts.Category -> --category <value> (omitted when empty)
//   - opts.Unit     -> --unit <value>     (omitted when empty)
//
// The function performs no I/O and no validation beyond the zero-value
// checks -- it is a pure transform of (cfg, opts) into an argv slice. A
// marshal failure (impossible for []int64) returns an error rather than a
// filter-less argv: the Python default for an unfiltered unlock is a full
// category sweep — the exact cross-session clobber Ticket 07 eliminated.
func unlockArgs(cfg infra.PythonConfig, opts UnlockOpts) ([]string, error) {
	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "unlock"}
	if len(opts.IDs) > 0 {
		idsJSON, err := json.Marshal(opts.IDs)
		if err != nil {
			return nil, errors.Wrap(err, "marshal unlock ids")
		}
		args = append(args, "--ids", string(idsJSON))
		return args, nil
	}
	if opts.Category != "" {
		args = append(args, "--category", opts.Category)
	}
	if opts.Unit != "" {
		args = append(args, "--unit", opts.Unit)
	}
	return args, nil
}

// RunDue executes the vocab_manager.py `due` subcommand and returns the
// parsed list of due cards (PY-02). It is the entry point for word
// selection in later phases (Phase 5 FSRS orchestration, Phase 7 session
// flow).
//
// Lifecycle:
//  1. Build argv via dueArgs (cfg.VocabScript + "--db" + dbPath + "due" +
//     operational flags). `--db` precedes the subcommand token per
//     vocab_manager.py's argparse contract.
//  2. Delegate to Bridge.run: semaphore acquire (D-02), per-call timeout
//     (cfg.DueTimeout), exec.CommandContext argv slice (no shell, T-03-07),
//     process-group kill (Linux), stdout/stderr capture. run() emits the
//     raw subprocess log entry via logResult.
//  3. Defer a semantic logResult call with command="due" so operators can
//     filter due-path entries in the log stream (SC-4 field whitelist).
//  4. Classify the outcome into one of the pybridge sentinels:
//     - context.DeadlineExceeded -> ErrPythonTimeout (subprocess took too
//     long; the process group was SIGKILLed by run()'s Cancel hook).
//     - exit != 0                -> ExitError wrapping ErrPythonExit (FSRS
//     or user error; stderr carries the script's diagnostic).
//     - empty stdout on exit 0   -> ErrPythonEmpty (the script's contract
//     is "exit 0 implies printable JSON"; an empty success stdout is a
//     bridge-level error rather than a silent zero-card result).
//     - extractTrailingJSON fail -> ErrPythonJSONParse (stdout had no
//     trailing JSON value -- likely the script crashed mid-print).
//     - json.Unmarshal fail      -> ErrPythonJSONParse (the trailing bytes
//     were not a valid DueCard array).
//
// On success the returned DueResult.Cards mirrors the JSON array
// vocab_manager.py prints (verified shape: array of DueCard).
func (b *Bridge) RunDue(ctx context.Context, opts DueOpts) (DueResult, error) {
	start := time.Now()
	args := dueArgs(b.cfg, opts)
	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.DueTimeout,
		b.cfg.Interpreter, args...)
	defer b.logResult("due", start, stdout, stderr, exitCode, err)

	if err != nil && (errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)) {
		return DueResult{}, errors.Wrap(ErrPythonTimeout, "run due")
	}
	if exitCode != 0 {
		return DueResult{}, &ExitError{
			Cmd:      "due",
			ExitCode: exitCode,
			Stderr:   string(stderr),
			Err:      ErrPythonExit,
		}
	}

	// Empty-success-stdout guard: extractTrailingJSON wraps its own
	// ErrPythonJSONParse on empty input, but the executor contract reserves
	// ErrPythonEmpty for this case -- check before parsing so the sentinel
	// classes stay distinct (RESEARCH Pitfall 2 acceptance).
	if len(stdout) == 0 {
		return DueResult{}, errors.Wrap(ErrPythonEmpty, "run due")
	}

	// vocab_manager.py may emit Warning: prefix lines on stdout before the
	// trailing JSON (Pitfall 2). extractTrailingJSON peels the last JSON
	// value off the mixed stream; we then Unmarshal the bytes into Cards.
	raw, parseErr := extractTrailingJSON(stdout)
	if parseErr != nil {
		return DueResult{}, errors.Wrap(ErrPythonJSONParse, "run due")
	}

	var result DueResult
	if err := json.Unmarshal(raw, &result.Cards); err != nil {
		return DueResult{}, errors.Wrap(ErrPythonJSONParse, "run due")
	}
	return result, nil
}

// reviewArgs builds the argv slice for the vocab_manager.py `review`
// subcommand, choosing automatically between inline --results (small
// payloads) and --file (large payloads) to stay under the platform argv
// length limit (RESEARCH lines 541-556 -- approval/length concerns).
//
// The returned slice is intended to be passed to Bridge.run as the variadic
// `args` tail. The shape is:
//
//	[vocabScript, "--db", dbPath, "review", (--results <json> | --file <path>), [--category <c>]
//
// As with dueArgs, `--db` is placed BEFORE the `review` subcommand token
// (vocab_manager.py:718 global --db).
//
// Large-payload selection (RESEARCH line 541 -- "avoids long CLI args /
// approval prompts"):
//   - json.Marshal(opts.Results) byte length <= 4096 AND
//     len(opts.Results) <= 100  -> inline --results, cleanup is a no-op.
//   - Otherwise                                -> write to a temp file via
//     os.CreateTemp("", "hta-review-*.json") and pass --file <path>; the
//     returned cleanup func removes the temp file. Callers MUST defer
//     cleanup.
//
// Ratings are forwarded VERBATIM -- again/good/hard/easy pass through with
// no validation, no mapping, no rewrites (RESEARCH Pitfall 4: the script's
// rating_map accepts all four; the help text is outdated). Any rating
// unknown to the script causes a Warning line on stdout and the card is
// skipped; the overall call still exits 0 with a trailing JSON payload, so
// RunReview treats that as success.
//
// The function returns a non-nil cleanup only when argErr is nil; callers
// that hit argErr MUST NOT invoke cleanup (it will be nil).
func reviewArgs(cfg infra.PythonConfig, opts ReviewOpts) ([]string, func(), error) {
	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "review"}

	// json.Marshal on a nil map still produces "null" -- avoid forwarding
	// that to the script. An empty/non-nil map marshals to "{}" which the
	// script accepts as "nothing to review".
	results := opts.Results
	if results == nil {
		results = map[string]string{}
	}
	jsonBytes, err := json.Marshal(results)
	if err != nil {
		return nil, nil, errors.Wrap(err, "marshal review results")
	}

	var cleanup func()
	if len(jsonBytes) <= reviewInlineByteCap && len(results) <= reviewInlineEntryCap {
		args = append(args, "--results", string(jsonBytes))
		cleanup = func() {} // no temp file to remove
	} else {
		// Large payload: write to a temp file and pass --file. The temp
		// file lives in the OS temp dir (restricted permissions); it
		// contains only word/rating pairs -- never secrets (T-03-14).
		tmp, err := os.CreateTemp("", "hta-review-*.json")
		if err != nil {
			return nil, nil, errors.Wrap(err, "create review results temp file")
		}
		if _, err := tmp.Write(jsonBytes); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			return nil, nil, errors.Wrap(err, "write review results temp file")
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmp.Name())
			return nil, nil, errors.Wrap(err, "close review results temp file")
		}
		args = append(args, "--file", tmp.Name())
		cleanup = func() { _ = os.Remove(tmp.Name()) }
	}

	if opts.Category != "" {
		args = append(args, "--category", opts.Category)
	}
	return args, cleanup, nil
}

// Constants for the inline-vs-file argv selection in reviewArgs. The byte
// cap is conservative for any mainstream platform argv limit (Windows
// 32KiB, Linux 128KiB, macOS 256KiB); the entry cap is a backstop against
// pathological inputs with many tiny entries. Past either threshold the
// bridge switches to --file to keep argv length bounded (RESEARCH line 541
// -- "avoids long CLI args / approval prompts").
const (
	reviewInlineByteCap  = 4096
	reviewInlineEntryCap = 100
)

// RunReview executes the vocab_manager.py `review` subcommand with the
// caller-supplied rating map (PY-03). Ratings pass through VERBATIM
// (again/good/hard/easy -- RESEARCH Pitfall 4: no mapping, no validation,
// no rewrites). Phase 10 (FEED) is the right place for any rating-mapping
// decision; Phase 3 only forwards.
//
// Lifecycle:
//  1. Build argv + cleanup via reviewArgs. Large payloads (--results JSON
//     > 4096 bytes OR > 100 entries) automatically use --file backed by a
//     temp file; cleanup removes the file when RunReview returns.
//  2. Defer cleanup BEFORE b.run so the temp file always gets removed,
//     even on subprocess failure.
//  3. Delegate to Bridge.run (semaphore, timeout, exec, logResult).
//  4. Defer a semantic logResult call with command="review".
//  5. Classify the outcome into a pybridge sentinel (same shape as RunDue).
//
// stdout contract (RESEARCH Pitfall 2 -- critical acceptance point): on a
// successful review the script MAY emit Warning: lines (unknown rating,
// missing card, ambiguous card across categories) BEFORE the trailing
// {"updated": [...]} JSON, all on stdout. Such a call still exits 0 and is
// SUCCESS, not an error -- extractTrailingJSON peels off the trailing JSON
// and the warnings are visible only in the logResult-captured stdout field.
func (b *Bridge) RunReview(ctx context.Context, opts ReviewOpts) (ReviewResult, error) {
	start := time.Now()
	args, cleanup, argErr := reviewArgs(b.cfg, opts)
	if argErr != nil {
		// cleanup is nil on argErr -- do NOT defer it.
		return ReviewResult{}, errors.Wrap(argErr, "build review argv")
	}
	defer cleanup()

	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.ReviewTimeout,
		b.cfg.Interpreter, args...)
	defer b.logResult("review", start, stdout, stderr, exitCode, err)

	if err != nil && (errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)) {
		return ReviewResult{}, errors.Wrap(ErrPythonTimeout, "run review")
	}
	if exitCode != 0 {
		return ReviewResult{}, &ExitError{
			Cmd:      "review",
			ExitCode: exitCode,
			Stderr:   string(stderr),
			Err:      ErrPythonExit,
		}
	}

	// Empty-success-stdout guard (same rationale as RunDue).
	if len(stdout) == 0 {
		return ReviewResult{}, errors.Wrap(ErrPythonEmpty, "run review")
	}

	// Pitfall 2 acceptance point: Warning: lines may precede the trailing
	// {"updated": [...]} JSON on the same stream. extractTrailingJSON
	// ignores them and returns just the JSON tail; we Unmarshal that into
	// ReviewResult. The default json field matching is case-insensitive
	// (Updated <-> "updated"), and UpdatedCard has explicit json tags.
	raw, parseErr := extractTrailingJSON(stdout)
	if parseErr != nil {
		return ReviewResult{}, errors.Wrap(ErrPythonJSONParse, "run review")
	}

	var result ReviewResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return ReviewResult{}, errors.Wrap(ErrPythonJSONParse, "run review")
	}
	return result, nil
}

// RunUnlock executes the vocab_manager.py `unlock` subcommand and returns
// the number of cards whose fetch locks were cleared. It is the entry
// point for clearing FSRS fetch locks (e.g. from the WebSocket test
// interface).
//
// Lifecycle (same shape as RunDue):
//  1. Build argv via unlockArgs (cfg.VocabScript + "--db" + dbPath +
//     "unlock" + operational flags). `--db` precedes the subcommand token.
//  2. Delegate to Bridge.run: semaphore acquire (D-02), per-call timeout
//     (cfg.DueTimeout), exec.CommandContext argv slice (no shell, T-03-07),
//     process-group kill (Linux), stdout/stderr capture.
//  3. Defer a semantic logResult call with command="unlock".
//  4. Classify the outcome into one of the pybridge sentinels:
//     - context.DeadlineExceeded -> ErrPythonTimeout.
//     - exit != 0                -> ExitError wrapping ErrPythonExit.
//     - empty stdout on exit 0   -> ErrPythonEmpty.
//     - extractTrailingJSON fail -> ErrPythonJSONParse.
//     - json.Unmarshal fail      -> ErrPythonJSONParse.
//
// On success the returned UnlockResult.Unlocked mirrors the {"unlocked":
// N} JSON object vocab_manager.py prints.
func (b *Bridge) RunUnlock(ctx context.Context, opts UnlockOpts) (UnlockResult, error) {
	start := time.Now()
	args, argErr := unlockArgs(b.cfg, opts)
	if argErr != nil {
		// unlockArgs only fails on programmer error (ids marshal). No
		// subprocess was spawned; do NOT invoke logResult (no run() happened,
		// nosem held — same contract as RunPregenerate).
		return UnlockResult{}, errors.Wrap(argErr, "build unlock argv")
	}
	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.DueTimeout,
		b.cfg.Interpreter, args...)
	defer b.logResult("unlock", start, stdout, stderr, exitCode, err)

	if err != nil && (errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)) {
		return UnlockResult{}, errors.Wrap(ErrPythonTimeout, "run unlock")
	}
	if exitCode != 0 {
		return UnlockResult{}, &ExitError{
			Cmd:      "unlock",
			ExitCode: exitCode,
			Stderr:   string(stderr),
			Err:      ErrPythonExit,
		}
	}

	// Empty-success-stdout guard (same rationale as RunDue).
	if len(stdout) == 0 {
		return UnlockResult{}, errors.Wrap(ErrPythonEmpty, "run unlock")
	}

	raw, parseErr := extractTrailingJSON(stdout)
	if parseErr != nil {
		return UnlockResult{}, errors.Wrap(ErrPythonJSONParse, "run unlock")
	}

	var result UnlockResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return UnlockResult{}, errors.Wrap(ErrPythonJSONParse, "run unlock")
	}
	return result, nil
}

// designateArgs builds the argv slice for the vocab_manager.py `designate`
// subcommand (Issue #80 指定默写). The shape is:
//
//	[vocabScript, "--db", dbPath, "designate", "--textbook-id", T,
//	 "--lesson", L, ["--fetch-lock-hours", N]]
//
// `--db` precedes the subcommand token (the top-level argparse contract
// every vocab_manager.py command shares, see dueArgs). --textbook-id and
// --lesson are ALWAYS present — RunDesignate pre-validates both — and
// --fetch-lock-hours rides the same whole-hours clamp as the due path
// (integerLockHours; <= 0 omits the flag so the script's 24h default
// applies). Pure transform, no I/O, trivially unit-testable.
func designateArgs(cfg infra.PythonConfig, opts DesignateOpts) []string {
	return createDesignationArgs(cfg, CreateDesignationOpts{DesignateOpts: opts})
}

// createDesignationArgs builds argv for designate, optionally including
// --request-key and --category for atomic snapshot create (Issue #80 U10).
func createDesignationArgs(cfg infra.PythonConfig, opts CreateDesignationOpts) []string {
	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "designate",
		"--textbook-id", strconv.FormatInt(opts.TextbookID, 10),
		"--lesson", opts.Lesson}
	if n, ok := integerLockHours(opts.FetchLockHours); ok {
		args = append(args, "--fetch-lock-hours", strconv.Itoa(n))
	}
	if strings.TrimSpace(opts.RequestKey) != "" {
		args = append(args, "--request-key", strings.TrimSpace(opts.RequestKey))
	}
	if opts.Category != "" {
		args = append(args, "--category", opts.Category)
	}
	return args
}

// RunDesignate executes the vocab_manager.py `designate` subcommand and
// returns the typed selection outcome (Issue #80 指定默写). It is the
// selection+occupancy path for designated dictation sessions — the
// classroom's explicit entry beside the automatic due/new selection.
//
// Lifecycle (same shape as RunUnlock, classification via classifyMgmtRun):
//  1. Pre-validate opts (textbook id > 0, lesson non-empty — 待确认 can
//     never be designated) BEFORE spawning any subprocess.
//  2. Delegate to Bridge.run under cfg.DueTimeout (a selection+lock command
//     kin to due, not a management mutation); logResult with
//     command="designate".
//  3. Classify into the pybridge sentinels (timeout / exit / empty /
//     JSON-parse), then unmarshal the trailing JSON object.
//
// A nil error does NOT mean words were selected: occupied and too_many are
// legitimate exit-0 outcomes reported through DesignateResult.Status — the
// API layer branches on Status, never on the error alone. The not-found
// diagnostics (unknown textbook id, lesson outside the list) print to
// stdout with exit 1 and ride ExitError.Stdout for the caller's 404/400
// mapping (the same Pitfall-2 chain as the other commands).
func (b *Bridge) RunDesignate(ctx context.Context, opts DesignateOpts) (DesignateResult, error) {
	if opts.TextbookID <= 0 {
		return DesignateResult{}, fmt.Errorf("designate: textbook id must be > 0, got %d", opts.TextbookID)
	}
	if opts.Lesson == "" {
		return DesignateResult{}, errors.New("designate: lesson is required (待确认 cannot be designated)")
	}

	start := time.Now()
	args := designateArgs(b.cfg, opts)
	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.DueTimeout,
		b.cfg.Interpreter, args...)
	defer b.logResult("designate", start, stdout, stderr, exitCode, err)

	raw, clsErr := classifyMgmtRun("designate", stdout, stderr, exitCode, err)
	if clsErr != nil {
		return DesignateResult{}, clsErr
	}

	var result DesignateResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return DesignateResult{}, errors.Wrap(ErrPythonJSONParse, "run designate")
	}
	return result, nil
}

// RunCreateDesignation runs designate with --request-key so locks and the
// pending designations snapshot land in one SQLite transaction (Issue #80
// U10). See CreateDesignationOpts for the path guard and validation rules.
func (b *Bridge) RunCreateDesignation(ctx context.Context, opts CreateDesignationOpts) (DesignateResult, error) {
	if opts.TextbookID <= 0 {
		return DesignateResult{}, fmt.Errorf("create designation: textbook id must be > 0, got %d", opts.TextbookID)
	}
	if opts.Lesson == "" {
		return DesignateResult{}, errors.New("create designation: lesson is required (待确认 cannot be designated)")
	}
	key := strings.TrimSpace(opts.RequestKey)
	if key == "" {
		return DesignateResult{}, errors.New("create designation: request key is required")
	}
	if len(key) > MaxDesignationRequestKeyLen {
		return DesignateResult{}, fmt.Errorf("create designation: request key longer than %d characters", MaxDesignationRequestKeyLen)
	}
	opts.RequestKey = key
	if goPath := strings.TrimSpace(opts.GoDatabasePath); goPath != "" {
		if filepath.Clean(goPath) != filepath.Clean(b.cfg.VocabDBPath) {
			return DesignateResult{}, ErrDesignationDatabaseSplit
		}
	}

	start := time.Now()
	args := createDesignationArgs(b.cfg, opts)
	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.DueTimeout,
		b.cfg.Interpreter, args...)
	defer b.logResult("designate", start, stdout, stderr, exitCode, err)

	raw, clsErr := classifyMgmtRun("designate", stdout, stderr, exitCode, err)
	if clsErr != nil {
		return DesignateResult{}, clsErr
	}

	var result DesignateResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return DesignateResult{}, errors.Wrap(ErrPythonJSONParse, "run create designation")
	}
	return result, nil
}
