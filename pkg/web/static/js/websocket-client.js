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

      this._reconnectTimer = null;
      // Set true by disconnect() so the close handler does NOT schedule a
      // reconnect for an intentional teardown.
      this._intentionalClose = false;
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

      this.ws = new global.WebSocket(this.url);
      // ArrayBuffer so Phase 7.1 binary audio frames arrive as typed buffers
      // (not Blobs), ready for Web Audio API decoding.
      this.ws.binaryType = 'arraybuffer';

      this.ws.onopen = this._onOpen.bind(this);
      this.ws.onmessage = this._onMessage.bind(this);
      this.ws.onclose = this._onClose.bind(this);
      this.ws.onerror = this._onError.bind(this);
    }

    /** @private open handler: reset attempts, flush queue, mark connected. */
    _onOpen() {
      this.reconnectAttempts = 0;
      this._setState(STATE_CONNECTED);
      this._flushQueue();
    }

    /**
     * @private message handler: dispatch binary frames raw, parse JSON control
     * frames. A malformed JSON frame routes to errorHandler, not a throw.
     */
    _onMessage(event) {
      if (event.data instanceof ArrayBuffer) {
        // Binary audio frame (Phase 7.1).
        this.messageHandler(event.data, true);
        return;
      }
      // Text frame: assume JSON control message. Parse defensively.
      try {
        var parsed = JSON.parse(event.data);
        this.messageHandler(parsed, false);
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
     */
    disconnect() {
      this._intentionalClose = true;
      if (this._reconnectTimer !== null) {
        global.clearTimeout(this._reconnectTimer);
        this._reconnectTimer = null;
      }
      if (this.ws) {
        try {
          this.ws.close();
        } catch (closeErr) {
          // Closing an already-closed socket can throw on some browsers; ignore.
        }
      }
      this._setState(STATE_DISCONNECTED);
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
