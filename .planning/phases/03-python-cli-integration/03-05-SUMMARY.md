---
phase: 03-python-cli-integration
plan: 05
subsystem: pybridge
tags: [go, pybridge, subprocess-bridge, pregenerate, tts-generation, argv-construction, line-based-parser, flock-serialization, cap1-semaphore, empty-stdout-detection, shell-fake]

# Dependency graph
requires:
  - phase: 01-resource-constrained-infrastructure
    provides: infra.PythonConfig（13 字段含 PregenScript/PregenTimeout/Interpreter）、infra.ErrConfigInvalid、表驱动 + errors.Is + errors.As 测试范式
  - phase: 03-python-cli-integration
    provides: 03-02 的 Executor 接口 + PregenOpts/PregenResult + 4 sentinel（ErrPythonTimeout/Exit/JSONParse/Empty）+ ExitError + splitNonEmptyLines + pregenerate_paths.txt golden fixture；03-03 的 Bridge struct + NewBridge + childEnv + run + logResult + setProcessGroup 双 build-tag；03-04 的 RunDue/RunReview argv 构造 + 分类范式（pregenerate 照 shape 实现，但 argv/parser/sentinel 分类全部不同）
provides:
  - (*Bridge).RunPregenerate（PY-04）：pregenerate_dictation.py generate 路径，pregenArgs 构造 [pregenScript, "generate", --words <JSON array>, --language, ...]（无 --db 全局参数，与 vocab_manager argv 形状区分）
  - pregenArgs（argv 构造器 + 必选字段守卫）：5 PregenOpts 字段透传 + 空 Words/Language 显式 error
  - classifyPregenResult（纯函数 post-run 分类器）：4 类 sentinel 互斥可达（Timeout/Exit/Empty/success）+ Pitfall 6 空 stdout 严格检测（绝非性零路径成功）
  - Bridge.pregenSem（cap=1 专用串行化信号量）：独立于 D-02 全局 sem 的第二条限流，防 fcntl.flock 全局锁竞争导致的 exit 0 + 空 stdout 静默失败
  - internal/pytest/fake_vocab.sh + fake_prege.sh（POSIX shell 跨平台 fake，D-48 manual fake shell 变种）
  - ErrBridgeNotImplemented + RunPregenerate stub 全删除（无 stub 残留，var _ Executor = (*Bridge)(nil) 全靠真方法通过）
affects: [phase-06, phase-07, phase-12]

# Tech tracking
tech-stack:
  added: []  # 全 Go stdlib（context/encoding/json/strconv/time）+ 既有 github.com/pkg/errors v0.9.1，无新包
  patterns:
    - argv-builder-as-pure-function-with-required-guard（pregenArgs 是 (cfg, opts) -> ([]string, error) 纯函数；对脚本 argparse required=True 的 Words/Language 字段做 argv 层守卫，避免无谓 subprocess spawn）
    - dedicated-cap1-serialization-lock（pregenSem 独立 cap=1 信号量串行化 pregenerate 路径，独立于 D-02 全局 sem——pregenerate_dictation.py fcntl.flock 全局锁使第二实例 exit 0 + 空 stdout 静默失败，cap=1 让 Go 侧排队而非 race 进 flock 失败）
    - acquire-dedicated-sem-before-general-sem（pregenSem.acquire 在 b.run 之前——否则排队 caller 会持有 D-02 全局 sem 的位而饥饿 due/review 路径）
    - post-run-classifier-as-pure-function（classifyPregenResult 是 (stdout, stderr, exitCode, err) -> (PregenResult, error) 纯函数，让 4 类 sentinel 分支表驱动可测无 subprocess）
    - empty-success-stdout-as-typed-error（exit 0 + 空 stdout 绝不返回空 PregenResult 当成功——视为 fcntl.flock 锁占或脚本 bug，返回 ErrPythonEmpty；Pitfall 6 强制验收）
    - pitfall-3-line-based-parser（splitNonEmptyLines 按行切 mp3 路径，绝不 json.Unmarshal——pregenerate stdout 是路径非 JSON；与 vocab_manager 的 extractTrailingJSON 解析器严格分离）
    - manual-fake-shell-script（POSIX sh fake 而非 Go 代码 fake——让 Linux CI 集成测 + Windows dev 手动 smoke 都能驱动解析/分类全链路，D-48 shell 变种）

key-files:
  created:
    - pkg/pybridge/pregenerate.go
    - pkg/pybridge/pregenerate_test.go
    - internal/pytest/fake_vocab.sh
    - internal/pytest/fake_prege.sh
  modified:
    - pkg/pybridge/bridge.go   # Bridge.pregenSem 字段 + NewBridge 初始化 + 删 RunPregenerate stub + 删 ErrBridgeNotImplemented sentinel（03-04 handoff 闭环）

