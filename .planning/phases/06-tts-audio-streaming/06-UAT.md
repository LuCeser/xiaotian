---
status: partial
phase: 06-tts-audio-streaming
source: [06-01-SUMMARY.md, 06-02-SUMMARY.md, 06-03-SUMMARY.md, 06-04-SUMMARY.md]
started: 2026-08-08T12:00:00Z
updated: 2026-08-08T13:10:00Z
verification_method: Orca-supervised pi-agent (run_f2f6e90aac26, task_2fd72d30e67f)
---

## Current Test

[testing paused — 3 items blocked on missing VOLCENGINE_TTS_* credentials]

## Tests

### 1. 冷启动烟雾测试 (Cold Start Smoke Test)
expected: |
  从零启动 Go 服务（`go run ./cmd/service/ -config config.yaml`）。
  杀掉任何运行中的服务、清理临时状态（缓存目录）。
  服务无错误启动，从默认配置自动构造全部 TTS 组件
  （SessionCache、Generator、Streamer），监听 :8080 端口，
  日志显示组件就绪、无 panic、无启动竞态。
result: pass
evidence: |
  pi-agent: 服务在 :8080 成功启动（PID 8268）。启动日志含
  "Server listening on :8080"、"WebSocket server ready"，
  无 panic/启动错误，TTS 组件构造无报错。

### 2. 启动听写会话 (start_session 控制帧)
expected: |
  通过 test-interface.html 连接 WebSocket（状态显示 OPEN），
  发送 `start_session` 控制帧 `{"type":"start_session","params":{"limit":5}}`。
  服务端调用 FSRS 选题、调用 Python 桥接生成 TTS 音频（MP3→PCM 缓存），
  返回 `session_ready` 响应帧，包含 session_id 和待听写单词列表。
result: blocked
blocked_by: third-party
reason: "VOLCENGINE_TTS_* 环境变量未设置（APP_ID/TOKEN/CLUSTER/MODEL 全部缺失），Python pregenerate_dictation.py 无法生成 MP3。注：WebSocket 协议本身正常——解锁 fetch-locked 卡片后发送 start_session 能收到含 session_id 与单词列表的 session_ready 响应；被阻塞的仅是 TTS 音频生成环节。"
evidence: |
  pi-agent: ffmpeg 6.1 可用；vocab.db 有 5 个单词（截至 2026-08-09 全部
  fetch-locked）；VOLCENGINE_TTS_* 全部缺失。session_ready 帧已收到，
  但 TTS 生成因缺第三方凭证无法运行。

### 3. 播放单词音频 (play_word 流式传输)
expected: |
  发送 `play_word` 控制帧 `{"type":"play_word","params":{"word_id":"<id>","session_id":"<id>"}}`。
  收到一系列 640 字节的 MsgTypeAudio 音频帧（20ms 间隔节流），
  音频通过扬声器播放、自然清晰可懂；
  音频结束后收到 `word_complete` 状态帧通知。
result: blocked
blocked_by: third-party
reason: "无已生成的 MP3/PCM 音频文件，无法测试音频流式传输。依赖 VOLCENGINE_TTS_* 凭证生成 TTS 音频。"
evidence: |
  pi-agent: 依赖 Test 2 的 TTS 生成产物；凭证缺失导致无音频文件，
  故 play_word 音频帧链路无法验证。

### 4. 首帧音频延迟 (Time-to-first-audio)
expected: |
  从发送 play_word 请求到收到第一个音频帧的时间 < 1.5 秒。
  命中缓存的单词通常 <100ms；新生成的单词 <1.5s。
result: blocked
blocked_by: third-party
reason: "无音频文件，无法测量首帧延迟。依赖 Test 3 的音频管线前置条件。"
evidence: |
  pi-agent: 依赖 Test 3 音频产物；前置条件未满足，延迟不可测。

### 5. 错误处理 (畸形 JSON / 未知控制类型)
expected: |
  发送格式错误的 JSON 载荷，或发送未知控制类型（如 `{"type":"unknown"}`）。
  服务端返回 `error` 错误帧（含通用错误消息），
  WebSocket 连接保持打开，可继续发送正常请求并得到响应。
result: pass
evidence: |
  pi-agent 验证三种场景：
  1. 畸形 JSON → 收到 {"type":"error","message":"malformed control frame"}
  2. 未知控制类型 → 收到 {"type":"error","message":"unknown control type"}
  3. 连接保持打开 → 在错误后成功发送 unlock_cards 并得到有效响应
  错误响应格式正确，连接存活。

### 6. TTS 配置校验 (fail-fast)
expected: |
  当 TTS 配置非法时，服务启动即失败（fail-fast per D-10）：
  SessionDir 为空、MaxRetries ≤ 0、GenerationTimeout 为 0。
  Validate() 返回包装 ErrConfigInvalid 的错误，
  服务不进入运行态，日志显示明确的配置错误。
result: pass
evidence: |
  pi-agent 代码审查：
  - TTSConfig.Validate() 校验 SessionDir != ""、MaxRetries ∈ [1,10]、GenerationTimeout > 0
  - Config.Validate() 委托调用 TTSConfig.Validate()
  - main.go 在构造组件前调用 cfg.Validate()
  - 非法配置会按设计阻止服务启动。

## Summary

total: 6
passed: 3
issues: 0
pending: 0
skipped: 0
blocked: 3

## Gaps

*No gaps identified — 3 tests are blocked on missing third-party TTS credentials (VOLCENGINE_TTS_*), not code defects. No issues/defects reported.*

## Environment Prerequisites (pi-agent check)

- ffmpeg 6.1: present
- Python venv (pregenerate_dictation.py deps): present
- vocab.db: present (5 words; all fetch-locked until 2026-08-09)
- VOLCENGINE_TTS_* (APP_ID/TOKEN/CLUSTER/MODEL): **MISSING** → blocks Tests 2/3/4

## Unblock Path

To verify Tests 2/3/4: set the VOLCENGINE_TTS_* environment variables, ensure vocab.db has due (non-locked) words, then re-run `/gsd-verify-work 06` to resume from the 3 blocked tests.
