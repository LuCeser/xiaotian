---
phase: 13-realtime-minimal-loop
plan: 06
subsystem: infra
tags: [tls, https, listenandserve-tls, config, websocket-wiring, mimo-adapters, deployment]

# Dependency graph
requires:
  - phase: 13-realtime-minimal-loop/13-01
    provides: pkg/lesson Migrate/NewStore/NewExecutor（EnsureLesson 在 Store、工具对在 Executor）
  - phase: 13-realtime-minimal-loop/13-02
    provides: mimo WithChatModel/PresetDictationTeacher/WithASRLanguage 选项词汇
  - phase: 13-realtime-minimal-loop/13-03
    provides: pkg/rt 传输端点/NewHandler/ServerConfig + ASRProvider/LLMProvider/TTSSynthesizer 接口
  - phase: 13-realtime-minimal-loop/13-04
    provides: rt.NewTurnManager/NewEventRecorder/TurnManagerConfig + nil providers→voice_unavailable 语义
  - phase: 13-realtime-minimal-loop/13-05
    provides: pkg/web/static/tutor/ 前端（data-page="tutor"）
provides:
  - "infra.TLSConfig + ServerConfig.TLS（L-7 三态：nil=HTTP、成对=HTTPS、空块/半对=ErrConfigInvalid）"
  - "infra.RTConfig + Config.RT（localhost 对默认、禁 \"*\"、r0-test 三固定词、10 叶子键全接 bindEnvForAllLeaves）"
  - "rt.MimoASR/MimoLLM/MimoTTS 三适配器 + rt.LessonTools（pkg/lesson→rt.LessonExecutor 编译期对接）"
  - "main.go 运行时接线：/rt 路由、/tutor/ 静态入口、lesson.Migrate、TLS 分支（Stat 预检 + ListenAndServeTLS，绝不静默回退 HTTP）"
  - "cmd/service/rt_wiring_test.go（自签证书 HTTPS/tutor//rt 三用例 + 无 TLS 基线 + 缺证书退出码 1）"
  - "docs/DEPLOYMENT.md「HTTPS / TLS 与 /tutor/ 入口」章节（证书部署、allowedOrigins 域名、h2 排查、无 key 降级说明）"
affects: [13-realtime-minimal-loop/13-07 (真机验收消费全部接线), RT-01, RT-10]

actuals:
  tokens: 13248     # chars/4 over the realized production diff (52994 chars, base d0e4ba5..a69746b)
  tasks: 3
  commits: 3        # MEASURED: git rev-list --count d0e4ba5..a69746b (docs commit follows)
  plan_head_before: d0e4ba5455eaf7b3077b1f1a74f5d791752fca55
  plan_head_after: a69746b4d3f6c3388657b28ee5cf3de922e7519

tech-stack:
  added: []         # 零新增依赖（crypto/ecdsa+x509 为 stdlib；gorilla/websocket 存量）
  patterns:
    - "TLS 三态契约：配置层 nil/成对/残缺三态写死，空 yaml 块经 viper IsSet 兜底（Unmarshal 的 AllKeys 展平会丢弃空 map）"
    - "供应商 wire 细节止步适配层：MimoTTS 是全链路唯一 base64 解码点（M-1），ASR 的 WAV 封装在 MimoASR 内完成"
    - "生产 LessonExecutor = Store+Executor 双依赖小适配器 + 三个方法表达式编译期签名检查（lesson 包方法拆分所致）"

key-files:
  created:
    - pkg/rt/mimo_adapters.go
    - cmd/service/rt_wiring_test.go
  modified:
    - pkg/infra/config.go
    - pkg/infra/config_test.go
    - cmd/service/main.go
    - docs/DEPLOYMENT.md

