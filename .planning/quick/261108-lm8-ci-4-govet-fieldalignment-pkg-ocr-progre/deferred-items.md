# Deferred Items — quick task 261108-lm8

Out-of-scope discoveries made during execution. NOT fixed here (scope boundary:
pre-existing, unrelated to this task's two-file field reorder; the plan explicitly
forbids widening the fix surface or loosening lint to pass the gate).

## 1. internal/e2ecorpus 在 Windows 平台全量门必然红（lint + tests 双失败）

- **Found during:** Task 2 (full-repo `scripts/harness/ci-local.sh` gate), 2026-11-08.
- **Evidence:**
  - Lint: `internal\e2ecorpus\prepare.go:57:35: SA4023: this comparison is always true (staticcheck)`
    — `lock_other.go` (`//go:build !unix`) 的 `lockCacheFile` 无条件返回错误，
    在 GOOS=windows 分析下 `err != nil` 恒真。Linux 下编译 `lock_unix.go`（可返回 nil），不触发。
  - Tests: `prepare_test.go` 无构建标签，Windows 上全部走 `lock_other.go` 的
    「cross-process .prepare.lock locking is unsupported on this platform」哨兵而 FAIL
    （TestPrepareOnlyMissing / TestPrepareSharedTextOneCall / TestPrepareBudgetCaps×2 /
    TestPrepareStopsAtFirstFailure / TestPrepareFailsOnDamagedCache×2 /
    TestPrepareConcurrentRunsSynthesizeOnce）。
- **Pre-existence:** e2ecorpus 全部三个提交（最新 3dae01f「区分 Unix 与其他平台的文件锁测试」）
  均为本任务基线 33f87c4 的祖先；本任务 diff 仅 pkg/ocr/progress.go 与
  pkg/web/api/textbook_progress.go 的字段声明序，对该包零影响。
- **Why not fixed:** 计划明文「若源于无关既有问题，如实上报不得为过门放宽 .golangci.yml」；
  修法（prepare_test.go 加 `//go:build unix` 或跳过策略）超出本任务授权。
- **Suggested follow-up:** 给 `internal/e2ecorpus/prepare_test.go` 补平台门（`//go:build unix`
  或 !unix 下 t.Skip），与既有 `lock_test.go`/`lock_other_test.go` 的平台拆分保持一致；
  SA4023 随测试平台门一并消失或按 .golangci.yml 的排除规则处理（需另行授权）。
- **Impact on this task:** none for the Linux CI verdict — GitHub run 42443216515
  同版本 lint 只报 4 条 fieldalignment（e2ecorpus 干净），本任务已消的那 4 条在
  本地固定版 v2.14.0 下 0 issues。
