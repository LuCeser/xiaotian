---
phase: 260823-urr
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - .scratch/speech-recognition-redesign/issues/02-core-dictation-flow-tts.md
  - .planning/STATE.md
autonomous: true
requirements: []
must_haves:
  truths:
    - ticket 02 issue 文件 Status 为 done 且列出全部 6 个提交（be653a4 + 评审修复 3a1fc59/799de7c/f8e5e31/d6c046b/45435bb），不再含 ready-for-agent
    - 7 条验收项中恰好 6 条勾选：第 1/3 条勾选并附取舍备注（真实 iPad 播放/触控属 UAT），第 2/4/6/7 条勾选无备注，第 5 条（<1.5s 延迟）保持未勾选并附取舍备注（未测量，留待真实设备 UAT）
    - 文末存在 Completion note，含验证方式（go test）、按 executor 实跑结果记录的实测结论、双轴 code review 5 项修复清单与 6 个提交清单
    - STATE.md 中指向 /gsd-plan-phase 8.1 的过时指针全部清零（frontmatter stopped_at、Current Position Next、Session Continuity Stopped at/Resume file 共 4 处），Next 指向 Phase 8.2（ticket 03 语音指令）或先做 ticket 02 的 iPad UAT
    - STATE.md Quick Tasks Completed 表零改动（本任务新行由 orchestrator 追加）
    - 全部变更收于单个原子提交：中文提交信息、带 260823-urr 标识、涵盖 issue 文件 + STATE.md + 本 quick 目录文档，issue 文件首次纳入 git 跟踪
  artifacts:
    - .scratch/speech-recognition-redesign/issues/02-core-dictation-flow-tts.md（Status done + 6/7 勾选 + 3 条取舍备注 + Completion note，首次跟踪）
    - .planning/STATE.md（仅 4 行级修正：frontmatter stopped_at、Next 行、Stopped at 行、Resume file 行）
    - .planning/quick/260823-urr-ticket-02/260823-urr-SUMMARY.md
  key_links:
    - 文档中记录的 6 个 commit hash 必须与 git log 实测一致（be653a4 实现提交 + 3a1fc59/799de7c/f8e5e31/d6c046b/45435bb 评审修复）
    - Completion note 的"实测全绿"结论必须与 executor 现场运行 `go test ./pkg/session/ ./cmd/service/ -count=1` 的真实输出同源；若任一测试失败，停止并如实报告，严禁预写"全绿"
---

# 快速任务：收尾 speech-recognition-redesign Ticket 02 状态文档

<objective>
Ticket 02（核心听写流，即 ROADMAP Phase 8.1 重定范围）的实现（be653a4）与双轴 code review 5 项修复已于 2026-08-23 完成，但状态文档仍停在"ready-for-agent、7 项全未勾选"，且 STATE.md 仍指向已被取代的"/gsd-plan-phase 8.1 re-plan"。本计划照 ticket 01 收尾模式（Status done + 按实证勾选 + 取舍备注 + Completion note）将两处文档对齐事实。

Purpose: 使 ticket 状态与项目状态可审计、可追溯（真实 commit hash + 实测结论），并把下一步明确指向 Phase 8.2 或 iPad UAT。
Output: 2 个 markdown 文件的针对性修正 + 1 个原子提交（含 issue 文件首次纳入 git 跟踪与 quick 目录文档）。
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@.scratch/speech-recognition-redesign/issues/01-xiaomi-mimo-service-integration.md
@.scratch/speech-recognition-redesign/issues/02-core-dictation-flow-tts.md
@.planning/STATE.md
@.planning/ROADMAP.md
</context>

<verified_facts>
以下事实已于 2026-08-23 规划时从 git log 与代码库实测确认，作为既定事实直接写入文档，不得改写：