key-decisions:
  - "L-7 空块兜底落在 LoadConfig 而非仅 Validate：viper Unmarshal 走 AllKeys（仅叶子键），yaml `tls: {}` 空块没有叶子键、对解码完全不可见（实测探针确认），指针不分配即静默禁用；LoadConfig 在 Unmarshal 后以 v.IsSet(\"server.tls\") && TLS==nil 拒绝启动（Rule 1，见 Deviations #1）"
  - "rt.LessonExecutor 的生产实现是 rt.LessonTools（Store.EnsureLesson + Executor 读写对的适配器）而非计划原文的 `var _ LessonExecutor = (*lesson.Executor)(nil)`——后者无法编译（EnsureLesson 在 *lesson.Store，13-01 拆分），rt.LessonExecutor godoc 本就预告\"a small adapter over both\"；三个方法表达式 var 保持编译期签名对接检查（Rule 1，见 Deviations #2）"
  - "rt 复用共享 mimoClient（M-7 决策注释固定于 main.go 构造点）：Timeout=max(voice 15s, photo 20s)=20s；真机若现 20s 截断则另立 client + RTConfig.timeout"
  - "EventRecorder 的 sessionID 用 main.go 自增计数器生成 rt-N：与 rt 包内部计数器逐次同步（一次 factory 调用恰好一次 NewTurnManager），事件流 session_id 与 session_ready 一致"
  - "rt_wiring_test 的 yaml staticDir/scriptsDir 为运行时从 os.Getwd 计算的绝对路径（cmd/service 距仓库根两级）；自撰 yaml 必含 python 三必填键，vocabScript/pregenScript 由 DefaultConfig 兜底（M-4 已按裁定执行）"

patterns-established:
  - "Pattern: 新配置段的完整四件套——struct+默认值+Validate 三态/词表规则+bindEnvForAllLeaves 分组注释与 Pitfall 6 锚定测试（TestRTAndTLSEnvBinding）"
  - "Pattern: 启动期证书 Stat 预检失败 = 显式 return err + 日志，绝不静默降级（trusted-entrance 禁令的服务端执法）"

requirements-completed: [RT-01, RT-10]   # 拷贝自 PLAN frontmatter requirements；共享 ID 由 orchestrator 按 #2388 门统一勾选

coverage:
  - id: D1
    description: "server.tls + rt 配置段：L-7 三态校验（nil/成对/空块/半对）、RTConfig 默认值（localhost 对、65551、10s/30s、r0-test 三词）与全部 10 个叶子键的 env 绑定"
    requirement: RT-01
    verification:
      - kind: unit
        ref: "pkg/infra/config_test.go#TestTLSThreeStateValidate / TestLoadConfigTLSEmptyBlockRejected / TestDefaultRTConfig / TestRTConfigValidate / TestLoadConfigRTEmptySectionTakesDefaults"
        status: pass
      - kind: unit
        ref: "pkg/infra/config_test.go#TestRTAndTLSEnvBinding（HTA_RT_LESSONID / HTA_SERVER_TLS_* 经 LoadConfig 生效）"
        status: pass
      - kind: unit
        ref: "command: go test ./pkg/infra/ -run \"TestTLS|TestRT\" -count=1 && go test ./pkg/infra/ -short -count=1"
        status: pass
    human_judgment: false
  - id: D2
    description: "运行时接线：mimo.Client 三适配器（WrapWAV 封 16k WAV、Chat 直通、base64 唯一解码点）+ LessonTools 编译期对接 + main.go 挂 /rt、/tutor/、lesson.Migrate、TLS 分支（Stat 预检 + ListenAndServeTLS + tls=true/false 日志字段）"
    requirement: RT-01
    verification:
      - kind: unit
        ref: "command: go build ./... && go vet ./cmd/service/ ./pkg/rt/ && go test ./pkg/rt/ ./pkg/lesson/ -count=1"
        status: pass
      - kind: unit
        ref: "grep 断言：main.go 含 /rt、/tutor/、ListenAndServeTLS、lesson.Migrate 四符号；mimo_adapters.go 含三个 var _ 接口检查 + WrapWAV 调用 + LessonTools var _"
        status: pass
    human_judgment: false
  - id: D3
    description: "HTTPS 接线测试闭环：自签证书下 /tutor/ 200 且含 data-page=\"tutor\"、/rt 挂载（普通 GET 得 400/403 非 404）、旧学生页 / 200；无 TLS 基线（M-4 对照）可启动；缺证书文件 run 返回 1"
    requirement: RT-01
    verification:
      - kind: integration
        ref: "cmd/service/rt_wiring_test.go#TestRunTLSHTTPSServesTutorAndRT / TestRunNoTLSHTTPBaseline / TestRunTLSMissingCertFails"
        status: pass
      - kind: integration
        ref: "command: go test ./cmd/service/ -run \"TestRunTLS|TestRunNoTLS\" -count=1 -v（3 PASS）&& go test ./cmd/service/ -short -count=1"
        status: pass
    human_judgment: false
  - id: D4
    description: "无 MIMO_API_KEY 降级路径：服务照常启动、/rt 照常挂载、session_init 返回 voice_unavailable、双 Warn 日志（RT-10 透明性服务端半边）"
    requirement: RT-10
    verification:
      - kind: integration
        ref: "cmd/service/rt_wiring_test.go 三个用例均在无 key 环境跑通（服务启动 + /rt 挂载断言）；voice_unavailable 单元语义由 13-04 turn_test 锚定"
        status: pass
    human_judgment: true
    rationale: "tutor 页对降级原因的可见呈现与真实故障可见性归 13-07 Task 3 步骤 7 真机验收（计划 Flagged Assumptions 明示）；自动化只能证明降级路径可编译可启动"
  - id: D5
    description: "docs/DEPLOYMENT.md「HTTPS / TLS 与 /tutor/ 入口」章节：server.tls 样例（fullchain/privkey）、allowedOrigins 证书域名要求、证书绝不入 git/.planning、systemd 零改动、部署核验步骤、h2 排查首查项、无 key 降级说明"
    requirement: RT-01
    verification:
      - kind: other
        ref: "grep 断言四关键词全命中：fullchain.pem+privkey.pem、allowedOrigins、TLSNextProto、NEVER enter git"
        status: pass
    human_judgment: false

