package e2ephoto

import (
	"os"
	"reflect"
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
// photo: 17 单词 + 13 词组, EVERY entry's picture-verified meaning read off
// the real photo (释义按图片人工标注 — never a dictionary sense; duck=躲避,
// skip=跳绳 stay the pinned key senses with the photo's printed bracketed
// forms declared as equivalents), the ellipsis alias table and the
// first-acceptance rules fixed BEFORE any real recognition ran. A silent
// edit to any of these must fail here rather than re-anchor expectations
// to a recognition result.
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
	seen := map[string]bool{}
	for _, e := range p.Entries {
		switch e.Kind {
		case KindWord:
			words++
		case KindPhrase:
			phrases++
		}
		want, ok := wantMeanings[e.Word]
		if !ok {
			t.Errorf("entry %q is not in the pinned picture-verified meaning table", e.Word)
			continue
		}
		seen[e.Word] = true
		if e.Meaning != want {
			t.Errorf("meaning[%s] = %q, want the picture-verified %q", e.Word, e.Meaning, want)
		}
	}
	if words != 17 || phrases != 13 {
		t.Errorf("annotation counts = %d words / %d phrases, want 17/13", words, phrases)
	}
	if len(seen) != len(wantMeanings) {
		t.Errorf("%d entries matched the pin table, want all %d (missing entries are pinned below)",
			len(seen), len(wantMeanings))
	}
	// 括注义等价表：照片印刷原文带括号，识别照抄原文也必须算对。
	for w, want := range map[string][]string{
		"duck": {"躲避（闪避）"},
		"skip": {"跳绳（跳过）"},
	} {
		for _, e := range p.Entries {
			if e.Word != w {
				continue
			}
			if !reflect.DeepEqual(e.MeaningAliases, want) {
				t.Errorf("meaningAliases[%s] = %v, want the photo-printed %v", w, e.MeaningAliases, want)
			}
		}
	}
	aliases := 0
	for _, e := range p.Entries {
		aliases += len(e.Aliases)
	}
	if aliases != 3 {
		t.Errorf("%d alias surfaces declared, want 3 (the ellipsis template phrases)", aliases)
	}
	if p.Rules != (Rules{MinWordHits: 16, MinPhraseHits: 12, MinPrecision: 0.95}) {
		t.Errorf("rules = %+v, want the pinned 16/17, 12/13, 0.95", p.Rules)
	}
}

// wantMeanings is the full human annotation read off the real photo
// (照片人工标注): 17 单词 + 13 词组, each with the meaning PRINTED beside it
// on the sheet — 括注／分号双义按原文照录，duck/skip 按计划钉定主要义.
var wantMeanings = map[string]string{
	"helpful":                  "有帮助的；有益的",
	"friendly":                 "友好的",
	"lovely":                   "可爱的",
	"different":                "不同的",
	"great":                    "好极了；很好",
	"interesting":              "有趣的",
	"writer":                   "作家",
	"painter":                  "画家",
	"smile":                    "微笑",
	"duck":                     "躲避",
	"skip":                     "跳绳",
	"afraid":                   "害怕的；担心的",
	"often":                    "经常；常常",
	"always":                   "总是；一直",
	"excited":                  "兴奋的；激动的",
	"hard":                     "困难的；努力的",
	"easy":                     "容易的",
	"be good at":               "擅长于",
	"help … (do) …":            "帮助（某人）做某事",
	"learn English":            "学习英语",
	"cheer for them":           "为他们喝彩",
	"want to be …":             "想成为…",
	"jump over":                "跳过",
	"slow down":                "慢下来；减速",
	"paint pictures":           "画画",
	"play a guessing game":     "玩猜谜游戏",
	"clean the blackboard":     "擦黑板",
	"share … with each other":  "互相分享",
	"tell interesting stories": "讲有趣的故事",
	"skip with a long rope":    "跳长绳",
}

// TestVerifyRealMaterial checks the pinned material hash against the
// real photo when XIAOTIAN_E2E_PHOTO_DIR is set; without it the photo
// (repo-external) is simply absent, so the check skips.
func TestVerifyRealMaterial(t *testing.T) {
	dir := os.Getenv("XIAOTIAN_E2E_PHOTO_DIR")
	if dir == "" {
		t.Skip("XIAOTIAN_E2E_PHOTO_DIR 未设置：实拍素材在仓库外，跳过哈希核对")
	}
	for _, p := range mustLoadReal(t) {
		if err := p.Verify(dir); err != nil {
			t.Errorf("Verify(%s/%s): %v", dir, p.File, err)
		}
	}
}
