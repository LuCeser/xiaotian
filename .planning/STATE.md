---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
current_phase: 6
current_phase_name: TTS Audio Streaming
status: planning
stopped_at: Phase 5 context gathered and reviewed
last_updated: "2026-08-02T06:41:30.676Z"
last_activity: 2026-08-02
last_activity_desc: Completed quick task 260802-ldq: 创建最小化前端验证界面
progress:
  total_phases: 5
  completed_phases: 5
  total_plans: 23
  completed_plans: 23
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2025-07-18)

**Core value:** Students receive personalized, real-time dictation practice with immediate feedback while parents get detailed progress tracking and the ability to influence the learning algorithm through their reviews.
**Current focus:** Phase 05 — fsrs-core-implementation

## Current Position

Phase: 6 — TTS Audio Streaming
Plan: Not started
Status: Ready to plan
Last activity: 2026-08-02 — Phase 05 complete, transitioned to Phase 6

Progress: [██████████░] 90%

## Performance Metrics

**Velocity:**

- Total plans completed: 23
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

### Pending Todos

源自 2026-07-25 架构评审（/architecture）：

- [ ] **统一连接注册表** — `infra.ConnectionRegistry` vs `ws.Hub` 双轨制，需 ADR 拍单一真相源（评审 P0-2）。`todos/pending/2026-07-25-unify-connection-registry-adr.md`
- [ ] **解决 WorkerPool 建而不用** — 无任何 Submit 调用，接线 or 登记 deferred（评审 P1-3）。`todos/pending/2026-07-25-resolve-workerpool-unused.md`
- [x] **Phase 4 前定 FSRS+SQLite 所有权** — Python(vocab_manager) 已实现 FSRS+SQLite，Phase 4/5 重新定义范围，需 ADR（评审 P1-4）。`todos/pending/2026-07-25-fsrs-sqlite-ownership-adr.md` → Resolved: ADR created at `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md`
- [ ] **仓库卫生批处理** — README 漂移 / gitignore 根级二进制 / 残留 .gitkeep 清理（评审 P2-5/P2-6/P3-7）。`todos/pending/2026-07-25-repo-hygiene-readme-gitignore-cleanup.md`

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

## Deferred Items

Items acknowledged and carried forward from previous milestone close:

| Category | Item | Status | Deferred At |
|----------|------|--------|-------------|
| *(none)* | | | |

## Session Continuity

Last session: 2026-08-01T12:29:05.860Z
Stopped at: Phase 5 context gathered and reviewed
Resume file: .planning/phases/05-fsrs-core-implementation/05-CONTEXT.md
