// vocab_mgmt_test.go exercises the Phase 9 management argv builders
// (addArgs / editArgs / renameUnitArgs / deleteWordsArgs) and the
// management-command sentinel classifier declared in vocab_mgmt.go.
//
// Coverage (Plan 09-01 Task 2 + Plan 09-03 Task 1/Task 2 behavior blocks):
//   - TestAddArgs: argv contract [VocabScript, --db, VocabDBPath, add],
//     --category always present, --unit omitted-when-empty, list-mode
//     JSON payload carrying word/meaning/example keys, and the
//     inline --words vs temp-file --file threshold (mirrors
//     TestReviewArgs in vocab_test.go).
//   - TestEditArgs: [.., edit] head, --id always present (decimal),
//     pointer fields emit flag+value ONLY when non-nil (nil -> flag
//     absent; non-nil empty string -> flag with empty value, the
//     None-vs-empty clear contract from Plan 09-02).
//   - TestRenameUnitArgs: hyphenated rename-unit token with
//     --old-unit/--new-unit/--category, --db before the subcommand.
//   - TestDeleteWordsArgs: hyphenated delete-words token, --confirm in
//     EVERY success case (Pitfall 1 — no dry-run argv possible), exactly
//     one target (--words XOR --unit), comma-joined word list.
//   - TestClassifyMgmtRun: the four sentinel classes (timeout / exit with
//     Stdout diagnostics / empty stdout / garbage stdout) table-driven
//     without a subprocess (classifyPregenResult precedent, Plan 03-05).
//
// Test framework: stdlib `testing` only, table-driven + t.Run subtests.
// Reuses argvFind / assertDBBeforeSubcommand from vocab_test.go (same
// package).
package pybridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

