/**
 * @file VoiceCommandController — push-to-talk microphone capture for voice
 *       commands (Ticket 03).
 *
 * Replaces the Web Speech API for COMMAND recognition: the student holds
 * the mic button, speaks a command (好了 / 重读 / 暂停 / 不会写 / 继续),
 * releases, and the server runs Xiaomi Mimo ASR + LLM intent analysis over
 * the streamed PCM (see cmd/service/voice.go). The button controls remain
 * the full fallback — when getUserMedia is unavailable (insecure context,
 * denied permission) this controller reports supported=false and app.js
 * hides the mic button.
 *
 * Capture pipeline (iPad Safari compatible, no build step):
 *   getUserMedia → AudioContext(native rate) → ScriptProcessorNode
 *   → linear-resample to 16kHz → Float32→Int16 → 320-sample (640-byte)
 *   chunks → FrameCodec MSG_TYPE.AUDIO frames → ws.send(binary)
 *
 * On stop(): the final partial chunk is ZERO-PADDED to 640 bytes — the
 * server codec rejects any other audio payload size (D-09) — then a
 * voice_command control frame triggers ASR.
 *
 * ScriptProcessorNode (deprecated but universally supported incl. iPad
 * Safari) is chosen over AudioWorklet deliberately: push-to-talk commands
 * are 2-5s utterances where main-thread jitter is negligible, and
 * AudioWorklet would need a separately-served module file for no
 * user-visible gain. Revisit if continuous capture lands (future avatar
 * work).
 *
 * NOTE: getUserMedia requires a secure context (https or localhost). Over
 * plain-http LAN testing the mic is blocked by the browser → supported
 * stays false and the button fallback serves the session.
 *
 * Exposes: window.VoiceCommandController
 */
