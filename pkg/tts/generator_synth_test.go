package tts

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// fakeWordSynth implements WordSynthesizer for generator tests: per-word PCM
// or a configurable failure set, plus call capture. Simulates the Mimo word
// source (DICT-03) without network I/O.
type fakeWordSynth struct {
	mu        sync.Mutex
	pcmByWord map[string][]byte
	failWords map[string]bool
	storeErr  error // returned for words in failWords with storeErrFail marker
	calls     int
	lastWords []string
}

func (f *fakeWordSynth) SynthesizeWord(_ context.Context, word string) (PCMData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastWords = append(f.lastWords, word)
	if f.failWords[word] {
		return PCMData{}, errors.New("simulated mimo synthesis failure: " + word)
	}
	if f.storeErr != nil {
		return PCMData{}, f.storeErr
	}
	samples, ok := f.pcmByWord[word]
	if !ok {
		samples = []byte{byte(f.calls), 0x42} //nolint:gosec // G115: test PCM marker; call counts in these tests fit in a byte
	}
	return PCMData{Samples: samples, FrameSize: 640}, nil
}

func (f *fakeWordSynth) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newSynthTestGenerator builds a Generator with a pregenerate-counting fake
// executor and a temp-dir cache — the standard harness for WithSource tests.
func newSynthTestGenerator(t *testing.T) (*Generator, *fakeExecutor, *SessionCache) {
	t.Helper()
	exec := &fakeExecutor{}
	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}
	gen, err := NewGenerator(exec, cache, nil, infra.DefaultConfig().TTS)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}
	// Deterministic conversion: the MP3 "path" maps to stable PCM bytes so
	// the fallback-overwrite proof can tell volcengine bytes from mimo bytes.
	gen.convertFn = func(mp3Path string, opts ConvertOpts) (PCMData, error) {
		return PCMData{Samples: []byte{0xEE, 0xEE, 0xEE, 0xEE}, FrameSize: 640}, nil
	}
	return gen, exec, cache
}

// TestGenerateForSessionWithSource_MimoSuccess: with a working synthesizer,
// every word flows through it (no pregenerate spawn), the cache holds the
// synth bytes, and the source annotation is SourceMimo.
func TestGenerateForSessionWithSource_MimoSuccess(t *testing.T) {
	gen, exec, cache := newSynthTestGenerator(t)
	synth := &fakeWordSynth{pcmByWord: map[string][]byte{
		"apple":  {0xAA, 0xAA},
		"banana": {0xBB, 0xBB},
	}}
	gen.SetWordSynthesizer(synth)

	paths, source, err := gen.GenerateForSessionWithSource(context.Background(), "sess-mimo", []string{"apple", "banana"})
	if err != nil {
		t.Fatalf("GenerateForSessionWithSource failed: %v", err)
	}
	if source != SourceMimo {
		t.Errorf("source = %q, want %q", source, SourceMimo)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want 2 entries", paths)
	}
	for _, w := range []string{"apple", "banana"} {
		if _, ok := paths[w]; !ok {
			t.Errorf("paths missing word %q", w)
		}
	}
	// Cache holds the synthesizer's bytes, not converted MP3 bytes.
	loaded, err := cache.Load("sess-mimo", "apple")
	if err != nil || string(loaded) != string([]byte{0xAA, 0xAA}) {
		t.Errorf("Load(apple) = (%x, %v), want aaaa", loaded, err)
	}
	// The pregenerate path must be untouched on the happy path.
	if exec.calledPregenerate != 0 {
		t.Errorf("RunPregenerate calls = %d, want 0", exec.calledPregenerate)
	}
}

// TestGenerateForSessionWithSource_FallbackOnSynthError: the first synthesis
// failure abandons the mimo path, invokes the whole-batch pregenerate path,
// and annotates SourceVolcengine. Also proves the cache-overwrite contract:
// words the synth already stored get overwritten by the fallback batch
// (SessionCache.Store truncates via os.WriteFile), so no mimo/volcengine
// byte mixing survives a successful fallback.
func TestGenerateForSessionWithSource_FallbackOnSynthError(t *testing.T) {
	gen, exec, cache := newSynthTestGenerator(t)
	exec.pregenerateResult = pybridge.PregenResult{
		Paths: []string{"/tmp/apple.mp3", "/tmp/banana.mp3"},
	}
	// apple synthesizes fine (mimo bytes AA); banana fails → whole-batch fallback.
	synth := &fakeWordSynth{pcmByWord: map[string][]byte{
		"apple": {0xAA, 0xAA},
	}, failWords: map[string]bool{"banana": true}}
	gen.SetWordSynthesizer(synth)

	paths, source, err := gen.GenerateForSessionWithSource(context.Background(), "sess-fallback", []string{"apple", "banana"})
	if err != nil {
		t.Fatalf("fallback path failed: %v", err)
	}
	if source != SourceVolcengine {
		t.Errorf("source = %q, want %q", source, SourceVolcengine)
	}
	if exec.calledPregenerate != 1 {
		t.Errorf("RunPregenerate calls = %d, want 1 (whole-batch fallback)", exec.calledPregenerate)
	}
	if synth.Calls() != 2 {
		t.Errorf("synth calls = %d, want 2 (apple then banana — synthesis stops at the first failure)", synth.Calls())
	}

	// All words must have cache entries — the invariant holds after fallback.
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want 2", paths)
	}
	// Overwrite proof: apple was cached with mimo bytes AA before the
	// fallback; the fallback must have replaced them with converted bytes EE.
	loaded, err := cache.Load("sess-fallback", "apple")
	if err != nil {
		t.Fatalf("Load(apple) failed: %v", err)
	}
	if string(loaded) == string([]byte{0xAA, 0xAA}) {
		t.Error("apple cache still holds mimo bytes — fallback did not overwrite")
	}
	if string(loaded) != string([]byte{0xEE, 0xEE, 0xEE, 0xEE}) {
		t.Errorf("apple cache = %x, want fallback bytes eeeeeeee", loaded)
	}
	if _, err := os.Stat(paths["banana"]); err != nil {
		t.Errorf("banana cache path missing: %v", err)
	}
	if paths["apple"] == "" {
		t.Error("paths[apple] empty")
	}
}

