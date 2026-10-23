package e2ephoto

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hex64 is a stand-in 64-hex digest for validation fixtures.
const hex64 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// validPhoto is the smallest manifest JSON that passes validation.
const validPhoto = `{"version":1,"photos":[{` +
	`"id":"p","file":"a.jpg","sha256":"` + hex64 + `","subject":"english",` +
	`"entries":[{"word":"duck","kind":"word","meaning":"躲避"}],` +
	`"mustHit":["duck"],"noiseSubstrings":["class"],` +
	`"rules":{"minWordHits":1,"minPhraseHits":0,"minPrecision":0.95}}]}`

// secondPhoto is another valid block, used to build duplicate-key cases.
const secondPhoto = `{"id":"p2","file":"b.jpg","sha256":"` + hex64 + `","subject":"english",` +
	`"entries":[{"word":"duck","kind":"word","meaning":"躲避"}],"mustHit":["duck"],` +
	`"noiseSubstrings":["x"],"rules":{"minWordHits":1,"minPhraseHits":0,"minPrecision":1}}`

func TestLoadValidation(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantErr string // substring the Load error must mention; "" = any error
	}{
		{"wrong version", `{"version":2,"photos":[]}`, "version"},
		{"no photos", `{"version":1,"photos":[]}`, "no photos"},
		{"broken json", `{"version":1,"photos":`, "parse"},
		{"empty id", strings.Replace(validPhoto, `"id":"p"`, `"id":""`, 1), "empty id"},
		{"absolute file", strings.Replace(validPhoto, `"file":"a.jpg"`, `"file":"/etc/a.jpg"`, 1), "relative name"},
		{"escaping file", strings.Replace(validPhoto, `"file":"a.jpg"`, `"file":"../a.jpg"`, 1), "relative name"},
		{"short sha256", strings.Replace(validPhoto, hex64, "abc", 1), "64-hex"},
		{"non-hex sha256", strings.Replace(validPhoto, hex64, strings.Repeat("z", 64), 1), "64-hex"},
		{"sha256 with punctuation", strings.Replace(validPhoto, hex64, strings.Repeat("a-", 32), 1), "64-hex"},
		{"unknown subject", strings.Replace(validPhoto, `"subject":"english"`, `"subject":"math"`, 1), "unknown subject"},
		{"unknown kind", strings.Replace(validPhoto, `"kind":"word"`, `"kind":"line"`, 1), "unknown kind"},
		{"empty word", strings.Replace(validPhoto, `"word":"duck"`, `"word":""`, 1), "empty word"},
		{"entry without meaning", strings.Replace(validPhoto, `,"meaning":"躲避"`, ``, 1), "no picture-verified meaning"},
		// meanings empty AFTER normalization compare equal to a recognized
		// row with no meaning — Load must reject them like a missing one
		// (PR #70 复审 P2).
		{"whitespace-only meaning", strings.Replace(validPhoto, `"meaning":"躲避"`, `"meaning":" "`, 1), "no picture-verified meaning"},
		{"punctuation-only meaning", strings.Replace(validPhoto, `"meaning":"躲避"`, `"meaning":"。"`, 1), "no picture-verified meaning"},
		{"empty meaning alias", strings.Replace(validPhoto, `"meaning":"躲避"}]`, `"meaning":"躲避","meaningAliases":[""]}]`, 1), "empty meaning"},
		{"whitespace-only meaning alias", strings.Replace(validPhoto, `"meaning":"躲避"}]`, `"meaning":"躲避","meaningAliases":["　"]}]`, 1), "empty meaning"},
		// normalization collisions: Compare keys entries by NormalizeWord,
		// so Load must reject surfaces that would overwrite each other.
		{"case-fold duplicate word", strings.Replace(validPhoto,
			`"entries":[{"word":"duck","kind":"word","meaning":"躲避"}]`,
			`"entries":[{"word":"duck","kind":"word","meaning":"躲避"},`+
				`{"word":"Duck","kind":"word","meaning":"躲避"}]`, 1), "collides"},
		{"fullwidth duplicate word", strings.Replace(validPhoto,
			`"entries":[{"word":"duck","kind":"word","meaning":"躲避"}]`,
			`"entries":[{"word":"duck","kind":"word","meaning":"躲避"},`+
				`{"word":"ｄｕｃｋ","kind":"word","meaning":"躲避"}]`, 1), "collides"},
		{"alias collides with word", strings.Replace(validPhoto,
			`"entries":[{"word":"duck","kind":"word","meaning":"躲避"}]`,
			`"entries":[{"word":"duck","kind":"word","meaning":"躲避"},`+
				`{"word":"goose","kind":"word","meaning":"鹅","aliases":["duck"]}]`, 1), "collides"},
		{"alias collides with alias", strings.Replace(validPhoto,
			`"entries":[{"word":"duck","kind":"word","meaning":"躲避"}]`,
			`"entries":[{"word":"duck","kind":"word","meaning":"躲避","aliases":["fowl"]},`+
				`{"word":"goose","kind":"word","meaning":"鹅","aliases":["fowl"]}]`, 1), "collides"},
		{"whitespace-only word", strings.Replace(validPhoto, `"word":"duck"`, `"word":"　"`, 1), "empty surface"},
		{"no entries", strings.Replace(validPhoto, `"entries":[{"word":"duck","kind":"word","meaning":"躲避"}],`, `"entries":[],`, 1), "no annotated entries"},
		{"empty mustHit", strings.Replace(validPhoto, `"mustHit":["duck"]`, `"mustHit":[]`, 1), "mustHit is empty"},
		{"mustHit not annotated", strings.Replace(validPhoto, `"mustHit":["duck"]`, `"mustHit":["goose"]`, 1), "not an annotated word"},
		{"empty noiseSubstrings", strings.Replace(validPhoto, `"noiseSubstrings":["class"]`, `"noiseSubstrings":[]`, 1), "noiseSubstrings is empty"},
		{"minWordHits above count", strings.Replace(validPhoto, `"minWordHits":1`, `"minWordHits":2`, 1), "minWordHits"},
		{"minPhraseHits above count", strings.Replace(validPhoto, `"minPhraseHits":0`, `"minPhraseHits":1`, 1), "minPhraseHits"},
		{"zero precision", strings.Replace(validPhoto, `"minPrecision":0.95`, `"minPrecision":0`, 1), "minPrecision"},
		{"precision above one", strings.Replace(validPhoto, `"minPrecision":0.95`, `"minPrecision":1.5`, 1), "minPrecision"},
		// strict decoding: a typo'd key must fail loudly instead of decoding
		// to 0 and silently lowering the bar (PR #70 复审 P2).
		{"typo threshold key", strings.Replace(validPhoto, `"minWordHits":1`, `"minWordHit":1`, 1), "unknown field"},
		{"unknown entry field", strings.Replace(validPhoto, `,"meaning":"躲避"}]`, `,"meaning":"躲避","extra":1}]`, 1), "unknown field"},
		{"unknown top-level field", strings.Replace(validPhoto, `{"version":1`, `{"version":1,"note":"x"`, 1), "unknown field"},
		// …and the threshold keys must be WRITTEN OUT: an omitted one would
		// decode to 0 while an explicit 0 stays a meaningful declared bar.
		{"omitted minWordHits key", strings.Replace(validPhoto, `"minWordHits":1,`, ``, 1), `rules is missing "minWordHits"`},
		{"omitted minPrecision key", strings.Replace(validPhoto, `,"minPrecision":0.95}`, `}`, 1), `rules is missing "minPrecision"`},
		{"omitted rules object", strings.Replace(validPhoto, `,"rules":{"minWordHits":1,"minPhraseHits":0,"minPrecision":0.95}`, ``, 1), `rules is missing "minWordHits"`},
		// duplicate id / file need a second photo block in the same file
		{"duplicate id", strings.Replace(
			strings.Replace(validPhoto, `}]}`, `},`+secondPhoto+`]}`, 1),
			`"id":"p2"`, `"id":"p"`, 1), "duplicate id"},
		{"duplicate file", strings.Replace(
			strings.Replace(validPhoto, `}]}`, `},`+secondPhoto+`]}`, 1),
			`"file":"b.jpg"`, `"file":"a.jpg"`, 1), "duplicate file"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, []byte(tc.json), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("Load() succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load() error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}

	t.Run("valid manifest loads", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "manifest.json")
		if err := os.WriteFile(path, []byte(validPhoto), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		photos, err := Load(path)
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if len(photos) != 1 || photos[0].ID != "p" || len(photos[0].Entries) != 1 {
			t.Fatalf("Load() = %+v, want the single photo p with one entry", photos)
		}
	})

	t.Run("uppercase hex sha256 loads", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "manifest.json")
		json := strings.Replace(validPhoto, hex64, strings.ToUpper(hex64), 1)
		if err := os.WriteFile(path, []byte(json), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		if _, err := Load(path); err != nil {
			t.Fatalf("Load(uppercase hex digest) = %v, want nil: Verify compares case-insensitively", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
			t.Fatal("Load(missing manifest) succeeded, want error")
		}
	})
}

func TestVerifyMaterial(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.jpg"), []byte("jpeg bytes"), 0o600); err != nil {
		t.Fatalf("write material: %v", err)
	}
	sum := sha256.Sum256([]byte("jpeg bytes"))
	p := Photo{ID: "p", File: "a.jpg", SHA256: hex.EncodeToString(sum[:])}

	if err := p.Verify(dir); err != nil {
		t.Fatalf("Verify(matching material) = %v, want nil", err)
	}
	p.SHA256 = hex64
	if err := p.Verify(dir); err == nil || !strings.Contains(err.Error(), "hashes to") {
		t.Fatalf("Verify(wrong hash) = %v, want a hash-mismatch hard failure", err)
	}
	p.File = "absent.jpg"
	err := p.Verify(dir)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Verify(missing material) = %v, want fs.ErrNotExist so the live run can skip", err)
	}
}
