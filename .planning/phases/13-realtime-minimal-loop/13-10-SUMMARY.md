---
phase: 13-realtime-minimal-loop
plan: 10
subsystem: infra
tags: [go, config, pybridge-adapter, fsrs, word-source, env-binding, wiring, deployment-docs]

# Dependency graph
requires:
  - phase: 13-realtime-minimal-loop/13-08
    provides: WordSource 接口（FetchDueWords/SubmitReview + ReviewWriteResult 真实集合语义）、TurnManagerConfig.Words/WordTimeout 字段与 main.go 注入点（Words 生产注入缺位 stub——本计划销项）、session_init 串行化与 lesson_unavailable 诚实降级
  - phase: 13-realtime-minimal-loop/13-09
    provides: 60 秒词计时器（WordTimeout 消费端）、VAD 门控与 ASR 容忍——生产取词接通即可真机
  - phase: 13-realtime-minimal-loop/13-07
    provides: pkg/lesson.Word json 标签 word/meaning/example 与 pybridge.DueCard 逐字对齐（零损映射前提）、lesson_unavailable/ErrLessonUnavailable 协议词表
  - phase: 13-realtime-minimal-loop/13-06
    provides: infra.RTConfig 十叶子键 bindEnvForAllLeaves 纪律（Pitfall 6）、main.go /rt 挂载与 pyBridge 构造、rt_wiring_test 三用例、DEPLOYMENT.md「HTTPS / TLS 与 /tutor/ 入口」章节（rt 段更新挂点）
provides:
  - "infra.RTConfig 转向后字段集：LessonWords 字段与 1..10 校验删除（旧配置残留键被 viper 静默忽略）、LessonID 可空（空 = FSM 每日派生 r0-YYYYMMDD；非空 ≤64、禁空白与路径分隔符）、WordTimeoutMs（0=默认 60000、负数/超 3600000 拒绝包 ErrConfigInvalid）、rt.wordTimeoutMs 叶子经 HTA_RT_WORDTIMEOUTMS 显式绑定"
  - "pkg/rt/mimo_adapters.go：PyFSRS 适配器（pybridge.Executor → rt.WordSource）——FetchDueWords 始终 RunDue(DueOpts{Limit, FetchLockHours:1}) 非 peek、DueCard→lesson.Word 零损直拷保序；SubmitReview 空 map 零 spawn、单次 RunReview、Updated 与请求集合比对返回 Requested/Updated/Missing、未知/重复 Updated 为 provider contract error、partial/zero+nil error 如实返回不盲重试；var _ WordSource 编译期检查"
  - "cmd/service/main.go 生产接线：pyBridge → rt.NewPyFSRS → rtTurnCfg.Words + WordTimeout(time.Duration(cfg.RT.WordTimeoutMs)*ms)，构造失败按既有 fail-fast 错误返回；无 key 降级语义不变（voice_unavailable 先于取词）"
  - "docs/DEPLOYMENT.md「FSRS 词源前提」：VPS 三前提（python 词库环境 + vocabDBPath vocab DB + MIMO_API_KEY）、rt.lessonWords 已移除迁移说明、lessonId/wordTimeoutMs 语义、vocab_manager.py due --help 必含 --fetch-lock-hours 的脚本核验与 sha256 记录、取词锁 1h 风险边界、lesson_unavailable 排障"
affects: [13-realtime-minimal-loop/13-11 (前端 paused/playback_ack_timeout——后端词源链路已闭合), 13-12 (真机验收——wordTimeoutMs 标定入口就绪), Phase 15 (scoped unlock/durable recovery 承接 T-13-20 残余)]

# Actuals (#2632)
actuals:
  tokens: 9400       # chars/4 over the realized diff (~37.6K chars across 6 files, +569/-52 lines)
  tasks: 3
  commits: 3          # MEASURED: git rev-list --count 82788d0..HEAD
