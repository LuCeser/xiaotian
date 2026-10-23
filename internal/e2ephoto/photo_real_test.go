package e2ephoto

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

// realManifest points at the repo's milestone-1 photo annotation. Go
// tests run with cwd = package dir, so the repo root is ../..
const realManifest = "../../tests/e2e-m1/photos/manifest.json"

// mustLoadReal loads the real annotation or fails the test.
func mustLoadReal(t *testing.T) []Photo {
	t.Helper()
	photos, err := Load(realManifest)
	if err != nil {
		t.Fatalf("Load(real manifest): %v", err)
	}
	return photos
}

// TestLoadRealManifest pins the plan §5 baseline for the one annotated
// photo: 17 单词 + 13 词组, with every picture-verified meaning stored in
// the manifest as the single source of truth, plus the ellipsis alias table
// and the first-acceptance rules fixed BEFORE any real recognition ran.
func TestLoadRealManifest(t *testing.T) {
	photos := mustLoadReal(t)
	if len(photos) != 1 {
		t.Fatalf("real manifest has %d photos, want 1", len(photos))
	}
	p := photos[0]
	if p.Subject != SubjectEnglish {
		t.Errorf("subject = %q, want %q", p.Subject, SubjectEnglish)
	}
	words, phrases := 0, 0
	for _, e := range p.Entries {
		switch e.Kind {
		case KindWord:
			words++
		case KindPhrase:
			phrases++
		}
		if e.Meaning == "" {
			t.Errorf("entry %q has no picture-verified meaning", e.Word)
		}
	}
	if words != 17 || phrases != 13 {
		t.Errorf("annotation counts = %d words / %d phrases, want 17/13", words, phrases)
	}
	aliases := 0
	for _, e := range p.Entries {
		aliases += len(e.Aliases)
	}
	if aliases != 5 {
		t.Errorf("%d alias surfaces declared, want 5 (紧凑形式 + 照片实际印刷的省略号空格)", aliases)
	}
	if p.Rules != (Rules{MinWordHits: 16, MinPhraseHits: 12, MinPrecision: 0.95}) {
		t.Errorf("rules = %+v, want the pinned 16/17, 12/13, 0.95", p.Rules)
	}
}

// TestRealPhotoMeaningsRejectDictionarySubstitutions proves that a result
// faithful to the manifest's photo transcription passes, while the exact
// dictionary-style substitutions that previously displaced the printed
// text fail. The correct meanings live only in manifest.json; this test
// intentionally lists only known-wrong replacements.
func TestRealPhotoMeaningsRejectDictionarySubstitutions(t *testing.T) {
	p := realPhoto(t)
	faithful := baseline(p)
	if rep := Compare(p, faithful); !rep.OK(p.Rules) {
		t.Fatalf("photo-faithful recognition must pass: %+v", rep)
	}

	dictionaryReplacements := map[string]string{
		"helpful":        "有帮助的；有益的",
		"great":          "好极了；很好",
		"afraid":         "害怕的；担心的",
		"often":          "经常；常常",
		"always":         "总是；一直",
		"excited":        "兴奋的；激动的",
		"hard":           "困难的；努力的",
		"be good at":     "擅长于",
		"help … (do) …":  "帮助（某人）做某事",
		"slow down":      "慢下来；减速",
		"paint pictures": "画画",
	}
	replaced := baseline(p)
	for word, meaning := range dictionaryReplacements {
		replaced = withMeaning(replaced, word, meaning)
	}
	rep := Compare(p, replaced)
	if rep.OK(p.Rules) {
		t.Fatalf("dictionary substitutions must fail: %+v", rep)
	}
	if len(rep.WrongMeanings) != len(dictionaryReplacements) {
		t.Fatalf("wrong meanings = %+v, want all %d dictionary substitutions rejected",
			rep.WrongMeanings, len(dictionaryReplacements))
	}
	for _, wrong := range rep.WrongMeanings {
		if got := dictionaryReplacements[wrong.Word]; got != wrong.Got {
			t.Errorf("unexpected mismatch %+v; replacement = %q", wrong, got)
		}
	}
}

// TestVerifyRealMaterial checks the pinned material hash against the
// real photo when XIAOTIAN_E2E_PHOTO_DIR is set. Without the variable,
// or with the variable set but the material file missing (计划第 5 节:
// 文件缺失只 skip 照片用例), the check skips; a PRESENT file that is
// corrupted or hashes wrong stays a hard failure.
func TestVerifyRealMaterial(t *testing.T) {
	dir := os.Getenv("XIAOTIAN_E2E_PHOTO_DIR")
	if dir == "" {
		t.Skip("XIAOTIAN_E2E_PHOTO_DIR 未设置：实拍素材在仓库外，跳过哈希核对")
	}
	for _, p := range mustLoadReal(t) {
		err := p.Verify(dir)
		if errors.Is(err, fs.ErrNotExist) {
			t.Skipf("素材文件缺失，按计划第 5 节 skip 照片哈希核对：%v", err)
		}
		if err != nil {
			t.Errorf("Verify(%s/%s): %v", dir, p.File, err)
		}
	}
}
