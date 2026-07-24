// Package pybridge implements the Go <-> Python subprocess bridge that lets
// the Home Tutor Agent Go service invoke the vendored vocab_manager.py and
// pregenerate_dictation.py scripts (Phase 3, D-03). The bridge is a durable
// access layer (D-09): later phases reach the existing Python business
// logic (FSRS engine, vocabulary store, TTS generation) exclusively through
// the Executor interface declared in this file.
//
// This file defines the public contract layer only: the Executor interface,
// the typed option/result structs passed across it, the sentinel errors
// callers use to classify failures, and the ExitError typed struct that
// wraps subprocess exit diagnostics. The Bridge implementation lives in
// bridge.go (Plan 03-03); the command methods live in vocab.go and
// pregenerate.go (Plan 03-04 / 03-05).
//
// All sentinel errors MUST be detected by callers via errors.Is, never by
// string matching (D-04).
package pybridge

import (
	"context"
	"errors"
	"fmt"
)

// Executor is the contract every Python subprocess invocation flows
// through (D-11 general-purpose wrapper). Phase 3 delivers exactly three
// paths: due / review (vocab_manager.py) and pregenerate
// (pregenerate_dictation.py). Secondary vocab_manager commands are left
// for later phases.
//
// Method semantics:
//   - The due path invokes `vocab_manager.py --db <path> due ...` (PY-02)
//     and returns the parsed due-card list. opts carries the operational
//     flags forwarded to the subprocess (--category/--limit/--units/
//     --exclude/--peek/--fetch-lock-hours).
//   - The review path invokes `vocab_manager.py --db <path> review ...`
//     (PY-03) with the supplied rating map. Ratings are forwarded
//     verbatim -- the bridge does not validate or rewrite the four FSRS
//     ratings (good/hard/again/easy); any mapping decision belongs to
//     Phase 10 (RESEARCH Pitfall 4).
//   - The pregenerate path invokes `pregenerate_dictation.py generate
//     ...` (PY-04) and returns the list of mp3 absolute paths the script
//     wrote to CacheDir. stdout is line-based paths (NOT JSON); see
//     splitNonEmptyLines.
//
// The interface never exposes raw stdout/stderr []byte -- callers always
// receive a typed result or a typed error (prohibition in 03-02-PLAN.md).
//
// Executor is a small focused interface (Phase 1 D-43) so tests can inject
// a manual fake (D-48) without spawning a real Python interpreter --
// Windows dev hosts have no venv, and CI integration tests are gated
// behind a linux build tag.
type Executor interface {
	RunDue(ctx context.Context, opts DueOpts) (DueResult, error)
	RunReview(ctx context.Context, opts ReviewOpts) (ReviewResult, error)
	RunPregenerate(ctx context.Context, opts PregenOpts) (PregenResult, error)
}

// DueOpts carries the operational flags for the vocab_manager.py due
// subcommand (PY-02). Every field maps 1:1 to a CLI flag documented in
// vocab_manager.py's argparse; the bridge does not invent semantics. A
// zero-valued field means "omit the flag" so the script's default applies.
type DueOpts struct {
	// Category filters by vocabulary category. Forwarded as --category.
	Category string

	// Limit caps the number of cards returned. Forwarded as --limit.
	// <= 0 means omit the flag (no limit).
	Limit int

	// Units restricts the result to the named units. Forwarded as
	// --units (repeatable). nil/empty means all units.
	Units []string

	// Exclude removes the named units from the result. Forwarded as
	// --exclude (repeatable).
	Exclude []string

	// Peek selects the no-side-effects mode (--peek): the script returns
	// the due set WITHOUT updating fetch_locked_until. Useful for
	// previews.
	Peek bool

	// FetchLockHours overrides the default fetch-lock window
	// (--fetch-lock-hours). vocab_manager treats this as a float. <= 0
	// means use the script default.
	FetchLockHours float64
}

// DueCard mirrors the JSON object vocab_manager.py's due subcommand emits
// per due card. Field tags align exactly with the script's json keys
// (verified against vocab_manager.py:255-265 -- id/word/meaning/example/
// unit/state/due/stability/difficulty). There is intentionally no
// "translation" or "pronunciation" field: the script does not emit them.
type DueCard struct {
	ID         int     `json:"id"`
	Word       string  `json:"word"`
	Meaning    string  `json:"meaning"`
	Example    string  `json:"example"`
	Unit       string  `json:"unit"`
	State      string  `json:"state"`
	Due        string  `json:"due"`
	Stability  float64 `json:"stability"`
	Difficulty float64 `json:"difficulty"`
}

// DueResult is the typed payload the due path returns. Cards is the
// parsed JSON array vocab_manager.py prints on success (possibly empty
// when no cards are due).
type DueResult struct {
	Cards []DueCard
}

