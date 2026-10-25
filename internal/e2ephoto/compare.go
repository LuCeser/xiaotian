package e2ephoto

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Recognized is one row of a fixed recognition result — the same shape
// the photo pipeline saves from a real response: the word/phrase surface
// plus the photo's own meaning text. Tests construct rows (固定识别结
// 果, no OCR call); the live spec (计划步骤 25) feeds the single saved
// real response, so one recognition serves every assertion.
type Recognized struct {
	Word    string `json:"word"`
	Meaning string `json:"meaning"`
}

// checkmarks are the runes a handwritten tick may be transcribed as. A
// tick merged into a word (friendly✓) is hard noise; it is NEVER
// stripped from the surface to force a hit (不删掉词内有意义的字符).
var checkmarks = map[rune]bool{'✓': true, '✔': true, '√': true, '∨': true}

// sentenceTokens: an unmatched row with at least this many tokens is a
// whole sentence from the reading region, not a word-list row. The
// longest true phrase ("skip with a long rope") has 5 tokens and is a
// hit — never an extra — when recognized as one entry.
const sentenceTokens = 5

// dotRunRE folds ASCII dot-runs to the ellipsis they abbreviate; single
// dots stay ("no." keeps its dot).
var dotRunRE = regexp.MustCompile(`\.{2,}`)

// NormalizeWord folds a word surface for comparison: NFKC (全角→半角),
// whitespace collapse, English case fold, dot-runs → "…". Nothing else
// is stripped or fuzzy-matched — a misread stays a misread, and split
// fragments are never joined into one hit.
func NormalizeWord(s string) string {
	n := strings.ToLower(norm.NFKC.String(s))
	n = strings.Join(strings.Fields(n), " ")
	return dotRunRE.ReplaceAllString(n, "…")
}

// meaningTail is the punctuation trimmed off the ends of a meaning.
const meaningTail = "。．.,，、;；:：!！?？·—-"

// NormalizeMeaning folds a meaning the same way, additionally trimming
// edge punctuation so “跳绳。” equals “跳绳”.
func NormalizeMeaning(s string) string {
	n := strings.ToLower(norm.NFKC.String(s))
	n = strings.Join(strings.Fields(n), " ")
	return strings.Trim(n, meaningTail)
}

// WrongMeaning records one 释义 mismatch on a hit entry: the annotation
// wanted Want but the recognition returned Got.
type WrongMeaning struct {
	Word string
	Want string
	Got  string
}

// Report categorizes the comparison outcome, so a failing first real
// recognition reports the difference instead of a bare boolean (首次识
// 别失败保留差异并分析，不改阈值不改标注).
type Report struct {
	Missing       []string       // 漏词／漏词组；拆词也表现为整条缺失
	Extra         []string       // 额外条目（非硬噪声），单独列出
	Noise         []string       // 表头／姓名栏／勾号／完整例句混入 → 硬失败
	MustMiss      []string       // 必须命中但缺失（Missing 的子集）
	WrongMeanings []WrongMeaning // 命中词条的图片释义不符
	WordHits      int
	PhraseHits    int
	Recognized    int // all rows, noise included — precision denominator
}

// Precision is hit entries over all recognized rows.
func (r Report) Precision() float64 {
	if r.Recognized == 0 {
		return 0
	}
	return float64(r.WordHits+r.PhraseHits) / float64(r.Recognized)
}

// OK applies the plan's first-acceptance rules: no hard noise, no
// missed must-hit, no wrong picture meaning, and recall/precision at or
// above the pinned thresholds.
func (r Report) OK(rules Rules) bool {
	return len(r.Noise) == 0 && len(r.MustMiss) == 0 && len(r.WrongMeanings) == 0 &&
		r.WordHits >= rules.MinWordHits && r.PhraseHits >= rules.MinPhraseHits &&
		r.Precision() >= rules.MinPrecision
}

