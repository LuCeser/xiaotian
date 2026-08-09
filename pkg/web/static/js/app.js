/**
 * @file Student Dictation Interface — application entry point
 *       (Phase 8, Plan 01 Task 03 — WebSocket client wiring).
 *
 * Purpose:
 *   Main application initialization and component orchestration for the
 *   student-facing dictation web interface. Loaded with `defer` from
 *   index.html so the DOM is fully parsed before this runs. Expects
 *   js/websocket-client.js to have loaded first (it defines
 *   window.WebSocketClient).
 *
 * Responsibilities (current scope — Plan 01 Task 03):
 *   1. Construct a WebSocketClient pointed at the backend /ws endpoint.
 *   2. Drive the connection-status indicator (#connectionStatus) from client
 *      state changes so students always see whether the app is online
 *      (Pitfall 4: never leave the student guessing).
 *   3. Log connection lifecycle events to the browser console for debugging.
 *
 * Deferred to Wave 2:
 *   - Word-count selector binding and start-session button wiring.
 *   - Incoming dictation progress rendering (progress bar, counter,
 *     encouragement) from WebSocket messages.
 *
 * Design constraints (see 08-RESEARCH.md):
 *   - Vanilla JS only — no framework, no build step (1C/1G VPS constraint).
 *   - No setTimeout-based state polling; state updates are event-driven via
 *     WebSocket messages + the client's stateCallback (Anti-Pattern #2).
 */

(function appEntry() {
  'use strict';

  var WS_STATE = window.WebSocketClient
    ? {
      CONNECTED: window.WebSocketClient.STATE_CONNECTED,
      CONNECTING: window.WebSocketClient.STATE_CONNECTING,
      DISCONNECTED: window.WebSocketClient.STATE_DISCONNECTED,
      ERROR: window.WebSocketClient.STATE_ERROR,
      FAILED: window.WebSocketClient.STATE_FAILED
    }
    : null;

  if (!window.WebSocketClient) {
    // websocket-client.js failed to load (rare; e.g. ad blocker). Show a
    // friendly disconnected state rather than crashing silently.
    console.error('[app] WebSocketClient unavailable — client script did not load.');
    setConnectionState('failed');
    return;
  }

  // Derive the WebSocket URL from the page location so the same build works
  // regardless of the configured server port. ws:// for http, wss:// for
  // https (iPad Safari requires wss on TLS deploys). The /ws path matches
  // the Go backend's WebSocket upgrade route.
  var wsUrl = buildWebSocketUrl(window.location);

  console.log('[app] initializing WebSocket client →', wsUrl);

  var wsClient = new window.WebSocketClient(
    wsUrl,
    handleMessage,
    handleError,
    handleStateChange
  );

  // Kick off the connection. Reconnection is handled internally by the
  // client (exponential backoff) — app.js does not retry manually.
  wsClient.connect();

  // Expose for console debugging and Wave 2 wiring. Not used for cross-script
  // coupling — Wave 2 will refactor this into a module-scoped StateManager.
  window.__wsClient = wsClient;

  // ------------------------------------------------------------------
  // Handlers (stubs — full behavior arrives in Wave 2)
  // ------------------------------------------------------------------

  /**
   * Inbound message dispatcher. Binary frames (Phase 7.1 audio) and JSON
   * control frames both route here. Wave 2 implements progress rendering,
   * session state transitions, and audio playback hand-off.
   */
  function handleMessage(data, isBinary) {
    if (isBinary) {
      // Binary audio frame — Wave 2 hands this to the Web Audio scheduler
      // (Phase 7.1 pattern). Logged now for connection verification only.
      console.debug('[app] binary frame received:', data && data.byteLength, 'bytes');
      return;
    }
    console.debug('[app] control message received:', data);
  }

  function handleError(err) {
    console.error('[app] WebSocket error:', err);
  }

  /**
   * State-change handler drives the connection indicator (Pitfall 4) and
   * logs lifecycle transitions. Maps the client's internal state names to
   * the indicator's data-state attribute values (CSS colors the dot).
   */
  function handleStateChange(newState) {
    console.log('[app] WebSocket state:', newState);
    setConnectionState(toIndicatorState(newState));
  }

  // ------------------------------------------------------------------
  // DOM helpers
  // ------------------------------------------------------------------

  function setConnectionState(indicatorState) {
    var el = document.getElementById('connectionStatus');
    if (!el) {
      return;
    }
    el.setAttribute('data-state', indicatorState);
    var textEl = el.querySelector('.connection-text');
    if (textEl) {
      textEl.textContent = indicatorLabel(indicatorState);
    }
  }

  function toIndicatorState(wsState) {
    switch (wsState) {
      case WS_STATE.CONNECTED:
        return 'connected';
      case WS_STATE.CONNECTING:
        return 'connecting';
      case WS_STATE.FAILED:
      case WS_STATE.ERROR:
        return 'failed';
      case WS_STATE.DISCONNECTED:
      default:
        return 'disconnected';
    }
  }

  function indicatorLabel(indicatorState) {
    switch (indicatorState) {
      case 'connected':
        return '已连接';
      case 'connecting':
        return '连接中…';
      case 'failed':
        return '连接失败，请刷新';
      case 'disconnected':
      default:
        return '未连接';
    }
  }

  function buildWebSocketUrl(loc) {
    var protocol = loc.protocol === 'https:' ? 'wss:' : 'ws:';
    // loc.host already includes the port when non-default.
    return protocol + '//' + loc.host + '/ws';
  }
})();
