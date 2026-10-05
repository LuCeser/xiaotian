---
phase: 13-realtime-minimal-loop
plan: 09
subsystem: api
tags: [go, websocket, state-machine, dictation-fsm, word-timer, timer-generation, vad-gating, echo-tail, asr-tolerance, invalid-response-retry, pause-resume]

# Dependency graph
requires:
  - phase: 13-realtime-minimal-loop/13-08
    provides: DictationTutor FSM 主环（dispatch 骨架、TurnTarget 捕获/首提交-幂等-冲突三分支、pendingPlayback ACK 门控、armWordTimer no-op 占位——本计划销项）、TurnManagerConfig.WordTimeout 字段
  - phase: 13-realtime-minimal-loop/13-07
    provides: pkg/mimo ErrEmptyTranscription / ErrInvalidResponse 双哨兵（errors.Is 判据）、pkg/rt StatePaused / ErrCodePlaybackAckTimeout / ReasonLessonComplete / ReasonNoDueWords、SentenceAckMsg 实际播放完成语义
provides:
  - "pkg/rt/dictation.go：四类口令模板纯函数（repeatText/slowReadText/pauseAckText/resumeAckText/confirmPausedRejectText，零 LLM、slow 不逐字拼写）、独立 paused 课堂位 + effectiveClientStateLocked、60 秒词计时器（armWordTimer/armWordTimerLocked/stopWordTimer/onWordTimeout/timeoutAdvance：timerGeneration 每次 arm/reset/stop 递增、回调锁内核验 generation+epoch+完整 TurnTarget+paused、processing 分支先于通用 state 拒绝重武装、confirm/timeout 竞争统一 advanceTarget 仅首提交生效、EventTimeoutFired 只由首提交发出）"
  - "pkg/rt/turn.go：dispatch 八意图穷尽 switch（无回落 chat 分支）、IngestAudio 三重静默门控（session_ready 起始保护默认 1000ms + playbackPending 播放期待确认窗 + ACK 驱动 VADEchoTailMs 默认 500ms 回声尾；lastPlaybackCompletedAt 只由 ack-driven settle 写入）、ASR 空转写容忍（EventSpeechEnded empty=true、零可见报错、不 reset 计时）、ASR ErrInvalidResponse 单次重试（同 ctx、attempts 进事件字段）、sender 保留（超时推进无请求上下文的播报通道）"
  - "pkg/rt/timing.go：EventTimeoutFired=timeout_fired 事件词表"
affects: [13-10 (PyFSRS WordSource 适配器——生产注入仍缺位), 13-12 (真机验收——窗口标定 1000/500ms、hint 泄题抽查、评分映射追认)]

# Actuals (#2632)
actuals:
  tokens: 38000      # chars/4 over the realized diff (~185K chars across 6 files, +1611/-53 lines)
  tasks: 3
  commits: 4          # MEASURED: git rev-list --count 5083e5f..HEAD（3 任务提交 + 1 Rule 3 测试补强）
commits: 4
plan_head_before: 5083e5f
plan_head_after: a34a42f

# Tech tracking
tech-stack:
  added: []           # 零新增依赖（T-13-SC：包安装面零变化）
  patterns:
    - "Pattern: 计时器 generation 失效语义——每次 arm/reset/stop 都递增 timerGeneration，已排队旧回调锁内先比 generation 再比 epoch + 完整 TurnTarget，同 epoch 同 index 的重置前回调也零副作用（T-13-16）"
    - "Pattern: 竞争统一出口——confirm 与 timeout 共用 advanceTarget（同幂等键 lessonID-advance-<index> + 同乐观锁），后到者只 replay Idempotent=true 对齐；outcome 记录/lesson_update/narration/EventTimeoutFired 只归首提交者"
    - "Pattern: 有效客户端状态覆盖位——课堂 paused 位独立于语音 processing/speaking 状态，settle/fail/interrupt 广播走 effectiveClientState；暂停期 uplink 不被暂停位门控，resume 经真实 ASR/分类路径识别"
    - "Pattern: 静默三窗门控——起始保护（session_ready+1000ms）、播放待确认窗（首个 audio_start 至有效 ACK）、回声尾（有效 ACK+500ms）全部门在 IngestAudio 入口，audio_end/speakTurn 返回不解除任何窗；interrupt 控制消息永不受门控"

