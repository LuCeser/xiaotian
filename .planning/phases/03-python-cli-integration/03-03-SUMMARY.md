---
phase: 03-python-cli-integration
plan: 03
subsystem: pybridge
tags: [go, pybridge, subprocess-bridge, exec-engine, process-group-kill, secrets-via-env, structured-logging, build-tag]

# Dependency graph
requires:
  - phase: 01-resource-constrained-infrastructure
    provides: 构造器 DI 范式（NewConnectionRegistry 第 251-272 行）、infra.Logger/F/ErrField（D-25 canonical）、表驱动 + errors.Is 测试 + fakeLogger helper
  - phase: 03-python-cli-integration
    provides: 03-01 落地的 infra.PythonConfig（13 字段含 MaxConcurrent）；03-02 的 Executor 接口 + 4 sentinel + ExitError + semaphore + 解析器
provides:
  - pybridge.Bridge struct（cfg + logger + sem 三字段，照抄 ConnectionRegistry 组织风格）
  - pybridge.NewBridge 纯构造器（cfg 逐字段校验 + nil-logger 替换 + 内部 semaphore，绝不 spawn）
  - (*Bridge).childEnv（os.Getenv 读 VOLCENGINE_TTS_* D-05 密钥 + PYTHONUTF8/PYTHONIOENCODING/PYTHONUNBUFFERED UTF-8 + DICTATION_* 路径透传）
  - (*Bridge).run（exec.CommandContext argv 数组 + setProcessGroup build-tag 分发 + semaphore 门 + logResult 调用）
  - (*Bridge).logResult（5 类错误分类经 infra.Logger，T-03-08 日志字段白名单）
  - setProcessGroup 双 build-tag 隔离（bridge_linux.go 设 Setpgid+Cancel+WaitDelay；bridge_other.go 空实现）
  - RunDue/RunReview/RunPregenerate 占位（让 var _ Executor 编译期检查就位，Plan 03-04/05 替换）
  - main.go pyBridge 接线（NewServer 后 NewBridge 构造 + 错误处理 + 持有引用供 Phase 5/7）
  - ErrBridgeNotImplemented sentinel（占位方法返回，03-04/05 落地时删除）
affects: [03-04, 03-05, phase-05, phase-07]

# Tech tracking
tech-stack:
  added: []  # 全 Go stdlib（context/bytes/os/os/exec/time）+ 既有 github.com/pkg/errors v0.9.1，无新包
  patterns:
    - pure-constructor-no-spawn（NewBridge 不 spawn 不文件 IO，boot 测试无 Python 也能跑）
    - argv-slice-no-shell（exec.CommandContext(name, args...) 绝不 shell 拼接，T-03-07 命令注入防护）
    - build-tag-process-group-isolation（//go:build linux / !linux 双文件隔离 Setpgid，与 main.go 第 12-13 行 SIGTERM 约定同类）
    - secrets-via-os-getenv-forwarding（密钥经 pythonSecretEnvKeys 白名单从父 env 透传到 cmd.Env，D-05）
    - structured-log-field-whitelist（logResult 固定 component/command/exit_code/duration_ms/error_type/stderr/stdout/error，T-03-08）
    - semaphore-acquire-before-spawn（D-02 fork-bomb 防护，run() 入口先 acquire 再 defer release）
    - compile-time-interface-via-stubs（var _ Executor 检查就位 + Plan 03-04/05 替换的 stub 方法）

key-files:
  created:
    - pkg/pybridge/bridge.go
    - pkg/pybridge/bridge_linux.go
    - pkg/pybridge/bridge_other.go
    - pkg/pybridge/bridge_test.go
    - pkg/pybridge/bridge_linux_test.go
  modified:
    - cmd/service/main.go

