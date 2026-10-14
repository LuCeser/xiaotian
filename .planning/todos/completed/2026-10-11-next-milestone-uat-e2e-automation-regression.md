---
created: 2026-10-11T01:37:08.793Z
title: "下个里程碑总纲：验收项全量端到端自动化＋本地完整回归"
area: planning
severity: major
files:
  - .planning/ROADMAP.md:294
  - .planning/phases/13.3-parent-tutor-fusion/deferred-items.md
  - docs/E2E-ISOLATION-SPEC.md
  - tests/e2e-rt/real/
---

## Problem

下一个里程碑的核心目标（用户 2026-10-11 登记）：把所有在验收（UAT）阶段能够由自动化执行的部分，实现成自动化验证脚本，从而在本地做端到端的自动化验证——每次有新修改，就能完整跑一遍回归，不靠手工验收或事后善后。

背景与已有决议：

- 2026-10-10（13.3 真机 UAT 会话）用户已拍板：UAT 验收项都应做成端到端验证——可自动化主体并入四层 E2E 体系的 L3（隔离实例＋合成客户端），iPad 物理项（bfcache/触控/音频体感）留 L4 人工。已登记于 ROADMAP.md:294（deferred 项）与 13.3 deferred-items.md，标注「待提升 backlog」。
- 本条是上述决议的**里程碑级总纲**：范围从「13.3 十项真机用例」扩大到「全部验收阶段的可自动化项」，并新增「每次修改后本地完整回归」的目标形态。规划下个里程碑时应把 ROADMAP.md:294 那条作为子集合并进来，一并兑现「待提升 backlog」。
- 四层体系与资产现状见 docs/E2E-ISOLATION-SPEC.md：L1 Go 集成（pkg/rt/turn_integration_test.go）、L2 UI vs mock（tests/e2e-rt/）、L3 真实服务合成客户端已有一套样板（tests/e2e-rt/real/ 五件套：config.e2e.yaml / seed-words.json / WAV 素材 / driver-rt-lesson.mjs / run-real.ps1，＋ `npm run e2e:rt-real` 一键隔离实跑，含净零生命周期与硬断言矩阵）、L4 人工认证。

约束：用户在 Windows 本地开发调试（项目约定：代码必须能在 Windows 本地跑通），回归脚本须 Windows 可执行（现有 run-real.ps1 即 PowerShell 范式）。

## Solution

里程碑规划时的展开路径（作为 /gsd-new-milestone 或下阶段规划的输入）：

1. **盘点全部验收项**：各 phase 的 UAT 清单（如 13.3-UAT.md 十项）、VERIFICATION.md、deferred-items.md，逐项标注「可自动化 → L3」或「物理项 → L4 人工」，形成自动化覆盖率矩阵。
2. **沿 tests/e2e-rt/real/ 范式资产化扩展 L3**：隔离实例（独立端口＋独立 db＋钉死 `rt.lessonId=e2e-*`）＋播种固定词表＋净零生命周期（finally 清理、幂等）＋断言矩阵固化＋driver/fixtures/配置模板入库。
3. **目标形态**：本地一条命令（Windows 可跑）执行全量回归（现 `npm run e2e:rt`、`e2e:rt-real` 的超集），「新修改后完整回归」成为常规动作。
4. **合并 ROADMAP.md:294 deferred 项**（13.3 十项）作为其中子集。


## Completion

- status: done
- completed: 2026-10-14
- delivery: docs/E2E-COVERAGE-MATRIX.md, npm run regress, family-journey / e2e:family / family-nav / rt-observables, L3 driver f_* assertions
- gate: browser E2E first; iPad/device residual L4 optional
