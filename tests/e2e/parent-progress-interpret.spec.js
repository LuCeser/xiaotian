/**
 * Parent progress-interpret E2E (Issue #82): 一句话更新教材教学进度并
 * 贯通自动选词, proven against the ISOLATED real service — real browser →
 * real Go service → real Python bridge → real SQLite (per-run temp DB).
 * Only the EXTERNAL text-LLM boundary is the mimo stub, keyed on the
 * parent's own sentence (text-progress-*.reply.json fixtures).
 *
 * Acceptance link (issue #82):
 *   AC-1/2: 「现在学到第三单元了」 through the real 教材管理 flow → the
 *     stubbed LLM proposes, the SERVER verifies the lesson against the
 *     authoritative list and the count by LIST position (进度理解A's list
 *     第二,第三,第一,第四 is deliberately NOT dictionary order — 已学完
 *     第 2 课「第三单元」, not 第 3 课), the parent's 记住这个进度 click
 *     persists through the ordinary PUT, a RELOAD reads it back, and a
 *     real /rt practice probe selects exactly the lessons at list
 *     positions ≤ 2 (第一/第四单元 words stay out).
 *   AC-3: same-name lesson in two books → unclear + minimal candidate
 *     buttons; the parent's explicit pick moves ONLY that book.
 *   AC-6: LLM unavailable (503) → the visible manual path is named;
 *     save failure (502) → 未记住 toast, row unchanged; a LATE model
 *     reply arriving after the parent's explicit select choice is
 *     dropped, never overriding it.
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const WebSocket = require('ws');

const CAT = 'english';
const UNIT = 'e2e-progress-interpret';

// 进度理解A's lesson list deliberately contradicts both dictionary order
// and the sentence's own numbering: 第三单元 is LIST position 2, so the
// honest progress after 「学到第三单元」 is 已学完第 2 课.
const BOOK_A = '进度理解A';
const BOOK_A_LESSONS = ['第二单元', '第三单元', '第一单元', '第四单元'];
const BOOK_B = '进度理解B';

// Seeded vocabulary → assignment. Eligible after 学到第三单元 (= list
// positions 1-2): L2a/L2b/L3. L1 (position 3) and L4 (position 4) stay out.
const WORDS = {
  L2a: 'pgL2a', // → A 第二单元 (list position 1)
  L2b: 'pgL2b', // → A 第二单元
  L3: 'pgL3', // → A 第三单元 (list position 2)
  L1: 'pgL1', // → A 第一单元 (list position 3 — NOT learned despite the name)
  L4: 'pgL4', // → A 第四单元 (list position 4)
};
const ALL_WORDS = Object.values(WORDS);

/** DELETE the whole unit through the real API (404 = already gone). */
async function deleteUnit(request) {
  await request.delete('/api/v1/words', { data: { category: CAT, unit: UNIT } }).catch(() => {});
}

const sorted = (list) => [...list].sort();

/** Open the parent page into the 词库 browse view. */
async function openWordLib(page) {
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await expect(page.locator('#browseSection')).toBeVisible();
}

/** One real /rt practice probe: resolves with session_ready's selected
 * word list (the REAL selection: participation + textbook scope). */
function practiceSelection() {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket('ws://127.0.0.1:18080/rt', {
      origin: 'http://127.0.0.1:18080',
    });
    const fail = setTimeout(() => reject(new Error('session_ready timeout')), 30_000);
    ws.on('error', (err) => { clearTimeout(fail); reject(err); });
    ws.on('open', () => {
      ws.send(JSON.stringify({
        type: 'session_init',
        practice: true,
        mode: 'test',
        audio: { sampleRate: 16000, format: 's16le', channels: 1 },
      }));
    });
    ws.on('message', (data) => {
      const msg = JSON.parse(String(data));
      if (msg.type === 'session_ready') {
        clearTimeout(fail);
        ws.close();
        resolve(msg.lesson.words);
      } else if (msg.type === 'error') {
        clearTimeout(fail);
        ws.close();
        reject(new Error('session error: ' + JSON.stringify(msg)));
      }
    });
  });
}

/** Fetch card ids for the seeded unit through the real read API. */
async function cardIdsByWord(request) {
  const res = await request.get(`/api/v1/words?category=${CAT}&unit=${UNIT}`);
  expect(res.ok()).toBeTruthy();
  const body = await res.json();
  const ids = {};
  for (const w of body.words) {
    ids[w.word] = w.id;
  }
  return ids;
}

