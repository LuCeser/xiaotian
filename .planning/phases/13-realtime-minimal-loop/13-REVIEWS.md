---
phase: 13
reviewers: [claude, codex]
reviewed_at: 2026-10-01T03:26:38.914Z
plans_reviewed: [13-01-PLAN.md, 13-02-PLAN.md, 13-03-PLAN.md, 13-04-PLAN.md, 13-05-PLAN.md, 13-06-PLAN.md, 13-07-PLAN.md]
---

# Cross-AI Plan Review — Phase 13

> 评审环境备注：本轮只有 Claude 一条通道产出有效评审。Codex 通道在两次尝试（1200s / 2700s）中均于首次工具调用处挂起（`codex_core::tools::router` 错误，与 GSD 无关的本机 codex 运行时故障），按掉线通道处理——其诊断存根见下方 Codex 节，不参与共识计权。gemini/qwen/cursor/antigravity 未安装，opencode 与本地模型通道因缺 jq 不可用（--all 下的信息性说明，非错误）。jq 缺失与 codex 故障修复后可用 `$gsd-review 13 --codex` 等补跑。

## Claude Review

# Phase 13（R0 真机最小闭环）7 份实施计划评审

**评审方法**：已逐项对照仓库源码验证（非仅读计划文本）——实际读取了 `pkg/database`、`pkg/mimo`、`pkg/ws`、`pkg/infra`、`pkg/tts`、`pkg/voice`、`cmd/service/main.go`、前端两个控制器、`package.json`/`playwright.config.js`/`Makefile`/`go.mod`/`.gitignore` 等，计划中的行号引用与符号声明基本全部核实。以下每条发现均附 `file:line` 证据。

---

## 1. Summary（总体评估）

这是一组**源码落地精度异常高**的计划：PATTERNS.md 中的行号引用（如 `pkg/ws/server.go:135` 的 `EnableCompression:false`、`session_repo.go` 的哨兵错误范式、`audio-controller.js` 的 `_playbackGeneration` 令牌三段）经逐文件核对**全部属实**；波次依赖（Wave1 三计划独立 → 13-03 契约冻结后 13-04/13-05 并行 → 13-06 接线 → 13-07 真机）排序正确，与 CONTEXT 八条锁定决策一一对应，RT-01/02/03/04/05/07/10 全覆盖且 R0 口径（成环优先、达标不前置）与 ROADMAP 一致。幂等设计（request_id=toolCall.ID + expected_version 在工具执行端单事务）忠实实现了 CONTEXT 的「不以 LLM 记忆实现幂等」。主要问题集中在**三处跨计划/计划内契约矛盾**（TTS base64 解码归属、打断历史裁剪时机、mock e2e 对存量 playwright 套件的污染）和**若干沿用范式时的遗漏**（env 绑定、yaml 模板引用失实）——均可在执行前用小幅计划修订消除，无需动架构。

**逐计划结论**：

| 计划 | 判定 | 关键证据 |
|---|---|---|
| 13-01 lesson | ✅ 优 | 范式引用全部核实（`session_repo.go:14-22,76-84,170-178`、`migrate.go:18-33`、`db.go:71-73`） |
| 13-02 mimo tools | ✅ 优 | 「client.go 零改动」主张**成立**（`client.go:297-309` options 直通 + `:312-313` 整体 marshal） |
| 13-03 rt 基础 | ✅ 良 | 传输参数照抄属实（`ws/server.go:135,136-160,200-215`）；TTS 24kHz 常量有双重代码依据（`client.go:143`、`mimo_synth.go:43`） |
| 13-04 轮次管理 | ⚠️ 良（有一处内部矛盾） | 打断历史「完成后 append」与验收测试「interrupt 后最后 assistant 消息含已播句」互相矛盾（详见 C-1） |
| 13-05 tutor 前端 | ⚠️ 良（有一处会破坏存量套件） | 存量模式引用属实（`audio-controller.js:47-52,391-398,468`、`voice-command-controller.js:142-153,260-309`）；但 spec 文件位置会污染存量 e2e（详见 H-1） |
| 13-06 服务接线 | ⚠️ 良 | 挂载点/分支位置属实（`main.go:505,525-540,541-559,111-133`）；漏 `bindEnvForAllLeaves`、yaml 模板引用失实（M-2/M-3） |
| 13-07 部署验证 | ✅ 优 | human-verify 门禁、脱敏、≥30 样本含失败、不以后端时序替代可听时刻，均与 REQUIREMENTS/13-CHECKPOINTS 对齐 |

