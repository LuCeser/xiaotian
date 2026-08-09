# Phase 8: Student Web Interface - Research

**Researched:** 2026-08-09
**Domain:** Frontend web development with WebSocket real-time communication
**Confidence:** MEDIUM

## Summary

Phase 8 requires building a responsive, iPad-optimized student interface that integrates with the existing Go WebSocket backend for real-time dictation sessions. The interface must be resource-efficient, handle WebSocket reconnections gracefully, and provide an accessible experience for elementary school students.

Based on the project's constraints (1C 1G VPS, strict memory limits, iPad Safari target) and the requirement for a simple student interface, **vanilla JavaScript with a minimal component approach** is recommended over heavy frameworks. This aligns with the project philosophy of fast feedback cycles and local debugging capabilities.

**Primary recommendation:** Use vanilla JavaScript with modern ES6+ features, CSS Grid/Flexbox for responsive layouts, and a simple state management pattern. Avoid framework overhead; focus on WebSocket reliability and kid-friendly UI/UX.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Session request handling | Frontend Server (static file serving) | Browser | Static HTML/JS served from Go backend; user interaction in browser |
| Real-time progress display | Browser | API/WebSocket | UI updates happen client-side via WebSocket messages |
| WebSocket connection management | Browser | Frontend Server | Client-side connection handling with server coordination |
| Responsive layout rendering | Browser | — | All layout logic runs in iPad Safari browser |
| State management (session progress) | Browser | — | Client-side tracking of session state and UI updates |
| Audio playback control | Browser | — | Web Audio API for gapless PCM audio playback |

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| **Vanilla JavaScript (ES6+)** | — | Core logic without framework overhead | Zero dependencies, fastest load time, full Safari compatibility [ASSUMED] |
| **WebSocket API** | — | Real-time communication with Go backend | Native browser API, no external libraries needed [ASSUMED] |
| **Web Audio API** | — | Gapless audio playback for PCM streams | Native browser API, handles binary audio data efficiently [ASSUMED] |
| **CSS Grid + Flexbox** | — | Responsive iPad-optimized layouts | Modern CSS, excellent Safari support, no JS overhead [ASSUMED] |

### Supporting (Minimal)
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| **None recommended** | — | — | Keep it simple for MVP; add libraries only if needed |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Vanilla JS | Preact (~3KB) | Preact adds React-like patterns but increases bundle size and complexity for simple UI [ASSUMED] |
| Vanilla JS | Alpine.js (~10KB) | Alpine adds reactivity but isn't needed for this simple interface [ASSUMED] |
| Vanilla JS | Svelte (compiled) | Svelte compiles to vanilla JS but adds build tool complexity [ASSUMED] |

**Installation:**
```bash
# No external dependencies needed for vanilla JavaScript approach
# If adding any libraries, use npm and verify versions:
npm view <package> version
```

**Version verification:** No external packages recommended for MVP approach. If libraries are added later, verify current versions against npm registry.

## Package Legitimacy Audit

> **No external packages required** for the recommended vanilla JavaScript approach. This audit section will be populated if the planner decides to add libraries.

| Package | Registry | Age | Downloads | Source Repo | Verdict | Disposition |
|---------|----------|-----|-----------|-------------|---------|-------------|
| N/A | N/A | N/A | N/A | N/A | N/A | No packages needed for MVP |

**Packages removed due to [SLOP] verdict:** None (no packages proposed)
**Packages flagged as suspicious [SUS]:** None (no packages proposed)

## Architecture Patterns

