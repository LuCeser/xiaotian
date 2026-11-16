const { test, expect } = require('@playwright/test');

const unit = 'e2e-phrase-examples';
async function words(request) {
  const response = await request.get('/api/v1/words?unit=' + unit);
  expect(response.ok()).toBeTruthy();
  return (await response.json()).words;
}
test.beforeEach(async ({ request }) => {
  await fixture(request, { 'give ... to ...': 'I give a book to Tom.' });
  await request.delete('/api/v1/words', { data: { category: 'english', unit } });
});

test('photo import keeps supplied examples and generates with the confirmed textbook background', async ({ page, request }) => {
  const { recognize, deleteWords, wordRows } = require('./helpers/photo-flow');
  const path = require('node:path');
  const targets = ['give ... to ...', 'look after'];
  await deleteWords(request, targets);
  // A historical blank must receive the material example on reimport.
  await add(request, [{ word: targets[1], meaning: '照顾' }]);
  await fixture(request, { 'give ... to ...': 'I give a book to Tom.' }, {
    photoReply: {
      textbook: { edition: '例句验证版', grade: '四年级', lesson: 'Unit 1' },
      entries: [
        { word: targets[0], meaning: '把某物给某人', lesson: 'Unit 1' },
        { word: targets[1], meaning: '照顾', example: '  I look after Tom.  ', lesson: 'Unit 1' },
      ],
    },
  });
  try {
    await recognize(page, path.join(__dirname, 'fixtures/photo-wordlist.png'));
    await page.getByRole('button', { name: '确认添加（2 个）', exact: true }).click();
    await expect.poll(async () => (await wordRows(request)).get(targets[0])?.example).toBe('I give a book to Tom.');
    expect((await wordRows(request)).get(targets[1]).example).toBe('  I look after Tom.  ');
    const inputs = (await state(request)).inputs;
    expect(inputs).toHaveLength(1);
    expect(inputs[0].context.textbook.edition).toBe('例句验证版');
    expect(inputs[0].context.textbook.grade).toBe('四年级');
    expect(inputs[0].meaning).toBe('把某物给某人');
    await expect(page.locator('.result-banner .result-summary')).toContainText('新增 1 个，合并 1 个');

    await fixture(request, { 'be ...': 'I want to be a doctor.' }, {
      photoReply: { textbook: null, entries: [{ word: 'be ...', meaning: '成为' }] },
    });
    await page.locator('#parent-photo-file').setInputFiles(path.join(__dirname, 'fixtures/photo-wordlist.png'));
    await page.getByRole('button', { name: '开始识别', exact: true }).click();
    await expect(page.locator('.photo-row')).toHaveCount(1);
    await page.locator('#parent-photo-textbook').selectOption('none');
    await page.getByRole('button', { name: '确认添加（1 个）', exact: true }).click();
    await expect.poll(async () => (await wordRows(request)).get('be ...')?.example).toBe('I want to be a doctor.');
    expect((await state(request)).inputs[0].context.textbook.grade).toBe('');
    expect((await state(request)).inputs[0].context.learnedLessons).toBeNull();
  } finally {
    await deleteWords(request, targets.concat(['be ...']));
  }
});

for (const mutation of ['example', 'meaning restored', 'word', 'delete and recreate']) {
  test('late generation does not overwrite target after ' + mutation, async ({ request }) => {
    await fixture(request, { 'give ... to ...': 'I give a book to Tom.' }, { delayMs: 600 });
    const pending = add(request, [{ word: 'give ... to ...', meaning: '把某物给某人' }]);
    await expect.poll(async () => (await state(request)).calls).toBe(1);
    const original = (await words(request))[0];
    if (mutation === 'delete and recreate') {
      await request.delete('/api/v1/words', { data: { category: 'english', unit } });
      await add(request, [{ word: original.word, meaning: original.meaning, example: 'I give a pen to Amy.' }]);
    } else {
      const change = { word: original.word };
      if (mutation === 'example') change.example = 'I give a pen to Amy.';
      if (mutation === 'word') change.word = 'give gifts';
      if (mutation === 'meaning restored') change.meaning = '给';
      expect((await request.put('/api/v1/words/' + original.id, { data: change })).ok()).toBeTruthy();
      if (mutation === 'meaning restored') {
        await request.put('/api/v1/words/' + original.id, { data: { word: original.word, meaning: original.meaning } });
      }
    }
    const result = await pending;
    expect(result.examples.saved).toBe(0);
    expect(result.examples.missing[0].reason).toBe('target_changed');
    expect((await words(request))[0].example).toBe(mutation.includes('example') || mutation.includes('recreate') ? 'I give a pen to Amy.' : '');
  });
}

