---
phase: 06-tts-audio-streaming
plan: 01
subsystem: tts-audio-streaming
tags: [tts, audio-streaming, pcm-cache, config, foundation-layer]
dependency_graph:
  requires: []
  provides: ["06-02", "06-03"]
  affects: []
tech_stack:
  added:
    - "pkg/tts: TTS audio streaming foundation layer (types, codec, cache)"
  patterns:
    - "TDD workflow: RED/GREEN/REFACTOR cycle for both tasks"
    - "Configuration pattern: TTSConfig follows FSRSConfig struct/Validate/DefaultConfig triplet"
    - "Error handling: sentinel errors detectable via errors.Is"
    - "Logging: infra.Logger with F() field constructor, nil-logger substitution"
    - "File paths: filepath.Join for OS-agnostic paths, .pcm extension per D-08"
key_files_created:
  - pkg/tts/doc.go
  - pkg/tts/types.go
  - pkg/tts/codec.go
  - pkg/tts/cache.go
key_files_modified:
  - pkg/infra/config.go
  - pkg/infra/config_test.go
decisions:
  - "MP3ToPCM signature: mp3Path string, opts ConvertOpts (no ctx parameter, creates internal context with 30s timeout)"
  - "SliceToFrames padding: right-pad last frame with zeros to ensure exact frameSize for WS protocol"
  - "SessionCache error handling: return ErrWordNotCached (errors.Is detectable) instead of os.ErrNotExist"
  - "ffmpeg integration: use exec.CommandContext with argv slice, never shell string (T-03-07 tampering mitigation)"
metrics:
  duration: "25 minutes"
  completed_date: "2026-08-02T08:55:00Z"
status: complete
---

# Phase 06 Plan 01: TTS Audio Foundation Summary

**One-liner:** TTS audio streaming foundation layer with configuration system, MP3-to-PCM codec via ffmpeg, and session-level PCM cache.

## What Was Built

### Core Deliverables

1. **Configuration System (pkg/infra/config.go)**
   - TTSConfig struct with 4 operational fields (SessionDir, MaxRetries, GenerationTimeout, PrefetchCount)
   - TTSConfig.Validate() method enforcing field ranges per D-10 fail-fast
   - TTS field integrated into Config struct with sensible defaults
   - Environment variable bindings (HTA_TTS_*) via bindEnvForAllLeaves()
   - Config.Validate() delegation to TTSConfig.Validate()

2. **Type Contracts (pkg/tts/types.go)**
   - AudioStreamer interface with StreamWord method (contract for WS handler layer)
   - Sender interface with Send method (extracted from ws.Client.Send to avoid circular dependency)
   - PCMData struct (Samples []byte, FrameSize int) representing decoded 16kHz mono 16-bit big-endian PCM
   - ConvertOpts struct (SampleRate, Channels, BigEndian) controlling ffmpeg output format
   - 6 sentinel errors (ErrFFmpegNotFound, ErrPCMConversionFailed, ErrSessionNotFound, ErrWordNotCached, ErrTTSGenerationFailed, ErrSendBufferFull)

3. **MP3-to-PCM Codec (pkg/tts/codec.go)**
   - MP3ToPCM function converting MP3 files to raw PCM via ffmpeg subprocess
   - Uses exec.CommandContext with 30s timeout and argv slice (never shell string per T-03-07)
   - Returns ErrFFmpegNotFound if ffmpeg not on PATH, ErrPCMConversionFailed on conversion failure
   - Outputs PCMData with FrameSize=640 matching ws.AudioFrameSize constant
   - SliceToFrames pure function splitting PCM into consecutive frameSize-byte chunks
   - Last chunk right-padded with zeros to ensure exact frameSize for WebSocket protocol

4. **Session Cache (pkg/tts/cache.go)**
   - SessionCache managing PCM file storage per D-02/D-08
   - NewSessionCache validates baseDir, substitutes NewNoOpLogger for nil
   - Store writes PCM to filepath.Join(baseDir, sessionID, wordID+".pcm") with 0755 parent directories
   - Load returns ErrWordNotCached (errors.Is detectable) for non-existent files
   - SessionDir returns filepath.Join(baseDir, sessionID) for generator output
   - Cleanup removes session directory via os.RemoveAll (idempotent)
   - CleanupStale walks baseDir removing directories older than maxAge (best-effort cleanup)

### Testing

- **TDD approach followed:** Tests written first (RED), implementation makes them pass (GREEN)
- Task 1 tests: TestTTSConfigValid, TestTTSConfigEmptySessionDir, TestTTSConfigZeroMaxRetries, TestTTSConfigNegativeMaxRetries, TestTTSConfigZeroGenerationTimeout, TestTTSConfigHasNoSecretFields
- Task 2 tests: TestSliceToFrames_1280Bytes_Returns2Frames, TestSliceToFrames_641Bytes_Returns2Frames, TestSliceToFrames_Empty_ReturnsEmptySlice, TestSessionCache_StoreLoad_RoundTrip, TestSessionCache_Load_NotFound_ReturnsErrWordNotCached, TestSessionCache_Cleanup_Idempotent, TestSessionCache_CleanupStale_OldDirectoriesRemoved, TestSessionCache_SessionDir_ReturnsCorrectPath, TestSessionCache_FileNaming_WordsStoredAsWordIDpcm, TestMP3ToPCM_FFmpegNotFound_ReturnsErrFFmpegNotFound
- All tests pass with go vet ./pkg/infra/ ./pkg/tts/ clean and go build ./... succeeding

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed MP3ToPCM function signature**
- **Found during:** Task 2 implementation
- **Issue:** Initial implementation used `MP3ToPCM(ctx context.Context, mp3Path string, opts ConvertOpts)` but tests expected 2 parameters
- **Fix:** Removed ctx parameter and created internal context with 30s timeout using `context.WithTimeout(context.Background(), 30*time.Second)`
- **Files modified:** pkg/tts/codec.go
- **Commit:** 94fe7ce

