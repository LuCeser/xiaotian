---
phase: 03-python-cli-integration
plan: 02
subsystem: infra
tags: [go, pybridge, subprocess-bridge, executor-interface, sentinel-errors, json-parser, semaphore, contract-layer]

# Dependency graph
requires:
  - phase: 01-resource-constrained-infrastructure
    provides: typed sentinel + errors.Is 约定（worker.go）、构造器 DI 范式、表驱动测试 + waitOrTimeout helper
  - phase: 03-python-cli-integration
    provides: 03-01 落地的 infra.PythonConfig（13 字段，含 MaxConcurrent 供信号量 N 用）
provides:
  - pybridge.Executor 接口（RunDue/RunReview/RunPregenerate 三方法，D-11 通用包装器契约）
  - pybridge.DueOpts/DueResult/DueCard、ReviewOpts/ReviewResult/UpdatedCard、PregenOpts/PregenResult 类型
  - 4 sentinel 错误（ErrPythonExit/ErrPythonTimeout/ErrPythonJSONParse/ErrPythonEmpty）
  - pybridge.ExitError typed struct（Cmd/ExitCode/Stderr/Err + Unwrap）
  - pybridge.extractTrailingJSON 纯函数（混合 stdout 提取末尾 JSON，Pitfall 2 防护）
  - pybridge.splitNonEmptyLines 纯函数（按行路径解析，Pitfall 3 防护）
  - pybridge.semaphore + newSemaphore + acquire(ctx)/release（D-02 全局有界信号量）
  - pkg/pybridge/testdata/ 4 个黄金 fixture（due_cards/review_updated/review_with_warnings/pregenerate_paths）
affects: [03-03, 03-04, 03-05, phase-05, phase-07]

# Tech tracking
tech-stack:
  added: []  # 全 Go stdlib（encoding/json/bytes/strings/context/fmt/errors）+ 既有 github.com/pkg/errors v0.9.1，无新包
  patterns:
    - tail-backward-scan JSON extraction（尾部回扫 + 括号栈计数 + 字符串字面量/转义感知）
    - blocking-via-ctx.Done() semaphore（照 worker.go Submit select idiom，default 分支换 ctx.Done()）
    - fast-path ctx check（已取消 ctx 不占位，避免 select 非确定性）
    - typed sentinel + ExitError（句号结尾命名照 worker.go；ExitError.Unwrap 让 errors.Is/errors.As 双通）
    - 同包白盒单测 + 表驱动 + 子测试 + 黄金 fixture（照 worker_test.go/config_test.go）

key-files:
  created:
    - pkg/pybridge/executor.go
    - pkg/pybridge/executor_test.go
    - pkg/pybridge/parse.go
    - pkg/pybridge/parse_test.go
    - pkg/pybridge/semaphore.go
    - pkg/pybridge/semaphore_test.go
    - pkg/pybridge/testdata/due_cards.json
    - pkg/pybridge/testdata/review_updated.json
    - pkg/pybridge/testdata/review_with_warnings.txt
    - pkg/pybridge/testdata/pregenerate_paths.txt
  modified: []

key-decisions:
  - "DueCard / UpdatedCard 字段对齐 vocab_manager.py 真实 JSON 契约（id/word/meaning/example/unit/state/due/stability/difficulty；word/rating/next_due/state/stability），而非 plan 草案的 translation/pronunciation/[]string——以源码为真值（Rule 1）"
  - "ReviewResult.Updated 采用 []UpdatedCard 而非 []string——vocab_manager.py:437-443 的 updated 是对象数组，[]string 会丢弃 rating/next_due/state/stability 数据（Rule 1）"
  - "extractTrailingJSON 用尾部回扫 + 括号栈 + 字符串字面量感知实现（plan 指定策略）；转义引号经前置反斜杠连续计数判定（偶数=真终结符，奇数=转义内容）"
  - "semaphore.acquire 加 fast-path ctx.Done() 检查——已取消 ctx 立即返回 ctx.Err() 不占位，避免 Go select 在两分支均就绪时非确定选 acquire 分支（Rule 1 修正）"
  - "sentinel 错误用 stdlib errors.New（照 worker.go 范式），错误包装用 github.com/pkg/errors.Wrap（照 config.go 范式）——两支既有约定共存"
  - "newSemaphore(<=0) panic 而非返回 error——程序设计错误，PythonConfig.Validate 上游已拦 MaxConcurrent∈[1,4]，panic 是第二道防线"