// TestGenerateForSessionWithSource_NilSynthIsVolcengine: no synthesizer
// attached → the pregenerate path with SourceVolcengine (backward compatible
// with every pre-DICT-03 wiring).
func TestGenerateForSessionWithSource_NilSynthIsVolcengine(t *testing.T) {
	gen, exec, _ := newSynthTestGenerator(t)
	exec.pregenerateResult = pybridge.PregenResult{Paths: []string{"/tmp/word.mp3"}}

	paths, source, err := gen.GenerateForSessionWithSource(context.Background(), "sess-plain", []string{"word"})
	if err != nil {
		t.Fatalf("GenerateForSessionWithSource failed: %v", err)
	}
	if source != SourceVolcengine {
		t.Errorf("source = %q, want %q", source, SourceVolcengine)
	}
	if len(paths) != 1 {
		t.Errorf("paths = %v, want 1", paths)
	}
}

// TestGenerateForSessionWithSource_EmptyWords: empty word list → empty map,
// SourceVolcengine, no synth call and no subprocess spawn (unchanged
// contract).
func TestGenerateForSessionWithSource_EmptyWords(t *testing.T) {
	gen, exec, _ := newSynthTestGenerator(t)
	synth := &fakeWordSynth{}
	gen.SetWordSynthesizer(synth)

	paths, source, err := gen.GenerateForSessionWithSource(context.Background(), "sess-empty", nil)
	if err != nil {
		t.Fatalf("GenerateForSessionWithSource failed: %v", err)
	}
	if paths == nil || len(paths) != 0 {
		t.Errorf("paths = %#v, want non-nil empty map", paths)
	}
	if source != SourceVolcengine {
		t.Errorf("source = %q, want %q", source, SourceVolcengine)
	}
	if exec.calledPregenerate != 0 || synth.Calls() != 0 {
		t.Errorf("pregenerate=%d synth=%d, want 0/0", exec.calledPregenerate, synth.Calls())
	}
}

// TestGenerateForSessionWithSource_FallbackBatchFailureSurfaces: when the
// fallback batch ALSO fails (pregenerate error), the session init still
// fails with ErrTTSGenerationFailed — a broken fallback is a hard error, not
// a silent pass.
func TestGenerateForSessionWithSource_FallbackBatchFailureSurfaces(t *testing.T) {
	gen, exec, _ := newSynthTestGenerator(t)
	exec.pregenerateError = errors.New("volcengine batch exploded")
	gen.SetWordSynthesizer(&fakeWordSynth{failWords: map[string]bool{"word": true}})

	_, _, err := gen.GenerateForSessionWithSource(context.Background(), "sess-doublefail", []string{"word"})
	if !errors.Is(err, ErrTTSGenerationFailed) {
		t.Errorf("err = %v, want ErrTTSGenerationFailed", err)
	}
}

// TestGenerateForSession_DelegatesAndDiscardsSource: the legacy method keeps
// its signature and returns the same paths (backward compat for existing
// callers/tests).
func TestGenerateForSession_DelegatesAndDiscardsSource(t *testing.T) {
	gen, exec, _ := newSynthTestGenerator(t)
	exec.pregenerateResult = pybridge.PregenResult{Paths: []string{"/tmp/word.mp3"}}

	paths, err := gen.GenerateForSession(context.Background(), "sess-legacy", []string{"word"})
	if err != nil {
		t.Fatalf("GenerateForSession failed: %v", err)
	}
	if len(paths) != 1 {
		t.Errorf("paths = %v, want 1", paths)
	}
}

// TestSetWordSynthesizer_NilDetaches: passing nil restores Volcengine-only
// behaviour after a synth was attached.
func TestSetWordSynthesizer_NilDetaches(t *testing.T) {
	gen, exec, _ := newSynthTestGenerator(t)
	exec.pregenerateResult = pybridge.PregenResult{Paths: []string{"/tmp/word.mp3"}}
	gen.SetWordSynthesizer(&fakeWordSynth{})
	gen.SetWordSynthesizer(nil)

	_, source, err := gen.GenerateForSessionWithSource(context.Background(), "sess-detach", []string{"word"})
	if err != nil {
		t.Fatalf("GenerateForSessionWithSource failed: %v", err)
	}
	if source != SourceVolcengine {
		t.Errorf("source = %q, want %q after nil detach", source, SourceVolcengine)
	}
}
