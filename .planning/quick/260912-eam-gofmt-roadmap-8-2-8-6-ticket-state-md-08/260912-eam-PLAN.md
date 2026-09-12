---
phase: 260912-eam
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - .gitignore
  - cmd/service/e2e_stories_test.go
  - pkg/mimo/chat_test.go
  - pkg/mimo/client.go
  - pkg/mimo/client_test.go
  - pkg/mimo/config.go
  - pkg/mimo/errors.go
  - pkg/mimo/types.go
  - pkg/voice/intent_test.go
  - .planning/ROADMAP.md
  - .planning/phases/08-student-web-interface/08-VERIFICATION.md
  - .planning/STATE.md
autonomous: true
requirements: []
must_haves:
  truths:
    - '验证门绿:go build ./... exit 0 且 go test ./pkg/mimo/ ./pkg/voice/ ./cmd/service/ -count=1 全 ok(cmd/service e2e 数十秒属正常);任一红 → 立即停止、零提交、SUMMARY 记 incomplete 并粘贴失败输出尾部,不做任何范围外修复'
    - '恰一个 chore 提交落 9 文件(+96/-97):diff-tree 文件集恰为 .gitignore + cmd/service/e2e_stories_test.go + pkg/mimo 六文件 + pkg/voice/intent_test.go,提交含 Co-Authored-By 尾行;工作区其余 M 项(约 29 个 CRLF 幻影:pkg/infra、pkg/tts、pkg/crc16)与全部 untracked(node_modules/、BUG-FIX-REPORT.md、test-*、*.py、nul 等)零裹挟、原样保留'
    - 'ROADMAP 8.2–8.6 五段各得三件套:Superseded 块(2026-09-12)+ "**Plans**: delivered directly as ticket NN (<done 日期>) — no GSD plan files" + 勾选的 ticket 交付条目(从对应 issue 文件 Status 行抄录 commits/日期);grep "delivered directly as ticket" .planning/ROADMAP.md = 6,五条 /gsd-plan-phase 占位计划行清零;各段 Goal/Success Criteria/Requirements/Source of truth 原文不动'
    - '08-VERIFICATION.md 历史 Behavioral Spot-Checks 表格零改写(grep "backend never emits" 仍为 3),表格最后一行(No debt markers PASS 行)与 "Step 7b:" 段落之间新增注记块:4 条 FAIL 验证时点为真、已被 08-06 与 tickets 02–05 关闭,引当前代码证据 pkg/session/session.go 与 pkg/web/static/js/websocket-client.js'
    - 'STATE.md 反映 tickets 01–08 全部实现完毕(最新 f79acdb 2026-09-11 ticket-08 review 修复)+ 下一步 31 项 iPad 真机 UAT(.scratch/speech-recognition-redesign/UAT-CHECKLIST.md);frontmatter 键集与结构不变,仅改值;executor 不提交 STATE.md 与 SUMMARY(docs 由 orchestrator 统一提交),executor 的 docs 提交恰只含 ROADMAP.md 与 08-VERIFICATION.md 各一次'
  artifacts:
    - chore 提交 ×1(9 文件工作区改动:pkg/mimo+pkg/voice gofmt 格式化、startStoriesSession 测试助手提取、.gitignore ADE 工具目录忽略规则)
    - docs(roadmap) 提交 ×1(Phase 8.2–8.6 按 8.1 模式标记 ticket 03–07 直达交付)
    - docs(verification) 提交 ×1(4 条过时 FAIL 的注记块)
    - STATE.md 工作区更新(编辑但不提交,orchestrator 负责)
    - 260912-eam-SUMMARY.md 写盘不提交
  key_links:
    - 'ROADMAP 8.2–8.6 各段 ↔ 对应 ticket issue 文件 Status 行:8.2←03(done 2026-08-31 + live verified 2026-09-08)、8.3←04(done 2026-09-08)、8.4←05(anchor 2fa555f)、8.5←06(anchor a06a5db)、8.6←07(anchor 6f3cc6e)——每条 Superseded 记录锚定可追溯证据'
    - '08-VERIFICATION 注记 ↔ 当前代码实证(写注记前先 grep 复核):progress_update / word_complete / session_complete 发送点在 pkg/session/session.go,play_word 发送点在 pkg/web/static/js/websocket-client.js'
    - 'STATE stopped_at ↔ git 现实:f79acdb(2026-09-11)是 tickets 01–08 收尾提交,唯一未决项是 31 项 iPad UAT'
