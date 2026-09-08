// tests/test-batch-form.js — headless verification of the batch-submission
// client contract (Ticket 05): the index→word answers mapping is pure and
// pinned here (trim, skip empties, all-empty → null = client-side validation
// before the wire). `node tests/test-batch-form.js`.
'use strict';

const assert = require('node:assert');

// The IIFE assigns onto its `global` arg — in Node's CommonJS that is the
// module's top-level `this` (module.exports).
const { BatchSubmissionForm } = require('../pkg/web/static/js/ui-components.js');

const words = ['apple', 'banana', 'cherry'];

// 1. Full mapping in queue order.
{
  const answers = BatchSubmissionForm.buildAnswers(words, ['apple', 'banane', 'cherry']);
  assert.deepStrictEqual(answers, { apple: 'apple', banana: 'banane', cherry: 'cherry' });
}

// 2. Values are trimmed.
{
  const answers = BatchSubmissionForm.buildAnswers(words, ['  apple  ', '', '\tcherry\n']);
  assert.deepStrictEqual(answers, { apple: 'apple', cherry: 'cherry' });
}

// 3. Empty rows are omitted (a blank grades incorrect server-side) — partial
//    submissions are allowed.
{
  const answers = BatchSubmissionForm.buildAnswers(words, ['', 'banana', '']);
  assert.deepStrictEqual(answers, { banana: 'banana' });
}

// 4. All-empty (or whitespace-only) → null: never send an empty answers map
//    (the server rejects it; the client validates first).
{
  assert.strictEqual(BatchSubmissionForm.buildAnswers(words, ['', '  ', '']), null);
  assert.strictEqual(BatchSubmissionForm.buildAnswers(words, []), null);
  assert.strictEqual(BatchSubmissionForm.buildAnswers([], ['x']), null);
}

console.log('test-batch-form.js: all assertions passed');