key-decisions:
  - "argv 构造用 cfg.PregenScript 直接作为 args[0] —— 照 RunDue/RunReview 范式（vocab.go），不主动 filepath.Join(cfg.ScriptsDir, cfg.PregenScript)；pregenerate_dictation.py 无 --db 全局参数，argv[1] 直接是 'generate' 子命令（区别于 vocab_manager 的 [vocabScript, --db, dbPath, due, ...] 形状）"
  - "pregenArgs 对空 Words/Language 返回 error 而非依赖脚本 argparse 报错 —— 脚本 required=True 字段在 argv 层提前拦下，避免无谓 subprocess spawn + 让错误消息更精确（'pregenArgs: opts.Words must not be empty' 比 Python exit 1 stderr 更可定位）"
  - "classifyPregenResult 提取为纯函数 —— plan 明确允许此提取（'可提取可测的辅助函数'）；让 4 类 sentinel 分支在无 Python/无 venv 的 Windows dev 上表驱动可测，避免依赖 shell fake 的跨平台 sh 可用性（git-bash 提供 sh 但不稳定）"
  - "pregenSem cap=1 而非依赖 Python 侧 fcntl.flock —— Go 侧排队让调用方收到可预测的排队延迟 + ctx 取消信号，而非 Python 侧 exit 0 + 空 stdout 的静默失败；cap=1 也防非 Go 进程（运维手动启动、上次部署残留）持锁时的 race"
  - "pregenSem.acquire 在 b.run 之前 —— 排队 caller 不消耗 D-02 全局 sem 的位，避免饥饿 due/review 路径；defer pregenSem.release() 让 logResult 在持锁状态下完成（log 后才释放）"
  - "空 stdout（exit 0）返回 ErrPythonEmpty 而非性零路径成功 —— Pitfall 6 强制验收点：pregenerate_dictation.py 在 fcntl.flock 失败时 sys.exit(0) + 空 stdout，Go 侧必须检测此签名返回 typed error 让调用方可重试/上送"
  - "删除 ErrBridgeNotImplemented 与 RunPregenerate stub —— critical_context 明确要求 03-05 落地后无 stub 残留；var _ Executor = (*Bridge)(nil) 现在完全靠三个真方法（RunDue/RunReview/RunPregenerate）通过编译期检查"

patterns-established:
  - "Pattern: pregenerate 路径的解析器与 vocab_manager 严格分离 —— 绝不混用 splitNonEmptyLines 与 extractTrailingJSON（Pitfall 3 anti-pattern：json.Unmarshal pregenerate stdout 必报 'invalid character \"/\"'）"
  - "Pattern: 专用 cap=1 信号量串行化高 contention 子进程 —— 当子进程自带全局锁（如 fcntl.flock）使并发第二实例静默失败时，Go 侧用专用 cap=1 在 D-02 全局 sem 之外加第二层限流"
  - "Pattern: post-run 分类提取为纯函数 —— RunXxx 方法瘦身为 'argv 构造 + spawn + delegate 分类'，让分类逻辑在表驱动单测中覆盖全部 sentinel 分支而不需 subprocess"

