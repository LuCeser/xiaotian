// Package grading turns a submitted dictation answer into a rich judgment:
// correct/wrong, the FSRS rating, and a character-level alignment (diff) of
// the expected word against what the student wrote (Ticket 06).
//
// The package is pure — no I/O, no clocks — so the whole grading contract is
// table-testable and the handlers stay thin (mirrors pkg/voice's split of
// intent classification from pipeline plumbing).
//
// Grading semantics (classroom dictation):
//   - Normalization first: case-folding + language-aware punctuation/whitespace
//     stripping (en also drops non-alphanumerics). A normalized equality is a
//     correct answer ("Apple." == "apple").
//   - A blank answer (nothing submitted) is wrong — no comparison, no diff.
//   - A non-blank wrong answer within near-miss distance (Damerau-Levenshtein
//     distance 1 on a word of at least 2 characters) is still WRONG for the
//     score, but grades FSRS "hard" instead of "again": the student almost
//     knew it. Common minor mistakes (one typo, one dropped/doubled letter,
//     a transposition like "recieve") land here.
//   - Everything else is wrong/again.
package grading

import (
	"strings"
)

// Op kinds for CharOp.Kind — the edit operation the alignment chose for one
// expected/heard rune pair. Del/Ins leave one side empty.
const (
	OpMatch = "match" // expected rune == heard rune
	OpSub   = "sub"   // expected rune replaced by a different heard rune
	OpDel   = "del"   // expected rune missing from the answer
	OpIns   = "ins"   // extra heard rune not in the expected word
	OpSwap  = "swap"  // adjacent transposition (one rune of a crossed pair)
)

// CharOp is one aligned rune pair of the diff between the normalized expected
// word and the normalized answer. Exactly one of Expected/Heard is empty only
// for del/ins; match/sub/swap carry both.
type CharOp struct {
	Expected string `json:"expected"`
	Heard    string `json:"heard"`
	Kind     string `json:"kind"`
}

// WordJudgment is the full outcome of grading one word.
type WordJudgment struct {
	Correct  bool     `json:"correct"`
	Rating   string   `json:"rating"` // FSRS verb: "good" | "hard" | "again"
	NearMiss bool     `json:"near_miss"`
	Blank    bool     `json:"blank"`    // no answer was submitted for this word
	Distance int      `json:"distance"` // Damerau-Levenshtein distance (0 for blanks)
	Diff     []CharOp `json:"diff"`     // full alignment; nil when Blank
}

// nearMissDistance is the maximum edit distance that still counts as "the
// student almost had it". Distance 1 covers the single-typo / dropped-letter /
// transposition family; anything further is a real miss.
const nearMissDistance = 1

// GradeWord compares a student's answer against the expected dictation word.
// lang is "en" or "zh" (the two normalization regimes, see Normalize).
func GradeWord(expected, heard, lang string) WordJudgment {
	if strings.TrimSpace(heard) == "" {
		return WordJudgment{Correct: false, Rating: "again", Blank: true}
	}

	normE := Normalize(expected, lang)
	normH := Normalize(heard, lang)
	if normE == normH {
		return WordJudgment{
			Correct:  true,
			Rating:   "good",
			Distance: 0,
			Diff:     alignment(normE, normH),
		}
	}

	e := []rune(normE)
	h := []rune(normH)
	dist := damerau(e, h)
	j := WordJudgment{
		Correct:  false,
		Distance: dist,
		Diff:     alignment(normE, normH),
	}
	// Near-miss: one edit away on a word long enough for that to be a typo
	// rather than a different word entirely. Still incorrect — the score must
	// not inflate — but FSRS hears "hard", and the report says 只差一点.
	if dist <= nearMissDistance && len(e) >= 2 {
		j.NearMiss = true
		j.Rating = "hard"
	} else {
		j.Rating = "again"
	}
	return j
}

