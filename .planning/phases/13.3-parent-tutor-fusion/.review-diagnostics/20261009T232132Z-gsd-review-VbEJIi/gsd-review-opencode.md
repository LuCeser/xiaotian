I'll verify the plans against the actual repository source before writing the review. Starting with the core backend files the plans mount receipts onto.
Core mounting points verified. Now reading the turn manager, API handlers, main wiring, and database config.
Now verifying the main wiring, database pool, and config chain.
Backend wiring verified. Now checking frontend files, tests, and config.
Frontend baseline verified (new files don't exist yet, as plans assume). Now the tutor page ending-state machinery and mock server.
Mock server confirmed: HTTP side has no /api/v1 routes (only `/__test/state` + static). Now verifying config chain, e2e assertion precedents, and remaining assets.
config.example.yaml notably has no `rt:` section — relevant for plan 13.3-05. Checking the remaining referenced assets.
所有源码核对已完成。以下是基于仓库实际代码逐项验证后的评审。

---

# Phase 13.3 计划评审（Cross-AI Plan Review）

**核对方式说明**：本评审对 6 份计划的全部关键机制断言做了 Read 级源码核对（check.go / practice.go / turn.go / store.go / practice_store.go / handlers.go / main.go / db.go / config.go / parent.html / parent-app.js / tutor/index.html / mock-rt-server.js / 各 spec 与配置文件），未发现计划引用的行号有实质性偏差；发现 1 处文件路径错误、1 个被计划继承的存量语义缺口、若干时序与工程细节问题，详见各节。

---

## 13.3-01

**Summary**：这是本阶段语义正确性最关键的计划。四个挂载点、幂等键、两阶段写、旁路纪律全部与源码逐一吻合；tracer 策略（先打通正常检查端到端再补三个终点）与既有 TDD 纪律一致。主要风险不在工程构造，而在一个被计划静默继承的存量边界：practice 课在检查阶段中断连接时，`uncheckedCloseFallback` 会走 FSRS 写而非 practice_logs 写，回执 kind 语义随之错位。

**Strengths**：
- 四个提交终点全部核实存在且形态与计划一致：`finishCheckSubmit`（check.go:600，SubmitReview 调用在 :617，`len(ratings)>0` 分支在 :616）、`finishCorrections`（check.go:812，`correctionInputs.requestID` 已在 :799-806 由 m.RequestID 填入）、`uncheckedCloseFallback`（check.go:892，幂等键 `lessonID+"-complete-check"` 在 :907）、`finishPracticeSubmit`（practice.go:392，goroutine 调用点 :384 处 `m.RequestID` 可用）——签名加 `requestID` 参数的改法可行。
- 「幂等路径零回执」的断言经源码追踪成立：reviewSeen 重复分支在 check.go:443-459 早退、CompleteCheck 的 Idempotent 分支在 check.go:549-574 早退、practice 在 practice.go:366-377 早退、correction latch 守卫在 check.go:720-744 早退；`uncheckedCloseFallback` 的挂载点（Idempotent return :921-925 之后、ratings 组装 :929 之前）选取精确。
- 迁移挂点真实：`lesson.Migrate` 的 statements 切片在 store.go:135-141，照 `practiceLogsDDL`（practice_store.go:26-35）加两条 DDL 的形态成立；main.go:310 practiceStore 构造区与 main.go:598-618 rtTurnCfg 注入行的「邻位」存在。
- 空批次不 Begin 的规则与四个终点的既有 `len>0` 分支逐一对齐（check.go:616/818/931、practice.go:436）；「没有尝试就没有回执」可直接复用现有分支结构。
- Task 1 checkpoint 把 RESEARCH 的 4 个 Open Questions 收口为阻塞式人工决策并回写标记，防止后续 wave 读到未定案口径——这是对 A1/A2/A5/A7 风险的正面处置。
- 两阶段写刻意不用事务、崩溃窗口两种情形呈现同一 pending 状态，语义设计诚实且与 S-05（不盲目重写）一致。

**Concerns**：
- **MEDIUM（继承的存量缺口）**：`uncheckedCloseFallback` 没有 practice 守卫——practice 课在检查阶段断开时，该函数（check.go:892-948）会对 practice 词执行 `SubmitReview`（FSRS 写，:932）而非 `RecordPractice`，违反 T-13.1-11 的 FSRS 隔离。本计划在该终点无条件挂 `kind=unchecked_fallback` 回执后，这类课在家长端（13.3-02/03）会显示为 r0x- 加练课却携带 FSRS 语义的兜底回执，且 practice_logs 为空。这是 13.1 的存量问题，但本计划是第一次把它变成可见数据，不应静默继承。
- **LOW**：回执 Begin/Complete 使用提交流程的 turn ctx（finishCheckSubmit 的 ctx 即 handleCheckSubmit 传入的可取消 ctx）。session 提前关闭导致 ctx 取消时，即使 SubmitReview 已成功返回，Complete 的 UPDATE 也可能因 ctx 取消而失败、回执停留 pending。pending＝「待核实」在语义上仍是保守诚实的，但窗口可以更窄。
- **LOW**：`err != nil` 分支回填 `missing=requested` 断言「全部未写入」——但 pybridge 存在 ErrPythonJSONParse（子进程 commit 成功后 Go 侧解析失败）的可能，此时 FSRS 实际已写而回执报 failed+全 missing。属于跨进程边界的固有残差，Phase 15 对账兜底，但计划未言明。
- **LOW**：correction 全同意（空 diff）时 `ClearUnchecked`（check.go:844）是一次真实持久化动作（清除 unchecked 标记）却不产生回执，家长端只能靠 unchecked 标志消失间接感知。可接受但未在计划中说明。

**Suggestions**：
- 在 Task 3 中给 `uncheckedCloseFallback` 增加 practice 分支决策：要么修存量缺口（practice 课兜底改记 `kind=practice` 回执＋RecordPractice，或至少不写 FSRS），要么在 must_haves 中显式登记「practice＋兜底路径的既有行为按原样记录回执」并在 SUMMARY 标注为已知偏差——不应让执行者无意识通过。
- Complete 建议用 `context.Background()` 加短 timeout，把「结果已知但回执没写上」的窗口与 turn 取消解耦。
- Test 6（旁路断言）建议同时断言回执写失败时**不产生**第二条回执（Begin 失败后 Complete 不再执行），钉死旁路的完整形态。

**Risk Assessment**：**MEDIUM**。机制映射几乎无误差、测试设计扎实；风险集中于 practice×兜底路径的语义缺口（存量 bug 首次显性化）与跨进程边界的固有残差，均不阻塞主线但需在执行前拍板处置口径。

---

## 13.3-02

**Summary**：投影白名单、有界分页、回执聚合的设计与 ROADMAP 成功标准 2 逐条对应，且把完结判定的唯一权威正确锚定在 turn.go:532-553 分支表上。NewHandler 签名波及面（main.go:679 唯一生产调用点＋handlers_test.go 901 行）识别准确。整体是一份低想象力但高确定性的计划，泄题防线的表驱动＋httptest 负向断言双层证明是正确做法。

**Strengths**：
- 完结分支表核实无误：turn.go:532-553 的五个分支（空卡片/checking/submitted+unchecked/`current_index>=len(cards)`/default）与 `LessonCompleted` 的语义（仅 `stage==submitted` 或 `stage=='' && currentIndex>=cardLen && cardLen>0` 为 true）完全对应；`stage=submitted` 且 unchecked 时判完结是正确的——check_started 已揭示词面（D-10 unmask 已发生），不属于泄题。
- 刻意导出 `LessonCompleted`（api 包跨包调用）并在 action 中写明「不得用包内小写名」——预判了唯一的编译陷阱。
- `lessonViewStudent`（turn.go:675-685）与家长接口投影规则「两套规则不要混用」的警示在 RESEARCH 与计划 read_first 中一致传承。
- NewHandler 现签名（handlers.go:65，`exec/cards/cfg/logger` 四参数）与 8 条路由（:72-79）核实；StripPrefix 挂载（main.go:678-679）确认内部 `"GET /lessons"` 即公网 `/api/v1/lessons`。
- 单连接池读纪律（db.go:71-73 `SetMaxOpenConns(1)`）的应对（仓储层硬 cap ≤50、显式列、详情单行、LatestByLessonIDs 空切片零查询）具体且可测试。
- 排序采用 `created_at DESC, lesson_id DESC` 双键（应对 SQLite datetime 秒级精度下同秒多课的并列），比 RESEARCH A4 更进一步。
- `save_status` 只来自回执表、无回执＝unknown、绝不从 stage 推导——反模式清单的核心项落进了 behavior 7。

**Concerns**：
- **LOW**：limit 校验双轨——handler 层「非整数或 <=0 → 400」与仓储层「<=0→20」并存。行为不矛盾（handler 先拦截），但两处规则可能漂移（未来有人绕过 handler 直接调仓储时语义不同）；建议仓储层规整只作为防御纵深并注释说明。
- **LOW**：`ListLessons` 为算计数 SELECT 了 `cards`＋`verdicts` 两个 JSON 列。≤10 词/课×≤50 行规模下无碍，但这是读路径最大的单次负载，若未来课程数增长需要重新评估。计划未提「若 JSON 损坏单行如何处理」——GetLesson 的容错语义（store.go:414-457）是整行报错，ListLessons 逐行 Scan 时坏行会让整个列表 500，一颗老鼠屎坏一锅。
- **LOW**：kind 判定用 `r0x-`/`r0-` 前缀字符串匹配，与 `resolvePracticeLessonID`（practice.go:115-126）的约定一致但属于契约的第二次字面复制，无共享常量。可接受（跨包），建议注释指回源头。

**Suggestions**：
- ListLessons 对单行 cards/verdicts 解析失败建议降级策略（该行 WordCount=0、判定计数为 0 并记日志，而不是整个列表失败）——家长端列表对历史脏行的鲁棒性比严格失败更有价值；若坚持严格失败，请在 behavior 中补一条坏行用例。
- behavior 2 的负向断言建议同时断言进行中课**不含** `cards`、`outcomes`、`unchecked` 之外的词面承载键（如 `words`、`results`、`verdicts`），把白名单写成键集穷举而非黑名单。

**Risk Assessment**：**LOW**。所有机制锚点核实无误，波及面处理纪律严格，测试矩阵覆盖了泄题防线的全部 stage 分支。剩余问题是防御性细节。

---

## 13.3-03

**Summary**：把 parent.html 升为三 tab 家庭工作区的改造方案与现状结构（addSection/browseSection/settingsSection、settingsSection 的 hidden 先例 parent.html:63、defer 顺序 :83-86）完全兼容；BrowseView 模式复用与 mock 桩补齐（mock-rt-server.js 确无任何 /api/v1 路由，HTTP 侧只有 `/__test/state`＋静态服务，mock-rt-server.js:380-386）均已核实可行。既有 e2e 的改造点（parent-management.spec.js 进页即断言 `#addSection` 可见，spec :48-51，tab 化后必须加前置点击）计划已正确识别。

**Strengths**：
- 与 13.3-05 同波并行的 coupling 声明属实：files_modified 零交集（本计划全在静态页/mock/e2e，05 全在 Go 配置链），无共享可变资源。
- mock 桩数据集设计出色：四课覆盖进行中/已保存/待核实/加练四种形态＋404 桩，且负向泄题断言用「桩数据独有词面字符串」检查 page content——这是前端侧防线的正确自动化形态。
- 「已核对 X 对 / Y 错 / Z 未确认」的文案与「任何位置不出现百分比」在 acceptance_criteria 中逐字可检；四态保存文案同样逐字钉死。
- tab 切换沿用 `hidden` 属性（settingsSection 先例），组件挂载时序与 defer 顺序零改动的约束写进了 acceptance_criteria（addSection 内部 DOM id 不变）。
- 加词成功提示补「按复习安排选入课堂，不一定是下一课」的落点（parent-modes.js:650 现有横幅处）核实可行。

**Concerns**：
- **MEDIUM-LOW**：mock e2e 下 parent 页初始加载即触发 `browse.refresh()` → `GET /api/v1/words` → mock 返回 404 纯文本（serveStatic 兜底），同理 `/api/v1/units`、`/api/v1/settings/fsrs` 也 404。组件会走失败分支（隐藏 panel 内不可见），不影响断言，但 (a) console 噪音可能干扰调试，(b) 若未来某个组件对 404 的降级路径有 bug，会在本 spec 里以意外方式显形。计划未提是否给这几个端点也补最小桩。
- **LOW**：Task 2 的详情视图数据合同依赖 13.3-02 落地后的响应结构体字段名，read_first 已写「以 handlers.go 实际字段为准」，但 behavior 中出现了具体字段（meaning/outcome/ocr_text 等）——若 02 执行时字段名微调，03 的断言要跟着改。波次顺序保证了这点可行，只是执行者需双向核对。
- **LOW**：「点行展开竖排详情」在列表页内的导航形态（返回列表按钮）未说明是否保留滚动位置，真机 UAT 可能报体验问题。

**Suggestions**：
- mock 桩建议顺带给 `/api/v1/units`、`/api/v1/settings/fsrs` 补空集桩（各返回 `{"units":[]}` 等），让 mock 环境下词库/设置 tab 也可无害打开——成本两行，消除 404 噪音与降级路径不确定性。
- LessonsView 的 JSDoc 契约头（计划已要求）建议同时写明「save_status 四态文案由服务端裁决、客户端只渲染」，与 T-13.3 侧的「服务端统一裁决」威胁处置互为印证。

**Risk Assessment**：**LOW**。现状结构勘察准确，既有能力保全的回归网（parent-management 全量＋tab 冒烟）完整，mock 路线与真后端路线互补。剩余为打磨项。

---

## 13.3-04

**Summary**：入口层计划整体可行——home.html「放进去即服务」（main.go:692-693 `http.FileServer` 挂 `/`＋static.go:92-100 给 .html no-cache）核实成立，session_ended 自然三理由分支（tutor/index.html:702-712）、`beginTerminalDrain`（:511-519）、`releasePlaybackResources`（:502-509）、`terminalDraining` 复位行（:791，13.1-08 Rule 1 同址）全部存在且与计划描述一致。但存在一处**文件路径错误**（tutor.css）和一处**e2e 时序断言的 flake 风险**，需在执行前修正。

**Strengths**：
- 挂载时机设计正确：`releasePlaybackResources` 只被 `beginTerminalDrain` 的 drain 完成回调调用（:516-518），`playbackResourceReleased` 单次守卫（:503）保证链接只追加一次；`pendingHomeButton` 与 terminalDraining 同址复位（:791）直接复用了 13.1-08 Rule 1 的教训位置。
- playback_ack_timeout 路径也显示「回首页」的口径（checkpoint ④）与 mock 的真实时序吻合：mock 在 audio_end 后 ~800ms 才发 error＋session_ended（mock-rt-server.js:755-772），而 drain 要等 ~6s 排程音频的真实 onended——session_ended 先于 drain 完成，`pendingHomeButton` 置位顺序成立。
- e2e 断言形态的引用准确：`window.__rtTutor.client.ws.readyState`===3 与 `__rtTutor.capture._stream`===null 的先例就在 word-controls.spec.js:49-51，照抄可行。
- `/` 字节不变的 git 断言（`git status --porcelain` 范围限定 index.html 与 index-banner.js 两文件）是把 RT-10 红线自动化的聪明做法，两文件确实存在。

**Concerns**：
- **MEDIUM（路径错误）**：files_modified 与 Task 2 read_first 均写 `pkg/web/static/styles/tutor.css`，但该文件不存在——实际路径是 **`pkg/web/static/tutor/styles/tutor.css`**（tutor/index.html:21 引用 `styles/tutor.css` 相对于 `/tutor/` 解析）。按错误路径执行会在错误位置新建孤儿文件、且 `.rt-btn-home` 样式不会生效（tutor 页加载不到），e2e 的链接可见性断言可能因无样式而不稳定。
- **MEDIUM-LOW（e2e flake）**：spec ①「session_ended 横幅出现后先断言回首页链接**尚不可见**（音频未释放时不提前出现）」在自然路径上时序脆弱：自然路径的 session_ended 以 summary 的 sentence_ack 为门（客户端播完才 ack），此时全部排程音频已播完，`drainNaturally()` 几乎立即 resolve——「先不可见」窗口可能只有几毫秒，Playwright 轮询很可能直接错过。该负向断言在 `?mock_ack_timeout=1` 路径（音频确实还在排队）才是确定性的。
- **LOW**：若 drain 完成先于 session_ended 到达（理论上网络重排时可发生），`releasePlaybackResources` 已执行且守卫返回，之后 banner 置位 `pendingHomeButton` 却无人再追加链接。生产 Go 侧 error→session_ended 背靠背发送（check.go/runNaturalEnding 语义），实际概率极低，但一行防御（置位时若已 released 则直接追加）可归零。
- **LOW**：git 断言依赖工作树在该两文件上本来就干净——若开发者本地有未提交改动会误报。e2e 环境通常干净，可接受。

**Suggestions**：
- 修正 files_modified 与 read_first 中 tutor.css 路径为 `pkg/web/static/tutor/styles/tutor.css`（必须，执行前改）。
- spec ① 的「提前出现」负向断言移到 ack_timeout 路径，或改为断言 `observe()` 的 `playbackResourceReleased` 标志先于链接出现（状态量而非 DOM 时序）；自然路径只保留正序断言（readyState==3 → _stream==null → 链接可见）。
- `pendingHomeButton` 置位处加「已 released 则立即追加」的一行防御。

**Risk Assessment**：**MEDIUM**。设计语义正确、时机链条源码级吻合，但路径错误若不修正会让 Task 2 直接产出错位文件，e2e 负向断言有 flake 隐患——两者都是一行级修正，修后降为 LOW。

---

## 13.3-05

**Summary**：配置链（yaml → infra.RTConfig → main.go rtTurnCfg → TurnManagerConfig → 裁决点）每一环都核实存在：RTConfig（config.go:196）与 LessonID 校验先例（config.go:264-276）、`normalizeSessionMode` 单点裁决（turn.go:568-573）、`tm.mode` 赋值处（turn.go:484）、rtTurnCfg 注入区（main.go:598-618）。缺省放行方向与 checkpoint ① 绑定、极性反转的替代口径也写了执行分支。是一份小而严谨的计划。

**Strengths**：
- 「短路在裁决单点之前、normalizeSessionMode 原语义保留」的实现方式避免了双裁决点漂移；`tm.mode` 的其余读取处（lessonViewFor/replyDisplay/processTurn 的 maskTranscript 分支，turn.go:697-718/1087）短路后自然全按 student，无需逐处修改——计划的「零改动」判断经源码核实成立。
- Test 3（缺省 false 时 mode=test 仍进 ModeTest）把「开发流零回归」钉进测试；Test 2 复用 lessonViewStudent 断言形态验证投影确实收敛。
- 与 13.3-03 的并行声明属实（文件零交集），且与 01/02 对 turn.go/turn_test.go/main.go 的修改靠波次串行化解——05 depends_on [13.3-02] 的保守排序让所有触碰 main.go 的计划全部串行，无合并冲突窗口。

**Concerns**：
- **LOW**：Task 1 action 写「config.example.yaml 的 rt: 段加 forceStudentMode」，但 **config.example.yaml 根本没有 rt: 段**（全文到 ws: 段为止，config.example.yaml:150-193）。执行者需要新建 rt: 段而非「在既有键风格下追加」；且新段只放 forceStudentMode 一个键、不提 allowedOrigins/wordTimeoutMs 等兄弟键，文档完整性略欠。
- **LOW**：`ForceStudent` 只影响服务端投影，`mode=test` 的会话在 WS 控制面权限上与 student 无差别——这与「防手滑非防孩子」的拍板一致，threat model 已如实登记，但建议在 config.example.yaml 注释里也写明「非认证手段」，防未来误用。

**Suggestions**：
- read_first 与 action 改为「config.example.yaml 新建 rt: 段（首键 forceStudentMode，注释指向 config.go RTConfig 全量键）」。
- Test 4 建议补一条：ForceStudent=true 时 session_init 不带 mode 字段（absent）也得 student——覆盖「无参数进来的旧书签」分支（normalizeSessionMode 已保证，但值得钉死在强制开关组合下）。

**Risk Assessment**：**LOW**。改动面小、锚点全部核实、测试覆盖四分支＋零回归。config.example.yaml 的段不存在问题只是文档层面的小坑。

---

## 13.3-06

**Summary**：收口计划忠实复刻 13.1-07 的纪律（四件套如实记录、flake 重跑须登记、红项不掩码交回 orchestrator），UAT 清单对 ROADMAP 七条成功标准的映射完整且包含了最关键的三项人工核查：零 FSRS 写（review_logs 计数不变）、双时点泄题检查、kill 进程制造崩溃窗口验证「待核实」。14 任务行的算术核对无误（3+2+3+2+2+2=14）。四条命令（package.json:3-5 的 test/e2e/e2e:rt＋go test）全部存在。

**Strengths**：
- 「崩溃窗口真机模拟：默写检查中 kill 服务进程→重启→家长端显示待核实」把 13.3-01 只能 backstop 的 must-have 变成了可执行的人工步骤——两份计划在验证责任上的交接闭环。
- 泄题检查的三层证据结构（13.3-02 表驱动 → 13.3-03 mock 负向断言 → 本计划真机双时点）在 UAT 用例中显式成文。
- RT-09 unclassified 假设登记＋四项微决策落地记录进 UAT 头部，杜绝「局部自动化全绿＝需求验收」的越界解读（与 REQUIREMENTS「阶段局部验证不自动勾选整个需求」一致）。
- 真机验收硬依赖（LAN iPad＋mkcert）如实声明无自动化替代。

**Concerns**：
- **LOW**：Task 1 的 automated verify 是 `node -e "...includes('家庭旅程')..."` 内嵌中文的 JS 字符串——在 Windows shell（cmd/pwsh 代码页）下中文经命令行传递有编码风险，`process.exit(1)` 可能因编码错乱而误触发。用 `grep -c` 或把校验写成临时脚本文件更稳。
- **LOW**：Task 2 的 `npm run e2e`（真后端 webServer＝run.ps1，playwright.config.js:33-40，workers:1、maxFailures:1）意味着任何一个既有用例先红就停——四件套「如实登记」的粒度是命令级，红项归因时可能需要多次重跑定位。13.1-07 已有此纪律，不算新风险。
- **LOW**：Task 2 前置「所有前置计划（01-05）已合并」是文字前提，无机械校验（如检查各 SUMMARY 存在）。收口计划漏跑某前置的防线只在流程上。

**Suggestions**：
- verify 命令改为对三个关键词分别 `grep -c`（bash 工具下可靠），或 `node` 读一个预置校验脚本。
- Task 2 开头加一步机械前置检查：确认 13.3-01~05 的 SUMMARY.md 全部存在再起跑四件套。

**Risk Assessment**：**LOW**。纯资产交付＋回归收口，纪律条款完备；剩余为 Windows 环境的工程细节。

---

## 跨计划对比（Cross-Plan Comparison）

- **依赖与波次**：01→02→{03,05}→04→06 的串并行结构成立。03/05 同波并行的零文件交集声明属实；04 与 03 因 e2e 端口互斥（真后端 8080＋mock 8099 均为全局 webServer）串行是正确的保守选择。所有触碰 `cmd/service/main.go`、`pkg/rt/turn.go`、`pkg/rt/turn_test.go` 的计划（01/02/05）全部落在不同波次，无合并冲突窗口。
- **数据合同链**：01 的回执表 → 02 的 LatestByLessonIDs/save_status → 03 的四态文案 → 06 的 UAT「待核实」模拟，四份计划对同一语义（pending≠failed≠unknown）的表述逐字一致，未发现口径漂移。
- **泄题防线**：02 服务端白名单投影（表驱动）＋03 前端负向断言＋06 真机双时点，三层证据结构完整覆盖 SC-2/SC-7。
- **发现的最强与最弱**：最扎实的是 13.3-01（机制映射零误差）与 13.3-02（投影权威锚定正确）；最需要修正的是 13.3-04 的 tutor.css 路径错误与 e2e 负向断言时序；最需要拍板的是 13.3-01 继承的 practice×兜底 FSRS 写存量缺口。

## 总体风险评估（Overall Risk Assessment）

**MEDIUM（可控）**。六份计划整体质量高：全部关键机制断言经源码逐行核实成立（挂载点、幂等键、迁移挂点、签名波及面、投影分支表、drain 时机链、mock 缺口、e2e 断言先例），无凭空想象的结构。需要处置的事项按优先级：

1. **执行前必须修正**：13.3-04 的 `pkg/web/static/styles/tutor.css` → `pkg/web/static/tutor/styles/tutor.css`（否则 Task 2 产出错位文件）。
2. **执行前应拍板**：practice 课经 `uncheckedCloseFallback` 走 FSRS 写的存量缺口（check.go:892-948 无 practice 守卫）——13.3-01 是修掉它还是如实登记为已知偏差，需明确决策而非静默通过。
3. **执行时应调整**：13.3-04 spec ① 的「提前出现」负向断言移到 ack_timeout 路径或改用状态量断言；13.3-05 需新建 config.example.yaml 的 rt: 段；13.3-06 的 node -e 中文校验换 grep。

## 共识式总结（Consensus Summary）

本组计划对「诚实性」这一阶段主轴的贯彻贯穿始终——回执四态、投影白名单、百分比禁令、「待核实」崩溃语义在四份计划间口径逐字一致，且每一条都有源码级锚点支撑。计划组的自我认知也准确：风险集中在语义边界而非工程构造。修正 13.3-04 的路径错误、对 practice×兜底存量缺口做出显式决策后，本阶段可以按波次推进；Wave 1 的四项微决策 checkpoint 是全组计划的地基，应严格执行不得跳过。
