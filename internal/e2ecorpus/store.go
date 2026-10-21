package e2ecorpus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GeneratorVersion identifies the writer of cache entries; bump when the
// on-disk layout changes.
const GeneratorVersion = "e2ecorpus-store-v1"

// durationTolerance bounds conversion/meta duration drift (resample
// rounding is sub-millisecond; anything larger means a real inconsistency).
const durationTolerance = 2 * time.Millisecond

// FileMeta describes one cached audio file: its SHA-256 and the format the
// actual WAV header carried (计划第 4 节：实际采样率、声道、时长、SHA-256).
type FileMeta struct {
	SHA256        string `json:"sha256"`
	SampleRate    int    `json:"sampleRate"`
	Channels      int    `json:"channels"`
	BitsPerSample int    `json:"bitsPerSample"`
	Bytes         int    `json:"bytes"`
	DurationMs    int64  `json:"durationMs"`
}

// Meta is the <key>.json sidecar of a cache entry.
type Meta struct {
	Key              string      `json:"key"`
	Text             string      `json:"text"`
	ConvertVersion   string      `json:"convertVersion"`
	GeneratorVersion string      `json:"generatorVersion"`
	Params           SynthParams `json:"params"`
	GeneratedAt      time.Time   `json:"generatedAt"`
	Source           FileMeta    `json:"source"`
	Input            FileMeta    `json:"input"`
}

// Cached is a verified cache entry; InputWAV is the 16kHz mono s16le WAV
// ready for /rt injection.
type Cached struct {
	InputWAV []byte
	Meta     Meta
}

// Save validates one synthesized entry and stores it atomically: source
// WAV, converted 16kHz input WAV and metadata sidecar. The source must be
// parseable mono s16 whose actual header rate matches the params (the
// header wins — a pcm16 reply is 24kHz whatever the request said, 计划第 4
// 节). All three files are staged as temps and renamed into place last; any
// failure removes everything, so a damaged save never leaves a hit-able
// "success" entry (空音频、错误格式、校验失败不缓存).
func Save(dir string, e Entry, p SynthParams, srcWAV []byte, generatedAt time.Time) (string, error) {
	key := Key(e, p)
	src, err := ParseWAV(srcWAV)
	if err != nil {
		return "", fmt.Errorf("e2ecorpus: source audio: %w", err)
	}
	if src.Channels != 1 {
		return "", fmt.Errorf("e2ecorpus: source audio has %d channels, want mono", src.Channels)
	}
	if src.SampleRate != p.SampleRate {
		return "", fmt.Errorf("e2ecorpus: params claim %d Hz but the WAV header carries %d Hz — the header wins, fix the params",
			p.SampleRate, src.SampleRate)
	}
	inPCM := src.PCM
	if src.SampleRate != InputSampleRate {
		if inPCM, err = ResamplePCM16(src.PCM, src.SampleRate, InputSampleRate); err != nil {
			return "", fmt.Errorf("e2ecorpus: convert to %d Hz: %w", InputSampleRate, err)
		}
	}
	inWAV := BuildWAV(inPCM, InputSampleRate)
	in, err := ParseWAV(inWAV)
	if err != nil {
		return "", fmt.Errorf("e2ecorpus: converted audio: %w", err)
	}
	if drift := src.Duration - in.Duration; drift > durationTolerance || drift < -durationTolerance {
		return "", fmt.Errorf("e2ecorpus: duration drift after conversion: %dms → %dms",
			src.Duration.Milliseconds(), in.Duration.Milliseconds())
	}
	meta := Meta{
		Key:              key,
		Text:             e.Text,
		ConvertVersion:   ConvertVersion,
		GeneratorVersion: GeneratorVersion,
		Params:           p,
		GeneratedAt:      generatedAt,
		Source:           fileMeta(srcWAV, src),
		Input:            fileMeta(inWAV, in),
	}
	metaRaw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", fmt.Errorf("e2ecorpus: marshal meta: %w", err)
	}
	if err := writeEntryAtomic(dir, key, srcWAV, inWAV, metaRaw); err != nil {
		return "", err
	}
	return key, nil
}

