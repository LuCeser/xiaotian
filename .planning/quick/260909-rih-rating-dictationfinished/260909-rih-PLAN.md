---
phase: 260909-rih
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - cmd/service/batch_test.go
  - cmd/service/handlers.go
  - cmd/service/photo.go
  - cmd/service/voice_fsm_test.go
  - pkg/session/fsm.go
  - pkg/session/session.go
  - pkg/web/static/js/app.js
  - pkg/web/static/js/state-manager.js
  - pkg/web/static/js/ui-components.js
autonomous: true
requirements: []
must_haves:
  truths:
    - 'go vet ./... 与 go test ./... 全树全绿,三个前端 JS 文件 node --check 通过,cmd/service 与 pkg/session 两目录 gofmt -l 零输出——工作区已有的 9 文件清理改动(+91/-62)通过整树验证门。任何一项红:立即停止,不尝试任何范围外修复,SUMMARY 状态记 incomplete 并附失败输出尾部。(规划期预跑实证全绿:vet 0、test 全 ok、JS_OK、gofmt 两目录零输出;执行期以执行时重跑结果为准)'
    - '恰一个内聚提交——git diff-tree --no-commit-id --name-only -r HEAD 恰为 9 个文件(cmd/service ×4 + pkg/session ×2 + pkg/web/static/js ×3),不多不少;提交主题行以 refactor(batch): 开头、以 (ticket 05) 结尾,正文含 5848db4 评审轮跟进的五条要点,末行为 Co-Authored-By: Claude Code 尾行'
    - '.gitignore 不入提交——工作区另有范围外的 .gitignore 改动(新增 .agents/.claude/.codex 忽略项,规划期发现的第 10 个改动文件);提交后 git status 仍显示 M .gitignore。所有 untracked 文件(node_modules/、根级 *.py、.scratch/、测试 HTML 等)一律不 add、不改动'
    - '提交后 9 个 payload 文件相对 HEAD 零 diff(cmd/service、pkg/session、pkg/web/static/js 三前缀下 git diff --name-only HEAD 为空),工作区 M 项仅剩 .gitignore'
  artifacts:
    - 单个 git 提交(9 文件,+91/-62;内容为 ticket 05 评审跟进清理:batchWordResult 去 Rating 字段、DictationFinished() FSM 镜像守卫、photo.go 分支合并、batchMode 迁 state manager、readFrameUntil 测试助手泛化)
    - .planning/quick/260909-rih-rating-dictationfinished/260909-rih-SUMMARY.md(执行器写盘但不提交——docs 提交由 orchestrator 负责)
  key_links:
    - handleSubmitAnswers 的 DICTATING 守卫 ↔ s.DictationFinished() ↔ FSM submit_answers 边条件(二者同源于 session.onLastWord())——handler 镜像不再有词相等近似漂移,5848db4 评审遗留的"word equality approximation"被本提交替换
    - batchWordResult 去掉 Rating 字段 ↔ 前端 app.js/state-manager.js/ui-components.js 不再消费 rating ↔ 服务端 UpdateRatings 仍收 ratings map——FSRS 评分只走服务端,线上契约两端一致
    - batch-mode-toggle 的 change 监听 ↔ stateManager.setState({batchMode}) ↔ app.js 各流程门读 state.batchMode——batch 模式权威从 DOM 轮询(batchModeEnabled)迁到 state manager
---

# 快速任务:验证并提交工作区已有的 9 文件清理改动(ticket 05 评审跟进)

<objective>
本任务是 **验证并提交(verify-and-commit)**,不是写新代码。改动已作为未提交的工作区状态存在于主 checkout(9 个 tracked 修改文件,+91/-62,规划期 git diff --stat 实证)。执行器必须**顺序运行在主 checkout 上**——本次运行禁用 worktree 隔离,因为 payload 是未提交状态,新 worktree 看不到它。

payload 内容(规划期已逐文件读 diff 实证):

