package e2ephoto

import (
	"strings"
	"testing"
)

// TestCompareNoiseHardFails pins 计划第 5 节's noise rule: 表头／姓名栏／
// 标题／小贴士 misread as entries is a HARD failure even while precision
// would still clear the 0.95 bar.
func TestCompareNoiseHardFails(t *testing.T) {
	p := realPhoto(t)
	cases := []struct {
		name string
		word string
		mark string // substring the noise reason must mention
	}{
		{"class name row", "Class 3  Name Li Ming  No. 12", "表头"},
		{"tips box", "小贴士：遇到不会的单词可以先跳过", "小贴士"},
		{"sheet title", "4A Unit 2 Reading Sheet", "unit"},
		// 照片上真实存在的中文标题／分区标题（英语朗读纸、一、单词、
		// 二、词组、三、句子）混入词条同样必须硬失败（PR #65 复审 P1：
		// 单词／词组等两字标题既不命中噪声子串也不足例句阈值）。
		{"chinese sheet title", "英语朗读纸", "朗读纸"},
		{"chinese word-section header", "单词", "单词"},
		{"chinese phrase-section header", "二、词组", "词组"},
		{"chinese sentence-section header", "三、句子", "句子"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := Compare(p, append(baseline(p), Recognized{Word: tc.word}))
			if rep.OK(p.Rules) {
				t.Fatalf("noise row must hard-fail the run: %+v", rep)
			}
			if len(rep.Noise) != 1 || !strings.Contains(rep.Noise[0], tc.mark) {
				t.Errorf("noise = %v, want one row citing %q", rep.Noise, tc.mark)
			}
			if rep.Precision() < p.Rules.MinPrecision {
				t.Errorf("precision %v already below the bar; the case must show noise failing OUTRIGHT, not via precision",
					rep.Precision())
			}
		})
	}
}

// TestCompareCheckmarkNoise: a handwritten tick merged into a word
// (friendly✓) is hard noise; the tick is never stripped to force a hit,
// so the word itself also counts as missed.
func TestCompareCheckmarkNoise(t *testing.T) {
	p := realPhoto(t)
	rep := Compare(p, withSurface(baseline(p), "friendly", "friendly✓"))
	if rep.OK(p.Rules) {
		t.Fatalf("checkmark row must hard-fail the run: %+v", rep)
	}
	if len(rep.Noise) != 1 || !strings.Contains(rep.Noise[0], "勾号") {
		t.Errorf("noise = %v, want one row citing the tick", rep.Noise)
	}
	if !contains(rep.Missing, "friendly") {
		t.Errorf("friendly must be missed, not rescued by deleting the tick: %v", rep.Missing)
	}
}

// TestCompareSentenceNoise: a whole example sentence misread as an entry
// is hard noise (完整例句).
func TestCompareSentenceNoise(t *testing.T) {
	p := realPhoto(t)
	rep := Compare(p, append(baseline(p), Recognized{Word: "i often play football with my friends"}))
	if rep.OK(p.Rules) {
		t.Fatalf("sentence row must hard-fail the run: %+v", rep)
	}
	if len(rep.Noise) != 1 || !strings.Contains(rep.Noise[0], "完整例句") {
		t.Errorf("noise = %v, want one row citing the sentence", rep.Noise)
	}
}

