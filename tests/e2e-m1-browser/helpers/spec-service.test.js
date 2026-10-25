// spec-service.test.js — Node tests for the m1 line's per-test service
// helper (e2e-test-plan §7 steps 11–13). The Go boot path is
// service-launcher.test.js's; what needs pinning here is the STUB CONTROL
// surface every spec leans on: install replaces scripts (not appends),
// state reflects exactly what was installed/recorded, and reset returns the
// shared stub to a clean slate (smoke.spec.js's zero-requests assertion
// depends on specs resetting). Driven against an in-process strict stub so
// no ports are borrowed from the Playwright run.
'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { makeStubControl } = require('./spec-service');
const { createStrictMimoStub } = require('./strict-mimo-stub');

test('stub control: install replaces, state reflects, reset clears', async () => {
  const stub = createStrictMimoStub();
  const { port } = await stub.listen(0);
  const ctl = makeStubControl('http://127.0.0.1:' + port);
  try {
    // Install round one and read it back through the control plane.
    const first = await ctl.scripts({ asr: [{ content: '语文' }], llm: [], image: [], tts: [{ ms: 1200 }] });
    assert.strictEqual(first.ok, true);
    assert.deepStrictEqual(first.remaining, { asr: 1, llm: 0, image: 0, tts: 1 });

    // A second install REPLACES (never appends): the stale asr entry is gone.
    const second = await ctl.scripts({ asr: [], llm: [], image: [], tts: [{ ms: 600 }, { ms: 600 }] });
    assert.deepStrictEqual(second.remaining, { asr: 0, llm: 0, image: 0, tts: 2 });

    // State carries the installed scripts and starts clean of violations.
    const mid = await ctl.state();
    assert.deepStrictEqual(mid.violations, []);
    assert.deepStrictEqual(mid.remaining.tts, 2);

    // Reset clears scripts, requests and violations in one call — the
    // guarantee the next spec (and smoke's empty-requests assertion) needs.
    const reset = await ctl.reset();
    assert.strictEqual(reset.ok, true);
    const after = await ctl.state();
    assert.deepStrictEqual(after, {
      violations: [], requests: [], remaining: { asr: 0, llm: 0, image: 0, tts: 0 },
    });
  } finally {
    await stub.close();
  }
});
