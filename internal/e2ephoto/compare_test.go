package e2ephoto

import (
	"reflect"
	"testing"
)

// realPhoto loads the single annotated photo of the real manifest.
func realPhoto(t *testing.T) Photo {
	t.Helper()
	photos := mustLoadReal(t)
	if len(photos) != 1 {
		t.Fatalf("real manifest has %d photos, want 1", len(photos))
	}
	return photos[0]
}

// baseline builds the ideal recognition result: every annotated entry
// recognized verbatim, meanings filled in where annotated.
func baseline(p Photo) []Recognized {
	rows := make([]Recognized, 0, len(p.Entries))
	for _, e := range p.Entries {
		rows = append(rows, Recognized{Word: e.Word, Meaning: e.Meaning})
	}
	return rows
}

func findRow(rows []Recognized, word string) int {
	for i, r := range rows {
		if r.Word == word {
			return i
		}
	}
	panic("test helper: no row " + word)
}

// withSurface swaps the surface of one canonical row, keeping its meaning.
func withSurface(rows []Recognized, canonical, surface string) []Recognized {
	rows[findRow(rows, canonical)].Word = surface
	return rows
}

// withMeaning replaces the meaning of one canonical row.
func withMeaning(rows []Recognized, canonical, meaning string) []Recognized {
	rows[findRow(rows, canonical)].Meaning = meaning
	return rows
}

// dropRow removes one canonical row from the result.
func dropRow(rows []Recognized, canonical string) []Recognized {
	i := findRow(rows, canonical)
	return append(rows[:i], rows[i+1:]...)
}