# Metrics
duration: 21 min
completed: 2026-10-02
status: complete
---

# Phase 13 Plan 06: 服务接线（config TLS/RT + mimo 适配器 + /rt 挂载 + TLS）Summary

**13-01～13-05 的零件接进运行时：infra config 新增 server.tls（L-7 三态：nil=HTTP、成对=HTTPS、空块/半对拒绝启动）与 rt 段（localhost 对默认 + r0-test 三固定词 + 10 叶子键 env 绑定），rt.MimoASR/LLM/TTS 三适配器（全链路唯一 base64 解码点）+ LessonTools 编译期对接 pkg/lesson，main.go 挂 /rt、/tutor/、lesson.Migrate 与 ListenAndServeTLS 分支（证书 Stat 预检，绝不静默回退 HTTP），自签证书 HTTPS 测试三件套与部署文档落地**

## Performance

- **Duration:** 21 min
- **Started:** 2026-10-02T02:32:23Z
- **Completed:** 2026-10-02T02:53:04Z
- **Tasks:** 3 / 3
- **Files:** 6（新建 2 / 修改 4），+1134 行

## Accomplishments

- 配置面就绪：TLSConfig/RTConfig 结构、默认值（65551 消息上限、10s 握手、30s 心跳、r0-test 三词「美丽/勇敢/聪明」）、Validate 三态与词表规则、bindEnvForAllLeaves 新增 10 键并配 TestRTAndTLSEnvBinding 锚定（Pitfall 6）
- 适配层落地：MimoASR 以 voice.WrapWAV 封 16kHz WAV 整段送 MiMo ASR（zh）；MimoLLM 直通 ChatOption（注入部署模型）；MimoTTS 解 base64 后经 AudioChunkFunc 上交裸 s16le——M-1 裁定的全链路唯一 base64 解码点；LessonTools 满足 rt.LessonExecutor 且三个方法表达式把 pkg/lesson 签名钉在编译期
- 运行时接线：database.Migrate 之后 lesson.Migrate + Store/Executor/LessonTools；有 key 时三适配器构造（M-7 决策注释固定于构造点）；rt.NewHandler 的 ManagerFactory 每连接产出 TurnManager + EventRecorder（rt-N 会话号）；/rt 与 /ws 并列挂载、无 key 也挂（降级透明）；/tutor/ StripPrefix 挂 staticDir/tutor；TLS 分支 Stat 预检两文件 + ListenAndServeTLS + tls=true/false 日志字段，Pitfall 8 h2 排查注释固定在调用点
- 测试与文档：TestRunNoTLSHTTPBaseline（M-4 对照，证明 yaml 骨架本身可启动）、TestRunTLSHTTPSServesTutorAndRT（自签 ECDSA 证书，/tutor/ 200+data-page、/rt 非 404、/ 200）、TestRunTLSMissingCertFails（退出码 1）；DEPLOYMENT.md 新章节覆盖证书部署到核验全流程
- 存量零回归：go build ./... / go vet ./... / go test ./pkg/... ./cmd/... -short 全绿（15 包）；npm test exit 0

