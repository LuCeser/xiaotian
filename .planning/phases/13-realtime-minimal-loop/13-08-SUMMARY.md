---
phase: 13-realtime-minimal-loop
plan: 08
subsystem: api
tags: [go, websocket, state-machine, dictation-fsm, intent-classification, idempotency, optimistic-concurrency, playback-ack, tts-streaming]

# Dependency graph
requires:
  - phase: 13-realtime-minimal-loop/13-07
    provides: pkg/lesson Word/EnsureLessonCards（首内容优先、legacy 空 Cards 边界）、pkg/rt ErrLessonUnavailable/ErrCodePlaybackAckTimeout/ReasonLessonComplete/ReasonNoDueWords、SentenceAckMsg 实际播放完成语义
  - phase: 13-realtime-minimal-loop/13-01
    provides: pkg/lesson Executor.AdvanceWord 幂等事务（requestID 主键 + expectedVersion 乐观锁）与 ConflictError
  - phase: 13-realtime-minimal-loop/13-04
    provides: TurnManager 传输骨架（epoch 取消、speakTurn 字幕先行/收即播、VAD 半双工门控）
provides:
  - "pkg/rt/dictation.go：WordSource 接口（FetchDueWords/SubmitReview + ReviewWriteResult 真实集合）、DictationTutor 状态机（词游标/perWordOutcome/评分映射/短历史 deque）、模板话术纯函数（开场/报词/总结/无词/失败后缀，零 LLM）、八值意图分类管线（单次 Chat + 严格 JSON + 白名单 + 一次纠正重试 + 确定性兜底）、r0-YYYYMMDD 课次派生"
  - "pkg/rt/turn.go 重排：session_init 按 lessonID 进程内串行化（唯一创建者 RunDue→EnsureLessonCards；零词 no_due_words / legacy 空 Cards lesson_unavailable / 已完结 lesson_complete 三显式路径）、开场 greeting 即首问候（零 LLM 调用）、TurnTarget 分类前捕获 + 首提交/幂等/冲突互斥分支、pendingPlayback（epoch+sentence_id，audio_start 前登记、粘性完成位）、播完 ACK 门控（TTS 完成/audio_end 不解除普通上行门控）、有界 ACK 等待（未确认 PCM 推导 + 3s 余量夹取 3..45s）、超时只发 ErrorMsg(playback_ack_timeout) 且自然结束路径仍发原 reason、interrupt 使旧 epoch 等待失效并裁剪 FSM 短历史、armWordTimer 占位（13-09 实现）"
  - "pkg/rt/timing.go：word_announced/word_advanced/intent_classified/lesson_complete/review_written/playback_ack_timeout/sentence_ack_rejected 事件词表（tool_start/tool_end/lesson_version 随 tool 环退场）"
  - "tool 环退场：lessonToolDefs/executeToolCall/validateToolArgs/toolErrorResult 与 TurnManagerConfig.LessonWords/MaxToolLoops/HistoryMaxTurns 全部删除，无死代码残留"
affects: [13-09 (词计时器/空转写容忍/回声尾), 13-10 (PyFSRS WordSource 适配器), 13-11 (前端 paused/playback_ack_timeout 处理), 13-12 (验收检查点——评分映射追认)]

# Actuals (#2632)
actuals:
  tokens: 33000      # chars/4 over the realized diff (~132K chars across 9 files, +2749/-1039 lines)
  tasks: 3
  commits: 3          # MEASURED: git rev-list --count 7068035..e671023
commits: 3
plan_head_before: 7068035b0793c579e26de8a69a001d3c17c7082b
plan_head_after: e67102324b2b9fa4f986e55673815f685af8d6a3

# Tech tracking
tech-stack:
  added: []           # 零新增依赖（T-13-SC：包安装面零变化）
  patterns:
    - "Pattern: 节奏所有权单真相源——课堂推进只经 TurnTarget（分类前锁内捕获）派生的幂等键与乐观锁；首提交/Idempotent/Conflict 三分支副作用互斥，迟到 confirm 绝不按返回后 currentIndex 重解释"
    - "Pattern: 播完 ACK 门控——pending 项与 audio_start 同锁先登记、粘性完成位保证早到最终 ACK 不丢、等待上限按全部未确认排程 PCM 推导并有界夹取；超时只表达超时本身（ErrorMsg code），永不伪造完成证据"
    - "Pattern: LLM 输出不可信——严格 JSON 白名单解析 + 一次纠正重试 + 确定性口头兜底（用户零可见 ErrorMsg）+ 服务端 error 事件；reply 进 TTS 前统一 500 rune 截断"

