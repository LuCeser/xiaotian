---
phase: 13-realtime-minimal-loop
plan: 03
subsystem: infra
tags: [websocket, gorilla, json-protocol, pcm-audio, realtime, timing, replaceable-providers]

requires: []
provides:
  - pkg/rt 协议契约（17 个 JSON 控制消息结构体 + 二进制 PCM 约定 + session_init 协商校验）
  - ASRProvider/LLMProvider/TTSSynthesizer 可替换供应商接口（AudioChunkFunc 裸 PCM 回调）
  - /rt WSS 传输端点（升级/Origin 白名单/读限/心跳/单写者发送泵/SessionManager 分派契约）
  - EventRecorder 服务端时序事件记录器（单调 t_mono_ns + 10 事件词表）
  - fake ASR/LLM/TTS 脚本化测试基建（供 13-04 轮次管理器测试复用）
affects: [13-realtime-minimal-loop/13-04 (turn/vad), 13-realtime-minimal-loop/13-05 (前端 rt-client), 13-realtime-minimal-loop/13-06 (MiMo 适配器与 /rt 挂载)]

actuals:
  tokens: 23800    # chars/4 over the realized pkg/rt diff (95037 chars / 4)
  tasks: 3
  commits: 3       # MEASURED: git rev-list --count bd0a4b4..d4caaa4
plan_head_before: bd0a4b4aa3faa29e45fa74b221810d19dabe1adf
plan_head_after: d4caaa45fdd65427d0c70d81974e353e0e68370a

tech-stack:
  added: []         # 零新增依赖（gorilla/websocket v1.5.3 存量）
  patterns:
    - "JSON 控制（TextMessage）+ 裸 PCM（BinaryMessage）双流传输，传输只搬运、轮次管理单点拥有状态"
    - "单写者发送泵 + 非对称溢出策略：控制满则报错、音频满则丢并计数（150MB 预算）"
    - "进程锚点单调时钟（time.Since(anchor)）事件记录，跨机对齐只按事件 ID + pong clock_offset"
    - "同波并行计划的 wire-JSON 惰性解析 fake 脚本（合并后字段自动可见，零冲突）"

key-files:
  created:
    - pkg/rt/protocol.go
    - pkg/rt/protocol_test.go
    - pkg/rt/providers.go
    - pkg/rt/timing.go
    - pkg/rt/timing_test.go
    - pkg/rt/providers_fake_test.go
    - pkg/rt/conn.go
    - pkg/rt/conn_test.go
  modified: []

key-decisions:
  - "session_ready 的 TTS 采样率以嵌套 tts{sampleRate} 对象（TTSSpec）落地而非扁平 int 字段——包注释/providers godoc/key_links 三处均以 session_ready.tts.sampleRate 为准，嵌套对象让该 wire 路径为真"
  - "ParseControl 解析全部 17 个类型词表（双向消息均可往返），方向合法性由轮次管理器（13-04）执法——测试与一致性检查天然获得全词表覆盖"
  - "fakeLLM 脚本条目支持 raw wire JSON 惰性解析：13-02（同波并行）拥有 mimo.ChatResponse 的 ToolCalls/FinishReason 字段，wire 条目把 Pattern-4 样例原文保存，合并后同一脚本自动带出这些字段，13-04 可直接构造 Go 值——避免对 pkg/mimo/types.go 的跨工作树写冲突"
  - "EventInterruptHandled 常量名为 handled、事件值为 REQUIREMENTS 词表的 \"interrupt\"（复盘第 10 节必须记录段口径）"
  - "发送泵构造与启动分离（newConnWriter/start）：背压语义可在不起泵的前提下确定性测试"
  - "RT-02/RT-04/RT-10 不在本次 mark-complete——三 ID 均被 13-02/13-05/13-06/13-07 共同声明（共享 ID 门 #2388 阻塞），且计划 Flagged Assumptions 明确真机验收归 13-05/13-07"

patterns-established:
  - "Origin 白名单两段式匹配 + EnableCompression:false 从 pkg/ws 整段照抄参数（禁 \"*\"、空 Origin 拒绝、去端口回退）——/rt 与 /ws 治理语义一致但代码独立（冻结栈零 import）"
  - "读治理三件套：SetReadLimit 硬上限 + 心跳滚动 read deadline（2×interval）+ 有界发送队列"
  - "发送方音频字节拷贝入队（上游音频缓冲有池化复用），控制消息在调用点立即 marshal 让序列化错误就地暴露"

requirements-completed: [RT-02, RT-04, RT-10]

