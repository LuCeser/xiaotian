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
// through (D-11 general-purpose wrapper). Phase 3 delivers exactly four
// paths: due / review / unlock (vocab_manager.py) and pregenerate
// (pregenerate_dictation.py).
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
//   - The unlock path invokes `vocab_manager.py --db <path> unlock ...`
//     and returns the count of cards whose fetch locks were cleared.
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
	RunUnlock(ctx context.Context, opts UnlockOpts) (UnlockResult, error)

	// RunAdd invokes the vocab_manager.py `add` subcommand (Phase 9, VOC-01)
	// with a batch of word entries and returns the {added, skipped} counts
	// the script prints. Duplicate word+category pairs are silently skipped
	// by the SQLite UNIQUE constraint and reported via Skipped.
	RunAdd(ctx context.Context, opts AddOpts) (AddResult, error)

	// RunEdit invokes the vocab_manager.py `edit` subcommand (Phase 9,
	// VOC-01, Plan 09-03) and updates ONLY the text fields whose *string
	// pointer is non-nil; nil pointers omit the flag so the script's
	// COALESCE keeps the current column. Never touches FSRS state (D-07).
	RunEdit(ctx context.Context, opts EditOpts) (EditResult, error)

	// RunSetParticipation changes classroom eligibility for a batch in one
	// Python process and one SQLite transaction. All-or-nothing: any id
	// that no longer exists rolls the whole batch back and surfaces as an
	// ExitError whose stdout names the missing ids (→ 404 upstream), so a
	// success result always means every requested card was updated.
	RunSetParticipation(ctx context.Context, opts SetParticipationOpts) (SetParticipationResult, error)

	// RunRenameUnit invokes the vocab_manager.py `rename-unit` subcommand
	// (Plan 09-03) and renames every card's unit within one category,
	// returning the {renamed, from, to} report.
	RunRenameUnit(ctx context.Context, opts RenameUnitOpts) (RenameUnitResult, error)

	// RunDeleteWords invokes the vocab_manager.py `delete-words` subcommand
	// (Plan 09-03) targeting either an explicit word list (--words,
	// comma-joined) or a whole unit (--unit) — exactly one, validated by
	// the argv builder. The confirmation flag is appended unconditionally
	// (Pitfall 1: no bridge code path may trigger the script's non-JSON
	// dry-run preview).
	RunDeleteWords(ctx context.Context, opts DeleteWordsOpts) (DeleteWordsResult, error)
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
	// use the script default. Ignored by the "bare" generation mode.
	WordsPerSegment int

	// GenerationMode selects the TTS strategy. Forwarded as
	// --generation-mode (contextual-segment|fragments|bare). Empty means
	// use the script default ("fragments"). Use "bare" for per-word
	// review tracks (one bare-word file per word, no narration), which
	// keeps the paths[i] <-> words[i] position mapping correct while
	// stripping scene/intro audio.
	GenerationMode string

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

// UnlockOpts carries the operational flags for the vocab_manager.py
// unlock subcommand. Category filters by category; Unit restricts the
// unlock to one unit, empty = all units.
type UnlockOpts struct {
	Category string
	Unit     string

	// IDs requests a SCOPED per-card release (Ticket 07): exactly these card
	// IDs are unlocked, nothing else in the category — one session exiting
	// must not drop another session's in-flight locks. Non-empty IDs take
	// precedence over Category/Unit, which are then ignored.
	IDs []int64
}

// UnlockResult is the typed payload the unlock path returns. Unlocked
// mirrors the {"unlocked": N} JSON object vocab_manager.py prints on a
// successful unlock call.
type UnlockResult struct {
	Unlocked int `json:"unlocked"`
}

// AddEntry is one word entry in an AddOpts batch. Field tags align exactly
// with the list-mode JSON keys vocab_manager.py's cmd_add reads
// (verified against vocab_manager.py:166-179 -- word/meaning/example). The
// bridge always marshals entries as a list of objects (list mode) so the
// Example field survives; dict mode ({"word": "meaning"}) would silently
// drop it (RESEARCH Pitfall 4).
type AddEntry struct {
	Word    string `json:"word"`
	Meaning string `json:"meaning"`
	Example string `json:"example"`
}

// AddOpts carries the operational flags for the vocab_manager.py add
// subcommand (Phase 9, VOC-01).
type AddOpts struct {
	// Category scopes every entry in the batch. Forwarded as --category.
	// Must be "english" or "chinese" (validated by callers / Bridge.RunAdd).
	Category string

	// Unit assigns the whole batch to one unit/lesson. Forwarded as
	// --unit. Empty means omit the flag (script default "").
	Unit string

	// Entries is the word batch. Forwarded as list-mode JSON via
	// --words (small payloads) or --file (large payloads) -- the same
	// inline-vs-temp-file selection reviewArgs uses.
	Entries []AddEntry
}

// AddResult is the typed payload the add path returns. Added/Skipped
// mirror the {"added": N, "skipped": N} JSON object vocab_manager.py
// prints on a successful add call (verified against vocab_manager.py:206).
// Skipped counts duplicates (UNIQUE(word, category) violations).
type AddResult struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
}

