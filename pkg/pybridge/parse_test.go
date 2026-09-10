package pybridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// These tests cover the pure-function parsers declared in 03-02-PLAN.md
// Task 2:
//   - extractTrailingJSON peels the last JSON value off mixed stdout
//     (Pitfall 2 -- vocab_manager mixes Warning/Error lines with JSON).
//   - splitNonEmptyLines splits line-based pregenerate stdout into paths
//     (Pitfall 3 -- pregenerate stdout is NOT JSON).
//
// Table-driven + golden fixtures under testdata/. No Python interpreter
// is spawned -- these are pure transforms.

// loadFixture reads a golden fixture from testdata/. It fails the test
// (t.Fatalf) on a missing file so a renamed fixture is caught loudly
// rather than producing a confusing "empty stdout" parse result.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// TestExtractTrailingJSON is the table-driven Pitfall 2 coverage: every
// realistic shape of vocab_manager stdout must yield a json.Valid result
// (or a typed ErrPythonJSONParse).
func TestExtractTrailingJSON(t *testing.T) {
	pureJSON := loadFixture(t, "due_cards.json")
	mixedStdout := loadFixture(t, "review_with_warnings.txt")

	cases := []struct {
		name      string
		in        []byte
		wantValid bool // expect the result to be json.Valid
		wantErr   bool // expect an error wrapping ErrPythonJSONParse
	}{
		{
			name:      "pure JSON array (due_cards.json fixture)",
			in:        pureJSON,
			wantValid: true,
		},
		{
			name:      "mixed stdout with Warning prefix lines (review_with_warnings.txt fixture)",
			in:        mixedStdout,
			wantValid: true,
		},
		{
			name: "nested objects inside array with prefix noise",
			in: []byte(`noise prefix line
[
  {"a": 1, "b": {"c": 2}},
  {"d": [3, 4]}
]`),
			wantValid: true,
		},
		{
			name:      "trailing whitespace after JSON is trimmed",
			in:        append([]byte(`{"updated": []}`), ' ', '\n', '\t', ' '),
			wantValid: true,
		},
		{
			name:      "JSON object value (not array)",
			in:        []byte(`status: ok\n{"updated": [{"word": "hi", "rating": "good", "next_due": "x", "state": "Review", "stability": 1.0}]}`),
			wantValid: true,
		},
		{
			name:    "plain text without brackets returns ErrPythonJSONParse",
			in:      []byte("Warning: nothing to see here\nno json at all"),
			wantErr: true,
		},
		{
			name:    "text with unmatched closing bracket returns ErrPythonJSONParse",
			in:      []byte("oops ] without opener"),
			wantErr: true,
		},
		{
			name:    "empty input returns ErrPythonJSONParse",
			in:      []byte(""),
			wantErr: true,
		},
		{
			name:    "whitespace-only input returns ErrPythonJSONParse",
			in:      []byte("   \n\t  "),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := extractTrailingJSON(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("extractTrailingJSON: want error wrapping ErrPythonJSONParse, got nil (raw=%s)", raw)
				}
				if !errors.Is(err, ErrPythonJSONParse) {
					t.Errorf("err: got %v, want errors.Is(err, ErrPythonJSONParse)=true", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractTrailingJSON: unexpected err: %v", err)
			}
			if !json.Valid(raw) {
				t.Errorf("extractTrailingJSON: result not json.Valid: %s", raw)
			}
		})
	}
}

