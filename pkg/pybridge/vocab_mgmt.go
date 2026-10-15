// vocab_mgmt.go implements the vocab_manager.py MANAGEMENT command paths on
// Bridge (Phase 9, VOC-01 — parent vocabulary management):
//   - (*Bridge).RunAdd: invokes the `add` subcommand with a list-mode JSON
//     batch of word entries and parses the {"added": N, "skipped": N}
//     result payload (vocab_manager.py:206).
//   - (*Bridge).RunEdit (Plan 09-03): invokes the `edit` subcommand with
//     --id plus one flag per non-nil *string field (nil → flag omitted,
//     provided-empty clears) and parses the {"edited": {...}} payload.
//   - (*Bridge).RunRenameUnit (Plan 09-03): invokes the hyphenated
//     `rename-unit` subcommand and parses {"renamed", "from", "to"}.
//   - (*Bridge).RunDeleteWords (Plan 09-03): invokes the hyphenated
//     `delete-words` subcommand with exactly one target (--words XOR
//     --unit) and ALWAYS appends --confirm (Pitfall 1 — no bridge code
//     path may trigger the script's non-JSON dry-run preview), parsing
//     {"deleted": N}.
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
//	[vocabScript, "--db", dbPath, <subcommand>, ...]
//
// with `--db` placed BEFORE the subcommand token; argparse would otherwise
// reject `--db` as an unknown subcommand flag. This is the same ordering
// contract documented at vocab.go:15-24. Subcommand tokens are the
// HYPHENATED argparse names (`rename-unit`, `delete-words`) — CONTEXT.md's
// rename_unit/delete_words are the Python function names, not argv tokens.
package pybridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
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

// editArgs builds the argv slice for the vocab_manager.py `edit` subcommand
// (Plan 09-03). The shape is:
//
//	[vocabScript, "--db", dbPath, "edit", "--id", <decimal>, ...flagPairs]
//
// where `--db` is placed BEFORE the `edit` subcommand token (top-level
// argparse, same contract as addArgs) and `--id` is ALWAYS present with the
// decimal value (RunEdit pre-validates ID > 0).
//
// Flag mapping (None-vs-empty contract, Plan 09-02 cmd_edit COALESCE):
//   - opts.Word    -> --word <value>    (omitted when the pointer is nil)
//   - opts.Meaning -> --meaning <value> (omitted when the pointer is nil)
//   - opts.Example -> --example <value> (omitted when the pointer is nil)
//   - opts.Unit    -> --unit <value>    (omitted when the pointer is nil)
//
// A non-nil pointer to the empty string EMITS the flag with an empty value:
// cmd_edit treats an absent flag as "keep the current column" and an
// explicit empty string as "clear the column". Zero-valued (nil) fields
// never emit flags (Shared Pattern 1).
//
// The function performs no I/O and no validation beyond the nil checks --
// it is a pure transform of (cfg, opts) into an argv slice, trivially
// testable without a Python interpreter.
func editArgs(cfg infra.PythonConfig, opts EditOpts) []string {
	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "edit",
		"--id", strconv.FormatInt(opts.ID, 10)}
	if opts.Word != nil {
		args = append(args, "--word", *opts.Word)
	}
	if opts.Meaning != nil {
		args = append(args, "--meaning", *opts.Meaning)
	}
	if opts.Example != nil {
		args = append(args, "--example", *opts.Example)
	}
	if opts.Unit != nil {
		args = append(args, "--unit", *opts.Unit)
	}
	return args
}

// setParticipationArgs builds the argv slice for the vocab_manager.py
// `set-participation` subcommand. The shape is:
//
//	<script> --db <db> set-participation --ids '<json array>' --participates true|false
//
// The ids ride as one JSON array argument (never shell-joined), so one
// batch of any legal size stays one argv.
func setParticipationArgs(cfg infra.PythonConfig, opts SetParticipationOpts) []string {
	raw, _ := json.Marshal(opts.IDs)
	return []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "set-participation",
		"--ids", string(raw), "--participates", strconv.FormatBool(opts.Participates)}
}