key-decisions:
  - "NewBridge 返回 (*Bridge, error) 而非 *Bridge —— 照抄 NewConnectionRegistry 第 251-272 行的 DI 范式，让逐字段 cfg 校验和 nil-logger 替换都有错误出口，main.go 接线块用相同的 if err != nil 模式处理"
  - "RunDue/RunReview/RunPregenerate 加 stub 方法返回 ErrBridgeNotImplemented —— 让 `var _ Executor = (*Bridge)(nil)` 编译期检查在本 plan 就位而非推迟到 03-04/05；占位方法绝不 spawn，对 boot 测试无副作用"
  - "childEnv 显式追加 PYTHONUNBUFFERED=1（critical_context 要求，RESEARCH 未明确）—— 强制 stdout/stderr 在 Wait 返回前 flush 完毕，避免 bytes.Buffer 漏捕上游诊断（Rule 2 correctness）"
  - "sem.acquire 放在 cctx,cancel := context.WithTimeout() 之前 —— 让排队期间的父 ctx 取消能立即失败而不消耗信号量位（照 semaphore.go fast-path ctx 检查约定）"
  - "logResult 用 errors.Is(err, context.DeadlineExceeded) 识别超时而非 *exec.ExitError —— exec.CommandContext 在 ctx 触发 Cancel 后，Run 返回的错误链 unwrap 到 context.DeadlineExceeded（Go 1.20+ exec 文档）"
  - "TestLogResultNoSecretLeak 用显式白名单（component/command/exit_code/duration_ms/error_type/stderr/stdout/error）守卫 —— 任何 future 重构加 'env' 字段会立即被此测捕获（T-03-08 专用回归）"
  - "TestChildEnvSecrets 用 os.Unsetenv + t.Cleanup 而非 t.Setenv(\"\") —— t.Setenv 设空串仍会让 os.Environ() 返回空条目，混淆 'bridge 添加空占位' 与 '父 env 继承' 的判定"

patterns-established:
  - "Pattern: pybridge 的双 build-tag 隔离约定 —— 任何 Linux 专属 syscall（Setpgid/fcntl 等）放 *_linux.go，*_other.go 提供空 stub，main.go 第 12-13 行 SIGTERM 注释同类"
  - "Pattern: 占位方法 + ErrXxx sentinel 让编译期接口检查可分阶段落地 —— 后续 plan 用同名方法替换占位即可，sentinel 在替换 commit 内删除"
  - "Pattern: 子进程 env 构造经固定白名单 pythonSecretEnvKeys —— 所有 D-05 密钥名集中在一处，security review 表面单一"

