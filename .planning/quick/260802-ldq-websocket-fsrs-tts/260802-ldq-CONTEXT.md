# Quick Task 260802-ldq: 创建最小化前端验证界面，用于测试WebSocket连接、FSRS单词选择和TTS音频流播放 - Context

**Gathered:** 2026-08-02
**Status:** Ready for planning

## Task Boundary

创建最小化前端验证界面，用于测试WebSocket连接、FSRS单词选择和TTS音频流播放。这是一个快速验证任务，目标是端到端测试已完成的后端基础设施（阶段1-5），为后续前端开发提供信心和反馈。

## Implementation Decisions

### 前端技术栈
- **决策：使用纯 HTML/CSS/JavaScript**
- **理由：** 零构建开销，直接在浏览器中打开即可测试；原生 WebSocket API 和 HTML5 Audio API 完全满足需求；符合"快速反馈周期优先"原则
- **技术选择：** 原生 WebSocket API、HTML5 Audio API、简单 DOM 操作

### 界面复杂度
- **决策：最小化界面，专注核心功能测试**
- **理由：** 刚好覆盖验证需求，不多不少；快速实现，避免过度设计
- **界面组成：**
  1. WebSocket 连接区域（状态指示器 + 连接/断开按钮）
  2. FSRS 测试区域（"获取待复习单词"按钮 + 结果显示）
  3. TTS 测试区域（"播放音频"按钮 + 播放状态）
  4. 日志区域（显示所有 WebSocket 消息和错误）

### 测试范围
- **决策：测试全部核心后端功能**
- **理由：** 端到端验证 WebSocket→FSRS→TTS 完整数据流
- **优先级排序：**
  1. WebSocket 连接（🔴 最高优先级）- 连接建立、消息收发、错误处理
  2. FSRS 单词选择（🟡 中等优先级）- 调用 SelectDueCards、显示返回结果
  3. TTS 音频流（🟢 验证优先级）- 接收音频数据、播放音频、测试延迟

### 数据来源
- **决策：使用真实数据库 + 测试数据**
- **理由：** 真实数据流验证，避免 mock 偏差；验证整个数据库到前端的管道
- **实施方案：** 使用项目现有 SQLite 数据库，添加少量测试单词数据（10-20个）

### Claude's Discretion
- 界面布局和视觉设计由 Claude 决定，优先考虑清晰度和可用性
- 日志区域的详细程度由 Claude 决定，平衡调试需求和可读性
- 测试单词数据的组织和内容由 Claude 决定，确保覆盖常见场景

## Specific Ideas

### 界面布局建议
- 采用垂直布局，从上到下：WebSocket → FSRS → TTS → 日志
- 使用卡片式设计，每个测试区域独立显示
- 状态指示器使用颜色编码（绿色=成功，红色=错误，黄色=进行中）

### WebSocket 测试场景
- 连接到本地 Go 服务（ws://localhost:8080/ws）
- 测试连接断开重连机制
- 验证二进制音频帧接收

### FSRS 测试场景
- 请求默认数量的单词（20个）
- 验证返回的单词数据结构
- 测试空结果场景（无待复习单词）

### TTS 测试场景
- 播放单个单词的音频
- 测试音频播放队列
- 测试播放中断和重新播放

## Canonical References

无外部规格文档引用 - 所有需求已在上述实施决策中完整捕获。

### 相关后端接口
- `pkg/fsrs/service.go` - FSRSService.SelectDueCards() 接口
- `pkg/websocket/hub.go` - WebSocket 连接和消息处理
- `pkg/pybridge/executor.go` - Python TTS 生成接口
- `.planning/phases/05-fsrs-core-implementation/05-02-SUMMARY.md` - FSRS 服务实现总结
- `.planning/phases/02-websocket-communication-layer/02-03-SUMMARY.md` - WebSocket 协议总结