---
phase: 9
slug: parent-vocabulary-management
# status lifecycle: draft (seeded by plan-phase) → validated (set by validate-phase §6)
# audit-milestone §5.5 distinguishes NOT-VALIDATED (draft) from PARTIAL (validated + nyquist_compliant: false) (#2117)
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-09-27
---

# Phase 9 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (unit + `-tags=e2e` real-Python) + plain node test scripts + Playwright |
| **Config file** | none needed (Makefile targets; playwright.config.js at repo root) |
| **Quick run command** | `go test -v -short ./... && npm test` |
| **Full suite command** | `go test -race -v ./... && npm test && npx playwright test && make py-e2e` |
| **Estimated runtime** | ~120–300 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test -v -short ./... && npm test`
- **After every plan wave:** Run the full suite command
- **Before `/gsd-verify-work`:** Full suite must be green
- **Max feedback latency:** ~300 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 9-01-01 | 01 | 1 | VOC-01, WEB-01 | T-09-01/03/04 | validation caps + argv-slice exec + textContent render | e2e (browser) + build | `npx playwright test tests/e2e/parent-management.spec.js -g tracer ; go build ./...` | ❌ W0 (created by task) | ⬜ pending |
| 9-01-02 | 01 | 1 | VOC-01, WEB-01 | T-09-04 | body caps + DisallowUnknownFields | unit | `go test ./pkg/pybridge/ ./pkg/web/api/ ./pkg/database/ -short -v` | ❌ W0 | ⬜ pending |
| 9-02-01 | 02 | 1 | VOC-01 | T-09-09 | edit never touches fsrs_card | integration (real Python) | `go test -tags=e2e -count=1 -run TestE2E_VocabEdit ./pkg/pybridge/ -v` | ❌ W0 | ⬜ pending |
| 9-02-02 | 02 | 1 | VOC-03 | T-09-06 | invalid config falls back + warns, never crashes | integration (real Python) | `go test -tags=e2e -count=1 -run TestE2E_FSRSConfig ./pkg/pybridge/ -v` | ❌ W0 | ⬜ pending |
| 9-03-01 | 03 | 2 | VOC-01, VOC-02 | T-09-05 | --confirm structurally guaranteed in argv | unit | `go test ./pkg/pybridge/ -short -v -run 'TestEditArgs|TestRenameUnitArgs|TestDeleteWordsArgs'` | ❌ W0 | ⬜ pending |
| 9-03-02 | 03 | 2 | VOC-01, VOC-02 | T-09-05/07 | ExitError.Stdout surfaces diagnostics | unit + integration | `go test ./pkg/pybridge/ -short -v ; go test -tags=e2e -count=1 -run 'TestE2E_Run' ./pkg/pybridge/ -v` | ❌ W0 | ⬜ pending |
| 9-04-01 | 04 | 2 | VOC-02 | T-09-02 | parameterized GROUP BY only | unit (in-memory sqlite) | `go test ./pkg/database/ -short -v -run 'TestCountByUnit|TestCardRepoList'` | ❌ W0 | ⬜ pending |
| 9-04-02 | 04 | 2 | VOC-02 | T-09-02 | category enum check before repo | unit (fakes) | `go test ./pkg/web/api/ -short -v -run TestListUnits` | ❌ W0 | ⬜ pending |
| 9-05-01 | 05 | 3 | VOC-01, VOC-02 | T-09-07 | not_found mapping, bounded messages | unit | `go test ./pkg/web/api/ -short -v -run 'TestEditWord|TestRenameUnit'` | ❌ W0 | ⬜ pending |
| 9-05-02 | 05 | 3 | VOC-02 | T-09-05 | XOR target + always-confirmed delete | unit | `go test ./pkg/web/api/ -short -v -run TestDeleteWords` | ❌ W0 | ⬜ pending |
| 9-05-03 | 05 | 3 | VOC-03 | T-09-06/10 | bounds validation + atomic write | unit | `go test ./pkg/web/api/ -short -v -run 'TestGetFSRS|TestPutFSRS'` | ❌ W0 | ⬜ pending |
| 9-06-01 | 06 | 3 | VOC-01 | — | N/A (pure parser) | unit (node) | `node tests/test-parent-batch-parser.js ; npm test` | ❌ W0 | ⬜ pending |
| 9-06-02 | 06 | 3 | VOC-01, WEB-01 | T-09-01 | node-based DOM construction only (negative grep) | unit (node) + manual smoke | `npm test ; if (rg -q 'innerHTML' pkg/web/static/js/parent-*.js) { 'FAIL: innerHTML found'; exit 1 } else { 'clean: no innerHTML' }` | ❌ W0 | ⬜ pending |
| 9-07-01 | 07 | 4 | VOC-02, WEB-01 | T-09-01 | textContent rendering (negative grep) | unit (node) + manual smoke | `npm test ; if (rg -q 'innerHTML' pkg/web/static/js/parent-browse.js) { 'FAIL: innerHTML found'; exit 1 } else { 'clean: no innerHTML' }` | ❌ W0 | ⬜ pending |
| 9-07-02 | 07 | 4 | VOC-02 | T-09-05 | count-showing destructive confirms | manual smoke + grep copy | `rg -c '确认删除' pkg/web/static/js/parent-browse.js` | ❌ W0 | ⬜ pending |
| 9-07-03 | 07 | 4 | VOC-03, WEB-01 | T-09-01 | clamped slider values before send | manual smoke + grep | `rg -c '已保存，下次听写生效' pkg/web/static/js/parent-modes.js` | ❌ W0 | ⬜ pending |
| 9-08-01 | 08 | 5 | WEB-01, VOC-01..03 | T-09-12 | e2e data confined to e2e-* units + cleanup | e2e (browser, real service) | `npx playwright test tests/e2e/parent-management.spec.js` | ✅ (from 9-01-01) | ⬜ pending |
| 9-08-02 | 08 | 5 | WEB-01 | T-09-12 | viewport matrix + backstop screenshots + phase gate | e2e + suite gate | `npx playwright test tests/e2e/parent-management.spec.js ; go test -short ./... ; npm test` | ✅ | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠ flaky*

---

## Wave 0 Requirements

All test files are created as the FIRST action of their owning task (tdd tasks
write the failing test before implementation) — no separate Wave 0 scaffold is
needed. RESEARCH Wave 0 gap list maps: vocab_mgmt_test.go → 9-03-01;
handlers_test.go / fsrs_config_test.go → 9-01-02/9-05-*; card_repo_query_test.go
→ 9-01-02/9-04-01; tests/test-parent-batch-parser.js → 9-06-01;
parent-management.spec.js → 9-01-01 (tracer) + 9-08-01 (full). Python edit
coverage extends the e2e-tagged pybridge suite (9-02-01) rather than standing
up pytest — no Python test infra is introduced.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Batch-paste a real textbook lesson (10–20 Chinese words) closes UAT gap C4 | VOC-01, D-06 | Real 课文 content + family workflow | On the local service: 批量添加 → paste lesson → preview → commit → confirm words appear grouped under the lesson unit in the 中文 filter |
| Backstop rows: long-text clamping + overflow scroll appearance | WEB-01 | Visual judgment beyond computed-style asserts | Review the 9-08-02 screenshots (over-length meaning/unit fixtures at 3 viewports); confirm 2-line clamp + ellipsis read naturally |
| Slider feel + readout clarity for a non-technical parent | VOC-03, D-15 | Subjective readability | Drag both sliders; confirm hints/当前/默认 readouts and the 只影响以后的复习安排 note are understandable without explanation |
| Shared-iPad touch targets | WEB-01 | Real-device touch feel | On the iPad: open /parent.html, verify every button/pill/input comfortably tappable (≥44px) in Safari |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < {N}s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