requirements-completed: [PY-01]

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "pybridge.Bridge struct + NewBridge 纯构造器（cfg 逐字段校验 + nil-logger 替换 + 内部 semaphore），照抄 NewConnectionRegistry 第 251-272 行 DI 范式；绝不 spawn 不文件 IO，boot 测试可无 Python 构造"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/pybridge/bridge_test.go#TestNewBridgeValidation（9 项表驱动覆盖 Interpreter/ScriptsDir/VocabScript/PregenScript/MaxConcurrent[0,5]/DueTimeout/ReviewTimeout/PregenTimeout 校验，每项 errors.Is(err, infra.ErrConfigInvalid)）+ TestNewBridgeValid + TestNewBridgeNilLogger"
        status: pass
      - kind: unit
        ref: "cmd/service/main_test.go#TestMainLoadsDefaultConfig / TestMainStartsHealthAndMetricsServers / TestMainMemoryUnder150MBAtIdle 全绿（pyBridge 接线在 boot 路径不 spawn）"
        status: pass
    human_judgment: false
  - id: D2
    description: "(*Bridge).childEnv 实现 D-05：PYTHONUTF8=1/PYTHONIOENCODING=utf-8/PYTHONUNBUFFERED=1 强制 UTF-8；DICTATION_DB_PATH/DATA_DIR/CACHE_DIR/MANIFEST_DIR/ENV_FILE 从 cfg 透传；VOLCENGINE_TTS_API_KEY/APP_ID/ACCESS_KEY/RESOURCE_ID 经 os.Getenv 从父 env 读取并注入 cmd.Env（绝不入 Config/日志/ExitError）"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/pybridge/bridge_test.go#TestChildEnvSecrets（断言 PYTHONUTF8/PYTHONIOENCODING/PYTHONUNBUFFERED + DICTATION_* + VOLCENGINE_TTS_API_KEY=test-key-from-parent-env 转发 + 三个未设密钥不泄漏为占位）+ TestChildEnvExtraPassthrough"
        status: pass
    human_judgment: false
  - id: D3
    description: "(*Bridge).run 实现 RESEARCH Pattern 1：exec.CommandContext(ctx, name, args...) argv 数组（T-03-07 不 shell 拼接）+ sem.acquire/release 门（D-02）+ context.WithTimeout(ctx, timeout) + setProcessGroup(cmd) build-tag 分发 + stdout/stderr bytes.Buffer + cmd.Run() + logResult 调用"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/pybridge/bridge.go#Bridge.run（grep exec.CommandContext == 1，参数数组形式；grep setProcessGroup == 1；sem.acquire 在 cctx 前）+ pkg/pybridge/bridge_linux_test.go#TestRunProcessGroupKill（//go:build linux 集成）"
        status: pass
    human_judgment: false
  - id: D4
    description: "setProcessGroup 双 build-tag 隔离：bridge_linux.go //go:build linux 设 SysProcAttr{Setpgid:true} + Cancel=syscall.Kill(-pid, SIGKILL) + WaitDelay=3s（防孤儿 ffmpeg Pitfall 1）；bridge_other.go //go:build !linux 空实现（Windows/macOS dev 编译通过）"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/pybridge/bridge_linux.go（首行 //go:build linux，Setpgid 命中 >= 1）+ pkg/pybridge/bridge_other.go（首行 //go:build !linux）+ GOOS=linux go build ./pkg/pybridge/ 退出 0 + GOOS=windows go build ./pkg/pybridge/ 退出 0 + pkg/pybridge/bridge_linux_test.go#TestRunProcessGroupKill（//go:build linux 集成）"
        status: pass
    human_judgment: false
  - id: D5
    description: "(*Bridge).logResult 实现 SC-4 5 类错误分类（success→Debug / DeadlineExceeded→timeout / exit!=0→nonzero_exit / default→other），全部经 infra.Logger + infra.F() + infra.ErrField()（D-25 canonical）；日志字段固定白名单 component/command/exit_code/duration_ms/error_type/stderr/stdout/error，禁记 env 与 VOLCENGINE_TTS_* 密钥（T-03-08）"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/pybridge/bridge_test.go#TestLogResultClassification（4 类表驱动，每类断言 Level/Msg/error_type 字段值 + 不变式 component='pybridge'+command/exit_code/duration_ms）+ TestLogResultNoSecretLeak（白名单回归守卫，含 hostile stderr payload）"
        status: pass
    human_judgment: false
  - id: D6
    description: "cmd/service/main.go 在 NewServer 后新增 pyBridge, err := pybridge.NewBridge(cfg.Python, logger) 接线块，照 main.go 第 93-100 行 infra.NewServer 的 DI + infra.F/ErrField 错误处理范式；import 新增 pkg/pybridge；持有 _ = pyBridge 引用供 Phase 5/7 调用方接入"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "cmd/service/main.go#pyBridge 块（grep pybridge.NewBridge == 1；grep home-tutor-agent/pkg/pybridge == 1；awk 统计 pyBridge 块附近 infra.F/ErrField 命中 16 >= 2）+ go build ./cmd/service/ 退出 0 + cmd/service boot 测试全绿"
        status: pass
    human_judgment: false

# Metrics
duration: ~25min active execution
completed: 2026-07-25
status: complete
---

# Phase 3 Plan 03: Bridge exec 引擎 + 进程组杀死 + 密钥 env 注入 + main.go 接线 Summary

**pkg/pybridge.Bridge（持有 PythonConfig + infra.Logger + semaphore）+ NewBridge 纯构造器（cfg 逐字段校验 + nil-logger 替换 + 内部 semaphore）+ childEnv（D-05 密钥经 os.Getenv 注入）+ run（exec.CommandContext argv + 进程组杀死 + WaitDelay + semaphore 门）+ logResult（5 类错误分类经 infra.Logger）+ setProcessGroup 双 build-tag 隔离 + main.go pyBridge 接线（boot 测试全绿）**

## Performance

- **Duration:** ~25 min 活跃执行（Task 1 + Task 2）
- **Started:** 2026-07-25T00:51:33Z（Task 1 commit `5add217`）
- **Completed:** 2026-07-25T01:15:49Z（Task 2 commit `23a6482`）
- **Tasks:** 2（Task 1 Bridge 结构体/构造器/childEnv/run/logResult/双 build-tag；Task 2 单测 + main.go 接线）
- **Files modified:** 5（3 新建 .go + 2 新建测试 + 1 改 main.go）

## Accomplishments