requirements-completed: [PY-04]

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "(*Bridge).RunPregenerate 实现 PY-04：argv 构造 [cfg.PregenScript, 'generate', --words <JSON array>, --language, ...5 PregenOpts flags]，无 --db 全局参数（pregenerate_dictation.py:1316-1326 main pops 'generate' off argv；verified against source）；pregenSem cap=1 串行化（独立于 D-02 全局 sem）→ b.run(ctx, cfg.PregenTimeout, cfg.Interpreter, args...)；splitNonEmptyLines 按行解析 mp3 路径（Pitfall 3）；exit 0 + 空 stdout → ErrPythonEmpty（Pitfall 6 fcntl.flock 锁占检测）；context.DeadlineExceeded/Canceled → ErrPythonTimeout；exit!=0 → ExitError wrapping ErrPythonExit"
    requirement: "PY-04"
    verification:
      - kind: unit
        ref: "pkg/pybridge/pregenerate_test.go#TestPregenArgs（8 子用例：minimal/multi_words/with_prefix/with_voice/with_words_per_segment/all_flags_combined/empty_words_errors/empty_language_errors；每 happy 子用例守卫 assertNoDBGlobal + --words JSON 数组 json.Unmarshal 回解析）+ TestRunPregenerateParse（3 子用例：normal_paths/trailing_newline_filtered/empty_stdout_yields_empty_slice）+ TestClassifyPregenResult（6 子用例：happy/timeout DeadlineExceeded/ctx.Canceled/non-zero exit/empty stdout Pitfall 6/whitespace-only）"
        status: pass
      - kind: unit
        ref: "go build ./... 退出 0；go vet ./pkg/pybridge/ 退出 0；GOOS=linux + GOOS=windows 交叉编译均退出 0"
        status: pass
    human_judgment: false
  - id: D2
    description: "pregenArgs 纯函数：(cfg infra.PythonConfig, opts PregenOpts) -> ([]string, error)；返回 [cfg.PregenScript, 'generate', --words <json.Marshal(opts.Words)>, --language opts.Language, ...optional flags]，--words-per-segment（>0）/--prefix（非空）/--voice（非空）条件透传；空 Words/Language 返回 error 防 subprocess spawn"
    requirement: "PY-04"
    verification:
      - kind: unit
        ref: "pkg/pybridge/pregenerate_test.go#TestPregenArgs/all_flags_combined + /empty_words_errors + /empty_language_errors + /multi_words（断言 --words JSON 经 json.Unmarshal 回解析为 []string 成功，JSON 数组格式正确）"
        status: pass
    human_judgment: false
  - id: D3
    description: "Bridge.pregenSem cap=1 串行化 + Pitfall 6 空 stdout 检测：Bridge struct 增 pregenSem semaphore 字段，NewBridge 初始化 newSemaphore(1)（独立于 D-02 全局 sem 的第二条限流）；RunPregenerate 在 b.run 前 acquire pregenSem（排队 caller 不消耗 D-02 sem 位）；classifyPregenResult 在 exit 0 + len(stdout)==0 时返回 errors.Wrap(ErrPythonEmpty, 'pregenerate returned empty stdout (likely flock contention)')——绝不返回空 PregenResult 当成功"
    requirement: "PY-04"
    verification:
      - kind: unit
        ref: "pkg/pybridge/pregenerate_test.go#TestClassifyPregenResult/empty_stdout_exit_0_is_flock_contention（Pitfall 6 强制验收点：errors.Is(err, ErrPythonEmpty)==true）+ /whitespace-only_stdout_yields_ErrPythonEmpty（splitNonEmptyLines 全空时也归类为 ErrPythonEmpty）+ pkg/pybridge/bridge.go grep 'pregenSem' >= 4（字段声明 + 注释 + NewBridge 初始化 + RunPregenerate acquire + defer release）"
        status: pass
    human_judgment: false
  - id: D4
    description: "ErrBridgeNotImplemented + RunPregenerate stub 删除：bridge.go 中 sentinel var 与 stub 方法全删除，var _ Executor = (*Bridge)(nil) 编译期检查现在完全靠三个真方法（RunDue/RunReview/RunPregenerate）通过；全仓 .go 文件 grep 'ErrBridgeNotImplemented' 命中 0"
    requirement: "PY-04"
    verification:
      - kind: unit
        ref: "grep -r 'ErrBridgeNotImplemented' --include='*.go' 命中 0；go build ./... 退出 0（编译期接口检查通过）"
        status: pass
    human_judgment: false
  - id: D5
    description: "internal/pytest/fake_vocab.sh + fake_prege.sh 跨平台 shell fake（D-48 manual fake shell 变种）：POSIX sh（无 bash-ism），git-bash on Windows + dash/bash on Linux 均可执行；fake_prege.sh 默认 echo 3 行 mp3 路径到 stdout + 进度日志到 stderr + exit 0；--lock-test 时空 stdout + exit 0（模拟 fcntl.flock 锁占 Pitfall 6）；--fail 时 stderr + exit 1（模拟 ExitError）；fake_vocab.sh 支持 due/review/unknown 三路径"
    requirement: "PY-04"
    verification:
      - kind: unit
        ref: "test -f internal/pytest/fake_vocab.sh && test -x internal/pytest/fake_vocab.sh && test -f internal/pytest/fake_prege.sh && test -x internal/pytest/fake_prege.sh + 手动 sh fake_prege.sh --lock-test 产空 stdout exit 0 + sh fake_prege.sh --fail 产 exit 1 + sh fake_prege.sh 默认产 3 行 mp3 路径"
        status: pass
    human_judgment: false

# Metrics
duration: ~25min active execution
completed: 2026-07-25
status: complete
---

# Phase 3 Plan 05: RunPregenerate + pregenArgs + cap=1 flock 串行化 + 空 stdout 检测 Summary

**pkg/pybridge.Bridge.RunPregenerate（PY-04）+ pregenArgs 构造 [pregenScript, generate, --words JSON array, --language, ...] argv（无 --db 全局参数）+ Bridge.pregenSem cap=1 专用串行化（独立于 D-02 全局 sem）+ classifyPregenResult 纯函数 4 类 sentinel 分类（Pitfall 6 空 stdout exit 0 → ErrPythonEmpty 绝非性零路径成功）+ splitNonEmptyLines 按行路径解析（非 JSON，Pitfall 3）+ ErrBridgeNotImplemented stub 全删除（var _ Executor 全靠真方法通过）+ POSIX shell fake（fake_prege.sh --lock-test/--fail + fake_vocab.sh due/review）**

