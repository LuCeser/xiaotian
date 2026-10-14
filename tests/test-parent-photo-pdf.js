// tests/test-parent-photo-pdf.js — headless verification of the scanned-PDF
// import pure functions (Issue #7): validatePdfFile / createPagePlan /
// applyPageEvent / retryPage / planSummary (the per-page status machine),
// mergePageRows / flattenPageRows (ordered per-page result merge),
// rowsByPage / applyLessonToPage (per-page batch lesson) and chunkBatch
// (commit slicing). The DOM/pdf.js runtime lives in parent-pdf.js and is
// covered by the browser e2e; everything here is pure, per the
// 前端纯函数 convention (window.parseBatch precedent).
// `node tests/test-parent-photo-pdf.js`
'use strict';

const assert = require('node:assert');

// The IIFEs assign onto their `global` arg — in Node's CommonJS that is the
// module's top-level `this` (module.exports).
const pdf = require('../pkg/web/static/js/parent-pdf-core.js');
const photo = require('../pkg/web/static/js/parent-photo.js');

// The two IIFEs each land on their own module.exports here, but share ONE
// window in the real page — applyLessonToPage looks isCommittable up
// lazily on its global (parent-photo.js loads after parent-pdf-core.js).
// Bridge them the way the browser does, then require's shape is honest.
pdf.isCommittable = photo.isCommittable;
const { classifyPhotoEntries } = photo;

// ----------------------------------------------------------------------
// A. validatePdfFile — the pre-load gate (Issue #7 AC-3: 大小超限给出明确
//    反馈, nothing is processed). A PDF is type application/pdf OR a .pdf
//    name (iPad Safari sometimes hands an empty MIME type).
// ----------------------------------------------------------------------

// 1. An application/pdf file within the cap validates.
{
  const v = pdf.validatePdfFile({ name: 'book.pdf', type: 'application/pdf', size: 1024 });
  assert.deepStrictEqual(v, { ok: true });
}

// 2. Empty MIME + .pdf name still validates (iPad Safari shape); the cap
//    boundary itself (exactly 40MiB) is allowed.
{
  const v = pdf.validatePdfFile({
    name: 'Scan 2026.PDF', type: '', size: pdf.MAX_PDF_BYTES,
  });
  assert.deepStrictEqual(v, { ok: true });
  assert.strictEqual(pdf.MAX_PDF_BYTES, 40 * 1024 * 1024);
}

// 3. A non-PDF file is rejected with not-pdf (photos keep their own path).
{
  const v = pdf.validatePdfFile({ name: 'a.png', type: 'image/png', size: 10 });
  assert.strictEqual(v.ok, false);
  assert.strictEqual(v.code, 'not-pdf');
  assert.ok(v.message.length > 0);
}

// 4. An over-cap PDF is rejected with pdf-too-large before pdf.js ever
//    loads it (the honest limit — no partial processing).
{
  const v = pdf.validatePdfFile({
    name: 'big.pdf', type: 'application/pdf', size: pdf.MAX_PDF_BYTES + 1,
  });
  assert.strictEqual(v.ok, false);
  assert.strictEqual(v.code, 'pdf-too-large');
  assert.ok(/40/.test(v.message), 'message names the limit: ' + v.message);
}

// 5. A missing file object is not-pdf, never a throw.
{
  assert.strictEqual(pdf.validatePdfFile(null).code, 'not-pdf');
}

// ----------------------------------------------------------------------
// B. createPagePlan / applyPageEvent / retryPage / planSummary — the
//    per-page status machine (AC-3 处理过程显示状态; AC-4 失败页可重试).
// ----------------------------------------------------------------------

// 6. A pageCount within the cap builds one pending page entry per page.
{
  const plan = pdf.createPagePlan(3);
  assert.strictEqual(plan.ok, true);
  assert.deepStrictEqual(plan.pages.map((p) => [p.no, p.status, p.error]), [
    [1, 'pending', ''],
    [2, 'pending', ''],
    [3, 'pending', ''],
  ]);
  assert.strictEqual(pdf.createPagePlan(pdf.MAX_PDF_PAGES).ok, true);
  assert.strictEqual(pdf.MAX_PDF_PAGES, 100);
}

// 7. Over-cap page count refuses the whole file (AC-3: 页数超限明确反馈).
{
  const plan = pdf.createPagePlan(pdf.MAX_PDF_PAGES + 1);
  assert.strictEqual(plan.ok, false);
  assert.strictEqual(plan.code, 'pdf-too-many-pages');
  assert.strictEqual(plan.limit, pdf.MAX_PDF_PAGES);
  assert.ok(!plan.pages);
}

