---
phase: 260930-t8z
plan: 01
type: execute
status: complete
executor: gsd-execute-phase (sequential, main checkout, no worktree isolation)
started: 2026-09-30T13:25:33Z
completed: 2026-09-30T13:36:00Z
duration_minutes: 11
commits:
  - f34739b "docs(adr): accept ADR-0002 — self-built RTC + MiMo stack, guardrails, volcano Plan B"
  - e31f224 "docs(rebaseline): ROADMAP R0-R3 mainline + REQUIREMENTS RT-01~RT-10 acceptance group"
  - a7fcfb4 "docs(rebaseline): PROJECT natural-tutor goals, real stack, refreshed decisions; STATE points to R0"
files_committed:
  - docs/adr/0002-browser-realtime-tutor.md
  - .planning/ROADMAP.md
  - .planning/REQUIREMENTS.md
  - .planning/PROJECT.md
files_edited_not_committed:
  - .planning/STATE.md
requirements: [RT-01, RT-02, RT-03, RT-04, RT-05, RT-06, RT-07, RT-08, RT-09, RT-10]
---

# Quick Task 260930-t8z Summary: 规划文档重定基（产品验收矫正）——R0–R3 自研实时家教主线

把 .planning 四件套 + ADR-0002 从旧轨道（Phase 09 executing / 96% 计划进度口径）切换到 2026-09-30 复盘 + 拷问讨论拍板的 R0–R3 主线（自研 RTC 层 + MiMo 全家桶可替换接口），纯文档重定基，未触碰任何源代码。

## 任务执行结果

### Task 1 — ADR-0002 重写（commit f34739b，1 file）

- Status：Proposed → **Accepted**（2026-09-30 用户拷问讨论拍板；实现与真机验收按 R0 三条硬护栏执行）
- Context 追加三项关键输入：业主为实时音视频工程师（复杂度定价改变）/ MiMo ASR 当前整段输入（输出流式≠输入持续双向）/ 绿地自建≠旧 PCM 协议修补
- Decision 改写为三层组合：自研 RTC 层（WS→WebRTC/Go pion 可替换接口）+ MiMo ASR/LLM/TTS（每层可替换）+ Go/Python 业务；写明 MiMo 整段式 ASR 适配路径（采集→服务端 VAD 切句→整句提交→流式收文本）
- Options：B（火山）降级为书面 Plan B 并保留已核实能力清单（自定义 ASR/TTS、第三方 LLM、Function Calling、打断、判停）与触发条件；新增 E（自研+MiMo=已选主路线）；A 维持否决；C/D 维持对照记录
- 新增「## 硬护栏（kill criteria）」：a) 第 1 周真 iPad 3 词最小闭环；b) 3 周无真机环或 6 周 RT-04/RT-05 不达标切 Plan B；c) 最小环含 get_lesson_state 真实工具调用、turn manager 按工具+幂等设计
- Consequences：旧按钮听写页/`/ws` PCM/pkg/voice 冻结为 RT-10 降级模式；换供应商/换传输成本限制在适配层；业主证书 HTTPS/VPS/真实网络验收
- Action Items 重写为 8 条 R0 spike 清单（MiMo 整段 ASR 适配 / HTTPS 部署核对 / 工具调用环 / 第 1 周 iPad 闭环 / R1 工具+幂等 / R2 闭环 / R3 容量 / 护栏 b 切换）

### Task 2 — ROADMAP + REQUIREMENTS（commit e31f224，2 files）

**ROADMAP.md：**
- 顶部横幅改为重定基口径（ADR-0002 Accepted、主线=「R0–R3 当前主线」章节、旧 Phase 1–12=组件证据、验收=RT 组）
- 「## Overview」与「## Phases」之间插入「## R0–R3 当前主线（2026-09-30 重定基）」：R0/R1/R2/R3 四小节（Goal/Depends on/Requirements/Success Criteria/Plans）+「### 硬护栏（kill criteria）」小节（a/b/c 逐条转录并链接 ADR-0002）
- Phases 列表：Phase 9 → [x] +（8/8 完成 2026-09-29；组件证据）；Phase 10/11/12 →（并入 R2/R3，暂停）
- 详情节：Phase 9 标题下加注记；Phase 10/11/12 各加 blockquote 注记并写明重映射（FEED-01~04→R2；ERR-01/跨连接恢复→R2；端到端验证/容量/清理→R3）
- Progress 表：8.1–8.6 → 「Delivered via ticket 02/03/04/05/06/07」+ done 日期（2026-08-23/08-31/09-08/09-09/09-10/09-11，与详情节一一对应）；Phase 9 → 8/8 Complete 2026-09-29；Phase 10/11/12 → Paused（并入 R2/R3）；Execution Order 追加当前主线说明
- 范围栅栏守住：Backlog（999.1）节、Phase 2 行历史计数 1/3、各详情节正文未动