key-files:
  created:
    - pkg/rt/dictation.go
  modified:
    - pkg/rt/turn.go
    - pkg/rt/timing.go
    - pkg/rt/mimo_adapters.go
    - pkg/rt/turn_test.go
    - pkg/rt/turn_integration_test.go
    - pkg/rt/providers_fake_test.go
    - pkg/rt/timing_test.go
    - cmd/service/main.go

key-decisions:
  - "开场 greeting 由 session_init 后的专用 goroutine 立即播报（读循环不被 ACK 等待阻塞）；话术为纯模板函数，首次可听延迟只含 FSRS 取词与 TTS 首包——首问候缺失缺陷就此修复（RT-04 结构前提）"
  - "AdvanceWord 幂等键从 MiMo tool_call id 改为 '%s-advance-%d'（lessonID+目标词序号，TurnTarget 派生）：节奏归服务端后幂等键不再依赖模型产出的任何标识（T-13-15c）"
  - "完结评分映射 confirmed→good/timeout→hard/hint→again 为实现裁量，表上注释注明 13-12 检查点提请用户追认（计划显式假设 1）"
  - "courseID 派生 r0-YYYYMMDD 用 time.FixedZone('CST',8*3600)（北京日历，不依赖 tzdata），同日重连经 EnsureLessonCards 首内容优先天然续课"
  - "lesson_unavailable/playback_ack_timeout 的用户可见文案均为固定人话常量（T-13-14），python 路径等细节只进服务端结构化日志"
  - "已删事件 tool_start/tool_end/lesson_version 与新增 FSM 事件词表共存于同一冻结词表测试——旧标识随 tool 环退场，重命名即 wire 变化的纪律不变"

patterns-established:
  - "Pattern: 播放完成协调三件套——pending 先于 audio_start 登记、epoch+sentence_id 精确匹配一次消费、重复/旧/未知 ACK 只发脱敏诊断零副作用"
  - "Pattern: 自然结束降级顺序——ACK 超时先 ErrorMsg(playback_ack_timeout) 再发携带原 lesson_complete/no_due_words reason 的 session_ended 后有序关闭；普通播报超时只有序关闭"

