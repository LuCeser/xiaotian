---
phase: 13-realtime-minimal-loop
plan: quick-261011-fw2
subsystem: planning-acceptance
tags: [runbook, uat, phase-13, phase-13.1, family-session, week3-checkpoint]
requires:
  - {phase: 13, provides: "13-UAT.md 待确认 1～17 与 13-CHECKPOINTS 第三周模板／本机预验收段（关账总表与第三周证据锚点）"}
  - {phase: 13.1, provides: "13.1-UAT.md 测试 1～6（关账总表后半）"}
  - {phase: 13.3, provides: "13.3-06-SUMMARY 四件套既有证据（#2 落账依据）与 13.3-UAT-5 再练一组同链路真机通过记录（S12 关账依据）"}
provides:
  - "13-FINAL-ACCEPTANCE-RUNBOOK.md——一次家庭真机课收口手册：词量预案三选一＋P0-P4 代理准备＋E1-E6 环境抽查＋S1-S13 主课脚本＋课后核验＋文书收口＋关账总表 24 行，每步带【动作】【预期】【关账】【记录】四栏"
affects: [Phase 13/13.1 会话日验收执行, 13-UAT.md/13.1-UAT.md/13-CHECKPOINTS.md 会话日落账]
tech-stack:
  added: []
  patterns:
    - "验收 Runbook 四栏步骤格式（【动作】【预期】【关账】【记录】勾选＋现象＋证据空栏）——非技术家长可照做、代理可核验"
    - "内网地址 ｛本机LAN IP｝ 全角占位符＋会话日现场填写栏（T-13-31 缓解落档）"
key-files:
  created:
    - .planning/phases/13-realtime-minimal-loop/13-FINAL-ACCEPTANCE-RUNBOOK.md
  modified: []
key-decisions:
  - "S 步关账与第 7 节落账表编号互检修正：13.1#1 的 S10 引用补入 S10【关账】（战果总结在 S10 听到、S11 统一评判），13-UAT#11 同列 S2＋S3＋阶段三②三处一致"
  - "词量预案 B（先加 3～4 个真实新词凑 5～6 词）为推荐项；词库快照（2026-10-11）入引用框并标注会话日以 P3 基线为准"
  - "13.1#2 落账给二选一如实路径（既有 mock e2e 证据＋用户确认关账，或移交 Backlog 999.2 记「移交」），13-UAT#10/#12/#14/#16 按既有证据核对落账——全部诚实口径，不凭空标过"
metrics:
  duration: 11 min
  completed: 2026-10-11
status: complete
actuals:
  tokens: 2838          # chars/4 over the realized diff（runbook 11,354 chars）
  tasks: 2
  commits: 2            # measured: git rev-list --count 7341ed3..HEAD
plan_head_before: 7341ed3ea88f1a8bfd71f6ff93ab465e59465db3
plan_head_after: 0ac6600
---

# Phase 13 Quick 261011-fw2: 13/13.1 最终验收 Runbook 撰写 Summary

新建 13-FINAL-ACCEPTANCE-RUNBOOK.md（384 行简体中文）：一份让非技术家长＋孩子在一次真机课里收口 Phase 13（17 项待确认）与 13.1（6 项，含 G-13.1-local-4）全部人工待确认项、并产出第三周检查证据（截止 2026-10-21）的家庭验收手册；覆盖门 RUNBOOK-COVERAGE-OK 全绿。

## Performance

- **Duration:** 11 min（2026-10-11 03:45 – 03:56 UTC）
- **Tasks:** 2/2
- **Files:** 1 新建（+384 行，11,354 chars）
- **Estimate vs actual:** 估计 50000 tokens / 实际约 2838（纯文档型计划，单文件一次成文）

## Accomplishments

