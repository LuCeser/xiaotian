/**
 * @file Student Dictation Interface — application entry point
 *       (Phase 8, Plan 03 — progress display wiring).
 *
 * Purpose:
 *   Main application initialization and component orchestration for the
 *   student-facing dictation web interface. Loaded with `defer` from
 *   index.html after websocket-client.js, state-manager.js, and
 *   ui-components.js so all constructors are available.
 *
 * Responsibilities (Wave 2 + Wave 3 scope):
 *   1. Construct the SessionStateManager (single source of truth).
 *   2. Construct a WebSocketClient pointed at the backend /ws endpoint and
 *      register the state manager so inbound control frames dispatch into
 *      structured state updates (handleControlFrame).
 *   3. Construct the SessionButton and subscribe it to the state manager.
 *   4. Construct the ProgressDisplay and subscribe it to the state manager
 *      so status text, progress bar, and word counter update in real time
 *      (Wave 3 / Plan 08-03). Reveals the progress section on session start.
 *   5. Drive the connection-status indicator (#connectionStatus) from the
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
  if (!window.SessionStateManager || !window.SessionButton || !window.ProgressDisplay || !window.AudioController || !window.SpeechController) {
    console.error('[app] SessionStateManager/SessionButton/ProgressDisplay/AudioController/SpeechController unavailable — scripts did not load.');
    setConnectionState('failed');
    return;
  }

  // Ticket 02 DICT-07: DictationControls is optional — if it fails to load,
  // the session still works without pause/resume/repeat buttons.
  var hasDictationControls = !!window.DictationControls;

  // Ticket 03: VoiceCommandController/Button are optional the same way —
  // without them (or without getUserMedia) the buttons are the controls.
  var hasVoiceCommand = !!window.VoiceCommandController && !!window.VoiceCommandButton;

  // Ticket 04: PhotoAnswerController/Button are optional — without them (or
  // without canvas imaging) the manual input is the answer fallback.
  var hasPhotoAnswer = !!window.PhotoAnswerController && !!window.PhotoAnswerButton && !!window.ManualAnswerForm;

  // Ticket 05: BatchSubmissionForm is optional the same way.
  var hasBatchForm = !!window.BatchSubmissionForm;

  // Ticket 06: FeedbackReport is optional the same way (the analysis screen).
  var hasFeedbackReport = !!window.FeedbackReport;

  // Plan 08-04 Task 4: global error handlers for production resilience
  window.addEventListener('error', function (event) {
    console.error('[app] Global error:', event.error || event.message);
    stateManager.setState({ lastError: '页面错误，请刷新重试' });
  });

  window.addEventListener('unhandledrejection', function (event) {
    console.error('[app] Unhandled promise rejection:', event.reason);
    stateManager.setState({ lastError: '异步操作失败，请刷新重试' });
  });

  // Plan 08-04 Task 4: resource cleanup on page unload
  window.addEventListener('beforeunload', function () {
    // Clean up audio controller resources
    if (audioController) {
      audioController.cleanup();
    }
    // Stop any in-progress speech recognition
    if (speechController) {
      speechController.cleanup();
    }
    // Release the mic if a voice command is mid-recording (Ticket 03)
    if (voiceController) {
      voiceController.cleanup();
    }
    // Discard a half-shipped photo upload (Ticket 04)
    if (photoController) {
      photoController.cleanup();
    }
    // Disconnect WebSocket cleanly
    if (wsClient) {
      wsClient.disconnect();
    }
  });

  // Plan 08-04 Task 4: additional error tracking for monitoring
  window.addEventListener('error', function (event) {
    // Track errors for analytics/monitoring (could be extended)
    if (typeof console !== 'undefined') {
      console.error('[app] Error tracked:', event.message);
    }
  }, true); // Use capture phase for comprehensive error catching

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
  // Audio playback: Web Audio API integration (Plan 08-04 Task 2)
  // ------------------------------------------------------------------
  var audioController;
  try {
    audioController = new window.AudioController();

    // Install visibility change listener for app-switch recovery (D-06).
    document.addEventListener('visibilitychange', function () {
      try {
        audioController.handleVisibilityChange();
      } catch (visErr) {
        console.error('[app] visibility change handler error:', visErr);
      }
      // Ticket 03: backgrounding mid-recording freezes the audio thread's
      // event delivery on iOS — abort the capture rather than ship a hole.
      try {
        if (voiceController && voiceController.isRecording && document.hidden) {
          voiceController.cancel();
        }
      } catch (voiceVisErr) {
        console.error('[app] voice cancel on hidden error:', voiceVisErr);
      }
    });
  } catch (audioErr) {
    console.error('[app] AudioController initialization failed:', audioErr);
    setConnectionState('failed');
    return;
  }

  // Wire playback end → dictation recognition
  audioController.setOnPlaybackEnd(function () {
    var state = stateManager.getState();
    // Ticket 05 batch mode: the student writes on paper through the whole
    // dictation — no per-word speech capture. The queue still advances per
    // word: _applyWordComplete auto-sends next_word (websocket-client.js),
    // and the server's dictating timeout is the backstop. The batch form
    // opens at WAITING_PARENT.
    if (state.batchMode) {
      return;
    }
    var wordId = state.currentWordId;
    if (wordId && state.sessionActive && state.status === 'DICTATING') {
      // Small delay to let iOS audio session switch from playback to record
      setTimeout(function () {
        startDictationForWord(wordId);
      }, 300);
    }
  });

  // Ticket 05: batch mode toggle. The checkbox is the input device; the
  // state manager is the authority — the change listener syncs state.batchMode
  // (and the per-word photo host visibility), and every flow gate reads the
  // flag from state snapshots. Flipping it mid-session still does the sane
  // thing. photoHost is assigned later in init but before any change event
  // can fire.
  var batchToggle = document.getElementById('batch-mode-toggle');
  if (batchToggle) {
    batchToggle.addEventListener('change', function () {
      var on = batchToggle.checked;
      stateManager.setState({ batchMode: on });
      // In batch mode the per-word photo affordance hides — answering is the
      // batch form's job at the end (voice COMMANDS like 暂停/重读 stay
      // available; they are flow controls, not answers). The controller
      // stays live for the form's 📷 rows.
      if (photoHost) {
        photoHost.style.display = on ? 'none' : '';
      }
    });
    stateManager.setState({ batchMode: batchToggle.checked });
  }

  // ------------------------------------------------------------------
  // Speech recognition: Web Speech API for dictation answers
  // ------------------------------------------------------------------
  var speechController;
  try {
    speechController = new window.SpeechController();
    // Probe browser support immediately after construction
    speechController.init();  // Detects SpeechRecognition or webkitSpeechRecognition
  } catch (speechErr) {
    console.error('[app] SpeechController initialization failed:', speechErr);
    // Non-critical: dictation will work but without answer checking
  }
  if (speechController) {
    // Wire speech recognition callbacks
    speechController.onResult(handleDictationResult);
    speechController.onError(handleDictationError);
    console.log('[app] SpeechController supported:', speechController.supported);
  }

  // Dictation loop state
  var dictationRatings = {};      // word → rating ("good"/"again")
  var pendingAnswerWordId = null; // word waiting for speech recognition
  var feedbackTimer = null;       // timer for feedback display delay
  var sessionCategory = '';       // category from session_ready for language detection

  // ------------------------------------------------------------------
  // WebSocket client: control frames dispatch into the state manager
  // ------------------------------------------------------------------
  var wsClient;
  try {
    wsClient = new window.WebSocketClient(
      wsUrl,
      handleMessage,
      handleError,
      handleStateChange
    );
    // Route inbound JSON control frames (session_ready/error/progress_update/
    // session_complete) through handleControlFrame into the state manager.
    wsClient.setStateManager(stateManager);
    wsClient.connect();
  } catch (wsErr) {
    console.error('[app] WebSocket client initialization failed:', wsErr);
    stateManager.setState({ lastError: '连接初始化失败，请刷新页面' });
    setConnectionState('failed');
    return;
  }

  // Capture session category for dictation language detection. session_ready
  // is a known control type dispatched inside handleControlFrame, so it never
  // reaches handleMessage — wrap the dispatcher to observe it first. The
  // backend does not yet send a category field; this defaults gracefully.
  // Ticket 06: a new session also retires the previous session's report.
  var originalHandleControlFrame = wsClient.handleControlFrame.bind(wsClient);
  wsClient.handleControlFrame = function (payload) {
    if (payload && payload.type === 'session_ready') {
      sessionCategory = payload.category || '';
      if (feedbackReport) {
        feedbackReport.hide();
      }
    }
    originalHandleControlFrame(payload);
  };

  // ------------------------------------------------------------------
  // Voice commands: push-to-talk mic → server ASR + intent (Ticket 03).
  // voice_result is an unknown type to WebSocketClient's dispatcher, so it
  // is forwarded to handleMessage (same path as answer_result); app.js
  // routes it to the mic button's feedback renderer.
  // ------------------------------------------------------------------
  var voiceController = null;
  var voiceButton = null;
  if (hasVoiceCommand) {
    try {
      voiceController = new window.VoiceCommandController(wsClient);
      voiceController.onError(handleVoiceError);
      var voiceHost = document.getElementById('voice-command-container');
      if (voiceHost) {
        voiceButton = new window.VoiceCommandButton(voiceHost, stateManager, voiceController);
        stateManager.subscribe(function (state) {
          try {
            voiceButton.update(state);
          } catch (voiceUpdateErr) {
            console.error('[app] voice button update error:', voiceUpdateErr);
          }
        });
        console.log('[app] VoiceCommandController supported:', voiceController.supported);
      }
    } catch (voiceErr) {
      console.error('[app] voice command initialization failed:', voiceErr);
      // Non-critical: session works with the button controls.
    }
  }

  // ------------------------------------------------------------------
  // Photo answers + manual input (Ticket 04): one-tap camera capture →
  // IMAGE frames → Mimo vision OCR; photo_result is an unknown type to
  // WebSocketClient's dispatcher, so it arrives in handleMessage like
  // voice_result. The ManualAnswerForm is the low-confidence/failed-OCR
  // fallback (and the always-available keyboard path): it submits through
  // the existing submit_answer wire path, so the reply reuses the shared
  // answer_result feedback flow.
  // ------------------------------------------------------------------
  var photoController = null;
  var photoButton = null;
  var manualForm = null;
  if (hasPhotoAnswer) {
    try {
      photoController = new window.PhotoAnswerController(wsClient);
      photoController.onError(handlePhotoError);
      var photoHost = document.getElementById('photo-answer-container');
      if (photoHost) {
        photoButton = new window.PhotoAnswerButton(photoHost, stateManager, photoController, function onPick() {
          // The photo replaces the auto-started mic answer for this word —
          // stop listening so a late speech result cannot judge the NEXT
          // word against this word's submission.
          if (speechController) {
            speechController.stopListening();
          }
        });
        stateManager.subscribe(function (state) {
          try {
            photoButton.update(state);
          } catch (photoUpdateErr) {
            console.error('[app] photo button update error:', photoUpdateErr);
          }
        });
        console.log('[app] PhotoAnswerController supported:', photoController.supported);
      }
      var manualHost = document.getElementById('manual-answer-container');
      if (manualHost) {
        manualForm = new window.ManualAnswerForm(manualHost, handleManualSubmit);
      }
    } catch (photoErr) {
      console.error('[app] photo answer initialization failed:', photoErr);
      // Non-critical: manual input + buttons remain.
    }
  }

  // ------------------------------------------------------------------
  // Batch submission (Ticket 05): write all → submit all → grade all. The
  // form opens when the session reaches WAITING_PARENT in batch mode (the
  // status now travels in progress_update frames). Per-row 📷 reuses the
  // PhotoAnswerController with the photo_ocr trigger (OCR fill, no judging).
  // ------------------------------------------------------------------
  var batchForm = null;
  var pendingOCRIndex = -1; // row awaiting a photo_ocr_result
  if (hasBatchForm) {
    try {
      var batchHost = document.getElementById('batch-submission-container');
      if (batchHost) {
        batchForm = new window.BatchSubmissionForm(
          batchHost,
          function onBatchSubmit(answers) {
            if (wsClient && typeof wsClient.send === 'function') {
              wsClient.send({ type: 'submit_answers', params: { answers: answers } });
            }
          },
          function onPickOCR(index) {
            if (!photoController) return;
            pendingOCRIndex = index;
            photoController.pickPhoto('photo_ocr');
          }
        );
      }
    } catch (batchErr) {
      console.error('[app] batch form initialization failed:', batchErr);
      // Non-critical: per-word answering remains.
    }
  }

  // WAITING_PARENT in batch mode → open the form (once). The per-word mode's
  // auto-ratings subscriber below is ratings-gated and stays inert in batch
  // mode (nothing answered per-word).
  stateManager.subscribe(function (state) {
    if (!batchForm || !state.batchMode) return;
    if (state.status === 'WAITING_PARENT' && state.sessionActive && !batchForm.isVisible()) {
      if (speechController) {
        speechController.stopListening();
      }
      batchForm.show(state.words || []);
    }
  });

  // ------------------------------------------------------------------
  // Feedback report (Ticket 06): the analysis_result frame renders the
  // post-grading report. It deliberately survives the session-end reset
  // (it IS the end-of-session display) — only the NEXT session hides it,
  // via the session_ready observer below.
  // ------------------------------------------------------------------
  var feedbackReport = null;
  if (hasFeedbackReport) {
    try {
      var reportHost = document.getElementById('feedback-report-container');
      if (reportHost) {
        feedbackReport = new window.FeedbackReport(reportHost);
      }
    } catch (reportErr) {
      console.error('[app] feedback report initialization failed:', reportErr);
      // Non-critical: the batch form's ✓/✗ marks remain.
    }
  }

  // ------------------------------------------------------------------
  // Session button: subscribes to the state manager for its visual states
  // Wire audio unlock into session button click (D-03 3-condition iOS unlock)
  // ------------------------------------------------------------------
  var buttonHost = document.getElementById('session-button-container');
  if (!buttonHost) {
    console.error('[app] #session-button-container missing — SessionButton cannot mount.');
  } else {
    try {
      var sessionButton = new window.SessionButton(buttonHost, stateManager, wsClient, audioController);
      // Re-render the button whenever the authoritative state changes.
      stateManager.subscribe(function (state) {
        try {
          sessionButton.update(state);
        } catch (updateErr) {
          console.error('[app] session button update error:', updateErr);
        }
      });
    } catch (buttonErr) {
      console.error('[app] SessionButton initialization failed:', buttonErr);
      stateManager.setState({ lastError: '按钮初始化失败，请刷新页面' });
    }
  }

  // ------------------------------------------------------------------
  // Progress display: mounts into #progress-display-container and subscribes
  // to the state manager for real-time progress updates (PLAN 08-03 Task 2).
  // Registered AFTER the session button so the button's visual state settles
  // before the progress bar redraws (plan: "progress display updates after
  // session button in subscriber order"). Also reveals the progress section
  // once a session starts (Wave 1 shipped it hidden).
  // ------------------------------------------------------------------
  var progressHost = document.getElementById('progress-display-container');
  var progressSection = document.getElementById('progressDisplay');
  if (!progressHost) {
    console.error('[app] #progress-display-container missing — ProgressDisplay cannot mount.');
  } else {
    try {
      var progressDisplay = new window.ProgressDisplay(progressHost, stateManager);
      stateManager.subscribe(function (state) {
        try {
          progressDisplay.update(state);
          // Reveal the section once a session is active or has reached a
          // non-idle state (e.g. FEEDBACK after completion). It stays visible
          // thereafter so the student sees their result.
          if (progressSection && progressSection.hasAttribute('hidden')) {
            if (state.sessionActive || (state.status && state.status !== 'IDLE')) {
              progressSection.removeAttribute('hidden');
            }
          }
        } catch (progressUpdateErr) {
          console.error('[app] progress display update error:', progressUpdateErr);
        }
      });
    } catch (progressErr) {
      console.error('[app] ProgressDisplay initialization failed:', progressErr);
      // Non-critical: log and continue without progress display
    }
  }

  // ------------------------------------------------------------------
  // Dictation controls: pause/resume/repeat/next buttons (Ticket 02 DICT-07)
  // ------------------------------------------------------------------
  var controlsHost = document.getElementById('dictation-controls-container');
  var controlsSection = document.getElementById('dictationControls');
  if (controlsHost && hasDictationControls) {
    try {
      var dictationControls = new window.DictationControls(controlsHost, stateManager, wsClient);
      stateManager.subscribe(function (state) {
        try {
          dictationControls.update(state);
          // Show controls section during active dictation
          if (controlsSection) {
            if (state.sessionActive && (state.status === 'DICTATING' || state.status === 'LISTENING' || state.status === 'PAUSED')) {
              controlsSection.removeAttribute('hidden');
            } else {
              controlsSection.setAttribute('hidden', '');
            }
          }
        } catch (ctrlUpdateErr) {
          console.error('[app] dictation controls update error:', ctrlUpdateErr);
        }
      });
    } catch (ctrlErr) {
      console.error('[app] DictationControls initialization failed:', ctrlErr);
      // Non-critical: session works without pause/resume/repeat
    }
  }

  // ------------------------------------------------------------------
  // Dictation feedback: recording indicator + per-word judgment
  // ------------------------------------------------------------------
  var feedbackHost = document.getElementById('dictation-feedback-container');
  var feedbackSection = document.getElementById('dictationFeedback');
  if (feedbackHost) {
    try {
      var dictationFeedback = new window.DictationFeedback(feedbackHost, stateManager);
      stateManager.subscribe(function (state) {
        try {
          dictationFeedback.update(state);
          // Show feedback section during dictation
          if (feedbackSection) {
            if (state.dictationState && state.sessionActive) {
              feedbackSection.removeAttribute('hidden');
            } else {
              feedbackSection.setAttribute('hidden', '');
            }
          }
        } catch (fbErr) {
          console.error('[app] dictation feedback update error:', fbErr);
        }
      });
    } catch (fbInitErr) {
      console.error('[app] DictationFeedback initialization failed:', fbInitErr);
    }
  }

  // ------------------------------------------------------------------
  // Connection indicator: also driven from the state manager
  // ------------------------------------------------------------------
  stateManager.subscribe(function (state) {
    setConnectionState(toIndicatorState(state.connectionStatus));
  });

  // Expose for console debugging and cross-script coupling (Plan 08-04 Task 2).
  window.__app = { stateManager: stateManager, wsClient: wsClient, audioController: audioController, speechController: speechController, voiceController: voiceController, photoController: photoController };
  // Expose audioController globally for websocket-client.js access (Plan 08-04 Task 2).
  window.audioController = audioController;
  window.speechController = speechController;

  // ------------------------------------------------------------------
  // Auto-submit ratings when entering WAITING_PARENT + reset dictation
  // state on session end.
  //
  // NOTE: setState() notifies subscribers unconditionally (no change
  // detection), so this handler MUST NOT call setState on every pass or
  // it will recurse infinitely. The needsReset dirty-check below makes
  // the reset path fire at most once per session end.
  // ------------------------------------------------------------------
  stateManager.subscribe(function (state) {
    if (state.status === 'WAITING_PARENT' && Object.keys(dictationRatings).length > 0) {
      submitSessionRatings();
    }
    // Reset dictation state on session end
    if (!state.sessionActive) {
      var needsReset = Object.keys(dictationRatings).length > 0 ||
        pendingAnswerWordId !== null ||
        feedbackTimer !== null ||
        !!state.dictationState ||
        (batchForm && batchForm.isVisible()) ||
        state.lastAnswerCorrect !== null && state.lastAnswerCorrect !== undefined ||
        state.lastExpected !== null && state.lastExpected !== undefined ||
        state.lastHeard !== null && state.lastHeard !== undefined;
      if (!needsReset) {
        return;
      }
      dictationRatings = {};
      pendingAnswerWordId = null;
      if (feedbackTimer) {
        clearTimeout(feedbackTimer);
        feedbackTimer = null;
      }
      if (speechController) {
        speechController.stopListening();
      }
      // Hide the manual-input fallback on session end (Ticket 04).
      if (manualForm) {
        manualForm.hide();
      }
      // Hide the batch form + reset OCR row tracking (Ticket 05).
      if (batchForm) {
        batchForm.hide();
      }
      pendingOCRIndex = -1;
      stateManager.setState({ dictationState: null, lastAnswerCorrect: null, lastExpected: null, lastHeard: null });
    }
  });

  // ------------------------------------------------------------------
  // Handlers
  // ------------------------------------------------------------------

  /**
   * Inbound message handler. With a state manager registered on the client,
   * JSON control frames are dispatched by handleControlFrame (never reach
   * here except for unknown control types forwarded for visibility). Binary
   * frames (Phase 7.1 audio) route to the AudioController for gapless playback.
   */
  function handleMessage(data, isBinary) {
    if (isBinary) {
      // Binary audio frame — route to AudioController (Plan 08-04 Task 2)
      if (audioController && data && data.byteLength >= 16) {
        // Parse frame header (16 bytes) for sequence number and extract payload
        var view = new DataView(data);
        var sequence = view.getUint32(2, false); // Big-endian sequence at offset 2
        var payload = new Uint8Array(data, 16); // Skip 16-byte header
        var state = stateManager.getState();
        // Ticket 06: in FEEDBACK the streaming audio is the spoken
        // encouragement — no current word, no word_complete trigger, so it
        // plays live as it arrives instead of accumulating.
        if (state.status === 'FEEDBACK') {
          audioController.playLiveFrame(payload, sequence);
          return;
        }
        var wordId = state.currentWordId || null;
        audioController.handleAudioFrame(payload, sequence, wordId);
      } else {
        console.warn('[app] binary frame received but AudioController unavailable or frame too small');
      }
      return;
    }
    // Unknown control frames forwarded by handleControlFrame arrive here
    // pre-parsed; legacy text frames may still be raw JSON strings.
    var parsed = data;
    if (typeof data === 'string') {
      try {
        parsed = JSON.parse(data);
      } catch (e) { /* not JSON, ignore */ }
    }
    if (parsed && typeof parsed === 'object') {
      // Dictation judgment result — drive the closed-loop feedback UI.
      if (parsed.type === 'answer_result') {
        handleAnswerResult(parsed);
        return;
      }
      // Voice command result (Ticket 03) — render the heard/transcript hint.
      if (parsed.type === 'voice_result') {
        handleVoiceResult(parsed);
        return;
      }
      // Photo answer result (Ticket 04) — judged / low-confidence / failed.
      if (parsed.type === 'photo_result') {
        handlePhotoResult(parsed);
        return;
      }
      // OCR-only photo result (Ticket 05) — fills the batch form row.
      if (parsed.type === 'photo_ocr_result') {
        handlePhotoOCRResult(parsed);
        return;
      }
      // Batch submission result (Ticket 05) — per-word judgments.
      if (parsed.type === 'batch_result') {
        handleBatchResult(parsed);
        return;
      }
      // Analysis report (Ticket 06) — score, char-level errors, trends.
      if (parsed.type === 'analysis_result') {
        handleAnalysisResult(parsed);
        return;
      }
    }
    // Forwarded control frame (unknown type) or legacy path — log it.
    console.debug('[app] control message received:', data);
  }

  /**
   * Voice command result from the backend (Ticket 03). The FSM effect of a
   * recognized command arrives separately through the regular state frames
   * (paused status, next word audio); this only renders what was heard and
   * unlocks the mic button.
   */
  function handleVoiceResult(result) {
    if (voiceButton) {
      voiceButton.handleResult(result);
    }
  }

  /**
   * Photo answer result from the backend (Ticket 04). Three outcomes:
   *   judged        → reuse the shared answer_result feedback flow;
   *   low confidence→ show the manual input prefilled with the best guess;
   *   failed        → show the manual input empty, with the reason as hint.
   * Every path unlocks the photo button.
   */
  function handlePhotoResult(result) {
    if (photoController) {
      photoController.resultReceived();
    }

    if (result.error) {
      // Manual input only helps when the OCR itself failed (fallback
      // "manual"). A session-guard reject ("no active session", "not in
      // dictating state") would fail identically on resubmit — surface it
      // as a status hint instead of a dead-end form.
      if (result.fallback === 'manual' && manualForm) {
        manualForm.setHint(result.error);
        manualForm.show('');
      } else if (photoButton) {
        photoButton.showError(result.error);
      }
      return;
    }

    if (result.low_confidence) {
      if (manualForm) {
        var pct = Math.round((result.confidence || 0) * 100);
        manualForm.setHint('不太确定（' + pct + '%），请核对或修改后提交');
        manualForm.show(result.text || '');
      }
      return;
    }

    if (result.judged) {
      // Same feedback flow as the spoken path: ratings accumulate, the
      // feedback card shows the outcome, the timer advances the session.
      handleAnswerResult({
        word_id: stateManager.getState().currentWordId,
        correct: result.correct,
        expected: result.expected,
        heard: result.text,
        rating: result.rating
      });
    }
  }

  /**
   * Manual input submission (Ticket 04): rides the existing submit_answer
   * wire path; the answer_result reply re-enters through handleAnswerResult.
   */
  function handleManualSubmit(text) {
    if (wsClient && typeof wsClient.send === 'function') {
      wsClient.send({
        type: 'submit_answer',
        params: { word_id: stateManager.getState().currentWordId, transcript: text }
      });
    }
    if (manualForm) {
      manualForm.hide();
    }
  }

  /**
   * OCR-only photo result (Ticket 05): fill the pending batch-form row with
   * the recognized text. The controller unlock mirrors handlePhotoResult.
   */
  function handlePhotoOCRResult(result) {
    if (photoController) {
      photoController.resultReceived();
    }
    if (batchForm && pendingOCRIndex >= 0) {
      batchForm.fillRow(pendingOCRIndex, result);
    }
    pendingOCRIndex = -1;
  }

  /**
   * Batch submission result (Ticket 05): render per-row marks + summary.
   * session_complete (FEEDBACK) follows through the existing end path.
   */
  function handleBatchResult(result) {
    if (batchForm) {
      batchForm.showResults(result);
    }
  }

  /**
   * Analysis report (Ticket 06): render the post-grading report. The spoken
   * encouragement arrives separately as live audio frames in FEEDBACK.
   */
  function handleAnalysisResult(analysis) {
    if (feedbackReport) {
      feedbackReport.show(analysis);
    }
  }

  /**
   * Photo capture/encode failures (Ticket 04): the session keeps running —
   * surface a hint pointing at the manual input, which stays available.
   */
  function handlePhotoError(errorType) {
    var messages = {
      'not-supported': '此浏览器不支持拍照提交，请手动输入',
      'not-an-image': '选择的不是图片，请重新拍照',
      'too-large': '照片太大了，请离近一点重拍',
      'read-failed': '照片读取失败，请重拍',
      'encode-failed': '照片处理失败，请重拍或手动输入'
    };
    var msg = messages[errorType] || '拍照提交失败，请手动输入';
    console.warn('[photo] error:', errorType);
    if (photoButton) {
      photoButton.showError(msg);
    }
  }

  /**
   * Mic capture failures (Ticket 03): permission denials and hardware
   * problems keep the session alive — surface a hint pointing at the button
   * fallback, which the acceptance criteria make the always-available path.
   * The hint renders inside the voice button's status line (local to the
   * mic affordance), not the session-level lastError surface.
   */
  function handleVoiceError(errorType) {
    var messages = {
      'not-supported': '此浏览器不支持语音命令，请用按钮',
      'mic-denied': '麦克风权限被拒绝，请用按钮或在设置中允许麦克风',
      'mic-unavailable': '麦克风不可用，请用按钮',
      'overflow': '录音太长了，请短一点再说',
      'start-failed': '录音启动失败，请用按钮'
    };
    var msg = messages[errorType] || '语音命令失败，请用按钮';
    console.warn('[voice] error:', errorType);
    if (voiceButton) {
      voiceButton.showError(msg);
    }
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
  // Dictation loop: playback end → recognition → judgment → next word
  // ------------------------------------------------------------------

  /**
   * Start the dictation loop for a word. Called when word audio finishes playing.
   * Begins speech recognition for the current word.
   */
  function startDictationForWord(wordId) {
    if (!speechController || !speechController.supported) {
      console.warn('[dictation] speech not supported — auto-advancing');
      // Skip to next word after a brief delay
      feedbackTimer = setTimeout(function () {
        sendNextWord(wordId);
      }, 1500);
      return;
    }

    // Determine language from session category
    var lang = (sessionCategory === 'chinese' || sessionCategory === 'zh') ? 'zh-CN' : 'en-US';
    speechController.init(lang);

    // Show recording indicator
    stateManager.setState({
      dictationState: 'listening',
      currentAnswerWordId: wordId
    });

    speechController.startListening(wordId);
  }

  /**
   * Handle speech recognition result. Compare and show feedback.
   */
  function handleDictationResult(wordId, transcript) {
    pendingAnswerWordId = null;
    // Send submit_answer to server for judgment
    if (wsClient && typeof wsClient.send === 'function') {
      wsClient.send({
        type: 'submit_answer',
        params: { word_id: wordId, transcript: transcript }
      });
    }
  }

  /**
   * Handle answer_result from server. Show feedback, then advance.
   */
  function handleAnswerResult(data) {
    // Photo-path calls (Ticket 04) carry no word_id — the session's current
    // word is authoritative anyway (D-13).
    var wordId = data.word_id || stateManager.getState().currentWordId;
    var correct = data.correct;
    var expected = data.expected;
    var heard = data.heard;
    var rating = data.rating;

    // Store rating for batch submit_review at end
    dictationRatings[expected] = rating;

    // Show feedback
    stateManager.setState({
      dictationState: correct ? 'correct' : 'incorrect',
      lastAnswerCorrect: correct,
      lastExpected: expected,
      lastHeard: heard
    });

    // Differential delay: correct 1.2s, incorrect 2.5s (time to read correct answer)
    var delay = correct ? 1200 : 2500;
    feedbackTimer = setTimeout(function () {
      stateManager.setState({ dictationState: null, lastAnswerCorrect: null, lastExpected: null, lastHeard: null });
      sendNextWord(wordId);
    }, delay);
  }

  /**
   * Handle speech recognition error. Show skip feedback, then advance.
   */
  function handleDictationError(wordId, errorType) {
    console.warn('[dictation] recognition error: ' + errorType + ' for word ' + wordId);
    pendingAnswerWordId = null;

    var messages = {
      'no-speech': '没听到声音',
      'not-allowed': '麦克风权限被拒绝',
      'timeout': '录音超时',
      'network': '网络错误',
      'audio-capture': '麦克风不可用',
      'start-failed': '录音启动失败',
      'not-initialized': '语音识别未初始化'
    };
    var msg = messages[errorType] || '识别失败';

    stateManager.setState({
      dictationState: 'skipped',
      lastHeard: msg
    });

    // No rating for skipped words — advance after delay
    feedbackTimer = setTimeout(function () {
      stateManager.setState({ dictationState: null, lastHeard: null });
      sendNextWord(wordId);
    }, 2500);
  }

  /**
   * Send next_word to advance the session.
   */
  function sendNextWord(wordId) {
    if (wsClient && typeof wsClient.send === 'function') {
      wsClient.send({
        type: 'next_word',
        params: { word_id: wordId }
      });
    }
  }

  /**
   * Submit accumulated ratings to FSRS when session completes.
   */
  function submitSessionRatings() {
    if (Object.keys(dictationRatings).length === 0) {
      console.log('[dictation] no ratings to submit');
      return;
    }
    if (wsClient && typeof wsClient.send === 'function') {
      console.log('[dictation] submitting ' + Object.keys(dictationRatings).length + ' ratings');
      wsClient.send({
        type: 'submit_review',
        params: { ratings: dictationRatings, category: sessionCategory }
      });
    }
    dictationRatings = {};
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
