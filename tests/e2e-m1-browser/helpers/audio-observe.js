// audio-observe.js — READ-ONLY WebAudio output observation for the m1
// browser line (PR #67 review P2, J05). Wire-level evidence (a stopNow in
// the player trace, zero old-epoch acks, the new opening playing) cannot
// tell whether the old epoch's audio REALLY stopped sounding: production
// stopNow() could skip sources[i].stop() and the generation bump + ledger
// clear would still block every old ack while the cancelled ask kept
// playing over the new opening. This helper closes that gap the same way
// rt-observe watches the socket: it wraps
// AudioBufferSourceNode.prototype.start/.stop BEFORE any page script and
// DELEGATES every call to the native method, recording per source
//   { id, startAt, stopAt, endedAt }   (performance.now() at each moment;
//   stopAt set only when production itself called .stop())
// plus the source's real 'ended' event. It never schedules, stops, mutes
// or reconnects anything — the natives do all the work, so observation
// cannot alter playback. Node-side, liveAt(sources, t) picks the sources
// that were still sounding at instant t: exactly the set a correct
// stopNow must have stopped by then.
//
// Cross-referencing with the ws log: rt-observe records each entry's t on
// the same performance.now() clock, so e.g. the interrupt_ack's t divides
// sources into old (started ≤ t) and new (started > t).
//
// Node test: audio-observe.test.js.
'use strict';

const AUDIO_OBSERVE_INIT = String.raw`
(function () {
  'use strict';
  if (window.__rtAudioObs) return;
  var nextId = 0;
  var records = [];
  var Proto = window.AudioBufferSourceNode && window.AudioBufferSourceNode.prototype;
  if (!Proto || !Proto.start || !Proto.stop) return;
  var nativeStart = Proto.start;
  var nativeStop = Proto.stop;
  Proto.start = function () {
    var rec = this.__rtAudioObsRecord;
    if (!rec) {
      rec = { id: ++nextId, startAt: null, stopAt: null, endedAt: null };
      this.__rtAudioObsRecord = rec;
      records.push(rec);
      this.addEventListener('ended', function () {
        rec.endedAt = performance.now();
      });
    }
    var out = nativeStart.apply(this, arguments);
    rec.startAt = performance.now();
    return out;
  };
  Proto.stop = function () {
    var rec = this.__rtAudioObsRecord;
    if (rec) rec.stopAt = performance.now();
    return nativeStop.apply(this, arguments);
  };
  window.__rtAudioObs = {
    sources: function () {
      return records.map(function (r) {
        return { id: r.id, startAt: r.startAt, stopAt: r.stopAt, endedAt: r.endedAt };
      });
    },
  };
})();
`;

/** Plant the audio observer before any page script runs (once per page). */
async function installAudioObserver(page) {
  await page.addInitScript(AUDIO_OBSERVE_INIT);
}

/** Snapshot of every source's lifecycle evidence (copy, page order kept). */
async function audioSources(page) {
  return page.evaluate(() => (window.__rtAudioObs ? window.__rtAudioObs.sources() : []));
}

/**
 * Sources still SOUNDING at instant t: started at/before t and not ended
 * by t. These are exactly the ones a correct stopNow must have stopped —
 * sources that ended naturally before t are done and need no stop.
 */
function liveAt(sources, t) {
  // Array.from first: a vm-realm array (the unit-test sandbox) filtered
  // through its own prototype would stay vm-realm and fail node's strict
  // deepEqual on prototype identity.
  return Array.from(sources || []).filter((s) => s.startAt !== null && s.startAt <= t
    && (s.endedAt === null || s.endedAt > t));
}

module.exports = { installAudioObserver, audioSources, liveAt, AUDIO_OBSERVE_INIT };
