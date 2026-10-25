评审覆盖 **6/6 个计划**。源码核验未完成：PowerShell 与备用 Node 工具均因 `windows sandbox failed: helper_unknown_error` 无法启动。

因此，下文区分两类结论：**计划文本中可直接确认的矛盾**，以及**需要读取源码验证的开放问题**。所有 `file:line` 均来自你提供的 Research，仅作为待核位置，不代表本次已经读取或验证；这份评审不能替代源码核验通过结论。

## 13.3-01

**Summary**

两阶段回执与四类提交终点的拆分符合需求，但“真实保存结果”“回执故障不影响提交”“每次尝试都有记录”之间尚有未处理的冲突。建议先明确故障与不确定结果的语义，再实现回执 API。

**Strengths**

- 计划明确在下游写入前记录 `pending`，返回后回填结果，并禁止回执驱动 FSRS 重试，能够表达跨进程写入的不确定性。待核挂载点：`pkg/rt/check.go:600`、`pkg/rt/check.go:812`、`pkg/rt/check.go:892`。
- 加练回执独立表达 `practice_logs` 存储结果，避免冒充 FSRS 成功。待核位置：`pkg/rt/practice.go:392`、`pkg/lesson/practice_store.go:84`。

**Concerns**

- **HIGH｜计划合同冲突：** Task 2 允许 `Begin` 失败后继续 FSRS 写入。此时进程退出后没有 `pending` 行，只能返回 `unknown`，无法满足“每次真实尝试恰有一行”和崩溃后必显示“待核实”的无条件承诺。
- **HIGH｜计划中的结果推断不诚实：** `err != nil` 时直接填 `updated=nil, missing=requested`，把“没有拿到确定结果”当成“全部未写入”。跨进程调用报错时，下游可能已经提交。需要核验错误分类和返回结果是否保留部分写入信息：`pkg/rt/mimo_adapters.go:216`、`third_party/python/vocab_manager.py:421`。
- **MEDIUM｜开放问题：** DDL 没有唯一约束，`Begin` 每次都插入；“恰一行”完全依赖既有提交守卫。须验证守卫是否覆盖并发连接、重连和修正提交，而不仅是同一 TurnManager 内重复消息。待核：`pkg/lesson/executor.go:592`。
- **MEDIUM｜验证缺口：** 现有 behavior 没有明确覆盖“下游已经成功、`Complete` 未发生、数据库重新打开后仍为 `pending`”；也没有验证回执查询延迟。错误只记录事件，并不能证明同步数据库调用不延缓主提交。待核：`pkg/database/db.go:71`。

**Suggestions**

- 明确区分确定成功、确定失败和结果不确定；不能确定未写入时，不生成“全部 Missing”的结论。
- 为 `Begin` 失败后的新课规定诚实展示文案，并修订无条件回执保证；不要把此类 `unknown` 标成“历史记录”。
- 增加重复 `Begin`、并发提交、部分结果、数据库重新打开和受控崩溃边界测试。若增加唯一约束，先明确键的作用域，避免误合并不同尝试。

**Risk Assessment**

**HIGH（暂定）**：主要风险是保存事实记录失真，直接影响 SC-1 与后续家长展示。

## 13.3-02

**Summary**

服务端投影、Go 直读和有界查询方向正确，但 API 数据合同尚未闭合：分页只有 `LIMIT`，整课保存状态实际取最新尝试，进行中响应字段要求也相互冲突。

**Strengths**

- 计划把隐藏词面放在服务端序列化层，并单列 `checking` 测试，避免依赖前端隐藏。待核状态分支：`pkg/rt/turn.go:532`；既有学生投影先例：`pkg/rt/turn.go:675`。
- 查询使用显式列、参数占位符和服务端上限，符合共享单连接数据库的约束。待核：`pkg/database/db.go:71`、`pkg/lesson/practice_store.go:138`。

**Concerns**

- **HIGH｜计划合同冲突：** `LatestByLessonIDs` 返回最新一条回执，却被用作整课 `save_status`。早先批次部分保存或待核实、后续只修正一个词成功时，列表可能显示整课“已保存”。这是最新尝试状态，不能直接代表整课状态。
- **MEDIUM｜分页未实现：** 只有 `limit`，没有游标、偏移或下一页信息；超过 50 课后无法读取更早记录。它满足“有界列表”，尚不满足 SC-2 的“有界分页”。
- **MEDIUM｜响应合同冲突：** Task 2 behavior 要求进行中行只有 `lesson_id/created_at/word_count/stage/kind/completed`；action 又要求输出 `unchecked/save_status/correct/wrong/unconfirmed`。实现者无法同时满足两者。
- **MEDIUM｜开放问题：** 判定计数与逐词结果需要核验实际 `CheckVerdict`、`WordOutcome` 的结构及关联方式，尤其是重复词、缺失判定、历史字段和加练数据。待核：`pkg/lesson/store.go:102`、`pkg/lesson/store.go:112`。

**Suggestions**