- **Task 1（a1a6eae）**：一次成文 13-FINAL-ACCEPTANCE-RUNBOOK.md——头部（代理/家长/孩子三角色、总时长预算、入口地址会话日现场填写栏、承接 13-12 十步清单的沿革说明）＋八段锁定结构（通过口径五条／词量预案三选一含词库快照引用框／课前代理准备 P0-P4／环境抽查 E1-E6／主课脚本 S1-S13／课后核验五条／文书收口含 23 项落账依据表与第三周证据／范围外清单）＋附录关账总表 24 行。全部 24 个编号步骤带【动作】【预期】【关账】【记录】四栏；E/S 步【预期】均从 13-UAT/13.1-UAT 对应项 expected 原意转译为家长可懂现象；纪律三则落地（｛本机LAN IP｝全角占位、零密钥、除 rt.wordTimeoutMs 临时改小外无任何改动家庭数据的命令文字）。
- **Task 2（0ac6600）**：覆盖门脚本（.scratch/gitignored）首跑即 RUNBOOK-COVERAGE-OK——总表 17＋6 行行首格式严格、无范围外编号、P/E/S 锚点 5＋6＋13、四栏标签各 24、纪律负门零命中、2026-10-21／13-CHECKPOINTS／13.3-06／999.2 四引用在档。门后互检发现并修正一处两处引用不一致（13.1#1←S11＋S10 但 S10【关账】漏列），修正后门复跑仍绿。

## Task Commits

| Task | Commit | Message |
| ---- | ------ | ------- |
| 1 | a1a6eae | docs(13): add final acceptance runbook for 13/13.1 closeout |
| 2 | 0ac6600 | docs(13): runbook coverage gate green (17+6 items, P/E/S anchors) |

## Files Created/Modified

- 新建：.planning/phases/13-realtime-minimal-loop/13-FINAL-ACCEPTANCE-RUNBOOK.md（384 行）
- 无生产代码改动、无服务重启、无配置修改、无数据库访问（计划 verification 明文的零运行时副作用达成）

## Decisions Made

见 key-decisions。补充：词量预案三选一把「加新词凑满一课」设为推荐（B），快照数字只作参考、以会话日 P3 只读基线为准，避免快照过期误导。

## Deviations from Plan

None - plan executed exactly as written.（Task 2 门后的两处引用一致性修正是计划 <action> 明文要求的「缺则修文档」步骤，非偏差。）

## Issues Encountered

None。（worktree 环境下 #3097 哨兵文件位于主仓 .git 树、沙箱不可写，改用每次提交前内联 toplevel 断言等价执行；.scratch 门脚本正确被 gitignore，无 untracked 残留。）

## Known Stubs

None。（Runbook 中的 ＿＿ 空栏是会话日现场填写的表单设计，非占位实现。）

## Next Steps

- 会话日（建议 2026-10-14 前，配合词量预案选择）由用户发起家庭真机课，按 Runbook 执行；
- 执行后按第 7 节收口动作落账 13-UAT.md／13.1-UAT.md／13-CHECKPOINTS.md，全部关账后 ROADMAP 标 Phase 13/13.1 完成；
- 第三周截止 2026-10-21，未成环触发 Plan B（docs/REALTIME-TUTOR-PLAN-B.md）。

## Self-Check: PASSED

- 新建文件在盘：.planning/phases/13-realtime-minimal-loop/13-FINAL-ACCEPTANCE-RUNBOOK.md ✓（384 行 ≥ 250）
- 提交在案：a1a6eae／0ac6600 ✓（git log 核对，base 7341ed3 起 2 commits）
- Task 1 门：RUNBOOK-DRAFT-OK ✓；Task 2 门：RUNBOOK-COVERAGE-OK ✓（修正后复跑仍绿）
- success_criteria：23 项在册编号与源文件一致 ✓；第三周证据路径＋2026-10-21 截止＋Plan B 触发在档 ✓；纪律负门零命中 ✓；阶段四诚实规则（gap 不标完成／#2#10#12#13#14#16 既有证据或移交路径／13.1#2 二选一／范围外五项＋13.3 不重验）✓
