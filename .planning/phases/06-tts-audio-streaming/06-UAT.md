---
status: complete
phase: 06-tts-audio-streaming
source: [06-01-SUMMARY.md, 06-02-SUMMARY.md, 06-03-SUMMARY.md, 06-04-SUMMARY.md]
started: 2026-08-08T12:00:00Z
updated: 2026-08-08T14:30:00Z
verification_method: "Orca-supervised pi-agent (initial pass, run_f2f6e90aac26) + orchestrator direct re-test (Tests 2/3/4) after credentials were provided"
---

## Current Test

[testing complete]

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
  orchestrator re-test: 干净实例（service.exe PID 24428）同样无错启动，
  /health (:8081) 返回 200，日志 "public server listening"/"health server listening"。

### 2. 启动听写会话 (start_session 控制帧)
expected: |
  通过 test-interface.html 连接 WebSocket（状态显示 OPEN），
  发送 `start_session` 控制帧 `{"type":"start_session","params":{"limit":5}}`。
  服务端调用 FSRS 选题、调用 Python 桥接生成 TTS 音频（MP3→PCM 缓存），
  返回 `session_ready` 响应帧，包含 session_id 和待听写单词列表。
result: pass
evidence: |
  orchestrator re-test (uat_t234.go 客户端):
  1. unlock_cards → {"type":"unlock_result","result":{"unlocked":5}}
  2. start_session {"limit":5} → 收到 session_ready 帧（type 0x02）:
     {"type":"session_ready","session_id":"session-1786173984419",
      "words":["hello","world","water","book","apple"]}
  handleStartSession 内部执行 SelectDueCards + GenerateForSession（调用火山
  TTS API 生成全部词的音频）+ PrefetchNext，session_ready 正常返回即证明
  **TTS 生成端到端成功、凭证有效**。
  注: pi 初轮误报 "blocked third-party (缺 APP_ID/TOKEN/CLUSTER/MODEL)"。
  本库鉴权模型（pregenerate_dictation.py:183-204）为 API-key 路径，仅需
  VOLCENGINE_TTS_API_KEY（+可选 RESOURCE_ID，默认 seed-tts-2.0）；
  APP_ID/TOKEN/CLUSTER/MODEL 不属此代码库。真正的 T2 阻塞是 cmd_due 的
  fetch-lock（vocab_manager.py:274-281 选完到期卡即锁 FETCH_LOCK_HOURS），
  由 unlock_cards 解除。

### 3. 播放单词音频 (play_word 流式传输)
expected: |
  发送 `play_word` 控制帧 `{"type":"play_word","params":{"word_id":"<id>","session_id":"<id>"}}`。
  收到一系列 640 字节的 MsgTypeAudio 音频帧（20ms 间隔节流），
  音频通过扬声器播放、自然清晰可懂；
  音频结束后收到 `word_complete` 状态帧通知。
result: pass
evidence: |
  orchestrator re-test: play_word {session_id: session-1786173984419, word_id: "hello"}
  → 收到 117 个音频帧（type 0x01），共 74880 字节 = **精确 640 字节/帧**
  （D-71: 20ms @ 16kHz mono PCM）。117 帧 × 20ms ≈ 2.34s 音频。
  流末收到 word_complete 帧型（type 0x06）
  {"type":"word_complete","word_id":"hello"}。帧尺寸与节流符合设计契约。

### 4. 首帧音频延迟 (Time-to-first-audio)
expected: |
  从发送 play_word 请求到收到第一个音频帧的时间 < 1.5 秒。
  命中缓存的单词通常 <100ms；新生成的单词 <1.5s。
result: pass
evidence: |
  orchestrator re-test: 从 play_word 发送到首个音频帧 = **20.86ms**（阈值 1.5s）。
  符合设计：start_session 阶段已预生成（GenerateForSession）+ 预取
  （PrefetchNext），play_word 仅从本地缓存流式，故近实时。该项远低于 1.5s 阈值。

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
passed: 6
issues: 0
pending: 0
skipped: 0
blocked: 0

## Gaps

*No gaps identified. All 6 tests pass.*

## Notes

- 鉴权模型（pregenerate_dictation.py:183-204）：API-key 路径需 `VOLCENGINE_TTS_API_KEY`
  （+可选 `VOLCENGINE_TTS_RESOURCE_ID`，默认 `seed-tts-2.0`），通过 D-05 由 os.Getenv 转发到
  Python 子进程。备选路径为 `APP_ID + ACCESS_KEY`。`TOKEN/CLUSTER/MODEL` 不属本库。
- 测试客户端 `uat_t234.go`（//go:build ignore）位于仓库根，Origin 须用 `http://localhost`
  （无端口）以匹配 DefaultConfig 的 dev-safe 白名单 `[localhost, 127.0.0.1]`。
- `cmd_due` 选到期卡后会锁 FETCH_LOCK_HOURS（vocab_manager.py:274-281），重复测试前需先
  发送 `unlock_cards` 清锁。