test('timeout keeps the word, reports the gap, and a later retry persists once', async ({ request }) => {
  await fixture(request, { 'give ... to ...': 'I give a book to Tom.' }, { delayMs: 10_000 });
  const result = await add(request, [{ word: 'give ... to ...', meaning: '把某物给某人' }]);
  expect(result.added).toBe(1);
  expect(result.examples.saved).toBe(0);
  expect(result.examples.missing[0].reason).toBe('provider_failed');
  const card = (await words(request))[0];
  expect(card.example).toBe('');
  await fixture(request, { 'give ... to ...': 'I give a book to Tom.' });
  const fill = await request.post('/api/v1/words/examples/fill', { data: { ids: [card.id] } });
  expect((await fill.json()).saved).toBe(1);
  expect((await words(request))[0].example).toBe('I give a book to Tom.');
  const again = await request.post('/api/v1/words/examples/fill', { data: { ids: [card.id] } });
  expect((await again.json()).skipped).toBe(1);
  expect((await words(request))).toHaveLength(1);
});

test('generation has a batch bound and remaining blanks can be explicitly filled', async ({ request }) => {
  const names = ['Tom', 'Amy', 'Sam', 'Ben', 'Tim', 'Dan', 'Ann', 'Sue', 'Bob', 'Jim', 'Max'];
  const examples = Object.fromEntries(names.map(name => ['look after ' + name, 'I look after ' + name + '.']));
  await fixture(request, examples);
  const result = await add(request, names.map(name => ({ word: 'look after ' + name, meaning: '照顾' })));
  expect(result.added).toBe(11);
  expect(result.examples.saved).toBe(10);
  expect(result.examples.missing).toHaveLength(1);
  expect(result.examples.missing[0].reason).toBe('batch_limit');
  expect((await state(request)).inputs).toHaveLength(10);
  const fill = await request.post('/api/v1/words/examples/fill', { data: { ids: [result.examples.missing[0].id] } });
  expect((await fill.json()).saved).toBe(1);
  expect((await words(request)).every(x => x.example)).toBeTruthy();
  const tooMany = await request.post('/api/v1/words/examples/fill', { data: { ids: (await words(request)).map(x => x.id) } });
  expect(tooMany.status()).toBe(400);
});

test('invalid generation reports a gap on the page while preserving the saved word', async ({ page, request }) => {
  await fixture(request, {}, { invalidJSON: true });
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库', exact: true }).click();
  await page.locator('#parent-word').fill('give ... to ...');
  await page.locator('#parent-meaning').fill('把某物给某人');
  await page.getByRole('radio', { name: '英语', exact: true }).check();
  await page.locator('#parent-unit').fill(unit);
  await page.getByRole('button', { name: '添加单词', exact: true }).click();
  await expect(page.locator('#phrase-example-result')).toContainText('词条已保存，1 个词组例句未补齐');
  expect((await words(request))[0].example).toBe('');
});

test('ordinary words, complete sentences and Chinese content do not generate examples', async ({ request }) => {
  await add(request, [{ word: 'apple', meaning: '苹果' }, { word: 'I like cats.', meaning: '我喜欢猫' }]);
  const chinese = await request.post('/api/v1/words', { data: { category: 'chinese', unit, entries: [{ word: '山川', meaning: '山与河流' }] } });
  expect(chinese.ok()).toBeTruthy();
  expect((await state(request)).calls).toBe(0);
  await request.delete('/api/v1/words', { data: { category: 'chinese', unit } });
});

test('whitespace-only examples are missing and can be generated or filled by supplied text', async ({ request }) => {
  const result = await add(request, [{ word: 'give ... to ...', meaning: '把某物给某人', example: ' \n\t' }]);
  expect(result.examples.saved).toBe(1);
  expect((await words(request))[0].example).toBe('I give a book to Tom.');
  await request.put('/api/v1/words/' + (await words(request))[0].id, {
    data: { word: 'give ... to ...', example: '\n\t' },
  });
  const again = await add(request, [{ word: 'give ... to ...', meaning: '把某物给某人', example: '  I give a pen to Amy.  ' }]);
  expect(again.skipped).toBe(1);
  expect((await words(request))[0].example).toBe('  I give a pen to Amy.  ');
  expect((await words(request))[0].example_source).toBe('material');
});

