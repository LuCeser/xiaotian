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
   */
  class SessionButton {
    constructor(container, stateManager, wsClient) {
      if (!container) {
        throw new Error('SessionButton requires a container element');
      }
      this.container = container;
      this.stateManager = stateManager;
      this.wsClient = wsClient;

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
     * Click handler: send start_session unless a session/request is already
     * in flight. Hard-codes limit: 30 (T-08-06) and flips to the starting
     * state immediately so the student sees feedback before the round trip.
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
})(typeof window !== 'undefined' ? window : this);
