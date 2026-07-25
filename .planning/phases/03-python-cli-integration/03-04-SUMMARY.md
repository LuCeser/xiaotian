---
phase: 03-python-cli-integration
plan: 04
subsystem: pybridge
tags: [go, pybridge, subprocess-bridge, vocab-manager, argv-construction, trailing-json-parser, fsrs-passthrough, large-payload-file-fallback]

# Dependency graph
requires:
  - phase: 01-resource-constrained-infrastructure
    provides: infra.PythonConfig（13 字段含 VocabScript/VocabDBPath/DueTimeout/ReviewTimeout/Interpreter）、infra.ErrConfigInvalid、表驱动 + errors.Is 测试范式
  - phase: 03-python-cli-integration
    provides: 03-02 的 Executor 接口 + DueOpts/DueResult/DueCard + ReviewOpts/ReviewResult/UpdatedCard + 4 sentinel + ExitError + extractTrailingJSON + splitNonEmptyLines + 4 个黄金 fixture；03-03 的 Bridge struct + NewBridge + childEnv + run + logResult + setProcessGroup 双 build-tag
provides:
  - (*Bridge).RunDue（PY-02）：vocab_manager.py due 路径，argv 构造 + extractTrailingJSON 解析末尾 JSON 数组进 DueResult.Cards
  - (*Bridge).RunReview（PY-03）：vocab_manager.py review 路径，argv 构造 + extractTrailingJSON 处理 Warning 前缀混合 stdout，Unmarshal 进 ReviewResult.Updated
  - dueArgs（argv 构造器）：cfg.VocabScript + "--db" cfg.VocabDBPath + "due" + 6 个 DueOpts 标志透传
  - reviewArgs（argv 构造器 + cleanup）：自动选 --results（<=4096B 且 <=100 条）或 --file 大负载（os.CreateTemp + cleanup）
  - ErrPythonTimeout/ErrPythonExit/ErrPythonJSONParse/ErrPythonEmpty 在 RunDue/RunReview 的错误分类（context.DeadlineExceeded/exit!=0/parse fail/empty stdout 四类）
affects: [03-05, phase-05, phase-07, phase-10]

# Tech tracking
tech-stack:
  added: []  # 全 Go stdlib（context/encoding/json/os/strconv/strings/time）+ 既有 github.com/pkg/errors v0.9.1，无新包
  patterns:
    - argv-builder-as-pure-function（dueArgs/reviewArgs 是 (cfg, opts) -> []string 的纯函数，无 I/O 无 spawn，全平台表驱动可测）
    - global-flag-before-subcommand（vocab_manager.py:718 argparse 全局 --db 必须在 due/review 子命令前——RESEARCH 第 541-556 行强制验收）
    - rating-passthrough-no-rewrite（ReviewOpts.Results 原样 json.Marshal 到 --results/--file，不校验不改写 4 种 rating——RESEARCH Pitfall 4）
    - payload-size-driven-flag-selection（jsonBytes <= 4096 且 len <= 100 走 --results，否则写 os.CreateTemp 临时文件走 --file；cleanup 闭包封装 os.Remove 让调用方 defer 即可）
    - empty-stdout-pre-check-before-parser（RunDue/RunReview 在 extractTrailingJSON 前显式检 len(stdout)==0 → ErrPythonEmpty，让 4 类 sentinel 真正互斥可达）
    - trailing-json-pipeline（extractTrailingJSON 提末尾 JSON → json.Unmarshal 进 typed struct，覆盖 Warning 前缀混合 stdout 路径——Pitfall 2）

key-files:
  created:
    - pkg/pybridge/vocab.go
    - pkg/pybridge/vocab_test.go
  modified:
    - pkg/pybridge/bridge.go   # 删除 RunDue/RunReview stub（与 vocab.go 真实现冲突），保留 RunPregenerate stub + ErrBridgeNotImplemented 给 03-05 替换

