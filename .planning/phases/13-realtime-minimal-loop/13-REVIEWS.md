---
phase: 13
reviewers: [claude]
reviewed_at: 2026-10-04T07:58:55.526Z
plans_reviewed: [13-01-PLAN.md, 13-02-PLAN.md, 13-03-PLAN.md, 13-04-PLAN.md, 13-05-PLAN.md, 13-06-PLAN.md, 13-07-PLAN.md, 13-08-PLAN.md, 13-09-PLAN.md, 13-10-PLAN.md, 13-11-PLAN.md, 13-12-PLAN.md, 13-13-PLAN.md]
external_review_count: 1
source_crosscheck: current_codex_orchestrator
---

# Cross-AI Plan Review — Phase 13

> 2026-10-04（Asia/Shanghai）重新评审全部 13 份计划；13-01～13-06 已执行，13-07～13-13 按 2026-10-03 状态机转向决策评估。本报告替代旧版七计划评审，历史内容仍可从 Git 读取。
>
> --all 检测结果：Claude、Codex、OpenCode 已安装；按技能独立性规则跳过当前执行者 Codex；OpenCode 以及三个本地模型评审方式因缺 jq 不可选；Gemini/CodeRabbit/Qwen/Cursor/Antigravity 未检测到。仅 Claude CLI 返回有效外部报告，不能称为多模型共识。
>
> 运行说明：GSD 默认 Windows 启动以 ENOENT 失败，经临时可执行文件路径适配后沿用同一 review-lane runner、提示词和 1200 秒上限成功完成；未改 GSD 安装。无提示词裁剪。Claude Code 版本 2.1.289；stderr 报告模型标识 glm-5.2[1m] 的 unrecognized_model 警告，因此这里只确认 Claude CLI 适配器，不能据名称认定使用了 Anthropic Claude 模型。警告没有使进程失败。
>
> 下节保留外部意见；结尾原本是否定“无仓库访问”的格式标记，已改为普通文字避免机器误判。后面的 Consensus Summary 明确列出执行者复核、补充发现和不采纳的推断，二者不得合并计为两位独立外部评审者。

## Claude Review

# Phase 13 计划集交叉评审报告（独立只读评审）

评审范围：13-01～13-06（已执行，对照现有代码复核）＋ 13-07～13-13（待执行，按计划机制与现有依赖评估）。已读取 `.planning/STATE.md`、13-CONTEXT/RESEARCH/PATTERNS/CHECKPOINTS、全部 PLAN/SUMMARY，并对照 `pkg/rt`、`pkg/lesson`、`pkg/mimo`、`pkg/pybridge`、`pkg/infra`、`cmd/service/main.go` 源码逐项核实。未运行任何实现工作流，未改动任何文件。

## 总体判断（Summary）

计划集与 2026-10-03 转向决策（STATE.md 末条）一致，且**代码现状与计划前提吻合**：工具环仍在位待删（`pkg/rt/turn.go:95-99` lessonToolDefs、`:549` executeToolCall、`:798` validateToolArgs）；`main.go:564-577` 的 rtTurnCfg 仍引用 LessonWords/MaxToolLoops；ASR 空 choices 仍与坏 JSON 共用 ErrInvalidResponse（`pkg/mimo/client.go:797-802`）；lesson 表无 cards 列（`pkg/lesson/store.go:41-85`）；协议无 paused/lesson_unavailable（`pkg/rt/protocol.go:67-88`）；pybridge DueCard 已有 word/meaning/example 三字段（`pkg/pybridge/executor.go:122-126`）且 DueOpts 支持 Limit/Peek/FetchLockHours（`pkg/pybridge/vocab_test.go:84-141`）。13-07～13-13 的依赖顺序、并行波次划分、幂等键设计（lessonID-advance-目标index）在机制层面成立。主要风险集中在：第一周截止时间、VAD 回声尾窗口与真实播放结束的错位、legacy 课堂行的边界语义、以及多份上位文档未随转向更新。

## 优点（Strengths）

