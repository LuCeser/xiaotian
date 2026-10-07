---
gsd_state_version: 1.0
milestone: v2.0
milestone_name: realtime-tutor — ACTIVE
current_phase: 13.1
current_phase_name: 三段式默写课堂（默写→检查→统一反馈）
status: executing
stopped_at: Completed 13.1-02-PLAN.md
last_updated: "2026-10-07T12:06:57.989Z"
last_activity: 2026-10-07
last_activity_desc: Phase 13.1 execution started
state_head: 65f5f7133434628e75acd8f143dde216e36dd11f
progress:
  total_phases: 5
  completed_phases: 0
  total_plans: 20
  completed_plans: 14
  percent: 0
---

# Project State

## Product Acceptance Rebaseline — 2026-09-30

产品验收未通过。v1.0 superseded / override_closeout，旧 Phase 10～12 不再执行；新里程碑 v2.0 realtime-tutor。Phase 13～16 对应 R0～R3。MiMo 可替换接口、自研 WebSocket→pion WebRTC、服务端状态机幂等推进、真实 FSRS 读写和真机 RT 验收为当前范围。

## Project Reference

See: .planning/PROJECT.md (updated 2026-10-04)

**Core value:** 学生在浏览器与家教自然实时交流，理解上下文、允许打断、等待纸笔书写，学习结果进入真实记录。
**Current focus:** Phase 13.1 — 三段式默写课堂（默写→检查→统一反馈）

## Current Position

Phase: 13.1 (三段式默写课堂（默写→检查→统一反馈）) — EXECUTING
Plan: 3 of 7
Status: Ready to execute
Last activity: 2026-10-07 — Phase 13.1 execution started
Next: /gsd-plan-phase 13.1 → 13-13 VPS 部署+正式验收

13-01～13-06 的实时语音基础已实现；状态机默写转向尚待执行 13-07～13-13，RT-01～RT-10 全部待验收。起算日 2026-09-30（规划计时），第一周/第三周/第六周截止为 2026-10-07/10-21/11-11 北京时间 23:59；不因迁移阶段或供应商重置。

本轮规划检查：7/7 待执行计划结构有效，7/7 阶段需求覆盖；CONTEXT 决策覆盖工具因无可跟踪 ID 跳过，独立检查已核对现行能力与计划的一致性。S-01～S-06、HIGH-1 及评审修正意见已落实。13-07 历史波次的无待执行依赖仅为 INFO；7 条未分类边界探测保留 unresolved，评级优先级及真机证据仍待人工检查。本轮仅修改规划文档，未运行实现测试、部署或真机验收。

## Performance Metrics

v2.0：4 个活跃阶段，Phase 13 已完成 13/13 个实施计划（13-12 按用户改判本机 E2E 口径收口），下一计划 13-13（Wave 7：VPS 部署+正式验收）；产品验收尚未通过，待用户三项（本机能力认证/iPad 真机认证/评级映射追认）与 13-13。历史统计保留于 milestones/v1.0-STATE.md，不用于新里程碑进度。
**Per-Plan Metrics:**

| Plan | Duration | Tasks | Files |
|------|----------|-------|-------|
| Phase 13.1 P01 | 309 min | 3 tasks | 17 files |
| Phase 13.1 P02 | 244 min | 3 tasks | 8 files |

## Accumulated Context

