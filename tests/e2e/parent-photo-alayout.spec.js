/**
 * Parent photo-import A 并排核对 workspace E2E (Issue #81).
 *
 * The redesign puts the 本次材料 large image and the 学习安排 panel beside
 * the 词条核对 table on wide screens, with a plain-text state line above
 * them (教材 · 进度 · 本批词数 · 本批课次 + one honest scheduling note)
 * and a separate 默写安排 block in the commit receipt. This spec pins the
 * acceptance contract against the ISOLATED real service: real browser →
 * real Go service → real Python bridge → real SQLite (per-run temp DB).
 * Only the external recognition service is stubbed with the fixture reply.
 *
 * Test-data isolation: ONE textbook is REST-created for this suite (the
 * fixture's PEP meta family is what matchTextbookForMeta preselects) and
 * its meta is neutralized in afterAll so later suites that reuse the same
 * fixture still take their own 新建教材 path. Every test imports into its
 * own unit and deletes exactly that unit in finally.
 *
 * Acceptance coverage (Issue #81):
 *   1. ≥1024px: source and review columns are genuinely side by side; the
 *      state line states known progress as a FACT (never a question); the
 *      receipt separates 默写安排 from the save result; edits persist and
 *      the saved words really enter the classroom.
 *   2. Duplicate re-import: 合并 3, stable card ids, no progress question.
 *   3. Unlearned lesson: warn hint + 暂不自动安排 3 词 receipt, saved words,
 *      and progress NOT advanced by the import.
 *   4. 放弃识别结果 writes nothing (API snapshot unchanged).
 *   5. 390px: no page-level horizontal overflow, primary actions reachable,
 *      16px inputs (iOS zoom floor), screenshot captured.
 */
// @ts-check
const { test, expect, request } = require('@playwright/test');
const path = require('node:path');
const {
  recognize, deleteUnit, wordRows, textbooks, awaitReceipt, practiceSelection,
} = require('./helpers/photo-flow');

const CAT = 'english';

const FIXTURES = path.resolve(__dirname, 'fixtures');
const FIXTURE_PEP = path.join(FIXTURES, 'photo-textbook-pep.png');

// The fixture's recognized meta is 人教版PEP / 三年级 / 上册 / Unit 3 /
// 第2课. The book carries the same meta family so the import preselects it.
const BOOK_NAME = 'Issue81并排核对·人教版PEP·三年级·上册·Unit 3';
const LESSONS = ['第1课', '第2课', '第3课'];
const LESSON = '第2课';

const UNITS = {
  wide: 'e2e-alayout-wide',
  reimport: 'e2e-alayout-reimport',
  unlearned: 'e2e-alayout-unlearned',
  discard: 'e2e-alayout-discard',
  narrow: 'e2e-alayout-narrow',
};

// One API context for the suite-scoped textbook (the per-test `request`
// fixture only lives for its own test).
let api;
let bookId = 0;

test.beforeAll(async () => {
  api = await request.newContext({ baseURL: 'http://127.0.0.1:18080' });
  const created = await api.post('/api/v1/textbooks', {
    data: {
      name: BOOK_NAME,
      category: CAT,
      edition: '人教版PEP',
      grade: '三年级',
      volume: '上册',
      module: 'Unit 3',
      lessons: LESSONS,
    },
  });
  expect(created.ok()).toBeTruthy();
  const book = await created.json();
  bookId = book.id;
  expect(bookId).toBeGreaterThan(0);

  const progress = await api.put('/api/v1/textbooks/' + bookId, {
    data: { learnedLessons: 2 },
  });
  expect(progress.ok()).toBeTruthy();
});

test.afterAll(async () => {
  // No DELETE route exists. Neutralize the meta family so the NEXT suite
  // that recognizes the same PEP fixture does not preselect this book.
  if (bookId) {
    await api.put('/api/v1/textbooks/' + bookId, {
      data: {
        edition: 'Issue81已清理',
        stage: 'Issue81已清理',
        grade: 'Issue81已清理',
        volume: 'Issue81已清理',
        module: 'Issue81已清理',
      },
    }).catch(() => {});
  }
  if (api) {
    await api.dispose();
  }
});

