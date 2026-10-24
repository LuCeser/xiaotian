// audio-observe.test.js — the READ-ONLY guarantee of the audio observer
// (PR #67 review P2): every wrapped start/stop must reach the native
// method (observation never alters playback), a production .stop() leaves
// stopAt evidence even though the 'ended' event lands later, and liveAt
// classifies exactly the sources a correct stopNow must have stopped —
// naturally-ended sources drop out, never-started ones never enter.
// Run: node --test tests/e2e-m1-browser/helpers/audio-observe.test.js
'use strict';

const assert = require('node:assert/strict');
const vm = require('node:vm');
const { test } = require('node:test');
const { liveAt, AUDIO_OBSERVE_INIT } = require('./audio-observe.js');

let clock = 0;

// Boot the observer init script in a bare sandbox with a fake
// AudioBufferSourceNode: native start/stop record their own calls, and
// addEventListener stores handlers so the test can fire a real 'ended'.
function bootObserver() {
  class FakeNativeSource {
    constructor() { this.listeners = {}; this.started = 0; this.stopped = 0; }
    start() { this.started += 1; }
    stop() { this.stopped += 1; }
    addEventListener(type, fn) {
      (this.listeners[type] = this.listeners[type] || []).push(fn);
    }
    fireEnded() {
      for (const fn of this.listeners.ended || []) fn();
    }
  }
  const sandbox = {
    performance: { now: () => clock },
    window: { AudioBufferSourceNode: FakeNativeSource },
  };
  vm.createContext(sandbox);
  vm.runInContext(AUDIO_OBSERVE_INIT, sandbox);
  sandbox.__makeSource = () => {
    const s = new sandbox.window.AudioBufferSourceNode();
    sandbox.window.AudioBufferSourceNode; // keep the reference obviously live
    return s;
  };
  return sandbox;
}

test('wrapped start/stop delegate to the natives and record lifecycle evidence', () => {
  clock = 100;
  const sandbox = bootObserver();
  const src = sandbox.__makeSource();
  clock = 200; src.start(5);            // production schedules the source
  clock = 210; src.fireEnded();         // its real 'ended' lands
  let rec = sandbox.window.__rtAudioObs.sources()[0];
  assert.equal(src.started, 1, 'start reached the native');
  assert.equal(src.stopped, 0, 'no native stop happened');
  // (vm-realm objects fail strict deepEqual on prototype identity — compare
  // the four lifecycle fields as primitives instead.)
  assert.equal(rec.id, 1);
  assert.equal(rec.startAt, 200);
  assert.equal(rec.stopAt, null);
  assert.equal(rec.endedAt, 210);

  clock = 300;
  src.stop();                           // production cancels a sounding source
  rec = sandbox.window.__rtAudioObs.sources()[0];
  assert.equal(src.stopped, 1, 'stop reached the native');
  assert.equal(rec.stopAt, 300, 'the production stop left evidence');
});

test('a stop before the ended event is exactly what a correct stopNow looks like', () => {
  clock = 0;
  const sandbox = bootObserver();
  const sounding = sandbox.__makeSource();
  const finished = sandbox.__makeSource();
  const queued = sandbox.__makeSource();
  clock = 10; sounding.start(0);
  clock = 15; finished.start(0);
  clock = 20; finished.fireEnded();     // done before the switch moment
  clock = 25; queued.start(30);         // scheduled, not yet sounding its audio
  const switchT = 50;
  clock = 60; sounding.stop();          // stopNow stops both in-flight ones…
  clock = 61; queued.stop();
  clock = 62; sounding.fireEnded();     // …their ended events land after
  clock = 63; queued.fireEnded();

  const sources = sandbox.window.__rtAudioObs.sources();
  // liveAt at the switch: the sounding + the queued one, NOT the finished
  // one — and both live ones carry stop evidence.
  const live = liveAt(sources, switchT);
  assert.deepEqual(live.map((s) => s.id).sort((a, b) => a - b), [1, 3]);
  for (const s of live) assert.ok(s.stopAt !== null, 'live source #' + s.id + ' was stopped');
});

test('never-started sources never enter the records; native errors propagate', () => {
  const sandbox = bootObserver();
  const src = sandbox.__makeSource();
  src.stop(); // native throws InvalidStateError — delegation must not swallow
  assert.equal(src.stopped, 1, 'even the throwing stop reached the native');
  assert.equal(sandbox.window.__rtAudioObs.sources().length, 0,
    'a source that never started leaves no record');
});

test('liveAt ignores records with no start (never really played)', () => {
  const sources = [
    { id: 1, startAt: null, stopAt: 5, endedAt: null }, // start() threw natively
    { id: 2, startAt: 1, stopAt: 4, endedAt: 6 },       // stopped mid-flight
  ];
  assert.deepEqual(liveAt(sources, 3).map((s) => s.id), [2]);
  assert.deepEqual(liveAt(sources, 7), [], 'after its ended event nothing is live');
});