以下保留旧里程碑的决策和待办登记用于追溯；旧阶段命令、历史缺陷判断及快速任务记录不构成当前任务建议。当前执行只以 Phase 13～16、RT-01～RT-10 和日期检查为准。旧登记的未解决状态见 milestones/v1.0-CLOSEOUT.md，不能自动恢复旧阶段。

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- [Phase ?]: PythonConfig 采用 13 个操作性字段（路径/超时/并发），照抄 MemoryConfig 范式；密钥经 os.Getenv 注入子进程而非入 Config（D-05）
- [Phase ?]: vendor 脚本逐字节拷入 third_party/python/（D-03）；fsrs 包名经上游 requirements.txt 确认正确，保持 fsrs>=0.0.1 不变
- [Phase 03]: pybridge.DueCard/UpdatedCard 字段照 vocab_manager.py 真实 JSON 契约（id/word/meaning/example/unit/state/due/stability/difficulty；word/rating/next_due/state/stability），非 plan 草案的 translation/pronunciation/[]string——Rule 1 修正
- [Phase 03]: pybridge semaphore.acquire 加 fast-path ctx.Done() 检查避免 Go select 非确定性（已取消 ctx + 空位时随机选分支）——Rule 1 修正
- [Phase 03]: pybridge 公共契约层 logger-free：接口/类型/纯函数/信号量不注入 infra.Logger；日志注入点在 Plan 03-03 Bridge 实现（D-25 canonical）
- [Phase ?]: Plan 03-03 Bridge: NewBridge 返回 (*Bridge, error) 照抄 NewConnectionRegistry DI 范式（cfg 逐字段校验 + nil-logger 替换）；RunDue/Review/Pregenerate 加 stub 返回 ErrBridgeNotImplemented 让 var _ Executor 编译期检查就位，Plan 03-04/05 替换时同 commit 删除 sentinel
- [Phase ?]: Plan 03-03 childEnv 加 PYTHONUNBUFFERED=1 强制 stdout/stderr 在 Wait 返回前 flush（Rule 2 correctness，避免 SIGKILL 时漏捕上游诊断）；setProcessGroup 双 build-tag 隔离照 main.go 第 12-13 行 SIGTERM 约定同类
- [Phase ?]: Plan 03-04: argv 构造用 cfg.VocabScript 直接作 args[0]（照 RESEARCH+plan），不主动 filepath.Join(ScriptsDir, VocabScript)——调用方对路径形式负全责
- [Phase ?]: Plan 03-04: ErrPythonEmpty 在 RunDue/RunReview 显式前置 len(stdout)==0 检查（Rule 1），让 4 类 sentinel 真正互斥可达，对齐 executor.go 契约
- [Phase ?]: Plan 03-04: reviewArgs 自动 size-based 选 --results vs --file（plan 明确路径），不读 opts.UseFile/ResultsFile；nil Results 兜底为空 map 防 'null' 被脚本拒（Rule 2）
- [Phase ?]: Plan 03-05: pregenArgs 对空 Words/Language 在 argv 层返回 error（脚本 required=True 字段提前拦下），避免无谓 subprocess spawn + 错误消息更精确
- [Phase ?]: Plan 03-05: Bridge.pregenSem cap=1 专用串行化（独立于 D-02 全局 sem），防 fcntl.flock 全局锁竞争致 exit 0 + 空 stdout 静默失败；acquire 在 b.run 前让排队 caller 不饥饿 due/review 路径
- [Phase ?]: Plan 03-05: classifyPregenResult 提取为纯函数让 4 类 sentinel 分支无 subprocess 表驱动可测（plan 明确允许）；Pitfall 6 空 stdout exit 0 严格返回 ErrPythonEmpty 绝非性零路径成功
- [Phase ?]: Plan 03-05: ErrBridgeNotImplemented + RunPregenerate stub 全删除（Phase 3 完结），var _ Executor = (*Bridge)(nil) 现完全靠 RunDue/RunReview/RunPregenerate 三个真方法通过编译期检查
- [Phase 04]: modernc.org/sqlite v1.55.0 作为纯 Go SQLite 驱动（CGO_ENABLED=0 跨平台契约）
- [Phase 04]: DatabaseConfig 仅含 Path 字段，pragmas（WAL/busy_timeout/foreign_keys）由 database.Open 拥有，不在 Config 中暴露
- [Phase 04]: CGO_ENABLED=0 在 Makefile build target 和 CI workflow 中锁定，防止隐式 CGO 依赖
- [Phase 09]: Plan 09-01: Go 默认 DB 路径优先用 python.vocabDBPath（database.path 为空时）——CardRepository 必须与 Python 写同一 vocab DB（多进程 WAL），否则读路径 "no such table: cards" — D-12 读路径（Go 经 CardRepository 读 SQLite）要求 Go 打开的库文件与 python.vocabDBPath 相同；旧的无条件 $HOME/.home-tutor-agent/vocab.db 默认值让读路径 500。同 commit 记录：addArgs 恒用 list-mode JSON 保住 example 字段；MgmtTimeout 为 Phase 9 管理命令共享超时
- [Phase 09]: [Phase 09]: Plan 09-02: fsrs_config 加载器对缺失/非法 KEY 做整文件失效处理（两个旋钮一起回退默认），绝不做部分配置——与 plan 的 any-failure 语义一致 — 部分配置会让回退语义变得难推理；计划文本明确要求 any failure 时用完整默认值构造 Scheduler
- [Phase 09]: [Phase 09]: Plan 09-02: loader 的 Warning 在模块导入时打到 stdout（先于命令 JSON），bridge 的 extractTrailingJSON 天然容忍——已由完整 py-e2e 套件验证而非假设 — Pitfall 9：损坏的 fsrs_config.json 不得弄垮学生听写路径；Warning+回退是唯一安全出口
- [Phase 09]: [Phase 09]: Plan 09-02: cmd_edit 在 commit 后重新 SELECT 打印更新后的 word，JSON 契约始终反映已提交状态 — 09-03 的 RunEdit 直接依赖该 JSON 的真实性；打印旧值会造成契约谎言
- [Phase 09]: unitsResponse reuses database.UnitCount directly so the GET /units wire shape has one definition and cannot drift — Handler-local duplicate structs drift silently when the repo type changes; embedding the repo type keeps endpoint and repository in lockstep (Plan 09-04)
- [Phase 09]: Plan 09-04 resume: interrupted Task 2 work adopted after diff review instead of rewritten; only its seed INSERT arg-count bug fixed — The partial work already mirrored listWords conventions and the plan test spec; rewriting would discard sound design and add churn (Plan 09-04)
- [Phase 09]: [Phase 09]: Plan 09-05 mutation handlers share one mapBridgeError — vocab_manager.py stdout diagnostics ("Card N not found"/"No cards found") map to 404 not_found truncated to 300 runes; the four pybridge sentinels map to 502 python_failed — One mapping cannot drift per-handler; Pitfall 2 closes end-to-end at the API layer and T-09-07 keeps subprocess output bounded in the error envelope
- [Phase 09]: [Phase 09]: Plan 09-05 fsrs_config.json written atomically (same-directory temp + Sync + os.Rename, failure removes temp and preserves original); FSRSSettings struct declaration order fixes on-disk key order — A half-written config would poison every future vocab_manager.py spawn (loader falls back wholesale); atomic replace + fixed key order keeps the Go writer key-for-key with the Python reader (D-14)
- [Phase 09]: Plan 09-06 resume: interrupted Task 2 work (781+99+25+189 lines across parent-modes.js/parent-app.js/parent.html/parent.css) adopted after per-file diff review against the plan spec instead of rewritten — every spec item (tabs, D-06 radio validation, D-08 optional fields, shared datalist, preview table, transparency banner, createElement/textContent) was already present and verified green; zero fragments needed rewriting (Plan 09-04 precedent) — Plan 09-06 resume: interrupted Task 2 work (781+99+25+189 lines across parent-modes.js/parent-app.js/parent.html/parent.css) adopted after per-file diff review against the plan spec instead of rewritten — every spec item (tabs, D-06 radio validation, D-08 optional fields, shared datalist, preview table, transparency banner, createElement/textContent) was already present and verified green; zero fragments needed rewriting (Plan 09-04 precedent)
- [Phase 09]: Batch result banner names the skipped words via a pre-commit GET /words snapshot: the API returns only {added,skipped} counts, but skips are UNIQUE(word,category), so skipped == pasted ∩ existing — the snapshot derives the readable list; on snapshot failure the banner still renders the honest counts (transparency never depends on the extra request) — Batch result banner names the skipped words via a pre-commit GET /words snapshot: the API returns only {added,skipped} counts, but skips are UNIQUE(word,category), so skipped == pasted ∩ existing — the snapshot derives the readable list; on snapshot failure the banner still renders the honest counts (transparency never depends on the extra request)
- [Phase 09]: The batch commit is refused client-side above 100 entries (MAX_BATCH_ENTRIES mirrors pkg/web/api maxEntries) with a split-paste hint, and at zero valid rows the commit button stays disabled — bounded request bodies enforced before the request is ever sent (threat-model trust boundary, Plan 09-01 caps) — The batch commit is refused client-side above 100 entries (MAX_BATCH_ENTRIES mirrors pkg/web/api maxEntries) with a split-paste hint, and at zero valid rows the commit button stays disabled — bounded request bodies enforced before the request is ever sent (threat-model trust boundary, Plan 09-01 caps)
- [Phase ?]: [Phase 09]: Plan 09-07 browse unit-level 重命名/删除 act per-category across the categories present in the group's rows, and header actions are WITHHELD in the 全部+search combination — destructive confirm copy must always match the DELETE {category, unit} server scope (T-09-05) — The mutation API requires category; a merged 全部 group can span both categories — iterating the rows' categories is exact without an extra fetch, and in the one ambiguous combination the group shows no unit-level buttons instead of a count the API would not honor
- [Phase ?]: [Phase 09]: Plan 09-07 unit-group headers build the 48px aria-expanded toggle as a SIBLING of the 重命名/删除 action buttons — buttons are never nested (invalid HTML breaks the accessibility tree); localStorage parent.units.collapsed stores a JSON array of unit names, re-read defensively on every render — UI-SPEC button-headers-with-aria-expanded is preserved per-toggle while keeping valid HTML; the per-render re-read makes collapse state self-healing against manual storage edits
- [Phase ?]: [Phase 09]: Plan 09-07 SettingsCard interval slider caps at 365 (plan spec) though the API allows 36500, and the edit modal sends ALL four fields (provided-empty clears the column) because the modal renders the word's full current state — Values above 365 were never settable from this UI; a full-state editor matching visible fields makes PUT semantics (empty = clear, D-08 partial-form valid) obvious to the parent
- [Phase 09]: E2E net-zero pattern formalized (09-08): dedicated e2e-* units only, finally-shaped API cleanup backstops the UI deletion path, suite idempotent across consecutive runs; the duplicate-skip banner is made deterministic by API-seeding the duplicate target before the paste
- [Phase 09]: Backstop styles shipped under Rule 2 in the e2e plan (09-08): the 2-line meaning clamp (-webkit-line-clamp on .word-meaning) and unit-name title attribute asserted by the must-haves were absent from the Plan 09-07 styles — fixed in the Task 2 commit and asserted behaviorally (rendered height within 2 line-heights; Chromium computes display:-webkit-box as flow-root while the clamp applies)
- [Phase 13]: R0 交互模型转向「状态机默写助手」（2026-10-03 用户决策；本机预验收 rt-6 证据：12 轮对话 0 次课堂工具调用）。服务端状态机控制节奏：FSRS 取到期词（上限 10，不足取全部）→ 老师开场报词 → 等「好了/可以了/下一个」确认或 60 秒超时自动推进 → 支持重读/慢读、不会写提示、词义解释、暂停继续口令 → 完结总结写学习记录。大模型仅做意图识别与话术生成，不再拥有课堂节奏裁量权。 — 用户原始能力模型是默写助手；R0 按 13-CONTEXT 以「自由对话+可选工具」验证实时链路，体验与产品定义错位。属规划阶段翻译损耗而非执行偏移——执行与计划一致（证据：13-CONTEXT L16/L17/L20、RT-06 原文、PROJECT.md「FSRS R2 贯通」）。本决策推翻：固定 3 词不选词、沉默不跳词（RT-06 原文）、FSRS 排 R2。伴随缺陷一并修：MiMo invalid API response（实测×4）、VAD 0 秒误触发、首问候缺失（13-03/04/05 实现缝隙）。
- [Phase 13.1]: 13.1-01 三段式tracer落码：完结写评级改为检查段(stage=checking持久化)→逐词manual改判→check_submit一次统一提交FSRS(D-02/D-07)→含对错战果总结过ACK门(D-04)；checkRating与wordRating同族共用rating常量禁分叉 — D-01/D-02/D-04/D-07模型改造(13.1-CONTEXT显式取代13-CONTEXT完结即写条款)；幂等闸=lessons.stage checking→submitted迁移+request_id去重+expected_version乐观锁，二次提交回放零副作用
- [Phase 13.1]: 打码权威在服务端发送侧（spoken+display 双轨）：TTS/LLM 语境/trim 恒原文，字幕与词表按学生模式脱敏，词明文不再上线；前端只画收到的内容
- [Phase 13.1]: maskTranscript 以 grading.Normalize 为唯一比对口径（按词 script 选 en/zh），替换落在原始 span，窗口=归一化词长+8，不写第二套归一化
- [Phase 13.1]: word_revealed 每个首提交 advance 无差别下发（含末词），幂等回放/冲突不发，测试模式完全不发；repeat/slow/resume 用显式打码模板变体避免叠词