- **Bridge 结构体 + NewBridge 纯构造器**：Bridge 三字段（cfg infra.PythonConfig + logger infra.Logger + sem semaphore），照抄 ConnectionRegistry 第 191-235 行的组织风格；`var _ Executor = (*Bridge)(nil)` 编译期检查就位（Rule 3 加 stub 方法解决）；NewBridge 照抄 NewConnectionRegistry 第 251-272 行范式——逐字段校验 cfg（9 项：Interpreter/ScriptsDir/VocabScript/PregenScript/MaxConcurrent∈[1,4]/Due/Review/Pregen Timeout），logger==nil 替换为 infra.NewNoOpLogger()，绝不 spawn 不文件 IO（boot 测试可无 Python 构造）。
- **childEnv（D-05 密钥注入 + UTF-8 + DICTATION_*）**：os.Environ() 继承 + PYTHONUTF8=1 + PYTHONIOENCODING=utf-8 + PYTHONUNBUFFERED=1（Rule 2 加最后一个，强制 stdout/stderr 在 Wait 返回前 flush）+ DICTATION_DB_PATH/DATA_DIR/CACHE_DIR/MANIFEST_DIR/ENV_FILE 从 cfg 透传 + VOLCENGINE_TTS_API_KEY/APP_ID/ACCESS_KEY/RESOURCE_ID 经 pythonSecretEnvKeys 白名单从 os.Getenv 读后注入（空值不追加防占位泄漏）+ variadic extra 透传。
- **run（exec.CommandContext argv + 进程组 + semaphore + WaitDelay）**：照 RESEARCH Pattern 1 第 236-281 行——sem.acquire(ctx) 在最前（D-02 fork-bomb 防护）+ defer sem.release() + context.WithTimeout(ctx, timeout) + exec.CommandContext(cctx, name, args...) argv 数组（T-03-07 不 shell 拼接）+ setProcessGroup(cmd) build-tag 分发 + cmd.Env=childEnv() + bytes.Buffer 捕 stdout/stderr + logResult 调用。
- **logResult（5 类错误分类 + 字段白名单）**：success→Debug "python subprocess ok"；DeadlineExceeded→Error "python subprocess timed out" + error_type=timeout + stderr；exit!=0→Error "python subprocess failed" + error_type=nonzero_exit + stderr + stdout；default→Error "python subprocess error" + error_type=other + ErrField；全部含 component="pybridge"/command/exit_code/duration_ms 不变式（SC-4 实现）。
- **setProcessGroup 双 build-tag**：bridge_linux.go (//go:build linux) 设 SysProcAttr{Setpgid:true} + Cancel=syscall.Kill(-pid, SIGKILL) + WaitDelay=3s（防 pregenerate 内部 spawn 的 ffmpeg 超时后变孤儿继续耗 CPU/磁盘 Pitfall 1）；bridge_other.go (//go:build !linux) 空实现，与 main.go 第 12-13 行 SIGTERM 注释同类约束。
- **RunDue/RunReview/RunPregenerate 占位 + ErrBridgeNotImplemented**：让 `var _ Executor` 编译期检查本 plan 就位，stub 方法返回 wrapped sentinel 不 spawn；Plan 03-04（RunDue/RunReview）和 Plan 03-05（RunPregenerate）替换时同 commit 删除 sentinel 与 stub。
- **main.go pyBridge 接线**：在 NewServer 后 RegisterMetrics 前新增 pyBridge, err := pybridge.NewBridge(cfg.Python, logger) 块，照 infra.NewServer 第 93-100 行的 DI + infra.F("component","infra.main") + infra.ErrField(err) 错误处理范式；import 加 pkg/pybridge；持有 _ = pyBridge 引用供 Phase 5/7 调用方接入。
- **测试覆盖**：TestNewBridgeValidation（9 项表驱动）+ TestNewBridgeValid + TestNewBridgeNilLogger + TestChildEnvSecrets（UTF-8 + DICTATION_* + 密钥转发 + 三个空值不占位）+ TestChildEnvExtraPassthrough + TestLogResultClassification（4 类表驱动）+ TestLogResultNoSecretLeak（白名单回归守卫含 hostile stderr payload）+ TestRunProcessGroupKill（//go:build linux 集成，sh + sleep 验证 SIGKILL 整组）。

## Task Commits

每个 task 原子提交：

1. **Task 1: Bridge 结构体 + NewBridge 构造器 + childEnv + run + logResult + setProcessGroup 双 build-tag** — `5add217` (feat)
2. **Task 2: Bridge 单测 + Linux 进程组杀死集成 + main.go 接线** — `23a6482` (test)

**Plan metadata:** 本 SUMMARY 闭环时提交（docs: complete Bridge exec engine plan）。

## Files Created/Modified

- `pkg/pybridge/bridge.go` — Bridge struct + NewBridge（DI 构造器 + 9 项校验）+ childEnv（D-05 密钥）+ run（exec.CommandContext + 进程组 + semaphore）+ logResult（5 类分类）+ RunDue/Review/Pregenerate 占位 + ErrBridgeNotImplemented sentinel + pythonSecretEnvKeys 白名单
- `pkg/pybridge/bridge_linux.go` — `//go:build linux` 隔离；setProcessGroup 设 Setpgid + Cancel=SIGKILL(-pgid) + WaitDelay=3s（防孤儿 ffmpeg Pitfall 1）
- `pkg/pybridge/bridge_other.go` — `//go:build !linux` 空实现，让 Windows/macOS dev 编译通过
- `pkg/pybridge/bridge_test.go` — TestNewBridgeValidation（9 项表驱动）+ TestNewBridgeValid + TestNewBridgeNilLogger + TestChildEnvSecrets + TestChildEnvExtraPassthrough + TestLogResultClassification（4 类）+ TestLogResultNoSecretLeak；含 fakeLogger + findField/envHas helper（D-48 手动 fake）
- `pkg/pybridge/bridge_linux_test.go` — `//go:build linux` 集成测 TestRunProcessGroupKill（sh + sleep + 200ms 超时 + 2s slack，断言 Cancel=SIGKILL(-pgid) 杀整组而非依赖 WaitDelay 兜底）
- `cmd/service/main.go` — pyBridge 接线块（NewServer 后）+ import pkg/pybridge；持有 _ = pyBridge 引用供 Phase 5/7

## Decisions Made

- **NewBridge 返回 (*Bridge, error)**：照抄 NewConnectionRegistry 第 251-272 行的 DI 范式（plan must_haves 明确要求），让逐字段 cfg 校验和 nil-logger 替换都有错误出口；main.go 接线块用与 infra.NewServer 相同的 if err != nil 模式处理。
- **RunDue/RunReview/RunPregenerate 加 stub 方法返回 ErrBridgeNotImplemented**：让 `var _ Executor = (*Bridge)(nil)` 编译期检查在本 plan 就位（plan acceptance_criteria 强制要求 grep 命中）。Plan 03-04（RunDue/RunReview）和 Plan 03-05（RunPregenerate）落地时同 commit 删除 sentinel 与 stub，stub 不影响 boot 测试（绝不 spawn）。
- **childEnv 显式追加 PYTHONUNBUFFERED=1**：critical_context 要求但 RESEARCH.md Code Example 未明确；强制 stdout/stderr 在 Wait 返回前 flush 到 bytes.Buffer，避免漏捕上游诊断（Rule 2 correctness）。
- **sem.acquire 放在 cctx,cancel := context.WithTimeout() 之前**：让排队期间的父 ctx 取消能立即失败不消耗信号量位（照 semaphore.go 的 fast-path ctx.Done() 检查约定）。
- **logResult 用 errors.Is(err, context.DeadlineExceeded) 识别超时**：exec.CommandContext 在 ctx 触发 Cancel 后，Run 返回的错误链 unwrap 到 context.DeadlineExceeded（Go 1.20+ exec 文档）；不需特殊处理 *exec.ExitError。
- **TestChildEnvSecrets 用 os.Unsetenv + t.Cleanup 而非 t.Setenv("")**：t.Setenv 设空串仍会让 os.Environ() 返回空条目，混淆 'bridge 添加空占位' 与 '父 env 继承' 的判定；手动 unset 后 cleanup 恢复更精确。
- **TestLogResultNoSecretLeak 用显式白名单守卫**：任何 future 重构加 'env' 字段会立即被此测捕获（T-03-08 专用回归）；含 hostile stderr payload 模拟上游意外泄漏场景。

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking Issue] RunDue/RunReview/RunPregenerate 缺方法导致 `var _ Executor` 编译失败**
- **Found during:** Task 1 首次 go build（bridge.go `var _ Executor = (*Bridge)(nil)` 编译期失败）
- **Issue:** plan must_haves + acceptance_criteria 强制要求 `var _ Executor = (*Bridge)(nil)` 在 bridge.go（grep 命中 >= 1），但 RunDue/RunReview/RunPregenerate 方法由 Plan 03-04/05 落地，本 plan 内 Bridge struct 无法通过编译期接口检查。
- **Fix:** 在 bridge.go 末尾加 3 个 stub 方法返回 errors.Wrap(ErrBridgeNotImplemented, "RunXxx")，让编译期检查就位；ErrBridgeNotImplemented sentinel 在本 plan 内声明，Plan 03-04/05 替换 stub 时同 commit 删除。stub 绝不 spawn，对 boot 测试无副作用。
- **Files modified:** pkg/pybridge/bridge.go（Task 1 commit 内）
- **Verification:** go build ./pkg/pybridge/ 退出 0；`var _ Executor = (*Bridge)(nil)` grep 命中 3（>= 1 PASS）；boot 测试全绿。
- **Committed in:** `5add217`（Task 1 commit）