// Compare judges a fixed recognition result against the annotation. A
// row hits an entry only on an exact normalized-surface match — the
// canonical word or a pre-declared alias. No edit distance, no in-word
// deletion, no joining of fragments: 漏词、拆词、拼写损坏和噪声都成为
// 可报告的失败而不是被宽容吞掉. EVERY row matching an entry surface has
// its meaning checked — a repeated word face stays an extra entry but a
// conflicting picture meaning on it fails the run in either row order.
func Compare(p Photo, rows []Recognized) Report {
	var r Report
	r.Recognized = len(rows)
	surface := make(map[string]int, 2*len(p.Entries))
	for i, e := range p.Entries {
		surface[NormalizeWord(e.Word)] = i
		for _, a := range e.Aliases {
			surface[NormalizeWord(a)] = i
		}
	}
	// hitRows keeps every row index matching entry i, not just the first:
	// 只核对首行会让后到的重复词面带着冲突释义混进 Extra 假绿（PR #70
	// 复审 P2），duck=鸭子 无论排在正确行前后都必须判失败。
	hitRows := make([][]int, len(p.Entries))
	for gi, g := range rows {
		i, ok := surface[NormalizeWord(g.Word)]
		if ok {
			hitRows[i] = append(hitRows[i], gi)
			if len(hitRows[i]) > 1 {
				// 重复词面仍按额外条目列出（精确率分母已含这一行），
				// 释义在下方与首次命中一并核对。
				r.Extra = append(r.Extra, g.Word)
				continue
			}
			if p.Entries[i].Kind == KindWord {
				r.WordHits++
			} else {
				r.PhraseHits++
			}
			continue
		}
		if reason := noiseReason(g.Word, p.NoiseSubstrings); reason != "" {
			r.Noise = append(r.Noise, fmt.Sprintf("%s（%s）", g.Word, reason))
		} else {
			r.Extra = append(r.Extra, g.Word)
		}
	}
	for i, e := range p.Entries {
		if len(hitRows[i]) == 0 {
			r.Missing = append(r.Missing, e.Word)
			continue
		}
		if e.Meaning == "" {
			// 未标注释义不允许静默跳过（PR #65 复审 P1）：词面全对也
			// 必须失败 —— Load 同样拒绝缺标注的 manifest。
			r.WrongMeanings = append(r.WrongMeanings, WrongMeaning{
				Word: e.Word, Want: "（图片释义未标注）", Got: rows[hitRows[i][0]].Meaning,
			})
			continue
		}
		for _, gi := range hitRows[i] {
			have := NormalizeMeaning(rows[gi].Meaning)
			if have != NormalizeMeaning(e.Meaning) && !meaningAliased(e.MeaningAliases, have) {
				r.WrongMeanings = append(r.WrongMeanings, WrongMeaning{
					Word: e.Word, Want: e.Meaning, Got: rows[gi].Meaning,
				})
			}
		}
	}
	for _, m := range p.MustHit {
		if len(hitRows[surface[NormalizeWord(m)]]) == 0 {
			r.MustMiss = append(r.MustMiss, m)
		}
	}
	return r
}

// meaningAliased reports whether the normalized recognized meaning is a
// pre-declared equivalent expression.
func meaningAliased(aliases []string, have string) bool {
	for _, a := range aliases {
		if NormalizeMeaning(a) == have {
			return true
		}
	}
	return false
}

// noiseReason classifies an unmatched row as hard noise: a handwritten
// tick merged into a word, a header/name-field fragment, or a whole
// sentence from the reading region. Such rows fail the run outright —
// precision alone would let a lone "Class Name No." row pass.
func noiseReason(word string, subs []string) string {
	n := NormalizeWord(word)
	for _, c := range n {
		if checkmarks[c] {
			return "勾号混入词条"
		}
	}
	for _, s := range subs {
		if strings.Contains(n, strings.ToLower(s)) {
			return "表头／姓名栏／标题混入词条：" + s
		}
	}
	if len(strings.Fields(n)) >= sentenceTokens {
		return "完整例句混入词条"
	}
	return ""
}
