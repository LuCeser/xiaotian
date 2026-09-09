---
created: 2026-08-09T09:12:52.000Z
title: "批量删除死代码和存根"
area: code-cleanup
severity: minor
files:
  - pkg/session/types.go:16-20 (FrameSender接口)
  - pkg/tts/streamer.go:188 (PrefetchNext)
  - pkg/tts/streamer.go:214 (ClearPrefetch)
  - pkg/ws/doc.go (存根文件)
  - pkg/session/fakes_test.go (相关假对象)
---

## Problem

**多个已死但经过测试的接口和函数**

分析识别出以下死代码模式：

### 1. FrameSender 接口（死接口）
- **位置**：`pkg/session/types.go:16-20`
- **状态**：已定义并有测试假对象（`fakes_test.go:155-176`），但 Session 结构直接存储 `client *ws.Client` 而非 FrameSender
- **影响**：删除接口不会改变任何生产行为

### 2. TTS 预取机制（死功能）
- **位置**：`pkg/tts/streamer.go:188, 214`
- **状态**：`PrefetchNext` 和 `ClearPrefetch` 无生产调用者，约45行并发机制 + 150行测试
- **影响**：prefetch 映射永远为空，检查总是失败

### 3. pkg/ws/doc.go（死存根）
- **位置**：`pkg/ws/doc.go:1-13`
- **状态**：字面说明"此文件保留依赖关系……将替换为实际协议实现"，但包早已实现
- **影响**：误导性的空白导入和过时注释

## Solution

**批量清理死代码**

### 清理步骤
1. **删除 FrameSender**：
   - 删除 `types.go` 中的接口定义
   - 删除 `fakes_test.go` 中的假对象实现
   - Session 保持直接使用 `*ws.Client`

2. **删除 TTS 预取**：
   - 删除 `PrefetchNext` 和 `ClearPrefetch` 方法
   - 删除 `prefetch` 映射和 `prefetchMu` 
   - 删除 `StreamWord` 内部的预取检查逻辑
   - 删除相关测试

3. **删除 ws/doc.go**：
   - 删除整个存根文件
   - 移除误导性的空白导入

**收益**：
- 减少约200+行死代码
- 消除测试与生产的不一致
- 提高代码库可维护性
- 移除误导性文档