---

## 2. Strengths（优点）

- **源码引用核查全部命中**。抽查的每一处行号/符号均真实存在且语义相符：`pkg/ws/server.go:135`（Safari TEXT→BINARY 压缩缺陷注释原文）、`server.go:136-160,178-188`（两段式 Origin + stripOriginPort）、`server.go:200,207-215`（SetReadLimit + pong 滚动 deadline）、`pkg/mimo/types.go:182-192`（ChatRequest 确无 tools/thinking 字段）、`pkg/infra/config.go:310-350`（WSConfig 范式）、`Makefile:17-19`（CGO_ENABLED=0 锁定）、`go.mod:7,11`（gorilla v1.5.3 / modernc v1.55.0，零新增依赖主张成立）。计划不是凭印象写的。
- **13-02 的「接口零改动」论断经机制验证成立**：`Chat` 的 `buildChatRequest` 对 `ChatRequest` 逐 option 赋值（`client.go:297-309`），`executeChatRequest` 直接 `json.Marshal(req)`（`client.go:312-313`）——新字段加 omitempty 后确实直通，且 `TestChatWithoutToolsUnchanged` 专门锚定存量请求体无漂移，防回归意识到位。
- **TTS 24kHz 事实有双重代码依据且跨计划自洽**：`pkg/tts/mimo_synth.go:42-44`（"Mimo pins pcm16 output at 24kHz regardless of the requested sample rate"）与 `client.go:143`（`2*24000` 注释）互证；13-03 的 `TTSOutputSampleRate=24000`、13-05 的 24kHz AudioBuffer、session_ready 声明采样率，三方一致，规避了「请求 16k 实得 24k」的变速陷阱（连 `PresetDictationTeacher` 请求 16k 的存量矛盾都被正确绕开）。
- **幂等与乐观并发设计精确实现 CONTEXT 锁定**：request_id 用 toolCall.ID、expected_version 用 `UPDATE...WHERE version=?` 0 行判冲突、两者都在 SQLite 单事务（13-01 Task 2 的事务顺序 INSERT→UPDATE→UPDATE result，冲突时回滚保证「tool_requests 无该行」的行为测试与 RESEARCH Pattern 5 逐字对应）。`db.go:71-73` 的 `SetMaxOpenConns(1)` 让单事务语义在 Go 侧天然串行，设计前提成立。
- **可替换接口与编译期契约贯穿**：13-03 定义 `SessionManager`/`Sender`/三供应商接口（签名与 `types.go:314-339` Client 接口逐字对齐，已核对 `Chat/SynthesizeStream/Transcribe` 签名），13-04 用 `var _ SessionManager` 检查，13-06 用三个 `var _` 对接——跨计划握手靠编译器不靠文档。
- **安全姿态扎实且有存量范式背书**：Origin 禁 `"*"`/拒空 Origin/去端口回退（照抄 `server.go:136-160`）、SetReadLimit+心跳（`server.go:200-215`）、工具参数执行前校验、威胁模型每计划一份；TLS 只收 cert/key 两路径、密钥不入 git/config（维持 `config.go:66-73` 的 no-secret 不变量与 D-05 约定）。
- **前端复用的 iOS 模式全部真实**：手势解锁三条件（`audio-controller.js:121-169`）、`_playbackGeneration` 捕获-比对-自增三段（`audio-controller.js:47-52, 391-398, 468`）、getUserMedia 约束三元组与 NotAllowedError 分流（`voice-command-controller.js:142-153`）、线性插值降采样+`_tail` 残余（`voice-command-controller.js:281-309`）——「打断后旧音频不复活」的客户端半边确实有已验证原型。
- **测试三轨分工诚实**：unit（fake providers 在 13-03 一次建设、13-04 复用）、mock e2e 明示「组件证据；真机验收归 13-07」、live 烟测 env 门控照抄 `integration_test.go:64-74`（无 key skip 不算失败）；13-07 坚持失败样本保留、不以 HTTP 成功/收帧替代可听时刻，与 REQUIREMENTS「必须记录」段完全对齐。
- **冻结边界被尊重**：`pkg/rt`、`pkg/lesson` 均不存在（绿地确认），全部计划零触碰 `pkg/ws`/`pkg/session`/`pkg/voice`；13-01 显式禁止 pkg/lesson import 冻结栈。

---

## 3. Concerns（问题）

### HIGH

