// vocab_mgmt_test.go exercises the Phase 9 management argv builder
// (addArgs) declared in vocab_mgmt.go.
//
// Coverage (Plan 09-01 Task 2 behavior block):
//   - TestAddArgs: argv contract [VocabScript, --db, VocabDBPath, add],
//     --category always present, --unit omitted-when-empty, list-mode
//     JSON payload carrying word/meaning/example keys, and the
//     inline --words vs temp-file --file threshold (mirrors
//     TestReviewArgs in vocab_test.go).
//
// Test framework: stdlib `testing` only, table-driven + t.Run subtests.
// Reuses argvFind / assertDBBeforeSubcommand from vocab_test.go (same
// package).
package pybridge

import (
	"encoding/json"
	"os"
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