requirements-completed: [RT-02, RT-03, RT-04, RT-07]  # 共享 ID：RT-02/03/04/07 亦被 13-09/13-12 等声明，mark-complete 由 orchestrator 按 shared-ID 门槛收口

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "状态机默写主环端到端：session_ready 词表 → 开场 greeting（零 LLM）即首问候+首词报词、报词 PCM 字节到达客户端 → 脚本化 ASR 确认 → lesson_update → 次词报词 → 完结总结 → 匹配 ACK 后 session_ended(lesson_complete) → SubmitReview 全量一次"
    requirement: RT-02
    verification:
      - kind: integration
        ref: "pkg/rt/turn_integration_test.go#TestDictationOverConnHappyPath（真实 WSS 全链：词表/音频字节/版本 2-3-4 递增/ACK 门/handle 完结/提交 3 词）"
        status: pass
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestDictationOpeningGreetsAndAnnounces + TestDictationConfirmAdvancesAndAnnouncesNext"
        status: pass
    human_judgment: false
  - id: D2
    description: "幂等推进：TurnTarget 分类前捕获；首提交记录 outcome+lesson_update+EventWordAdvanced+报次词或完结，Idempotent 命中只对齐零副作用，版本冲突只回读对齐——互斥且不双跳"
    requirement: RT-07
    verification:
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestDictationDoubleConfirmIdempotent（二次只对齐：无 lesson_update/无播报/无事件/无完结）"
        status: pass
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestDictationVersionConflictAlignsWithoutSideEffects（无重试写/无播报/回读对齐）"
        status: pass
    human_judgment: false
  - id: D3
    description: "session_init 三显式路径：零到期词（session_ready 空表 + 说明句 ACK 门后 no_due_words）、legacy 空 Cards（lesson_unavailable 可见失败，不 RunDue 不伪填充）、已完结重连（完成态恢复，不重复总结/SubmitReview）"
    requirement: RT-03
    verification:
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestLegacyEmptyCardsIsLessonUnavailable + TestCompletedReconnectDoesNotReviewOrNarrate"
        status: pass
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestNoDueWordsWaitsForPlaybackAck"
        status: pass
      - kind: integration
        ref: "pkg/rt/turn_integration_test.go#TestDictationOverConnNoDueWaitsForAck"
        status: pass
    human_judgment: false
  - id: D4
    description: "播放完成协调：pending 于 audio_start 前登记、epoch+sentence_id 校验、重复/旧/未知 ACK 零副作用、早到最终 ACK 粘性不丢、TTS 完成/audio_end 不解除上行门控、有界超时（先 ErrorMsg 后原 reason）、断线取消等待"
    requirement: RT-03
    verification:
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestDictationRegistersPendingBeforeAudioStartAndObservesEarlyFinalAck + TestSentenceAckRejectsStaleUnknownAndDuplicate"
        status: pass
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestPlaybackAckTimeoutIsVisibleAndBounded（超时 ErrorMsg 先于原 reason、无伪造完成/计时武装、上限内有序关闭）"
        status: pass
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestCompletionDisconnectCancelsAckWait"
        status: pass
      - kind: integration
        ref: "pkg/rt/turn_integration_test.go#TestDictationOverConnInterruptMidAnnouncement（旧 epoch ACK 失效、零迟到帧、课堂仍可推进）"
        status: pass
    human_judgment: false
  - id: D5
    description: "意图分类契约：八值词表+词义/例句 ground truth 注入+严格 JSON 输出契约（hint 禁泄题、meaning 不编造）；非法输出一次纠正重试后确定性口头兜底 + 服务端 error 事件"
    requirement: RT-02
    verification:
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestClassifyMessagesCarryVocabGroundTruthAndSafety（源断言：词表/ground truth/儿童安全禁令/泄题与编造规则在 system 提示）"
        status: pass
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestClassifyRetriesOnceOnInvalidJSON + TestClassifyFallsBackToChatAfterSecondFailure"
        status: pass
    human_judgment: false
  - id: D6
    description: "完结诚实记录：先 SubmitReview 后播总结；ReviewWriteResult 实际集合决定口径——全量/部分/零更新/真实错误分别以真实计数、'本次保存 M/N 个'、记录未保存后缀呈现，SubmitReview 仅一次不盲重试"
    requirement: RT-07
    verification:
      - kind: unit
        ref: "pkg/rt/turn_test.go#TestCompletionWritesReviewAndWaitsForPlaybackAck + TestCompletionPartialReviewReportsActualCount + TestCompletionZeroUpdatedWithNilErrorIsNotSuccess + TestCompletionReviewFailureSpeaksHonestSuffix"
        status: pass
    human_judgment: false
  - id: D7
    description: "话术价值观落地（计划 prohibition #1/#2/#3）：儿童安全禁令原文迁入分类系统提示、词义仅据词库 ground truth（缺失如实说明）、学习记录写入失败如实可见不口头宣称已保存——文本存在性已由 D5/D6 测试断言，实际语音体验的语气温感归真人 UAT"
    verification: []
    human_judgment: true
    rationale: "话术鼓励式程度与小学生听感是产品判断级验收（计划 flagged: unverified）；自动化只能锚定提示词文本与失败话术后缀，TTS 实际听感需 13-12 真机检查点由用户评判"

# Metrics
duration: 41min
completed: 2026-10-05
status: complete
---

# Phase 13 Plan 08: 状态机默写主环（dictation.go + turn.go 管线重排 + 测试收口）Summary

**课堂节奏所有权从 LLM 工具环收回服务端 DictationTutor 状态机：开场即首问候（零 LLM）、confirm 经 TurnTarget 幂等键推进（首提交/幂等/冲突互斥）、完结先写 Review 后播总结且诚实报告实际保存集合、播放完成以 epoch+sentence_id ACK 门控全程——tool 环代码与测试整体退场，23 个新行为/集成用例与全部遗留用例全绿**