func TestAddArgs(t *testing.T) {
	cfg := infra.PythonConfig{
		VocabScript: "vocab_manager.py",
		VocabDBPath: "/var/lib/hta/vocab.db",
	}

	t.Run("argv starts with script --db dbPath add", func(t *testing.T) {
		argv, cleanup, err := addArgs(cfg, AddOpts{
			Category: "english",
			Entries:  []AddEntry{{Word: "hello"}},
		})
		if err != nil {
			t.Fatalf("addArgs returned unexpected error: %v", err)
		}
		defer cleanup()

		if want := "vocab_manager.py"; argv[0] != want {
			t.Errorf("argv[0] = %q, want %q", argv[0], want)
		}
		if got := argv[2]; got != cfg.VocabDBPath {
			t.Errorf("argv[2] = %q, want db path %q", got, cfg.VocabDBPath)
		}
		assertDBBeforeSubcommand(t, argv, "add")
	})

	t.Run("category always present", func(t *testing.T) {
		argv, cleanup, err := addArgs(cfg, AddOpts{
			Category: "chinese",
			Entries:  []AddEntry{{Word: "你好"}},
		})
		if err != nil {
			t.Fatalf("addArgs returned unexpected error: %v", err)
		}
		defer cleanup()

		idx := argvFind(argv, "--category")
		if idx < 0 {
			t.Fatalf("argv %v missing --category (always-present contract)", argv)
		}
		if got := argv[idx+1]; got != "chinese" {
			t.Errorf("--category value = %q, want %q", got, "chinese")
		}
	})

	t.Run("unit omitted when empty", func(t *testing.T) {
		argv, cleanup, err := addArgs(cfg, AddOpts{
			Category: "english",
			Entries:  []AddEntry{{Word: "hello"}},
		})
		if err != nil {
			t.Fatalf("addArgs returned unexpected error: %v", err)
		}
		defer cleanup()

		if idx := argvFind(argv, "--unit"); idx >= 0 {
			t.Errorf("argv %v has --unit for empty opts.Unit (omit-when-empty contract)", argv)
		}
	})

	t.Run("unit present when set", func(t *testing.T) {
		argv, cleanup, err := addArgs(cfg, AddOpts{
			Category: "english",
			Unit:     "L10",
			Entries:  []AddEntry{{Word: "hello"}},
		})
		if err != nil {
			t.Fatalf("addArgs returned unexpected error: %v", err)
		}
		defer cleanup()

		idx := argvFind(argv, "--unit")
		if idx < 0 {
			t.Fatalf("argv %v missing --unit for opts.Unit=%q", argv, "L10")
		}
		if got := argv[idx+1]; got != "L10" {
			t.Errorf("--unit value = %q, want %q", got, "L10")
		}
	})

	t.Run("payload is list-mode JSON with word/meaning/example keys", func(t *testing.T) {
		// RESEARCH Pitfall 4 acceptance: entries MUST marshal as a JSON
		// ARRAY of objects so the example field survives (dict mode would
		// drop it — vocab_manager.py:166-179 reads word/meaning/example).
		argv, cleanup, err := addArgs(cfg, AddOpts{
			Category: "english",
			Entries: []AddEntry{
				{Word: "apple", Meaning: "苹果", Example: "I eat an apple."},
			},
		})
		if err != nil {
			t.Fatalf("addArgs returned unexpected error: %v", err)
		}
		defer cleanup()

		idx := argvFind(argv, "--words")
		if idx < 0 {
			t.Fatalf("argv %v missing --words for small payload", argv)
		}
		var decoded []map[string]string
		if err := json.Unmarshal([]byte(argv[idx+1]), &decoded); err != nil {
			t.Fatalf("--words value not valid JSON: %v (val=%q)", err, argv[idx+1])
		}
		if len(decoded) != 1 {
			t.Fatalf("decoded %d entries, want 1 (list-mode array)", len(decoded))
		}
		for _, key := range []string{"word", "meaning", "example"} {
			if _, ok := decoded[0][key]; !ok {
				t.Errorf("decoded entry missing key %q (list-mode contract)", key)
			}
		}
		if decoded[0]["word"] != "apple" || decoded[0]["meaning"] != "苹果" ||
			decoded[0]["example"] != "I eat an apple." {
			t.Errorf("decoded entry = %v, want the original AddEntry fields", decoded[0])
		}
	})

	t.Run("small payload uses --words with no-op cleanup", func(t *testing.T) {
		// 100 single-char entries marshal to ~3.9KB, under the 4096-byte
		// cap — this isolates the entry-count condition (100 == cap).
		entries := make([]AddEntry, 0, addInlineEntryCap)
		for i := 0; i < addInlineEntryCap; i++ {
			entries = append(entries, AddEntry{Word: "w"})
		}
		argv, cleanup, err := addArgs(cfg, AddOpts{Category: "english", Entries: entries})
		if err != nil {
			t.Fatalf("addArgs returned unexpected error: %v", err)
		}

		if idx := argvFind(argv, "--words"); idx < 0 {
			t.Fatalf("argv %v missing --words for %d-entry payload (<= cap %d)",
				argv, len(entries), addInlineEntryCap)
		}
		if idx := argvFind(argv, "--file"); idx >= 0 {
			t.Errorf("argv %v has --file for small payload, want inline --words only", argv)
		}
		// cleanup must be a safe no-op (calling it twice must not panic —
		// reviewArgsCleanupIdempotent precedent).
		cleanup()
		cleanup()
	})

	t.Run("large payload by entry count goes to temp file", func(t *testing.T) {
		// 101 two-char entries marshal to ~4.0KB, under the byte cap — the
		// entry count alone (101 > 100) must force the temp file.
		entries := make([]AddEntry, 0, addInlineEntryCap+1)
		for i := 0; i < addInlineEntryCap+1; i++ {
			entries = append(entries, AddEntry{Word: "ww"})
		}
		argv, cleanup, err := addArgs(cfg, AddOpts{Category: "english", Entries: entries})
		if err != nil {
			t.Fatalf("addArgs returned unexpected error: %v", err)
		}

		if idx := argvFind(argv, "--file"); idx < 0 {
			t.Fatalf("argv %v missing --file for %d-entry payload (> cap %d)",
				argv, len(entries), addInlineEntryCap)
		}
		if idx := argvFind(argv, "--words"); idx >= 0 {
			t.Errorf("argv %v has --words for large payload, want --file only", argv)
		}

		// The --file path must be a real file on disk that cleanup removes.
		path := argv[argvFind(argv, "--file")+1]
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("--file path %q does not exist before cleanup: %v", path, err)
		}
		if info.Size() == 0 {
			t.Errorf("--file %q is empty, want the marshalled JSON payload", path)
		}
		cleanup()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("--file %q still exists after cleanup, want removed: err=%v", path, err)
		}
	})

	t.Run("large payload by byte size goes to temp file", func(t *testing.T) {
		// A small entry count with very long fields still trips the byte cap
		// (same argv-length defence as the review path).
		entries := []AddEntry{
			{Word: strings.Repeat("w", addInlineByteCap), Example: strings.Repeat("e", 64)},
		}
		argv, cleanup, err := addArgs(cfg, AddOpts{Category: "english", Entries: entries})
		if err != nil {
			t.Fatalf("addArgs returned unexpected error: %v", err)
		}
		defer cleanup()

		if idx := argvFind(argv, "--file"); idx < 0 {
			t.Errorf("argv %v missing --file for payload > %d bytes", argv, addInlineByteCap)
		}
		if idx := argvFind(argv, "--words"); idx >= 0 {
			t.Errorf("argv %v has --words for large byte-size payload, want --file only", argv)
		}
	})
}

