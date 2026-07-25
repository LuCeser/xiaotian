---
phase: 03-python-cli-integration
verified: 2026-07-25T10:40:00Z
status: human_needed
score: 4/4 must-haves verified at code level
behavior_unverified: 1 # RunDue/RunReview/RunPregenerate 的真 Python+venv+ffmpeg 端到端在 1C/1G VPS 上的运行时行为
overrides_applied: 0
human_verification:
  - test: "在 VPS 部署期 make py-bootstrap 建 venv + pip install fsrs/requests/edge-tts，配置 VOLCENGINE_TTS_* 密钥后，经 pybridge 触发 vocab_manager.py due/review 与 pregenerate_dictation.py generate 端到端执行"
    expected: "due 返回 DueResult.Cards（数组），review 返回 ReviewResult.Updated（数组），pregenerate 返回 PregenResult.Paths（mp3 绝对路径列表）；超时/非零退出/空 stdout 经 infra.Logger 落结构化日志（component=pybridge，error_type=timeout/nonzero_exit/other）；VOLCENGINE_TTS_* 密钥不出现在任何日志字段"
    why_human: "Windows dev 机无 venv/ffmpeg/Volcengine 密钥；CI 用 POSIX shell fake（internal/pytest/fake_*.sh）+ 黄金 fixture 验证桥的解析/超时/错误分类逻辑，但真 Python 解释器在 venv 内对 vendored 脚本的端到端行为只能由部署 UAT 触达（vendored 脚本已与上游逐字节 diff 为空，per 03-01 checkpoint）"
---

# Phase 3: Python CLI Integration 验证报告

**Phase 目标:** Subprocess bridge to existing vocab_manager.py and pregenerate_dictation.py
**验证时间:** 2026-07-25T10:40:00Z
**状态:** human_needed
**是否复验:** 否（首次验证）

## 目标达成度

### 可观察真值（Observable Truths）

| # | 真值 | 状态 | 证据 |
| --- | --- | --- | --- |
| 1 | System executes vocab_manager due command and returns word selection results (SC-1, PY-02) | ✓ VERIFIED (code) + ⚠️ PRESENT_BEHAVIOR_UNVERIFIED (VPS 端到端) | `(*Bridge).RunDue` @ `pkg/pybridge/vocab.go:119`；`dueArgs` @ `vocab.go:63-87` 构造 `[vocabScript, "--db", dbPath, "due", ...6 DueOpts flags]`（`--db` 在子命令前——vocab_manager.py:718 全局参数顺序）；TestDueArgs 8 子测试 + TestRunDueParseFromFixtures 全 PASS；extractTrailingJSON 提末尾 JSON + json.Unmarshal 进 DueResult.Cards |
| 2 | System executes vocab_manager review command with feedback parameters (SC-2, PY-03) | ✓ VERIFIED (code) + ⚠️ PRESENT_BEHAVIOR_UNVERIFIED (VPS 端到端) | `(*Bridge).RunReview` @ `vocab.go:272`；`reviewArgs` @ `vocab.go:193-237` 实现 `--results`（≤4096B 且 ≤100 条）/`--file`（os.CreateTemp + cleanup）自动切换；4 种 rating（again/good/hard/easy）原样序列化不改写（RESEARCH Pitfall 4）；TestReviewArgs 6 子测试（含 all_four_ratings_forwarded_verbatim + large_results_*_goes_to_file）+ TestRunReviewParseFromFixtures/CleanJSON 全 PASS |
| 3 | System integrates with pregenerate_dictation.py for TTS audio generation (SC-3, PY-04) | ✓ VERIFIED (code) + ⚠️ PRESENT_BEHAVIOR_UNVERIFIED (VPS 端到端) | `(*Bridge).RunPregenerate` @ `pkg/pybridge/pregenerate.go:165`；`pregenArgs` @ `pregenerate.go:79-113` 构造 `[pregenScript, "generate", "--words", <JSON array>, "--language", ...optional]`（无全局 --db，与 vocab 路径不同）；splitNonEmptyLines 按行解析 mp3 路径（绝不用 json.Unmarshal——Pitfall 3）；`Bridge.pregenSem` cap=1 串行化 + 空 stdout → ErrPythonEmpty（Pitfall 6）；TestPregenArgs 8 子测试 + TestRunPregenerateParse 3 子测试 + TestClassifyPregenResult 6 子测试全 PASS |
| 4 | Subprocess errors are captured and surfaced to logging system (SC-4) | ✓ VERIFIED | `(*Bridge).logResult` @ `bridge.go:333-363` 实现 5 类分类（ok/timeout/nonzero_exit/other/default），全部经 infra.Logger + infra.F() 落结构化字段（component=pybridge，command/exit_code/duration_ms/error_type/stderr/stdout/error 白名单，T-03-08）；RunDue/RunReview/RunPregenerate 各 defer b.logResult 调用一次；TestLogResultClassification + TestLogResultNoSecretLeak 全 PASS |