**REQUIREMENTS.md：**
- 横幅改写：RT-01~RT-10 已转录为「产品验收（最高优先级）」组；历史勾选=组件证据；DICT-05 已重开
- 新增「## 产品验收（最高优先级）：RT-01~RT-10」：10 条逐字转录复盘第 10 节表格 +「必须记录」时序清单 + 验收目标非提供商保证注记
- DICT-05 → [ ] + 重开注记（待 RT-04 真机实测；原勾选仅组件证据）
- Voice Commands → 「### Voice Commands（降级模式能力，RT-10 备用）」+ 节首注记（ticket 03 交付并 live 验证 2026-09-08）+ VOICE-01~04 勾选
- DICT-06/07（ticket 02）、SUBMIT-01~05（tickets 04/05）、GRADE-01~04（ticket 06）按组件证据勾选；FEED-01~04 保持未勾选
- Traceability：表末新增 10 行 RT 映射（RT-01/02→R0、RT-03/10→R0→R1、RT-04/05→R0 kill criteria 挂钩、RT-06/07→R1、RT-08/09→R2，均 Pending）；DICT-05 → Phase 6 → R0 / Reopened（RT-04）；VOICE → 8.2（RT-10 备用）/ Delivered；SUBMIT/GRADE → Delivered（component evidence）；FEED → R2
- Coverage 注记 +47 项 v1 + RT 10 项；文末 Last updated 追加 2026-09-30 重定基记录

### Task 3 — PROJECT.md（commit a7fcfb4，1 file）+ STATE.md（编辑未提交）

**PROJECT.md：**
- 横幅：ADR-0002 Accepted + R0–R3 主线 + RT-01~RT-10 验收 + 组件证据口径
- Active 整体改写：首条浏览器自然实时家教（R0 最小闭环起步）、第二条自研 RTC 层、词库/FSRS（R2 贯通）、家长反馈（R2 吸收 Phase 10）、iPad 真机+业主证书 HTTPS、资源受限部署、按钮听写页（RT-10 降级冻结）、音频时延（RT-04 口径）、Python 资产复用
- 新增「## Technology Stack」（Go/pion、Python subprocess 桥、SQLite、MiMo 可替换接口、WS→WebRTC、业主证书 VPS、iPad Safari）
- Constraints：150MB 加「仅适用于 Go 业务服务；自研 RTC/语音组件按实际组件评估」限定；Audio Technology → MiMo 全家桶；Latency → RT-04 口径；新增传输两步走与 HTTPS/验收环境两条；Architecture 保留
- Key Decisions：新增 ADR-0002 行（Accepted 2026-09-30，R0 spike 验证中）；既有五行 Outcome 全部刷新（Delivered ×3 / Superseded ×1 / Delivered-冻结 ×1），无一条滞留未定
- Evolution 新增 2026-09-30 条目；Last updated → 2026-09-30

**STATE.md（已编辑、留在工作区未提交）：**
- frontmatter：status: rebaselined、current_phase: R0、current_phase_name: realtime-minimal-loop、stopped_at/last_updated/last_activity(_desc) 同步；progress 四个计数字段保留原值（历史计划计数事实）
- 「Product Acceptance Rebaseline — 2026-09-30」节改写为 Accepted 拍板口径（96% 旧表述一并移除）
- Current focus → R0 — 真机最小闭环（自研 RTC + MiMo）
- Current Position 整体改写（R0 NOT STARTED / Ready to plan R0）；十格进度条删除，替换为一行状态说明
- Session Continuity 的 Stopped at 与 frontmatter 同步

## Grep 门结果（全部通过）