// ---------------------------------------------------------------------------
// Plan 09-03 Task 1: argv builders for edit / rename-unit / delete-words.
// ---------------------------------------------------------------------------

// strPtr is a test helper wrapping a string literal in a pointer so table
// rows can express "field provided" (non-nil) vs "field absent" (nil) for
// EditOpts's optional *string fields.
func strPtr(s string) *string { return &s }

// assertNoInlinePayload is the Plan 09-03 inline-payload absence check: none
// of the edit / rename-unit / delete-words commands carry JSON payloads --
// they are flags-only commands (delete-words' --words is a plain
// comma-joined list, not JSON). The temp-file path (--file) and the JSON
// batch flags used by add/review (--file/--results) must therefore never
// appear.
func assertNoInlinePayload(t *testing.T, argv []string, cmd string) {
	t.Helper()
	for _, banned := range []string{"--file", "--results"} {
		if idx := argvFind(argv, banned); idx >= 0 {
			t.Errorf("%s argv %v unexpectedly contains %q (flags-only command, no payload file)",
				cmd, argv, banned)
		}
	}
}

// TestEditArgs covers editArgs argv construction (Plan 09-03 Task 1):
// [VocabScript, --db, VocabDBPath, edit, --id <decimal>, ...flag pairs] where
// each of --word/--meaning/--example/--unit appears with a value ONLY when
// the corresponding *string field is non-nil. A non-nil pointer to the empty
// string MUST emit the flag with an empty value -- that is the
// provided-empty-clears contract from the Plan 09-02 CLI (cmd_edit's
// COALESCE treats an absent flag as keep and an explicit empty string as
// clear).
func TestEditArgs(t *testing.T) {
	cfg := infra.PythonConfig{
		VocabScript: "vocab_manager.py",
		VocabDBPath: "/var/lib/hta/vocab.db",
	}

	cases := []struct {
		name    string
		opts    EditOpts
		present map[string]string // flag -> expected value
		absent  []string          // flags that must NOT appear
	}{
		{
			name:    "all pointers nil emits the minimal edit --id argv",
			opts:    EditOpts{ID: 7},
			present: map[string]string{"--id": "7"},
			absent:  []string{"--word", "--meaning", "--example", "--unit", "--participates"},
		},
		{
			name: "word only",
			opts: EditOpts{ID: 42, Word: strPtr("banana")},
			present: map[string]string{
				"--id":   "42",
				"--word": "banana",
			},
			absent: []string{"--meaning", "--example", "--unit"},
		},
		{
			name: "explicit empty string clears meaning (flag emitted with empty value)",
			opts: EditOpts{ID: 1, Meaning: strPtr("")},
			present: map[string]string{
				"--id":      "1",
				"--meaning": "",
			},
			absent: []string{"--word", "--example", "--unit"},
		},
		{
			name: "all four fields set",
			opts: EditOpts{
				ID:      3,
				Word:    strPtr("pear"),
				Meaning: strPtr("梨"),
				Example: strPtr("I eat a pear."),
				Unit:    strPtr("U2"),
			},
			present: map[string]string{
				"--id":      "3",
				"--word":    "pear",
				"--meaning": "梨",
				"--example": "I eat a pear.",
				"--unit":    "U2",
			},
			absent: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := editArgs(cfg, tc.opts)

			// Global invariant: argv[0]=script, argv[1]=--db, argv[2]=dbPath,
			// argv[3]="edit" (vocab_manager.py top-level --db contract).
			if want := "vocab_manager.py"; argv[0] != want {
				t.Errorf("argv[0] = %q, want %q", argv[0], want)
			}
			assertDBBeforeSubcommand(t, argv, "edit")

			for flag, want := range tc.present {
				idx := argvFind(argv, flag)
				if idx < 0 {
					t.Errorf("argv %v missing %q (want value %q)", argv, flag, want)
					continue
				}
				if idx+1 >= len(argv) {
					t.Errorf("argv %v: %q present but has no value", argv, flag)
					continue
				}
				if got := argv[idx+1]; got != want {
					t.Errorf("%s value = %q, want %q", flag, got, want)
				}
			}
			for _, flag := range tc.absent {
				if idx := argvFind(argv, flag); idx >= 0 {
					t.Errorf("argv %v unexpectedly contains %q "+
						"(nil pointer field must omit the flag)", argv, flag)
				}
			}

			// --id is ALWAYS present with the decimal value, in every row.
			idx := argvFind(argv, "--id")
			if idx < 0 {
				t.Errorf("argv %v missing --id (always-present contract)", argv)
			} else if got := argv[idx+1]; got != strconv.FormatInt(tc.opts.ID, 10) {
				t.Errorf("--id value = %q, want decimal %q", got, strconv.FormatInt(tc.opts.ID, 10))
			}

			assertNoInlinePayload(t, argv, "edit")
			if idx := argvFind(argv, "--words"); idx >= 0 {
				t.Errorf("edit argv %v unexpectedly contains --words (flags-only command)", argv)
			}
		})
	}
}

