---
created: 2026-07-25T05:01:02.742Z
updated: 2026-09-09T11:58:28.029Z
title: "仓库卫生总纲：README 漂移 + gitignore + 根级调试产物与文档的合并清理"
area: tooling
files:
  - README.md
  - .gitignore
  - go.mod
  - cmd/service/.gitkeep
  - cmd/service/main.go:192
---

## Problem

架构评审 P2/P3 的卫生问题，加上 2026-09-09 捕捉的"项目代码非常杂乱，梳理无用的代码、文档，合并或者删除"，合并为一个总纲式清理待办：

### 一、已跟踪文件的问题（源自 2026-07-25 架构评审 P2-5/P2-6/P3-7）

1. **README 漂移严重**（`README.md`）：
   - 写 "Application entry point (main.go added in a later plan)"——`main.go` 早已存在。
   - 写 "Phase 1: in progress"——Phase 1 已 `completed 2026-07-23`。
   - 依赖列表声称 `prometheus/client_golang`、`testify`，但 `go.mod` 实际只有 gorilla/websocket、pkg/errors、viper、zap——**两个都没装**。
   - 项目布局只列 `cmd/`、`pkg/infra/`、`internal/`，**完全没提 `pkg/ws`、`pkg/pybridge`、`pkg/crc16`**。
2. **构建产物泄漏根目录**：`.gitignore` 有 `*.exe` 和 `/bin/`，但未覆盖根级无扩展名二进制（`home-tutor.exe~` 同理是编辑器备份残留）。
3. **残留清理**：`cmd/service/.gitkeep` 仍在追踪（与 `main.go` 并存）；`cmd/service/main.go:192` 的 `var _ = fmt.Sprintf` 占位 + 无用 `fmt` import。

### 二、根目录未跟踪调试/测试产物堆积（2026-09-09 捕捉）

- **HTML 调试页**：`debug-test.html`、`test-audio-bug-frontend.html`、`test-audio-sequence-bug.html`、`test-interface-debug.html`
- **E2E/WS 测试脚本**：`test-e2e-multi-word.js`、`test-e2e-runner.js`、`test-session-complete.js`、`test2-websocket.js`、`test2-websocket.py`
- **Python 调试/修数脚本**：`check_db.py`、`check_due.py`、`check_schema.py`、`reset-vocab.py`、`reset-vocab.sh`、`unlock_cards.py`、`fix_conflicts.py`
- **系统残留**：`nul`（Windows 重定向产物）、`home-tutor.exe~`（编辑器备份）
- **依赖目录**：根级 `node_modules/`、`package-lock.json`、`skills-lock.json`——**删除前必须确认**是否被 `.claude/package.json` / GSD 工具链引用
- **缓存/数据**：`third_party/python/__pycache__/`、`.scratch/debug/`、`.hta-data/`

### 三、根级一次性分析/报告文档需合并或删除（2026-09-09 捕捉）

`BUG-FIX-REPORT.md`、`BUG-FIX-VERIFICATION.md`、`SESSION-ID-BUG-ANALYSIS.md`、`SESSION-ISSUE-ANALYSIS.md`、`E2E-TEST-GUIDE.md`——多为一次性调试记录；结论仍有价值的并入 `.planning/` 或 `docs/`，其余删除。

## Solution

**先盘点分类，再分批原子提交执行**（keep / move / delete / gitignore 四类，逐文件判定）：

1. `README.md`：修正阶段状态、依赖列表（对照 `go.mod`）、项目布局（补 `pkg/ws` / `pkg/pybridge` / `pkg/crc16`）；或把"当前状态"类信息只放 `.planning/STATE.md`，README 只写稳定事实。
2. `.gitignore`：补根级裸二进制、`*.exe~`、`nul`、`__pycache__/`、`node_modules/`（确认后）；顺手删掉根目录残留产物。
3. 调试脚本/HTML：有留存价值的移入 `.scratch/` 或 `scripts/`，其余删除；E2E 脚本若 ticket 08 要用则归档到明确位置。
4. 分析文档：有价值结论合并进 `.planning/` 对应 SUMMARY 或 `docs/`，然后删原文件。
5. 删 `cmd/service/.gitkeep`；删 `main.go` 无用的 `fmt` import 与 `:192` 占位。

**保护清单（清理时绝不动）**：
- `CONTEXT.md` — matt skills（grill-with-docs）的领域词汇表
- `.scratch/speech-recognition-redesign/issues/06–08` — 活跃 ticket 链
- `docs/adr/`、`.planning/` 全部工件

相关但不并入（范围独立）：`todos/pending/2026-08-09-dead-code-cleanup.md`（Go 内部死代码：FrameSender、TTS 预取、ws/doc.go）。

源自 2026-07-25 架构评审 P2-5 / P2-6 / P3-7 + 2026-09-09 用户捕捉。
