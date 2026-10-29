/**
 * Parent UAT-gap E2E (GitHub Issues #2–#6): the four observable scenarios the
 * existing suites cannot reach, proven against the ISOLATED real service —
 * real browser → real Go service → real Python bridge → real SQLite (per-run
 * temp DB, SHARED by every test in the run). Only the external recognition
 * service is stubbed (fixture-matched canned replies, the Issue #3 boundary).
 *
 * Why a separate file: the three photo/scope suites are edited by another
 * lane, so these cases live here instead of being appended to them. Every
 * test builds its OWN data (own words, own unique book name) so it passes
 * standalone, and every finally-cleanup deletes only this suite's words
 * (deleteWords is word-scoped; books have no DELETE route by design, so
 * the names are chosen to never collide with another suite's books).
 *
 * Meta-collision note: matchTextbookForMeta preselects an EXISTING book by
 * the recognized meta. The books this suite creates deliberately clear the
 * five optional meta fields (版本/学段/年级/上下册/模块) before submit, so a
 * PEP recognition in another suite can never hijack onto our book — and ours
 * can never hijack onto theirs (a lookalike with no meta matches nothing).
 *
 * Acceptance link, one test per UAT gap:
 *   1. T21 课次缺失的明确处理 — blank batch lesson (= 待确认): Issue #80
 *      AC-06 removed the import-side include override, so the receipt
 *      says 已指定课次待确认 + 暂不自动安排, the classroom keeps the words
 *      out, and the 不指定教材 branch carries its own 补充词汇不要求虚构
 *      课次 honesty note.
 *   2. T18 新建教材在提交时建立 — 新建教材… shows the one-time confirm hint,
 *      prefills the name from the recognized meta, refuses an empty name, and
 *      the book does NOT exist in the real DB until 确认添加 succeeds (there
 *      is no separate create button).
 *   3. T24-b 释义差异：保留词库释义 — a 释义不同 row resolves to 保留词库释义,
 *      the receipt says so, the library meaning survives byte-for-byte, and
 *      the negative half: 无教材模式 never renders the two confirm radios.
 *   4. T15 待确认课次 + 直接加入范围 — the browse 指定教材课次 modal with
 *      待确认（课次不确定） + 不受课次进度限制: the chip carries 待确认 and
 *      its warning title, and the word is admitted into real classroom
 *      selection despite having no lesson and no learned progress.
 *
 * Classroom probes: raw /rt practice session_init (mode=test full-reveal;
 * r0x lesson ids are second-granular, so at most one probe per test).
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const path = require('node:path');
const {
  recognize, deleteWords, textbooks, wordRows, awaitReceipt, practiceSelection,
} = require('./helpers/photo-flow');

const CAT = 'english';
// This suite's whole vocabulary: the PEP fixture's faces plus its own
// REST-seeded words. The textbook-mode commit writes NO unit tag
// (Issue #89), so the exact word list is the suite's isolation key —
// before AND after each test.
const WORDS = ['tiger', 'panda', 'grape', 'uatGapPend1', 'uatGapPend2'];

const FIXTURES = path.resolve(__dirname, 'fixtures');
const FIXTURE_PEP = path.join(FIXTURES, 'photo-textbook-pep.png');

// The PEP header the stub reports (photo-textbook-pep.reply.json). Its
// textbookMetaSummary is what prefills the 新建教材 name field.
const PEP_PREFILL = '人教版PEP·三年级·上册·Unit 3';

// Unique book names — the suite must never touch another lane's books.
const BOOK1 = 'UAT待确认范围';
const BOOK2 = 'UAT提交时建立';
const BOOK3 = 'UAT保留词库释义';
const BOOK4 = 'UAT浏览待确认';

// Case 4's own vocabulary (REST-seeded; not shared with any fixture family).
const PEND_WORDS = ['uatGapPend1', 'uatGapPend2'];

/** Batch-apply one lesson to every checked row through the toolbar. */
async function batchSetLesson(page, lesson) {
  await page.locator('#parent-photo-lesson').fill(lesson);
  await page.getByRole('button', { name: '应用到勾选行' }).click();
}