---

# 快速任务:归账清理——提交 9 文件工作区改动 + ROADMAP 8.2–8.6 ticket 直达标记 + STATE 前移 + 08-VERIFICATION 过时 FAIL 注记

<objective>
本任务是**归账(accounting)**,不是写新功能:四件事——(1) 把主 checkout 里 9 个 tracked 文件的未提交改动以恰一个原子提交落库;(2) ROADMAP.md 把 Phase 8.2–8.6 五段按 8.1 已有的 Superseded 模式标记为已由 ticket 03–07 直接交付;(3) STATE.md 的 stopped_at 等过时指针前移到"tickets 01–08 全部实现完毕,下一步 iPad UAT";(4) 08-VERIFICATION.md 的 4 条过时 FAIL 加注记(不改写历史表格)。

**执行环境(硬约束):执行器必须顺序运行在主 checkout 上,本次运行禁用 worktree 隔离**——Task 1 的 payload 是未提交的工作区状态,新 worktree 看不到它;Task 2–4 编辑的 .planning 文档同样绑定主检出。

**提交归属(硬约束)**:executor 正常提交三样东西——9 文件 chore 提交、ROADMAP.md docs 提交、08-VERIFICATION.md docs 提交。**STATE.md 与 SUMMARY 只编辑/写盘、绝不提交**(docs 由 orchestrator 统一收口)。

Purpose: 工作区与规划文档对齐"ticket 01–08 已全部交付"的现实,消除 ROADMAP/STATE/VERIFICATION 三处账实不符。
Output: 3 个提交 + STATE.md 工作区更新 + SUMMARY(写盘不提交)。
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@.planning/ROADMAP.md
@.planning/phases/08-student-web-interface/08-VERIFICATION.md

规划期已实证的事实(执行器直接采用,无需重新调查):

