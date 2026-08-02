# Phase 6: TTS Audio Streaming - Context

**Gathered:** 2026-08-02
**Status:** Ready for planning

## Phase Boundary

实现实时TTS音频传输系统，将TTS生成的音频通过WebSocket二进制帧协议流式传输到iPad浏览器，在<1.5秒延迟目标内提供单词听写提示。本阶段专注于音频生成、缓存、传输和错误处理的完整链路实现。

## Implementation Decisions

### TTS音频生成策略
- **D-01:** 采用按需生成策略，在会话开始时批量生成当前会话所需单词的TTS音频（基于Phase 3的pregenerate_dictation.py集成），避免预生成所有单词造成的存储浪费
- **D-02:** 使用会话级缓存，生成的音频文件缓存在会话目录中，会话结束后清理，平衡重复播放需求和存储约束

### 音频流传输机制
- **D-03:** 采用按需分块传输，基于WebSocket二进制帧协议（640B帧，20ms@16kHz PCM），将TTS生成的音频文件分块传输到客户端
- **D-04:** 实现音频预取机制，在播放当前单词时预加载下一个单词的音频块，消除网络延迟对<1.5秒目标的影响

### 错误恢复与容错
- **D-05:** 实现3次重试策略，TTS生成失败时自动重试，超过3次后跳过该单词并记录错误日志，不影响会话继续
- **D-06:** WebSocket传输错误时触发断点续传机制，记录已传输的帧位置，重连后从断点继续传输

### 音频质量与格式
- **D-07:** 使用固定音频质量配置（16kHz采样率，单声道，PCM格式），确保与现有WebSocket协议（Phase 2）的兼容性
- **D-08:** 音频文件命名规范：`{word_id}.pcm`，存储在会话临时目录`/tmp/tts_sessions/{session_id}/`

### 集成与接口设计
- **D-09:** 在`pkg/tts`包中实现`AudioStreamer`接口，负责音频文件的读取、分块和WebSocket传输
- **D-10:** 扩展PythonConfig添加TTSScript配置，指向`pregenerate_dictation.py`路径，复用Phase 3的subprocess调用模式

### Claude's Discretion
- 音频缓冲区大小：使用20ms@16kHz = 640B，与Phase 2的WebSocket二进制帧协议对齐
- 会话目录清理策略：会话结束30分钟后自动删除临时音频文件
- TTS生成超时：单个单词生成超时设置为5秒，超时后触发重试机制

## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project Requirements
- `.planning/REQUIREMENTS.md` — DICT-03, DICT-05, PY-04需求定义
- `.planning/PROJECT.md` — 技术约束和基础设施限制（150MB内存预算，1C 1G VPS）

### Prior Phase Context
- `.planning/phases/05-fsrs-core-implementation/05-CONTEXT.md` — FSRS单词选择逻辑，TTS需要基于选中的单词生成音频
- `.planning/phases/03-python-cli-integration/03-CONTEXT.md` — subprocess调用模式，pregenerate_dictation.py集成方式
- `.planning/phases/02-websocket-communication-layer/02-CONTEXT.md` — WebSocket二进制帧协议（640B帧，20ms@16kHz）

### Architecture Decisions
- `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` — Python(vocab_manager)的FSRS实现所有权，TTS生成需要与此协调

### External Specifications
- `D:/workspace/ai/xiaotian-skills/shared/dictation/scripts/pregenerate_dictation.py` (1348行) — 现有TTS生成pipeline，需要通过subprocess集成

## Existing Code Insights

### Reusable Assets
- `pkg/pybridge/bridge.go` — subprocess执行引擎，可用于调用TTS生成脚本
- `pkg/ws/hub.go` — WebSocket连接管理，已支持二进制帧传输
- `infra/config.go` — 分层配置加载模式，可扩展TTSScript配置字段

### Established Patterns
- Subprocess调用模式：通过PythonConfig配置脚本路径，使用semaphore限制并发（Phase 3模式）
- 错误处理模式：4类sentinel错误（ErrPythonNotFound, ErrPythonTimeout, ErrPythonEmpty, ErrPythonNonZero），TTS需遵循相同契约
- 资源管理：使用sync.Pool进行帧复用，50MB音频缓冲池预算（Phase 1决策）

### Integration Points
- WebSocket传输：复用Phase 2的Hub和Client机制，添加音频专用消息类型
- Session管理：与Phase 7的session state集成，在DICTATING状态下传输音频
- Python集成：扩展现有的Bridge接口，添加RunTTSGeneration方法

## Specific Ideas

No specific requirements — 基于<1.5秒延迟目标和1C 1G VPS约束，采用标准流式传输实现。

## Deferred Ideas

None — discussion stayed within phase scope.

---

*Phase: 06-TTS Audio Streaming*
*Context gathered: 2026-08-02*
*Auto-mode: All decisions made autonomously using recommended defaults*