1. **cmd/service/handlers.go**:batchWordResult 去掉 Rating 字段(FSRS 评分可由 Correct 完全推导,只走服务端 UpdateRatings 调用);DICTATING 守卫改用 s.DictationFinished()(不再镜像词相等检查);未作答单词短路判 incorrect/again,不再调 compareAnswer;Results 预分配 make([]batchWordResult, 0, len(words))。
2. **pkg/session/session.go**(+17):新增 onLastWord() 私有助手(调用方持锁)与 DictationFinished() 公共方法(RLock 线程安全)。
3. **pkg/session/fsm.go**:三处内联 s.wordIndex >= len(s.wordQueue)-1 边条件改调 s.onLastWord()。
4. **cmd/service/photo.go**:ok 与 ErrLowConfidence 两个应答分支合并(LowConfidence: err == ocr.ErrLowConfidence)。
5. **pkg/web/static/js/app.js / state-manager.js / ui-components.js**:batchModeEnabled() DOM 轮询迁为 state manager 权威(toggle change 监听同步 state.batchMode 与 per-word photo host 可见性);state-manager 增 batchMode: false 初始态;batch 表单行初始化循环简化、values() 去 null 守卫。
6. **cmd/service/batch_test.go / voice_fsm_test.go**:readControlUntil 泛化为 readFrameUntil(t, conn, msgType, substr),readControlUntil/readErrorUntil 变为钉住帧类型的薄包装;断言不再检查 Rating。

**范围外(勿动)**:工作区另有 M .gitignore(新增 .agents/.claude/.codex 忽略项——ADE 工具配置,规划期发现的第 10 个改动文件,不属于本 payload);大量 untracked 文件(node_modules/、根级 *.py、.scratch/、测试 HTML 等)一律不碰。

Purpose: 给 ticket 05 双轴评审轮(5848db4)的后续清理一个验证背书 + 原子落库。
Output: 恰一个 refactor(batch) 提交,恰含 9 文件;SUMMARY 写盘不提交。
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md

规划期事实(执行器无需重读全部 diff,以下为已实证依据):
- 9 文件清单与 +91/-62 来自 git diff --stat(2026-09-09 规划时点)。
- 规划期预跑全绿:go vet ./... exit 0;go test ./... 全 ok(pkg/mimo 约 21 秒属正常,缓存 ok 也算绿);node --check 三 JS 通过;gofmt -l cmd/service pkg/session 零输出。
- gofmt 注意:全树 gofmt -l 会列出 44 个**范围外**预存在漂移文件(pkg/infra、pkg/mimo、pkg/tts、pkg/pybridge、pkg/fsrs、pkg/crc16、pkg/voice 等,CRLF 所致)——这就是格式门**只扫 cmd/service 与 pkg/session 两目录**的原因,严禁扩大到全树。
- git add 时会打 "LF will be replaced by CRLF" 警告——本仓库 Windows checkout 的正常现象,不是错误,忽略。
- 提交信息模板里的 5848db4 指前一轮评审修复提交(引入了本 payload 替换掉的 word-equality 镜像),措辞已对照该提交全文核实。
</context>

<tasks>

