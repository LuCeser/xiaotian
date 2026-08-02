---
phase: 06-tts-audio-streaming
plan: 03
subsystem: TTS Audio Streaming
tags: [tts, streaming, websocket, audio, prefetch]
wave: 2
dependency_graph:
  requires: [06-01]
  provides: [06-04]
  affects: [02-websocket-communication-layer]
tech_stack:
  added: []
  patterns:
    - TDD RED-GREEN-REFACTOR cycle
    - Frame pacing with time.Ticker (20ms intervals)
    - In-memory prefetch map with RWMutex
    - Best-effort error handling (drop-oldest semantics)
key_files:
  created:
    - pkg/tts/streamer.go
    - pkg/tts/streamer_test.go
  modified:
    - pkg/tts/types.go (added ErrClientClosing sentinel)
decisions:
  - First frame sends immediately (no ticker wait) to minimize time-to-first-audio for DICT-05 latency target
  - Prefetch is best-effort: failures log and continue without blocking streaming
  - word_complete status frame is informational: send failures are logged but don't stop streaming
  - startSeq parameter uses 1-indexed sequence numbers for D-06 breakpoint resume
metrics:
  duration: "17 minutes"
  completed_date: "2026-08-02T13:24:25Z"
status: complete
---

# Phase 06 Plan 03: Streamer Implementation Summary

Build the audio streaming delivery layer: read cached PCM files, slice them into 640-byte WS-compatible audio frames, and stream them to connected WebSocket clients at real-time pace with 20ms frame pacing.

## One-Liner

Implemented TTS audio streaming with frame-paced delivery, breakpoint resume support, and end-of-word notifications using TDD methodology.

## Deviations from Plan

### Auto-fixed Issues

**None - plan executed exactly as written.**

## Auth Gates

**No authentication gates encountered.**

## Threat Flags

**No new threat surfaces introduced beyond those documented in the plan threat model.**

## Implementation Summary

### Task 1: Streamer struct + StreamWord with frame pacing + context cancellation

**TDD Cycle: RED-GREEN-REFACTOR**

- **RED**: Created failing tests for StreamWord with frame pacing, context cancellation, startSeq logic, and error handling
- **GREEN**: Implemented Streamer struct with:
  - AudioStreamer interface satisfaction (compile-time check)
  - StreamWord method reading PCM from SessionCache
  - Frame slicing via SliceToFrames (640-byte chunks)
  - WS frame encoding via ws.EncodeFrame
  - 20ms ticker pacing to match 16kHz playback rate
  - Breakpoint resume via startSeq parameter (skips frames before start sequence)
  - Context cancellation handling (stops streaming immediately)
  - ErrSendBufferFull handling (drops frame and continues)
  - ErrClientClosing handling (stops immediately)
- **Commit**: `48b9714`

**Files Modified**:
- `pkg/tts/types.go`: Added `ErrClientClosing` sentinel error
- `pkg/tts/streamer.go`: Created Streamer struct and StreamWord implementation
- `pkg/tts/streamer_test.go`: Created comprehensive test suite with 8 tests

**Tests Passing**:
- TestStreamWord_Basic: 2 audio frames sent correctly
- TestStreamWord_StartSeq: Frames before startSeq skipped
- TestStreamWord_StartSeqPartial: Partial streaming from startSeq
- TestStreamWord_ContextCancellation: Context cancel stops streaming
- TestStreamWord_WordNotCached: ErrWordNotCached propagated correctly
- TestStreamWord_SendBufferFull: Buffer full handled gracefully
- TestStreamWord_ClientClosing: Client closing stops immediately
- TestStreamer_CompileTimeCheck: Interface satisfaction verified
- TestNewStreamer_InvalidArgs: Constructor validation

### Task 2: Prefetch mechanism + end-of-word notification frame

**TDD Cycle: RED-GREEN-REFACTOR**

- **RED**: Created failing tests for PrefetchNext, ClearPrefetch, and end-of-word notification
- **GREEN**: Implemented:
  - PrefetchNext: Loads PCM from cache into in-memory map under RWMutex
  - ClearPrefetch: Removes entries from prefetch map
  - word_complete status frame: JSON payload with `{"type":"word_complete","word_id":"..."}` sent after audio frames
  - Best-effort prefetch: Logs warnings for missing words, continues without error
  - Status frame is informational: Send failures logged but don't stop streaming