**Score:** 4/4 truths verified at code level（1 条带 VPS 端到端 behavior_unverified，转入 human_needed）

### 必需工件（Required Artifacts）

| Artifact | Expected | Status | Details |
| -------- | -------- | ------ | ------- |
| `pkg/infra/config.go`（PythonConfig） | 13 操作性字段 + DefaultConfig 默认值 + Validate 校验 + bindEnv python.* | ✓ VERIFIED | PythonConfig @ lines 107-172（13 字段含 Interpreter/ScriptsDir/VocabScript/PregenScript/VocabDBPath/DataDir/CacheDir/ManifestDir/EnvFile/DueTimeout/ReviewTimeout/PregenTimeout/MaxConcurrent）；DefaultConfig @ 358-365 设 VocabScript/PregenScript 默认名 + DueTimeout=10s/ReviewTimeout=10s/PregenTimeout=120s/MaxConcurrent=2；Validate @ 548-573；bindEnv @ 645-657 注册全部 13 个 python.* 叶子键 |
| `pkg/pybridge/executor.go` | Executor 接口 + 4 sentinel + ExitError + typed opts/results | ✓ VERIFIED | Executor @ 53-57 含 RunDue/RunReview/RunPregenerate 三方法；4 sentinel（ErrPythonExit/ErrPythonTimeout/ErrPythonJSONParse/ErrPythonEmpty）@ 198-221 均以句号结尾；ExitError @ 234-252 含 Cmd/ExitCode/Stderr/Err + Error()+Unwrap() |
| `pkg/pybridge/parse.go` | extractTrailingJSON（尾部回扫+括号栈）+ splitNonEmptyLines | ✓ VERIFIED | extractTrailingJSON @ 41-96（字符串字面量状态跟踪 + 括号栈计数处理嵌套）；splitNonEmptyLines @ 115-126（TrimSpace + 过滤空行） |
| `pkg/pybridge/semaphore.go` | chan struct{} cap N + ctx-aware acquire | ✓ VERIFIED | semaphore @ 19；newSemaphore @ 28-33（n<=0 panic）；acquire @ 54-67（fast-path 已取消 ctx 检查 + select { s<-struct{}{} / <-ctx.Done() }） |
| `pkg/pybridge/bridge.go` | Bridge struct + NewBridge 纯构造器 + childEnv + run + logResult | ✓ VERIFIED | Bridge @ 81-107（cfg/logger/sem/pregenSem 四字段）；NewBridge @ 127-170（cfg 逐字段校验 + nil-logger→NewNoOpLogger + newSemaphore）；childEnv @ 198-228（PYTHONUTF8/PYTHONIOENCODING/PYTHONUNBUFFERED + DICTATION_* + pythonSecretEnvKeys 经 os.Getenv 透传）；run @ 268-308（exec.CommandContext argv 数组 + setProcessGroup + semaphore 门）；logResult @ 333-363（5 类分类）；`var _ Executor = (*Bridge)(nil)` 编译期检查 @ 45 |
| `pkg/pybridge/bridge_linux.go` | //go:build linux + Setpgid+Cancel+WaitDelay | ✓ VERIFIED | 首行 `//go:build linux`；setProcessGroup @ 50-60 设 SysProcAttr{Setpgid:true} + Cancel=syscall.Kill(-pid, SIGKILL) + WaitDelay=3s |
| `pkg/pybridge/bridge_other.go` | //go:build !linux 空实现 | ✓ VERIFIED | 首行 `//go:build !linux`；setProcessGroup(cmd) 空函数体（防 Windows/macOS 编译失败） |
| `pkg/pybridge/vocab.go` | RunDue + RunReview + dueArgs + reviewArgs | ✓ VERIFIED | RunDue @ 119-160；RunReview @ 272-318；dueArgs @ 63-87（`--db` 在 due 子命令前，标志透传）；reviewArgs @ 193-237（自动 --results vs --file 切换 + cleanup 闭包） |
| `pkg/pybridge/pregenerate.go` | RunPregenerate + pregenArgs + classifyPregenResult | ✓ VERIFIED | RunPregenerate @ 165-189（pregenSem cap=1 串行化 + 调 classifyPregenResult）；pregenArgs @ 79-113（generate 子命令 + --words JSON 数组 + --language）；classifyPregenResult @ 215-258（Timeout/Exit/Empty/success 四类，splitNonEmptyLines 按行解析） |
| `cmd/service/main.go`（pyBridge 接线） | NewBridge 构造 + 错误处理 + 持有引用 | ✓ VERIFIED | pyBridge 接线块 @ 103-118（infra.NewServer 后 pybridge.NewBridge(cfg.Python, logger) + infra.F + infra.ErrField 错误处理 + `_ = pyBridge` 持有引用供 Phase 5/7）；import `github.com/xiaotian/home-tutor-agent/pkg/pybridge` @ 29 |
| `third_party/python/vocab_manager.py` | vendor 上游脚本 | ✓ VERIFIED | `diff -q` 与 `../xiaotian-skills/shared/dictation/scripts/vocab_manager.py` 输出空（逐字节一致，31336 bytes） |
| `third_party/python/pregenerate_dictation.py` | vendor 上游脚本 | ✓ VERIFIED | `diff -q` 与 `../xiaotian-skills/shared/dictation/scripts/pregenerate_dictation.py` 输出空（逐字节一致，50183 bytes） |
| `third_party/python/requirements.txt` | fsrs/requests/edge-tts + import 注释 | ✓ VERIFIED | 含 `fsrs>=0.0.1` / `requests>=2.31.0` / `edge-tts>=6.1.0`，每项上方注释标注被哪个脚本哪一行 import |
| `third_party/python/.env.example` | 非密钥 DICTATION_* 路径 + VOLCENGINE 注释掉 | ✓ VERIFIED | 所有 VOLCENGINE_TTS_* 行注释掉 + D-05 不变式声明 |
| `Makefile`（py-bootstrap/py-sync） | venv 建立 + 上游同步 target | ✓ VERIFIED | py-bootstrap target @ line 49（venv 创建 + pip install + pip freeze 锁定）；py-sync target @ line 64（UPSTREAM_SCRIPTS 默认 `../xiaotian-skills/shared/dictation/scripts`）；.PHONY 行含两个 target |
| `config.example.yaml`（python: 块） | 含 python: 操作性配置，无密钥 | ✓ VERIFIED | python: 块 @ line 76，每字段注释引用 D-xx 决策，密钥位置明确声明经部署环境注入（D-05） |
| `internal/pytest/fake_vocab.sh` | POSIX shell fake for vocab_manager | ✓ VERIFIED | 83 行 POSIX sh，dispatch due（echo JSON 数组）/review（Warning 前缀 + 末尾 JSON）/error（exit 1）三路径 |
| `internal/pytest/fake_prege.sh` | POSIX shell fake for pregenerate | ✓ VERIFIED | 84 行 POSIX sh，default 路径 echo 3 mp3 路径 + --lock-test 路径空 stdout（Pitfall 6）+ --fail 路径 exit 1 |

