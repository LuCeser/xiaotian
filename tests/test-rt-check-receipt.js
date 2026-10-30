// Issue #96 U1 — formatCheckReceipt kind and copy.
'use strict';

const assert = require('node:assert');
const { formatCheckReceipt } = require('../pkg/web/static/tutor/js/rt-check-receipt.js');

{
  const r = formatCheckReceipt(3, 3, []);
  assert.strictEqual(r.kind, 'saved');
  assert.strictEqual(r.text, '批改已保存。复习安排已更新，3 个词。');
}

{
  const r = formatCheckReceipt(3, 2, ['香蕉']);
  assert.strictEqual(r.kind, 'partial');
  assert.strictEqual(r.text, '批改已保存。复习安排更新了 2 个，还有 1 个没有更新。');
}

{
  const r = formatCheckReceipt(2, 0, ['苹果', '香蕉']);
  assert.strictEqual(r.kind, 'failed');
  assert.strictEqual(r.text, '批改结果已保留。复习安排没有保存。');
}

{
  const r = formatCheckReceipt(0, 0, []);
  assert.strictEqual(r.kind, 'empty');
  assert.strictEqual(r.text, '这次没有要保存的学习记录。');
}

console.log('test-rt-check-receipt.js: ok');
