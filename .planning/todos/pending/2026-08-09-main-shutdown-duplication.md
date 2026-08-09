---
created: 2026-08-09T09:12:52.000Z
title: "提取 main.go 重复关闭序列帮助器"
area: cmd-service
severity: minor
files:
  - cmd/service/main.go:421-444
---

## Problem

**main.go 中四重复制粘贴关闭序列**

### 当前状态
`main.go:421-444` 包含四个几乎相同的关闭块：
- `publicSrv.Shutdown`
- `wsServer.Shutdown` 
- `server.Shutdown`
- `healthSrv.Shutdown`

每个都：
- 使用相同的字段集在错误时记录相同的 Warn
- 具有相同的错误处理模式
- 重复相同的超时上下文管理

### 浅层模块气味
"排空一个服务器，记录错误"的概念内联重复使用了四次，而非提取为可复用的帮助器。

## Solution

**提取 shutdownAll 帮助器**

### 实现方案
1. 创建通用关闭帮助器：
   ```go
   func shutdownAll(ctx context.Context, logger infra.Logger, shutdowns ...struct {
       name string
       shutdown func(context.Context) error
   }) {
       for _, s := range shutdowns {
           if err := s.shutdown(ctx); err != nil {
               logger.Warn("server shutdown error",
                   infra.F("component", s.name),
                   infra.ErrField(err),
               )
           }
       }
   }
   ```

2. 重构 main.go 关闭逻辑：
   ```go
   shutdownAll(shutdownCtx, logger,
       {name: "public", shutdown: publicSrv.Shutdown},
       {name: "ws", shutdown: wsServer.Shutdown},
       {name: "infra", shutdown: server.Shutdown},
       {name: "health", shutdown: healthSrv.Shutdown},
   )
   ```

**收益**：
- 消除重复代码
- 统一错误处理模式
- 更容易添加新的服务器关闭
- 提高可维护性
