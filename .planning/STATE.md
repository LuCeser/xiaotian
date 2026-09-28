---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
current_phase: 09
current_phase_name: parent-vocabulary-management
status: executing
stopped_at: Completed 09-05-PLAN.md
last_updated: "2026-09-28T14:54:37.346Z"
last_activity: 2026-09-28
last_activity_desc: Plan 09-01 complete
progress:
  total_phases: 16
  completed_phases: 9
  total_plans: 51
  completed_plans: 46
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-08-09)

**Core value:** Students receive personalized, real-time dictation practice with immediate feedback while parents get detailed progress tracking and the ability to influence the learning algorithm through their reviews.
**Current focus:** Phase 09 — parent-vocabulary-management

## Current Position

Phase: 09 (parent-vocabulary-management) — EXECUTING
Plan: 6 of 7 complete
Status: Ready to execute
Last activity: 2026-09-28 — Plan 09-05 complete
Next: ①执行 09-06（添加单词 UI：批量粘贴解析器 + 添加单词 双模式卡片）②随后 09-07（浏览/整理 + 学习设置 UI）③09-08 收尾 E2E；注意 VOC-01/02/03 待 09-06/07/08 SUMMARY 落地后才在 REQUIREMENTS.md 翻 Complete（shared-ID gate）

Progress: [█████████░] 90%

## Performance Metrics

**Velocity:**

- Total plans completed: 37
- Average duration: - min
- Total execution time: 0.0 hours

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| 01 | 8 | - | - |
| 02 | 3 | - | - |
| 03 | 5 | - | - |
| 04 | 5 | - | - |
| 05 | 2 | - | - |
| 06 | 4 | - | - |
| 07 | 3 | - | - |
| 08 | 7 | - | - |

**Recent Trend:**

- Last 5 plans: -
- Trend: -

*Updated after each plan completion*
**Per-Plan Metrics:**

| Plan | Duration | Tasks | Files |
|------|----------|-------|-------|
| Phase 03 P01 | 20 | 3 tasks | 10 files |
| Phase 03 P02 | ~10 | 2 tasks | 10 files |
| Phase 03 P03 | ~25 | 2 tasks | 6 files |
| Phase 03 P04 | ~12 | 2 tasks | 3 files |
| Phase 03 P05 | ~25 | 2 tasks | 5 files |
| Phase 04 P01 | ~15 | 2 tasks | 6 files |
| Phase 04 P02 | ~15 | 2 tasks | 2 files |
| Phase 08-student-web-interface P04 | 5.5 minutes | 4 tasks | 6 files |
| Phase 09 P01 | 39min | 2 tasks | 22 files |
| Phase 09 P02 | 24min | 2 tasks | 2 files |
| Phase 09 P03 | 26min | 2 tasks | 7 files |
| Phase 09 P04 | 26min | 2 tasks | 4 files |
| Phase 09 P05 | 16min | 3 tasks | 6 files |

## Accumulated Context

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

### Roadmap Evolution

- Phase 07.1 inserted after Phase 7: Research and fix iOS WebSocket audio playback issues for iPad Safari browsers (URGENT)
- Phase 8.1 edited: Re-scoped to ticket 02 (student-paced Mimo TTS dictation); auto-advance plans superseded by user decision 2026-08-23
- Phase 8.6 inserted after Phase 8: Phases 8.2-8.6 inserted from speech-recognition-redesign tickets 03-07 (voice commands, photo OCR, batch submission, auto grading, multi-user); ticket 08 absorbed into Phase 12; FEED re-sourced in Phase 10. Ticket set = single source of truth (user decision 2026-08-23)

## Deferred Items

Items acknowledged and carried forward from previous milestone close:

| Category | Item | Status | Deferred At |
|----------|------|--------|-------------|
| *(none)* | | | |

## Session Continuity

Last session: 2026-09-28T14:54:37.331Z
Stopped at: Completed 09-05-PLAN.md
Resume file: None
