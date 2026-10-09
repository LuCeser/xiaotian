---
status: testing
phase: 13-realtime-minimal-loop
source: [13-01-SUMMARY.md, 13-02-SUMMARY.md, 13-03-SUMMARY.md, 13-04-SUMMARY.md, 13-05-SUMMARY.md, 13-06-SUMMARY.md, 13-07-SUMMARY.md, 13-08-SUMMARY.md, 13-09-SUMMARY.md, 13-10-SUMMARY.md, 13-11-SUMMARY.md, 13-12-SUMMARY.md]
started: 2026-10-09T01:27:38.327Z
updated: 2026-10-09T01:31:00.795Z
---

## Current Test

number: 1
name: 本机冷启动
expected: |
  本地 HTTPS 服务已由代理运行 .\run.ps1 -Https 启动；页面和健康检查返回 200，当前 Windows 用户环境的证书校验通过。打开 https://localhost:8443/tutor/，确认浏览器无需忽略证书告警即可打开，并显示开始上课入口；此项先不要开始课程，不清理或重置家庭词库。自动预检不代替本项人工确认。
awaiting: user response

## Tests

### 1. 本机冷启动
expected: 本地 HTTPS 服务已由代理运行 .\run.ps1 -Https 启动；页面和健康检查返回 200，当前 Windows 用户环境的证书校验通过。打开 https://localhost:8443/tutor/，确认浏览器无需忽略证书告警即可打开，并显示开始上课入口；此项先不要开始课程，不清理或重置家庭词库。自动预检不代替本项人工确认。
result: [pending]

### 2. 真实 MiMo 调用与关闭 thinking
expected: 在有密钥的环境执行既有实时调用检查，关闭 thinking 后正常返回，并保留耗时与响应结果证据。历史工具调用质量检查属于已停用流程，不能用于证明当前状态机课堂通过。
result: [pending]
coverage_id: 13-02:D2
summary_file: 13-02-SUMMARY.md
classification_reason: human_judgment
rationale: "有 MIMO_API_KEY 的真机运行（含耗时毫秒数日志）需 key 可用环境；执行环境无 key，SKIP 为设计行为。A4 判定证据延后采集，已登记 .planning/WINDOWS.md unrun-verify"
coverage_description: "真 key 烟测通道（A4 证据）：thinking 关闭后无 reasoning_content + 工具调用质量摸底 + 默认开启延迟对比 t.Logf 证据"

### 3. 家教页面与安全上下文提示
expected: 打开 /tutor/，状态、字幕、进度、连接和错误提示清晰可读，主要按钮便于点击。以非安全 HTTP 地址访问时明确提示需 HTTPS 并禁用开始；localhost 的安全上下文例外不能用于该对照。
result: [pending]
coverage_id: 13-05:D1
summary_file: 13-05-SUMMARY.md
classification_reason: human_judgment
rationale: "非安全上下文的页面级降级呈现（HTTP 下提示文案与禁用按钮的视觉表现）无法在 Chromium 127.0.0.1 安全上下文中自动化复现；真机 HTTPS/HTTP 对比归 13-07 Task 2"
coverage_description: "/tutor/ 独立自然家教入口页：状态条三态+连接态、字幕区、课堂进度、错误面板、>=60px 控件、RT-01 非安全上下文提示并禁用开始按钮"

### 4. 旧入口引导与麦克风错误提示
expected: 旧学生页显示降级模式提示并链接到 /tutor/。拒绝麦克风权限后，家教页显示可理解的中文原因和处理办法。
result: [pending]
coverage_id: 13-05:D5
summary_file: 13-05-SUMMARY.md
classification_reason: human_judgment
rationale: "横幅视觉呈现与真实麦克风拒绝/服务端故障下学生看到的错误面板表现需人工抽查；真实故障表现归 13-07 Task 3 步骤 7"
coverage_description: "旧学生页降级模式横幅（不计语音验收、指向 /tutor/）与麦克风错误/服务端错误的人话呈现（RT-10 客户端半边）"

### 5. 缺少语音密钥时的明确提示
expected: 在隔离配置中不设置 MIMO_API_KEY，服务仍能启动，家教页开始后明确提示语音不可用；不应一直等待或显示已成功连接语音。
result: [pending]
coverage_id: 13-06:D4
summary_file: 13-06-SUMMARY.md
classification_reason: human_judgment
rationale: "tutor 页对降级原因的可见呈现与真实故障可见性归 13-07 Task 3 步骤 7 真机验收（计划 Flagged Assumptions 明示）；自动化只能证明降级路径可编译可启动"
coverage_description: "无 MIMO_API_KEY 降级路径：服务照常启动、/rt 照常挂载、session_init 返回 voice_unavailable、双 Warn 日志（RT-10 透明性服务端半边）"

### 6. 话术准确性与儿童听感
expected: 老师话术适合儿童，词义依据词库，资料缺失时如实说明。学习记录保存失败时明确提示，不宣称已保存；评分和提交时机按已实施的 13.1 三段式课堂执行。
result: [pending]
coverage_id: 13-08:D7
summary_file: 13-08-SUMMARY.md
classification_reason: human_judgment
rationale: "话术鼓励式程度与小学生听感是产品判断级验收（计划 flagged: unverified）；自动化只能锚定提示词文本与失败话术后缀，TTS 实际听感需 13-12 真机检查点由用户评判"
coverage_description: "话术价值观落地（计划 prohibition #1/#2/#3）：儿童安全禁令原文迁入分类系统提示、词义仅据词库 ground truth（缺失如实说明）、学习记录写入失败如实可见不口头宣称已保存——文本存在性已由 D5/D6 测试断言，实际语音体验的语气温感归真人 UAT"

### 7. 重读、慢读、提示、释义与暂停继续
expected: 当前词可重读、慢读、请求提示和释义，均不提前推进。暂停话术播完后仍显示暂停，语音继续可恢复，暂停时说好了不推进；提示次数及最终评分按 13.1 当前规则执行。
result: [pending]
coverage_id: 13-09:D1
summary_file: 13-09-SUMMARY.md
classification_reason: validation_failed
coverage_description: "四类口令 + 暂停/继续全交互集：repeat/slow 模板重读当前词（游标不动）、hint 记 again + 播模型 reply、meaning 播 ground truth reply、pause 置独立位并挂起计时、paused 期真实 processTurn 识别 resume 并在 ACK 后重武装、confirm 暂停期温和拒绝零推进"