1. **幂等键设计比旧 tool_call id 方案更强**。13-08 用 `lessonID-advance-目标index` 作确定性 request_id，落在 13-01 已验证的单事务幂等层上（`pkg/lesson/executor.go` 的 BeginTx + request_id 主键 + expected_version，SUMMARY 有测试锚定）。「确认与超时对同一目标竞争只推进一次」由此结构性保证，不依赖时序。
2. **转向后的威胁面显著收窄且论证正确**。LLM 从「可发起写工具」降为「只能产出 intent 标签 + reply 文本」，提示注入的后果上限等于一次推进（等价于超时），13-08 威胁寄存器 T-13-13 的 accept 论证成立；reply 裁剪（T-13-15）与固定人话错误文案（T-13-14）补齐了剩余面。
3. **契约先行（13-07）与并行波次 ownership 清晰**。13-07 三个子系统互不依赖（mimo 哨兵 / lesson 词卡 / rt 协议词表），13-08（FSM）与 13-11（前端）同波并行、13-09 与 13-10 同波并行，文件集无交集：13-08/13-09/13-11 都动 `pkg/rt` 与 tutor 前端但改不同文件（dictation.go/turn.go vs 前端 JS），唯一共享点 turn.go 在 13-08→13-09 串行，无写冲突。13-08 Task 1 step 6 明确「编译对端同步同 commit」，避免了半编译中间态。
4. **缺陷修复与转向同源归因正确**。VAD 0 秒误触发、invalid API response ×4 被归因到「误切空段打 ASR」，13-07 哨兵分离（ErrEmptyTranscription ≠ ErrInvalidResponse，互斥断言双向）→ 13-09 静默容忍 + 单次重试，链条闭合。
5. **验收证据纪律强**。13-12/13-13 坚持「本机预验收 ≠ 正式验收」「mock 不计语音通过」「样本不剔除失败」「以真实可听时刻计量」，prohibition 均为判断级且带 blocking-human 门；脱敏三重纪律（JSONL 不入库、grep 暂存 diff、凭据不入档）落实到位。
6. **已执行计划的 SUMMARY 与代码对得上**。抽查 13-06 声明的 TLS 三态、IsSet 兜底、LessonTools 适配器等在 config.go/main.go/mimo_adapters.go 均存在（RTConfig 现状 `pkg/infra/config.go:193-253`、默认三词 `:998-999`，与 13-10 将重构的字段一致）。

## 问题（Concerns）

**HIGH-1｜VAD 回声尾窗口与客户端真实播放结束错位（13-09 Task 3）**
`VADEchoTailMs=500` 从「服务端 speakTurn 结束」起算，但 audio_end 是服务端**发完**的时刻，客户端此刻仍在播放排队 chunk（rt-playback 有 60ms 起播偏移 + 队列调度，且 24kHz chunk 在途）。服务端 state 回 listening、500ms 窗口过期后，iPad 扬声器仍在放老师的话，回声进麦克风会被 VAD 切句 → 伪学生轮次 → ASR 空转写（已被 13-07 容忍，无害）或更糟：被分类为 confirm 类意图的杂音。这是 RT-05/回声自检的真机隐患，建议以 sentence_ack（客户端实际播完）驱动回声尾窗口，或把默认窗放大到 ≥1s 并在 13-12 标定项中显式列出此错位。

**MEDIUM-1｜legacy 课堂行 / 空 cards 的重连语义未指定（13-07 + 13-08）**
13-08 的 handleSessionInit 规则是「GetLessonState 命中即跳过取词直接用已存 Cards」。但：(a) 13-06 期间本地与 VPS 可能已落 `r0-test` 行（words=美丽/勇敢/聪明，迁移后 cards='[]'）——重连会拿到一个**无词卡、无 ground truth、且不知进度的旧测试课**，hint/meaning 全部走「没有释义」兜底；(b) 当天课程完结后（current_index==len）再次连接同一 lessonID 的行为未定义（应走完结/无课提示还是重建）。建议 13-08 明确：`cards 为空` 或 `课程已完结` 时的分支（视为 ErrLessonNotFound 重取词，或发 lesson_complete 幂等回复）。

