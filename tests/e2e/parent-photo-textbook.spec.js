/**
 * Parent photo-import textbook E2E (Issue #5): 照片导入自动识别教材与课次
 * 并批量校正, proven against the ISOLATED real service — real browser →
 * real Go service → real Python bridge → real SQLite (per-run temp DB,
 * SHARED by every test in the run). Only the external recognition service
 * is stubbed (fixture-matched canned replies, the Issue #3 boundary).
 *
 * Test-data isolation: the per-run DB keeps textbooks across tests (no
 * DELETE route exists — by design), and the attribution preselect matches
 * EXISTING books by meta. Each test therefore works on its OWN meta
 * family (PEP / 冀教版 / 译林版) so a book another test created can never
 * hijack a preselect. The PEP book is shared by tests 1 and 2 ON PURPOSE
 * (test 2 exercises the existing-book path of a photo import); test 2
 * still passes standalone because its pep recognize would create the book
 * itself when test 1 did not run.
 *
 * Acceptance link (issue #5 AC-6), one test per observable scenario:
 *   1. 错误课次识别被批量修正 — the stub MISREADS the printed lesson
 *      (第2课 for 第1课); the parent batch-corrects (plus one per-row
 *      override), and the CONFIRMED result — not the recognition —
 *      decides the stored attribution (AC-3), with the 待确认 honesty for
 *      the unread 学段 (AC-2) and the create-form prefill (AC-1).
 *   2. 两本教材不串进度 — photos build two textbooks; advancing ONE
 *      book's progress unlocks only that book's words (AC-4 half).
 *   3. 确认后课堂排除未学词 — a second photo into the EXISTING book
 *      (honestly preselected by meta match, AC-1) adds 第2课 words that
 *      stay OUT of real classroom selection until the progress moves
 *      (AC-4); the receipt says so.
 *   4. 一次多选两张照片 (Issue #80) — ONE picker selection, two photos of
 *      the same book: each photo's rows carry their own page lesson, the
 *      确认 appends the missing lesson once at the END of the list
 *      (appendLessons, never the whole list, never learnedLessons), and
 *      NOTHING is written before 确认 (AC-01).
 *
 * Classroom probes: raw /rt practice session_init (mode=test full-reveal;
 * ≥1.1s apart — r0x lesson ids are second-granular, same-second probes
 * replay the first's frozen row).
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const path = require('node:path');
const {
  recognize, deleteWords, textbooks, awaitReceipt, practiceSelection,
} = require('./helpers/photo-flow');

const CAT = 'english';
// This suite's whole vocabulary (every fixture family it imports). The
// textbook-mode commit writes NO unit tag (Issue #89), so the exact word
// list is the suite's isolation key — before AND after each test.
const WORDS = ['tiger', 'panda', 'grape', 'horse', 'sheep', 'cow', 'pig', 'bird', 'frog'];

const FIXTURES = path.resolve(__dirname, 'fixtures');
const FIXTURE_PEP = path.join(FIXTURES, 'photo-textbook-pep.png');
const FIXTURE_JIJIANG = path.join(FIXTURES, 'photo-textbook-jijiang.png');
const FIXTURE_LEXIN = path.join(FIXTURES, 'photo-textbook-lexin.png');
const FIXTURE_LEXIN2 = path.join(FIXTURES, 'photo-textbook-lexin2.png');

const BOOK_PEP = '人教版PEP·三年级·上册·Unit 3';
const BOOK_JIJIANG = '冀教版·五年级·上册·Unit 1';
const BOOK_LEXIN = '译林版·六年级·下册·Unit 5';
const BOOK_LEXIN_DUAL = '译林版·六年级·下册·Unit 5·双图隔离';

/** Real word→assignments snapshot for the english category (the shared
 *  wordRows helper returns whole rows; this suite only reads textbooks). */
async function wordAssignments(request) {
  const resp = await request.get('/api/v1/words?category=' + CAT);
  const data = await resp.json();
  const map = new Map();
  for (const w of data.words || []) {
    map.set(w.word, w.textbooks || []);
  }
  return map;
}

/** Batch-apply one lesson to every checked row through the toolbar. */
async function batchSetLesson(page, lesson) {
  await page.locator('#parent-photo-lesson').fill(lesson);
  await page.getByRole('button', { name: '应用到勾选行' }).click();
}

