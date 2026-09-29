---
status: testing
phase: 09-parent-vocabulary-management
source: [09-VERIFICATION.md]
started: 2026-09-30T00:00:00.000Z
updated: 2026-09-30T00:00:00.000Z
---

## Current Test

number: 1
name: Backstop visual re-check (长文本 2 行截断 / 单元名省略号 / 320px 预览滚动)
expected: |
  自动断言已在 commit 730732d/e904f7a 连续两轮全绿（行为式断言：渲染高度 ≤ 2 行行高等）；
  UI-SPEC 将这三处标记 🧪 需人眼确认。截图证据位于 test-results/（backstop-longtext-desktop.png、
  backstop-preview-overflow.png、matrix-{phone,tablet,desktop}.png）。人眼复核：词义 2 行截断、
  单元名省略号 + title 全文、批量预览 320px 内滚动。
awaiting: user response

## Tests

### 1. Backstop visual re-check
expected: 长文本 2 行截断 / 单元名省略号（title 全文）/ 批量预览 320px 上限内滚动 — 人眼确认截图或实机页面
result: [pending]

### 2. Real-device iPad Safari open /parent.html
expected: 家长页在 iPad Safari 真机打开，视觉质量可接受（Chromium 三视口矩阵已绿；本项为 09-01 D5 遗留人工项，可并入 Phase 12 SC2 一并关闭）
result: [pending]

## Summary

total: 2
passed: 0
issues: 0
pending: 2
skipped: 0
blocked: 0

## Gaps