key-decisions:
  - "argv 构造用 cfg.VocabScript 直接作为 args[0] —— 照 RESEARCH 第 547-555 行 Code Example 与 plan must_haves，不主动 filepath.Join(cfg.ScriptsDir, cfg.VocabScript)；调用方（部署/配置）对路径形式负全责（相对/绝对均可），bridge 不二猜"
  - "ErrPythonEmpty 在 RunDue/RunReview 显式前置 len(stdout)==0 检查 —— extractTrailingJSON 对空 stdout 返回的是 wrapped ErrPythonJSONParse（语义混淆），前置检查让 4 类 sentinel（Timeout/Exit/JSONParse/Empty）真正互斥可达，匹配 executor.go 的 ExitError/ErrPythonEmpty 契约（Rule 2 correctness）"
  - "reviewArgs 自动选 --results vs --file 而非读 opts.UseFile/opts.ResultsFile —— plan 明确描述的 size-based 自动选择路径；ReviewOpts 上既有的 UseFile/ResultsFile 字段保留不动（forward-compat，本 plan 不消费者），避免 plan 之外的 scope creep"
  - "reviewArgs 对 nil Results map 兜底为空 map —— json.Marshal(nil) 产 'null' 字面量 vocab_manager.py 会拒绝，显式替换为 map[string]string{} 让序列化结果为 '{}'（脚本接受为 'nothing to review'）， Defence against caller passing nil（Rule 2 robustness）"
  - "defer cleanup() 在 b.run 之前注册 —— 即使 b.run 失败（超时/exit!=0），临时文件也必被删除；defer LIFO 保证 b.logResult 先 fire（log 时文件仍在）再 cleanup（防 leak）"
  - "保留 ErrBridgeNotImplemented 与 RunPregenerate stub —— plan critical_context 明确要求：03-05 才替换 RunPregenerate，本 plan 不删 sentinel 避免 dangling reference / cross-plan 死代码 churn；ErrBridgeNotImplemented 文档注释同步反映 03-04 已落地（Rule 1 注释跟随代码现实）"
  - "logResult 在 RunDue/RunReview 与 b.run 内各调一次（双日志）—— plan acceptance_criteria 强制要求 vocab.go 含 >= 2 处 b.logResult；run() 内 logResult 是 03-03 契约的一部分不能动，双层日志是可接受的冗余（command 字段值不同：'due'/'review' vs 解释器路径）"

patterns-established:
  - "Pattern: argv 构造纯函数 + 表驱动断言 --db-before-subcommand —— 后续 vocab_manager 次要命令（add/stats/list/export 等）若引入，照 dueArgs/reviewArgs 形状即可"
  - "Pattern: 错误分类层在 RunXxx 而非 run() —— run() 只 spawn+capture+logResult，RunXxx 负责 sentinel 包装（Timeout/Exit/JSONParse/Empty）；让 pregenerate（03-05）可重用同一 run() 但用不同 sentinel（splitNonEmptyLines 失败路径）"
  - "Pattern: 大负载自动 --file + cleanup 闭包 —— 后续凡是受平台 argv 长度限制的子命令（如 pregenerate --words）可照此 shape；cleanup 函数化为 defer cleanup() 让调用方零负担"

