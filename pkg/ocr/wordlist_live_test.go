package ocr

// wordlist_live_test.go — Issue #3's recognition-quality gate: the browser
// e2e proves the FLOW against a stubbed vision reply, which by design says
// nothing about whether the real model actually reads a word-list photo.
// This test sends the REAL rendered fixture
// (tests/e2e/fixtures/photo-wordlist.png — printed textbook style, word +
// meaning table, plus body-text and exercise noise) through the REAL mimo
// vision service with the production prompt, and asserts the extraction
// contract: the clear word-list entries come back with their photo meanings
// (phrases intact), and the noise does not become entries.
//
// Gated on MIMO_API_KEY (repo .env is read the same way cmd/service's live
// tests do); without a key the test skips so offline runs stay green. Each
// run costs ONE vision call. Run:
//
//	go test ./pkg/ocr/ -run TestWordlistLive -v
//
// Boundary (the student-photo live test's same 口径): the fixture is a
// cleanly rendered printed-style page — no camera skew, shadows or paper
// texture. Real-camera word-list photos remain on-device UAT, exactly as
// real 6-12 handwriting accuracy does for the student-answer pipeline.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// liveWordlistKey loads MIMO_API_KEY from the env or the repo-root .env
// (cmd/service liveAPIKey twin — pkg/ocr cannot import package main).
func liveWordlistKey(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("live test requires the real vision service; skipped in -short mode")
	}
	if f, err := os.Open(filepath.Join("..", "..", ".env")); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			value = strings.Trim(value, `"'`)
			if key == "MIMO_API_KEY" && os.Getenv(key) == "" {
				os.Setenv(key, value)
			}
		}
	}
	apiKey := os.Getenv("MIMO_API_KEY")
	if apiKey == "" {
		t.Skip("MIMO_API_KEY not set — live wordlist test skipped")
	}
	return apiKey
}

// TestWordlistLive_RealVision is the "不把模拟通过当成识别质量通过" check:
// the same fixture the stubbed browser e2e uses, against the real service.
func TestWordlistLive_RealVision(t *testing.T) {
	apiKey := liveWordlistKey(t)

	fixture := filepath.Join("..", "..", "tests", "e2e", "fixtures", "photo-wordlist.png")
	image, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture %s: %v (regenerate via scripts/gen-wordlist-fixture.py)", fixture, err)
	}

	client, err := mimo.NewClient(mimo.ClientConfig{
		APIKey:  apiKey,
		BaseURL: infra.DefaultConfig().Voice.BaseURL,
		Timeout: infra.DefaultConfig().Photo.RequestTimeout,
	}, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("mimo client: %v", err)
	}

	// The production adapter shape (cmd/service mimoListReader): ChatVision
	// over the sniffed MIME type with the photo chat model.
	read := ListReader(func(ctx context.Context, systemPrompt, userPrompt string, img []byte, mimeType string) (string, error) {
		resp, err := client.ChatVision(ctx, systemPrompt, userPrompt, img, mimeType,
			mimo.WithChatModel(infra.DefaultConfig().Photo.ChatModel))
		if err != nil {
			return "", err
		}
		return resp.Text(), nil
	})
	pipeline := NewWordlistPipeline(read, infra.DefaultConfig().Photo.MaxImageBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := pipeline.Recognize(ctx, image)
	if err != nil {
		t.Fatalf("live recognize: %v", err)
	}

	// The fixture's five clear entries (word → expected meaning). The
	// assertion is a SUBSET check with ≥4 hits: a live model may drop one
	// borderline entry, but the bulk of the list must come through with the
	// photo's own meanings.
	want := map[string]string{
		"apple":         "苹果",
		"banana":        "香蕉",
		"ice cream":     "冰激凌",
		"go for a walk": "散步",
		"river":         "河流",
	}
	got := map[string]string{}
	for _, e := range res.Entries {
		got[strings.ToLower(e.Word)] = e.Meaning
	}
	hits := 0
	for word, meaning := range want {
		gotMeaning, ok := got[word]
		if !ok {
			continue
		}
		if strings.Contains(gotMeaning, meaning) {
			hits++
		} else {
			t.Errorf("live entry %q meaning = %q, want it to contain %q", word, gotMeaning, meaning)
		}
	}
	if hits < 4 {
		t.Fatalf("live recognition found only %d/5 expected entries (got %v)", hits, formatEntries(res.Entries))
	}

	// Noise must not become entries (Issue #3 acceptance: 普通正文词和练习题
	// 不自动作为词条). Phrase integrity is pinned by the ice-cream check
	// above ("ice cream" as ONE entry with spaces).
	for _, e := range res.Entries {
		lower := strings.ToLower(e.Word)
		for _, noise := range []string{"read", "exercise", "fill", "copy", "unit", "word list"} {
			if strings.Contains(lower, noise) {
				t.Errorf("noise became an entry: %q (all entries: %v)", e.Word, formatEntries(res.Entries))
			}
		}
	}
	t.Logf("live wordlist recognition: meta=%+v, %d entries, %d/5 expected hits", res.Textbook, len(res.Entries), hits)
}

func formatEntries(entries []WordlistEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, fmt.Sprintf("%s=%s", e.Word, e.Meaning))
	}
	return strings.Join(parts, ", ")
}