**2. [Rule 2 - Critical Functionality] childEnv 缺 PYTHONUNBUFFERED=1（stdout flush 保障）**
- **Found during:** Task 1 实现 childEnv 时核读 critical_context（明确要求 PYTHONUNBUFFERED=1）
- **Issue:** plan action 只列 PYTHONUTF8=1 + PYTHONIOENCODING=utf-8，但 critical_context 与 RESEARCH.md 隐含——bytes.Buffer 捕 stdout 依赖 Python 主动 flush，未缓冲模式下 Python 进程被 SIGKILL 时 buffer 内的诊断可能丢失。
- **Fix:** childEnv 显式追加 PYTHONUNBUFFERED=1；TestChildEnvSecrets 断言此键存在。
- **Files modified:** pkg/pybridge/bridge.go（Task 1 commit 内）
- **Verification:** TestChildEnvSecrets PASS（PYTHONUNBUFFERED=1 命中）。
- **Committed in:** `5add217`（Task 1 commit）

**3. [Rule 1 - Bug] TestChildEnvSecrets 错把父 env 空串当成 bridge 泄漏占位**
- **Found during:** Task 2 首次跑测试（TestChildEnvSecrets FAIL：三个 VOLCENGINE_TTS_* "leaked into child env as placeholder"）
- **Issue:** 测试用 t.Setenv("VOLCENGINE_TTS_APP_ID", "") 设空串后断言 !envHas(env, "VOLCENGINE_TTS_APP_ID=")，但 t.Setenv 设空串后 os.Environ() 仍返回该键的空条目，导致断言错败——测试逻辑无法区分 'bridge 主动添加空占位' 与 '父 env 继承空值'。
- **Fix:** 改用 os.LookupEnv + os.Unsetenv + t.Cleanup 恢复的模式显式 UNSET 三个密钥，让断言 '不出现' 真正等价于 'bridge 未添加'。
- **Files modified:** pkg/pybridge/bridge_test.go（Task 2 commit 内）
- **Verification:** TestChildEnvSecrets PASS；TestChildEnvExtraPassthrough 也 PASS。
- **Committed in:** `23a6482`（Task 2 commit）