patterns-established:
  - "Pattern: pybridge 公共契约层 logger-free——本 plan 的接口/类型/纯函数/信号量均不注入 infra.Logger；日志注入点在 Plan 03-03 的 Bridge 实现"
  - "Pattern: 解析器只切分/提取不校验语义——extractTrailingJSON 只保证字节是合法 JSON 值，json.Unmarshal 由调用方做；splitNonEmptyLines 只切行，路径遍历校验留 Plan 03-05（T-03-06）"
  - "Pattern: ExitError 字段日志白名单（Cmd/ExitCode/Stderr）——密钥绝不在此结构体（V7/SC-4），密钥走子进程 env（D-05）"

requirements-completed: [PY-01]

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "pybridge.Executor 接口（RunDue/RunReview/RunPregenerate 三方法）+ 4 sentinel 错误（ErrPythonExit/Timeout/JSONParse/Empty，句号结尾，errors.Is 检测）+ ExitError typed struct（Cmd/ExitCode/Stderr/Err + Unwrap，让调用方区分 FSRS/用户错误 vs 基础设施错误）"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/pybridge/executor_test.go#TestExitErrorError / TestExitErrorErrorEmptyStderr / TestExitErrorUnwrap / TestSentinelErrors / TestSentinelErrorsAreDistinct"
        status: pass
      - kind: unit
        ref: "go build ./... 退出 0；go vet ./pkg/pybridge/ 退出 0"
        status: pass
    human_judgment: false
  - id: D2
    description: "DueOpts/DueResult/DueCard、ReviewOpts/ReviewResult/UpdatedCard、PregenOpts/PregenResult 六组 typed 选项/结果结构体——字段对齐 vocab_manager.py:255-265（due）与 vocab_manager.py:437-443（review updated）的真实 JSON 契约（Rule 1 修正 plan 草案的 translation/pronunciation/[]string）"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/pybridge/parse_test.go#TestExtractTrailingJSON_MixedWithWarnings（json.Unmarshal 进 []UpdatedCard 验证字段对齐）"
        status: pass
    human_judgment: false
  - id: D3
    description: "extractTrailingJSON(stdout) (json.RawMessage, error) 纯函数：尾部回扫 + 括号栈计数 + 字符串字面量/转义感知，从混合 stdout（Warning/Error 前缀行 + 末尾 JSON）提取最后一个完整 JSON 值（RESEARCH Pattern 2 / Pitfall 2 防护）"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/pybridge/parse_test.go#TestExtractTrailingJSON（9 子用例：纯 JSON/混合/嵌套/尾空白/对象值/无 JSON/不匹配括号/空输入/纯空白）+ TestExtractTrailingJSON_MixedWithWarnings（Pitfall 2 验收点，断言整段非 JSON + 提取结果 json.Valid + Unmarshal 进 UpdatedCard）"
        status: pass
    human_judgment: false
  - id: D4
    description: "splitNonEmptyLines(stdout) []string 纯函数：按行切分 pregenerate mp3 路径，过滤空行 + TrimSpace（CRLF 容忍）；只切分不校验路径（T-03-06 路径遍历防护留 Plan 03-05）（RESEARCH Pattern 3 / Pitfall 3）"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/pybridge/parse_test.go#TestSplitNonEmptyLines（7 子用例：fixture/尾换行/空行/CRLF/前导空白/空输入/纯空白）+ TestSplitNonEmptyLines_FixtureContent（Pitfall 3 验收点，断言 golden fixture 精确 round-trip）"
        status: pass
    human_judgment: false
  - id: D5
    description: "semaphore(chan struct{}, cap N) + newSemaphore + acquire(ctx)/release()：D-02 全局有界信号量，select idiom 照 worker.go Submit 但用 ctx.Done() 替代 default 实现阻塞排队（RESEARCH Pattern 5）；acquire 加 fast-path ctx 检查避免非确定性（Rule 1）"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/pybridge/semaphore_test.go#TestSemaphoreAcquireRelease / TestSemaphoreAcquireBlockedThenReleased / TestSemaphoreAcquireOnCancelledCtx / TestSemaphoreZeroCapPanics / TestSemaphoreNegativeCapPanics / TestSemaphoreConcurrentAcquirers"
        status: pass
    human_judgment: false
  - id: D6
    description: "pkg/pybridge/testdata/ 四个黄金 fixture：due_cards.json（2 card，对齐 vocab_manager:255-265）、review_updated.json（updated 数组）、review_with_warnings.txt（Warning 前缀 + 末尾 JSON，Pitfall 2 验收用，整段非 JSON）、pregenerate_paths.txt（3 条 mp3 绝对路径）"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/pybridge/parse_test.go 经 loadFixture 读 4 个 fixture 全部成功 round-trip（due_cards/review_with_warnings 经 extractTrailingJSON、pregenerate_paths 经 splitNonEmptyLines）"
        status: pass
    human_judgment: false

