/**
 * @file rt-playback — teacher-audio playback for the /tutor/ page
 *       (Phase 13, Plan 05, RT-05 client half).
 *
 * Plays each 24 kHz s16le downlink chunk AS IT ARRIVES (never waits for a
 * whole sentence), schedules gaplessly on the audio clock, and never lets
 * interrupted audio revive:
 *
 *   - generation token (audio-controller.js P1-1 pattern): every async
 *     callback captures the token at schedule time and returns immediately
 *     when it has moved on — stopNow() invalidates all in-flight callbacks.
 *   - epoch gate: after a stop, _acceptingAudio stays false until the next
 *     audio_start of the CURRENT epoch, so late chunks from the cancelled
 *     turn are dropped (迟到复活防线).
 *   - queue cap (T-13-06c): a flooding server cannot balloon page memory —
 *     beyond MAX_QUEUED_CHUNKS scheduled-ahead sources, chunks are dropped
 *     and counted.
 *
 * Interruption detection (Pattern 3): while teacher audio is playing, each
 * raw capture frame from rt-capture.js is measured (RMS); energy above
 * INTERRUPT_RMS_THRESHOLD sustained for INTERRUPT_SUSTAIN_MS triggers
 * stopNow + the interrupt control message
 * {epoch, last_sentence_id, played_until_ms}. The speaker-echo threshold is
 * calibrated on the real device (13-07); both constants are URL-tunable
 * (?interrupt_rms=…&interrupt_sustain_ms=…&queue_max=…).
 *
 * sentence_ack is sent when the last scheduled source of a sentence ends
 * with a valid token and its audio_end has been seen — the "actually
 * played" ground truth for history trimming and RT-04 measurement.
 *
 * playedUntilMs is a pure exported function (tests/test-rt-playback.js);
 * same IIFE/global pattern as rt-client.js.
 */
