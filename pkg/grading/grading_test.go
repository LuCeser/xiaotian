package grading

import (
	"reflect"
	"testing"
)

// The word-level judgment contract: normalization regime (en/zh), blank
// semantics, near-miss fuzzy rating, and the Damerau-Levenshtein character
// alignment the report renders.
func TestGradeWord(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		heard    string
		lang     string
		wantOK   bool
		wantRate string
		wantNear bool
		wantDist int
	}{
		// Exact matches.
		{"exact en", "apple", "apple", "en", true, "good", false, 0},
		{"exact zh", "苹果", "苹果", "zh", true, "good", false, 0},

		// Normalization: case, whitespace, punctuation.
		{"case-insensitive", "Apple", "APPLE", "en", true, "good", false, 0},
		{"en strips punctuation", "don't", "dont", "en", true, "good", false, 0},
		{"en strips spaces", "ice cream", "ICE  cream!", "en", true, "good", false, 0},
		{"zh strips punctuation", "你好。", "你好", "zh", true, "good", false, 0},
		{"zh strips fullwidth punct", "苹果，", "苹果！", "zh", true, "good", false, 0},
		{"zh preserves CJK", "计算机", "计 算机", "zh", true, "good", false, 0},

		// Blanks: no answer submitted → wrong/again, never near-miss.
		{"blank", "apple", "", "en", false, "again", false, 0},
		{"blank zh", "苹果", "", "zh", false, "again", false, 0},
		{"whitespace-only blank", "apple", "   ", "en", false, "again", false, 0},

		// Wrong answers.
		{"totally different", "apple", "orange", "en", false, "again", false, 5},
		{"zh wrong char", "香蕉", "香焦", "zh", false, "hard", true, 1},
		{"en one substitution", "word", "ward", "en", false, "hard", true, 1},
		{"en one deletion", "word", "wor", "en", false, "hard", true, 1},
		{"en one insertion", "word", "words", "en", false, "hard", true, 1},
		{"en transposition", "receive", "recieve", "en", false, "hard", true, 1},
		{"en two edits", "beautiful", "beutifull", "en", false, "again", false, 2},
		{"zh three subs", "香蕉苹果", "香焦平过", "zh", false, "again", false, 3},
		// A single-char word that is wrong is a total miss, not a near-miss.
		{"single-char word wrong", "a", "b", "en", false, "again", false, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GradeWord(tt.expected, tt.heard, tt.lang)
			if got.Correct != tt.wantOK {
				t.Errorf("Correct = %v, want %v", got.Correct, tt.wantOK)
			}
			if got.Rating != tt.wantRate {
				t.Errorf("Rating = %q, want %q", got.Rating, tt.wantRate)
			}
			if got.NearMiss != tt.wantNear {
				t.Errorf("NearMiss = %v, want %v", got.NearMiss, tt.wantNear)
			}
			if got.Blank != isBlank(tt.heard) {
				t.Errorf("Blank = %v for heard %q", got.Blank, tt.heard)
			}
			if !got.Blank && got.Distance != tt.wantDist {
				t.Errorf("Distance = %d, want %d", got.Distance, tt.wantDist)
			}
		})
	}
}

func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// The diff must be a full alignment of the two normalized strings: every
// expected rune and every heard rune appears exactly once, matched runes line
// up, and a transposed pair is reported as two swap ops carrying the crossed
// rune pair.
func TestGradeWord_DiffAlignment(t *testing.T) {
	got := GradeWord("receive", "recieve", "en")
	want := []CharOp{
		{Expected: "r", Heard: "r", Kind: OpMatch},
		{Expected: "e", Heard: "e", Kind: OpMatch},
		{Expected: "c", Heard: "c", Kind: OpMatch},
		{Expected: "e", Heard: "i", Kind: OpSwap},
		{Expected: "i", Heard: "e", Kind: OpSwap},
		{Expected: "v", Heard: "v", Kind: OpMatch},
		{Expected: "e", Heard: "e", Kind: OpMatch},
	}
	if !reflect.DeepEqual(got.Diff, want) {
		t.Errorf("swap diff = %+v, want %+v", got.Diff, want)
	}

	// banana → bandana is one insertion (the 'd'), distance 1.
	got = GradeWord("banana", "bandana", "en")
	want = []CharOp{
		{Expected: "b", Heard: "b", Kind: OpMatch},
		{Expected: "a", Heard: "a", Kind: OpMatch},
		{Expected: "n", Heard: "n", Kind: OpMatch},
		{Expected: "", Heard: "d", Kind: OpIns},
		{Expected: "a", Heard: "a", Kind: OpMatch},
		{Expected: "n", Heard: "n", Kind: OpMatch},
		{Expected: "a", Heard: "a", Kind: OpMatch},
	}
	if !reflect.DeepEqual(got.Diff, want) {
		t.Errorf("ins diff = %+v, want %+v", got.Diff, want)
	}

	got = GradeWord("苹果", "葡萄", "zh")
	if len(got.Diff) != 2 || got.Diff[0].Kind != OpSub || got.Diff[1].Kind != OpSub {
		t.Errorf("zh sub diff = %+v, want two substitutions", got.Diff)
	}

	// Blank answers carry no diff (there is nothing to align).
	got = GradeWord("苹果", "", "zh")
	if got.Diff != nil {
		t.Errorf("blank diff = %+v, want nil", got.Diff)
	}

	// Correct answers align everything as matches.
	got = GradeWord("苹果", "苹果", "zh")
	if len(got.Diff) != 2 || got.Diff[0].Kind != OpMatch || got.Diff[1].Kind != OpMatch {
		t.Errorf("correct diff = %+v, want all matches", got.Diff)
	}
}

// Every op kind is reachable; the op sequences are the deterministic DP
// backtrace (ties broken match > swap > sub > del > ins).
func TestGradeWord_DiffKinds(t *testing.T) {
	cases := []struct {
		expected, heard, lang string
		wantKinds             []string
	}{
		{"word", "wrd", "en", []string{OpMatch, OpDel, OpMatch, OpMatch}},            // deletion (the 'o')
		{"word", "world", "en", []string{OpMatch, OpMatch, OpMatch, OpIns, OpMatch}}, // insertion
		{"abcd", "badc", "en", []string{OpSwap, OpSwap, OpSwap, OpSwap}},             // two swaps
	}
	for _, tc := range cases {
		got := GradeWord(tc.expected, tc.heard, tc.lang)
		kinds := make([]string, len(got.Diff))
		for i, op := range got.Diff {
			kinds[i] = op.Kind
		}
		if !reflect.DeepEqual(kinds, tc.wantKinds) {
			t.Errorf("GradeWord(%q,%q) kinds = %v, want %v", tc.expected, tc.heard, kinds, tc.wantKinds)
		}
	}
}