## Performance

- **Duration:** 41 min（2026-10-05T01:28:29Z → 2026-10-05T02:09Z 前后）
- **Started:** 2026-10-05T01:28:29Z
- **Completed:** 2026-10-05T02:09Z
- **Tasks:** 3 / 3
- **Files changed:** 9（新建 1：pkg/rt/dictation.go；其余为计划声明文件 + 2 个 Rule 3 编译对端文件，见 Deviations）

## Accomplishments

- **首问候缺失缺陷修复（RT-04 结构前提）**：session_init 经 per-lessonID 进程内串行化取词建课后，专用 goroutine 立即播报模板开场白+第 1 词（`greetingText` 纯函数，零 LLM 调用），首次可听延迟只含 FSRS 取词与 TTS 首包；TestDictationOpeningGreetsAndAnnounces 断言开场期间 LLM 调用为 0
- **节奏所有权收归 FSM（RT-07 组件级证据）**：分类启动前锁内捕获 immutable TurnTarget，AdvanceWord 幂等键 `lessonID-advance-<index>`；首提交才记 outcome/发 lesson_update/报次词或完结，Idempotent 命中只对齐、版本冲突只回读——三分支互斥由双确认与冲突测试锚定，重复触发不双跳
- **LLM 职责收窄 + 确定性兜底**：八值意图词表、词义/例句 ground truth 注入（缺失如实注明）、hint 禁泄题/meaning 不编造写进 system 提示（源断言测试）；非法 JSON 一次纠正重试后固定口头兜底（用户零可见 ErrorMsg）+ 服务端 error 事件
- **播放完成协调（S-01/HIGH-1 缓解）**：pendingPlayback 以 epoch+sentence_id 与 audio_start 同锁先登记、粘性完成位使早到最终 ACK 不丢；播完 ACK 门控覆盖到 13-09 回声尾前的整个窗口；有界等待按未确认排程 PCM 推导夹取 3..45s；超时只发 ErrorMsg(playback_ack_timeout) 并按普通/自然结束两路有序收尾，自然结束仍携带原 reason——集成测试证明 interrupt 后零迟到帧且 ACK 门不被截断
- **完结诚实化（S-05/透明性禁令）**：先 SubmitReview 后播总结；partial/zero 更新以真实计数事件 + 「本次保存 M/N 个」呈现且不整批盲重试，真实错误播记录未保存后缀——nil error 永不被当全量成功
- **tool 环退场无残留**：lessonToolDefs/executeToolCall/validateToolArgs/toolErrorResult、LessonWords/MaxToolLoops/HistoryMaxTurns 及其全部引用（含 cmd/service/main.go 编译对端）删除；事件词表换新并保持冻结词表测试纪律

## Task Commits

Each task was committed atomically:

1. **Task 1: 端到端默写主环 — dictation.go 状态机 + turn.go 管线重排** - `93fe6a0` (feat)
2. **Task 2: 意图分类契约 + 完结路径（总结/学习记录/诚实失败）** - `1caac3d` (feat)
3. **Task 3: 测试套件收口 — 打断裁剪 FSM 语义 + 整链集成三用例** - `e671023` (test)

**Plan metadata:** 本 SUMMARY 提交（docs(13-08)）

## Files Created/Modified

