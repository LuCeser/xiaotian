// tests/test-photo-controller.js — headless verification of the
// PhotoAnswerController ship path (Ticket 04): JPEG chunking into 32KiB
// IMAGE frames (msgType 7), per-photo sequence numbering from 1, the
// photo_submit trigger ordering, the client-side upload cap, and the cancel
// path. Runs on stock Node with no framework and no installs:
// `node tests/test-photo-controller.js`.
'use strict';

const assert = require('node:assert');

// The controller IIFE assigns onto its `global` arg — in Node's CommonJS
// that is the module's top-level `this` (module.exports), so the class
// arrives as a named export (no DOM needed for the ship-path methods).
const { PhotoAnswerController } = require('../pkg/web/static/js/photo-controller.js');

/** Recording wsClient double: captures control sends and binary frames. */
function makeFakeWs() {
  return {
    sends: [],
    frames: [],
    send(payload) { this.sends.push(payload); },
    sendFrame(msgType, seq, bytes) {
      this.frames.push({ msgType, seq, bytes: Buffer.from(bytes) });
      return true;
    }
  };
}

/** Build a controller wired to fakes. `supported` is forced on — Node has
 *  no DOM, but _ship never touches it. */
function makeController() {
  const c = new PhotoAnswerController(makeFakeWs());
  c.supported = true;
  return c;
}

// ---------------------------------------------------------------------
// 1. _ship: 100000 bytes → 4 chunks (3×32768 + 1696), ordered, reassembling
// ---------------------------------------------------------------------
{
  const c = makeController();
  const jpeg = Buffer.alloc(100000);
  for (let i = 0; i < jpeg.length; i++) jpeg[i] = i & 0xFF;
  c._ship(new Uint8Array(jpeg));

  const frames = c.wsClient.frames;
  assert.strictEqual(frames.length, 4, '100000 bytes → 4 frames');
  assert.strictEqual(frames[0].bytes.length, 32768, 'full chunks are 32KiB');
  assert.strictEqual(frames[3].bytes.length, 100000 - 3 * 32768, 'final chunk carries the remainder');
  for (const f of frames) {
    assert.strictEqual(f.msgType, 7, 'msg type IMAGE (0x07)');
  }
  assert.deepStrictEqual(frames.map(f => f.seq), [1, 2, 3, 4], 'sequences start at 1 and increase');

  const reassembled = Buffer.concat(frames.map(f => f.bytes));
  assert.ok(reassembled.equals(jpeg), 'chunks reassemble to the original bytes in order');

  // The trigger follows the frames, exactly once.
  assert.strictEqual(c.wsClient.sends.length, 1, 'one control send');
  assert.deepStrictEqual(c.wsClient.sends[0], { type: 'photo_submit', params: {} });
  assert.strictEqual(c.isProcessing, true, 'processing lock set');
}

// ---------------------------------------------------------------------
// 2. Sequence restarts at 1 for the next photo (server-side dedup contract)
// ---------------------------------------------------------------------
{
  const c = makeController();
  c._ship(new Uint8Array(4096));
  c.resultReceived(); // photo_result arrived → button unlocked
  c._ship(new Uint8Array(8192));
  const frames = c.wsClient.frames;
  assert.strictEqual(frames.length, 2, 'each small photo is one frame');
  assert.deepStrictEqual(frames.map(f => f.seq), [1, 1], 'sequence restarts at 1 per photo');
  assert.strictEqual(c.wsClient.sends.length, 2, 'one trigger per photo');
}

// ---------------------------------------------------------------------
// 3. Upload cap: oversize image refuses to ship and reports 'too-large'
// ---------------------------------------------------------------------
{
  const c = makeController();
  const errors = [];
  c.onError(t => errors.push(t));
  c._ship(new Uint8Array(786433)); // 768KiB + 1

  assert.strictEqual(c.wsClient.frames.length, 0, 'no frames shipped');
  assert.strictEqual(c.wsClient.sends.length, 0, 'no trigger sent');
  assert.strictEqual(c.isProcessing, false, 'processing lock NOT set');
  assert.deepStrictEqual(errors, ['too-large'], "'too-large' reported");
}

// ---------------------------------------------------------------------
// 4. cancel: notifies the server only when chunks actually shipped
// ---------------------------------------------------------------------
{
  const c = makeController();
  c.cancel();
  assert.strictEqual(c.wsClient.sends.length, 0, 'cancel with nothing shipped is silent');

  c._ship(new Uint8Array(4096));
  c.cancel();
  assert.deepStrictEqual(c.wsClient.sends[1], { type: 'photo_cancel', params: {} }, 'photo_cancel after chunks shipped');
  assert.strictEqual(c.isProcessing, false, 'cancel clears the processing lock');
}

// ---------------------------------------------------------------------
// 5. resultReceived clears the processing lock
// ---------------------------------------------------------------------
{
  const c = makeController();
  c._ship(new Uint8Array(4096));
  assert.strictEqual(c.isProcessing, true, 'locked while awaiting photo_result');
  c.resultReceived();
  assert.strictEqual(c.isProcessing, false, 'unlocked by resultReceived');
}

console.log('test-photo-controller.js: all assertions passed');
