const { test, expect } = require('@playwright/test');

const quiet = 'interrupt_rms=99&interrupt_sustain_ms=999999';

test('answers stay hidden until every word is dictated', async ({ page }) => {
  await page.goto('/tutor/?' + quiet + '&mock_step_ms=500');
  await page.click('#rt-start');
  await expect(page.locator('#rt-words .rt-word-current')).toHaveText('2');
  await expect(page.locator('#rt-words .rt-word-chip').nth(0)).toHaveText('1');
  await expect(page.locator('#rt-check')).toBeVisible();
  await expect(page.locator('#rt-words .rt-word-chip')).toHaveText(['apple', 'banana', 'cat']);
});

test('clicking a numbered word re-reads it without moving progress', async ({ page }) => {
  await page.goto('/tutor/?' + quiet + '&mock_step_ms=5000');
  await page.click('#rt-start');
  await expect(page.locator('#rt-status')).toHaveAttribute('data-state', 'listening');
  const first = page.locator('#rt-words button').nth(0);
  await expect(first).toHaveText('1');
  await expect(page.locator('#rt-words button').nth(1)).toBeDisabled();
  await first.click();
  await expect(page.locator('#rt-trace .rt-trace-teacher').last()).toContainText('再读');
  await expect(page.locator('#rt-words .rt-word-current')).toHaveText('1');
  await expect(page.locator('#rt-words .rt-word-chip')).toHaveText(['1', '2', '3']);
  await expect(page.locator('#rt-error')).toBeHidden();
});

test('clicking a completed number interrupts speech without revealing the answer', async ({ page }) => {
  await page.goto('/tutor/?' + quiet + '&mock_step_ms=3000');
  await page.click('#rt-start');
  await expect(page.locator('#rt-words .rt-word-current')).toHaveText('2');
  await page.locator('#rt-words button').nth(0).click();
  await expect(page.locator('#rt-trace .rt-trace-teacher').last()).toContainText('再读');
  await expect(page.locator('#rt-trace .rt-trace-teacher').last()).toContainText('第1个词');
  await expect(page.locator('#rt-words .rt-word-chip')).toHaveText(['1', '2', '3']);
  await expect(page.locator('#rt-words .rt-word-current')).toHaveText('2');
  await expect(page.locator('#rt-error')).toBeHidden();
});

test('natural completion keeps the course-ended status after socket close', async ({ page }) => {
  await page.goto('/tutor/?' + quiet + '&mock_step_ms=100');
  await page.click('#rt-start');
  await expect(page.locator('#rt-check')).toBeVisible();
  for (const row of await page.locator('#rt-check .rt-check-row').all()) {
    await row.getByRole('button', { name: '对', exact: true }).click();
  }
  await page.locator('.rt-check-submit').click();
  await expect(page.locator('#rt-status')).toHaveAttribute('data-state', 'ended');
  await expect.poll(() => page.evaluate(() => window.__rtTutor.client.ws.readyState)).toBe(3);
  await expect(page.locator('#rt-conn-text')).toHaveText('课程已结束');
  await expect.poll(() => page.evaluate(() => window.__rtTutor.capture._stream)).toBe(null);
});