// Normalize folds a word for comparison based on language: lowercase + trim,
// then zh strips punctuation/whitespace (preserving CJK) while en keeps only
// [a-z0-9]. This is the single normalization authority for grading; the
// handler-side compareAnswer delegates here.
func Normalize(word, lang string) string {
	w := strings.ToLower(strings.TrimSpace(word))
	if lang == "zh" || lang == "zh-CN" || lang == "chinese" {
		// Chinese: strip punctuation and whitespace only (preserve CJK chars).
		w = strings.ReplaceAll(w, " ", "")
		w = strings.ReplaceAll(w, "，", "")
		w = strings.ReplaceAll(w, "。", "")
		w = strings.ReplaceAll(w, "！", "")
		w = strings.ReplaceAll(w, "？", "")
		w = strings.ReplaceAll(w, ",", "")
		w = strings.ReplaceAll(w, ".", "")
		w = strings.ReplaceAll(w, "!", "")
		w = strings.ReplaceAll(w, "?", "")
		return w
	}
	// English: strip non-alphanumeric.
	var b strings.Builder
	for _, r := range w {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// damerau computes the restricted (optimal string alignment) Damerau-
// Levenshtein distance between two rune slices — Levenshtein plus adjacent
// transpositions, which is how spelling mistakes actually happen
// ("recieve", "香蕉"→"香焦" style single slips aside).
func damerau(e, h []rune) int {
	if len(e) == 0 {
		return len(h)
	}
	if len(h) == 0 {
		return len(e)
	}
	dp := damerauDP(e, h)
	return dp[len(e)][len(h)]
}

// damerauDP fills the OSA DP matrix: dp[i][j] = distance between e[:i] and
// h[:j]. Shared by damerau (reads the corner) and alignment (backtraces it)
// so the distance and the diff can never disagree (dictation words are short;
// the O(len²) matrix is irrelevant).
func damerauDP(e, h []rune) [][]int {
	n, m := len(e), len(h)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
		dp[i][0] = i
	}
	for j := 0; j <= m; j++ {
		dp[0][j] = j
	}

	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := 1
			if e[i-1] == h[j-1] {
				cost = 0
			}
			best := dp[i-1][j-1] + cost // substitution / match
			if v := dp[i-1][j] + 1; v < best {
				best = v // deletion
			}
			if v := dp[i][j-1] + 1; v < best {
				best = v // insertion
			}
			// Adjacent transposition.
			if i > 1 && j > 1 && e[i-1] == h[j-2] && e[i-2] == h[j-1] {
				if v := dp[i-2][j-2] + 1; v < best {
					best = v
				}
			}
			dp[i][j] = best
		}
	}
	return dp
}

// alignment produces the full char-level diff of the normalized forms by
// backtracking the same DP matrix damerau builds. Ties break in the fixed
// priority match > swap > sub > del > ins so the emitted op sequence is
// deterministic (tests pin exact sequences). For equal inputs the result is
// all matches — GradeWord uses it for the correct case too, so the report
// can render any word, right or wrong, the same way.
func alignment(normE, normH string) []CharOp {
	e := []rune(normE)
	h := []rune(normH)
	n, m := len(e), len(h)

	if n == 0 && m == 0 {
		return nil
	}

	dp := damerauDP(e, h)

	// Backtrace from (n, m), emitting ops in reverse then flipping.
	var ops []CharOp
	i, j := n, m
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && e[i-1] == h[j-1] && dp[i][j] == dp[i-1][j-1]:
			ops = append(ops, CharOp{Expected: string(e[i-1]), Heard: string(h[j-1]), Kind: OpMatch})
			i, j = i-1, j-1
		case i > 1 && j > 1 && e[i-1] == h[j-2] && e[i-2] == h[j-1] && dp[i][j] == dp[i-2][j-2]+1:
			ops = append(ops, CharOp{Expected: string(e[i-1]), Heard: string(h[j-1]), Kind: OpSwap})
			ops = append(ops, CharOp{Expected: string(e[i-2]), Heard: string(h[j-2]), Kind: OpSwap})
			i, j = i-2, j-2
		case i > 0 && j > 0 && dp[i][j] == dp[i-1][j-1]+1:
			ops = append(ops, CharOp{Expected: string(e[i-1]), Heard: string(h[j-1]), Kind: OpSub})
			i, j = i-1, j-1
		case i > 0 && dp[i][j] == dp[i-1][j]+1:
			ops = append(ops, CharOp{Expected: string(e[i-1]), Heard: "", Kind: OpDel})
			i--
		default: // j > 0, insertion
			ops = append(ops, CharOp{Expected: "", Heard: string(h[j-1]), Kind: OpIns})
			j--
		}
	}
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}
	return ops
}