- `pkg/rt/dictation.go`（新建）- WordSource/ReviewWriteResult、DictationTutor（单锁纪律）、模板话术、评分映射（13-12 待追认注释）、意图分类（buildClassifyMessages/extractJSON/parseIntentJSON/一次重试/兜底）、r0-YYYYMMDD 派生、固定人话错误文案常量
- `pkg/rt/turn.go` - 管线重排：TurnManagerConfig（Words/WordTimeout）、handleSessionInit 串行化+三显式路径、greeting/announcement/natural-ending 运行器、pendingPlayback/PlaybackRef/awaitPlaybackCompletion、confirmTarget 三分支、enterCompletion、speakTurn 短历史+门控、handleInterrupt epoch 失效+deque 裁剪、armWordTimer 占位
- `pkg/rt/timing.go` - 新增 word_announced/word_advanced/intent_classified/lesson_complete/review_written/playback_ack_timeout/sentence_ack_rejected；删除 tool_start/tool_end/lesson_version
- `pkg/rt/mimo_adapters.go` - LessonTools 补 EnsureLessonCards 适配与签名 pin（LessonExecutor 接口随 FSM 需要 扩展）
- `pkg/rt/providers_fake_test.go` - fakeWordSource（脚本化 due/review/记录 SubmitReview）、intentScript、ttCards
- `pkg/rt/turn_test.go` - fakeExecutor 幂等回放+Cards+NoRow；8 个 Task 1 FSM 行为测试 + 3 个 classify + 4 个 completion + no-due/超时/断线 + 打断裁剪 deque 双子用例；tool 环失效用例删除
- `pkg/rt/turn_integration_test.go` - 13-04 harness 更新为默写流：TestDictationOverConnHappyPath / InterruptMidAnnouncement / NoDueWaitsForAck + hold-then-ack 观测法
- `pkg/rt/timing_test.go` - 事件词表锚更新（随常量增删）
- `cmd/service/main.go` - rtTurnCfg 删除 LessonWords/MaxToolLoops/HistoryMaxTurns 三行；infra RTConfig.LessonWords 键未动（归 13-10）

## Must-Have Truths Verification

| Truth | Evidence | Status |
|-------|----------|--------|
| session_init 后老师立即开口：到期词→课堂行→session_ready→开场白+第 1 词逐句流式 TTS 首包即发，开场零 LLM | TestDictationOpeningGreetsAndAnnounces（LLM 调用=0）+ TestDictationOverConnHappyPath（greeting 即达含 PCM 字节） | PASS |
| 节奏由服务端状态机独占：TurnTarget 捕获、首提交才记录/播报，Idempotent 与冲突只对齐不覆盖不重复 | TestDictationDoubleConfirmIdempotent + TestDictationVersionConflictAlignsWithoutSideEffects + 集成版本 2/3/4 递增 | PASS |
| LLM 仅意图识别与话术：单次 Chat 严格 JSON、白名单、重试一次后固定兜底；tool 环符号删除 | TestClassify 三用例；`rg lessonToolDefs|executeToolCall|validateToolArgs|toolErrorResult` 零命中（编译即证明） | PASS |
| 最后一个词后：完结统计→SubmitReview→按 Requested/Updated/Missing 实际集合诚实报告→播总结→匹配 ACK 后 lesson_complete | TestCompletion 四用例 + TestPlaybackAckTimeoutIsVisibleAndBounded + 集成 hold-then-ack | PASS |
| 到期词 0：空词表 session_ready + 说明句 ACK 后 no_due_words；legacy 空 Cards 显式 lesson_unavailable；已完结重连只恢复完成态 | TestNoDueWordsWaitsForPlaybackAck + TestLegacyEmptyCardsIsLessonUnavailable + TestCompletedReconnectDoesNotReviewOrNarrate | PASS |
| pendingPlayback 双键身份、audio_start 前登记、早到最终 ACK 可观察、门控持续、有界超时不伪造完成、interrupt/断线取消等待 | TestDictationRegistersPendingBeforeAudioStartAndObservesEarlyFinalAck + TestSentenceAckRejectsStaleUnknownAndDuplicate + TestCompletionDisconnectCancelsAckWait + 集成 interrupt 用例 | PASS |
| 进程内整链 + 「interrupt 后无迟到帧」两条集成测试绿 | TestDictationOverConnHappyPath + TestDictationOverConnInterruptMidAnnouncement | PASS |

## Threat Mitigations Delivered