test('photo textbook: misread lesson is batch-corrected; the CONFIRMED result governs the attribution', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  try {
    await recognize(page, FIXTURE_PEP);

    // AC-2: the read-off-the-photo header shows every field it could read
    // and 待确认 for the one it could not — nothing fabricated.
    // ALL SIX segments must render (版本/学段/年级/上下册/模块/课次),
    // behind the 识别到： prefix (parent-photo.js). The stub's PEP reply has
    // stage=null → 学段 must be 待确认, never invented.
    const meta = page.locator('.photo-textbook-panel .photo-meta-line').first();
    await expect(meta).toContainText('识别到：');
    await expect(meta).toContainText('版本 人教版PEP');
    await expect(meta).toContainText('学段 待确认');
    await expect(meta).toContainText('年级 三年级');
    await expect(meta).toContainText('上下册 上册');
    await expect(meta).toContainText('模块 Unit 3');
    await expect(meta).toContainText('课次 第2课'); // the stub's MISREAD

    // AC-1: no existing book matches the PEP header — Issue #80 §2.2 has
    // the panel STOP at 请选择 once any book exists (suite order leaves
    // 外研版/北师大版 behind), or preselect 新建 on a clean DB. The parent
    // lands on 新建教材 either way, prefilled from the meta.
    const select = page.locator('#parent-photo-textbook');
    if ((await select.inputValue()) === 'unset') {
      await select.selectOption('new');
    }
    await expect(page.locator('#parent-photo-tb-name')).toHaveValue(BOOK_PEP);

    // AC-3: batch-correct the whole page to the lesson actually printed.
    await batchSetLesson(page, '第1课');
    await expect(page.locator('input[aria-label="课次 第 1 行"]')).toHaveValue('第1课');
    // Per-row editing still works: one row moves to another lesson.
    await page.locator('input[aria-label="课次 第 3 行"]').fill('第2课');

    await page.getByRole('button', { name: '确认添加（3 个）' }).click();

    // Honest receipt: per-group assignment lines + the unlearned note.
    const banner = page.locator('.result-banner');
    await awaitReceipt(page, BOOK_PEP, { 新增: 3 });
    await expect(banner).toContainText('已指定课次「第1课」：tiger、panda');
    await expect(banner).toContainText('已指定课次「第2课」：grape');
    await expect(banner).toContainText('尚未学到');

    // The real database carries the CONFIRMED attribution (not the
    // misread), and the created book's lesson list came from the
    // confirmed lessons in row order.
    const books = await textbooks(request);
    const bookPep = books.find((b) => b.name === BOOK_PEP);
    expect(bookPep).toBeTruthy();
    expect(bookPep.lessons).toEqual(['第1课', '第2课']);
    expect(bookPep.edition).toBe('人教版PEP');
    expect(bookPep.grade).toBe('三年级');
    expect(bookPep.volume).toBe('上册');

    const assigns = await wordAssignments(request);
    const tiger = (assigns.get('tiger') || []).find((t) => t.name === BOOK_PEP);
    expect(tiger && tiger.lesson).toBe('第1课');
    const grape = (assigns.get('grape') || []).find((t) => t.name === BOOK_PEP);
    expect(grape && grape.lesson).toBe('第2课');
  } finally {
    await deleteWords(request, WORDS);
  }
});