key-files:
  created: []
  modified:
    - pkg/rt/dictation.go
    - pkg/rt/turn.go
    - pkg/rt/timing.go
    - pkg/rt/turn_test.go
    - pkg/rt/turn_integration_test.go
    - pkg/rt/timing_test.go   # Rule 3：事件词表锚同步（见 Deviations 1）

key-decisions:
  - "计时器字段（timerFactory/timerGen/wordTimer）落在 TurnManager 而非计划散文所述 DictationTutor：tutor 无回引 tm、回调闭包需持锁校验，单锁纪律要求句柄与生成计数随 tm.mu 同锁；文件归属（dictation.go）与计划 artifacts 一致"
  - "超时推进复用 confirm 的 advanceTarget（本次从 confirmTarget 抽取）：同幂等键同乐观锁，confirm/timeout 先后两种顺序都由执行器 Idempotent 语义裁决，首提交者独占 outcome/消息/播报/review——无平行的第二套推进代码"
  - "空转写容忍分支落在 Task 2（提前于计划的任务归属）：Task 2 验收明列「空转写不重置」且命名用例 TestWordTimerEmptyTranscriptionDoesNotReset 必须在该提交内绿——容忍判据是计时器语义的前置；Task 3 再补 ASR 重试半边与整合用例"
  - "集成 harness 适配 13-09 门控：itSendUtterance 增加 1.4s 起手等待（真实学生等老师说完+回声尾过；harness 以线速突发送帧，不等待则整句落在窗内被门吞掉——是测量伪影而非产品语义）、itServer 心跳 50ms→30s（起手等待期间不读帧，激进心跳会误杀连接）"
  - "processTurn 在空转写分支仍保留 turn_start/state 广播（13-08 既有序列，ASR 前发 turn_start）：truth「无轮次发起」按「无分类调用、无教师回合、无课程交互」执行；移动 turn_start 时序属 13-08 冻结面，不在此扩权"

patterns-established:
  - "Pattern: 门控窗口显式可注入——VADStartGraceMs/VADEchoTailMs 进 TurnManagerConfig（0→默认 1000/500），窗口控制逻辑 ACK 驱动、数值留待 13-12 真机标定"
  - "Pattern: 重试纪律三件套——同 ctx、最多一次、失败计数（attempts）进事件字段；ASR（Transcribe）与 LLM（classifyTurn 兜底）两条路径共用"