// 8. The lifecycle: start → running, done → done, fail → failed with the
//    reason recorded. Transitions return a NEW plan (the old one is never
//    mutated — re-render diffing relies on it).
{
  let plan = pdf.createPagePlan(2);
  const before = plan;
  plan = pdf.applyPageEvent(plan, 1, 'start');
  assert.notStrictEqual(plan, before);
  assert.strictEqual(plan.pages[0].status, 'running');
  assert.strictEqual(before.pages[0].status, 'pending');
  assert.strictEqual(plan.pages[1].status, 'pending');

  plan = pdf.applyPageEvent(plan, 1, 'done');
  assert.strictEqual(plan.pages[0].status, 'done');

  plan = pdf.applyPageEvent(plan, 2, 'start');
  plan = pdf.applyPageEvent(plan, 2, 'fail', '识别服务超时');
  assert.strictEqual(plan.pages[1].status, 'failed');
  assert.strictEqual(plan.pages[1].error, '识别服务超时');
}

// 9. retryPage: a failed page resets to pending (error cleared) so the
//    page loop can run it again; done pages are never re-run by it.
{
  let plan = pdf.createPagePlan(2);
  plan = pdf.applyPageEvent(plan, 1, 'start');
  plan = pdf.applyPageEvent(plan, 1, 'done');
  plan = pdf.applyPageEvent(plan, 2, 'start');
  plan = pdf.applyPageEvent(plan, 2, 'fail', 'x');
  plan = pdf.retryPage(plan, 2);
  assert.strictEqual(plan.pages[1].status, 'pending');
  assert.strictEqual(plan.pages[1].error, '');
  const retried = pdf.retryPage(plan, 1); // a done page stays done
  assert.strictEqual(retried.pages[0].status, 'done');
}

// 10. planSummary: counts per status plus the two whole-file verdicts —
//     processing while anything is pending/running, settled once nothing
//     is, allSucceeded only when every page done (a partial failure is
//     never a success — AC-3's 不伪称全文件成功).
{
  let plan = pdf.createPagePlan(3);
  assert.deepStrictEqual(pdf.planSummary(plan), {
    total: 3, pending: 3, running: 0, done: 0, failed: 0,
    processing: true, allSettled: false, allSucceeded: false,
  });
  plan = pdf.applyPageEvent(plan, 1, 'start');
  plan = pdf.applyPageEvent(plan, 1, 'done');
  plan = pdf.applyPageEvent(plan, 2, 'start');
  plan = pdf.applyPageEvent(plan, 2, 'fail', 'e');
  assert.deepStrictEqual(pdf.planSummary(plan), {
    total: 3, pending: 1, running: 0, done: 1, failed: 1,
    processing: true, allSettled: false, allSucceeded: false,
  });
  plan = pdf.applyPageEvent(plan, 3, 'start');
  plan = pdf.applyPageEvent(plan, 3, 'done');
  assert.deepStrictEqual(pdf.planSummary(plan), {
    total: 3, pending: 0, running: 0, done: 2, failed: 1,
    processing: false, allSettled: true, allSucceeded: false,
  });
}

// ----------------------------------------------------------------------
// C. mergePageRows — the ordered per-page segment replace (AC-2 多页结果
//    不乱序、不重复; pages may COMPLETE out of order when failed pages are
//    retried, but rows always land in page order). this.rows is the single
//    source of truth: edits to OTHER pages' rows must survive every merge.
// ----------------------------------------------------------------------

// 11. Pages merged out of completion order still land in PAGE order, every
//     row tagged with its page and re-indexed globally (index must equal
//     the position in the array — the conflict-radio and edit handlers
//     address form.rows[index] with it).
{
  let rows = pdf.mergePageRows([], 2, [
    { word: 'sun', meaning: '太阳' },
    { word: 'moon', meaning: '月亮' },
  ]);
  rows = pdf.mergePageRows(rows, 1, [
    { word: 'apple', meaning: '苹果' },
  ]);
  assert.deepStrictEqual(rows.map((r) => [r.index, r.page, r.word]), [
    [0, 1, 'apple'],
    [1, 2, 'sun'],
    [2, 2, 'moon'],
  ]);
  assert.deepStrictEqual(pdf.mergePageRows(null, 1, []), []);
}

