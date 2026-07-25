---
created: 2026-07-25T05:01:02.742Z
title: "统一连接注册表：infra.ConnectionRegistry vs ws.Hub（需 ADR）"
area: infra
files:
  - pkg/infra/server.go:182
  - pkg/infra/connection.go
  - pkg/ws/hub.go:51
  - pkg/ws/hub.go:101
  - config.example.yaml:25
---

## Problem

存在**两套各自维护 100 连接上限的连接注册中心，且互不相识**：

- `infra.ConnectionRegistry`（`pkg/infra/connection.go`）——Phase 1 精心做的资源护栏：100 连接上限、令牌桶限流（`connection.throttleRatePerSec`）、清理 goroutine（`session.cleanupInterval`）、内存 load-shedding 联动。由 `infra.Server` 构造与启动。
- `ws.Hub`（`pkg/ws/hub.go:51`）——Phase 2 的连接管理器：独立的 `clients` map、独立的 `limit`（`:101`），**无令牌桶、无清理 goroutine、不联动内存**。

`pkg/infra/server.go:182` 的注释已承认这点：`LifecycleHooks left zero-valued ... WebSocket layer will supply OnConnect/OnDisconnect hooks via a setter when it becomes the caller of ConnectionRegistry`——**这个 setter 至今未写**。

后果：一旦 WS 接入运行时（见 P0-1，Phase 2 的 02-03 接线），真实连接上限由 `ws.Hub` 单独决定，`infra.ConnectionRegistry` 那套令牌桶 / 清理 / 内存联动**全部失效**。`RES-03`（100 并发上限）将只在 health 快照里看得见，拦不住真实连接。

## Solution

进 ADR，二选一并明确单一真相源：

- **(A) 推荐**：让 `ws.Hub` 通过 `infra.ConnectionRegistry` 注册/注销——`ConnectionRegistry` 作为唯一真相源（持有上限 / 令牌桶 / 清理），`Hub` 退化为 I/O 通道。补上 `server.go:182` 注释里承诺的 LifecycleHooks setter。符合现有分层（资源护栏集中在 infra，WS 只管协议层）。
- (B) 删掉 `ConnectionRegistry`，把令牌桶 + 清理逻辑搬进 `ws.Hub`。

**前置依赖**：本决策应在 P0-1（WS 接线）动手前或同时拍板，否则接线后会暴露双轨问题。源自 2026-07-25 架构评审 P0-2。