/**
 * The 新建教材 mini-form is prefilled from the recognized meta. We clear the
 * five optional fields so the created book carries NO meta — see the header
 * note on cross-suite preselect collisions — and keep only the unique name.
 */
async function prepareNewBook(page, name) {
  await page.locator('#parent-photo-tb-name').fill(name);
  for (const key of ['edition', 'stage', 'grade', 'volume', 'module']) {
    await page.locator('#parent-photo-tb-' + key).fill('');
  }
}

/** Open the parent page into the 词库 browse view. */
async function openWordLib(page) {
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await expect(page.locator('#browseSection')).toBeVisible();
}

/** Create one textbook through the real REST path (no UI needed here). */
async function createBook(request, name, lessons) {
  const resp = await request.post('/api/v1/textbooks', {
    data: { name, category: CAT, lessons },
  });
  expect(resp.ok()).toBeTruthy();
  return resp.json();
}

/** Seed words into this suite's unit through the real REST path. */
async function seedWords(request, words, meaning) {
  const resp = await request.post('/api/v1/words', {
    data: {
      category: CAT,
      entries: words.map((w) => ({ word: w, meaning })),
    },
  });
  expect(resp.ok()).toBeTruthy();
}

/** Select words in the browse list, then drive the 指定教材课次 modal. */
async function assignThroughUI(page, words, textbookLabel, lessonLabel, include) {
  // Selection SURVIVES the browse refresh that follows every assign — clear
  // it first, or a later batch would silently re-assign still-checked rows.
  const stale = page.locator('.word-row .word-select:checked');
  while (await stale.count() > 0) {
    await stale.first().uncheck();
  }
  for (const w of words) {
    await page.locator(`.word-row[data-word="${w}"]`)
      .getByRole('checkbox', { name: `选择 ${w}` }).check();
  }
  await page.locator('#bulk-assign-textbook-btn').click();
  const dialog = page.locator('.modal:not(.modal-wide)');
  await expect(dialog).toBeVisible();
  await dialog.locator('#assign-textbook').selectOption({ label: textbookLabel });
  await dialog.locator('#assign-lesson').selectOption({ label: lessonLabel });
  if (include) {
    await dialog.locator('#assign-include').check();
  }
  await dialog.getByRole('button', { name: '确认指定' }).click();
  await expect(dialog).toBeHidden({ timeout: 15_000 });
}

test('T21: blank lesson commits as 课次待确认, out of the classroom until progress moves (AC-06: no import-side 加入范围)', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  try {
    await recognize(page, FIXTURE_PEP);

    // The task's related 补充词汇 note lives on the 不指定教材 branch (not on
    // the textbook path): verify it there, then move to 新建教材 for the
    // actual commit.
    await page.locator('#parent-photo-textbook').selectOption('none');
    await expect(page.locator('.photo-book-detail'))
      .toContainText('补充词汇不要求虚构课次');

    // Back to a fresh textbook for the actual commit.
    await page.locator('#parent-photo-textbook').selectOption('new');

    // Blank batch lesson = 待确认 (the row default inherits the header's
    // 第2课, so it must be cleared explicitly).
    await batchSetLesson(page, '');

    // Issue #80 AC-06: the import offers NO 加入范围 affordance anymore —
    // 待确认 words must wait for progress (加入范围 lives in 词库浏览).
    await expect(page.locator('#parent-photo-include')).toHaveCount(0);
    await expect(page.locator('.photo-state-line'))
      .toContainText('待确认课次的 3 词保存后暂不自动安排');

    // The name goes LAST: 应用到勾选行 re-renders the whole 教材详情 panel,
    // rebuilding #parent-photo-tb-name back to the recognized prefill.
    // Filling it before the batch apply is silently discarded, and the
    // commit then tries to re-create another suite's PEP book.
    await prepareNewBook(page, BOOK1);
    await expect(page.locator('#parent-photo-tb-name')).toHaveValue(BOOK1);

    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await awaitReceipt(page, BOOK1, { 新增: 3 });

    // The group receipt names the 待确认 lesson group — never a 默写范围
    // grant (an import cannot join words into the scope).
    await expect(page.locator('.result-banner'))
      .toContainText('已指定课次待确认：tiger、panda、grape');
    await expect(page.locator('.result-banner'))
      .toContainText('暂不自动安排 3 词（课次待确认）');

    // No progress on the new book and no include flag: the real classroom
    // keeps every one of them out until the textbook page moves progress.
    const picked = await practiceSelection();
    for (const w of ['tiger', 'panda', 'grape']) {
      expect(picked).not.toContain(w);
    }
  } finally {
    await deleteWords(request, WORDS);
  }
});

