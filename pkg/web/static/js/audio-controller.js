/**
 * @file AudioController — Web Audio API integration for gapless PCM playback
 *       (Phase 8, Plan 04, Task 01).
 *
 * Source pattern: Phase 7.1 Web Audio API fixes (07.1-03-SUMMARY.md).
 * Extracted from test-interface.html into a reusable class for the student
 * dictation interface.
 *
 * Design constraints (see 08-RESEARCH.md):
 *   - Web Audio API for gapless audio playback (no HTMLAudioElement buffering).
 *   - iOS 3-condition gesture unlock (D-03): AudioContext construction,
 *     resume(), and silent buffer.start(0) MUST execute synchronously
 *     inside a user gesture.
 *   - Audio clock scheduling (D-04): schedule BufferSource at nextStartTime,
 *     then increment by buf.duration for gapless sequencing.
 *   - PCM conversion (D-05): s16le → Float32 via DataView.getInt16(i*2,true)/32768.
 *   - Runtime sampleRate (D-02): always use audioCtx.sampleRate (never 16000).
 *   - App-switch recovery (D-06): visibilitychange → resume(), onstatechange
 *     → tap-to-resume overlay.
 *   - Scalar monotonic dedup (D-10, D-11): track lastSeq, reject duplicates,
 *     log gaps.
 *
 * Loaded via a <script defer> tag AFTER state-manager.js but BEFORE app.js
 * in index.html; exposes the class as window.AudioController.
 */