test('photo textbook: two books built from photos keep independent progress', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  try {
    // Photo 1 → the PEP book (created by test 1 when it ran; when it did
    // not, THIS recognize creates it — the flow works either way, only
    // the preselect differs). Photo 2 → the 冀教版 book, freshly created.
    await recognize(page, FIXTURE_PEP);
    await batchSetLesson(page, '第1课');
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await awaitReceipt(page, BOOK_PEP, { 新增: 3 });

    await recognize(page, FIXTURE_JIJIANG);
    // Books exist (PEP) but none matches 冀教版 — Issue #80 §2.2 stops at
    // 请选择 instead of guessing 新建; the parent explicitly creates the
    // new book, which restores the lesson column.
    const select = page.locator('#parent-photo-textbook');
    await expect(select).toHaveValue('unset');
    await select.selectOption('new');
    // 冀教版's header was read correctly — 第1课 stands, no correction.
    await expect(page.locator('input[aria-label="课次 第 1 行"]')).toHaveValue('第1课');
    await page.getByRole('button', { name: '确认添加（2 个）' }).click();
    await awaitReceipt(page, BOOK_JIJIANG, { 新增: 2 });

    // Both books start unlearned: neither book's words are selectable.
    let picked = await practiceSelection();
    expect(picked).not.toContain('tiger');
    expect(picked).not.toContain('horse');

    // Advance ONLY the 冀教版 book: its words unlock, PEP's stay out.
    const books = await textbooks(request);
    const bookJijiang = books.find((b) => b.name === BOOK_JIJIANG);
    const put = await request.put('/api/v1/textbooks/' + bookJijiang.id, { data: { learnedLessons: 1 } });
    expect(put.ok()).toBeTruthy();

    await page.waitForTimeout(1100); // r0x ids are second-granular
    picked = await practiceSelection();
    expect(picked).toContain('horse');
    expect(picked).toContain('sheep');
    expect(picked).not.toContain('tiger');
    expect(picked).not.toContain('panda');
    expect(picked).not.toContain('grape');

    // Advance the PEP book too: everything unlocks — still independently.
    const bookPep = books.find((b) => b.name === BOOK_PEP);
    const putPep = await request.put('/api/v1/textbooks/' + bookPep.id, { data: { learnedLessons: 1 } });
    expect(putPep.ok()).toBeTruthy();

    await page.waitForTimeout(1100);
    picked = await practiceSelection();
    for (const w of ['tiger', 'panda', 'grape', 'horse', 'sheep']) {
      expect(picked).toContain(w);
    }
  } finally {
    await deleteWords(request, WORDS);
  }
});

test('photo textbook: confirmed-but-unlearned lessons stay out of the classroom until progress moves; a re-photo honestly preselects the existing book', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  try {
    // Photo 1 → the 译林版 book with 第1课, then mark 第1课 learned.
    await recognize(page, FIXTURE_LEXIN);
    // In suite order other books exist and none matches 译林版 — the
    // panel stops at 请选择 (Issue #80 §2.2); standalone it preselects
    // 新建 on the clean DB. Either way the parent lands on 新建教材.
    const freshSelect = page.locator('#parent-photo-textbook');
    if ((await freshSelect.inputValue()) === 'unset') {
      await freshSelect.selectOption('new');
    }
    await page.getByRole('button', { name: '确认添加（2 个）' }).click();
    await awaitReceipt(page, BOOK_LEXIN, { 新增: 2 });
    const books1 = await textbooks(request);
    const bookLexin = books1.find((b) => b.name === BOOK_LEXIN);
    const put = await request.put('/api/v1/textbooks/' + bookLexin.id, { data: { learnedLessons: 1 } });
    expect(put.ok()).toBeTruthy();

    // Photo 2 (same book, next lesson): the meta HONESTLY matches the
    // existing book — it is preselected, no re-asking of the background
    // (AC-2's 只按教材确认一次).
    await recognize(page, FIXTURE_LEXIN2);
    const select = page.locator('#parent-photo-textbook');
    await expect(select).not.toHaveValue('new');
    await expect(select).not.toHaveValue('none');
    // The confirmed lesson is not in the book's list yet: the panel says
    // it will append at the end (order editable in 教材管理 later).
    await expect(page.locator('.photo-book-detail')).toContainText('第2课」不在教材课次表里');

    await page.getByRole('button', { name: '确认添加（2 个）' }).click();
    await awaitReceipt(page, BOOK_LEXIN, { 新增: 2 });
    // The unlearned-lesson honesty note names exactly 第2课.
    await expect(page.locator('.result-banner')).toContainText('课次「第2课」尚未学到');

    // AC-4: the classroom keeps excluding the unlearned lesson's words…
    let picked = await practiceSelection();
    expect(picked).toContain('cow');
    expect(picked).toContain('pig');
    expect(picked).not.toContain('bird');
    expect(picked).not.toContain('frog');

    // …and the appended lesson joined the book's list in order.
    const books2 = await textbooks(request);
    expect(books2.find((b) => b.name === BOOK_LEXIN).lessons).toEqual(['第1课', '第2课']);

    // Progress moves → the very next classroom serves them.
    const put2 = await request.put('/api/v1/textbooks/' + bookLexin.id, { data: { learnedLessons: 2 } });
    expect(put2.ok()).toBeTruthy();
    await page.waitForTimeout(1100);
    picked = await practiceSelection();
    expect(picked).toContain('bird');
    expect(picked).toContain('frog');
  } finally {
    await deleteWords(request, WORDS);
  }
});