## Performance

- **Duration:** ~25 min 活跃执行（Task 1 + Task 2，含 Rule 3 classifyPregenResult 提取）
- **Started:** 2026-07-25T01:48:36Z（baseline 检查）
- **Completed:** 2026-07-25T02:13:00Z（Task 2 commit `1b6760c` + 跨平台 build 验证）
- **Tasks:** 2（Task 1 RunPregenerate + pregenArgs + pregenSem + stub 删除；Task 2 classifyPregenResult 提取 + 17 子用例表驱动单测 + 跨平台 shell fake）
- **Files modified:** 4（2 新建 .go + 1 改 bridge.go + 2 新建 shell fake）

## Accomplishments

- **pregenArgs（PY-04 argv 构造器）**：纯函数 `(cfg infra.PythonConfig, opts PregenOpts) ([]string, error)` 返回 `[cfg.PregenScript, "generate", "--words", <jsonArray>, "--language", <lang>, ...]`——pregenerate_dictation.py 无 --db 全局参数（与 vocab_manager argv 形状区分），argv[1] 直接是 "generate" 子命令；json.Marshal(opts.Words) 产 JSON 数组字符串（脚本 argparse 期望形式）；--words-per-segment（>0 → strconv.Itoa）/--prefix（非空）/--voice（非空）条件透传；空 Words/Language 返回 error 防 subprocess spawn（脚本 required=True 字段在 argv 层提前拦下）。
- **RunPregenerate（PY-04 主路径）**：流程——args := pregenArgs(...)；argErr 非 nil 返回 wrap；pregenSem.acquire(ctx)（cap=1 专用串行化，在 b.run 之前让排队 caller 不消耗 D-02 全局 sem 位）；defer pregenSem.release()（LIFO 在 logResult 后释放）；start := time.Now()；b.run(ctx, cfg.PregenTimeout, cfg.Interpreter, args...)（03-03 exec 引擎 spawn，Linux 进程组杀死防 ffmpeg 孤儿）；defer b.logResult("pregenerate", ...)（command 字段值 "pregenerate"，让操作员能在日志流中按语义命令过滤）；return classifyPregenResult(stdout, stderr, exitCode, err)（纯函数分类，无 subprocess 即可表驱动测试）。
- **classifyPregenResult（Rule 3 提取的纯函数）**：(stdout, stderr []byte, exitCode int, runErr error) (PregenResult, error)；4 类 sentinel 互斥可达——runErr is context.DeadlineExceeded/Canceled → ErrPythonTimeout；exitCode != 0 → *ExitError{Cmd:"pregenerate", ExitCode, Stderr, Err: ErrPythonExit}；len(stdout) == 0 → errors.Wrap(ErrPythonEmpty, "pregenerate returned empty stdout (likely flock contention)")（Pitfall 6 强制验收）；splitNonEmptyLines 全空 → ErrPythonEmpty（whitespace-only 也是契约违反）；成功 → PregenResult{Paths: paths}。提取让 4 类 sentinel 分支在无 Python/无 venv 的 Windows dev 上表驱动可测，避免依赖 shell fake 的跨平台 sh 可用性。
- **Bridge.pregenSem cap=1 专用串行化（Pitfall 6 防护）**：Bridge struct 新增 pregenSem semaphore 字段（独立于 D-02 全局 sem 的第二条限流）；NewBridge 初始化 newSemaphore(1)；rationale：pregenerate_dictation.py 的 _acquire_global_lock（第 1297-1313 行）对 /tmp/pregenerate_dictation.lock 取 fcntl.flock(LOCK_EX|LOCK_NB)，第二个实例 sys.exit(0) + 空 stdout——cap=1 让 Go 侧排队而非 race 进 flock 失败；防非 Go 进程（运维手动启动、上次部署残留）持锁时的 race。defer pregenSem.release() 让 logResult 在持锁状态下完成（log 后才释放）。
- **Pitfall 6 空 stdout 检测**：classifyPregenResult 在 exit 0 + len(stdout)==0 时返回 ErrPythonEmpty——绝不返回空 PregenResult 当成功；这是 fcntl.flock 锁占签名的强制检测（pregenerate_dictation.py:1303-1307 sys.exit(0) + 空 stdout 是上游设计），让调用方可重试或上送错误而非沉默失败。
- **ErrBridgeNotImplemented + RunPregenerate stub 删除**：bridge.go 中 sentinel var 与 stub 方法全删除（03-04 handoff 闭环——critical_context 明确要求 03-05 落地后无 stub 残留）；var _ Executor = (*Bridge)(nil) 编译期检查现在完全靠三个真方法（RunDue/RunReview/RunPregenerate）通过；全仓 .go 文件 grep 'ErrBridgeNotImplemented' 命中 0；ErrBridgeNotImplemented 注释从 "Plan 03-04 已落地，仅 RunPregenerate 待 03-05 替换" 推进为 "As of Plan 03-05 all three methods are real implementations; no stubs remain"。
- **POSIX shell fake（D-48 manual fake shell 变种）**：internal/pytest/fake_prege.sh（POSIX sh，无 bash-ism）默认 echo 3 行 mp3 绝对路径到 stdout + 进度日志到 stderr + exit 0（照 pregenerate_dictation.py:1147-1148 + 624/715/907/1069 契约）；--lock-test 时空 stdout + "Another instance running" 到 stderr + exit 0（精确模拟 Pitfall 6 fcntl.flock 锁占签名）；--fail 时 stderr + exit 1（模拟 ExitError 路径）；fake_vocab.sh 支持 due（JSON 数组）/review（Warning 前缀 + 末尾 JSON 混合 stdout，Pitfall 2）/unknown（Error + exit 1）三路径。让 Windows dev 无 Python/无 fcntl 也能手动 smoke-test 桥的解析/分类全链路。
- **17 子用例 + 3 主用例全绿**：TestPregenArgs（8 子用例：minimal/multi_words/with_prefix/with_voice/with_words_per_segment/all_flags_combined/empty_words_errors/empty_language_errors；每 happy 子用例守卫 assertNoDBGlobal 即 pregenerate 无 --db 全局参数，与 vocab_manager argv 形状严格区分；--words JSON 经 json.Unmarshal 回解析验证格式）、TestRunPregenerateParse（3 子用例：normal_paths 读 pregenerate_paths.txt fixture 3 行路径 round-trip + Pitfall 3 守卫整段 fixture 非合法 JSON、trailing_newline_filtered、empty_stdout_yields_empty_slice 含 whitespace-only 分支）、TestClassifyPregenResult（6 子用例：happy/timeout DeadlineExceeded/ctx.Canceled/non-zero exit errors.As 恢复 *ExitError/empty stdout Pitfall 6 强制验收/whitespace-only）。