func TestSetParticipationArgs(t *testing.T) {
	cfg := infra.PythonConfig{VocabScript: "vocab_manager.py", VocabDBPath: "vocab.db"}
	argv := setParticipationArgs(cfg, SetParticipationOpts{IDs: []int64{4, 9}, Participates: false})
	assertDBBeforeSubcommand(t, argv, "set-participation")
	for flag, want := range map[string]string{"--ids": "[4,9]", "--participates": "false"} {
		idx := argvFind(argv, flag)
		if idx < 0 || idx+1 >= len(argv) || argv[idx+1] != want {
			t.Fatalf("argv %v: %s = %q, want %q", argv, flag, argv[idx+1], want)
		}
	}
}

// TestRenameUnitArgs covers renameUnitArgs argv construction (Plan 09-03
// Task 1): the subcommand token is the HYPHENATED "rename-unit" (the Python
// function name rename_unit is NOT the argv token — vocab_manager.py:897),
// and --old-unit/--new-unit/--category are all present with the given
// values, with --db before the subcommand.
func TestRenameUnitArgs(t *testing.T) {
	cfg := infra.PythonConfig{
		VocabScript: "vocab_manager.py",
		VocabDBPath: "/var/lib/hta/vocab.db",
	}

	t.Run("hyphenated subcommand token with all three flags", func(t *testing.T) {
		argv := renameUnitArgs(cfg, RenameUnitOpts{
			OldUnit:  "old-x",
			NewUnit:  "new-x",
			Category: "english",
		})

		if want := "vocab_manager.py"; argv[0] != want {
			t.Errorf("argv[0] = %q, want %q", argv[0], want)
		}
		// assertDBBeforeSubcommand pins argv[3]: the hyphenated token, NOT
		// rename_unit (CONTEXT.md's underscore form is the Python function
		// name and would make argparse exit 2 "invalid choice").
		assertDBBeforeSubcommand(t, argv, "rename-unit")

		for flag, want := range map[string]string{
			"--old-unit": "old-x",
			"--new-unit": "new-x",
			"--category": "english",
		} {
			idx := argvFind(argv, flag)
			if idx < 0 {
				t.Errorf("argv %v missing %q", argv, flag)
				continue
			}
			if got := argv[idx+1]; got != want {
				t.Errorf("%s value = %q, want %q", flag, got, want)
			}
		}

		assertNoInlinePayload(t, argv, "rename-unit")
		if idx := argvFind(argv, "--words"); idx >= 0 {
			t.Errorf("rename-unit argv %v unexpectedly contains --words (flags-only command)", argv)
		}
	})
}