test('T18: 新建教材 is established only at commit — empty name refused, no book before submit', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  try {
    await recognize(page, FIXTURE_PEP);

    // Explicitly pick 新建教材… (the preselect may already point at another
    // suite's PEP book).
    await page.locator('#parent-photo-textbook').selectOption('new');

    // The one-time confirm hint (source: parent-photo.js:1376).
    await expect(page.locator('.photo-book-detail'))
      .toContainText('确认必要信息建立教材（只需这一次）');

    // The name is prefilled from the recognized meta and stays editable.
    const nameInput = page.locator('#parent-photo-tb-name');
    await expect(nameInput).toHaveValue(PEP_PREFILL);
    await nameInput.fill('UAT临时名');
    await expect(nameInput).toHaveValue('UAT临时名');

    // Empty name → the commit refuses before writing anything (source:
    // parent-photo.js:2063). The error is the photo form's inline surface.
    await nameInput.fill('');
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await expect(page.locator('#parent-photo-form-error'))
      .toContainText('请先填写教材名称');

    // The book does NOT exist in the real DB before a successful submit:
    // there is no separate create button.
    let books = await textbooks(request);
    expect(books.find((b) => b.name === BOOK2)).toBeFalsy();

    // A real name (meta cleared) lets the same confirm establish it.
    await prepareNewBook(page, BOOK2);
    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await awaitReceipt(page, BOOK2, { 新增: 3 });

    books = await textbooks(request);
    const created = books.find((b) => b.name === BOOK2);
    expect(created).toBeTruthy();
    expect(created.lessons).toEqual(['第2课']);
  } finally {
    await deleteWords(request, WORDS);
  }
});

test('T24-b: 保留词库释义 keeps the library meaning, merges the source, and never renders in 无教材模式', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  try {
    // One word already in the library with a DIFFERENT reading than the
    // photo's — the conflict vehicle.
    const seed = await request.post('/api/v1/words', {
      data: {
        category: CAT,
        entries: [{ word: 'tiger', meaning: '词库老虎', example: '' }],
      },
    });
    expect(seed.ok()).toBeTruthy();

    await recognize(page, FIXTURE_PEP);
    const conflict = page.locator('.photo-row-conflict');
    await expect(conflict).toHaveCount(1);
    await expect(conflict).toContainText('词库释义：词库老虎');

    // Negative half: with no textbook chosen the two confirm radios are not
    // rendered at all (supplementary imports have no source to merge — the
    // resolution is textbook-mode-only, source: parent-photo.js:1813).
    await page.locator('#parent-photo-textbook').selectOption('none');
    await expect(page.locator('.photo-conflict-choice')).toHaveCount(0);

    // Textbook mode renders BOTH readings as radios.
    await page.locator('#parent-photo-textbook').selectOption('new');
    await expect(page.locator('.photo-conflict-choice')).toHaveCount(1);
    await expect(page.locator('.photo-conflict-choice input[value="adopt"]')).toBeVisible();
    await prepareNewBook(page, BOOK3);
    await page.locator('.photo-conflict-choice input[value="keep"]').check();

    await page.getByRole('button', { name: '确认添加（3 个）' }).click();
    await awaitReceipt(page, BOOK3, { 新增: 2, 合并: 1 });

    // The receipt names the keep resolution (source: parent-photo.js:2727).
    await expect(page.locator('.result-banner')).toContainText('保留词库释义，仅合并来源');

    // The library meaning survives byte-for-byte and the source merged.
    const rows = await wordRows(request);
    expect(rows.get('tiger').meaning).toBe('词库老虎');
    const assign = (rows.get('tiger').textbooks || []).find((t) => t.name === BOOK3);
    expect(assign && assign.lesson).toBe('第2课');
  } finally {
    await deleteWords(request, WORDS);
  }
});