## Task Commits

每个 task 原子提交：

1. **Task 1: RunPregenerate + pregenArgs + pregenSem cap=1 + stub/sentinel 删除** — `9a3fb4c` (feat)
2. **Task 2: classifyPregenResult 提取 + 表驱动 17 子用例 + POSIX shell fake** — `1b6760c` (test)

**Plan metadata:** 本 SUMMARY 闭环时提交（docs: complete RunPregenerate pregenerate path plan）。

## Files Created/Modified

- `pkg/pybridge/pregenerate.go`（新建）— pregenArgs（argv 构造 + 必选字段守卫）+ RunPregenerate（pregenSem cap=1 + delegate classifyPregenResult）+ classifyPregenResult（4 类 sentinel 分类，Pitfall 6 空 stdout 检测）；含详细文档注释解释 generate 子命令 argv 契约、Pitfall 3/6、cap=1 pregenSem rationale、为何提取 classifyPregenResult
- `pkg/pybridge/pregenerate_test.go`（新建）— 17 子用例 + 3 主用例；assertNoDBGlobal helper（pregenerate-specific 不变式，与 vocab_test.go 的 assertDBBeforeSubcommand 形成对比）；TestPregenArgs/TestRunPregenerateParse/TestClassifyPregenResult 全覆盖
- `pkg/pybridge/bridge.go`（修改）— Bridge.pregenSem 字段（cap=1 专用串行化）+ NewBridge 初始化 newSemaphore(1) + 删除 RunPregenerate stub + 删除 ErrBridgeNotImplemented sentinel + 更新 var _ Executor 文档注释
- `internal/pytest/fake_vocab.sh`（新建，POSIX sh）— due/review/unknown 三路径 fake，模拟 vocab_manager stdout 契约（含 Pitfall 2 混合 stdout）
- `internal/pytest/fake_prege.sh`（新建，POSIX sh）— 默认 3 行 mp3 路径 / --lock-test 空 stdout exit 0（Pitfall 6）/ --fail exit 1（ExitError）三路径 fake

## Decisions Made