/** Apply 保留词库释义 to every currently-conflicting row (if any). */
async function keepAllConflicts(page) {
  const rows = page.locator('.photo-row-conflict');
  const count = await rows.count();
  for (let i = 0; i < count; i += 1) {
    const keep = rows.nth(i).locator('.photo-conflict-choice input[value="keep"]');
    if (await keep.count()) {
      await keep.check();
    }
  }
  return count;
}

/** The state line is plain text: it must never ask the parent a question
 *  or render a progress-choice control (the old flow re-asked progress). */
async function expectNoAsk(stateLine) {
  await expect(stateLine).toBeVisible();
  await expect(stateLine).not.toContainText('？');
  await expect(stateLine).not.toContainText('?');
  await expect(stateLine).not.toContainText('请问');
  await expect(stateLine.locator('button, input, select, textarea')).toHaveCount(0);
}

test('wide layout: side-by-side source/review, fact-only state line, separate 默写安排 receipt, persisted edits feed the classroom', async ({ page, request: req }) => {
  await deleteUnit(req, UNITS.wide);
  await page.setViewportSize({ width: 1280, height: 900 });
  try {
    await recognize(page, FIXTURE_PEP, UNITS.wide);

    // The workspace replaces the compact picker once a file is chosen.
    const workspace = page.locator('.photo-workspace');
    await expect(workspace).toBeVisible();
    await expect(page.locator('.photo-source-img')).toBeVisible();
    await expect(page.locator('.photo-review-title')).toHaveText('词条核对');

    // ≥1024px: the two columns share the row — source strictly left of
    // review, vertical spans overlapping, no horizontal pile-up.
    const source = page.locator('.photo-col-source');
    const review = page.locator('.photo-col-review');
    await expect(source).toBeVisible();
    await expect(review).toBeVisible();
    const sb = await source.boundingBox();
    const rb = await review.boundingBox();
    expect(sb && rb).toBeTruthy();
    expect(sb.x + sb.width, 'source column ends before the review column starts')
      .toBeLessThanOrEqual(rb.x + 1);
    expect(sb.y, 'columns share the row').toBeLessThan(rb.y + rb.height);
    expect(rb.y).toBeLessThan(sb.y + sb.height);

    // State line: textbook + known progress are stated as facts, and the
    // positive hint is the exact no-re-ask sentence.
    const stateLine = page.locator('.photo-state-line');
    await expect(stateLine).toContainText(BOOK_NAME);
    await expect(stateLine).toContainText('已学完第 2 课');
    await expect(stateLine.locator('.photo-state-hint-good')).toContainText(
      '课次「第2课」已学到，保存后即可安排默写，无需再确认进度。');
    await expectNoAsk(stateLine);

    // Inline edits: re-type the word face and give two rows their own
    // meanings (the same two surfaces later re-imported as conflicts).
    const row = (i) => page.locator(`.photo-row[data-row-index="${i}"]`);
    const tigerWord = row(0).locator('input[aria-label="单词 第 1 行"]');
    const tigerMeaning = row(0).locator('input[aria-label="释义 第 1 行"]');
    await tigerWord.fill('tiger');
    await tigerMeaning.fill('老虎（本次核对）');
    await expect(tigerWord).toHaveValue('tiger');
    await expect(tigerMeaning).toHaveValue('老虎（本次核对）');
    await row(1).locator('input[aria-label="单词 第 2 行"]').fill('panda');
    await row(1).locator('input[aria-label="释义 第 2 行"]').fill('熊猫（本次核对）');

    await page.getByRole('button', { name: '确认添加（3 个）' }).click();

    // The receipt keeps 保存结果 and 默写安排 visually separate; the scope
    // line is derived from the actual assign results (第2课 is learned).
    await awaitReceipt(page, BOOK_NAME, { 新增: 3 });
    const banner = page.locator('.result-banner');
    await expect(banner.locator('.result-scope-title')).toHaveText('默写安排');
    await expect(banner.locator('.result-scope-good')).toContainText(
      '可安排默写 3 词（课次已学）');
    await expect(banner.locator('.result-scope-next')).toContainText('下一步：');

    // Persisted truth after a reload (client state gone), then the real
    // classroom selection.
    await page.reload();
    const rows = await wordRows(req);
    for (const word of ['tiger', 'panda', 'grape']) {
      const stored = rows.get(word);
      expect(stored, word + ' is stored').toBeTruthy();
      const mine = (stored.textbooks || []).find((t) => t.name === BOOK_NAME);
      expect(mine, word + ' carries this textbook').toBeTruthy();
      expect(mine.lesson).toBe(LESSON);
    }
    expect(rows.get('tiger').meaning).toBe('老虎（本次核对）');
    expect(rows.get('panda').meaning).toBe('熊猫（本次核对）');

    const picked = await practiceSelection();
    for (const word of ['tiger', 'panda', 'grape']) {
      expect(picked).toContain(word);
    }
  } finally {
    await deleteUnit(req, UNITS.wide);
  }
});