requirements-completed: [PY-02, PY-03]

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "(*Bridge).RunDue 实现 PY-02：argv 构造 [cfg.VocabScript, --db, cfg.VocabDBPath, due, ...6 DueOpts flags]，--db 在 due 子命令前（vocab_manager.py:718 全局参数顺序，RESEARCH 第 541-556 行强制）；b.run(ctx, cfg.DueTimeout, cfg.Interpreter, args...) 经 03-03 exec 引擎 spawn；exit 0 时 len(stdout)==0 → ErrPythonEmpty 前置检查，否则 extractTrailingJSON 提末尾 JSON 数组 + json.Unmarshal 进 DueResult.Cards；context.DeadlineExceeded → ErrPythonTimeout，exit!=0 → ExitError wrapping ErrPythonExit"
    requirement: "PY-02"
    verification:
      - kind: unit
        ref: "pkg/pybridge/vocab_test.go#TestDueArgs（8 子用例覆盖 minimal/limit/peek/units/category/exclude/fetch-lock-hours/all-combined，每子用例断言 argv[1]=='--db' && argv[3]=='due'）+ TestRunDueParseFromFixtures（读 testdata/due_cards.json 经 extractTrailingJSON+Unmarshal 进 []DueCard，spot-check Word/ID/Unit/Stability 字段对齐）"
        status: pass
      - kind: unit
        ref: "go build ./pkg/pybridge/ 退出 0；go vet ./pkg/pybridge/ 退出 0；GOOS=windows 与 GOOS=linux 交叉编译均退出 0"
        status: pass
    human_judgment: false
  - id: D2
    description: "(*Bridge).RunReview 实现 PY-03：argv 构造 [cfg.VocabScript, --db, cfg.VocabDBPath, review, (--results <json> | --file <path>), --category <c>?]，rating 透传 4 种（again/good/hard/easy）原样不改写（RESEARCH Pitfall 4）；大负载（jsonBytes > 4096 OR len(Results) > 100）自动走 --file 经 os.CreateTemp('hta-review-*.json') + cleanup os.Remove；b.run(ctx, cfg.ReviewTimeout, ...) spawn；exit 0 时前置 len(stdout)==0 → ErrPythonEmpty，否则 extractTrailingJSON 处理 Warning 前缀混合 stdout（Pitfall 2 验收）+ Unmarshal 进 ReviewResult.Updated"
    requirement: "PY-03"
    verification:
      - kind: unit
        ref: "pkg/pybridge/vocab_test.go#TestReviewArgs（6 子用例：small→--results、all_four_ratings→4 值原样透传、large by entry count→--file、large by byte size→--file、nil Results→'{}' 兜底、category 透传）+ TestRunReviewParseFromFixtures（Pitfall 2 强制验收点：先断言整段 review_with_warnings.txt 非合法 JSON，再 extractTrailingJSON 提末尾 JSON Unmarshal 进 ReviewResult，断言 Updated[0].Word=='goodbye'/Rating=='again'）+ TestRunReviewParseCleanJSON（防过拟合，覆盖干净 review_updated.json 路径）"
        status: pass
    human_judgment: false
  - id: D3
    description: "reviewArgs 错误分类 + cleanup 闭包不变式：argv 构造阶段任何错误（json.Marshal 失败、os.CreateTemp 失败、tmp.Write 失败、tmp.Close 失败）返回前必清理已分配的临时文件；返回的 cleanup 函数幂等可多次调用不 panic"
    requirement: "PY-03"
    verification:
      - kind: unit
        ref: "pkg/pybridge/vocab_test.go#TestReviewArgsCleanupIdempotent（连续调 cleanup 两次不 panic）+ TestReviewArgs/large_results_by_entry_count_goes_to_file（断言 cleanup() 后 os.Stat 报 IsNotExist）"
        status: pass
    human_judgment: false

# Metrics
duration: ~12min active execution
completed: 2026-07-25
status: complete
---

# Phase 3 Plan 04: RunDue + RunReview + argv 构造器 + Pitfall 2 解析管线 Summary

**pkg/pybridge.Bridge.RunDue（due 子命令 PY-02）+ RunReview（review 子命令 PY-03）经 argv 构造器（dueArgs/reviewArgs）正确放置 --db 全局参数在子命令前 + 透传 6 个 DueOpts 标志 / 4 种 rating（again/good/hard/easy 不改写 RESEARCH Pitfall 4）+ 大负载自动 --file（os.CreateTemp + cleanup）+ extractTrailingJSON 解析末尾 JSON（含 Warning 前缀混合 stdout Pitfall 2 验收）+ 4 类 sentinel 错误分类（ErrPythonTimeout/Exit/JSONParse/Empty）**

## Performance

- **Duration:** ~12 min 活跃执行（Task 1 + Task 2）
- **Started:** 2026-07-25T01:27:58Z（Task 1 commit `4247cc2`）
- **Completed:** 2026-07-25T01:42:00Z（Task 2 commit `cfc592d` + 跨平台 build 验证）
- **Tasks:** 2（Task 1 vocab.go 实现 + bridge.go stub 清理；Task 2 vocab_test.go 18 子用例表驱动单测）
- **Files modified:** 3（1 新建 vocab.go + 1 新建 vocab_test.go + 1 改 bridge.go 删 RunDue/RunReview stub）

