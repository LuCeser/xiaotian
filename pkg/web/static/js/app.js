/**
 * @file Student Dictation Interface — application entry point
 *       (Phase 8, Plan 01 — scaffold; wired in Plan 01 Task 03).
 *
 * Purpose:
 *   Main application initialization and component orchestration for the
 *   student-facing dictation web interface. Loaded with `defer` from
 *   index.html so the DOM is fully parsed before this runs.
 *
 * Responsibilities (when fully wired):
 *   1. Initialize the WebSocket client (js/websocket-client.js) and connect
 *      to the Go backend's /ws endpoint with exponential-backoff reconnection.
 *   2. Drive the connection-status indicator (#connectionStatus) from client
 *      state changes so students always see whether the app is online.
 *   3. Bind the word-count selector and start-session button to outgoing
 *      WebSocket requests.
 *   4. Render incoming dictation progress (progress bar, word counter,
 *      encouragement) from WebSocket messages.
 *
 * Design constraints (see 08-RESEARCH.md):
 *   - Vanilla JS only — no framework, no build step (1C/1G VPS constraint).
 *   - No setTimeout-based state polling; state updates are event-driven via
 *     WebSocket messages (Anti-Pattern #2).
 *
 * NOTE: This file is intentionally minimal in Plan 01 Task 02. Task 03 adds
 *       the WebSocketClient import and connection wiring. Full session-flow
 *       wiring arrives in Wave 2.
 */

(function appEntry() {
  'use strict';

  // Sentinel log so developers can confirm the entry point loaded and the
  // static file server is serving JS correctly. Task 03 replaces this with
  // real WebSocket client initialization.
  if (typeof console !== 'undefined') {
    console.log('[app] 听写小助手 entry point loaded — WebSocket wiring pending (Task 03).');
  }
})();
