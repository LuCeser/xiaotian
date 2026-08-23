# Handoff: Connection Error Debugging

## 问题描述

**症状：** 点击"我想听写30个单词"按钮后，前端显示 `connection error`，但后端日志显示会话创建成功。

**环境：**
- Windows 11 本地开发环境
- Go 服务运行在 `0.0.0.0:8080`
- 前端通过 `http://192.168.31.236:8080/` 访问

## 已完成的工作

### 1. 后端错误处理增强 (handlers.go)
- 添加了详细的会话创建日志
- 改进了错误消息格式
- 添加了会话创建前/后的追踪日志

### 2. 端到端测试验证
- 创建了独立的WebSocket测试客户端 (`test/uat_runner.go`)
- 验证了22/23项UAT测试通过
- 确认会话创建成功返回 `session_ready` 响应

### 3. 关键发现
从服务器日志可以看到：
```json
{"level":"info","ts":...,"msg":"session creation succeeded","session_id":"...","words_count":5}
```

但前端仍然显示 `connection error`。

## 可能的问题根源

### 假设1：帧格式解析问题
- 后端发送的是Phase 2二进制帧（16字节头 + payload）
- 前端 `websocket-client.js` 的 `_onMessage` 处理可能有问题
- 需要检查前端是否正确解析二进制帧

### 假设2：消息类型不匹配
- 后端发送 `MsgTypeControl` (0x02) 类型的帧
- 前端可能期望不同的消息类型

### 假设3：状态管理问题
- `state-manager.js` 的 `handleControlFrame` 可能没有正确处理响应
- `session_ready` 响应可能没有正确更新UI状态

## 需要检查的文件

### 后端
- `cmd/service/handlers.go:165-215` - handleStartSession 函数
- `pkg/session/session.go:115-200` - NewSession 函数

### 前端
- `pkg/web/static/js/websocket-client.js:180-250` - _onMessage 处理
- `pkg/web/static/js/websocket-client.js:400-450` - handleControlFrame
- `pkg/web/static/js/state-manager.js` - 状态管理
- `pkg/web/static/js/ui-components.js:100-160` - SessionButton 处理

## 调试建议

1. **在浏览器开发者工具中检查WebSocket消息**
   - 打开 Network → WS 标签
   - 观察发送和接收的消息
   - 确认是否收到 `session_ready` 响应

2. **添加前端调试日志**
   - 在 `websocket-client.js` 的 `_onMessage` 中添加 `console.log`
   - 检查消息是否到达前端

3. **检查帧格式**
   - 后端发送的是16字节头的二进制帧
   - 前端需要正确解析这个格式

## 相关提交

- `d816cd6` - fix(08): resolve remaining code review findings CR-5 and CR-6
- `42b67a1` - fix(08): CR-4 reset audioController lastSeq and scheduledTime between sessions
- `3a31dbb` - fix(08): CR-3 wire audio routing chain

## 下一步

1. 在浏览器中打开开发者工具
2. 访问 `http://192.168.31.236:8080/`
3. 点击"我想听写30个单词"
4. 观察 WebSocket 消息
5. 分析为什么 `session_ready` 响应没有正确更新UI

## 快速验证命令

```bash
# 启动服务
./run.ps1

# 运行端到端测试
go build -o test/uat_runner.exe test/uat_runner.go && ./test/uat_runner.exe

# 检查服务健康
curl http://127.0.0.1:8081/health
```