### System Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                     iPad Safari Browser                      │
│                                                               │
│  ┌──────────────────┐         ┌─────────────────────┐      │
│  │  Static HTML/CSS  │         │  WebSocket Client   │      │
│  │  + Vanilla JS     │◄────────┤  (Binary Protocol)  │◄─────┼─────┐
│  └──────────────────┘         └─────────────────────┘      │     │
│            ▲                           │                     │     │
│            │                           ▼                     │     │
│  ┌──────────────────┐         ┌─────────────────────┐      │     │
│  │  UI Components   │         │  Audio Controller   │      │     │
│  │  - Session Btn   │         │  (Web Audio API)    │      │     │
│  │  - Progress Bar  │         │  - PCM Playback     │      │     │
│  │  - Status Display│         │  - Gapless Scheduling│     │     │
│  └──────────────────┘         └─────────────────────┘      │     │
│            ▲                                                   │     │
│            │                                                   │     │
│  ┌──────────────────┐         ┌─────────────────────┐      │     │
│  │ State Manager    │         │  Reconnection Logic  │      │     │
│  │ - Session State  │         │  - Exponential Backoff│    │     │
│  │ - Progress Tracking│     │  - Session Recovery   │     │     │
│  └──────────────────┘         └─────────────────────┘      │     │
└─────────────────────────────────────────────────────────────┘     │
                                                                   │
┌──────────────────────────────────────────────────────────────┐│
│                   Go Backend (VPS)                             ││
│                                                               ││
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐  ││
│  │ HTTP Server  │────│WebSocket Hub │────│Session Manager│  ││
│  │ (Static Files)│    │(Binary Frame)│    │(FSM States)   │  ││
│  └──────────────┘    └──────────────┘    └──────────────┘  ││
│                                                               ││
└───────────────────────────────────────────────────────────────┘
```

### Recommended Project Structure
```
pkg/web/
├── static/                      # Static files served by Go backend
│   ├── index.html               # Main student interface
│   ├── styles/
│   │   ├── main.css              # Main stylesheet
│   │   └── responsive.css       # iPad-specific responsive rules
│   └── js/
│       ├── app.js               # Main application entry point
│       ├── websocket-client.js  # WebSocket connection management
│       ├── audio-controller.js   # Web Audio API for playback
│       ├── ui-components.js      # Session button, progress display
│       └── state-manager.js      # Session state and progress tracking
└── templates/                   # If server-side rendering needed
    └── student.html             # Template for student interface
```

### Pattern 1: Simple State Management for Session Progress
**What:** A lightweight state management pattern using a centralized state object and subscriber pattern for UI updates.
**When to use:** Real-time progress tracking with multiple UI components needing updates.
**Example:**
```javascript
// Source: [ASSUMED - standard vanilla JS pattern]
class SessionStateManager {
  constructor() {
    this.state = {
      sessionActive: false,
      currentWord: 0,
      totalWords: 0,
      completedWords: 0,
      status: 'IDLE', // IDLE, LISTENING, DICTATING, WAITING_PARENT, FEEDBACK
      connectionStatus: 'DISCONNECTED'
    };
    this.subscribers = [];
  }

  // Update state and notify subscribers
  setState(updates) {
    this.state = { ...this.state, ...updates };
    this.notify();
  }

  // Subscribe to state changes
  subscribe(callback) {
    this.subscribers.push(callback);
    // Return unsubscribe function
    return () => {
      this.subscribers = this.subscribers.filter(cb => cb !== callback);
    };
  }

  // Notify all subscribers of state change
  notify() {
    this.subscribers.forEach(callback => callback(this.state));
  }

  // Get current state
  getState() {
    return { ...this.state };
  }
}

// Usage in main application
const stateManager = new SessionStateManager();

// UI components subscribe to updates
stateManager.subscribe((state) => {
  updateProgressBar(state);
  updateStatusDisplay(state);
});
```

### Pattern 2: WebSocket Client with Reconnection Logic
**What:** A robust WebSocket wrapper with automatic reconnection, exponential backoff, and session recovery.
**When to use:** WebSocket connections that need to survive temporary network issues.
**Example:**
```javascript
// Source: [ASSUMED - standard WebSocket pattern]
class WebSocketClient {
  constructor(url, audioController, stateManager) {
    this.url = url;
    this.ws = null;
    this.reconnectAttempts = 0;
    this.maxReconnectAttempts = 10;
    this.reconnectDelay = 1000; // Start with 1 second
    this.audioController = audioController;
    this.stateManager = stateManager;
    this.messageQueue = []; // Queue messages while disconnected
  }

