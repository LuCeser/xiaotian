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
// by a dictionary sense); empty means not yet annotated, in which case
// the comparator leaves that meaning unchecked. Aliases are the
// pre-declared accepted surfaces (the ellipsis template phrases);
// MeaningAliases are pre-declared equivalent expressions.
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
// never silently drop a photo from the live suite.
func Load(path string) ([]Photo, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("e2ephoto: read manifest: %w", err)
	}
	var m struct {
		Photos  []Photo `json:"photos"`
		Version int     `json:"version"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("e2ephoto: parse manifest %s: %w", path, err)
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

// validate checks one photo against the enums and its own rules.
func (p Photo) validate() error {
	byWord := make(map[string]bool, len(p.Entries))
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
		if byWord[e.Word] {
			return fmt.Errorf("photo %s: duplicate word %q", p.ID, e.Word)
		}
		byWord[e.Word] = true
	}
	switch {
	case p.ID == "":
		return fmt.Errorf("empty id")
	case p.File == "" || filepath.IsAbs(p.File) || strings.Contains(p.File, ".."):
		return fmt.Errorf("photo %s: file %q must be a plain relative name", p.ID, p.File)
	case len(p.SHA256) != 64:
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
