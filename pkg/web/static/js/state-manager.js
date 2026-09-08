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
  var STATUS_PAUSED = 'PAUSED';
  var STATUS_WAITING_PARENT = 'WAITING_PARENT';
  var STATUS_SUBMISSION = 'SUBMISSION'; // Ticket 05: batch answers being graded
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
      // Audio frame routing (Plan 08-04 Task 2).
      currentWordId: null, // word whose audio frames are currently being accumulated
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
     * After merging, progress counters are normalized (PLAN 08-03 Task 2,
     * T-08-08 defense-in-depth): completedWords is clamped to [0, totalWords]
     * and negatives on any progress field are corrected. The primary range
     * check lives at the trust boundary in WebSocketClient._applyProgressUpdate
     * (which REJECTS bad frames); this is a safety net so a future code path
     * that bypasses the ws client cannot leave the store in an impossible state.
     *
     * Plan 08-04 Task 4: add state validation and consistency checks.
     *
     * @param {object} updates - partial state to merge (e.g.
     *        {sessionActive: true, status: 'DICTATING'}).
     * @returns {object} the new state snapshot (shallow copy).
     */
    setState(updates) {
      if (!updates || typeof updates !== 'object') {
        if (typeof console !== 'undefined') {
          console.error('[state] setState called with invalid updates:', updates);
        }
        return this.getState();
      }

      // Plan 08-04 Task 4: validate state transitions before applying
      if (!this._validateStateUpdate(this.state, updates)) {
        if (typeof console !== 'undefined') {
          console.error('[state] Invalid state transition rejected:', this.state, '→', updates);
        }
        return this.getState();
      }

      // Merge onto a fresh object so the internal reference swaps atomically;
      // subscribers always observe a consistent snapshot.
      this.state = Object.assign({}, this.state, updates);
      // T-08-08 defense-in-depth: normalize progress counters after merge so
      // the store never holds an out-of-range value regardless of the caller.
      this._normalizeProgress();
      this.notify();
      return this.getState();
    }

    /**
     * Clamp progress counters to sane bounds. Called after every setState
     * merge. Guards against: negative completedWords, completedWords exceeding
     * totalWords, and divide-by-zero downstream (ProgressDisplay also guards
     * totalWords === 0 before dividing, so this is belt-and-braces).
     *
     * Also enforces the terminal-state invariant: once the FSM reaches
     * FEEDBACK (the session-complete terminal state), completedWords MUST equal
     * totalWords so the progress bar renders 100% regardless of which code
     * path drove the transition. The ws-client's session-complete handler
     * already sets this, but normalizing here protects against future callers.
     *
     * @private
     */
    _normalizeProgress() {
      var s = this.state;
      // Terminal-state invariant: FEEDBACK forces 100% completion.
      // PAUSED does NOT force completion — progress is frozen at the current level.
      if (s.status === STATUS_FEEDBACK && typeof s.totalWords === 'number' && s.totalWords > 0) {
        s.completedWords = s.totalWords;
        return;
      }
      // Coerce to integer bounds; only touch fields that are set.
      if (typeof s.completedWords === 'number') {
        if (s.completedWords < 0) {
          s.completedWords = 0;
        }
        if (typeof s.totalWords === 'number' && s.totalWords > 0 && s.completedWords > s.totalWords) {
          s.completedWords = s.totalWords;
        }
      }
      if (typeof s.currentWord === 'number' && s.currentWord < 0) {
        s.currentWord = 0;
      }
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

    /**
     * Get the current word ID for audio frame routing (Plan 08-04 Task 2).
     * Used by the app layer to route audio frames to the AudioController.
     *
     * @returns {string|null} current word ID or null.
     */
    getCurrentWordId() {
      return this.state.currentWordId || null;
    }

    /**
     * Reset state to initial values (Plan 08-04 Task 4). Called on error
     * conditions or session cleanup.
     */
    reset() {
      this.state = initialState();
      this.notify();
    }

    /**
     * Validate state transition (Plan 08-04 Task 4). Reject invalid
     * transitions that would break session or connection invariants.
     *
     * @private
     * @param {object} oldState - current state snapshot.
     * @param {object} updates - proposed updates.
     * @returns {boolean} true if transition is valid, false otherwise.
     */
    _validateStateUpdate(oldState, updates) {
      // SessionActive cannot go from false to true without session_id
      if (updates.sessionActive === true && !updates.session_id && !oldState.session_id) {
        return false;
      }

      // Status must be a valid FSM state
      if (updates.status) {
        var validStates = [STATUS_IDLE, STATUS_LISTENING, STATUS_DICTATING, STATUS_PAUSED, STATUS_WAITING_PARENT, STATUS_SUBMISSION, STATUS_FEEDBACK];
        if (validStates.indexOf(updates.status) === -1) {
          return false;
        }
      }

      // connectionStatus must be a valid connection state
      if (updates.connectionStatus) {
        var validConnStates = [CONN_DISCONNECTED, CONN_CONNECTING, CONN_CONNECTED, CONN_ERROR, CONN_FAILED];
        if (validConnStates.indexOf(updates.connectionStatus) === -1) {
          return false;
        }
      }

      return true;
    }
  }

  // Expose status + connection constants on the constructor for consumers
  // (e.g. ui-components.js checks state.status === SessionStateManager.STATUS_DICTATING).
  SessionStateManager.STATUS_IDLE = STATUS_IDLE;
  SessionStateManager.STATUS_LISTENING = STATUS_LISTENING;
  SessionStateManager.STATUS_DICTATING = STATUS_DICTATING;
  SessionStateManager.STATUS_PAUSED = STATUS_PAUSED;
  SessionStateManager.STATUS_WAITING_PARENT = STATUS_WAITING_PARENT;
  SessionStateManager.STATUS_SUBMISSION = STATUS_SUBMISSION;
  SessionStateManager.STATUS_FEEDBACK = STATUS_FEEDBACK;

  SessionStateManager.CONN_DISCONNECTED = CONN_DISCONNECTED;
  SessionStateManager.CONN_CONNECTING = CONN_CONNECTING;
  SessionStateManager.CONN_CONNECTED = CONN_CONNECTED;
  SessionStateManager.CONN_ERROR = CONN_ERROR;
  SessionStateManager.CONN_FAILED = CONN_FAILED;

  global.SessionStateManager = SessionStateManager;
})(typeof window !== 'undefined' ? window : this);