### 8. 等候书写与超时只推进一次
expected: 老师实际播完后开始等待书写；默认约 60 秒未确认才超时推进。暂停期间不计时，确认与超时几乎同时发生也只推进一个词。
result: [pending]
coverage_id: 13-09:D2
summary_file: 13-09-SUMMARY.md
classification_reason: validation_failed
coverage_description: "60 秒词计时器：只在报词/回复最后一句有效 sentence_ack 后武装；timerGeneration 使同 epoch/index 的旧排队回调零副作用；锁内复核关闭+epoch+完整 TurnTarget+paused；processing 到点先于通用拒绝重武装；speaking 由 ACK 驱动不提前推进；confirm/timeout 双向竞争仅首提交产生 outcome/消息/播报/review，EventTimeoutFired 只由首提交发出"

### 9. 真实扬声器回声、提示与打断
expected: 用真实扬声器和麦克风测试：播完后静默 0.5～1.5 秒及更久均不误识别或误推进；请求提示或释义不泄露当前默写答案；学生插话后老师及时停播，旧音频不再次播放。
result: [pending]
coverage_id: 13-09:D6
summary_file: 13-09-SUMMARY.md
classification_reason: human_judgment
rationale: "计划显式假设：静默期默认窗口为待真机标定参数（13-12 记录真实播后 0.5–1.5s 与更长静默观察）；hint 泄题归 13-12 检查点第 4 步抽查；RT-05 整体验收归 13-12/13-13"
coverage_description: "真机标定与产品判断面：1000/500ms 窗口默认值、hint/meaning 回复真实不泄题（T-13-17 真机抽查）、打断与口令共存整体验收（RT-05 probe 边缘）、话术听感——自动测试只锚定控制逻辑与提示词文本，数值与听感须 13-12 真机检查点收口"

### 10. 默认词源与等候时间配置
expected: 当前配置不再依赖 rt.lessonWords；lessonId 可省略按日生成，默认等候时间为 60000ms，合法配置覆盖生效，非法值启动时报错。此项因历史 coverage 格式不兼容保留待确认。
result: [pending]
coverage_id: 13-10:D1
summary_file: 13-10-SUMMARY.md
classification_reason: validation_failed
coverage_description: "RTConfig 转向重构：LessonWords 字段与 1..10 校验退场、LessonID 可空（轻量合法性校验）、WordTimeoutMs 带范围校验、DefaultConfig 空 lessonId + 60000ms、HTA_RT_WORDTIMEOUTMS 显式 env 绑定无静默失效"

### 11. 真实词库取词与复习结果核对
expected: 课堂使用真实到期词，词、释义和例句保持一致。检查后复习提交的实际更新数量可核对；部分更新或失败有明确记录，不盲目重试。
result: [pending]
coverage_id: 13-10:D2
summary_file: 13-10-SUMMARY.md
classification_reason: validation_failed
coverage_description: "PyFSRS 适配器：RunDue 显式 FetchLockHours=1 非 peek、DueCard→lesson.Word 零损直拷保序、limit<=0 置 10；SubmitReview 空 map 零 spawn、单次 RunReview、partial/zero Updated+nil error 返回真实 Requested/Updated/Missing 集合不盲重试、未知/重复 Updated 契约报错"

### 12. 服务实际使用 FSRS 词源
expected: 真实服务开始课堂时从 Python 词库取词，词源无法构造时明确失败；缺语音密钥仍能启动并提示降级。不能仅凭端点可访问认定真实取词通过。
result: [pending]
coverage_id: 13-10:D3
summary_file: 13-10-SUMMARY.md
classification_reason: validation_failed
coverage_description: "main.go 生产注线：pyBridge→rt.NewPyFSRS→rtTurnCfg.Words/WordTimeout 注入，构造失败 fail-fast；无 key 降级语义不变；LessonWords 零引用（编译即证）"

### 13. 词库环境故障与部署步骤
expected: 在隔离环境模拟脚本过旧、词库缺失或解释器不可用，页面明确提示课堂暂不可用并提供重试引导。按部署文档检查环境能定位原因。
result: [pending]
coverage_id: 13-10:D5
summary_file: 13-10-SUMMARY.md
classification_reason: human_judgment
rationale: "自动化只能锚定降级路径存在且可编译可启动（wiring 三用例 + PyFSRS 错误传播测试）；真实 VPS python 词库环境下的故障呈现、文档核验步骤的实际执行归 13-12/13-13 真机检查点"
coverage_description: "真机故障呈现与部署可复现：lesson_unavailable 在真实 python 环境（脚本过旧缺 --fetch-lock-hours、vocab DB 缺失、venv 不可用）下的实际表现与 DEPLOYMENT 步骤可执行性须 13-12 真机检查点收口（计划 flagged: RT-10 probe 边缘谓词 unresolved）"

### 14. 本机预检与真实取词证据
expected: 核对既有本机预检和 HTTPS 探针记录，真实会话的词数、课键与词库一致。本机证据明确标为预验收；现阶段回归应使用隔离测试服务和数据库。
result: [pending]
coverage_id: 13-12:D1
summary_file: 13-12-SUMMARY.md
classification_reason: validation_failed
coverage_description: "全量预检 + 8443 通道三探针 + 真实 RunDue 取词冒烟（session_ready words=10，词源链路在转向接线下真实工作）落档 13-CHECKPOINTS 本机预验收段"