- **H-1（13-05）：mock e2e 的 spec 位置会污染存量 playwright 套件，且验收标准断言了错误的事实。**
  计划把新 spec 放在 `tests/e2e/rt-loop.spec.js`，但存量 `playwright.config.js:16` 是 `testDir: 'tests/e2e'` 且**无 testMatch 过滤**——playwright 默认 testMatch 会匹配 `rt-loop.spec.js`。后果：`npm run e2e` / `make e2e`（Makefile:48-49）会把这个面向 mock 服务（:8099）的 spec 拿去跑**真实 Go 后端**（webServer 为 run.ps1 启动的 :8080 真服务，`playwright.config.js:33-41`），必然失败。计划验收项「存量 npx playwright test（Go 后端套件）不受新 config 影响（testMatch 隔离）」方向反了——**新 config 隔离了新运行，但旧 config 没有被隔离于新文件**；且 13-07 Task 1 预检只跑 `make test + npm test + playwright.rt.config`，`make e2e` 不在门内，该回归会**静默入库**。修复很小（见建议 1），但按「破坏既有功能 + 验收断言失实」定为 HIGH。

### MEDIUM

- **M-1（13-03↔13-04↔13-06）：TTS chunk 的 base64 解码归属在两个计划里互相矛盾。**
  13-03 的接口 `TTSSynthesizer.SynthesizeStream(..., callback mimo.StreamCallback, ...)` 中 callback 载荷是 `mimo.StreamChunk`，其 `AudioData` 是 **base64 字符串**（`types.go:276-283`）；13-04 明确「chunk.AudioData base64 解码为 s16le 字节 → SendAudio(字节)」＝**turn.go 解码**；而 13-06 的 MimoTTS 又要求「chunk.AudioData base64 解码为字节经包装 StreamCallback 上交（**调用方拿裸 s16le**）」＝**适配器解码**。`mimo.StreamCallback` 类型无法干净地承载裸字节——若适配器把原始字节塞进 string 字段，13-04 的二次 base64 解码会产出噪音（大概率报错、小概率静默 garbage）。两个计划只能有一个是对的。会在 13-06 集成时暴露（可被编译/测试抓住），但这是跨计划音频主路径的契约矛盾，应在执行前裁定。
- **M-2（13-04 计划内部）：打断后的历史裁剪与「全部句完成才 append」自相矛盾。**
  Task 2 action 第 8 条规定「**全部句完成**：history append assistant 纯文本消息」，第 5 条 interrupt 又要求「把**最后一条 assistant 消息**内容替换为实际已播放句拼接」。逐帧推演：interrupt 发生在 speakTurn 中途时最终文本**尚未入 history**，此时「最后一条 assistant 消息」是 tool_calls 消息（Content 为空串）——裁剪要么扑空、要么把播放文本错写进 tool_calls 消息（该消息会在下一轮 Chat 原样重放，wire 形状未定义）。而验收标准「两句 teacher_sentence 只 ack 第一句后 interrupt → 下一次 llm.Chat 收到的**最后 assistant 消息内容只含第一句文本**」又预设了「先 append 全文、interrupt 时裁剪」的另一种设计。这是 RT-05「下一轮理解实际已播放内容」的核心机制，两套语义混在一处；执行者的 TDD 门禁会逼出正确行为，但计划文本应先自洽（另需补：工具环进行中被 interrupt 时无口语文本可裁剪的情形）。
- **M-3（13-06）：新增配置叶子未接入 `bindEnvForAllLeaves`，违反仓库自身成文的 Pitfall 6。**
  `pkg/infra/config.go:1239-1335` 为每个叶子字段显式 `BindEnv`，且注释明言「every leaf MUST be bound explicitly or HTA_* env vars are **silently inert**」（`config.go:1255-1256,1293` 等）。13-06 Task 1 加 `server.tls.certFile/keyFile` 与 `rt.*` 七字段却只字未提更新此列表——文件配置仍可用（viper 按 key 覆盖），但 `HTA_SERVER_TLS_*`/`HTA_RT_*` 将静默失效，与全仓约定及既有测试手法（`main_test.go:415-417` 正是用 `HTA_PYTHON_*` env 驱动 run()）不一致。
