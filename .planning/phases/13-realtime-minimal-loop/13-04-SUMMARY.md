---
phase: 13-realtime-minimal-loop
plan: 04
subsystem: api
tags: [websocket, vad, fsm, tool-calling, streaming-tts, go]

requires:
  - phase: 13-03
    provides: pkg/rt protocol/providers/timing/conn 基础三件套（SessionManager 契约、AudioChunkFunc、EventRecorder）
  - phase: 13-02
    provides: pkg/mimo tools/tool_calls/thinking 关闭（WithTools/WithThinkingDisabled、ToolCallsRequested）
  - phase: 13-01
    provides: pkg/lesson 幂等工具执行层（Lesson/AdvanceResult/ConflictError）
provides:
  - rt.VAD 能量切句（hysteresis/hangover/pre-roll/30s 硬切）
  - rt.TurnManager 轮次 FSM（LISTENING→PROCESSING→SPEAKING）+ epoch 单调取消 + MiMo 工具环 + 逐句流式 TTS + 上下文裁剪 + 半双工门控
  - rt.LessonExecutor 接口（签名逐字对齐 pkg/lesson，13-06 编译期对接）
  - 进程内整链集成测试（binary 帧→VAD→fake 供应商→SendAudio 字节级回归）
affects: [13-06 服务接线, 13-07 真机验证, pkg/rt]

actuals:
  tokens: 23000
  tasks: 3
  commits: 5

tech-stack:
  added: []
  patterns:
    - "epoch 单调取消令牌：迟到 TTS chunk 按 epoch 丢弃（ErrStaleEpoch）"
    - "工具环先执行后播报：tool_calls 本地执行、role:tool 回填再请求，最终文本才进 TTS"
    - "打断历史裁剪统一语义：speakTurn 先 append 全文记索引，interrupt 按已播句裁剪，工具环消息永不裁剪"

key-files:
  created:
    - pkg/rt/vad.go
    - pkg/rt/vad_test.go
    - pkg/rt/turn.go
    - pkg/rt/turn_test.go
    - pkg/rt/turn_integration_test.go
  modified: []

key-decisions:
  - "TTS chunk 回调参数为裸 s16le 字节（13-03 AudioChunkFunc），turn.go 零 base64 解码——解码唯一归属 13-06 MimoTTS 适配器（M-1）"
  - "打断历史语义：spoken-text 消息按已播句裁剪（零播放整条移除）；工具环进行中 interrupt 不裁剪仅取消（M-2）"
  - "validateToolArgs 判定 lesson_id == cfg.LessonID 会话绑定，不符即 invalid_arguments 回喂不调 executor（M-5/RESEARCH V4）"
  - "request_id 取 toolCall.ID（不由 LLM 提供），经 13-01 幂等事务防重复推进"

patterns-established:
  - "Pattern: 每跳工具环入口校验 currentEpoch()==发起 epoch，失效整轮中止不再发起 Chat/TTS"
  - "Pattern: 半双工门控在 IngestAudio 入口（state!=listening 直接丢弃并计数）"

requirements-completed: [RT-03, RT-05, RT-07]

coverage:
  - id: D1
    description: "能量 VAD 切句：hysteresis/hangover/pre-roll/MaxSegmentMs 30s 硬切，Feed 非整帧输入无样本丢失"
    requirement: RT-03
    verification:
      - kind: unit
        ref: "pkg/rt/vad_test.go#TestVADSilenceNoSegment/TestVADSpeechThenSilenceCloses/TestVADHardCutAtMax/TestVADShortGapMerged/TestVADRMSFrameCalc"
        status: pass
    human_judgment: false
  - id: D2
    description: "TurnManager 轮次核心：FSM、epoch 取消、MiMo 工具环（先执行后播报）、逐句流式 TTS、上下文裁剪到实际已播句、半双工门控、时序事件"
    requirement: RT-05
    verification:
      - kind: unit
        ref: "pkg/rt/turn_test.go（工具环/epoch/历史裁剪/门控/参数校验/splitSentences 行为测试全绿）"
        status: pass
      - kind: unit
        ref: "go vet ./pkg/rt/"
        status: pass
    human_judgment: false
  - id: D3
    description: "进程内整链集成：WSS 连接→session_init→VAD 关段→fake ASR/LLM/TTS→二进制回程字节级相等；播放中 interrupt 后无迟到音频帧"
    requirement: RT-07
    verification:
      - kind: integration
        ref: "pkg/rt/turn_integration_test.go#TestTurnOverConnHappyPath/TestTurnOverConnInterruptMidStream"
        status: pass
    human_judgment: false