/** Create one textbook through the real REST write path. */
async function createTextbook(request, name, lessons) {
  const res = await request.post('/api/v1/textbooks', {
    data: { name, category: CAT, lessons },
  });
  expect(res.ok()).toBeTruthy();
  const books = await (await request.get(`/api/v1/textbooks?category=${CAT}`)).json();
  return books.textbooks.find((b) => b.name === name);
}

/** Open 教材管理; resolves the modal locator. */
async function openManager(page) {
  await page.locator('#textbook-manage-btn').click();
  const mgr = page.locator('.modal.modal-wide');
  await expect(mgr).toBeVisible();
  return mgr;
}

/** Submit one sentence through the 说进度 area. */
async function sayProgress(mgr, sentence) {
  await mgr.locator('#tb-progress-say').fill(sentence);
  await mgr.getByRole('button', { name: '小天理解' }).click();
}

test('progress interpret: one sentence persists verified LIST-position progress, survives reload, and shapes the real classroom selection', async ({ page, request }) => {
  await deleteUnit(request);
  const seed = await request.post('/api/v1/words', {
    data: {
      category: CAT, unit: UNIT,
      entries: ALL_WORDS.map((w) => ({ word: w, meaning: '进度e2e' })),
    },
  });
  expect(seed.ok()).toBeTruthy();

  const bookA = await createTextbook(request, BOOK_A, BOOK_A_LESSONS);
  const ids = await cardIdsByWord(request);
  const assign = async (words, lesson) => {
    const res = await request.post(`/api/v1/textbooks/${bookA.id}/assign`, {
      data: { cardIds: words.map((w) => ids[w]), lesson },
    });
    expect(res.ok()).toBeTruthy();
  };
  await assign([WORDS.L2a, WORDS.L2b], '第二单元');
  await assign([WORDS.L3], '第三单元');
  await assign([WORDS.L1], '第一单元');
  await assign([WORDS.L4], '第四单元');

  try {
    await openWordLib(page);
    const mgr = await openManager(page);

    // Nothing is written before the parent's explicit click: the row
    // still reads 尚未开始 while the proposal is on screen.
    const rowA = mgr.locator('.textbook-row', { hasText: BOOK_A });
    await expect(rowA.locator('.textbook-progress')).toHaveText('尚未开始（共 4 课）');

    await sayProgress(mgr, '现在学到第三单元了');
    await expect(mgr.locator('.textbook-interpret-result')).toContainText(
      '小天理解：《进度理解A》已学完「第三单元」——它和之前的课次都算已学', { timeout: 15_000 });
    await expect(rowA.locator('.textbook-progress')).toHaveText('尚未开始（共 4 课）');

    await mgr.getByRole('button', { name: '记住这个进度' }).click();
    await expect(page.locator('.toast-success')).toContainText('已记住：《进度理解A》已学完「第三单元」');
    // LIST position, not the sentence's own number: 第三单元 is 第 2 课.
    await expect(rowA.locator('.textbook-progress')).toHaveText('已学完第 2 课「第三单元」（共 4 课）');

    // 刷新读回 (AC-2): close, reload, reopen — the progress persisted.
    await mgr.getByRole('button', { name: '取消' }).click();
    await expect(mgr).toBeHidden();
    await page.reload();
    await page.getByRole('tab', { name: '词库' }).click();
    const mgr2 = await openManager(page);
    await expect(mgr2.locator('.textbook-row', { hasText: BOOK_A }).locator('.textbook-progress'))
      .toHaveText('已学完第 2 课「第三单元」（共 4 课）');
    await mgr2.getByRole('button', { name: '取消' }).click();
    await expect(mgr2).toBeHidden();

    // The real classroom honors the sentence-made progress: exactly the
    // list positions ≤ 2 (第二单元 + 第三单元); 第一单元 (position 3,
    // dictionary thinking would include it) and 第四单元 stay out.
    const picked = sorted(await practiceSelection());
    expect(picked).toEqual(sorted([WORDS.L2a, WORDS.L2b, WORDS.L3]));
  } finally {
    await deleteUnit(request);
  }
});