// renameUnitArgs builds the argv slice for the vocab_manager.py
// `rename-unit` subcommand (Plan 09-03). The shape is:
//
//	[vocabScript, "--db", dbPath, "rename-unit", "--old-unit", <old>,
//	 "--new-unit", <new>, "--category", <c>]
//
// The subcommand token is the HYPHENATED argparse name "rename-unit"
// (vocab_manager.py:897) -- the Python function name rename_unit is NOT a
// valid argv token and would make argparse exit 2 with "invalid choice".
// All three flags are always present: the script declares --old-unit and
// --new-unit as required=True and RunRenameUnit pre-validates all three
// fields non-empty, so there is no omit-when-empty case here.
func renameUnitArgs(cfg infra.PythonConfig, opts RenameUnitOpts) []string {
	return []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "rename-unit",
		"--old-unit", opts.OldUnit,
		"--new-unit", opts.NewUnit,
		"--category", opts.Category}
}

// deleteWordsArgs builds the argv slice for the vocab_manager.py
// `delete-words` subcommand (Plan 09-03). The shape is one of:
//
//	[vocabScript, "--db", dbPath, "delete-words", "--words", <comma-joined>, "--category", <c>, "--confirm"]
//	[vocabScript, "--db", dbPath, "delete-words", "--unit", <u>, "--category", <c>, "--confirm"]
//
// Target validation mirrors the script's mutually-exclusive argparse group
// and happens BEFORE any argv is constructed:
//   - len(opts.Words) > 0 XOR opts.Unit != "" is required; anything else
//     (no target, or both targets) returns a wrapped validation error and a
//     nil argv.
//   - Word-list mode joins opts.Words with a comma (the script splits the
//     value back into a list, vocab_manager.py cmd_delete_words words mode).
//
// Pitfall 1 (structural dry-run guard): --confirm is appended
// UNCONDITIONALLY on every success path. Without it the script prints a
// non-JSON "Would delete ..." preview and exits 0, which downstream would
// masquerade as an ErrPythonJSONParse crash; the Go layer must never be
// able to produce a dry-run argv. Zero-valued optional fields never emit
// flags (Shared Pattern 1) -- hence exactly one of --words/--unit.
func deleteWordsArgs(cfg infra.PythonConfig, opts DeleteWordsOpts) ([]string, error) {
	hasWords := len(opts.Words) > 0
	hasUnit := opts.Unit != ""
	if !hasWords && !hasUnit {
		return nil, errors.New("deleteWordsArgs: exactly one target required (--words or --unit)")
	}
	if hasWords && hasUnit {
		return nil, errors.New("deleteWordsArgs: --words and --unit are mutually exclusive targets")
	}

	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "delete-words"}
	if hasUnit {
		args = append(args, "--unit", opts.Unit)
	} else {
		args = append(args, "--words", strings.Join(opts.Words, ","))
	}
	args = append(args, "--category", opts.Category)

	// Pitfall 1: ALWAYS confirm. No code path returns without this flag.
	args = append(args, "--confirm")
	return args, nil
}