- **M-4（13-06）：read_first 引用的「main_test.go 既有 yaml 模板」不存在。**
  `cmd/service/main_test.go` 的全部 boot 测试都以**不存在的 config 路径**+env 覆盖运行（`main_test.go:42,72,116...` + `:415-417`），仓库里没有「临时 config yaml」先例。13-06 Task 3 的 TLS 测试需要自撰完整 yaml，且必须包含 Validate 硬性要求的 `python.interpreter/scriptsDir/dataDir`（`config.go:1103-1108,1137-1140`）——计划把它说成照抄既有模板，执行者会找不到模板（易走弯路，且漏 python 段时 run 返回 1 会与「缺证书返回 1」的断言混淆）。
- **M-5（13-04）：`lesson_id` 未做会话绑定校验，弱于 RESEARCH 自身声明的控制项。**
  Research 安全域 V4 写明「lesson_id 由服务端会话绑定，不信客户端自报」，但 13-04 的 `validateToolArgs` 只校验「lesson_id 非空字符串 + 工具名白名单」。幻觉/注入的 lesson_id 会直达 `lesson.Executor`（R0 库中只有一课，误写他课会得 `ErrLessonNotFound`，危害有限，但这是已声明却未落地的缓解措施）。应在 turn.go 入口加 `lessonID == cfg.LessonID` 判定。
- **M-6（13-07 / RT-04）：跨机时钟的「句末→首实际可听」测量方法论被显式悬置。**
  `speech_ended` 只能产生于服务端 VAD、`first_audio_played` 只能产生于客户端——RT-04 的核心指标天然跨时钟，而 CHECKPOINTS 明令禁止跨机相减。13-07 Task 1 的处理是「分列，成环基线用第一周口径说明」，等于把「第六周如何算 P50/P95」推迟到未定义。R0 范围内（只齐测量）成立，但第六周切换判定依赖这个数字，越晚定义方法论，第六周风险越大。
- **M-7（13-06）：复用共享 mimoClient 的 20s HTTP 超时是未声明的假设。**
  `main.go:404-407` 将共享 client 的 Timeout 定为 `max(voice 15s, photo 20s)=20s`，且该 timeout 覆盖**整个请求含流式 body 读取**。rt 复用该 client：VAD max-segment 30s 音频的整段 ASR、长句 TTS 流都挤在 20s 内——按快于实时的处理速度大概率够，但计划未记录该预算，也没有为 rt 另立 client/超时的决策记录。

### LOW

- **L-1（13-03）**：timing.go 称时间戳为「服务器**单调**时钟纳秒」，但 `time.Now().UnixNano()` 并不保证单调（受 NTP 步进影响）。服务端内部求时长应锚 `time.Since` 起点；跨机对齐本来就按事件 ID，无实害，但 godoc 措辞应准确。
- **L-2（13-01）**：事务里「sqlite 报 UNIQUE 约束失败」的判定方式未指明（modernc 驱动的约束错误需按 `sqlite.Error`/code 匹配，不是字符串包含）；建议在 action 里点名 `errors.Is(err, sqlite.ErrConstraint)` 类判法，防执行者写成字符串匹配（与「禁止字符串匹配」的既有约定冲突）。
- **L-3（13-01/存量交互）**：`db.go:71-73` 单连接池下，`AdvanceWord` 事务持有唯一连接期间，冻结栈 `/ws` 会话表的写会被短暂阻塞。R0 事务只有三条短语句，可接受；建议在 13-01 注释里写明该前提。
- **L-4（13-04）**：逐句 TTS 是**串行**的（句 2 的合成等句 1 流完）——首包延迟达标不受影响，但多句回复的句间接续存在串行合成空隙；R0 可接受，Phase 16 前可考虑「播放中预合成下一句」。
- **L-5（13-04，产品设计确认项）**：半双工门控意味着 SPEAKING 期间学生打断话语本身不被采集，interrupt 后学生需（继续）重说——与 RESEARCH Pattern 3 的设计一致，但对「插话即成轮」的体验预期要有认识（真机验证时观察）。
- **L-6（13-02）**：`Message` 同时用于 TTS `TTSRequest.Messages`（`types.go:20`），新增 `ToolCalls/ToolCallID` 靠 omitempty 保证 TTS 请求体不漂移——`TestChatWithoutToolsUnchanged` 只断言顶层无 `tools/thinking` 键，未覆盖 message 级；建议补一条 message 级断言。
- **L-7（13-06）**：`TLS *TLSConfig` 指针 + yaml 中出现空 `tls: {}` 块时，viper 会给出非 nil 但两字段全空的结构——Validate 只管「一空一非空」，main.go 的 `cfg.Server.TLS != nil` 分支会把空路径传给 `ListenAndServeTLS`。建议 Validate 直接判「两字段都空 = 视为禁用或报错」二选一写死。
- **L-8（研究文档小误）**：RESEARCH 称 `ws` npm 包为「存量 devDep」，实际在 `package.json:6-8` 的 **dependencies**；无实害，备案。