commits: 3
plan_head_before: 82788d05166f8c3de1d01c67fd3a4e3e92b2dc76
plan_head_after: dab7798

# Tech tracking
tech-stack:
  added: []           # 零新增依赖（T-13-SC：go.mod 零变化，包安装面零变化）
  patterns:
    - "Pattern: 词源单通道——课堂词永远来自 FSRS 到期获取（PyFSRS→WordSource），配置面不再承载词表；rt.lessonWords 类固定词配置随转向退场，旧键静默忽略 + 文档迁移说明"
    - "Pattern: 显式覆盖脚本默认——pybridge 调用方对影响锁语义/批次生命周期的参数（FetchLockHours）必须显式传值并在适配层测试断言，vendored 脚本默认值只是源码事实不是运行时契约"
    - "Pattern: 诚实写回摘要——SubmitReview 以返回 Updated 集合与请求集合比对产出 Requested/Updated/Missing，nil error 不代表全量保存；未知/重复 Updated 按契约违规报错不自动重试"

key-files:
  created:
    - pkg/rt/mimo_adapters_test.go
  modified:
    - pkg/infra/config.go
    - pkg/infra/config_test.go
    - pkg/rt/mimo_adapters.go
    - cmd/service/main.go
    - docs/DEPLOYMENT.md

key-decisions:
  - "FetchLockHours=1 常量固定在适配层（pyfsrsFetchLockHours）：1h 覆盖 R0 正常课堂并限制 RunDue 成功但建课前失败的锁影响；不依赖 vendored 脚本 24h 默认（部署文档要求实际脚本 --help 必含该参数并记录 sha256）"
  - "SubmitReview 的 Requested/missing 集合排序输出（sort.Strings）仅為确定性报告；Updated 保持脚本返回顺序——集合成员关系才是语义，顺序只为日志可比对"
  - "lessonId 轻量校验取 ≤64 + 禁空白/路径分隔符（不做过深的词表约束）：非空时仅防明显误配（路径注入/截断），语义正确性由 FSM 每日派生兜底"
  - "rt_wiring_test.go 零改动：三用例（HTTPS /tutor//rt、无 TLS 基线、缺证书退出 1）本就不构造 TurnManagerConfig、不做 session_init 全流程（取词不经 python），核对后确认与 TurnManagerConfig 字段变化无交集"
  - "DEPLOYMENT.md 排障按可见故障组织（lesson_unavailable 三成因：python 环境坏/legacy 空 Cards/python 侧失败），细节只指向服务端结构化日志——与 T-13-14 固定人话文案纪律一致"

patterns-established:
  - "Pattern: 配置键退场四件套——删字段、删校验、删 env 绑定、文档迁移说明；旧配置携带已删除键被 viper 静默忽略属预期，空段取默认测试锚定（TestLoadConfigRTEmptySectionTakesDefaults）"
  - "Pattern: 适配层锁参数显式化——跨进程副作用的参数值在适配层常量化并在 fake Executor 上断言 opts 逐字段命中"