- 区分“最近一次保存尝试”与“整课保存摘要”；保留部分保存和未解决尝试的信息。无需引入 Phase 15 对账。
- 补一个有界分页合同，固定排序及下一页参数。
- 为进行中、普通完结、未核对、加练、历史课分别写固定 JSON 示例，统一字段存在性、空数组和判定语义。
- 增加 executor spy，断言成果 GET 不启动 Python；增加读前后数据快照测试，验证只读行为。

**Risk Assessment**

**HIGH（暂定）**：当前保存聚合方式可能产生错误成功提示；其余主要是接口合同和边界覆盖问题。

## 13.3-03

**Summary**

三 tab 与原生 JS 组件适合现有范围，但确定性测试数据不足以支撑“四态、部分保存、未知判定全部覆盖”的结论，且加练结果与界面判定规则不一致。

**Strengths**

- 计划要求保留既有 DOM id、组件挂载顺序和词库测试，能够减少 tab 改造的回归范围。待核：`pkg/web/static/parent.html:63`。
- 使用 `createElement/textContent` 渲染用户词库数据，并保持学习记录只读，方向合理。待核既有 DOM 纪律：`pkg/web/static/js/parent-app.js:124`。

**Concerns**

- **HIGH｜跨计划合同缺口：** 02 的加练 `results` 只有 `word/correct/hints/rating`，03 却依赖 `manual` 区分“对错”与“未确认”。没有明确的加练判定规则，容易把真实加练结果显示为未知，或错误地视作人工核对。
- **MEDIUM｜测试覆盖不足：** 四个 mock 课次明确覆盖 `saved/pending`，没有规定 `failed/unknown` 数据；也没有包含“部分保存”和“对、错、未确认同时存在”的课次。四态展示和未知词统计并未得到完整验证。
- **MEDIUM｜计划机制不足：** 更新 `?v=` 和 HTML `no-cache`，不能保证 Safari 从 bfcache 恢复后重新读取成果。计划没有明确 `pageshow` 或重新进入 tab 时的刷新行为。待核：`pkg/infra/static.go:92`。
- **MEDIUM｜开放问题：** 隐藏 panel 的外层切换是否与既有 `settingsSection.hidden` 控制冲突，需要读取原挂载及显示逻辑验证；不能仅凭“已有隐藏挂载先例”认定安全。待核：`pkg/web/static/parent.html:63`。

**Suggestions**

- 显式定义加练判定来源；前端按 `kind` 处理，或让 API 输出统一的判定字段。
- 补齐 `failed`、历史 `unknown`、部分保存、混合未确认及 XSS 文本 fixture。
- 明确返回页面和进入学习记录 tab 时的刷新规则，并测试数据变化后重新进入页面。
- 增加真实 API 数据驱动的成果展示测试；现有词库回归不能证明 LessonsView 与实际响应合同一致。

**Risk Assessment**

**MEDIUM（暂定）**：范围受控，但存在用户可见的结果误读和测试盲区。

## 13.3-04

**Summary**

纯静态首页和终结态导航实现范围合理；主要问题是“原文件不变”的证明方式无效，以及音频释放时序测试缺少可控制的等待条件。

**Strengths**

- 首页仅新增独立静态文件，不迁移 `/`，符合范围约束。待核静态注册位置：`cmd/service/main.go:693`。
- 把导航追加安排在资源释放之后，方向符合 SC-5。待核终结处理：`pkg/web/static/tutor/index.html:641`；横幅构造：`pkg/web/static/tutor/index.html:251`。

**Concerns**

- **MEDIUM｜不变断言无效：** `git status --porcelain` 为空只能说明当前工作树干净。修改文件后提交也会通过，因此不能证明相对阶段起点字节不变；已有用户修改又可能使测试无关失败。
- **MEDIUM｜时序测试可能不稳定：** “看到结束横幅后立即断言链接不可见”依赖测试执行速度。若 drain 很快完成，正确实现也会失败。需要 mock 明确保持音频未完成，再受控释放。
- **MEDIUM｜开放问题：** WebSocket 关闭和 capture stream 为 `null`，并不足以单独证明播放队列已清空、播放器资源已释放。必须核验实际 drain Promise 的完成条件。待核：`pkg/web/static/tutor/index.html:641`。
- **MEDIUM｜验收口径变化：** `playback_ack_timeout` 的“总结播完（尽力而为）”放宽了 SC-5 字面条件。若 checkpoint 接受这一例外，应同步修改验收合同，且异常路径不能显示总结已成功播完。

**Suggestions**

- 在阶段开始时记录两个保留文件的字节哈希，结束后比较；将仓库不变检查放在阶段验证中。
- 给 mock 增加明确的 drain 等待与释放条件，分别验证释放前后状态。
- 必测 `lesson_complete/practice_complete/no_due_words`、用户主动结束及连续两课；不要把缺少加练 mock 当作跳过该分支的理由。
- 检查链接去重、旧链接清理及新课所有相关状态复位。

**Risk Assessment**

**MEDIUM（暂定）**：实现较小，但当前测试可能既误报，又遗漏音频未真正结束的情况。

## 13.3-05

