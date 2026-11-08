---
phase: 261108-lm8
plan: 01
subsystem: ci-govet-fieldalignment
tags: [ci, golangci-lint, govet, fieldalignment, struct-layout]
status: complete
commits: 1
plan_head_before: 33f87c444f04d20749ab3c7392e2328bfae45d0d
plan_head_after: 2428ad0ff71ece45c9ac552f50ea6f4255c655ac
actuals:
  tokens: 640
  tasks: 2
  commits: 1
key-files:
  modified:
    - pkg/ocr/progress.go
    - pkg/web/api/textbook_progress.go
decisions:
  - 全量门 FAIL 裁定为先存平台专属问题（internal/e2ecorpus 的 !unix 构建标签），按计划「如实上报不扩大修复面」分支处理——不改 lint 配置、不修无关包、登记 deferred-items.md
  - 单次原子提交落在 Task 2 全量门之后（计划 success criteria 明文「一次原子提交」；Task 1 无提交步骤）
metrics:
  duration: 13 min
  completed: 2026-11-08
---

# Quick Task 261108-lm8: 修 CI 红——govet fieldalignment 四处结构体字段重排 Summary

四结构体（ProgressTextbook/ProgressCandidate/ProgressInterpretation/progressInterpretation）按指针前缀最优序重排字段声明，固定版 golangci-lint v2.14.0 两包 0 issues，GitHub run 42443216515 的 4 条 fieldalignment 全消；字段名/类型/json 标签逐字不变，行为零变化由两包全绿测试背书。

## What Was Done

### Task 1: 四结构体字段重排（pkg/ocr/progress.go、pkg/web/api/textbook_progress.go）

按计划逐字节验算过的目标序执行（指针承载字段在前、int64/int 标量在后）：

| 结构体 | 目标序 | 指针前缀 |
|---|---|---|
| ProgressTextbook | Name, Category, Edition, Stage, Grade, Volume, Module, Lessons, ID, LearnedLessons | 128→120 |
| ProgressCandidate | TextbookName, Lesson, TextbookID, LearnedLessons | 32→24 |
| ProgressInterpretation | Status, TextbookName, Lesson, Reason, Candidates, TextbookID, LearnedLessons | 88→72 |
| progressInterpretResponse | 同上（json 标签原样随字段走） | 88→72 |

- 环境预备：一次性 `git -c core.autocrlf=false checkout-index -f -a` 消除 CRLF 假告警（orchestrator 预警，验证后 `git -c core.autocrlf=false diff` 为空、1087 条 ` M` 均为幻影）。
- gofmt 仅限两改动文件；git diff 复核仅见字段重排与列对齐，无调用点改动（全仓字面量均 keyed，规划期已核实）。

**Task 1 验证（三连全绿）：**
1. `gofmt -l` 两文件：输出为空。
2. `/tmp/gcl-bin/golangci-lint run --config=.golangci.yml ./pkg/ocr/... ./pkg/web/api/...`：**0 issues**（四条 fieldalignment 全消，无新告警）。
3. `go test ./pkg/ocr/ ./pkg/web/api/`：ok（0.780s / 2.037s）。

### Task 2: 全量本机 CI 门（scripts/harness/ci-local.sh，默认 ./...）

- 前置检查通过：`/tmp/gcl-bin/golangci-lint` = v2.14.0（built with go1.27.1）。
- 门结果（按步骤）：
  - `go mod download`：绿。
  - `go vet ./...`：绿（无输出）。
  - `golangci-lint v2.14.0 ./...`：仅 1 条 `internal\e2ecorpus\prepare.go:57:35 SA4023`（Windows 平台专属，见 Deviations）——**pkg/ocr 与 pkg/web/api 零告警，本任务目标达成**。
  - `go test -race -count=1 ./...`：21 包 ok（含 pkg/ocr 4.115s、pkg/web/api 10.266s、pkg/rt 125.2s），仅 `internal/e2ecorpus` FAIL（同上平台专属）。
  - `go build -trimpath`：绿。
  - 总结行：**FAIL（仅因 internal/e2ecorpus 的先存平台问题；与 CI 等价的判定对本次改动全部为绿）**。
