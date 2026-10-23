package e2ecorpus

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeSourceWAV builds a mono s16le WAV of the given duration at the
// first-batch synthesis rate (24kHz), standing in for a TTS reply.
func fakeSourceWAV(seconds float64) []byte {
	n := int(24000 * seconds)
	pcm := make([]byte, n*2)
	for i := 0; i < n; i++ {
		putSample(pcm, i, int16(math.Sin(float64(i)/12)*6000))
	}
	return BuildWAV(pcm, 24000)
}

// assertDirEmpty fails when anything was left behind in a failed save's dir.
func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("failed save left %d files behind: %v", len(left), left)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	src := fakeSourceWAV(0.5)

	key, err := Save(dir, sampleEntry(), FirstBatchParams(), src, at)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !Hit(dir, key) {
		t.Fatal("saved entry does not count as a hit")
	}

	cached, err := Load(dir, key)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cached.Meta.Text != sampleEntry().Text {
		t.Errorf("meta.Text = %q, want the corpus text", cached.Meta.Text)
	}
	if !cached.Meta.GeneratedAt.Equal(at) {
		t.Errorf("meta.GeneratedAt = %v, want %v", cached.Meta.GeneratedAt, at)
	}
	in, err := ParseWAV(cached.InputWAV)
	if err != nil {
		t.Fatalf("cached input WAV: %v", err)
	}
	if in.SampleRate != InputSampleRate || in.Channels != 1 || in.BitsPerSample != 16 {
		t.Errorf("cached input is %dHz/%dch/%dbit, want 16kHz mono s16le", in.SampleRate, in.Channels, in.BitsPerSample)
	}
	// 时长一致性: converting 500ms at 24kHz must land on ~500ms at 16kHz.
	if drift := in.Duration - 500*time.Millisecond; drift > durationTolerance || drift < -durationTolerance {
		t.Errorf("input duration = %v, want 500ms ± %v", in.Duration, durationTolerance)
	}
}

// TestSaveRejectsBadSource: garbage, empty audio, stereo or lying params
// must be refused — and refused cleanly, leaving no hit-able files behind
// (空音频、错误格式、校验失败不缓存).
func TestSaveRejectsBadSource(t *testing.T) {
	cases := []struct {
		name    string
		wantErr string
		src     []byte
	}{
		{"garbage bytes", "not a RIFF", []byte("definitely not audio data......")},
		{"empty audio", "empty", BuildWAV(nil, 24000)},
		{"stereo source", "mono", buildTestWAV(24000, 2, make([]byte, 4800))},
		{"params rate lie", "header", fakeSourceWAVAt(22050, 0.1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			params := FirstBatchParams()
			if tc.name == "params rate lie" {
				params.SampleRate = 24000 // header says 22050 — params must lose
			}
			if _, err := Save(dir, sampleEntry(), params, tc.src, time.Now()); err == nil {
				t.Fatal("Save succeeded, want rejection")
			} else if !contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
			assertDirEmpty(t, dir)
		})
	}
}

// fakeSourceWAVAt is fakeSourceWAV at an arbitrary rate, for the mismatch case.
func fakeSourceWAVAt(rate int, seconds float64) []byte {
	n := int(float64(rate) * seconds)
	pcm := make([]byte, n*2)
	for i := 0; i < n; i++ {
		putSample(pcm, i, int16(math.Sin(float64(i)/12)*6000))
	}
	return BuildWAV(pcm, rate)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// TestLoadRejectsCorruption: every tampering below must surface as a Load
// error — a damaged entry may never pass as usable (损坏文件拒绝).
func TestLoadRejectsCorruption(t *testing.T) {
	saveFresh := func(t *testing.T) (dir, key string) {
		t.Helper()
		dir = t.TempDir()
		key, err := Save(dir, sampleEntry(), FirstBatchParams(), fakeSourceWAV(0.3), time.Now())
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
		return dir, key
	}
	tamperInput := func(t *testing.T, dir, key string) {
		t.Helper()
		path := InputPath(dir, key)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw[len(raw)-10] ^= 0xFF                               // flip a sample near the end
		if err := os.WriteFile(path, raw, 0o600); err != nil { //nolint:gosec // G703: path is built from t.TempDir() + a Save-returned key, not user input
			t.Fatal(err)
		}
	}

	t.Run("input byte flipped", func(t *testing.T) {
		dir, key := saveFresh(t)
		tamperInput(t, dir, key)
		if _, err := Load(dir, key); err == nil {
			t.Fatal("Load accepted a damaged input file")
		}
	})

	t.Run("meta truncated", func(t *testing.T) {
		dir, key := saveFresh(t)
		if err := os.WriteFile(MetaPath(dir, key), []byte(`{"key":`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir, key); err == nil {
			t.Fatal("Load accepted unparseable metadata")
		}
	})

	t.Run("stale convert version", func(t *testing.T) {
		dir, key := saveFresh(t)
		rewriteMeta(t, MetaPath(dir, key), map[string]any{"convertVersion": "ancient-v0"})
		if _, err := Load(dir, key); err == nil {
			t.Fatal("Load accepted a stale conversion version")
		}
	})

	t.Run("meta key mismatch", func(t *testing.T) {
		dir, key := saveFresh(t)
		rewriteMeta(t, MetaPath(dir, key), map[string]any{"key": "someotherkey"})
		if _, err := Load(dir, key); err == nil {
			t.Fatal("Load accepted metadata written for another key")
		}
	})

	t.Run("missing input file", func(t *testing.T) {
		dir, key := saveFresh(t)
		if err := os.Remove(InputPath(dir, key)); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir, key); err == nil {
			t.Fatal("Load accepted a missing input file")
		}
	})

	t.Run("duration rewritten in meta", func(t *testing.T) {
		dir, key := saveFresh(t)
		raw, err := os.ReadFile(MetaPath(dir, key))
		if err != nil {
			t.Fatal(err)
		}
		var meta map[string]any
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatal(err)
		}
		input := meta["input"].(map[string]any)
		input["durationMs"] = 9999.0
		rewriteMeta(t, MetaPath(dir, key), meta)
		if _, err := Load(dir, key); err == nil {
			t.Fatal("Load accepted a duration inconsistent with the audio")
		}
	})
}

// rewriteMeta patches fields of a metadata sidecar and writes it back.
func rewriteMeta(t *testing.T, path string, patch map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if uErr := json.Unmarshal(raw, &meta); uErr != nil {
		t.Fatal(uErr)
	}
	for k, v := range patch {
		if v == nil {
			delete(meta, k)
			continue
		}
		meta[k] = v
	}
	out, mErr := json.Marshal(meta)
	if mErr != nil {
		t.Fatal(mErr)
	}
	if wErr := os.WriteFile(path, out, 0o600); wErr != nil {
		t.Fatal(wErr)
	}
}

// TestSaveDoesNotOverwriteOnFailure: a failed re-save (bad source) must
// leave an existing good entry intact rather than deleting it.
func TestSaveDoesNotOverwriteOnFailure(t *testing.T) {
	dir := t.TempDir()
	entry := sampleEntry()
	params := FirstBatchParams()
	key, err := Save(dir, entry, params, fakeSourceWAV(0.3), time.Now())
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if _, err := Save(dir, entry, params, []byte("junk"), time.Now()); err == nil {
		t.Fatal("second Save with garbage succeeded, want rejection")
	}
	if !Hit(dir, key) {
		t.Fatal("good entry vanished after a failed re-save")
	}
	if _, err := Load(dir, key); err != nil {
		t.Fatalf("good entry no longer loads: %v", err)
	}
}
