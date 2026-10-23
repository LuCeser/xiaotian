package e2ecorpus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSynth stands in for the real MIMO TTS: it counts calls, returns a
// deterministic valid WAV per text, and can fail selected texts. No real
// provider is ever contacted from these tests (计划步骤 22：迭代时只用
// fake provider 验证，不真实合成).
type fakeSynth struct {
	errOn map[string]error
	calls []string
}

func (f *fakeSynth) SynthesizeWAV(_ context.Context, text string, _ SynthParams) ([]byte, error) {
	f.calls = append(f.calls, text)
	if err, ok := f.errOn[text]; ok {
		return nil, err
	}
	n := 24000 / 10 // 100ms of audio
	pcm := make([]byte, n*2)
	for i := 0; i < n; i++ {
		putSample(pcm, i, int16(i%100))
	}
	return BuildWAV(pcm, FirstBatchParams().SampleRate), nil
}

// writeManifest writes a small manifest with the given (id, text) pairs and
// returns its path.
func writeManifest(t *testing.T, idText ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"version":1,"entries":[`)
	for i := 0; i+1 < len(idText); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%q,"group":"confirm","text":%q,"scene":"dictation","expect":"advance"}`, idText[i], idText[i+1])
	}
	b.WriteString(`]}`)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runPrepare(t *testing.T, manifest string, synth *fakeSynth, limit, maxChars int) (PrepareReport, error) {
	t.Helper()
	entries, err := LoadManifest(manifest)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	return Prepare(context.Background(), entries, FirstBatchParams(), synth, PrepareOptions{
		Dir:      t.TempDir(),
		Limit:    limit,
		MaxChars: maxChars,
		Now:      func() time.Time { return time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC) },
	})
}

// TestPrepareOnlyMissing: everything missing is synthesized once; the second
// run over the same dir synthesizes nothing (命中不生成).
func TestPrepareOnlyMissing(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, "a", "好了", "b", "等一下", "c", "下一个")
	entries, err := LoadManifest(manifest)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	opts := PrepareOptions{Dir: dir, Now: func() time.Time { return time.Now() }}
	synth := &fakeSynth{}

	report, err := Prepare(context.Background(), entries, FirstBatchParams(), synth, opts)
	if err != nil {
		t.Fatalf("first Prepare: %v", err)
	}
	if len(report.Synthesized) != 3 || report.Cached != 0 || len(report.BudgetSkipped) != 0 {
		t.Fatalf("first report = %+v, want 3 synthesized", report)
	}
	if len(synth.calls) != 3 {
		t.Fatalf("fake synthesizer called %d times, want 3", len(synth.calls))
	}

	second, err := Prepare(context.Background(), entries, FirstBatchParams(), synth, opts)
	if err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	if len(second.Synthesized) != 0 || second.Cached != 3 {
		t.Fatalf("second report = %+v, want 0 synthesized / 3 cached", second)
	}
	if len(synth.calls) != 3 {
		t.Fatalf("second run synthesized again: %d total calls, want still 3 (命中不生成)", len(synth.calls))
	}
}

// TestPrepareSharedTextOneCall: two entries with identical text must cost
// one synthesis — same key, one cache entry.
func TestPrepareSharedTextOneCall(t *testing.T) {
	manifest := writeManifest(t, "a", "嗯", "b", "嗯")
	synth := &fakeSynth{}
	report, err := runPrepareWith(manifest, synth)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(synth.calls) != 1 {
		t.Fatalf("synthesized %d times for shared text, want 1", len(synth.calls))
	}
	if report.Cached != 0 || len(report.Synthesized) != 1 {
		t.Fatalf("report = %+v, want 1 synthesized", report)
	}
}

func runPrepareWith(manifest string, synth *fakeSynth) (PrepareReport, error) {
	entries, err := LoadManifest(manifest)
	if err != nil {
		return PrepareReport{}, err
	}
	return Prepare(context.Background(), entries, FirstBatchParams(), synth, PrepareOptions{
		Dir:      filepath.Dir(manifest),
		MaxChars: 1000,
	})
}