### 15. 真实课堂、学习记录与现行评分规则
expected: 真实麦克风和扬声器下完成课堂，确认、四类口令、暂停恢复、打断和超时正常工作。当前按 13.1 检查后统一评分：对且无提示 good、对且有提示 hard、错 again，保存结果可核对；历史完结即提交口径不再适用。
result: [pending]
coverage_id: 13-12:D2
summary_file: 13-12-SUMMARY.md
classification_reason: validation_failed
rationale: "麦克风/扬声器/儿童语音/回声不可自动化——代理 E2E 覆盖协议与状态机全流程，物理声道与主观确认留用户三级顺序后两级"
coverage_description: "默写课实测 + 评级映射追认 + 三伴随缺陷零复发复核（按用户 2026-10-06 改判：自动化部分由代理本机 E2E 执行）"

### 16. 时序报告工具
expected: 既有时序报告 self-test 通过，报告能区分有时钟校准、无校准以及失败样本。历史 coverage 格式不兼容，因此此项保留待确认。
result: [pending]
coverage_id: 13-12:D3
summary_file: 13-12-SUMMARY.md
classification_reason: validation_failed
coverage_description: "rt-timing-report 报告管线可用（--self-test 全断言通过，含 clock_offset/无 offset/失败样本分支）"

### 17. 真实响应与打断时序样本
expected: 对实际会话导出的 rt-timing JSONL 生成报告，记录配对数量、校准误差、失败和超时，不排除不利样本。零配对或样本不足只能说明报告可用，不能算正式延迟验收通过。
result: [pending]
coverage_id: 13-12:D4
summary_file: 13-12-SUMMARY.md
classification_reason: validation_failed
coverage_description: "对会话 rt-timing JSONL 跑报告并摘要落档（标注：本机样本仅验证报告管线，不作第六周判定证据）"

### 18. 课堂存储与幂等迁移
expected: 课堂存储与幂等迁移符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-01:D1
summary_file: 13-01-SUMMARY.md
coverage_description: "lessons/tool_requests DDL + idempotent Migrate + Store (EnsureLesson first-content-wins via INSERT OR IGNORE, GetLesson with ErrLessonNotFound)"
verification_refs: [{"kind":"unit","ref":"pkg/lesson/store_test.go#TestStoreMigrateIdempotent","status":"pass"},{"kind":"unit","ref":"pkg/lesson/store_test.go#TestStoreEnsureLesson","status":"pass"},{"kind":"unit","ref":"pkg/lesson/store_test.go#TestStoreGetLessonNotFound","status":"pass"},{"kind":"unit","ref":"pkg/lesson/store_test.go#TestStoreNewStoreGuards","status":"pass"}]

### 19. 重复推进去重与版本冲突处理
expected: 重复推进去重与版本冲突处理符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-01:D2
summary_file: 13-01-SUMMARY.md
coverage_description: "Executor.AdvanceWord idempotent transaction (request_id replay) + expected_version optimistic concurrency (ConflictError.CurrentVersion) + GetLessonState delegation"
verification_refs: [{"kind":"unit","ref":"pkg/lesson/executor_test.go#TestAdvanceWordIdempotent","status":"pass"},{"kind":"unit","ref":"pkg/lesson/executor_test.go#TestAdvanceWordVersionConflict","status":"pass"},{"kind":"unit","ref":"pkg/lesson/executor_test.go#TestAdvanceWordLessonChange","status":"pass"},{"kind":"unit","ref":"pkg/lesson/executor_test.go#TestAdvanceWordNotFound","status":"pass"}]

### 20. MiMo 工具调用协议兼容
expected: MiMo 工具调用协议兼容符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-02:D1
summary_file: 13-02-SUMMARY.md
coverage_description: "MiMo function-calling 与 thinking 关闭的 wire 双向支持：请求 tools/tool_choice/thinking、响应 tool_calls/finish_reason、role:tool 回填、三个 ChatOption 与两个 ChatResponse 辅助方法，存量行为零漂移"
verification_refs: [{"kind":"unit","ref":"pkg/mimo/chat_tools_test.go#TestChatToolsRequestBody","status":"pass"},{"kind":"unit","ref":"pkg/mimo/chat_tools_test.go#TestChatToolCallsResponseParse","status":"pass"},{"kind":"unit","ref":"pkg/mimo/chat_tools_test.go#TestToolMessageRoundTrip","status":"pass"},{"kind":"unit","ref":"pkg/mimo/chat_tools_test.go#TestChatWithoutToolsUnchanged","status":"pass"},{"kind":"unit","ref":"pkg/mimo/chat_tools_test.go#TestToolMessageFieldsOmittedOnEmpty","status":"pass"},{"kind":"integration","ref":"go build ./... && go vet ./pkg/mimo/ && go test ./pkg/mimo/ -short -count=1","status":"pass"}]

### 21. 实时控制协议与采样率校验
expected: 实时控制协议与采样率校验符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-03:D1
summary_file: 13-03-SUMMARY.md
coverage_description: "JSON 控制协议全集（17 消息结构体、wire 字段名冻结、ping/pong 时钟偏移探针对）与 session_init 采样率协商校验（仅 16000/s16le/1ch）"
verification_refs: [{"kind":"unit","ref":"pkg/rt/protocol_test.go#TestProtocolSessionInitRoundTrip 及全部 TestProtocol* 往返/矩阵/拒绝测试","status":"pass"},{"kind":"unit","ref":"command: go test ./pkg/rt/ -run TestProtocol -count=1","status":"pass"}]

### 22. 供应商接口与时序记录
expected: 供应商接口与时序记录符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-03:D2
summary_file: 13-03-SUMMARY.md
coverage_description: "可替换供应商接口（ASR/LLM/TTS，AudioChunkFunc 裸 PCM 回调、接口面无 base64）+ 单调时序事件记录器（10 事件词表、t_mono_ns 同锚）+ 脚本化 fake 基建"
verification_refs: [{"kind":"unit","ref":"pkg/rt/timing_test.go#TestTimingEmitFields / TestTimingMonoNsMonotonicAndSameSourceAsEmit / TestTimingEventVocabulary","status":"pass"},{"kind":"unit","ref":"pkg/rt/providers_fake_test.go#TestFakesLLMWireToolCallsScript / TestFakesTTSProducesBarePCMWithFinalFlag 等","status":"pass"},{"kind":"unit","ref":"command: go test ./pkg/rt/ -run \\\"TestTiming|TestFakes\\\" -count=1","status":"pass"}]