requirements-completed: [RT-10]  # 拷贝自 PLAN frontmatter requirements；RT-10 为降级透明配置半边，真实故障呈现归 13-12/13-13（计划 flagged assumptions）

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "RTConfig 转向重构：LessonWords 字段与 1..10 校验退场、LessonID 可空（轻量合法性校验）、WordTimeoutMs 带范围校验、DefaultConfig 空 lessonId + 60000ms、HTA_RT_WORDTIMEOUTMS 显式 env 绑定无静默失效"
    requirement: RT-10
    verification:
      - kind: unit
        ref: "pkg/infra/config_test.go#TestDefaultRTConfig（LessonID==\"\" 且 WordTimeoutMs==60000）+ TestRTConfigValidate（空白/过长/路径分隔符拒绝、负数/超 1h 拒绝、0 与边界 3600000 放行）"
        status: pass
      - kind: unit
        ref: "pkg/infra/config_test.go#TestRTAndTLSEnvBinding（HTA_RT_WORDTIMEOUTMS=30000 经 LoadConfig 生效）+ TestLoadConfigRTEmptySectionTakesDefaults（空 rt 段取新默认）"
        status: pass
      - kind: command
        ref: "go test ./pkg/infra/ -short -count=1 -run \"TestRT|TestTLS|TestLoadConfig\" -v（全 PASS）+ go test ./pkg/infra/ -short -count=1 全绿"
        status: pass
    human_judgment: false
  - id: D2
    description: "PyFSRS 适配器：RunDue 显式 FetchLockHours=1 非 peek、DueCard→lesson.Word 零损直拷保序、limit<=0 置 10；SubmitReview 空 map 零 spawn、单次 RunReview、partial/zero Updated+nil error 返回真实 Requested/Updated/Missing 集合不盲重试、未知/重复 Updated 契约报错"
    requirement: RT-10
    verification:
      - kind: unit
        ref: "pkg/rt/mimo_adapters_test.go#TestPyFSRSFetchDueMapsCards（Limit/FetchLockHours==1/非peek 断言 + 字段映射）+ TestPyFSRSFetchDueErrorPropagates + TestPyFSRSSubmitReviewPassesResultsAndSkipsEmpty + TestPyFSRSSubmitReviewReportsPartialUpdated + TestPyFSRSSubmitReviewReportsZeroUpdated（后三者均断言 RunReview 恰好一次）+ TestPyFSRSNilExecRejected"
        status: pass
      - kind: command
        ref: "go test ./pkg/rt/ -count=1 -run \"TestPyFSRS\" -v（6 PASS / 0 FAIL）+ go build ./... + go test ./pkg/rt/ -count=1 全绿"
        status: pass
    human_judgment: false
  - id: D3
    description: "main.go 生产注线：pyBridge→rt.NewPyFSRS→rtTurnCfg.Words/WordTimeout 注入，构造失败 fail-fast；无 key 降级语义不变；LessonWords 零引用（编译即证）"
    requirement: RT-10
    verification:
      - kind: integration
        ref: "cmd/service/rt_wiring_test.go#TestRunTLSHTTPSServesTutorAndRT / TestRunNoTLSHTTPBaseline / TestRunTLSMissingCertFails（新注线下三用例零改动通过——/rt 挂载断言不经 python 调用路径）"
        status: pass
      - kind: command
        ref: "go build ./... && go vet ./cmd/service/ ./pkg/rt/（exit 0）+ go test ./cmd/service/ -short -count=1 全绿"
        status: pass
    human_judgment: false
  - id: D4
    description: "docs/DEPLOYMENT.md「FSRS 词源前提」：VPS 三前提、lessonWords 迁移说明、lessonId/wordTimeoutMs 语义、--fetch-lock-hours 脚本核验 + sha256 记录、1h 锁风险边界（RunDue 后建课前失败/跨进程并发创建；同 lessonID 重连复用 Cards 不再 RunDue；崩溃残余锁 1h 回队）、lesson_unavailable 排障"
    requirement: RT-10
    verification:
      - kind: other
        ref: "grep 断言四关键词全命中：wordTimeoutMs / lesson_unavailable / fetch-lock-hours / vocabDBPath"
        status: pass
    human_judgment: false
  - id: D5
    description: "真机故障呈现与部署可复现：lesson_unavailable 在真实 python 环境（脚本过旧缺 --fetch-lock-hours、vocab DB 缺失、venv 不可用）下的实际表现与 DEPLOYMENT 步骤可执行性须 13-12 真机检查点收口（计划 flagged: RT-10 probe 边缘谓词 unresolved）"
    verification: []
    human_judgment: true
    rationale: "自动化只能锚定降级路径存在且可编译可启动（wiring 三用例 + PyFSRS 错误传播测试）；真实 VPS python 词库环境下的故障呈现、文档核验步骤的实际执行归 13-12/13-13 真机检查点"

