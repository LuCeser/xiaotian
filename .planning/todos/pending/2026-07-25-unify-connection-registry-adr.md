---
created: 2026-07-25T05:01:02.742Z
updated: 2026-08-09T09:12:52.000Z
title: "紧急：ConnectionRegistry死代码清理（1,414行）- 删除或重新连接"
area: architecture
severity: blocker
files:
  - pkg/infra/connection.go (608行)
  - pkg/infra/connection_test.go (806行)
  - cmd/service/main.go:310-312
  - pkg/infra/server.go:200
  - pkg/ws/hub.go (273行实现相同功能)
---

## Problem

**🚨 紧急架构债务：`pkg/infra/connection.go` 实现的连接注册表在生产环境中完全被绕过，成为1,414行死代码。**

### 死代码证据
1. **生产绕过确认**：`cmd/service/main.go:310-312` 将 Hub 直接连接为连接统计提供程序：
   ```go
   server.SetConnectionStatsProvider(func() infra.ConnectionStats {
       return hub.Stats()
   })
   ```
2. **注册表从未启动**：`infra/server.go:233-251` 的 Start 方法调用 `memMonitor.Start` 和 `workers.Start`，但**从不调用 `s.registry.Start`**
3. **生产调用者不存在**：注册表的 `conns` 映射永远保持为空，从任何非测试调用者都不会对其调用 `Register`/`Unregister`/`Touch`

### 死代码规模
- `connection.go`：608 行（ConnectionRegistry、Connection、SessionID、ConnectionState、LifecycleHooks、ConnectionStats、TokenBucket、newUUIDv4）
- `connection_test.go`：806 行
- **总计：~1,414 行代码，零生产调用者**

### 架构重复
`pkg/ws/hub.go`（273 行）实现了相同概念（有界客户端映射、原子接受/拒绝/清理计数器、`Stats() infra.ConnectionStats`），但具有完全不兼容的 API。两个模块，相同工作，Hub 胜出，注册表成为遗留器官。

### 浅层性信号
删除 `pkg/infra/connection.go` 和 `registry` 字段将改变**零**生产行为。剩余引用（server.go:356 的 `Components()` 返回、server.go:333 的 `Snapshot()` 回退）仅为测试服务的防御性管道。

## Solution

**决策点：立即选择以下方案之一**

### 方案 A：删除 ConnectionRegistry（推荐）
**理由**：Hub 已经在生产中工作且功能完整，保留1,414行死代码无意义。

**行动**：
1. 删除 `pkg/infra/connection.go` 和 `connection_test.go`
2. 从 `infra.Server` 中移除 `registry` 字段和相关引用
3. 清理 `server.go:287` 的死 `regCancel` 变量
4. 更新测试以使用 Hub 的统计接口

**影响**：
- 减少代码负担：-1,414 行
- 简化架构：单一连接跟踪真相源
- 维护性提升：消除平行的API表面

### 方案 B：重新连接 ConnectionRegistry
**理由**：如果认为 ConnectionRegistry 的令牌桶和清理逻辑有价值。

**行动**：
1. 在 `server.Start` 中调用 `s.registry.Start(regCtx)`
2. 删除 `main.go:310-312` 的 Hub 绕过
3. 删除或调整 Hub 的 `Stats()` 方法以避免冲突
4. 确保 Hub 正确使用 ConnectionRegistry API

**影响**：
- 恢复 Phase 1 的资源护栏设计
- 但需要重构 Hub 以适应不兼容的 API
- 架构复杂度增加

**前置依赖**：源自 2026-08-09 架构深度分析，确认死代码状态。建议优先选择方案 A 以减少技术债务。