## Task Commits

Each task was committed atomically:

1. **Task 1: infra config — server.tls 与 rt 配置段** - `2b0b9fa` (feat)
2. **Task 2: mimo_adapters.go + main.go 接线（/rt、/tutor/、TLS、lesson 迁移）** - `1738f92` (feat)
3. **Task 3: TLS 接线测试 + DEPLOYMENT.md 证书部署段** - `a69746b` (test)

**Plan metadata:** 本 SUMMARY 提交（docs）

## Files Created/Modified

- `pkg/infra/config.go` - TLSConfig（成对语义 godoc）、ServerConfig.TLS、RTConfig 七字段 + Validate、Config.RT、DefaultConfig 填充、Config.Validate 三态与 RT 委派、bindEnvForAllLeaves 10 新键、LoadConfig 的 IsSet 空块兜底
- `pkg/infra/config_test.go` - TestTLSThreeStateValidate（三态+半对）、TestLoadConfigTLSEmptyBlockRejected（viper 层）、TestDefaultRTConfig（默认词长 3）、TestRTConfigValidate（7 拒绝用例）、TestLoadConfigRTEmptySectionTakesDefaults、TestRTAndTLSEnvBinding
- `pkg/rt/mimo_adapters.go` - MimoASR/MimoLLM/MimoTTS + 三个 var _ 接口检查 + LessonTools（var _ LessonExecutor + 三个方法表达式签名检查）
- `cmd/service/main.go` - lesson.Migrate/NewStore/NewExecutor/NewLessonTools、mimo 分支内三适配器（M-7 注释）、rt.NewHandler+ManagerFactory（rtSessionCounter）、/rt 与 /tutor/ 挂载、TLS Stat 预检 + ListenAndServeTLS 分支 + tls 日志字段、无 key 双 Warn
- `cmd/service/rt_wiring_test.go` - 自签证书生成、自撰 yaml 骨架（绝对路径 staticDir）、三用例 + 端口占用 t.Skip 约定
- `docs/DEPLOYMENT.md` - 「HTTPS / TLS 与 /tutor/ 入口」章节（配置样例、allowedOrigins、证书纪律、systemd、核验、h2 排查、降级说明）

## Decisions Made

- 见 frontmatter key-decisions（L-7 IsSet 兜底、LessonTools 形态、M-7 共享 client、rt-N 会话号、测试绝对路径）。

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] yaml `tls: {}` 空块对 viper Unmarshal 不可见，L-7 三态在结构体层无法覆盖**
- **Found during:** Task 1（TestLoadConfigTLSEmptyBlockRejected 首跑失败）
- **Issue:** viper 的 Unmarshal 以 AllKeys（仅叶子键）构建解码映射，`server.tls: {}` 没有任何叶子键，整个空块被丢弃——*TLSConfig 指针保持 nil，TLS 被静默禁用，恰是 must_haves 明令禁止的形态；计划原文的 Validate 三态检查永远看不到这个输入
- **Fix:** 以临时探针测试实证 viper 行为（IsSet 对空块 true、对无配置/env 均 false）后，在 LoadConfig 的 Unmarshal 之后加兜底：`cfg.Server.TLS == nil && v.IsSet("server.tls")` 即返回 ErrConfigInvalid（env 半对会先经 AllKeys 进入结构体走 Validate 配对检查，无误伤）；探针文件已删除
- **Files modified:** pkg/infra/config.go
- **Verification:** TestLoadConfigTLSEmptyBlockRejected 绿；TestRTAndTLSEnvBinding（env 对进入结构体路径）与 TestLoadConfigRTEmptySectionTakesDefaults（无 tls 块 TLS=nil）同时绿，证明兜底无误伤
- **Committed in:** 2b0b9fa（Task 1 commit）

**2. [Rule 1 - 契约矛盾] `var _ LessonExecutor = (*lesson.Executor)(nil)` 无法编译——EnsureLesson 在 *lesson.Store**
- **Found during:** Task 2（mimo_adapters.go 编写期）
- **Issue:** 计划 action 1 的对接检查逐字执行必然编译失败：13-01 把 EnsureLesson 放在 Store、GetLessonState/AdvanceWord 放在 Executor，单一类型不可能满足三方法接口；rt.LessonExecutor 的 godoc（13-04 写就）已预告生产实现是"a small adapter over both"
- **Fix:** 新增 rt.LessonTools{store, exec} 小适配器实现三方法并以 `var _ LessonExecutor = (*LessonTools)(nil)` 完成接口检查；另以三个方法表达式 var（(*lesson.Store).EnsureLesson 等）把 pkg/lesson 方法签名钉在编译期——key_link 的"编译期对接检查"语义完整保留
- **Files modified:** pkg/rt/mimo_adapters.go
- **Verification:** go build ./... 绿；go vet ./pkg/rt/ 绿；签名漂移即编译失败
- **Committed in:** 1738f92（Task 2 commit）