coverage:
  - id: D1
    description: "JSON 控制协议全集（17 消息结构体、wire 字段名冻结、ping/pong 时钟偏移探针对）与 session_init 采样率协商校验（仅 16000/s16le/1ch）"
    requirement: RT-02
    verification:
      - kind: unit
        ref: "pkg/rt/protocol_test.go#TestProtocolSessionInitRoundTrip 及全部 TestProtocol* 往返/矩阵/拒绝测试"
        status: pass
      - kind: unit
        ref: "command: go test ./pkg/rt/ -run TestProtocol -count=1"
        status: pass
    human_judgment: false
  - id: D2
    description: "可替换供应商接口（ASR/LLM/TTS，AudioChunkFunc 裸 PCM 回调、接口面无 base64）+ 单调时序事件记录器（10 事件词表、t_mono_ns 同锚）+ 脚本化 fake 基建"
    requirement: RT-04
    verification:
      - kind: unit
        ref: "pkg/rt/timing_test.go#TestTimingEmitFields / TestTimingMonoNsMonotonicAndSameSourceAsEmit / TestTimingEventVocabulary"
        status: pass
      - kind: unit
        ref: "pkg/rt/providers_fake_test.go#TestFakesLLMWireToolCallsScript / TestFakesTTSProducesBarePCMWithFinalFlag 等"
        status: pass
      - kind: unit
        ref: "command: go test ./pkg/rt/ -run \"TestTiming|TestFakes\" -count=1"
        status: pass
    human_judgment: false
  - id: D3
    description: "/rt WSS 传输端点：升级（EnableCompression:false Safari 规避 + 两段式 Origin 白名单禁 \"*\" 拒空 Origin）、SetReadLimit + 心跳滚动 deadline、单写者发送泵（音频丢弃计数/控制报错）、SessionManager/Sender 契约与断连释放"
    requirement: RT-10
    verification:
      - kind: integration
        ref: "pkg/rt/conn_test.go#TestOriginEnforcement / TestReadLimit / TestRoutingToManager / TestWriterBackpressure / TestWriterDelivers / TestHeartbeatRollsDeadline"
        status: pass
      - kind: unit
        ref: "command: go build ./... && go vet ./pkg/rt/ && go test ./pkg/rt/ -count=1 && go test ./pkg/ws/ -short -count=1"
        status: pass
    human_judgment: false

duration: 2h 58m
completed: 2026-10-01
status: complete
---

# Phase 13 Plan 03: pkg/rt 基础三件套 Summary

**/rt 实时层契约与传输地基：17 消息 JSON 控制协议（session_init 16000/s16le/1ch 协商 + ping/pong 时钟偏移探针）、ASR/LLM/TTS 可替换接口（裸 PCM 回调，base64 止步适配层）、Origin/读限/心跳三件套 WSS 端点与单写者发送泵、单调锚点事件记录器——全部测试绿，冻结栈 pkg/ws 零影响**

## Performance

