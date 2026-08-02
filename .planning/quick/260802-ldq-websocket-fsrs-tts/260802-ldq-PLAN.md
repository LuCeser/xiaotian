---
phase: quick
plan: 260802-ldq
type: execute
wave: 1
depends_on: []
files_modified:
  - test-interface.html
autonomous: false
requirements:
  - WS-CONN
  - FSRS-TEST
  - TTS-TEST
user_setup:
  - service: go-websocket
    why: "Backend WebSocket service must be running for interface testing"
    env_vars:
      - name: SERVER_PORT
        value: "8080"
    dashboard_config:
      - task: "Start Go WebSocket server"
        location: "Run `go run cmd/server/main.go` from project root"

must_haves:
  truths:
    - User can open test-interface.html directly in a browser and see a functional interface
    - WebSocket connection successfully establishes to ws://localhost:8080/ws with visible status indication
    - FSRS "Get Due Words" button sends control frame and displays returned word data in the log area
    - TTS audio streaming receives binary frames and plays audio through Web Audio API
    - All WebSocket messages (control, audio, errors) are logged with timestamps and sequence numbers
  artifacts:
    - test-interface.html — Single-file HTML/CSS/JavaScript interface with WebSocket binary frame handling
  key_links:
    - Browser → WebSocket server: Connection must use `ws://localhost:8080/ws?user_id=test&device=web` URL pattern
    - WebSocket binary frames → Frame parser: Must set `ws.binaryType = 'arraybuffer'` before connecting
    - Frame parser → Audio handler: 16-byte header parsing must match `pkg/ws/protocol.go` exactly (big-endian)
    - Audio handler → Web Audio API: PCM conversion must handle 16-bit signed integer to float32 normalization
---

## Objective

Create a minimal, single-file HTML interface that validates the completed backend infrastructure (Phases 1-5) by testing WebSocket connection, FSRS word selection, and TTS audio streaming end-to-end.

**Purpose:** Provide immediate confidence that the WebSocket→FSRS→TTS pipeline works correctly before building the full frontend, with a debuggable interface for troubleshooting.

**Output:** A standalone `test-interface.html` file that can be opened directly in any modern browser (Chrome/Safari 14+) with no build step or external dependencies.

## Context

@.planning/STATE.md
@.planning/quick/260802-ldq-websocket-fsrs-tts/260802-ldq-CONTEXT.md
@.planning/quick/260802-ldq-websocket-fsrs-tts/260802-ldq-RESEARCH.md

## Tasks

### Task 1: Create HTML interface structure with WebSocket connection

**Files:** `test-interface.html`

**Action:**

Create a single-file HTML interface with the following structure (all CSS/JavaScript inline):

**HTML structure:**
- Vertical layout with four card sections: WebSocket Status, FSRS Test, TTS Audio Test, and Message Log
- WebSocket Status card: Status indicator (green/yellow/red), connection button (Connect/Disconnect), user_id input field (default "test")
- FSRS Test card: "Get Due Words" button, word count input (default 20), results display area (show word/meaning/example)
- TTS Audio Test card: "Play Test Audio" button, playback status indicator, audio frame counter (show total frames received)
- Message Log card: Scrollable log area with timestamped entries, color-coded by message type (control=blue, audio=green, error=red, status=gray)

