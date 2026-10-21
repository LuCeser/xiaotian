package e2ecorpus

import (
	"os"
	"path/filepath"
	"testing"
)

// realManifest points at the repo's milestone-1 corpus. Go tests run with
// cwd = package dir, so the repo root is ../..
const realManifest = "../../tests/e2e-m1/corpus/manifest.json"

// TestLoadRealManifest validates the actual corpus file: every group of the
// first batch is represented, so a group accidentally dropped from the
// manifest cannot silently shrink the suite.
func TestLoadRealManifest(t *testing.T) {
	entries, err := Load(realManifest)
	if err != nil {
		t.Fatalf("Load(real manifest): %v", err)
	}
	perGroup := map[string]int{}
	for _, e := range entries {
		perGroup[e.Group]++
	}
	for _, group := range []string{
		GroupConfirm, GroupDeny, GroupRepeat, GroupSubject,
		GroupSubjectAmbiguous, GroupAnswer, GroupVague, GroupState,
	} {
		if perGroup[group] < 2 {
			t.Errorf("group %s has %d entries in the real manifest, want ≥2 (all %v)",
				group, perGroup[group], perGroup)
		}
	}
}

// TestLoadRealManifest_SceneDecidesExpectation pins the "同一句话用于多个
// 课堂状态，状态决定预期动作" rule on real data: “嗯” must appear at least
// twice with different expects, and “好了” must appear in dictation (advance)
// and non-dictation (no-plain-advance) scenes.
func TestLoadRealManifest_SceneDecidesExpectation(t *testing.T) {
	entries, err := Load(realManifest)
	if err != nil {
		t.Fatalf("Load(real manifest): %v", err)
	}
	expectByText := map[string]map[string]bool{}
	for _, e := range entries {
		if expectByText[e.Text] == nil {
			expectByText[e.Text] = map[string]bool{}
		}
		expectByText[e.Text][e.Expect] = true
	}
	for _, text := range []string{"嗯", "好了"} {
		if len(expectByText[text]) < 2 {
			t.Errorf("text %q maps to %d distinct expects, want ≥2 (scene must decide the expectation)",
				text, len(expectByText[text]))
		}
	}
}

func TestLoadValidation(t *testing.T) {
	valid := `{"version":1,"entries":[{"id":"a","group":"confirm","text":"好了","scene":"dictation","expect":"advance"}]}`
	cases := []struct {
		name string
		json string
	}{
		{"duplicate id", `{"version":1,"entries":[` +
			`{"id":"a","group":"confirm","text":"好了","scene":"dictation","expect":"advance"},` +
			`{"id":"a","group":"deny","text":"等一下","scene":"dictation","expect":"hold"}]}`},
		{"unknown group", `{"version":1,"entries":[{"id":"a","group":"misc","text":"好了","scene":"dictation","expect":"advance"}]}`},
		{"unknown expect", `{"version":1,"entries":[{"id":"a","group":"confirm","text":"好了","scene":"dictation","expect":"maybe"}]}`},
		{"unknown scene", `{"version":1,"entries":[{"id":"a","group":"confirm","text":"好了","scene":"recess","expect":"advance"}]}`},
		{"empty text", `{"version":1,"entries":[{"id":"a","group":"confirm","text":"","scene":"dictation","expect":"advance"}]}`},
		{"empty entries", `{"version":1,"entries":[]}`},
		{"wrong version", `{"version":2,"entries":[{"id":"a","group":"confirm","text":"好了","scene":"dictation","expect":"advance"}]}`},
		{"select without subject", `{"version":1,"entries":[{"id":"a","group":"subject","text":"语文","scene":"subject-select","expect":"select-subject"}]}`},
		{"select with bad subject", `{"version":1,"entries":[{"id":"a","group":"subject","text":"语文","scene":"subject-select","expect":"select-subject","subject":"math"}]}`},
		{"subject without select", `{"version":1,"entries":[{"id":"a","group":"confirm","text":"好了","scene":"dictation","expect":"advance","subject":"chinese"}]}`},
		{"broken json", `{"version":1,"entries":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, []byte(tc.json), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if entries, err := Load(path); err == nil {
				t.Fatalf("Load() = %v entries, want an error", len(entries))
			}
		})
	}

	t.Run("valid manifest loads", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "manifest.json")
		if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		entries, err := Load(path)
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if len(entries) != 1 || entries[0].ID != "a" {
			t.Fatalf("Load() = %+v, want the single entry a", entries)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
			t.Fatal("Load(missing file) succeeded, want error")
		}
	})
}
