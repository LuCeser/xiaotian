// tests/test-rt-photo.js — headless verification of the /tutor/ RTPhoto
// controller's pure parts (13.1-03): the four photo-controller constants
// ported verbatim, the quality-ladder pick, 32KiB chunk slicing (final
// partial chunk kept, no empty tail on exact multiples), base64 framing,
// the photo_chunk JSON ship path (final=true on the last chunk), the
// client-side upload cap, and the cancel-after-ship rule. Runs on stock
// Node with no framework and no installs: `node tests/test-rt-photo.js`.
'use strict';

const assert = require('node:assert');

// The controller IIFE assigns onto its `global` arg — in Node's CommonJS
// that is the module's top-level `this` (module.exports), so the class
// arrives as a named export (no DOM needed for the ship-path methods).
const { RTPhoto } = require('../pkg/web/static/tutor/js/rt-photo.js');

// ---------------------------------------------------------------------
// 1. The four ported constants match photo-controller.js verbatim
// ---------------------------------------------------------------------
{
  assert.strictEqual(RTPhoto.MAX_EDGE, 1600, 'MAX_EDGE ported verbatim');
  assert.strictEqual(RTPhoto.CHUNK_BYTES, 32768, 'CHUNK_BYTES ported verbatim');
  assert.strictEqual(RTPhoto.MAX_UPLOAD_BYTES, 786432, 'MAX_UPLOAD_BYTES ported verbatim');
  assert.deepStrictEqual(
    RTPhoto.QUALITY_LADDER,
    [
      { quality: 0.8, edge: 1600 },
      { quality: 0.6, edge: 1600 },
      { quality: 0.6, edge: 1280 },
      { quality: 0.5, edge: 1024 }
    ],
    'QUALITY_LADDER ported verbatim'
  );
}

// ---------------------------------------------------------------------
// 2. pickLadderStep: first fit ≤ 768KiB wins; all overflow → null
// ---------------------------------------------------------------------
{
  assert.strictEqual(RTPhoto.pickLadderStep([500000, 900000, 800000, 700000]).index, 0,
    'the first step fits');
  assert.strictEqual(RTPhoto.pickLadderStep([800000, 900000, 700000, 600000]).index, 2,
    'an early overflow falls through to the smaller edge');
  assert.strictEqual(RTPhoto.pickLadderStep([900000, 900000, 800000, 900000]), null,
    'every step overflowing reports too-large');
  assert.strictEqual(RTPhoto.pickLadderStep([786432]).index, 0,
    'exactly MAX_UPLOAD_BYTES fits (≤)');
  assert.strictEqual(RTPhoto.pickLadderStep([]), null, 'no probe results → null');
}

// ---------------------------------------------------------------------
// 3. sliceChunks: 100000 bytes → 4 chunks (3×32KiB + remainder); reassembles
// ---------------------------------------------------------------------
{
  const jpeg = Buffer.alloc(100000);
  for (let i = 0; i < jpeg.length; i++) jpeg[i] = i & 0xFF;
  const chunks = RTPhoto.sliceChunks(new Uint8Array(jpeg));
  assert.strictEqual(chunks.length, 4, '100000 bytes → 4 chunks');
  assert.strictEqual(chunks[0].length, 32768, 'full chunks are 32KiB');
  assert.strictEqual(chunks[3].length, 100000 - 3 * 32768, 'final chunk carries the remainder');
  const reassembled = Buffer.concat(chunks.map(c => Buffer.from(c)));
  assert.ok(reassembled.equals(jpeg), 'chunks reassemble to the original bytes in order');

  // An exact multiple yields no empty tail chunk.
  assert.strictEqual(RTPhoto.sliceChunks(new Uint8Array(65536)).length, 2,
    '65536 bytes → exactly 2 chunks');
  assert.strictEqual(RTPhoto.sliceChunks(new Uint8Array(1)).length, 1,
    'one byte still ships as one chunk');
}

