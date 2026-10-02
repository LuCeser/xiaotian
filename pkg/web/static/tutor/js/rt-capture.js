/**
 * @file rt-capture — continuous microphone capture for the /tutor/ page
 *       (Phase 13, Plan 05, RT-02).
 *
 * ONE user gesture authorizes the whole session (no per-utterance
 * hold-to-talk): RTCapture.start() must be called synchronously inside the
 * start-button click handler. It then, in order:
 *
 *   1. Synchronously runs the iOS 3-condition unlock INSIDE the gesture
 *      (new AudioContext + resume + silent-buffer start — no await; mirrors
 *      audio-controller.js D-03, except the context is constructed WITHOUT
 *      a sampleRate constraint so the capture graph runs at the device's
 *      real rate, typically 48000 on iOS — Pitfall 5: never assume 16 kHz).
 *   2. Awaits getUserMedia with the classroom constraint triple
 *      (echoCancellation / noiseSuppression / autoGainControl) and holds
 *      the stream for the whole session.
 *   3. Wires the capture graph: AudioWorklet (js/rt-pcm-worklet.js) when
 *      available, ScriptProcessor(4096,1,1) with the zero-gain mute path
 *      otherwise (render-thread jank would drop audio in a long session,
 *      hence worklet-first).
 *
 * Every frame is downsampled from the MEASURED audioCtx.sampleRate to
 * 16 kHz s16le (linear interpolation + remainder buffer, algorithm copied
 * from voice-command-controller.js lines 262-309) and handed to the
 * onUplink callback as an Int16Array for the /rt binary channel. Raw
 * Float32 frames are also forwarded via onFrame so rt-playback.js can run
 * its RMS interruption detection while the teacher speaks (Pattern 3).
 *
 * stop() releases every MediaStreamTrack and disconnects the graph but
 * keeps the AudioContext — visibilitychange only resumes it, never
 * rebuilds (D-06). That is the child-audio privacy boundary: a stopped
 * session never keeps the mic open.
 *
 * Pure functions resampleTo16k / floatToInt16 are exported for
 * tests/test-rt-playback.js (same IIFE/global pattern as rt-client.js).
 */
