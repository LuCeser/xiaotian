/**
 * @file Student Dictation Interface — application entry point
 *       (Phase 8, Plan 02 Task 03 — session lifecycle wiring).
 *
 * Purpose:
 *   Main application initialization and component orchestration for the
 *   student-facing dictation web interface. Loaded with `defer` from
 *   index.html after websocket-client.js, state-manager.js, and
 *   ui-components.js so all constructors are available.
 *
 * Responsibilities (Wave 2 scope):
 *   1. Construct the SessionStateManager (single source of truth).
 *   2. Construct a WebSocketClient pointed at the backend /ws endpoint and
 *      register the state manager so inbound control frames dispatch into
 *      structured state updates (handleControlFrame).
 *   3. Construct the SessionButton and subscribe it to the state manager.
 *   4. Drive the connection-status indicator (#connectionStatus) from the
 *      state manager so students always see whether the app is online
 *      (Pitfall 4: never leave the student guessing).
 *
 * Design constraints (see 08-RESEARCH.md):
 *   - Vanilla JS only — no framework, no build step (1C/1G VPS constraint).
 *   - No setTimeout-based state polling; state updates are event-driven via
 *     WebSocket messages + the client's stateCallback (Anti-Pattern #2).
 *   - Binary audio frames still route to handleMessage for the Phase 7.1
 *     Web Audio hand-off (future Wave); JSON control frames are dispatched
 *     by the ws client's handleControlFrame into the state manager.
 */

(function appEntry() {
  'use strict';

  // ------------------------------------------------------------------
  // Dependency availability checks (graceful degradation)
  // ------------------------------------------------------------------
  if (!window.WebSocketClient) {
    console.error('[app] WebSocketClient unavailable — client script did not load.');
    setConnectionState('failed');
    return;
  }
  if (!window.SessionStateManager || !window.SessionButton) {
    console.error('[app] SessionStateManager/SessionButton unavailable — scripts did not load.');
    setConnectionState('failed');
    return;
  }

  var WS_STATE = {
    CONNECTED: window.WebSocketClient.STATE_CONNECTED,
    CONNECTING: window.WebSocketClient.STATE_CONNECTING,
    DISCONNECTED: window.WebSocketClient.STATE_DISCONNECTED,
    ERROR: window.WebSocketClient.STATE_ERROR,
    FAILED: window.WebSocketClient.STATE_FAILED
  };

  // Derive the WebSocket URL from the page location so the same build works
  // regardless of the configured server port. ws:// for http, wss:// for
  // https (iPad Safari requires wss on TLS deploys). The /ws path matches
  // the Go backend's WebSocket upgrade route.
  var wsUrl = buildWebSocketUrl(window.location);
  console.log('[app] initializing WebSocket client →', wsUrl);

  // ------------------------------------------------------------------
  // Single source of truth: SessionStateManager
  // ------------------------------------------------------------------
  var stateManager = new window.SessionStateManager();

  // ------------------------------------------------------------------
  // WebSocket client: control frames dispatch into the state manager
  // ------------------------------------------------------------------
  var wsClient = new window.WebSocketClient(
    wsUrl,
    handleMessage,
    handleError,
    handleStateChange
  );
  // Route inbound JSON control frames (session_ready/error/progress_update/
  // session_complete) through handleControlFrame into the state manager.
  wsClient.setStateManager(stateManager);
  wsClient.connect();

  // ------------------------------------------------------------------
  // Session button: subscribes to the state manager for its visual states
  // ------------------------------------------------------------------
  var buttonHost = document.getElementById('session-button-container');
  if (!buttonHost) {
    console.error('[app] #session-button-container missing — SessionButton cannot mount.');
  } else {
    var sessionButton = new window.SessionButton(buttonHost, stateManager, wsClient);
    // Re-render the button whenever the authoritative state changes.
    stateManager.subscribe(function (state) {
      sessionButton.update(state);
    });
  }

  // ------------------------------------------------------------------
  // Connection indicator: also driven from the state manager
  // ------------------------------------------------------------------
  stateManager.subscribe(function (state) {
    setConnectionState(toIndicatorState(state.connectionStatus));
  });

  // Expose for console debugging. Not used for cross-script coupling.
  window.__app = { stateManager: stateManager, wsClient: wsClient };

  // ------------------------------------------------------------------
  // Handlers
  // ------------------------------------------------------------------

  /**
   * Inbound message handler. With a state manager registered on the client,
   * JSON control frames are dispatched by handleControlFrame (never reach
   * here except for unknown control types forwarded for visibility). Binary
   * frames (Phase 7.1 audio) route here for the future Web Audio hand-off.
   */
  function handleMessage(data, isBinary) {
    if (isBinary) {
      // Binary audio frame — Wave 3 hands this to the Web Audio scheduler
      // (Phase 7.1 pattern). Logged now for connection verification only.
      console.debug('[app] binary frame received:', data && data.byteLength, 'bytes');
      return;
    }
    // Forwarded control frame (unknown type) or legacy path — log it.
    console.debug('[app] control message received:', data);
  }

  /**
   * Error handler: log and reflect in state so the UI can react. The state
   * manager's lastError is the canonical surface for backend-originated
   * errors (set by handleControlFrame); socket-level errors land here.
   */
  function handleError(err) {
    console.error('[app] WebSocket error:', err);
    stateManager.setState({ lastError: 'connection error' });
  }

  /**
   * State-change handler pipes the client's connection state into the
   * SessionStateManager as connectionStatus — the single path through which
   * the connection indicator updates (via the subscriber above).
   */
  function handleStateChange(newState) {
    console.log('[app] WebSocket state:', newState);
    stateManager.setState({ connectionStatus: newState });
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