// TestDeleteWordsArgs covers deleteWordsArgs argv construction (Plan 09-03
// Task 1): hyphenated delete-words token, exactly one target (--words comma-
// joined XOR --unit), --category present, and --confirm present in EVERY
// success case (Pitfall 1: without --confirm the script prints a non-JSON
// preview and exits 0, which would masquerade as a parse crash downstream).
// Both under-specified (no target) and over-specified (both targets) opts
// return a validation error BEFORE any argv is constructed.
func TestDeleteWordsArgs(t *testing.T) {
	cfg := infra.PythonConfig{
		VocabScript: "vocab_manager.py",
		VocabDBPath: "/var/lib/hta/vocab.db",
	}

	cases := []struct {
		name        string
		opts        DeleteWordsOpts
		wantErr     bool
		presentKV   map[string]string // flag -> expected value
		presentFlag []string          // valueless flags (e.g. --confirm)
		absent      []string          // flags that must NOT appear
	}{
		{
			name:    "no target at all is a validation error",
			opts:    DeleteWordsOpts{Category: "english"},
			wantErr: true,
		},
		{
			name:    "words and unit together are mutually exclusive",
			opts:    DeleteWordsOpts{Words: []string{"apple"}, Unit: "L10", Category: "english"},
			wantErr: true,
		},
		{
			name: "words mode joins with commas and always confirms",
			opts: DeleteWordsOpts{
				Words:    []string{"apple", "banana", "cherry"},
				Category: "english",
			},
			presentKV: map[string]string{
				"--words":    "apple,banana,cherry",
				"--category": "english",
			},
			presentFlag: []string{"--confirm"},
			absent:      []string{"--unit"},
		},
		{
			name: "unit mode targets the unit and always confirms",
			opts: DeleteWordsOpts{
				Unit:     "L10",
				Category: "chinese",
			},
			presentKV: map[string]string{
				"--unit":     "L10",
				"--category": "chinese",
			},
			presentFlag: []string{"--confirm"},
			absent:      []string{"--words"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv, err := deleteWordsArgs(cfg, tc.opts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("deleteWordsArgs(%+v) = %v, want validation error", tc.opts, argv)
				}
				if argv != nil {
					t.Errorf("deleteWordsArgs returned argv %v alongside error %v, want nil argv", argv, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("deleteWordsArgs returned unexpected error: %v", err)
			}

			if want := "vocab_manager.py"; argv[0] != want {
				t.Errorf("argv[0] = %q, want %q", argv[0], want)
			}
			assertDBBeforeSubcommand(t, argv, "delete-words")

			for flag, want := range tc.presentKV {
				idx := argvFind(argv, flag)
				if idx < 0 {
					t.Errorf("argv %v missing %q (want value %q)", argv, flag, want)
					continue
				}
				if got := argv[idx+1]; got != want {
					t.Errorf("%s value = %q, want %q", flag, got, want)
				}
			}
			for _, flag := range tc.presentFlag {
				if idx := argvFind(argv, flag); idx < 0 {
					// Pitfall 1 acceptance: EVERY success row must carry
					// --confirm — a missing flag here means the Go layer
					// can trigger the script's non-JSON dry-run preview.
					t.Errorf("argv %v missing %q (Pitfall 1: delete MUST always confirm)", argv, flag)
				}
			}
			for _, flag := range tc.absent {
				if idx := argvFind(argv, flag); idx >= 0 {
					t.Errorf("argv %v unexpectedly contains %q "+
						"(delete-words takes exactly one target)", argv, flag)
				}
			}

			assertNoInlinePayload(t, argv, "delete-words")
		})
	}
}

// ---------------------------------------------------------------------------
// Plan 09-03 Task 2: shared sentinel classification for the management
// commands (the classifyPregenResult precedent, Plan 03-05 — pure function
// so all four sentinel branches are table-testable without a subprocess).
// ---------------------------------------------------------------------------

// TestClassifyMgmtRun covers classifyMgmtRun, the post-run classifier shared
// by the vocab_manager.py management commands (add/edit/rename-unit/
// delete-words):
//   - context.DeadlineExceeded/Canceled -> ErrPythonTimeout
//   - exitCode != 0 -> *ExitError wrapping ErrPythonExit whose Stdout field
//     carries the captured stdout bytes (Pitfall 2: rename-unit's "No cards
//     found ..." diagnostic prints to STDOUT with exit 1)
//   - empty stdout on exit 0 -> ErrPythonEmpty
//   - garbage stdout -> ErrPythonJSONParse
//   - otherwise -> the extracted trailing JSON bytes, nil error
func TestClassifyMgmtRun(t *testing.T) {
	cases := []struct {
		name         string
		stdout       []byte
		stderr       []byte
		exitCode     int
		runErr       error
		wantSentinel error // sentinel the returned error must wrap; nil = success
		wantExit     bool  // expect a recoverable *ExitError (errors.As)
		wantStdout   string
		wantRaw      string
	}{
		{
			name:         "context deadline maps to ErrPythonTimeout",
			runErr:       context.DeadlineExceeded,
			wantSentinel: ErrPythonTimeout,
		},
		{
			name:         "context canceled maps to ErrPythonTimeout",
			runErr:       context.Canceled,
			wantSentinel: ErrPythonTimeout,
		},
		{
			name:         "nonzero exit wraps ErrPythonExit carrying stdout (Pitfall 2)",
			stdout:       []byte("No cards found with unit 'old-x' in category 'english'"),
			stderr:       []byte(""),
			exitCode:     1,
			wantSentinel: ErrPythonExit,
			wantExit:     true,
			wantStdout:   "No cards found with unit 'old-x' in category 'english'",
		},
		{
			name:         "empty stdout on exit 0 maps to ErrPythonEmpty",
			stdout:       nil,
			exitCode:     0,
			wantSentinel: ErrPythonEmpty,
		},
		{
			name:         "garbage stdout maps to ErrPythonJSONParse",
			stdout:       []byte("Traceback (most recent call last):"),
			exitCode:     0,
			wantSentinel: ErrPythonJSONParse,
		},
		{
			name:       "warning-prefixed success returns the trailing JSON",
			stdout:     []byte("Warning: invalid fsrs_config.json\n{\"deleted\": 2}"),
			exitCode:   0,
			wantRaw:    `{"deleted": 2}`,
			wantStdout: "", // no ExitError on success
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := classifyMgmtRun("rename-unit", tc.stdout, tc.stderr, tc.exitCode, tc.runErr)

			if tc.wantSentinel != nil {
				if err == nil {
					t.Fatalf("classifyMgmtRun(%q) = %s, nil error, want sentinel %v",
						tc.stdout, raw, tc.wantSentinel)
				}
				if !errors.Is(err, tc.wantSentinel) {
					t.Fatalf("err = %v, want errors.Is %v", err, tc.wantSentinel)
				}
				if raw != nil {
					t.Errorf("raw = %s on an error path, want nil", raw)
				}
			} else if err != nil {
				t.Fatalf("classifyMgmtRun(%q) returned unexpected error: %v", tc.stdout, err)
			}

			if tc.wantExit {
				var ee *ExitError
				if !errors.As(err, &ee) {
					t.Fatalf("err = %v, want a recoverable *ExitError via errors.As", err)
				}
				if ee.Cmd != "rename-unit" {
					t.Errorf("ExitError.Cmd = %q, want %q", ee.Cmd, "rename-unit")
				}
				if ee.ExitCode != tc.exitCode {
					t.Errorf("ExitError.ExitCode = %d, want %d", ee.ExitCode, tc.exitCode)
				}
				if ee.Stdout != tc.wantStdout {
					// Pitfall 2 acceptance: the captured stdout bytes MUST
					// ride in ExitError.Stdout so the API layer can map the
					// script's stdout diagnostics to a user-facing message.
					t.Errorf("ExitError.Stdout = %q, want %q", ee.Stdout, tc.wantStdout)
				}
				if !errors.Is(ee, ErrPythonExit) {
					t.Errorf("ExitError.Err does not unwrap to ErrPythonExit")
				}
			}

			if tc.wantRaw != "" && string(raw) != tc.wantRaw {
				t.Errorf("raw = %s, want %s", raw, tc.wantRaw)
			}
		})
	}
}

