/**
 * Browser equivalents for 13.3 UAT-8/9/10 navigation & negative list
 * (mock stack — npm run e2e:rt). Replaces “must use iPad Safari” with
 * Chromium history/viewport/hit-target checks.
 */
// @ts-check
const { test, expect } = require('@playwright/test');

test('UAT-8/10: /home.html two doors, no gate; / stays degraded', async ({ page }) => {
  await page.goto('/home.html');
  await expect(page.locator('a[href="/tutor/"]')).toHaveText('开始今天的听写');
  await expect(page.locator('a[href="/parent.html"]')).toHaveText('家长：记录与词库');
  expect(await page.locator('input').count()).toBe(0);
  expect(await page.locator('script').count()).toBe(0);

  await page.goto('/');
  await expect(page.locator('#rt-degraded-banner')).toContainText('此页面为降级备用模式');
});

test('UAT-9: parent lessons back-nav keeps fresh list (no stale detail)', async ({ page }) => {
  await page.goto('/parent.html');
  await expect(page.locator('#lessonsSection')).toBeVisible();
  const first = page.locator('.lesson-row').first();
  await expect(first).toBeVisible();
  const id = await first.getAttribute('data-lesson-id');
  await first.click();
  await expect(page.locator('.lesson-detail')).toBeVisible();
  await page.getByRole('button', { name: '返回列表' }).click();
  await expect(page.locator('#lessonsSection')).toBeVisible();
  await expect(page.locator(`.lesson-row[data-lesson-id="${id}"]`)).toBeVisible();

  // History back/forward must not white-screen the workspace.
  await page.goBack();
  await page.goForward();
  await expect(page.locator('#lessonsSection')).toBeVisible({ timeout: 10_000 });
});

test('UAT-9: home doors meet 60px hit target in portrait and landscape', async ({ page }) => {
  for (const viewport of [
    { width: 810, height: 1080 },
    { width: 1080, height: 810 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto('/home.html');
    for (const href of ['/tutor/', '/parent.html']) {
      const box = await page.locator(`a[href="${href}"]`).boundingBox();
      expect(box, href).toBeTruthy();
      expect(box.height, `${href} height @ ${viewport.width}x${viewport.height}`).toBeGreaterThanOrEqual(60);
    }
  }
});

test('UAT-10: parent tabs expose no cut-list entry points', async ({ page }) => {
  await page.goto('/parent.html');
  for (const name of ['学习记录', '词库', '设置']) {
    await page.getByRole('tab', { name }).click();
  }
  const body = await page.locator('body').innerText();
  for (const banned of ['改判', '重评', '趋势统计', '掌握度', '对话回放', '登录', 'PIN']) {
    expect(body, banned).not.toContain(banned);
  }
});