// EditOpts carries the operational flags for the vocab_manager.py edit
// subcommand (Phase 9, Plan 09-03). The None-vs-empty distinction is the
// whole point of the CLI contract (Plan 09-02): a nil pointer means "omit
// the flag, keep the current column" while a non-nil pointer — including
// one to the empty string — means "emit the flag", and an explicit empty
// string CLEARS the column via cmd_edit's COALESCE update. FSRS learning
// state is never touched either way (D-07).
type EditOpts struct {
	// ID is the card id to edit. Forwarded as --id (decimal). Validated
	// > 0 by Bridge.RunEdit before any subprocess spawn.
	ID int64

	// Word replaces the word text when non-nil. Forwarded as --word.
	Word *string

	// Meaning replaces the meaning when non-nil. Forwarded as --meaning.
	Meaning *string

	// Example replaces the example when non-nil. Forwarded as --example.
	Example *string

	// Unit moves the card to another unit when non-nil. Forwarded as
	// --unit.
	Unit *string
}

// EditResult is the typed payload the edit path returns. Edited mirrors
// the nested {"edited": {"id": N, "word": "..."}} JSON object
// vocab_manager.py's cmd_edit prints after committing (the post-update
// word from a fresh SELECT — Plan 09-02 contract).
type EditResult struct {
	Edited struct {
		ID   int64  `json:"id"`
		Word string `json:"word"`
	} `json:"edited"`
}

// SetParticipationOpts carries the batch eligibility change: 1..500 unique
// positive card ids plus the target flag. One opts value = one subprocess
// = one SQLite transaction (Issue #2's batch contract — the per-card fanout
// a review killed would have queued one Python process per word).
type SetParticipationOpts struct {
	IDs          []int64
	Participates bool
}

type SetParticipationResult struct {
	Requested int `json:"requested"`
	Updated   int `json:"updated"`
	// Missing is kept for wire stability; the all-or-nothing script always
	// reports an empty list on the success path (a missing id is a 404).
	Missing []int64 `json:"missing"`
}

// RenameUnitOpts carries the operational flags for the vocab_manager.py
// rename-unit subcommand (Plan 09-03). All three fields are required by
// the script (argparse required=True / non-empty check); RunRenameUnit
// pre-validates them non-empty.
type RenameUnitOpts struct {
	// OldUnit is the current unit name. Forwarded as --old-unit.
	OldUnit string

	// NewUnit is the replacement unit name. Forwarded as --new-unit.
	NewUnit string

	// Category scopes the rename. Forwarded as --category.
	Category string
}

// RenameUnitResult is the typed payload the rename path returns. Field
// tags align exactly with the script's json keys (verified against
// vocab_manager.py cmd_rename_unit: {"renamed": N, "from": ..., "to": ...}).
type RenameUnitResult struct {
	Renamed int    `json:"renamed"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// DeleteWordsOpts carries the operational flags for the vocab_manager.py
// delete-words subcommand (Plan 09-03). Exactly one target must be set —
// Words (explicit word list) XOR Unit (whole unit) — enforced by
// deleteWordsArgs before any argv is constructed (mirrors the script's
// mutually-exclusive argparse group).
type DeleteWordsOpts struct {
	// Words is the explicit word list to delete. Forwarded as --words
	// (comma-joined). Mutually exclusive with Unit.
	Words []string

	// Unit deletes every card in this unit. Forwarded as --unit. Mutually
	// exclusive with Words.
	Unit string

	// Category scopes the deletion. Forwarded as --category.
	Category string
}

// DeleteWordsResult is the typed payload the delete path return. Deleted
// mirrors the {"deleted": N, ...} JSON object vocab_manager.py's
// cmd_delete_words prints on a confirmed delete (the extra "unit"/"words"
// echo keys the script appends are intentionally not modelled — the count
// is the only field Plan 09-05's endpoints surface).
type DeleteWordsResult struct {
	Deleted int `json:"deleted"`
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
// stderr text, and — since Plan 09-03 — the captured stdout text. The
// wrapped Err field is the classification sentinel (typically
// ErrPythonExit) so callers can both errors.Is(err, ErrPythonExit) AND
// errors.As(err, &exitErr) to pull out the structured fields.
//
// Stdout exists because some vocab_manager.py failure diagnostics print to
// STDOUT, not stderr: rename-unit's "No cards found ..." message goes to
// stdout followed by exit 1 (Pitfall 2). Without this field the diagnostic
// would be lost and the API layer could only report an empty failure.
// Stdout is bounded command output (script diagnostics, never secrets —
// T-09-07); API-layer consumers truncate before echoing it to clients.
//
// Security note (V7 / SC-4): Cmd, ExitCode, Stderr, and Stdout are safe to
// log; secret values (VOLCENGINE_TTS_*) never appear in these fields --
// they ride in the subprocess env per D-05, never on argv or stdout.
type ExitError struct {
	Cmd      string
	ExitCode int
	Stderr   string

	// Stdout carries the captured subprocess stdout bytes for commands
	// whose failure diagnostics print to stdout (Pitfall 2). Empty for
	// failures that emit nothing on stdout; populated only on the
	// non-zero-exit branch of the management classifier.
	Stdout string

	Err error
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