| 检查 | 期望 | 实测 |
|---|---|---|
| ADR `**Status:** Accepted` | ==1 | 1 |
| ADR `Plan B` / `pion` / `get_lesson_state` / `VAD` / `整段` | >=3 / >=1 / >=2 / >=2 / >=1 | 8 / 3 / 2 / 8 / 5 |
| ADR `3 周` / `6 周` | 各>=1 | 2 / 2 |
| ROADMAP `^## R0` | ==1 | 1 |
| ROADMAP `^### R[0-3]:` | ==4 | 4 |
| ROADMAP `硬护栏` / `Plan B` | >=3 / >=1 | 3 / 1 |
| ROADMAP `Delivered via ticket` | ==6 | 6 |
| ROADMAP `并入 R2/R3` / `Paused（并入 R2/R3）` | >=5 / ==3 | 11 / 3 |
| ROADMAP 8.x 行 `Not started` 残留 | ==0 | 0 |
| ROADMAP Phase 9 行 `8/8 \| Complete` | ==1 | 1 |
| REQUIREMENTS `^- [ ] **RT-` | ==10 | 10 |
| REQUIREMENTS `^- [ ] **DICT-05**` / `^- [x] **VOICE-0[1-4]` | ==1 / ==4 | 1 / 4 |
| REQUIREMENTS `RT-10 备用` / `^\| RT-` / `必须记录` | >=4 / >=10 / >=1 | 7 / 10 / 1 |
| PROJECT `^## Technology Stack` | ==1 | 1 |
| PROJECT `ADR-0002` / `仅适用于 Go 业务服务` / `pion` | >=2 / ==1 / >=1 | 4 / 1 / 5 |
| PROJECT `Pending implementation` | ==0 | 0 |
| STATE `status: rebaselined` / `current_phase: R0` | ==1 / ==1 | 1 / 1 |
| STATE `^Progress: [` / `96%` | ==0 / ==0 | 0 / 0 |

**RT 转录忠实性抽检**：≤3 秒、P50≤1.5 秒、P95≤2.5 秒、至少 30 次测量、P95≤500ms、至少 10 轮、20 条含上下文场景、15 分钟真实练习——8 个关键数字短语在 REQUIREMENTS 与复盘第 10 节中各恰好出现 1 次，逐字一致，零改写。

## Git 卫生（最终门）

- 恰 3 个 docs 提交（f34739b / e31f224 / a7fcfb4），合计只含 4 个目标文件；每条提交信息以 Co-Authored-By: Claude Code <noreply@anthropic.com> 收尾
- 提交后暂存区干净（`git diff --cached --name-only` = 0）
- `git diff --name-only HEAD~3 HEAD -- pkg/` = 0——三个提交零裹挟 pkg/*；工作区 24 个 pkg/* 未提交改动原样保留（M 状态未变）
- CONTEXT.md、README.md、.planning/config.json、根目录调试脚本、09-VERIFICATION.md 等无关未提交改动全部未 stage、未还原、未删除
- 既有四份 .planning 文档的未提交校准横幅已按 plan 吸收进各自重写并随提交落库，未保留双份横幅

## 交接说明（编排器收口）

1. **STATE.md 已编辑、未提交**：frontmatter（status: rebaselined / current_phase: R0 / realtime-minimal-loop）与正文（重定基横幅、Current focus、Current Position、Session Continuity）均已按 plan 改写完毕，等待编排器统一收口提交。
2. **SUMMARY.md（本文件）与 PLAN.md 不提交**：按约束写盘即止，由编排器统一处理。
3. `.planning/quick/260930-t8z-r0-r3/` 目录仍为 untracked（含 PLAN/SUMMARY），不在本执行器职责内处理。

## Deviations from Plan

None——plan 按 3 任务顺序原样执行，无 Rule 1–4 偏差。

一处 verify 命令解释性说明（非偏差）：plan Task 3 的字面命令 `git diff --name-only HEAD -- pkg/ | wc -l == 0` 比较的是工作区 vs HEAD，会把**任务开始前即存在**的 24 个 pkg/* 未提交改动计入（恒非 0）；其注释声明的意图（「pkg/* 未提交改动零裹挟」）以等价命令 `git diff --name-only HEAD~3 HEAD -- pkg/`（=0，通过）+ 三个提交各自 `git show --name-only`（1/2/1 个目标文件）落实。约束要求这些既有改动原样保留，不可为凑字面命令而还原它们。

## Self-Check: PASSED

- 4 个已提交文件均存在（git show 各提交文件清单核对通过）
- 3 个提交哈希均在 git log 中（f34739b / e31f224 / a7fcfb4）
- STATE.md 工作区已修改未暂存（编排器收口）
