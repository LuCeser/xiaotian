/**
 * @file SessionStateManager — minimal pub/sub state store for the student
 *       dictation interface (Phase 8, Plan 02 Task 01).
 *
 * Source pattern: 08-RESEARCH.md Pattern 1 (Simple State Management for
 * Session Progress). A single SessionStateManager holds the authoritative
 * session + connection state; UI components subscribe and re-render on
 * change. No framework, no build step (1C/1G VPS constraint).
 *
 * Design notes:
 *   - State is a plain object snapshot. setState() merges a partial update
 *     and synchronously notifies every subscriber with a shallow copy, so
 *     subscribers never mutate the internal state (Pitfall: shared mutable
 *     state). This mirrors the read-only notify contract the research doc
 *     prescribes.
 *   - Status + connectionStatus are enumerated strings (constants exported
 *     on the constructor) so consumers compare against STATUS.DICTATING
 *     rather than typo-prone literals.
 *   - Subscribers unregister via the returned unsubscribe function so
 *     components can clean up on teardown (no leak across reconnects).
 *
 * Loaded via a <script defer> tag BEFORE ui-components.js and app.js in
 * index.html; exposes the class as window.SessionStateManager.
 */
(function (global) {
  'use strict';

  // Session lifecycle status (Phase 7 FSM echoes). The student interface
  // only needs to reflect a subset, but we keep the full vocabulary so the
  // state manager can accept any backend transition without rework.
  var STATUS_IDLE = 'IDLE';
  var STATUS_LISTENING = 'LISTENING';
  var STATUS_DICTATING = 'DICTATING';
  var STATUS_WAITING_PARENT = 'WAITING_PARENT';
  var STATUS_FEEDBACK = 'FEEDBACK';

  // Connection status mirrors WebSocketClient's five-state machine so app.js
  // can pipe the client's stateCallback straight into setState().
  var CONN_DISCONNECTED = 'DISCONNECTED';
  var CONN_CONNECTING = 'CONNECTING';
  var CONN_CONNECTED = 'CONNECTED';
  var CONN_ERROR = 'ERROR';
  var CONN_FAILED = 'FAILED';

  /**
   * Initial state snapshot. Every field has a primitive default so
   * subscribers can render immediately on page load (e.g. the session
   * button renders its idle label before any WebSocket event arrives).
   */
  function initialState() {
    return {
      // Session lifecycle.
      sessionActive: false, // true between session_ready and session_complete
      status: STATUS_IDLE, // current session FSM status
      session_id: null, // server-generated session id (from session_ready)
      words: [], // ordered list of word ids in the active session
      // Progress counters.
      currentWord: 0, // 1-based index of the word currently being dictated
      totalWords: 0, // total words promised by session_ready (words.length)
      completedWords: 0, // words fully delivered this session
      // Connection (driven by WebSocketClient.stateCallback).
      connectionStatus: CONN_DISCONNECTED,
      // Last backend error message (string) or null. Surfaced to the UI so a
      // failed session start explains itself instead of looking frozen.
      lastError: null
    };
  }

  /**
   * SessionStateManager is the single source of truth for session + connection
   * state. UI components subscribe() and react to notify(); the WebSocket
   * client and app.js call setState() as events arrive.
   *
   * @class
   */
  class SessionStateManager {
    constructor() {
      this.state = initialState();
      this.subscribers = [];
    }

    /**
     * Merge a partial update into the state and notify every subscriber.
     * Only top-level keys are merged (shallow). Unknown keys are accepted so
     * future message types can extend state without editing this class.
     *
     * @param {object} updates - partial state to merge (e.g.
     *        {sessionActive: true, status: 'DICTATING'}).
     * @returns {object} the new state snapshot (shallow copy).
     */
    setState(updates) {
      if (!updates || typeof updates !== 'object') {
        return this.getState();
      }
      // Merge onto a fresh object so the internal reference swaps atomically;
      // subscribers always observe a consistent snapshot.
      this.state = Object.assign({}, this.state, updates);
      this.notify();
      return this.getState();
    }

    /**
     * Register a subscriber that receives the current state snapshot on every
     * change. Returns an unsubscribe function so components can release the
     * subscription on teardown.
     *
     * @param {function} callback - invoked as (stateSnapshot) on notify().
     * @returns {function} unsubscribe — call to remove the callback.
     */
    subscribe(callback) {
      if (typeof callback !== 'function') {
        throw new TypeError('SessionStateManager.subscribe requires a function');
      }
      this.subscribers.push(callback);
      var subscribers = this.subscribers;
      return function unsubscribe() {
        var idx = subscribers.indexOf(callback);
        if (idx !== -1) {
          subscribers.splice(idx, 1);
        }
      };
    }

    /**
     * Notify every subscriber with a shallow copy of the current state.
     * A throwing subscriber is logged and skipped so one bad listener cannot
     * silence the rest (defensive: UI components are loaded dynamically).
     */
    notify() {
      var snapshot = this.getState();
      for (var i = 0; i < this.subscribers.length; i++) {
        try {
          this.subscribers[i](snapshot);
        } catch (subscriberErr) {
          if (typeof console !== 'undefined') {
            console.error('[state] subscriber threw', subscriberErr);
          }
        }
      }
    }

    /**
     * Return a shallow copy of the current state. Callers must not rely on
     * mutating the returned object — always go through setState().
     *
     * @returns {object} state snapshot copy.
     */
    getState() {
      // Shallow copy is sufficient: every field is a primitive or the words
      // array. We copy the array so a subscriber cannot mutate the canonical
      // word list by reference.
      var copy = Object.assign({}, this.state);
      copy.words = this.state.words.slice();
      return copy;
    }
  }

  // Expose status + connection constants on the constructor for consumers
  // (e.g. ui-components.js checks state.status === SessionStateManager.STATUS_DICTATING).
  SessionStateManager.STATUS_IDLE = STATUS_IDLE;
  SessionStateManager.STATUS_LISTENING = STATUS_LISTENING;
  SessionStateManager.STATUS_DICTATING = STATUS_DICTATING;
  SessionStateManager.STATUS_WAITING_PARENT = STATUS_WAITING_PARENT;
  SessionStateManager.STATUS_FEEDBACK = STATUS_FEEDBACK;

  SessionStateManager.CONN_DISCONNECTED = CONN_DISCONNECTED;
  SessionStateManager.CONN_CONNECTING = CONN_CONNECTING;
  SessionStateManager.CONN_CONNECTED = CONN_CONNECTED;
  SessionStateManager.CONN_ERROR = CONN_ERROR;
  SessionStateManager.CONN_FAILED = CONN_FAILED;

  global.SessionStateManager = SessionStateManager;
})(typeof window !== 'undefined' ? window : this);