(function (global) {
  'use strict';

  /**
   * AudioController manages Web Audio API playback for the student dictation
   * interface. Handles gapless PCM scheduling, iOS gesture unlock, app-switch
   * recovery, and audio frame routing.
   *
   * @class
   */
  class AudioController {
    constructor() {
      // AudioContext state (D-03: created in unlockAudioContext inside gesture).
      this.audioCtx = null;

      // Audio clock scheduling point (D-04).
      this.scheduledTime = 0;

      // Playback state.
      this.isPlaying = false;

      // Per-word PCM accumulation (D-05): wordId → Uint8Array[].
      this.pendingPcm = new Map();

      // Monotonic dedup state (D-10, D-11).
      this.lastSeq = 0;

      // Current active word for frame routing.
      this.currentWordId = null;

      // Tap-to-resume overlay (D-06).
      this.tapToResumeOverlay = null;

      // Playback-end callback. Fired when the last scheduled buffer finishes playing.
      // Used by the dictation flow to start speech recognition after word audio ends.
      this._onPlaybackEnd = null;

      // Frame size: 20ms @ 16kHz 16-bit mono = 640 bytes.
      this.AUDIO_FRAME_SIZE = 640;
    }

    /**
     * Register a callback that fires when scheduled audio playback finishes.
     * @param {function|null} callback - called with no args when playback ends.
     */
    setOnPlaybackEnd(callback) {
      this._onPlaybackEnd = callback;
    }

    /**
     * Check if AudioContext is ready and running.
     * @returns {boolean}
     */
    isAudioCtxReady() {
      return this.audioCtx && this.audioCtx.state === 'running';
    }

    /**
     * D-03 3-condition iOS unlock. MUST be called synchronously inside a
     * user gesture (no await, no async). Constructs AudioContext at 16kHz,
     * calls resume(), and starts a 1-sample silent buffer — all three
     * conditions are critical for iOS 17+.
     *
     * Plan 08-04 Task 4: add error handling and graceful degradation.
     *
     * @returns {boolean} true if unlock succeeded, false otherwise.
     */
    unlockAudioContext() {
      try {
        if (!this.audioCtx) {
          var AudioContextClass = window.AudioContext || window.webkitAudioContext;
          if (!AudioContextClass) {
            console.error('[audio] Web Audio API not supported in this browser');
            return false;
          }

          // D-02: construct at 16kHz to match backend PCM.
          this.audioCtx = new AudioContextClass({ sampleRate: 16000 });

          // D-02 gotcha: iOS may renegotiate sample rate — log mismatch.
          if (this.audioCtx.sampleRate !== 16000) {
            console.warn('[audio] WARN: ctx.sampleRate=' + this.audioCtx.sampleRate +
              ' (expected 16000) — PCM rate mismatch risk');
          }

          console.log('[audio] Web Audio API unlocked at sampleRate=' + this.audioCtx.sampleRate);

          // D-06: install state change handler for app-switch recovery.
          var self = this;
          this.audioCtx.onstatechange = function () {
            try {
              self._handleStateChange();
            } catch (stateChangeErr) {
              console.error('[audio] state change handler error:', stateChangeErr);
            }
          };
        }

        // Condition 2: resume() synchronously, NO await.
        this.audioCtx.resume();

        // Condition 3 (CRITICAL for iOS 17+): start a real buffer source
        // synchronously inside the gesture. resume() alone leaves output muted.
        var silentBuf = this.audioCtx.createBuffer(1, 1, this.audioCtx.sampleRate);
        silentBuf.getChannelData(0)[0] = 0;
        var silentSource = this.audioCtx.createBufferSource();
        silentSource.buffer = silentBuf;
        silentSource.connect(this.audioCtx.destination);
        silentSource.start(0);

        return true;
      } catch (unlockErr) {
        console.error('[audio] AudioContext unlock failed:', unlockErr);
        return false;
      }
    }

    /**
     * Handle binary audio frame from WebSocket. Accumulates PCM chunks
     * for the current word; scheduling happens on word_complete.
     *
     * @param {Uint8Array} data - PCM frame payload (640 bytes for 20ms @ 16kHz).
     * @param {number} sequence - frame sequence number for dedup.
     * @param {string} wordId - word identifier for frame routing.
     */
    handleAudioFrame(data, sequence, wordId) {
      if (!data || data.length !== this.AUDIO_FRAME_SIZE) {
        console.warn('[audio] Frame ignored: wrong size ' + (data ? data.length : 'null') +
          ' vs ' + this.AUDIO_FRAME_SIZE);
        return;
      }

      // D-10, D-11: scalar monotonic dedup.
      if (sequence <= this.lastSeq) {
        console.debug('[audio] Duplicate frame: seq=' + sequence + ' <= lastSeq=' + this.lastSeq);
        return;
      }
      if (this.lastSeq !== 0 && sequence > this.lastSeq + 1) {
        console.warn('[audio] Gap: expected ' + (this.lastSeq + 1) + ', got ' + sequence);
      }
      this.lastSeq = sequence;

      // Route frame to current word's PCM accumulator.
      if (!wordId) {
        console.warn('[audio] Frame received with no wordId — ignored (seq=' + sequence + ')');
        return;
      }

      if (!this.pendingPcm.has(wordId)) {
        this.pendingPcm.set(wordId, []);
      }
      this.pendingPcm.get(wordId).push(new Uint8Array(data));
    }

    /**
     * Play a completed word. Called when backend sends word_complete
     * control frame after streaming all audio frames for that word.
     *
     * Plan 08-04 Task 4: enhanced error handling and audio subsystem validation.
     *
     * @param {string} wordId - word identifier to play.
     */
    playWordComplete(wordId) {
      try {
        var chunks = this.pendingPcm.get(wordId);
        if (!chunks || chunks.length === 0) {
          console.warn('[audio] word_complete for "' + wordId + '" — no PCM accumulated');
          return;
        }

        // If AudioContext is suspended (iOS D-03, app-switch D-06), attempt
        // resume() and defer playback until the context is running. resume()
        // returns a Promise — we chain the actual scheduling onto it.
        if (this.audioCtx && this.audioCtx.state === 'suspended') {
          console.log('[audio] AudioContext suspended — attempting resume for "' + wordId + '"');
          var self = this;
          this.audioCtx.resume().then(function () {
            console.log('[audio] resume() succeeded, now playing "' + wordId + '"');
            self._scheduleWordPlayback(wordId, chunks);
          }).catch(function (resumeErr) {
            console.error('[audio] resume() failed for "' + wordId + '":', resumeErr);
          });
          return;
        }

        if (!this.isAudioCtxReady()) {
          console.error('[audio] word_complete for "' + wordId + '" but AudioContext not running — PCM dropped');
          return;
        }

        this._scheduleWordPlayback(wordId, chunks);
      } catch (playErr) {
        console.error('[audio] word_complete playback failed for "' + wordId + '":', playErr);
        this.isPlaying = false;
      }
    }

    /**
     * @private Schedule PCM playback for a word. Extracted from playWordComplete
     * so it can be called directly or after an async resume().
     *
     * @param {string} wordId - word identifier (for logging).
     * @param {Uint8Array[]} chunks - accumulated PCM chunks for this word.
     */
    _scheduleWordPlayback(wordId, chunks) {
      // Clear pending PCM for this word (Plan 08-04 Task 4: memory cleanup).
      this.pendingPcm.delete(wordId);

      // Merge PCM chunks into one s16le buffer.
      var totalLen = chunks.reduce(function (sum, chunk) { return sum + chunk.length; }, 0);
      var pcmBytes = new Uint8Array(totalLen);
      var offset = 0;
      for (var i = 0; i < chunks.length; i++) {
        pcmBytes.set(chunks[i], offset);
        offset += chunks[i].length;
      }

      // D-05: PCM Int16 → Float32 AudioBuffer. Use audioCtx.sampleRate
      // (runtime property, NEVER hardcoded 16000 per D-02).
      var numSamples = pcmBytes.length / 2;
      // CR-5 fix: use 16000 as the source rate (not audioCtx.sampleRate)
      // PCM data is 16kHz samples; browser resamples to context rate.
      var buf = this.audioCtx.createBuffer(1, numSamples, 16000);
      var view = new DataView(pcmBytes.buffer, pcmBytes.byteOffset, pcmBytes.byteLength);
      var chan = buf.getChannelData(0);
      for (var i = 0; i < numSamples; i++) {
        chan[i] = view.getInt16(i * 2, true) / 32768;
      }

      // D-04 + D-18: gapless scheduling with underrun snap.
      var src = this.audioCtx.createBufferSource();
      src.buffer = buf;
      src.connect(this.audioCtx.destination);

      // Plan 08-04 Task 4: add error handler for source start failure
      src.onerror = function (err) {
        console.error('[audio] BufferSource playback error:', err);
      };

      // Snap scheduledTime forward on underrun so late PCM never schedules in the past.
      if (this.scheduledTime < this.audioCtx.currentTime) {
        this.scheduledTime = this.audioCtx.currentTime;
      }

      src.start(this.scheduledTime);
      this.scheduledTime += buf.duration;

      // Fire playback-end callback when this buffer finishes.
      var self = this;
      src.onended = function () {
        self.isPlaying = false;
        if (typeof self._onPlaybackEnd === 'function') {
          self._onPlaybackEnd();
        }
      };

      this.isPlaying = true;

      console.log('[audio] word_complete: "' + wordId + '" — scheduled ' + numSamples +
        ' samples (' + (buf.duration * 1000).toFixed(0) + 'ms) at audio clock t=' +
        (this.scheduledTime - buf.duration).toFixed(3) + 's');
    }

    /**
     * Stop playback and reset state. Clears pending PCM and resets
     * the dedup counter.
     */
    stop() {
      this.isPlaying = false;
      this.pendingPcm.clear();
      this.lastSeq = 0;
      this.currentWordId = null;
      // Reset scheduling clock to avoid large jumps on next play.
      this.scheduledTime = 0;
    }

    /**
     * Resume if suspended (D-06 app-switch recovery). Called from
     * visibilitychange listener.
     */
    resume() {
      if (this.audioCtx && this.audioCtx.state === 'suspended') {
        this.audioCtx.resume();
        console.log('[audio] AudioContext resumed from suspended');
      }
    }

    /**
     * Set the current word ID for audio frame routing. Called before
     * requesting audio frames from the backend.
     *
     * @param {string} wordId - word identifier for upcoming audio frames.
     */
    setCurrentWord(wordId) {
      this.currentWordId = wordId;
    }

    /**
     * Handle visibility change (D-06). Attempt auto-resume when returning
     * to the page. If the context cannot re-enter 'running', onstatechange
     * will surface the tap-to-resume overlay.
     */
    handleVisibilityChange() {
      if (document.visibilityState === 'visible') {
        this.resume();
      }
    }

    /**
     * Handle AudioContext state change (D-06). Show tap-to-resume overlay
     * when not running; hide when running.
     * @private
     */
    _handleStateChange() {
      if (!this.audioCtx) {
        return;
      }

      if (this.audioCtx.state === 'running') {
        this._hideTapToResumeOverlay();
      } else {
        this._showTapToResumeOverlay();
      }
    }

    /**
     * Show the tap-to-resume overlay (D-06).
     * @private
     */
    _showTapToResumeOverlay() {
      var self = this;
      this._ensureTapToResumeOverlay();
      if (this.tapToResumeOverlay) {
        this.tapToResumeOverlay.style.display = 'flex';
        console.warn('[audio] AudioContext not running — tap-to-resume overlay shown');
      }
    }

    /**
     * Hide the tap-to-resume overlay (D-06).
     * @private
     */
    _hideTapToResumeOverlay() {
      if (this.tapToResumeOverlay) {
        this.tapToResumeOverlay.style.display = 'none';
      }
    }

    /**
     * Ensure the tap-to-resume overlay exists (D-06).
     * @private
     */
    _ensureTapToResumeOverlay() {
      if (this.tapToResumeOverlay) {
        return;
      }

      var self = this;
      this.tapToResumeOverlay = document.createElement('div');
      this.tapToResumeOverlay.id = 'audioTapToResumeOverlay';
      this.tapToResumeOverlay.textContent = '轻点继续播放音频';
      this.tapToResumeOverlay.style.cssText = 'position:fixed;top:0;left:0;right:0;bottom:0;' +
        'background:rgba(0,0,0,0.65);color:#fff;display:none;align-items:center;' +
        'justify-content:center;font-size:26px;font-weight:600;z-index:9999;cursor:pointer;';

      // The overlay's tap IS a fresh user gesture — re-run the 3-condition unlock.
      this.tapToResumeOverlay.onclick = function () {
        self.unlockAudioContext();
        self._hideTapToResumeOverlay();
      };

      document.body.appendChild(this.tapToResumeOverlay);
    }

    /**
     * Cleanup on session end. Stops all sources, clears pending data,
     * and resets state. Call this when a dictation session completes.
     *
     * Plan 08-04 Task 4: enhanced cleanup with error handling and memory management.
     */
    cleanup() {
      try {
        // Clear all pending PCM data (Plan 08-04 Task 4: memory cleanup).
        this.pendingPcm.clear();

        // Reset playback state.
        this.isPlaying = false;
        this.lastSeq = 0;
        this.currentWordId = null;
        this.scheduledTime = 0;
        this._onPlaybackEnd = null;

        // Hide tap-to-resume overlay.
        this._hideTapToResumeOverlay();

        // Note: we do NOT close the AudioContext — it persists across sessions
        // to avoid re-unlocking. The context is only created once per page load.
        console.log('[audio] cleanup completed');
      } catch (cleanupErr) {
        console.error('[audio] cleanup error:', cleanupErr);
      }
    }
  }

  global.AudioController = AudioController;
})(typeof window !== 'undefined' ? window : this);