func TestNormalizeWord(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Ｈｅｌｐｆｕｌ", "helpful"},                            // 全角→半角＋大小写
		{"BE　GOOD　AT", "be good at"},                      // 全角空格折叠
		{"Clean  the Blackboard", "clean the blackboard"}, // 空白折叠
		{"help ... (do) ...", "help … (do) …"},            // ASCII 点串→省略号
		{"no.", "no."}, // 单个点必须保留
		{"Ｎｏ. 12", "no. 12"},
	}
	for _, tc := range cases {
		if got := NormalizeWord(tc.in); got != tc.want {
			t.Errorf("NormalizeWord(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeMeaning(t *testing.T) {
	cases := []struct{ in, want string }{
		{"跳绳。", "跳绳"},
		{" 躲避 ", "躲避"},
		{"Duck!", "duck"},
	}
	for _, tc := range cases {
		if got := NormalizeMeaning(tc.in); got != tc.want {
			t.Errorf("NormalizeMeaning(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCompareIdealPasses: surface variation (case, full-width spaces,
// ASCII ellipsis) must still hit every entry — a perfect result passes
// with full recall and precision.
func TestCompareIdealPasses(t *testing.T) {
	p := realPhoto(t)
	rows := withSurface(baseline(p), "helpful", "Helpful")
	rows = withSurface(rows, "be good at", "BE　GOOD　AT")
	rows = withSurface(rows, "help … (do) …", "help ... (do) ...")
	rep := Compare(p, rows)
	if !rep.OK(p.Rules) {
		t.Fatalf("ideal result not OK: %+v", rep)
	}
	if rep.WordHits != 17 || rep.PhraseHits != 13 || rep.Recognized != 30 || rep.Precision() != 1 {
		t.Errorf("hits = %d/%d words/phrases, %d rows, precision %v; want 17/13, 30, 1",
			rep.WordHits, rep.PhraseHits, rep.Recognized, rep.Precision())
	}
	if len(rep.Missing)+len(rep.Extra)+len(rep.Noise)+len(rep.MustMiss)+len(rep.WrongMeanings) != 0 {
		t.Errorf("ideal result has differences: %+v", rep)
	}
}

// TestCompareRecallThreshold pins the fixed bar: 单词召回 16/17 tolerates
// exactly one miss, two misses fail.
func TestCompareRecallThreshold(t *testing.T) {
	p := realPhoto(t)
	rep := Compare(p, dropRow(baseline(p), "often"))
	if !rep.OK(p.Rules) {
		t.Errorf("one missed word should still pass the 16/17 bar: %+v", rep)
	}
	if rep.WordHits != 16 || !reflect.DeepEqual(rep.Missing, []string{"often"}) {
		t.Errorf("one miss: hits = %d, missing = %v; want 16, [often]", rep.WordHits, rep.Missing)
	}

	rep = Compare(p, dropRow(dropRow(baseline(p), "often"), "always"))
	if rep.OK(p.Rules) {
		t.Errorf("two missed words must fail the 16/17 bar: %+v", rep)
	}
	if rep.WordHits != 15 {
		t.Errorf("two misses: word hits = %d, want 15", rep.WordHits)
	}
}

// TestCompareMustHit: a must-hit word missing fails the run even while
// overall recall still meets the bar.
func TestCompareMustHit(t *testing.T) {
	p := realPhoto(t)
	for _, must := range p.MustHit {
		rep := Compare(p, dropRow(baseline(p), must))
		if rep.OK(p.Rules) {
			t.Errorf("dropping must-hit %q still passed: %+v", must, rep)
		}
		if !reflect.DeepEqual(rep.MustMiss, []string{must}) {
			t.Errorf("dropping %q: MustMiss = %v, want [%q]", must, rep.MustMiss, must)
		}
	}
}

// TestCompareSplitPhraseNeverJoins: 拆词 — a phrase recognized as two
// fragments misses as a whole, the fragments surface as extra entries,
// and nothing glues them into a hit.
func TestCompareSplitPhraseNeverJoins(t *testing.T) {
	p := realPhoto(t)
	rows := append(dropRow(baseline(p), "clean the blackboard"),
		Recognized{Word: "clean the"}, Recognized{Word: "blackboard"})
	rep := Compare(p, rows)
	if rep.OK(p.Rules) {
		t.Fatalf("split phrase must fail: %+v", rep)
	}
	if rep.PhraseHits != 12 || !contains(rep.Missing, "clean the blackboard") {
		t.Errorf("phrase counted wrong: hits = %d, missing = %v; want 12 with the whole phrase missed",
			rep.PhraseHits, rep.Missing)
	}
	if !contains(rep.Extra, "clean the") || !contains(rep.Extra, "blackboard") {
		t.Errorf("fragments must surface as extras, got %v", rep.Extra)
	}
}

// TestComparePrecision: two junk rows drag precision to 30/32 < 0.95 and
// fail the run with nothing else wrong.
func TestComparePrecision(t *testing.T) {
	p := realPhoto(t)
	rows := append(baseline(p), Recognized{Word: "livety"}, Recognized{Word: "writter"})
	rep := Compare(p, rows)
	if rep.OK(p.Rules) {
		t.Fatalf("precision 30/32 must fail the 0.95 bar: %+v", rep)
	}
	if rep.Precision() != 30.0/32 {
		t.Errorf("precision = %v, want %v", rep.Precision(), 30.0/32)
	}
	if !reflect.DeepEqual(rep.Extra, []string{"livety", "writter"}) ||
		len(rep.Noise) > 0 || len(rep.MustMiss) > 0 || len(rep.WrongMeanings) > 0 {
		t.Errorf("only precision should fail: %+v", rep)
	}
}

// TestCompareEmptyResult: an empty recognition fails with everything
// missing rather than dividing by zero.
func TestCompareEmptyResult(t *testing.T) {
	p := realPhoto(t)
	rep := Compare(p, nil)
	if rep.OK(p.Rules) || rep.Precision() != 0 || len(rep.Missing) != len(p.Entries) {
		t.Errorf("empty result: OK = %v, precision = %v, missing = %d; want fail/0/%d",
			rep.OK(p.Rules), rep.Precision(), len(rep.Missing), len(p.Entries))
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