// Load reads an entry back and verifies it end to end: metadata parses and
// matches the key and current conversion version, both files match their
// recorded SHA-256, parse as the recorded format, and source/input durations
// agree (时长一致性). A damaged entry is an error — never a silent miss that
// could be papered over by re-synthesizing (计划第 4 节：默认只读缓存).
func Load(dir, key string) (Cached, error) {
	metaRaw, err := os.ReadFile(MetaPath(dir, key))
	if err != nil {
		return Cached{}, fmt.Errorf("e2ecorpus: read meta: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return Cached{}, fmt.Errorf("e2ecorpus: parse meta: %w", err)
	}
	if meta.Key != key {
		return Cached{}, fmt.Errorf("e2ecorpus: meta key %q does not match requested %q", meta.Key, key)
	}
	if meta.ConvertVersion != ConvertVersion {
		return Cached{}, fmt.Errorf("e2ecorpus: meta convert version %q, want %q (stale entry under this key)",
			meta.ConvertVersion, ConvertVersion)
	}
	src, srcErr := readVerified(SourcePath(dir, key), meta.Source)
	if srcErr != nil {
		return Cached{}, fmt.Errorf("e2ecorpus: source: %w", srcErr)
	}
	in, inErr := readVerified(InputPath(dir, key), meta.Input)
	if inErr != nil {
		return Cached{}, fmt.Errorf("e2ecorpus: input: %w", inErr)
	}
	if in.Info.SampleRate != InputSampleRate || in.Info.Channels != 1 || in.Info.BitsPerSample != 16 {
		return Cached{}, fmt.Errorf("e2ecorpus: input is %dHz/%dch/%dbit, want 16kHz mono s16le",
			in.Info.SampleRate, in.Info.Channels, in.Info.BitsPerSample)
	}
	if drift := src.Info.Duration - in.Info.Duration; drift > durationTolerance || drift < -durationTolerance {
		return Cached{}, fmt.Errorf("e2ecorpus: source (%dms) and input (%dms) durations disagree",
			src.Info.Duration.Milliseconds(), in.Info.Duration.Milliseconds())
	}
	return Cached{InputWAV: in.Raw, Meta: meta}, nil
}

// verifiedFile is a cached file's bytes plus its parsed format.
type verifiedFile struct {
	Raw  []byte
	Info WAVInfo
}

// readVerified checks one cached file against its recorded metadata.
func readVerified(path string, fm FileMeta) (verifiedFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return verifiedFile{}, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != fm.SHA256 {
		return verifiedFile{}, fmt.Errorf("%s content hash mismatch — file damaged", filepath.Base(path))
	}
	info, err := ParseWAV(raw)
	if err != nil {
		return verifiedFile{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if info.SampleRate != fm.SampleRate || info.Channels != fm.Channels || info.BitsPerSample != fm.BitsPerSample {
		return verifiedFile{}, fmt.Errorf("%s is %dHz/%dch/%dbit, metadata says %dHz/%dch/%dbit",
			filepath.Base(path), info.SampleRate, info.Channels, info.BitsPerSample,
			fm.SampleRate, fm.Channels, fm.BitsPerSample)
	}
	if drift := info.Duration - time.Duration(fm.DurationMs)*time.Millisecond; drift > durationTolerance || drift < -durationTolerance {
		return verifiedFile{}, fmt.Errorf("%s duration %dms does not match metadata %dms",
			filepath.Base(path), info.Duration.Milliseconds(), fm.DurationMs)
	}
	return verifiedFile{Raw: raw, Info: info}, nil
}

func fileMeta(wav []byte, info WAVInfo) FileMeta {
	sum := sha256.Sum256(wav)
	return FileMeta{
		SHA256:        hex.EncodeToString(sum[:]),
		SampleRate:    info.SampleRate,
		Channels:      info.Channels,
		BitsPerSample: info.BitsPerSample,
		Bytes:         len(wav),
		DurationMs:    info.Duration.Milliseconds(),
	}
}

// writeEntryAtomic stages every file as a temp beside its target, then
// renames all three into place. A failed publish rolls back whatever was
// already renamed and removes the temps — Hit only turns true when all
// three files survive together.
func writeEntryAtomic(dir, key string, srcWAV, inWAV, metaRaw []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("e2ecorpus: create cache dir: %w", err)
	}
	files := []struct {
		path string
		data []byte
	}{
		{SourcePath(dir, key), srcWAV},
		{InputPath(dir, key), inWAV},
		{MetaPath(dir, key), metaRaw},
	}
	tmps := make([]string, len(files))
	removeTemps := func() {
		for _, tmp := range tmps {
			if tmp != "" {
				_ = os.Remove(tmp)
			}
		}
	}
	for i, f := range files {
		tmp, err := writeStaged(f.path, f.data)
		if err != nil {
			removeTemps()
			return err
		}
		tmps[i] = tmp
	}
	var published []string
	for i, f := range files {
		if err := os.Rename(tmps[i], f.path); err != nil {
			for _, done := range published { // roll back already-published files
				_ = os.Remove(done)
			}
			removeTemps()
			return fmt.Errorf("e2ecorpus: publish %s: %w", filepath.Base(f.path), err)
		}
		published = append(published, f.path)
		tmps[i] = ""
	}
	return nil
}

// writeStaged writes data to a fresh temp file beside path (same directory,
// so the later rename is atomic) and returns the temp name. Crash-durability
// is not required: the cache is regenerable, only torn visibility matters.
func writeStaged(path string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("e2ecorpus: stage %s: %w", filepath.Base(path), err)
	}
	name := f.Name()
	discard := func() {
		_ = f.Close()
		_ = os.Remove(name)
	}
	if _, err := f.Write(data); err != nil {
		discard()
		return "", fmt.Errorf("e2ecorpus: write %s: %w", filepath.Base(path), err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("e2ecorpus: close %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("e2ecorpus: chmod %s: %w", name, err)
	}
	return name, nil
}