# Metrics
duration: ~10min active execution
completed: 2026-07-24
status: complete
---

# Phase 3 Plan 02: pybridge 公共契约层 Summary

**pkg/pybridge 暴露 Executor 接口（RunDue/RunReview/RunPregenerate）+ 6 组 typed 选项/结果（字段对齐 vocab_manager.py 真实 JSON 契约）+ 4 sentinel 错误 + ExitError + extractTrailingJSON（尾部回扫提取末尾 JSON）+ splitNonEmptyLines（按行路径）+ D-02 全局有界信号量 + 4 个黄金 fixture**

## Performance

- **Duration:** ~10 min 活跃执行（Task 1 + Task 2，含 Rule 1 修正迭代）
- **Started:** 2026-07-24T22:49:34+08:00（Task 1 commit `aba73ac`）
- **Completed:** 2026-07-24T22:55:48+08:00（Task 2 commit `7288acd`）
- **Tasks:** 2（Task 1 Executor 接口/typed/sentinel/ExitError；Task 2 解析器/信号量/fixture）
- **Files modified:** 10 新建（6 个 .go + 4 个 testdata fixture）

## Accomplishments

- **Executor 接口契约就绪**：`pybridge.Executor` 三方法（RunDue/RunReview/RunPregenerate），照 worker.go `Task` 接口的小聚焦形状（D-43）；接口上方文档块统一说明三路径语义与"绝不暴露 raw stdout/stderr"不变式；`var _ Executor = (*Bridge)(nil)` 编译期检查推迟到 Plan 03-03。
- **6 组 typed 选项/结果 + 2 typed 卡片结构**：DueOpts（6 字段，对应 vocab_manager due 的 --category/--limit/--units/--exclude/--peek/--fetch-lock-hours）、ReviewOpts（4 字段，含 UseFile 大负载走 --file）、PregenOpts（5 字段，对应 pregenerate 的 --words/--language/--words-per-segment/--prefix/--voice）；DueCard（9 字段对齐真实 due JSON）、UpdatedCard（5 字段对齐真实 review updated JSON）；ReviewResult.Updated 为 []UpdatedCard 而非 plan 草案的 []string。
- **4 sentinel 错误 + ExitError typed struct**：ErrPythonExit/ErrPythonTimeout/ErrPythonJSONParse/ErrPythonEmpty，句号结尾照 worker.go 约定，调用方经 errors.Is 检测（D-04，绝不字符串匹配）；ExitError 含 Cmd/ExitCode/Stderr/Err + Unwrap()，Error() 格式稳定（`python command "<cmd>" exited <code>: <stderr>`）供日志扫描器，errors.Is/errors.As 双通让调用方区分 FSRS/用户错误（exit 1 + stderr 诊断）vs 基础设施错误（超时/解析）。
- **extractTrailingJSON（Pitfall 2 防护）**：尾部回扫 + 括号栈计数 + 字符串字面量/转义感知，从 vocab_manager 的混合 stdout（Warning/Error 前缀行 + 末尾 JSON）提取最后一个完整 JSON 值；转义引号经前置反斜杠连续计数判定（偶数=真终结符）。9 子用例表驱动 + TestExtractTrailingJSON_MixedWithWarnings 验收点（断言整段非 JSON + 提取结果 json.Valid + Unmarshal 进 UpdatedCard）。
- **splitNonEmptyLines（Pitfall 3 防护）**：按行切分 pregenerate 的 mp3 绝对路径 stdout（非 JSON），过滤空行 + TrimSpace 容忍 CRLF；只切分不校验路径（T-03-06 路径遍历防护留 Plan 03-05 桥层）。7 子用例 + TestSplitNonEmptyLines_FixtureContent 验收点（golden fixture 精确 round-trip）。
- **semaphore（D-02）**：`chan struct{}` 容量 N + acquire(ctx)/release()，select idiom 照 worker.go Submit 但用 ctx.Done() 分支替代 default 实现阻塞排队（RESEARCH Pattern 5）；newSemaphore(<=0) panic 防 fork-bomb；Rule 1 修正后 acquire 加 fast-path ctx 检查避免已取消 ctx 与空位时的 select 非确定性。6 用例覆盖阻塞/释放/取消/panic/并发。
- **4 个黄金 fixture**：due_cards.json（2 card，对齐 vocab_manager:255-265 真实字段）、review_updated.json、review_with_warnings.txt（3 行 Warning 前缀含 `{unset}` 干扰括号 + 末尾 review JSON，Pitfall 2 验收用）、pregenerate_paths.txt（3 条 mp3 绝对路径）。