**MEDIUM-2｜RunDue 取词锁定与课堂夭折的交互（13-10）**
PyFSRS 用非 peek RunDue（会触发 fetch-lock），词取走后若会话中断、SubmitReview 永不发生，词保持锁定直到锁窗过期。T-13-20 已按 R0 accept，但计划未显式设置或核对 `FetchLockHours` 的脚本默认值——若默认很长（如 24h），孩子当天重连会「没有到期词」直接 no_due_words 结束。建议 13-10 在 DueOpts 中显式决定 FetchLockHours（或核实脚本默认值并写入 DEPLOYMENT 排障段）。

**MEDIUM-3｜第一周时间线（项目风险，非计划缺陷）**
2026-10-07 23:59 截止，剩 13-07～13-13 七个计划，含两个 blocking-human 检查点、真机多会话采样（≥30×2）与用户资产（证书/VPS/python 词库）依赖。13-13 flagged assumption 已如实登记「不自动延后、不把本机结果记为通过」，处置正确，但按当前剩余工作量，第一周未成环的概率不低；应预先按 13-CHECKPOINTS 规则准备失败记录与聚焦清单。

**MEDIUM-4｜上位文档与转向决策冲突（文档问题，单独报告，非功能阻塞）**
- `13-CONTEXT.md:16-17` 仍是旧决策原文（固定 3 词、LLM get_lesson_state/advance_word 工具环、「普通沉默不跳词」），与 STATE.md 2026-10-03 决策直接矛盾；13-07+ 的 PLAN 引用它为 context 又声明取代。13-RESEARCH 的 OQ 勘误与 13-CHECKPOINTS 模板勘误已打补丁，但 CONTEXT 本体未改。
- `.planning/REQUIREMENTS.md` RT-06 仍写「不因普通沉默自动跳词」，而新模型 60 秒超时正是沉默推进；STATE.md 已声明推翻 RT-06 原文，但 RT-06 归属 Phase 14 且需求文本未更新。
- `PROJECT.md` Active 首条仍写「3 固定词…真实读写工具调用及幂等验证」（旧口径）。
建议在执行 13-07 前做一次小规模文档对齐（或至少在 CONTEXT 头部加转向注记，如 CHECKPOINTS 已做的那样），避免后续执行者读 CONTEXT 走回旧路。

**LOW-1｜StatePaused 命名撞车**：冻结栈 `pkg/database/schema.go:36` 已有 `StatePaused = "PAUSED"`（旧按钮 FSM），新 `rt.StatePaused = "paused"`。值与包均不同、无功能冲突，仅排查/grep 时需注意。

**LOW-2｜13-11 mock 以定时器推进课堂**，不经学生音频验证确认路径的前端表现（已自我登记为 T-13-23，13-12 真机对账兜底）。可接受。

**LOW-3｜13-13 要求 ≥30 次打断样本**，即孩子需成功插话 30 次（可多会话累计）。对儿童用户是不轻的负担，样本采集安排上建议允许多天累计并在 CHECKPOINTS 记录进行中状态。

**LOW-4｜13-09 意图重置计时的细则**：「任何被处理的学生话语（confirm 以外）重置计时」含 chat；而 ErrEmptyTranscription 轮（无 turn 发起）不重置——语义正确但建议在 13-09 测试里加一条空转写不重置计时的用例，防实现把「收到音频」当「学生互动」。

## 具体建议（Suggestions）

1. 13-09：回声尾窗口改为 sentence_ack 驱动或默认 ≥1000ms，并在 13-12 Task 2 第 5 步（打断项）旁增加「老师播报结束 0.5–1.5s 后保持安静」的回声观察步骤。
2. 13-08：handleSessionInit 补两个显式分支——`已存课堂 cards 为空`（含 legacy r0-test 行）与`current_index==len`（已完结），并各配一条测试。
3. 13-10：DueOpts 显式设定/核实 FetchLockHours，写入 DEPLOYMENT「FSRS 词源前提」排障顺序。
4. 13-07 执行前：给 13-CONTEXT.md 加转向注记（一段即可），REQUIREMENTS RT-06 加「2026-10-03 口径修订」脚注，PROJECT Active 首条改写为状态机默写口径。
5. 13-12：评级映射追认（confirmed→good / timeout→hard / hint→again）建议同时确认 hint 后又超时的覆盖顺序（现计划为「最后状态为准」，即 hard 覆盖 again——确认这是产品意图）。