## Accomplishments

- **dueArgs（PY-02 argv 构造器）**：纯函数 `(cfg infra.PythonConfig, opts DueOpts) []string` 返回 `[cfg.VocabScript, "--db", cfg.VocabDBPath, "due", ...]`——`--db` 必须在 `due` 子命令前是 vocab_manager.py:718 argparse 全局参数契约（RESEARCH 第 541-556 行强制验收，测试用 assertDBBeforeSubcommand 守卫 `argv[1]=="--db" && argv[3]=="due"`）；6 个 DueOpts 字段全部条件透传（Category 非空→--category、Limit>0→--limit、Units 非空→--units 逗号拼接、Exclude 非空→--exclude 逗号拼接、Peek=true→--peek 布尔标志、FetchLockHours>0→--fetch-lock-hours strconv.FormatFloat）；零值字段不产标志（minimal 子用例守卫）。
- **RunDue（PY-02 主路径）**：`func (b *Bridge) RunDue(ctx, opts) (DueResult, error)` 流程——args := dueArgs(...)；stdout/stderr/exitCode/err := b.run(ctx, b.cfg.DueTimeout, b.cfg.Interpreter, args...)（03-03 exec 引擎 spawn）；defer b.logResult("due", ...) 落语义日志；错误分类 4 类——context.DeadlineExceeded/Canceled → ErrPythonTimeout（subprocess 超时被进程组 SIGKILL）、exit!=0 → ExitError{Cmd:"due", ExitCode, Stderr, Err: ErrPythonExit}（FSRS/用户错误）、len(stdout)==0 → ErrPythonEmpty（exit 0 但空 stdout 是契约违反）、extractTrailingJSON 失败或 json.Unmarshal 失败 → ErrPythonJSONParse；成功路径 json.Unmarshal(raw, &result.Cards) 进 DueResult.Cards。
- **reviewArgs（PY-03 argv 构造器 + cleanup）**：`func reviewArgs(cfg, opts) ([]string, func(), error)` 返回 argv + cleanup 闭包 + error；argv 前缀同 dueArgs 但子命令为 "review"；json.Marshal(opts.Results) 后按尺寸自动选 --results（jsonBytes <= 4096 且 len(Results) <= 100）或 --file（os.CreateTemp("", "hta-review-*.json") 写临时文件）；临时文件失败路径（CreateTemp/Write/Close）均显式 os.Remove 已分配文件防 leak；opts.Category 非空时追加 --category；rating 透传 4 种（again/good/hard/easy）原样不改写（RESEARCH Pitfall 4——脚本 rating_map vocab_manager.py:359-364 明确含 easy，help 文本第 746 行过时）；nil Results map 兜底为空 map 让 Marshal 结果为 '{}'（防 'null' 字面量被脚本拒）。
- **RunReview（PY-03 主路径）**：流程同 RunDue 但用 cfg.ReviewTimeout + argv 含 --results/--file；defer cleanup() 在 b.run 之前注册保证临时文件总被删除；exit 0 + Warning 前缀 + 末尾 JSON 是 SUCCESS 不是 error（critical_context 强制语义——RESEARCH Pitfall 2 验收：review 输出 `Warning: unknown rating ...` 行后仍跟 `{"updated":[...]}` JSON，整体 exit 0）。
- **ErrPythonEmpty 前置检查（Rule 2 correctness）**：在 extractTrailingJSON 之前显式检 `len(stdout)==0` 返回 ErrPythonEmpty；extractTrailingJSON 对空 stdout 默认返回 wrapped ErrPythonJSONParse（语义混淆），前置检查让 4 类 sentinel（Timeout/Exit/JSONParse/Empty）真正互斥可达，对齐 executor.go 的 ErrPythonEmpty 契约。
- **ErrBridgeNotImplemented 保留 + 文档同步**：03-05 才替换 RunPregenerate，本 plan 不删 sentinel 避免 dangling reference；ErrBridgeNotImplemented 注释从 "until 03-04 / 03-05" 更新为 "Plan 03-04 已落地，仅 RunPregenerate 待 03-05 替换"（Rule 1 注释跟随代码现实）。
- **18 子用例 + 6 主用例全绿**：TestDueArgs 8 子用例（minimal/limit/peek/units/category/exclude/fetch-lock-hours/all-combined，每子用例守卫 --db-before-subcommand）、TestReviewArgs 6 子用例（small→--results、all_four_ratings→4 值透传、large by entry count→--file + cleanup、large by byte size→--file、nil Results→'{}'、category 透传）、TestRunDueParseFromFixtures（due_cards.json round-trip）、TestRunReviewParseFromFixtures（Pitfall 2 强制验收：先验证整段 stdout 非合法 JSON 守卫 Warning 前缀存在，再 extractTrailingJSON+Unmarshal 进 ReviewResult，断言 Updated[0]）、TestRunReviewParseCleanJSON（防过拟合覆盖干净 review_updated.json）、TestReviewArgsCleanupIdempotent（cleanup 闭包幂等性）。

