# ADR-0006：课堂按需例句的触发、上下文来源与失败语义

**Status:** Accepted（Issue #9 实施前拍板）
**Date:** 2026-10-15
**Deciders:** 项目用户（规格 US-16～US-20 与父规格实施决策已确认）；开发者执行技术设计

## Context

父规格（Issue #1）已确认：例句在孩子需要时依据教材版本、年级／学期、当前已学内容按需生成；导入与例句解耦（US-16 无需填例句即可导入）；句子要用孩子能理解的词汇和句式（US-18）；以具体教材和已学内容适配水平，地区与年级只是背景信息（US-19），首次使用不要求能力测评（US-20）。Issue #9 要求课堂把这个链路落地。ADR-0005 第 54 行已预约：本任务复用文本生成 seam 与「教材背景纯函数」形态，但不共享 #8 的端点——例句按需单次生成，语境含课堂当刻状态。

现状（2026-10-15 核实）：

- 课堂意图词表为八个听写值＋三个检查值（`pkg/rt/dictation.go` intentWhitelist）；`meaning`（问词义）意图的 reply 由**分类调用一次产出**，规则是「只能依据已给出的释义与例句组织」，两者都没有时固定话术（`buildClassifySystemPrompt`）。没有「求例句」意图，没有任何例句生成。
- 词条快照 `lesson.Word{Word, Meaning, Example}` 随课堂行留档；`cards.example` 列一直存在、由 Python 唯一写入（`cmd_add`/`cmd_edit`），导入不要求填写，通常为空。
- 课堂内对 LLM 的唯一调用是分类（`classifyOncePayload`，`tm.llm` = `rt.LLMProvider`，同 `mimo.Client.Chat`）；#8 的家长端生成走独立 `ocr.ChatReader` seam＋纯函数提示词（`pkg/ocr/meaning.go`），课堂侧无对应物。
- 教材（版本/学段/年级/册/模块、课次名单、`learned_lessons`）与归属（`card_assignments`）Python 写、Go 读；`database.TextbookRepository`/`CardRepository` 已能查全部归属与按学科列卡；课堂 `TurnManagerConfig` 尚无教材 seam。
- 打断／过期机制现成：轮次 `ctx`＋`epoch`（分类后 `epochCurrent` 校验、`speakTurn` 逐块校验）、`TurnTarget` 词绑定、`replyDisplay`→`maskTranscript` 学生端屏幕掩码（语音照读，D-10）。

## Decision

1. **触发是听写阶段的两个语音意图：`meaning`（问词义）与新增 `example`（求例句／怎么用）。** 词表从八值变九值（`intentWhitelist` 十二值）。分类仍是一次调用、`classifyMaxTokens` 不变；`meaning` 的 reply 契约原文不动，`example` 新增对称契约：只能原样使用已给出的例句，没有例句时固定话术「老师这边还没有这个例句，你先凭印象写吧」。分类 reply 在这两个意图上降级为**兜底话术**——服务端在 dispatch 阶段决定最终回复。
2. **按需生成是 dispatch 阶段的第二次、也是最后一次 LLM 调用。** 仅当 `target.WordIndex >= 0`（听写阶段——检查阶段经 `checkDispatch` 以 `WordIndex=-1` 落入同一 case，天然保持现状：检查阶段不生成，例句请求按 chat 处理，与 hint 同规）且快照满足：`Meaning != ""`（有已确认释义可依）且 `Example == ""`（无教材例句可引）时，用课堂轮次 `ctx` 调 `tm.llm.Chat`（`WithThinkingDisabled`、`exampleMaxTokens=300`，**不重试**），产出完整说给学生听的回复（先简释词义再给一个例句／或直接给例句）。生成前后各校验一次 `epochCurrent`：过期静默中止，绝不让迟到回复说话（沿用 processTurn 分类后的中止纪律）。成功后 `runAnnouncementDisplay(reply, replyDisplay(reply), purposeChat, armWordTimer)`——练习位置从未移动，`onAck=armWordTimer` 即「继续原来的练习」。
3. **生成上下文 = 快照已确认释义 ＋ 教材背景纯函数 ＋ 已学词汇样本，无地区。** 教材背景由新 seam `TurnManagerConfig.Examples` 提供（`ExampleContext` 结构＋`ExampleResolver` 具体实现，窄接口只依赖 `CardRepository.List` 与 `TextbookRepository.AssignmentsByCardIDs/Get`）：
   - **词条解析**：按词面在 `category='english'` 的卡内精确匹配（`UNIQUE(word, category)` 保证唯一）。找不到卡（语文词、快照后删词）→ **不生成**，说兜底话术——这是英语课堂的边界门，#10～#13 的语文内容结构性排除，不靠提示词自律。
   - **教材挑选**：该卡的归属里，优先「课次在已学范围内」的教材（多本时主教材优先，再按 ID 稳定排序）；没有已学归属时取任一归属的教材元信息（加入范围的词仍在教材语境里）；无归属（补充词汇）→ 如实按「课外补充词汇，没有教材归属」生成，仍带已学词汇样本。
   - **已学词汇样本**：`List(ParticipatingOnly+ScopeEligibleOnly, category=english)` 的词面（排除当前词、上限 60 个）。口径说明：这是「练习资格通过」集合而非严格「已学课次词」——它与选题同口径（孩子实际在练的词）、单查询可得；严格已学集没有现成查询，本任务不新造。提示词措辞为「例句优先只用这些词和最基础的常见词」。
   - 提示词由纯函数构建（persona 与儿童安全措辞从分类提示词原样搬运），单元测试钉死：携带版本/学段/年级/册（未登记项省略）、已学进度（0 课如实说「暂无已学课次」）、已学词汇列表；**没有任何地区字段**（US-19）。