### Pending Todos

源自 2026-07-25 架构评审（/architecture）：

- [x] **统一连接注册表** — `infra.ConnectionRegistry` vs `ws.Hub` 双轨制，需 ADR 拍单一真相源（评审 P0-2）。`todos/pending/2026-07-25-unify-connection-registry-adr.md` → Resolved: Phase 07-03 D-04 — Hub is single source of truth; ConnectionRegistry soft-deprecated from metrics path; Hub.Stats() feeds ServerSnapshot via SetConnectionStatsProvider
- [x] **解决 WorkerPool 建而不用** — 无任何 Submit 调用，接线 or 登记 deferred（评审 P1-3）。`todos/pending/2026-07-25-resolve-workerpool-unused.md` → Resolved: Phase 07-03 D-06 — session.audit.go calls pool.Submit for Create/UpdateState/AppendEvent (first real caller)
- [x] **Phase 4 前定 FSRS+SQLite 所有权** — Python(vocab_manager) 已实现 FSRS+SQLite，Phase 4/5 重新定义范围，需 ADR（评审 P1-4）。`todos/pending/2026-07-25-fsrs-sqlite-ownership-adr.md` → Resolved: ADR created at `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md`
- [ ] **仓库卫生批处理** — README 漂移 / gitignore 根级二进制 / 残留 .gitkeep 清理（评审 P2-5/P2-6/P3-7）；**2026-09-09 扩容为总纲**：根目录未跟踪调试产物（test-\*.html/js/py、check_\*.py、nul、\_\_pycache\_\_ 等）+ 一次性分析文档（BUG-FIX-\*.md、SESSION-\*.md 等）盘点分类后合并或删除。`todos/pending/2026-07-25-repo-hygiene-readme-gitignore-cleanup.md`
- [ ] **iOS WebSocket 音频播放问题研究** — iPad Safari 上 WebSocket 流式 TTS 播放问题，需调研当前方案能否达到预期效果。`todos/pending/2026-08-08-research-and-fix-ios-websocket-audio-playback-issues-for-ipa.md`

