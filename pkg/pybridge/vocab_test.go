// vocab_test.go exercises the argv builders (dueArgs / reviewArgs) and the
// stdout-parsing paths (RunDue / RunReview) declared in vocab.go.
//
// Coverage strategy (Plan 03-04 Task 2):
//   - TestDueArgs: pure argv construction. No subprocess, no Python.
//     Covers minimal + every flag (limit/peek/units/exclude/category/
//     fetch-lock-hours), with every subtest asserting the vocab_manager-
//     specific --db-before-subcommand ordering.
//   - TestReviewArgs: argv construction for the review path, including
//     auto --results vs --file selection and 4-rating verbatim forwarding
//     (RESEARCH Pitfall 4 acceptance).
//   - TestRunDueParseFromFixtures / TestRunReviewParseFromFixtures:
//     exercises the parser pipeline (extractTrailingJSON -> json.Unmarshal)
//     against the golden fixtures. No subprocess spawned -- we replicate
//     the post-run() parsing steps in-process so the tests run on Windows
//     dev hosts without a venv.
//
// Test framework: stdlib `testing` only, table-driven + t.Run subtests,
// manual fakes (D-48). Mirrors config_test.go / worker_test.go style.
package pybridge

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// argvFind returns the index of the first argv element equal to want, or -1
// if absent. Used to assert flag positioning (e.g. "--db" must come before
// "due") without writing brittle whole-slice comparisons.
func argvFind(argv []string, want string) int {
	for i, v := range argv {
		if v == want {
			return i
		}
	}
	return -1
}

// assertDBBeforeSubcommand is the RESEARCH lines 541-556 acceptance check:
// vocab_manager.py declares --db on the top-level parser (line 718), so
// argv MUST be [vocabScript, "--db", dbPath, subcommand, ...]. We assert
// args[1]=="--db" AND args[3]==<subcommand> for the two paths this plan
// owns.
func assertDBBeforeSubcommand(t *testing.T, argv []string, subcommand string) {
	t.Helper()
	if got := len(argv); got < 4 {
		t.Fatalf("argv too short: got %d elements %v, want >= 4", got, argv)
	}
	if argv[1] != "--db" {
		t.Errorf("argv[1] = %q, want %q (vocab_manager.py:718 --db must precede %q)",
			argv[1], "--db", subcommand)
	}
	if argv[3] != subcommand {
		t.Errorf("argv[3] = %q, want %q (subcommand must come right after the db path)",
			argv[3], subcommand)
	}
}

// TestDueArgs covers dueArgs argv construction. Every subtest asserts the
// vocab_manager-specific global --db ordering (args[1]=="--db" AND
// args[3]=="due") plus the specific flag(s) under test.
func TestDueArgs(t *testing.T) {
	cfg := infra.PythonConfig{
		VocabScript: "vocab_manager.py",
		VocabDBPath: "/var/lib/hta/vocab.db",
	}

	cases := []struct {
		name      string
		extraArgs []string // flags we expect to see appended (in order)
		opts      DueOpts
	}{
		{
			name:      "minimal",
			opts:      DueOpts{},
			extraArgs: nil,
		},
		{
			name: "with limit",
			opts: DueOpts{Limit: 30},
			extraArgs: []string{
				"--limit", "30",
			},
		},
		{
			name: "with peek",
			opts: DueOpts{Peek: true},
			extraArgs: []string{
				"--peek",
			},
		},
		{
			name: "with units",
			opts: DueOpts{Units: []string{"unit1", "unit2"}},
			extraArgs: []string{
				"--units", "unit1,unit2",
			},
		},
		{
			name: "with category",
			opts: DueOpts{Category: "english"},
			extraArgs: []string{
				"--category", "english",
			},
		},
		{
			name: "with exclude",
			opts: DueOpts{Exclude: []string{"alpha", "beta"}},
			extraArgs: []string{
				"--exclude", "alpha,beta",
			},
		},
		{
			name: "with fetch lock hours",
			opts: DueOpts{FetchLockHours: 6.0},
			extraArgs: []string{
				"--fetch-lock-hours", "6",
			},
		},
		{
			name: "all flags combined",
			opts: DueOpts{
				Category:       "chinese",
				Limit:          50,
				Units:          []string{"L10", "L11"},
				Exclude:        []string{"skip"},
				Peek:           true,
				FetchLockHours: 12.5,
			},
			extraArgs: []string{
				"--category", "chinese",
				"--limit", "50",
				"--units", "L10,L11",
				"--exclude", "skip",
				"--peek",
				"--fetch-lock-hours", "12",
			},
		},
		{
			name: "fractional lock hours below one become one",
			opts: DueOpts{FetchLockHours: 0.5},
			extraArgs: []string{
				"--fetch-lock-hours", "1",
			},
		},
		{
			name: "limit and lock hours clamp to the script ceilings",
			opts: DueOpts{Limit: 10000, FetchLockHours: 1e12},
			extraArgs: []string{
				"--limit", strconv.Itoa(MaxDueLimit),
				"--fetch-lock-hours", strconv.Itoa(MaxFetchLockHours),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := dueArgs(cfg, tc.opts)

			// Global invariant: argv[0]=script, argv[1]=--db, argv[2]=dbPath,
			// argv[3]="due", then optional flags. This is the
			// vocab_manager.py:718 contract (RESEARCH lines 541-556).
			if want := "vocab_manager.py"; argv[0] != want {
				t.Errorf("argv[0] = %q, want %q", argv[0], want)
			}
			assertDBBeforeSubcommand(t, argv, "due")

			// Each extra flag pair/flag must appear, in the supplied order,
			// after the subcommand. We do not require CONTIGUOUS ordering
			// between unrelated flags, only that the elements appear in the
			// relative order they were declared (matches dueArgs append
			// order).
			searchFrom := 4 // first index after subcommand
			for _, want := range tc.extraArgs {
				idx := argvFind(argv[searchFrom:], want)
				if idx < 0 {
					t.Errorf("argv %v missing expected element %q after %q",
						argv, want, "due")
					break
				}
				searchFrom += idx + 1
			}

			// Limit/Peek/FetchLockHours zero-value must NOT emit the flag
			// (omit-when-zero contract). Verifiable by absence for the
			// "minimal" subtest specifically.
			if tc.name == "minimal" {
				for _, banned := range []string{"--limit", "--peek", "--units",
					"--exclude", "--category", "--fetch-lock-hours"} {
					if argvFind(argv, banned) >= 0 {
						t.Errorf("minimal argv %v unexpectedly contains %q "+
							"(zero-valued DueOpts fields must omit the flag)", argv, banned)
					}
				}
			}
		})
	}
}

