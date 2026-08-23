/**
 * @file WebSocketClient — reconnection-capable WebSocket client for the
 *       student dictation interface (Phase 8, Plan 01 Task 03).
 *
 * Source pattern: 08-RESEARCH.md Pattern 3 (verified against Phase 7.1's
 * audio playback client). Wraps the native WebSocket with:
 *   - exponential backoff reconnection (1s * 2^attempt, capped at 30s, max
 *     10 attempts) so transient network drops and iPad backgrounding recover
 *     automatically.
 *   - A bounded message queue (max 100) that buffers outbound messages while
 *     disconnected and flushes on reconnect (Pitfall 2: prevent memory
 *     overflow on unstable networks).
 *   - Binary (ArrayBuffer) + JSON control-frame dispatch so the same client
 *     carries Phase 7.1 audio frames and future control messages.
 *   - Explicit state machine (DISCONNECTED → CONNECTING → CONNECTED | ERROR
 *     → FAILED) surfaced via stateCallback so app.js can drive the
 *     connection indicator (Pitfall 4).
 *
 * Loaded via a <script defer> tag BEFORE app.js in index.html; exposes the
 * class as window.WebSocketClient. No module bundler, no build step.
 */
(function (global) {
  'use strict';

  // Connection state constants. Exported on the class for easy comparison by
  // consumers (e.g. app.js checks client.state === WebSocketClient.STATE_CONNECTED).
  var STATE_DISCONNECTED = 'DISCONNECTED';
  var STATE_CONNECTING = 'CONNECTING';
  var STATE_CONNECTED = 'CONNECTED';
  var STATE_ERROR = 'ERROR';
  var STATE_FAILED = 'FAILED';

  // Defaults — tunable via constructor options where useful.
  var DEFAULT_MAX_RECONNECT_ATTEMPTS = 10; // T-08-04: cap to prevent flood
  var MAX_QUEUE_SIZE = 100; // Pitfall 2: ~6KB cap, drop oldest when exceeded
  var BASE_BACKOFF_MS = 1000; // 1s base for exponential backoff
  var MAX_BACKOFF_MS = 30000; // cap backoff at 30s
  var CONNECTION_TIMEOUT_MS = 10000; // Plan 08-04 Task 4: 10s connection timeout
  var MAX_MESSAGE_SIZE = 1024 * 1024; // Plan 08-04 Task 4: 1MB max message size

  // G-08-3: UTF-8 helpers for moving JSON text into/out of binary frame
  // payloads. TextEncoder/TextDecoder are universal on target browsers
  // (iPad Safari iOS 17+ included), so no manual fallback is needed.
  function textEncode(str) {
    return new global.TextEncoder().encode(str); // Uint8Array
  }
  function textDecode(bytes) {
    return new global.TextDecoder('utf-8').decode(bytes);
  }

  /**
   * WebSocketClient wraps the native WebSocket with exponential-backoff
   * reconnection and a bounded offline message queue.
   *
   * @param {string} url - WebSocket endpoint, e.g. "ws://host:8080/ws".
   * @param {function} messageHandler - invoked as (data, isBinary) on every
   *        inbound frame. isBinary is true for ArrayBuffer payloads (Phase
   *        7.1 audio frames), false for parsed JSON control frames.
   * @param {function} [errorHandler] - invoked as (error) on socket errors.
   *        Defaults to a console.error sink.
   * @param {function} [stateCallback] - invoked as (newState) on every state
   *        transition. app.js uses this to update #connectionStatus.
   * @param {object} [options] - optional overrides:
   *        { maxReconnectAttempts: number }
   */
  class WebSocketClient {
    constructor(url, messageHandler, errorHandler, stateCallback, options) {
      this.url = url;
      this.messageHandler = messageHandler || function () {};
      this.errorHandler = errorHandler || function defaultErrorHandler(err) {
        if (typeof console !== 'undefined') {
          console.error('[ws] socket error', err);
        }
      };
      this.stateCallback = stateCallback || function () {};

      options = options || {};
      this.maxReconnectAttempts = options.maxReconnectAttempts || DEFAULT_MAX_RECONNECT_ATTEMPTS;

      // Runtime state.
      this.ws = null;
      this.reconnectAttempts = 0;
      this.messageQueue = [];
      this.state = STATE_DISCONNECTED;

      // Optional SessionStateManager (registered via setStateManager). When
      // present, inbound JSON control frames are dispatched into structured
      // state updates instead of being handed raw to messageHandler. Wave 2
      // session-lifecycle wiring.
      this.stateManager = null;

      this._reconnectTimer = null;
      // Set true by disconnect() so the close handler does NOT schedule a
      // reconnect for an intentional teardown.
      this._intentionalClose = false;

      // Plan 08-04 Task 4: connection timeout tracking
      this._connectionTimer = null;
      // Circuit breaker: track consecutive failures
      this._consecutiveErrors = 0;

      // G-08-3: monotonic client-side sequence counter for outbound control
      // frames (mirrors the backend's D-12 atomic counter). Each encoded
      // frame carries the next sequence so the server can order/dedup.
      this._seqCounter = new (global.SequenceCounter || function () { this._s = 0; })();
    }

    /**
     * Establish the WebSocket connection. Safe to call when already connected
     * or connecting (no-op). Resets the intentional-close flag from a prior
     * disconnect() so a fresh connect attempt can reconnect freely.
     */
    connect() {
      if (this.state === STATE_CONNECTED || this.state === STATE_CONNECTING) {
        return;
      }
      if (typeof global.WebSocket === 'undefined') {
        // Browser without WebSocket support (should not happen on any iPad
        // Safari, but fail gracefully rather than throwing).
        this._setState(STATE_FAILED);
        this.errorHandler(new Error('WebSocket API unavailable in this browser'));
        return;
      }

      this._intentionalClose = false;
      this._setState(STATE_CONNECTING);

      // Plan 08-04 Task 4: connection timeout guard
      this._clearConnectionTimer();
      this._connectionTimer = global.setTimeout(() => {
        if (this.state === STATE_CONNECTING) {
          // CR-6 fix: Close the WebSocket and null handlers to orphan the socket
          // before triggering reconnect, preventing race conditions where the
          // original socket fires onopen/onclose after reconnect starts.
          if (this.ws) {
            try {
              this.ws.close();
            } catch (e) {
              // Ignore errors during close
            }
            this.ws.onopen = null;
            this.ws.onclose = null;
            this.ws.onmessage = null;
            this.ws.onerror = null;
            this.ws = null;
          }
          this.errorHandler(new Error('Connection timeout (10s)'));
          this._setState(STATE_ERROR);
          this._onClose(); // Trigger reconnect logic
        }
      }, CONNECTION_TIMEOUT_MS);

      try {
        this.ws = new global.WebSocket(this.url);
        // ArrayBuffer so Phase 7.1 binary audio frames arrive as typed buffers
        // (not Blobs), ready for Web Audio API decoding.
        this.ws.binaryType = 'arraybuffer';

        this.ws.onopen = this._onOpen.bind(this);
        this.ws.onmessage = this._onMessage.bind(this);
        this.ws.onclose = this._onClose.bind(this);
        this.ws.onerror = this._onError.bind(this);
      } catch (constructErr) {
        this._setState(STATE_ERROR);
        this.errorHandler(constructErr);
        this._clearConnectionTimer();
      }
    }

    /** @private open handler: reset attempts, flush queue, mark connected. */
    _onOpen() {
      this.reconnectAttempts = 0;
      this._consecutiveErrors = 0; // Reset circuit breaker
      this._clearConnectionTimer();
      this._setState(STATE_CONNECTED);
      this._flushQueue();
    }

    /**
     * @private message handler. G-08-3: the backend sends EVERY frame as a
     * Phase-2 binary message (16-byte header + CRC-16). Decode the frame, then
     * dispatch by MsgType:
     *   - AUDIO: pass the original framed ArrayBuffer to messageHandler; app.js
     *     parses the header (sequence at offset 2, payload at offset 16) and
     *     routes PCM into AudioController. We pass event.data unchanged so that
     *     existing header-parsing stays intact.
     *   - CONTROL/ERROR/other: payload is a JSON control frame. Decode text,
     *     JSON.parse, and dispatch into the state manager (handleControlFrame)
     *     or the generic messageHandler when no state manager is registered.
     *
     * Plan 08-04 Task 4: message-size guard retained for memory protection.
     */
    _onMessage(event) {
      // Plan 08-04 Task 4: reject oversized messages to prevent memory exhaustion
      if (event.data instanceof ArrayBuffer && event.data.byteLength > MAX_MESSAGE_SIZE) {
        this.errorHandler(new Error('Message too large: ' + event.data.byteLength + ' bytes'));
        return;
      }

      // G-08-3: text frames are no longer expected on the wire. If one arrives
      // (e.g. a misconfigured peer), surface it instead of silently mis-routing.
      if (!(event.data instanceof ArrayBuffer)) {
        this.errorHandler(new Error('unexpected text frame; backend should send binary Phase-2 frames'));
        return;
      }

      var decoded;
      try {
        decoded = FrameCodec.decode(event.data);
      } catch (decodeErr) {
        // CRC failure, unsupported version, or length mismatch — drop + surface.
        this.errorHandler(decodeErr);
        return;
      }

      if (decoded.msgType === FrameCodec.MSG_TYPE.AUDIO) {
        this.messageHandler(event.data, true);
        return;
      }

      var jsonText;
      try {
        jsonText = textDecode(decoded.payload);
      } catch (tdErr) {
        this.errorHandler(tdErr);
        return;
      }
      var parsed;
      try {
        parsed = JSON.parse(jsonText);
      } catch (parseErr) {
        this.errorHandler(parseErr);
        return;
      }

      if (this.stateManager) {
        this.handleControlFrame(parsed);
      } else {
        this.messageHandler(parsed, false);
      }
    }

    /** @private close handler: reconnect unless this was intentional. */
    _onClose() {
      if (this._intentionalClose) {
        this._setState(STATE_DISCONNECTED);
        return;
      }
      this._attemptReconnect();
    }

    /** @private error handler: surface + flag state; close follows. */
    _onError(err) {
      this._setState(STATE_ERROR);
      this.errorHandler(err);
      // The browser fires onclose after onerror; _onClose will schedule the
      // reconnect. We do NOT reconnect directly here to avoid double timers.
    }

    /**
     * Send a message now if connected; otherwise enqueue it for the next
     * successful (re)connection. Objects are JSON-stringified and wrapped in a
     * Phase-2 binary control frame (G-08-3); strings are treated as
     * pre-encoded JSON payloads and framed the same way. The queue is bounded
     * (MAX_QUEUE_SIZE) — oldest entries drop first to protect the 1C/1G memory
     * budget (Pitfall 2).
     *
     * G-08-3: the backend only accepts BinaryMessage + DecodeFrame, so every
     * outbound control frame is encoded via FrameCodec (16-byte header +
     * CRC-16-CCITT, MsgType=Control). Sending a raw JSON text frame would be
     * silently dropped at the server boundary.
     *
     * @param {string|object} message
     */
    send(message) {
      if (this.state === STATE_CONNECTED && this.ws && this.ws.readyState === global.WebSocket.OPEN) {
        var json = typeof message === 'string' ? message : JSON.stringify(message);
        var frame = FrameCodec.encode(
          FrameCodec.MSG_TYPE.CONTROL,
          this._nextSeq(),
          textEncode(json)
        );
        this.ws.send(frame);
        return;
      }
      this.messageQueue.push(message);
      if (this.messageQueue.length > MAX_QUEUE_SIZE) {
        this.messageQueue.shift(); // drop oldest
      }
    }

    /**
     * @private next outbound control-frame sequence number (monotonic uint32,
     * client-side). Guards against a missing SequenceCounter (defensive — the
     * script load order in index.html guarantees FrameCodec/SequenceCounter
     * load before this client).
     */
    _nextSeq() {
      if (this._seqCounter && typeof this._seqCounter.next === 'function') {
        return this._seqCounter.next();
      }
      this._fallbackSeq = (this._fallbackSeq || 0) + 1;
      return this._fallbackSeq >>> 0;
    }

    /** @private flush queued messages after a (re)connection. */
    _flushQueue() {
      while (this.messageQueue.length > 0 && this.state === STATE_CONNECTED) {
        this.send(this.messageQueue.shift());
      }
    }

    /**
     * @private exponential backoff reconnection: delay = BASE * 2^attempt,
     * capped at MAX_BACKOFF_MS. Aborts at maxReconnectAttempts and surfaces
     * the FAILED state so app.js can tell the student the connection is gone.
     */
    _attemptReconnect() {
      if (this.reconnectAttempts >= this.maxReconnectAttempts) {
        this._setState(STATE_FAILED);
        return;
      }
      // exponential backoff: 1s, 2s, 4s, 8s, 16s, 30s, 30s, ... capped.
      var delay = Math.min(BASE_BACKOFF_MS * Math.pow(2, this.reconnectAttempts), MAX_BACKOFF_MS);
      this.reconnectAttempts++;
      this._setState(STATE_CONNECTING);

      this._reconnectTimer = global.setTimeout(() => {
        this._reconnectTimer = null;
        this.connect();
      }, delay);
    }

    /** @private state transition + notify stateCallback. */
    _setState(newState) {
      if (this.state === newState) {
        return;
      }
      this.state = newState;
      try {
        this.stateCallback(newState);
      } catch (cbErr) {
        // A throwing state callback must not break the reconnect loop.
        if (typeof console !== 'undefined') {
          console.error('[ws] stateCallback threw', cbErr);
        }
      }
    }

    /**
     * Intentionally tear down the connection. Cancels any pending reconnect
     * timer and suppresses the automatic reconnect that onclose would
     * otherwise schedule. Safe to call multiple times.
     *
     * Plan 08-04 Task 4: proper resource cleanup on page unload.
     */
    disconnect() {
      this._intentionalClose = true;
      if (this._reconnectTimer !== null) {
        global.clearTimeout(this._reconnectTimer);
        this._reconnectTimer = null;
      }
      this._clearConnectionTimer();
      if (this.ws) {
        try {
          this.ws.close();
        } catch (closeErr) {
          // Closing an already-closed socket can throw on some browsers; ignore.
        }
      }
      // Plan 08-04 Task 4: clear message queue on disconnect
      this.messageQueue = [];
      this._setState(STATE_DISCONNECTED);
    }

    /**
     * @private clear the connection timeout timer.
     */
    _clearConnectionTimer() {
      if (this._connectionTimer !== null) {
        global.clearTimeout(this._connectionTimer);
        this._connectionTimer = null;
      }
    }

    /**
     * Register a SessionStateManager so inbound JSON control frames are
     * dispatched into structured state updates. Optional — when unset, the
     * client keeps Wave 1 behavior (parsed JSON handed to messageHandler).
     * Safe to call multiple times.
     *
     * @param {SessionStateManager} [stateManager]
     */
    setStateManager(stateManager) {
      this.stateManager = stateManager || null;
    }

    /**
     * Dispatch a parsed JSON control frame into the registered state manager.
     * Validates each frame's structure before applying it (T-08-05: do not
     * let a malformed/spoofed frame drive UI state). Unknown types are logged
     * and forwarded to messageHandler so consumers can extend the protocol.
     *
     * Handled types (see cmd/service/handlers.go):
     *   - session start ack: {type, session_id, words[]}
     *   - error:             {type, message}
     *   - progress:          {type, currentWord, completedWords}
     *   - word done:         {type, word_id}
     *   - session end:       {type, totalWords}
     *
     * @param {object} payload - parsed JSON control frame.
     */
    handleControlFrame(payload) {
      // T-08-05: structural validation before any state mutation.
      if (!payload || typeof payload !== 'object' || typeof payload.type !== 'string') {
        this.errorHandler(new Error('malformed control frame: missing string type'));
        return;
      }
      if (!this.stateManager) {
        // No state store registered — fall back to the generic handler.
        this.messageHandler(payload, false);
        return;
      }

      switch (payload.type) {
        case 'session_ready':
          this._applySessionReady(payload);
          break;
        case 'error':
          this._applyError(payload);
          break;
        case 'progress_update':
          this._applyProgressUpdate(payload);
          break;
        case 'word_complete':
          this._applyWordComplete(payload);
          break;
        case 'session_complete':
          this._applySessionComplete(payload);
          break;
        case 'paused':
          this._applyPaused(payload);
          break;
        default:
          // Unknown control type — log and forward so consumers can evolve
          // the protocol without editing the client.
          if (typeof console !== 'undefined') {
            console.warn('[ws] unknown control frame type:', payload.type, payload);
          }
          this.messageHandler(payload, false);
      }
    }

    /** @private apply a session-start ack frame (T-08-05 validates fields). */
    _applySessionReady(payload) {
      if (typeof payload.session_id !== 'string' || !Array.isArray(payload.words)) {
        this.errorHandler(new Error('malformed session-start ack frame'));
        return;
      }

      // CR-4 fix: Reset audio controller state between sessions to prevent
      // sequence counter carryover that would reject all frames in session 2+
      if (typeof window !== 'undefined' && window.audioController) {
        window.audioController.stop();
      }

      var firstWordId = payload.words.length > 0 ? payload.words[0] : null;
      this.stateManager.setState({
        sessionActive: true,
        status: 'DICTATING',
        session_id: payload.session_id,
        words: payload.words.slice(),
        totalWords: payload.words.length,
        currentWord: 0,
        completedWords: 0,
        currentWordId: firstWordId, // CR-3 fix: initialize audio routing to first word
        lastError: null
      });

      // CR-3 fix: Send play_word control frame to start streaming audio for first word.
      // Backend controlFramePayload expects {"type","params":{...}} — word_id must be
      // nested under params or the router forwards an empty params object and the
      // handler rejects it with "invalid play_word params".
      if (firstWordId) {
        this.send({ type: 'play_word', params: { word_id: firstWordId } });
      }
    }

    /** @private apply an error frame — record message, preserve session flag. */
    _applyError(payload) {
      var message = typeof payload.message === 'string' ? payload.message : 'unknown error';
      this.stateManager.setState({ lastError: message });
    }

    /**
     * @private apply a progress frame.
     * T-08-05 validates numeric types; T-08-08 additionally rejects negative
     * values and completedWords > totalWords (tampering mitigation). A rejected
     * frame is surfaced via errorHandler and does NOT mutate state, so the UI
     * keeps the last known-good progress rather than rendering a broken bar.
     */
    _applyProgressUpdate(payload) {
      if (typeof payload.currentWord !== 'number' || typeof payload.completedWords !== 'number') {
        this.errorHandler(new Error('malformed progress frame'));
        return;
      }
      // T-08-08: reject negative progress values.
      if (payload.currentWord < 0 || payload.completedWords < 0) {
        this.errorHandler(new Error('malformed progress frame: negative values'));
        return;
      }
      // T-08-08: reject completedWords exceeding the promised total. totalWords
      // is 0 before session_ready resolves; in that window we accept the frame
      // (the session_ready ack will reset counters authoritatively).
      var total = this.stateManager.getState().totalWords;
      if (total > 0 && payload.completedWords > total) {
        this.errorHandler(new Error('malformed progress frame: completedWords exceeds totalWords'));
        return;
      }
      this.stateManager.setState({
        currentWord: payload.currentWord,
        completedWords: payload.completedWords
      });
    }

    /**
     * @private apply a word_complete frame — increment completedWords by one
     * AND trigger audio playback via the global AudioController (Plan 08-04 Task 2).
     * Used by the backend's per-word streaming loop. word_id is informational
     * (not stored). T-08-08: clamp the increment at totalWords so a sequence
     * of word_complete frames cannot push progress past 100%.
     */
    _applyWordComplete(payload) {
      var snapshot = this.stateManager.getState();
      var total = snapshot.totalWords;
      var next = snapshot.completedWords + 1;
      if (total > 0 && next > total) {
        next = total; // clamp at 100%
      }
      this.stateManager.setState({ completedWords: next });

      // Plan 08-04 Task 2: trigger audio playback when word completes.
      var wordId = typeof payload.word_id === 'string' ? payload.word_id : null;
      if (wordId && typeof window !== 'undefined' && window.audioController) {
        window.audioController.playWordComplete(wordId);
      }

      // CR-3 fix: Advance to next word and send next_word control frame.
      // Same params-nesting requirement as play_word above.
      // NOTE: read completedWords from live state (not the stale snapshot)
      // because setState({completedWords: next}) above already updated it.
      var words = this.stateManager.getState().words;
      var completedNow = this.stateManager.getState().completedWords;
      var nextWordIndex = completedNow; // 0-based index of the next word to play
      if (nextWordIndex < words.length) {
        var nextWordId = words[nextWordIndex];
        this.stateManager.setState({ currentWordId: nextWordId });
        this.send({ type: 'next_word', params: { word_id: nextWordId } });
      }
    }

    /** @private apply a session_complete frame → end the session. */
    _applySessionComplete(payload) {
      var total = typeof payload.totalWords === 'number' ? payload.totalWords : 0;
      this.stateManager.setState({
        sessionActive: false,
        status: 'FEEDBACK',
        completedWords: total
      });
    }

    /** @private apply a paused frame → set status to PAUSED. */
    _applyPaused(payload) {
      this.stateManager.setState({ status: 'PAUSED' });
    }
  }

  // Expose state constants on the constructor for consumer comparisons.
  WebSocketClient.STATE_DISCONNECTED = STATE_DISCONNECTED;
  WebSocketClient.STATE_CONNECTING = STATE_CONNECTING;
  WebSocketClient.STATE_CONNECTED = STATE_CONNECTED;
  WebSocketClient.STATE_ERROR = STATE_ERROR;
  WebSocketClient.STATE_FAILED = STATE_FAILED;

  global.WebSocketClient = WebSocketClient;
})(typeof window !== 'undefined' ? window : this);