- **9 文件清单与体量**(git diff --stat,2026-09-12 规划时点):.gitignore(+10)、cmd/service/e2e_stories_test.go(66 行变动)、pkg/mimo/{chat_test,client,client_test,config,errors,types}.go、pkg/voice/intent_test.go,合计 +96/-97。
- **内容三类**:(a) .gitignore 新增 ADE 工具目录忽略规则——`## ADE` 段下 .agents、.claude/*(含 !.claude/CLAUDE.md 反排除)、.codex、.hta-data、.opencode;(b) e2e_stories_test.go 提取 `startStoriesSession(t, conn) sessionReadyResponse` 测试助手去重;(c) pkg/mimo 六文件 + intent_test.go 的 gofmt 格式化——结构体字段对齐为主,intent_test.go 另有长行表格测试函数字面量折行(逐字 token 不变,零行为变化;git diff -w 复核仅此两文件有非空白行结构差异,均为折行/空行)。
- **工作区其余 M 项是 CRLF 幻影**:pkg/infra/*(21 文件)、pkg/tts/*(10)、pkg/crc16/crc16.go 共约 29 个文件在 git status 显示 M,但 git diff 经行尾归一化后为空(规划期 git diff --stat 仅 9 文件实证)。**这些幻影不提交、不碰、不追**;提交后 status 仍显示它们属预期。
- **untracked 杂物一律不碰**:node_modules/、BUG-FIX-REPORT.md、BUG-FIX-VERIFICATION.md、CONTEXT.md、E2E-TEST-GUIDE.md、SESSION-*.md、check_*.py、debug-test.html、test-*.js/html/py、fix_conflicts.py、reset-vocab.*、unlock_cards.py、nul、home-tutor.exe~、docs/adr/、.scratch/debug/、third_party/data/ 等(仓库卫生 todo 另行处理)。
- **git add 会打 "LF will be replaced by CRLF" 警告**——Windows checkout 正常现象,不是错误,忽略。
- **ticket → phase 对应与证据锚点**(orchestrator 已核实):8.2←ticket 03(done 2026-08-31 + live verified 2026-09-08)、8.3←ticket 04(done 2026-09-08)、8.4←ticket 05(anchor commit 2fa555f)、8.5←ticket 06(anchor a06a5db)、8.6←ticket 07(anchor 6f3cc6e);issue 文件在 .scratch/speech-recognition-redesign/issues/NN-*.md,各自 Status/Completion 行载有精确 done 日期与 commit 清单。
- **8.1 参照模式**(ROADMAP 约第 305–317 行):Superseded 块 + "**Plans**: delivered directly as ticket 02 (2026-08-23) — no GSD plan files" + 勾选 ticket 条目列全部 commits 与验收状态。
- 项目运行环境 Windows + Git Bash,命令须 bash 兼容;go test ./cmd/service/ 的 e2e 用例需数十秒属正常。
</context>

<tasks>

<task type="auto">
  <name>Task 1: 验证门 → 精确暂存恰 9 文件 → 单一 chore 提交</name>
  <files>.gitignore, cmd/service/e2e_stories_test.go, pkg/mimo/chat_test.go, pkg/mimo/client.go, pkg/mimo/client_test.go, pkg/mimo/config.go, pkg/mimo/errors.go, pkg/mimo/types.go, pkg/voice/intent_test.go</files>
  <action>四步,顺序执行:

  **第一步:验证门(提交前跑,工作区内容即提交内容)。** 依次 go build ./... 与 go test ./pkg/mimo/ ./pkg/voice/ ./cmd/service/ -count=1。任一红:立即停止本计划,不尝试任何修复(修复超出归账任务范围),不产生任何提交,SUMMARY 状态记 incomplete 并粘贴失败输出关键尾部(失败包名 + 断言/错误消息),注明"9 文件改动保持未提交工作区状态"。全绿才进第二步。

  **第二步:精确暂存。** git add 用 9 个显式路径一次性列出(空格分隔):.gitignore cmd/service/e2e_stories_test.go pkg/mimo/chat_test.go pkg/mimo/client.go pkg/mimo/client_test.go pkg/mimo/config.go pkg/mimo/errors.go pkg/mimo/types.go pkg/voice/intent_test.go。**严禁 git add -A、git add -u、git add . 、git add 目录前缀**——工作区还有约 29 个 CRLF 幻影 M 项和大量不该提交的 untracked 杂物(见 context),批量/通配暂存会把它们扫进来。LF→CRLF 警告忽略。

  **第三步:暂存集断言后单一提交。** 先跑 git diff --cached --name-only,确认结果恰为上述 9 个路径、无任何多余项;不符则 git restore --staged 全部重来。然后单次 git commit,信息一字不差:

  主题行:

  chore: land workspace hygiene — mimo/voice gofmt, e2e stories helper, ADE gitignore

  正文(空行接主题行):

  Quick-task workspace accounting (260912-eam):

  - pkg/mimo (6 files) + pkg/voice/intent_test.go: gofmt only —
  struct-field alignment and wrapping of over-long table-test func
  literals; no behavior change
  - cmd/service/e2e_stories_test.go: extract startStoriesSession
  helper deduplicating the e2e stories session bootstrap
  - .gitignore: ignore ADE tool dirs (.agents, .claude/* except
  CLAUDE.md, .codex, .hta-data, .opencode)

  末行(trailer):

  Co-Authored-By: Claude Code <no-reply@anthropic.com>

  多行信息用单次 git commit 原样传入(主题 + 空行 + 正文 + 空行 + trailer);一行一个 -m 分段亦可,但换行结构与逐字内容不得变。

  **第四步:提交后自查(下方 automated 全过才算完)。** HEAD 文件集恰 9 个;再跑一次 go build ./... 作收尾 sanity(满足"提交后验证");CRLF 幻影 M 项与 untracked 原样;SUMMARY 尚未写盘也尚未提交任何 .planning 文件。
  </action>
  <verify>
    <automated>cd /d/workspace/ai/xiaotian && go build ./... && go test ./pkg/mimo/ ./pkg/voice/ ./cmd/service/ -count=1 >/dev/null 2>&1 && test "$(git diff-tree --no-commit-id --name-only -r HEAD | sort | tr '\n' ' ')" = ".gitignore cmd/service/e2e_stories_test.go pkg/mimo/chat_test.go pkg/mimo/client.go pkg/mimo/client_test.go pkg/mimo/config.go pkg/mimo/errors.go pkg/mimo/types.go pkg/voice/intent_test.go " && git log -1 --format=%s | grep -q '^chore: land workspace hygiene' && git log -1 --format=%B | grep -q '^Co-Authored-By: Claude Code <no-reply@anthropic.com>$' && git status --porcelain -- node_modules | grep -q '^?? node_modules/$' && git status --porcelain -- pkg/tts | grep -q '^ M ' && echo TASK1_OK</automated>
  </verify>
  <done>验证门全绿(build exit 0、三包测试全 ok);HEAD 恰含 9 个 payload 文件(diff-tree 逐路径比对通过);提交主题 chore 前缀 + Co-Authored-By 尾行俱在;node_modules/ 仍 untracked、pkg/tts 幻影 M 项仍在(零裹挟实证);看到 TASK1_OK。红灯路径:已停止、零提交、SUMMARY 如实记 incomplete。</done>
</task>

<task type="auto">
  <name>Task 2: ROADMAP 8.2–8.6 五段按 8.1 模式标记 ticket 直达交付,单独 docs 提交</name>
  <files>.planning/ROADMAP.md</files>
  <action>对 Phase 8.2/8.3/8.4/8.5/8.6 五个段落(约第 321–414 行)各做同一套编辑(用 Edit 工具做范围替换,不动各段 Goal/Success Criteria/Requirements/Source of truth/Depends on;标题行 ### Phase 8.x: ...(INSERTED) 无复选框,无需处理):

  **先取证。** 逐一读五个 issue 文件的 Status/Completion 行,抄录各自的 done 日期与交付 commit 清单(8.1 的 ticket-02 条目列 be653a4 + 全部评审修复 commits 的写法即范本):.scratch/speech-recognition-redesign/issues/03-voice-command-recognition-intent.md(done 2026-08-31,live verified 2026-09-08)、04-photo-ocr-answer-submission.md(done 2026-09-08)、05-batch-answer-submission-workflow.md(anchor 2fa555f)、06-automatic-grading-detailed-analysis.md(anchor a06a5db)、07-multi-user-session-isolation-scaling.md(anchor 6f3cc6e)。done 日期以 issue Status 行为准,需要佐证时 git log -1 --format=%as &lt;hash&gt;。

  **每段两处替换。** 其一,把 "**Plans**: 0 plans" 这一行替换为 Superseded 块 + 新 Plans 行(以 8.2 为例,其余四段同构换 ticket 号):

  **Superseded** (2026-09-12 — delivered directly via ticket set):

  - ticket 03 → done 2026-08-31 + live verified 2026-09-08 (见 .scratch/speech-recognition-redesign/issues/03-voice-command-recognition-intent.md Status 行)

  **Plans**: delivered directly as ticket 03 (2026-08-31) — no GSD plan files

  其二,把 Plans: 列表里那条未勾选的、指向 /gsd-plan-phase 8.x 的占位行替换为勾选交付条目(措辞与 8.1 的 ticket-02 条目同款,"delivered directly:" 后接 commits):

  - [x] Ticket 03 — voice command recognition (Mimo ASR + LLM intent, button fallback) delivered directly: &lt;issue Status 行抄录的 commit 清单&gt;; issue closed done 2026-08-31, live verified 2026-09-08

  8.3←ticket 04、8.4←ticket 05、8.5←ticket 06、8.6←ticket 07 照此换号换日期换 commits;ticket 04–07 条目里各自概括一句交付主题(照各 issue 标题/Goal),验收状态(如 live verified)只写 issue Status 行实际有的。

  **提交(只含 ROADMAP.md)。** git add .planning/ROADMAP.md 后 git diff --cached --name-only 断言仅此一文件,再单次提交:

  docs(roadmap): mark phases 8.2-8.6 delivered directly as tickets 03-07

  正文:

  Accounting per the 8.1 Superseded pattern: each section records
  its ticket delivery (issue Status line + commits) instead of the
  stale zero-plan placeholder. The ticket set remains the single
  source of truth (user decision 2026-08-23).

  末行 trailer 同 Task 1(Co-Authored-By: Claude Code <no-reply@anthropic.com>)。
  </action>
  <verify>
    <automated>cd /d/workspace/ai/xiaotian && test "$(grep -c 'delivered directly as ticket' .planning/ROADMAP.md)" -eq 6 && test "$(grep -c 'TBD (run /gsd-plan-phase' .planning/ROADMAP.md)" -eq 0 && test "$(grep -c 'Superseded.*2026-09-12' .planning/ROADMAP.md)" -eq 5 && test "$(git diff-tree --no-commit-id --name-only -r HEAD)" = ".planning/ROADMAP.md" && git log -1 --format=%B | grep -q '^Co-Authored-By: Claude Code' && echo TASK2_OK</automated>
  </verify>
  <done>五段各有 Superseded 块(2026-09-12)、"delivered directly as ticket NN (日期) — no GSD plan files" 的 Plans 行、勾选 ticket 条目(issue Status 行 commits 抄录);全文件 "delivered directly as ticket" 计 6(8.1 + 五段各一)、五条 /gsd-plan-phase 占位计划行清零;HEAD docs 提交恰只含 .planning/ROADMAP.md;看到 TASK2_OK。</done>
</task>

<task type="auto">
  <name>Task 3: 08-VERIFICATION 过时 FAIL 注记(提交)+ STATE.md 前移(只编辑不提交)</name>
  <files>.planning/phases/08-student-web-interface/08-VERIFICATION.md, .planning/STATE.md</files>
  <action>**第一段:证据复核(写注记前必做)。** 四条 grep 全部非空才继续,任何一条空 → 停止本任务,SUMMARY 注明"注记未写:证据缺失",不得写无凭注记:grep -n progress_update pkg/session/session.go;grep -n word_complete pkg/session/session.go;grep -n session_complete pkg/session/session.go;grep -n play_word pkg/web/static/js/websocket-client.js。

  **第二段:注记块(追加,不改写历史)。** 在 08-VERIFICATION.md 的 Behavioral Spot-Checks 表格最后一行(No debt markers 那行 PASS,约第 142 行)与 "Step 7b:" 段落(约第 144 行)之间插入:

  > **注记(2026-09-12,归账时点):** 上表 4 条 FAIL(后端不发 progress_update / word_complete / session_complete;前端不发 play_word)在验证时点为真——本次验证早于 08-06 与 tickets 02–05 的后续交付,上述缺口现均已关闭。历史表格按当时实况保留,不做改写。当前代码证据(grep 复核):progress_update / word_complete / session_complete 发送点在 pkg/session/session.go;play_word 发送点在 pkg/web/static/js/websocket-client.js。

  **第三段:提交(只含 08-VERIFICATION.md,STATE 尚未编辑,先提交再动 STATE 防误暂存)。** git add .planning/phases/08-student-web-interface/08-VERIFICATION.md,git diff --cached --name-only 断言仅此一文件,单次提交:

  docs(verification): annotate phase 08 stale FAILs, closed by 08-06 + tickets 02-05

  正文:

  The four FAIL spot-checks (backend not emitting progress_update /
  word_complete / session_complete; frontend not sending play_word)
  were true at verification time, which predated the 08-06 delivery
  and tickets 02-05. Annotation block appended after the table
  citing current code evidence; the historical table is left as-is.

  末行 trailer 同前(Co-Authored-By: Claude Code <no-reply@anthropic.com>)。

  **第四段:STATE.md 前移(Edit 改值,保持 frontmatter 键集与结构不变,绝不提交)。** 五处:
  1. frontmatter stopped_at(现停在 ticket 02/ticket 06 旧况)改为:Tickets 01–08(speech-recognition-redesign)全部实现完毕——最新 f79acdb(2026-09-11,ticket-08 review 修复);下一步 31 项 iPad 真机 UAT(.scratch/speech-recognition-redesign/UAT-CHECKLIST.md)。
  2. frontmatter last_updated / last_activity / last_activity_desc 改为 2026-09-12 当日值,desc 概括本归账任务(9 文件提交 + ROADMAP 8.2–8.6 标记 + VERIFICATION 注记)。
  3. Current Position 的 Next: 行(现指向已完成的 /gsd-plan-phase 8.2)改为:Next: iPad 真机 UAT——31 项清单 .scratch/speech-recognition-redesign/UAT-CHECKLIST.md(tickets 01–08 实现收尾后的唯一未决验收)。
  4. Session Continuity 的 Last session 改 2026-09-12;Stopped at 改为:tickets 01–08 全部实现完毕(f79acdb 为最新提交);工作区 9 文件清理改动已随本 quick task 落库;31 项 iPad 真机 UAT 未做。
  5. Resume file 改为:.scratch/speech-recognition-redesign/UAT-CHECKLIST.md — next action:在 iPad 真机上逐项过 31 项 UAT 清单。
  STATE.md 改完保持未暂存、未提交状态;SUMMARY 同样只写盘(orchestrator 统一收口 docs)。
  </action>
  <verify>
    <automated>cd /d/workspace/ai/xiaotian && test "$(git diff-tree --no-commit-id --name-only -r HEAD)" = ".planning/phases/08-student-web-interface/08-VERIFICATION.md" && test "$(grep -c 'backend never emits' .planning/phases/08-student-web-interface/08-VERIFICATION.md)" -eq 3 && grep -q '注记(2026-09-12' .planning/phases/08-student-web-interface/08-VERIFICATION.md && grep -q 'f79acdb' .planning/STATE.md && grep -q 'UAT-CHECKLIST' .planning/STATE.md && git status --porcelain -- .planning/STATE.md | grep -q '^ M ' && echo TASK3_OK</automated>
  </verify>
  <done>HEAD docs 提交恰只含 08-VERIFICATION.md;历史表格原样("backend never emits" 仍 3 处),注记块在表格后且引两处代码证据;STATE.md 含 f79acdb 与 UAT-CHECKLIST 指针、stopped_at/Next/Session Continuity 四处前移,frontmatter 结构未变,状态为已修改未暂存;看到 TASK3_OK。</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| 工作区 → git 历史 | 本任务把未提交改动转入永久历史——一旦裹挟杂物即污染仓库(不可轻易撤销),是唯一实质信任边界 |
| 规划文档 → 读者 | ROADMAP/VERIFICATION/STATE 是后续会话的决策依据,失实的"已交付"标记会误导后续规划 |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-260912-01 | Tampering | git 暂存操作 | high | mitigate | 严禁 -A/-u/./目录前缀批量暂存;9 个显式路径 + 提交前 git diff --cached --name-only 断言 + 提交后 diff-tree 逐路径比对三重闸;node_modules 仍 untracked、pkg/tts 幻影仍在作零裹挟实证 |
| T-260912-02 | Tampering | 08-VERIFICATION.md 历史表格 | medium | mitigate | 只追加注记不改写表格——正向门 grep "backend never emits" = 3 锁定历史行原样;注记先 grep 复核代码证据再落笔,拒绝无凭注记 |
| T-260912-03 | Repudiation | ROADMAP 交付可追溯性 | medium | mitigate | 每段 Superseded 记录锚定 issue 文件 Status 行(done 日期 + commit 清单),与 8.1 已有范本同构;提交带 Co-Authored-By trailer |
| T-260912-04 | Repudiation | STATE.md 提交归属 | low | mitigate | STATE/SUMMARY 由 orchestrator 统一提交;Task 3 先提交 VERIFICATION 再编辑 STATE,时序上杜绝误暂存;git status 终检 STATE 为未暂存 M |
</threat_model>

<verification>
- Task 1:验证门(build + 三包 -count=1 测试)绿 → 恰 9 文件 chore 提交 → TASK1_OK;红灯路径零提交并如实记 incomplete。
- Task 2:ROADMAP 五段 Superseded 三件套齐、"delivered directly as ticket" 计 6、占位计划行清零、docs 提交恰一文件 → TASK2_OK。
- Task 3:证据 grep 四条全非空 → 注记块落位且历史表格原样 → docs 提交恰一文件 → STATE 四处前移且未暂存 → TASK3_OK。
- 三个提交主题分别为 chore: land workspace hygiene…、docs(roadmap): mark phases 8.2-8.6…、docs(verification): annotate phase 08 stale FAILs…,均带 Co-Authored-By 尾行。
</verification>

<success_criteria>
- 工作区 9 文件真实改动(+96/-97)以恰一个原子提交落库,CRLF 幻影与 untracked 杂物零裹挟(git status 复核)。
- ROADMAP 8.2–8.6 与 8.1 模式完全同构,账面与"tickets 03–07 已直达交付"的现实一致,每条记录可溯源到 issue Status 行。
- 08-VERIFICATION 的 4 条 FAIL 有了带代码证据的过时注记,历史验证记录未被篡改。
- STATE 的 stopped_at/Next/Resume 全部指向唯一未决项:31 项 iPad 真机 UAT(UAT-CHECKLIST.md)。
- executor 提交恰 3 个(chore ×1 + docs ×2);STATE.md 与 SUMMARY 留在工作区由 orchestrator 收口。
</success_criteria>

<output>
Create `.planning/quick/260912-eam-gofmt-roadmap-8-2-8-6-ticket-state-md-08/260912-eam-SUMMARY.md` when done
</output>
