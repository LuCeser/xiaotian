# Quick Task 260802-ldq: Minimal Frontend Validation Interface - Research

**Researched:** 2026-08-02
**Domain:** Web frontend (HTML/CSS/JavaScript) for WebSocket/FSRS/TTS validation
**Confidence:** MEDIUM

## Summary

This research covers the minimal frontend implementation needed to validate the completed backend infrastructure (Phases 1-5). The focus is on practical, browser-native WebSocket debugging, HTML5 Audio API for streaming binary PCM audio, common JavaScript pitfalls, and simple logging UI patterns - all within a single HTML file that can be opened directly in a browser.

**Primary recommendation:** Use a single HTML file with vanilla JavaScript leveraging browser-native WebSocket and Web Audio APIs. No build step or framework required.

## User Constraints (from CONTEXT.md)

### Locked Decisions
- **Frontend technology:** Pure HTML/CSS/JavaScript (zero build overhead, direct browser testing)
- **Interface complexity:** Minimal interface focused on core functionality testing
- **Test scope:** All core backend functions - WebSocket connection (🔴 highest priority), FSRS word selection (🟡 medium), TTS audio streaming (🟢 validation priority)
- **Data source:** Real database with test data (10-20 words)

### Claude's Discretion
- Interface layout and visual design (prioritize clarity and usability)
- Log area detail level (balance debugging needs with readability)
- Test word data organization and content (ensure common scenario coverage)

### Deferred Ideas
None - all requirements captured in implementation decisions.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| WebSocket connection management | Browser / Client | — | Native WebSocket API handles connection lifecycle in browser |
| Binary audio frame decoding | Browser / Client | — | JavaScript receives ArrayBuffer frames and decodes 16-byte headers |
| PCM audio playback | Browser / Client | — | Web Audio API converts binary PCM to audio output |
| FSRS request/response | API / Backend | — | Frontend sends control frames, backend processes via Python |
| Session state tracking | Browser / Client | — | Local UI state reflects WebSocket connection status |

## Standard Stack

### Core
| Technology | Version | Purpose | Why Standard |
|------------|---------|---------|--------------|
| Native WebSocket API | ES6+ | WebSocket connection | Browser-native, no dependencies required [ASSUMED] |
| Web Audio API | ES6+ | PCM audio playback | Optimized for real-time audio streaming [ASSUMED] |
| FileReader API | ES6+ | Binary data handling | Required for ArrayBuffer operations [ASSUMED] |

### Supporting
| Technology | Version | Purpose | When to Use |
|------------|---------|---------|-------------|
| DataView API | ES6+ | Binary frame parsing | Decode 16-byte headers with big-endian fields [ASSUMED] |
| Console API | All | Debug logging | Use console.table for structured log display [ASSUMED] |

**Installation:** None required - browser-native APIs only.

**Version verification:** N/A - browser built-in APIs.

## Package Legitimacy Audit

Not applicable - no external packages to install. Pure browser-native JavaScript.

## Architecture Patterns

