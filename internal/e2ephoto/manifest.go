// Package e2ephoto pins the milestone-1 external-photo annotation (计划
// 第 5 节): the repo keeps ONLY the relative filename, the material hash
// and the human annotation with the comparison rules — the photo itself
// never enters the repo (it lives under XIAOTIAN_E2E_PHOTO_DIR). The
// comparator judges a FIXED recognition result against that annotation,
// so quality assertions need no OCR call from tests (计划步骤 24), and
// the expectations are pinned BEFORE the first real recognition runs —
// they are never derived from or rewritten by model output.
package e2ephoto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Entry kinds: a 单词 stays one token, a 词组 stays ONE entry with its
// spaces intact — recognition may never split it into separate hits.
const (
	KindWord   = "word"
	KindPhrase = "phrase"
)

// Subject values; 语文实拍素材以后按同一 manifest 扩展（计划第 5 节）.
const (
	SubjectEnglish = "english"
	SubjectChinese = "chinese"
)

// Entry is one human-annotated expected item. Meaning is the photo's OWN
// meaning as read off the image (图片证据, e.g. duck=躲避 — never replaced
// by a dictionary sense) and is REQUIRED: an unannotated entry would let
// a recognition pass on surface alone (PR #65 复审 P1), so Load rejects
// it. Aliases are the pre-declared accepted surfaces (the ellipsis
// template phrases); MeaningAliases are pre-declared equivalent
// expressions and never replace the photo text stored in Meaning.
type Entry struct {
	Word           string   `json:"word"`
	Kind           string   `json:"kind"`
	Meaning        string   `json:"meaning,omitempty"`
	Aliases        []string `json:"aliases,omitempty"`
	MeaningAliases []string `json:"meaningAliases,omitempty"`
}

// Rules are the first-acceptance thresholds fixed in the plan BEFORE any
// real recognition ran (单词召回 ≥16/17、词组召回 ≥12/13、词条精确率
// ≥95%). A failing first run is analyzed against the difference — these
// numbers are never loosened to make it pass.
type Rules struct {
	MinWordHits   int     `json:"minWordHits"`
	MinPhraseHits int     `json:"minPhraseHits"`
	MinPrecision  float64 `json:"minPrecision"`
}

// Photo is one annotated photo. File is a plain relative name resolved
// against the photo dir; SHA256 pins the material — a present file with
// the wrong hash is a hard failure, only a MISSING file may become a
// skip (计划第 5 节).
type Photo struct {
	ID              string   `json:"id"`
	File            string   `json:"file"`
	SHA256          string   `json:"sha256"`
	Subject         string   `json:"subject"`
	Entries         []Entry  `json:"entries"`
	MustHit         []string `json:"mustHit"`
	NoiseSubstrings []string `json:"noiseSubstrings"`
	Rules           Rules    `json:"rules"`
}

// Load reads and validates a photo manifest ({"version":1,"photos":[...]}).
// Validation fails the whole file on the first bad photo — a typo must
// never silently drop a photo from the live suite. Decoding is strict:
// unknown fields are rejected (a "minWordHit" typo must fail loudly, not
// silently lower the bar), and the threshold keys must be present as
// numbers even when their value is 0 (PR #70 复审 P2).
func Load(path string) ([]Photo, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("e2ephoto: read manifest: %w", err)
	}
	var m struct {
		Photos  []Photo `json:"photos"`
		Version int     `json:"version"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("e2ephoto: parse manifest %s: %w", path, err)
	}
	if err := requireRulesKeys(raw); err != nil {
		return nil, fmt.Errorf("e2ephoto: manifest %s: %w", path, err)
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("e2ephoto: manifest %s version = %d, want 1", path, m.Version)
	}
	if len(m.Photos) == 0 {
		return nil, fmt.Errorf("e2ephoto: manifest %s has no photos", path)
	}
	ids := make(map[string]bool, len(m.Photos))
	files := make(map[string]bool, len(m.Photos))
	for i, p := range m.Photos {
		if err := p.validate(); err != nil {
			return nil, fmt.Errorf("e2ephoto: manifest %s photos[%d]: %w", path, i, err)
		}
		if ids[p.ID] {
			return nil, fmt.Errorf("e2ephoto: manifest %s: duplicate id %q", path, p.ID)
		}
		if files[p.File] {
			return nil, fmt.Errorf("e2ephoto: manifest %s: duplicate file %q", path, p.File)
		}
		ids[p.ID], files[p.File] = true, true
	}
	return m.Photos, nil
}

// requireRulesKeys rejects a photo whose rules object is missing any of
// the three threshold keys or writes a non-number in one of them. A
// missing key would decode to the zero value and silently lower that bar
// to 0 (a wholly omitted minWordHits keeps loading today), and so does a
// null one: Go's decoder accepts null into numeric fields and leaves the
// zero value, while an explicit "minWordHits":0 stays legal — 0 is a
// meaningful threshold when declared (PR #70 复审 P2).
func requireRulesKeys(raw []byte) error {
	var probe struct {
		Photos []struct {
			Rules map[string]json.RawMessage `json:"rules"`
		} `json:"photos"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	for i, p := range probe.Photos {
		for _, key := range []string{"minWordHits", "minPhraseHits", "minPrecision"} {
			v, ok := p.Rules[key]
			if !ok {
				return fmt.Errorf("photos[%d]: rules is missing %q", i, key)
			}
			// A JSON value opening with '-' or a digit is a number; anything
			// else — null, "1", true, {} — is not a numeric threshold.
			t := strings.TrimSpace(string(v))
			if t == "" || t[0] != '-' && (t[0] < '0' || t[0] > '9') {
				return fmt.Errorf("photos[%d]: rules.%s = %s: threshold must be a number", i, key, t)
			}
		}
	}
	return nil
}