// 12. Re-recognizing a page REPLACES that page's segment in place — the
//     retry can never duplicate what it already produced, and the OTHER
//     pages' row OBJECTS survive untouched (parent edits persist).
{
  let rows = pdf.mergePageRows([], 1, [{ word: 'old', meaning: '' }]);
  rows = pdf.mergePageRows(rows, 2, [{ word: 'keep', meaning: '原义' }]);
  const page2Row = rows[1];
  page2Row.meaning = '家长改过的释义'; // an edit made mid-run
  rows = pdf.mergePageRows(rows, 1, [
    { word: 'new-a', meaning: '' },
    { word: 'new-b', meaning: '' },
  ]);
  assert.deepStrictEqual(rows.map((r) => [r.page, r.word, r.meaning]), [
    [1, 'new-a', ''],
    [1, 'new-b', ''],
    [2, 'keep', '家长改过的释义'],
  ]);
  assert.strictEqual(rows[2], page2Row, 'the surviving row is the SAME object');
}

// 13. Cross-page same-word dedup rides the EXISTING classifier: reclassify
//     runs classifyPhotoEntries over the whole merged array, so a
//     review-unit repeat of page 1's word on page 4 is an invalid 与第 N
//     行重复 row — never a second submittable copy (AC-2).
{
  let rows = pdf.mergePageRows([], 1, [
    { word: 'apple', meaning: '苹果', lesson: '第1课' },
    { word: 'river', meaning: '河流', lesson: '第1课' },
  ]);
  rows = pdf.mergePageRows(rows, 4, [
    { word: 'apple', meaning: '苹果', lesson: '第4课' }, // review-unit repeat
    { word: 'goat', meaning: '山羊', lesson: '第4课' },
  ]);
  const reclassified = classifyPhotoEntries(
    rows.map((r) => ({ word: r.word, meaning: r.meaning, lesson: r.lesson })), []);
  assert.deepStrictEqual(reclassified.map((r) => [r.word, r.status, r.reason]), [
    ['apple', 'new', ''],
    ['river', 'new', ''],
    ['apple', 'invalid', 'duplicate:1'],
    ['goat', 'new', ''],
  ]);
}

// ----------------------------------------------------------------------
// D. rowsByPage / applyLessonToPage / chunkBatch — the per-page lesson
//    batch (AC-6 可批量调整课次, US-13) and the commit slicing into the
//    server's batch caps (AC-5 整本教材受控批量提交).
// ----------------------------------------------------------------------

// 14. rowsByPage groups the flattened rows in page order (page-0 = the
//     plain photo flow's untagged rows, one group).
{
  const groups = pdf.rowsByPage([
    { index: 0, page: 1, word: 'a' },
    { index: 1, page: 1, word: 'b' },
    { index: 2, page: 3, word: 'c' },
    { index: 3, word: 'photo-only' },
  ]);
  assert.deepStrictEqual(groups.map((g) => [g.no, g.rows.map((r) => r.word)]), [
    [0, ['photo-only']],
    [1, ['a', 'b']],
    [3, ['c']],
  ]);
}

// 15. applyLessonToPage sets ONE lesson on the page's committable checked
//     rows only (the toolbar's semantics scoped to a page); invalid rows
//     and other pages are untouched, and the input array is not mutated.
{
  const rows = [
    { word: 'a', lesson: '', status: 'new', checked: true, page: 1 },
    { word: 'b', lesson: '旧', status: 'new', checked: false, page: 1 },
    { word: 'c', lesson: '', status: 'invalid', reason: 'empty-word', page: 1 },
    { word: 'd', lesson: '第2课', status: 'new', checked: true, page: 2 },
  ];
  const next = pdf.applyLessonToPage(rows, 1, '第1课');
  assert.deepStrictEqual(next.map((r) => r.lesson), ['第1课', '旧', '', '第2课']);
  assert.deepStrictEqual(rows.map((r) => r.lesson), ['', '旧', '', '第2课']);
  // An empty lesson is a REAL value too — 批量改回待确认 must clear.
  const cleared = pdf.applyLessonToPage(next, 1, '');
  assert.deepStrictEqual(cleared.map((r) => r.lesson), ['', '旧', '', '第2课']);
}

// 16. chunkBatch slices for the server caps: 100 entries per add call
//     (maxEntries), 500 ids per assign call — order preserved, no loss.
{
  assert.deepStrictEqual(pdf.chunkBatch([], 100), []);
  const items = Array.from({ length: 250 }, (_, i) => i);
  const chunks = pdf.chunkBatch(items, 100);
  assert.deepStrictEqual(chunks.map((c) => c.length), [100, 100, 50]);
  assert.deepStrictEqual([].concat(...chunks), items);
  assert.deepStrictEqual(pdf.chunkBatch([1, 2], 0), [[1], [2]]); // size < 1 ⇒ 1
}

console.log('test-parent-photo-pdf: all assertions passed');
