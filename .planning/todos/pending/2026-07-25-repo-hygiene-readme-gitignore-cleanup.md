---
created: 2026-07-25T05:01:02.742Z
title: "仓库卫生：修 README 漂移 + gitignore 根级二进制 + 清理残留"
area: tooling
files:
  - README.md
  - .gitignore
  - go.mod
  - cmd/service/.gitkeep
  - cmd/service/main.go:192
---

## Problem

架构评审 P2/P3 的若干卫生问题，合并处理：

1. **README 漂移严重**（`README.md`）：
   - 写 "Application entry point (main.go added in a later plan)"——`main.go` 早已存在。
   - 写 "Phase 1: in progress"——Phase 1 已 `completed 2026-07-23`。
   - 依赖列表声称 `prometheus/client_golang`、`testify`，但 `go.mod` 实际只有 gorilla/websocket、pkg/errors、viper、zap——**两个都没装**。
   - 项目布局只列 `cmd/`、`pkg/infra/`、`internal/`，**完全没提 `pkg/ws`、`pkg/pybridge`、`pkg/crc16`**。
2. **构建产物泄漏根目录**：仓库根出现 `service`（~11MB）和 `service.exe`（~12MB）。`.gitignore` 有 `*.exe`（覆盖 `service.exe`）和 `/bin/`，但**未覆盖根级 `service`（无扩展名）**——`git status` 一直显示 `?? service`（未追踪、未忽略）。
3. **残留清理**：`cmd/service/.gitkeep` 仍在追踪（与 `main.go` 并存）；`cmd/service/main.go:192` 的 `var _ = fmt.Sprintf` 占位 + 无用 `fmt` import。

## Solution

- `README.md`：修正阶段状态、依赖列表（对照 `go.mod`）、项目布局（补 `pkg/ws` / `pkg/pybridge` / `pkg/crc16`）；或把"当前状态"类信息只放 `.planning/STATE.md`，README 只写稳定事实。
- `.gitignore`：加一条根级 `/service`（或忽略根级裸二进制）；顺手 `rm` 掉根目录的 `service` / `service.exe`。
- 删 `cmd/service/.gitkeep`；删 `main.go` 无用的 `fmt` import 与 `:192` 占位。

源自 2026-07-25 架构评审 P2-5 / P2-6 / P3-7。
