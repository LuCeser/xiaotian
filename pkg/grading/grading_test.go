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
		wantRate string
		wantDist int
		wantOK   bool
		wantNear bool
	}{
		// Exact matches.
		{"exact en", "apple", "apple", "en", "good", 0, true, false},
		{"exact zh", "苹果", "苹果", "zh", "good", 0, true, false},

		// Normalization: case, whitespace, punctuation.
		{"case-insensitive", "Apple", "APPLE", "en", "good", 0, true, false},
		{"en strips punctuation", "don't", "dont", "en", "good", 0, true, false},
		{"en strips spaces", "ice cream", "ICE  cream!", "en", "good", 0, true, false},
		{"zh strips punctuation", "你好。", "你好", "zh", "good", 0, true, false},
		{"zh strips fullwidth punct", "苹果，", "苹果！", "zh", "good", 0, true, false},
		{"zh preserves CJK", "计算机", "计 算机", "zh", "good", 0, true, false},

		// Blanks: no answer submitted → wrong/again, never near-miss.
		{"blank", "apple", "", "en", "again", 0, false, false},
		{"blank zh", "苹果", "", "zh", "again", 0, false, false},
		{"whitespace-only blank", "apple", "   ", "en", "again", 0, false, false},

		// Wrong answers.
		{"totally different", "apple", "orange", "en", "again", 5, false, false},
		{"zh wrong char", "香蕉", "香焦", "zh", "hard", 1, false, true},
		{"en one substitution", "word", "ward", "en", "hard", 1, false, true},
		{"en one deletion", "word", "wor", "en", "hard", 1, false, true},
		{"en one insertion", "word", "words", "en", "hard", 1, false, true},
		{"en transposition", "receive", "recieve", "en", "hard", 1, false, true}, //nolint:misspell // deliberate typo fixture
		{"en two edits", "beautiful", "beutifull", "en", "again", 2, false, false},
		{"zh three subs", "香蕉苹果", "香焦平过", "zh", "again", 3, false, false},
		// A single-char word that is wrong is a total miss, not a near-miss.
		{"single-char word wrong", "a", "b", "en", "again", 1, false, false},
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
	got := GradeWord("receive", "recieve", "en") //nolint:misspell // deliberate typo fixture
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