(function (global) {
  'use strict';

  /** Target sample rate the wire protocol fixes (20ms@16kHz, D-09). */
  var TARGET_SAMPLE_RATE = 16000;
  /** Samples per outbound chunk (320 samples = 640 bytes, 20ms). */
  var SAMPLES_PER_CHUNK = 320;
  /** Processor buffer size in native-rate samples (power of two, 4096 ≈ 93ms@44.1kHz). */
  var PROCESSOR_BUFFER = 4096;
  /** Hard recording cap (bytes) — mirrors server voice.maxRecordingBytes default (~10.2s). */
  var MAX_RECORDING_BYTES = 327680;

  /**
   * Concatenate Int16Arrays into one flat Int16Array.
   * @private
   * @param {Int16Array[]} arrays
   * @returns {Int16Array}
   */
  function concatInt16(arrays) {
    var total = 0, i;
    for (i = 0; i < arrays.length; i++) total += arrays[i].length;
    var out = new Int16Array(total);
    var off = 0;
    for (i = 0; i < arrays.length; i++) {
      out.set(arrays[i], off);
      off += arrays[i].length;
    }
    return out;
  }

  /**
   * VoiceCommandController captures one push-to-talk recording and ships it
   * to the backend as audio frames plus a voice_command trigger.
   *
   * @class
   * @param {WebSocketClient} wsClient - connected client; needs send()
   *        (control frames) and sendFrame() (binary audio frames).
   */
  class VoiceCommandController {
    constructor(wsClient) {
      this.wsClient = wsClient;

      /** True when the browser exposes getUserMedia (secure context). */
      this.supported = !!(global.navigator &&
        global.navigator.mediaDevices &&
        typeof global.navigator.mediaDevices.getUserMedia === 'function');

      /** True between start() and stop()/cancel(). */
      this.isRecording = false;

      /** True from stop() until resultReceived() (mic UI locked). */
      this.isProcessing = false;

      // Internals (rebuilt per recording).
      this._stream = null;       // active MediaStream
      this._audioCtx = null;     // capture AudioContext (native rate)
      this._processor = null;    // ScriptProcessorNode
      this._source = null;       // MediaStreamAudioSourceNode
      this._pendingNative = [];  // native-rate Int16Arrays not yet resampled
      this._tail = new Int16Array(0); // 16kHz remainder (< 320 samples)
      this._chunkQueue = [];     // complete 320-sample chunks awaiting send
      this._byteCount = 0;       // PCM bytes shipped this recording (cap guard)
      this._seq = 0;             // audio-frame sequence, monotonic per recording
      this._overflowed = false;

      /** @type {function|null} Called with the voice_result payload. */
      this._onResult = null;
      /** @type {function|null} Called with an errorType string. */
      this._onError = null;
    }

    /**
     * Register the voice_result callback (payload = parsed voice_result
     * frame; also invoked with {type:'voice_unavailable'} when the backend
     * rejects voice_command because it is disabled).
     * @param {function} fn
     */
    onResult(fn) { this._onResult = fn; }

    /**
     * Register the error callback (errorType: 'not-supported' |
     * 'mic-denied' | 'mic-unavailable' | 'overflow' | 'start-failed').
     * @param {function} fn
     */
    onError(fn) { this._onError = fn; }

    /**
     * Called by app.js when a voice_result frame arrives: clears the
     * processing lock so the mic button re-enables.
     */
    resultReceived() { this.isProcessing = false; }

    /**
     * Start capturing. Called on pointerdown/keydown. Idempotent while
     * recording or processing. The AudioContext is constructed BEFORE
     * awaiting getUserMedia so it is born inside the user gesture —
     * iPad Safari keeps contexts created outside a gesture suspended, and
     * the permission prompt await leaves the gesture stack.
     *
     * @returns {Promise<boolean>} true when capture actually started.
     */
    async start() {
      if (!this.supported) {
        this._emitError('not-supported');
        return false;
      }
      if (this.isRecording || this.isProcessing) return false;

      var AudioCtx = global.AudioContext || global.webkitAudioContext;
      if (!AudioCtx) {
        this._emitError('not-supported');
        return false;
      }
      this._audioCtx = new AudioCtx();

      try {
        this._stream = await global.navigator.mediaDevices.getUserMedia({
          audio: {
            echoCancellation: true,   // TTS playback leaks into the mic
            noiseSuppression: true,   // classroom environment
            autoGainControl: true     // children's voices vary in level
          }
        });
      } catch (micErr) {
        this._emitError(micErr && micErr.name === 'NotAllowedError' ? 'mic-denied' : 'mic-unavailable');
        this._teardownStream();
        return false;
      }

      try {
        if (this._audioCtx.state === 'suspended' && this._audioCtx.resume) {
          await this._audioCtx.resume();
        }
        this._source = this._audioCtx.createMediaStreamSource(this._stream);

        var self = this;
        this._processor = this._audioCtx.createScriptProcessor(PROCESSOR_BUFFER, 1, 1);
        this._processor.onaudioprocess = function (event) {
          self._handleAudio(event.inputBuffer.getChannelData(0));
        };

        // ScriptProcessor only pulls while connected to a destination;
        // route through a zero-gain node so nothing is audibly played back.
        var mute = this._audioCtx.createGain();
        mute.gain.value = 0;
        this._source.connect(this._processor);
        this._processor.connect(mute);
        mute.connect(this._audioCtx.destination);
      } catch (setupErr) {
        console.error('[voice] capture setup failed:', setupErr);
        this._emitError('start-failed');
        this._teardownStream();
        return false;
      }

      this.isRecording = true;
      return true;
    }

    /**
     * Stop capturing and ship the command. Called on pointerup/keyup.
     * Flushes the resampler, zero-pads the final partial chunk to the fixed
     * 640-byte wire size, then sends the voice_command trigger. Safe to
     * call when not recording (no-op).
     */
    stop() {
      if (!this.isRecording) return;
      this.isRecording = false;

      // Drain the resampler while the AudioContext (source of sampleRate)
      // is still alive, then pad+queue the trailing partial chunk.
      this._resamplePending();
      if (this._tail.length > 0) {
        var padded = new Int16Array(SAMPLES_PER_CHUNK);
        padded.set(this._tail);
        this._tail = new Int16Array(0);
        this._chunkQueue.push(padded);
      }

      this._teardownStream();
      this._flushChunks();

      if (this._overflowed) {
        // The server already spoiled the recording; do not trigger ASR.
        this._overflowed = false;
        this.isProcessing = false;
        this._resetBuffers();
        this._emitError('overflow');
        return;
      }

      this.isProcessing = true;
      this.wsClient.send({ type: 'voice_command', params: {} });
    }

    /**
     * Abort the current recording WITHOUT triggering ASR (e.g. session
     * ended, page hidden). Also clears the processing lock.
     */
    cancel() {
      this.isRecording = false;
      this.isProcessing = false;
      this._overflowed = false;
      this._teardownStream();
      this._resetBuffers();
    }

    /**
     * Release everything (page unload). Mirrors SpeechController.cleanup.
     */
    cleanup() {
      this.cancel();
      this._onResult = null;
      this._onError = null;
    }

    // ------------------------------------------------------------------
    // @private audio path: Float32 → Int16 → 16kHz → 640-byte frames
    // ------------------------------------------------------------------

    /**
     * One native-rate Float32 buffer from the processor: convert to Int16,
     * run the resampler, ship whatever chunks became complete.
     * @private
     * @param {Float32Array} input
     */
    _handleAudio(input) {
      if (!this.isRecording) return;
      var samples = new Int16Array(input.length);
      for (var i = 0; i < input.length; i++) {
        var s = input[i];
        if (s > 1) s = 1; else if (s < -1) s = -1;
        samples[i] = s < 0 ? s * 0x8000 : s * 0x7FFF;
      }
      this._pendingNative.push(samples);
      this._resamplePending();
      this._flushChunks();
    }

    /**
     * Resample every pending native-rate buffer to 16kHz and slice the
     * combined stream (tail + new samples) into complete 320-sample chunks.
     * Linear interpolation is ample for ASR of short spoken commands; the
     * <320-sample remainder waits in _tail for the next buffer or the
     * padded flush at stop().
     * @private
     */
    _resamplePending() {
      if (this._pendingNative.length === 0) return;
      var native = concatInt16(this._pendingNative);
      this._pendingNative = [];

      var source;
      if (!this._audioCtx || this._audioCtx.sampleRate === TARGET_SAMPLE_RATE) {
        source = native; // already at the target rate
      } else {
        var ratio = this._audioCtx.sampleRate / TARGET_SAMPLE_RATE;
        var outLen = Math.floor(native.length / ratio);
        source = new Int16Array(outLen);
        for (var j = 0; j < outLen; j++) {
          var srcPos = j * ratio;
          var i0 = Math.floor(srcPos);
          var frac = srcPos - i0;
          var s0 = native[i0];
          var s1 = (i0 + 1 < native.length) ? native[i0 + 1] : s0;
          source[j] = s0 + (s1 - s0) * frac;
        }
      }

      var combined = concatInt16([this._tail, source]);
      var full = Math.floor(combined.length / SAMPLES_PER_CHUNK);
      for (var c = 0; c < full; c++) {
        this._chunkQueue.push(combined.slice(c * SAMPLES_PER_CHUNK, (c + 1) * SAMPLES_PER_CHUNK));
      }
      this._tail = combined.slice(full * SAMPLES_PER_CHUNK);
    }

    /**
     * Send every queued chunk as a MSG_TYPE.AUDIO frame. Stops shipping
     * past MAX_RECORDING_BYTES and flags overflow (the server independently
     * spoils its copy of the recording — this cap is the client-side
     * courtesy that avoids shipping audio the server will discard).
     * @private
     */
    _flushChunks() {
      while (this._chunkQueue.length > 0) {
        var chunk = this._chunkQueue.shift();
        var bytes = chunk.length * 2;
        if (this._byteCount + bytes > MAX_RECORDING_BYTES) {
          this._overflowed = true;
          this._chunkQueue = [];
          return;
        }
        this._seq++;
        var msgType = (global.FrameCodec && global.FrameCodec.MSG_TYPE.AUDIO) || 1;
        this.wsClient.sendFrame(msgType, this._seq,
          new Uint8Array(chunk.buffer, chunk.byteOffset, bytes));
        this._byteCount += bytes;
      }
    }

    /**
     * Drop all buffered samples/chunks and counters (overflow or reset).
     * @private
     */
    _resetBuffers() {
      this._chunkQueue = [];
      this._pendingNative = [];
      this._tail = new Int16Array(0);
      this._byteCount = 0;
      this._seq = 0;
    }

    /**
     * Disconnect the graph and stop the mic tracks so the iOS recording
     * indicator goes out immediately.
     * @private
     */
    _teardownStream() {
      if (this._processor) {
        try {
          this._processor.disconnect();
          this._processor.onaudioprocess = null;
        } catch (e) { /* already torn down */ }
        this._processor = null;
      }
      if (this._source) {
        try { this._source.disconnect(); } catch (e) { /* ignore */ }
        this._source = null;
      }
      if (this._audioCtx) {
        try { this._audioCtx.close(); } catch (e) { /* ignore */ }
        this._audioCtx = null;
      }
      if (this._stream) {
        var tracks = this._stream.getTracks();
        for (var i = 0; i < tracks.length; i++) tracks[i].stop();
        this._stream = null;
      }
    }

    /**
     * @private
     */
    _emitError(errorType) {
      console.warn('[voice] ' + errorType);
      if (typeof this._onError === 'function') this._onError(errorType);
    }
  }

  global.VoiceCommandController = VoiceCommandController;
})(typeof window !== 'undefined' ? window : this);