test('photo textbook: ONE pick of two photos — lessons append once in order, progress untouched, nothing written before 确认 (Issue #80 §2.3/§2.4)', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  // Independent setup: the 译林版 book already exists with 第1课 learned,
  // so this test always exercises appendLessons — never suite-order luck.
  const create = await request.post('/api/v1/textbooks', {
    data: {
      name: BOOK_LEXIN_DUAL,
      category: CAT,
      edition: '译林版',
      grade: '六年级',
      volume: '下册',
      module: 'Unit 5',
      lessons: ['第1课'],
    },
  });
  expect(create.ok()).toBeTruthy();
  const created = await create.json();
  const putProgress = await request.put(`/api/v1/textbooks/${created.id}`, {
    data: { learnedLessons: 1 },
  });
  expect(putProgress.ok()).toBeTruthy();
  const textbookWrites = [];
  page.on('request', (req) => {
    if (req.method() === 'PUT' && /\/api\/v1\/textbooks\/\d+$/.test(req.url())) {
      textbookWrites.push(req.postData() || '');
    }
  });
  try {
    // One picker selection, TWO fixture photos (Issue #80 §2.3): each
    // photo walks its own recognize; the draft merges in photo order and
    // each photo's rows carry that photo's page lesson.
    await recognize(page, [FIXTURE_LEXIN, FIXTURE_LEXIN2]);
    const dualBook = (await textbooks(request)).find((b) => b.name === BOOK_LEXIN_DUAL);
    expect(dualBook).toBeTruthy();
    const select = page.locator('#parent-photo-textbook');
    await select.selectOption(String(dualBook.id));
    const bookId = String(dualBook.id);
    await expect(bookId).not.toBe('unset');
    await expect(page.locator('input[aria-label="课次 第 1 行"]')).toHaveValue('第1课');
    await expect(page.locator('input[aria-label="课次 第 3 行"]')).toHaveValue('第2课');
    const commitBtn = page.getByRole('button', { name: '确认添加（4 个）' });
    await expect(commitBtn).toBeEnabled();

    // AC-01 未按确认不写库：词库没有这四个词，教材名单与进度原样。
    const wordsBefore = await wordAssignments(request);
    expect(wordsBefore.get('cow')).toBeFalsy();
    expect(wordsBefore.get('bird')).toBeFalsy();
    const booksBefore = await textbooks(request);
    const lexinBefore = booksBefore.find((b) => b.name === BOOK_LEXIN_DUAL);
    expect(lexinBefore).toBeTruthy();
    expect(lexinBefore.lessons).toEqual(['第1课']);
    const learnedBefore = lexinBefore.learnedLessons;

    await commitBtn.click();
    await awaitReceipt(page, BOOK_LEXIN_DUAL, { 新增: 4 });

    // The append-only invariants (§2.1): both lessons present exactly
    // once, in photo order at the END of the list, progress unchanged.
    const booksAfter = await textbooks(request);
    const lexinAfter = booksAfter.find((b) => b.name === BOOK_LEXIN_DUAL);
    expect(lexinAfter).toBeTruthy();
    expect(lexinAfter.lessons).toEqual(['第1课', '第2课']);
    expect(lexinAfter.lessons.indexOf('第1课'))
      .toBe(lexinAfter.lessons.lastIndexOf('第1课'));
    expect(lexinAfter.learnedLessons).toBe(learnedBefore);

    // No import write ever carries the whole list or the progress (§2.4);
    // an existing book's missing names ride appendLessons alone.
    for (const body of textbookWrites) {
      expect(body.includes('learnedLessons')).toBe(false);
      expect(body.includes('"lessons"')).toBe(false);
    }
    const appends = textbookWrites.filter((b) => b.includes('appendLessons'));
    expect(appends.length).toBe(1);
    expect(JSON.parse(appends[0]).appendLessons).toEqual(['第2课']);

    // The stored attribution follows each photo's own lesson.
    const assigns = await wordAssignments(request);
    expect(((assigns.get('cow') || []).find((t) => t.name === BOOK_LEXIN_DUAL) || {}).lesson).toBe('第1课');
    expect(((assigns.get('frog') || []).find((t) => t.name === BOOK_LEXIN_DUAL) || {}).lesson).toBe('第2课');
  } finally {
    await deleteWords(request, WORDS);
  }
});
