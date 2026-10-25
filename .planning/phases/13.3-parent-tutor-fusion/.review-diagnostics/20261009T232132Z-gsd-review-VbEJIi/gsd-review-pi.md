我已对仓库源码做了 Read 级核对（`pkg/lesson/store.go`、`practice_store.go`、`pkg/rt/check.go`、`practice.go`、`turn.go`、`dictation.go`、`pkg/web/api/handlers.go`、`cmd/service/main.go`、`pkg/infra/config.go`、`static.go`、`db.go`、`pkg/web/static/*`、`tests/e2e-rt/mock-rt-server.js`、`word-controls.spec.js`、`config.example.yaml`、`config.local.yaml`、`run.ps1`、`.planning/phases/13.1-*/` 等）。以下为完整评审。

---

## 13.3-01

**Summary**
持久保存回执表 + 两阶段写（pending→saved/failed）+ 正常检查挂载端到端。计划对四个提交终点的定位精确，挂载语义（旁路、幂等、诚实词集）与现有代码注释纪律一致，是后续所有家长展示的数据基础。

**Strengths**
- 四提交终点行号经核实全部准确：`finishCheckSubmit`（`pkg/rt/check.go:600`）、`finishCorrections`（`check.go:812`）、`uncheckedCloseFallback`（`check.go:892`）、`finishPracticeSubmit`（`pkg/rt/practice.go:392`）。
- `ReviewWriteResult` 确为词集 `Requested/Updated/Missing []string`（`pkg/rt/dictation.go:54`），与计划「存词集」假设完全一致。
- 幂等键来源三处正确：正常/加练 `m.RequestID`（`check.go:501` 校验非空）、兜底 `lessonID+"-complete-check"`（`check.go:907`）、`ClearUnchecked` `lessonID+"-clear-unchecked"`（`check.go:844`）。
- 旁路纪律（回执 error 只 Emit 不 return）与既有 S-05「绝不盲目重试」注释（`check.go:641`）衔接正确。
- checkpoint:human-verify 门正确放在 Wave 1 起点，四项微决策（含回执词集）先定案后写码，避免返工。

**Concerns**
- **MEDIUM — 回执表无唯一约束**：`lesson_save_receipts` DDL 只有 `idx_receipts_lesson(lesson_id)` 普通索引，无 `UNIQUE(lesson_id, kind)` 或 `UNIQUE(lesson_id, request_id)`。计划完全依赖上游 latch/`reviewSeen` 守卫防重，但表本身不设防。语义上每课每 kind 至多一条（check/correction/unchecked_fallback/practice 各自一次），`UNIQUE(lesson_id, kind)` 成立且是廉价的纵深防御。建议补上，防未来上游守卫被改错时静默产生双回执。
- **LOW — 四终点 requestID 来源各异**：normal 需新增 `requestID` 参数（调用点 `check.go:598` 传 `m.RequestID`）；correction 在 `correctionInputs.requestID`（`check.go:800`）；uncheckedCloseFallback 无客户端 ID（用 lessonID+"-complete-check"）；practice 需新增参数。计划已逐一覆盖，但实现时需防混用（尤其 uncheckedCloseFallback 是同步 close 路径，无 Sender）。
- **LOW — `requested` 双口径**：`finishCheckSubmit` 既有 `requested` 是 `len(ratings)`（int，`check.go:613`），回执存的是词集（`[]string`）。计划已写「requested=keys(ratings)」，实现时须从 ratings map keys 提取，勿复用 int 口径。

**Suggestions**
- 在 `receiptDDL` 追加 `UNIQUE(lesson_id, kind)`，并加一条重复 Insert 的测试（Expect 幂等或报错，planner 决定）。
- 明确 `Begin` 词集序列化前排序（消除 map 迭代序）——计划已写，落实为单测断言即可。

**Risk Assessment**: LOW–MEDIUM。机制简单、先例充分，主要风险在表约束缺失与四终点 requestID 拼装，均为实现期可控。

---

## 13.3-02

**Summary**
两个只读成果接口 + 投影白名单。核心价值是服务端序列化层「进行中课无词面」的泄题防线。计划对完结判定、签名波及、lesson_id 校验的理解全部准确，是本阶段正确性最关键的 plan。

