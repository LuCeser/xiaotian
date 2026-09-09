package tts

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// TestGenerateText_Success_ChineseFeedback: the Ticket-06 spoken-feedback
// path — a Chinese sentence synthesizes via pregenerate (bare, chinese voice
// profile), converts, and lands in the cache under the text itself as key,
// with the returned path readable and Load-able.
func TestGenerateText_Success_ChineseFeedback(t *testing.T) {
	exec := &fakeExecutor{
		pregenerateResult: pybridge.PregenResult{Paths: []string{"/tmp/fb.mp3"}},
	}
	cache, err := NewSessionCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSessionCache failed: %v", err)
	}
	gen, err := NewGenerator(exec, cache, nil, infra.DefaultConfig().TTS)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}
	convert := &mockConvertFn{pcmData: PCMData{Samples: []byte{0x01, 0x02}}}
	gen.convertFn = convert.call

	const text = "太棒了！3 个词全部正确。"
	path, err := gen.GenerateText(context.Background(), "session-1", text, "chinese")
	if err != nil {
		t.Fatalf("GenerateText failed: %v", err)
	}

	// The pregenerate call carries the sentence verbatim, chinese voice, bare.
	if exec.calledPregenerate != 1 {
		t.Fatalf("RunPregenerate calls = %d, want 1", exec.calledPregenerate)
	}
	if len(exec.lastOpts.Words) != 1 || exec.lastOpts.Words[0] != text {
		t.Errorf("Words = %v, want [%q]", exec.lastOpts.Words, text)
	}
	if exec.lastOpts.Language != "chinese" || exec.lastOpts.GenerationMode != "bare" {
		t.Errorf("opts language/mode = %q/%q, want chinese/bare", exec.lastOpts.Language, exec.lastOpts.GenerationMode)
	}

	// The returned path exists and Load round-trips through the same key.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("returned path not on disk: %v (%q)", err, path)
	}
	loaded, err := cache.Load("session-1", text)
	if err != nil || len(loaded) != 2 {
		t.Errorf("Load(feedback text) = (%d bytes, %v), want 2 bytes round-trip", len(loaded), err)
	}
}

// TestGenerateText_EmptyText: blank text is rejected before any subprocess.
func TestGenerateText_EmptyText(t *testing.T) {
	exec := &fakeExecutor{}
	cache, _ := NewSessionCache(t.TempDir(), nil)
	gen, _ := NewGenerator(exec, cache, nil, infra.DefaultConfig().TTS)

	if _, err := gen.GenerateText(context.Background(), "s", "   ", "chinese"); err == nil {
		t.Error("empty text accepted, want error")
	}
	if exec.calledPregenerate != 0 {
		t.Errorf("RunPregenerate calls = %d, want 0", exec.calledPregenerate)
	}
}

// TestGenerateText_PregenerateFails: an executor error surfaces as
// ErrTTSGenerationFailed (detectable via errors.Is).
func TestGenerateText_PregenerateFails(t *testing.T) {
	exec := &fakeExecutor{pregenerateError: errors.New("boom")}
	cache, _ := NewSessionCache(t.TempDir(), nil)
	gen, _ := NewGenerator(exec, cache, nil, infra.DefaultConfig().TTS)

	_, err := gen.GenerateText(context.Background(), "s", "加油", "chinese")
	if !errors.Is(err, ErrTTSGenerationFailed) {
		t.Errorf("err = %v, want ErrTTSGenerationFailed", err)
	}
}

// TestGenerateText_NoPaths: a path-less pregen result is a failure, not an
// index panic.
func TestGenerateText_NoPaths(t *testing.T) {
	exec := &fakeExecutor{}
	cache, _ := NewSessionCache(t.TempDir(), nil)
	gen, _ := NewGenerator(exec, cache, nil, infra.DefaultConfig().TTS)

	if _, err := gen.GenerateText(context.Background(), "s", "加油", "chinese"); err == nil {
		t.Error("path-less pregen accepted, want error")
	}
}