// TestPrepareBudgetCaps: the item cap and the character cap each stop the
// run and record what they left behind.
func TestPrepareBudgetCaps(t *testing.T) {
	t.Run("item limit", func(t *testing.T) {
		manifest := writeManifest(t, "a", "好了", "b", "好嘞", "c", "下一个")
		report, err := runPrepare(t, manifest, &fakeSynth{}, 2, 0)
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if len(report.Synthesized) != 2 || len(report.BudgetSkipped) != 1 || report.BudgetSkipped[0] != "c" {
			t.Fatalf("report = %+v, want a/b synthesized and c skipped", report)
		}
	})

	t.Run("char limit", func(t *testing.T) {
		// Each text is 3 runes; a 7-char cap fits two items (6) but not three.
		manifest := writeManifest(t, "a", "好了呀", "b", "好嘞呀", "c", "下一个")
		report, err := runPrepare(t, manifest, &fakeSynth{}, 0, 7)
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if len(report.Synthesized) != 2 || report.Chars != 6 || len(report.BudgetSkipped) != 1 {
			t.Fatalf("report = %+v, want 2 synthesized (6 chars) and 1 skipped", report)
		}
	})
}

// TestPrepareStopsAtFirstFailure: a provider error halts the run with the
// already-saved items intact — no further quota is spent after a hard
// failure (计划第 6 节).
func TestPrepareStopsAtFirstFailure(t *testing.T) {
	manifest := writeManifest(t, "a", "好了", "b", "等一下", "c", "下一个")
	synth := &fakeSynth{errOn: map[string]error{"等一下": errors.New("provider 429")}}
	report, err := runPrepare(t, manifest, synth, 0, 0)
	if err == nil {
		t.Fatal("Prepare hid the provider error, want it surfaced")
	}
	if !strings.Contains(err.Error(), "provider 429") || !strings.Contains(err.Error(), "b") {
		t.Errorf("error %q does not identify the failing entry/cause", err)
	}
	if len(report.Synthesized) != 1 || len(synth.calls) != 2 {
		t.Fatalf("report = %+v with %d calls, want 1 saved then stop", report, len(synth.calls))
	}
}

// TestPrepareRejectsProviderGarbage: a fake provider returning non-audio
// bytes must fail the run via Save's validation, leaving no cache files
// (the permanent .prepare.lock infrastructure file does not count).
func TestPrepareRejectsProviderGarbage(t *testing.T) {
	dir := t.TempDir()
	entries := []Entry{{ID: "a", Group: GroupConfirm, Text: "好了", Scene: SceneDictation, Expect: ExpectAdvance}}
	synth := garbageSynth{}
	_, err := Prepare(context.Background(), entries, FirstBatchParams(), synth, PrepareOptions{Dir: dir})
	if err == nil {
		t.Fatal("Prepare accepted non-audio provider output")
	}
	left, _ := os.ReadDir(dir)
	for _, f := range left {
		if f.Name() == cacheLockName {
			continue
		}
		t.Fatalf("rejected synthesis left %s behind", f.Name())
	}
}

type garbageSynth struct{}

func (garbageSynth) SynthesizeWAV(_ context.Context, _ string, _ SynthParams) ([]byte, error) {
	return []byte("not audio at all"), nil
}

// prepareAgainst runs Prepare against a caller-owned cache dir, so tests can
// pre-seed or tamper with entries before the run.
func prepareAgainst(t *testing.T, dir string, synth Synthesizer) (PrepareReport, error) {
	t.Helper()
	entries, err := LoadManifest(writeManifest(t, "a", "好了"))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	return Prepare(context.Background(), entries, FirstBatchParams(), synth, PrepareOptions{Dir: dir})
}

// saveOneInto saves a single "好了" entry into dir and returns its key.
func saveOneInto(t *testing.T, dir string) string {
	t.Helper()
	entry := Entry{ID: "a", Group: GroupConfirm, Text: "好了", Scene: SceneDictation, Expect: ExpectAdvance}
	key, err := Save(dir, entry, FirstBatchParams(), fakeSourceWAV(0.1), time.Now())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return key
}

