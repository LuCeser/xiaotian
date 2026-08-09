---
created: 2026-08-09T09:12:52.000Z
title: "拆分 infra/config.go 上帝文件"
area: infra
severity: minor
files:
  - pkg/infra/config.go (1,106行)
---

## Problem

**config.go 是一个1,106行的上帝文件**

### 当前状态
单个文件包含14种结构类型：
- `Config`, `MemoryConfig`, `SessionConfig`, `SessionTimeoutConfig`
- `BufferConfig`, `ServerConfig`, `LogConfig`, `WorkerConfig` 
- `ConnectionConfig`, `ShutdownConfig`, `PythonConfig`
- `WSConfig`, `DatabaseConfig`, `FSRSConfig`, `TTSConfig`
- 以及各自的 `Validate()` 方法

### 问题
1. **认知负担**：理解 `TTSConfig.MaxRetries` 需要滚过不相关的 `ConnectionConfig` 和 `BufferConfig` 定义
2. **缺失边界**：天然边界（每个消费包的配置一个文件）缺失
3. **按行数深层，按概念浅层**：整个配置树是一个命名空间

## Solution

**按消费包拆分配置文件**

### 拆分策略
1. **按功能域分组**：
   - `config_memory.go` - MemoryConfig
   - `config_session.go` - SessionConfig + SessionTimeoutConfig  
   - `config_server.go` - ServerConfig + ShutdownConfig
   - `config_worker.go` - WorkerConfig + BufferConfig
   - `config_connection.go` - ConnectionConfig
   - `config_database.go` - DatabaseConfig
   - `config_python.go` - PythonConfig
   - `config_ws.go` - WSConfig
   - `config_tts.go` - TTSConfig
   - `config_fsrs.go` - FSRSConfig
   - `config_log.go` - LogConfig
   - `config.go` - 主 Config 结构 + Validate()

2. **保持向后兼容**：
   - 所有类型保持公共导出
   - 包路径保持不变
   - 仅内部文件组织变化

**收益**：
- 更容易找到相关配置
- 减少认知负担
- 更清晰的职责分离
- 更容易维护和测试