<task type="auto">
  <name>Task 1: 整树验证门——vet + 全量测试 + JS 语法 + payload 目录 gofmt,任何红即停</name>
  <files>(无文件改动——纯验证)</files>
  <action>在主 checkout 依次跑四个门,全部通过才算绿:

  1. go vet ./...
  2. go test ./...(注意 pkg/mimo 约 21 秒属正常;(cached) 的 ok 也算通过)
  3. 三个前端 JS 各跑一次 node --check(app.js、state-manager.js、ui-components.js)
  4. gofmt -l cmd/service pkg/session——输出必须为空(只扫这两个 payload 目录,绝不扩大范围;全树扫描会命中 44 个范围外预存在漂移文件,见 context)

  **红灯处置(任一门失败)**:立即停止本计划,不尝试任何修复(包括看似琐碎的修复——修复超出本任务范围),不进入 Task 2,不产生任何提交;在 SUMMARY 中把状态记为 incomplete,粘贴失败输出的关键尾部(失败包名 + 断言/错误消息),并注明"验证门未过,9 文件改动保持未提交工作区状态"。

  绿灯:直接进入 Task 2。验证输出在 SUMMARY 中引用结论即可(全绿一句话 + 各门耗时)。
  </action>
  <verify>
    <automated>cd /d/workspace/ai/xiaotian && go vet ./... && go test ./... && node --check pkg/web/static/js/app.js && node --check pkg/web/static/js/state-manager.js && node --check pkg/web/static/js/ui-components.js && test -z "$(gofmt -l cmd/service pkg/session)" && echo ALL_GREEN</automated>
  </verify>
  <done>四门全绿并看到 ALL_GREEN(go vet 零报告、go test 全 ok、三 JS 语法通过、payload 两目录 gofmt 零输出);或红灯时已按处置规则停止且零提交零改动。</done>
</task>

<task type="auto">
  <name>Task 2: 精确暂存恰 9 文件,单一内聚提交(refactor(batch), ticket 05)</name>
  <files>cmd/service/batch_test.go, cmd/service/handlers.go, cmd/service/photo.go, cmd/service/voice_fsm_test.go, pkg/session/fsm.go, pkg/session/session.go, pkg/web/static/js/app.js, pkg/web/static/js/state-manager.js, pkg/web/static/js/ui-components.js</files>
  <action>四步,顺序执行:

  **第一步:精确暂存。** git add 用 9 个显式路径(一次性列出,空格分隔):cmd/service/batch_test.go cmd/service/handlers.go cmd/service/photo.go cmd/service/voice_fsm_test.go pkg/session/fsm.go pkg/session/session.go pkg/web/static/js/app.js pkg/web/static/js/state-manager.js pkg/web/static/js/ui-components.js
  严禁 git add -A、git add -u、git add .、git add 目录前缀——工作区还有范围外的 M .gitignore 和大量 untracked 文件,通配/批量暂存会把它们扫进来。git add 打出的 LF→CRLF 警告是本 checkout 正常现象,忽略。

  **第二步:暂存集断言(提交前必做)。** 运行 git diff --cached --name-only 并确认结果恰为上述 9 个路径、无任何多余项。若不符:git restore --staged 全部重来,不得带偏差提交。

  **第三步:单一提交,信息一字不差。** 主题行:

  refactor(batch): rating off the wire, DictationFinished() guard, batchMode via state manager (ticket 05)

  正文(空行接主题行,五条要点,末行前空行):

  Follow-up cleanup from the 5848db4 review round:

  - batchWordResult drops the Rating field — the FSRS rating is fully
  derivable from Correct, so it travels only in the server-side
  UpdateRatings call; the FE stops reading it.
  - The handler's DICTATING guard mirrors the FSM via s.DictationFinished()
  instead of the word-equality approximation (which could false-accept a
  duplicated queue word). pkg/session gains onLastWord() plus the
  thread-safe DictationFinished(); the FSM's three inline last-word
  checks call the helper.
  - Unanswered words short-circuit to incorrect/again without calling
  compareAnswer; photo.go merges the ok/LowConfidence reply branches.
  - FE: batchMode moves from DOM polling (batchModeEnabled) to state-manager
  authority — the toggle change listener syncs state.batchMode and the
  per-word photo host visibility; batch form row init and values()
  simplified.
  - Tests: readControlUntil/readErrorUntil collapse into a generalized
  readFrameUntil; batch assertions drop the Rating checks.

  末行(trailer):

  Co-Authored-By: Claude Code <noreply@anthropic.com>

  多行信息用单次 git commit 传入(主题 + 空行 + 正文 + 空行 + trailer,换行原样保留);一行一个 -m 分段亦可,但换行结构与逐字内容不得变。如对信息与内容对应性有疑,git diff --cached 复核后再提交。

  **第四步:提交后自查(下方 automated 全过才算完)。** .gitignore 仍为未暂存修改状态;untracked 集合原样;SUMMARY 只写盘不提交(docs 提交由 orchestrator 负责)。
  </action>
  <verify>
    <automated>cd /d/workspace/ai/xiaotian && test "$(git diff-tree --no-commit-id --name-only -r HEAD | sort | tr '\n' ' ')" = "cmd/service/batch_test.go cmd/service/handlers.go cmd/service/photo.go cmd/service/voice_fsm_test.go pkg/session/fsm.go pkg/session/session.go pkg/web/static/js/app.js pkg/web/static/js/state-manager.js pkg/web/static/js/ui-components.js " && git log -1 --format=%s | grep -q '^refactor(batch): .*ticket 05)$' && git log -1 --format=%B | grep -q '^Co-Authored-By: Claude Code <noreply@anthropic.com>$' && git status --porcelain -- .gitignore | grep -q '^ M \.gitignore' && test -z "$(git diff --name-only HEAD -- cmd/service pkg/session pkg/web/static/js)" && echo COMMIT_OK</automated>
  </verify>
  <done>HEAD 恰含 9 个 payload 文件(diff-tree 比对通过);提交主题 refactor(batch) 前缀 + (ticket 05) 结尾;正文五条要点 + Co-Authored-By 尾行俱在;.gitignore 仍为 M 且未入任何提交;三前缀(cmd/service、pkg/session、pkg/web/static/js)下工作区相对 HEAD 零 diff;看到 COMMIT_OK。</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| WS 服务端 → 前端 batch_result 帧 | 本提交把 Rating 从线上载荷移除——服务端内部数据(FSRS 评分派生)不再跨信任边界下发,收窄而非扩大暴露面 |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-260909-01 | Tampering | git 暂存操作 | high | mitigate | 严禁 -A/-u/. 批量暂存;9 个显式路径 + 提交前 git diff --cached --name-only 断言 + 提交后 diff-tree 比对三重闸 |