> 注：评审 P0-1（WS 层未接入运行时，服务监听不到 :8080）未建 todo。
> **2026-07-25 更新**：Phase 02 手动收尾已完成（02-02/02-03 SUMMARY 补写、flaky test 修复、ROADMAP 3/3）——但这两个计划交付的是 WS *库*（Hub/Client/Server/Router），**并非** main.go 运行时接线。P0-1（main.go 监听 :8080 + mux.HandleFunc("/ws", server.HandleWebSocket)）仍属 **Phase 1 Plan 08** 范围，当前未交付、未建 todo —— 建议后续为 P0-1 单独建 todo。

### Blockers/Concerns

None yet.

### Quick Tasks Completed

| # | Description | Date | Commit | Status | Directory |
|---|-------------|------|--------|--------|-----------|
| 260801-fh3 | 让 Home Tutor Agent 在 Windows 本机能跑起来调试 | 2026-08-01 | 077e77d | | [260801-fh3-home-tutor-agent-windows](./quick/260801-fh3-home-tutor-agent-windows/) |
| 260801-ge1 | 接入 WebSocket 运行时(P0-1) | 2026-08-01 | bfb24d3 | Verified | [260801-ge1-websocket-p0-1](./quick/260801-ge1-websocket-p0-1/) |
| 260801-kdq | 根治 ws TestHubStopsOnContextCancel flake | 2026-08-01 | b65ad5e | | [260801-kdq-ws-testhubstopsoncontextcancel-flake](./quick/260801-kdq-ws-testhubstopsoncontextcancel-flake/) |
| 260802-ldq | 创建最小化前端验证界面，用于测试WebSocket连接、FSRS单词选择和TTS音频流播放 | 2026-08-02 | ad599a5 | Verified | [260802-ldq-websocket-fsrs-tts](./quick/260802-ldq-websocket-fsrs-tts/) |
| 260930-t8z | 重定基规划文档：产品验收矫正（R0-R3主线，ADR-0002转正） | 2026-09-30 | a7fcfb4 | | [260930-t8z-r0-r3](./quick/260930-t8z-r0-r3/) |
| 260823-cff | Fix category field in session_ready response for correct speech recognition language | 2026-08-23 | e8556fa | complete | [20260823-category-field-fix](./quick/20260823-category-field-fix/) |
| 260823-fvx | 重构小米ASR实现以符合官方API规范 | 2026-08-23 | 9a8a7b3,3edb20a,cb72cab | complete | [260823-fvx-asr](./quick/260823-fvx-asr/) |
| 260823-j2n | 收尾 speech-recognition-redesign Ticket 01 状态文档（issue 勾选/总结更新/STATE 补 commit） | 2026-08-23 | 371010c,eb58b7e | complete | [260823-j2n-speech-recognition-redesign-ticket-01-is](./quick/260823-j2n-speech-recognition-redesign-ticket-01-is/) |
| 260823-sdx | 修复 session.go 过期 WordComplete 事件竞态：stream generation 计数器 | 2026-08-23 | 3a1fc59 | complete | [260823-sdx-session-go-wordcomplete-stream-generatio](./quick/260823-sdx-session-go-wordcomplete-stream-generatio/) |
| 260823-t13 | 提取 PAUSED 5 分钟超时到 Config.SessionTimeout.PausedStateTimeout | 2026-08-23 | 799de7c | complete | [260823-t13-paused-5-config-sessiontimeout-pausedsta](./quick/260823-t13-paused-5-config-sessiontimeout-pausedsta/) |
| 260823-ti8 | 文档化 onTransition 无锁读 repeatPending/prevState 的单写者不变量 | 2026-08-23 | f8e5e31 | complete | [260823-ti8-ontransition-repeatpending-prevstate](./quick/260823-ti8-ontransition-repeatpending-prevstate/) |
| 260823-tyq | 补全 DictationControls 六个方法的 JSDoc 文档注释 | 2026-08-23 | d6c046b | complete | [260823-tyq-dictationcontrols-jsdoc](./quick/260823-tyq-dictationcontrols-jsdoc/) |
| 260823-u8t | ticket 02 评审小清理集四子项（60px 触控高度/测试错误检查/_applyPaused 防护/godoc TODO） | 2026-08-23 | 45435bb | complete | [260823-u8t-ticket-02](./quick/260823-u8t-ticket-02/) |
| 260823-urr | 收尾 ticket 02 状态文档（issue 置 done 6/7 勾选 + Completion note；STATE 转向 Phase 8.2/iPad UAT） | 2026-08-23 | 3d2cb6f | complete | [260823-urr-ticket-02](./quick/260823-urr-ticket-02/) |
| 260823-vn9 | ticket 02 评审终局清理集（update() JSDoc 死分支如实描述/_applyPaused JSDoc 收紧为类型校验/StaleWordCompleteDropped 测试去时序依赖/ROADMAP 8.1 对齐实况） | 2026-08-23 | 4f5c644,0ff7cca | complete | [260823-vn9-ticket-02-code-review-1-ui-components-js](./quick/260823-vn9-ticket-02-code-review-1-ui-components-js/) |
| 260823-x38 | 补齐 ticket 02 生命周期文档提交清单（issue 02 Status/Completion note 补终审结论、STATE stopped_at×2、ROADMAP 8.1 ticket 行——均补 4f5c644/0ff7cca） | 2026-08-23 | a1ae3f6,f7b92fd,d3b5fa2 | complete | [260823-x38-ticket-02-4f5c644-0ff7cca-roadmap-8-1-e0](./quick/260823-x38-ticket-02-4f5c644-0ff7cca-roadmap-8-1-e0/) |
| 260909-rih | 验证并提交工作区清理改动：Rating 字段下线、DictationFinished 收敛、测试辅助重构 | 2026-09-09 | 2fa555f | complete | [260909-rih-rating-dictationfinished](./quick/260909-rih-rating-dictationfinished/) |
| 260912-eam | 归账清理：9 文件工作区改动落库（gofmt/e2e helper/gitignore）；ROADMAP 8.2–8.6 标记 tickets 03–07 直达交付；08-VERIFICATION 过时 FAIL 注记 | 2026-09-12 | 9e3057c,8411fa8,82e8aa8 | complete | [260912-eam-gofmt-roadmap-8-2-8-6-ticket-state-md-08](./quick/260912-eam-gofmt-roadmap-8-2-8-6-ticket-state-md-08/) |
| 260928-tsp | 测试提速：live 测试加 -short 跳过 + mimo 重试延迟压缩 + run.ps1 增量编译 + mimo RealAPI skip & run.ps1 hint restore | 2026-09-28 | 33f8c52,87534a4 | complete |  |
| 20 | 新建 13-UAT-ISSUES-20261006 本机验收问题台账（U-1 E2E 残留三处已清/防复发待办、U-2 去重版本倒退缺陷立账未修、U-3 确认误判观察） | 2026-10-06 | 4b39357 | — | — |
| 261006-u2o | U-9：评级映射实现＋逐词提示计数（裁定口径在 CHECKPOINTS；将来降级为 13.1 的未核对兜底路径） | 2026-10-06 | f948b17 | complete | [261006-u2o-u-9-checkpoints-13-1](./quick/261006-u2o-u-9-checkpoints-13-1/) |
| 261007-0ct | U-2：去重版本倒退防护——重置课行的过期去重记录判定失效＋同事务清除重执行推进（pkg/lesson/executor.go） | 2026-10-07 | 7fdfac7 | complete | [261007-0ct-u-2-pkg-lesson-executor-go](./quick/261007-0ct-u-2-pkg-lesson-executor-go/) |
| 261007-22t | U-1：E2E 隔离规范资产化——四层设计 L3 落地（tests/e2e-rt/real 五件套＋npm run e2e:rt-real＋docs/E2E-ISOLATION-SPEC.md，台账防复发闭环） | 2026-10-07 | 9b11c8f,09dc38d,c1d3c94 | complete | [261007-22t-e2e-memory-u-1](./quick/261007-22t-e2e-memory-u-1/) |