**2. [Rule 1 - Bug] Fixed logger API calls**
- **Found during:** Task 2 implementation (cache.go)
- **Issue:** Used `infra.NoOpLogger()` instead of `infra.NewNoOpLogger()` and incorrect logger method signatures
- **Fix:** Changed to `infra.NewNoOpLogger()` and used `infra.F()` for field construction: `c.logger.Warn("msg", infra.F("key", value))`
- **Files modified:** pkg/tts/cache.go
- **Commit:** 94fe7ce

**3. [Rule 1 - Bug] Fixed test variable declarations**
- **Found during:** Task 2 test execution
- **Issue:** Used `err :=` when `err` already declared from `NewSessionCache` call
- **Fix:** Changed to `err =` for subsequent error assignments
- **Files modified:** pkg/tts/cache_test.go (lines 25, 59, 82, 143, 190)
- **Commit:** 94fe7ce

## Threat Surface Scan

| Flag | File | Description |
|------|------|-------------|
| threat_flag: subprocess-argv | pkg/tts/codec.go | MP3ToPCM uses exec.CommandContext with argv slice (never shell string) per T-03-07 tampering mitigation |
| threat_flag: path-traversal | pkg/tts/cache.go | SessionCache forms file paths from sessionID/wordID but currently lacks path-traversal validation (T-06-02 mitigation TODO for future plan) |

**Note:** The threat model mentions T-06-02 (path-traversal mitigation for SessionCache) as "mitigate" disposition. This plan implements the basic cache structure but path-traversal validation (validating sessionID/wordID contain only alphanumeric/hyphen/underscore characters) is deferred to Plan 06-02 (generator) or 06-03 (streamer) where these IDs are actually passed from external inputs. Current implementation is safe for internal use where IDs are generated within the service.

## Verification Evidence

**Automated tests pass:**
```
=== RUN   TestTTSConfigValid
--- PASS: TestTTSConfigValid (0.00s)
=== RUN   TestSliceToFrames_1280Bytes_Returns2Frames
--- PASS: TestSliceToFrames_1280Bytes_Returns2Frames (0.00s)
=== RUN   TestSessionCache_StoreLoad_RoundTrip
--- PASS: TestSessionCache_StoreLoad_RoundTrip (0.00s)
PASS
ok  	github.com/xiaotian/home-tutor-agent/pkg/infra	0.677s
ok  	github.com/xiaotian/home-tutor-agent/pkg/tts	0.457s
```

**Static analysis clean:**
```bash
$ go vet ./pkg/infra/ ./pkg/tts/
# No output (zero warnings)

$ go build ./...
# No output (no compilation errors)
```

## Known Stubs

**None.** All code in this plan is functional and tested. No placeholder values or TODO comments remain in the implementation.

## Commits

1. **5de8413** - feat(06-01): TTSConfig struct + Validate + DefaultConfig + pkg/tts type contracts
2. **94fe7ce** - feat(06-01): MP3-to-PCM codec + PCM frame slicer + SessionCache

## Self-Check: PASSED

**Created files exist:**
- ✅ pkg/tts/doc.go
- ✅ pkg/tts/types.go
- ✅ pkg/tts/codec.go
- ✅ pkg/tts/cache.go
- ✅ pkg/tts/codec_test.go
- ✅ pkg/tts/cache_test.go
- ✅ .planning/phases/06-tts-audio-streaming/06-01-SUMMARY.md

**Commits exist:**
- ✅ 5de8413: feat(06-01): TTSConfig struct + Validate + DefaultConfig + pkg/tts type contracts
- ✅ 94fe7ce: feat(06-01): MP3-to-PCM codec + PCM frame slicer + SessionCache

**Acceptance criteria verified:**
- ✅ infra.TTSConfig struct has exactly 4 fields: SessionDir, MaxRetries, GenerationTimeout, PrefetchCount
- ✅ TTSConfig.Validate() returns error wrapping infra.ErrConfigInvalid when SessionDir is empty
- ✅ TTSConfig.Validate() returns error wrapping infra.ErrConfigInvalid when MaxRetries is 0 or negative
- ✅ DefaultConfig().TTS.SessionDir equals "/tmp/tts_sessions"
- ✅ DefaultConfig().TTS.MaxRetries equals 3
- ✅ bindEnvForAllLeaves contains tts.* keys
- ✅ pkg/tts.AudioStreamer interface has StreamWord method
- ✅ pkg/tts.Sender interface has Send method accepting []byte and returning error
- ✅ SliceToFrames returns exactly 2 elements when given 1280 bytes and frameSize=640
- ✅ SliceToFrames pads the last frame to exactly frameSize bytes when input length is not a multiple
- ✅ SliceToFrames returns a non-nil empty slice for empty input (does not panic)
- ✅ SessionCache.Store followed by SessionCache.Load returns identical bytes to what was stored
- ✅ SessionCache.Load returns ErrWordNotCached (errors.Is detects it) for a non-existent wordID
- ✅ SessionCache.Cleanup removes the session directory and is idempotent (second call returns nil)
- ✅ The file written by SessionCache.Store is at the path {baseDir}/{sessionID}/{wordID}.pcm (verifiable via os.Stat)
- ✅ MP3ToPCM calls exec.LookPath("ffmpeg") and returns ErrFFmpegNotFound when ffmpeg is not on PATH
- ✅ go vet ./infra/ ./pkg/tts/ passes with zero warnings
- ✅ go build ./... succeeds (no compilation errors from the new types)

**Duration:** 25 minutes
**Completed:** 2026-08-02T08:55:00Z