(function (global) {
  'use strict';

  /** Downlink TTS rate (pkg/rt/protocol.go TTSOutputSampleRate). */
  var TTS_RATE = 24000;

  /** Interruption detection defaults — calibrate on the real iPad (13-07). */
  var INTERRUPT_RMS_THRESHOLD = 0.08;
  var INTERRUPT_SUSTAIN_MS = 200;

  /** Scheduled-ahead chunk cap (T-13-06c, iPad page memory self-defense). */
  var MAX_QUEUED_CHUNKS = 64;

  /** Scheduling lead: start no earlier than now + 60 ms of audio clock. */
  var SCHEDULE_LEAD_SEC = 0.06;

  /**
   * Apply URL-parameter tuning overrides. Accepts anything with .get()
   * (URLSearchParams) or a plain object. Invalid values are ignored so a
   * typo can never zero out a threshold.
   * @param {Object} params
   * @returns {{interruptRms: number, interruptSustainMs: number, maxQueuedChunks: number}}
   */
  function applyTuning(params) {
    var tuning = {
      interruptRms: INTERRUPT_RMS_THRESHOLD,
      interruptSustainMs: INTERRUPT_SUSTAIN_MS,
      maxQueuedChunks: MAX_QUEUED_CHUNKS
    };
    if (!params) return tuning;
    function pick(name) {
      if (typeof params.get === 'function') return params.get(name);
      return params[name];
    }
    function pos(name, current) {
      var v = parseFloat(pick(name));
      return isFinite(v) && v > 0 ? v : current;
    }
    tuning.interruptRms = pos('interrupt_rms', tuning.interruptRms);
    tuning.interruptSustainMs = pos('interrupt_sustain_ms', tuning.interruptSustainMs);
    tuning.maxQueuedChunks = Math.floor(pos('queue_max', tuning.maxQueuedChunks));
    return tuning;
  }

  /**
   * Pure estimate of how far into one sentence the student actually heard,
   * in milliseconds, clamped to [0, totalMs].
   *
   *   totalMs     = totalSamples / sampleRate * 1000 (the whole sentence)
   *   remainingMs = max(0, scheduledEnd - currentTime) on the audio clock
   *   playedMs    = totalMs - remainingMs
   *
   * @param {number} scheduledEndSec - audio-clock time the sentence's last
   *        source was scheduled to end
   * @param {number} currentTimeSec - audio-clock now
   * @param {number} totalSamples - sentence samples scheduled so far
   * @param {number} sampleRate - TTS_RATE
   * @returns {number} whole milliseconds of sentence actually played
   */
  function playedUntilMs(scheduledEndSec, currentTimeSec, totalSamples, sampleRate) {
    var totalMs = (totalSamples / sampleRate) * 1000;
    var remainingMs = Math.max(0, (scheduledEndSec - currentTimeSec) * 1000);
    var played = totalMs - remainingMs;
    if (played < 0) played = 0;
    if (played > totalMs) played = totalMs;
    return Math.round(played);
  }

  /** Monotonic millisecond clock (injectable for tests). */
  function defaultNow() {
    if (typeof performance !== 'undefined' && performance && typeof performance.now === 'function') {
      return performance.now();
    }
    return Date.now();
  }

  /**
   * Teacher-audio playback controller.
   * @param {Object} [opts]
   * @param {Object} [opts.timing] - shared TimingLog (rt-client.js)
   * @param {Object} [opts.client] - RTCClient (sentence_ack / interrupt sends)
   * @param {function} [opts.now] - clock for the sustain window (tests)
   * @param {Object} [opts.tuning] - explicit tuning overrides (tests); when
   *        absent, URL parameters are honored in a browser
   * @constructor
   */
  function RTCPlayback(opts) {
    opts = opts || {};
    this.timing = opts.timing || null;
    this.client = opts.client || null;
    this.audioCtx = null;
    this._now = opts.now || defaultNow;

    var tuning = opts.tuning;
    if (!tuning && typeof global.location !== 'undefined' && global.location &&
        global.location.search && typeof global.URLSearchParams === 'function') {
      tuning = applyTuning(new global.URLSearchParams(global.location.search));
    }
    tuning = tuning || {};
    this.interruptRms = tuning.interruptRms || INTERRUPT_RMS_THRESHOLD;
    this.interruptSustainMs = tuning.interruptSustainMs || INTERRUPT_SUSTAIN_MS;
    this.maxQueuedChunks = tuning.maxQueuedChunks || MAX_QUEUED_CHUNKS;

    /** P1-1 generation token — captured by every async callback. */
    this._generation = 0;
    this._epoch = 0;
    this._turnId = 0;
    this._sentenceId = null;
    this._sentenceEndSeen = false;
    /** False after stopNow/turn boundary until a matching audio_start. */
    this._acceptingAudio = false;
    /** Audio-clock cursor for gapless scheduling across sentences. */
    this._cursor = 0;
    this._sentenceScheduledEnd = 0;
    this._sentenceSamples = 0;
    this._pendingSources = 0;
    /** @type {Array<AudioBufferSourceNode>} */
    this._sources = [];
    this.playing = false;
    /** Chunks dropped by the epoch gate or the queue cap (T-13-06c). */
    this.droppedChunks = 0;
    this._sustainFrom = null;
    this._turnFirstAudioDone = false;
  }

  /** Attach the shared AudioContext (created by RTCapture's gesture). */
  RTCPlayback.prototype.setAudioContext = function (ctx) {
    this.audioCtx = ctx;
  };

  /**
   * New turn announced: adopt its epoch and reset per-turn bookkeeping.
   * Audio acceptance waits for the turn's first audio_start (late chunks
   * from a cancelled turn must not revive).
   */
  RTCPlayback.prototype.onTurnStart = function (turnId, epoch) {
    this._turnId = turnId;
    this._epoch = epoch;
    this._turnFirstAudioDone = false;
    this._sentenceId = null;
    this._sentenceEndSeen = false;
    this._acceptingAudio = false;
  };

  /**
   * First audio chunk of one sentence announced. A stale epoch (older than
   * the current turn's) is ignored outright.
   */
  RTCPlayback.prototype.onAudioStart = function (epoch, sentenceId) {
    if (epoch !== this._epoch) return;
    this._sentenceId = sentenceId;
    this._sentenceEndSeen = false;
    this._acceptingAudio = true;
    this._sentenceSamples = 0;
    this._sentenceScheduledEnd = this._cursor;
  };

  /**
   * One downlink PCM chunk (ArrayBuffer, s16le mono @ TTS_RATE): convert,
   * wrap in an AudioBuffer, and schedule gaplessly — playback starts as the
   * chunk arrives, never after the sentence completes.
   */
  RTCPlayback.prototype.onAudioChunk = function (arrayBuffer) {
    if (!this._acceptingAudio) { this.droppedChunks += 1; return; } // late revival guard
    if (this._pendingSources >= this.maxQueuedChunks) { this.droppedChunks += 1; return; } // T-13-06c
    if (!this.audioCtx || !arrayBuffer || !arrayBuffer.byteLength) { this.droppedChunks += 1; return; }

    var bytes = new Int16Array(arrayBuffer);
    var n = bytes.length; // even byteLength only; a trailing odd byte is truncated
    if (n === 0) { this.droppedChunks += 1; return; }

    var floats = new Float32Array(n);
    for (var i = 0; i < n; i++) floats[i] = bytes[i] / 32768;

    var buffer = this.audioCtx.createBuffer(1, n, TTS_RATE);
    buffer.getChannelData(0).set(floats);

    var source = this.audioCtx.createBufferSource();
    source.buffer = buffer;
    source.connect(this.audioCtx.destination);

    var startAt = this.audioCtx.currentTime + SCHEDULE_LEAD_SEC;
    if (this._cursor > startAt) startAt = this._cursor;
    source.start(startAt);
    this._cursor = startAt + n / TTS_RATE;
    this._sentenceScheduledEnd = this._cursor;
    this._sentenceSamples += n;

    var generation = this._generation; // capture BEFORE going async
    var self = this;
    this._pendingSources += 1;
    this._sources.push(source);
    this.playing = true;

    if (!this._turnFirstAudioDone) {
      this._turnFirstAudioDone = true;
      if (this.timing) {
        this.timing.push('first_audio_played', {
          turn_id: this._turnId, epoch: this._epoch, sentence_id: this._sentenceId
        });
      }
    }

    source.onended = function () {
      if (self._generation !== generation) return; // stale callback — revived audio never acts
      self._pendingSources -= 1;
      var idx = self._sources.indexOf(source);
      if (idx >= 0) self._sources.splice(idx, 1);
      if (self._pendingSources <= 0) {
        self._pendingSources = 0;
        self.playing = false;
        self._maybeAckSentence();
      }
    };
    source.onerror = function (err) {
      if (self._generation !== generation) return;
      console.error('[rt-playback] source error:', err);
    };
  };

  /**
   * Last audio chunk of one sentence announced. The ack fires when the
   * final source ends (or immediately if every source already played out).
   */
  RTCPlayback.prototype.onAudioEnd = function (epoch, sentenceId) {
    if (epoch !== this._epoch || sentenceId !== this._sentenceId) return;
    this._sentenceEndSeen = true;
    if (this._pendingSources === 0) {
      this._maybeAckSentence();
    }
  };

  /** @private */
  RTCPlayback.prototype._maybeAckSentence = function () {
    if (!this._sentenceEndSeen || !this._sentenceId) return;
    var sentenceId = this._sentenceId;
    var epoch = this._epoch;
    this._sentenceId = null;
    this._sentenceEndSeen = false;
    this._acceptingAudio = false; // any chunk before the next audio_start is out of contract
    if (this.client) {
      this.client.sendJSON({ type: 'sentence_ack', sentence_id: sentenceId, epoch: epoch });
    }
  };

  /**
   * Raw capture frame (from rt-capture.js onFrame): RMS interruption
   * detection while teacher audio is playing. Above-threshold energy must
   * SUSTAIN interruptSustainMs (intermittent noise resets the window);
   * below-threshold frames clear it.
   */
  RTCPlayback.prototype.onFrame = function (frame) {
    if (!this.playing) {
      this._sustainFrom = null;
      return;
    }
    var sum = 0;
    for (var i = 0; i < frame.length; i++) sum += frame[i] * frame[i];
    var rms = Math.sqrt(sum / frame.length);
    if (rms >= this.interruptRms) {
      if (this._sustainFrom === null) {
        this._sustainFrom = this._now();
      } else if (this._now() - this._sustainFrom >= this.interruptSustainMs) {
        this._triggerInterrupt('rms');
      }
    } else {
      this._sustainFrom = null;
    }
  };

  /**
   * Manual interruption entry point (e2e hook / future UI button): same
   * path as the RMS detector.
   */
  RTCPlayback.prototype.interruptNow = function () {
    this._triggerInterrupt('manual');
  };

  /** @private */
  RTCPlayback.prototype._triggerInterrupt = function (source) {
    // played_until_ms MUST be computed before stopNow clears sentence state.
    var playedUntil = this._playedUntilMsNow();
    var epoch = this._epoch;
    var sentenceId = this._sentenceId || '';
    if (this.timing) {
      this.timing.push('interrupt_start', { epoch: epoch, sentence_id: sentenceId, source: source });
    }
    if (this.client) {
      this.client.sendJSON({
        type: 'interrupt',
        epoch: epoch,
        last_sentence_id: sentenceId,
        played_until_ms: playedUntil
      });
    }
    this.stopNow();
  };

  /**
   * Stop everything immediately: bump the generation token (all in-flight
   * async callbacks become no-ops), stop and forget every scheduled source,
   * clear the queue and close the epoch gate — interrupted audio never
   * revives. interrupt_stop is logged only when playback was actually live
   * (a teardown of an already-quiet session is not an interruption).
   */
  RTCPlayback.prototype.stopNow = function () {
    var wasLive = this.playing || this._pendingSources > 0;
    this._generation += 1; // P1-1: invalidate every captured token
    var sources = this._sources;
    this._sources = [];
    for (var i = 0; i < sources.length; i++) {
      try { sources[i].stop(); } catch (stopErr) { /* already ended */ }
    }
    this._pendingSources = 0;
    this.playing = false;
    this._acceptingAudio = false; // late old-epoch chunks are dropped on arrival
    this._cursor = 0;
    this._sentenceId = null;
    this._sentenceEndSeen = false;
    this._sentenceSamples = 0;
    this._sentenceScheduledEnd = 0;
    this._sustainFrom = null;
    if (wasLive && this.timing) {
      this.timing.push('interrupt_stop', { epoch: this._epoch });
    }
  };

  /** @returns {number} played_until_ms estimate for the current sentence */
  RTCPlayback.prototype._playedUntilMsNow = function () {
    var now = this.audioCtx ? this.audioCtx.currentTime : this._sentenceScheduledEnd;
    return playedUntilMs(this._sentenceScheduledEnd, now, this._sentenceSamples, TTS_RATE);
  };

  global.RTCPlayback = RTCPlayback;
  global.playedUntilMs = playedUntilMs;
  global.applyTuning = applyTuning;
  global.INTERRUPT_RMS_THRESHOLD = INTERRUPT_RMS_THRESHOLD;
  global.INTERRUPT_SUSTAIN_MS = INTERRUPT_SUSTAIN_MS;
  global.MAX_QUEUED_CHUNKS = MAX_QUEUED_CHUNKS;
  global.TTS_RATE = TTS_RATE;
})(typeof window !== 'undefined' ? window : this);
