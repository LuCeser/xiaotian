/**
 * @file rt-client — /rt WebSocket protocol client, client-side timing log,
 *       and clock-offset estimation for the natural real-time tutor page
 *       (Phase 13, Plan 05).
 *
 * Wire contract: pkg/rt/protocol.go (13-03) is the single source of truth.
 * Every JSON field name here mirrors it verbatim — snake_case message-level
 * keys (session_id, turn_id, t_client_ms), camelCase keys inside the audio
 * spec objects (sampleRate, format, channels). The mock e2e suite
 * (tests/e2e-rt/) doubles as the protocol-conformance test.
 *
 *   Client → server: session_init / interrupt / sentence_ack / ping /
 *                    end_session (TextMessage) + bare s16le PCM (BinaryMessage)
 *   Server → client: session_ready / turn_start / transcript /
 *                    teacher_sentence / audio_start / audio_end /
 *                    lesson_update / interrupt_ack / pong / state / error /
 *                    session_ended + bare 24 kHz PCM (BinaryMessage)
 *
 * Browser glue (WebSocket, Blob download) is separated from the pure logic
 * (TimingLog push/toJSONL, estimateClockOffset, URL derivation) so the pure
 * half runs on stock Node for tests/test-rt-timing.js — same IIFE/global
 * pattern as js/timing.js (under Node the `global` argument is
 * module.exports, so require() surfaces every export below).
 */