# Metrics
duration: 11min
completed: 2026-10-05
status: complete
---

# Phase 13 Plan 10: RTConfig 转向重构 + PyFSRS 适配器 + 生产注线 + 部署文档 Summary

**FSRS 词源生产链路闭合：RTConfig 按转向重构（固定三词配置退场、lessonId 可空每日派生、wordTimeoutMs 可调显式 env 绑定）、pybridge.Executor→rt.WordSource 的 PyFSRS 适配器（显式 FetchLockHours=1、真实 Updated 集合诚实返回）、main.go 生产注线 TurnManager(Words/WordTimeout)、DEPLOYMENT.md 词源前提与排障——6 个 PyFSRS 测试 + infra RT 组 + wiring 三用例 + 全仓短测全绿，零新增依赖**

## Performance

- **Duration:** 11 min（2026-10-05T17:26:11Z → 2026-10-05T17:36:53Z）
- **Started:** 2026-10-05T17:26:11Z
- **Completed:** 2026-10-05T17:36:53Z
- **Tasks:** 3 / 3
- **Files changed:** 6（新建 1：pkg/rt/mimo_adapters_test.go；计划声明的 cmd/service/rt_wiring_test.go 核对后零改动，见 Decisions）

## Accomplishments

- **RTConfig 转向重构（T-13-19 缓解）**：LessonWords 字段与 1..10 校验整体删除——词永远来自 FSRS 到期获取，配置面不再承载词表；旧 config.yaml 残留的 rt.lessonWords 键被 viper 静默忽略（预期行为），DEPLOYMENT.md 明示迁移。LessonID 可空（空 = FSM 按北京时间每日派生 r0-YYYYMMDD，非空原样使用且仅做 ≤64/禁空白/禁路径分隔符轻量检查）；WordTimeoutMs 新增（0=默认 60000 放行，负数或超 3600000 拒绝包 ErrConfigInvalid）；rt 分组删除 lessonWords 绑定、新增 wordTimeoutMs（HTA_RT_WORDTIMEOUTMS）——Pitfall 6 纪律延续，TestRTAndTLSEnvBinding 锚定生效
- **PyFSRS 适配器（S-05/R-06 落地）**：pybridge.Executor → rt.WordSource 单一适配点。FetchDueWords 始终 RunDue(DueOpts{Limit:limit(≤0→10), FetchLockHours:1}) 非 peek（pyfsrsFetchLockHours 常量 + fake 断言逐字段命中），DueCard→lesson.Word 按 word/meaning/example 零损直拷保序（13-07 标签对齐兑现）；SubmitReview 空 map 零 spawn 返回零值、非空恰好一次 RunReview，以返回 Updated 的 Word 集合与请求键集合比对产出 Requested/Updated/Missing——partial/zero Updated 且 error=nil 如实返回真实集合（13-08 的「本次保存 M/N 个」/记录未保存后缀口径的数据源），未知/重复 Updated 作为 provider contract error 不自动重试；godoc 注明 nil error 不代表全部保存
- **main.go 生产注线**：pyBridge（boot 期既有构造）→ rt.NewPyFSRS → rtTurnCfg{LessonID(可空)、Words、WordTimeout: cfg.RT.WordTimeoutMs*ms}，构造失败按既有 fail-fast 风格 return err；无 key 降级语义不变（voice_unavailable 先于取词）；main.go 零 LessonWords 引用（编译即证）。wiring 三用例在新注线下零改动通过（不构造 TurnManagerConfig、不做 session_init 全流程，取词不经 python）
- **DEPLOYMENT.md rt 段（T-13-21 accept / T-13-20 部署半边）**：新增「FSRS 词源前提」——VPS 三前提（python 词库环境 + vocabDBPath vocab DB + MIMO_API_KEY）、rt 配置键转向后语义与迁移说明、部署核验（`python <vocab_manager.py> due --help` 必含 `--fetch-lock-hours` + sha256 记录，vendored 24h 默认只是源码事实）、1h 锁风险边界解释（RunDue 后建课前失败 / 跨进程并发创建；同 lessonID 重连复用 Cards 不再 RunDue；崩溃残余锁 1h 回队，scoped unlock/durable recovery 留 Phase 15）、lesson_unavailable 三成因排障

