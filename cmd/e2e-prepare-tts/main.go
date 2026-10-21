// Command e2e-prepare-tts is the EXPLICIT cache-preparation step for the
// milestone-1 live corpus (计划第 4/6 节). Tests only ever read the cache
// (cache-only); this command is the single sanctioned way to synthesize
// missing child-input audio via the real MIMO TTS, bounded by per-run item
// and character caps. It never refreshes existing entries and never runs
// implicitly: it requires the same explicit live switch as the live tests
// (internal/livetest) — without XIAOTIAN_LIVE_TESTS=1 and MIMO_API_KEY it
// prints why and exits 0 (a skip, not a failure).
//
// Usage:
//
//	XIAOTIAN_LIVE_TESTS=1 MIMO_API_KEY=… \
//	XIAOTIAN_E2E_AUDIO_DIR=~/xiaotian-e2e-audio \
//	go run ./cmd/e2e-prepare-tts -limit 12 -max-chars 200
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xiaotian/home-tutor-agent/internal/e2ecorpus"
	"github.com/xiaotian/home-tutor-agent/internal/livetest"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// defaultManifestPath is resolved from the working directory the command is
// expected to run from (repo root).
const defaultManifestPath = "tests/e2e-m1/corpus/manifest.json"

func main() {
	os.Exit(run())
}

func run() int {
	manifest := flag.String("manifest", defaultManifestPath, "corpus manifest path")
	dir := flag.String("dir", "", "audio cache dir (default $XIAOTIAN_E2E_AUDIO_DIR, else the user cache dir)")
	limit := flag.Int("limit", 12, "max items synthesized per run (≤0: uncapped)")
	maxChars := flag.Int("max-chars", 200, "max total synthesized characters per run (≤0: uncapped)")
	timeout := flag.Duration("timeout", 10*time.Minute, "overall run timeout")
	flag.Parse()

	cacheDir, err := resolveCacheDir(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-prepare-tts: %v\n", err)
		return 2
	}

	// Same explicit gate as the live tests: running this command is intent,
	// but the switch and credential must still be present (无 Key skip).
	if msg := skipReason(); msg != "" {
		fmt.Println(msg)
		return 0
	}

	entries, err := e2ecorpus.LoadManifest(*manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-prepare-tts: %v\n", err)
		return 2
	}

	client, err := mimo.NewClient(mimo.ClientConfig{
		APIKey:  os.Getenv(livetest.KeyEnv),
		BaseURL: infra.DefaultConfig().Voice.BaseURL,
		Timeout: 120 * time.Second,
	}, infra.NewNoOpLogger())
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-prepare-tts: mimo client: %v\n", err)
		return 2
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	report, err := e2ecorpus.Prepare(ctx, entries, e2ecorpus.FirstBatchParams(),
		mimoSynthesizer{client: client}, e2ecorpus.PrepareOptions{
			Dir:      cacheDir,
			Limit:    *limit,
			MaxChars: *maxChars,
		})
	printReport(cacheDir, report)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-prepare-tts: %v\n", err)
		return 1
	}
	return 0
}

// skipReason returns the reason this run must not touch the provider, or ""
// when it may run. Mirrors internal/livetest's rule table.
func skipReason() string {
	if !livetest.Enabled() {
		return "live calls disabled: set " + livetest.SwitchEnv + "=1 to allow real synthesis; nothing synthesized"
	}
	if os.Getenv(livetest.KeyEnv) == "" {
		return livetest.SwitchEnv + "=1 but " + livetest.KeyEnv + " is not set — prepare skipped (missing credentials)"
	}
	return ""
}

// resolveCacheDir picks the audio cache dir: -dir flag, then
// XIAOTIAN_E2E_AUDIO_DIR, else the user cache dir outside the repo
// (计划第 4 节：音频缓存默认放仓库外).
func resolveCacheDir(flagDir string) (string, error) {
	for _, candidate := range []string{flagDir, os.Getenv("XIAOTIAN_E2E_AUDIO_DIR")} {
		if candidate != "" {
			return candidate, nil
		}
	}
	userCache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no cache dir: pass -dir or set XIAOTIAN_E2E_AUDIO_DIR (%w)", err)
	}
	return filepath.Join(userCache, "xiaotian", "e2e-audio"), nil
}

// mimoSynthesizer adapts pkg/mimo's TTS to e2ecorpus.Synthesizer, mapping
// every SynthParams field onto the request so the audio matches its key.
type mimoSynthesizer struct {
	client mimo.Client
}

func (s mimoSynthesizer) SynthesizeWAV(ctx context.Context, text string, p e2ecorpus.SynthParams) ([]byte, error) {
	opts := []mimo.TTSOption{
		mimo.WithVoice(p.Voice),
		mimo.WithAudioFormat(p.Format),
		mimo.WithSampleRate(p.SampleRate),
	}
	if p.Style != "" {
		opts = append(opts, mimo.WithStyleInstruction(p.Style))
	}
	audio, err := s.client.Synthesize(ctx, text, opts...)
	if err != nil {
		return nil, err
	}
	return audio.Samples, nil
}

func printReport(dir string, report e2ecorpus.PrepareReport) {
	fmt.Printf("audio cache dir: %s\n", dir)
	fmt.Printf("cached (not re-synthesized): %d\n", report.Cached)
	fmt.Printf("synthesized: %d item(s), %d char(s)\n", len(report.Synthesized), report.Chars)
	for _, id := range report.Synthesized {
		fmt.Printf("  + %s\n", id)
	}
	if len(report.BudgetSkipped) > 0 {
		fmt.Printf("left for a later run by the item/char caps: %d\n", len(report.BudgetSkipped))
		for _, id := range report.BudgetSkipped {
			fmt.Printf("  ~ %s\n", id)
		}
	}
}
