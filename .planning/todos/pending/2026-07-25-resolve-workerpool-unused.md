---
created: 2026-07-25T05:01:02.742Z
title: "解决 WorkerPool 建而不用：接线 or 登记 deferred"
area: infra
files:
  - pkg/infra/server.go:250
  - pkg/infra/worker.go
  - config.example.yaml:57
---

## Problem

`infra.Server.Start` 会拉起 worker goroutine（`pkg/infra/server.go:250` 调 `s.workers.Start(ctx)`），配置里也声明了 `worker.poolSize=4 / queueSize=256`（`config.example.yaml:57`）。

但全仓库**无任何非测试 `Submit` 调用**——WorkerPool 建好、启动后一直在空转，占用 goroutine 却从不被喂任务。

风险：未来业务代码（Phase 7 会话编排）很可能"绕过"它直接起 goroutine，导致这个池永远没人用、却没人敢删，变成永久僵尸组件。

## Solution

二选一，并在 ROADMAP / 对应 phase 计划里登记：

- **(A) 接线**：WS handler / 会话编排把耗时任务（如调 `pyBridge.RunDue/RunReview/RunPregenerate`、TTS 生成）`Submit` 进 WorkerPool，让它在 Phase 7 真正承担背压职责。
- **(B) 暂停**：先在 `01-deferred-items.md` 登记 deferred，并停掉空转的 worker goroutine（避免无谓占用），等 Phase 7 再启用。

无论选哪个，**不要让它继续无声空转**。源自 2026-07-25 架构评审 P1-3。