## Task Commits

每个 task 原子提交：

1. **Task 1: Executor 接口 + typed 选项/结果 + 4 sentinel + ExitError** — `aba73ac` (feat)
2. **Task 2: extractTrailingJSON + splitNonEmptyLines + semaphore + 黄金 fixture** — `7288acd` (feat，含 Rule 1 semaphore 修正)

**Plan metadata:** 待本 SUMMARY 闭环时提交（docs: complete pybridge contract layer plan）。

## Files Created/Modified

- `pkg/pybridge/executor.go` — Executor 接口 + 6 组 typed 选项/结果 + 2 typed 卡片 + 4 sentinel + ExitError（含 Unwrap）
- `pkg/pybridge/executor_test.go` — TestExitErrorError/ErrorEmptyStderr/Unwrap + TestSentinelErrors（句号结尾）+ TestSentinelErrorsAreDistinct（互异性守卫）
- `pkg/pybridge/parse.go` — extractTrailingJSON（尾部回扫 + 括号栈 + 字符串/转义感知）+ splitNonEmptyLines（按行 + TrimSpace）
- `pkg/pybridge/parse_test.go` — 表驱动 9+7 子用例 + 两个 Pitfall 验收点（_MixedWithWarnings / _FixtureContent）+ loadFixture helper
- `pkg/pybridge/semaphore.go` — semaphore 类型 + newSemaphore（panic on <=0）+ acquire（fast-path ctx + 阻塞 select）+ release
- `pkg/pybridge/semaphore_test.go` — 6 用例：阻塞/释放/已取消 ctx/panic(0)/panic(-1)/并发（max in-flight <= cap）
- `pkg/pybridge/testdata/due_cards.json` — 2 card 黄金样本（id/word/meaning/example/unit/state/due/stability/difficulty）
- `pkg/pybridge/testdata/review_updated.json` — `{"updated": [...]}` 黄金样本（2 UpdatedCard）
- `pkg/pybridge/testdata/review_with_warnings.txt` — 混合 stdout（3 行 Warning 含 `{unset}` 干扰 + 末尾 review JSON），Pitfall 2 验收
- `pkg/pybridge/testdata/pregenerate_paths.txt` — 3 条 `/opt/hta/cache/*.mp3` 绝对路径，Pitfall 3 验收

## Decisions Made