test('duplicate re-import: 合并 3, stable card ids, known progress stays a fact', async ({ page, request: req }) => {
  await deleteUnit(req, UNITS.reimport);
  try {
    // First import in THIS test's unit. Depending on whether the wide test
    // already saved the fixture, this is 新增 or 合并 — either way the
    // second upload below is a real duplicate.
    await recognize(page, FIXTURE_PEP, UNITS.reimport);
    await keepAllConflicts(page);
    await expect(page.getByRole('button', { name: '确认添加（3 个）' })).toBeVisible();
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await expect(page.locator('.result-banner .result-summary')).toBeVisible({ timeout: 30_000 });

    const before = await wordRows(req);
    const beforeIds = new Map(
      ['tiger', 'panda', 'grape'].map((w) => [w, before.get(w).id]));

    // The SAME fixture again: all three are duplicates → one 合并 3 line,
    // and no progress question appears anywhere — the state line repeats
    // the known-progress fact.
    await recognize(page, FIXTURE_PEP, UNITS.reimport);
    const stateLine = page.locator('.photo-state-line');
    await expect(stateLine.locator('.photo-state-hint-good')).toContainText(
      '课次「第2课」已学到，保存后即可安排默写，无需再确认进度。');
    await expectNoAsk(stateLine);
    await keepAllConflicts(page);

    await expect(page.getByRole('button', { name: '确认添加（3 个）' })).toBeVisible();
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await awaitReceipt(page, BOOK_NAME, { 合并: 3 });

    const after = await wordRows(req);
    for (const [word, id] of beforeIds) {
      expect(after.get(word), word + ' still exists').toBeTruthy();
      expect(after.get(word).id, word + ' kept its card id').toBe(id);
    }
    expect(after.size).toBe(before.size);
  } finally {
    await deleteUnit(req, UNITS.reimport);
  }
});