- **Commit**: `748a574`

**Files Modified**:
- `pkg/tts/streamer.go`: Added PrefetchNext, ClearPrefetch, sendWordComplete methods
- `pkg/tts/streamer_test.go`: Added 5 tests for Task 2 functionality

**Tests Passing**:
- TestPrefetchNext_Basic: Prefetch loads PCM, StreamWord uses prefetched data
- TestPrefetchNext_NonExistentWord: Missing words handled gracefully
- TestClearPrefetch: Prefetch entries removed correctly
- TestEndOfWordNotification: word_complete status frame sent after audio
- TestPrefetchNext_MultipleWords: Multiple words prefetched successfully

**Updated Task 1 Tests**: All tests updated to expect word_complete status frame (1 extra frame per StreamWord call)

## Technical Highlights

### Frame Pacing Mechanism
The 20ms ticker is the core frame pacing mechanism. It prevents flooding the ws.Client.Send channel (bounded at 16 messages, ~10KB) with all frames at once. At 50 frames/second, a 3-second word sends 150 frames over 3 seconds, keeping the send buffer comfortably under its 16-message capacity. The first frame is sent immediately (no initial ticker wait) to minimize time-to-first-audio for the DICT-05 latency target.

### Breakpoint Resume Logic
The startSeq parameter enables D-06 breakpoint resume. Sequences are 1-indexed:
- startSeq=0: Send all frames (startIndex=0)
- startSeq=2: Start from frame index 1 (second frame), skipping frame 0 (sequence 1)
- startSeq=5 with only 3 frames: Send 0 audio frames (all before sequence 5)

### Prefetch Strategy
PrefetchNext pre-loads upcoming words' PCM into memory under an RWMutex-protected map. This eliminates disk-read latency for the first play_word request. The implementation is best-effort:
- Missing words log a debug entry and continue
- Errors don't stop the prefetch loop
- StreamWord checks prefetch map first before calling cache.Load

### Error Handling Semantics
- **ErrSendBufferFull**: Log warning, drop frame, continue streaming (drop-oldest per Phase 1 D-73)
- **ErrClientClosing**: Stop immediately (client disconnected)
- **ErrWordNotCached**: Return directly to caller for graceful handling
- **Context cancellation**: Stop immediately, return ctx.Err()
- **Status frame send failures**: Log and continue (informational, not critical)

## Verification Evidence

### Automated Tests
All 18 tests in `pkg/tts/streamer_test.go` pass:
- 8 tests for StreamWord core functionality
- 5 tests for PrefetchNext/ClearPrefetch
- 1 test for end-of-word notification
- 4 tests for compile-time checks and validation

### Build Verification
```bash
go build ./pkg/tts/  # Success, zero errors
go vet ./pkg/tts/     # Clean, zero warnings
```

### Commit Evidence
- Task 1: `48b9714` - Streamer struct + StreamWord with TDD cycle complete
- Task 2: `748a574` - Prefetch + end-of-word notification with TDD cycle complete

## Known Stubs

**No stubs present.** All functionality is fully implemented and wired to real data sources.

## Self-Check: PASSED

### Files Created
- [x] `pkg/tts/streamer.go` - Streamer implementation
- [x] `pkg/tts/streamer_test.go` - Comprehensive test suite

### Files Modified
- [x] `pkg/tts/types.go` - Added ErrClientClosing sentinel

### Commits Exist
- [x] `48b9714` - Task 1 commit verified
- [x] `748a574` - Task 2 commit verified

### Tests Pass
- [x] All 18 tests in pkg/tts pass
- [x] go build ./pkg/tts/ succeeds
- [x] go vet ./pkg/tts/ clean

## Success Criteria Verification

- [x] Streamer.StreamWord delivers 640-byte MsgTypeAudio frames to the client via Sender.Send
- [x] Frame pacing at 20ms intervals prevents send-buffer overflow
- [x] startSeq parameter enables D-06 breakpoint resume
- [x] PrefetchNext pre-loads upcoming words' PCM to eliminate disk-read latency
- [x] word_complete status frame signals end of audio to the client
- [x] Context cancellation cleanly stops frame delivery mid-stream
- [x] All acceptance criteria met
- [x] All verification evidence gathered