## Task Commits

Each task was committed atomically:

1. **Task 1: RTConfig 重构 — lessonWords 退场、lessonId 可空、wordTimeoutMs** - `ccda0e0` (feat)
2. **Task 2: rt.PyFSRS 适配器 — pybridge.Executor → WordSource** - `3120b25` (feat)
3. **Task 3: main.go 注线 + wiring 测试 + DEPLOYMENT.md rt 段** - `dab7798` (feat)

**Plan metadata:** 本 SUMMARY 提交（docs(13-10)）

## Files Created/Modified

- `pkg/infra/config.go` - RTConfig：LessonWords 字段删除、LessonID godoc 改可空语义、WordTimeoutMs 字段（json/mapstructure wordTimeoutMs）；Validate 移除 lessonId 非空与 lessonWords 规则、新增 wordTimeoutMs 范围与 lessonId 轻量检查；DefaultConfig RT 段 LessonID=""/WordTimeoutMs=60000；bindEnvForAllLeaves rt 分组换绑 wordTimeoutMs
- `pkg/infra/config_test.go` - TestDefaultRTConfig（空 lessonId + 60000ms）、TestRTConfigValidate（9 拒绝 + 4 放行子用例）、TestLoadConfigRTEmptySectionTakesDefaults（新默认锚定）、TestRTAndTLSEnvBinding（+HTA_RT_WORDTIMEOUTMS）
- `pkg/rt/mimo_adapters.go` - PyFSRS 结构/NewPyFSRS 构造（nil exec 拒绝、nil logger 兜底 NoOp）、FetchDueWords（FetchLockHours=1 常量、零损映射）、SubmitReview（诚实集合、契约违规报错）、var _ WordSource 编译期检查
- `pkg/rt/mimo_adapters_test.go`（新建）- fakePyBridge 手动 fake（D-48，未用方法 panic 防误用）；6 个 TestPyFSRS* 用例（5 个计划命名 + nil-guard）
- `cmd/service/main.go` - rt.NewPyFSRS 构造与错误处理；rtTurnCfg 注入 Words/WordTimeout，LessonID 注释更新为可空语义
- `docs/DEPLOYMENT.md` - 新增「FSRS 词源前提」小节（前提/配置键/脚本核验/锁风险边界/lesson_unavailable 排障）
- `cmd/service/rt_wiring_test.go` - 核对后零改动（三用例与 TurnManagerConfig 无交集，测试全过）

## Must-Have Truths Verification

