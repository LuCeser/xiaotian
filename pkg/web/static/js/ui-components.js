/**
 * @file SessionButton — kid-friendly session-request button component
 *       (Phase 8, Plan 02 Task 02).
 *
 * Source pattern: 08-RESEARCH.md Code Examples (Simple Kid-Friendly Session
 * Button Component). Renders the prominent 30-word dictation CTA required by
 * DICT-01, drives its own idle/starting/active/complete visual
 * states from the SessionStateManager, and emits the start_session control
 * frame on click.
 *
 * Threat model (08-02 PLAN <threat_model>):
 *   - T-08-06 (Tampering, start_session params): the word limit is hard-coded
 *     to 30 here — there is no user-editable input field. The backend
 *     re-validates.
 *   - T-08-07 (DoS, button-click flood): the button disables itself the
 *     moment a request is in flight and stays disabled for the whole session.
 *
 * Loaded via a <script defer> tag AFTER state-manager.js and BEFORE app.js
 * in index.html; exposes the class as window.SessionButton.
 */
(function (global) {
  'use strict';

  // The fixed session size required by DICT-01. Hard-coded (T-08-06): no UI
  // input can change it, and the backend validates the value regardless.
  var SESSION_LIMIT = 30;

  // Button copy. The idle label is the literal DICT-01 string; the other
  // labels narrate each transition so the student is never left guessing
  // (Pitfall 4 from 08-RESEARCH.md).
  var LABEL_IDLE = 'I want to dictate 30 words';
  var LABEL_STARTING = 'Starting session...';
  var LABEL_ACTIVE = 'Session in progress...';
  var LABEL_COMPLETE = 'Done! Start another?';

  /**
   * SessionButton renders the session-request CTA into a container element
   * and keeps it in sync with the SessionStateManager.
   *
   * @class
   * @param {HTMLElement} container - element the button mounts into
   *        (#session-button-container).
   * @param {SessionStateManager} stateManager - the shared state store.
   * @param {WebSocketClient} wsClient - used to send the start_session frame.
   * @param {AudioController} audioController - Web Audio API integration (Plan 08-04 Task 2).
   */
  class SessionButton {
    constructor(container, stateManager, wsClient, audioController) {
      if (!container) {
        throw new Error('SessionButton requires a container element');
      }
      this.container = container;
      this.stateManager = stateManager;
      this.wsClient = wsClient;
      this.audioController = audioController;

      // In-flight request flag. True between click and session_ready/error.
      // Distinct from state.sessionActive (which is only true after the
      // backend confirms the session) so we can disable the button during
      // the wait regardless of backend timing (T-08-07).
      this.requesting = false;

      // Last error string we already surfaced. Lets update() detect a NEW
      // error arriving during a request (to abort the wait) without reacting
      // to stale errors from a previous session.
      this._lastSeenError = null;

      this.button = null;
      this.render();
    }

    /**
     * Build the button element and mount it into the container. Idempotent:
     * clears any previous button first so re-renders do not stack elements.
     * The button uses the kid-button + session-button classes so main.css
     * sizes it as a large touch target (iOS HIG 60px+) and styles the CTA.
     */
    render() {
      // Clear previous content (safe for re-mount / re-init on reconnect).
      while (this.container.firstChild) {
        this.container.removeChild(this.container.firstChild);
      }

      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'kid-button session-button';
      // DICT-01 literal label. aria-label gives screen readers the same
      // affordance with the action phrasing.
      btn.textContent = LABEL_IDLE;
      btn.setAttribute('aria-label', 'Start a dictation session with 30 words');
      // Announce state transitions to assistive tech.
      btn.setAttribute('aria-live', 'polite');
      btn.addEventListener('click', this.handleClick.bind(this));

      this.container.appendChild(btn);
      this.button = btn;

      // Apply the current state so the initial label/disabled matches
      // (e.g. if constructed after a session was already active).
      if (this.stateManager) {
        this.update(this.stateManager.getState());
      }
    }

    /**
     * Click handler: unlock audio (D-03) then send start_session unless a
     * session/request is already in flight. Hard-codes limit: 30 (T-08-06)
     * and flips to the starting state immediately so the student sees feedback
     * before the round trip.
     *
     * @param {Event} [evt] - DOM click event (ignored beyond signaling).
     */
    handleClick(evt) {
      // T-08-07: ignore clicks while busy or active. The button is also
      // disabled in these states, but this guard defends against rapid
      // double-clicks before the disabled attribute applies.
      if (this.requesting) {
        return;
      }
      var state = this.stateManager ? this.stateManager.getState() : {};
      if (state.sessionActive) {
        return;
      }

      this.requesting = true;
      // Clear any prior error label and show the in-flight state.
      if (this.stateManager) {
        this.stateManager.setState({ lastError: null });
      }
      this.applyVisual(LABEL_STARTING, true);

      // Plan 08-04 Task 2: unlock audio before starting session (D-03).
      // This is the FIRST synchronous statement of the tap.
      if (this.audioController && typeof this.audioController.unlockAudioContext === 'function') {
        this.audioController.unlockAudioContext();
        if (typeof console !== 'undefined') {
          console.log('[session-button] audio unlocked before session start');
        }
      }

      // Emit the control frame. The ws client queues it if still connecting
      // and flushes on open, so a fast click during the initial handshake
      // still reaches the backend.
      if (this.wsClient && typeof this.wsClient.send === 'function') {
        this.wsClient.send({
          type: 'start_session',
          params: { limit: SESSION_LIMIT }
        });
        if (typeof console !== 'undefined') {
          console.log('[session-button] sent start_session (limit=' + SESSION_LIMIT + ')');
        }
      } else if (typeof console !== 'undefined') {
        console.error('[session-button] no WebSocketClient — cannot start session');
        // Without a client the request can never succeed; abort the wait.
        this.requesting = false;
        this.applyVisual(LABEL_IDLE, true);
      }
    }

    /**
     * State-driven view update (called by stateManager.subscribe). Translates
     * the authoritative session + connection state into the button's label
     * and disabled attribute.
     *
     * Transitions:
     *   - sessionActive true        → "Session in progress...", disabled
     *   - requesting + new error    → revert to idle/enable + surface error
     *   - requesting (no error yet) → "Starting session...", disabled
     *   - otherwise                 → idle label, enabled
     *
     * @param {object} state - snapshot from SessionStateManager.
     */
    update(state) {
      if (!this.button) {
        return;
      }
      var sessionActive = !!(state && state.sessionActive);
      var currentError = state ? state.lastError : null;

      if (sessionActive) {
        // Backend confirmed the session — clear the in-flight flag and lock
        // the button for the duration (T-08-07).
        this.requesting = false;
        this._lastSeenError = currentError;
        this.applyVisual(LABEL_ACTIVE, true);
        return;
      }

      if (this.requesting) {
        // A new error arriving while waiting means the start failed — abort
        // the wait and surface the error so the student can retry.
        if (currentError && currentError !== this._lastSeenError) {
          this.requesting = false;
          this._lastSeenError = currentError;
          // Surface the backend message briefly; next click resets it.
          this.applyVisual(currentError, false);
          return;
        }
        // Still waiting for session_ready.
        this.applyVisual(LABEL_STARTING, true);
        return;
      }

      // Idle. Track the latest error so a subsequent request only reacts to
      // errors newer than this point.
      this._lastSeenError = currentError;
      this.applyVisual(LABEL_IDLE, false);
    }

    /**
     * Apply a label + disabled state to the button element. Extracted so the
     * state transitions stay readable in update()/handleClick().
     *
     * @param {string} label - button textContent.
     * @param {boolean} disabled - whether the button should be disabled.
     * @private
     */
    applyVisual(label, disabled) {
      if (!this.button) {
        return;
      }
      this.button.textContent = label;
      this.button.disabled = !!disabled;
      this.button.setAttribute('aria-disabled', disabled ? 'true' : 'false');
    }
  }

  // Expose the hard-coded limit for tests / introspection.
  SessionButton.SESSION_LIMIT = SESSION_LIMIT;
  SessionButton.LABEL_IDLE = LABEL_IDLE;

  global.SessionButton = SessionButton;

  // ==================================================================
  // ProgressDisplay — real-time dictation progress indicator
  // (Phase 8, Plan 03 Task 01).
  //
  // Source: 08-RESEARCH.md Code Examples (Progress Display Component) +
  // Pattern 3 (iPad-Optimized Responsive Design). Renders the
  // progress-display-section content as a JS-driven component (mirroring
  // the Wave 2 SessionButton pattern) and updates status text, progress
  // bar fill, and word counter from the SessionStateManager in real time.
  //
  // Threat model (08-03 PLAN <threat_model>):
  //   - T-08-08 (Tampering, progress values): the component clamps the
  //     computed percentage to [0, 100] and guards totalWords === 0 before
  //     dividing, so a malformed snapshot (negative completedWords,
  //     completedWords > totalWords) cannot break the UI. Range validation
  //     also lives in WebSocketClient._applyProgressUpdate (Task 2).
  //   - T-08-09 (Info disclosure): the component renders only the student's
  //     own session state from the single SessionStateManager — no
  //     cross-session data can reach it (accepted per plan).
  // ==================================================================

  // Status message mapping (PLAN Task 1 spec). Each FSM status (Phase 7
  // echoes) maps to a short, student-readable English string. These appear
  // in the 22px status-text element on iPad per the plan's readability target.
  var PROGRESS_STATUS_MESSAGES = {
    IDLE: 'Ready to start',
    LISTENING: 'Listening...',
    DICTATING: 'Dictation in progress',
    WAITING_PARENT: 'Waiting for parent',
    FEEDBACK: 'Session complete!'
  };
  var PROGRESS_ERROR_MESSAGE = 'Something went wrong';

  /**
   * ProgressDisplay renders the dictation progress UI (status text, progress
   * bar, word counter, encouragement) into a container element and keeps it
   * in sync with the SessionStateManager. Replaces Wave 1's static inline
   * progress markup with a JS-driven component so the structure matches the
   * authoritative state (same pattern as SessionButton in Wave 2).
   *
   * @class
   * @param {HTMLElement} container - element the progress UI mounts into
   *        (#progress-display-container).
   * @param {SessionStateManager} stateManager - the shared state store.
   */
  class ProgressDisplay {
    constructor(container, stateManager) {
      if (!container) {
        throw new Error('ProgressDisplay requires a container element');
      }
      this.container = container;
      this.stateManager = stateManager;

      // Cached DOM references (populated by render()). Kept on the instance
      // so update() can mutate them without re-querying on every state change.
      this.statusRow = null;
      this.statusText = null;
      this.progressContainer = null; // the role=progressbar outer track
      this.progressFill = null;      // the gradient inner fill
      this.progressText = null;      // "X / Y words" counter
      this.encouragement = null;     // milestone praise line

      // Track the last percentage we rendered so encouragement only fires on
      // a milestone crossing (avoids re-praising on every progress_update).
      this._lastPercent = -1;

      this.render();
    }

    /**
     * Build the progress UI structure and mount it into the container.
     * Idempotent: clears any previous content first (safe for re-mount).
     * Uses the existing main.css class names (.progress-status, .status-text,
     * .progress-container, .progress-bar/.progress-fill, .word-counter,
     * .encouragement) so Wave 1's tested responsive styles apply unchanged.
     */
    render() {
      while (this.container.firstChild) {
        this.container.removeChild(this.container.firstChild);
      }

      // Status row: icon + readable status text (22px on iPad via CSS).
      this.statusRow = document.createElement('div');
      this.statusRow.className = 'progress-status';
      var icon = document.createElement('span');
      icon.className = 'status-icon';
      icon.setAttribute('aria-hidden', 'true');
      icon.textContent = '📝';
      this.statusText = document.createElement('span');
      this.statusText.className = 'status-text';
      // aria-live on the row so status transitions are announced together.
      this.statusRow.setAttribute('aria-live', 'polite');
      this.statusRow.appendChild(icon);
      this.statusRow.appendChild(this.statusText);

      // Progress bar track (role=progressbar for AT). The fill lives inside.
      this.progressContainer = document.createElement('div');
      this.progressContainer.className = 'progress-container';
      this.progressContainer.setAttribute('role', 'progressbar');
      this.progressContainer.setAttribute('aria-label', 'Dictation progress');
      this.progressContainer.setAttribute('aria-valuemin', '0');
      this.progressContainer.setAttribute('aria-valuemax', '100');
      this.progressContainer.setAttribute('aria-valuenow', '0');
      this.progressFill = document.createElement('div');
      this.progressFill.className = 'progress-bar progress-fill';
      this.progressFill.style.width = '0%';
      this.progressContainer.appendChild(this.progressFill);

      // Word counter: "X / Y words" (PLAN Task 1 format).
      this.progressText = document.createElement('div');
      this.progressText.className = 'word-counter';
      this.progressText.setAttribute('aria-live', 'polite');

      // Encouragement line (kid-friendly milestone praise).
      this.encouragement = document.createElement('div');
      this.encouragement.className = 'encouragement';
      this.encouragement.setAttribute('aria-live', 'polite');

      this.container.appendChild(this.statusRow);
      this.container.appendChild(this.progressContainer);
      this.container.appendChild(this.progressText);
      this.container.appendChild(this.encouragement);

      // Apply the current state so the initial render matches (e.g. a
      // ProgressDisplay constructed mid-session shows live progress).
      if (this.stateManager) {
        this.update(this.stateManager.getState());
      }
    }

    /**
     * State-driven view update (called by stateManager.subscribe). Translates
     * the authoritative session state into status text, progress bar width,
     * word counter, and encouragement. Handles all PLAN Task 3 edge cases:
     *   - totalWords === 0: show "0 / 0 words", bar at 0% (no divide-by-zero)
     *   - session not active: empty/ready state
     *   - session_complete (FEEDBACK): force 100% + "Session complete!"
     *   - error: show the error message in status text, maintain progress
     *
     * @param {object} state - snapshot from SessionStateManager.
     */
    update(state) {
      if (!this.statusText) {
        return; // not yet rendered
      }
      state = state || {};
      var status = state.status || 'IDLE';
      var sessionActive = !!state.sessionActive;
      var totalWords = state.totalWords || 0;
      var completedWords = state.completedWords || 0;
      var lastError = state.lastError || null;

      // --- Status text ---
      // An error takes priority for the status line so the student sees what
      // went wrong, but we DO NOT reset the progress bar (PLAN Task 3: "On
      // error: show error message in status text, maintain current progress").
      if (lastError) {
        this.statusText.textContent = lastError;
      } else {
        this.statusText.textContent = this._messageForStatus(status);
      }

      // --- Progress percentage ---
      // T-08-08: clamp to [0, 100] and guard divide-by-zero. A spoofed
      // progress_update with completedWords > totalWords cannot overflow the
      // bar; a negative cannot invert it.
      var percent = 0;
      if (totalWords > 0) {
        percent = Math.round((completedWords / totalWords) * 100);
        percent = Math.min(100, Math.max(0, percent));
      }
      // On FEEDBACK (session_complete), force 100% so the bar visibly fills
      // even if the last progress_update lagged.
      if (status === 'FEEDBACK' && totalWords > 0) {
        percent = 100;
        completedWords = totalWords;
      }
      this.progressFill.style.width = percent + '%';
      this.progressContainer.setAttribute('aria-valuenow', String(percent));

      // --- Word counter ("X / Y words") ---
      this.progressText.textContent = completedWords + ' / ' + totalWords + ' words';

      // --- Encouragement (milestone praise, fires only on crossing) ---
      this.encouragement.textContent = this._encouragementFor(percent, status);

      this._lastPercent = percent;
    }

    /**
     * Map an FSM status to its student-readable status string. Falls back to
     * the IDLE message for unknown values so the UI never shows "undefined".
     *
     * @private
     * @param {string} status - one of SessionStateManager.STATUS_*.
     * @returns {string}
     */
    _messageForStatus(status) {
      if (PROGRESS_STATUS_MESSAGES.hasOwnProperty(status)) {
        return PROGRESS_STATUS_MESSAGES[status];
      }
      return PROGRESS_STATUS_MESSAGES.IDLE;
    }

    /**
     * Return a short encouragement string for a milestone crossing or terminal
     * state. Returns '' when there is nothing new to say (so the line stays
     * blank between milestones rather than flickering).
     *
     * @private
     * @param {number} percent - current clamped percentage.
     * @param {string} status - current FSM status.
     * @returns {string}
     */
    _encouragementFor(percent, status) {
      if (status === 'FEEDBACK') {
        return '🎉 Great job!';
      }
      // Only fire encouragement on a milestone crossing to avoid re-praising
      // on every word_complete / progress_update.
      var prev = this._lastPercent;
      if (percent >= 75 && prev < 75) return '⭐ Almost there!';
      if (percent >= 50 && prev < 50) return '👍 Halfway done!';
      if (percent >= 25 && prev < 25) return '😊 Good start!';
      return '';
    }
  }

  // Expose the status map + error message for tests / introspection.
  ProgressDisplay.STATUS_MESSAGES = PROGRESS_STATUS_MESSAGES;
  ProgressDisplay.ERROR_MESSAGE = PROGRESS_ERROR_MESSAGE;

  global.ProgressDisplay = ProgressDisplay;
})(typeof window !== 'undefined' ? window : this);