**CSS styling (embedded in `<style>`):**
- Use clean, readable sans-serif font (system-ui, -apple-system, or Segoe UI)
- Card-based design with white background, subtle shadow, and rounded corners (8px)
- Status colors: green (#10b981) for connected, yellow (#f59e0b) for connecting, red (#ef4444) for error/disconnected
- Buttons: 16px padding, rounded corners, hover state with darker background
- Log area: monospace font (Consolas/Monaco), 300px min-height, overflow-y auto, light gray background (#f9fafb)
- Responsive: Works on iPad viewport (1024px width) with reasonable margins

**JavaScript - WebSocket connection:**
- Set `ws.binaryType = 'arraybuffer'` before connecting (CRITICAL for binary frames)
- Connect to `ws://localhost:8080/ws?user_id={user_id}&device=web` where user_id comes from input field
- Track connection state explicitly (CONNECTING → OPEN → CLOSED → ERROR)
- Update status indicator on every state change (color + text label)
- Implement exponential backoff reconnection (max 3 retries, 1s→2s→4s delays) after user clicks Connect
- Log all WebSocket events (open, message, error, close) to the log area with timestamps

**Verification:**
```bash
# Manual verification (browser-based)
# 1. Open test-interface.html in Chrome/Safari
# 2. Click "Connect" button
# 3. Verify status indicator turns green with "Connected" text
# 4. Check log area shows "WebSocket connected" timestamp entry
# 5. Click "Disconnect" and verify status turns red with "Disconnected" text
# 6. Check browser DevTools Network tab confirms WebSocket connection to ws://localhost:8080/ws
```

**Done:**
- Status indicator shows correct state (CONNECTING=yellow, OPEN=green, CLOSED/ERROR=red)
- Log area displays all WebSocket lifecycle events with readable timestamps
- Connect/Disconnect buttons toggle visibility based on current state
- Interface loads and functions correctly in Chrome and Safari browsers without console errors

---

### Task 2: Implement binary frame parser and FSRS word selection test

**Files:** `test-interface.html` (extend Task 1)

**Action:**

**Binary frame parser (per CONTEXT.md locked decision and RESEARCH.md findings):**

Implement `parseFrameHeader(arrayBuffer)` function that decodes the 16-byte header exactly as specified in `pkg/ws/protocol.go`:
- Use `DataView` API with explicit big-endian parameter (`false` for all getUint*/getInt* calls)
- Header structure: `version (uint8)`, `msgType (uint8)`, `sequence (uint32)`, `timestamp (uint32)`, `payloadLen (uint16)`, `reserved (uint16)`, `crc16 (uint16)`
- Return object with all header fields for logging and routing

Message type constants (from `pkg/ws/protocol.go`):
```javascript
const MsgType = {
  Audio: 0x01,
  Control: 0x02,
  Error: 0x03,
  Heartbeat: 0x04,
  Session: 0x05,
  Status: 0x06
};
```

Frame routing in `ws.onmessage` handler:
- Extract header + payload using `parseFrameHeader()`
- Log every frame: `[MsgType] Seq={sequence} Len={payloadLen}` in the log area
- Route to handlers based on `msgType` field

**FSRS word selection request (Control frame):**

Implement `requestDueCards()` function:
- Build JSON payload: `{type: 'select_due_cards', params: {limit: <from_input>, category: '', units: [], fetch_lock_hours: 0.5}}`
- Encode header with `MsgType.Control`, incrementing sequence counter, current timestamp
- Encode frame: concatenate 16-byte header + UTF-8 encoded payload (no CRC computation for this validation interface — set crc16=0)
- Send via `ws.send(encodedFrame)`

**FSRS response handler:**

Implement `handleControlFrame(payload)`:
- Decode `TextDecoder.decode(payload)` to get JSON string
- Parse JSON and display in FSRS results area
- Show word count and array of words (each with id, word, meaning, example)
- Handle empty results: Display "No due words found" message
- Log to message log area: `[CONTROL] Received {count} words`

Bind "Get Due Words" button to `requestDueCards()`:
- Only enable when WebSocket state is OPEN (disable button otherwise)
- Show loading state (button text="Loading...") while waiting for response
- Handle timeout: 5-second timer with error log if no response

**Verification:**
```bash
# Manual verification (browser-based)
# 1. Connect WebSocket (Task 1 verification complete)
# 2. Click "Get Due Words" button
# 3. Verify log area shows "[CONTROL] Sent select_due_cards request"
# 4. Check that results area displays word data or "No due words found"
# 5. Verify log shows "[CONTROL] Received X words" entry
# 6. Open browser DevTools Console and verify no JSON parse errors
```

**Done:**
- Control frame sent to backend results in FSRS word selection call
- Backend response is displayed in the results area with word/meaning/example fields
- Log area shows both sent request and received response with correct message types
- Binary frame parser correctly extracts sequence numbers and payload lengths (log validation)
- Empty results case (no due words) displays appropriate message

---

### Task 3: Implement TTS audio streaming with Web Audio API

**Files:** `test-interface.html` (extend Task 2)

**Action:**

**Web Audio API initialization:**

Create global `audioContext` instance:
- Initialize on first user interaction (click "Start Testing" or "Play Test Audio")
- Handle Safari autoplay policy: Show a one-time "Click to enable audio" prompt if needed
- Sample rate: 16000 Hz (from protocol spec in RESEARCH.md)

**Audio frame handler:**

Implement `handleAudioFrame(payload)` function:
- Validate payload size: Expect 640 bytes (320 samples @ 16-bit mono)
- Use `DataView` to read 16-bit signed PCM samples (big-endian: `getInt16(offset, false)`)
- Convert to float32 normalized to [-1, 1]: `sample / 32768.0`
- Create `AudioBuffer` with 1 channel, 320 samples, 16000 Hz sample rate
- Fill channel data array with converted samples
- Schedule playback using `AudioBufferSourceNode` → `audioContext.destination`
- Track frame sequence numbers: Log gaps or duplicates in the log area as warnings

**Audio playback state tracking:**
- Frame counter: Display total frames received in the TTS Test card
- Playback status: Show "Playing", "Idle", or "Buffering" based on audio context state
- Detect underruns: Log warnings if sequence gap > 1 (missing frames)

**Test audio button:**

Implement "Play Test Audio" functionality:
- Send a control frame requesting TTS generation (assume backend has a test endpoint)
- For this validation: If backend doesn't have a test TTS endpoint, the button plays a synthesized test tone (440Hz sine wave, 1 second) using Web Audio API to verify audio path works
- Show playback status and frame counter updates
- Log audio events: "[AUDIO] Frame {seq} played", "[AUDIO] Playback started"

**Sequence number tracking:**
- Maintain array of received sequence numbers
- Detect gaps: If `current_seq > last_seq + 1`, log warning `[AUDIO] Gap detected: missing {last_seq+1} to {current_seq-1}`
- Detect duplicates: If sequence already seen, log warning `[AUDIO] Duplicate frame {seq}`

**Error handling:**
- Catch Web Audio API errors (e.g., sample rate mismatch, buffer size errors)
- Log to message log area with red color
- Show error message in TTS status indicator

**Verification:**
```bash
# Manual verification (browser-based)
# 1. Complete Task 1 and Task 2 verification (WebSocket connected, FSRS working)
# 2. Click "Play Test Audio" button
# 3. Verify browser prompts for audio permission (first time only)
# 4. Grant permission and check that status changes to "Playing"
# 5. Verify frame counter increments as audio frames arrive
# 6. Listen for audio output (test tone or real TTS audio)
# 7. Check log area for "[AUDIO] Frame X played" entries
# 8. Verify no red error messages in log area
# 9. Test on Safari: Verify audio plays after first user interaction
```

**Done:**
- Web Audio API initializes correctly on first user interaction (Safari autoplay policy handled)
- Binary audio frames are decoded from 16-bit PCM and played through browser speakers
- Frame counter displays total audio frames received
- Sequence number tracking detects and logs gaps or duplicates
- Audio playback is audible in both Chrome and Safari browsers
- Log area shows all audio-related events with appropriate color coding
- No Web Audio API errors appear in browser console

---

## Threat Model

### Trust Boundaries

| Boundary | Description |
|----------|-------------|
| Browser → Backend (WebSocket) | Untrusted browser sends control frames that trigger backend operations (FSRS queries, TTS generation) |
| Backend → Browser (binary frames) | Browser receives untrusted binary data that must be validated before parsing (frame length, message types) |

### STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-quick-01 | Tampering | WebSocket frame parser | low | accept | Validation interface only — no persistent state changes. Corrupted frames cause parse errors logged to console, no security impact. |
| T-quick-02 | Information disclosure | Message log area | low | accept | Log displays in user's own browser session only. No sensitive data beyond test words and frames. No external transmission. |
| T-quick-03 | Denial of service | Audio frame playback | low | accept | Local browser only — excessive frames only affect user's own browser tab. User can close tab to stop. |
| T-quick-04 | Tampering | FSRS request parameters | low | accept | Validation interface uses fixed/harmless parameters (limit=20, no destructive operations). Backend FSRS service is read-only for this endpoint. |

## Verification

**Overall checks:**
- [ ] Interface loads without errors in Chrome and Safari browsers
- [ ] WebSocket connection establishes successfully on port 8080
- [ ] Binary frame parser correctly decodes 16-byte headers with big-endian fields
- [ ] FSRS "Get Due Words" sends control frame and displays results
- [ ] TTS audio streaming plays through Web Audio API
- [ ] All message types (control, audio, error, status) are logged with timestamps
- [ ] Safari autoplay policy is handled (audio requires user interaction)
- [ ] Sequence number tracking detects gaps and duplicates
- [ ] Interface works on iPad viewport (responsive layout)

**Success criteria:**
- User can open `test-interface.html` and verify WebSocket→FSRS→TTS pipeline end-to-end within 5 minutes
- All three test areas (WebSocket, FSRS, TTS) show green success indicators when working correctly
- Log area provides sufficient debugging information to identify connection or parsing issues
- Interface functions correctly on both Chrome and Safari browsers without build tools or external dependencies

## Output

Create `test-interface.html` when done. No commit required (quick task outside main milestone workflow).
