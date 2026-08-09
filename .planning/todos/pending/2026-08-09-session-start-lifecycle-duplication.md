---
created: 2026-08-09T09:12:52.000Z
title: "折叠 Session.Start 到单一生命周期构造函数"
area: session
severity: minor
files:
  - pkg/session/session.go:219-223
  - pkg/session/manager.go:175-183
  - pkg/session/session_test.go
---

## Problem

**两条并行的生命周期设置路径，一个仅用于测试**

### 当前状态
1. **`Session.Start`** (`session.go:219-223`)：
   - 设置 `done`、`runCtx`、`cancel`
   - 调用 `go s.run(s.runCtx)`
   - **仅用于测试**（10个测试调用点）

2. **`Manager.NewSession`** (`manager.go:175-183`)：
   - 重复相同的三字段设置
   - 添加 `onExit` 回调（映射清理）
   - 调用 `go s.run(s.runCtx)` 直接而非委托给 Start
   - **唯一的生产路径**

### 风险
如果未来调用者使用包级 `NewSession + Start` 模式，`onExit` 回调（会话注册表清理）将被静默绕过。

这是浅层模块的气味 - 两个必须一致设置的点，删除 `Start` 并强制所有调用者通过一个构造函数将生命周期集中在一个地方。

## Solution

**统一到单一生命周期构造函数**

### 方案 A：删除 Session.Start（推荐）
1. 从 `Session` 中删除 `Start` 方法
2. 将所有生命周期设置集中在 `Manager.NewSession`
3. 更新测试以通过 Manager 创建会话（模拟生产路径）

### 方案 B：重构为显式工厂
1. 让 `Session.Start` 成为生产路径
2. `Manager.NewSession` 调用 `Start` 而非重复逻辑
3. 确保 `onExit` 通过参数传递

**收益**：
- 消除生命周期设置的重复逻辑
- 确保所有会话都正确设置清理回调
- 减少测试与生产路径的差异