| Truth | Evidence | Status |
|-------|----------|--------|
| RTConfig 与转向对齐：rt.lessonWords 移除、rt.lessonId 可空（空=每日派生 r0-YYYYMMDD）、新增 rt.wordTimeoutMs（默认 60000，>0 校验）；全部叶子键经 bindEnvForAllLeaves 显式绑定 | TestDefaultRTConfig + TestRTConfigValidate + TestRTAndTLSEnvBinding + TestLoadConfigRTEmptySectionTakesDefaults；config.go 无 LessonWords（rg 零命中，仅 godoc 迁移说明） | PASS |
| rt.PyFSRS 适配器：FetchDueWords 显传 DueOpts{Limit:10,FetchLockHours:1}；SubmitReview 核对 Updated 与请求集合返回 requested/updated/missing，partial/zero nil-error 不冒充全量成功且不盲重试；空 map 跳过调用 | TestPyFSRSFetchDueMapsCards（opts 逐字段断言）+ Partial/Zero/PassesAndSkipsEmpty（各断言 RunReview 恰好一次） | PASS |
| main.go 接线：pyBridge 构造 rt.PyFSRS 注入 TurnManagerConfig.Words/WordTimeout；无 key 降级语义不变；rt_wiring_test 三用例在无 python 调用路径前提下保持全绿 | main.go NewPyFSRS 调用 + Words/WordTimeout 注入；TestRunTLSHTTPSServesTutorAndRT / TestRunNoTLSHTTPBaseline / TestRunTLSMissingCertFails 三用例通过（日志显示 rt endpoint mounted, lesson_id=""） | PASS |
| docs/DEPLOYMENT.md rt 段更新：VPS 需 python 词库环境 + vocab DB + MIMO_API_KEY；rt.lessonWords 已移除、lessonId/wordTimeoutMs 语义说明 | grep 四关键词命中（wordTimeoutMs/lesson_unavailable/fetch-lock-hours/vocabDBPath）+ 三前提/脚本核验/锁边界小节 | PASS |

## Threat Mitigations Delivered

| Threat | Disposition | Where |
|--------|-------------|-------|
| T-13-19 (Tampering, 旧配置残留 lessonWords 引发的静默歧义, low) | mitigate | viper 对未知键忽略属预期（Validate 无 lessonWords 规则）；TestLoadConfigRTEmptySectionTakesDefaults 锚定空段取默认；DEPLOYMENT.md「rt.lessonWords 已移除」迁移说明——旧配置无需手工清理即可启动 |
| T-13-20 (DoS, RunDue 后建课前失败或并发创建留下未使用锁批, medium) | mitigate | 适配层显式 FetchLockHours=1（pyfsrsFetchLockHours 常量 + TestPyFSRSFetchDueMapsCards 断言，不依赖脚本默认）；13-08 lessonID 初始化串行化防同进程并发创建锁走第二批（既有）；DEPLOYMENT.md 要求部署核验实际脚本 `due --help` 含 --fetch-lock-hours 并记录 sha256；进程崩溃残余锁 1h 后回队；scoped unlock/durable recovery 留 Phase 15 |
| T-13-21 (Information Disclosure, wordTimeoutMs 等新配置经 env 泄漏部署细节, low) | accept | 非 secret 键；密钥纪律不变（MIMO_API_KEY 仅经 secrets.env/env 注入，不入 config/git——DEPLOYMENT.md 存量约定延续） |
| T-13-SC (Tampering, 包安装, high) | mitigate | 零新增依赖（go.mod/go.sum 零变化），包安装面零变化 |

## Decisions Made

见 frontmatter key-decisions（FetchLockHours=1 常量落适配层、Requested/Missing 排序仅为确定性、lessonId 轻量校验口径、rt_wiring_test 零改动核验、DEPLOYMENT 排障按可见故障组织）。

## Deviations from Plan

None - plan executed exactly as written.（计划 Task 3 对 rt_wiring_test.go 的「核对并更新…若有」条件分支核实为「无直接构造、无 r0-test 三词断言」→ 零改动，属计划自身的条件语义而非偏差；该文件列入 files_modified 但终态未变，已在上表如实记录。）

## Stub Ledger Updates

| Stub | 处置 |
|------|------|
| TurnManagerConfig.Words 生产注入缺位：session_init 现答 lesson_unavailable（**13-08 登记表登记，cmd/service/main.go 564-569**） | **就此销项**——main.go 已注入 rt.NewPyFSRS(pyBridge, logger)，生产 session_init 经 RunDue 真实取词 |

本计划无新增 stub。13-09 登记表当时仅余此一项（armWordTimer 已于 13-09 销项），至此 13-08/13-09 登记表全部清空。

## Issues Encountered