(function (global) {
  'use strict';

  // ---------------------------------------------------------------------
  // Constants frozen by pkg/rt/protocol.go
  // ---------------------------------------------------------------------

  /** The only uplink audio configuration R0 accepts (ValidateAudioInit). */
  var UPLINK_AUDIO_SPEC = { sampleRate: 16000, format: 's16le', channels: 1 };

  /** Downlink TTS sample rate declared via session_ready.tts.sampleRate. */
  var TTS_SAMPLE_RATE = 24000;

  /** ping cadence after session_ready: one probe immediately, then 10 s. */
  var PING_INTERVAL_MS = 10000;

  /**
   * Client-side timing event vocabulary (RT-04 evidence). The server half
   * lives in pkg/rt/timing.go; the two JSONL streams align by turn/epoch
   * event IDs plus the clock_offset estimate — never by subtracting a
   * client timestamp from a server one.
   */
  var RT_EVENTS = {
    PAGE_READY: 'page_ready',
    AUTH_COMPLETE: 'auth_complete',
    CONNECTED: 'connected',
    FIRST_AUDIO_PLAYED: 'first_audio_played',
    INTERRUPT_START: 'interrupt_start',
    INTERRUPT_STOP: 'interrupt_stop',
    CLOCK_OFFSET: 'clock_offset'
  };

  /**
   * Error code → student-facing Chinese message. Keys cover the server wire
   * codes (pkg/rt/protocol.go ErrCode*) plus the client-local capture error
   * types ('mic-denied' / 'mic-unavailable' from rt-capture.js) — RT-10:
   * every failure must surface a human-readable reason, never a silent
   * dead button.
   */
  var ERROR_MESSAGES = {
    'mic-denied': '麦克风权限被拒绝——请在浏览器的网站设置里允许使用麦克风，然后重新打开本页',
    'mic-unavailable': '麦克风不可用——请检查麦克风是否被其他程序占用，或换最新版 Safari / Chrome 打开',
    'voice_unavailable': '语音服务未开放——服务器缺少语音配置，请联系家长或管理员',
    'provider_failed': '语音服务暂时出错，请稍等片刻再试',
    'unsupported_format': '浏览器上报的音频格式不被支持（需要 16kHz 单声道 PCM）',
    'invalid_message': '通信内容异常，请刷新页面重试',
    'session_closed': '课程会话已结束，请重新开始',
    // 13-07 protocol additions (pkg/rt/protocol.go ErrCode*): the classroom
    // could not start — honest failure, the page offers the start button as
    // the retry entry. Same wording as the 13-08 fixed human constant.
    'lesson_unavailable': '取词失败了，请稍后再试',
    // 13-11 (T-13-24): the server STOPPED WAITING for a playback-completion
    // ack — never evidence that audio was heard. The wording promises the
    // already-scheduled audio keeps playing and must NOT claim completion.
    'playback_ack_timeout': '播放确认超时，老师会先把已收到的语音播完；若卡住将自动结束',
    // 13.1-08 IN-01 (13.1-03 RT-10 degraded path): no OCR reader configured
    // server-side — the check stage keeps working manual-only. Verbatim copy
    // of the server's fixed constant photoUnavailableNotice (pkg/rt/photo.go;
    // wire code pkg/rt/protocol.go ErrCodePhotoUnavailable) — encouraging,
    // child-safe wording.
    'photo_unavailable': '拍照检查现在用不了，我们看着屏幕逐个确认吧'
  };

  /**
   * Map an error code to its human message (RT-10).
   * @param {string} code - wire error code or capture error type
   * @param {string} [fallback] - text for unknown codes
   * @returns {string}
   */
  function humanizeError(code, fallback) {
    if (code && Object.prototype.hasOwnProperty.call(ERROR_MESSAGES, code)) {
      return ERROR_MESSAGES[code];
    }
    return fallback || ('发生了未知错误' + (code ? '（' + code + '）' : ''));
  }

  /**
   * Safe-context capability probe (RT-01): navigator.mediaDevices only
   * exists in secure contexts (HTTPS or localhost). The tutor page calls
   * this at load; when it returns false the page must show the
   * "语音不可用：需要 HTTPS" reason and disable the start button instead of
   * leaving a clickable button that can only fail.
   * @param {Navigator} nav - navigator (injectable for tests)
   * @returns {boolean}
   */
  function isVoiceCaptureAvailable(nav) {
    return !!(nav && nav.mediaDevices && typeof nav.mediaDevices.getUserMedia === 'function');
  }

  /**
   * Derive the /rt WebSocket URL from a location object: wss on https,
   * ws otherwise (localhost dev / mock e2e), same host, path /rt.
   * @param {Location} loc - window.location (injectable for tests)
   * @returns {string|null} null when loc is missing
   */
  function deriveRtUrl(loc) {
    if (!loc || !loc.host) return null;
    var scheme = loc.protocol === 'https:' ? 'wss://' : 'ws://';
    return scheme + loc.host + '/rt';
  }

  /** Monotonic-ish millisecond clock: performance.now when available. */
  function nowMs() {
    if (typeof performance !== 'undefined' && performance && typeof performance.now === 'function') {
      return performance.now();
    }
    return Date.now();
  }

  // ---------------------------------------------------------------------
  // TimingLog — client-side timing evidence (RT-04)
  // ---------------------------------------------------------------------

  /**
   * Ordered client timing event log. push/toJSONL are pure logic (Node
   * testable); exportFile is the DOM download half and is a no-op returning
   * null where document is unavailable.
   *
   * Redaction boundary (T-13-07): entries carry event names, timestamps and
   * IDs only — no transcript text, no audio, no credentials.
   * @constructor
   */
  function TimingLog() {
    /** @type {Array<Object>} */
    this._entries = [];
  }

  /**
   * Record one event at the current client clock.
   * @param {string} event - RT_EVENTS value
   * @param {Object} [extra] - additional ID/duration fields (turn_id, epoch…)
   */
  TimingLog.prototype.push = function (event, extra) {
    var entry = { event: event, t_ms: Math.round(nowMs() * 1000) / 1000 };
    if (extra && typeof extra === 'object') {
      for (var k in extra) {
        if (Object.prototype.hasOwnProperty.call(extra, k) && k !== 'event' && k !== 't_ms') {
          entry[k] = extra[k];
        }
      }
    }
    this._entries.push(entry);
  };

  /** @returns {Array<Object>} shallow copy of every entry */
  TimingLog.prototype.entries = function () {
    return this._entries.slice();
  };

  /**
   * @param {string} event
   * @returns {number} how many entries carry that event name
   */
  TimingLog.prototype.count = function (event) {
    var n = 0;
    for (var i = 0; i < this._entries.length; i++) {
      if (this._entries[i].event === event) n += 1;
    }
    return n;
  };

  /**
   * Serialize to JSON Lines — one JSON object per line, each carrying at
   * least {event, t_ms}. Lines never contain server-side fields (the raw
   * t_server_mono_ns stamp stays inside rt-client; only the derived
   * offset_ms/rtt_ms pair is recorded).
   * @returns {string}
   */
  TimingLog.prototype.toJSONL = function () {
    var lines = [];
    for (var i = 0; i < this._entries.length; i++) {
      lines.push(JSON.stringify(this._entries[i]));
    }
    return lines.length > 0 ? lines.join('\n') + '\n' : '';
  };

  /**
   * Download the JSONL as rt-timing-<date>.jsonl (browser only).
   * @param {function} [blobCtor] - Blob constructor (injectable for tests)
   * @returns {string|null} filename, or null where the DOM is unavailable
   */
  TimingLog.prototype.exportFile = function (blobCtor) {
    if (typeof document === 'undefined' || !document.createElement) return null;
    var data = this.toJSONL();
    if (!data) return null;
    var d = new Date();
    var pad = function (n) { return (n < 10 ? '0' : '') + n; };
    var name = 'rt-timing-' + d.getFullYear() + pad(d.getMonth() + 1) + pad(d.getDate()) +
      '-' + pad(d.getHours()) + pad(d.getMinutes()) + pad(d.getSeconds()) + '.jsonl';
    var BlobCtor = blobCtor || (typeof Blob !== 'undefined' ? Blob : null);
    if (!BlobCtor || typeof URL === 'undefined' || !URL.createObjectURL) return null;
    var url = URL.createObjectURL(new BlobCtor([data], { type: 'application/x-ndjson' }));
    var a = document.createElement('a');
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
    return name;
  };

  // ---------------------------------------------------------------------
  // estimateClockOffset — pure (RT-04 cross-machine alignment, M-6)
  // ---------------------------------------------------------------------

  /**
   * Estimate the client↔server clock offset from one ping/pong exchange
   * under the symmetric-delay assumption (RTT/2 each way).
   *
   *   offset_ms = t_server_mono_ns / 1e6 - (t0_ms + t1_ms) / 2
   *   rtt_ms    = t1_ms - t0_ms
   *
   * t0 is the client clock when the ping left, t1 when the pong arrived,
   * serverMonoNs the server's monotonic stamp (same anchor as every server
   * rt_event t_mono_ns). Cross-machine clocks are NEVER subtracted
   * directly; this estimate is what aligns the two JSONL streams.
   *
   * @param {number} t0Ms - ping send time (client clock, ms)
   * @param {number} t1Ms - pong receive time (client clock, ms)
   * @param {number} serverMonoNs - pong.t_server_mono_ns
   * @returns {{offset_ms: number, rtt_ms: number}}
   */
  function estimateClockOffset(t0Ms, t1Ms, serverMonoNs) {
    var rtt = t1Ms - t0Ms;
    var offset = serverMonoNs / 1e6 - (t0Ms + t1Ms) / 2;
    return {
      offset_ms: Math.round(offset * 1000) / 1000,
      rtt_ms: Math.round(rtt * 1000) / 1000
    };
  }

  // ---------------------------------------------------------------------
  // RTCClient — WebSocket connection + JSON/binary dispatch
  // ---------------------------------------------------------------------

  /**
   * /rt protocol client. Owns the WebSocket, dispatches control messages to
   * a handler dictionary, forwards binary PCM frames to the playback
   * handler, and runs the ping/pong clock-offset probe.
   * @param {Object} [opts]
   * @param {TimingLog} [opts.timing] - shared timing log
   * @param {function} [opts.wsFactory] - WebSocket constructor (tests)
   * @constructor
   */
  function RTCClient(opts) {
    opts = opts || {};
    /** @type {TimingLog} */
    this.timing = opts.timing || new TimingLog();
    this._wsFactory = opts.wsFactory || null;
    /** @type {Object<string, function>} */
    this._handlers = {};
    this._binaryHandler = null;
    this._closeCallback = null;
    this._pingTimer = null;
    this._pingStarted = false;
    this.sessionId = null;
    this.ws = null;
    // 13.1 D-08: UI mode sent in session_init ("student"|"test"). The page
    // sets it BEFORE connect(); absent/invalid normalizes server-side to
    // student, so a page that never sets it stays safe by default.
    this.initMode = null;
    // 13.1-05 D-11: practice flag sent in session_init (「再练一组」). The
    // page sets it BEFORE connect() for ONE practice session; the field is
    // left false by every flow that never touches it (server-side absent ==
    // the daily due lesson).
    this.initPractice = false;
  }

  /**
   * Register the handler for one control message type. pong additionally
   * feeds the built-in clock-offset estimator before the user handler runs.
   * @param {string} type - wire message type ('session_ready', …)
   * @param {function} fn - called with the parsed message object
   */
  RTCClient.prototype.on = function (type, fn) {
    this._handlers[type] = fn;
  };

  /** Register the binary PCM (downlink audio) handler. */
  RTCClient.prototype.onBinary = function (fn) {
    this._binaryHandler = fn;
  };

  /** Register a callback fired when the socket closes. */
  RTCClient.prototype.onClose = function (fn) {
    this._closeCallback = fn;
  };

  /**
   * Open the /rt connection. On open, records 'connected' and sends
   * session_init declaring the (post-downsample) uplink spec — the rate is
   * declared by the client and verified by the server, never assumed.
   * @param {string} [url] - explicit ws(s) URL; default deriveRtUrl(location)
   */
  RTCClient.prototype.connect = function (url) {
    var self = this;
    var wsUrl = url || deriveRtUrl(global.location);
    var WS = this._wsFactory || (typeof WebSocket !== 'undefined' ? WebSocket : null);
    if (!WS || !wsUrl) return false;
    var ws = new WS(wsUrl);
    this.ws = ws;
    ws.binaryType = 'arraybuffer';

    ws.onopen = function () {
      self.timing.push(RT_EVENTS.CONNECTED);
      const initMsg = {
        type: 'session_init',
        audio: UPLINK_AUDIO_SPEC,
        mode: self.initMode || 'student'
      };
      if (self.initPractice === true) initMsg.practice = true; // 13.1-05 D-11
      self.sendJSON(initMsg);
    };

    ws.onmessage = function (ev) {
      var data = ev.data;
      if (typeof data === 'string') {
        var msg = null;
        try {
          msg = JSON.parse(data);
        } catch (parseErr) {
          return; // malformed control frame: ignore, server logs it too
        }
        if (msg && typeof msg.type === 'string') {
          self._dispatch(msg);
        }
        return;
      }
      if (data && data.byteLength !== undefined && typeof self._binaryHandler === 'function') {
        self._binaryHandler(data);
      }
    };

    ws.onclose = function () {
      self._stopPing();
      if (typeof self._closeCallback === 'function') {
        self._closeCallback();
      }
    };
    // onerror deliberately not swallowed into UI state here: the close
    // callback is the single teardown signal the page reacts to.
    ws.onerror = function () { /* surfaced via onclose */ };
    return true;
  };

  /**
   * Internal dispatch: built-in behavior (pong → clock offset,
   * session_ready → ping cadence) runs first, then the page handler.
   * @private
   */
  RTCClient.prototype._dispatch = function (msg) {
    if (msg.type === 'pong') {
      var est = estimateClockOffset(msg.t_client_ms, nowMs(), msg.t_server_mono_ns);
      this.timing.push(RT_EVENTS.CLOCK_OFFSET, { offset_ms: est.offset_ms, rtt_ms: est.rtt_ms });
    } else if (msg.type === 'session_ready') {
      this.sessionId = msg.session_id;
      this._startPing();
    }
    var h = this._handlers[msg.type];
    if (typeof h === 'function') {
      h(msg);
    }
  };

  /** @private */
  RTCClient.prototype._startPing = function () {
    if (this._pingStarted) return;
    this._pingStarted = true;
    this._sendPing();
    var self = this;
    this._pingTimer = setInterval(function () { self._sendPing(); }, PING_INTERVAL_MS);
  };

  /** @private */
  RTCClient.prototype._stopPing = function () {
    if (this._pingTimer !== null) {
      clearInterval(this._pingTimer);
      this._pingTimer = null;
    }
  };

  /** @private */
  RTCClient.prototype._sendPing = function () {
    this.sendJSON({ type: 'ping', t_client_ms: Math.round(nowMs()) });
  };

  /** @returns {boolean} whether the socket is in OPEN state */
  RTCClient.prototype._wsOpen = function () {
    return !!(this.ws && this.ws.readyState === 1);
  };

  /**
   * Send one control message as JSON text.
   * @returns {boolean} false when the socket is not open
   */
  RTCClient.prototype.sendJSON = function (obj) {
    if (!this._wsOpen()) return false;
    this.ws.send(JSON.stringify(obj));
    return true;
  };

  /**
   * Send one binary audio frame (uplink PCM). Accepts an ArrayBuffer or an
   * ArrayBufferView; views with a byteOffset are copied to a standalone
   * buffer so no unrelated memory rides along.
   * @returns {boolean} false when the socket is not open
   */
  RTCClient.prototype.sendBinary = function (data) {
    if (!this._wsOpen()) return false;
    var buf = data;
    if (data && typeof data.buffer !== 'undefined' && data.buffer) {
      buf = data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength);
    }
    this.ws.send(buf);
    return true;
  };

  /** Stop the ping cadence and close the socket (idempotent). */
  RTCClient.prototype.close = function () {
    this._stopPing();
    if (this.ws && (this.ws.readyState === 0 || this.ws.readyState === 1)) {
      try {
        this.ws.close();
      } catch (closeErr) { /* already closing */ }
    }
  };

  // ---------------------------------------------------------------------
  // Exports (window globals in the browser, module.exports under Node)
  // ---------------------------------------------------------------------

  global.RTCClient = RTCClient;
  global.TimingLog = TimingLog;
  global.estimateClockOffset = estimateClockOffset;
  global.humanizeError = humanizeError;
  global.isVoiceCaptureAvailable = isVoiceCaptureAvailable;
  global.deriveRtUrl = deriveRtUrl;
  global.RT_EVENTS = RT_EVENTS;
  global.ERROR_MESSAGES = ERROR_MESSAGES;
  global.UPLINK_AUDIO_SPEC = UPLINK_AUDIO_SPEC;
  global.TTS_SAMPLE_RATE = TTS_SAMPLE_RATE;
})(typeof window !== 'undefined' ? window : this);