- 实现提交 be653a4（feat(02): core dictation flow）改 12 个文件 +614 行：pkg/session/timing.go（AdaptiveTimeout：8s 基线 + 超过 4 字每字 +1s、上限 15s）、pkg/session/fsm.go（pause/resume/repeat 迁移 + prevState/repeatPending 追踪）、pkg/session/session.go（PAUSED 态事件循环处理：不计时、重置 deadline、sendPausedFrame、自适应超时取 min(adaptive, config)）、cmd/service/handlers.go（handlePause/handleResume/handleRepeat）、pkg/database/schema.go（StatePaused）、前端 pkg/web/static/js/ui-components.js（DictationControls，60px+ 触控目标）、app.js、state-manager.js（PAUSED 状态）、websocket-client.js（paused 帧）、index.html
- 测试产物实测存在：pkg/session/timing_test.go（2 个测试函数，表驱动共 12 用例，覆盖 0-20 字词）、pkg/session/fsm_test.go（11 个测试函数：pause/resume、repeat、PAUSED 态 next_word、PAUSED 态最后一词、PAUSED 态非法事件、prevState 追踪）；session_test.go 含 fakeStreamer StreamCalls 断言（流式 TTS 管线在会话层被测试打通）
- be653a4 提交时"All existing tests pass"
- 评审修复 5 项（快速任务 260823-sdx/t13/ti8/tyq/u8t）：3a1fc59（#1/5 repeat 竞态跳词——按流代数丢弃过期 WordComplete 事件）、799de7c（#2/5 PAUSED 超时提取到 SessionTimeout.PausedStateTimeout，默认 5 分钟）、f8e5e31（#3/5 文档化 onTransition 无锁读 repeatPending/prevState 的单写者不变量）、d6c046b（#4/5 补全 DictationControls 六个方法 JSDoc）、45435bb（#5/5 DictationControls 触控高度对齐 iOS HIG 60px、TestPrevState_Tracked 补 transition 错误检查、_applyPaused 补 payload 防护、sendPausedFrame godoc 补 Sequence TODO 说明）
- STATE.md 当前有 4 处过时指针：frontmatter 第 8 行 stopped_at、第 34 行 Current Position Next、第 153/154 行 Session Continuity Stopped at/Resume file，均指向"awaiting /gsd-plan-phase 8.1 re-plan"
- ROADMAP Phase 8.2（下一步候选）= ticket 03 语音指令，source of truth 为 .scratch/speech-recognition-redesign/issues/03-voice-command-recognition-intent.md

## 验收项勾选判定（规划时实证，executor 按此执行；若与 go test 现场结果冲突以现场为准并停下报告）

1. 第 1 条（hear words via Mimo TTS）：勾选 + 取舍备注——会话级流式 TTS 管线经 fakeStreamer 测试验证（StreamCalls 断言），真实 iPad 音频播放待 UAT
2. 第 2 条（adaptive 8-15s）：勾选，无备注——AdaptiveTimeout 12 用例全覆盖
3. 第 3 条（button controls）：勾选 + 取舍备注——后端三 handler 与前端 DictationControls 经自动化验证，真实触控交互待 UAT
4. 第 4 条（FSM LISTENING→DICTATING→PAUSED）：勾选，无备注——fsm_test.go 11 个测试覆盖
5. 第 5 条（<1.5s latency）：**不勾** + 取舍备注——未测量，需真实 iPad 端到端实测，留待 UAT
6. 第 6 条（traditional dictation rhythm）：勾选，无备注——学生控速模型（客户端触发 next/repeat + PAUSED 不计时 + 自适应书写时间），评审确认
7. 第 7 条（maintains existing WebSocket protocol）：勾选，无备注——既有 ws/session 测试全绿，控制复用既有 control frame 模式（新增 paused 帧）
</verified_facts>

<tasks>

<task type="auto">
  <name>Task 1: Issue 文件收尾（status → done + 按实证勾选 + 取舍备注 + Completion note）</name>
  <files>.scratch/speech-recognition-redesign/issues/02-core-dictation-flow-tts.md</files>
  <action>先运行 `go test ./pkg/session/ ./cmd/service/ -count=1` 确认当前全绿并记下实际输出摘要（包名 + ok 行），作为 Completion note 实测结论的事实来源；若任一测试失败，停止本任务并如实报告，不得继续写"全绿"。然后仅编辑 issue 文件，做三类修改：

1. Status 行：`**Status:** ready-for-agent` 改为 `**Status:** done (2026-08-23, commits: be653a4, 3a1fc59, 799de7c, f8e5e31, d6c046b, 45435bb)`（照 ticket 01 的 Status 行格式，实现提交在前、评审修复在后）。

2. 按判定勾选 7 条验收项（行内追加备注用" — 取舍备注：" 前缀，照 ticket 01 第 6/7 条的写法）：
   - 第 1 条勾选，行尾追加：取舍备注——会话级流式 TTS 管线经 fakeStreamer 测试验证（StreamCalls 断言）；真实 iPad 音频播放待 UAT 实测
   - 第 2、4、6、7 条勾选，不改文字、不加备注
   - 第 3 条勾选，行尾追加：取舍备注——后端 handlePause/handleResume/handleRepeat 与前端 DictationControls（60px+ 触控目标）经自动化验证；真实触控交互待 UAT
   - 第 5 条保持未勾选，行尾追加：取舍备注——未测量，需真实 iPad 端到端实测，留待 UAT（流式 TTS 与帧下发路径已就绪，无已知系统性阻塞）