// validate checks one photo against the enums and its own rules.
func (p Photo) validate() error {
	byWord := make(map[string]bool, len(p.Entries))
	bySurface := make(map[string]string, 2*len(p.Entries))
	words, phrases := 0, 0
	for _, e := range p.Entries {
		switch e.Kind {
		case KindWord:
			words++
		case KindPhrase:
			phrases++
		default:
			return fmt.Errorf("photo %s: entry %q: unknown kind %q", p.ID, e.Word, e.Kind)
		}
		if e.Word == "" {
			return fmt.Errorf("photo %s: entry with empty word", p.ID)
		}
		// Reject meanings that are empty AFTER normalization, not just the
		// literal "": “ ”或“。” folds to "", which would compare equal to a
		// recognition row with no meaning at all (PR #70 复审 P2).
		if NormalizeMeaning(e.Meaning) == "" {
			return fmt.Errorf("photo %s: entry %q has no picture-verified meaning", p.ID, e.Word)
		}
		// Same for the declared equivalents: an alias folding to "" lets an
		// empty recognized meaning pass as aliased.
		for _, a := range e.MeaningAliases {
			if NormalizeMeaning(a) == "" {
				return fmt.Errorf("photo %s: entry %q: meaning alias %q normalizes to an empty meaning", p.ID, e.Word, a)
			}
		}
		byWord[e.Word] = true
		// bySurface mirrors Compare's hit surface: entries are keyed by
		// NormalizeWord (word and aliases alike), so two declarations that
		// normalize alike would silently overwrite each other's hits —
		// reject them before one recognition row can be miscounted.
		for _, decl := range append([]string{e.Word}, e.Aliases...) {
			surface := NormalizeWord(decl)
			if surface == "" {
				return fmt.Errorf("photo %s: %q normalizes to an empty surface", p.ID, decl)
			}
			if prev, dup := bySurface[surface]; dup {
				return fmt.Errorf("photo %s: %q collides with %q after normalization (both %q)",
					p.ID, decl, prev, surface)
			}
			bySurface[surface] = e.Word
		}
	}
	switch {
	case p.ID == "":
		return fmt.Errorf("empty id")
	case p.File == "" || filepath.IsAbs(p.File) || strings.Contains(p.File, ".."):
		return fmt.Errorf("photo %s: file %q must be a plain relative name", p.ID, p.File)
	case len(p.SHA256) != 64 || !isHex(p.SHA256):
		return fmt.Errorf("photo %s: sha256 %q is not a 64-hex digest", p.ID, p.SHA256)
	case p.Subject != SubjectEnglish && p.Subject != SubjectChinese:
		return fmt.Errorf("photo %s: unknown subject %q", p.ID, p.Subject)
	case len(p.Entries) == 0:
		return fmt.Errorf("photo %s: no annotated entries", p.ID)
	case len(p.MustHit) == 0:
		return fmt.Errorf("photo %s: mustHit is empty", p.ID)
	case len(p.NoiseSubstrings) == 0:
		return fmt.Errorf("photo %s: noiseSubstrings is empty", p.ID)
	case p.Rules.MinWordHits < 0 || p.Rules.MinWordHits > words:
		return fmt.Errorf("photo %s: minWordHits %d outside 0..%d", p.ID, p.Rules.MinWordHits, words)
	case p.Rules.MinPhraseHits < 0 || p.Rules.MinPhraseHits > phrases:
		return fmt.Errorf("photo %s: minPhraseHits %d outside 0..%d", p.ID, p.Rules.MinPhraseHits, phrases)
	case p.Rules.MinPrecision <= 0 || p.Rules.MinPrecision > 1:
		return fmt.Errorf("photo %s: minPrecision %v outside (0,1]", p.ID, p.Rules.MinPrecision)
	}
	for _, m := range p.MustHit {
		if !byWord[m] {
			return fmt.Errorf("photo %s: mustHit %q is not an annotated word", p.ID, m)
		}
	}
	for _, s := range p.NoiseSubstrings {
		if s == "" {
			return fmt.Errorf("photo %s: empty noise substring", p.ID)
		}
	}
	return nil
}

// isHex reports whether s consists solely of hex digits, so a pinned
// digest must be a real 64-hex SHA-256 — not any 64-character string.
func isHex(s string) bool {
	for _, c := range s {
		switch {
		case '0' <= c && c <= '9', 'a' <= c && c <= 'f', 'A' <= c && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// Verify resolves File under dir and checks the material hash. A missing
// directory or file surfaces an error wrapping fs.ErrNotExist (the live
// run turns that into a skip); a present file whose hash does not match
// the pinned digest is a hard failure and must never be downgraded to a
// skip.
func (p Photo) Verify(dir string) error {
	// Guard here too, not only in validate: Verify may be called on a
	// Photo built in code rather than loaded through Load.
	if p.File == "" || filepath.IsAbs(p.File) || strings.Contains(p.File, "..") {
		return fmt.Errorf("e2ephoto: photo %s: file %q must be a plain relative name", p.ID, p.File)
	}
	raw, err := os.ReadFile(filepath.Join(dir, p.File)) // #nosec G703 -- File is traversal-guarded just above and validated at Load
	if err != nil {
		return fmt.Errorf("e2ephoto: photo %s: read material: %w", p.ID, err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, p.SHA256) {
		return fmt.Errorf("e2ephoto: photo %s: material %s hashes to %s, manifest pins %s",
			p.ID, p.File, got, p.SHA256)
	}
	return nil
}