requirements-completed: [RT-03, RT-05]  # 共享 ID：RT-03 亦被 13-08/13-12 声明、RT-05 归 13-12 真机整体验收；mark-complete 由 orchestrator 按 shared-ID 门槛收口

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "四类口令 + 暂停/继续全交互集：repeat/slow 模板重读当前词（游标不动）、hint 记 again + 播模型 reply、meaning 播 ground truth reply、pause 置独立位并挂起计时、paused 期真实 processTurn 识别 resume 并在 ACK 后重武装、confirm 暂停期温和拒绝零推进"
    requirement: RT-03
    verification:
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestIntentRepeatReAnnouncesCurrentWord + TestIntentSlowReadsWordWithPausesNotSpelling + TestIntentHintRecordsAgainAndSpeaksReply + TestIntentMeaningSpeaksGroundedReply + TestIntentPauseAckLeavesClientPaused + TestIntentResumeRecognizedWhilePausedAndRearmsAfterAck + TestIntentConfirmWhilePausedIsGentleRejected + TestIntentDispatchSwitchCoversVocabulary（源断言：八意图穷尽、无 default）"
        status: pass
      - kind: command
        ref: "go test ./pkg/rt/ -count=1 -run \"TestIntent\" -v（8 PASS / 0 FAIL）"
        status: pass
    human_judgment: false
  - id: D2
    description: "60 秒词计时器：只在报词/回复最后一句有效 sentence_ack 后武装；timerGeneration 使同 epoch/index 的旧排队回调零副作用；锁内复核关闭+epoch+完整 TurnTarget+paused；processing 到点先于通用拒绝重武装；speaking 由 ACK 驱动不提前推进；confirm/timeout 双向竞争仅首提交产生 outcome/消息/播报/review，EventTimeoutFired 只由首提交发出"
    requirement: RT-03
    verification:
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestWordTimerArmsOnlyAfterPlaybackAck + TestWordTimerResetInvalidatesQueuedCallbackSameEpochAndIndex + TestWordTimerEmptyTranscriptionDoesNotReset + TestWordTimerProcessingBranchRearms + TestWordTimerSuspendedWhilePaused + TestStaleTimerIgnored + TestTimeoutConfirmRaceConfirmWins + TestTimeoutConfirmRaceTimeoutWins"
        status: pass
      - kind: command
        ref: "go test ./pkg/rt/ -count=1 -run \"TestWordTimer|TestWordTimeout|TestStaleTimer|TestTimeoutConfirmRace\" -v（8 PASS / 0 FAIL）"
        status: pass
    human_judgment: false
  - id: D3
    description: "VAD 静默期门控：起始保护期吞掉整句突发上行；audio_end 后延迟 ACK 仍挡普通 VAD；有效 ACK 启动 500ms 尾窗、窗内继续挡、过期放行；旧 epoch ACK 拒绝且零副作用；播放期 interrupt 控制消息仍立即生效；普通播报缺 ACK 有界可见（ErrorMsg playback_ack_timeout）且有序关闭、绝不伪造播放完成/计时起点/推进"
    requirement: RT-03
    verification:
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestVADStartGraceSuppressesEarlyAudio + TestVADEchoTailStartsAtPlaybackAck + TestVADEchoTailRejectsStaleAck + TestPlaybackPendingInterruptStillImmediate + TestPlaybackPendingTimeoutIsVisibleNoTimerAdvance"
        status: pass
      - kind: unit
        ref: "源断言：rg lastPlaybackCompletedAt 生产代码仅 settleListening 一处写入（turn.go:1274）"
        status: pass
    human_judgment: false
  - id: D4
    description: "ASR 空转写容忍（ErrEmptyTranscription → empty=true 事件、回 listening、零可见报错、不发起分类、计时不动）与 invalid-response 重试整合（ASR 侧单次重试新补 + LLM 侧既有兜底核对，同 ctx/最多一次/attempts 进事件字段）"
    requirement: RT-03
    verification:
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestASREmptyTranscriptionSilentlyIgnoredAndTimerUnchanged + TestASRInvalidResponseRetriedOnce + TestWordTimerEmptyTranscriptionDoesNotReset"
        status: pass
    human_judgment: false
  - id: D5
    description: "打断与口令共存 backstop（RT-05 组件级）：播放期门控下 interrupt 立即生效并解除门控；打断不重置词游标、不丢 outcome；既有 TestTurnInterrupt* / TestDictationOverConnInterruptMidAnnouncement 零改动通过"
    requirement: RT-05
    verification:
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestPlaybackPendingInterruptStillImmediate + TestTurnInterruptStopsAudioAndBumpsEpoch + TestTurnInterruptTrimsShortHistoryToPlayedSentences（既有，零改动通过）"
        status: pass
    human_judgment: false
  - id: D6
    description: "真机标定与产品判断面：1000/500ms 窗口默认值、hint/meaning 回复真实不泄题（T-13-17 真机抽查）、打断与口令共存整体验收（RT-05 probe 边缘）、话术听感——自动测试只锚定控制逻辑与提示词文本，数值与听感须 13-12 真机检查点收口"
    verification: []
    human_judgment: true
    rationale: "计划显式假设：静默期默认窗口为待真机标定参数（13-12 记录真实播后 0.5–1.5s 与更长静默观察）；hint 泄题归 13-12 检查点第 4 步抽查；RT-05 整体验收归 13-12/13-13"

# Metrics
duration: 60min
completed: 2026-10-05
status: complete
---

# Phase 13 Plan 09: 四类口令 + 60 秒词计时器 + 三缺陷服务端修复 Summary