## 风险评估（Risk Assessment）

**总体：中。** 架构一致性高（转向决策 → 13-07 契约 → 13-08 主环 → 13-09 交互全集 → 13-10 生产接线 → 13-11 前端 → 13-12/13-13 验收，链路无断点、无循环依赖、并行波次文件所有权干净），幂等与威胁模型在转向后反而更稳。真正的不确定性集中在**真机物理层**（回声/静默期/打断阈值，HIGH-1 与既有 A1/A5 假设）与**时间线**（MEDIUM-3）；两者都有已安排的真机检查点兜底，不会静默产假绿。文档滞后（MEDIUM-4）是当前最应先修的低成本项。

## 分计划简评

| 计划 | 评估 | 要点 |
|---|---|---|
| 13-07 | ✅ 良好 | 三个契约独立、存量零回归有锚；Word 标签与 DueCard 逐字对齐已核实（executor.go:122-126）；迁移幂等含 legacy 补列。建议补：rt.StatePaused 与旧栈撞车的 godoc 注记 |
| 13-08 | ✅ 良好（tracer 恰当） | 幂等键设计强；编译对端同 commit 处理干净；需补 MEDIUM-1 的空 cards/已完结分支 |
| 13-09 | ⚠️ 良好但有一处真机隐患 | 计时器四重校验 + 幂等竞争测试完备；HIGH-1 回声尾窗口与客户端播放结束错位需在实现/标定时修正 |
| 13-10 | ✅ 良好 | 配置四件套与 env 纪律延续；MEDIUM-2 FetchLockHours 未显式决策 |
| 13-11 | ✅ 良好 | innerHTML 负门 + 隔离复检维持；mock 定时推进的局限已登记并由 13-12 对账 |
| 13-12 | ✅ 良好 | 本机预验收通道有据（CHECKPOINTS 2026-10-03 段）；「本机 ≠ 正式」纪律严；依赖 user 到期词，前置条件已列 |
| 13-13 | ✅ 良好 | 部署/验收/报告/检查表四件齐；第一周行如实更新承诺明确；时间线风险如实登记 |
| 13-01～06（已执行） | ✅ 与代码吻合 | 抽查关键声明（TLS 三态、LessonTools、工具环在位待删、约束码 1555 扩展）均在源码可证；13-04 的工具环运行时路径按约将被 13-08 取代，wire 支持保留符合决策 |

*本次评审声明已读取仓库；下文汇总另列复核与修正。*

---

## Consensus Summary

### 评审范围与结论

本次仅一个外部评审实例成功返回，因此**没有多模型共识**。Claude CLI 原始判断为 MEDIUM；结合当前执行者的源码复核，建议先修订下列计划缺口，再执行 13-08/13-09/13-11。补充发现均指向待执行计划与现有代码之间的具体关系，不表示这些 FSM 缺陷已经存在于尚未实现的代码中。

### Agreed Strengths

无两位独立外部评审者可供交叉计数。已复核的优点是：13-07 冻结数据/协议契约，再由 13-08 和 13-11 消费；现有课堂推进在单事务中维护 request_id 和 expected_version（pkg/lesson/executor.go:101、:122、:146）；13-12/13-13 分开本机预验收与正式真机验收。数据库幂等成立不等于调用方所有副作用都已幂等，见 S-03。

### Agreed Concerns

无多外部评审共识。外部 HIGH-1 经复核成立；其余按下列修正使用，不能把原报告中的全部肯定表述当成已验证事实。

### 执行者源码复核：优先修订项

#### S-01 — HIGH：完结消息会截断仍在播放的总结（13-08、13-11）