| Threat | Disposition | Where |
|--------|-------------|-------|
| T-13-13 (Elevation, 转写注入伪造 confirm, low) | accept | LLM 只产 intent 标签且经白名单；confirm 仍只推进一词并经幂等键/乐观锁；FSM 不执行模型产出的任何结构化参数（dispatch 无 tool 参数路径） |
| T-13-14 (Info Disclosure, 错误消息泄漏, medium) | mitigate | lesson_unavailable/playback_ack_timeout 用户文案均为固定人话常量；细节只进服务端结构化日志（EventError code 字段） |
| T-13-15 (Tampering, reply 超长/控制字符, medium) | mitigate | speakTurn 入口统一 truncateRunes(500)；字幕走既有 teacher_sentence 通道 |
| T-13-15b (Spoofing, 旧/伪造 sentence_ack, high) | mitigate | pendingPlayback epoch+sentence_id 精确匹配、audio_start 前登记、一次消费；重复/旧/未知 ACK 只发 sentence_ack_rejected 诊断零副作用；集成测试覆盖 audio_end 后未 ACK 不结束 |
| T-13-15c (Tampering, 幂等命中重复副作用, high) | mitigate | TurnTarget 分类前捕获；首提交/Idempotent/Conflict 互斥分支；双确认与冲突测试断言 outcome/narration/review 仅一次 |
| T-13-SC (Tampering, 包安装, high) | mitigate | 零新增依赖，包安装面零变化 |

## Decisions Made

见 frontmatter key-decisions（幂等键 FSM 派生、评分映射 13-12 待追认、CST 固定时区派生、固定人话文案、事件词表退旧换新）。

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] 计划文件清单外新增 pkg/rt/mimo_adapters.go 与 pkg/rt/timing_test.go 改动**
- **Found during:** Task 1（首次 go build）
- **Issue:** LessonExecutor 接口按计划新增 EnsureLessonCards（session_init 建课路径需要），LessonTools 适配器缺该方法导致编译失败；timing.go 按「无死代码残留」删除 tool 环事件常量（tool_start/tool_end/lesson_version）后，timing_test.go 的词表锚测试引用失效
- **Fix:** LessonTools 补 EnsureLessonCards 方法与签名 pin；timing_test.go 词表锚同步更新（删三旧事件、增七新事件）
- **Files modified:** pkg/rt/mimo_adapters.go, pkg/rt/timing_test.go
- **Verification:** go build ./... / go vet ./pkg/rt/ 通过
- **Committed in:** 93fe6a0（Task 1）

**2. [Rule 3 - Blocking] Task 1 对 turn_integration_test.go 的旧整链用例就地删除（而非仅同步字段）**
- **Found during:** Task 1（`go test ./pkg/rt/ -count=1` 全绿要求）
- **Issue:** 旧 TestTurnOverConnHappyPath/InterruptMidStream 断言「无需 ACK 即回 listening」「会话开场无 greeting」——与新 FSM 的播完 ACK 门控、session_init 即 greeting 语义直接冲突，保留必红；计划 Task 1 只要求「字面量同步删三字段」，整链重写归 Task 3
- **Fix:** Task 1 删除两旧用例保留 harness 帮助函数（编译对端同步的一部分）；Task 3 以三个 TestDictationOverConn* 用例重写整链覆盖
- **Files modified:** pkg/rt/turn_integration_test.go
- **Verification:** Task 1 起全包 -count=1 绿；Task 3 三用例全过
- **Committed in:** 93fe6a0（删除）、e671023（重写）

**3. [Rule 2 - Missing Critical] plan 显式要求之外的重复 ACK 诊断事件常量 EventSentenceAckRejected**
- **Found during:** Task 1（pendingPlayback 实现）
- **Issue:** 计划 Task 1 步骤 4 要求「重复/旧 epoch/未知句只发脱敏诊断事件」，但 artifacts 事件清单未含对应常量；复用 EventError 会污染可见失败口径
- **Fix:** timing.go 新增 EventSentenceAckRejected="sentence_ack_rejected"（字段仅 reason/epoch，脱敏边界同 T-13-06b）
- **Files modified:** pkg/rt/timing.go
- **Verification:** TestSentenceAckRejectsStaleUnknownAndDuplicate 断言三次拒绝各记一条
- **Committed in:** 93fe6a0（Task 1）

---

**Total deviations:** 3 auto-fixed（3 blocking/critical，全部编译/计划显式要求落地层面）
**Impact on plan:** 无范围蔓延——均为计划意图的机械落地或其直接必然后果；验证命令 -run 过滤器全部按计划原样命中（Task 1 `TestDictation|...` 命中 8/8，Task 2 `TestClassify|...` 命中 10/10，Task 3 `TestDictationOverConn` 命中 3/3），无 13-07 式过滤漂移。

## Known Stubs