### Key Link 验证（Wiring）

| From | To | Via | Status | Details |
| ---- | --- | --- | ------ | ------- |
| main.go | pybridge.Bridge | `pyBridge, err := pybridge.NewBridge(cfg.Python, logger)` @ main.go:110 | ✓ WIRED | 错误处理含 infra.F + infra.ErrField @ 113-115；`_ = pyBridge` 持有引用 @ 118 供 Phase 5/7 |
| Bridge.run | exec.CommandContext | `exec.CommandContext(cctx, name, args...)` @ bridge.go:285（argv 数组形式，绝不 shell） | ✓ WIRED | setProcessGroup(cmd) @ 290（build-tag 分发）；childEnv 注入 cmd.Env @ 292 |
| Bridge.run | Bridge.sem（D-02 信号量） | `b.sem.acquire(ctx)` + `defer b.sem.release()` @ bridge.go:272-275 | ✓ WIRED | semaphore acquire 在 spawn 前，防 fork-bomb |
| RunDue/RunReview | extractTrailingJSON | `extractTrailingJSON(stdout)` @ vocab.go:150, 308 | ✓ WIRED | 提末尾 JSON 后 json.Unmarshal 进 typed result |
| RunPregenerate | splitNonEmptyLines | `splitNonEmptyLines(stdout)` @ pregenerate.go:248（在 classifyPregenResult 内） | ✓ WIRED | 按行解析路径，绝不 json.Unmarshal（Pitfall 3） |
| RunPregenerate | Bridge.pregenSem（cap=1） | `b.pregenSem.acquire(ctx)` + `defer b.pregenSem.release()` @ pregenerate.go:178-181 | ✓ WIRED | 在 b.run 之前 acquire，flock 串行化 |
| RunDue/RunReview/RunPregenerate | Bridge.logResult | 各方法 defer b.logResult(command, ...) 一次（command=due/review/pregenerate） | ✓ WIRED | vocab.go:124, 283；pregenerate.go:186；错误分类落结构化日志（SC-4） |
| childEnv | pythonSecretEnvKeys（D-05 密钥经 os.Getenv） | `for _, k := range pythonSecretEnvKeys { if v := os.Getenv(k); v != "" { env = append(env, k+"="+v) } }` @ bridge.go:221-225 | ✓ WIRED | 密钥绝不入 Config/日志/ExitError |
| PythonConfig（Plan 01） | NewBridge（Plan 03） | NewBridge cfg 形参逐字段校验 + 持有 @ bridge.go:127-170 | ✓ WIRED | 13 字段全部消费 |
| config.example.yaml python: 块 | Viper → PythonConfig | bindEnvForAllLeaves 注册 13 个 python.* 键 @ config.go:645-657 | ✓ WIRED | Layered loader：defaults < file < env |