**Summary**

配置链和服务端单点裁决清晰，但计划主要交付“可配置强制模式”，家庭正式入口实际启用该配置仍只留在 UAT 描述中。

**Strengths**

- 强制判断发生在服务端 mode 赋值处，客户端 URL 无法绕过已启用的开关。待核：`pkg/rt/turn.go:484`、`pkg/rt/turn.go:568`。
- 同时验证强制模式和开发兼容分支，且明确不把该键当成权限机制，符合已定范围。待核配置注入：`cmd/service/main.go:598`。

**Concerns**

- **MEDIUM｜交付步骤缺口：** SC-6 要求生产配置强制 student，但 Task 1/2 没有明确修改本地 `config.local.yaml`；“建议 true”和 UAT 要求用户设置，不能证明执行结束时已生效。
- **MEDIUM｜开放问题：** 需要核验 `run.ps1 -Https` 实际选择哪个配置、配置覆盖优先级，以及 main.go 是否完整透传。仅测试结构体零值和 mapstructure 解析，不足以验证家庭入口。待核：`cmd/service/main.go:598`。
- **LOW｜承诺范围需明确：** `forceStudentMode=false` 允许任何连接到该服务的客户端请求 test，并不自行实现“仅本机开发放行”。应明确这是开发配置例外，还是还需要既有运行配置保证其使用范围。

**Suggestions**

- 将本地配置更新及服务重启后的核验列为明确执行步骤；只改目标键，不输出或提交其他配置内容。
- 补一次实际配置加载链测试，验证正式配置对 `session_init(mode="test")` 返回学生投影。
- 在文档中写清正式配置与开发配置的使用条件，无需增加鉴权。

**Risk Assessment**

**MEDIUM（暂定）**：代码改动风险较低，配置落地遗漏会直接导致 SC-6 未达成。

## 13.3-06

**Summary**

验收资产覆盖了家庭旅程，但崩溃模拟步骤不能制造目标状态；回归失败与任务完成的判定也存在冲突。当前计划适合交付“待人工验收”的准备状态，不能作为阶段完成证明。

**Strengths**

- 将完整旅程、关闭后重开、加练、只读检查和 Safari 返回行为纳入 UAT，符合产品目标。持久数据待核位置：`pkg/lesson/store.go:42`、`pkg/lesson/practice_store.go:26`。
- 明确保留失败记录、不以自动化代替人工验收，能避免把测试通过扩大解释为产品通过。FSRS 写入待核：`third_party/python/vocab_manager.py:421`。

**Concerns**

- **HIGH｜崩溃步骤不成立：** “默写检查中 kill 服务→重启→显示待核实”不能保证产生回执。按 01 的计划，`Begin` 在保存调用之前发生；尚未提交时杀进程，可能根本没有回执，直接杀进程也不能假定正常关闭兜底会执行。待核兜底位置：`pkg/rt/check.go:892`。
- **MEDIUM｜验证对象被替换：** 用 mock 文案抽查替代真实崩溃窗口，只验证“pending 会显示待核实”，没有证明“FSRS 已写完但回执未完成时留下 pending”。
- **MEDIUM｜完成条件冲突：** must-have 要求四件套全绿，Task 2 的 done 又接受“红项已登记”。登记失败可以完成结果记录，不能完成全绿阶段门槛。也不应仅凭重跑通过就确认“环境性 flake”。
- **MEDIUM｜证据强度不足：** `review_logs` 行数不变只能证明没有新增记录，不能排除修改既有记录或 cards。核查期间若有后台保存，也会混入无关变化。待核表结构：`third_party/python/vocab_manager.py:161`。

**Suggestions**

- 用测试专用受控停止点制造边界：确认 `Begin` 已落库、下游已成功，再阻止 `Complete`；重启后核查持久 `pending`。不需要加入生产故障接口或对账能力。
- 分开记录“回归结果已记录”“自动化通过”“真机人工通过”；任何失败或未执行项都阻止阶段验收完成。
- 在课堂写入结束后执行只读核查，结合数据快照与 API executor spy 提供证据。
- 在 14 行任务表中如实区分人工确认、自动化结果和待执行项；文档关键词检查仅用于检查文件存在性。

**Risk Assessment**

**HIGH（暂定）**：当前崩溃步骤可能给出错误验收预期，完成条件也可能让未通过状态被误记为完成。

## 跨计划结论

实施前建议优先修订四项：

1. **保存语义：** 不把调用错误推断为全部未写入，也不把最新一次成功推断为整课全部保存。
2. **API 合同：** 补实际分页，统一进行中字段及加练判定形状。
3. **验证机制：** 用受控停止点验证持久崩溃状态，用受控 drain 验证导航时机，用阶段起点哈希证明文件不变。
4. **验收门槛：** 明确正式配置实际启用，以及自动化、人工验收分别通过后才能完成阶段。

总体风险暂定 **HIGH**，集中在保存事实和验收证据的可信度。六份计划的文本评审已完成；源码核验覆盖 **0/6**，上述待核问题仍需读取实际实现后确认。
