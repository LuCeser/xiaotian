package infra

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// staticAssetRoot resolves pkg/web/static relative to this package's
// directory (tests run with CWD = package dir). The accent-token tests read
// the REAL shipped assets — the same tree StaticFileServer mounts — so the
// family-accent contract is pinned against what students' iPads download,
// not a fixture copy that could drift.
const staticAssetRoot = "../../pkg/web/static"

// accentDeclMatches extracts the --accent custom-property declaration from a
// stylesheet. One capture group: the 6-digit hex value.
var accentDeclMatches = regexp.MustCompile(`(?m)--accent:\s*(#[0-9a-fA-F]{6})\s*;`)

// TestStaticAssetsRetireTutorBlue pins Issue #16 acceptance criterion 1: the
// phase-13 tutor blue #2f6fed must not survive anywhere under pkg/web/static
// — not in CSS, not in inline HTML, not in JS. A competing accent hex is a
// regression the moment it reappears in any asset, so the scan walks the
// whole tree rather than tutor.css alone.
func TestStaticAssetsRetireTutorBlue(t *testing.T) {
	// Root-scoped read: gosec G122 flags filepath.WalkDir + os.ReadFile
	// because a symlinked entry could redirect the read outside the static
	// tree (same rationale as SessionCache.CleanupStale in pkg/tts).
	root, err := os.OpenRoot(staticAssetRoot)
	if err != nil {
		t.Fatalf("open static root %s: %v", staticAssetRoot, err)
	}
	defer root.Close()

	var offenders []string
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, readErr := root.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(strings.ToLower(string(body)), "#2f6fed") {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", staticAssetRoot, err)
	}
	if len(offenders) > 0 {
		t.Errorf("retired tutor blue #2f6fed found in: %s\n(want zero occurrences under pkg/web/static, Issue #16)",
			strings.Join(offenders, ", "))
	}
}

// TestFamilyAccentSingleValueAcrossStylesheets pins Issue #16 acceptance
// criterion 2: home.css, parent.css and tutor.css each declare --accent in
// their :root with the SAME value, so the three pages' accent relationship
// is confirmable at a glance (same token name, same hex) — and now also by
// machine. The pages share the value BY VALUE (no @import — the 13.3 cut
// list keeps every stylesheet standalone), which is exactly why this test
// must hold all three declarations to one another: nothing else keeps them
// in sync.
func TestFamilyAccentSingleValueAcrossStylesheets(t *testing.T) {
	sheets := []string{
		"styles/home.css",
		"styles/parent.css",
		"tutor/styles/tutor.css",
	}
	var familyAccent string
	for _, sheet := range sheets {
		body, err := os.ReadFile(filepath.Join(staticAssetRoot, sheet))
		if err != nil {
			t.Fatalf("read %s: %v", sheet, err)
		}
		m := accentDeclMatches.FindStringSubmatch(string(body))
		if m == nil {
			t.Errorf("%s declares no \"--accent: #hhhhhh;\" custom property", sheet)
			continue
		}
		value := strings.ToLower(m[1])
		if familyAccent == "" {
			familyAccent = value
			continue
		}
		if value != familyAccent {
			t.Errorf("%s --accent = %s, want %s (single family accent, Issue #16)",
				sheet, value, familyAccent)
		}
	}
}

// TestTutorAccentConsumedViaToken guards the token discipline inside
// tutor.css: the accent hex may appear ONLY in the :root --accent
// declaration. Every accent surface (state chips, focus rings, current-word
// chip, progress gradient end, check submit, photo controls, 回首页) must
// consume var(--accent), so a future family-accent change stays a one-line
// edit — a second hardcoded #2563eb would silently fork the family again,
// the exact defect Issue #16 exists to close.
func TestTutorAccentConsumedViaToken(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(staticAssetRoot, "tutor", "styles", "tutor.css"))
	if err != nil {
		t.Fatalf("read tutor.css: %v", err)
	}
	css := string(body)
	if got := strings.Count(strings.ToLower(css), "#2563eb"); got != 1 {
		t.Errorf("tutor.css contains %d occurrences of the family accent hex #2563eb, want exactly 1 (the :root --accent declaration); accent surfaces must use var(--accent)",
			got)
	}
	if got := strings.Count(css, "var(--accent)"); got < 6 {
		t.Errorf("tutor.css consumes var(--accent) %d times, want >= 6 (the Issue #16 accent surfaces)", got)
	}
}
