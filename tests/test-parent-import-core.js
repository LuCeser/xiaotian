/**
 * Issue #80 U3 — pure helpers for per-photo recognition identity.
 * `node tests/test-parent-import-core.js`
 */
'use strict';

const path = require('node:path');

global.window = global;
require(path.resolve(__dirname, '..', 'pkg', 'web', 'static', 'js', 'parent-import-core.js'));

const core = global;

let failures = 0;
let checks = 0;

function assert(cond, label) {
  checks++;
  if (!cond) {
    failures++;
    console.error('FAIL: ' + label);
  }
}

function eq(actual, expected, label) {
  assert(actual === expected, label + ' — got ' + JSON.stringify(actual) +
    ', want ' + JSON.stringify(expected));
}

(function testReplaceNotAppend() {
  var state = core.createImportPhotoState();
  core.setPhotoRecognition(state, 2, {
    meta: { edition: 'A' },
    entries: [{ word: 'x', meaning: '甲', lesson: '第1课' }],
  });
  core.setPhotoRecognition(state, 2, {
    meta: { edition: 'B' },
    entries: [{ word: 'y', meaning: '乙', lesson: '第9课' }],
  });
  var rec = core.getPhotoRecognition(state, 2);
  eq(rec.entries[0].word, 'y', 'retry replaces entries');
  eq(rec.meta.edition, 'B', 'retry replaces meta');
  eq(core.photoMetaListFromState(state).length, 2, 'meta list spans page index');
})();

(function testOriginalAfterEdit() {
  var state = core.createImportPhotoState();
  core.setPhotoRecognition(state, 1, {
    meta: { grade: '三' },
    entries: [{ word: 'cat', meaning: '猫', lesson: '第2课' }],
  });
  var row = { word: 'cat', meaning: '改义', lesson: '家长课', page: 1 };
  var orig = core.originalEntryForRow(state, row);
  eq(orig.lesson, '第2课', 'original lesson survives row edits');
  eq(orig.meaning, '猫', 'original meaning from OCR');
})();

(function testStoredEntriesCarryOrdinal() {
  var state = core.createImportPhotoState();
  core.setPhotoRecognition(state, 1, {
    meta: { grade: '三' },
    entries: [
      { word: 'bat', meaning: '甲', lesson: '第1课' },
      { word: 'bat', meaning: '乙', lesson: '第2课' },
    ],
  });
  var rec = core.getPhotoRecognition(state, 1);
  eq(rec.entries[0].entryIndex, 0, 'stored entry 0 carries its ordinal');
  eq(rec.entries[1].entryIndex, 1, 'stored entry 1 carries its ordinal');
  eq(core.recognitionEntriesForPage(state, 1)[1].entryIndex, 1,
    'entries export keeps the ordinal');
})();

(function testOrdinalLinksOwnEntryAfterWordEdit() {
  var state = core.createImportPhotoState();
  core.setPhotoRecognition(state, 1, {
    meta: { grade: '三' },
    entries: [
      { word: 'bat', meaning: '甲', lesson: '第1课' },
      { word: 'bat', meaning: '乙', lesson: '第2课' },
    ],
  });
  // The parent corrected both word faces — the current word matches no
  // stored entry; the ordinal must still find each row's OWN entry.
  var row0 = { word: 'cat', meaning: '甲', lesson: '第1课', page: 1, entryIndex: 0 };
  var row1 = { word: 'hat', meaning: '乙', lesson: '第2课', page: 1, entryIndex: 1 };
  eq(core.originalEntryForRow(state, row0).word, 'bat',
    'corrected word still links to its original OCR entry');
  eq(core.originalEntryForRow(state, row1).lesson, '第2课',
    'same-page duplicate word links to its OWN entry, not the first match');
})();

(function testUnstampedRowFallsBackToWordMatch() {
  var state = core.createImportPhotoState();
  core.setPhotoRecognition(state, 1, {
    meta: { grade: '三' },
    entries: [{ word: 'cat', meaning: '猫', lesson: '第2课' }],
  });
  var row = { word: 'cat', meaning: '猫', lesson: '', page: 1 };
  eq(core.originalEntryForRow(state, row).lesson, '第2课',
    'rows without the ordinal keep the word-match fallback');
  var gone = { word: 'cat', meaning: '猫', lesson: '', page: 1, entryIndex: 9 };
  assert(core.originalEntryForRow(state, gone) === null,
    'a stale out-of-range ordinal returns null, not a wrong entry');
})();

if (failures) {
  console.error('test-parent-import-core: ' + failures + ' of ' + checks + ' checks failed');
  process.exit(1);
}
console.log('test-parent-import-core: all ' + checks + ' checks passed');