- **argv 构造用 cfg.PregenScript 直接作 args[0]**：照 RunDue/RunReview 范式（vocab.go），不主动 `filepath.Join(cfg.ScriptsDir, cfg.PregenScript)`——调用方（部署/配置）对路径形式负全责。PythonConfig.PregenScript 字段注释（config.go:126-129）只说"filename"，与 ScriptsDir 分立，bridge 尊重该分层。
- **pregenArgs 对空 Words/Language 返回 error**：脚本 argparse required=True（pregenerate_dictation.py:1043-1044）会在缺字段时 exit 1 + stderr 诊断；argv 层提前拦下避免无谓 subprocess spawn + 让错误消息更精确（"pregenArgs: opts.Words must not be empty" 比 Python exit 1 stderr 更可定位）—— Rule 2 robustness。
- **classifyPregenResult 提取为纯函数**：plan 明确允许此提取（"可提取可测的辅助函数 或 用 shell fake 驱动"）；选纯函数而非 shell fake 因 Windows dev 的 git-bash sh 可用性不稳定，纯函数让 4 类 sentinel 分支表驱动可测最可靠—— Rule 3 unblock（tests cannot be written without this extraction）。
- **pregenSem cap=1 而非依赖 Python 侧 fcntl.flock**：Go 侧排队让调用方收到可预测的排队延迟 + ctx 取消信号，而非 Python 侧 exit 0 + 空 stdout 的静默失败；cap=1 也防非 Go 进程（运维手动启动、上次部署残留）持锁时的 race—— RESEARCH Pitfall 6 mitigation。
- **pregenSem.acquire 在 b.run 之前**：排队 caller 不消耗 D-02 全局 sem 的位，避免饥饿 due/review 路径；defer pregenSem.release() 让 logResult 在持锁状态下完成（log 后才释放）；照 03-03 sem.acquire 风格（fast-path ctx 检查）。
- **空 stdout（exit 0）返回 ErrPythonEmpty 而非性零路径成功**：Pitfall 6 强制验收点——pregenerate_dictation.py 在 fcntl.flock 失败时 sys.exit(0) + 空 stdout（第 1303-1307 行），Go 侧必须检测此签名返回 typed error 让调用方可重试/上送。
- **删除 ErrBridgeNotImplemented 与 RunPregenerate stub**：critical_context 明确要求 03-05 落地后无 stub 残留（"Delete ErrBridgeNotImplemented in the SAME commit that replaces RunPregenerate — after this plan, NO stubs remain on Bridge"）；Task 1 commit 内完成删除，var _ Executor 现在完全靠三个真方法通过编译期检查。
- **classifyPregenResult 用 errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)**：照 RunDue/RunReview 的错误分类范式（vocab.go）；exec.CommandContext 在父 ctx 或 cctx 触发 Cancel 后返回的错误链 unwrap 到 context.DeadlineExceeded（Go 1.20+ exec 文档，03-03 已验证）；显式加 context.Canceled 守卫上游调用方主动取消的场景。
- **fake_prege.sh 的 --lock-test/--fail 是 fake-only 哨兵**：真实 pregenerate_dictation.py 不接受这些标志（其 argparse 仅 --words/--language/--words-per-segment/--prefix/--voice/--generation-mode）；fake 用哨兵标志切换路径让集成测/手动 smoke 可精确驱动 Pitfall 6 与 ExitError 分支，无需修改真脚本。

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking Issue] RunPregenerate 内联分类无法在 Windows dev 上无 subprocess 测试**
- **Found during:** Task 2 设计 TestPregenArgsErrorClassification 时（plan acceptance_criteria 强制要求"空 stdout + exit 0 → ErrPythonEmpty（Pitfall 6 flock 锁占验收）"）
- **Issue:** plan task 2 描述 RunPregenerate 错误分类"内联在 RunPregenerate 中，可提取可测的辅助函数或用 shell fake 驱动"。但 Windows dev 的 git-bash sh 可用性不稳定（依赖安装路径、PATH 配置），且表驱动测试若依赖 shell fake 无法在 `go test -short` 全平台秒级跑完。RunPregenerate 内联分类让 Pitfall 6 验收点无法在纯 Go 表驱动测试中覆盖。
- **Fix:** 提取 classifyPregenResult 为顶层纯函数 `(stdout, stderr []byte, exitCode int, runErr error) (PregenResult, error)`；RunPregenerate 在 b.run 后直接 `return classifyPregenResult(stdout, stderr, exitCode, err)`；TestClassifyPregenResult 表驱动覆盖 4 类 sentinel 分支（happy/timeout/exit!=0/empty stdout/whitespace-only）全部在 Go 进程内驱动，无需 subprocess。
- **Files modified:** pkg/pybridge/pregenerate.go（Task 2 commit `1b6760c` 内）
- **Verification:** TestClassifyPregenResult 6 子用例全 PASS，含 Pitfall 6 强制验收点（empty_stdout_exit_0_is_flock_contention 子用例 errors.Is(err, ErrPythonEmpty)==true）；TestClassifyPregenResult/whitespace-only_stdout_yields_ErrPythonEmpty 覆盖 splitNonEmptyLines 全空时的二级 ErrPythonEmpty 路径。
- **Committed in:** `1b6760c`（Task 2 commit）