### 23. 实时 WebSocket 连接与资源释放
expected: 实时 WebSocket 连接与资源释放符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-03:D3
summary_file: 13-03-SUMMARY.md
coverage_description: "/rt WSS 传输端点：升级（EnableCompression:false Safari 规避 + 两段式 Origin 白名单禁 \\\"*\\\" 拒空 Origin）、SetReadLimit + 心跳滚动 deadline、单写者发送泵（音频丢弃计数/控制报错）、SessionManager/Sender 契约与断连释放"
verification_refs: [{"kind":"integration","ref":"pkg/rt/conn_test.go#TestOriginEnforcement / TestReadLimit / TestRoutingToManager / TestWriterBackpressure / TestWriterDelivers / TestHeartbeatRollsDeadline","status":"pass"},{"kind":"unit","ref":"command: go build ./... && go vet ./pkg/rt/ && go test ./pkg/rt/ -count=1 && go test ./pkg/ws/ -short -count=1","status":"pass"}]

### 24. 语音活动检测与分句
expected: 语音活动检测与分句符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-04:D1
summary_file: 13-04-SUMMARY.md
coverage_description: "能量 VAD 切句：hysteresis/hangover/pre-roll/MaxSegmentMs 30s 硬切，Feed 非整帧输入无样本丢失"
verification_refs: [{"kind":"unit","ref":"pkg/rt/vad_test.go#TestVADSilenceNoSegment/TestVADSpeechThenSilenceCloses/TestVADHardCutAtMax/TestVADShortGapMerged/TestVADRMSFrameCalc","status":"pass"}]

### 25. 轮次取消、逐句播放及上下文裁剪
expected: 轮次取消、逐句播放及上下文裁剪符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-04:D2
summary_file: 13-04-SUMMARY.md
coverage_description: "TurnManager 轮次核心：FSM、epoch 取消、MiMo 工具环（先执行后播报）、逐句流式 TTS、上下文裁剪到实际已播句、半双工门控、时序事件"
verification_refs: [{"kind":"unit","ref":"pkg/rt/turn_test.go（工具环/epoch/历史裁剪/门控/参数校验/splitSentences 行为测试全绿）","status":"pass"},{"kind":"unit","ref":"go vet ./pkg/rt/","status":"pass"}]

### 26. 实时传输集成与打断后迟到音频拒收
expected: 实时传输集成与打断后迟到音频拒收符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-04:D3
summary_file: 13-04-SUMMARY.md
coverage_description: "进程内整链集成：WSS 连接→session_init→VAD 关段→fake ASR/LLM/TTS→二进制回程字节级相等；播放中 interrupt 后无迟到音频帧"
verification_refs: [{"kind":"integration","ref":"pkg/rt/turn_integration_test.go#TestTurnOverConnHappyPath/TestTurnOverConnInterruptMidStream","status":"pass"}]

### 27. 客户端时序记录与时钟校准
expected: 客户端时序记录与时钟校准符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-05:D2
summary_file: 13-05-SUMMARY.md
coverage_description: "rt-client.js 协议客户端 + TimingLog 时序证据 + ping/pong 时钟偏移估计（RT-04 客户端半边，M-6）"
verification_refs: [{"kind":"unit","ref":"tests/test-rt-timing.js#TimingLog JSONL 行格式/无服务端字段/estimateClockOffset 已知数值与零 RTT 对称用例","status":"pass"},{"kind":"automated_ui","ref":"tests/e2e-rt/rt-loop.spec.js#clock_offset poll >= 1（mock 回 pong 带 hrtime 单调戳）","status":"pass"}]

### 28. 单次授权持续录音及采样率转换
expected: 单次授权持续录音及采样率转换符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-05:D3
summary_file: 13-05-SUMMARY.md
coverage_description: "一次手势授权的持续采集：iOS 三条件同步解锁、getUserMedia 单次调用贯穿 10 轮、实测采样率降采样 16k s16le 上行"
verification_refs: [{"kind":"unit","ref":"tests/test-rt-playback.js#resampleTo16k 48k 正弦 1 秒→16000 样本<2% 误差 + tail 块语义 + floatToInt16 钳位","status":"pass"},{"kind":"automated_ui","ref":"tests/e2e-rt/rt-loop.spec.js#__gumCallCount === 1 贯穿 10 轮 + capture.isCapturing 持真","status":"pass"}]

### 29. 流式播放、打断与播放确认
expected: 流式播放、打断与播放确认符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-05:D4
summary_file: 13-05-SUMMARY.md
coverage_description: "24kHz 收即播调度 + generation 令牌旧回调不复活 + epoch 门迟到 chunk 丢弃 + RMS 打断闭环 + sentence_ack + 64 chunk 队列上限（T-13-06c）"
verification_refs: [{"kind":"unit","ref":"tests/test-rt-playback.js#generation 令牌/迟到 chunk/队列上限/playedUntilMs/RMS 三态窗口","status":"pass"},{"kind":"automated_ui","ref":"tests/e2e-rt/rt-loop.spec.js#interrupt during playback（interrupt 消息三字段、epoch 递增、新一轮渲染）","status":"pass"}]

### 30. TLS 与实时配置校验
expected: TLS 与实时配置校验符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-06:D1
summary_file: 13-06-SUMMARY.md
coverage_description: "server.tls + rt 配置段：L-7 三态校验（nil/成对/空块/半对）、RTConfig 默认值（localhost 对、65551、10s/30s、r0-test 三词）与全部 10 个叶子键的 env 绑定"
verification_refs: [{"kind":"unit","ref":"pkg/infra/config_test.go#TestTLSThreeStateValidate / TestLoadConfigTLSEmptyBlockRejected / TestDefaultRTConfig / TestRTConfigValidate / TestLoadConfigRTEmptySectionTakesDefaults","status":"pass"},{"kind":"unit","ref":"pkg/infra/config_test.go#TestRTAndTLSEnvBinding（HTA_RT_LESSONID / HTA_SERVER_TLS_* 经 LoadConfig 生效）","status":"pass"},{"kind":"unit","ref":"command: go test ./pkg/infra/ -run \\\"TestTLS|TestRT\\\" -count=1 && go test ./pkg/infra/ -short -count=1","status":"pass"}]