// TestExtractTrailingJSON_MixedWithWarnings is the Pitfall 2 acceptance
// test called out in 03-02-PLAN.md: extractTrailingJSON on the golden
// mixed stdout must return a json.RawMessage that is json.Valid AND
// unmarshals into the review {"updated": [...]} shape. This guards
// against a regression where someone replaces the parser with a naive
// whole-stdout json.Unmarshal.
func TestExtractTrailingJSON_MixedWithWarnings(t *testing.T) {
	mixed := loadFixture(t, "review_with_warnings.txt")

	// Sanity: the mixed fixture must NOT be valid JSON as a whole --
	// otherwise the test would pass trivially and not exercise the
	// peeling logic.
	if json.Valid(mixed) {
		t.Fatalf("fixture review_with_warnings.txt should NOT be pure JSON; it must have Warning prefix lines")
	}

	raw, err := extractTrailingJSON(mixed)
	if err != nil {
		t.Fatalf("extractTrailingJSON: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("extracted payload not json.Valid: %s", raw)
	}

	var got struct {
		Updated []UpdatedCard `json:"updated"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal extracted payload: %v", err)
	}
	if len(got.Updated) == 0 {
		t.Errorf("expected non-empty updated list, got 0")
	}
	// Spot-check the parsed card matches the fixture.
	first := got.Updated[0]
	if first.Word != "goodbye" {
		t.Errorf("updated[0].Word: got %q, want %q", first.Word, "goodbye")
	}
	if first.Rating != "again" {
		t.Errorf("updated[0].Rating: got %q, want %q", first.Rating, "again")
	}
}

// TestSplitNonEmptyLines covers the pregenerate stdout parser (Pitfall
// 3): line-based paths, NOT JSON.
func TestSplitNonEmptyLines(t *testing.T) {
	pathsFixture := loadFixture(t, "pregenerate_paths.txt")

	cases := []struct {
		name      string
		in        []byte
		wantLines int
	}{
		{
			name:      "pregenerate_paths.txt fixture",
			in:        pathsFixture,
			wantLines: 3,
		},
		{
			name:      "trailing newline does not add empty entry",
			in:        []byte("/a/b.mp3\n/c/d.mp3\n"),
			wantLines: 2,
		},
		{
			name:      "blank lines between paths are dropped",
			in:        []byte("/a.mp3\n\n\n/b.mp3\n"),
			wantLines: 2,
		},
		{
			name:      "carriage returns stripped (CRLF tolerance)",
			in:        []byte("/a.mp3\r\n/b.mp3\r\n"),
			wantLines: 2,
		},
		{
			name:      "leading whitespace on lines is trimmed",
			in:        []byte("  /a.mp3\n\t/b.mp3\n"),
			wantLines: 2,
		},
		{
			name:      "empty input yields empty slice",
			in:        []byte(""),
			wantLines: 0,
		},
		{
			name:      "whitespace-only input yields empty slice",
			in:        []byte("\n \n\t\n"),
			wantLines: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitNonEmptyLines(tc.in)
			if len(got) != tc.wantLines {
				t.Fatalf("len: got %d, want %d (got=%v)", len(got), tc.wantLines, got)
			}
			// Every returned line MUST be non-empty and trimmed of
			// surrounding whitespace.
			for i, l := range got {
				if l == "" {
					t.Errorf("line[%d] is empty", i)
					continue
				}
				if l[0] == ' ' || l[0] == '\t' || l[len(l)-1] == ' ' || l[len(l)-1] == '\t' {
					t.Errorf("line[%d] has surrounding whitespace: %q", i, l)
				}
			}
		})
	}
}

// TestSplitNonEmptyLines_FixtureContent is the Pitfall 3 acceptance
// test: the golden pregenerate_paths.txt fixture must round-trip into
// the exact expected absolute-path list.
func TestSplitNonEmptyLines_FixtureContent(t *testing.T) {
	pathsFixture := loadFixture(t, "pregenerate_paths.txt")
	got := splitNonEmptyLines(pathsFixture)
	want := []string{
		"/opt/hta/cache/hello.mp3",
		"/opt/hta/cache/goodbye.mp3",
		"/opt/hta/cache/thank_you.mp3",
	}
	if len(got) != len(want) {
		t.Fatalf("len: got %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line[%d]: got %q, want %q", i, got[i], w)
		}
	}
}