// classifyMgmtRun is the shared post-run classifier for the vocab_manager.py
// management commands (add / edit / rename-unit / delete-words), extracted
// to a pure function so all sentinel branches are table-testable without a
// subprocess (classifyPregenResult precedent, Plan 03-05; Windows dev hosts
// have no venv for the timeout branch).
//
// Classification rules (in order -- first match wins):
//  1. runErr is context.DeadlineExceeded/Canceled -> ErrPythonTimeout
//     (b.run's Cancel hook already killed the process group).
//  2. exitCode != 0 -> *ExitError wrapping ErrPythonExit. The Stdout field
//     carries the captured stdout bytes (Pitfall 2): rename-unit's "No
//     cards found ..." diagnostic prints to STDOUT with exit 1, and
//     surfacing it lets the API layer map the message to a user-facing
//     not-found response instead of an empty failure. Stderr is captured
//     as before.
//  3. len(stdout) == 0 -> ErrPythonEmpty (the script's contract is
//     "exit 0 implies printable JSON"; an empty success stdout is a
//     bridge-level error, never a silent success — this is also what a
//     stray dry-run would look like, so it must stay loud).
//  4. extractTrailingJSON fail -> ErrPythonJSONParse (Warning-tolerant:
//     the loader's Warning lines precede the JSON on the same stream).
//  5. otherwise -> the extracted trailing JSON bytes with a nil error;
//     the caller unmarshals into its typed result and maps any residual
//     failure to ErrPythonJSONParse.
//
// The function performs no I/O and never logs (logResult owns the
// structured-log side); cmd feeds both the wrap messages and
// ExitError.Cmd.
func classifyMgmtRun(cmd string, stdout, stderr []byte, exitCode int, runErr error) (json.RawMessage, error) {
	if runErr != nil && (errors.Is(runErr, context.DeadlineExceeded) ||
		errors.Is(runErr, context.Canceled)) {
		return nil, errors.Wrap(ErrPythonTimeout, "run "+cmd)
	}
	if exitCode != 0 {
		return nil, &ExitError{
			Cmd:      cmd,
			ExitCode: exitCode,
			Stderr:   string(stderr),
			Stdout:   string(stdout), // Pitfall 2: stdout diagnostics survive
			Err:      ErrPythonExit,
		}
	}
	if len(stdout) == 0 {
		return nil, errors.Wrap(ErrPythonEmpty, "run "+cmd)
	}

	raw, parseErr := extractTrailingJSON(stdout)
	if parseErr != nil {
		return nil, errors.Wrap(ErrPythonJSONParse, "run "+cmd)
	}
	return raw, nil
}

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

	raw, clsErr := classifyMgmtRun("add", stdout, stderr, exitCode, err)
	if clsErr != nil {
		return AddResult{}, clsErr
	}

	var result AddResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return AddResult{}, errors.Wrap(ErrPythonJSONParse, "run add")
	}
	return result, nil
}

// RunEdit executes the vocab_manager.py `edit` subcommand and returns the
// parsed {"edited": {...}} payload (Phase 9, Plan 09-03). It is the entry
// point for the parent page's word-edit form.
//
// Lifecycle (same shape as RunAdd / RunDue):
//  1. Pre-validate opts (ID > 0) BEFORE spawning any subprocess.
//  2. Build argv via editArgs: --id always present; each of --word/
//     --meaning/--example/--unit appears only for non-nil pointers (nil
//     keeps the column; provided-empty clears it — Plan 09-02 contract).
//  3. Delegate to Bridge.run: semaphore (D-02), per-call timeout
//     (cfg.MgmtTimeout), argv slice (no shell, T-03-07), stdout/stderr
//     capture; logResult with command="edit".
//  4. Classify the outcome into the pybridge sentinels (timeout / exit /
//     empty / JSON-parse), exactly like RunAdd.
//
// The script itself never touches FSRS state on this path (D-07): cmd_edit
// updates only the four text columns via COALESCE.
func (b *Bridge) RunEdit(ctx context.Context, opts EditOpts) (EditResult, error) {
	if opts.ID <= 0 {
		return EditResult{}, fmt.Errorf("edit: id must be > 0, got %d", opts.ID)
	}

	start := time.Now()
	args := editArgs(b.cfg, opts)
	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.MgmtTimeout,
		b.cfg.Interpreter, args...)
	defer b.logResult("edit", start, stdout, stderr, exitCode, err)

	raw, clsErr := classifyMgmtRun("edit", stdout, stderr, exitCode, err)
	if clsErr != nil {
		return EditResult{}, clsErr
	}

	var result EditResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return EditResult{}, errors.Wrap(ErrPythonJSONParse, "run edit")
	}
	return result, nil
}

// RunSetParticipation executes the vocab_manager.py `set-participation`
// subcommand (Issue #2) and returns the {requested, updated, missing}
// report. Lifecycle mirrors RunEdit:
//
//  1. Pre-validate opts (1..500 unique positive ids) BEFORE spawning —
//     mirrors the script's own guard so a caller bug never wastes a process
//     (double validation, the repo's established pattern).
//  2. One subprocess, one SQLite BEGIN IMMEDIATE transaction. The script is
//     all-or-nothing: any missing id rolls the whole batch back, exits 1,
//     and prints "card ids not found: ..." to stdout — classifyMgmtRun
//     captures it into ExitError.Stdout so the API layer maps it to a 404
//     with the real ids (Pitfall 2). A nil error therefore ALWAYS means
//     every requested card now carries the requested flag.
//  3. Learning state is never touched: the script writes only the
//     participates column (D-07 kin — proven byte-level by the e2e suite).
func (b *Bridge) RunSetParticipation(ctx context.Context, opts SetParticipationOpts) (SetParticipationResult, error) {
	if err := validateBatchIDs("set-participation", opts.IDs); err != nil {
		return SetParticipationResult{}, err
	}
	start := time.Now()
	args := setParticipationArgs(b.cfg, opts)
	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.MgmtTimeout, b.cfg.Interpreter, args...)
	defer b.logResult("set-participation", start, stdout, stderr, exitCode, err)
	raw, clsErr := classifyMgmtRun("set-participation", stdout, stderr, exitCode, err)
	if clsErr != nil {
		return SetParticipationResult{}, clsErr
	}
	var result SetParticipationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return SetParticipationResult{}, errors.Wrap(ErrPythonJSONParse, "run set-participation")
	}
	return result, nil
}