// TestCompareWrongMeaning pins the picture-meaning rule: duck recognized
// with the dictionary sense 鸭子 is wrong (the photo means 躲避), while an
// equivalent with listed tolerance (trailing punctuation on 跳绳) passes.
func TestCompareWrongMeaning(t *testing.T) {
	p := realPhoto(t)
	rows := withMeaning(baseline(p), "duck", "鸭子")
	rows = withMeaning(rows, "skip", "跳绳。")
	rep := Compare(p, rows)
	if rep.OK(p.Rules) {
		t.Fatalf("wrong picture meaning must fail the run: %+v", rep)
	}
	if len(rep.WrongMeanings) != 1 || rep.WrongMeanings[0] != (WrongMeaning{Word: "duck", Want: "躲避", Got: "鸭子"}) {
		t.Errorf("wrong meanings = %+v, want exactly duck wanting 躲避 got 鸭子", rep.WrongMeanings)
	}
	// an annotated meaning must actually be there: empty is also wrong
	rep = Compare(p, withMeaning(baseline(p), "duck", ""))
	if len(rep.WrongMeanings) != 1 || rep.WrongMeanings[0].Word != "duck" {
		t.Errorf("empty meaning for annotated duck: %+v, want one duck mismatch", rep.WrongMeanings)
	}

	// 照片印刷原文带括注义（躲避（闪避）／跳绳（跳过）），已事先列为
	// 等义表达：识别按原文照抄也必须通过。
	rows = withMeaning(baseline(p), "duck", "躲避（闪避）")
	rows = withMeaning(rows, "skip", "跳绳（跳过）")
	if rep := Compare(p, rows); !rep.OK(p.Rules) {
		t.Fatalf("photo-printed bracketed meanings must pass via declared aliases: %+v", rep)
	}
}

// TestCompareEllipsisAliases: only the pre-declared compact alias (and
// normalization of dot-runs) rescues the ellipsis template phrases; an
// undeclared paraphrase is a miss plus an extra, never a fuzzy hit. One
// missed phrase sits inside the tolerated 12/13 bar — two fail it, and
// key phrases are covered by must-hit instead.
func TestCompareEllipsisAliases(t *testing.T) {
	p := realPhoto(t)
	rows := withSurface(baseline(p), "help … (do) …", "help…(do)…") // declared alias
	rows = withSurface(rows, "share … with each other", "share...with each other")
	rep := Compare(p, rows)
	if !rep.OK(p.Rules) {
		t.Fatalf("declared alias surfaces must hit: %+v", rep)
	}

	rows = withSurface(baseline(p), "help … (do) …", "help sb do sth")
	rep = Compare(p, rows)
	if !contains(rep.Missing, "help … (do) …") || !contains(rep.Extra, "help sb do sth") {
		t.Fatalf("paraphrase must miss and surface as extra: missing %v extra %v", rep.Missing, rep.Extra)
	}
	if rep.PhraseHits != 12 || !rep.OK(p.Rules) {
		t.Errorf("one paraphrased phrase = %d hits, OK = %v; want 12 hits, still inside the 12/13 bar",
			rep.PhraseHits, rep.OK(p.Rules))
	}

	rep = Compare(p, withSurface(rows, "want to be …", "want to be a teacher"))
	if rep.OK(p.Rules) {
		t.Fatalf("two paraphrased phrases must fail the 12/13 bar: %+v", rep)
	}
}

// TestCompareMeaningAlias pins the equivalent-expression rule on a
// synthetic photo: a pre-declared 等义表达 passes, anything else fails.
func TestCompareMeaningAlias(t *testing.T) {
	p := Photo{
		ID: "synthetic", Subject: SubjectEnglish,
		Entries: []Entry{{Word: "skip", Kind: KindWord, Meaning: "跳绳",
			MeaningAliases: []string{"跳绳子"}}},
		MustHit: []string{"skip"}, NoiseSubstrings: []string{"class"},
		Rules: Rules{MinWordHits: 1, MinPhraseHits: 0, MinPrecision: 1},
	}
	rep := Compare(p, []Recognized{{Word: "Skip", Meaning: "跳绳子。"}})
	if !rep.OK(p.Rules) {
		t.Fatalf("declared equivalent meaning must pass: %+v", rep)
	}
	rep = Compare(p, []Recognized{{Word: "skip", Meaning: "跳远"}})
	if rep.OK(p.Rules) || len(rep.WrongMeanings) != 1 {
		t.Fatalf("undeclared meaning must fail exactly once: %+v", rep)
	}
}