---

## 4. Suggestions（建议）

1. **（对 H-1，必改）** 13-05 将 mock e2e 迁至独立目录 `tests/e2e-rt/`（`playwright.rt.config.js` 的 testDir 指向它），或给存量 `playwright.config.js` 加 `testIgnore: '**/rt-loop.spec.js'`；同时把验收项改为可真正验证的断言（如 `npx playwright test --list` 输出**不含** rt-loop）。
2. **（对 M-1，必改）** 裁定 base64 解码唯一归属。推荐：13-03 的 `TTSSynthesizer` 回调改为 rt 自有类型（如 `func(pcm []byte, isFinal bool) error`），解码归 MimoTTS 适配器——「供应商 wire 细节止步于适配层」与本阶段可替换接口的定位最一致；同步修订 13-04 的 speakTurn 描述（删去其自行解码语句）。
3. **（对 M-2，必改）** 13-04 统一为「speakTurn 开始时 append 全文 assistant 消息 → interrupt 时按 sentence_ack/played_until 裁剪为实际已播内容」，并补「工具环进行中 interrupt（尚无口语文本，不裁剪，仅取消）」的分支；使 action 与验收测试同构。
4. **（对 M-3/M-4）** 13-06 Task 1 补 `bindEnvForAllLeaves` 增项（server.tls.certFile/keyFile + rt.* 七键）与对应 config_test 用例；Task 3 的 read_first 改为「main_test.go 的 env 驱动模式（`main_test.go:415-417`）+ 自撰含 python 必填段的临时 yaml」，并在测试里先跑一次「无 TLS 配置正常 200」对照，避免缺段返回 1 与缺证书返回 1 混淆断言。
5. **（对 M-5）** 13-04 `validateToolArgs` 增加 `args.lesson_id == cfg.LessonID` 判定（不符即 invalid_arguments 回喂），落实 Research V4 已声明的控制。
6. **（对 M-6）** 在 13-07 Task 1 里现在就定义 RT-04 口径，例如：a) 每会话一次时钟偏移估计（客户端发起 `ping`，服务端回 `t_recv`，客户端以 RTT/2 估计偏移并记录进 JSONL）；或 b) 明确双代理指标组——服务端 `speech_ended→first_audio_chunk_sent`（同钟）+ 客户端 `transcript_received→first_audio_played`（同钟），并声明第六周以哪组为准。任选其一写死，避免第六周临期争论。
7. **（对 M-7）** 13-06 在接线处加一行决策注释：rt 复用共享 mimoClient、20s 单请求预算的依据（ASR 快于实时、逐句 TTS 短句），或为 rt 构造独立 client（`rt` 配置段已有位置可放 timeout）。
8. **（增强，可选）** 13-04 收尾可加一个 `TurnManager + conn.go` 的进程内集成测试（httptest + gorilla Dialer + 13-03 fakes），把「binary 帧→VAD→fake ASR/LLM/TTS→SendAudio」整链走一遍——现有计划只有分件测试，整链拼装问题（如 sender 并发使用）目前靠 `var _` 与人工推演。

---

## 5. Risk Assessment（风险评级）

**总体：MEDIUM（执行前应用上述 1–3 修订后可降为 LOW-MEDIUM）**

- **计划质量风险：低**。源码引用核实率极高、依赖排序正确、CONTEXT 锁定决策逐条落地、R0「成环优先/达标不前置」的口径贯穿始终；无范围蔓延（词库/OCR/并发容量均被显式挡在界外）。
- **集成返工风险：中**。H-1 会静默破坏存量 `make e2e`；M-1/M-2 是两处契约级矛盾，会在 13-05/13-06 执行中暴露并触发偏差处理——都可被编译/测试/TDD 门禁拦下，不会带病进入真机，但会造成返工与计划文本失真。
- **真机风险：中且已被正确管理**。回声/AEC、h2 回退、采样率协商等最大的不确定项全部显式登记为假设（A1–A7）并配有真机 checkpoint 与降级路径（rnnoise-go、禁 h2 一行修复、URL 参数标定），13-07 不允许以 mock 证据替代真机验收——这是该批计划最成熟的部分。
- **日程风险提示**：第一周截止 2026-10-07。H-1/M-1/M-2 均为计划级小修，建议在 `/gsd-execute-phase 13` 前完成修订（半小时量级），避免把可前置的矛盾留到 Wave 2/3 的执行偏差里消化。