- 证据：13-08-PLAN.md:152 在 speakTurn 返回后发送 session_ended 并 internalClose；同计划 :116 的无到期词分支也在 speakTurn 后结束。现有 pkg/rt/turn.go:654、:675、:686 只等待 TTS 流发送完，不等待浏览器播完。pkg/web/static/tutor/js/rt-playback.js:215 把音频安排到未来时间，:237 才在播放结束回调释放播放任务。pkg/web/static/tutor/index.html:223 的 session_ended 处理调用 :161 的 stopEverything，其中 :165 立即 playback.stopNow。
- 机制：提供商生成快于实时播放时，总结仍在播放或排队，session_ended 随即触发停止，学生听不到完整总结或“记录未保存”的告知。13-11-PLAN.md:149 只断言字幕/横幅存在，无法证明总结被听完。
- 修订：定义自然完结的播放完成条件，与用户主动停止、故障、打断分开处理；麦克风释放与音频播放结束分别管理。复用客户端实际播放完成确认时明确最后一句、epoch、超时及断线行为。增加“音频已发完但浏览器尚未播完”的验收用例，同时覆盖 lesson_complete 和 no_due_words。

#### S-02 — HIGH：重置后的旧计时回调无法靠 epoch/index 识别（13-09）

- 证据：13-09-PLAN.md:112 只保存 (epoch, wordIndex)，resetWordTimer 停止后以相同参数重建；:113 只核验关闭、epoch、游标、paused 和 listening；:115 却要求手动触发旧回调不推进。现有 pkg/rt/turn.go:465 只读取 epoch，普通新语音轮次在 :466 递增 turnID，不递增 epoch。
- 机制：repeat/chat 重置计时但不推进词游标或打断，旧回调若已启动、等待锁，在互动结束后获取锁，其所有校验仍成立。停止计时器不能撤回已经开始的回调，旧回调可能提前推进当前词。
- 修订：每次武装/重置/停止更新独立计时器代数或身份，在锁内比较并使旧回调失效；确定性测试模拟旧回调已排队、重置完成后才执行。另将 :113 的 processing 分支放在通用 state 拒绝之前，消除“非 listening 丢弃”和“processing 重置”的矛盾。

#### S-03 — MEDIUM：幂等命中后的副作用规则自相矛盾（13-08、13-09）

- 证据：13-08-PLAN.md:120 对“成功或 Idempotent=true”都要求写 outcome、发 LessonUpdateMsg/EventWordAdvanced、播下一词；:125 的测试却要求 Idempotent=true 不重复发消息或报词。13-09-PLAN.md:114 又要求竞争时 outcome 取先到者。pkg/lesson/executor.go:246 的幂等读取仅返回首次数据库结果，不阻止调用者重复播报和改内存记录。
- 修订：把首次提交、幂等命中、版本冲突写成互斥分支；明确重复结果只做必要的状态对齐，不覆盖首次 outcome、不重复播报或触发完结写记录。分类启动时捕获目标词/版本，避免迟到确认按照已经变化的 currentIndex 重新计算另一目标。分别验证确认先到与超时先到；计划当前的单向用例不能证明全部竞争安全。

#### S-04 — MEDIUM：暂停播报后会恢复 listening，暂停状态显示不稳定（13-09、13-11）

- 证据：13-09-PLAN.md:86 依次发 paused、调用 speakTurn(pauseAckText)、停止计时；13-08-PLAN.md:114 明确保留 speakTurn 的机制。pkg/rt/turn.go:627 播报时设 speaking，:686/:690 无条件恢复并广播 listening。13-11-PLAN.md:90 要求 paused chip 随 state 显示。
- 机制：按计划直接实现时，暂停确认语音结束后页面显示 listening，内部 paused 位却仍阻止推进。若直接把内部 state 改成 paused，pkg/rt/turn.go:437 和 :461 又会拒绝上行语音和新轮次，导致听不到“继续”。
- 修订：明确内部语音处理状态与课堂暂停位的关系，播报结束后根据暂停位发出正确 UI 状态，并保留暂停期的口令识别。用真实后端验证“暂停确认播完→仍显示暂停→说继续可恢复”，不能仅靠 mock 广播 paused。

#### S-05 — MEDIUM：学习记录写入应核对 Updated，不能只看 error（13-10）