  connect() {
    this.ws = new WebSocket(this.url);
    this.ws.binaryType = 'arraybuffer'; // Important for binary audio frames

    this.ws.onopen = () => {
      console.log('WebSocket connected');
      this.reconnectAttempts = 0;
      this.reconnectDelay = 1000;
      this.stateManager.setState({ connectionStatus: 'CONNECTED' });
      
      // Send any queued messages
      while (this.messageQueue.length > 0) {
        const msg = this.messageQueue.shift();
        this.send(msg);
      }
    };

    this.ws.onmessage = (event) => {
      this.handleMessage(event.data);
    };

    this.ws.onclose = (event) => {
      console.log('WebSocket disconnected:', event.code, event.reason);
      this.stateManager.setState({ connectionStatus: 'DISCONNECTED' });
      this.attemptReconnect();
    };

    this.ws.onerror = (error) => {
      console.error('WebSocket error:', error);
      this.stateManager.setState({ connectionStatus: 'ERROR' });
    };
  }

  send(message) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(message));
    } else {
      this.messageQueue.push(message);
      console.log('Message queued (disconnected):', message);
    }
  }

  attemptReconnect() {
    if (this.reconnectAttempts >= this.maxReconnectAttempts) {
      console.error('Max reconnect attempts reached');
      this.stateManager.setState({ 
        connectionStatus: 'FAILED',
        sessionActive: false 
      });
      return;
    }

    this.reconnectAttempts++;
    const delay = this.reconnectDelay * Math.pow(2, this.reconnectAttempts - 1);
    
    console.log(`Reconnecting in ${delay}ms (attempt ${this.reconnectAttempts})`);
    setTimeout(() => this.connect(), delay);
  }

  handleMessage(data) {
    // Handle binary audio frames
    if (data instanceof ArrayBuffer) {
      this.audioController.handleAudioFrame(data);
      return;
    }

    // Handle control messages (JSON)
    const message = JSON.parse(data);
    
    switch (message.type) {
      case 'session_started':
        this.stateManager.setState({
          sessionActive: true,
          totalWords: message.wordCount,
          currentWord: 0,
          completedWords: 0,
          status: 'DICTATING'
        });
        break;
      
      case 'progress_update':
        this.stateManager.setState({
          currentWord: message.currentWord,
          completedWords: message.completedWords
        });
        break;

      case 'session_complete':
        this.stateManager.setState({
          sessionActive: false,
          status: 'FEEDBACK',
          completedWords: message.totalWords
        });
        break;
      
      case 'error':
        console.error('Server error:', message.error);
        this.stateManager.setState({
          status: 'ERROR',
          lastError: message.error
        });
        break;
    }
  }
}
```

### Pattern 3: iPad-Optimized Responsive Design
**What:** CSS patterns optimized specifically for iPad Safari with proper touch targets, readable text, and adaptive layouts.
**When to use:** Building interfaces specifically targeting iPad devices.
**Example:**
```css
/* Source: [ASSUMED - standard responsive design for iPad] */

/* Base styles - mobile-first approach */
* {
  box-sizing: border-box;
  -webkit-tap-highlight-color: transparent; /* Remove default tap highlight */
}

body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
  font-size: 16px;
  line-height: 1.5;
  margin: 0;
  padding: 0;
  /* Prevent zoom on focus for iOS */
  touch-action: manipulation;
}