**Strengths**
- `LessonCompleted` 白名单精确复刻 `pkg/rt/turn.go:532-553` 分支表：`len(cards)==0`→不可用 / `stage==checking`→false / `stage==submitted(+unchecked)`→true / `current_index>=len(cards)`→legacy 完结。计划正确区分「投影完结」与「FSM resume 分支」——`submitted+unchecked` 对投影是完结（词可示），对 FSM 是 correction 重开（`turn.go:543`），两套语义不混用，这是关键正确性点。
- 泄题防线走「JSON 里没有才是没有」（服务端不输出字段），而非前端隐藏，方向正确。
- `NewHandler` 签名变更波及点完整：唯一生产调用点 `cmd/service/main.go:679` + `handlers_test.go`（901 行，实测一致）。
- lesson_id 校验对齐 `pkg/infra/config.go:264-276`（≤64 runes、无空白、无 `/` `\`），先例确凿。

**Concerns**
- **MEDIUM — 未核对兜底课的判定计数呈现口径未钉死**：`LessonSummary.Correct/Wrong` 只数 `Manual=true`，而 unchecked 兜底课（`uncheckedCloseFallback` 用 `uncheckedRatings` 派生 verdict）其 verdict 全部 `Manual=false`（`check.go:892` 起的 fallback 路径）。结果是该课 `Correct=0`、`Unconfirmed=WordCount`。这本身是诚实的，但必须用单测/e2e 钉死「未核对课呈现为全未确认」而非「0 对 0 错」被误读。计划 behavior 里未显式覆盖此断言。
- **LOW — practice 课详情数据源二选一未注释**：practice 课的 `lessons` 行本身也存了 verdicts（`submitPractice` 也走 `CompleteCheck`，`practice.go:366`），但计划选 `practice_logs`（`ListPracticeByLesson`）为 results 权威源。选择合理，但需在代码注释说明为何不读 `lessons.verdicts`，避免后续维护者困惑。
- **LOW — `ListLessons` 的 SELECT 含 `cards, verdicts` 两个 JSON 大列**：计划自警「只 SELECT 需要的列」，`cards`（词数）、`verdicts`（manual 计数）确属需要，`words` 列正确未选。≤50 行下可接受，但单连接池下 JSON 解析成本（`db.go:71` SetMaxOpenConns(1)）需以 LIMIT 硬 cap + 单行详情兜住（计划已做）。

**Suggestions**
- 在 `TestLessonCompleted` 或 httptest 里补「submitted+unchecked 课 → Correct=0/Unconfirmed=全词数」断言，锁定未核对呈现口径。
- `getLesson` 的 practice 分支加一行注释说明「practice_logs 为逐词练习权威源，lessons.verdicts 仅用于 stage 流转」。

**Risk Assessment**: MEDIUM。泄题投影是本阶段最高风险面，但计划防线（白名单 + 表驱动 + httptest 负向断言）设计正确，主要补强点是未核对计数口径的测试覆盖。

---

## 13.3-03

**Summary**
parent.html 三 tab 化 + LessonsView 组件 + mock /api/v1 桩。纯前端 + mock，但牵动既有 parent-management e2e 全量改造，且有三处易错点（fetch 接线、双 hidden 结构、正确率分母）。

**Strengths**
- 正确识别 `parent-management.spec.js` 需 tab 前置步骤（进页先点「词库」）。
- mock server 无 `/api/v1` 的事实被正确识别（`mock-rt-server.js` 仅有 static + `/rt` + `__test_state`，无任何 `/api/v1` 路由），Pitfall 5 结论准确。
- XSS 防护（createElement + textContent 纪律）延续 `parent-browse.js` 既有模式。

**Concerns**
- **MEDIUM — `fetchJSON` 是私有函数，LessonsView 不能全局调用**：`fetchJSON` 定义在 `parent-app.js` IIFE 内部（`pkg/web/static/js/parent-app.js:59`），仅暴露于 `window.__parentApp.fetchJSON`（`:231`）。组件（BrowseView/AddCard/SettingsCard）都是通过构造器 options 接收 fetch 回调（`:149-218`）。计划 Task 2 步骤 1 措辞「fetchJSON 或组件内同款 AbortController 封装」有歧义——若执行者误以为 `window.fetchJSON` 全局可达，会得到 undefined。建议 PLAN 明确「LessonsView 构造器 options 注入 `fetchLessons`/`fetchLessonDetail`，由 parent-app.js 用私有 fetchJSON 接线」。
- **MEDIUM — 三 tab 化的双 hidden 陷阱**：`settingsSection` 现有 `hidden` 属性由 SettingsCard mount 后移除（`parent.html:63` 起）。若再包一层「设置」tab panel 的 hidden，会出现两层隐藏状态需分别管理；且「词库」tab 需同时切 `addSection`+`browseSection` 两个 section。计划只说「归入 panel」未定死 wrapper 结构（wrapper div 承载两 section vs 每 section 单独切 hidden）。建议 planner 在 PLAN 里定死：用 wrapper div，settingsSection 的 mount-unhide 语义保留在其自身，tab 只切 wrapper。
- **LOW — 加词提示口径需两处都加**：单个添加走 `showToast('已添加')`（`parent-modes.js:343`），批量添加走 `renderResult` banner（`:650`）。计划说「单个添加与批量添加的成功反馈」，实现时两处都要加，勿只改 banner。
- **LOW — 正确率分母负向断言**：「已核对 X 对 / Y 错 / Z 未确认」来自服务端 `correct/wrong/unconfirmed` 直译，任何视图无百分比。需 e2e 负向断言「无 `%` 字符」钉死，计划已明示但建议落在 spec 断言清单。

**Suggestions**
- PLAN 中把「LessonsView 构造器 options 注入 fetch 回调」和「tab 用 wrapper div、settingsSection 双 hidden 语义分离」两处写死，避免执行歧义。
- parent-modes.js 两处（toast + banner）都追加选词口径行。

**Risk Assessment**: MEDIUM。纯前端但三处易错点（fetch 私有接线、双 hidden、两处提示）叠加既有 e2e 全量改造，回归风险真实存在。

---

## 13.3-04

**Summary**
home.html 两扇门 + tutor 终结态「回首页」。纯静态 + 前端时机控制，机制清晰，风险低。「回首页」时机（drain 完成后）与现有 terminalDraining/releasePlaybackResources 结构精确对齐。

**Strengths**
- 「回首页」时机精确：session_ended 三自然理由（`tutor/index.html:699-710`）置标志 → `beginTerminalDrain`（`:511`）→ `drainNaturally().then` → `releasePlaybackResources`（`:502`）追加链接，规避 Pitfall 6（提前出现）。
- 断言形态照抄已核实的 `word-controls.spec.js:49-51`（`readyState===3` + `__rtTutor.capture._stream===null`）。
- `/` 字节不变用 `git status --porcelain` 限定两文件断言，合理。
- 复位点正确：`startBtn` click 处 `terminalDraining=false`（`:791`）同步复位 `pendingHomeButton`，防跨课残留。

**Concerns**
- **LOW — 两路径 drain 时序需分别覆盖**：自然路径是 session_ended → beginTerminalDrain；timeout 路径是 error 事件先 `beginTerminalDrain`（`:671-675`），session_ended 到达时 `terminalDraining` 已 true 直接 return（`:713-716`）。两路径最终都进 `releasePlaybackResources`，checkpoint ④「timeout 也显示」口径与代码一致，但 e2e 需分别覆盖两路径的「先 drain 后显示」时序（计划已列）。
- **LOW — practice 路径覆盖取舍**：计划说「若有现成 practice mock 脚本则复用」。已核实 mock server 有 practice 脚本（`:1092` `practice_complete` pendingTerminal），实践上可覆盖，spec 注释说明取舍即可。
- **LOW — home.css 独立不复用 parent.css 合并**：计划已明示，触控目标≥48px 需自行实现，注意 iPad 触控约定。

**Suggestions**
- 无重大改进，按计划执行即可；建议 spec 断言里显式覆盖 timeout 路径（`:671` 分支）而非仅默认路径。

**Risk Assessment**: LOW。机制成熟、先例充分，主要靠 e2e 时序断言锁死。

---

## 13.3-05

**Summary**
生产强制 student 模式。裁决单点识别精确，配置链（RTConfig → main.go → TurnManagerConfig → normalizeSessionMode）正确，缺省放行方向向后兼容。

**Strengths**
- 裁决单点准确：`tm.mode` 仅在 `turn.go:484` 赋值 `normalizeSessionMode(m.Mode)`，所有下游（`lessonViewFor` `:698`、`replyDisplay` `:714`、greeting 打码 `:653`）读 `tm.mode`/`sessionMode`，短路一处即可，无遗漏读取点。
- 缺省 false=放行（Go 零值向后兼容）方向正确，`config.yaml` 不加键即延续 mode=test 开发流。
- `Validate`（`config.go:245`）不触及新 bool 键，无需新增分支——已核实。

**Concerns**
- **MEDIUM — `config.example.yaml` 根本没有 `rt:` 段**：实测文件 194 行，段表只有到 `ws:`（`:150`），`grep '^rt:'` 无匹配。计划 Task 1 说「config.example.yaml 的 rt: 段加 forceStudentMode 示例」是**加键到不存在的段**，会误导执行者。需改为「在 config.example.yaml **新增** `rt:` 段（含 forceStudentMode 注释，参照 config.local.yaml:91 现有 rt: 段形态）」。
- **LOW — 防手滑语义注释**：计划 prohibition「不当作认证/权限机制」正确，落实为 config.go 字段注释即可。

**Suggestions**
- 修正 PLAN 措辞：config.example.yaml 需新建 `rt:` 段，而非在既有段加键。给执行者一份 `config.local.yaml:91-97` 的 rt: 段形态作模板。

**Risk Assessment**: LOW。改动极小、单点明确，唯一实质偏差是 config.example.yaml 缺 rt: 段的文档措辞。

---

## 13.3-06

**Summary**
阶段收口：全量回归四件套 + 家庭旅程真机 UAT 清单 + VALIDATION 收口。资产交付型计划，无生产代码改动。

**Strengths**
- 四件套命令与 13.1-07 收口纪律对齐；`13.1-07-SUMMARY.md`/`13.1-08-SUMMARY.md`、`13.1-UAT.md` 均已核实存在。
- UAT 清单覆盖七条成功标准逐条映射，含零 FSRS 写核查（`review_logs` 计数不变）、泄题真机检查（默写中/检查中两时点请求两接口断言无词面）、崩溃窗口「待核实」模拟。
- 明确「真机人工验收，不以自动化替代」（13.1 教训）。

**Concerns**
- **LOW — Per-Task Map 行数核对**：6 计划任务数 3+2+3+2+2+2=14，与计划「14 行」一致，核对通过。
- **LOW — 崩溃窗口「kill 服务进程」模拟门槛高**：对家长/孩子实际执行较难，但计划已将其列为 Manual-Only 可选抽查（mock e2e 已覆盖「待核实」文案），可接受。
- **LOW — 验收资产不代打结论**：计划 correctly 只交付可执行清单，/gsd-verify-work 由用户真机执行。正确。

**Suggestions**
- 无重大改进。建议 UAT 头部明确「七条成功标准 → 用例编号」的映射表，方便勾选追溯（计划已要求映射，落实为显式表格即可）。

**Risk Assessment**: LOW。纯收口资产，无代码风险。

---

## 跨计划综合评估

**依赖链**: `01 → 02 → (03 ∥ 05) → 04 → 06`。03 与 05 同波并行的耦合声明经核实成立——`files_modified` 零交集（03 只碰 `pkg/web/static/*` 与 `tests/e2e*`，05 只碰 `pkg/infra/config.go`、`pkg/rt/turn.go`、`main.go`、`config.example.yaml`），无共享可变资源。04 依赖 03（home.html 链接到 03 改造后的 /parent.html），顺序正确。

**整体质量**: 六个计划对仓库地形的引用高度准确（我核对了 30+ 处 `file:line`，绝大多数精确到行，仅个别 ±2 行偏差，如 `normalizeSessionMode` 计划写 568 实际 569、`lessonViewStudent` 计划写 675-685 实际 683-691，不影响执行）。计划不是「纸上谈兵」，每个机制（幂等 latch、迁移、投影、静态服务、mode 裁决）都有仓内先例可循，且坑点清单（Pitfall 1-8）多数源自仓内注释明文记录的历史事故。

**主要风险排序**（跨计划）:
1. **泄题投影白名单**（13.3-02）— 正确性最关键，计划设计正确，需补「未核对课计数呈现」测试。
2. **回执表缺 UNIQUE 约束**（13.3-01）— 纵深防御缺口。
3. **parent.html 双 hidden 结构 + fetch 私有接线**（13.3-03）— 两处执行歧义易导致回归。
4. **config.example.yaml 缺 rt: 段**（13.3-05）— 文档任务措辞偏差。

**总体 Risk Assessment: MEDIUM**。计划整体设计严谨、可追溯性强、对既有代码语义的理解正确；风险集中在三处实现级细节（回执约束、tab 结构、fetch 接线）和一处文档偏差，均可在执行期以小幅修订解决，不构成方向性问题。无 HIGH 级风险：泄题防线虽敏感，但三层证据（表驱动 + httptest 负向断言 + 真机 UAT）设计到位。

（已完成全部 6 个计划 id 的逐一评审，无未覆盖项。）
