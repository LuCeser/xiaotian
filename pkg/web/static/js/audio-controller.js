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

      // P1-1: playback generation token. stop()/cleanup() increment it;
      // onended handlers capture the value at schedule time and ignore
      // their event if the generation has moved on, so a source scheduled
      // before a stop (e.g. the first of a double play) can never fire a
      // stale playback-end callback after teardown.
      this._playbackGeneration = 0;

      // Rolling PCM accumulator (D-05): every conforming audio frame is
      // appended here in arrival order; word_complete consumes the ENTIRE
      // buffer as the completing word's audio. Replaces the former
      // per-wordId pendingPcm Map — see handleAudioFrame for why
      // client-side wordId keying can never be authoritative.
      this.rollingPcm = [];

      // Monotonic dedup state (D-10, D-11).
      this.lastSeq = 0;

      // Current active word — informational/log-only (frames are NOT keyed
      // or dropped by it; see the rolling accumulator in handleAudioFrame).
      this.currentWordId = null;

      // Tap-to-resume overlay (D-06).
      this.tapToResumeOverlay = null;

      // Playback-end callback. Fired when the last scheduled buffer finishes playing.
      // Used by the dictation flow to start speech recognition after word audio ends.
      this._onPlaybackEnd = null;

      // Play-phase callback (2026-09-24): fired as 1 = first pronunciation
      // starts, 2 = second pronunciation (double play), 0 = playback over.
      // Drives the "第一遍/第二遍" rhythm chip; never fires for live
      // 'feedback' frames. Guarded by the same generation token as onended.
      this._onPlayPhase = null;

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
     * Register a callback that fires on play-phase transitions of WORD
     * playback: 1 = first pronunciation, 2 = second pronunciation (double
     * play), 0 = playback finished. Live 'feedback' frames never fire it.
     * @param {function|null} callback - called as cb(phase).
     */
    setOnPlayPhase(callback) {
      this._onPlayPhase = callback;
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
     * Handle binary audio frame from WebSocket. Appends the PCM chunk to
     * the single rolling accumulator; scheduling happens on word_complete.
     *
     * ROLLING ACCUMULATOR RATIONALE: the 16-byte frame header carries no
     * word_id, so client-side keying (the former per-wordId pendingPcm Map)
     * can never be authoritative — it silently dropped or mis-filed frames
     * whenever the frontend's currentWordId drifted from what the backend
     * was actually streaming (verified on iPad: the backend's DICTATING
     * timeout fires milliseconds before the frontend's batch gap, the
     * backend self-advances to word N+1 while currentWordId still points at
     * word N, and frames pile up under the wrong key → silent words).
     * The wire contract is "frames stream, then word_complete delimits the
     * word": every frame since the previous word_complete belongs to the
     * word now completing. Appending unconditionally is immune to
     * currentWordId drift, backend self-advance, REPEAT_WORD re-streams,
     * and routing races. wordId is accepted for LOGGING ONLY — a missing or
     * mismatched wordId must never drop a frame.
     *
     * @param {Uint8Array} data - PCM frame payload (640 bytes for 20ms @ 16kHz).
     * @param {number} sequence - frame sequence number for dedup.
     * @param {string|null} [wordId] - log-only context; NOT used for routing.
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

      // Append unconditionally to the rolling buffer (wordId is log-only —
      // see the rationale above; frames are never dropped for routing).
      this.rollingPcm.push(new Uint8Array(data));
      console.debug('[audio] frame seq=' + sequence + ' buffered (wordId=' +
        (wordId || 'none') + ', chunks=' + this.rollingPcm.length + ')');
    }

    /**
     * Play one live audio frame immediately (Ticket 06): the spoken
     * encouragement streams in FEEDBACK, where there is no current word and
     * no word_complete trigger for the accumulate-then-play path. The frame
     * joins the same gapless schedule (D-04) and the same monotonic dedup
     * (D-10/D-11) as word audio.
     *
     * @param {Uint8Array} data - PCM frame payload (640 bytes).
     * @param {number} sequence - frame sequence number for dedup.
     */
    playLiveFrame(data, sequence) {
      try {
        if (!data || data.length !== this.AUDIO_FRAME_SIZE) {
          console.warn('[audio] live frame ignored: wrong size');
          return;
        }
        if (sequence <= this.lastSeq) {
          console.debug('[audio] live frame duplicate: seq=' + sequence);
          return;
        }
        if (this.lastSeq !== 0 && sequence > this.lastSeq + 1) {
          console.warn('[audio] live frame gap: expected ' + (this.lastSeq + 1) + ', got ' + sequence);
        }
        this.lastSeq = sequence;

        if (this.audioCtx && this.audioCtx.state === 'suspended') {
          var self = this;
          this.audioCtx.resume().then(function () {
            self._scheduleWordPlayback('feedback', [new Uint8Array(data)]);
          }).catch(function (resumeErr) {
            console.error('[audio] resume() failed for live frame:', resumeErr);
          });
          return;
        }
        if (!this.isAudioCtxReady()) {
          console.error('[audio] live frame but AudioContext not running — dropped');
          return;
        }
        this._scheduleWordPlayback('feedback', [new Uint8Array(data)]);
      } catch (liveErr) {
        console.error('[audio] live frame playback failed:', liveErr);
      }
    }

    /**
     * Play a completed word. Called when backend sends word_complete
     * control frame after streaming all audio frames for that word.
     *
     * Takes the ENTIRE rolling accumulator as this word's audio and clears
     * it before scheduling: the completing word owns every frame since the
     * previous word_complete (wire contract — see handleAudioFrame). A
     * REPEAT_WORD re-stream appends fresh frames to the now-cleared buffer,
     * so the repeat's own word_complete plays ONLY the new audio — no stale
     * double-append is possible.
     *
     * Plan 08-04 Task 4: enhanced error handling and audio subsystem validation.
     * P1-1: audio_plays threading — see @param plays.
     *
     * @param {string} wordId - word identifier (log-only; the buffer is global).
     * @param {number} [plays] - 2 plays the pronunciation twice with a 1s
     *        gap between pronunciations (backend audio_plays=2 for
     *        single-pronunciation audio); 1/undefined plays once (legacy
     *        paired audio). Any other value plays once.
     * @returns {number} total scheduled playback time in ms (double play
     *        includes the 1s gap); 0 when nothing was scheduled. The batch
     *        pacing timer (websocket-client _applyWordComplete) adds this to
     *        the writing gap so the gap is not eaten by the double play.
     */
    playWordComplete(wordId, plays) {
      try {
        // Hand off the whole rolling buffer, then swap in a fresh one so
        // frames streamed for the NEXT word (or a REPEAT_WORD re-stream)
        // accumulate from zero.
        var chunks = this.rollingPcm;
        this.rollingPcm = [];
        if (chunks.length === 0) {
          console.warn('[audio] word_complete for "' + wordId + '" — no PCM accumulated');
          return 0;
        }

        // Scheduled playback duration — identical math to
        // _scheduleWordPlayback (numSamples/16000 per play, 1s gap between
        // the two plays), computed up front so every return path below can
        // report it even when scheduling is deferred (suspended ctx).
        var totalLen = 0;
        for (var i = 0; i < chunks.length; i++) {
          totalLen += chunks[i].length;
        }
        var wordSec = (totalLen / 2) / 16000; // s16le mono @16kHz
        var playbackMs = Math.round((plays === 2 ? wordSec * 2 + 1.0 : wordSec) * 1000);

        // If AudioContext is suspended (iOS D-03, app-switch D-06), attempt
        // resume() and defer playback until the context is running. resume()
        // returns a Promise — we chain the actual scheduling onto it.
        if (this.audioCtx && this.audioCtx.state === 'suspended') {
          console.log('[audio] AudioContext suspended — attempting resume for "' + wordId + '"');
          var self = this;
          this.audioCtx.resume().then(function () {
            console.log('[audio] resume() succeeded, now playing "' + wordId + '"');
            self._scheduleWordPlayback(wordId, chunks, plays);
          }).catch(function (resumeErr) {
            console.error('[audio] resume() failed for "' + wordId + '":', resumeErr);
          });
          return playbackMs;
        }

        if (!this.isAudioCtxReady()) {
          console.error('[audio] word_complete for "' + wordId + '" but AudioContext not running — PCM dropped');
          return 0;
        }

        this._scheduleWordPlayback(wordId, chunks, plays);
        return playbackMs;
      } catch (playErr) {
        console.error('[audio] word_complete playback failed for "' + wordId + '":', playErr);
        this.isPlaying = false;
        return 0;
      }
    }

    /**
     * @private Schedule PCM playback for a word. Extracted from playWordComplete
     * so it can be called directly or after an async resume().
     *
     * Buffer lifecycle is the CALLER's job: playWordComplete hands off and
     * clears the rolling accumulator; playLiveFrame passes an ad-hoc
     * single-frame array. This method never touches this.rollingPcm.
     * wordId is log-only (see handleAudioFrame).
     *
     * P1-1: when plays === 2, TWO BufferSources are created from the SAME
     * buffer — the first at scheduledTime, the second one buffer-duration
     * plus a 1s gap later — and scheduledTime advances to the end of the
     * second play. The playback-end callback fires only after the LAST
     * source ends (remaining-count closure). plays === 1 (default)
     * preserves the single-source behavior exactly.
     *
     * @param {string} wordId - word identifier (for logging).
     * @param {Uint8Array[]} chunks - accumulated PCM chunks for this word.
     * @param {number} [plays] - 2 → double pronunciation with 1s gap;
     *        anything else → single play.
     */
    _scheduleWordPlayback(wordId, chunks, plays) {
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
      if (this.scheduledTime < this.audioCtx.currentTime) {
        this.scheduledTime = this.audioCtx.currentTime;
      }

      // P1-1: playback-end fires ONLY after the last scheduled source ends.
      // The generation token (incremented by stop()/cleanup()) invalidates
      // handlers of sources scheduled before a teardown, so a stale source
      // — e.g. the first play of a pair, stopped between plays — can never
      // fire _onPlaybackEnd or clobber isPlaying.
      var generation = this._playbackGeneration;
      var self = this;
      var remaining = plays === 2 ? 2 : 1;
      // Play-phase transitions (第一遍/第二遍/结束) are word-playback only —
      // live 'feedback' frames (the spoken encouragement) never fire them.
      var isWord = wordId !== 'feedback';
      var handleSourceEnd = function () {
        if (self._playbackGeneration !== generation) {
          return; // stop()/cleanup() happened after scheduling — stale source
        }
        remaining -= 1;
        if (remaining > 0) {
          // Double play: the first pronunciation just ended — the second
          // one starts after the 1s gap.
          if (isWord && typeof self._onPlayPhase === 'function') {
            self._onPlayPhase(2);
          }
          return;
        }
        self.isPlaying = false;
        if (isWord && typeof self._onPlayPhase === 'function') {
          self._onPlayPhase(0);
        }
        if (typeof self._onPlaybackEnd === 'function') {
          self._onPlaybackEnd();
        }
      };

      // Compute start times: double play = same buffer twice with a 1s gap
      // between pronunciations; scheduledTime ends up at the second play's
      // end so the next word stays gapless behind it.
      var startTimes = [];
      if (plays === 2) {
        startTimes.push(this.scheduledTime);
        startTimes.push(this.scheduledTime + buf.duration + 1.0);
        this.scheduledTime = startTimes[1] + buf.duration;
      } else {
        startTimes.push(this.scheduledTime);
        this.scheduledTime += buf.duration;
      }

      for (var s = 0; s < startTimes.length; s++) {
        var src = this.audioCtx.createBufferSource();
        src.buffer = buf;
        src.connect(this.audioCtx.destination);

        // Plan 08-04 Task 4: add error handler for source start failure
        src.onerror = function (err) {
          console.error('[audio] BufferSource playback error:', err);
        };

        src.onended = handleSourceEnd;
        src.start(startTimes[s]);
      }

      this.isPlaying = true;

      // First pronunciation starts (scheduled at the audio clock — the tiny
      // lead time is imperceptible, and it keeps the rhythm chip simple).
      if (isWord && typeof this._onPlayPhase === 'function') {
        this._onPlayPhase(1);
      }

      console.log('[audio] word_complete: "' + wordId + '" — scheduled ' + numSamples +
        ' samples (' + (buf.duration * 1000).toFixed(0) + 'ms) x' + startTimes.length +
        ' at audio clock t=' + startTimes[0].toFixed(3) + 's');
    }

    /**
     * Stop playback and reset state. Clears the rolling buffer and resets
     * the dedup counter.
     */
    stop() {
      this.isPlaying = false;
      // P1-1: invalidate onended handlers of any in-flight sources (a
      // BufferSource cannot be cancelled via stop() references we keep —
      // the generation token makes their callbacks no-ops instead).
      this._playbackGeneration += 1;
      this.rollingPcm = [];
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
     * Set the current word ID. Informational only since the rolling
     * accumulator replaced per-wordId routing — frames are never keyed or
     * dropped by this value (see handleAudioFrame).
     *
     * @param {string} wordId - word identifier (log-only context).
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
        // Clear the rolling PCM accumulator (Plan 08-04 Task 4: memory cleanup).
        this.rollingPcm = [];

        // Reset playback state.
        this.isPlaying = false;
        // P1-1: same stale-onended invalidation as stop().
        this._playbackGeneration += 1;
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