## Task Commits

每个 task 原子提交：

1. **Task 1: vocab.go RunDue + RunReview + dueArgs + reviewArgs + bridge.go stub 清理** — `4247cc2` (feat)
2. **Task 2: vocab_test.go 表驱动 18 子用例 + Pitfall 2 验收** — `cfc592d` (test)

**Plan metadata:** 本 SUMMARY 闭环时提交（docs: complete RunDue/RunReview vocab path plan）。

## Files Created/Modified

- `pkg/pybridge/vocab.go`（新建）— dueArgs + RunDue + reviewArgs + RunReview + 常量 reviewInlineByteCap/reviewInlineEntryCap；含详细文档注释解释 --db-before-subcommand 契约、Pitfall 2/4、4 类 sentinel 错误分类、--file 大负载选择策略
- `pkg/pybridge/vocab_test.go`（新建）— 18 子用例 + 6 主用例；argvFind + assertDBBeforeSubcommand helper；Pitfall 2 守卫（整段非合法 JSON 验证 Warning 存在）；TestReviewArgsCleanupIdempotent 幂等性守卫
- `pkg/pybridge/bridge.go`（修改）— 删除 RunDue/RunReview stub 与其文档注释（与 vocab.go 同名方法冲突）；保留 RunPregenerate stub；ErrBridgeNotImplemented 文档同步反映 03-04 已落地

## Decisions Made