**转向决策交互全集闭合：四类口令（重读/慢读/不会写提示/词义解释）与暂停继续在 FSM 内穷尽实现、60 秒每词计时器以 timerGeneration + 完整 TurnTarget 锁内校验落地并与 confirm/打断/暂停全部竞争确定、VAD 0 秒误触发的起始保护/回声尾门控、ASR 空转写容忍与 invalid API response 单次重试整合——24 个计划命名用例 + 全部既有用例全绿，零新增依赖、零 wire 形状变化、零 web/ 改动**

## Performance

- **Duration:** ~60 min（2026-10-05T16:20Z 前后 → 2026-10-05T17:16Z；按提交跨度 00:27:59→01:16:05 +08:00 计 48min 提交期 + 前置实现）
- **Started:** 2026-10-05T16:20Z 前后（首个编辑）
- **Completed:** 2026-10-05T17:16Z（末次提交 a34a42f）
- **Tasks:** 3 / 3
- **Files changed:** 6（全部为计划声明文件 + 1 个 Rule 3 词表锚文件，见 Deviations）

## Accomplishments

- **四类口令意图实现（RT-03）**：dispatch 从「confirm + default 回落 chat」重写为八意图穷尽 switch（源断言测试锚定无 default 分支）——repeat/slow 走模板纯函数重读当前词（`slowReadText` 词重复两遍句号分隔、禁逐字拼写的 prohibition 以测试断言：无 ASCII 字母/无「苹。果」式拆字）、hint 先记 `outcomeHint=again` 再播模型 reply（覆盖顺序留 13-12 追认注释）、meaning 播 ground truth reply、pause/resume 操作独立 paused 课堂位
- **暂停位与语音状态分离**：`DictationTutor.paused` 独立位 + `effectiveClientStateLocked()`——暂停确认播报期间内部 state 可为 speaking/processing，但 settle/fail/interrupt 广播经有效状态函数，播完后学生仍见 paused；暂停期 uplink 不被暂停位门控，resume 经真实 ASR/分类路径识别并在 ACK 后重武装计时；confirm 在 paused 时温和拒绝（零 lesson_update、零 executor 调用）
- **60 秒词计时器（RT-07 超时半边组件证据）**：`armWordTimer` 占位销项——只在报词/回复最后一句有效 sentence_ack 后武装（announceAcked / 意图回复 onAck）；每次 arm/reset/stop 递增 `timerGeneration`，回调锁内先比 generation 再校验关闭/epoch/**完整 TurnTarget**/paused（T-13-16：已排队旧回调同 epoch 同 index 也零副作用，注入 factory 捕获回调的确定性测试）；processing 到点在通用 `state != listening` 拒绝之前重武装；speaking 由 ACK 驱动无提前推进；confirm 与 timeout 经抽取的统一 `advanceTarget`（同幂等键 `lessonID-advance-<index>`）竞争，双向先后顺序测试锁死「首提交者独占 outcome/lesson_update/播报/review、EventTimeoutFired 只由首提交发出、后到者仅对齐」
- **VAD 静默期门控（T-13-18）**：IngestAudio 三窗门控——session_ready 起始保护（VADStartGraceMs 默认 1000ms，吞掉连接初期的整句高能量突发）、播放待确认窗（既有 playbackPending，audio_end/TTS 完成不解除）、ACK 驱动回声尾（有效 sentence_ack 才写 `lastPlaybackCompletedAt` 并启动 VADEchoTailMs 默认 500ms，尾窗过后恢复普通 VAD）；源断言：lastPlaybackCompletedAt 生产代码仅 settleListening 一处写入；interrupt 控制消息不受任何门控；普通播报缺 ACK 沿用 3..45s 有界等待 → ErrorMsg(playback_ack_timeout) + 有序关闭，绝不伪造完成/启动计时/推进词语
- **ASR 空转写容忍 + invalid response 重试整合（实测 ×4 缺陷）**：Transcribe 返回 `ErrEmptyTranscription` → EventSpeechEnded(empty=true)、静默回 listening、零用户可见报错、不发起分类、计时 generation 与句柄均不动（沉默不算互动）；Transcribe 返回 `ErrInvalidResponse` → 同 ctx 单次重试、再失败 failTurn 且 attempts=2 进事件字段；LLM 侧 classifyTurn 兜底同步补 attempts 字段——两处重试共用「同 ctx、最多一次、失败计数进事件字段」纪律
- **零回归**：13-08 全部 TestDictation*/TestClassify*/TestCompletion* 用例零改动通过；13-11 前端契约零触碰（`git diff 5083e5f..HEAD -- pkg/web` 为空）；wire 形状零变化（只加常量与事件字段，零消息类型增减）