- 证据：13-10-PLAN.md:113 仅规定调用 RunReview；13-08-PLAN.md:152 随 SubmitReview 成功就发 EventReviewWritten(count=N)。pkg/pybridge/vocab.go:311 明确子进程会以 exit 0 返回部分/零 updated 并带 Warning，:363 直接返回解析结果和 nil；pkg/pybridge/executor.go:183 定义了 Updated 结果。
- 机制：词在取出后被删除，或其他跳过条件出现时，RunReview 返回 nil error 但未写全。适配器若丢弃 Updated，将把未保存报告为成功；“词库唯一词”假设不能覆盖词被删除的情况。
- 修订：核对请求集合与实际更新集合，部分成功要明确报告实际数量并走诚实反馈。增加空 updated/部分 updated 且 error=nil 的测试；不要整批盲目重试已经更新的 FSRS 项。

#### S-06 — LOW：两个验证过滤器漏掉计划指定用例

13-09-PLAN.md:88 的 TestConfirmWhilePausedIsGentleRejected 不被 :91 的 -run TestIntent 匹配；13-10-PLAN.md:115 将用例拼为 TestPyFRSSubmitReviewPassesResultsAndSkipsEmpty，不被 :118 的 -run TestPyFSRS 匹配。全包测试可能兜底，但局部“用例名必须出现”的验收条件无法按原命令通过。统一名称或修正过滤器。

### Divergent Views / 对外部意见的修正

1. **幂等与计时器并未完整证明。** 外部称“结构性保证”“竞争测试完备”，但 S-02/S-03 给出了计划机制和测试契约之间的缺口，不能照单采纳。
2. **回声尾计时问题成立，单纯把窗口扩大到 1 秒不是确定性修复。** 13-09-PLAN.md:139 从服务端 speakTurn 结束起算，pkg/web/static/tutor/js/rt-playback.js:215/:237 证明实际播放更晚结束。应基于有效的播放完成证据，并处理客户端缺失确认、断线、打断；同一差异也影响书写计时开始点。真机标定不能替代控制逻辑的正确性。
3. **取词锁风险需要收窄触发条件。** 外部 MEDIUM-2 所称普通同日重连再次取词，不符合 13-08-PLAN.md:116 的复用已存 Cards 分支。风险主要是 RunDue 已锁词但课堂创建未成功、跨日/更换 lessonID、并发创建产生未使用的词批。当前相邻资产仓库脚本 D:/workspace/ai/xiaotian-skills/shared/dictation/scripts/vocab_manager.py:98 的默认锁定值确为 24 小时；:278 附近负责写锁。部署仍须核对实际配置的脚本版本，不把邻仓文件当 VPS 事实。
4. **旧 cards 为空和已完结重连仍是有效缺口。** 13-07-PLAN.md:118 的迁移测试明确保留空 Cards；13-08-PLAN.md:116 直接消费持久化 Cards，缺少边界分支。不要按外部建议简单“视为不存在再 EnsureLessonCards”：pkg/lesson/store.go:128 的 INSERT OR IGNORE 会保留同 ID 旧行，不能完成替换。应明确可兼容读取、拒绝提示或受控迁移；已完结课堂不得索引越界或重复提交学习记录。
5. **不重新扩大已接受范围。** 13-08-PLAN.md:242 已明确接受 R0 掉线丢失内存 outcome、完整恢复归 Phase 15。本评审不把该取舍重新列为阻塞项；只要求对计划已承诺的行为如实报告。
6. **文档过期成立，转向决策保持优先。** STATE.md:90 已明确取代固定三词、LLM 控制节奏及沉默不跳词的旧要求。修订 CONTEXT/REQUIREMENTS/PROJECT 时应同步该决策，不能借文档冲突恢复旧设计。第一周/第六周时间检查也保持各自口径，不能把第一周成环误写成第一周完成所有延迟达标测量。

### 后续规划输入

优先修订 13-08/13-09/13-11 的播放结束协议、计时器失效规则与幂等分支，再补暂停状态和 FSRS 写入核对。保留 13-12/13-13 真机验收安排，自动化与 mock 只作组件证据。调用 /gsd-plan-phase 13 --reviews 吸收本报告；本次只更新评审报告，没有修改计划或业务代码，也没有运行测试、部署或真机验收。