### Roadmap Evolution

- Phase 07.1 inserted after Phase 7: Research and fix iOS WebSocket audio playback issues for iPad Safari browsers (URGENT)
- Phase 8.1 edited: Re-scoped to ticket 02 (student-paced Mimo TTS dictation); auto-advance plans superseded by user decision 2026-08-23
- Phase 8.6 inserted after Phase 8: Phases 8.2-8.6 inserted from speech-recognition-redesign tickets 03-07 (voice commands, photo OCR, batch submission, auto grading, multi-user); ticket 08 absorbed into Phase 12; FEED re-sourced in Phase 10. Ticket set = single source of truth (user decision 2026-08-23)
- Phase 13.1 inserted after Phase 13: 三段式默写课堂（默写→检查→统一反馈）：R0 验收问题 U-5/U-6/U-7/U-10 讨论收敛；三段式先行于 13-13 部署（用户 2026-10-06）

## Deferred Items

旧里程碑以 superseded / override_closeout 关闭：审计有 17 项开放登记及未验收阶段，详见 [关闭记录](milestones/v1.0-CLOSEOUT.md) 和 [原始审计](milestones/v1.0-ARTIFACT-AUDIT.json)。它们未被判为通过。

| Category | Item | Status | 接收范围 |
|---|---|---|---|
| human_uat | Phase 9 长文本/省略号/预览视觉确认 | 未执行 | Phase 16 人工验收 |
| human_uat | Phase 9 真 iPad 家长页视觉/触控/缩放 | 未执行 | Phase 16 真机验收 |
| roadmap | 旧 Phase 10～12 | superseded | Phase 13～16 / RT-07～RT-10 及全量复验 |
| historical_records | 历史调试/快速任务/待办/验证登记 | 原状态保留，不阻塞 R0 | 相关问题仅在影响新体验时处理 |

## Session Continuity

Last session: 2026-10-07T12:04:13.256Z
Stopped at: Completed 13.1-02-PLAN.md
Resume file: None