// ---------------------------------------------------------------------
// 4. bytesToBase64 framing matches the standard alphabet (incl. 32KiB)
// ---------------------------------------------------------------------
{
  assert.strictEqual(
    RTPhoto.bytesToBase64(new Uint8Array([1, 2, 3])),
    Buffer.from([1, 2, 3]).toString('base64'),
    'small payload framing'
  );
  const chunk = Buffer.alloc(32768);
  for (let i = 0; i < chunk.length; i++) chunk[i] = (i * 7) & 0xFF;
  assert.strictEqual(
    RTPhoto.bytesToBase64(new Uint8Array(chunk)),
    chunk.toString('base64'),
    'full 32KiB chunk framing'
  );
}

// ---------------------------------------------------------------------
// 5. shipJPEG: one photo_chunk JSON message per chunk via sendJSON,
//    sequences from 1, final=true on the last, payloads reassemble
// ---------------------------------------------------------------------
{
  const jpeg = Buffer.alloc(70000);
  for (let i = 0; i < jpeg.length; i++) jpeg[i] = (i * 3) & 0xFF;
  const sent = [];
  const c = new RTPhoto({ sendJSON: m => sent.push(m) });
  c.supported = true;

  assert.strictEqual(c.shipJPEG(new Uint8Array(jpeg)), true, 'upload shipped');
  assert.strictEqual(sent.length, 3, '70000 bytes → 3 photo_chunk messages');
  sent.forEach((m, i) => {
    assert.strictEqual(m.type, 'photo_chunk', 'control type is photo_chunk (JSON, no binary frames)');
    assert.strictEqual(m.seq, i + 1, 'sequences start at 1 and increase');
    assert.strictEqual(m.final, i === sent.length - 1, 'final=true only on the last chunk');
    const end = Math.min((i + 1) * 32768, jpeg.length);
    assert.ok(Buffer.from(m.data, 'base64').equals(jpeg.subarray(i * 32768, end)),
      'chunk payload reassembles positionally');
  });
  assert.strictEqual(c.isProcessing, true, 'processing lock set');
}

// ---------------------------------------------------------------------
// 6. Upload cap: oversize image refuses without shipping
// ---------------------------------------------------------------------
{
  const sent = [];
  const errors = [];
  const c = new RTPhoto({ sendJSON: m => sent.push(m) });
  c.onError(t => errors.push(t));
  assert.strictEqual(c.shipJPEG(new Uint8Array(786433)), false, '786433 bytes refused');
  assert.strictEqual(sent.length, 0, 'no messages shipped');
  assert.strictEqual(c.isProcessing, false, 'processing lock NOT set');
  assert.deepStrictEqual(errors, ['too-large'], "'too-large' reported");
}

// ---------------------------------------------------------------------
// 7. cancel: photo_cancel only after chunks shipped (the _shipped>0 rule);
//    the next upload's sequence restarts at 1
// ---------------------------------------------------------------------
{
  const sent = [];
  const c = new RTPhoto({ sendJSON: m => sent.push(m) });
  c.cancel();
  assert.strictEqual(sent.length, 0, 'cancel with nothing shipped is silent');

  c.shipJPEG(new Uint8Array(4096));
  c.cancel();
  assert.deepStrictEqual(sent[1], { type: 'photo_cancel' }, 'photo_cancel after chunks shipped');
  assert.strictEqual(c.isProcessing, false, 'cancel clears the processing lock');

  c.shipJPEG(new Uint8Array(4096));
  assert.strictEqual(sent[2].seq, 1, 'sequence restarts at 1 after cancel');
}

// ---------------------------------------------------------------------
// 8. reset clears the processing lock (the page calls it on ocr_result)
// ---------------------------------------------------------------------
{
  const c = new RTPhoto({ sendJSON: () => {} });
  c.shipJPEG(new Uint8Array(4096));
  assert.strictEqual(c.isProcessing, true, 'locked while awaiting recognition');
  c.reset();
  assert.strictEqual(c.isProcessing, false, 'unlocked by reset');
}

console.log('test-rt-photo.js: all assertions passed');