// TestTextbookCreateArgs pins the textbook-create argv contract (Issue #4):
// --db before the hyphenated subcommand, --name/--category always present,
// empty metadata flags omitted, --lessons always carried as ONE JSON-array
// argv element (the --ids precedent — never shell-joined).
func TestTextbookCreateArgs(t *testing.T) {
	cfg := infra.PythonConfig{VocabScript: "vocab_manager.py", VocabDBPath: "vocab.db"}
	argv := textbookCreateArgs(cfg, TextbookCreateOpts{
		Name: "人教版PEP", Category: "english", Edition: "人教版",
		Lessons: []string{"第3课", "第1课", "第2课"},
	})
	assertDBBeforeSubcommand(t, argv, "textbook-create")
	for flag, want := range map[string]string{
		"--name":     "人教版PEP",
		"--category": "english",
		"--edition":  "人教版",
		"--lessons":  `["第3课","第1课","第2课"]`,
	} {
		idx := argvFind(argv, flag)
		if idx < 0 || idx+1 >= len(argv) || argv[idx+1] != want {
			t.Fatalf("argv %v: %s = %q, want %q", argv, flag, argvValue(argv, flag), want)
		}
	}
	for _, absent := range []string{"--stage", "--grade", "--volume", "--module"} {
		if argvFind(argv, absent) >= 0 {
			t.Fatalf("argv %v: %s present for an empty field, want omitted", argv, absent)
		}
	}
	// Nil lessons still carry an explicit empty array (indistinguishable
	// from "no lessons yet" by design).
	argv2 := textbookCreateArgs(cfg, TextbookCreateOpts{Name: "X", Category: "chinese"})
	if got := argvValue(argv2, "--lessons"); got != "[]" {
		t.Fatalf("nil lessons argv --lessons = %q, want []", got)
	}
}

