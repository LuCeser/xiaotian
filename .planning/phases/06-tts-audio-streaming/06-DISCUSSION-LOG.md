# Phase 6: TTS Audio Streaming - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-08-02
**Phase:** 06-TTS Audio Streaming
**Areas discussed:** TTS音频生成策略, 音频流传输机制, 错误恢复与容错, 音频质量与格式, 集成与接口设计
**Mode:** Auto (autonomous decision-making)

---

## TTS音频生成策略

| Option | Description | Selected |
|--------|-------------|----------|
| 预生成所有单词 | 在系统初始化时预生成所有单词的TTS音频，存储成本高 | |
| 按需生成 | 在会话开始时批量生成当前会话所需单词的TTS音频 | ✓ |
| 实时生成 | 在播放时才生成音频，延迟高影响用户体验 | |

**[auto] Selection:** 按需生成 — 推荐默认选项，平衡存储成本和响应速度
**Notes:** 基于Phase 3的pregenerate_dictation.py集成，避免预生成所有单词造成的存储浪费，在1C 1G VPS约束下最优

---

## 音频缓存策略

| Option | Description | Selected |
|--------|-------------|----------|
| 无缓存 | 每次播放都重新生成或读取文件 | |
| 会话级缓存 | 在会话期间缓存生成的音频，会话结束后清理 | ✓ |
| 单词级缓存 | 永久缓存每个单词的音频，适合单词量小的场景 | |

**[auto] Selection:** 会话级缓存 — 推荐默认选项，平衡重复播放需求和存储约束
**Notes:** 会话结束后30分钟自动清理临时文件，符合1C 1G VPS存储限制

---

## 音频流传输协议

| Option | Description | Selected |
|--------|-------------|----------|
| 连续流 | 持续传输音频流，客户端控制播放 | |
| 按需分块 | 基于WebSocket二进制帧协议按需传输 | ✓ |
| 预加载 | 一次性传输整个会话的音频，内存占用高 | |

**[auto] Selection:** 按需分块 — 推荐默认选项，与Phase 2 WebSocket协议对齐
**Notes:** 使用640B帧（20ms@16kHz PCM），与现有二进制帧协议兼容，实现预取机制满足<1.5秒延迟目标

---

## 错误恢复机制

| Option | Description | Selected |
|--------|-------------|----------|
| 重试策略 | 失败时自动重试（最多3次），然后跳过单词 | ✓ |
| 跳过单词 | 失败时立即跳过，不重试 | |
| 会话中止 | 任何TTS失败都中止整个会话 | |

**[auto] Selection:** 重试策略 — 推荐默认选项，最大化会话完成率
**Notes:** 3次重试平衡了容错性和用户体验，超过限制后优雅降级（跳过单词+记录日志）

---

## 音频质量控制

| Option | Description | Selected |
|--------|-------------|----------|
| 固定质量 | 使用统一的音频参数，简化实现 | ✓ |
| 自适应质量 | 根据网络条件动态调整质量 | |
| 用户可配置 | 允许用户选择音质，增加复杂度 | |

**[auto] Selection:** 固定质量 — 推荐默认选项，确保与现有协议兼容
**Notes:** 16kHz采样率，单声道，PCM格式，与Phase 2的WebSocket二进制帧协议完全兼容

---

## Claude's Discretion

以下区域使用了Claude的自由裁量权，选择了推荐默认选项：
- 音频缓冲区大小：640B（20ms@16kHz），与Phase 2协议对齐
- 会话目录清理策略：30分钟后删除，平衡存储和调试需求
- TTS生成超时：5秒，基于网络和API响应时间的经验值

## Deferred Ideas

None — discussion stayed within phase scope.

---

**Auto-mode execution:** All 5 gray areas discussed and resolved autonomously using recommended defaults.
**Decision rationale:** Based on project constraints (<1.5s latency, 1C 1G VPS, 150MB memory budget) and prior phase decisions (WebSocket binary frame protocol, subprocess integration pattern).