// tests/test-rt-processing-fallback.js — Issue #94 U5: the browser-side
// 「思考中」 second watch's PURE decision function and the thinking_timeout
// wording. The server bounds the thinking phase at rt.processingTimeout
// (20s) and answers the thinking_timeout ErrorMsg; when that message itself
// is lost, the page's local 25s fallback (server 20s + 5s slack) is the
// only visible recovery. What is locked here:
//   1. shouldTriggerProcessingFallback fires at/after the threshold and
//      ONLY then — never before, and never without a processing entry
//      (listening/speaking clear the entry, so those states cannot fire).
//   2. The threshold is 25s (server 20s + 5s slack).
//   3. ERROR_MESSAGES carries thinking_timeout with the server's exact
//      wording (pkg/rt/turn.go thinkingTimeoutNotice).
// Runs on stock Node: `node tests/test-rt-processing-fallback.js`.
'use strict';

const assert = require('node:assert');

// The IIFE assigns onto its `global` arg — in Node's CommonJS that is the
// module's top-level this (module.exports). Same pattern as
// test-rt-client-lifecycle.js.
const {
  ERROR_MESSAGES,
  humanizeError,
  shouldTriggerProcessingFallback,
  PROCESSING_FALLBACK_THRESHOLD_MS
} = require('../pkg/web/static/tutor/js/rt-client.js');

// ---------------------------------------------------------------------------
// 1. The trigger decision: threshold arithmetic only, no DOM, no timers
// ---------------------------------------------------------------------------
{
  const enteredAt = 10_000;

  // Before the threshold: still inside the server's own window — the local
  // fallback must stay silent and let the server's message land.
  assert.strictEqual(
    shouldTriggerProcessingFallback(enteredAt, enteredAt + 24_999, 25_000),
    false,
    '24.999s into processing must not trigger (threshold not reached)'
  );
  assert.strictEqual(
    shouldTriggerProcessingFallback(enteredAt, enteredAt + 25_000, 25_000),
    true,
    'exactly 25s into processing must trigger (inclusive threshold)'
  );
  assert.strictEqual(
    shouldTriggerProcessingFallback(enteredAt, enteredAt + 120_000, 25_000),
    true,
    'long past the threshold must trigger (the server message was lost)'
  );

  // No processing entry (never entered, or left to listening/speaking):
  // nothing to recover — the chip is not stuck on 思考中.
  assert.strictEqual(
    shouldTriggerProcessingFallback(null, Date.now(), 25_000),
    false,
    'null entry (listening/speaking) must never trigger'
  );
  assert.strictEqual(
    shouldTriggerProcessingFallback(undefined, Date.now(), 25_000),
    false,
    'undefined entry must never trigger'
  );

  // The threshold defaults to the exported 25s constant.
  assert.strictEqual(
    shouldTriggerProcessingFallback(enteredAt, enteredAt + PROCESSING_FALLBACK_THRESHOLD_MS),
    true,
    'omitted threshold defaults to PROCESSING_FALLBACK_THRESHOLD_MS'
  );
  assert.strictEqual(
    shouldTriggerProcessingFallback(enteredAt, enteredAt + PROCESSING_FALLBACK_THRESHOLD_MS - 1),
    false,
    'one ms short of the default threshold must not trigger'
  );

  // A custom threshold (the unit-test surface for future tuning).
  assert.strictEqual(
    shouldTriggerProcessingFallback(0, 1_000, 1_000),
    true,
    'custom threshold is honored'
  );
}

// ---------------------------------------------------------------------------
// 2. The 25s constant: server 20s + 5s slack
// ---------------------------------------------------------------------------
{
  assert.strictEqual(PROCESSING_FALLBACK_THRESHOLD_MS, 25_000,
    'the local fallback threshold is 25s (server 20s + 5s slack)');
}

// ---------------------------------------------------------------------------
// 3. The wire wording: identical to the server's fixed notice
// ---------------------------------------------------------------------------
{
  const want = '老师想得太久了，请再对我说一遍';
  assert.strictEqual(ERROR_MESSAGES.thinking_timeout, want,
    'ERROR_MESSAGES.thinking_timeout carries the server wording');
  assert.strictEqual(humanizeError('thinking_timeout'), want,
    'humanizeError maps the wire code for the page-local fallback too');
}

console.log('test-rt-processing-fallback: all assertions passed');