## Task Commits

Each task was committed atomically:

1. **Task 1: 四类口令意图实现（repeat/slow/hint/meaning 的模板与 reply 路径）** - `5de4047` (feat)
2. **Task 2: 60 秒词计时器 — 武装/重置/挂起/到点推进** - `8ba3902` (feat)
3. **Task 3: 缺陷修复 — VAD 静默期门控 + ASR 空转写容忍 + 坏响应重试整合** - `a74e514` (feat)
4. **（Rule 3 补强）暂停/继续命名用例补齐计时器断言** - `a34a42f` (test)

**Plan metadata:** 本 SUMMARY 提交（docs(13-09)）

## Files Created/Modified

- `pkg/rt/dictation.go` - 四类口令模板纯函数、paused 位、effectiveClientStateLocked、计时器全套（armWordTimer/armWordTimerLocked/stopWordTimer/stopWordTimerLocked/onWordTimeout/timeoutAdvance）、classifyTurn 兜底 attempts 字段（Rule 3，见 Deviations 1）
- `pkg/rt/turn.go` - dispatch 八意图穷尽重写 + speakCurrentWordReading、TurnManagerConfig.VADStartGraceMs/VADEchoTailMs（默认 1000/500）、TurnManager 计时器字段 + sender 保留 + 门控时间戳字段、session_ready 三路径打点、IngestAudio 三窗门控重写、settleListening 尾窗 + 有效状态广播、failTurn/sendError 变参字段、processTurn 空转写分支 + ASR 单次重试、confirmTarget→advanceTarget 抽取、internalClose 停表、armWordTimer 占位删除
- `pkg/rt/timing.go` - EventTimeoutFired = "timeout_fired"
- `pkg/rt/turn_test.go` - captureSender.stateCount / ttSentencesSince / ttIntentFixture(Cfg) / ttRunIntentTurn / ttPaused / ttTimerState / ttUtterance 助手；TestIntent* 七用例 + switch 源断言；TestWordTimer*/TestStaleTimer*/TestTimeoutConfirmRace* 八用例；TestVAD*/TestASR*/TestPlaybackPending*/TestPausedStillRecognizes* 八用例
- `pkg/rt/turn_integration_test.go` - TestDictationOverConnEarlyNoiseDoesNotStartTurn；itSendUtterance 起手等待 + itServer 心跳调整（Rule 3，见 Deviations 3）
- `pkg/rt/timing_test.go` - 事件词表锚增 EventTimeoutFired（Rule 3，见 Deviations 1）

## Must-Have Truths Verification