### Data-Flow Trace（Level 4）

| Artifact | Data Variable | Source | Produces Real Data | Status |
| -------- | ------------- | ------ | ------------------ | ------ |
| RunDue | DueResult.Cards | `json.Unmarshal(raw, &result.Cards)`，raw 来自 extractTrailingJSON(stdout)，stdout 来自 cmd.Run() 捕获 | 在测试中用 testdata/due_cards.json + testdata/review_with_warnings.txt 黄金 fixture 驱动 → 是 | ✓ FLOWING (code) |
| RunReview | ReviewResult.Updated | 同上，Unmarshal 进 UpdatedCard 数组 | fixture 覆盖 Warning 混合 stdout 路径 → 是 | ✓ FLOWING (code) |
| RunPregenerate | PregenResult.Paths | `splitNonEmptyLines(stdout)`，stdout 来自 cmd.Run() 捕获 | testdata/pregenerate_paths.txt fixture 驱动 → 是 | ✓ FLOWING (code) |
| logResult | 结构化字段 | `infra.F("component", "pybridge")` 等 7 个固定字段 + infra.ErrField(err) | TestLogResultClassification + TestLogResultNoSecretLeak 用 fakeLogger 断言字段值 → 是 | ✓ FLOWING |

VPS 端到端：真 Python 子进程 stdout → bytes.Buffer → extractTrailingJSON/splitNonEmptyLines → typed result 的数据流路径由代码层 wiring 证明成立；真 Python 解释器在 venv 内对 vendored 脚本的实际输出未在 CI 跑（Windows dev 无 venv），属 UAT。