// TestTextbookUpdateArgs pins the gated-field contract: every zero-valued
// field omits its flag (the script keeps the column), gates force presence.
func TestTextbookUpdateArgs(t *testing.T) {
	cfg := infra.PythonConfig{VocabScript: "vocab_manager.py", VocabDBPath: "vocab.db"}
	argv := textbookUpdateArgs(cfg, TextbookUpdateOpts{ID: 7})
	assertDBBeforeSubcommand(t, argv, "textbook-update")
	if got := argvValue(argv, "--id"); got != "7" {
		t.Fatalf("--id = %q, want 7", got)
	}
	for _, absent := range []string{"--name", "--lessons", "--learned-lessons", "--set-primary", "--edition"} {
		if argvFind(argv, absent) >= 0 {
			t.Fatalf("argv %v: %s present without a change, want omitted", argv, absent)
		}
	}

	argv = textbookUpdateArgs(cfg, TextbookUpdateOpts{
		ID: 7, HasLearnedLessons: true, LearnedLessons: 2, SetPrimary: true,
		HasLessons: true, Lessons: []string{"A课", "B课"}, Grade: "三年级",
	})
	if got := argvValue(argv, "--learned-lessons"); got != "2" {
		t.Fatalf("--learned-lessons = %q, want 2", got)
	}
	if got := argvValue(argv, "--lessons"); got != `["A课","B课"]` {
		t.Fatalf("--lessons = %q, want the JSON array", got)
	}
	if got := argvValue(argv, "--grade"); got != "三年级" {
		t.Fatalf("--grade = %q, want 三年级", got)
	}
	if argvFind(argv, "--set-primary") < 0 {
		t.Fatalf("argv %v: --set-primary missing", argv)
	}
}