---

## Codex Review

codex review failed or returned empty output.

## Diagnostic

**Attempt 1** — killed at the lane's 1200s floor timeout; stderr transcript ends:

```
codex
我会逐一核对计划引用的源码和测试，重点检查工具事务、音频取消、时序测量与运行时接入是否能支撑真机闭环，再给出按计划分组的审查意见。

hook: PreToolUse
hook: PreToolUse

[spawn error: ETIMEDOUT]
```

**Attempt 2** — timeout raised to 2700s via `review.timeouts.codex`; killed at 2701s with the identical signature (session begins, first tool call attempted, hangs at PreToolUse).

**Minimal reproduction outside GSD** (2 attempts, incl. one with project `.codex/hooks.json` temporarily disabled):

```
codex
I'll read the first three lines of go.mod.
hook: PreToolUse
2026-10-01T03:25:12.536737Z ERROR codex_core::tools::router: error=code-mode host closed its stdout
```

Even a trivial "read go.mod and reply PROBE-OK" prompt hangs forever at the first tool call. Variant errors across runs (`[spawn error: ETIMEDOUT]` and `codex_core::tools::router: error=code-mode host closed its stdout`), both in codex's tool-invocation layer.

**Environment:** codex-cli 0.159.2 on Windows, model gpt-6.1-sol (reasoning=high), `--ephemeral`, sandbox workspace-write. Hooks were trusted and SessionStart/UserPromptSubmit hooks completed; the hang is in the tool router, reproducible with project hooks disabled.

**Conclusion:** not a budget/time problem — `codex exec` on this host cannot execute any tool call right now. Lane dropped; verdict carries no consensus weight. Fix the local codex installation (or upgrade codex-cli) and re-run `$gsd-review 13 --codex`.

---

## Consensus Summary

仅一名有效评审人（Claude），下述条目为单源结论，未经第二模型交叉确认；Codex 修复后补跑可作交叉验证。

### Agreed Strengths

（单评审人，无跨模型共识；以下为 Claude 评审中证据最扎实的强项，置信度较高）

- 源码落地精度高：抽查的 `file:line` 引用（`pkg/ws/server.go:135-215`、`pkg/mimo/types.go:182-192`、`pkg/infra/config.go:310-350`、`pkg/tts/mimo_synth.go:42-44` 等）全部核实命中；波次依赖与 CONTEXT 八条锁定决策一一对应。
- 幂等与乐观并发设计忠实实现「不以 LLM 记忆实现幂等」：request_id=toolCall.ID + expected_version 在 SQLite 单事务，且 `db.go:71-73` 的 `SetMaxOpenConns(1)` 使事务天然串行。
- 测试三轨分工诚实：unit / mock e2e（明示组件证据）/ env 门控 live 烟测，13-07 坚持失败样本保留、不以收帧替代可听时刻。

### Agreed Concerns

（单评审人；按优先级列出 Claude 的发现，待第二评审人确认）

1. **H-1（13-05）**：mock e2e spec 放 `tests/e2e/rt-loop.spec.js` 会污染存量 playwright 套件——存量 config 无 testMatch 过滤，`make e2e` 会拿面向 mock 服务的 spec 跑真实 Go 后端，必然失败且静默入库（13-07 预检不含 `make e2e`）。
2. **M-1（13-03↔13-04↔13-06）**：TTS chunk 的 base64 解码归属在三个计划间互相矛盾（turn.go 解码 vs 适配器解码），跨计划音频主路径契约需在执行前裁定。
3. **M-2（13-04 内部）**：打断后历史裁剪时机自相矛盾（全部句完成才 append vs interrupt 时替换最后一条 assistant 消息），这是 RT-05「下一轮理解实际已播放内容」的核心机制。
4. **M-3/M-4（13-06）**：新增配置叶子未接入 `bindEnvForAllLeaves`（违反仓库成文 Pitfall 6，`HTA_RT_*` env 将静默失效）；引用的「既有 yaml 模板」不存在。
5. **日程风险**：H-1/M-1/M-2 均为计划级小修（半小时量级），应在执行前完成，避免 Wave 2/3 返工——第一周截止 2026-10-07。

### Divergent Views

无——第二名评审人（Codex）未产出评审，不存在分歧记录。
