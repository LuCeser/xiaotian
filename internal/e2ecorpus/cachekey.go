package e2ecorpus

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ConvertVersion versions the 24kHz→16kHz mono s16le conversion applied
// before /rt injection. Bump it whenever the conversion changes — the cache
// key includes it, so stale converted inputs can never be hit (计划第 4 节：
// 缓存键包含转换算法版本).
const ConvertVersion = "resample-16k-mono-s16le-v1"

// InputSampleRate is the wire format every /rt client audio path expects:
// 16kHz mono signed 16-bit LE.
const InputSampleRate = 16000

// SynthParams fixes every synthesis parameter that can change the audio
// bytes. All of them take part in the cache key.
type SynthParams struct {
	Model      string `json:"model"`      // e.g. "mimo-v2.5-tts"
	Voice      string `json:"voice"`      // e.g. "冰糖"
	Style      string `json:"style"`      // style instruction; "" = 正常语速无风格
	Format     string `json:"format"`     // source format requested from TTS, e.g. "wav"
	SampleRate int    `json:"sampleRate"` // effective synthesis sample rate (24000)
}

// FirstBatchParams is the first-batch profile: one fixed voice, normal
// speed (计划第 4 节：首批固定一种音色、正常语速；音色名称不得标为真实童声).
// Returned by value so callers cannot mutate the shared profile.
func FirstBatchParams() SynthParams {
	return SynthParams{
		Model:      "mimo-v2.5-tts",
		Voice:      "冰糖",
		Style:      "",
		Format:     "wav",
		SampleRate: 24000,
	}
}

// Key derives the cache key for an entry's audio: SHA-256 over the text and
// every synthesis/conversion parameter above. Two entries with the same
// text intentionally share one key — the same sentence is synthesized once
// and reused across scenes (scene decides the expectation, not the audio).
func Key(e Entry, p SynthParams) string {
	sum := sha256.Sum256([]byte(keyMaterial(e, p)))
	return hex.EncodeToString(sum[:])
}

// keyMaterial is the canonical string hashed into a key. Unit-separator
// delimited so parameter values containing "|" cannot forge collisions.
func keyMaterial(e Entry, p SynthParams) string {
	return strings.Join([]string{
		"e2ecorpus-key-v1",
		e.Text,
		p.Model,
		p.Voice,
		p.Style,
		p.Format,
		strconv.Itoa(p.SampleRate),
		ConvertVersion,
	}, "\x1f")
}

// Cache layout inside the audio dir (which lives OUTSIDE the repo, pointed
// at by XIAOTIAN_E2E_AUDIO_DIR):
//
//	<key>.src.wav    source audio exactly as returned by TTS
//	<key>.in16k.wav  16kHz mono s16le WAV ready for /rt injection
//	<key>.json       metadata + SHA-256 of both files (store.go)
func SourcePath(dir, key string) string { return filepath.Join(dir, key+".src.wav") }
func InputPath(dir, key string) string  { return filepath.Join(dir, key+".in16k.wav") }
func MetaPath(dir, key string) string   { return filepath.Join(dir, key+".json") }

// Hit reports whether a cache entry is present (all three files exist).
// Presence is the cheap pre-check before synthesis; integrity (hashes,
// parseable WAV, duration consistency) is verified when the entry is read
// back, and a damaged entry is reported rather than silently regenerated
// (计划第 4 节：默认只读缓存，prepare-missing 只补缺失项).
func Hit(dir, key string) bool {
	for _, p := range []string{SourcePath(dir, key), InputPath(dir, key), MetaPath(dir, key)} {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}