// TestReviewArgs covers reviewArgs argv construction: the --db-before-
// subcommand ordering, the inline --results vs --file selection, the
// verbatim 4-rating forwarding (RESEARCH Pitfall 4), and the temp-file
// cleanup contract.
func TestReviewArgs(t *testing.T) {
	cfg := infra.PythonConfig{
		VocabScript: "vocab_manager.py",
		VocabDBPath: "/var/lib/hta/vocab.db",
	}

	t.Run("small results goes inline", func(t *testing.T) {
		opts := ReviewOpts{
			Results: map[string]string{"hello": "good"},
		}
		argv, cleanup, err := reviewArgs(cfg, opts)
		if err != nil {
			t.Fatalf("reviewArgs returned unexpected error: %v", err)
		}
		defer cleanup()

		assertDBBeforeSubcommand(t, argv, "review")
		if idx := argvFind(argv, "--results"); idx < 0 {
			t.Fatalf("argv %v missing --results for small payload", argv)
		}
		if idx := argvFind(argv, "--file"); idx >= 0 {
			t.Errorf("argv %v has --file for small payload, want inline --results only", argv)
		}

		// The --results JSON value must carry the rating verbatim. The
		// script's rating_map accepts all four (good/hard/again/easy);
		// the bridge does no rewriting.
		val := argv[argvFind(argv, "--results")+1]
		var parsed map[string]string
		if err := json.Unmarshal([]byte(val), &parsed); err != nil {
			t.Fatalf("--results value not valid JSON: %v (val=%q)", err, val)
		}
		if got := parsed["hello"]; got != "good" {
			t.Errorf("--results[hello] = %q, want %q (rating forwarded verbatim)", got, "good")
		}
	})

	t.Run("all four ratings forwarded verbatim", func(t *testing.T) {
		// RESEARCH Pitfall 4 acceptance point: the bridge MUST forward all
		// four ratings without rewriting or dropping any. The script's
		// rating_map (vocab_manager.py:359-364) accepts again/good/hard/easy;
		// the help text (line 746) listing only good|hard|again is outdated.
		opts := ReviewOpts{
			Results: map[string]string{
				"w_again": "again",
				"w_good":  "good",
				"w_hard":  "hard",
				"w_easy":  "easy",
			},
		}
		argv, cleanup, err := reviewArgs(cfg, opts)
		if err != nil {
			t.Fatalf("reviewArgs returned unexpected error: %v", err)
		}
		defer cleanup()

		idx := argvFind(argv, "--results")
		if idx < 0 {
			t.Fatalf("argv %v missing --results", argv)
		}
		var parsed map[string]string
		if err := json.Unmarshal([]byte(argv[idx+1]), &parsed); err != nil {
			t.Fatalf("--results value not valid JSON: %v", err)
		}
		for _, r := range []string{"again", "good", "hard", "easy"} {
			found := false
			for _, v := range parsed {
				if v == r {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("rating %q missing from --results payload %v "+
					"(RESEARCH Pitfall 4: all four ratings must forward verbatim)", r, parsed)
			}
		}
	})

	t.Run("large results by entry count goes to file", func(t *testing.T) {
		// >reviewInlineEntryCap entries forces --file even if each entry is
		// tiny (defence against the platform argv length limit and against
		// operator-approval prompts on very long commands).
		results := make(map[string]string, reviewInlineEntryCap+1)
		for i := 0; i < reviewInlineEntryCap+1; i++ {
			results["word"+string(rune('a'+i%26))+string(rune('0'+i/26))] = "good"
		}
		opts := ReviewOpts{Results: results}

		argv, cleanup, err := reviewArgs(cfg, opts)
		if err != nil {
			t.Fatalf("reviewArgs returned unexpected error: %v", err)
		}

		if idx := argvFind(argv, "--file"); idx < 0 {
			t.Fatalf("argv %v missing --file for large payload (entry count > %d)",
				argv, reviewInlineEntryCap)
		}
		if idx := argvFind(argv, "--results"); idx >= 0 {
			t.Errorf("argv %v has --results for large payload, want --file only", argv)
		}

		// The --file path must be a real file on disk that cleanup removes.
		fileIdx := argvFind(argv, "--file")
		path := argv[fileIdx+1]
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

	t.Run("large results by byte size goes to file", func(t *testing.T) {
		// A small entry count with very long word strings still trips the
		// byte cap (defence against argv length limits at the OS layer).
		results := map[string]string{
			strings.Repeat("w", reviewInlineByteCap): "good",
		}
		opts := ReviewOpts{Results: results}

		argv, cleanup, err := reviewArgs(cfg, opts)
		if err != nil {
			t.Fatalf("reviewArgs returned unexpected error: %v", err)
		}
		defer cleanup()

		if idx := argvFind(argv, "--file"); idx < 0 {
			t.Errorf("argv %v missing --file for large byte-size payload "+
				"(serialised > %d bytes)", argv, reviewInlineByteCap)
		}
		if idx := argvFind(argv, "--results"); idx >= 0 {
			t.Errorf("argv %v has --results for large byte-size payload, want --file only", argv)
		}
	})

	t.Run("nil results map does not panic", func(t *testing.T) {
		// Defence: json.Marshal(nil) would produce "null" which the script
		// would reject; reviewArgs must substitute an empty map so the
		// bridge forwards "{}" instead. The script accepts an empty object
		// as "nothing to review" and returns exit 0 with an empty updated
		// array.
		opts := ReviewOpts{Results: nil}
		argv, cleanup, err := reviewArgs(cfg, opts)
		if err != nil {
			t.Fatalf("reviewArgs returned unexpected error on nil Results: %v", err)
		}
		defer cleanup()

		idx := argvFind(argv, "--results")
		if idx < 0 {
			t.Fatalf("argv %v missing --results for nil Results map", argv)
		}
		if got := argv[idx+1]; got != "{}" {
			t.Errorf("nil Results -> --results = %q, want %q (empty map, not null)", got, "{}")
		}
	})

	t.Run("category appended when set", func(t *testing.T) {
		opts := ReviewOpts{
			Results:  map[string]string{"hello": "good"},
			Category: "chinese",
		}
		argv, cleanup, err := reviewArgs(cfg, opts)
		if err != nil {
			t.Fatalf("reviewArgs returned unexpected error: %v", err)
		}
		defer cleanup()

		if idx := argvFind(argv, "--category"); idx < 0 {
			t.Errorf("argv %v missing --category when opts.Category set", argv)
		}
	})
}

// TestRunDueParseFromFixtures exercises RunDue's post-run() parsing pipeline
// against the golden due_cards.json fixture. We do NOT spawn a Python
// subprocess; instead we drive the same sequence RunDue uses
// (extractTrailingJSON -> json.Unmarshal into []DueCard) directly. This
// proves the field tags align with the fixture shape (Rule 1 contract
// check carried over from Plan 03-02) and that the parser pipeline returns
// a non-empty Cards slice for a well-formed stdout.
func TestRunDueParseFromFixtures(t *testing.T) {
	fixturePath := filepath.Join("testdata", "due_cards.json")
	stdout, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}

	raw, parseErr := extractTrailingJSON(stdout)
	if parseErr != nil {
		t.Fatalf("extractTrailingJSON on due_cards.json: %v", parseErr)
	}
	if !json.Valid(raw) {
		t.Fatalf("extractTrailingJSON result not json.Valid: %s", string(raw))
	}

	var cards []DueCard
	if err := json.Unmarshal(raw, &cards); err != nil {
		t.Fatalf("json.Unmarshal into []DueCard: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("parsed %d cards, want 2 (fixture has 2 entries)", len(cards))
	}
	// Spot-check first card against the fixture's known values. Field-tag
	// alignment is the Rule 1 contract from 03-02: DueCard must mirror
	// vocab_manager.py:255-265 exactly.
	first := cards[0]
	if first.Word != "hello" {
		t.Errorf("cards[0].Word = %q, want %q", first.Word, "hello")
	}
	if first.ID != 42 {
		t.Errorf("cards[0].ID = %d, want 42", first.ID)
	}
	if first.Unit != "unit-1" {
		t.Errorf("cards[0].Unit = %q, want %q", first.Unit, "unit-1")
	}
	if first.Stability != 1.25 {
		t.Errorf("cards[0].Stability = %v, want 1.25", first.Stability)
	}
	// Second card spot-check.
	second := cards[1]
	if second.Word != "goodbye" {
		t.Errorf("cards[1].Word = %q, want %q", second.Word, "goodbye")
	}
	if second.State != "Review" {
		t.Errorf("cards[1].State = %q, want %q", second.State, "Review")
	}
}

// TestRunReviewParseFromFixtures is the RESEARCH Pitfall 2 acceptance
// point: vocab_manager.py's review subcommand emits Warning: prefix lines
// (unknown rating, card not found, ambiguous card) BEFORE the trailing
// {"updated": [...]} JSON, all on the same stdout stream. We MUST extract
// just the trailing JSON and Unmarshal successfully -- a naive
// json.Unmarshal(stdout, ...) would fail on the "Warning:" prefix bytes.
//
// We do not spawn a Python subprocess; we drive the same sequence
// RunReview uses (extractTrailingJSON -> json.Unmarshal into ReviewResult)
// against the golden review_with_warnings.txt fixture.
func TestRunReviewParseFromFixtures(t *testing.T) {
	fixturePath := filepath.Join("testdata", "review_with_warnings.txt")
	stdout, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}

	// Sanity: the fixture's whole stdout must NOT be valid JSON -- if it
	// were, the Warning prefix would be missing and the test would no
	// longer cover the Pitfall 2 path.
	if json.Valid(stdout) {
		t.Fatalf("review_with_warnings.txt is pure JSON; the Warning prefix "+
			"lines required for the Pitfall 2 acceptance are absent: %s",
			string(stdout))
	}

	raw, parseErr := extractTrailingJSON(stdout)
	if parseErr != nil {
		t.Fatalf("extractTrailingJSON on review_with_warnings.txt: %v "+
			"(Pitfall 2: must peel trailing JSON off mixed stdout)", parseErr)
	}
	if !json.Valid(raw) {
		t.Fatalf("extractTrailingJSON result not json.Valid: %s", string(raw))
	}

	var result ReviewResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("json.Unmarshal into ReviewResult: %v "+
			"(Pitfall 2: trailing JSON must parse into {\"updated\":[...]})", err)
	}
	if len(result.Updated) != 1 {
		t.Fatalf("parsed %d UpdatedCard, want 1 (only 'goodbye' survived; "+
			"the other Warning lines were skipped)", len(result.Updated))
	}
	got := result.Updated[0]
	if got.Word != "goodbye" {
		t.Errorf("Updated[0].Word = %q, want %q", got.Word, "goodbye")
	}
	if got.Rating != "again" {
		t.Errorf("Updated[0].Rating = %q, want %q", got.Rating, "again")
	}
	if got.State != "Learning" {
		t.Errorf("Updated[0].State = %q, want %q", got.State, "Learning")
	}
}

// TestRunReviewParseCleanJSON is the non-mixed-stdout counterpart: when
// the script emits pure JSON (no Warning prefix), RunReview's pipeline
// must still succeed. This guards against over-fitting on the Warning
// fixture.
func TestRunReviewParseCleanJSON(t *testing.T) {
	fixturePath := filepath.Join("testdata", "review_updated.json")
	stdout, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}

	raw, parseErr := extractTrailingJSON(stdout)
	if parseErr != nil {
		t.Fatalf("extractTrailingJSON on review_updated.json: %v", parseErr)
	}

	var result ReviewResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("json.Unmarshal into ReviewResult: %v", err)
	}
	if len(result.Updated) != 2 {
		t.Fatalf("parsed %d UpdatedCard, want 2 (clean fixture has 2 entries)",
			len(result.Updated))
	}
	// Spot-check one entry to make sure the UpdatedCard field tags align.
	if result.Updated[0].Word != "hello" {
		t.Errorf("Updated[0].Word = %q, want %q", result.Updated[0].Word, "hello")
	}
	if result.Updated[1].Rating != "again" {
		t.Errorf("Updated[1].Rating = %q, want %q", result.Updated[1].Rating, "again")
	}
}