test('T15: 待确认 + 直接加入范围 through the browse modal admits the word into real selection', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  try {
    // This suite's own words and book (no fixture family shared).
    await seedWords(request, PEND_WORDS, '待确认e2e');
    await createBook(request, BOOK4, ['A课']);

    await openWordLib(page);
    for (const w of PEND_WORDS) {
      await expect(page.locator(`.word-row[data-word="${w}"]`)).toBeVisible();
    }

    // 待确认（课次不确定） + the modal's own include label
    // 不受课次进度限制，直接加入默写范围 (source: parent-textbook.js:612/698).
    await assignThroughUI(page, PEND_WORDS, BOOK4, '待确认（课次不确定）', true);

    // The refreshed chip carries 待确认, its pending styling, and the warning
    // title (source: parent-browse.js:846-852).
    const chip = page.locator(`.word-row[data-word="${PEND_WORDS[0]}"] .textbook-chip`);
    await expect(chip).toBeVisible({ timeout: 15_000 });
    await expect(chip).toContainText('待确认');
    await expect(chip).toHaveClass(/textbook-chip-pending/);
    await expect(chip).toHaveAttribute(
      'title', '课次待确认：暂不自动加入默写范围，确认课次或手动加入后生效');

    // No lesson, no learned progress — include_in_scope alone admits them
    // into the real classroom (the 待确认 path is not progress-gated).
    const picked = await practiceSelection();
    for (const w of PEND_WORDS) {
      expect(picked).toContain(w);
    }
  } finally {
    await deleteWords(request, WORDS);
  }
});

// ---------------------------------------------------------------------------
// Task-text vs source discrepancies found while reading the code (assertions
// above use the SOURCE wording, not the audit's paraphrase):
//
//   - T21 (Issue #80 AC-06): the photo panel's include checkbox is GONE —
//     an import never joins words into 默写范围; the 待确认 group stores
//     includeInScope:false and the classroom keeps it out until progress
//     moves. The browse modal's 不受课次进度限制 override (parent-
//     textbook.js) still exists and is exercised in T15.
//   - T21 补充词汇 note: 「这些词将作为补充词汇加入，默认加入默写范围（补充
//     词汇不要求虚构课次）。」 (parent-photo.js) renders ONLY on the
//     不指定教材 branch, never on the textbook path the task's flow describes.
//     Covered by switching to 不指定教材 in T21 (no assertion skipped).
//   - T21 group note: the actual line is
//     「已指定课次待确认：<words>」 — an import without an explicit
//     include can never write 默写范围 wording.
//   - T18 hint: actual is
//     「确认必要信息建立教材（只需这一次）；识别到的信息已预填，可修改。」
//     (parent-photo.js); the error is
//     「请先填写教材名称（缺失的教材信息只需这一次确认）」(parent-photo.js).
//   - T24-b keep note: 「保留词库释义，仅合并来源」 (parent-photo.js) —
//     matches the audit exactly.
//   - T15 chip title: 「课次待确认：暂不自动加入默写范围，确认课次或手动加入
//     后生效」 (parent-browse.js) — matches the audit exactly.
//
// No assertion was skipped: every control the task describes exists and is
// reachable through the real UI.
// ---------------------------------------------------------------------------