(function (global) {
  'use strict';

  /** The only uplink rate the /rt protocol accepts (ValidateAudioInit). */
  var TARGET_SAMPLE_RATE = 16000;

  /**
   * Emit block = 320 samples (20 ms at 16 kHz) — keeps WebSocket binary
   * frames near the legacy 640-byte rhythm. /rt has no fixed wire frame
   * size; the block only prevents tiny fragmented frames and gives the
   * remainder buffer its meaning.
   */
  var EMIT_BLOCK_SAMPLES = 320;

  /**
   * Convert one Float32 frame to clamped Int16.
   * Clamp to [-1, 1] BEFORE scaling (voice-command-controller lines 262-267):
   * +1.0 → 32767, -1.0 → -32768.
   * @param {Float32Array} input
   * @returns {Int16Array}
   */
  function floatToInt16(input) {
    var out = new Int16Array(input.length);
    for (var i = 0; i < input.length; i++) {
      var s = input[i];
      if (s > 1) s = 1; else if (s < -1) s = -1;
      out[i] = s < 0 ? s * 0x8000 : s * 0x7FFF;
    }
    return out;
  }

  /** Concatenate Int16Arrays into one (allocation helper). */
  function concatInt16(parts) {
    var total = 0;
    for (var p = 0; p < parts.length; p++) total += parts[p].length;
    var out = new Int16Array(total);
    var at = 0;
    for (var q = 0; q < parts.length; q++) {
      out.set(parts[q], at);
      at += parts[q].length;
    }
    return out;
  }

  /**
   * Downsample one native-rate Float32 frame to 16 kHz Int16, carrying the
   * previous call's unconsumed 16 kHz remainder (algorithm copied verbatim
   * in spirit from voice-command-controller.js lines 281-309):
   *
   *   1. floatToInt16 (clamp + scale)
   *   2. if inputRate !== 16000: linear interpolation by
   *      ratio = inputRate / 16000 (floor per call — same accepted
   *      sub-sample loss as the shipped controller; ASR-tolerant)
   *   3. prepend `tail`, emit complete EMIT_BLOCK_SAMPLES blocks, keep the
   *      remainder as the new tail
   *
   * @param {Float32Array} input - native-rate samples from the capture graph
   * @param {number} inputRate - MEASURED audioCtx.sampleRate (e.g. 48000)
   * @param {Int16Array} [tail] - leftover 16 kHz samples from the last call
   * @returns {{output: Int16Array, tail: Int16Array}}
   */
  function resampleTo16k(input, inputRate, tail) {
    var carried = tail || new Int16Array(0);
    var native = floatToInt16(input);

    var resampled;
    if (!inputRate || inputRate === TARGET_SAMPLE_RATE) {
      resampled = native; // device already runs at the target rate
    } else {
      var ratio = inputRate / TARGET_SAMPLE_RATE;
      var outLen = Math.floor(native.length / ratio);
      resampled = new Int16Array(outLen);
      for (var j = 0; j < outLen; j++) {
        var srcPos = j * ratio;
        var i0 = Math.floor(srcPos);
        var frac = srcPos - i0;
        var s0 = native[i0];
        var s1 = (i0 + 1 < native.length) ? native[i0 + 1] : s0;
        resampled[j] = s0 + (s1 - s0) * frac;
      }
    }

    var combined = concatInt16([carried, resampled]);
    var fullBlocks = Math.floor(combined.length / EMIT_BLOCK_SAMPLES);
    var emitSamples = fullBlocks * EMIT_BLOCK_SAMPLES;
    return {
      output: combined.slice(0, emitSamples),
      tail: combined.slice(emitSamples)
    };
  }

  /**
   * Continuous capture controller (see file header for the lifecycle).
   * @param {Object} [opts]
   * @param {Object} [opts.timing] - shared TimingLog (rt-client.js)
   * @constructor
   */
  function RTCapture(opts) {
    opts = opts || {};
    this.timing = opts.timing || null;
    /** Shared AudioContext — created inside the start() gesture. */
    this.audioCtx = null;
    this._stream = null;
    this._source = null;
    this._workletNode = null;
    this._processor = null;
    this._mute = null;
    this._tail = new Int16Array(0);
    this._onUplink = null;
    this._onFrame = null;
    this._onError = null;
    this._usingWorklet = false;
    this.isCapturing = false;
  }

  /** Register the Int16Array uplink sink (one call per emitted block). */
  RTCapture.prototype.onUplink = function (fn) { this._onUplink = fn; };

  /** Register the raw Float32 frame tap (playback-side RMS detection). */
  RTCapture.prototype.onFrame = function (fn) { this._onFrame = fn; };

  /**
   * Register the failure callback: 'mic-denied' (NotAllowedError) or
   * 'mic-unavailable' (everything else) — RT-10 requires the split.
   */
  RTCapture.prototype.onError = function (fn) { this._onError = fn; };

  /**
   * Start capturing. MUST be called synchronously inside a user gesture:
   * the iOS 3-condition unlock below runs before the first await so the
   * AudioContext is born inside the gesture stack (iPad Safari keeps
   * contexts created outside a gesture suspended).
   *
   * @returns {Promise} resolves when the stream is live and the graph is
   *          wired; rejects with the error type string
   *          ('mic-denied' | 'mic-unavailable').
   */
  RTCapture.prototype.start = function () {
    var self = this;
    if (this.isCapturing) return Promise.resolve(true);

    var AudioCtx = global.AudioContext || global.webkitAudioContext;
    if (!AudioCtx) {
      this._fail('mic-unavailable');
      return Promise.reject('mic-unavailable');
    }

    // --- 3-condition unlock, synchronously inside the gesture ------------
    // NO sampleRate constraint: record what the device actually gives us
    // (iOS typically 48000) and downsample ourselves — Pitfall 5.
    try {
      this.audioCtx = new AudioCtx();
      if (this.audioCtx.resume) this.audioCtx.resume(); // condition 2, sync
      var silentBuf = this.audioCtx.createBuffer(1, 1, this.audioCtx.sampleRate);
      silentBuf.getChannelData(0)[0] = 0;
      var silentSource = this.audioCtx.createBufferSource(); // condition 3
      silentSource.buffer = silentBuf;
      silentSource.connect(this.audioCtx.destination);
      silentSource.start(0);
    } catch (unlockErr) {
      this._fail('mic-unavailable');
      return Promise.reject('mic-unavailable');
    }

    // --- permission + stream (async; gesture already banked) -------------
    var nav = global.navigator;
    return nav.mediaDevices.getUserMedia({
      audio: {
        echoCancellation: true,   // teacher TTS leaks into the mic
        noiseSuppression: true,   // classroom environment
        autoGainControl: true     // children's voices vary in level
      }
    }).then(function (stream) {
      self._stream = stream;
      return self._wireGraph();
    }).then(function () {
      self.isCapturing = true;
      return true;
    })['catch'](function (micErr) {
      var type = (micErr && micErr.name === 'NotAllowedError') ||
        micErr === 'mic-denied' ? 'mic-denied' : 'mic-unavailable';
      self._teardownGraph();
      self._fail(type);
      throw type;
    });
  };

  /**
   * Wire the capture graph: AudioWorklet when available, ScriptProcessor
   * fallback otherwise. Both end in _handleFrame.
   * @private
   */
  RTCapture.prototype._wireGraph = function () {
    var self = this;
    var ctx = this.audioCtx;
    this._source = ctx.createMediaStreamSource(this._stream);
    this._mute = ctx.createGain();
    this._mute.gain.value = 0; // capture only — nothing is played back

    var useWorklet = !!(ctx.audioWorklet && typeof ctx.audioWorklet.addModule === 'function');
    if (!useWorklet) {
      return Promise.resolve(self._wireScriptProcessor());
    }
    return ctx.audioWorklet.addModule('js/rt-pcm-worklet.js').then(function () {
      self._usingWorklet = true;
      self._workletNode = new global.AudioWorkletNode(ctx, 'rt-pcm');
      self._workletNode.port.onmessage = function (ev) {
        self._handleFrame(ev.data);
      };
      self._source.connect(self._workletNode);
      self._workletNode.connect(self._mute);
      self._mute.connect(ctx.destination); // worklet is pulled only when connected
    })['catch'](function (workletErr) {
      console.warn('[rt-capture] AudioWorklet unavailable, falling back to ScriptProcessor:', workletErr);
      self._usingWorklet = false;
      return self._wireScriptProcessor();
    });
  };

  /**
   * ScriptProcessor fallback with the mute wiring copied from
   * voice-command-controller lines 159-173 (the processor is only pulled
   * while connected to a destination; zero gain keeps it silent).
   * @private
   */
  RTCapture.prototype._wireScriptProcessor = function () {
    var self = this;
    this._processor = this.audioCtx.createScriptProcessor(4096, 1, 1);
    this._processor.onaudioprocess = function (event) {
      self._handleFrame(event.inputBuffer.getChannelData(0));
    };
    this._source.connect(this._processor);
    this._processor.connect(this._mute);
    this._mute.connect(this.audioCtx.destination);
    return true;
  };

  /**
   * One native-rate Float32 frame: tap the raw frame for playback-side
   * interruption detection, then downsample + emit complete blocks.
   * @private
   */
  RTCapture.prototype._handleFrame = function (frame) {
    if (!this.isCapturing && !frame) return;
    if (typeof this._onFrame === 'function') {
      this._onFrame(frame);
    }
    var result = resampleTo16k(frame, this.audioCtx ? this.audioCtx.sampleRate : TARGET_SAMPLE_RATE, this._tail);
    this._tail = result.tail;
    if (result.output.length > 0 && typeof this._onUplink === 'function') {
      this._onUplink(result.output);
    }
  };

  /**
   * Stop capturing and release everything mic-related: every
   * MediaStreamTrack is stopped and the graph is disconnected. The
   * AudioContext SURVIVES — visibilitychange only resumes it, never
   * rebuilds (D-06) — but no audio leaves the device after this returns
   * (child-audio privacy boundary).
   */
  RTCapture.prototype.stop = function () {
    this.isCapturing = false;
    this._teardownGraph();
  };

  /** @private */
  RTCapture.prototype._teardownGraph = function () {
    if (this._stream) {
      var tracks = this._stream.getTracks ? this._stream.getTracks() : [];
      for (var i = 0; i < tracks.length; i++) {
        if (tracks[i].stop) tracks[i].stop();
      }
      this._stream = null;
    }
    var nodes = [this._workletNode, this._processor, this._source, this._mute];
    for (var n = 0; n < nodes.length; n++) {
      if (nodes[n] && nodes[n].disconnect) {
        try { nodes[n].disconnect(); } catch (discErr) { /* already gone */ }
      }
    }
    this._workletNode = null;
    this._processor = null;
    this._source = null;
    this._mute = null;
    this._usingWorklet = false;
    this._tail = new Int16Array(0);
  };

  /** @private */
  RTCapture.prototype._fail = function (type) {
    if (typeof this._onError === 'function') {
      this._onError(type);
    }
  };

  global.RTCapture = RTCapture;
  global.resampleTo16k = resampleTo16k;
  global.floatToInt16 = floatToInt16;
  global.TARGET_SAMPLE_RATE = TARGET_SAMPLE_RATE;
  global.EMIT_BLOCK_SAMPLES = EMIT_BLOCK_SAMPLES;
})(typeof window !== 'undefined' ? window : this);