- **argv 构造用 cfg.VocabScript 直接作 args[0]**：照 RESEARCH 第 547-555 行 Code Example 与 plan must_haves；不主动 `filepath.Join(cfg.ScriptsDir, cfg.VocabScript)`——调用方（部署/配置）对路径形式负全责。PythonConfig.VocabScript 字段注释（config.go:122-124）也只说"filename"，与 ScriptsDir 分立，bridge 尊重该分层。
- **ErrPythonEmpty 前置检查**：plan 草案描述的"extractTrailingJSON 返回空 raw"路径实际不可达（parser 对空输入返回 wrapped ErrPythonJSONParse）；前置 len(stdout)==0 检查让 ErrPythonEmpty sentinel 真正可达，匹配 executor.go 的契约——Rule 2 correctness（critical functionality for distinct error classification）。
- **reviewArgs 自动 size-based 选择不读 opts.UseFile/ResultsFile**：plan 明确描述 size-based 自动选择；ReviewOpts 既有的 UseFile/ResultsFile 字段保留不动（forward-compat，本 plan 不消费者），避免 plan 之外的 scope creep。
- **nil Results map 兜底为空 map**：json.Marshal(nil) 产 "null" 字面量 vocab_manager.py 会拒（cmd_review 第 350 行 `if not isinstance(results, dict): print("Error: --results must be a JSON object"); sys.exit(1)`）；显式替换 map[string]string{} 让 Marshal 结果为 "{}"（脚本接受为 "nothing to review" 返回 exit 0 + 空 updated 数组）——Rule 2 robustness。
- **defer cleanup() 在 b.run 之前注册**：即使 b.run 失败（超时/exit!=0）临时文件也必被删除；defer LIFO 保证 b.logResult 先 fire（log 时文件仍在）再 cleanup（防 leak）；这是 Go defer 参数立即求值的标准用法。
- **保留 ErrBridgeNotImplemented 与 RunPregenerate stub**：plan critical_context 明确要求 03-05 才替换 RunPregenerate；本 plan 不删 sentinel 避免 cross-plan 死代码 churn / dangling reference；ErrBridgeNotImplemented 注释同步反映 03-04 已落地（Rule 1 注释跟随代码现实，不引入误导）。
- **logResult 在 RunDue/RunReview 与 b.run 内各调一次（双日志）**：plan acceptance_criteria 强制要求 vocab.go 含 >= 2 处 b.logResult；run() 内 logResult 是 03-03 契约的一部分（command 字段值为解释器路径）不能动；RunDue/RunReview 加自己的 logResult（command 字段值为 "due"/"review"）让操作员能在日志流中按语义命令过滤；双层日志是可接受的冗余。
- **错误分类用 errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)**：exec.CommandContext 在父 ctx 或 cctx 触发 Cancel 后返回的错误链 unwrap 到 context.DeadlineExceeded（Go 1.20+ exec 文档，03-03 已验证）；显式加 context.Canceled 守卫上游调用方主动取消的场景（rule 1 robustness）。

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] ErrPythonEmpty 哨兵在 RunDue/RunReview 中不可达（plan 草案描述的解析器返回路径不匹配实现）**
- **Found during:** Task 1 实现 RunDue 错误分类分支时核读 extractTrailingJSON 源码
- **Issue:** plan 描述错误分类为 "若 parseErr 非 nil → ErrPythonJSONParse；若 len(raw) == 0 → ErrPythonEmpty"。但 pkg/pybridge/parse.go 的 extractTrailingJSON 实现是：对空 stdout 返回 `(nil, errors.Wrap(ErrPythonJSONParse, "empty stdout"))`——即空 stdout 实际归类为 ErrPythonJSONParse 而非 ErrPythonEmpty。plan 的 `len(raw)==0` 分支永远不可达（因为 extractTrailingJSON 不返回空 raw + nil err 的组合），ErrPythonEmpty sentinel 形同虚设，违反 executor.go 的契约（"exit 0 + 空 stdout → ErrPythonEmpty"）。
- **Fix:** 在 RunDue/RunReview 中显式前置 `if len(stdout) == 0: return ErrPythonEmpty` 检查，在 extractTrailingJSON 之前执行；让 4 类 sentinel（Timeout/Exit/JSONParse/Empty）真正互斥可达。
- **Files modified:** pkg/pybridge/vocab.go（Task 1 commit `4247cc2` 内）
- **Verification:** grep 'ErrPythonEmpty' vocab.go 命中 >= 2（RunDue 与 RunReview 各一）；executor.go 的 ErrPythonEmpty 注释"exit 0 但 stdout 空"现在真正是 bridge 层返回路径。
- **Committed in:** `4247cc2`（Task 1 commit）

**2. [Rule 2 - Critical Functionality] reviewArgs 对 nil Results map 序列化为 'null' 导致脚本拒绝**
- **Found during:** Task 1 实现 reviewArgs 时核读 vocab_manager.py cmd_review 第 350 行契约
- **Issue:** Go 的 `json.Marshal(nil)` 产出 `null` 字面量；vocab_manager.py 第 350 行守卫 `if not isinstance(results, dict): print("Error: --results must be a JSON object"); sys.exit(1)`——传入 null 会被拒绝为 exit 1。若调用方零值初始化 ReviewOpts{}（Results 字段为 nil map），reviewArgs 会构造致命 argv。
- **Fix:** reviewArgs 内显式 `if results == nil { results = map[string]string{} }` 兜底，让 Marshal 结果为 "{}"；脚本接受为 "nothing to review" 返回 exit 0 + 空 updated 数组。
- **Files modified:** pkg/pybridge/vocab.go（Task 1 commit `4247cc2` 内）
- **Verification:** TestReviewArgs/nil_results_map_does_not_panic 子用例 PASS（断言 --results 值为 "{}" 而非 "null"）。
- **Committed in:** `4247cc2`（Task 1 commit）

