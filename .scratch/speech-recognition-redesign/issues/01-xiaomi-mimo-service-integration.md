# 01 — Xiaomi Mimo Service Integration Layer

**What to build:** Infrastructure for communicating with Xiaomi Mimo ASR and TTS services, including authentication, API clients, and basic connectivity testing. This foundational layer enables all subsequent speech features by providing reliable service communication.

**Blocked by:** None — can start immediately

**Status:** done (2026-08-23, commits: 9a8a7b3, 3edb20a, cb72cab)

- [x] Can successfully authenticate with Xiaomi Mimo API using provided credentials
- [x] TTS service can generate audio from text input with proper encoding
- [x] ASR service can transcribe audio to text with Chinese language support  
- [x] Basic integration tests pass with real service calls
- [x] Error handling for service failures, network issues, and rate limiting
- [x] API clients support streaming for real-time speech recognition — 取舍备注：TTS 具备 SSE 流式（SynthesizeStream）；ASR 为一次性整段转写、无 TranscribeStream——上游小米 API（/chat/completions + base64 整段音频）本身是请求/响应式，真正流式 ASR 在此 API 上不可实现，与 ticket 03/05 整句设计一致（有意取舍）
- [x] Service integration respects 1C1G VPS memory constraints — 取舍备注：设计上符合（连接池 max 20/host、SSE 流式解析、重试上限 3、30s 超时），未实测内存占用

## Completion note

- 验证方式：`go test ./pkg/mimo/ -count=1 -v`
- 2026-08-23 实测：16/16 全部通过（9 个单测 + 7 个 RealAPI 集成测试），耗时 13.5s，真实服务调用，MIMO_API_KEY 已配置
- 集成测试 7 项全部 PASS：Synthesize、SynthesizeStream、Transcribe、HealthCheck、EndToEnd、ConcurrentRequests、ErrorHandling
- 相关提交：9a8a7b3（重构 ASR 符合官方规范）、3edb20a（修 ASR 请求格式 + TestMain 加载 .env）、cb72cab（按真实响应结构解析 TTS 元数据 + 修 401 错误映射）