### 31. 服务启动与实时功能集成
expected: 服务启动与实时功能集成符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-06:D2
summary_file: 13-06-SUMMARY.md
coverage_description: "运行时接线：mimo.Client 三适配器（WrapWAV 封 16k WAV、Chat 直通、base64 唯一解码点）+ LessonTools 编译期对接 + main.go 挂 /rt、/tutor/、lesson.Migrate、TLS 分支（Stat 预检 + ListenAndServeTLS + tls=true/false 日志字段）"
verification_refs: [{"kind":"unit","ref":"command: go build ./... && go vet ./cmd/service/ ./pkg/rt/ && go test ./pkg/rt/ ./pkg/lesson/ -count=1","status":"pass"},{"kind":"unit","ref":"grep 断言：main.go 含 /rt、/tutor/、ListenAndServeTLS、lesson.Migrate 四符号；mimo_adapters.go 含三个 var _ 接口检查 + WrapWAV 调用 + LessonTools var _","status":"pass"}]

### 32. HTTPS 页面及实时端点集成
expected: HTTPS 页面及实时端点集成符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-06:D3
summary_file: 13-06-SUMMARY.md
coverage_description: "HTTPS 接线测试闭环：自签证书下 /tutor/ 200 且含 data-page=\\\"tutor\\\"、/rt 挂载（普通 GET 得 400/403 非 404）、旧学生页 / 200；无 TLS 基线（M-4 对照）可启动；缺证书文件 run 返回 1"
verification_refs: [{"kind":"integration","ref":"cmd/service/rt_wiring_test.go#TestRunTLSHTTPSServesTutorAndRT / TestRunNoTLSHTTPBaseline / TestRunTLSMissingCertFails","status":"pass"},{"kind":"integration","ref":"command: go test ./cmd/service/ -run \\\"TestRunTLS|TestRunNoTLS\\\" -count=1 -v（3 PASS）&& go test ./cmd/service/ -short -count=1","status":"pass"}]

### 33. HTTPS 部署文档核验
expected: HTTPS 部署文档核验符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-06:D5
summary_file: 13-06-SUMMARY.md
coverage_description: "docs/DEPLOYMENT.md「HTTPS / TLS 与 /tutor/ 入口」章节：server.tls 样例（fullchain/privkey）、allowedOrigins 证书域名要求、证书绝不入 git/.planning、systemd 零改动、部署核验步骤、h2 排查首查项、无 key 降级说明"
verification_refs: [{"kind":"other","ref":"grep 断言四关键词全命中：fullchain.pem+privkey.pem、allowedOrigins、TLSNextProto、NEVER enter git","status":"pass"}]

### 34. 空转写与非法响应区分
expected: 空转写与非法响应区分符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-07:D1
summary_file: 13-07-SUMMARY.md
coverage_description: "pkg/mimo ASR 空转写类型化：HTTP 200 + JSON 解码成功 + choices 为空 → ErrEmptyTranscription（errors.Is 可判定），解码失败仍 → ErrInvalidResponse，两哨兵互斥"
verification_refs: [{"kind":"unit","ref":"pkg/mimo/client_test.go#TestClient_TranscribeEmptyChoices（空 choices → ErrEmptyTranscription 且 ≠ ErrInvalidResponse）","status":"pass"},{"kind":"unit","ref":"pkg/mimo/client_test.go#TestClient_TranscribeInvalidJSON（非法 JSON → ErrInvalidResponse 且 ≠ ErrEmptyTranscription）","status":"pass"},{"kind":"unit","ref":"command: go test ./pkg/mimo/ -short -count=1 全绿","status":"pass"}]

### 35. 课堂词卡保存与迁移
expected: 课堂词卡保存与迁移符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-07:D2
summary_file: 13-07-SUMMARY.md
coverage_description: "pkg/lesson 词卡存储：Word 类型（json 标签 word/meaning/example 与 pybridge.DueCard 逐字对齐）、lessons.cards 列、PRAGMA 探测幂等补列迁移、EnsureLessonCards 首内容优先、GetLesson 返回 Cards"
verification_refs: [{"kind":"unit","ref":"pkg/lesson/store_test.go#TestEnsureLessonCards（往返 + 首内容优先 + 非法参数拒写）","status":"pass"},{"kind":"unit","ref":"pkg/lesson/store_test.go#TestMigrateAddsCardsColumnToLegacyTable（legacy 表补列幂等、旧行不损、Cards 空切片）","status":"pass"},{"kind":"unit","ref":"pkg/lesson/store_test.go#TestEnsureLessonCardsDoesNotReplaceLegacyEmptyCards + TestGetLessonCorruptedCardsJSON","status":"pass"},{"kind":"unit","ref":"command: go test ./pkg/lesson/ ./pkg/database/ -count=1 全绿","status":"pass"}]

### 36. 暂停、错误码和播放确认协议
expected: 暂停、错误码和播放确认协议符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-07:D3
summary_file: 13-07-SUMMARY.md
coverage_description: "pkg/rt 协议词表扩展：StatePaused、lesson_unavailable/playback_ack_timeout 错误码+哨兵+ErrorCode 映射、lesson_complete/no_due_words 结束原因、SentenceAckMsg 播放完成语义、playback_ack_timeout 仅 ErrorMsg.Code 冻结"
verification_refs: [{"kind":"unit","ref":"pkg/rt/protocol_test.go#TestProtocolStateRoundTrip（含 StatePaused 往返）","status":"pass"},{"kind":"unit","ref":"pkg/rt/protocol_test.go#TestErrorCode（两新哨兵裸/Wrap 映射 + 默认回退）","status":"pass"},{"kind":"unit","ref":"pkg/rt/protocol_test.go#TestProtocolPausedAndReasonLiterals + TestProtocolSentenceAckCompletionRoundTrip","status":"pass"},{"kind":"unit","ref":"command: go test ./pkg/rt/ -run \\\"TestProtocol|TestErrorCode\\\" -count=1（17 消息往返零漂移全绿）","status":"pass"}]