// TestReviewArgsLargePayloadNoLeak asserts that when reviewArgs hits an
// error AFTER creating the temp file, the file is removed before the
// error is returned (no temp-file leak on partial-failure paths). The
// current implementation's only post-create failure mode is tmp.Write /
// tmp.Close; we simulate a write failure by hijacking a path that cannot
// be written. Because os.CreateTemp is hard to make fail deterministically
// on dev hosts, this test instead asserts the simpler invariant: the
// happy-path cleanup actually unlinks the file (already covered above) AND
// calling cleanup twice does not panic. The double-cleanup check guards
// against a future refactor that might capture the cleanup closure
// incorrectly.
func TestReviewArgsCleanupIdempotent(t *testing.T) {
	cfg := infra.PythonConfig{
		VocabScript: "vocab_manager.py",
		VocabDBPath: "/var/lib/hta/vocab.db",
	}
	opts := ReviewOpts{
		Results: map[string]string{
			strings.Repeat("w", reviewInlineByteCap): "good",
		},
	}
	_, cleanup, err := reviewArgs(cfg, opts)
	if err != nil {
		t.Fatalf("reviewArgs returned unexpected error: %v", err)
	}
	// cleanup is safe to call multiple times -- os.Remove on a missing
	// path returns an error that we ignore inside the closure.
	cleanup()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second cleanup panicked: %v", r)
		}
	}()
	cleanup()
}

