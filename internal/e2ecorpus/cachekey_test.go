package e2ecorpus

import (
	"os"
	"strings"
	"testing"
)

func sampleEntry() Entry {
	return Entry{ID: "confirm-01", Group: GroupConfirm, Text: "好了", Scene: SceneDictation, Expect: ExpectAdvance}
}

// TestKeyStable: the same entry and params must always derive the same key —
// a wavering key would make every run a cache miss and re-synthesize.
func TestKeyStable(t *testing.T) {
	first := Key(sampleEntry(), FirstBatchParams())
	for i := 0; i < 3; i++ {
		if got := Key(sampleEntry(), FirstBatchParams()); got != first {
			t.Fatalf("Key() = %q on call %d, want stable %q", got, i, first)
		}
	}
}

// TestKeyChangesWithParams: any parameter that can change the audio bytes
// must change the key (计划第 4 节缓存键清单).
func TestKeyChangesWithParams(t *testing.T) {
	base := FirstBatchParams()
	baseKey := Key(sampleEntry(), base)
	mutations := map[string]func(p *SynthParams){
		"model":       func(p *SynthParams) { p.Model = "mimo-v3-tts" },
		"voice":       func(p *SynthParams) { p.Voice = "Mia" },
		"style":       func(p *SynthParams) { p.Style = "gentle" },
		"format":      func(p *SynthParams) { p.Format = "pcm16" },
		"sample rate": func(p *SynthParams) { p.SampleRate = 16000 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			p := base
			mutate(&p)
			if got := Key(sampleEntry(), p); got == baseKey {
				t.Errorf("changed %s produced the same key", name)
			}
		})
	}
	t.Run("text", func(t *testing.T) {
		e := sampleEntry()
		e.Text = "好嘞"
		if got := Key(e, base); got == baseKey {
			t.Error("different text produced the same key")
		}
	})
}

// TestKeyIncludesConvertVersion: bumping the conversion algorithm version
// must invalidate old keys, so the key material carries it verbatim.
func TestKeyIncludesConvertVersion(t *testing.T) {
	if !strings.Contains(keyMaterial(sampleEntry(), FirstBatchParams()), ConvertVersion) {
		t.Fatalf("keyMaterial does not contain ConvertVersion %q", ConvertVersion)
	}
}

// TestKeySharedAcrossIDs: entries with identical text share one key on
// purpose — one synthesis serves every scene using that sentence.
func TestKeySharedAcrossIDs(t *testing.T) {
	other := sampleEntry()
	other.ID, other.Group, other.Scene, other.Expect = "vague-01", GroupVague, SceneDictation, ExpectClarify
	if Key(sampleEntry(), FirstBatchParams()) != Key(other, FirstBatchParams()) {
		t.Error("same text under different IDs produced different keys — scenes would re-synthesize shared audio")
	}
}

// TestPathsDistinct: source, converted input and metadata must not collide.
func TestPathsDistinct(t *testing.T) {
	src, in, meta := SourcePath("dir", "abc"), InputPath("dir", "abc"), MetaPath("dir", "abc")
	if src == in || src == meta || in == meta {
		t.Fatalf("cache paths collide: %q %q %q", src, in, meta)
	}
	for _, p := range []string{src, in, meta} {
		if !strings.HasPrefix(p, "dir"+string(os.PathSeparator)) {
			t.Errorf("path %q does not lay out under the cache dir", p)
		}
	}
}

// TestHit: presence of all three files — and nothing less — counts as a hit.
func TestHit(t *testing.T) {
	dir := t.TempDir()
	const key = "deadbeef"
	if Hit(dir, key) {
		t.Fatal("Hit on empty dir = true, want false")
	}
	if err := os.WriteFile(SourcePath(dir, key), []byte("wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Hit(dir, key) {
		t.Fatal("Hit with only the source file = true, want false")
	}
	if err := os.WriteFile(InputPath(dir, key), []byte("wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Hit(dir, key) {
		t.Fatal("Hit without metadata = true, want false")
	}
	if err := os.WriteFile(MetaPath(dir, key), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Hit(dir, key) {
		t.Fatal("Hit with all three files = false, want true")
	}
}