| T-260909-02 | Information Disclosure | batchWordResult 载荷 | low | mitigate | 已由 payload 本身缓解:Rating 字段删除,评分只留在服务端 UpdateRatings 调用内(本提交即其落库) |
| T-260909-03 | Repudiation | 提交可追溯性 | low | mitigate | 单一内聚提交 + Co-Authored-By trailer + SUMMARY 记录哈希,STATE 快速任务表登记 |
</threat_model>

<verification>
- Task 1 四门(vet / 全量 test / node --check ×3 / payload 两目录 gofmt)全绿,输出 ALL_GREEN;红灯路径有明确处置(停止 + incomplete + 零提交)。
- Task 2 提交后:HEAD 文件集恰 9、主题/正文/trailer 逐字符合、.gitignore 与 untracked 全部原样、三前缀零残留 diff,输出 COMMIT_OK。
- 规划期预跑上述全部命令均为绿——执行期以重跑结果为准,预跑结论只作可行性背书。
</verification>

<success_criteria>
- 工作区 9 文件清理改动(+91/-62)以恰一个 refactor(batch) 原子提交落库,提交信息符合仓库惯例(type(scope) 主题 + 要点正文 + Co-Authored-By 尾行,与 5848db4/37f37b4 同款)。
- 范围外内容(.gitignore 的 ADE 忽略项、全部 untracked)经 git status 复核零裹挟。
- 验证门先于提交:任何红→零提交,SUMMARY 如实记 incomplete。
- SUMMARY 写盘但未提交(orchestrator 负责 docs 提交)。
</success_criteria>

<output>
Create `.planning/quick/260909-rih-rating-dictationfinished/260909-rih-SUMMARY.md` when done
</output>