### 行为点检（Behavioral Spot-Checks）

| 行为 | 命令 | 结果 | 状态 |
| ---- | ---- | ---- | ---- |
| pybridge 全部单测 | `go test -short -count=1 ./pkg/pybridge/` | `ok github.com/xiaotian/home-tutor-agent/pkg/pybridge 0.575s` | ✓ PASS |
| infra 全部单测（含 PythonConfig 守卫） | `go test -short -count=1 ./pkg/infra/` | `ok github.com/xiaotian/home-tutor-agent/pkg/infra 1.239s` | ✓ PASS |
| cmd/service 单测（含 pyBridge 接线 boot 路径） | `go test -short -count=1 ./cmd/service/` | `ok github.com/xiaotian/home-tutor-agent/cmd/service 0.952s` | ✓ PASS |
| TestNewBridgeValidation（9 子测试覆盖 cfg 校验） | `go test -run TestNewBridgeValidation -v ./pkg/pybridge/` | 全 PASS | ✓ PASS |
| TestChildEnvSecrets（D-05 密钥经 os.Getenv 注入） | 同上 | 全 PASS（含 VOLCENGINE_TTS_API_KEY 未设时不出现 + 设了后透传） | ✓ PASS |
| TestLogResultClassification（5 类错误分类） | 同上 | 全 PASS（fakeLogger 收到正确 error_type 字段） | ✓ PASS |
| TestLogResultNoSecretLeak（T-03-08 字段白名单） | 同上 | 全 PASS（含上游 stderr 仿冒 VOLCENGINE 字串也不泄漏到字段） | ✓ PASS |
| TestExtractTrailingJSON（9 子测试含 Pitfall 2 混合 stdout） | 同上 | 全 PASS（json.Valid 验证末尾 JSON） | ✓ PASS |
| TestDueArgs（8 子测试，--db 在 due 前） | 同上 | 全 PASS | ✓ PASS |
| TestReviewArgs（6 子测试，4 rating 透传 + --file 大负载） | 同上 | 全 PASS | ✓ PASS |
| TestPregenArgs（8 子测试，generate 子命令 + --words JSON） | 同上 | 全 PASS | ✓ PASS |
| TestClassifyPregenResult（6 子测试，含 Pitfall 6 空 stdout→ErrPythonEmpty） | 同上 | 全 PASS | ✓ PASS |
| TestSemaphoreAcquireRelease/BlockedThenReleased/OnCancelledCtx/ZeroCap/Negative/ConcurrentAcquirers | 同上 | 全 PASS（6 项覆盖 ctx-aware + panic 守卫 + 并发） | ✓ PASS |
| TestPythonConfigHasNoSecretFields（reflect 守卫 D-05） | `go test -run TestPythonConfigHasNoSecretFields ./pkg/infra/` | PASS | ✓ PASS |
| TestLoadConfigInvalidPython*（Interpreter/ScriptsDir/MaxConcurrent/Timeouts） | `go test -run TestLoadConfigInvalidPython ./pkg/infra/` | 全 PASS（errors.Is(err, ErrConfigInvalid) 为 true） | ✓ PASS |
| Linux/Windows 双平台编译 | `GOOS=linux go build ./pkg/pybridge/` + `GOOS=windows go build ./pkg/pybridge/` + `go build ./cmd/service/` | 三个均退出 0（linux-build-ok / windows-build-ok / service-build-ok） | ✓ PASS |
| Linux 进程组杀死集成测（仅代码审查） | 文件 `pkg/pybridge/bridge_linux_test.go` 在 `//go:build linux` 后 | Windows dev 机跳过；代码存在且用 `sh -c "sleep 30 & sleep 30"` 模拟 grandchild 验证 SIGKILL(-pgid) 杀整组 | ? SKIP（Linux-only，需 Linux CI 跑） |
| go vet | `go vet ./pkg/pybridge/ ./pkg/infra/ ./cmd/service/` | 退出 0（无输出） | ✓ PASS |
| vendor 脚本与上游 diff | `diff -q third_party/python/*.py ../xiaotian-skills/shared/dictation/scripts/*.py` | 输出空（vocab_manager.py + pregenerate_dictation.py 逐字节一致） | ✓ PASS |