3. 文末新增 `## Completion note` 小节（照 ticket 01 的 Completion note 结构），要点列表包含：(a) 验证方式 `go test ./pkg/session/ ./cmd/service/ -count=1`；(b) 2026-08-23 实测结果——按本 task 第一步的真实输出记录（timing 12 用例、fsm 11 个测试、session 集成含 fakeStreamer 断言，全部通过）；(c) 双轴 code review（Standards + Spec）发现 5 项问题已全部修复；(d) 相关提交逐条列出——be653a4（实现：AdaptiveTimeout + FSM pause/resume/repeat + DictationControls）、3a1fc59（#1/5 repeat 竞态跳词——按流代数丢弃过期 WordComplete 事件）、799de7c（#2/5 PAUSED 超时提取到 SessionTimeout.PausedStateTimeout）、f8e5e31（#3/5 onTransition 无锁读单写者不变量文档化）、d6c046b（#4/5 DictationControls JSDoc 补全）、45435bb（#5/5 60px 触控高度对齐 iOS HIG、测试错误检查、_applyPaused 防护、godoc TODO 说明）；(e) 遗留 UAT 项一行——<1.5s 延迟实测与真实 iPad 音频播放/触控交互（验收第 5 条未勾，见取舍备注）。

本 task 只编辑不提交（单原子提交在 Task 2 末尾统一做）。严禁写入任何密钥明文或环境变量赋值形式的值。</action>
  <verify>
    <automated>F="D:/workspace/ai/xiaotian/.scratch/speech-recognition-redesign/issues/02-core-dictation-flow-tts.md"; cd /d/workspace/ai/xiaotian && go test ./pkg/session/ ./cmd/service/ -count=1 && [ "$(grep -c 'ready-for-agent' "$F")" = "0" ] && grep -q 'done (2026-08-23, commits: be653a4' "$F" && [ "$(grep -cE '^- \[x\] ' "$F")" = "6" ] && [ "$(grep -cE '^- \[ \] ' "$F")" = "1" ] && grep -qE '^- \[ \] Audio quality' "$F" && [ "$(grep -c '取舍备注' "$F")" = "3" ] && grep -qi 'completion note' "$F" && for h in be653a4 3a1fc59 799de7c f8e5e31 d6c046b 45435bb; do grep -q "$h" "$F" || exit 1; done && [ "$(grep -cE 'MIMO_API_KEY[ ]*=' "$F")" = "0" ]</automated>
  </verify>
  <done>Status 行为 done 且列出全部 6 个提交；恰好 6 条勾选（第 1/3 条带取舍备注）+ 第 5 条未勾选且带取舍备注；第 2/4/6/7 条勾选无备注；Completion note 含 go test 验证方式、与实跑输出一致的实测结论、5 项评审修复与提交清单、遗留 UAT 行；go test 两包现跑全绿；无密钥明文。</done>
</task>

<task type="auto">
  <name>Task 2: STATE.md 状态推进（8.1 re-plan 指针清零 → ticket 02 done 待 UAT，Next 指向 8.2/UAT）+ 单原子提交</name>
  <files>.planning/STATE.md, .planning/quick/260823-urr-ticket-02/</files>
  <action>只做以下 4 处行级修正（编辑前先 Read 定位行号；Quick Tasks Completed 表一行都不动，本任务 260823-urr 新行由 orchestrator 追加）：

1. frontmatter `stopped_at:` 行（当前含"awaiting /gsd-plan-phase 8.1 after 2026-08-23 ticket-set calibration"）改为：Ticket 02 (Phase 8.1 scope) implemented be653a4 + dual-axis review fixes 5/5 — done pending iPad UAT; next /gsd-plan-phase 8.2 (ticket 03) or UAT。保持单行、值内不得出现冒号加空格（YAML 安全）。
2. Current Position 的 `Next:` 行（当前指向 /gsd-plan-phase 8.1 re-plan）改为：指向 /gsd-plan-phase 8.2 —— ticket 03 语音指令（voice commands via Mimo ASR）；并注明可先做 ticket 02 的 iPad 真机 UAT（<1.5s 延迟实测 + 音频播放/触控交互，验收项 5 未勾）。
3. Session Continuity 的 `Stopped at:` 行改为：Ticket 02 implemented + reviewed + fixed (be653a4, 3a1fc59, 799de7c, f8e5e31, d6c046b, 45435bb); ticket 02 status doc closed — awaiting iPad UAT。
4. Session Continuity 的 `Resume file:` 行改为：指向 .scratch/speech-recognition-redesign/issues/03-voice-command-recognition-intent.md（Phase 8.2 source of truth）— next action /gsd-plan-phase 8.2；或先做 iPad UAT（ticket 02 验收项 5）。