// RunRenameUnit executes the vocab_manager.py `rename-unit` subcommand and
// returns the parsed {renamed, from, to} report (Plan 09-03). It is the
// entry point for the parent page's unit-rename control.
//
// Lifecycle mirrors RunEdit. Pre-validation requires all three fields
// non-empty (the script independently checks them and would exit 1 with a
// plain-text diagnostic). NOTE (Pitfall 2): when the old unit matches no
// cards, the script prints its "No cards found ..." diagnostic to STDOUT
// and exits 1 — classifyMgmtRun captures that stdout into ExitError.Stdout
// so the API layer can map it to a not-found response with the real
// message.
func (b *Bridge) RunRenameUnit(ctx context.Context, opts RenameUnitOpts) (RenameUnitResult, error) {
	if opts.OldUnit == "" || opts.NewUnit == "" || opts.Category == "" {
		return RenameUnitResult{}, errors.New(
			"rename-unit: oldUnit, newUnit and category are all required")
	}

	start := time.Now()
	args := renameUnitArgs(b.cfg, opts)
	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.MgmtTimeout,
		b.cfg.Interpreter, args...)
	defer b.logResult("rename-unit", start, stdout, stderr, exitCode, err)

	raw, clsErr := classifyMgmtRun("rename-unit", stdout, stderr, exitCode, err)
	if clsErr != nil {
		return RenameUnitResult{}, clsErr
	}

	var result RenameUnitResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return RenameUnitResult{}, errors.Wrap(ErrPythonJSONParse, "run rename-unit")
	}
	return result, nil
}

// RunDeleteWords executes the vocab_manager.py `delete-words` subcommand
// and returns the parsed {"deleted": N} count (Plan 09-03). It is the
// entry point for the parent page's word deletion (single word, multi-word,
// or whole unit).
//
// Lifecycle mirrors RunEdit with two destructive-command specifics:
//  1. Target validation (exactly one of --words/--unit) is delegated to
//     deleteWordsArgs, which fails BEFORE any subprocess spawn when the
//     opts under- or over-specify the target.
//  2. deleteWordsArgs appends --confirm unconditionally (Pitfall 1): the
//     bridge can never trigger the script's non-JSON dry-run preview.
//
// Category is pre-validated non-empty (same rule as RunAdd): an explicit
// empty --category would override the script's "english" default and
// silently match nothing, reporting a misleading success for a destructive
// operation.
func (b *Bridge) RunDeleteWords(ctx context.Context, opts DeleteWordsOpts) (DeleteWordsResult, error) {
	if opts.Category == "" {
		return DeleteWordsResult{}, errors.New("delete-words: category is required")
	}

	start := time.Now()
	args, argErr := deleteWordsArgs(b.cfg, opts)
	if argErr != nil {
		// deleteWordsArgs returns nil argv on validation failure -- no
		// subprocess was spawned; do NOT invoke logResult (no run()
		// happened, no sem held -- same contract as RunAdd).
		return DeleteWordsResult{}, errors.Wrap(argErr, "build delete-words argv")
	}
	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.MgmtTimeout,
		b.cfg.Interpreter, args...)
	defer b.logResult("delete-words", start, stdout, stderr, exitCode, err)

	raw, clsErr := classifyMgmtRun("delete-words", stdout, stderr, exitCode, err)
	if clsErr != nil {
		return DeleteWordsResult{}, clsErr
	}

	var result DeleteWordsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return DeleteWordsResult{}, errors.Wrap(ErrPythonJSONParse, "run delete-words")
	}
	return result, nil
}