- 顺序执行器在主工作树（main 分支）直接提交（config `branching_strategy: none`，与 13-01～13-09 一致）；每任务仅暂存计划声明文件，工作区内无关改动（.gitignore、STATE.md、CONTEXT.md、pkg/infra 其余文件与 pkg/tts 等 EOL 噪声、根目录未跟踪 scratch 文件）一律未触碰
- 计划 Task 2 verify 的 fails_when 写「三个用例名任一缺失」而 action 列五个用例——按五个命名用例全部落地执行（TestPyFSRS 过滤器 6 PASS 含 nil-guard 补充用例），两处口径以 action 项为准

## User Setup Required

None - no external service configuration required.（生产部署前提见 docs/DEPLOYMENT.md「FSRS 词源前提」新小节：python 词库环境 + vocabDBPath + MIMO_API_KEY；13-12 真机检查点承接实际核验）

## Explicit Assumptions Registered（计划带入，供 13-12 检查点）

- RT-10（降级与故障可见配置半边）probe 边缘谓词 unresolved——默认口径 = lesson_unavailable 错误码 + DEPLOYMENT 排障条目 + wiring 测试；真实故障呈现归 13-12/13-13
- 显式假设：RunDue 词库多 category 同名词未消歧（ReviewOpts.Category 留空）——家庭词库当前单 category，Phase 15 批改闭环时再定

## Next Phase Readiness

- 13-11（前端 paused/playback_ack_timeout 处理）：后端词源链路已闭合——生产启动路径 config → pyBridge → PyFSRS → TurnManager(Words) 完整，session_ready/报词/ACK 门/timeout_fired 等事件词表可直接消费
- 13-12（验收检查点）：wordTimeoutMs（HTA_RT_WORDTIMEOUTMS / config）与 VAD 窗口参数的标定入口均已就绪；RT-10 真实故障呈现、评分映射追认、话术听感 UAT 待检查点收口
- Phase 15：T-13-20 残余（scoped unlock / durable recovery）与多 category 同名词消歧承接
- RT-10 为本计划声明的唯一 requirement（配置半边组件级证据齐备）；共享 ID 勾选由 orchestrator 按 shared-ID 门槛收口

## Self-Check: PASSED

- [x] 6 个变更文件全部在磁盘且已提交（新建 pkg/rt/mimo_adapters_test.go 存在）；go.mod/go.sum/STATE.md/ROADMAP.md 零变化（git diff 82788d0..HEAD 核实）
- [x] 提交 ccda0e0 / 3120b25 / dab7798 均在 git log（HEAD=dab7798）；commits: 3 为实测值（git rev-list --count 82788d0..HEAD）
- [x] 三任务验收标准逐条复核：config.go 无 LessonWords 字段与 1..10 校验、含 wordTimeoutMs 叶子绑定；mimo_adapters.go 含 PyFSRS/NewPyFSRS/var _ WordSource、FetchLockHours=1 非 peek、空 results 不触发 RunReview、partial/zero 如实不盲重试；main.go 含 NewPyFSRS 调用与 Words/WordTimeout 注入、零 LessonWords 引用；DEPLOYMENT.md 四关键词命中
- [x] 计划级验证命令全部按原文通过：go test ./pkg/infra/ -run "TestRT|TestTLS|TestLoadConfig" -v；go test ./pkg/rt/ -run "TestPyFSRS" -v（6 PASS）；go build ./... && go vet ./cmd/service/ ./pkg/rt/ && go test ./cmd/service/ -short && go test ./pkg/rt/ ./pkg/infra/ -short；grep×3 命中
- [x] 零回归：go test ./pkg/... ./cmd/... -short -count=1 全绿（16 包全 ok）；go vet ./... exit 0；wiring 三用例非 short 模式额外实测通过；`git diff 82788d0..HEAD -- pkg/web` 为空（零前端改动）
- [x] 无未登记 stub（13-08 Words 注入缺位销项）；无计划外威胁面新增（零新增依赖、零新端点、零 wire 形状变化）

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-05*
