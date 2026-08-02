---
phase: 06-tts-audio-streaming
plan: 02
subsystem: TTS Audio Generation
tags: [tts, batch-generation, retry-logic, session-lifecycle]
dependency_graph:
  requires:
    - 06-01 (TTSConfig, codec.MP3ToPCM, SessionCache)
    - 03-05 (pybridge.RunPregenerate)
  provides:
    - Generator.GenerateForSession (produces PCM files for streaming)
    - Generator.CleanupSession (session lifecycle entry point)
  affects:
    - 06-03 (Streamer will consume PCM files from Generator)
tech_stack:
  added: []
  patterns:
    - TDD flow (RED/GREEN commits)
    - Manual fake Executor per D-48
    - Dependency injection (convertFn field)
    - Error classification and retry logic
key_files:
  created:
    - pkg/tts/generator.go
    - pkg/tts/generator_test.go
  modified: []
decisions:
  - Error classification: ErrFFmpegNotFound is non-retryable (infrastructure failure)
  - Retry counter starts at 1 for natural log messages ("attempt 1/3")
  - Context deadline exceeded is retryable (transient network/ffmpeg issue)
  - Session cleanup delegates to SessionCache for single responsibility
  - convertFn field injection for testing (same pattern as pybridge's exec.Cmd)
metrics:
  duration: "15 minutes"
  completed_date: "2026-08-02T13:22:00Z"
status: complete
---

# Phase 06 Plan 02: TTS Batch Generation Pipeline Summary

Build the TTS batch generation pipeline: given a list of due words, generate TTS audio via pregenerate_dictation.py (Phase 3 bridge), convert MP3 output to PCM, cache results per-session, and handle failures with the D-05 three-retry-skip strategy.

## One-Liner

Implemented TTS batch generator with GenerateForSession that calls Python bridge's RunPregenerate, converts MP3-to-PCM with retry logic (non-retryable ffmpeg errors, retryable timeouts/transients), skips words after 3 attempts, and provides CleanupSession for session lifecycle management.

## Tasks Completed

### Task 1: Generator struct + GenerateForSession batch pipeline

**RED commit (e941773):** `test(06-02): add failing test for TTS batch generator`
- 7 tests covering GenerateForSession behavior, PregenOpts construction, cache integration, and error cases
- Uses fake Executor (no real Python subprocess) per D-48 pattern
- Uses mock convertFn for MP3-to-PCM injection

**GREEN commit (5879c1a):** `feat(06-02): implement TTS batch Generator with GenerateForSession`
- Generator struct composes pybridge.Executor, SessionCache, convertFn, logger, TTSConfig
- NewGenerator validates non-nil Executor, substitutes NoOpLogger for nil logger
- GenerateForSession calls RunPregenerate with batch word list (Language="english")
- Converts each MP3 to PCM using injected convertFn (defaults to codec.MP3ToPCM)
- Stores successfully converted PCM files in SessionCache at {sessionID}/{wordID}.pcm
- Returns map of wordID → cache path for successful generations
- Empty word list returns empty map without calling RunPregenerate (subprocess guard)
- Failed conversions skip the word without aborting session (retry logic in Task 2)
- All tests pass with fake Executor and mock convertFn

### Task 2: Retry-skip logic + error classification + session cleanup helper

**RED+GREEN commit (e33255b):** `feat(06-02): implement retry-skip logic and CleanupSession`
- **Tests added (RED):**
  - TestRetryConvert_FFmpegNotFound_NoRetry: ErrFFmpegNotFound returns immediately (no retry)
  - TestRetryConvert_PCMConversionFailed_RetriesSucceed: 2 failures + 1 success on 3rd retry
  - TestRetryConvert_Timeout_SkipsAfterRetries: context.DeadlineExceeded retries then skips
  - TestRetryConvert_LogsWarningForSkippedWords: warnings logged with word_id field
  - TestCleanupSession_Success and _Idempotent: cache.Cleanup delegation

- **Implementation (GREEN):**
  - retryConvert method implements D-05 three-retry-then-skip strategy
  - Error classification: ErrFFmpegNotFound (no retry), context.DeadlineExceeded (retry), other (retry)
  - Per-attempt debug/warn logging with attempt number and max_retries
  - Returns ErrTTSGenerationFailed wrapped with "word generation exhausted retries"
  - CleanupSession delegates to cache.Cleanup with session_id logging
  - GenerateForSession now calls retryConvert and logs warnings for skipped words
  - Fixed TestGenerateForSession_CodecFailsOnSecondWord for retry behavior

All tests pass with fake Executor and mock convertFn (no real Python subprocess)

## Deviations from Plan

### Auto-fixed Issues

None - plan executed exactly as written. All acceptance criteria met:
- Generator struct has fields exec (pybridge.Executor), cache (*SessionCache), logger (infra.Logger), cfg (infra.TTSConfig)
- NewGenerator returns ErrTTSGenerationFailed (errors.Is detects) when exec is nil
- GenerateForSession with empty words returns empty map, nil error, and does NOT call exec.RunPregenerate
- GenerateForSession constructs PregenOpts with Language="english" and Words matching the input
- GenerateForSession calls cache.Store for each successfully converted word
- GenerateForSession with a codec that fails on one word still returns results for other words
- GenerateForSession returns map keys matching wordID strings, values matching cache file paths
- go vet ./pkg/tts/ passes with zero warnings
- retryConvert returns ErrFFmpegNotFound immediately without retrying when convertFn returns that sentinel
- retryConvert succeeds on the 3rd attempt when the first 2 fail with ErrPCMConversionFailed
- retryConvert returns ErrTTSGenerationFailed (errors.Is detects) after MaxRetries exhausted
- GenerateForSession logs a warning for each skipped word containing the word identifier
- CleanupSession calls cache.Cleanup and returns nil on a valid session
- CleanupSession is idempotent (calling twice does not error)

## Known Stubs

None - no stubs found in implementation.

## Threat Flags

None - no new security-relevant surface introduced beyond what was already in the threat model:
- T-06-04 (Words injection) - Words originate from FSRS DueCard.Word field; no additional injection surface
- T-06-05 (MaxRetries exhaustion) - Mitigated by MaxRetries cap [1,10] and pregenSem serialization
- T-06-06 (Skipped words not logged) - Mitigated: every skipped word emits structured log with word_id, error_type, component tag
- T-06-SC (No new packages) - Zero external Go dependencies added

## Verification Evidence

- `go build ./pkg/tts/` compiles with zero errors
- `go test ./pkg/tts/ -run TestGenerator` passes (all generator tests)
- `go test ./pkg/tts/ -run TestRetryConvert` passes (retry logic)
- `go vet ./pkg/tts/` clean
- No real Python subprocess spawned in any test (all use fake Executor)
- All 21 tests in pkg/tts pass (cache, codec, generator tests)

## Integration Points

**Calls:**
- `pybridge.Executor.RunPregenerate(ctx, PregenOpts{Words, Language})` → generates MP3 files
- `codec.MP3ToPCM(mp3Path, ConvertOpts{SampleRate: 16000, Channels: 1, BigEndian: true})` → converts to PCM
- `SessionCache.Store(sessionID, wordID, pcmData.Samples)` → persists PCM files
- `SessionCache.SessionDir(sessionID)` → returns cache directory path
- `SessionCache.Cleanup(sessionID)` → removes session artifacts (CleanupSession)

**Consumed by:**
- Plan 06-03 Streamer will call `Generator.GenerateForSession` to produce PCM files for streaming
- Phase 7 session lifecycle will call `Generator.CleanupSession` to clean up TTS resources

## Technical Notes

### Retry Logic Design
The retryConvert method implements the D-05 strategy with error classification:
1. **ErrFFmpegNotFound**: Returns immediately (infrastructure failure, retrying won't help)
2. **context.DeadlineExceeded**: Retries with debug logging (transient timeout)
3. **Other errors (ErrPCMConversionFailed, etc.)**: Retries with warn logging

The retry counter starts at 1 (not 0) so log messages read naturally: "attempt 1/3", "attempt 2/3", "attempt 3/3".

### Dependency Injection Pattern
The convertFn field follows the same pattern as pybridge's dependency on exec.Cmd:
- Production code defaults to `codec.MP3ToPCM`
- Tests inject a mock function that returns canned results or errors
- This avoids spawning real ffmpeg subprocesses in tests

### Session Lifecycle
CleanupSession provides a single entry point for session cleanup:
- Delegates to `cache.Cleanup(sessionID)` 
- Logs with component="tts.generator" and session_id
- Idempotent (SessionCache.Cleanup returns nil if directory doesn't exist)
- Phase 7 will call this after dictation session completes

## Files Modified

**Created:**
- `pkg/tts/generator.go` (131 lines) - Generator struct and methods
- `pkg/tts/generator_test.go` (605 lines) - Test suite with fake Executor

**Total:** 2 files created, 736 lines added

## Commits

1. `e941773` - test(06-02): add failing test for TTS batch generator (RED)
2. `5879c1a` - feat(06-02): implement TTS batch Generator with GenerateForSession (GREEN Task 1)
3. `e33255b` - feat(06-02): implement retry-skip logic and CleanupSession (GREEN Task 2)

## Self-Check: PASSED

All verification steps completed successfully:
- [x] All tasks executed (2/2)
- [x] Each task committed individually
- [x] SUMMARY.md created in plan directory
- [x] go test ./pkg/tts/ passes (all 21 tests)
- [x] go vet ./pkg/tts/ clean
- [x] No deviations from plan
- [x] All acceptance criteria met
- [x] Integration points documented