// textbookCreateArgs builds the argv slice for the vocab_manager.py
// `textbook-create` subcommand (Issue #4). The shape is:
//
//	[script, "--db", db, "textbook-create", "--name", N, "--category", C,
//	 [--edition E] [--stage S] [--grade G] [--volume V] [--module M],
//	 "--lessons", '<json array>']
//
// --lessons ALWAYS rides as one JSON-array argv element (the --ids
// precedent) — empty metadata flags are omitted, but the lesson list is
// always present so the script's default ("") and an explicit empty array
// are indistinguishable by design (both mean "no lessons yet").
func textbookCreateArgs(cfg infra.PythonConfig, opts TextbookCreateOpts) []string {
	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "textbook-create",
		"--name", opts.Name, "--category", opts.Category}
	for _, m := range []struct{ flag, value string }{
		{"edition", opts.Edition}, {"stage", opts.Stage}, {"grade", opts.Grade},
		{"volume", opts.Volume}, {"module", opts.Module},
	} {
		if m.value != "" {
			args = append(args, "--"+m.flag, m.value)
		}
	}
	lessons := opts.Lessons
	if lessons == nil {
		lessons = []string{}
	}
	raw, _ := json.Marshal(lessons)
	return append(args, "--lessons", string(raw))
}

// textbookUpdateArgs builds the argv slice for `textbook-update` (Issue
// #4). Nil pointer fields omit their flags entirely so the script keeps
// the column — editArgs' nil contract, one shape.
func textbookUpdateArgs(cfg infra.PythonConfig, opts TextbookUpdateOpts) []string {
	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "textbook-update",
		"--id", strconv.FormatInt(opts.ID, 10)}
	if opts.Name != nil {
		args = append(args, "--name", *opts.Name)
	}
	for _, m := range []struct {
		value *string
		flag  string
	}{
		{value: opts.Edition, flag: "edition"},
		{value: opts.Stage, flag: "stage"},
		{value: opts.Grade, flag: "grade"},
		{value: opts.Volume, flag: "volume"},
		{value: opts.Module, flag: "module"},
	} {
		if m.value != nil {
			args = append(args, "--"+m.flag, *m.value)
		}
	}
	if opts.Lessons != nil {
		raw, _ := json.Marshal(opts.Lessons)
		args = append(args, "--lessons", string(raw))
	}
	if opts.LearnedLessons != nil {
		args = append(args, "--learned-lessons", strconv.Itoa(*opts.LearnedLessons))
	}
	if opts.SetPrimary {
		args = append(args, "--set-primary")
	}
	return args
}

// assignWordsArgs builds the argv slice for `assign-words` (Issue #4):
//
//	[script, "--db", db, "assign-words", "--textbook-id", T, "--ids", '<json>',
//	 "--lesson", L, ["--include-in-scope"]]
//
// --lesson is ALWAYS present (empty string = 待确认) so a batch never
// accidentally rides the argparse default.
func assignWordsArgs(cfg infra.PythonConfig, opts AssignWordsOpts) []string {
	raw, _ := json.Marshal(opts.IDs)
	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "assign-words",
		"--textbook-id", strconv.FormatInt(opts.TextbookID, 10),
		"--ids", string(raw), "--lesson", opts.Lesson}
	if opts.IncludeInScope {
		args = append(args, "--include-in-scope")
	}
	return args
}

// unassignWordsArgs builds the argv slice for `unassign-words` (Issue #4).
func unassignWordsArgs(cfg infra.PythonConfig, opts UnassignWordsOpts) []string {
	raw, _ := json.Marshal(opts.IDs)
	return []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "unassign-words",
		"--textbook-id", strconv.FormatInt(opts.TextbookID, 10),
		"--ids", string(raw)}
}