### Probe 执行

Phase 3 的 PLAN/SUMMARY 未声明或引用任何 `scripts/*/tests/probe-*.sh` 探针；本 phase 的"可运行检查"由 `go test` 与 `go build`/`go vet` 承担（已在行为点检节列出）。无 probe 缺失。

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
| ----------- | ----------- | ----------- | ------ | -------- |
| PY-01 | 03-01, 03-02, 03-03 | System integrates with existing vocab_manager.py CLI | ✓ SATISFIED | PythonConfig（13 字段）+ vendor 脚本 + Bridge exec 引擎（含 process-group 杀死 + 密钥 env 注入 + 5 类日志分类）+ Executor 接口 + main.go 接线；REQUIREMENTS.md 第 55 行 + 第 133 行均标 Complete |
| PY-02 | 03-04 | System executes subprocess commands for word selection (due command) | ✓ SATISFIED | RunDue + dueArgs（--db 在 due 子命令前）+ extractTrailingJSON；TestDueArgs 8 子测试 + TestRunDueParseFromFixtures；REQUIREMENTS.md 第 56 行 + 第 134 行均标 Complete |
| PY-03 | 03-04 | System executes subprocess commands for FSRS updates (review command) | ✓ SATISFIED | RunReview + reviewArgs（--results/--file 自动切换 + 4 rating 原样透传）；TestReviewArgs 6 子测试 + TestRunReviewParseFromFixtures；REQUIREMENTS.md 第 57 行 + 第 135 行均标 Complete |
| PY-04 | 03-05 | System integrates with existing pregenerate_dictation.py for TTS generation | ✓ SATISFIED | RunPregenerate + pregenArgs（generate 子命令 + --words JSON 数组）+ splitNonEmptyLines + pregenSem cap=1 flock 串行化 + 空 stdout ErrPythonEmpty；TestPregenArgs 8 子测试 + TestClassifyPregenResult 6 子测试；REQUIREMENTS.md 第 58 行 + 第 136 行均标 Complete |

无孤儿需求：REQUIREMENTS.md 映射到 Phase 3 的全部 4 个 ID（PY-01..PY-04）均在 PLAN frontmatter 中声明并由实现满足。

### 反模式扫描（Anti-Patterns Found）

| File | Line | Pattern | Severity | Impact |
| ---- | ---- | ------- | -------- | ------ |
| （无） | - | - | - | - |