| Truth | Evidence | Status |
|-------|----------|--------|
| 四类口令全部确定性行为；暂停位与语音 state 分离，播报期间可 speaking/processing、播完仍 paused，暂停期继续收音识别 resume，confirm 温和拒绝 | TestIntent* 七用例 + TestIntentDispatchSwitchCoversVocabulary 源断言 + TestPausedStillRecognizesResumeThroughRealProcessTurn | PASS |
| 60 秒计时只在有效 sentence_ack 后武装；每次 arm/reset/stop 递增 timerGeneration；回调锁内核验 generation+epoch+完整 TurnTarget+暂停位；空转写不重置；processing 到点先于通用拒绝处理为重武装 | TestWordTimerArmsOnlyAfterPlaybackAck + TestWordTimerResetInvalidatesQueuedCallbackSameEpochAndIndex（注入 factory 捕获「已排队」回调）+ TestStaleTimerIgnored + TestWordTimerProcessingBranchRearms + TestWordTimerEmptyTranscriptionDoesNotReset | PASS |
| 回声保护覆盖整段待确认期：首个 audio_start 前门控、服务端 audio_end/TTS 完成不解除、匹配 ACK 后才启动 500ms 尾窗；session_ready 起始静默期默认 1000ms；interrupt 控制消息始终立即生效 | TestVADEchoTailStartsAtPlaybackAck（audio_end 后延迟 ACK 仍挡）+ TestVADStartGraceSuppressesEarlyAudio + TestVADEchoTailRejectsStaleAck + TestPlaybackPendingInterruptStillImmediate；源断言 lastPlaybackCompletedAt 单一写入点 | PASS |
| MiMo invalid API response ×4 缺陷修复：ASR ErrEmptyTranscription → 静默回 listening（事件记录、无可见报错、无轮次发起）；分类调用 ErrInvalidResponse → 重试一次（13-08 兜底半边 + 本计划 ASR 半边整合） | TestASREmptyTranscriptionSilentlyIgnoredAndTimerUnchanged + TestASRInvalidResponseRetriedOnce + TestClassifyRetriesOnceOnInvalidJSON/FallsBackToChat（既有，零改动通过） | PASS |
| 打断与四类口令共存（backstop）：打断停播清队后口令下一轮正常生效；打断不重置词游标、不丢 outcome | TestPlaybackPendingInterruptStillImmediate + 既有 TestTurnInterruptStopsAudioAndBumpsEpoch / TestTurnInterruptTrimsShortHistoryToPlayedSentences / 集成 InterruptMidAnnouncement 零改动通过 | PASS |

## Threat Mitigations Delivered

| Threat | Disposition | Where |
|--------|-------------|-------|
| T-13-16 (DoS, 旧计时回调重置后仍推进, high) | mitigate | timerGeneration 每次 arm/reset/stop 递增；回调锁内先比 generation、再比 epoch + 完整 TurnTarget + paused；注入 factory 捕获回调构造「已排队才获锁」的确定性复现（TestWordTimerResetInvalidatesQueuedCallbackSameEpochAndIndex）；TestStaleTimerIgnored 四种过期维度零副作用 |
| T-13-17 (Tampering, hint/meaning reply 泄整词写法, medium) | mitigate (R0 范围) | system 提示硬约束已在 13-08 冻结（源断言测试）；slowReadText 模板级禁拼写以测试断言（无 ASCII 字母、无逐字拆分）；模型真实输出抽查归 13-12 检查点第 4 步；R0 不做输出过滤（词表开放、误杀率高，判断级 prohibition 携带） |
| T-13-18 (DoS, 门控误伤真实起句 / 0 秒误触发, medium) | mitigate | 起始保护 + 播放待确认窗 + ACK 驱动 500ms 尾窗全部在 IngestAudio 入口；interrupt 不受门控；缺 ACK 有界报错并结束会话、不伪造播放完成/计时起点；窗口数值 1000/500ms 为待真机标定参数（13-12 记录误触发/漏检观察） |
| T-13-18b (Spoofing, paused UI 与内部状态分裂, medium) | mitigate | effectiveClientStateLocked 统一对外状态；TestIntentPauseAckLeavesClientPaused 断言播完仍 paused；TestPausedStillRecognizesResumeThroughRealProcessTurn 走真实 processTurn 验证 resume 识别 |
| T-13-SC (Tampering, 包安装, high) | mitigate | 零新增依赖，包安装面零变化 |

## Decisions Made

见 frontmatter key-decisions（计时器字段落位 TurnManager、advanceTarget 统一出口、空转写分支任务归属前移、集成 harness 门控适配、turn_start 序列不动的 truth 口径）。

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Task 3 计划文件清单外的两处同步改动：pkg/rt/dictation.go 与 pkg/rt/timing_test.go**
- **Found during:** Task 3（重试整合核查 + 事件锚）
- **Issue:** 「两处重试共用失败计数进事件字段」纪律要求 LLM 半边（classifyTurn，位于 dictation.go）补 attempts 字段；timing.go 新增 EventTimeoutFired 后冻结词表锚测试（timing_test.go）须同步增行，否则违反 13-08 起的「事件词表换新保持冻结词表测试」纪律
- **Fix:** classifyTurn 兜底 EventError 增 `attempts: 2`；timing_test.go 词表锚增 EventTimeoutFired 行
- **Files modified:** pkg/rt/dictation.go, pkg/rt/timing_test.go
- **Verification:** TestClassifyFallsBackToChatAfterSecondFailure 零改动通过（只加字段）；TestTimingEventVocabulary 全绿
- **Committed in:** a74e514（Task 3）