### 37. 既有调用兼容性
expected: 既有调用兼容性符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-07:D4
summary_file: 13-07-SUMMARY.md
coverage_description: "存量调用方零回归 backstop：pkg/voice Transcribe 消费者、EnsureLesson 既有调用点（pkg/rt/turn.go、pkg/rt/mimo_adapters.go）、pkg/lesson Executor.AdvanceWord 均行为不变"
verification_refs: [{"kind":"unit","ref":"command: go build ./... && go vet ./pkg/mimo/ ./pkg/lesson/ ./pkg/rt/ && go test ./pkg/mimo/ ./pkg/lesson/ ./pkg/rt/ ./pkg/voice/ -short -count=1 全绿","status":"pass"},{"kind":"unit","ref":"grep 断言：无生产代码 errors.Is(ErrInvalidResponse) 特判；EnsureLesson 调用点签名未改","status":"pass"}]

### 38. 状态机默写课堂完整流程
expected: 状态机默写课堂完整流程符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-08:D1
summary_file: 13-08-SUMMARY.md
coverage_description: "状态机默写主环端到端：session_ready 词表 → 开场 greeting（零 LLM）即首问候+首词报词、报词 PCM 字节到达客户端 → 脚本化 ASR 确认 → lesson_update → 次词报词 → 完结总结 → 匹配 ACK 后 session_ended(lesson_complete) → SubmitReview 全量一次"
verification_refs: [{"kind":"integration","ref":"pkg/rt/turn_integration_test.go#TestDictationOverConnHappyPath（真实 WSS 全链：词表/音频字节/版本 2-3-4 递增/ACK 门/handle 完结/提交 3 词）","status":"pass"},{"kind":"unit","ref":"pkg/rt/turn_test.go#TestDictationOpeningGreetsAndAnnounces + TestDictationConfirmAdvancesAndAnnouncesNext","status":"pass"}]

### 39. 重复确认与冲突处理
expected: 重复确认与冲突处理符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-08:D2
summary_file: 13-08-SUMMARY.md
coverage_description: "幂等推进：TurnTarget 分类前捕获；首提交记录 outcome+lesson_update+EventWordAdvanced+报次词或完结，Idempotent 命中只对齐零副作用，版本冲突只回读对齐——互斥且不双跳"
verification_refs: [{"kind":"unit","ref":"pkg/rt/turn_test.go#TestDictationDoubleConfirmIdempotent（二次只对齐：无 lesson_update/无播报/无事件/无完结）","status":"pass"},{"kind":"unit","ref":"pkg/rt/turn_test.go#TestDictationVersionConflictAlignsWithoutSideEffects（无重试写/无播报/回读对齐）","status":"pass"}]

### 40. 完结和无到期词的播放确认
expected: 完结和无到期词的播放确认符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-08:D3
summary_file: 13-08-SUMMARY.md
coverage_description: "session_init 三显式路径：零到期词（session_ready 空表 + 说明句 ACK 门后 no_due_words）、legacy 空 Cards（lesson_unavailable 可见失败，不 RunDue 不伪填充）、已完结重连（完成态恢复，不重复总结/SubmitReview）"
verification_refs: [{"kind":"unit","ref":"pkg/rt/turn_test.go#TestLegacyEmptyCardsIsLessonUnavailable + TestCompletedReconnectDoesNotReviewOrNarrate","status":"pass"},{"kind":"unit","ref":"pkg/rt/turn_test.go#TestNoDueWordsWaitsForPlaybackAck","status":"pass"},{"kind":"integration","ref":"pkg/rt/turn_integration_test.go#TestDictationOverConnNoDueWaitsForAck","status":"pass"}]

### 41. 复习记录只提交一次及失败可见
expected: 复习记录只提交一次及失败可见符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-08:D4
summary_file: 13-08-SUMMARY.md
coverage_description: "播放完成协调：pending 于 audio_start 前登记、epoch+sentence_id 校验、重复/旧/未知 ACK 零副作用、早到最终 ACK 粘性不丢、TTS 完成/audio_end 不解除上行门控、有界超时（先 ErrorMsg 后原 reason）、断线取消等待"
verification_refs: [{"kind":"unit","ref":"pkg/rt/turn_test.go#TestDictationRegistersPendingBeforeAudioStartAndObservesEarlyFinalAck + TestSentenceAckRejectsStaleUnknownAndDuplicate","status":"pass"},{"kind":"unit","ref":"pkg/rt/turn_test.go#TestPlaybackAckTimeoutIsVisibleAndBounded（超时 ErrorMsg 先于原 reason、无伪造完成/计时武装、上限内有序关闭）","status":"pass"},{"kind":"unit","ref":"pkg/rt/turn_test.go#TestCompletionDisconnectCancelsAckWait","status":"pass"},{"kind":"integration","ref":"pkg/rt/turn_integration_test.go#TestDictationOverConnInterruptMidAnnouncement（旧 epoch ACK 失效、零迟到帧、课堂仍可推进）","status":"pass"}]

### 42. 儿童安全话术与词库释义约束
expected: 儿童安全话术与词库释义约束符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-08:D5
summary_file: 13-08-SUMMARY.md
coverage_description: "意图分类契约：八值词表+词义/例句 ground truth 注入+严格 JSON 输出契约（hint 禁泄题、meaning 不编造）；非法输出一次纠正重试后确定性口头兜底 + 服务端 error 事件"
verification_refs: [{"kind":"unit","ref":"pkg/rt/turn_test.go#TestClassifyMessagesCarryVocabGroundTruthAndSafety（源断言：词表/ground truth/儿童安全禁令/泄题与编造规则在 system 提示）","status":"pass"},{"kind":"unit","ref":"pkg/rt/turn_test.go#TestClassifyRetriesOnceOnInvalidJSON + TestClassifyFallsBackToChatAfterSecondFailure","status":"pass"}]

