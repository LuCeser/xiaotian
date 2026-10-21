// smoke.spec.js — the m1 browser line's first B-layer spec (issue #58,
// e2e-test-plan §7 step 5): REAL page + REAL read-only REST against the
// isolated real service. Per plan §2 the only replaced piece is the
// external vendor — so a bare page load and a pure-Go read route must
// produce ZERO stub traffic and ZERO stub violations; any vendor call
// here would already be a leak the strict stub must catch.
'use strict';

const http = require('node:http');
const { test, expect } = require('@playwright/test');

// stubState reads the strict stub's control plane (127.0.0.1:18285, pinned
// by tests/playwright.m1.config.js + config.m1-e2e.yaml).
function stubState() {
  return new Promise((resolve, reject) => {
    http.get('http://127.0.0.1:18285/__strict/state', (res) => {
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => resolve(JSON.parse(Buffer.concat(chunks).toString('utf8'))));
    }).on('error', reject);
  });
}

test('real tutor page loads from the isolated real service', async ({ page }) => {
  await page.goto('/tutor/');
  await expect(page).toHaveTitle(/听说小老师/);
  await expect(page.locator('#rt-status')).toBeVisible();
  await expect(page.locator('#rt-conn')).toHaveAttribute('data-conn', 'idle');
});

test('read-only REST answers on the fresh isolated database', async ({ request }) => {
  const res = await request.get('/api/v1/lessons');
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(Array.isArray(body.lessons)).toBeTruthy(); // lessonsResponse shape
  expect(body.lessons).toHaveLength(0); // fresh temp DB: no seeded rows
});

test('page load makes no vendor traffic and the strict stub stays clean', async ({ page }) => {
  await page.goto('/');
  await page.goto('/parent.html');
  const state = await stubState();
  expect(state.violations).toEqual([]);
  expect(state.requests).toEqual([]);
});