**2. [Rule 3 - Blocking] ASR ErrEmptyTranscription 容忍分支随 Task 2 落地（计划将其 action 项列于 Task 3）**
- **Found during:** Task 2（命名用例 TestWordTimerEmptyTranscriptionDoesNotReset 首跑即红）
- **Issue:** Task 2 验收明列「空转写不重置」且其 verify 过滤器必须命中该命名用例——容忍判据是计时器语义的前置条件，容忍分支不存在则 Task 2 提交必然带红测试
- **Fix:** processTurn 增加 errors.Is(err, mimo.ErrEmptyTranscription) 分支（empty=true 事件 + settle + 不 reset）；Task 3 再补 ASR ErrInvalidResponse 单次重试半边与整合用例（TestASREmptyTranscriptionSilentlyIgnoredAndTimerUnchanged），两个命名用例各归其位
- **Files modified:** pkg/rt/turn.go（Task 2 提交 8ba3902 内）
- **Verification:** 两个用例分别在 Task 2 / Task 3 的 verify 输出中 PASS
- **Committed in:** 8ba3902（分支）、a74e514（整合用例）

**3. [Rule 3 - Blocking] 集成 harness 适配 13-09 门控窗口（itSendUtterance 起手等待 + itServer 心跳调整）**
- **Found during:** Task 3（全包回归：TestDictationOverConnHappyPath / InterruptMidAnnouncement 稳定失败，定位耗 ~30min）
- **Issue:** harness 以线速突发送帧——整句上行（80 帧 2.4s 名义时长）在实际 ~2ms 内到达，整句落入起始保护窗（默认 1000ms）或回声尾窗内被门整体吞掉，turn 永不发起（这是突发投递的测量伪影：真实客户端按 30ms/帧实时上行，窗内最多丢前 ~500ms 起音，VAD 随后正常开段）；另 1.4s 起手等待期间 harness 不读帧，50ms 测试心跳的 100ms 读截止无 pong 应答即杀连接
- **Fix:** itSendUtterance 增加 1.4s 起手等待（真实学生行为：等老师播完+回声尾过再开口）；itServer 心跳 50ms→30s（这些用例不断言空闲踢除）
- **Files modified:** pkg/rt/turn_integration_test.go
- **Verification:** TestDictationOverConn* 六连跑全绿（默认 1000/500ms 生产窗口下）；全包 -count=1 两次全绿
- **Committed in:** a74e514（Task 3）

**4. [Rule 3 - Minor] 命名用例的计时器断言补齐（Task 1 用例断言面在 Task 2 机制就绪后强化）**
- **Found during:** 收尾自检（Task 1 的 TestIntentResume...AndRearmsAfterAck / TestIntentPauseAckLeavesClientPaused 在 Task 1 提交时计时器仍是占位，只断言了 FSM 层行为）
- **Issue:** 命名中「Rearms」与 pause 的「先停止 timer」缺少直接断言（重武装行为当时仅由 Task 2 用例间接覆盖）
- **Fix:** 两用例补 ttTimerState 断言——pause 后句柄 nil、resume ACK 后句柄非 nil
- **Files modified:** pkg/rt/turn_test.go
- **Verification:** TestIntent 过滤器 8 PASS / 0 FAIL
- **Committed in:** a34a42f（独立 test 提交）

---

**Total deviations:** 4 auto-fixed（3 blocking + 1 minor，全部为计划意图的机械落地、编译/验收强制前置、或测试基建同步；零范围蔓延）
**Impact on plan:** 无。计划级验证命令三组过滤器均按计划原文命中（Task 1 TestIntent 8 PASS、Task 2 计时器过滤器 8 PASS、Task 3 门控/ASR/集成过滤器 20 PASS），无 13-07 式过滤漂移；零新增依赖、零 wire 形状变化、零 web/ 改动、零 STATE/ROADMAP 触碰。