// TestPrepareFailsOnDamagedCache: an existing entry that no longer verifies
// must fail the run loudly — never reported as a cached hit (假绿), never
// silently re-synthesized over the evidence. Refreshing an entry is an
// explicit act, not something prepare-missing does (计划第 4 节：默认只读
// 缓存；第 6 节：硬失败保留证据).
func TestPrepareFailsOnDamagedCache(t *testing.T) {
	t.Run("damaged content is not a hit", func(t *testing.T) {
		dir := t.TempDir()
		key := saveOneInto(t, dir)
		path := InputPath(dir, key)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Flip a sample — all three files still exist, so presence checks alone stay true.
		raw[len(raw)-10] ^= 0xFF
		err = os.WriteFile(path, raw, 0o600) //nolint:gosec // G703: path is built from t.TempDir() + a Save-returned key
		if err != nil {
			t.Fatal(err)
		}

		synth := &fakeSynth{}
		report, err := prepareAgainst(t, dir, synth)
		if err == nil {
			t.Fatalf("Prepare counted a damaged entry as cached: %+v", report)
		}
		if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "好了") {
			t.Errorf("error %q does not identify the damaged entry", err)
		}
		if !strings.Contains(err.Error(), "damaged") {
			t.Errorf("error %q does not say the entry is damaged", err)
		}
		if len(synth.calls) != 0 {
			t.Errorf("damaged entry re-synthesized %d time(s), want 0 — refresh must be explicit", len(synth.calls))
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("damaged input file was deleted — evidence must be kept for inspection")
		}
	})

	t.Run("incomplete entry is not a fresh miss", func(t *testing.T) {
		dir := t.TempDir()
		key := saveOneInto(t, dir)
		if err := os.Remove(MetaPath(dir, key)); err != nil {
			t.Fatal(err)
		}

		synth := &fakeSynth{}
		report, err := prepareAgainst(t, dir, synth)
		if err == nil {
			t.Fatalf("Prepare treated a torn entry as a fresh miss and re-synthesized: %+v", report)
		}
		if !strings.Contains(err.Error(), "incomplete") {
			t.Errorf("error %q does not say the entry is incomplete", err)
		}
		if len(synth.calls) != 0 {
			t.Errorf("torn entry re-synthesized %d time(s), want 0", len(synth.calls))
		}
		if _, statErr := os.Stat(SourcePath(dir, key)); statErr != nil {
			t.Errorf("surviving files were deleted — evidence must be kept for inspection")
		}
	})
}

// slowCountingSynth is a race-safe fake synthesizer whose first call pauses
// briefly, widening the window in which a concurrent unserialized run can
// finish its own missing-entry decision and start synthesizing too.
type slowCountingSynth struct {
	mu    sync.Mutex
	calls int
}

func (s *slowCountingSynth) SynthesizeWAV(_ context.Context, _ string, _ SynthParams) ([]byte, error) {
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	s.mu.Unlock()
	if first {
		time.Sleep(100 * time.Millisecond)
	}
	pcm := make([]byte, 4800) // 100ms of 24kHz audio
	for i := 0; i < len(pcm)/2; i++ {
		putSample(pcm, i, int16(i%100))
	}
	return BuildWAV(pcm, FirstBatchParams().SampleRate), nil
}

func (s *slowCountingSynth) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestPrepareConcurrentRunsSynthesizeOnce: two prepare runs sharing one
// cache dir must serialize on the cache lock — the entry is synthesized
// exactly once, one run sees it cached, and the published files verify
// (PR #63 review：两个准备进程不得同时判定缺失后交错覆盖三个文件).
func TestPrepareConcurrentRunsSynthesizeOnce(t *testing.T) {
	dir := t.TempDir()
	entries := []Entry{{ID: "a", Group: GroupConfirm, Text: "好了", Scene: SceneDictation, Expect: ExpectAdvance}}
	synth := &slowCountingSynth{}

	// Both runs start together, so both face the same missing entry.
	var start, done sync.WaitGroup
	start.Add(2)
	done.Add(2)
	reports := make([]PrepareReport, 2)
	errs := make([]error, 2)
	for i := range reports {
		go func(i int) {
			defer done.Done()
			start.Done()
			start.Wait()
			reports[i], errs[i] = Prepare(context.Background(), entries, FirstBatchParams(), synth, PrepareOptions{Dir: dir})
		}(i)
	}
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent run %d: %v", i+1, err)
		}
	}
	if got := synth.count(); got != 1 {
		t.Fatalf("synthesized %d times across concurrent runs, want exactly 1", got)
	}
	// Exactly one run synthesized the entry; the other found it cached.
	if synthRuns := len(reports[0].Synthesized) + len(reports[1].Synthesized); synthRuns != 1 {
		t.Fatalf("reports %+v disagree about who synthesized the entry", reports)
	}
	if cachedRuns := reports[0].Cached + reports[1].Cached; cachedRuns != 1 {
		t.Fatalf("reports %+v disagree about who found the entry cached", reports)
	}
	key := Key(entries[0], FirstBatchParams())
	if _, err := Load(dir, key); err != nil {
		t.Fatalf("entry published by the winning run does not verify: %v", err)
	}
}