**3. [Rule 1 - Bug] ErrBridgeNotImplemented 文档注释与代码现实不同步（03-04 后已只剩 RunPregenerate stub）**
- **Found during:** Task 1 删除 bridge.go RunDue/RunReview stub 后核读 ErrBridgeNotImplemented 注释
- **Issue:** bridge.go 的 ErrBridgeNotImplemented 注释原写 "until Plan 03-04 (RunDue/RunReview) and Plan 03-05 (RunPregenerate) replace them" 与 "Once 03-04 / 03-05 land the stubs (and this sentinel) are deleted"；本 plan 03-04 已替换 RunDue/RunReview 但保留 RunPregenerate stub + sentinel（critical_context 明确要求），原注释误导（让人以为 sentinel 应该在 03-04 一并删除）。
- **Fix:** ErrBridgeNotImplemented 注释更新为 "Plan 03-04 replaced RunDue/RunReview with real implementations in vocab.go; only RunPregenerate remains as a stub until Plan 03-05 lands"；错误消息字符串从 "not yet implemented (Plan 03-04 / 03-05)" 改为 "not yet implemented (Plan 03-05)"。
- **Files modified:** pkg/pybridge/bridge.go（Task 1 commit `4247cc2` 内）
- **Verification:** grep 'ErrBridgeNotImplemented = errors.New' bridge.go 命中 1，消息含 "Plan 03-05" 不含 "03-04"。
- **Committed in:** `4247cc2`（Task 1 commit）

---

**Total deviations:** 3 auto-fixed（1 个 Rule 1 sentinel 可达性 bug、1 个 Rule 2 nil-map 兜底 correctness、1 个 Rule 1 文档同步）。均 correctness 必需，无 scope creep，无新文件（plan 列出的 vocab.go + vocab_test.go），无架构变更。

## Issues Encountered

- **GOOS=linux go build 在 Windows 上首次较慢**：03-03 SUMMARY 已记录的限制——首次 GOOS=linux 交叉编译触发 stdlib 工具链下载耗时较长（~30s）；本 plan 验证 GOOS=windows + GOOS=linux 双平台 build 时同样出现。非本 plan 引入的限制，已让命令后台完成。
- **CGO_ENABLED=0 导致 `go test -race` 不可用**：本机 Windows 环境 cgo 未启用（与 03-02/03-03 SUMMARY 同一限制），退回 `go test` 无 race；race 检测留 Linux CI。

## User Setup Required

None - 无外部服务配置。本 plan 是纯 Go 方法 + 表驱动单测，无 Python/venv/密钥依赖（解析器 pipeline 测试用 testdata/ 黄金 fixture + 纯函数 argv 构造，无 subprocess spawn）。密钥经 childEnv 注入是 03-03 已落地的运行时行为。

## Next Phase Readiness

- **就绪**：`(*Bridge).RunDue` 与 `(*Bridge).RunReview` 可供 Phase 5（FSRS 编排）与 Phase 7（会话流）直接调用——`pyBridge.RunDue(ctx, DueOpts{Limit: 30, Peek: true})` 与 `pyBridge.RunReview(ctx, ReviewOpts{Results: map[string]string{"hello":"good"}})` 即可，返回 typed DueResult/ReviewResult + 4 类 sentinel 错误分类。
- **就绪**：argv 构造正确性经表驱动 14 子用例 + --db-before-subcommand 全局不变式守卫；rating 透传 4 种经 Pitfall 4 验收点；Pitfall 2 混合 stdout 解析经 review_with_warnings.txt 强制验收。
- **就绪**：03-05（RunPregenerate）可照 dueArgs/reviewArgs 的 shape 实现 pregenerateArgs（无 --db 全局参数，pregenerate_dictation.py 用 --words/--language/--prefix/--voice）；照 RunDue/RunReview 的 shape 实现 RunPregenerate（用 splitNonEmptyLines 而非 extractTrailingJSON，Pitfall 3 验收；路径遍历校验 T-03-06；pregenerate flock 单实例串行 Pitfall 6）。
- **就绪**：ErrBridgeNotImplemented sentinel + RunPregenerate stub 仍在 bridge.go，03-05 替换 RunPregenerate 时同 commit 删除 sentinel + stub（critical_context 提醒）。
- **阻塞**：无。Phase 03 Wave 3 的 Plan 03-05（RunPregenerate）可立即开工。
- **提醒下游**：
  - Phase 5（FSRS 编排）若需校验 DueResult.Cards 的 state/difficulty/stability 字段语义，应在 Phase 5 层做——bridge 不校验（D-09：FSRS 逻辑留 Python）。
  - Phase 10（家长反馈）的 rating 映射决策（若 UI 暴露的 rating 与 vocab_manager 的 again/good/hard/easy 不同）属 Phase 10 范围，Phase 3 仅透传。
  - 03-05 实现 RunPregenerate 时，注意 pregenerate_dictation.py 的 stdout 是按行 mp3 路径（非 JSON），绝不能 json.Unmarshal；splitNonEmptyLines 后必须校验路径在 cfg.CacheDir 范围内防 tampering（T-03-06）；fcntl flock 全局串行（Pitfall 6）可能需要 bridge 层 cap=1 信号量或 exit0+空输出重试。