### 43. 意图分类和响应容错
expected: 意图分类和响应容错符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-08:D6
summary_file: 13-08-SUMMARY.md
coverage_description: "完结诚实记录：先 SubmitReview 后播总结；ReviewWriteResult 实际集合决定口径——全量/部分/零更新/真实错误分别以真实计数、'本次保存 M/N 个'、记录未保存后缀呈现，SubmitReview 仅一次不盲重试"
verification_refs: [{"kind":"unit","ref":"pkg/rt/turn_test.go#TestCompletionWritesReviewAndWaitsForPlaybackAck + TestCompletionPartialReviewReportsActualCount + TestCompletionZeroUpdatedWithNilErrorIsNotSuccess + TestCompletionReviewFailureSpeaksHonestSuffix","status":"pass"}]

### 44. 播放确认驱动的回声保护
expected: 播放确认驱动的回声保护符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-09:D3
summary_file: 13-09-SUMMARY.md
coverage_description: "VAD 静默期门控：起始保护期吞掉整句突发上行；audio_end 后延迟 ACK 仍挡普通 VAD；有效 ACK 启动 500ms 尾窗、窗内继续挡、过期放行；旧 epoch ACK 拒绝且零副作用；播放期 interrupt 控制消息仍立即生效；普通播报缺 ACK 有界可见（ErrorMsg playback_ack_timeout）且有序关闭、绝不伪造播放完成/计时起点/推进"
verification_refs: [{"kind":"unit","ref":"pkg/rt/turn_test.go#TestVADStartGraceSuppressesEarlyAudio + TestVADEchoTailStartsAtPlaybackAck + TestVADEchoTailRejectsStaleAck + TestPlaybackPendingInterruptStillImmediate + TestPlaybackPendingTimeoutIsVisibleNoTimerAdvance","status":"pass"},{"kind":"unit","ref":"源断言：rg lastPlaybackCompletedAt 生产代码仅 settleListening 一处写入（turn.go:1274）","status":"pass"}]

### 45. 空转写容错与非法响应重试
expected: 空转写容错与非法响应重试符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-09:D4
summary_file: 13-09-SUMMARY.md
coverage_description: "ASR 空转写容忍（ErrEmptyTranscription → empty=true 事件、回 listening、零可见报错、不发起分类、计时不动）与 invalid-response 重试整合（ASR 侧单次重试新补 + LLM 侧既有兜底核对，同 ctx/最多一次/attempts 进事件字段）"
verification_refs: [{"kind":"unit","ref":"pkg/rt/turn_test.go#TestASREmptyTranscriptionSilentlyIgnoredAndTimerUnchanged + TestASRInvalidResponseRetriedOnce + TestWordTimerEmptyTranscriptionDoesNotReset","status":"pass"}]

### 46. 打断与口令共存
expected: 打断与口令共存符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-09:D5
summary_file: 13-09-SUMMARY.md
coverage_description: "打断与口令共存 backstop（RT-05 组件级）：播放期门控下 interrupt 立即生效并解除门控；打断不重置词游标、不丢 outcome；既有 TestTurnInterrupt* / TestDictationOverConnInterruptMidAnnouncement 零改动通过"
verification_refs: [{"kind":"unit","ref":"pkg/rt/turn_test.go#TestPlaybackPendingInterruptStillImmediate + TestTurnInterruptStopsAudioAndBumpsEpoch + TestTurnInterruptTrimsShortHistoryToPlayedSentences（既有，零改动通过）","status":"pass"}]

### 47. FSRS 词源部署文档
expected: FSRS 词源部署文档符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-10:D4
summary_file: 13-10-SUMMARY.md
coverage_description: "docs/DEPLOYMENT.md「FSRS 词源前提」：VPS 三前提、lessonWords 迁移说明、lessonId/wordTimeoutMs 语义、--fetch-lock-hours 脚本核验 + sha256 记录、1h 锁风险边界（RunDue 后建课前失败/跨进程并发创建；同 lessonID 重连复用 Cards 不再 RunDue；崩溃残余锁 1h 回队）、lesson_unavailable 排障"
verification_refs: [{"kind":"other","ref":"grep 断言四关键词全命中：wordTimeoutMs / lesson_unavailable / fetch-lock-hours / vocabDBPath","status":"pass"}]

### 48. 暂停、完结、无到期词与课堂进度呈现
expected: 暂停、完结、无到期词与课堂进度呈现符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-11:D1
summary_file: 13-11-SUMMARY.md
coverage_description: "tutor 页入口状态可见（RT-01 客户端半边）：paused chip 五态分派、lesson_complete 完结横幅+字幕保留总结、no_due_words「今天没有到期单词」提示、动态词数进度（初始占位「第 - / - 个词」、current_index==len → 100%+已完成）"
verification_refs: [{"kind":"e2e","ref":"tests/e2e-rt/rt-loop.spec.js#complete classroom runs on a single getUserMedia authorization（已完成态 + 完结横幅 + 字幕保留总结 + mock lessonUpdates=3/currentIndex=3）","status":"pass"},{"kind":"e2e","ref":"tests/e2e-rt/rt-loop.spec.js#pause stays paused after the confirmation speech; no-due path ends honestly（paused chip、确认话术后保持 paused、无到期词提示、无完结横幅）","status":"pass"}]

### 49. 单次授权与打断后的课堂持续运行
expected: 单次授权与打断后的课堂持续运行符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-11:D2
summary_file: 13-11-SUMMARY.md
coverage_description: "单次授权持续听说（RT-02 客户端半边）：__gumCallCount===1 贯穿完整默写课堂；报词中 interruptNow 打断——interrupt 三字段、epoch 递增、旧句无 ACK 且 stopNow 后不复活、课堂推进至完结不跳词"
verification_refs: [{"kind":"e2e","ref":"tests/e2e-rt/rt-loop.spec.js#complete classroom runs on a single getUserMedia authorization（gumCallCount===1 断言）","status":"pass"},{"kind":"e2e","ref":"tests/e2e-rt/rt-loop.spec.js#interrupt during the second announcement bumps the epoch and the lesson still completes（mock interrupts/epoch/acks + 客户端 trace 无旧键 ACK/onended 复活）","status":"pass"}]

