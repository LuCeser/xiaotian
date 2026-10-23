// fake-mic.js — CONTROLLABLE MediaStream mic source for the m1 browser line
// (issue #58, e2e-test-plan §7 step 7; plan §4's 真实浏览器 injection row:
// "Playwright 初始化脚本只替换 getUserMedia 的音频来源"). The ONLY thing
// replaced is navigator.mediaDevices.getUserMedia: it returns a synthetic
// MediaStream built from a private AudioContext + MediaStreamAudioDestination
// node. Everything downstream stays 100% production — the /tutor/ page's
// RTCapture still runs createMediaStreamSource → AudioWorklet (or
// ScriptProcessor) → resampleTo16k → s16le uplink, and the page never knows
// the source was synthetic. The tool NEVER touches the classroom, the WS or
// the player (plan §2 rules 1–2).
//
// Control surface (test-only, on window.__rtFakeMic):
//   info() → { pcmPlays, contextState, tracks: [{kind, state, label}] }
//   playPcm16(b64, rate, amplitude) → Promise<{ms}> — plays one s16le mono
//       corpus clip into the stream (the R-layer corpus entry point; the
//       AudioBufferSourceNode resamples rate → context rate automatically).
// The stream rebuilds on the next getUserMedia call after production stopped
// its tracks (a restart must never capture an ended track's silence).
//
// Cross-talk isolation by construction: the synthetic stream's only input is
// whatever this tool plays; teacher playback rides the PRODUCTION context's
// destination and can never enter this graph. A looping zero buffer keeps
// real (silent) frames flowing before any clip plays, and a zero-gain tap on
// this context's destination keeps it rendering without audible output (the
// same mute pattern production capture uses).
'use strict';

const FAKE_MIC_INIT = String.raw`
(function () {
  'use strict';
  if (window.__rtFakeMic) return;
  var state = { ctx: null, dest: null, tracks: [], pcmPlays: 0 };

  function rebuild() {
    if (!state.ctx) state.ctx = new AudioContext();
    if (state.ctx.state === 'suspended' && state.ctx.resume) state.ctx.resume();
    if (state.dest) { try { state.dest.disconnect(); } catch (e) { /* gone */ } }
    state.dest = state.ctx.createMediaStreamDestination();
    var loop = state.ctx.createBufferSource(); // looping silence → real frames
    loop.buffer = state.ctx.createBuffer(1, state.ctx.sampleRate, state.ctx.sampleRate);
    loop.loop = true;
    loop.connect(state.dest);
    var mute = state.ctx.createGain(); // keeps rendering, plays nothing
    mute.gain.value = 0;
    loop.connect(mute);
    mute.connect(state.ctx.destination);
    loop.start();
    state.tracks = state.dest.stream.getAudioTracks();
  }

  window.__rtFakeMic = {
    info: function () {
      return {
        pcmPlays: state.pcmPlays,
        contextState: state.ctx ? state.ctx.state : 'none',
        tracks: state.tracks.map(function (t) {
          return { kind: t.kind, state: t.readyState, label: t.label };
        })
      };
    },
    // One s16le mono clip (base64) played into the synthetic stream at the
    // given rate; resolves when the clip has actually finished rendering.
    playPcm16: function (b64, rate, amplitude) {
      if (!state.ctx || !state.dest) return Promise.reject('fake mic graph not created yet');
      var bin = atob(b64);
      var n = Math.floor(bin.length / 2);
      if (!n) return Promise.reject('empty pcm clip');
      var amp = (typeof amplitude === 'number' && amplitude > 0 && amplitude <= 1) ? amplitude : 0.6;
      var buf = state.ctx.createBuffer(1, n, rate || 16000);
      var ch = buf.getChannelData(0);
      for (var i = 0; i < n; i++) {
        var s = (bin.charCodeAt(2 * i) | (bin.charCodeAt(2 * i + 1) << 8));
        if (s >= 0x8000) s -= 0x10000;
        ch[i] = (s / 32768) * amp;
      }
      var src = state.ctx.createBufferSource();
      src.buffer = buf;
      src.connect(state.dest);
      state.pcmPlays += 1;
      return new Promise(function (resolve) {
        src.onended = function () { resolve({ ms: Math.round((n / (rate || 16000)) * 1000) }); };
        src.start();
      });
    }
  };

  if (navigator.mediaDevices && navigator.mediaDevices.getUserMedia) {
    navigator.mediaDevices.getUserMedia = function () {
      var live = state.tracks.length > 0 && state.tracks.every(function (t) {
        return t.readyState === 'live';
      });
      if (!live) rebuild();
      return Promise.resolve(state.dest.stream);
    };
  }
})();
`;

/** Plant the synthetic mic source before any page script runs. */
async function installFakeMic(page) {
  await page.addInitScript(FAKE_MIC_INIT);
}

/** Read the control surface (tracks/context/clip counters). */
async function micInfo(page) {
  return page.evaluate(() => window.__rtFakeMic.info());
}

/** Play one s16le clip into the synthetic stream; resolves when rendered. */
async function playPcm16(page, b64, rate, amplitude) {
  return page.evaluate(({ c, r, a }) => window.__rtFakeMic.playPcm16(c, r, a),
    { c: b64, r: rate, a: amplitude });
}

module.exports = { installFakeMic, micInfo, playPcm16 };