然后收尾：先按本 PLAN 的 output 节创建 260823-urr-SUMMARY.md，再做全部变更的单个原子提交（中文提交信息、带 260823-urr 标识），提交范围涵盖 issue 文件（首次纳入跟踪）、STATE.md 与本 quick 目录全部文档，例如：`git add .scratch/speech-recognition-redesign/issues/02-core-dictation-flow-tts.md .planning/STATE.md .planning/quick/260823-urr-ticket-02/` 后 `git commit -m "docs(260823-urr): 收尾 ticket 02 状态文档——issue 置 done（6/7 勾选+取舍备注）+ Completion note；STATE 转向 Phase 8.2/iPad UAT"`。不再做其他任何提交。</action>
  <verify>
    <automated>F="D:/workspace/ai/xiaotian/.planning/STATE.md"; cd /d/workspace/ai/xiaotian && [ "$(grep -c 'gsd-plan-phase 8.1' "$F")" = "0" ] && [ "$(grep -c 'awaiting re-plan' "$F")" = "0" ] && grep -q 'gsd-plan-phase 8.2' "$F" && grep -q '03-voice-command-recognition-intent' "$F" && grep -q 'be653a4' "$F" && grep -q '260823-u8t' "$F" && git ls-files --error-unmatch .scratch/speech-recognition-redesign/issues/02-core-dictation-flow-tts.md >/dev/null && git log --oneline -1 | grep -q '260823-urr' && git show --name-only --format= HEAD | grep -q 'STATE.md' && git show --name-only --format= HEAD | grep -q '02-core-dictation-flow-tts.md'</automated>
  </verify>
  <done>STATE.md 不再出现指向 8.1 re-plan 的过时指针（两处短语清零）；Next 与 Resume file 指向 Phase 8.2（ticket 03 issue 文件路径）且保留"或先 iPad UAT"分支；Stopped at 含 be653a4 等 6 个提交；Quick Tasks 表完好（260823-u8t 行仍在且无行增删）；issue 文件已被 git 跟踪；单个 260823-urr 原子提交同时涵盖 issue 文件、STATE.md 与 quick 目录文档。</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| （无） | 纯 markdown 状态文档编辑，不涉及代码、网络、外部输入；敏感输入仅为既有 commit hash 与本地 go test 输出 |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-urr-01 | Repudiation | issue 文件 + 提交历史 | low | mitigate | issue 文件首次纳入 git 跟踪，6 个 hash 与 git log 逐字一致，gate 逐一断言 |
| T-urr-02 | Tampering | Completion note 实测结论 | medium | mitigate | "全绿"结论必须来自 executor 现场运行 go test 的真实输出，测试失败即停止；Task 1 verify 内嵌 go test 重跑 |
| T-urr-03 | Information Disclosure | 状态文档 | low | mitigate | 严禁写入密钥明文或 env 赋值形式，两文件 gate 含零匹配门 |
</threat_model>

<verification>
1. Task 1/2 的两个 `<automated>` 验证命令全部零退出码通过。
2. `git log --oneline -2` 顶部为唯一一个 260823-urr 提交，`git show --stat HEAD` 只含 issue 文件、STATE.md 与 .planning/quick/260823-urr-ticket-02/ 下文档。
3. `git status --short .scratch/speech-recognition-redesign/` 中 02 号 issue 文件不再未跟踪。
4. `git diff HEAD~1 HEAD -- .planning/STATE.md` 仅 4 行级变更（frontmatter stopped_at、Next、Stopped at、Resume file），Quick Tasks 表无任何行增删。
5. 人读复核：issue 文件第 5 条为唯一未勾选项且取舍备注指向 UAT；STATE.md 全文无残留的 8.1 重规划字样。
</verification>

<success_criteria>
- issue 文件：Status=done（6 提交）、6/7 勾选、第 1/3/5 条取舍备注（第 5 条不勾）、Completion note（go test 实测 + 5 项评审修复清单 + 遗留 UAT 行）
- STATE.md：8.1 re-plan 指针清零、Next/Resume 指向 Phase 8.2（ticket 03）或 iPad UAT、Quick Tasks 表零改动
- 单个 260823-urr 中文原子提交，涵盖两个内容文件 + quick 目录文档，issue 文件首次跟踪
</success_criteria>

<output>
Create `.planning/quick/260823-urr-ticket-02/260823-urr-SUMMARY.md` when done (在 Task 2 的原子提交之前创建)
</output>