test('unlearned lesson: warn state line + 暂不自动安排 receipt, saved words, progress untouched', async ({ page, request: req }) => {
  await deleteUnit(req, UNITS.unlearned);
  // learnedLessons=1 means only 第1课 is learned, so the fixture's 第2课
  // is beyond progress. The import must NOT advance the book's progress.
  const setProgress = await req.put('/api/v1/textbooks/' + bookId, {
    data: { learnedLessons: 1 },
  });
  expect(setProgress.ok()).toBeTruthy();

  try {
    await recognize(page, FIXTURE_PEP, UNITS.unlearned);

    const stateLine = page.locator('.photo-state-line');
    await expect(stateLine.locator('.photo-state-hint-warn')).toContainText(
      '课次「第2课」尚未学到：保存后这部分词暂不自动安排默写，进度不受影响。');
    await expectNoAsk(stateLine);

    await keepAllConflicts(page);
    await expect(page.getByRole('button', { name: '确认添加（3 个）' })).toBeVisible();
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await expect(page.locator('.result-banner .result-summary')).toBeVisible({ timeout: 30_000 });

    // The receipt tells the truth about scheduling, separately from saving.
    const banner = page.locator('.result-banner');
    await expect(banner.locator('.result-scope-title')).toHaveText('默写安排');
    await expect(banner.locator('.result-scope-wait')).toHaveText(
      '暂不自动安排 3 词（课次「第2课」尚未学到）');

    // The import saved the words but did NOT move progress.
    const books = await textbooks(req);
    const book = books.find((b) => b.id === bookId);
    expect(book).toBeTruthy();
    expect(book.learnedLessons).toBe(1);
    const rows = await wordRows(req);
    for (const word of ['tiger', 'panda', 'grape']) {
      expect(rows.has(word), word + ' is saved').toBe(true);
    }
  } finally {
    await deleteUnit(req, UNITS.unlearned);
  }
});

test('放弃识别结果 writes nothing (API snapshot unchanged)', async ({ page, request: req }) => {
  await deleteUnit(req, UNITS.discard);
  const snapshot = (map) => JSON.stringify(
    [...map.entries()].sort(([a], [b]) => a.localeCompare(b)));
  const before = snapshot(await wordRows(req));

  try {
    await recognize(page, FIXTURE_PEP, UNITS.discard);
    await expect(page.locator('.photo-row')).toHaveCount(3);

    await page.getByRole('button', { name: '放弃识别结果' }).click();
    await expect(page.locator('.photo-row')).toHaveCount(0);
    await expect(page.locator('#add-panel-photo .preview-empty')).toBeVisible();

    expect(snapshot(await wordRows(req))).toBe(before);
  } finally {
    await deleteUnit(req, UNITS.discard);
  }
});

test('narrow 390×844: no page overflow, primary actions reachable, 16px inputs', async ({ page, request: req }) => {
  await deleteUnit(req, UNITS.narrow);
  // Restore a known-progress book for the narrow check.
  const setProgress = await req.put('/api/v1/textbooks/' + bookId, {
    data: { learnedLessons: 2 },
  });
  expect(setProgress.ok()).toBeTruthy();

  try {
    await page.setViewportSize({ width: 390, height: 844 });
    await recognize(page, FIXTURE_PEP, UNITS.narrow);
    await keepAllConflicts(page);

    // The table may scroll INSIDE .preview-scroll; the document itself
    // must never be wider than the phone viewport.
    const overflow = await page.evaluate(() => ({
      scrollW: document.scrollingElement.scrollWidth,
      innerW: window.innerWidth,
    }));
    expect(overflow.scrollW, 'no horizontal document overflow').toBeLessThanOrEqual(overflow.innerW);

    // The two inputs keep the 16px iOS no-zoom floor.
    const fontSizes = await page.evaluate(() => {
      const inputs = Array.from(document.querySelectorAll(
        '.photo-row input[aria-label^="单词 "], .photo-row input[aria-label^="释义 "]'));
      return inputs.map((el) => parseFloat(getComputedStyle(el).fontSize));
    });
    expect(fontSizes.length).toBeGreaterThan(0);
    for (const size of fontSizes) {
      expect(size, 'review input font-size >= 16px').toBeGreaterThanOrEqual(16);
    }

    const commit = page.getByRole('button', { name: '确认添加（3 个）' });
    await commit.scrollIntoViewIfNeeded();
    await expect(commit).toBeVisible();
    await expect(commit).toBeEnabled();

    const discard = page.getByRole('button', { name: '放弃识别结果' });
    await discard.scrollIntoViewIfNeeded();
    await expect(discard).toBeVisible();

    await page.screenshot({ path: test.info().outputPath('narrow-alayout.png'), fullPage: true });
  } finally {
    await deleteUnit(req, UNITS.narrow);
  }
});