Phase 3 文件（pkg/pybridge/*.go、pkg/infra/config.go、cmd/service/main.go、third_party/python/、internal/pytest/fake_*.sh、Makefile、config.example.yaml）扫描 TBD/FIXME/XXX：0 命中；TODO/HACK/PLACEHOLDER/coming soon/not yet implemented：0 命中；空实现 return null/return {}/return []：0 命中（仅 `setProcessGroup(cmd *exec.Cmd) {}` 在 bridge_other.go 是 //go:build !linux 平台占位，文档注释明确说明语义——这是平台隔离约定，非 stub）。

**预存在 debt（不在本 phase 范围）:** `pkg/ws TestHubStopsOnContextCancel` 在 `go test -short ./...` 下偶发失败（goroutine-count 断言对并行调度时序敏感，Phase 2 既有）。已记录在 `.planning/phases/03-python-cli-integration/deferred-items.md` Item 1，Phase 3 未触 pkg/ws。隔离运行稳定通过，非 Phase 3 回归。

### Human Verification Required

### 1. VPS 部署期 venv + 真密钥端到端 UAT

**Test:** 在 VPS 上 `make py-bootstrap`（建 venv + `pip install -r third_party/python/requirements.txt` 锁版本），配置 `VOLCENGINE_TTS_API_KEY/APP_ID/ACCESS_KEY/RESOURCE_ID` 经部署环境注入，配置 `config.yaml` 的 `python:` 块指向 venv 解释器与 vendored 脚本路径，然后通过 `/gsd-verify-work` 触发：(a) vocab_manager.py due 子命令（验证返回 DueResult.Cards 非空）；(b) review 子命令带 4 种 rating（验证 ReviewResult.Updated 与 FSRS 状态更新）；(c) pregenerate_dictation.py generate 子命令（验证 PregenResult.Paths 含 mp3 绝对路径，ffmpeg 子进程在超时后被进程组杀死——观察 `tail -f` 日志看 error_type=timeout 时无孤儿 ffmpeg）。

**Expected:** RunDue 返回 DueResult.Cards（数组）；RunReview 返回 ReviewResult.Updated（数组，含 UpdatedCard 字段 word/rating/next_due/state/stability）；RunPregenerate 返回 PregenResult.Paths（mp3 绝对路径列表）；超时/非零退出/空 stdout 经 infra.Logger 落结构化日志（component=pybridge，error_type=timeout/nonzero_exit/other）；VOLCENGINE_TTS_* 密钥绝不出现在任何日志字段（TestLogResultNoSecretLeak 已在单测层守卫，UAT 二次确认）。

**Why human:** Windows dev 机无 venv/ffmpeg/Volcengine 密钥；CI 用 POSIX shell fake（internal/pytest/fake_*.sh）+ 黄金 fixture 验证桥的解析/超时/错误分类逻辑，但真 Python 解释器在 venv 内对 vendored 脚本的端到端行为（含 fsrs 包导入、Volcengine TTS HTTP 调用、ffmpeg 子进程 spawn）只能由部署 UAT 触达。vendored 脚本已与上游 `../xiaotian-skills/shared/dictation/scripts/*` 逐字节 `diff -q` 为空（见 03-01-SUMMARY.md checkpoint 解决节，orchestrator 以 upstream requirements.txt 为权威证据 resolve 为 approved）。

### Gaps Summary

无代码层 gap。所有 4 个 Success Criteria 与全部 PLAN must_haves（truths/prohibitions/artifacts/key_links）均在代码层通过单测与 wiring 验证。Requirements PY-01..PY-04 全部 SATISFIED，REQUIREMENTS.md traceability 表标记一致（4 个均 Complete）。

唯一的 human_needed 项是 VPS 部署期 venv + 真密钥 + ffmpeg + Volcengine TTS 端到端 UAT——这是部署时关注点而非代码 gap，按 verification_context 约定经 `/gsd-verify-work` 在部署时跑。

代码层亮点：
-argv 构造三处分离（dueArgs/reviewArgs/pregenArgs）严格遵循各自上游契约（vocab `--db` 在子命令前 vs pregenerate 无全局 --db，generate 子命令在前）；
- 4 类 sentinel 错误（ErrPythonTimeout/ErrPythonExit/ErrPythonJSONParse/ErrPythonEmpty）在 RunDue/RunReview/RunPregenerate 真正互斥可达（empty-stdout 前置检查让 ErrPythonEmpty 与 ErrPythonJSONParse 分离）；
- T-03-08 密钥防泄漏有两层守卫：pythonSecretEnvKeys 白名单（代码）+ TestLogResultNoSecretLeak（单测）；D-05 密钥不入 Config 有 TestPythonConfigHasNoSecretFields reflect 守卫；
- T-03-09 孤儿 ffmpeg DoS 防护完整：Linux setProcessGroup 设 Setpgid+Cancel=syscall.Kill(-pid,SIGKILL)+WaitDelay=3s，bridge_linux_test.go 用 sh -c "sleep 30 & sleep 30" 模拟 grandchild 验证（Linux CI 跑）；
- Pitfall 6 flock 串行化双层：pregenSem cap=1（Go 层排队）+ classifyPregenResult 空 stdout 检测（兜底非 Go 进程持锁场景）。

---

_Verified: 2026-07-25T10:40:00Z_
_Verifier: Claude (gsd-verifier)_
