/**
 * @file rt-playback — teacher-audio playback for the /tutor/ page
 *       (Phase 13, Plan 05, RT-05 client half; per-sentence ledger 13-11).
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
 * Per-sentence ledger (13-11, S-01/HIGH-1 client half): every scheduled
 * chunk is booked against its (epoch, sentence_id) entry. One
 * sentence_ack is sent ONLY after BOTH (a) that sentence's audio_end has
 * been seen and (b) EVERY AudioBufferSourceNode scheduled for it has fired
 * onended — exactly once per key. Adjacent-sentence overlap, duplicate
 * audio_end, and old-epoch late onended can never mis-ack another sentence.
 *
 * drainNaturally() (T-13-24b): the terminal-draining primitive. It stops
 * NOTHING and resolves only after every scheduled source's real onended;
 * a watchdog derived from the remaining scheduled audio (max scheduledEnd
 * - currentTime + 3 s, clamped to 3..45 s) is the only path that stopNow()s
 * and resolves 'timed_out' — so a never-firing onended cannot hold the
 * shared playback resources forever. An explicit stopNow mid-drain
 * resolves 'stopped'. The watchdog path never records a sentence_ack.
 *
 * Interruption detection (Pattern 3): while teacher audio is playing, each
 * raw capture frame from rt-capture.js is measured (RMS); energy above
 * INTERRUPT_RMS_THRESHOLD sustained for INTERRUPT_SUSTAIN_MS triggers
 * stopNow + the interrupt control message
 * {epoch, last_sentence_id, played_until_ms}. The speaker-echo threshold is
 * calibrated on the real device (13-07); both constants are URL-tunable
 * (?interrupt_rms=…&interrupt_sustain_ms=…&queue_max=…).
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

  /** Watchdog clamp for drainNaturally (T-13-24b): 3 s minimum, 45 s cap. */
  var DRAIN_WATCHDOG_MIN_MS = 3000;
  var DRAIN_WATCHDOG_MAX_MS = 45000;

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

  /** Ledger identity key: (epoch, sentence_id) as a plain string. */
  function ledgerKey(epoch, sentenceId) {
    return epoch + ':' + sentenceId;
  }

  /**
   * Teacher-audio playback controller.
   * @param {Object} [opts]
   * @param {Object} [opts.timing] - shared TimingLog (rt-client.js)
   * @param {Object} [opts.client] - RTCClient (sentence_ack / interrupt sends)
   * @param {function} [opts.now] - clock for the sustain window (tests)
   * @param {function} [opts.setTimeoutFn] - watchdog timer (node fake clock)
   * @param {function} [opts.clearTimeoutFn] - watchdog clear (node fake clock)
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
    this._setTimeoutFn = opts.setTimeoutFn || function (fn, ms) { return setTimeout(fn, ms); };
    this._clearTimeoutFn = opts.clearTimeoutFn || function (id) { clearTimeout(id); };

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
    /** @type {Array<AudioBufferSourceNode>} every still-scheduled source */
    this._sources = [];
    /**
     * Per-sentence ledger (13-11): key epoch:sentence_id →
     * {epoch, sentenceId, endSeen, acked, remaining, sources}.
     */
    this._ledger = new Map();
    /**
     * Acceptance history (issue #69 rethink3 review P2-7): every
     * (epoch, sentence_id) whose audio_start PASSED the epoch gate,
     * recorded once and NEVER cleared by stopNow / the drain watchdog /
     * releaseSharedResources — the runtime ledger is cancellation state
     * (stopNow clears it on purpose: interrupted audio never acks), the
     * history is EVIDENCE of what was accepted. Judging a rejection from
     * the live ledger alone misreads accepted-but-stopped audio as
     * refused-at-the-gate. Only resetSession drops it: a new /rt
     * connection restarts the epoch space at 0 and old keys would alias
     * the new connection's frames.
     * @type {Map<string, {epoch:number, sentenceId:string}>}
     */
    this._acceptedKeys = new Map();
    this._currentKey = null;
    this.playing = false;
    /** Chunks dropped by the epoch gate or the queue cap (T-13-06c). */
    this.droppedChunks = 0;
    this._sustainFrom = null;
    this._turnFirstAudioDone = false;
    /**
     * Ordered observation trace (e2e): 'onended:<epoch>:<sid>',
     * 'ack:<epoch>:<sid>', 'stopNow', 'drain:drained|timed_out|stopped',
     * plus page notes via note() ('error:*', 'session_ended:*', 'close',
     * 'release'). Causal order on one thread = array order.
     * @type {Array<string>}
     */
    this.trace = [];
    /** Every stopNow count (drain path must leave this untouched). */
    this.stopNowCalls = 0;
    /** Drain bookkeeping (drainNaturally). */
    this._drainPromise = null;
    this._drainSettle = null;
    this._drainCheck = null;
    this._drainInWatchdog = false;
  }

  /**
   * Attach the shared AudioContext (created by RTCapture's gesture). A NEW
   * context is a NEW clock domain: the gapless-scheduling cursor measured on
   * the previous context's clock is meaningless against this one. Without
   * the reset, a 「再练一组」 restart after a natural ending (whose
   * releaseSharedResources — deliberately no stopNow — keeps the old cursor)
   * schedules the fresh session's first chunks at the OLD context's final
   * cursor, tens of seconds into the new context's future: nothing sounds,
   * no onended ever fires and the server's playback-ack wait kills the
   * classroom (finish.spec.js J16 found it; stopNow already zeroes the
   * cursor, so the explicit-end restart path was never affected).
   */
  RTCPlayback.prototype.setAudioContext = function (ctx) {
    this.audioCtx = ctx;
    this._cursor = 0;
  };

  /** Append a page-level observation to the shared ordered trace. */
  RTCPlayback.prototype.note = function (ev) {
    this.trace.push(String(ev));
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
    this._currentKey = null;
  };

  /**
   * The SERVER moved the epoch and told us so (interrupt_ack — either the
   * receipt of this page's own interrupt, or a server-side termination like
   * the subject-pick killing the clarify question). Everything scheduled
   * under the old epoch is dead: stop the queued/live sources NOW (the
   * epoch gate only DROPS future old-epoch chunks — it never stops what is
   * already scheduled), then adopt the new epoch — without the adopt, the
   * next announcement's audio_start (announcements carry no turn_start)
   * would be rejected as a stale epoch and the classroom would die on its
   * playback-ack timeout.
   * @param {number} epoch - the new epoch from interrupt_ack.new_epoch
   */
  RTCPlayback.prototype.onEpochBump = function (epoch) {
    this.stopNow();
    this._epoch = epoch;
  };

  /**
   * A NEW /rt connection begins (the page's start flow). The previous
   * session's teardown (stopEverything or the terminal drain) already
   * stopped its audio — clear any stragglers silently, and CRITICALLY drop
   * the epoch: every server session numbers epochs from 0, so a page whose
   * player epoch was bumped (a subject pick, an interrupt) must ADOPT the
   * next connection's fresh epoch space (onAudioStart) instead of rejecting
   * its announcement audio as "stale" — announcements carry no turn_start,
   * so nothing else would ever move the epoch back down (J15/J16: the
   * in-page restart after 结束 otherwise died on playback_ack_timeout).
   */
  RTCPlayback.prototype.resetSession = function () {
    if (this._sources.length) this.stopNow();
    this._ledger.clear();
    // New /rt connection = new epoch space from 0: the acceptance history
    // of the PREVIOUS connection must not alias this one's keys (P2-7).
    this._acceptedKeys.clear();
    this._epoch = null;
    this._turnFirstAudioDone = false;
    this._sentenceId = null;
    this._sentenceEndSeen = false;
    this._acceptingAudio = false;
    this._currentKey = null;
  };

  /**
   * First audio chunk of one sentence announced. A stale epoch (older than
   * the current turn's) is ignored outright.
   */
  RTCPlayback.prototype.onAudioStart = function (epoch, sentenceId) {
    if (this._epoch === null) {
      // First announcement after resetSession: adopt the new connection's
      // epoch space wholesale (see resetSession).
      this._epoch = epoch;
    }
    if (epoch !== this._epoch) return;
    var key = ledgerKey(epoch, sentenceId);
    // Acceptance happened: the gate let this frame book. Recorded in the
    // history BEFORE the ledger bookkeeping so a later stopNow (which clears
    // the ledger) can never erase the fact that audio was accepted (P2-7).
    if (!this._acceptedKeys.has(key)) {
      this._acceptedKeys.set(key, { epoch: epoch, sentenceId: sentenceId });
    }
    this._sentenceId = sentenceId;
    this._sentenceEndSeen = false;
    this._acceptingAudio = true;
    this._sentenceSamples = 0;
    this._sentenceScheduledEnd = this._cursor;
    this._currentKey = key;
    var entry = this._ledger.get(key);
    if (!entry) {
      this._ledger.set(key, {
        epoch: epoch, sentenceId: sentenceId,
        endSeen: false, acked: false, remaining: 0, sources: []
      });
    } else if (!entry.acked) {
      // Same (epoch, sentence_id) announced again: restart its bookkeeping.
      entry.endSeen = false;
      entry.remaining = 0;
      entry.sources = [];
    }
  };

  /**
   * One downlink PCM chunk (ArrayBuffer, s16le mono @ TTS_RATE): convert,
   * wrap in an AudioBuffer, and schedule gaplessly — playback starts as the
   * chunk arrives, never after the sentence completes. The chunk is booked
   * against the sentence announced by the latest audio_start.
   */
  RTCPlayback.prototype.onAudioChunk = function (arrayBuffer) {
    if (!this._acceptingAudio) { this.droppedChunks += 1; return; } // late revival guard
    if (this._sources.length >= this.maxQueuedChunks) { this.droppedChunks += 1; return; } // T-13-06c
    if (!this.audioCtx || !arrayBuffer || !arrayBuffer.byteLength) { this.droppedChunks += 1; return; }

    var entry = this._currentKey ? this._ledger.get(this._currentKey) : null;
    if (!entry) { this.droppedChunks += 1; return; } // chunk before its audio_start

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
    entry.remaining += 1;
    entry.sources.push(source);
    source.__rtKey = this._currentKey;
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
      self._onSourceEnded(source);
    };
    source.onerror = function (err) {
      if (self._generation !== generation) return;
      console.error('[rt-playback] source error:', err);
    };
  };

  /**
   * One scheduled source actually finished playing (real onended, current
   * generation). Books the completion against its (epoch, sentence_id)
   * ledger entry — never against whatever sentence happens to be current.
   * @private
   */
  RTCPlayback.prototype._onSourceEnded = function (source) {
    var key = source.__rtKey;
    var entry = (key !== undefined && key !== null) ? this._ledger.get(key) : null;
    var idx = this._sources.indexOf(source);
    if (idx >= 0) this._sources.splice(idx, 1);
    if (entry) {
      entry.remaining = Math.max(0, entry.remaining - 1);
      var eIdx = entry.sources.indexOf(source);
      if (eIdx >= 0) entry.sources.splice(eIdx, 1);
      this.trace.push('onended:' + key);
      this._maybeAckEntry(entry);
    }
    if (this._sources.length === 0) {
      this.playing = false;
    }
    if (this._drainCheck) this._drainCheck();
  };

  /**
   * Last audio chunk of one sentence announced. Marks that sentence's
   * ledger entry; the ack still waits for every source's real onended.
   * Duplicate audio_end for the same key is idempotent.
   */
  RTCPlayback.prototype.onAudioEnd = function (epoch, sentenceId) {
    if (epoch !== this._epoch) return;
    var entry = this._ledger.get(ledgerKey(epoch, sentenceId));
    if (!entry) return;
    entry.endSeen = true;
    this._sentenceEndSeen = true;
    this._maybeAckEntry(entry);
  };

  /**
   * Send this sentence's sentence_ack exactly once — only when its
   * audio_end has been seen AND every scheduled source of that sentence
   * has fired onended (13-07 SentenceAckMsg contract: actual playback
   * completion, never a start signal, never evidence of another sentence).
   * @private
   */
  RTCPlayback.prototype._maybeAckEntry = function (entry) {
    if (entry.acked || !entry.endSeen || entry.remaining > 0) return;
    entry.acked = true;
    var key = ledgerKey(entry.epoch, entry.sentenceId);
    this.trace.push('ack:' + key);
    if (this._currentKey === key) {
      // Out-of-contract guard applies only to the sentence that WAS current:
      // a late-completing earlier sentence must not close the current one's
      // chunk gate.
      this._currentKey = null;
      this._sentenceId = null;
      this._sentenceEndSeen = false;
      this._acceptingAudio = false;
    }
    if (this.timing) {
      this.timing.push('sentence_ack', { epoch: entry.epoch, sentence_id: entry.sentenceId });
    }
    if (this.client) {
      this.client.sendJSON({
        type: 'sentence_ack', sentence_id: entry.sentenceId, epoch: entry.epoch
      });
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
   * clear the whole ledger and close the epoch gate — interrupted audio
   * never revives. interrupt_stop is logged only when playback was actually
   * live (a teardown of an already-quiet session is not an interruption).
   */
  RTCPlayback.prototype.stopNow = function () {
    this.stopNowCalls += 1;
    this.trace.push('stopNow');
    var wasLive = this.playing || this._sources.length > 0;
    this._generation += 1; // P1-1: invalidate every captured token
    var sources = this._sources;
    this._sources = [];
    for (var i = 0; i < sources.length; i++) {
      try { sources[i].stop(); } catch (stopErr) { /* already ended */ }
    }
    this._ledger.clear(); // explicit cancellation: no entry can ack afterwards
    this._currentKey = null;
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
    // A stopNow during terminal draining resolves it as 'stopped' — unless
    // the drain's own watchdog is the caller (that path resolves
    // 'timed_out' itself, T-13-24b).
    if (this._drainSettle && !this._drainInWatchdog) {
      var settle = this._drainSettle;
      this._drainSettle = null;
      settle('stopped');
    }
  };

  /**
   * Detach the shared playback resources after a NATURAL drain (never
   * mid-audio): clear the completed ledger and drop the AudioContext
   * reference. Deliberately does NOT stopNow — the drain path's
   * stopNowCalls count must stay clean (T-13-24: no truncation evidence on
   * the natural path). A restarted session re-attaches a fresh context via
   * setAudioContext.
   */
  RTCPlayback.prototype.releaseSharedResources = function () {
    this._ledger.clear();
    this._currentKey = null;
    this._sentenceId = null;
    this._sentenceEndSeen = false;
    this.audioCtx = null;
  };

  /**
   * Terminal-draining primitive (13-11, T-13-24b): wait for every
   * scheduled source's REAL onended without stopping anything, then
   * resolve 'drained'. A watchdog derived from the remaining scheduled
   * audio (+3 s, clamped to 3..45 s) is the ONLY internal escape: it
   * stopNow()s and resolves 'timed_out' — and because the ledger is then
   * cleared, it never records a sentence_ack. An explicit stopNow mid-drain
   * resolves 'stopped'. Repeat calls while a drain is active return the
   * same promise; after completion a fresh call resolves immediately.
   * @returns {Promise<string>} 'drained' | 'timed_out' | 'stopped'
   */
  RTCPlayback.prototype.drainNaturally = function () {
    if (this._drainPromise) return this._drainPromise;
    var self = this;
    this._drainPromise = new Promise(function (resolve) {
      var settled = false;
      var watchdogId = null;
      function finish(result) {
        if (settled) return;
        settled = true;
        self._drainPromise = null;
        self._drainSettle = null;
        self._drainCheck = null;
        if (watchdogId !== null) self._clearTimeoutFn(watchdogId);
        self.trace.push('drain:' + result);
        resolve(result);
      }
      self._drainSettle = finish;
      self._drainCheck = function () {
        if (!settled && self._sources.length === 0) finish('drained');
      };
      var nowSec = self.audioCtx ? self.audioCtx.currentTime : 0;
      var remainMs = Math.max(0, (self._cursor - nowSec) * 1000);
      var waitMs = Math.round(remainMs + 3000);
      if (waitMs < DRAIN_WATCHDOG_MIN_MS) waitMs = DRAIN_WATCHDOG_MIN_MS;
      if (waitMs > DRAIN_WATCHDOG_MAX_MS) waitMs = DRAIN_WATCHDOG_MAX_MS;
      watchdogId = self._setTimeoutFn(function () {
        self._drainInWatchdog = true;
        self.stopNow();
        self._drainInWatchdog = false;
        finish('timed_out');
      }, waitMs);
      self._drainCheck();
    });
    return this._drainPromise;
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
  global.DRAIN_WATCHDOG_MIN_MS = DRAIN_WATCHDOG_MIN_MS;
  global.DRAIN_WATCHDOG_MAX_MS = DRAIN_WATCHDOG_MAX_MS;
  global.TTS_RATE = TTS_RATE;
})(typeof window !== 'undefined' ? window : this);