| Stub | File | Line | Reason |
|------|------|------|--------|
| armWordTimer no-op 占位（FSM 武装计时回调点已被测试路径覆盖，计时器本体未实现） | pkg/rt/turn.go | 505-509 | 计划显式锁定：「本任务内为 no-op 占位方法 armWordTimer，13-09 实现」 |
| TurnManagerConfig.Words 生产注入缺位：session_init 现答 lesson_unavailable | cmd/service/main.go | 564-569 | 计划显式锁定：「Words/WordTimeout 注入归 13-10」；infra RTConfig.LessonWords 键未动 |

以上两处均为计划内的分阶段边界（非缺陷），已在代码注释与本表登记供 verifier 校验。

## Issues Encountered

- 计划由 orchestrator 指定为顺序执行器，在主工作树（main 分支）直接提交（config `branching_strategy: none`，与 13-01～13-07 一致）；每任务仅暂存计划声明文件 + 上述 Rule 3 文件，工作区内无关改动（.gitignore、CONTEXT.md、pkg/infra 等 EOL 噪声、根目录未跟踪 scratch 文件）一律未触碰
- 测试驱动中确认两处计划未细化但实现必需的时序细节并按计划语义落地：processTurn 在次词播报 ACK 等待上阻塞（测试需 goroutine + 分步 ack）；播报句数随话术动态变化（集成测试 hold-then-ack 观测法避免 gorilla 连接超时毒化）

## User Setup Required

None - no external service configuration required.

## Explicit Assumptions Registered（计划带入，供 13-12 检查点）

- 显式假设 1：评分映射 confirmed→good/timeout→hard/hint→again 为实现裁量，13-12 检查点提请用户追认（outcomeRating 表上注释已注明）
- 显式假设 2：课中掉线丢失 perWordOutcome（已确认词评分不落库）——恢复语义归 Phase 15 RT-08，R0 接受
- 显式假设 3：首次可听 ≤3s 预算含 FSRS 取词（python 子进程）；R0 只保证结构与测量，达标判定归第六周检查

## Next Phase Readiness

- 13-09（词计时器/空转写容忍/回声尾）：armWordTimer 占位就绪（settleListening→announceAcked 回调点已被 8 个用例覆盖）；WordTimeout 字段已定义（0→60s 默认）；空转写容忍可在 processTurn 的 transcript=="" 分支落地；500ms 回声尾可挂 await 成功路径
- 13-10（PyFSRS 适配器）：WordSource 接口与 ReviewWriteResult 语义冻结（partial/zero+nil error 合法）；main.go 注入点已留（Words nil → lesson_unavailable 诚实降级）；legacy 空 Cards / fetch lock 残留的诚实记录路径就绪
- 13-11（前端）：playback_ack_timeout 义务（诚实提示 + 排空已排程音频）在超时路径已按协议 godoc 顺序发出；session_ended 自然结束两 reason 全链路可观测
- 13-12（验收）：评分映射追认、话术听感 UAT（coverage D7 human_judgment）待检查点收口
- RT-02/RT-03/RT-04/RT-07 为共享 ID（13-09/13-12 等亦声明），REQUIREMENTS 勾选由 orchestrator 按 shared-ID 门槛收口

## Self-Check: PASSED

- [x] 新建文件 pkg/rt/dictation.go 存在；8 个修改文件全部在磁盘（Test-Path 验证）
- [x] 提交 93fe6a0 / 1caac3d / e671023 均在 git log（HEAD=e671023 验证）
- [x] commits: 3 为实测值（git rev-list --count 7068035..HEAD），与 narrated 一致
- [x] 计划级验证命令全部通过：go build ./... / go vet ./pkg/rt/ / go test ./pkg/rt/ ./pkg/lesson/ ./pkg/mimo/ -count=1 全绿；TestDictation*/TestClassify*/TestCompletion*/TestDictationOverConn* 全组通过（Task 1 8 用例、Task 2 10 用例、Task 3 三集成用例均在各自 verify 输出中出现且 PASS）
- [x] 零回归：13-03/13-04 遗留协议/传输/VAD 用例零改动通过（全包 -count=1 绿）
- [x] 无未登记 stub（armWordTimer/Words 注入两处计划边界已登记）；无计划外威胁面新增（零新增依赖、零新端点）

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-05*