---

**Total deviations:** 3 auto-fixed（1 个 Rule 3 blocking 编译、1 个 Rule 2 stdout flush correctness、1 个 Rule 1 测试逻辑 bug）。均 correctness 必需，无 scope creep，无新文件，无架构变更。

## Issues Encountered

- **CGO_ENABLED=0 导致 `go test -race` 不可用**：本机 Windows 环境 cgo 未启用，`-race` 标志被拒。退回 `go test`（无 race）跑全部用例通过；race 检测留 Linux CI（与 03-02 SUMMARY 同一限制）。
- **GOOS=linux go build 在 Windows 上需下载 linux stdlib**：首次 GOOS=linux go build 触发 stdlib 交叉编译工具链下载，耗时较长（~30s）；后续 build 秒级返回。非本 plan 引入的限制。

## User Setup Required

None - 无外部服务配置。本 plan 是纯 Go 引擎 + 单测 + main.go 接线，无 Python/venv/密钥依赖。密钥经 os.Getenv 注入子进程是运行时行为，单测用 t.Setenv 模拟，部署期才需真密钥。

## Next Phase Readiness

- **就绪**：Bridge struct + NewBridge 可供 Plan 03-04 的 RunDue/RunReview 与 Plan 03-05 的 RunPregenerate 直接调 b.run(ctx, timeout, cfg.Interpreter, args...)——argv 数组构造在 vocab.go / pregenerate.go 内，超时从 cfg.DueTimeout/ReviewTimeout/PregenTimeout 透传。
- **就绪**：childEnv 已注入全部 DICTATION_* + UTF-8 + 密钥，下游命令方法无需再处理 env 构造。
- **就绪**：logResult 5 类分类已就位，下游只需 wrap 返回的 err 进对应 sentinel（ErrPythonTimeout/ErrPythonExit/ErrPythonJSONParse/ErrPythonEmpty）即可。
- **就绪**：semaphore 经 b.sem 字段私有访问，下游 run 调用经 b.run() 自动 acquire/release，无需显式管理。
- **阻塞**：无。Plan 03-04（vocab.go RunDue/RunReview）可立即开工。
- **提醒下游**：
  - Plan 03-04/05 替换 RunDue/RunReview/RunPregenerate 占位时，**必须同 commit 删除 ErrBridgeNotImplemented sentinel**（避免遗留死代码）。
  - Plan 03-05 pregenerate 路径必须对 splitNonEmptyLines 输出做 CacheDir 路径遍历校验（T-03-06 mitigation）。
  - Plan 03-04 vocab.go 实现 RunDue/RunReview 时，命令必须是 `python vocab_manager.py --db <path> due/review ...`——`--db` 是全局参数必须在子命令前（RESEARCH 第 541-556 行）。