- **Duration:** 2h 58m（含一次 API 限流中断的挂钟时间）
- **Started:** 2026-10-01T12:49:09Z
- **Completed:** 2026-10-01T15:47:27Z
- **Tasks:** 3 / 3
- **Files created:** 8（pkg/rt/*，2663 行，零修改存量文件）

## Accomplishments

- 协议契约冻结为代码：前端 rt-client.js（13-05）与轮次管理器（13-04）可立即并行开发——每个消息类型有 JSON 往返测试锚定 wire 字段名（snake_case 消息级 / camelCase 音频对象），未知/畸形控制消息返回 ErrCodeInvalidMessage 包装错误
- session_init 协商按 CONTEXT 锁定实现：客户端声明采样率、服务端校验（仅 16000/s16le/1ch，48000/f32le/2ch 均被测试矩阵拒绝并携带 unsupported_format 错误码）；session_ready.tts.sampleRate 声明 24kHz 下行采样率（TTSOutputSampleRate，godoc 引用 pkg/tts/mimo_synth.go 结论）
- 可替换接口落地（ADR-0002）：ASR/LLM 签名对齐 mimo.Client（Transcribe/Chat），TTS 回调为 rt 自有 AudioChunkFunc(pcm []byte, isFinal bool)——MiMo base64 wire 细节止步 13-06 适配层（review M-1 裁定）
- 时序事件记录器（review L-1/M-6 裁定）：EventRecorder 以进程锚点 time.Since 输出单调 t_mono_ns（无 UnixNano），pong 消息与全部 rt_event 同锚，10 事件常量锚定 REQUIREMENTS 必须记录词表（EventInterruptHandled 值 "interrupt"），Emit godoc 写明脱敏边界（T-13-06b：只允许事件名/ID/时长/版本类字段）
- /rt 传输端点完成"只搬运"职责：Origin 白名单禁 "*" 拒空 Origin 两段式匹配 + EnableCompression:false（Safari TEXT→BINARY 规避）、SetReadLimit 切断超限帧、心跳滚动 read deadline、单写者发送泵（控制满则报错/音频满则丢并计数，适配 150MB 预算）、读循环退出即调 SessionManager.Close（RT-10/SC7 服务端半边）
- fake 测试基建就绪：fakeASR/fakeLLM 脚本队列（LLM 支持 wire JSON 惰性解析，13-04 工具环测试直接受益）、fakeTTS 裸 PCM chunk + isFinal 收尾 + 注入延迟 + 流中途持续产出模式

## Task Commits

Each task was committed atomically:

1. **Task 1: protocol.go — 控制消息全集 + session_init 协商校验** - `6a0b5ce` (feat)
2. **Task 2: providers.go + timing.go — 可替换接口与时序事件记录器** - `adf5ae7` (feat)
3. **Task 3: conn.go — /rt WSS 端点** - `d4caaa4` (feat)

**Plan metadata:** 本 SUMMARY 提交（docs）

## Files Created/Modified

- `pkg/rt/protocol.go` - 17 个控制消息结构体 + 类型/错误码/状态常量 + ParseControl + ValidateAudioInit + ErrorCode + TTSOutputSampleRate
- `pkg/rt/protocol_test.go` - 全消息往返 + wire 字段名断言 + ping/pong 回显 + 协商矩阵 + 未知/畸形拒绝
- `pkg/rt/providers.go` - ASRProvider/LLMProvider/TTSSynthesizer 接口 + AudioChunkFunc 类型
- `pkg/rt/timing.go` - EventRecorder（进程锚点单调时钟）+ 10 个事件常量 + rt_event Emit
- `pkg/rt/timing_test.go` - 内联捕获 logger 断言字段名 + 单调性 + nil logger 兜底 + 事件词表锚定
- `pkg/rt/providers_fake_test.go` - fakeASR/fakeLLM/fakeTTS 脚本化实现与测试（含 wire JSON 惰性解析）
- `pkg/rt/conn.go` - ServerConfig/Handler/HandleWebSocket + connWriter 单写者发送泵 + heartbeat + stripOriginPort
- `pkg/rt/conn_test.go` - Origin 矩阵/读限/路由/背压/投递/心跳滚动 deadline/配置校验（httptest + gorilla Dialer）

## Decisions Made

- **session_ready.tts 嵌套对象**：计划结构体清单写扁平 `TTSSampleRate int`，但包注释（本计划自写）、providers.go godoc、key_links 三处均声明 `session_ready.tts.sampleRate` wire 路径——嵌套 `TTSSpec{SampleRate}` 让两处一致为真（详见 Deviations #2）
- **ParseControl 全词表**：解析全部 17 类型而非仅客户端上行集——往返测试天然覆盖双向，方向执法归 13-04 轮次管理器（"传输只搬运"）
- **fakeLLM wire 脚本惰性解析**：同波的 13-02 拥有 mimo.ChatResponse 的 ToolCalls/FinishReason 字段扩展；wire 条目存 Pattern-4 样例原文、Chat() 时才解析——今天断言可编译字段（role/content），合并后同一样例自动带出新字段，13-04 无需改 fake
- **发送泵构造/启动分离**：`newConnWriter` 不起泵、`start()` 显式启动——TestWriterBackpressure 在不起泵时确定性断言满队列语义（音频丢弃计数、控制报错），TestWriterDelivers 单独覆盖起泵后的真实投递与拷贝语义
- **Requirement 不标完成**：RT-02/RT-04/RT-10 均被 13-02/13-05/13-06/13-07 共同声明（共享 ID 门阻塞），且计划 Flagged Assumptions 明确：RT-02 整体验收归 13-05 mock e2e + 13-07 真机；RT-04 测量口径归 13-07；RT-10 故障在 UI 的可见性归 13-05/13-07——本次只交付服务端半边证据
- **17 vs 16**：计划 artifacts 散文写"16 个消息结构体"，按结构体清点实为 17（AudioStart/AudioEnd 同形但独立）——以代码为准

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] fakeLLM 的 finish_reason=="tool_calls" 脚本以 wire JSON 惰性解析交付**
- **Found during:** Task 2
- **Issue:** 验收标准要求"fakeLLM 能配置返回 finish_reason=='tool_calls' 的脚本"，但 mimo.ChatResponse 的 ToolCalls/FinishReason 字段由同波并行的 13-02（独立工作树）添加——本工作树中这些字段不存在，直接引用无法编译；若 13-03 自行添加则与 13-02 在 pkg/mimo/types.go 必然合并冲突（13-03 的 files_modified 不含 pkg/mimo）
- **Fix:** llmScript 增加 wire 条目：原文保存 Pattern-4 工具调用样例、Chat() 调用时才 json.Unmarshal。今天断言可编译字段（role=="assistant"、content==""）与样例完整性（finish_reason/tool_calls 标记在）；波次合并后同一脚本自动解析出完整字段，13-04 亦可直接以 Go 值构造脚本——fake 代码零改动、零跨树冲突
- **Files modified:** pkg/rt/providers_fake_test.go
- **Verification:** TestFakesLLMWireToolCallsScript 绿；go test ./pkg/rt/ -count=1 全绿
- **Committed in:** adf5ae7（Task 2 commit）

**2. [Rule 1 - 契约矛盾修正] SessionReadyMsg 的 TTS 采样率字段形态**
- **Found during:** Task 1
- **Issue:** 计划结构体清单写扁平 `TTSSampleRate int`，但同一 action 的包注释、providers.go godoc（Task 2）、key_links 三处均以 `session_ready.tts.sampleRate` 为 wire 路径——扁平字段的 wire 路径是 `ttsSampleRate`，将使包注释成为谎言且 13-05 按注释实现的解析落空
- **Fix:** 定义嵌套 `TTSSpec{SampleRate int \`json:"sampleRate"\`}`，SessionReadyMsg.TTS 以 json:"tts" 嵌套——三处文档与 wire 实际一致
- **Files modified:** pkg/rt/protocol.go
- **Verification:** TestProtocolSessionReadyRoundTrip 断言 session_ready.tts.sampleRate==TTSOutputSampleRate
- **Committed in:** 6a0b5ce（Task 1 commit）

---

**Total deviations:** 2 auto-fixed（1 blocking、1 契约矛盾）
**Impact on plan:** 均为命名/交付形态级修正，不改变架构与职责边界。协议以代码冻结后即为 13-04/13-05 的唯一事实源。

## Issues Encountered

- TestHeartbeatRollsDeadline 首版失败：gorilla 客户端只在 ReadMessage 期间处理控制帧（默认 ping handler 的自动 pong 亦然），测试客户端静默 300ms 不读则从不回 pong、服务端 100ms 后按 deadline 断连。修复：测试期起后台 reader goroutine 持续读（pong 得以发出），静默后写帧证明读循环存活——这也记录了一个真实客户端约束：浏览器 WebSocket 自动回 pong 不受应用读取节奏影响，Go 测试客户端需要显式读
- 测试中途遭遇 API 限流中断（Task 2 提交后）：恢复后核对 git log/status 确认无部分提交状态，继续 Task 3

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- 13-04（turn/vad，wave 2）可用的全部契约就绪：SessionManager/Sender/ManagerFactory、三个供应商接口、fake 基建、协议错误码（ErrCodeProviderFailed 等供工具环错误路径）
- 13-05（前端，wave 2）可按 protocol.go 冻结的 wire 字段名实现 rt-client.js——ping/pong 时钟偏移估计的协议基础（t_client_ms 回显 + t_server_mono_ns 同锚）已落地
- 13-06（适配器 + /rt 挂载 + TLS）可照 cmd/service/main.go 的 /ws 挂载段并列挂载 HandleWebSocket；mimo_adapters 承接 AudioChunkFunc 的 base64 解码唯一归属
- 待合并事项：13-02 合并后 fakeLLM 的 wire 脚本自动获得 ToolCalls/FinishReason 解析（无需任何改动）

## Self-Check: PASSED

- [x] pkg/rt/protocol.go、protocol_test.go、providers.go、timing.go、timing_test.go、providers_fake_test.go、conn.go、conn_test.go 全部存在于磁盘并已提交
- [x] git log 含 6a0b5ce / adf5ae7 / d4caaa4 三个任务提交（base bd0a4b4 之上，rev-list 计数 3）
- [x] 计划级验证命令全部通过：go build ./... / go vet ./pkg/rt/ / go test ./pkg/rt/ -count=1 / go test ./pkg/ws/ -short -count=1（冻结栈零影响）
- [x] 工作树无未跟踪/未提交文件

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-01*