### System Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                         Browser Frontend                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  ┌──────────────┐  WebSocket frames  ┌──────────────────────┐  │
│  │ WebSocket    │ ──────────────────> │ Binary Frame Parser  │  │
│  │ Connection   │ <────────────────── │ (16-byte header)     │  │
│  └──────────────┘                     └──────────────────────┘  │
│         │                                          │             │
│         │ control frames                          │ audio       │
│         ▼                                          ▼             │
│  ┌──────────────┐                         ┌──────────────┐    │
│  │ FSRS Control │                         │ Web Audio API │    │
│  │ (send/select)│                         │ (PCM playback)│    │
│  └──────────────┘                         └──────────────┘    │
│         │                                          │             │
│         ▼                                          ▼             │
│  ┌──────────────┐                         ┌──────────────┐    │
│  │ Log Display  │                         │ Audio Player │    │
│  │ (console)    │                         │ (status)     │    │
│  └──────────────┘                         └──────────────┘    │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
         │                                    ▲
         │ WebSocket (ws://localhost:8080/ws) │
         │                                    │
┌─────────────────────────────────────────────────────────────────┐
│                    Backend Go WebSocket Service                   │
├─────────────────────────────────────────────────────────────────┤
│  pkg/ws.Server → pkg/ws.Hub → pkg/ws.Client → FrameHandler     │
│                                                                   │
│  Routes:                                                          │
│  - Control frames → FSRS Service → Python vocab_manager          │
│  - Audio frames → PCM binary payload (640 bytes @ 16kHz)         │
└─────────────────────────────────────────────────────────────────┘
```

### Recommended Project Structure

```
test-interface.html  # Single-file interface (all HTML/CSS/JS inline)
```

### Pattern 1: WebSocket Binary Frame Handling
**What:** Receive and decode WebSocket binary frames with 16-byte headers
**When to use:** All WebSocket communication with the Go backend
**Example:**
```javascript
// VERIFIED: Based on pkg/ws/protocol.go implementation
const HeaderSize = 16;
const AudioFrameSize = 640;

// Message types from pkg/ws/protocol.go
const MsgType = {
  Audio: 0x01,
  Control: 0x02,
  Error: 0x03,
  Heartbeat: 0x04,
  Session: 0x05,
  Status: 0x06
};

function parseFrameHeader(arrayBuffer) {
  const view = new DataView(arrayBuffer);
  return {
    version: view.getUint8(0),
    msgType: view.getUint8(1),
    sequence: view.getUint32(2, false), // big-endian
    timestamp: view.getUint32(6, false), // big-endian
    payloadLen: view.getUint16(10, false), // big-endian
    reserved: view.getUint16(12, false), // big-endian
    crc16: view.getUint16(14, false) // big-endian
  };
}

ws.onmessage = (event) => {
  const data = new Uint8Array(event.data);
  if (data.length < HeaderSize) {
    console.error('Frame too short:', data.length);
    return;
  }

  const header = parseFrameHeader(data.slice(0, HeaderSize));
  const payload = data.slice(HeaderSize);

  switch (header.msgType) {
    case MsgType.Audio:
      handleAudioFrame(payload);
      break;
    case MsgType.Control:
      handleControlFrame(payload);
      break;
    // ... handle other types
  }
};
```

### Pattern 2: Web Audio API for PCM Streaming
**What:** Convert binary PCM data to audio playback
**When to use:** Playing 640-byte audio frames from WebSocket
**Example:**
```javascript
// ASSUMED: Standard Web Audio API pattern
const audioContext = new (window.AudioContext || window.webkitAudioContext)();
const sampleRate = 16000; // 16kHz PCM from protocol spec

function handleAudioFrame(pcmData) {
  const audioBuffer = audioContext.createBuffer(
    1, // mono channel
    AudioFrameSize / 2, // 640 bytes = 320 samples @ 16-bit
    sampleRate
  );

  const channelData = audioBuffer.getChannelData(0);
  const view = new DataView(pcmData.buffer);

  // Convert 16-bit PCM to float32 [-1, 1]
  for (let i = 0; i < AudioFrameSize / 2; i++) {
    const sample = view.getInt16(i * 2, false); // big-endian
    channelData[i] = sample / 32768.0;
  }

  const source = audioContext.createBufferSource();
  source.buffer = audioBuffer;
  source.connect(audioContext.destination);
  source.start();
}
```

### Anti-Patterns to Avoid
- **Blocking the main thread with large audio buffers:** Process audio frames in chunks, not one large buffer
- **Not handling Safari's stricter WebSocket policies:** Safari requires explicit user interaction before audio playback
- **Silently dropping frames:** Log every frame type and sequence number for debugging
- **Assuming all WebSocket messages are binary:** Check `event.data` type (Blob vs ArrayBuffer)

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Binary data parsing | Custom byte-by-byte parsing | DataView API | Handles endianness, alignment, and type conversion correctly [ASSUMED] |
| Audio playback scheduling | setTimeout-based sequencing | Web Audio API scheduling | Precise timing, no drift, handles device differences [ASSUMED] |
| WebSocket reconnection | Custom retry logic | Exponential backoff with max retries | Handles network instability gracefully [ASSUMED] |
| Frame buffering | Array-based queue | AudioContext's built-in scheduling | Designed for real-time audio streaming [ASSUMED] |

**Key insight:** The Web Audio API and DataView API are battle-tested solutions for exactly this use case. Custom implementations drift over time and fail edge cases.

## Common Pitfalls

### Pitfall 1: Safari Audio Autoplay Policy
**What goes wrong:** Audio playback is blocked until user interaction
**Why it happens:** Safari requires explicit user gesture before audio context can start
**How to avoid:** Add a "Start Testing" button that initializes the audio context on first click
**Warning signs:** No audio plays, console shows "The AudioContext was not allowed to start"

### Pitfall 2: WebSocket Binary Frame Handling
**What goes wrong:** Treating binary frames as text, or incorrect byte order
**Why it happens:** Confusion between `ArrayBuffer`, `Blob`, and string data types
**How to avoid:** Always check `event.data` type, use `DataView` with explicit endianness (false = big-endian for this protocol)
**Warning signs:** Garbled audio data, parse errors, incorrect sequence numbers

### Pitfall 3: Ignoring Sequence Numbers
**What goes wrong:** Audio frames played out of order, gaps in sequence
**Why it happens:** Not tracking the sequence field from the frame header
**How to avoid:** Log every frame's sequence number, alert on gaps or duplicates
**Warning signs:** Audio glitches, stuttering playback

### Pitfall 4: Not Handling Connection State
**What goes wrong:** UI shows "connected" when WebSocket is actually closed
**Why it happens:** Relying only on the `open` event, not handling `close` or `error`
**How to avoid:** Track WebSocket state explicitly (CONNECTING, OPEN, CLOSED, ERROR), update UI on every state change
**Warning signs:** Commands fail silently, no error feedback

## Code Examples

### WebSocket Connection with Error Handling
```javascript
// ASSUMED: Standard WebSocket error handling pattern
const ws = new WebSocket('ws://localhost:8080/ws?user_id=test&device=web');

ws.binaryType = 'arraybuffer'; // CRITICAL: Must be set for binary frames

const state = {
  status: 'CONNECTING',
  lastError: null,
  sequenceNumbers: []
};

ws.onopen = () => {
  state.status = 'OPEN';
  updateStatus('connected', 'success');
  console.log('WebSocket connected');
};

ws.onerror = (error) => {
  state.status = 'ERROR';
  state.lastError = error;
  updateStatus('connection error', 'error');
  console.error('WebSocket error:', error);
};

ws.onclose = (event) => {
  state.status = 'CLOSED';
  updateStatus('disconnected', 'warning');
  console.log('WebSocket closed:', event.code, event.reason);
};

function updateStatus(message, level) {
  const statusEl = document.getElementById('status');
  statusEl.textContent = message;
  statusEl.className = level; // 'success', 'error', 'warning'
}
```

### FSRS Word Selection Request
```javascript
// ASSUMED: Based on pkg/fsrs/service.go SelectDueCards interface
function requestDueCards() {
  const payload = JSON.stringify({
    type: 'select_due_cards',
    params: {
      limit: 20,
      category: '',
      units: [],
      fetch_lock_hours: 0.5
    }
  });

  // VERIFIED: Must use MsgType.Control from pkg/ws/protocol.go
  const header = {
    version: 0x01,
    msgType: MsgType.Control,
    sequence: nextSequence(),
    timestamp: Date.now() / 1000,
    payloadLen: payload.length,
    reserved: 0,
    crc16: 0 // TODO: Compute CRC-16-CCITT
  };

  const frame = encodeFrame(header, new TextEncoder().encode(payload));
  ws.send(frame);
}

let sequenceCounter = 0;
function nextSequence() {
  return ++sequenceCounter;
}
```

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Modern browser with WebSocket | All features | ✓ | Chrome 90+, Safari 14+ | — |
| Web Audio API | Audio playback | ✓ | Chrome 90+, Safari 14+ | — |
| DataView API | Binary parsing | ✓ | All modern browsers | — |

**Missing dependencies with no fallback:** None

**Missing dependencies with fallback:** None

## Validation Architecture

This is a quick validation task, not a full phase. Test infrastructure is not applicable. The interface itself is the validation tool.

## Sources

### Primary (HIGH confidence)
- `pkg/ws/hub.go` - Hub connection management implementation [VERIFIED: codebase read]
- `pkg/ws/server.go` - Server configuration and WebSocket upgrade handling [VERIFIED: codebase read]
- `pkg/ws/protocol.go` - Binary frame protocol specification (16-byte header, message types) [VERIFIED: codebase read]
- `pkg/ws/codec.go` - Frame encoding/decoding and CRC validation [VERIFIED: codebase read]
- `pkg/ws/connection.go` - Client connection lifecycle and I/O pumps [VERIFIED: codebase read]
- `pkg/fsrs/service.go` - FSRS service interface and SelectDueCards implementation [VERIFIED: codebase read]

### Secondary (MEDIUM confidence)
- Web Audio API standard patterns [ASSUMED: training knowledge]
- WebSocket binary data handling [ASSUMED: training knowledge]
- DataView API for binary parsing [ASSUMED: training knowledge]

### Tertiary (LOW confidence)
- Safari-specific debugging features [marked for validation]

## Metadata

**Confidence breakdown:**
- Standard stack: LOW - browser APIs based on training knowledge (network restrictions prevented official doc verification)
- Architecture: HIGH - based on verified codebase implementation
- Pitfalls: MEDIUM - common web development patterns, Safari-specific needs user validation

**Research date:** 2026-08-02
**Valid until:** 30 days (browser APIs are stable)

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Web Audio API `createBuffer` and PCM conversion pattern | Pattern 2 | May not work on older Safari versions - test on target iPad |
| A2 | Safari Web Inspector WebSocket debugging workflow | Pitfall 1 section | Debugging workflow may differ - user should verify on actual device |
| A3 | DataView API big-endian parameter (false = big-endian) | Pattern 1 | Byte order could be reversed if assumption incorrect - test with known sequence number |
| A4 | WebSocket `binaryType = 'arraybuffer'` is sufficient for protocol | Code Examples | May need Blob handling on older browsers - unlikely for modern iPad |

**If this table is empty:** Not empty - 4 assumptions tagged, primarily around browser API specifics that need on-device verification.

## Open Questions

1. **Safari 14+ Web Audio API timestamp handling**
   - What we know: Web Audio API is supported, but Safari has stricter autoplay policies
   - What's unclear: Whether `audioContext.createBuffer` works identically to Chrome for 16kHz mono PCM
   - Recommendation: Test the provided code pattern on actual iPad device before full implementation

2. **CRC-16-CCITT computation in JavaScript**
   - What we know: Go backend uses CRC-16-CCITT (Polynomial 0x1021, Initial 0xFFFF) from `pkg/crc16/crc16.go`
   - What's unclear: Whether there's a standard JavaScript library or need to port the Go implementation
   - Recommendation: Port the Go CRC-16 implementation to JavaScript for this validation interface (it's only ~20 lines of bit operations)