- **DueCard/UpdatedCard 字段照源码不照草案**：plan 草案写"DueCard 含 word/translation/pronunciation"与"Updated []string"，但 vocab_manager.py:255-265 的真实 due JSON 是 id/word/meaning/example/unit/state/due/stability/difficulty，vocab_manager.py:437-443 的真实 updated 是 word/rating/next_due/state/stability 对象数组。Rule 1 修正为对齐源码——下游 Plan 03-04 才能直接 json.Unmarshal。
- **extractTrailingJSON 实现策略照 plan**：尾部回扫 + 括号栈计数。转义引号用"前置反斜杠连续计数"判定（偶数=真终结符），正确处理 `\"` 与 `\\\"` 等嵌套转义。
- **sentinel 用 stdlib errors.New，错误包装用 pkg/errors.Wrap**：照 worker.go 与 config.go 两支既有约定共存——sentinel 是 worker.go 范式（stdlib），Wrap 是 config.go 范式（pkg/errors）。errors.Is/errors.As 在两支下均工作。
- **newSemaphore(<=0) panic 而非 error**：PythonConfig.Validate 已在上游拦 MaxConcurrent∈[1,4]，构造期非法 n 是程序设计错误而非运行时条件，panic 是第二道防线（与 worker.go 的 ErrInvalidWorkerConfig 返回 error 风格不同，因为 semaphore 是 internal 工具不暴露给调用方校验）。
- **logger-free 契约层**：本 plan 的接口/类型/纯函数/信号量均不注入 infra.Logger；日志注入点在 Plan 03-03 的 Bridge 实现（logResult 用 infra.Logger + infra.F() + infra.ErrField()，照 main.go D-25 canonical 那一支）。

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] ReviewResult.Updated 与 DueCard 字段类型对齐源码（plan 草案误写）**
- **Found during:** Task 1（实现 typed 结果结构前读 vocab_manager.py:255-265, 437-443 核对契约）
- **Issue:** plan 草案写"ReviewResult.Updated []string 对应 {"updated":[...]} JSON"与"DueCard 含 word/translation/pronunciation"，但 vocab_manager.py 真实契约是：due card 对象有 9 字段（id/word/meaning/example/unit/state/due/stability/difficulty，无 translation/pronunciation）；review 的 updated 是对象数组（word/rating/next_due/state/stability，非裸字符串数组）。若照草案实现，下游 Plan 03-04 的 json.Unmarshal 会丢数据或字段错配。
- **Fix:** ReviewResult.Updated 改为 []UpdatedCard（新增 UpdatedCard typed struct，5 字段对齐源码 json tags）；DueCard 改为 9 字段对齐源码（删除草案的 translation/pronunciation，新增 meaning/example/unit/state/due/stability/difficulty/id）。
- **Files modified:** pkg/pybridge/executor.go（Task 1 commit 内）
- **Verification:** TestExtractTrailingJSON_MixedWithWarnings 把提取出的 JSON Unmarshal 进 []UpdatedCard 并断言 first.Word=="goodbye" / first.Rating=="again"，验证字段对齐。
- **Committed in:** `aba73ac`（Task 1 commit）

**2. [Rule 1 - Bug] semaphore.acquire 在已取消 ctx 与空位同时就绪时非确定返回 nil**
- **Found during:** Task 2（TestSemaphoreAcquireOnCancelledCtx 首跑失败）
- **Issue:** 原实现只有单个 `select { case s <- struct{}{}: return nil; case <-ctx.Done(): return ctx.Err() }`。当 ctx 已取消且信号量有空位时，两分支均就绪，Go select 随机选一——有时返回 nil（占了位），违反 Go "ctx 已 done => 无副作用" 惯例与 plan "排队期间超时/取消 => 调用方转 backpressure" 的意图。
- **Fix:** acquire 加 fast-path `select { case <-ctx.Done(): return ctx.Err(); default: }`，已取消 ctx 立即返回不占位；之后再走阻塞 select 排队。
- **Files modified:** pkg/pybridge/semaphore.go（Task 2 commit 内）
- **Verification:** TestSemaphoreAcquireOnCancelledCtx 复跑通过（已取消 ctx 返回 context.Canceled 且不占位，后续 live ctx acquire 立即成功）；TestSemaphoreAcquireRelease 的第三次 acquire 仍正确阻塞到 50ms 超时。
- **Committed in:** `7288acd`（Task 2 commit）

---

**Total deviations:** 2 auto-fixed（均 Rule 1 correctness bug，1 个类型契约对齐源码 + 1 个并发原语非确定性）
**Impact on plan:** 两处 auto-fix 都是 correctness 必需——前者保证下游 Plan 03-04 json.Unmarshal 字段对齐，后者保证 D-02 信号量在已取消 ctx 下的可预测行为。无 scope creep，无新文件，无架构变更。

## Issues Encountered