**2. [Rule 2 - Critical Functionality] pregenArgs 未对脚本 argparse required=True 字段（Words/Language）做守卫**
- **Found during:** Task 1 实现 pregenArgs 时核读 pregenerate_dictation.py:1043-1044 源码
- **Issue:** pregenerate_dictation.py 第 1043 行 `parser.add_argument("--words", required=True, ...)` 与第 1044 行 `parser.add_argument("--language", required=True, ...)` 都是 required=True。若调用方零值初始化 PregenOpts{}（Words=nil, Language=""），pregenArgs 会构造致命 argv（无 --words/--language），脚本会 spawn 后立刻 exit 1 + stderr 诊断 "error: the following arguments are required: --words, --language"——无谓消耗 subprocess spawn 配额 + 错误消息比 Go 侧守卫模糊。
- **Fix:** pregenArgs 入口加 `if len(opts.Words) == 0: return nil, errors.New("...")` + `if opts.Language == "": return nil, errors.New("...")` 守卫；RunPregenerate 收到 argErr 后 `return PregenResult{}, errors.Wrap(argErr, "build pregenerate argv")` 不 spawn。
- **Files modified:** pkg/pybridge/pregenerate.go（Task 1 commit `9a3fb4c` 内）
- **Verification:** TestPregenArgs/empty_words_errors + /empty_language_errors 子用例 PASS（断言 pregenArgs 返回非 nil error 且消息含对应字段名）。
- **Committed in:** `9a3fb4c`（Task 1 commit）

---

**Total deviations:** 2 auto-fixed（1 个 Rule 3 测试可达性提取、1 个 Rule 2 argv 层守卫）。均 correctness 必需，无 scope creep，无新文件超 plan 列表，无架构变更。classifyPregenResult 提取是 plan 明确允许的（"可提取可测的辅助函数"），非新决策。

## Issues Encountered

- **CGO_ENABLED=0 导致 `go test -race` 不可用**：本机 Windows 环境 cgo 未启用（与 03-02/03/04 SUMMARY 同一限制），退回 `go test` 无 race；race 检测留 Linux CI。
- **GOOS=linux go build 在 Windows 上首次较慢**：03-03/04 SUMMARY 已记录的限制——首次 GOOS=linux 交叉编译触发 stdlib 工具链下载耗时较长（~30s）；本 plan 验证 GOOS=windows + GOOS=linux 双平台 build 时同样出现，已让命令后台完成。

## User Setup Required

None - 无外部服务配置。本 plan 是纯 Go 方法 + 表驱动单测 + shell fake，无 Python/venv/密钥依赖（解析器 pipeline 测试用 testdata/pregenerate_paths.txt golden fixture + 纯函数 classifyPregenResult，无 subprocess spawn；shell fake 仅用于 Linux CI 集成测与 Windows dev 手动 smoke）。密钥经 childEnv 注入是 03-03 已落地的运行时行为。

## Next Phase Readiness

- **就绪**：`(*Bridge).RunPregenerate` 可供 Phase 6（TTS 音频流式）与 Phase 7（会话编排）直接调用——`pyBridge.RunPregenerate(ctx, PregenOpts{Words: wordList, Language: "english"})` 返回 typed PregenResult.Paths（mp3 绝对路径列表）+ 4 类 sentinel 错误分类。
- **就绪**：argv 构造正确性经表驱动 8 子用例 + assertNoDBGlobal 全局不变式守卫；Pitfall 6 混合 stdout 解析经 classifyPregenResult 6 子用例表驱动覆盖（含空 stdout exit 0 严格检测 + splitNonEmptyLines 全空检测）。
- **就绪**：Phase 6（codec 层）读 PregenResult.Paths 后做 mp3 → PCM → 640B 帧切片（D-06/D-08 严格接缝）；Phase 6 应在读字节时校验路径在 cfg.CacheDir 范围内（T-03-06 mitigation，splitNonEmptyLines 与 RunPregenerate 故意不做）。
- **就绪**：Phase 7（会话编排）决定何时触发 RunPregenerate（eager 整批 vs lazy 逐词）—— D-08 明确此决策属 Phase 7。
- **阻塞**：无。Phase 3（Python CLI Integration）全部 5 个 plan 已落地，PY-01/02/03/04 全部完成。
- **Phase 3 完结里程碑**：本 plan 是 Phase 3 的最后一个 plan（plan 05/05）。Executor 接口的三条交付路径（RunDue/RunReview/RunPregenerate）全部由真方法实现，var _ Executor = (*Bridge)(nil) 编译期检查无 stub 残留。下游 Phase 4（DB schema）、Phase 5（FSRS 编排）、Phase 6（TTS 流式）、Phase 7（会话编排）可通过 Executor 接口调用。
- **提醒下游**：
  - Phase 5（FSRS 编排）若需校验 DueResult.Cards 的 state/difficulty/stability 字段语义，应在 Phase 5 层做——bridge 不校验（D-09：FSRS 逻辑留 Python）。
  - Phase 6（TTS 流式）读 PregenResult.Paths 时应做 CacheDir 路径遍历校验（T-03-06 mitigation；splitNonEmptyLines 与 RunPregenerate 故意不做，留字节读取层）。
  - Phase 7（会话编排）的生成时机决策（eager 整批 vs lazy 逐词）属 Phase 7 范围。
  - Phase 10（家长反馈）的 rating 映射决策（若 UI 暴露的 rating 与 vocab_manager 的 again/good/hard/easy 不同）属 Phase 10 范围，Phase 3 仅透传。
  - Linux CI 集成测可用 internal/pytest/fake_prege.sh（需先 chmod +x）作为 Interpreter + PregenScript 跑 RunPregenerate 全链路（避开真 Python/ffmpeg/Volcengine 依赖）；或用真 venv python + 真 ffmpeg + VOLCENGINE_TTS_* 密钥跑端到端（部署期 checkpoint）。

