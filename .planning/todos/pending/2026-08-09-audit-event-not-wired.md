---
created: 2026-08-09T09:12:52.000Z
title: "连接 auditEvent 到事件循环或删除"
area: session
severity: major
files:
  - pkg/session/audit.go:62
  - pkg/session/audit_test.go:72
  - pkg/session/session.go (事件循环)
---

## Problem

**测试过但从未调用的函数 - 等待被破坏的契约**

`auditEvent` 函数在 `pkg/session/audit.go:62` 中定义并有完整测试覆盖（`audit_test.go:72`），但事件循环（`run`/`handleEvent`/`onTransition`）从不调用它。

对比其他审计函数：
- `auditCreate`：从 `session.go:206` 调用 ✅
- `auditState`：从 `session.go:344` 调用 ✅  
- `auditEvent`：**无生产调用者** ❌

这正是"为可测试性提取纯函数但错误隐藏在调用方式中"模式的字面实现 - 这里的错误是缺少调用点。

## Solution

**决策点：**

### 选项 A：连接到事件循环（推荐）
如果 auditEvent 提供有价值的审计功能，将其集成到事件循环的适当位置：
1. 在 `handleEvent` 或 `onTransition` 中添加调用
2. 确保事件类型正确映射到审计记录
3. 更新测试以验证集成行为

### 选项 B：删除函数
如果审计事件不需要专用处理：
1. 删除 `audit.go` 中的 `auditEvent` 函数
2. 删除 `audit_test.go` 中的相关测试
3. 清理不必要的代码负担

**风险**：经过测试但未调用的函数是契约违规的等待发生。未来的开发者可能假设它被调用并基于此编写依赖代码。

**影响**：提高代码真实性 - 所有存在的代码都应该在生产中被使用。
