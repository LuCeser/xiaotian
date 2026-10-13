/**
 * Parent photo RE-import E2E (Issue #6): 重复导入保留多来源并确认释义差异,
 * proven against the ISOLATED real service — real browser → real Go → real
 * Python bridge → real SQLite (per-run temp DB shared across the run, the
 * Issue #3 boundary: only the external vision service is stubbed with
 * fixture-matched canned replies).
 *
 * Fixture families: 科普版 / 仁爱版 are OWNED by this suite (the #5
 * families never collide). "eagle" is printed in BOTH books — the
 * multi-source vehicle; the 科普版 page itself is the 重传 vehicle; its
 * "camel" row is the 释义差异 candidate.
 *
 * Acceptance link (issue #6), one test per observable scenario:
 *   1. 重传同一页 — the same page submitted twice creates each word ONCE:
 *      ids stable, one assignment per (word, book), receipt says 合并 not
 *      新增 (AC-1/AC-6; history-at-byte-level is the pybridge e2e's half).
 *   2. 同词两本教材 — eagle keeps BOTH sources; the LEARNED source admits
 *      it into real classroom selection while the unlearned one never
 *      blocks; whale (unlearned-only) stays out. The parent's 移出
 *      (participates=0) survives re-import and still gates selection, and
 *      an explicit include_in_scope on the target book survives re-import
 *      (AC-2/AC-4 combination rules).
 *   3. 释义差异确认 — the conflict shows BOTH readings before submit; an
 *      unresolved commit reports 待处理差异 and keeps the original meaning
 *      with NO source merge; 采用照片释义 confirms → the meaning updates
 *      through the real edit path, the source merges, and the classroom
 *      serves the confirmed word (AC-3/AC-7).
 *
 * Classroom probes: raw /rt practice session_init (mode=test full-reveal;
 * ≥1.1s apart — r0x lesson ids are second-granular, same-second probes
 * replay the first's frozen row).
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const path = require('node:path');
const {
  recognize, deleteUnit, wordRows, textbooks, awaitReceipt, practiceSelection,
} = require('./helpers/photo-flow');

const CAT = 'english';
const UNIT = 'e2e-photo-reimport';

const FIXTURES = path.resolve(__dirname, 'fixtures');
const FIXTURE_KEPU = path.join(FIXTURES, 'photo-textbook-kepu.png');
const FIXTURE_RENJIAO = path.join(FIXTURES, 'photo-textbook-renjiao.png');

const BOOK_KEPU = '科普版·小学·三年级·上册·Unit 2';
const BOOK_RENJIAO = '仁爱版·小学·三年级·上册·Unit 1';

test('re-upload: the same page committed twice creates each word once — ids stable, sources not duplicated, receipt says 合并', async ({ page, request }) => {
  await deleteUnit(request, UNIT);
  try {
    // First commit: three 新增 into the freshly created 科普版 book.
    await recognize(page, FIXTURE_KEPU, UNIT);
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await awaitReceipt(page, BOOK_KEPU, { 新增: 3 });
    await expect(page.locator('.result-banner')).toContainText(
      '已指定课次「第1课」：eagle、deer、camel');

    const first = await wordRows(request);
    for (const w of ['eagle', 'deer', 'camel']) {
      const row = first.get(w);
      expect(row).toBeTruthy();
      const mine = (row.textbooks || []).filter((t) => t.name === BOOK_KEPU);
      expect(mine.length).toBe(1);
      expect(mine[0].lesson).toBe('第1课');
    }

    // Re-upload the very SAME page: every row reads 已有; the commit merges
    // sources only (AC-1: 重复导入不创建新的练习内容).
    await recognize(page, FIXTURE_KEPU, UNIT);
    await expect(page.locator('.photo-row-exists')).toHaveCount(3);
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await awaitReceipt(page, BOOK_KEPU, { 合并: 3 });
    await expect(page.locator('.result-banner')).toContainText(
      '合并：eagle（词库已有，仅指定课次）');

    // Real database truth: same three words, SAME ids (the identity
    // anchor), still exactly ONE 科普版 source each — 重复提交不重复写入.
    const second = await wordRows(request);
    for (const w of ['eagle', 'deer', 'camel']) {
      expect(second.get(w).id).toBe(first.get(w).id);
      const mine = (second.get(w).textbooks || []).filter((t) => t.name === BOOK_KEPU);
      expect(mine.length).toBe(1);
      expect(mine[0].lesson).toBe('第1课');
    }
  } finally {
    await deleteUnit(request, UNIT);
  }
});

test('one word, two books: a learned source admits it, an unlearned one never blocks; 移出 and explicit include survive re-import', async ({ page, request }) => {
  await deleteUnit(request, UNIT);
  try {
    // 科普版 第1课 (eagle/deer/camel), then marked learned.
    await recognize(page, FIXTURE_KEPU, UNIT);
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await awaitReceipt(page, BOOK_KEPU, { 新增: 3 });
    const books1 = await textbooks(request);
    const kepu = books1.find((b) => b.name === BOOK_KEPU);
    const put = await request.put('/api/v1/textbooks/' + kepu.id, { data: { learnedLessons: 1 } });
    expect(put.ok()).toBeTruthy();

    // 仁爱版 re-imports eagle (the multi-source word) and adds whale.
    await recognize(page, FIXTURE_RENJIAO, UNIT);
    await expect(page.locator('.photo-row-exists')).toHaveCount(1);
    await expect(page.locator('.photo-row-new')).toHaveCount(1);
    await page.getByRole('button', { name: '确认添加（2 个）' }).click();
    await awaitReceipt(page, BOOK_RENJIAO, { 新增: 1, 合并: 1 });

    // AC-2/US-32: eagle keeps BOTH sources in the real projection.
    let rows = await wordRows(request);
    const eagleBooks = (rows.get('eagle').textbooks || []).map((t) => t.name).sort();
    expect(eagleBooks).toEqual([BOOK_RENJIAO, BOOK_KEPU].sort());

    // US-33: the learned 科普版 source admits eagle into REAL classroom
    // selection while the unlearned 仁爱版 source does not block; whale
    // (unlearned-only) stays out.
    let picked = await practiceSelection();
    for (const w of ['eagle', 'deer', 'camel']) {
      expect(picked).toContain(w);
    }
    expect(picked).not.toContain('whale');

    // AC-4: the parent's explicit 移出 (participates=0) …
    const eagleId = rows.get('eagle').id;
    const off = await request.put('/api/v1/words/participation', {
      data: { ids: [eagleId], participates: false },
    });
    expect(off.ok()).toBeTruthy();

    // … survives a re-import of the page that contains the word.
    await recognize(page, FIXTURE_KEPU, UNIT);
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await awaitReceipt(page, BOOK_KEPU, { 合并: 3 });
    rows = await wordRows(request);
    expect(rows.get('eagle').participates).toBe(false); // 不能静默重新开启
    await page.waitForTimeout(1100); // r0x ids are second-granular
    picked = await practiceSelection();
    expect(picked).not.toContain('eagle');
    expect(picked).toContain('deer');

    // Sticky include: an explicit include_in_scope on the target book is
    // set through the real assign API, then the SAME page re-imports
    // WITHOUT any include choice — the include must survive (import never
    // silently undoes a parent's explicit 加入范围), and the 移出 still
    // gates selection despite it (the AND combination rule).
    const include = await request.post('/api/v1/textbooks/' + kepu.id + '/assign', {
      data: { cardIds: [eagleId], lesson: '第1课', includeInScope: true },
    });
    expect(include.ok()).toBeTruthy();
    await recognize(page, FIXTURE_KEPU, UNIT);
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await awaitReceipt(page, BOOK_KEPU, { 合并: 3 });
    rows = await wordRows(request);
    const kepuAssign = rows.get('eagle').textbooks.find((t) => t.name === BOOK_KEPU);
    expect(kepuAssign.includeInScope).toBe(true); // 粘性保留
    expect(rows.get('eagle').participates).toBe(false); // 移出不受影响
    await page.waitForTimeout(1100);
    picked = await practiceSelection();
    expect(picked).not.toContain('eagle'); // include ≠ 绕过移出
  } finally {
    await deleteUnit(request, UNIT);
  }
});

test('definition diff: unresolved stays 待处理差异 with the original kept; 采用照片释义 confirms, merges the source, and the classroom serves the word', async ({ page, request }) => {
  await deleteUnit(request, UNIT);
  try {
    // Pre-seed camel with a DIFFERENT meaning (an earlier batch add).
    const post = await request.post('/api/v1/words', {
      data: { category: CAT, unit: UNIT, entries: [{ word: 'camel', meaning: '沙漠之舟', example: '' }] },
    });
    expect(post.ok()).toBeTruthy();

    // The photo brings camel with 骆驼 — the conflict shows BOTH readings
    // BEFORE any submit (AC-3: 提交前展示差异).
    await recognize(page, FIXTURE_KEPU, UNIT);
    const conflict = page.locator('.photo-row-conflict');
    await expect(conflict).toHaveCount(1);
    await expect(conflict).toContainText('词库释义：沙漠之舟');
    await expect(conflict.locator('input[value="adopt"]')).toBeVisible();

    // Commit WITHOUT resolving: camel reports 待处理差异, the original
    // meaning stays, and camel gets NO source (未确认不合并不覆盖).
    await page.getByRole('button', { name: '确认添加（2 个）' }).click();
    await awaitReceipt(page, BOOK_KEPU, { 新增: 2, 待处理差异: 1 });
    await expect(page.locator('.result-banner')).toContainText('待处理差异：camel');
    let rows = await wordRows(request);
    expect(rows.get('camel').meaning).toBe('沙漠之舟');
    expect((rows.get('camel').textbooks || []).length).toBe(0);

    // Re-upload: this time the parent confirms 采用照片释义 — the row
    // joins the commit (button count 2 → 3).
    await recognize(page, FIXTURE_KEPU, UNIT);
    const conflict2 = page.locator('.photo-row-conflict');
    await expect(conflict2).toHaveCount(1);
    await conflict2.locator('input[value="adopt"]').check();
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await awaitReceipt(page, BOOK_KEPU, { 合并: 3 });
    await expect(page.locator('.result-banner')).toContainText('采用照片释义更新，已合并来源');

    // The confirmed reading replaced the meaning through the REAL edit
    // path, and the source merged.
    rows = await wordRows(request);
    expect(rows.get('camel').meaning).toBe('骆驼');
    const camelAssign = rows.get('camel').textbooks.find((t) => t.name === BOOK_KEPU);
    expect(camelAssign && camelAssign.lesson).toBe('第1课');

    // AC-7: once the lesson is learned, the real classroom serves the
    // word (selection reads the confirmed cards.meaning).
    const books = await textbooks(request);
    const kepu = books.find((b) => b.name === BOOK_KEPU);
    const put = await request.put('/api/v1/textbooks/' + kepu.id, { data: { learnedLessons: 1 } });
    expect(put.ok()).toBeTruthy();
    await page.waitForTimeout(1100);
    const picked = await practiceSelection();
    expect(picked).toContain('camel');
  } finally {
    await deleteUnit(request, UNIT);
  }
});