test('progress interpret: same-name lesson in two books asks the minimal question and moves only the explicitly chosen book', async ({ page, request }) => {
  const bookB = await createTextbook(request, BOOK_B, ['第三单元', '第五单元']);
  expect(bookB.id).toBeTruthy();

  await openWordLib(page);
  const mgr = await openManager(page);

  await sayProgress(mgr, '第三单元学完了');
  const result = mgr.locator('.textbook-interpret-result');
  await expect(result).toContainText('请选择是哪一本', { timeout: 15_000 });

  // The minimal confirmation: one button per candidate book.
  const candidateB = result.getByRole('button', { name: `《进度理解B》：已学完「第三单元」` });
  await expect(candidateB).toBeVisible();
  await expect(result.getByRole('button', { name: `《进度理解A》：已学完「第三单元」` })).toBeVisible();

  await candidateB.click();
  await expect(page.locator('.toast-success')).toContainText('已记住：《进度理解B》已学完「第三单元」');
  // Only the explicitly chosen book moved (第三单元 is position 1 in B).
  await expect(mgr.locator('.textbook-row', { hasText: BOOK_B }).locator('.textbook-progress'))
    .toHaveText('已学完第 1 课「第三单元」（共 2 课）');
  // 进度理解A keeps its OWN progress from the previous test — a same-name
  // lesson never leaks across books.
  await expect(mgr.locator('.textbook-row', { hasText: BOOK_A }).locator('.textbook-progress'))
    .toHaveText('已学完第 2 课「第三单元」（共 4 课）');
});

test('progress interpret: a non-progress sentence is explained, not applied', async ({ page }) => {
  await openWordLib(page);
  const mgr = await openManager(page);

  await sayProgress(mgr, '今天开始默写吧');
  const result = mgr.locator('.textbook-interpret-result');
  await expect(result).toContainText('这句话不是在说明教学进度', { timeout: 15_000 });
  await expect(result).toContainText('要开始默写请回到首页');
  // No confirm action is offered for a non-progress statement.
  await expect(mgr.getByRole('button', { name: '记住这个进度' })).toHaveCount(0);
});

test('progress interpret: degraded paths stay honest (503 names the manual path, save failure claims nothing, late replies never override)', async ({ page }) => {
  await openWordLib(page);
  const mgr = await openManager(page);
  const result = mgr.locator('.textbook-interpret-result');
  const rowA = mgr.locator('.textbook-row', { hasText: BOOK_A });

  // LLM unavailable (AC-6): the endpoint answers 503 — the copy names the
  // always-available manual path instead of pretending.
  await page.route('**/api/v1/textbooks/progress/interpret', async (route) => {
    await route.fulfill({
      status: 503, contentType: 'application/json',
      body: '{"error":{"code":"interpretation_unavailable","message":"not configured"}}',
    });
  }, { times: 1 });
  await sayProgress(mgr, '现在学到第三单元了');
  await expect(result).toContainText('理解服务暂不可用');
  await expect(result).toContainText('直接用下方每本教材的进度选择');

  // Save failure (AC-6): the PUT fails once — the toast says 未记住, the
  // row keeps its progress, and the proposal STAYS for a retry. (第四单元
  // is unique to 进度理解A — 进度理解B exists by now, so the same-name
  // 第三单元 would honestly turn ambiguous.)
  await page.route('**/api/v1/textbooks/*', async (route) => {
    if (route.request().method() === 'PUT') {
      await route.fulfill({
        status: 502, contentType: 'application/json',
        body: '{"error":{"code":"python_failed","message":"temporary"}}',
      });
      return;
    }
    await route.continue();
  }, { times: 1 });
  await sayProgress(mgr, '现在学到第四单元了');
  await expect(result).toContainText('小天理解：《进度理解A》已学完「第四单元」', { timeout: 15_000 });
  await mgr.getByRole('button', { name: '记住这个进度' }).click();
  await expect(page.locator('.toast-error')).toContainText('保存失败，进度没有记住');
  await expect(rowA.locator('.textbook-progress')).toHaveText('已学完第 2 课「第三单元」（共 4 课）');
  await expect(result).toContainText('小天理解：《进度理解A》已学完「第四单元」');

  // Late model reply (AC-6): the interpret response is delayed past the
  // parent's explicit select choice — the stale reply is dropped and the
  // parent's choice stands.
  await page.route('**/api/v1/textbooks/progress/interpret', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 800));
    await route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ status: 'proposed', textbookName: BOOK_A, lesson: '第四单元', learnedLessons: 4 }),
    });
  }, { times: 1 });
  await sayProgress(mgr, '现在学到第三单元了');
  await expect(result).toContainText('小天正在理解…');
  // The parent's explicit choice while the model is still "thinking".
  await rowA.locator(`select[aria-label="已学进度 ${BOOK_A}"]`)
    .selectOption({ label: '已学完第 1 课「第二单元」' });
  await expect(rowA.locator('.textbook-progress')).toHaveText('已学完第 1 课「第二单元」（共 4 课）');
  // The late reply lands after the choice: no proposal, no override.
  await page.waitForTimeout(1500);
  await expect(result).not.toContainText('第四单元');
  await expect(result).not.toContainText('小天理解');
  await expect(rowA.locator('.textbook-progress')).toHaveText('已学完第 1 课「第二单元」（共 4 课）');
});