## Known Stubs

| Stub | File | Line | Reason |
|------|------|------|--------|
| TurnManagerConfig.Words 生产注入缺位：session_init 现答 lesson_unavailable | cmd/service/main.go | 564-569 | 13-08 登记的计划边界，本计划不涉——13-10（PyFSRS WordSource 适配器）注入（**13-08 登记表中 armWordTimer 占位一项就此销项**：已在 dictation.go 实现全量计时器，见 Task 2） |

## Issues Encountered

- 顺序执行器在主工作树（main 分支）直接提交（config `branching_strategy: none`，与 13-01～13-11 一致）；每任务仅暂存计划声明文件 + Rule 3 文件，工作区内无关改动（.gitignore、STATE.md、CONTEXT.md、pkg/infra、pkg/tts 等 EOL 噪声、根目录未跟踪 scratch 文件）一律未触碰
- 集成失败定位（Deviation 3）约耗 30 分钟：先经二分（stash 回 Task 2 HEAD → 5/5 稳定）确认 Task 3 引入，再以临时 println 插桩捕获「整句落在窗内被吞」的直接证据后移除插桩；修复为 harness 行为对齐真实客户端节奏，产品门控语义未因此松动

## User Setup Required

None - no external service configuration required.

## Explicit Assumptions Registered（计划带入，供 13-12 检查点）

- 显式假设：静默期默认窗口（起始 1000ms / 回声尾 500ms）为待真机标定参数——窗口起点控制已由 ACK 驱动，13-12 只标定数值并记录老师真实播完后 0.5–1.5s 与更长静默的误触发/漏检观察
- 显式假设：hint 先记 again 与后续 confirm/timeout 的最终评分覆盖顺序为默认提案（outcomeRating 注释），13-12 用户确认后固化
- RT-03（四类口令场景）、RT-05（打断与口令共存）为 probe 边缘：组件级以本计划 truths + 测试为口径，真机整体验收归 13-12/13-13

## Next Phase Readiness

- 13-10（PyFSRS 适配器）：WordSource 接口与注入点就绪（Words nil → lesson_unavailable 诚实降级不变）；计时器/口令/门控全部就位，生产取词接通即可真机
- 13-12（验收）：三组真机标定/抽查/追认入口已备——窗口数值（VADStartGraceMs/VADEchoTailMs 可经 config 注入）、hint 泄题抽查、评分映射追认（confirmed→good / timeout→hard / hint→again）
- stub 台账：armWordTimer 占位（13-08 登记）销项；Words 生产注入（13-08 登记）归 13-10
- RT-03 为共享 ID（13-08/13-12 亦声明）、RT-05 归 13-12 真机整体验收——REQUIREMENTS 勾选由 orchestrator 按 shared-ID 门槛收口

## Self-Check: PASSED

- [x] 6 个修改文件全部在磁盘且已提交；零新建文件（计划 artifacts 为既有文件扩展面）
- [x] 提交 5de4047 / 8ba3902 / a74e514 / a34a42f 均在 git log（HEAD=a34a42f，git rev-list --count 5083e5f..HEAD=4 实测与 narrated 一致）
- [x] 24 个计划命名用例逐一在测试源中存在（脚本核对 func 名全集）
- [x] 三组任务 verify 过滤器按计划原文执行：TestIntent 8 PASS / 计时器组 8 PASS / 门控-ASR-集成组 20 PASS，三组 0 FAIL；`go vet ./pkg/rt/` 通过
- [x] 计划级验证：go build ./... 通过；go test ./pkg/rt/ -count=1 连续两次全绿；go test ./pkg/rt/ ./pkg/lesson/ ./pkg/mimo/ -count=1 全绿
- [x] 零回归：13-08 全部 FSM/分类/完结用例零改动通过；`git diff 5083e5f..HEAD -- pkg/web` 为空（13-11 前端契约零触碰）
- [x] 无 STATE.md / ROADMAP.md 改动；无未登记 stub（Words 注入缺位为 13-08 登记的既有边界，armWordTimer 占位销项）
- [x] 零新增依赖（go.mod 零变化）、零新增消息类型（wire 形状冻结）

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-05*
