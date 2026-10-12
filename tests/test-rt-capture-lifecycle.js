'use strict';
const assert = require('node:assert/strict');
const captureModule = require('../pkg/web/static/tutor/js/rt-capture.js');

function deferred() {
  let resolve, reject;
  const promise = new Promise((a, b) => { resolve = a; reject = b; });
  return { promise, resolve, reject };
}
const flush = () => new Promise(resolve => setImmediate(resolve));

function fixture() {
  const contexts = [], requests = [];
  class Node {
    constructor(ctx, kind) { this.context = ctx; this.kind = kind; this.outputs = []; ctx.nodes.push(this); }
    connect(target) {
      if (target.context !== this.context) throw new Error('cross-context connection');
      if (this.kind === 'gain' && this.context.failGainOnce) {
        this.context.failGainOnce = false;
        throw new Error('simulated partial worklet setup failure');
      }
      this.outputs.push(target);
    }
    disconnect() { this.outputs = []; }
    start() {}
  }
  captureModule.AudioContext = class {
    constructor() {
      this.nodes = []; this.sampleRate = 48000;
      this.module = deferred();
      this.audioWorklet = { addModule: () => this.module.promise };
      this.destination = new Node(this, 'destination');
      contexts.push(this);
    }
    resume() { return Promise.resolve(); }
    createBuffer() { return { getChannelData: () => new Float32Array(1) }; }
    createBufferSource() { return new Node(this, 'buffer'); }
    createMediaStreamSource() { return new Node(this, 'source'); }
    createGain() { const n = new Node(this, 'gain'); n.gain = {}; return n; }
    createScriptProcessor() { return new Node(this, 'script'); }
  };
  captureModule.AudioWorkletNode = class extends Node {
    constructor(ctx) { super(ctx, 'worklet'); this.port = {}; }
  };
  captureModule.navigator = { mediaDevices: { getUserMedia() {
    const request = deferred(); requests.push(request); return request.promise;
  } } };
  function stream() {
    const track = { stopped: false, stop() { this.stopped = true; } };
    return { track, getTracks: () => [track] };
  }
  const capture = new captureModule.RTCapture();
  let bytes = 0;
  capture.onUplink(pcm => { bytes += pcm.byteLength; });
  return { capture, contexts, requests, stream, bytes: () => bytes,
    liveProcessors: () => contexts.flatMap(ctx => ctx.nodes).filter(n =>
      (n.kind === 'worklet' || n.kind === 'script') && n.outputs.length &&
      n.context.nodes.some(source => source.kind === 'source' && source.outputs.includes(n))) };
}

async function restartDuringWorkletLoad() {
  const f = fixture();
  const firstStream = f.stream(), secondStream = f.stream();
  const first = f.capture.start();
  f.requests[0].resolve(firstStream);
  await flush(); // first getUserMedia done, addModule still pending
  f.capture.stop(); // lesson closes before microphone startup has completed
  const second = f.capture.start(); // "practice another set"
  f.requests[1].resolve(secondStream);
  await flush();
  f.contexts[0].module.resolve(); // stale worklet completion comes back first
  await flush();
  f.contexts[1].module.resolve();
  await Promise.all([first, second]);
  const live = f.liveProcessors();
  assert.equal(live.length, 1, 'restart must have ONE capture processor; got ' + live.map(n => n.kind).join(', '));
  assert.equal(await first, false, 'cancelled startup reports inactive');
  assert.equal(await second, true);
  assert.equal(firstStream.track.stopped, true);
  assert.equal(secondStream.track.stopped, false);
  assert.equal(live[0].kind, 'worklet');
}

async function stopDuringPermission() {
  const f = fixture();
  const pending = f.capture.start();
  f.capture.stop();
  const late = f.stream();
  f.requests[0].resolve(late);
  f.contexts[0].module.resolve();
  assert.equal(await pending, false);
  assert.equal(late.track.stopped, true, 'late granted microphone must be released');
  assert.equal(f.capture.isCapturing, false);
  assert.equal(f.liveProcessors().length, 0);
}

async function concurrentStartsAndLateFrames() {
  const f = fixture();
  const first = f.capture.start(), duplicate = f.capture.start();
  assert.equal(f.requests.length, 1, 'pending start must be shared');
  const stream = f.stream();
  f.requests[0].resolve(stream); f.contexts[0].module.resolve();
  await Promise.all([first, duplicate]);
  const callback = f.capture._workletNode.port.onmessage;
  callback({ data: new Float32Array(1024).fill(0.1) });
  const beforeStop = f.bytes();
  assert.ok(beforeStop > 0);
  f.capture.stop();
  callback({ data: new Float32Array(1024).fill(0.1) });
  assert.equal(f.bytes(), beforeStop, 'queued frames after stop must be ignored');
  assert.equal(stream.track.stopped, true);
  const restarted = f.capture.start();
  f.requests[1].resolve(f.stream()); f.contexts[1].module.resolve();
  await restarted;
  callback({ data: new Float32Array(1024).fill(0.1) });
  assert.equal(f.bytes(), beforeStop, 'old callback cannot enter a live new session');
  f.capture._workletNode.port.onmessage({ data: new Float32Array(1024).fill(0.1) });
  assert.ok(f.bytes() > beforeStop, 'new session still captures');
}

async function failedOldStartCannotDamageNewStart() {
  const f = fixture();
  let errors = 0;
  f.capture.onError(() => { errors++; });
  const first = f.capture.start();
  f.requests[0].resolve(f.stream());
  await flush();
  f.capture.stop();
  const second = f.capture.start();
  const active = f.stream();
  f.requests[1].resolve(active); f.contexts[1].module.resolve();
  await second;
  f.contexts[0].module.reject(new Error('old module failed'));
  assert.equal(await first, false);
  assert.equal(errors, 0);
  assert.equal(active.track.stopped, false);
  assert.equal(f.liveProcessors().length, 1);
}

async function partialWorkletFailureHasOneFallback() {
  const f = fixture();
  const started = f.capture.start();
  f.contexts[0].failGainOnce = true;
  f.requests[0].resolve(f.stream()); f.contexts[0].module.resolve();
  await started;
  const live = f.liveProcessors();
  assert.equal(live.length, 1);
  assert.equal(live[0].kind, 'script');
  const callback = live[0].onaudioprocess;
  callback({ inputBuffer: { sampleRate: 48000, getChannelData: () => new Float32Array(4096).fill(0.1) } });
  assert.ok(f.bytes() > 0);
  const beforeStop = f.bytes();
  f.capture.stop();
  callback({ inputBuffer: { getChannelData: () => new Float32Array(4096).fill(0.1) } });
  assert.equal(f.bytes(), beforeStop);
}

(async () => {
  await restartDuringWorkletLoad();
  await stopDuringPermission();
  await concurrentStartsAndLateFrames();
  await failedOldStartCannotDamageNewStart();
  await partialWorkletFailureHasOneFallback();
  console.log('rt capture lifecycle: all assertions passed');
})().catch(err => { console.error(err); process.exitCode = 1; });