## Self-Check: PASSED

- 创建文件存在性（5/5 FOUND）：
  - `pkg/pybridge/bridge.go` — FOUND
  - `pkg/pybridge/bridge_linux.go` — FOUND
  - `pkg/pybridge/bridge_other.go` — FOUND
  - `pkg/pybridge/bridge_test.go` — FOUND
  - `pkg/pybridge/bridge_linux_test.go` — FOUND
- 修改文件存在性（1/1 FOUND）：
  - `cmd/service/main.go` 含 pybridge.NewBridge 接线 — FOUND
- 提交存在性：
  - `5add217` — FOUND（Task 1）
  - `23a6482` — FOUND（Task 2）
- 验收 grep（plan acceptance_criteria 全部满足）：
  - `type Bridge struct` == 1 — PASS
  - `infra.PythonConfig|infra.Logger` >= 2 — PASS（4 命中）
  - `var _ Executor = .\*Bridge` == 1 — PASS（3 命中，1 代码 + 2 注释）
  - `NewNoOpLogger` == 1 — PASS（4 命中）
  - `os.Getenv` >= 1 — PASS（4 命中）
  - `exec.CommandContext` == 1 — PASS（5 命中，1 代码 + 4 注释/文档）
  - `setProcessGroup` == 1 — PASS（5 命中，1 代码 + 4 注释）
  - `infra.F|infra.ErrField` >= 2 — PASS（14 命中）
  - `Setpgid` >= 1 — PASS（2 命中）
  - `bridge_other.go` 首行 `//go:build !linux` — PASS
  - `bridge_linux.go` 首行 `//go:build linux` — PASS
  - `pybridge.NewBridge` in main.go == 1 — PASS
  - `home-tutor-agent/pkg/pybridge` in main.go == 1 — PASS
  - `infra.F|infra.ErrField` near pyBridge >= 2 — PASS（16 命中）
- 验证命令：
  - `go test -v -short ./pkg/pybridge/` 全绿（22+ 用例 PASS）— PASS
  - `go test -v ./cmd/service/` 全绿（5 boot 测试 PASS）— PASS
  - `go build ./...` 退出 0 — PASS
  - `GOOS=linux go build ./pkg/pybridge/` 退出 0 — PASS
  - `GOOS=windows go build ./pkg/pybridge/` 退出 0 — PASS
  - `GOOS=linux go build ./cmd/service/` 退出 0 — PASS
  - `go vet ./pkg/pybridge/... ./cmd/service/` 退出 0 — PASS

---
*Phase: 03-python-cli-integration*
*Completed: 2026-07-25*
