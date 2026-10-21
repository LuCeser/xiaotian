package e2ephoto

import (
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
// photo: 17 单词 + 13 词组, the two picture-verified meanings (duck=躲避,
// skip=跳绳 — never a dictionary sense), the ellipsis alias table and the
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
	meanings := map[string]string{}
	for _, e := range p.Entries {
		switch e.Kind {
		case KindWord:
			words++
		case KindPhrase:
			phrases++
		}
		if e.Meaning != "" {
			meanings[e.Word] = e.Meaning
		}
	}
	if words != 17 || phrases != 13 {
		t.Errorf("annotation counts = %d words / %d phrases, want 17/13", words, phrases)
	}
	for w, m := range map[string]string{"duck": "躲避", "skip": "跳绳"} {
		if meanings[w] != m {
			t.Errorf("meaning[%s] = %q, want the picture-verified %q", w, meanings[w], m)
		}
	}
	if len(meanings) != 2 {
		t.Errorf("%d meanings annotated, want exactly the two picture-verified ones", len(meanings))
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