### 50. 真实播放完成确认与资源释放
expected: 真实播放完成确认与资源释放符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-11:D3
summary_file: 13-11-SUMMARY.md
coverage_description: "播放完成协调客户端半边（T-13-24/24b）：按 (epoch,sentence_id) 账本一次性 ACK（audio_end+全部 onended 同时满足）；drainNaturally 正常排空/watchdog 3..45s 双夹取/timed_out 不伪造 ACK/mid-drain stopped；e2e 顺序断言 onended→ack→session_ended(lesson_complete)→close 与 timeout 排空（排队时不释放不 stopNow、最后 onended 后才释放）"
verification_refs: [{"kind":"unit","ref":"tests/test-rt-playback.js（多句交叠独立 ACK、旧 epoch 隔离、重复事件、正常排空、watchdog 下限 3000ms/上限 45000ms 假时钟、timed_out 零 ACK、stopped）","status":"pass"},{"kind":"e2e","ref":"tests/e2e-rt/rt-loop.spec.js#complete classroom…（trace 顺序 + stopNowCalls===0 + drain:drained→release）","status":"pass"},{"kind":"e2e","ref":"tests/e2e-rt/rt-loop.spec.js#playback-ack timeout drains with sources queued…（obs 排队/未释放/stopNow 不变 + 顺序断言 + summaryAcked=false）","status":"pass"}]

### 51. 故障中文提示及文本安全渲染
expected: 故障中文提示及文本安全渲染符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-11:D4
summary_file: 13-11-SUMMARY.md
coverage_description: "故障人话呈现（RT-10 客户端半边）：humanizeError 的 lesson_unavailable（可重试引导）与 playback_ack_timeout（不声称已听完）映射、未知码回退不回归；注入纪律负门（rg 逐字匹配四 JS + index.html 零命中）"
verification_refs: [{"kind":"unit","ref":"tests/test-rt-timing.js（两新码映射 + timeout 文案无「已听完/播放完成」 + 未知码回退）","status":"pass"},{"kind":"unit","ref":"command: rg -q innerHTML 于四个前端 JS + index.html（退出码 1 = clean）","status":"pass"}]

### 52. 实时课堂模拟服务与回归测试
expected: 实时课堂模拟服务与回归测试符合该实施计划的测试契约；依据 SUMMARY 中记录通过的验证判定，仅代表相应实现覆盖。
result: pass
source: automated
coverage_id: 13-11:D5
summary_file: 13-11-SUMMARY.md
coverage_description: "mock 默写流参考实现与四用例回归网：完结总结/no-due 提示严格等匹配 sentence_ack 才 session_ended；未知/旧 epoch/重复 ACK 只记拒绝观测；mock_ack_timeout 依次 ErrorMsg(playback_ack_timeout)→原 lesson_complete reason→正常 close（timeout 永不入 reason）；存量套件零拾取（--list 不含 rt-loop，H-1 门）"
verification_refs: [{"kind":"e2e","ref":"command: npx playwright test -c playwright.rt.config.js（4 passed，8+ 次复跑稳定）","status":"pass"},{"kind":"e2e","ref":"command: npx playwright test --list（7 tests in 4 files，零 rt-loop 拾取）","status":"pass"}]

## Summary

total: 52
passed: 35
issues: 0
pending: 17
skipped: 0
blocked: 0

## Gaps

[none yet]

## Verification Scope

- 前置 API coverage 检查已通过；仅缩短能力名称，将参数说明移至理由栏，决策未变。
- 自动通过项依据历史 SUMMARY 的 coverage 分类结果，本次未重跑这些实现测试；不代表整个阶段或真实设备验收通过。
- 13.1 已改变课堂为默写→检查→统一反馈；需要人工确认的旧描述已按现行产品语义呈现，原始描述保留供追溯。
- 用户 2026-10-09 将原 13-13 部署范围移至独立 Phase 13.2；当前只验收本地开发机。原测试 14 已从本地 Tests 列表移入 13.2 正式复验，历史描述见该阶段 references/local-uat-transfer.md，不阻塞本地阶段；本地未通过不得启动部署。
- 没有可调用的 Playwright/Puppeteer MCP，本次 UI 检查采用人工验收。
- 不删除家庭词库、不重置课行、不向生产课堂注入合成测试；自动化复测遵循 docs/E2E-ISOLATION-SPEC.md。
- 用户 2026-10-09 的整组统一显词、点击序号重读与完结连接提示反馈已修复，复验项与自动验证证据记入 [Phase 13.1 UAT](../13.1-three-stage-dictation-lesson/13.1-UAT.md) 测试 3～5；原人工验收项不因这些修复自动通过。

## Coverage Metadata Warnings

历史 SUMMARY 验证字段存在以下格式问题，对应交付均保留人工待确认，未丢弃或自动记为通过：

- 13-09-SUMMARY.md D1: invalid_kind (verification[1].kind)
- 13-09-SUMMARY.md D2: invalid_kind (verification[1].kind)
- 13-10-SUMMARY.md D1: invalid_kind (verification[2].kind)
- 13-10-SUMMARY.md D2: invalid_kind (verification[1].kind)
- 13-10-SUMMARY.md D3: invalid_kind (verification[1].kind)
- 13-12-SUMMARY.md D1: invalid_kind (verification[0].kind)
- 13-12-SUMMARY.md D2: invalid_kind (verification[2].kind)
- 13-12-SUMMARY.md D2: invalid_status (verification[2].status)
- 13-12-SUMMARY.md D3: invalid_kind (verification[0].kind)
- 13-12-SUMMARY.md D4: invalid_kind (verification[0].kind)
- 13-12-SUMMARY.md D4: invalid_kind (verification[1].kind)
- 13-12-SUMMARY.md D4: invalid_status (verification[1].status)

## Deferred Follow-Ups

- source_phase: "13"
  idea: "公网/iPad 正式呈现与听感复验移至 Phase 13.2；当前优先本地开发机可运行。"
  source_test: "13-11:D6（原测试 14）"
  target_phase: "13.2"
  deferred_at: 2026-10-09