// TestAssignWordsArgs pins the assign/unassign argv contract: --lesson is
// ALWAYS present (empty string = 待确认), --include-in-scope only when
// set, ids ride as one JSON argv element.
func TestAssignWordsArgs(t *testing.T) {
	cfg := infra.PythonConfig{VocabScript: "vocab_manager.py", VocabDBPath: "vocab.db"}
	argv := assignWordsArgs(cfg, AssignWordsOpts{TextbookID: 3, IDs: []int64{11, 12}})
	assertDBBeforeSubcommand(t, argv, "assign-words")
	for flag, want := range map[string]string{
		"--textbook-id": "3",
		"--ids":         "[11,12]",
		"--lesson":      "",
	} {
		idx := argvFind(argv, flag)
		if idx < 0 || idx+1 >= len(argv) || argv[idx+1] != want {
			t.Fatalf("argv %v: %s = %q, want %q", argv, flag, argvValue(argv, flag), want)
		}
	}
	if argvFind(argv, "--include-in-scope") >= 0 {
		t.Fatalf("argv %v: --include-in-scope present without the flag", argv)
	}

	argv = assignWordsArgs(cfg, AssignWordsOpts{
		TextbookID: 3, IDs: []int64{11}, Lesson: "第2课", IncludeInScope: true,
	})
	if got := argvValue(argv, "--lesson"); got != "第2课" {
		t.Fatalf("--lesson = %q, want 第2课", got)
	}
	if argvFind(argv, "--include-in-scope") < 0 {
		t.Fatalf("argv %v: --include-in-scope missing", argv)
	}

	argv = unassignWordsArgs(cfg, UnassignWordsOpts{TextbookID: 3, IDs: []int64{11}})
	assertDBBeforeSubcommand(t, argv, "unassign-words")
	if got := argvValue(argv, "--ids"); got != "[11]" {
		t.Fatalf("--ids = %q, want [11]", got)
	}
}

// TestRunTextbookCreateValidation pins the pre-subprocess guards: a blank
// name or a non-vocabulary category fails without any argv (caller bugs
// never waste a process — the RunAdd/RunEdit pattern).
func TestRunTextbookCreateValidation(t *testing.T) {
	b, err := NewBridge(infra.PythonConfig{
		Interpreter: "no-such-python", ScriptsDir: "scripts", VocabScript: "vocab_manager.py",
	}, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	if _, err := b.RunTextbookCreate(context.Background(), TextbookCreateOpts{Name: "  "}); err == nil {
		t.Fatal("blank name accepted, want rejection")
	}
	if _, err := b.RunTextbookCreate(context.Background(), TextbookCreateOpts{Name: "X", Category: "math"}); err == nil {
		t.Fatal("bad category accepted, want rejection")
	}
	if _, err := b.RunTextbookUpdate(context.Background(), TextbookUpdateOpts{ID: 1}); err == nil {
		t.Fatal("empty update accepted, want at-least-one-change rejection")
	}
	if _, err := b.RunAssignWords(context.Background(), AssignWordsOpts{TextbookID: 1, IDs: []int64{2, 2}}); err == nil {
		t.Fatal("duplicate ids accepted, want rejection")
	}
	if _, err := b.RunUnassignWords(context.Background(), UnassignWordsOpts{TextbookID: 0, IDs: []int64{2}}); err == nil {
		t.Fatal("zero textbook id accepted, want rejection")
	}
}