4. **优先级：教材例句原样引用，生成只补缺，永不替换教材原文。** `Example != ""` 时直接说分类 reply（其中已含教材例句），不调生成；`Meaning == ""` 时不生成、说固定话术（缺可靠释义不凭空断定词义）。生成失败（调用错误或回复不可解析／超长）→ 说分类兜底 reply，不伪称造出了句子。生成例句**不落库**：Python 是 cards 唯一写入方，生成句不冒充教材例句，重复求助按需再生成。
5. **宽松解析沿用 #8 形态但不设裸文本兜底。** 剥代码围栏→最外层 JSON 对象→`reply`（兼容 `sentence`）非空且 ≤ `maxExampleReplyRunes`（400）可用；不可用即失败走兜底。`speakTurn` 的 500 上限仍作最后截断保险。
6. **观测事件 `example_served`**（timing.go 词表新增）：字段 `word_index`＋`result`（`stored`＝引教材例句／`generated`／`fallback`＝生成失败或降级／`no_meaning`＝无可靠释义），count/label 类字段，不进任何词面或文本（T-13-06b 边界）。

## Options Considered

### A：导入或课堂开始预生成例句——否决

验收第 1 条明令禁止；历史教训是整批逐词合成曾把会话就绪拖到 23.9 秒；1C1G 内存不允许囤积。

### B：把例句塞进分类调用一次产出——否决

`classifyMaxTokens=200` 装不下讲解＋例句；分类失败与生成失败语义耦合（分类失败今天有确定性兜底，混入生成会把兜底也弄丢）；已存例句与生成例句落在同一契约里，「不替换教材原文」只能靠提示词自律。

### C：生成例句写回 `cards.example`——否决

违反写分离契约（Python 唯一写入方）；生成句无法与教材原文区分来源（#8 的来源标记精神）；「不替换教材原文」验收直接冲突。

### D：课堂为当堂词批量预取——否决

同 A：阻塞课堂就绪，且多数课堂从不求助这些词。

### E：检查阶段（check）也生成——暂缓

默写已结束、无「练习位置要保持」的语义；检查阶段词表 prompt 已按存储信息诚实组织。例句请求在检查阶段按 chat 处理（与 hint 同规）。如需要另立任务。

### F：加 UI 按钮（类似 read_word）——否决（本任务）

read_word 是机械重读，按钮是刚需；例句/解释求助是自然语言请求，语音通道是产品主路径（规格与验收都以「请求」行文）。无对应验收故事，不扩面。

## Consequences

- 协议零变更：无新消息类型、无前端改动、无迁移、无 Python 改动；旧课堂行（无新字段）不受影响——本决策不动词条快照结构。
- 意图词表九值（＋检查三值）需同步更新分类提示词、白名单与既有钉住「八值/ELEVEN 值」的测试。
- 例句贴合度与难度、真机试听属真实服务人工验收项（父规格补充测试边界），与 #8 同规，不在自动回归内冒充；自动回归覆盖触发、上下文、降级与继续练习。
- 分类 reply 在 meaning/example 意图上从「最终回复」降为「兜底回复」，对既有 meaning 行为的可见变化仅是「有确认释义且无例句时多一句生成的例句」。
- 「已学词汇」样本口径（练习资格集合）与严格「已学课次词」的差异：补充词汇会进入样本。若日后需要严格口径，需新查询与本 ADR 增补。