// validateBatchIDs enforces the shared 1..500-unique-positive ids contract
// (Issue #2's set-participation shape — assign-words/unassign-words reuse
// it verbatim so every batch command speaks the same cap).
func validateBatchIDs(op string, ids []int64) error {
	if len(ids) == 0 || len(ids) > 500 {
		return fmt.Errorf("%s: ids must contain 1..500 values", op)
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return fmt.Errorf("%s: id must be > 0, got %d", op, id)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%s: duplicate id %d", op, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// runMgmt is the shared mgmt-command lifecycle after argv building: run
// one subprocess under MgmtTimeout, log it, classify into the pybridge
// sentinels, unmarshal the trailing JSON. The Issue #4 textbook Run*
// methods all ride it (the older RunEdit-family commands keep their
// inline shape).
func runMgmt[T any](b *Bridge, ctx context.Context, cmd string, args []string) (T, error) {
	start := time.Now()
	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.MgmtTimeout, b.cfg.Interpreter, args...)
	defer b.logResult(cmd, start, stdout, stderr, exitCode, err)
	raw, clsErr := classifyMgmtRun(cmd, stdout, stderr, exitCode, err)
	if clsErr != nil {
		var zero T
		return zero, clsErr
	}
	var result T
	if err := json.Unmarshal(raw, &result); err != nil {
		var zero T
		return zero, errors.Wrap(ErrPythonJSONParse, "run "+cmd)
	}
	return result, nil
}

// RunTextbookCreate executes the vocab_manager.py `textbook-create`
// subcommand (Issue #4). Pre-validation mirrors the script's own guards
// (name/category) so a caller bug never wastes a subprocess.
func (b *Bridge) RunTextbookCreate(ctx context.Context, opts TextbookCreateOpts) (TextbookMutationResult, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return TextbookMutationResult{}, errors.New("textbook-create: name is required")
	}
	if opts.Category != "english" && opts.Category != "chinese" {
		return TextbookMutationResult{}, fmt.Errorf(
			"textbook-create: category must be english or chinese, got %q", opts.Category)
	}
	return runMgmt[TextbookMutationResult](b, ctx, "textbook-create", textbookCreateArgs(b.cfg, opts))
}

// RunTextbookUpdate executes the vocab_manager.py `textbook-update`
// subcommand (Issue #4). At least one change must be set (the script
// would no-op otherwise — surfaced here as a caller bug, not a silent
// round trip).
func (b *Bridge) RunTextbookUpdate(ctx context.Context, opts TextbookUpdateOpts) (TextbookMutationResult, error) {
	if opts.ID <= 0 {
		return TextbookMutationResult{}, fmt.Errorf("textbook-update: id must be > 0, got %d", opts.ID)
	}
	if opts.Name == nil && opts.Edition == nil && opts.Stage == nil && opts.Grade == nil &&
		opts.Volume == nil && opts.Module == nil && opts.Lessons == nil &&
		opts.LearnedLessons == nil && !opts.SetPrimary {
		return TextbookMutationResult{}, errors.New("textbook-update: at least one change is required")
	}
	return runMgmt[TextbookMutationResult](b, ctx, "textbook-update", textbookUpdateArgs(b.cfg, opts))
}

// RunAssignWords executes the vocab_manager.py `assign-words` subcommand
// (Issue #4). All-or-nothing: the script's not-found stdout diagnostic
// maps to 404 upstream (same Pitfall-2 chain as set-participation).
func (b *Bridge) RunAssignWords(ctx context.Context, opts AssignWordsOpts) (AssignWordsResult, error) {
	if opts.TextbookID <= 0 {
		return AssignWordsResult{}, fmt.Errorf("assign-words: textbook id must be > 0, got %d", opts.TextbookID)
	}
	if err := validateBatchIDs("assign-words", opts.IDs); err != nil {
		return AssignWordsResult{}, err
	}
	return runMgmt[AssignWordsResult](b, ctx, "assign-words", assignWordsArgs(b.cfg, opts))
}

// RunUnassignWords executes the vocab_manager.py `unassign-words`
// subcommand (Issue #4).
func (b *Bridge) RunUnassignWords(ctx context.Context, opts UnassignWordsOpts) (UnassignWordsResult, error) {
	if opts.TextbookID <= 0 {
		return UnassignWordsResult{}, fmt.Errorf("unassign-words: textbook id must be > 0, got %d", opts.TextbookID)
	}
	if err := validateBatchIDs("unassign-words", opts.IDs); err != nil {
		return UnassignWordsResult{}, err
	}
	return runMgmt[UnassignWordsResult](b, ctx, "unassign-words", unassignWordsArgs(b.cfg, opts))
}