## Self-Check: PASSED

- 创建文件存在性（2/2 FOUND）：
  - `pkg/pybridge/vocab.go` — FOUND
  - `pkg/pybridge/vocab_test.go` — FOUND
- 修改文件存在性（1/1 FOUND）：
  - `pkg/pybridge/bridge.go` 仍含 RunPregenerate stub + ErrBridgeNotImplemented，无 RunDue/RunReview stub — FOUND
- 提交存在性：
  - `4247cc2` — FOUND（Task 1）
  - `cfc592d` — FOUND（Task 2）
- 验收 grep（plan Task 1 + Task 2 acceptance_criteria 全部满足）：
  - `func dueArgs` in vocab.go == 1 — PASS
  - `func reviewArgs` in vocab.go == 1 — PASS
  - `func.*RunDue|func.*RunReview` in vocab.go == 2 — PASS
  - `VocabScript, "--db", cfg.VocabDBPath, "due"` 命中 1 — PASS
  - `VocabScript, "--db", cfg.VocabDBPath, "review"` 命中 1 — PASS
  - `extractTrailingJSON` in vocab.go >= 2 — PASS（9 命中，含调用与文档注释）
  - `b.logResult` in vocab.go >= 2 — PASS（2 命中，RunDue + RunReview 各一）
  - `ErrPythonTimeout|ErrPythonExit|ErrPythonJSONParse|ErrPythonEmpty` in vocab.go >= 4 — PASS（17 命中）
  - `CreateTemp|--file` in vocab.go >= 1 — PASS（8 命中）
  - `bridge.go RunDue/RunReview stub` == 0 — PASS（已删）
  - `bridge.go RunPregenerate stub` == 1 — PASS（保留给 03-05）
  - `ErrBridgeNotImplemented = errors.New` in bridge.go == 1 — PASS（保留）
  - TestDueArgs / TestReviewArgs / TestRunDueParseFromFixtures / TestRunReviewParseFromFixtures 均在 vocab_test.go 命中 — PASS
  - "all four ratings forwarded verbatim" Pitfall 4 验收子用例命中 — PASS
  - "large results by entry count" + "large results by byte size" --file 验收子用例命中 — PASS
- 验证命令：
  - `go test -count=1 -v -run 'TestDueArgs|TestReviewArgs|TestRunDueParseFromFixtures|TestRunReviewParseFromFixtures|TestRunReviewParseCleanJSON|TestReviewArgsCleanupIdempotent' ./pkg/pybridge/` 全绿（18 子用例 + 6 主用例 PASS）— PASS
  - `go test -count=1 -short ./pkg/pybridge/` 全绿（无回归，含 03-01/02/03 全部用例）— PASS
  - `go build ./pkg/pybridge/` 退出 0 — PASS
  - `go build ./...` 退出 0（全仓无回归）— PASS
  - `go vet ./pkg/pybridge/` 退出 0 — PASS
  - `GOOS=windows go build ./pkg/pybridge/` 退出 0 — PASS
  - `GOOS=linux go build ./pkg/pybridge/` 退出 0 — PASS

---
*Phase: 03-python-cli-integration*
*Completed: 2026-07-25*