duration: 20min
completed: 2026-10-02
status: complete
---

# Phase 13: R0 真机最小闭环 — Plan 04 Summary

**pkg/rt 轮次管理核心：能量 VAD 切句 + TurnManager FSM（epoch 取消、MiMo 工具环、逐句流式 TTS、打断历史裁剪、半双工门控），fake 供应商驱动的全套单测 + websocket 整链集成测试**

## Performance

- **Duration:** ~20 min 有效执行（跨两次会话：VAD 00:04-00:05，TurnManager+集成 08:35-08:41）
- **Started:** 2026-10-02T00:04 (UTC+8)
- **Completed:** 2026-10-02T08:41 (UTC+8)
- **Tasks:** 3
- **Files modified:** 5（全部新建）

## Accomplishments

- 能量 VAD 切句落地：hysteresis 开段、hangover 关段、pre-roll 段首回填、30s 硬切防 DataURL 超限，五个行为测试锚定
- TurnManager 实现完整轮次闭环：LISTENING→PROCESSING→SPEAKING FSM、epoch 单调取消令牌（迟到 TTS chunk 丢弃）、MiMo 工具环（先执行后播报、request_id=toolCall.ID 幂等）、逐句流式 TTS 收即转发、上下文裁剪到实际已播放句、半双工门控、ping/pong 时钟偏移应答
- 打断历史语义按 M-2 裁定统一：spoken-text 消息按已播句裁剪（零播放整条移除），工具环消息（tool_calls/role:tool）永不裁剪
- 进程内整链集成测试：binary 帧→VAD→fake 供应商→SendAudio 字节级回归 + 播放中 interrupt 无迟到帧

## Task Commits

Each task was committed atomically:

1. **Task 1: vad.go 能量 VAD 切句** - `0fdd43c` (test) + `3b59ac2` (feat)
2. **Task 2: turn.go 轮次 FSM + epoch 取消 + MiMo 工具环 + 逐句流式 TTS** - `81185a0` (test) + `61d2e95` (feat)
3. **Task 3: turn_integration_test.go 整链集成** - `7789dc4` (test)

## Files Created/Modified

- `pkg/rt/vad.go` - VADConfig 七字段 + VAD 状态机（Feed/Reset/FrameRMS 纯函数）
- `pkg/rt/vad_test.go` - 五个 VAD 行为测试（合成 PCM 驱动）
- `pkg/rt/turn.go` - TurnManager：HandleControl（session_init/interrupt/sentence_ack/ping/end_session）、IngestAudio、processTurn 工具环、speakTurn 流式播放、Close
- `pkg/rt/turn_test.go` - 工具环/epoch/裁剪/门控/参数校验行为测试
- `pkg/rt/turn_integration_test.go` - httptest + gorilla Dialer 整链两用例

## Decisions Made

None - followed plan as specified（M-1/M-2/M-5 审查裁定均已按计划落地）

## Deviations from Plan

None - plan executed exactly as written

## Issues Encountered

None

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- pkg/rt 轮次核心就绪，13-06 可接线：TurnManager 实现 SessionManager（`var _ SessionManager` 编译期检查在位）；LessonExecutor 签名逐字对齐 pkg/lesson
- RT-03/RT-05/RT-07 组件级证据齐备；3 词场景与真机时序整体验收归 13-07（Flagged Assumptions 登记在案）
- 收尾备注：本 SUMMARY 由 orchestrator 于 2026-10-02 人工收尾补写——上次会话在 worktree 合并后、SUMMARY 写入前中断；commit 证据与验证（go build ./... / go vet / go test ./pkg/rt/ ./pkg/lesson/ -count=1 全绿）已复核

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-02*
