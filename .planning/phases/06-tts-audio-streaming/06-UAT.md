---
status: testing
phase: 06-tts-audio-streaming
source: [06-VERIFICATION.md]
started: 2026-08-08T12:00:00Z
updated: 2026-08-08T12:00:00Z
---

## Current Test

number: 1
name: End-to-end TTS audio playback test
expected: |
  Connect via test-interface.html, start a session, play a word, and hear audio through speakers with time-to-first-audio < 1.5 seconds
awaiting: user response

## Tests

### 1. End-to-end TTS audio playback test

**Objective:** Verify complete TTS pipeline works in production environment with real external dependencies

**Expected behavior:**
1. Audio plays through browser speakers
2. Time-to-first-audio < 1.5 seconds (typically <100ms for cached audio, <1.5s for fresh generation)
3. No error frames appear during playback
4. Audio sounds natural and intelligible

**Prerequisites:**
- ffmpeg is installed and on PATH (run: `ffmpeg -version`)
- Python venv exists with pregenerate_dictation.py dependencies (run: `python third_party/python/pregenerate_dictation.py --help`)
- VOLCENGINE_TTS_API_KEY, VOLCENGINE_TTS_APP_ID, VOLCENGINE_TTS_ACCESS_KEY environment variables are set
- vocab.db exists with due words (run: `python third_party/python/vocab_manager.py --db ~/.home-tutor-agent/vocab.db due --limit 5`)

**Test steps:**
1. Start the Go service: `cd D:/workspace/ai/xiaotian && go run ./cmd/service/ -config config.yaml`
2. Verify the service boots without error and listens on :8080
3. Open test-interface.html in a browser (Chrome or Safari)
4. Enter a user ID and click "Connect"
5. Verify the WebSocket connection status shows green/OPEN
6. Click "Get Due Words" — verify word list appears in the FSRS results area
7. If the interface has a "Play Word" or audio test button, click it and verify:
   - Audio frames appear in the message log with [AUDIO] prefix
   - The frame counter increments
   - Audio plays through the speakers (may require clicking "Enable Audio" first for Safari autoplay policy)
8. If no audio button exists in the test interface, use the browser console to send a control frame manually:
   - Build a JSON payload: `JSON.stringify({type:"play_word",params:{word_id:"<id_from_step_6>",session_id:"<from_start_session>"}})`
   - Send it as a MsgTypeControl (0x02) binary frame via the WebSocket
9. Verify no error frames appear in the log during audio playback
10. Check the Go service logs for "tts streamer" entries confirming frame delivery
11. Measure time from play_word request to first audio frame (should be <1.5s)

**Result:** pending

---

## Summary

total: 1
passed: 0
issues: 0
pending: 1
skipped: 0
blocked: 0

## Gaps

*No gaps identified - awaiting test results*