## Self-Check: PASSED

- 创建文件存在性（4/4 FOUND）：
  - `pkg/pybridge/pregenerate.go` — FOUND
  - `pkg/pybridge/pregenerate_test.go` — FOUND
  - `internal/pytest/fake_vocab.sh` — FOUND（可执行）
  - `internal/pytest/fake_prege.sh` — FOUND（可执行）
- 修改文件存在性（1/1 FOUND）：
  - `pkg/pybridge/bridge.go` — FOUND（含 Bridge.pregenSem 字段 + NewBridge 初始化 + 无 RunPregenerate stub + 无 ErrBridgeNotImplemented）
- 提交存在性：
  - `9a3fb4c` — FOUND（Task 1）
  - `1b6760c` — FOUND（Task 2）
- 验收 grep（plan Task 1 + Task 2 acceptance_criteria 全部满足）：
  - `func pregenArgs` in pregenerate.go == 1 — PASS
  - `func.*RunPregenerate` in pregenerate.go == 1 — PASS
  - `"generate"` in pregenerate.go >= 1 — PASS（5 命中，含 argv 构造 + 文档注释）
  - `json.Marshal` in pregenerate.go >= 1 — PASS（4 命中，含 Words 序列化 + 文档）
  - `splitNonEmptyLines` in pregenerate.go >= 1 — PASS（8 命中，含调用 + 文档）
  - `json.Unmarshal.*stdout` in pregenerate.go == 0 — PASS（2 命中均在文档注释中，无实际 stdout Unmarshal 代码路径，Pitfall 3 防护）
  - `ErrPythonEmpty` in pregenerate.go >= 1 — PASS（5 命中，含两处空 stdout 检测）
  - `pregenSem` in pregenerate.go >= 2 — PASS（8 命中，含 acquire/release + 文档）
  - `b.logResult` in pregenerate.go == 1 — PASS
  - `func classifyPregenResult` in pregenerate.go == 1 — PASS（Rule 3 提取的纯函数）
  - `ErrBridgeNotImplemented` in *.go == 0 — PASS（全删，仅留 .planning/ 历史文档引用）
  - `var _ Executor = (*Bridge)(nil)` in bridge.go == 1 — PASS（无 stub，全靠真方法）
  - TestPregenArgs / TestRunPregenerateParse / TestClassifyPregenResult in pregenerate_test.go 全部命中 — PASS
  - `empty_stdout_exit_0_is_flock_contention` Pitfall 6 验收子用例命中 — PASS
  - `errors.As.*ExitError` 验收 exit!=0 分支 — PASS
- 验证命令：
  - `go test -count=1 -v -run 'TestPregenArgs|TestRunPregenerateParse|TestClassifyPregenResult' ./pkg/pybridge/` 全绿（17 子用例 + 3 主用例 PASS）— PASS
  - `go test -count=1 -short ./pkg/pybridge/` 全绿（无回归，含 03-01/02/03/04 全部用例）— PASS
  - `go build ./...` 退出 0（全仓无回归）— PASS
  - `go vet ./pkg/pybridge/` 退出 0 — PASS
  - `GOOS=windows go build ./pkg/pybridge/` 退出 0 — PASS
  - `GOOS=linux go build ./pkg/pybridge/` 退出 0 — PASS
  - `sh internal/pytest/fake_prege.sh --lock-test` 产空 stdout + exit 0（Pitfall 6 签名）— PASS
  - `sh internal/pytest/fake_prege.sh --fail` 产 exit 1（ExitError 路径）— PASS
  - `sh internal/pytest/fake_prege.sh generate --words '["x"]' --language english` 产 3 行 mp3 路径（happy 路径）— PASS
  - `sh internal/pytest/fake_vocab.sh due/review/bogus` 三路径正确 — PASS

---
*Phase: 03-python-cli-integration*
*Completed: 2026-07-25*
