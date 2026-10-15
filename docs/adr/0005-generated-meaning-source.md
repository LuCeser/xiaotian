# ADR-0005：缺失英语释义的补充位置、来源标记与失败语义

**Status:** Accepted（Issue #8 实施前拍板）
**Date:** 2026-10-14
**Deciders:** 项目用户（规格 US-6/US-15/US-19/US-20 与父规格实施决策已确认）；开发者执行技术设计

## Context

父规格（Issue #1）已确认：英语释义优先使用材料原文，缺失时自动补充并标记来源；系统识别和补充的信息应可区分，自动补充英语释义不能冒充教材原文；生成依据是具体教材和已学内容，不以地区或年级推定水平，首次使用不要求能力测评。Issue #8（缺失英语释义按教材语境补充并标明来源）要求落地这条链路，生成服务选型与来源存储规则留给本任务的技术决策。

现状（2026-10-14 核实）：

- 识别管线 `pkg/ocr/wordlist.go` 的 system prompt 已约定「词表没有给释义就留空」，`WordlistEntry.Meaning` 合法为空；预览分类器 `classifyPhotoEntries`（parent-photo.js:130）对空释义没有专门状态——空释义行是普通可提交的 'new' 行。
- `cards` 表只有单值 `meaning`（vocab_manager.py:150-161），无来源维度；ADR-0003 已定单释义＋导入时显式确认，课堂选题与解释读 `cards.meaning`（pkg/rt/dictation.go buildClassifySystemPrompt）。
- 识别是 Go→mimo 的无状态视觉调用（`POST /api/v1/words/photo/recognize`，ListReader seam）；文本 LLM seam `mimo.Client.Chat` 已有现成消费者（语音意图分类、rt）。Python 是 cards 的唯一写入方（写分离契约），`cmd_add` 以 list-mode JSON 收词条。
- 已确认的教材上下文在预览层现成可得：教材面板的书目列表含 edition/stage/grade/volume/module/learnedLessons（GET /api/v1/textbooks），新建教材路径读 mini-form 的确认值。

## Decision

1. **补充是预览层的独立无状态端点，不并入识别。** 新增 `POST /api/v1/words/meaning/generate`：入参 `{words[], textbook{edition,stage,grade,volume,module}, learnedLessons|null}`，出参 `{meanings:{word→meaning}, missing:[word]}`。识别草稿在客户端（ADR-0004 语义），补充同样只改草稿、不写库；提交仍走既有 POST /words。独立的理由：(a) 材料原文优先是结构性保证——识别结果原样落行，补充只填空释义行，生成无从覆盖原文；(b) PDF 跨页合并去重后才存在确定的缺失集合，逐页生成会重复调用；(c) 生成失败的重试不牵动重新识别。
2. **生成上下文 = 已确认教材元信息＋已学进度刻度，无地区。** 提示词由纯函数构建：书目确认值（版本/学段/年级/册/模块，未登记项如实省略）＋已学进度（既有书取 learned_lessons，新建书「尚未登记学习进度」——不伪装成已学 0 课）。不引入地区、能力测评等任何字段（US-19/US-20）。单元测试钉死提示词携带上述背景。
3. **来源 = `cards.meaning_source` 单列，值域 {'material','generated'}，默认 'material'。** 迁移走 PRAGMA 探测 ALTER（fetch_locked_until/participates 先例）；写入链路 API AddRequest → pybridge.AddEntry → list-mode JSON → cmd_add INSERT 同步加字段。两条规则：(a) 编辑不改来源——家长修改生成释义仍是补充来源（不冒充教材原文），修改原文释义仍是原文来源；取消（清空）后按空释义存 'material'，空释义永不做标记；(b) 标记展示规则 = meaning 非空且 source='generated'（预览行徽标「系统补充」、词库行徽标、课堂解释不带标记——课堂读最终确认释义，来源区别是家长侧信息）。Go 读侧沿 cardColumns 探测列、缺失时降级读 'material'（participates 先例）。不引入第三值 'parent'（无验收故事；「不冒充」由规则 (a) 满足）。
4. **触发与范围。** 预览层家长显式按钮「补充缺失释义（N 词）」，N = 将走新增路径且释义为空的行（status 'new'、meaning 空、勾选）；失败后按钮变「重试生成（N 词）」，只重发仍缺失的行。exists/conflict 行永不生成（词库释义即课堂用义，#6 的确认语义不动）；「不指定教材」路径不提供生成（无教材语境可依，不凭年级猜水平）；语文内容类型（#10-#13 的词语/古诗/课文）结构性排除——本端点属英语词表导入域，提示词限定只给英语单词/短语的简明中文释义，不生成古诗/课文原文填空。
5. **失败诚实。** 调用级错误走既有 envelope：`generation_unavailable`（503，未配 key）、`generation_failed`（502，外部调用失败或回复不可解析，可重试）、`invalid_request`（400，words 空/超限）。词级缺失不是 HTTP 错误：请求的词不在回复里、释义为空或超长 → missing，行内显示「释义生成失败，可重试或保留缺失」，保留缺失的行照常提交（空释义、无标记）——回执沿用四类分桶，不伪称生成成功。
6. **批量与上限。** 端点每次至多 100 词（对齐 add 路径 maxEntries），释义长度上限对齐 maxMeaningRunes（1000 字符，超长按缺失处理）；客户端按 100/批复用 chunkBatch 顺序分片，聚合各片真实结果。

## Options Considered

### A：并入识别调用一次生成——否决

识别与生成失败语义耦合（识别成功而生成失败无法单独重试）；PDF 逐页识别会带着跨页重复词反复生成；识别回复与生成回复混在一个契约里，「原文优先」只能靠提示词自律而非结构保证。

### B：每词一次生成调用——否决

调用放大 100 倍于 1C1G 部署与外部服务配额；词与词共享同一教材背景，无逐词差异化收益。

### C：来源存进行（card_assignments）或新表——否决

来源是词条属性不是归属属性（同一词条在任何教材语境下，其释义来源唯一）；ADR-0003 已定单释义锚点，按来源存释义的变体彼时已否决，本决策不翻案。

### D：'parent' 第三值标记家长改写——否决

无对应验收故事；「补充不冒充原文」只要求区分 material/generated，编辑不改变来源性质即可满足，第三值徒增词库视图的标记负担。

### E：对「不指定教材」的补充词汇也提供生成——否决

无教材语境可依时生成只能凭词面或年级猜水平，与 US-19 相悖；该路径家长仍可手填释义（既有编辑能力）。

## Consequences

- 既有库零破坏迁移：老库由 Python 首次命令补列，Go 侧降级读取；释义来源对既有词条一律 'material'（历史数据本就来自材料或家长手填）。
- e2e 桩需要文本生成夹具约定：mimo-stub 现对无图 chat 一律回通用文本，需新增按提示词子串匹配的 `text-*.reply.json` 夹具（含 failcount 变体），未匹配的文本调用维持通用回复（课堂偶发流量不受影响）。
- 生成释义的正确性与难度验收属真实服务人工项（父规格补充测试边界），与 iPad 实机项同列，不在自动回归内冒充。
- Issue #9（课堂按进度生成例句）可复用本任务的文本生成 seam 与「教材背景纯函数」形态，但不共享端点（例句按需单次生成，语境含课堂当刻状态）。
- 未来若出现「同一词多教材多释义并存」需求，仍需新 ADR（ADR-0003 的既有边界）。
