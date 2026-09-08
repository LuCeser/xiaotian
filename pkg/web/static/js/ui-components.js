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
    PAUSED: 'Paused — tap resume to continue',
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
      if (status === 'PAUSED') {
        return '⏸ Paused';
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

  // ==================================================================
  // DictationFeedback — recording indicator + correct/wrong feedback
  // (Phase 8.1, dictation closed loop).
  // ==================================================================

  var DICTATION_FEEDBACK_MESSAGES = {
    listening: '🎤 请说出来...',
    correct: '✓',
    incorrect: '✗',
    skipped: '⏭ 跳过'
  };

  /**
   * DictationFeedback renders the per-word dictation feedback UI:
   * recording indicator, correct/incorrect verdict, and the expected
   * answer when wrong.
   *
   * @class
   * @param {HTMLElement} container - element to mount into
   * @param {SessionStateManager} stateManager - the shared state store
   */
  class DictationFeedback {
    constructor(container, stateManager) {
      if (!container) throw new Error('DictationFeedback requires a container element');
      this.container = container;
      this.stateManager = stateManager;

      this.feedbackEl = null;
      this.wordDisplayEl = null;
      this.heardEl = null;

      // Inject pulse animation CSS if not already present
      if (!document.getElementById('dictation-feedback-styles')) {
        var style = document.createElement('style');
        style.id = 'dictation-feedback-styles';
        style.textContent = '@keyframes df-pulse { 0%,100% { opacity:1; } 50% { opacity:0.3; } }';
        document.head.appendChild(style);
      }

      this.render();
    }

    render() {
      while (this.container.firstChild) {
        this.container.removeChild(this.container.firstChild);
      }

      // Main feedback container
      this.feedbackEl = document.createElement('div');
      this.feedbackEl.className = 'dictation-feedback';
      this.feedbackEl.setAttribute('aria-live', 'assertive');
      this.feedbackEl.style.cssText = 'text-align:center;padding:20px;min-height:80px;display:flex;flex-direction:column;align-items:center;justify-content:center;';

      // Word display (shows the current word being dictated)
      this.wordDisplayEl = document.createElement('div');
      this.wordDisplayEl.className = 'dictation-word';
      this.wordDisplayEl.style.cssText = 'font-size:28px;font-weight:700;color:#1f2937;margin-bottom:12px;';

      // Feedback message
      this.heardEl = document.createElement('div');
      this.heardEl.className = 'dictation-heard';
      this.heardEl.style.cssText = 'font-size:16px;color:#6b7280;';

      this.feedbackEl.appendChild(this.wordDisplayEl);
      this.feedbackEl.appendChild(this.heardEl);
      this.container.appendChild(this.feedbackEl);

      // Apply current state
      if (this.stateManager) {
        this.update(this.stateManager.getState());
      }
    }

    update(state) {
      if (!this.feedbackEl) return;
      state = state || {};

      var dictationState = state.dictationState;
      var currentWordId = state.currentWordId;
      var expected = state.lastExpected;
      var heard = state.lastHeard;
      var correct = state.lastAnswerCorrect;

      // Show/hide based on dictation state
      if (!dictationState || !state.sessionActive) {
        this.feedbackEl.style.display = 'none';
        return;
      }
      this.feedbackEl.style.display = 'flex';

      // Word display
      if (currentWordId) {
        this.wordDisplayEl.textContent = currentWordId;
        this.wordDisplayEl.style.display = 'block';
      } else if (expected) {
        this.wordDisplayEl.textContent = expected;
        this.wordDisplayEl.style.display = 'block';
      } else {
        this.wordDisplayEl.style.display = 'none';
      }

      // Feedback styling
      switch (dictationState) {
        case 'listening':
          this.feedbackEl.style.background = '#fef3c7';
          this.feedbackEl.style.borderRadius = '12px';
          this.heardEl.textContent = DICTATION_FEEDBACK_MESSAGES.listening;
          this.heardEl.style.color = '#92400e';
          // Add pulsing animation
          this.heardEl.style.animation = 'df-pulse 1s infinite';
          break;

        case 'correct':
          this.feedbackEl.style.background = '#d1fae5';
          this.feedbackEl.style.borderRadius = '12px';
          this.heardEl.textContent = DICTATION_FEEDBACK_MESSAGES.correct + ' 正确！';
          this.heardEl.style.color = '#065f46';
          this.heardEl.style.animation = 'none';
          break;

        case 'incorrect':
          this.feedbackEl.style.background = '#fee2e2';
          this.feedbackEl.style.borderRadius = '12px';
          var msg = DICTATION_FEEDBACK_MESSAGES.incorrect;
          if (expected) msg += ' 正确答案: ' + expected;
          if (heard) msg += ' (听到: ' + heard + ')';
          this.heardEl.textContent = msg;
          this.heardEl.style.color = '#991b1b';
          this.heardEl.style.animation = 'none';
          break;

        case 'skipped':
          this.feedbackEl.style.background = '#f3f4f6';
          this.feedbackEl.style.borderRadius = '12px';
          this.heardEl.textContent = DICTATION_FEEDBACK_MESSAGES.skipped + (heard ? ' (' + heard + ')' : '');
          this.heardEl.style.color = '#6b7280';
          this.heardEl.style.animation = 'none';
          break;

        default:
          this.feedbackEl.style.display = 'none';
      }
    }
  }

  global.DictationFeedback = DictationFeedback;

  // ==================================================================
  // DictationControls — pause/resume/repeat/next buttons
  // (Ticket 02, DICT-07).
  //
  // Kid-friendly large touch targets (60px+ per iOS HIG) with Chinese labels.
  // Visible only during DICTATING or PAUSED states. Buttons send control
  // frames via the WebSocket client.
  // ==================================================================

  /**
   * DictationControls renders the dictation control buttons (pause/resume,
   * repeat, next word) and keeps them in sync with the session state.
   *
   * @class
   * @param {HTMLElement} container - element the controls mount into
   * @param {SessionStateManager} stateManager - the shared state store
   * @param {WebSocketClient} wsClient - used to send control frames
   */
  class DictationControls {
    constructor(container, stateManager, wsClient) {
      if (!container) throw new Error('DictationControls requires a container element');
      this.container = container;
      this.stateManager = stateManager;
      this.wsClient = wsClient;

      this.pauseResumeBtn = null;
      this.repeatBtn = null;
      this.nextBtn = null;
      this.isPaused = false;

      this.render();
    }

    /**
     * Build the three dictation control buttons and mount them into the
     * container. Idempotent: clears the container's existing children first
     * so re-renders do not stack elements. Creates a .dictation-controls
     * wrapper (role=group, aria-label "听写控制", centered flex layout), then
     * builds the pause/resume ("⏸ 暂停"), repeat ("🔁 再读一遍"), and
     * next-word ("⏭ 下一个") buttons via _createButton, each with its own
     * Chinese aria-label. After mounting, if stateManager exists,
     * immediately calls update() with its current state so the initial
     * visibility and button labels are correct (e.g. when constructed while
     * a session is already active).
     */
    render() {
      while (this.container.firstChild) {
        this.container.removeChild(this.container.firstChild);
      }

      var wrapper = document.createElement('div');
      wrapper.className = 'dictation-controls';
      wrapper.setAttribute('role', 'group');
      wrapper.setAttribute('aria-label', '听写控制');
      wrapper.style.cssText = 'display:flex;gap:12px;justify-content:center;flex-wrap:wrap;padding:16px 0;';

      // Pause / Resume button (toggle)
      this.pauseResumeBtn = this._createButton('⏸ 暂停', this._handlePauseResume.bind(this));
      this.pauseResumeBtn.setAttribute('aria-label', '暂停听写');

      // Repeat button
      this.repeatBtn = this._createButton('🔁 再读一遍', this._handleRepeat.bind(this));
      this.repeatBtn.setAttribute('aria-label', '再读一遍当前单词');

      // Next word button
      this.nextBtn = this._createButton('⏭ 下一个', this._handleNext.bind(this));
      this.nextBtn.setAttribute('aria-label', '跳到下一个单词');

      wrapper.appendChild(this.pauseResumeBtn);
      wrapper.appendChild(this.repeatBtn);
      wrapper.appendChild(this.nextBtn);
      this.container.appendChild(wrapper);

      if (this.stateManager) {
        this.update(this.stateManager.getState());
      }
    }

    /**
     * Button factory shared by the three controls. Creates a type=button
     * element with the kid-button + dictation-control-button classes, sets
     * its textContent to label, applies the inline styles that keep every
     * control a large iOS HIG touch target (min-width:100px,
     * min-height:60px, font-size:18px, ...), binds the click listener, and
     * returns the element.
     *
     * @private
     * @param {string} label - button textContent.
     * @param {Function} onClick - click listener to bind.
     * @returns {HTMLButtonElement}
     */
    _createButton(label, onClick) {
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'kid-button dictation-control-button';
      btn.textContent = label;
      btn.style.cssText = 'min-width:100px;min-height:60px;font-size:18px;padding:12px 20px;border-radius:12px;border:2px solid #e5e7eb;background:#fff;color:#374151;cursor:pointer;touch-action:manipulation;user-select:none;';
      btn.addEventListener('click', onClick);
      return btn;
    }

    /**
     * Pause/resume button click handler. Sends a "resume" control frame
     * (params: empty object) when this.isPaused is true and a "pause" frame
     * otherwise. Does NOT flip isPaused locally — the flag's single source
     * of truth is update(), which syncs it from the authoritative backend
     * status, so the button label/color change waits for the state round
     * trip to flow back through update(). Silently returns when wsClient is
     * missing or its send is not a function.
     *
     * @private
     */
    _handlePauseResume() {
      if (!this.wsClient || typeof this.wsClient.send !== 'function') return;
      if (this.isPaused) {
        this.wsClient.send({ type: 'resume', params: {} });
      } else {
        this.wsClient.send({ type: 'pause', params: {} });
      }
    }

    /**
     * Repeat button click handler. Sends a "repeat" control frame (params:
     * empty object) asking the backend to re-read the current word.
     * Silently returns when wsClient is missing or its send is not a
     * function.
     *
     * @private
     */
    _handleRepeat() {
      if (!this.wsClient || typeof this.wsClient.send !== 'function') return;
      this.wsClient.send({ type: 'repeat', params: {} });
    }

    /**
     * Next-word button click handler. Reads currentWordId from
     * stateManager.getState() (falling back to an empty string when
     * stateManager is missing or the field is absent) and sends it as
     * word_id in a "next_word" control frame. Silently returns when
     * wsClient is missing or its send is not a function.
     *
     * @private
     */
    _handleNext() {
      if (!this.wsClient || typeof this.wsClient.send !== 'function') return;
      var state = this.stateManager ? this.stateManager.getState() : {};
      var wordId = state.currentWordId || '';
      this.wsClient.send({ type: 'next_word', params: { word_id: wordId } });
    }

    /**
     * State-driven view update (called by stateManager.subscribe). Syncs
     * PAUSED into this.isPaused — the single source of truth for that flag,
     * which _handlePauseResume reads to decide between resume and pause
     * frames. Shows the controls only while sessionActive and status is
     * DICTATING, LISTENING, or PAUSED; otherwise hides the container and
     * returns early. While visible, toggles the pause/resume button's
     * label, aria-label, and colors from isPaused (PAUSED → "▶ 继续" with a
     * blue background/border, otherwise "⏸ 暂停" with white). The trailing
     * next-word disable targets WAITING_PARENT, which the early return above
     * already excludes — that line is unreachable today and is kept solely
     * as a guard in case the show condition ever widens.
     *
     * @param {object} state - snapshot from SessionStateManager.
     */
    update(state) {
      if (!this.container) return;
      state = state || {};

      var isActive = !!state.sessionActive;
      var status = state.status || 'IDLE';
      var isDictating = status === 'DICTATING' || status === 'LISTENING';
      var isPaused = status === 'PAUSED';

      this.isPaused = isPaused;

      // Show controls only during active dictation or paused
      var shouldShow = isActive && (isDictating || isPaused);
      this.container.style.display = shouldShow ? 'block' : 'none';

      if (!shouldShow) return;

      // Update pause/resume button label
      if (isPaused) {
        this.pauseResumeBtn.textContent = '▶ 继续';
        this.pauseResumeBtn.setAttribute('aria-label', '继续听写');
        this.pauseResumeBtn.style.background = '#dbeafe';
        this.pauseResumeBtn.style.borderColor = '#3b82f6';
      } else {
        this.pauseResumeBtn.textContent = '⏸ 暂停';
        this.pauseResumeBtn.setAttribute('aria-label', '暂停听写');
        this.pauseResumeBtn.style.background = '#fff';
        this.pauseResumeBtn.style.borderColor = '#e5e7eb';
      }

      // Disable next button if waiting for parent
      this.nextBtn.disabled = (status === 'WAITING_PARENT');
    }
  }

  /**
   * VoiceCommandButton — push-to-talk mic control for voice commands
   * (Ticket 03). Hold to record, release to ship: the
   * VoiceCommandController streams PCM frames while held and sends the
   * voice_command trigger on release; the backend replies voice_result,
   * which handleResult() renders as a heard/transcript hint. Action "none"
   * or an ASR failure shows the retry hint — the DictationControls buttons
   * remain the full fallback, so this component HIDES itself entirely when
   * the controller reports !supported (no getUserMedia: insecure context,
   * denied permission, unsupported browser).
   *
   * Kept next to DictationControls because it shares the 60px-touch-target
   * styling and the "labels follow authoritative backend state" rule: the
   * FSM effect of a command arrives as regular state frames (paused/word
   * audio), never from this component.
   *
   * @class
   */
  class VoiceCommandButton {
    /**
     * @param {HTMLElement} container - mount point (#voice-command-container).
     * @param {SessionStateManager} stateManager - authoritative session state.
     * @param {VoiceCommandController} controller - mic capture controller.
     */
    constructor(container, stateManager, controller) {
      if (!container) throw new Error('VoiceCommandButton requires a container element');
      this.container = container;
      this.stateManager = stateManager;
      this.controller = controller;

      this.micBtn = null;
      this.statusText = null;
      /** Last voice_result payload (kept for re-renders). */
      this.lastResult = null;

      this.render();
    }

    /**
     * Build the mic button + status line. The button uses Pointer Events
     * (iPad Safari 13+) so one code path covers touch/mouse/pen; Space/Enter
     * via keydown/keyup (no repeat) mirrors it for keyboard users.
     * Rendering is skipped when the controller is unsupported — the button
     * fallback owns that mode entirely.
     */
    render() {
      while (this.container.firstChild) {
        this.container.removeChild(this.container.firstChild);
      }
      if (!this.controller || !this.controller.supported) {
        this.micBtn = null;
        this.statusText = null;
        return;
      }

      var wrapper = document.createElement('div');
      wrapper.className = 'voice-command';
      wrapper.setAttribute('role', 'group');
      wrapper.setAttribute('aria-label', '语音命令');
      wrapper.style.cssText = 'display:flex;flex-direction:column;align-items:center;gap:8px;padding:16px 0;';

      var self = this;
      this.micBtn = document.createElement('button');
      this.micBtn.type = 'button';
      this.micBtn.className = 'kid-button voice-command-button';
      this.micBtn.textContent = '🎙 按住说话';
      this.micBtn.setAttribute('aria-label', '按住说话，松开发送语音命令');
      // iOS HIG touch-target parity with DictationControls (60px minimum);
      // touch-action:none keeps Safari from synthesizing scrolls/long-press
      // menus out of the hold gesture.
      this.micBtn.style.cssText = 'min-width:160px;min-height:60px;font-size:18px;padding:12px 24px;border-radius:30px;border:2px solid #93c5fd;background:#eff6ff;color:#1d4ed8;cursor:pointer;touch-action:none;user-select:none;-webkit-user-select:none;';
      this.micBtn.addEventListener('pointerdown', function (e) { self._onHoldStart(e); });
      this.micBtn.addEventListener('pointerup', function (e) { self._onHoldEnd(e); });
      this.micBtn.addEventListener('pointercancel', function () { self._onHoldCancel(); });
      this.micBtn.addEventListener('pointerleave', function (e) { self._onHoldEnd(e); });
      // Suppress the context menu a long hold triggers on touch devices.
      this.micBtn.addEventListener('contextmenu', function (e) { e.preventDefault(); });
      // Keyboard parity: hold Space/Enter, release to send.
      this.micBtn.addEventListener('keydown', function (e) {
        if ((e.key === ' ' || e.key === 'Enter') && !e.repeat) {
          e.preventDefault();
          self._onHoldStart(e);
        }
      });
      this.micBtn.addEventListener('keyup', function (e) {
        if (e.key === ' ' || e.key === 'Enter') {
          e.preventDefault();
          self._onHoldEnd(e);
        }
      });

      this.statusText = document.createElement('p');
      this.statusText.className = 'voice-command-status';
      this.statusText.setAttribute('role', 'status');
      this.statusText.setAttribute('aria-live', 'polite');
      this.statusText.style.cssText = 'min-height:24px;margin:0;font-size:16px;color:#6b7280;';
      this.statusText.textContent = '可以说：好了 / 重读 / 暂停 / 不会写';

      wrapper.appendChild(this.micBtn);
      wrapper.appendChild(this.statusText);
      this.container.appendChild(wrapper);

      if (this.stateManager) {
        this.update(this.stateManager.getState());
      }
    }

    /**
     * State-driven re-render (called via stateManager.subscribe). Enables the
     * mic only during dictation-eligible statuses; keeps the label synced
     * with the controller's recording/processing lifecycle. A backend error
     * arriving while a command is pending also clears the processing lock —
     * the disabled-mode "voice commands unavailable" error frame is a reply
     * voice_result never comes for.
     *
     * @param {object} state - SessionStateManager snapshot.
     */
    update(state) {
      if (!this.micBtn || !this.controller) return;
      state = state || {};

      if (state.lastError && this.controller.isProcessing) {
        this.controller.resultReceived();
        this._setStatus('语音命令不可用：' + state.lastError + '（请用下面的按钮）');
      }

      var active = state.sessionActive &&
        (state.status === 'DICTATING' || state.status === 'LISTENING' || state.status === 'PAUSED');
      var busy = this.controller.isRecording || this.controller.isProcessing;
      this.micBtn.disabled = !active || busy;
      this.micBtn.style.opacity = this.micBtn.disabled ? '0.5' : '1';

      if (this.controller.isRecording) {
        this.micBtn.textContent = '🎙 录音中…松开发送';
      } else if (this.controller.isProcessing) {
        this.micBtn.textContent = '⏳ 识别中…';
      } else {
        this.micBtn.textContent = '🎙 按住说话';
      }
    }

    /**
     * Render a voice_result payload (called from app.js's handler; also
     * unlocks the controller's processing lock).
     *
     * @param {object} result - {action, transcript, source, confidence}.
     */
    handleResult(result) {
      this.lastResult = result;
      if (this.controller) this.controller.resultReceived();
      var labels = {
        next_word: '下一个 ⏭',
        repeat: '再读一遍 🔁',
        pause: '暂停 ⏸',
        resume: '继续 ▶',
        cannot_write: '跳过这个词 ✏️'
      };
      if (result && labels[result.action]) {
        this._setStatus('听到了「' + (result.transcript || '') + '」→ ' + labels[result.action]);
      } else {
        // Action "none" (or unrecognized): the acceptance-critical fallback
        // hint — try again or use the buttons.
        this._setStatus('没听清，请再试一次，或点下面的按钮');
      }
      if (this.stateManager) this.update(this.stateManager.getState());
    }

    /**
     * Surface a mic-capture failure hint (called from app.js's onError
     * wiring). Kept local to this component — a mic problem must not paint
     * the session-level error surfaces, which lastError drives.
     *
     * @param {string} message - user-facing Chinese hint.
     */
    showError(message) {
      this._setStatus(message);
      if (this.stateManager) this.update(this.stateManager.getState());
    }

    /**
     * Hold started (pointerdown / keydown): begin capture. The permission
     * prompt resolves after the gesture; failures surface via the
     * controller's onError callback (wired by app.js).
     *
     * @private
     * @param {Event} e
     */
    _onHoldStart(e) {
      if (e.cancelable) e.preventDefault();
      if (this.micBtn.disabled) return;
      this.controller.start();
      this.update(this.stateManager ? this.stateManager.getState() : {});
    }

    /**
     * Hold ended normally (pointerup / pointerleave / keyup): stop + ship.
     * @private
     * @param {Event} e
     */
    _onHoldEnd(e) {
      if (e.cancelable) e.preventDefault();
      if (!this.controller.isRecording) return;
      this.controller.stop();
      this.update(this.stateManager ? this.stateManager.getState() : {});
    }

    /**
     * Hold aborted (browser cancelled the gesture): drop the recording
     * without triggering ASR.
     * @private
     */
    _onHoldCancel() {
      if (!this.controller.isRecording) return;
      this.controller.cancel();
      this._setStatus('录音取消 — 请再试一次，或点下面的按钮');
      this.update(this.stateManager ? this.stateManager.getState() : {});
    }

    /**
     * @private
     */
    _setStatus(text) {
      if (this.statusText) this.statusText.textContent = text;
    }
  }

  /**
   * PhotoAnswerButton — the 拍照提交 affordance (Ticket 04).
   *
   * One tap opens the iPad camera via the hidden file input inside
   * PhotoAnswerController; the picked photo ships automatically and a
   * photo_result frame arrives separately (judged / low-confidence /
   * failed). This component only opens the picker and tracks the
   * processing lock — the outcome renders through the shared dictation
   * feedback and the ManualAnswerForm, never here.
   *
   * Rendering is skipped when the controller reports !supported (no canvas
   * imaging) — the manual input and buttons remain the fallback.
   *
   * @class
   */
  class PhotoAnswerButton {
    /**
     * @param {HTMLElement} container - mount point (#photo-answer-container).
     * @param {SessionStateManager} stateManager - authoritative session state.
     * @param {PhotoAnswerController} controller - photo capture controller.
     * @param {function} onPick - called on tap, before the picker opens
     *        (app.js stops speech recognition there: the photo path replaces
     *        the auto-started mic answer for this word).
     */
    constructor(container, stateManager, controller, onPick) {
      if (!container) throw new Error('PhotoAnswerButton requires a container element');
      this.container = container;
      this.stateManager = stateManager;
      this.controller = controller;
      this.onPick = typeof onPick === 'function' ? onPick : function () {};

      this.photoBtn = null;
      this.statusText = null;

      this.render();
    }

    /**
     * Build the camera button + status line. iOS HIG 60px touch-target
     * parity with the mic/button controls.
     */
    render() {
      while (this.container.firstChild) {
        this.container.removeChild(this.container.firstChild);
      }
      if (!this.controller || !this.controller.supported) {
        this.photoBtn = null;
        this.statusText = null;
        return;
      }

      var wrapper = document.createElement('div');
      wrapper.className = 'photo-answer';
      wrapper.setAttribute('role', 'group');
      wrapper.setAttribute('aria-label', '拍照提交答案');
      wrapper.style.cssText = 'display:flex;flex-direction:column;align-items:center;gap:8px;padding:16px 0;';

      var self = this;
      this.photoBtn = document.createElement('button');
      this.photoBtn.type = 'button';
      this.photoBtn.className = 'kid-button photo-answer-button';
      this.photoBtn.textContent = '📷 拍照提交';
      this.photoBtn.setAttribute('aria-label', '把写好的答案拍照上传');
      this.photoBtn.style.cssText = 'min-width:160px;min-height:60px;font-size:18px;padding:12px 24px;border-radius:30px;border:2px solid #86efac;background:#f0fdf4;color:#15803d;cursor:pointer;touch-action:manipulation;user-select:none;-webkit-user-select:none;';
      this.photoBtn.addEventListener('click', function () {
        if (self.controller.isProcessing) return;
        self.onPick();
        self.controller.pickPhoto();
        self.update(self.stateManager ? self.stateManager.getState() : {});
      });

      this.statusText = document.createElement('p');
      this.statusText.className = 'photo-answer-status';
      this.statusText.setAttribute('role', 'status');
      this.statusText.setAttribute('aria-live', 'polite');
      this.statusText.style.cssText = 'min-height:24px;margin:0;font-size:16px;color:#6b7280;';
      this.statusText.textContent = '写好后拍下来，自动识别';

      wrapper.appendChild(this.photoBtn);
      wrapper.appendChild(this.statusText);
      this.container.appendChild(wrapper);

      if (this.stateManager) {
        this.update(this.stateManager.getState());
      }
    }

    /**
     * State-driven re-render. Photo answering is enabled exactly while the
     * spoken path is (DICTATING/LISTENING); the FSM stays untouched —
     * photo_result only feeds the shared feedback flow.
     *
     * @param {object} state - SessionStateManager snapshot.
     */
    update(state) {
      if (!this.photoBtn || !this.controller) return;
      state = state || {};

      if (state.lastError && this.controller.isProcessing) {
        this.controller.resultReceived();
        this._setStatus('拍照提交不可用：' + state.lastError + '（请手动输入）');
      }

      var active = state.sessionActive &&
        (state.status === 'DICTATING' || state.status === 'LISTENING');
      this.photoBtn.disabled = !active || this.controller.isProcessing;
      this.photoBtn.style.opacity = this.photoBtn.disabled ? '0.5' : '1';

      if (this.controller.isProcessing) {
        this.photoBtn.textContent = '⏳ 识别中…';
      } else {
        this.photoBtn.textContent = '📷 拍照提交';
      }
    }

    /**
     * Surface a capture/encode failure in the local status line.
     * @param {string} message - already-localized hint text.
     */
    showError(message) {
      this._setStatus(message);
    }

    /**
     * @private
     */
    _setStatus(text) {
      if (this.statusText) this.statusText.textContent = text;
    }
  }

  /**
   * ManualAnswerForm — the manual-input fallback (Ticket 04).
   *
   * Shown when OCR is low-confidence or fails (photo_result), and always
   * available to the keyboard answer path: a prefilled text input + submit
   * button that sends submit_answer (the existing spoken-answer wire path —
   * the reply is the same answer_result feedback). Event-driven, not
   * state-driven: app.js shows/hides it around the photo flow and on session
   * end, so there is no stateManager subscription here.
   *
   * The 16px input font is the iOS zoom-on-focus defense (see index.html
   * viewport note); the 60px submit button keeps HIG touch-target parity.
   *
   * @class
   */
  class ManualAnswerForm {
    /**
     * @param {HTMLElement} container - mount point (#manual-answer-container).
     * @param {function} onSubmit - called with the typed text on 提交.
     */
    constructor(container, onSubmit) {
      if (!container) throw new Error('ManualAnswerForm requires a container element');
      this.container = container;
      this.onSubmit = typeof onSubmit === 'function' ? onSubmit : function () {};

      this.form = null;
      this.input = null;
      this.hintText = null;

      this.render();
      this.hide();
    }

    /**
     * Build the hidden form: hint line + input + submit.
     */
    render() {
      var self = this;
      this.form = document.createElement('form');
      this.form.className = 'manual-answer-form';
      this.form.setAttribute('role', 'group');
      this.form.setAttribute('aria-label', '手动输入答案');
      this.form.style.cssText = 'display:flex;flex-direction:column;align-items:center;gap:10px;padding:16px 0;';

      this.hintText = document.createElement('p');
      this.hintText.className = 'manual-answer-hint';
      this.hintText.setAttribute('role', 'status');
      this.hintText.setAttribute('aria-live', 'polite');
      this.hintText.style.cssText = 'min-height:22px;margin:0;font-size:16px;color:#92400e;';
      this.hintText.textContent = '请输入你写的答案';

      var row = document.createElement('div');
      row.style.cssText = 'display:flex;gap:10px;align-items:center;flex-wrap:wrap;justify-content:center;';

      this.input = document.createElement('input');
      this.input.type = 'text';
      this.input.className = 'manual-answer-input';
      this.input.setAttribute('aria-label', '答案');
      this.input.setAttribute('autocomplete', 'off');
      this.input.setAttribute('autocapitalize', 'none');
      this.input.setAttribute('autocorrect', 'off');
      // 16px font is the iOS zoom-on-focus defense.
      this.input.style.cssText = 'font-size:16px;min-height:48px;padding:8px 16px;border-radius:14px;border:2px solid #fcd34d;width:min(280px, 70vw);text-align:center;';

      var submitBtn = document.createElement('button');
      submitBtn.type = 'submit';
      submitBtn.className = 'kid-button manual-answer-submit';
      submitBtn.textContent = '✓ 提交';
      submitBtn.style.cssText = 'min-height:60px;min-width:96px;font-size:18px;padding:12px 20px;border-radius:30px;border:2px solid #fcd34d;background:#fffbeb;color:#92400e;cursor:pointer;touch-action:manipulation;';

      this.form.addEventListener('submit', function (e) {
        e.preventDefault();
        var text = (self.input.value || '').trim();
        if (!text) {
          self.setHint('先写下答案再提交哦');
          return;
        }
        self.onSubmit(text);
      });

      row.appendChild(this.input);
      row.appendChild(submitBtn);
      this.form.appendChild(this.hintText);
      this.form.appendChild(row);
      this.container.appendChild(this.form);
    }

    /**
     * Show the form, optionally prefilled with the OCR best guess.
     * @param {string} [prefill] - OCR text to correct (empty when OCR failed).
     */
    show(prefill) {
      this.input.value = prefill || '';
      this.form.style.display = 'flex';
      try { this.input.focus(); } catch (e) { /* focus is best-effort */ }
    }

    /**
     * Hide the form and clear the input.
     */
    hide() {
      this.input.value = '';
      this.form.style.display = 'none';
    }

    /**
     * Update the hint line (why manual input is showing / what to fix).
     * @param {string} text
     */
    setHint(text) {
      if (this.hintText) this.hintText.textContent = text;
    }
  }

  /**
   * BatchSubmissionForm — the 写完一起交 affordance (Ticket 05).
   *
   * Renders one numbered row per dictation word (number only — showing the
   * word would give the answer away): a text input plus a 📷 button whose
   * photo_ocr result fills the row. Submitting sends submit_answers with
   * {word: text}; the batch_result reply renders per-row ✓/✗ marks
   * (the "progress indicators during submission"), and session_complete
   * (FEEDBACK) ends the flow through the existing path.
   *
   * Rows are index-keyed and mapped to words at submit time via
   * buildAnswers() — the mapping lives in one pure, Node-tested function.
   *
   * @class
   */
  class BatchSubmissionForm {
    /**
     * @param {HTMLElement} container - mount point (#batch-submission-container).
     * @param {function} onSubmit - called with the answers map on 提交全部.
     * @param {function} onPickOCR - called with the row index when its 📷 is
     *        tapped (app.js points the PhotoAnswerController at photo_ocr).
     */
    constructor(container, onSubmit, onPickOCR) {
      if (!container) throw new Error('BatchSubmissionForm requires a container element');
      this.container = container;
      this.onSubmit = typeof onSubmit === 'function' ? onSubmit : function () {};
      this.onPickOCR = typeof onPickOCR === 'function' ? onPickOCR : function () {};

      this.form = null;
      this.inputs = [];
      this.rowMarks = [];
      this.hintText = null;
      this.words = [];
      this.submitted = false;

      this.render([]);
      this.hide();
    }

    /**
     * Build the form skeleton: hint + rows + submit button. Rows are
     * (re)built by show(words).
     */
    render() {
      var self = this;
      this.form = document.createElement('form');
      this.form.className = 'batch-submission-form';
      this.form.setAttribute('role', 'group');
      this.form.setAttribute('aria-label', '批量提交答案');
      this.form.style.cssText = 'display:flex;flex-direction:column;align-items:center;gap:12px;padding:16px 0;';

      this.hintText = document.createElement('p');
      this.hintText.className = 'batch-submission-hint';
      this.hintText.setAttribute('role', 'status');
      this.hintText.setAttribute('aria-live', 'polite');
      this.hintText.style.cssText = 'min-height:22px;margin:0;font-size:16px;color:#1d4ed8;';
      this.hintText.textContent = '把每个词的答案填进来（可以拍照识别），全部填好后一起提交';

      this.rowsHost = document.createElement('div');
      this.rowsHost.style.cssText = 'display:flex;flex-direction:column;gap:10px;max-height:44vh;overflow-y:auto;width:min(420px, 92vw);';

      this.submitBtn = document.createElement('button');
      this.submitBtn.type = 'submit';
      this.submitBtn.className = 'kid-button batch-submission-submit';
      this.submitBtn.textContent = '✅ 提交全部答案';
      this.submitBtn.style.cssText = 'min-height:60px;min-width:220px;font-size:18px;padding:12px 24px;border-radius:30px;border:2px solid #93c5fd;background:#eff6ff;color:#1d4ed8;cursor:pointer;touch-action:manipulation;';

      this.form.addEventListener('submit', function (e) {
        e.preventDefault();
        if (self.submitted) return;
        var answers = BatchSubmissionForm.buildAnswers(self.words, self.values());
        if (!answers) {
          self.setHint('至少填一个答案哦（没写出来的可以空着）');
          return;
        }
        self.submitted = true;
        self.submitBtn.disabled = true;
        self.submitBtn.textContent = '⏳ 批改中…';
        self.setHint('正在批改…');
        self.onSubmit(answers);
      });

      this.form.appendChild(this.hintText);
      this.form.appendChild(this.rowsHost);
      this.form.appendChild(this.submitBtn);
      this.container.appendChild(this.form);
    }

    /**
     * Rebuild the rows for a word list and reveal the form.
     * @param {string[]} words - the session's dictation queue
     */
    show(words) {
      this.words = (words || []).slice();
      this.submitted = false;
      this.submitBtn.disabled = false;
      this.submitBtn.textContent = '✅ 提交全部答案';
      this.setHint('把每个词的答案填进来（可以拍照识别），全部填好后一起提交');

      var self = this;
      this.rowsHost.textContent = '';
      this.inputs = [];
      this.rowMarks = [];
      for (var i = 0; i < this.words.length; i++) {
        this.inputs.push(null);
        this.rowMarks.push(null);
      }
      this.words.forEach(function (word, i) {
        var row = document.createElement('div');
        row.className = 'batch-row';
        row.style.cssText = 'display:flex;align-items:center;gap:10px;';

        var label = document.createElement('span');
        label.textContent = '第 ' + (i + 1) + ' 题';
        label.style.cssText = 'min-width:64px;font-size:16px;font-weight:600;color:#374151;';

        var input = document.createElement('input');
        input.type = 'text';
        input.setAttribute('aria-label', '第 ' + (i + 1) + ' 题答案');
        input.setAttribute('autocomplete', 'off');
        input.setAttribute('autocapitalize', 'none');
        input.setAttribute('autocorrect', 'off');
        input.style.cssText = 'flex:1;font-size:16px;min-height:48px;padding:8px 12px;border-radius:12px;border:2px solid #d1d5db;text-align:center;';

        var camBtn = document.createElement('button');
        camBtn.type = 'button';
        camBtn.textContent = '📷';
        camBtn.setAttribute('aria-label', '拍照识别第 ' + (i + 1) + ' 题答案');
        camBtn.style.cssText = 'min-height:48px;min-width:48px;font-size:20px;border-radius:12px;border:2px solid #86efac;background:#f0fdf4;cursor:pointer;touch-action:manipulation;';
        camBtn.addEventListener('click', function () {
          self.onPickOCR(i);
        });

        var mark = document.createElement('span');
        mark.style.cssText = 'min-width:26px;font-size:18px;text-align:center;';
        row.appendChild(label);
        row.appendChild(input);
        row.appendChild(camBtn);
        row.appendChild(mark);
        self.rowsHost.appendChild(row);
        self.inputs[i] = input;
        self.rowMarks[i] = mark;
      });

      this.form.style.display = 'flex';
    }

    /**
     * Hide the form.
     */
    hide() {
      this.form.style.display = 'none';
    }

    /**
     * Whether the form is currently revealed.
     * @returns {boolean}
     */
    isVisible() {
      return this.form.style.display !== 'none';
    }

    /**
     * Collect the current input values (index-keyed).
     * @returns {string[]}
     */
    values() {
      return this.inputs.map(function (input) { return input ? input.value : ''; });
    }

    /**
     * Fill one row from a photo_ocr result.
     * @param {number} index - row index
     * @param {object} result - photo_ocr_result payload
     */
    fillRow(index, result) {
      if (index < 0 || index >= this.inputs.length) return;
      if (result.error) {
        this.setHint('第 ' + (index + 1) + ' 题：' + result.error);
        return;
      }
      if (result.text) {
        this.inputs[index].value = result.text;
      }
      if (result.low_confidence) {
        this.setHint('第 ' + (index + 1) + ' 题不太确定（' + Math.round((result.confidence || 0) * 100) + '%），请核对');
      }
    }

    /**
     * Render the batch_result judgments: per-row ✓/✗ + heard text + summary
     * hint (the submission progress indicator).
     * @param {object} result - batch_result payload
     */
    showResults(result) {
      var self = this;
      (result.results || []).forEach(function (r, i) {
        if (i >= self.rowMarks.length) return;
        var mark = self.rowMarks[i];
        mark.textContent = r.correct ? '✅' : '❌';
        mark.title = r.heard ? '你写的是：' + r.heard : '没有作答';
        if (self.inputs[i]) {
          self.inputs[i].disabled = true;
        }
      });
      this.submitBtn.textContent = '批改完成';
      this.setHint('批改完成：' + (result.correct_count || 0) + ' / ' + (result.total || this.words.length) + ' 正确');
    }

    /**
     * Update the hint line.
     * @param {string} text
     */
    setHint(text) {
      if (this.hintText) this.hintText.textContent = text;
    }

    /**
     * Map index-keyed input values onto the word-keyed answers map for
     * submit_answers. Pure/static so Node tests pin the contract: entries
     * are trimmed, empty entries are omitted (a blank grades incorrect
     * server-side), and an all-empty submission returns null (client-side
     * validation BEFORE the wire).
     *
     * @param {string[]} words - session word queue
     * @param {string[]} values - index-keyed input values
     * @returns {Object<string,string>|null}
     */
    static buildAnswers(words, values) {
      var answers = {};
      var any = false;
      for (var i = 0; i < (words || []).length; i++) {
        var text = ((values && values[i]) || '').trim();
        if (text === '') continue;
        answers[words[i]] = text;
        any = true;
      }
      return any ? answers : null;
    }
  }

  global.DictationControls = DictationControls;
  global.VoiceCommandButton = VoiceCommandButton;
  global.PhotoAnswerButton = PhotoAnswerButton;
  global.ManualAnswerForm = ManualAnswerForm;
  global.BatchSubmissionForm = BatchSubmissionForm;
})(typeof window !== 'undefined' ? window : this);
