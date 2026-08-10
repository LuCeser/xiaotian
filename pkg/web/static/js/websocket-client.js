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
     * @private message handler: dispatch binary frames raw, parse JSON control
     * frames. A malformed JSON frame routes to errorHandler, not a throw.
     *
     * When a SessionStateManager is registered (Wave 2), parsed JSON control
     * frames route to the structured control dispatcher for state updates
     * instead of the generic messageHandler. Binary frames always go to
     * messageHandler (Phase 7.1 audio hand-off).
     *
     * Plan 08-04 Task 4: add message size limits for memory protection.
     */
    _onMessage(event) {
      // Plan 08-04 Task 4: reject oversized messages to prevent memory exhaustion
      if (event.data instanceof ArrayBuffer && event.data.byteLength > MAX_MESSAGE_SIZE) {
        this.errorHandler(new Error('Message too large: ' + event.data.byteLength + ' bytes'));
        return;
      }
      if (typeof event.data === 'string' && event.data.length > MAX_MESSAGE_SIZE) {
        this.errorHandler(new Error('Message too large: ' + event.data.length + ' chars'));
        return;
      }

      if (event.data instanceof ArrayBuffer) {
        // Binary audio frame (Phase 7.1).
        this.messageHandler(event.data, true);
        return;
      }
      // Text frame: assume JSON control message. Parse defensively.
      try {
        var parsed = JSON.parse(event.data);
        if (this.stateManager) {
          // Wave 2: structured dispatch into the state store.
          this.handleControlFrame(parsed);
        } else {
          this.messageHandler(parsed, false);
        }
      } catch (parseErr) {
        // Deliver raw text so the consumer can decide; surface the parse issue.
        this.errorHandler(parseErr);
        this.messageHandler(event.data, false);
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
     * successful (re)connection. Objects are stringified to JSON; strings are
     * sent as-is. The queue is bounded (MAX_QUEUE_SIZE) — oldest entries drop
     * first to protect the 1C/1G memory budget (Pitfall 2).
     *
     * @param {string|object} message
     */
    send(message) {
      if (this.state === STATE_CONNECTED && this.ws && this.ws.readyState === global.WebSocket.OPEN) {
        var payload = typeof message === 'string' ? message : JSON.stringify(message);
        this.ws.send(payload);
        return;
      }
      this.messageQueue.push(message);
      if (this.messageQueue.length > MAX_QUEUE_SIZE) {
        this.messageQueue.shift(); // drop oldest
      }
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
      this.stateManager.setState({
        sessionActive: true,
        status: 'DICTATING',
        session_id: payload.session_id,
        words: payload.words.slice(),
        totalWords: payload.words.length,
        currentWord: 0,
        completedWords: 0,
        lastError: null
      });
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
  }

  // Expose state constants on the constructor for consumer comparisons.
  WebSocketClient.STATE_DISCONNECTED = STATE_DISCONNECTED;
  WebSocketClient.STATE_CONNECTING = STATE_CONNECTING;
  WebSocketClient.STATE_CONNECTED = STATE_CONNECTED;
  WebSocketClient.STATE_ERROR = STATE_ERROR;
  WebSocketClient.STATE_FAILED = STATE_FAILED;

  global.WebSocketClient = WebSocketClient;
})(typeof window !== 'undefined' ? window : this);
