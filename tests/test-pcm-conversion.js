// tests/test-pcm-conversion.js — pure-JS verification of the PCM Int16→Float32
// conversion used in test-interface.html's word_complete scheduling path (D-05).
// Runs on stock Node with no framework and no installs.
'use strict';

const assert = require('node:assert');

// Same math as test-interface.html handleStatusFrame word_complete arm:
//   view.getInt16(i * 2, true) / 32768   (little-endian s16le PCM → Float32)
function pcmInt16ToFloat32(pcmBytes) {
    const numSamples = pcmBytes.length / 2;
    const out = new Float32Array(numSamples);
    const view = new DataView(pcmBytes.buffer, pcmBytes.byteOffset, pcmBytes.byteLength);
    for (let i = 0; i < numSamples; i++) {
        out[i] = view.getInt16(i * 2, true) / 32768;
    }
    return out;
}

// [0xFF, 0x7F] little-endian = 0x7FFF = 32767 → ≈ 32767/32768 (0.99997)
assert(Math.abs(pcmInt16ToFloat32(new Uint8Array([0xFF, 0x7F]))[0] - (32767 / 32768)) < 1e-6);

// [0x00, 0x80] little-endian = 0x8000 = -32768 → -1.0 exactly
assert(pcmInt16ToFloat32(new Uint8Array([0x00, 0x80]))[0] === -1.0);

// [0x00, 0x00] little-endian = 0 → 0.0
assert(Math.abs(pcmInt16ToFloat32(new Uint8Array([0x00, 0x00]))[0] - 0.0) < 1e-6);

// Multi-sample sanity: [32767, -32768] → [≈0.99997, -1.0]
const two = pcmInt16ToFloat32(new Uint8Array([0xFF, 0x7F, 0x00, 0x80]));
assert(two.length === 2);
assert(Math.abs(two[0] - 32767 / 32768) < 1e-6);
assert(two[1] === -1.0);

console.log('test-pcm-conversion: PASS');
process.exit(0);