for (const sentence of ['I give to Tom.', 'I to Tom give a book.', 'I forgive a book to Tom.', 'I give a book to Tom. I smile.', 'I give a book to Tom…', 'I give a book to Tom.中文']) {
  test('rejects invalid fixed fragments or sentence structure: ' + sentence, async ({ request }) => {
    await fixture(request, { 'give ... to ...': sentence });
    const result = await add(request, [{ word: 'give ... to ...', meaning: '把某物给某人' }]);
    expect(result.examples.saved).toBe(0);
    expect(result.examples.missing[0].reason).toBe('invalid_output');
    expect((await words(request))[0].example).toBe('');
  });
}
test.afterEach(async ({ request }) => {
  await request.delete('/api/v1/words', { data: { category: 'english', unit } });
});

async function fixture(request, examples, extra = {}) {
  await request.post('http://127.0.0.1:18180/phrase-fixture', { data: { examples, ...extra } });
}
async function state(request) {
  return (await request.get('http://127.0.0.1:18180/phrase-fixture')).json();
}
async function add(request, entries, extra = {}) {
  const response = await request.post('/api/v1/words', { data: { category: 'english', unit, entries, ...extra } });
  expect(response.ok(), await response.text()).toBeTruthy();
  return response.json();
}

test('partial generation preserves originals, reports missing and retries historical blanks only', async ({ page, request }) => {
  await fixture(request, {
    'give ... to ...': 'I give a book to Tom.',
    'look after': 'I look ... after Tom.', // invalid placeholder
  });
  const entries = [
    { word: 'give ... to ...', meaning: '把某物给某人' },
    { word: 'look after', meaning: '照顾' },
    { word: 'ice cream', meaning: '冰激凌', example: '  I like ice cream.  ' },
    { word: 'take care', meaning: '' },
    { word: 'apple', meaning: '苹果' },
    { word: 'I like cats.', meaning: '我喜欢猫' },
  ];
  const result = await add(request, entries);
  expect(result.added).toBe(6);
  expect(result.examples.saved).toBe(1);
  expect(result.examples.missing.map(x => x.reason).sort()).toEqual(['invalid_output', 'no_meaning']);
  const initial = await words(request);
  expect(initial.find(x => x.word === 'ice cream').example).toBe('  I like ice cream.  ');
  expect(initial.find(x => x.word === 'ice cream').example_source).toBe('material');
  expect((await state(request)).inputs.map(x => x.word)).toEqual(['give ... to ...', 'look after']);

  await fixture(request, { 'look after': 'I look after my cat.' });
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库', exact: true }).click();
  await page.getByRole('textbox', { name: '搜索单词或意思' }).fill('look after');
  await expect(page.locator('.word-row')).toHaveCount(1);
  await page.getByRole('button', { name: '补充缺失例句', exact: true }).click();
  await expect.poll(async () => (await words(request)).find(x => x.word === 'look after').example).toBe('I look after my cat.');
  expect((await state(request)).inputs.map(x => x.word)).toEqual(['look after']);
  const again = await add(request, entries);
  expect(again.added).toBe(0);
  expect(again.skipped).toBe(6);
  expect(again.examples.saved).toBe(0);
  expect((await words(request)).length).toBe(6);
  expect((await state(request)).calls).toBe(1);
  const repeat = await request.post('/api/v1/words/examples/fill', { data: { ids: initial.filter(x => x.example).map(x => x.id) } });
  expect((await repeat.json()).saved).toBe(0);
  expect((await state(request)).calls).toBe(1);
});

test('phrase added by parent retains a generated example after reopening and editing', async ({ page, request }) => {
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库', exact: true }).click();
  await page.locator('#parent-word').fill('give ... to ...');
  await page.locator('#parent-meaning').fill('把某物给某人');
  await page.getByRole('radio', { name: '英语', exact: true }).check();
  await page.locator('#parent-unit').fill(unit);
  await page.getByRole('button', { name: '添加单词', exact: true }).click();
  await expect.poll(async () => (await words(request))[0]?.example).toBe('I give a book to Tom.');
  await page.reload();
  await page.getByRole('tab', { name: '词库', exact: true }).click();
  const row = page.locator('[data-word="give ... to ..."]');
  await expect(row).toContainText('I give a book to Tom.');
  await expect(row).toContainText('系统生成');
  await row.getByRole('button', { name: '编辑', exact: true }).click();
  await page.getByRole('dialog').getByRole('textbox', { name: '例句', exact: true }).fill('I give a pen to Amy.');
  await page.getByRole('dialog').getByRole('button', { name: '保存修改', exact: true }).click();
  await expect.poll(async () => (await words(request))[0]?.example).toBe('I give a pen to Amy.');
});