/* iPad-specific styles */
@media only screen and (min-device-width: 768px) and (max-device-width: 1024px) {
  /* Increase touch targets to minimum 44x44px for iOS guidelines */
  .button, .touch-target {
    min-height: 44px;
    min-width: 44px;
    padding: 12px 24px;
    font-size: 18px;
  }

  /* Optimize text readability */
  body {
    font-size: 18px; /* Larger text for better readability */
  }

  /* Use larger tap targets for kid-friendly interface */
  .kid-button {
    min-height: 60px;
    min-width: 60px;
    padding: 16px 32px;
    font-size: 20px;
    border-radius: 12px; /* Rounded corners for friendly appearance */
  }
  
  /* Main layout container */
  .main-container {
    max-width: 1024px;
    margin: 0 auto;
    padding: 20px;
  }

  /* Session button - prominent, easy to tap */
  .session-button {
    width: 100%;
    max-width: 600px;
    margin: 40px auto;
    padding: 24px 48px;
    font-size: 24px;
    font-weight: bold;
    background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
    color: white;
    border: none;
    border-radius: 16px;
    cursor: pointer;
    transition: transform 0.2s;
    -webkit-transition: -webkit-transform 0.2s;
  }

  .session-button:active {
    transform: scale(0.98);
    -webkit-transform: scale(0.98);
  }

  /* Progress display - clear and large */
  .progress-container {
    padding: 20px;
    background: #f8f9fa;
    border-radius: 12px;
    margin: 20px 0;
  }

  .progress-bar {
    height: 24px;
    background: #e9ecef;
    border-radius: 12px;
    overflow: hidden;
  }

  .progress-fill {
    height: 100%;
    background: linear-gradient(90deg, #56ccf2 0%, #2f80ed 100%);
    transition: width 0.3s ease;
    -webkit-transition: width 0.3s ease;
  }

  /* Status text - large and readable */
  .status-text {
    font-size: 22px;
    text-align: center;
    margin: 16px 0;
    color: #495057;
  }
}

/* Landscape orientation adjustments */
@media only screen and (min-device-width: 768px) and (max-device-width: 1024px) 
and (orientation: landscape) {
  .main-container {
    padding: 15px;
  }
  
  .session-button {
    margin: 20px auto;
    padding: 18px 36px;
  }
}
```

### Anti-Patterns to Avoid
- **Heavy framework usage**: React/Vue add unnecessary complexity and bundle size for this simple interface
- **Blocking UI operations**: Never use synchronous operations that freeze the UI during WebSocket messages
- **Ignoring iOS memory limits**: Avoid caching large amounts of data in browser storage
- **Poor touch target sizes**: iOS guidelines require minimum 44x44px targets; smaller targets frustrate users
- **Missing visual feedback**: Always provide immediate visual feedback for touch interactions

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| WebSocket reconnection logic | Custom retry logic with bugs | Established exponential backoff pattern | Proper reconnection is complex; standard patterns prevent connection storms [ASSUMED] |
| Audio playback scheduling | Manual timeout-based audio sequencing | Web Audio API scheduling system | Ensures gapless playback, handles timing precision, prevents audio glitches [ASSUMED] |
| State synchronization | Custom pub/sub with race conditions | Centralized state manager with subscriber pattern | Prevents state inconsistencies, easier to debug, predictable updates [ASSUMED] |
| Responsive layouts | Media query spaghetti | CSS Grid + Flexbox with mobile-first approach | Modern approach, better browser support, easier maintenance [ASSUMED] |
| Touch interaction handling | Custom gesture detection | Standard touch events + CSS :active states | Native performance, iOS-optimized behavior, less code [ASSUMED] |

**Key insight:** For a student interface with real-time requirements, reliability and simplicity beat custom solutions. Use browser-native APIs and established patterns rather than building complex custom systems.

## Common Pitfalls

### Pitfall 1: iOS Safari Memory Pressure
**What goes wrong:** Application crashes or becomes unresponsive due to memory limits on iOS Safari.
**Why it happens:** iOS Safari has strict memory limits (often ~500MB-1GB per tab) and will terminate tabs that exceed limits.
**How to avoid:**
- Avoid caching large audio files in memory; stream via WebSocket
- Use object pooling for frequently allocated/deallocated objects
- Clean up event listeners and timers when not needed
- Monitor memory usage in iOS Safari developer tools
**Warning signs:** Tab crashes, slow performance, scrolling lag, interface freezing

### Pitfall 2: WebSocket Connection Fragility
**What goes wrong:** WebSocket connections drop frequently, causing session interruptions and poor user experience.
**Why it happens:** iOS Safari aggressively closes WebSocket connections when tabs are backgrounded or during network changes.
**How to avoid:**
- Implement exponential backoff reconnection strategy
- Queue messages during disconnection and replay on reconnect
- Send periodic heartbeat messages to keep connection alive
- Handle reconnection gracefully without losing session state
**Warning signs:** Frequent "disconnected" messages, lost progress, stalled sessions

### Pitfall 3: Audio Playback Gaps and Glitches
**What goes wrong:** Audio playback has gaps between words, stutters, or fails to play in sequence.
**Why it happens:** HTMLAudioElement has loading delays and imprecise timing; manual sequencing is error-prone.
**How to avoid:**
- Use Web Audio API for precise timing and gapless playback
- Pre-schedule audio buffers ahead of playback time
- Handle sequence counter monotonicity strictly
- Implement proper cleanup on session end
**Warning signs:** Audio gaps, words playing out of order, audio cutting off mid-word

### Pitfall 4: Poor Touch Interaction Experience
**What goes wrong:** Buttons are hard to tap, interface feels unresponsive, accidental activations occur.
**Why it happens:** Touch targets too small, no visual feedback, ignoring iOS-specific touch behaviors.
**How to avoid:**
- Use minimum 44x44px touch targets (iOS Human Interface Guidelines)
- Provide immediate visual feedback on touch (:active states)
- Prevent default touch behaviors like zoom and scroll where inappropriate
- Test on real iPad hardware; desktop testing misses touch issues
**Warning signs:** Difficulty tapping buttons, accidental activations, unresponsive interface

### Pitfall 5: Inadequate Accessibility for Young Students
**What goes wrong:** Interface is confusing or unusable for elementary school students.
**Why it happens:** Complex layouts, small text, unclear feedback, assuming adult-level comprehension.
**How to avoid:**
- Use large, clear text with simple language
- Provide obvious visual feedback for all actions
- Use color and icons to supplement text instructions
- Keep interface simple and uncluttered
- Test with actual students in target age group
**Warning signs:** Students confused about what to do, difficulty completing tasks, frustration

## Code Examples

Verified patterns from official sources:

### Web Audio API Integration for PCM Playback
```javascript
// Source: [ASSUMED - Web Audio API standard pattern]
class AudioController {
  constructor() {
    this.audioContext = new (window.AudioContext || window.webkitAudioContext)();
    this.sequenceCounter = 0;
    this.scheduledTime = 0;
    this.isPlaying = false;
  }

  // Convert PCM data to AudioBuffer and schedule playback
  async playPCMFrame(pcmData) {
    if (!this.isPlaying) {
      this.isPlaying = true;
      this.scheduledTime = this.audioContext.currentTime;
    }

    // Convert Int16Array PCM to Float32Array for Web Audio API
    const floatData = new Float32Array(pcmData.length / 2);
    for (let i = 0; i < floatData.length; i++) {
      const intSample = (pcmData[i * 2 + 1] << 8) | pcmData[i * 2];
      floatData[i] = intSample / 32768.0; // Convert to -1.0 to 1.0 range
    }

    // Create audio buffer
    const audioBuffer = this.audioContext.createBuffer(
      1, // mono channel
      floatData.length,
      16000 // 16kHz sample rate (matches backend)
    );
    
    // Copy data to buffer
    audioBuffer.copyToChannel(floatData, 0);

    // Create buffer source and schedule playback
    const source = this.audioContext.createBufferSource();
    source.buffer = audioBuffer;
    source.connect(this.audioContext.destination);
    
    // Schedule at precise time for gapless playback
    source.start(this.scheduledTime);
    
    // Update scheduled time for next frame
    this.scheduledTime += audioBuffer.duration;
    this.sequenceCounter++;
  }

  stop() {
    this.isPlaying = false;
    this.scheduledTime = 0;
    this.sequenceCounter = 0;
  }

  resume() {
    if (!this.isPlaying) {
      this.isPlaying = true;
      this.scheduledTime = this.audioContext.currentTime;
    }
  }
}
```

### Simple Kid-Friendly Session Button Component
```javascript
// Source: [ASSUMED - standard DOM manipulation pattern]
class SessionButton {
  constructor(container, stateManager, wsClient) {
    this.container = container;
    this.stateManager = stateManager;
    this.wsClient = wsClient;
    this.button = null;
    this.render();
  }

  render() {
    this.button = document.createElement('button');
    this.button.className = 'session-button kid-button';
    this.button.textContent = "I want to dictate 30 words";
    this.button.setAttribute('aria-label', 'Start a dictation session with 30 words');
    
    this.button.addEventListener('click', () => this.handleClick());
    
    this.container.appendChild(this.button);
  }

  handleClick() {
    const state = this.stateManager.getState();
    
    if (state.sessionActive) {
      // Session already active - maybe show confirmation
      if (confirm('Session is already active. Stop current session?')) {
        this.wsClient.send({ type: 'stop_session' });
      }
    } else {
      // Request new session
      this.button.disabled = true;
      this.button.textContent = 'Starting session...';
      
      this.wsClient.send({
        type: 'start_session',
        wordCount: 30
      });
    }
  }

  update(state) {
    if (state.sessionActive) {
      this.button.textContent = 'Session in progress...';
      this.button.disabled = true;
    } else if (state.status === 'FEEDBACK') {
      this.button.textContent = 'Start another session';
      this.button.disabled = false;
    } else {
      this.button.textContent = 'I want to dictate 30 words';
      this.button.disabled = false;
    }
  }
}
```

### Progress Display Component
```javascript
// Source: [ASSUMED - standard DOM manipulation pattern]
class ProgressDisplay {
  constructor(container, stateManager) {
    this.container = container;
    this.stateManager = stateManager;
    this.elements = {};
    this.render();
  }

  render() {
    const html = `
      <div class="progress-container">
        <div class="status-text">Ready to start</div>
        <div class="progress-bar">
          <div class="progress-fill" style="width: 0%"></div>
        </div>
        <div class="progress-text">0 / 0 words</div>
      </div>
    `;
    
    this.container.innerHTML = html;
    
    // Cache element references
    this.elements.statusText = this.container.querySelector('.status-text');
    this.elements.progressFill = this.container.querySelector('.progress-fill');
    this.elements.progressText = this.container.querySelector('.progress-text');
  }

  update(state) {
    // Update status text
    const statusMessages = {
      'IDLE': 'Ready to start',
      'LISTENING': 'Listening...',
      'DICTATING': 'Dictation in progress',
      'WAITING_PARENT': 'Waiting for parent',
      'FEEDBACK': 'Session complete!',
      'ERROR': 'Something went wrong'
    };
    
    this.elements.statusText.textContent = statusMessages[state.status] || 'Unknown status';
    
    // Update progress bar
    if (state.totalWords > 0) {
      const percentage = (state.completedWords / state.totalWords) * 100;
      this.elements.progressFill.style.width = `${percentage}%`;
      this.elements.progressText.textContent = `${state.completedWords} / ${state.totalWords} words`;
    } else {
      this.elements.progressFill.style.width = '0%';
      this.elements.progressText.textContent = '0 / 0 words';
    }
  }
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| HTMLAudioElement for audio | Web Audio API scheduling system | ~2015 (wider Safari support) | Enables gapless playback with precise timing; essential for dictation flows |
| Framework-heavy UIs (React/Vue) | Vanilla JS with modern ES6+ | Ongoing trend for simple interfaces | Faster load times, less complexity, better mobile performance |
| Polling for updates | WebSocket real-time communication | ~2012 (modern browsers) | True real-time updates without server load from polling |
| Server-side rendering | Client-side single page apps (simple) | ~2015-2020 | Better responsiveness, smoother UX for simple interfaces |
| jQuery for DOM manipulation | Modern vanilla JS (querySelector, addEventListener) | ~2015+ ES6 adoption | Lighter weight, better performance, native browser APIs |

**Deprecated/outdated:**
- **HTMLAudioElement for sequential playback**: Use Web Audio API instead for gapless playback
- **jQuery**: No longer needed; modern vanilla JS is sufficient and lighter
- **Server-side rendering for simple apps**: Adds complexity; client-side rendering is simpler for this use case
- **Long-polling for real-time updates**: WebSockets provide true real-time communication without HTTP overhead

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Vanilla JavaScript is sufficient for this interface; no framework needed | Standard Stack | Medium - Could result in more complex code, but framework adds overhead |
| A2 | Web Audio API provides gapless playback capability | Code Examples | Low - Web Audio API is well-established; may need refinement for specific iOS issues |
| A3 | iOS Safari memory limits require streaming approach | Common Pitfalls | High - Incorrect assumption could cause crashes; needs real device testing |
| A4 | 44x44px minimum touch target size for iOS is sufficient | Architecture Patterns | Low - iOS HIG guidelines are well-documented |
| A5 | Exponential backoff is standard for WebSocket reconnection | Don't Hand-Roll | Low - Well-established pattern for connection management |
| A6 | CSS Grid + Flexbox provide sufficient responsive capabilities | Standard Stack | Low - Modern CSS with good Safari support |

**If this table is empty:** All claims in this research were verified or cited — no user confirmation needed.

## Open Questions

1. **Audio playback testing on real iPad hardware**
   - What we know: Web Audio API provides the capabilities needed
   - What's unclear: Real-world performance on actual iPad devices with iOS Safari
   - Recommendation: Test early with real device; Phase 7.1 addressed some issues but more testing needed

2. **Optimal state management complexity**
   - What we know: Simple state manager is sufficient for session tracking
   - What's unclear: Whether state management needs to handle more complex scenarios
   - Recommendation: Start simple; add complexity only if needed during implementation

3. **WebSocket message queue size limits**
   - What we know: Need to queue messages during disconnection
   - What's unclear: Maximum queue size before memory issues occur
   - Recommendation: Implement queue size limits with overflow handling

4. **Accessibility requirements for young students**
   - What we know: Basic accessibility principles (large text, clear feedback)
   - What's unclear: Specific needs of elementary school students in this context
   - Recommendation: Test with actual students; incorporate feedback during development

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go backend service | WebSocket communication | ✓ | (from Phase 2) | — |
| Modern browser with WebSocket support | Client-side functionality | ✓ | Safari iOS | — |
| Node.js/npm (if adding libraries) | Package management | ✓ | (verify) | — |
| Real iPad device | iOS-specific testing | ? | — | Use Safari iOS simulator for initial testing |

**Missing dependencies with no fallback:** None

**Missing dependencies with fallback:**
- Real iPad hardware: Use Safari iOS simulator for initial testing, but plan for real device testing

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Manual testing + JSDoc comments for verification |
| Config file | None — manual approach for MVP interface |
| Quick run command | Open `http://localhost:8080` in Safari, test basic interactions |
| Full suite command | Manual testing checklist covering all success criteria |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| DICT-01 | Student can request dictation session via button | Manual | Click "I want to dictate 30 words" button, verify session starts | ❌ Wave 0 |
| WEB-02 | Interface displays real-time session progress | Manual | Verify progress bar updates as session progresses | ❌ Wave 0 |
| WEB-04 | Responsive design works on iPad Safari | Manual | Open on iPad Safari, verify layout and responsiveness | ❌ Wave 0 |

### Sampling Rate
- **Per task commit:** Manual smoke test of implemented features
- **Per wave merge:** Complete manual testing of all implemented features
- **Phase gate:** Manual verification of all success criteria on real iPad Safari

### Wave 0 Gaps
- [ ] `pkg/static/js/tests/` — Basic unit tests for state manager, WebSocket client
- [ ] Manual testing checklist — Success criteria verification steps
- [ ] Test fixtures — Mock WebSocket server for development testing

*(Since this is a UI-focused phase with manual testing emphasis, automated test infrastructure is minimal; focus on real device testing)*

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | No | N/A — Student interface has no authentication in MVP |
| V3 Session Management | Partial | WebSocket session management via backend (Phase 7) |
| V4 Access Control | No | N/A — Public interface for MVP |
| V5 Input Validation | Yes | Basic validation of session request parameters (word count limits) |
| V6 Cryptography | No | N/A — No cryptographic operations in student interface |

### Known Threat Patterns for {Web Interface + WebSocket}

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| WebSocket message injection | Tampering | Validate all incoming messages; sanitize before processing |
| XSS via status display | Tampering | Sanitize server-provided text before DOM insertion; use textContent vs innerHTML |
| CSRF via session initiation | Spoofing | Consider CSRF tokens if session initiation becomes protected |
| Session hijacking | Elevation of rights | Rely on backend WebSocket authentication (from Phase 2) |
| Memory exhaustion via message flood | Denial of Service | Implement rate limiting and message queue size limits |

## Sources

### Primary (HIGH confidence)
- [None — rate limited during research session]

### Secondary (MEDIUM confidence)
- [None — rate limited during research session]

### Tertiary (LOW confidence)
- Web search attempts rate-limited; findings based on training knowledge and project context

**Note:** Due to API rate limiting during research, most findings are marked [ASSUMED]. These should be verified during implementation and testing phases, particularly on real iPad Safari browsers.

## Metadata

**Confidence breakdown:**
- Standard stack: MEDIUM - Vanilla JS recommendation is sound but framework alternatives worth considering
- Architecture: MEDIUM - Patterns are standard but iOS-specific behavior needs verification
- Pitfalls: MEDIUM - Based on known mobile/web development issues but device-specific testing needed

**Research date:** 2026-08-09
**Valid until:** 2026-09-09 (30 days — recommend refresh if implementation delayed)

## User Constraints (from CONTEXT.md)

### Locked Decisions
- **快速反馈周期优先** (Fast Feedback Cycle First) - Interface must support rapid local iteration and testing
- **Component Isolation** - Must integrate with existing Go WebSocket backend
- **Minimum End-to-End Flow** - Focus on core value: student opens webpage → system reads words → student responds → system shows feedback
- **Target: iPad web browser (Safari)** - Must work optimally on iPad Safari
- **Infrastructure: 1C1G VPS, 150MB Go memory budget** - Frontend must be resource-efficient

### Claude's Discretion
- **Frontend approach** - Vanilla JS vs lightweight frameworks (recommend vanilla for simplicity)
- **UI/UX design** - Kid-friendly interface patterns (recommend simple, large touch targets, clear feedback)
- **State management complexity** - Keep simple; add complexity only if needed
- **Testing approach** - Manual testing focus with real device verification

### Deferred Ideas (OUT OF SCOPE)
- Advanced analytics dashboard (deferred to v2)
- Social features (out of scope for MVP)
- Mobile native apps (iPad web approach chosen)

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| DICT-01 | Student can request dictation session via button ("I want to dictate 30 words") | Pattern 3: Simple UI components; Session Button implementation |
| WEB-02 | Student interface for dictation session requests and progress display | State management pattern; Progress display component; WebSocket integration |
| WEB-04 | Responsive design optimized for iPad Safari browser | Pattern 3: iPad-optimized CSS with media queries and touch targets |

---

**Next Steps:** Planner can now create implementation tasks based on this research. Key areas to focus on:
1. Static file serving from Go backend
2. Vanilla JavaScript WebSocket client with reconnection
3. Web Audio API integration for gapless playback
4. Kid-friendly UI components with proper touch targets
5. Responsive CSS optimized for iPad Safari
6. Simple state management for session progress
7. Real device testing for iOS-specific behaviors