// argvValue returns the value following the flag, or "" when absent (a
// small convenience for the textbook argv tests).
func argvValue(argv []string, flag string) string {
	idx := argvFind(argv, flag)
	if idx < 0 || idx+1 >= len(argv) {
		return ""
	}
	return argv[idx+1]
}

func TestWholeLockHours(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{-3, 0},
		{math.NaN(), 0},
		{math.Inf(-1), 0},
		{0.5, 1},
		{6, 6},
		{12.5, 12},
		{24.0, 24},
		{1e12, float64(MaxFetchLockHours)},
		{math.Inf(1), float64(MaxFetchLockHours)},
	}
	for _, tc := range cases {
		got := WholeLockHours(tc.in)
		if got != tc.want && !(math.IsNaN(tc.in) && got == 0) {
			t.Errorf("WholeLockHours(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestVocabManagerRejectsUnboundedLimitAndLock shells the vendored script
// so the argparse ceilings are checked against the same constants Go emits.
// Skipped when the project venv is absent (default CI has no Python deps).
func TestVocabManagerRejectsUnboundedLimitAndLock(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	py := filepath.Join(root, "third_party", "python", ".venv", "bin", "python")
	script := filepath.Join(root, "third_party", "python", "vocab_manager.py")
	if _, err := os.Stat(py); err != nil {
		t.Skip("venv python not present")
	}

	reject := []struct {
		name string
		args []string
	}{
		{"due limit above max", []string{"due", "--limit", strconv.Itoa(MaxDueLimit + 1)}},
		{"new limit above max", []string{"new", "--limit", strconv.Itoa(MaxDueLimit + 1)}},
		{"lock hours above max", []string{"due", "--fetch-lock-hours", strconv.Itoa(MaxFetchLockHours + 1)}},
		{"fractional lock hours", []string{"due", "--fetch-lock-hours", "1.5"}},
	}
	for _, tc := range reject {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(py, append([]string{script, "--db", filepath.Join(t.TempDir(), "v.db")}, tc.args...)...) //nolint:gosec // G204: py and tc.args are this test's own table literals, not untrusted input
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected argparse rejection, got success:\n%s", out)
			}
			if !strings.Contains(strings.ToLower(string(out)), "error") {
				t.Fatalf("output = %s", out)
			}
		})
	}

	t.Run("in range accepted", func(t *testing.T) {
		cmd := exec.Command(py, script, "--db", filepath.Join(t.TempDir(), "v.db"),
			"due", "--limit", "1", "--fetch-lock-hours", "24")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("in-range flags rejected: %v\n%s", err, out)
		}
	})
}
