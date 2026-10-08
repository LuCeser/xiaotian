# Session Not Found Bug Analysis

## Current Issue

**症状**: 只读出第一个词 "apple"，然后出现大量 "session not found" 错误

**日志分析**:
```
session creation succeeded, session_id: "202d847383ac47fdb5e847a6e4463e86"
worker error: "update state for session 0: session not found"
```

## 问题分析

### 1. Session创建成功
- `session_id` 正确生成: `"202d847383ac47fdb5e847a6e4463e86"`
- Session结构体正确初始化: `s.id = sessionID` (session.go:193)
- 7个词汇成功获取

### 2. TTS Stream调用正确
- `spawnStreamWord()` 正确调用: `s.streamer.StreamWord(streamCtx, s.client, s.id, wordID, 0)` (session.go:416)
- 使用正确的session_id: `s.id` = `"202d847383ac47fdb5e847a6e4463e86"`

### 3. Worker错误
- **关键问题**: Worker尝试更新 "session 0" 而不是 "202d847383ac47fdb5e847a6e4463e86"
- 错误信息: `"update state for session 0: session not found"`
- 这说明某处session_id被设置为 "0" 或没有正确传递

## 可能的原因

### 1. Worker Session ID传递问题
Worker任务可能在以下位置使用了错误的session_id:
- 状态更新时的session查找
- Worker pool中的任务分发

### 2. Session过早清理
Session可能在以下情况下被提前删除:
- `removeSession(connID)` 被过早调用
- Worker任务执行时session已经不存在

### 3. 连接ID不匹配
`clientConnID(client)` 在不同调用中可能返回不同的值:
- 指针地址变化
- 连接重连或重新建立

## 调试建议

### 1. 添加Session ID日志
在以下位置添加日志，跟踪session_id的传递:
- `spawnStreamWord` 调用前后
- Worker任务启动时
- 状态更新尝试时

### 2. 检查Worker Pool实现
确认Worker如何接收和使用session_id

### 3. 验证Session生命周期
确认session何时被创建、使用和清理

### 4. 检查连接管理
确认WebSocket连接的稳定性和client指针的一致性

## 已验证正确的部分

✅ **Session ID生成**: `newSessionID()` 正确生成UUID  
✅ **Session初始化**: `s.id = sessionID` 正确赋值  
✅ **TTS调用**: `StreamWord(streamCtx, s.client, s.id, wordID, 0)` 使用正确ID  
✅ **前端Bug 2修复**: Stale snapshot问题已修复  

## 待解决部分

❌ **Worker Session ID传递**: 需要检查Worker如何获取session_id  
❌ **Session生命周期管理**: 需要确认session何时被清理  
❌ **连接ID稳定性**: 需要验证clientConnID的一致性  

## 下一步行动

1. 检查Worker Pool如何传递session_id
2. 添加详细的session_id跟踪日志
3. 验证session在多词播放时的生命周期
4. 检查是否存在竞态条件导致session过早清理