---

**Total deviations:** 2 auto-fixed（2 处为实现层修正，均不改变架构与验收语义）
**Impact on plan:** 两处都是计划意图与既有代码事实的落差弥合：L-7 禁令因 viper 行为需要双层数防（结构体三态 + LoadConfig IsSet 兜底）；lesson 对接检查因 13-01 的方法拆分需要适配器形态。无范围蔓延。

## Issues Encountered

- rt_wiring_test 首版 yaml 用相对路径 `../pkg/web/static`：`cmd/service/..` 是 `cmd` 而非仓库根（少一级），baseline 测试启动即 "static directory not accessible"。改为运行时从 os.Getwd 计算绝对路径（`filepath.Dir(filepath.Dir(wd))` 为仓库根），测试内自愈，未形成提交级偏差
- TestRunTLSMissingCertFails 初次与其他用例一同失败正是 M-4 预警的混淆形态（骨架问题 vs 证书问题）——baseline 对照用例修复后，该用例的退出码 1 才可归因于证书缺失，验证了计划 M-4 裁定的必要性

## User Setup Required

None - no external service configuration required.（生产 HTTPS 部署步骤见 docs/DEPLOYMENT.md 新章节：证书路径配置 + allowedOrigins 列入证书域名；13-07 Task 2 的 checkpoint:human-verify 承接真机证书核验）

## Threat Mitigations Delivered

| Threat | Disposition | Where |
|--------|-------------|-------|
| T-13-06 (凭据泄漏进 git/规划文档, high) | mitigate | 测试仅用 t.TempDir() 自签临时证书；DEPLOYMENT.md 明文"NEVER enter git/.planning"；config 只存路径不存凭据 |
| T-13-01c (/rt 生产 Origin, high) | mitigate | RTConfig.Validate 禁 "*" 且要求非空 + rt.NewHandler 双层执法；部署文档强制证书域名 |
| 静默降级禁令（prohibition #1） | mitigate | TLS 三态 + Stat 预检 + ListenAndServeTLS 失败显式退出（exit 1），无任何 HTTP 回退路径 |
| 降级透明禁令（prohibition #2） | mitigate | 无 key 时 /rt 照常挂载 + session_init voice_unavailable + 双 Warn 日志；tutor 页呈现归 13-07 验收 |

## Next Phase Readiness

- 13-07 只差部署与真机：本地 localhost HTTP 全链可启动（baseline 测试锚定）；HTTPS 入口经自签证书测试闭环，VPS 换业主证书即切换（DEPLOYMENT.md 步骤可执行）
- RT-01 服务端半边证据齐备（TLS 接线 + 缺证书失败 + /tutor//rt 挂载）；「用户证书真机可信」整体验收归 13-07 Task 2；RT-10 真实故障可见性归 13-07 Task 3 步骤 7
- RT-01/RT-10 共享 ID（13-05/13-07 亦声明）由 orchestrator 按 #2388 门统一勾选，本 SUMMARY 仅按计划 frontmatter 拷贝

## Self-Check: PASSED

- [x] 6 个任务文件全部存在于磁盘并已提交（git status 干净）
- [x] git log --all --grep="13-06" 返回 3 个任务提交（2b0b9fa / 1738f92 / a69746b）
- [x] commits: 3 为实测值（git rev-list --count d0e4ba5..a69746b）
- [x] 计划级验证全通过：go build ./... / go vet ./... / go test ./pkg/... ./cmd/... -short -count=1（15 包全 ok）/ npm test（exit 0）
- [x] 三任务验收标准逐条复核：grep 四符号、三个 var _ + WrapWAV、HTTPS 三断言、缺证书退出码 1、DEPLOYMENT 四关键词、bindEnv 10 键、TestRTAndTLSEnvBinding、默认 LessonWords 长度 3

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-02*