// ReviewOpts carries the operational flags for the vocab_manager.py
// review subcommand (PY-03). Ratings are passed through verbatim -- see
// the Executor doc comment.
type ReviewOpts struct {
	// Results maps word -> rating (one of good/hard/again/easy).
	// Forwarded to the script as the --results JSON object. The bridge
	// does NOT validate or rewrite ratings (RESEARCH Pitfall 4).
	Results map[string]string

	// Category disambiguates a word that exists in multiple categories.
	// Forwarded as --category. Empty means ambiguous; the script emits a
	// Warning and skips.
	Category string

	// ResultsFile is the path to a file holding the --results JSON for
	// large payloads that would exceed the platform argv length limit.
	// Forwarded as --file when UseFile is true. Empty means use --results
	// inline.
	ResultsFile string

	// UseFile selects the --file path over the inline --results JSON.
	// When true the bridge writes Results to ResultsFile and passes
	// --file; when false it passes Results inline as --results.
	UseFile bool
}

// UpdatedCard mirrors the JSON object vocab_manager.py's review subcommand
// appends to its "updated" array per reviewed card. Field tags align
// exactly with the script's json keys (verified against
// vocab_manager.py:437-443 -- word/rating/next_due/state/stability).
type UpdatedCard struct {
	Word      string  `json:"word"`
	Rating    string  `json:"rating"`
	NextDue   string  `json:"next_due"`
	State     string  `json:"state"`
	Stability float64 `json:"stability"`
}

// ReviewResult is the typed payload the review path returns. Updated
// mirrors the {"updated": [...]} JSON object vocab_manager.py prints on a
// successful review call (verified shape: array of UpdatedCard, NOT array
// of bare strings).
type ReviewResult struct {
	Updated []UpdatedCard
}

// PregenOpts carries the operational flags for the
// pregenerate_dictation.py generate subcommand (PY-04).
type PregenOpts struct {
	// Words is the word list to synthesize. Forwarded to the script as
	// the --words JSON array (required).
	Words []string

	// Language selects the TTS language profile. Forwarded as --language
	// (english|chinese).
	Language string

	// WordsPerSegment controls how many words are concatenated per
	// output mp3 segment. Forwarded as --words-per-segment. <= 0 means
	// use the script default.
	WordsPerSegment int

	// Prefix namespaces the generated files inside CacheDir. Forwarded
	// as --prefix. The script enforces ASCII safety via
	// _prefix_is_safe; the bridge does not duplicate that check here
	// (T-03-06 path-traversal mitigation lives at the bridge layer in
	// Plan 03-05).
	Prefix string

	// Voice overrides the default TTS voice. Forwarded as --voice.
	Voice string
}

// PregenResult is the typed payload the pregenerate path returns. Paths
// is the list of mp3 absolute paths the script printed to stdout, one
// per line (NOT JSON -- see splitNonEmptyLines).
type PregenResult struct {
	Paths []string
}

// Sentinel errors returned by the pybridge layer. Callers MUST detect
// them via errors.Is, never by string matching (D-04 -- fail-fast on
// contract violations, observable without parsing log text). The naming
// and documentation style mirrors pkg/infra/worker.go.
var (
	// ErrPythonExit is returned when the Python subprocess exits non-zero.
	// The accompanying ExitError (recoverable via errors.As) carries the
	// command, exit code, and stderr text so callers can distinguish
	// FSRS/user errors (exit 1 + stderr diagnostics) from infrastructure
	// errors (timeout / parse failure).
	ErrPythonExit = errors.New("python subprocess exited non-zero.")

	// ErrPythonTimeout is returned when the per-call context expires
	// before the subprocess returns. The bridge kills the entire process
	// group (RESEARCH Pattern 1) so no orphan ffmpeg survives.
	ErrPythonTimeout = errors.New("python subprocess timed out.")

	// ErrPythonJSONParse is returned when extractTrailingJSON cannot
	// locate a complete JSON object in the subprocess stdout (e.g. the
	// script crashed mid-print, or stdout was empty/non-JSON).
	ErrPythonJSONParse = errors.New("python stdout JSON parse failed.")

	// ErrPythonEmpty is returned when the subprocess exited 0 but stdout
	// is empty -- the script's contract is "exit 0 implies printable
	// payload", so an empty success stdout is treated as a bridge-level
	// error rather than a silent zero-card / zero-path result.
	ErrPythonEmpty = errors.New("python stdout empty.")
)

// ExitError is the typed error returned alongside ErrPythonExit (and
// recoverable via errors.As on any wrapped pybridge error). It captures
// the subprocess diagnostics the bridge needs to log: command, exit code,
// and stderr text. The wrapped Err field is the classification sentinel
// (typically ErrPythonExit) so callers can both errors.Is(err,
// ErrPythonExit) AND errors.As(err, &exitErr) to pull out the structured
// fields.
//
// Security note (V7 / SC-4): Cmd, ExitCode, and Stderr are safe to log;
// secret values (VOLCENGINE_TTS_*) never appear in these fields -- they
// ride in the subprocess env per D-05, never on argv or stdout.
type ExitError struct {
	Cmd      string
	ExitCode int
	Stderr   string
	Err      error
}

// Error implements the error interface. The format is stable so log
// scrapers can rely on it: `python command "<cmd>" exited <code>:
// <stderr>`.
func (e *ExitError) Error() string {
	return fmt.Sprintf("python command %q exited %d: %s",
		e.Cmd, e.ExitCode, e.Stderr)
}

// Unwrap returns the classification sentinel so errors.Is works through
// the wrapping ExitError (D-04): errors.Is(&ExitError{Err: ErrPythonExit},
// ErrPythonExit) == true.
func (e *ExitError) Unwrap() error { return e.Err }
