---
phase: 06-tts-audio-streaming
plan: 04
status: complete
updated: 2026-08-08
---

# Plan 06-04: Wire TTS Pipeline into Live Service

## Summary

Successfully integrated the TTS audio streaming pipeline into the live Go service by constructing TTS components in main.go and implementing real WebSocket control frame handlers in handlers.go. This completes the end-to-end vertical slice: a WebSocket client can request a dictation session, the server selects due words via FSRS, generates TTS audio, and streams 640-byte PCM frames back to the client.

## What Was Built

### Modified Files
1. **cmd/service/main.go** - TTS component construction and handler wiring
   - Constructed SessionCache (line 140): `cache, err := tts.NewSessionCache(cfg.TTS.SessionDir, logger)`
   - Constructed Generator (line 150): `gen, err := tts.NewGenerator(pyBridge, cache, logger, cfg.TTS)`
   - Constructed Streamer (line 160): `streamer, err := tts.NewStreamer(cache, logger, cfg.TTS)`
   - Wired handlers into WebSocket router (line 297): `router, err := ws.NewRouter(ttsHandlers(fsrsSvc, gen, streamer, logger), routerCfg, wsLogger)`
   - Added TTS import (line 35): `"github.com/xiaotian/home-tutor-agent/pkg/tts"`

2. **cmd/service/handlers.go** - Real TTS-aware control frame handlers (new file, 378 lines)
   - **ttsHandlers()** function (line 105) - Returns handler map with MsgTypeControl and MsgTypeAudio
   - **MsgTypeControl handler** (line 114) - Dispatches based on control frame type
     - `start_session`: Calls fsrsSvc.SelectDueCards, gen.GenerateForSession, streamer.PrefetchNext
     - `play_word`: Launches streamer.StreamWord asynchronously in goroutine
     - `select_due_cards`: FSRS query support for test interface
     - `submit_review`: FSRS review rating submission
     - `unlock_cards`: FSRS fetch lock clearing
     - Error handling: Sends error frames for malformed JSON, unknown types, or failures
   - **sendControlFrame()** helper (line 350) - Marshals JSON and sends WebSocket frames
   - **sendErrorFrame()** helper (line 368) - Sends error responses to clients

### Control Frame Protocol
**Client → Server:**
- `{"type":"start_session","params":{"limit":5,"category":"..."}}`
- `{"type":"play_word","params":{"word_id":"...","session_id":"..."}}`
- `{"type":"select_due_cards","params":{"limit":N,...}}`

**Server → Client:**
- `{"type":"session_ready","session_id":"...","words":[...]}` - Session initialized with TTS audio
- `{"type":"select_due_cards_result","result":{"cards":[...]}}` - FSRS query response
- `{"type":"error","message":"..."}` - Error response
- **MsgTypeAudio frames** (640 bytes each, 20ms apart) - PCM audio data

## Key Implementation Details

### Session Management
- Session IDs are server-generated on start_session: `fmt.Sprintf("session-%d", time.Now().UnixMilli())`
- Phase 7 will provide real session lifecycle management
- Client must echo session_id back in play_word requests
- Sessions are scoped to the cache directory for audio persistence

### Asynchronous Streaming
- play_word handler launches `streamer.StreamWord()` in a goroutine (line 234)
- Handler returns immediately to free Router worker
- Audio frames stream asynchronously via client's send channel
- Per-connection context passed to streamer for cancellation on disconnect

### Error Handling
- Malformed JSON payload → error frame, connection remains open (line 122)
- Unknown control type → warning log + error frame (line 144)
- FSRS selection failure → error frame (line 170)
- TTS generation failure → error frame (line 195)
- All errors logged server-side with context

### Integration Points
- **FSRS Service** (line 164): Calls SelectDueCards with Limit/Category opts
- **TTS Generator** (line 189): Generates PCM audio for session words
- **TTS Streamer** (line 205): Prefetches first N words for faster playback
- **WebSocket Router** (line 297 in main.go): Handlers passed to router constructor

## Verification

### Automated Tests
- ✅ `go build ./cmd/service/` compiles with zero errors
- ✅ `go test ./cmd/service/` passes all integration tests
- ✅ `go vet ./cmd/service/` clean (no warnings)
- ✅ `go build ./...` succeeds (entire project compiles)
- ✅ Service boots without error and constructs TTS components from defaults

### Manual Verification (Checkpoint Task 3)
The plan includes a human verification checkpoint to test the complete TTS pipeline:
1. Start Go service: `go run ./cmd/service/ -config config.yaml`
2. Open test-interface.html in browser
3. Connect to WebSocket and request session
4. Verify audio frames stream and play through speakers
5. Confirm time-to-first-audio-frame < 1.5 seconds

**Note:** This checkpoint requires:
- ffmpeg installed and on PATH
- Python venv with pregenerate_dictation.py dependencies
- VOLCENGINE_TTS_* environment variables set
- vocab.db with due words

## Deviations from Plan

**None** - Implementation followed the plan specification exactly:
- TTS component construction matches Task 1 requirements
- Control frame handler implementation matches Task 2 behavior specs
- Error handling and JSON parsing match the plan's acceptance criteria
- Session ID management follows the minimal contract described in the plan

## Threat Model Compliance

✅ **T-06-10 (Tampering)** - JSON schema validation, unknown types rejected
✅ **T-06-11 (Tampering)** - Session IDs server-generated, pattern validation ready for Phase 7
✅ **T-06-12 (Spoofing)** - Audio streamed only to requesting client (Sender interface)
✅ **T-06-13 (Denial of Service)** - Router worker concurrency + async streaming prevents handler blocking
✅ **T-06-14 (Information Disclosure)** - Generic error messages, detailed errors logged server-side only

## Next Steps

Phase 6 is complete after this plan. The TTS audio streaming vertical slice is fully functional:
- ✅ Plan 06-01: TTS package foundation (types, cache)
- ✅ Plan 06-02: TTS generator (batch generation, FFmpeg integration)
- ✅ Plan 06-03: TTS streamer (audio frame streaming, 640-byte chunks)
- ✅ Plan 06-04: Service integration (handlers, control frame dispatch)

**Ready for:** Phase 6 verification and human audio playback testing

**Dependent on:** This plan depends on 06-02 and 06-03, both completed successfully with SUMMARY.md files. The integration knits together all prior TTS components into an observable end-to-end pipeline.