- **CGO_ENABLED=0 导致 `go test -race` 不可用**：本机 Windows 环境 cgo 未启用，`-race` 标志被拒绝。已退回 `go test`（无 race）跑全部用例通过；race 检测留 Linux CI（RESEARCH Validation architecture 已规划 `go test -race -v ./...` 在 Linux job 跑）。非本 plan 引入的限制。
- **pkg/ws `TestHubStopsOnContextCancel` 偶发失败（既有，非本 plan 回归）**：03-01 SUMMARY 已记录到 deferred-items.md。本 plan 仅触 pkg/pybridge/*，`go build ./...` 全绿，未回归该测试。

## User Setup Required

None - 无外部服务配置。本 plan 是纯 Go 契约层 + 单测，无 Python/venv/密钥依赖（密钥经 os.Getenv 注入子进程是 Plan 03-03 Bridge 实现期的事，D-05）。

## Next Phase Readiness

- **就绪**：`pybridge.Executor` 接口 + typed 选项/结果 + sentinel + ExitError 可供 Plan 03-03 的 `NewBridge(cfg PythonConfig, logger infra.Logger) (*Bridge, error)` 直接实现（构造器照 `pkg/infra/connection.go:251-272` 的 NewConnectionRegistry 范式：逐字段校验 cfg + nil-logger 替换 + 内部建 semaphore(cap cfg.MaxConcurrent)）。
- **就绪**：`extractTrailingJSON` 可供 Plan 03-04 的 RunDue/RunReview 直接调（解析 vocab_manager 混合 stdout）；`splitNonEmptyLines` 可供 Plan 03-05 的 RunPregenerate 直接调（解析按行 mp3 路径）。
- **就绪**：`semaphore.acquire(ctx)/release()` 可供 Plan 03-03 的 Bridge.run 包在每次 subprocess 调用前后（`defer sem.release()` 配对）。
- **就绪**：4 个黄金 fixture 可供 Plan 03-04/05 的解析单测复用（loadFixture helper 已在 parse_test.go 中）。
- **阻塞**：无。Phase 03 Wave 1 的 Plan 03-03（Bridge 引擎）可立即开工。
- **提醒下游**：
  - Plan 03-03 实现 Bridge 时，sentinel 错误经 errors.Is 检测（D-04），日志 error_type ∈ {`nonzero_exit`,`timeout`,`json_parse`,`empty`,`other`}（照 RESEARCH.md 第 559-581 行 logResult 范式）。
  - Plan 03-03 的 Bridge compile-time 检查 `var _ Executor = (*Bridge)(nil)` 应放在 bridge.go 或 bridge_test.go（本 plan 推迟）。
  - Plan 03-05 的 pregenerate 路径解析后必须校验路径在配置的 CacheDir 范围内（T-03-06 tampering mitigation，splitNonEmptyLines 故意不做）。

## Self-Check: PASSED

- 创建文件存在性（10/10 FOUND）：
  - `pkg/pybridge/executor.go` — FOUND
  - `pkg/pybridge/executor_test.go` — FOUND
  - `pkg/pybridge/parse.go` — FOUND
  - `pkg/pybridge/parse_test.go` — FOUND
  - `pkg/pybridge/semaphore.go` — FOUND
  - `pkg/pybridge/semaphore_test.go` — FOUND
  - `pkg/pybridge/testdata/due_cards.json` — FOUND
  - `pkg/pybridge/testdata/review_updated.json` — FOUND
  - `pkg/pybridge/testdata/review_with_warnings.txt` — FOUND
  - `pkg/pybridge/testdata/pregenerate_paths.txt` — FOUND
- 提交存在性：
  - `aba73ac` — FOUND（Task 1）
  - `7288acd` — FOUND（Task 2）
- 验收 grep（plan acceptance_criteria 全部满足）：
  - `RunDue|RunReview|RunPregenerate` == 3 — PASS
  - 4 sentinel 名 >= 4 — PASS（13，含声明 + 文档引用）
  - `func.*ExitError.*Unwrap` == 1 — PASS
  - `type DueOpts|ReviewOpts|PregenOpts` == 3 — PASS
  - `func extractTrailingJSON` == 1 — PASS
  - `func splitNonEmptyLines` == 1 — PASS
  - `type semaphore` == 1 — PASS
  - `func.*semaphore.*acquire` == 1 — PASS
- 验证命令：
  - `go test -v -short ./pkg/pybridge/` 全绿（22 子用例 + 8 主用例 PASS）— PASS
  - `go vet ./pkg/pybridge/` 退出 0 — PASS
  - `go build ./...` 退出 0（全仓无回归）— PASS
  - testdata/ 下 4 个 fixture 齐全 — PASS

---
*Phase: 03-python-cli-integration*
*Completed: 2026-07-24*
