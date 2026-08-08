# Phase 6: TTS Audio Streaming - Context

**Gathered:** 2026-08-02
**Updated:** 2026-08-08 (pi-agent decision research)
**Status:** Ready for execution

## Phase Boundary

实现实时TTS音频传输系统，将TTS生成的音频通过WebSocket二进制帧协议流式传输到iPad浏览器，在<1.5秒延迟目标内提供单词听写提示。本阶段专注于音频生成、缓存、传输和错误处理的完整链路实现。

## Implementation Decisions

### TTS API提供商策略（2026-08-08 pi-agent决策）
- **D-01:** 采用 Doubao 作为主提供商，Circuit Breaker Fallback → XiaoMimiMo，利用 Doubao 的 NLP 背景提供更好的发音准确性（小学教育关键）
- **D-02:** 实现初始 Manual Fallback Switch 先部署，后启用 Circuit Breaker（防止级联失败，自动故障转移，可配置阈值）
- **D-03:** Per-session Rate Limiting 使用内存 map 实现（不用 Redis 省内存），10分钟会话窗口，防止滥用同时允许自然交互
- **D-04:** Circuit Breaker State ~1KB/provider，Health Checks ~100KB，Fallback 增加额外 500ms 延迟预算
- **D-05:** 需要两个提供商的 Load Testing：50 并发会话验证延迟和配额管理

### 缓存管理策略（2026-08-08 pi-agent决策）
- **D-06:** Session-level Cache 采用 15词硬上限，每会话 ~1.44MB 最大值（15词 × 3秒 × 32KB/s）
- **D-07:** 100 并发会话实际内存使用 ~25-40MB（由于 prefetch 和流式传输），剩余预算 60MB
- **D-08:** SessionCache 加 size counter，Store 时限，超限返回 ErrWordNotCached 触发重新生成
- **D-09:** Cleanup 在会话状态转换时调用（FEEDBACK → COMPLETE），CleanupStale 作为 30分钟超时安全网

### 流量控制与背压机制（2026-08-08 pi-agent决策）
- **D-10:** Adaptive Send Buffer (8-32 消息范围基于发送成功率)，基于现有 Bounded SendBuffer (16×640B = ~10KB/连接)
- **D-11:** Priority Drop (Oldest) 策略，保证新鲜音频，符合听写场景（新鲜提示 > 完美连续性）
- **D-12:** 现有 ErrSendBufferFull 检测扩展 Adaptive Sizing，显著改善 Safari 变量网络条件下的韧性
- **D-13:** 不采用 Frame-level Window（过度工程），<1.5s 目标已有 prefetch 提供 latency 余量

### 音频质量与格式
- **D-20:** 使用固定音频质量配置（16kHz采样率，单声道，PCM格式），确保与现有WebSocket协议（Phase 2）的兼容性
- **D-21:** 音频文件命名规范：`{word_id}.pcm`，存储在会话临时目录`/tmp/tts_sessions/{session_id}/`

### 会话集成契约（2026-08-08 pi-agent决策）
- **D-22:** 采用 Hybrid Dual-Mode Contract（控制帧 + FSM 触发器），4-5 文件实现
- **D-23:** 控制帧处理器保持 thin 层（仅调用 FSM 转换方法），FSM 保持唯一真相源
- **D-24:** 支持 Phase 6 端到端验证（通过控制帧）+ Phase 7 架构纯净性（通过 FSM）
- **D-25:** 渐进迁移路径：控制帧先用于 Phase 6 测试，FSM 触发器为 Phase 7 准备

### 错误恢复与容错
- **D-14:** 实现3次重试策略，TTS生成失败时自动重试，超过3次后跳过该单词并记录错误日志，不影响会话继续
- **D-15:** WebSocket传输错误时触发断点续传机制，记录已传输的帧位置，重连后从断点继续传输

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
*Updated: 2026-08-08 with pi-agent decision research*
*Decisions: 25 implementation decisions locked via advisor research*