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
  var LABEL_COMPLETE = '完成啦！再来一组？';

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
     *   - FEEDBACK (session ended)  → "完成啦！再来一组？" (the wrap-up
     *     invitation — 2026-09-24: previously unreachable, so the button
     *     snapped back to the idle label the moment the report arrived)
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

      if (state && state.status === 'FEEDBACK') {
        // Wrap-up: the report is on screen; invite another round instead of
        // pretending nothing happened.
        this._lastSeenError = currentError;
        this.applyVisual(LABEL_COMPLETE, false);
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
  // echoes) maps to a short, student-readable string. These appear in the
  // 22px status-text element on iPad per the plan's readability target.
  // 2026-09-24 redesign: plain Chinese a 6-12 year old parses at a glance —
  // the status line says WHAT is happening, the sub-hint below says WHOSE
  // turn it is.
  var PROGRESS_STATUS_MESSAGES = {
    IDLE: '准备好啦',
    LISTENING: '正在读词…',
    DICTATING: '听写进行中',
    PAUSED: '已暂停',
    WAITING_PARENT: '本次听写结束',
    SUBMISSION: '正在批改…',
    FEEDBACK: '成绩出来啦！'
  };
  var PROGRESS_ERROR_MESSAGE = '出了点问题';

  // WAITING_PARENT is mode-aware (B1/B2 fix): the static map above keeps a
  // mode-agnostic fallback, but the actionable hint depends on how the
  // session was answered — batch students must fill and submit the form
  // (visible at this status), per-word students are auto-completing while
  // the review is submitted and the summary is generated.
  var PROGRESS_WAITING_PARENT_MESSAGES = {
    batch: '本次听写结束 —— 请填写答案并提交',
    perWord: '本次听写结束，正在生成小结…'
  };

  // Per-status icon (2026-09-24 redesign): one glance tells the student
  // which moment of the session they are in. Rendered in .status-icon.
  var PROGRESS_STATUS_ICONS = {
    IDLE: '📝',
    LISTENING: '👂',
    DICTATING: '🎧',
    PAUSED: '⏸',
    WAITING_PARENT: '🏁',
    SUBMISSION: '⏳',
    FEEDBACK: '🎉'
  };

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
      this.statusIcon = null;
      this.statusText = null;
      this.playPhaseEl = null;       // "第一遍/第二遍/轮到你了" rhythm chip
      this.subHintEl = null;         // whose-turn-it-is guidance line
      this.progressContainer = null; // the role=progressbar outer track
      this.progressFill = null;      // the gradient inner fill
      this.progressText = null;      // "已完成 X / Y 个词" counter
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
      this.statusIcon = document.createElement('span');
      this.statusIcon.className = 'status-icon';
      this.statusIcon.setAttribute('aria-hidden', 'true');
      this.statusIcon.textContent = PROGRESS_STATUS_ICONS.IDLE;
      this.statusText = document.createElement('span');
      this.statusText.className = 'status-text';
      // aria-live on the row so status transitions are announced together.
      this.statusRow.setAttribute('aria-live', 'polite');
      this.statusRow.appendChild(this.statusIcon);
      this.statusRow.appendChild(this.statusText);

      // Rhythm chip (2026-09-24): "📢 第一遍 / 📢 第二遍 / 🎤 轮到你了 /
      // ✍️ 写下来" — the dictation beat made visible. Hidden when idle.
      this.playPhaseEl = document.createElement('div');
      this.playPhaseEl.className = 'play-phase';
      this.playPhaseEl.setAttribute('role', 'status');
      this.playPhaseEl.style.display = 'none';

      // Sub-hint: whose turn it is / what to do next, one short line.
      this.subHintEl = document.createElement('div');
      this.subHintEl.className = 'progress-subhint';

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
      this.container.appendChild(this.playPhaseEl);
      this.container.appendChild(this.progressContainer);
      this.container.appendChild(this.progressText);
      this.container.appendChild(this.subHintEl);
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
      var batchMode = !!state.batchMode;

      // --- Status text + icon ---
      // An error takes priority for the status line so the student sees what
      // went wrong, but we DO NOT reset the progress bar (PLAN Task 3: "On
      // error: show error message in status text, maintain current progress").
      if (lastError) {
        this.statusText.textContent = lastError;
      } else {
        this.statusText.textContent = this._messageForStatus(status, batchMode);
      }
      this.statusIcon.textContent = PROGRESS_STATUS_ICONS.hasOwnProperty(status)
        ? PROGRESS_STATUS_ICONS[status]
        : PROGRESS_STATUS_ICONS.IDLE;

      // --- Rhythm chip (2026-09-24) ---
      // Priority: answering > playing > writing (batch) > hidden. The chip
      // only lives while a session is actively dictating; everything else
      // (idle, paused, wrap-up) hides it so it never lies about the beat.
      this._updatePlayPhase(state, status);

      // --- Sub-hint: whose turn / what next ---
      if (lastError) {
        this.subHintEl.textContent = '';
      } else {
        this.subHintEl.textContent = ProgressDisplay.subHint(status, batchMode);
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

      // --- Word counter ("已完成 X / Y 个词") ---
      this.progressText.textContent = '已完成 ' + completedWords + ' / ' + totalWords + ' 个词';

      // --- Encouragement (milestone praise, fires only on crossing) ---
      this.encouragement.textContent = this._encouragementFor(percent, status);

      this._lastPercent = percent;
    }

    /**
     * Map an FSM status to its student-readable status string. Falls back to
     * the IDLE message for unknown values so the UI never shows "undefined".
     * Thin delegate to the static statusMessage() so the pure resolution
     * logic stays Node-testable without a DOM.
     *
     * @private
     * @param {string} status - one of SessionStateManager.STATUS_*.
     * @param {boolean} batchMode - whether the session is in batch mode
     *        (state.batchMode); picks the WAITING_PARENT hint.
     * @returns {string}
     */
    _messageForStatus(status, batchMode) {
      return ProgressDisplay.statusMessage(status, batchMode);
    }

    /**
     * Render the rhythm chip from the state snapshot (2026-09-24).
     *   1. Answering aloud beats everything ("🎤 轮到你了").
     *   2. While word audio plays: "📢 第一遍" then "📢 第二遍" (double play).
     *   3. In batch mode, playback over + still dictating = writing time
     *      ("✍️ 写下来").
     *   4. Anything else (idle, paused, waiting, feedback, retry prompt):
     *      hidden — the chip never competes with other guidance.
     *
     * @private
     * @param {object} state - SessionStateManager snapshot.
     * @param {string} status - resolved FSM status string.
     */
    _updatePlayPhase(state, status) {
      var chip = this.playPhaseEl;
      if (!chip) return;
      var text = '';
      if (state.sessionActive && (status === 'DICTATING' || status === 'LISTENING')) {
        if (!state.batchMode && state.dictationState === 'listening') {
          text = '🎤 轮到你了';
        } else if (state.playPhase === 1) {
          text = '📢 第一遍';
        } else if (state.playPhase === 2) {
          text = '📢 第二遍';
        } else if (state.batchMode) {
          text = '✍️ 写下来';
        }
      }
      if (text) {
        chip.textContent = text;
        chip.style.display = '';
      } else {
        chip.style.display = 'none';
      }
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
        return '🎉 完成啦，你真棒！';
      }
      if (status === 'PAUSED') {
        return '⏸ 休息一下';
      }
      // Only fire encouragement on a milestone crossing to avoid re-praising
      // on every word_complete / progress_update.
      var prev = this._lastPercent;
      if (percent >= 75 && prev < 75) return '⭐ 就快完成了！';
      if (percent >= 50 && prev < 50) return '👍 完成一半啦！';
      if (percent >= 25 && prev < 25) return '😊 好的开始！';
      return '';
    }
  }

  // Expose the status maps + error message for tests / introspection.
  ProgressDisplay.STATUS_MESSAGES = PROGRESS_STATUS_MESSAGES;
  ProgressDisplay.WAITING_PARENT_MESSAGES = PROGRESS_WAITING_PARENT_MESSAGES;
  ProgressDisplay.STATUS_ICONS = PROGRESS_STATUS_ICONS;
  ProgressDisplay.ERROR_MESSAGE = PROGRESS_ERROR_MESSAGE;

  /**
   * Resolve the status-line text for a state snapshot. Pure/static so Node
   * tests pin the mode-aware WAITING_PARENT contract (batch → fill-the-form
   * hint, per-word → auto-completing hint) without a DOM. All other statuses
   * fall through to the static map; unknown values fall back to IDLE.
   *
   * @param {string} status - one of SessionStateManager.STATUS_*.
   * @param {boolean} batchMode - state.batchMode for WAITING_PARENT.
   * @returns {string}
   */
  ProgressDisplay.statusMessage = function (status, batchMode) {
    if (status === 'WAITING_PARENT') {
      return batchMode
        ? PROGRESS_WAITING_PARENT_MESSAGES.batch
        : PROGRESS_WAITING_PARENT_MESSAGES.perWord;
    }
    if (PROGRESS_STATUS_MESSAGES.hasOwnProperty(status)) {
      return PROGRESS_STATUS_MESSAGES[status];
    }
    return PROGRESS_STATUS_MESSAGES.IDLE;
  };

  /**
   * Sub-hint line: whose turn it is and what to do next (2026-09-24
   * redesign). Mode-aware for DICTATING/WAITING_PARENT; pure/static so Node
   * tests pin the copy contract without a DOM. '' means "no hint needed".
   *
   * @param {string} status - one of SessionStateManager.STATUS_*.
   * @param {boolean} batchMode - state.batchMode.
   * @returns {string}
   */
  ProgressDisplay.subHint = function (status, batchMode) {
    switch (status) {
      case 'LISTENING':
      case 'DICTATING':
        return batchMode
          ? '仔细听，把每个词写在纸上'
          : '仔细听，听完大声说出这个词';
      case 'PAUSED':
        return '点「▶ 继续」接着听写';
      case 'WAITING_PARENT':
        return batchMode
          ? '把纸上写的答案填进下面的表格，填完一起提交'
          : '成绩马上出来，请稍等';
      case 'SUBMISSION':
        return '正在批改…';
      case 'FEEDBACK':
        return '看看下面的听写报告';
      default:
        return '';
    }
  };

  global.ProgressDisplay = ProgressDisplay;

  // ==================================================================
  // DictationFeedback — recording indicator + correct/wrong feedback
  // (Phase 8.1, dictation closed loop).
  // ==================================================================

  var DICTATION_FEEDBACK_MESSAGES = {
    listening: '轮到你了——大声说出刚听到的词',
    correct: '答对啦！',
    incorrect: '✗'
  };

  /**
   * DictationFeedback renders the per-word dictation feedback UI:
   * recording indicator, correct/incorrect verdict, and the expected
   * answer when wrong.
   *
   * 2026-09-24 redesign:
   *   - The answer word is NEVER shown while listening or waiting for a
   *     retry — displaying it would give the dictation answer away (the
   *     batch form hides words for the same reason). It only appears in
   *     the correct/incorrect verdict, after the student has answered.
   *   - New 'retry' state (replacing the old auto-advancing 'skipped'):
   *     a gentle guidance card, not an error strip — a child not speaking
   *     up is normal. It points at the repeat/next buttons and stays up
   *     until the student acts.
   *   - All styling lives in main.css (.df-card variants); no inline
   *     cssText, no JS-injected keyframes.
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
      this.iconEl = null;
      this.wordDisplayEl = null;
      this.heardEl = null;

      this.render();
    }

    render() {
      while (this.container.firstChild) {
        this.container.removeChild(this.container.firstChild);
      }

      // Main feedback card (variant class swapped per state in update()).
      this.feedbackEl = document.createElement('div');
      this.feedbackEl.className = 'dictation-feedback df-card';
      this.feedbackEl.setAttribute('aria-live', 'assertive');

      this.iconEl = document.createElement('div');
      this.iconEl.className = 'df-icon';
      this.iconEl.setAttribute('aria-hidden', 'true');

      // Word display — verdicts only (never while the answer is still due).
      this.wordDisplayEl = document.createElement('div');
      this.wordDisplayEl.className = 'dictation-word df-word';

      // Feedback message / guidance text.
      this.heardEl = document.createElement('div');
      this.heardEl.className = 'dictation-heard df-message';

      this.feedbackEl.appendChild(this.iconEl);
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
      var expected = state.lastExpected;
      var heard = state.lastHeard;

      // Show/hide based on dictation state
      if (!dictationState || !state.sessionActive) {
        this.feedbackEl.style.display = 'none';
        return;
      }
      this.feedbackEl.style.display = 'flex';

      var word = '';
      var icon = '';
      var msg = '';
      switch (dictationState) {
        case 'listening':
          this.feedbackEl.className = 'dictation-feedback df-card df-listening';
          icon = '🎤';
          msg = DICTATION_FEEDBACK_MESSAGES.listening;
          break;

        case 'correct':
          this.feedbackEl.className = 'dictation-feedback df-card df-correct';
          icon = '✓';
          word = expected || '';
          msg = DICTATION_FEEDBACK_MESSAGES.correct;
          break;

        case 'incorrect':
          this.feedbackEl.className = 'dictation-feedback df-card df-incorrect';
          icon = '✗';
          word = expected || '';
          msg = heard ? '你写的是：' + heard : '';
          break;

        case 'retry':
          // Silence / recognition trouble: stay put, guide gently — the
          // student advances explicitly via 🔁 / 📷 / ⏭ (no auto-advance).
          this.feedbackEl.className = 'dictation-feedback df-card df-retry';
          icon = '💡';
          msg = heard || '没听到声音——点 🔁 再听一遍，或点 ⏭ 下一个';
          break;

        default:
          this.feedbackEl.style.display = 'none';
          return;
      }

      this.iconEl.textContent = icon;
      if (word) {
        this.wordDisplayEl.textContent = word;
        this.wordDisplayEl.style.display = 'block';
      } else {
        this.wordDisplayEl.style.display = 'none';
      }
      this.heardEl.textContent = msg;
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

      // Pause / Resume button (toggle)
      this.pauseResumeBtn = this._createButton('⏸ 暂停', this._handlePauseResume.bind(this));
      this.pauseResumeBtn.setAttribute('aria-label', '暂停听写');

      // Repeat button
      this.repeatBtn = this._createButton('🔁 再读一遍', this._handleRepeat.bind(this));
      this.repeatBtn.setAttribute('aria-label', '再读一遍当前单词');
      this.repeatBtn.classList.add('dictation-control-button--repeat');

      // Next word button
      this.nextBtn = this._createButton('⏭ 下一个', this._handleNext.bind(this));
      this.nextBtn.setAttribute('aria-label', '跳到下一个单词');
      this.nextBtn.classList.add('dictation-control-button--next');

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
     * its textContent to label, binds the click listener, and returns the
     * element. Sizing/visuals live in main.css (.dictation-control-button)
     * so the 60px iOS HIG touch target is themeable.
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
      btn.addEventListener('click', onClick);
      return btn;
    }

    /**
     * Stop the in-flight dictation speech recognition, if any (the global
     * SpeechController app.js exposes). Needed before any control that
     * replaces the current word's turn — otherwise a late recognition
     * result would be judged against the WRONG (next) word server-side.
     * @private
     */
    _stopDictationListening() {
      if (global.speechController && typeof global.speechController.stopListening === 'function') {
        global.speechController.stopListening();
      }
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
     * Pausing also stops the in-flight recognition (2026-09-24): an answer
     * judged while PAUSED is rejected server-side, and the resume re-stream
     * restarts recognition cleanly at playback end.
     *
     * @private
     */
    _handlePauseResume() {
      if (!this.wsClient || typeof this.wsClient.send !== 'function') return;
      if (this.isPaused) {
        this.wsClient.send({ type: 'resume', params: {} });
      } else {
        this._stopDictationListening();
        this.wsClient.send({ type: 'pause', params: {} });
      }
    }

    /**
     * Repeat button click handler. Sends a "repeat" control frame (params:
     * empty object) asking the backend to re-read the current word.
     * Clears the retry prompt immediately (2026-09-24): the re-stream's
     * playback end restarts recognition, which is the retry closed loop —
     * the prompt's job is done the moment the student asks for a replay.
     * Silently returns when wsClient is missing or its send is not a
     * function.
     *
     * @private
     */
    _handleRepeat() {
      if (!this.wsClient || typeof this.wsClient.send !== 'function') return;
      this._stopDictationListening();
      if (this.stateManager) {
        this.stateManager.setState({ dictationState: null, lastHeard: null });
      }
      this.wsClient.send({ type: 'repeat', params: {} });
    }

    /**
     * Next-word button click handler. Sends the current word_id in a
     * "next_word" control frame, then (2026-09-24 fixes):
     *   - stops the in-flight recognition so a late result cannot be judged
     *     against the word the backend is about to stream;
     *   - clears the retry prompt (the student DID act — this is the
     *     explicit advance the prompt asks for);
     *   - advances currentWordId to the upcoming word (same bookkeeping as
     *     app.js's sendNextWord) so the next playback-end starts
     *     recognition tagged with the right word and the feedback card
     *     never displays the previous one.
     * Silently returns when wsClient is missing or its send is not a
     * function.
     *
     * @private
     */
    _handleNext() {
      if (!this.wsClient || typeof this.wsClient.send !== 'function') return;
      var state = this.stateManager ? this.stateManager.getState() : {};
      var wordId = state.currentWordId || '';
      this._stopDictationListening();
      if (this.stateManager) {
        var updates = { dictationState: null, lastHeard: null };
        var nextIndex = state.completedWords;
        if (Array.isArray(state.words) && nextIndex < state.words.length) {
          updates.currentWordId = state.words[nextIndex];
        }
        this.stateManager.setState(updates);
      }
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

      // Update pause/resume button label (visual variant via .is-paused)
      if (isPaused) {
        this.pauseResumeBtn.textContent = '▶ 继续';
        this.pauseResumeBtn.setAttribute('aria-label', '继续听写');
        this.pauseResumeBtn.classList.add('is-paused');
      } else {
        this.pauseResumeBtn.textContent = '⏸ 暂停';
        this.pauseResumeBtn.setAttribute('aria-label', '暂停听写');
        this.pauseResumeBtn.classList.remove('is-paused');
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
      this.rowsHost = null;
      this.submitBtn = null;
      this.inputs = [];
      this.rowMarks = [];
      this.hintText = null;
      this.words = [];
      this.submitted = false;

      this.render();
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

      // Visible wrap-up title (2026-09-24): the form appearing IS the
      // end-of-dictation moment in batch mode — say so explicitly so it
      // reads as the designed next step, not a surprise.
      this.titleText = document.createElement('p');
      this.titleText.className = 'batch-form-title';
      this.titleText.textContent = '🏁 听写结束，该交卷啦';

      this.hintText = document.createElement('p');
      this.hintText.className = 'batch-submission-hint';
      this.hintText.setAttribute('role', 'status');
      this.hintText.setAttribute('aria-live', 'polite');
      this.hintText.style.cssText = 'min-height:22px;margin:0;font-size:16px;color:#1d4ed8;';
      this.hintText.textContent = BatchSubmissionForm.DEFAULT_HINT;

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

      this.form.appendChild(this.titleText);
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
      this.setHint(BatchSubmissionForm.DEFAULT_HINT);

      var self = this;
      this.rowsHost.textContent = '';
      this.inputs = [];
      this.rowMarks = [];
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
        // 60px touch target (iOS HIG parity with every other button); the
        // compact 52px width keeps dense rows readable.
        camBtn.style.cssText = 'min-height:60px;min-width:52px;font-size:20px;border-radius:12px;border:2px solid #86efac;background:#f0fdf4;cursor:pointer;touch-action:manipulation;';
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
      return this.inputs.map(function (input) { return input.value; });
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
     * Render the batch_result judgments: per-row marks (✅ / 🟡 near-miss /
     * ❌), heard text, and the summary hint (the submission progress
     * indicator). Ticket 06 adds the near-miss tier — still wrong, but the
     * mark tells the student it was one character away.
     * @param {object} result - batch_result payload
     */
    showResults(result) {
      var self = this;
      (result.results || []).forEach(function (r, i) {
        if (i >= self.rowMarks.length) return;
        var mark = self.rowMarks[i];
        if (r.correct) {
          mark.textContent = '✅';
        } else if (r.near_miss) {
          mark.textContent = '🟡';
        } else {
          mark.textContent = '❌';
        }
        mark.title = r.heard ? (r.near_miss ? '只差一点！你写的是：' + r.heard : '你写的是：' + r.heard) : '没有作答';
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
     * The default hint line (shared by render and show so the text lives
     * once).
     */
    static get DEFAULT_HINT() {
      return '把每个词的答案填进来（可以拍照识别），全部填好后一起提交';
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

    /**
     * Whether the form should open for a state-manager snapshot. Pure/static
     * so Node tests pin the visibility gate without a DOM: batch mode +
     * active session + WAITING_PARENT + not already open. The form mounts in
     * its own top-level section (index.html) precisely so this status can
     * reveal it — the dictation-controls section hides at WAITING_PARENT.
     *
     * @param {Object} state - SessionStateManager snapshot (status, batchMode,
     *        sessionActive).
     * @param {boolean} isFormVisible - current isVisible() of the form.
     * @returns {boolean}
     */
    static shouldOpenForState(state, isFormVisible) {
      return !!(state &&
        state.batchMode &&
        state.sessionActive &&
        state.status === 'WAITING_PARENT' &&
        !isFormVisible);
    }
  }

  /**
   * FeedbackReport — the post-grading analysis screen (Ticket 06).
   *
   * Renders the analysis_result frame: score headline + spoken encouragement,
   * per-wrong-word cards with CHARACTER-LEVEL highlighting (server diff ops →
   * colored spans on the correct spelling and the student's answer), error
   * patterns, practice suggestions, and the cross-session trend list with
   * repeat-miss words.
   *
   * Unlike the batch form (hidden by the session-end reset), the report IS
   * the end-of-session display: it stays up after FEEDBACK and is hidden only
   * when the next session starts (app.js hides it on session_ready).
   *
   * Char-span mapping lives in the pure static buildCharSpans so Node tests
   * pin the diff→highlight contract without a DOM.
   *
   * @class
   */
  class FeedbackReport {
    /**
     * @param {HTMLElement} container - mount point (#feedback-report-container).
     */
    constructor(container) {
      if (!container) throw new Error('FeedbackReport requires a container element');
      this.container = container;
      this.card = null;
      this.render();
      this.hide();
    }

    /**
     * Build the card skeleton. Content is (re)built by show(analysis).
     */
    render() {
      this.card = document.createElement('div');
      this.card.className = 'feedback-report';
      this.card.setAttribute('role', 'region');
      this.card.setAttribute('aria-label', '听写报告');
      this.container.appendChild(this.card);
    }

    /**
     * Render an analysis_result payload and reveal the report.
     * @param {object} analysis - analysis_result frame payload
     */
    show(analysis) {
      if (!analysis || typeof analysis !== 'object') return;
      var self = this;
      var a = analysis;

      this.card.textContent = '';

      // Headline: emoji + score + counts + the spoken encouragement.
      var head = document.createElement('div');
      head.className = 'feedback-report-head';
      var emoji = document.createElement('span');
      emoji.className = 'feedback-report-emoji';
      emoji.textContent = FeedbackReport.scoreEmoji(a.score);
      var scoreLine = document.createElement('div');
      scoreLine.className = 'feedback-report-score';
      scoreLine.textContent = '得分 ' + (a.score || 0) + ' 分（' + (a.correct_count || 0) + ' / ' + (a.total || 0) + ' 个词）';
      head.appendChild(emoji);
      head.appendChild(scoreLine);
      this.card.appendChild(head);

      if (a.encouragement) {
        var cheer = document.createElement('p');
        cheer.className = 'feedback-report-cheer';
        cheer.setAttribute('role', 'status');
        cheer.textContent = a.encouragement;
        this.card.appendChild(cheer);
      }

      // Wrong words with char-level highlights.
      (a.errors || []).forEach(function (e) {
        self.card.appendChild(FeedbackReport.buildErrorCard(e));
      });

      // Error patterns.
      (a.patterns || []).forEach(function (p) {
        var el = document.createElement('p');
        el.className = 'feedback-report-pattern';
        el.textContent = '🔍 ' + p;
        self.card.appendChild(el);
      });

      // Practice suggestions.
      (a.suggestions || []).forEach(function (s) {
        var el = document.createElement('p');
        el.className = 'feedback-report-suggestion';
        el.textContent = '✏️ ' + s;
        self.card.appendChild(el);
      });

      // Repeat-miss words.
      (a.repeat_words || []).forEach(function (r) {
        var el = document.createElement('p');
        el.className = 'feedback-report-repeat';
        el.textContent = '🔁 「' + r.word + '」之前也错过 ' + r.times + ' 次，重点复习';
        self.card.appendChild(el);
      });

      // Trend list (oldest first, 今天 last) — textual, no chart.
      var trends = a.trends || [];
      if (trends.length > 0) {
        var trendBox = document.createElement('div');
        trendBox.className = 'feedback-report-trends';
        var trendTitle = document.createElement('p');
        trendTitle.className = 'feedback-report-trends-title';
        trendTitle.textContent = '📈 最近成绩';
        trendBox.appendChild(trendTitle);
        trends.forEach(function (t, i) {
          var line = document.createElement('div');
          line.className = 'feedback-report-trend-line' + (i === trends.length - 1 ? ' current' : '');
          var arrow = '';
          if (i > 0) {
            arrow = t.score > trends[i - 1].score ? ' ↑' : (t.score < trends[i - 1].score ? ' ↓' : ' —');
          }
          line.textContent = t.date + '：' + (t.correct || 0) + '/' + (t.total || 0) + '（' + (t.score || 0) + ' 分）' + arrow;
          trendBox.appendChild(line);
        });
        this.card.appendChild(trendBox);
      }

      this.reveal(true);
    }

    /**
     * Hide the report (called when the next session starts).
     */
    hide() {
      this.reveal(false);
    }

    /**
     * Whether the report is currently revealed.
     * @returns {boolean}
     */
    isVisible() {
      return this.card.style.display !== 'none';
    }

    /** @private show/hide the card + its wrapper section. */
    reveal(on) {
      this.card.style.display = on ? 'block' : 'none';
      var section = this.container.closest ? this.container.closest('section') : null;
      if (section) {
        if (on) {
          section.removeAttribute('hidden');
        } else {
          section.setAttribute('hidden', '');
        }
      }
    }

    /**
     * One wrong word's card: word + near-miss badge, correct spelling with
     * highlights, the student's answer with highlights, and the hint.
     * @param {object} e - one analysis errors[] entry
     * @returns {HTMLElement}
     */
    static buildErrorCard(e) {
      var card = document.createElement('div');
      card.className = 'feedback-report-error';

      var title = document.createElement('p');
      title.className = 'feedback-report-error-title';
      title.textContent = e.word;
      if (e.blank) {
        title.textContent += '（没有作答）';
      } else if (e.near_miss) {
        title.textContent += ' 🟡只差一点';
      } else {
        title.textContent += ' ❌';
      }
      card.appendChild(title);

      if (!e.blank) {
        card.appendChild(FeedbackReport.buildCharRow('正确写法', e.ops ? FeedbackReport.buildCharSpans(e.ops, 'expected') : []));
        card.appendChild(FeedbackReport.buildCharRow('你写的', e.ops ? FeedbackReport.buildCharSpans(e.ops, 'heard') : []));
      }

      if (e.hint) {
        var hint = document.createElement('p');
        hint.className = 'feedback-report-error-hint';
        hint.textContent = '💡 ' + e.hint;
        card.appendChild(hint);
      }
      return card;
    }

    /**
     * One labeled char-highlight row.
     * @private
     */
    static buildCharRow(label, spans) {
      var row = document.createElement('p');
      row.className = 'feedback-report-char-row';
      var lbl = document.createElement('span');
      lbl.className = 'feedback-report-char-label';
      lbl.textContent = label + '：';
      row.appendChild(lbl);
      spans.forEach(function (s) {
        var el = document.createElement('span');
        el.className = 'feedback-report-char feedback-report-char-' + s.cls;
        el.textContent = s.ch;
        row.appendChild(el);
      });
      return row;
    }

    /**
     * Map server diff ops onto highlight spans for one side of the word.
     * Pure/static so Node tests pin the contract:
     *   expected side — match: ok; sub/swap: bad; del: miss; ins: (nothing —
     *     the expected word has no such char);
     *   heard side — match: dim; sub/swap: bad; ins: extra; del: (nothing).
     *
     * @param {Object[]} ops - mismatch ops ({expected, heard, kind})
     * @param {string} side - 'expected' | 'heard'
     * @returns {Object[]} spans - [{ch, cls}]
     */
    static buildCharSpans(ops, side) {
      var spans = [];
      (ops || []).forEach(function (op) {
        switch (op.kind) {
          case 'match':
            spans.push({ ch: op.expected, cls: side === 'expected' ? 'ok' : 'dim' });
            break;
          case 'sub':
          case 'swap':
            spans.push({ ch: side === 'expected' ? op.expected : op.heard, cls: 'bad' });
            break;
          case 'del':
            if (side === 'expected') {
              spans.push({ ch: op.expected, cls: 'miss' });
            }
            break;
          case 'ins':
            if (side === 'heard') {
              spans.push({ ch: op.heard, cls: 'extra' });
            }
            break;
        }
      });
      return spans;
    }

    /**
     * Headline emoji by score tier.
     * @param {number} score
     * @returns {string}
     */
    static scoreEmoji(score) {
      if (score >= 100) return '🏆';
      if (score >= 80) return '🎉';
      if (score >= 60) return '👍';
      return '💪';
    }
  }

  global.DictationControls = DictationControls;
  global.VoiceCommandButton = VoiceCommandButton;
  global.PhotoAnswerButton = PhotoAnswerButton;
  global.ManualAnswerForm = ManualAnswerForm;
  global.BatchSubmissionForm = BatchSubmissionForm;
  global.FeedbackReport = FeedbackReport;
})(typeof window !== 'undefined' ? window : this);
