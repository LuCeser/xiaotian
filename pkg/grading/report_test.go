package grading

import (
	"strings"
	"testing"
)

func input(word, heard, lang string) WordInput {
	return WordInput{Word: word, Heard: heard, Judgment: GradeWord(word, heard, lang)}
}

// Score arithmetic + queue-order error list.
func TestAnalyze_ScoreAndErrors(t *testing.T) {
	r := Analyze([]WordInput{
		input("apple", "apple", "en"),
		input("banana", "bandana", "en"), // near-miss
		input("cherry", "", "en"),        // blank
	})
	if r.Total != 3 || r.CorrectCount != 1 {
		t.Errorf("counts = %d/%d, want 1/3", r.CorrectCount, r.Total)
	}
	if r.Score != 33 {
		t.Errorf("score = %d, want 33", r.Score)
	}
	if len(r.Errors) != 2 {
		t.Fatalf("errors = %d, want 2", len(r.Errors))
	}
	if r.Errors[0].Word != "banana" || r.Errors[1].Word != "cherry" {
		t.Errorf("errors not in queue order: %+v", r.Errors)
	}
	if !r.Errors[0].NearMiss || !r.Errors[1].Blank {
		t.Errorf("error flags wrong: %+v", r.Errors)
	}

	// Perfect session: no errors, full score.
	r = Analyze([]WordInput{input("苹果", "苹果", "zh"), input("香蕉", "香蕉", "zh")})
	if r.Score != 100 || len(r.Errors) != 0 {
		t.Errorf("perfect session = %+v", r)
	}

	// Empty session guards.
	r = Analyze(nil)
	if r.Total != 0 || r.Score != 0 {
		t.Errorf("empty session = %+v", r)
	}
}

// Per-word hints: blank / substitution / missing / extra / transposition /
// near-miss prefix. Ops is the FULL alignment — matches included (the UI
// renders whole words with highlights), so the op count equals the longer
// normalized side.
func TestAnalyze_WordHints(t *testing.T) {
	cases := []struct {
		name, word, heard, lang string
		wantSub                 string
	}{
		{"blank", "cherry", "", "en", "没有作答"},
		{"sub", "word", "ward", "en", "「o」写成了「a」"},
		{"missing", "word", "wrd", "en", "漏写了「o」"},
		{"extra", "word", "world", "en", "多写了「l」"},
		{"swap", "receive", "recieve", "en", "顺序颠倒了"}, //nolint:misspell // deliberate typo fixture
		{"zh sub", "香蕉", "香焦", "zh", "「蕉」写成了「焦」"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Analyze([]WordInput{input(tc.word, tc.heard, tc.lang)})
			if len(r.Errors) != 1 {
				t.Fatalf("errors = %+v", r.Errors)
			}
			if !strings.Contains(r.Errors[0].Hint, tc.wantSub) {
				t.Errorf("hint = %q, want it to contain %q", r.Errors[0].Hint, tc.wantSub)
			}
			if r.Errors[0].Blank {
				if r.Errors[0].Ops != nil {
					t.Errorf("blank ops = %+v, want nil", r.Errors[0].Ops)
				}
			} else if len(r.Errors[0].Ops) == 0 {
				t.Errorf("ops empty for %q→%q", tc.word, tc.heard)
			}
		})
	}

	// Near-miss words prepend the 只差一点 encouragement.
	r := Analyze([]WordInput{input("banana", "bandana", "en")})
	if !strings.HasPrefix(r.Errors[0].Hint, "只差一点") {
		t.Errorf("near-miss hint = %q, want 只差一点 prefix", r.Errors[0].Hint)
	}
}

// Session-level patterns fire only when a pattern has enough evidence (>=2).
func TestAnalyze_Patterns(t *testing.T) {
	// Two blanks + one wrong.
	r := Analyze([]WordInput{
		input("apple", "apple", "en"),
		input("banana", "", "en"),
		input("cherry", "", "en"),
		input("dog", "cat", "en"),
	})
	if !hasContaining(r.Patterns, "2 题没有作答") {
		t.Errorf("blank pattern missing: %v", r.Patterns)
	}

	// Two near-misses.
	r = Analyze([]WordInput{
		input("banana", "bandana", "en"),
		input("receive", "recieve", "en"), //nolint:misspell // deliberate typo fixture
	})
	if !hasContaining(r.Patterns, "2 个词只差一点点") {
		t.Errorf("near-miss pattern missing: %v", r.Patterns)
	}

	// Two missing-tail words (answer is a proper prefix of the word).
	r = Analyze([]WordInput{
		input("eating", "eatin", "en"),
		input("playing", "playin", "en"),
	})
	if !hasContaining(r.Patterns, "漏了词尾") {
		t.Errorf("tail pattern missing: %v", r.Patterns)
	}

	// A single occurrence of each pattern stays below the evidence bar.
	r = Analyze([]WordInput{
		input("banana", "", "en"),
		input("dog", "dg", "en"),
	})
	for _, p := range r.Patterns {
		if strings.Contains(p, "没有作答") {
			t.Errorf("single blank produced a pattern: %v", r.Patterns)
		}
	}

	// Perfect sessions produce no patterns.
	r = Analyze([]WordInput{input("apple", "apple", "en")})
	if len(r.Patterns) != 0 {
		t.Errorf("perfect session patterns = %v", r.Patterns)
	}
}

// Suggestions always include the practice-the-wrong-words line when there are
// wrong words; encouragement tiers by score.
func TestAnalyze_SuggestionsAndEncouragement(t *testing.T) {
	r := Analyze([]WordInput{
		input("apple", "apple", "en"),
		input("banana", "bandana", "en"),
	})
	if !hasContaining(r.Suggestions, "banana") {
		t.Errorf("practice suggestion missing the wrong word: %v", r.Suggestions)
	}
	if !strings.Contains(r.Encouragement, "1 个词") || !strings.Contains(r.Encouragement, "对了 1 个") {
		t.Errorf("encouragement = %q, wants counts", r.Encouragement)
	}

	r = Analyze([]WordInput{input("apple", "apple", "en"), input("dog", "dog", "en")})
	if !strings.Contains(r.Encouragement, "太棒了") {
		t.Errorf("perfect encouragement = %q", r.Encouragement)
	}

	r = Analyze([]WordInput{
		input("a", "apple", "en"), input("b", "banana", "en"),
		input("c", "cat", "en"), input("d", "dog", "en"), input("e", "", "en"),
	})
	if !strings.Contains(r.Encouragement, "别灰心") {
		t.Errorf("low-score encouragement = %q", r.Encouragement)
	}

	// Rounding: 2/3 → 67.
	r = Analyze([]WordInput{
		input("a", "a", "en"), input("b", "b", "en"), input("c", "x", "en"),
	})
	if r.Score != 67 {
		t.Errorf("score = %d, want 67", r.Score)
	}
	if !strings.Contains(r.Encouragement, "不错") {
		t.Errorf("mid-score encouragement = %q", r.Encouragement)
	}
}

func hasContaining(list []string, substr string) bool {
	for _, s := range list {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}