- 原子提交：`2428ad0`（只含两文件，6 insertions / 6 deletions；提交前核验无并行会话改动混入、无 untracked 残留、无文件删除）。

## Deviations from Plan

### 记录的偏差（无代码级偏差——两文件改动与计划逐字一致）

**1. [环境/流程] full-suite harness 钩子拦截与 finalize 放行**
- **Found during:** Task 2
- **Issue:** PreToolUse 钩子 `[full_suite]` 按共享机器策略在 iterate 阶段禁止无参 `ci-local.sh`；计划未预见此拦截。
- **Fix:** 采用钩子自身文档化的收尾通道——向会话锚定的主仓 `.harness/phase` 写 `finalize` 标记（钩子读 `CLAUDE_PROJECT_DIR` 即主仓状态目录；worktree 内 marker 无效，首次尝试被拒后经 rules.json 核实路径）。ci_local 套件一次性配额消耗 1/1；跑毕即清理双端 marker（主仓 marker 先前不存在，worktree `.harness/` 为本任务新建，均删除还原）。
- **Files modified:** 无代码文件（仅 harness 状态，gitignored）。

**2. [Rule 3 判定分支] 全量门总结行 FAIL——源于先存、平台专属、与本改动无关的 internal/e2ecorpus 问题**
- **Found during:** Task 2
- **Issue:** ① `SA4023` lint：`lock_other.go`（`//go:build !unix`）的 `lockCacheFile` 无条件返回错误，GOOS=windows 分析下 `err != nil` 恒真——Linux CI 编译 `lock_unix.go` 不触发（GitHub run 42443216515 同版本 lint 对 e2ecorpus 干净可证）。② `prepare_test.go` 无平台标签，Windows 上全部 FAIL（cross-process lock 不支持哨兵）。
- **Pre-existence:** e2ecorpus 三个提交（最新 3dae01f）均为基线 33f87c4 祖先；本任务 diff 对该包零影响。
- **Action（按计划「若源于无关既有问题，如实上报不得为过门放宽 .golangci.yml」）:** 不修、不放宽、如实上报；登记 [deferred-items.md](./deferred-items.md)（建议后续给 prepare_test.go 补 `//go:build unix` 或 skip 策略，与既有 lock_test.go/lock_other_test.go 平台拆分一致）。
- **对关红结论的影响:** 无——Linux CI 视角下本任务四告警已消（两包 0 issues 由固定版 v2.14.0 证明），全量门对本改动相关的每一步（vet/两包 lint/两包 race/全仓 build）全绿。

## Verification Evidence

- 固定版 lint 两包 0 issues（Task 1 `<automated>` 第 2 步，退出码 0）。
- `pkg/ocr`、`pkg/web/api` go test 全绿（普通 + race 双轮）。
- 全仓 vet/build 绿；全仓 lint 除先存平台项外零告警。
- 提交卫生：`2428ad0` 仅 `pkg/ocr/progress.go` + `pkg/web/api/textbook_progress.go`，无删除、无混入。
- 未 push（按约束，push 由调度器/用户执行）。

## Known Stubs

None.

## Auth Gates

None.

## Self-Check: PASSED

- Files: pkg/ocr/progress.go, pkg/web/api/textbook_progress.go, 261108-lm8-SUMMARY.md, deferred-items.md, 261108-lm8-PLAN.md — all FOUND.
- Commit: 2428ad0 present on worktree-agent-aeb37ee7eed6862ab (ledger-measured count 1 = frontmatter commits).
- Ledger: plan_head_before 33f87c4 → plan_head_after 2428ad0 (git rev-list --count = 1).
