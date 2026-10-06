---
phase: 261007-0ct
plan: 01
type: quick
subsystem: pkg/lesson（课堂推进幂等执行层）
tags: [u-2, idempotency, stale-record, version-guard, tdd, 13-12, sqlite]
requires:
  - U-2 缺陷裁定（13-UAT-ISSUES-20261006.md U-2 条目：期望行为三行锁定，不得重议）
  - L-3 事务时长前提（executor.go L-3 注释；database.Open SetMaxOpenConns(1) 单连接池串行写事务）
provides:
  - idempotentHit U-2 版本倒退守卫——stored NewVersion > 课行当前 version 即判定过期，绝不回放（Error 级五字段响亮日志）
  - AdvanceWord 单次 purge 重试——DELETE 过期记录＋重占＋推进＋存结果同事务原子提交，重执行走首提交路径（word_announced 用户可感知）
  - staleRecordError（非导出，errors.As 可检）＋去重命中而课行已不存在改判 ErrLessonNotFound
  - captureLogger 捕获日志器测试基建（executor_test.go，可断言 Error 级条目，middleware_test fakeLogger 同款范式）
affects:
  - pkg/rt/turn.go 幂等分支（守卫保证其永远收不到过期结果——三分支互斥注释仍为真，本任务零改动）
  - 13-13 VPS 验收（同一天删课行重开路径不再逐词吞推进直至假完结；E2E 隔离环境复测项）
  - Phase 13.1（三段式课堂建立在推进不再假完结之上）
tech-stack:
  added: []
  patterns: [TDD RED→GREEN（行为级断言失败先证伪现行代码）, 事务体提取＋开关参数单次重试, 同事务清除重执行原子自愈]
key-files:
  modified:
    - pkg/lesson/executor.go      # staleRecordError 类型；AdvanceWord 收拢为参数校验＋advanceTx 单次重试；advanceTx 提取（事务体原样）＋purgeStale 按主键 DELETE；idempotentHit 版本倒退守卫＋缺课行 ErrLessonNotFound；AdvanceWord/idempotentHit/L-3 三处 doc 注释同步
    - pkg/lesson/executor_test.go # 三新测试（重置复现/合法重放边界/缺课行）＋captureLogger；newExecutorForTest 委托 newExecutorWithCaptureForTest；既有 4 测试零改动
decisions:
  - purge 仅在过期判定成立后、同事务内按主键 DELETE 单条——0-rows UPDATE 冲突路径经 defer Rollback 整事务回退（含已做 DELETE），并发下绝不误删合法记录（T-261007-02 mitigate）
  - 重试单次即止：单连接池串行写事务下检测与重试之间无法再产生过期记录，第二次 stale 实际不可达；仍发生则原样上抛走 turn.go 通用错误分支（用户可感知「课堂进度更新失败，请再试一次」），防无限循环
  - Task 2 的 -race 门在本机不可达，以计划自记的非 race 全量基线口径替代（与 U-9 前例 261006-u2o 同口径，详见 Deviations）
metrics:
  duration: 约 13 分钟（16:28:17Z → 16:41:32Z）
  completed: 2026-10-07
  commits: 1
  plan_head_before: ab94b4120255aafa2be44650b69fd3f2093991bb
  plan_head_after: 7fdfac77adee5523c29d7b4c62e798757a3a6e0c
actuals:
  tokens: 4823        # chars/4 over realized diff（19292 chars；116+/3− executor.go、224+/2− executor_test.go）
  tasks: 2
  commits: 1
status: complete
---

# Quick Task 261007-0ct: U-2 去重版本倒退防护 Summary

**One-liner:** idempotentHit 回放前核对课行当前 version——stored NewVersion > current 判定为重置课行的过期去重记录（Error 级五字段响亮日志，绝不静默），同事务按主键清除后重执行推进（走首提交路径，下一词正常播报）；去重命中而课行已不存在改判 ErrLessonNotFound；合法重放（stored ≤ current）语义零变化，pkg/rt 零改动。

## 任务完成情况

### Task 1（TDD RED，无提交——按计划「全程不改生产代码、不提交」）

三新测试＋captureLogger 捕获日志器落盘于 executor_test.go（newExecutorForTest 委托
newExecutorWithCaptureForTest，既有 4 测试代码零改动）。行为级 RED 实跑输出（逐字采捕）：

```
--- FAIL: TestAdvanceWordStaleRecordAfterLessonReset (0.01s)
    executor_test.go:165: advance after reset = {NewIndex:1 NewVersion:2 Idempotent:true}, want real execution (Idempotent:false); stale dedupe record was replayed
    executor_test.go:179: lessons after re-advance = version 1 index 0, want version 2 index 1
    executor_test.go:198: lessons.version after third call = 1, want 2 (replay must not advance)
    executor_test.go:204: no Error-level log containing "stale" was captured; stale dedupe record was handled silently
--- FAIL: TestAdvanceWordStaleRecordLessonMissing (0.01s)
    executor_test.go:280: AdvanceWord dedupe hit with missing lesson error = <nil>, want ErrLessonNotFound
```

失败方向与计划预判完全一致：删课行重建（version 归 1）后旧记录（stored NewVersion=2）被照常回放
（Idempotent:true 吞推进、version 停在 1、无任何日志）、缺课行时 nil error＋回放。边界测试
TestAdvanceWordIdempotentReplayWhenLessonAdvanced 在 RED 期即为绿（计划明示属预期；RED 判据是整跑
非绿）。非「意外绿」，未触发 inconclusive 分支。计划 RED 门输出 `RED_CONFIRMED`（EXIT:1）。

### Task 2（TDD GREEN＋单一提交 7fdfac7）

- **Commit:** `7fdfac77adee5523c29d7b4c62e798757a3a6e0c`
- **Subject:** `fix(13-12): U-2 去重版本倒退防护——重置课行的过期去重记录判定失效并重执行推进`
- **Files（diff-tree 比对恰此 2 项）:** pkg/lesson/executor.go（116+/3−）、pkg/lesson/executor_test.go（224+/2−）
- **Trailer:** `Co-Authored-By: Claude Code <noreply@anthropic.com>` 在末行
- 无意外文件删除（--diff-filter=D 零输出）；`.gsd/` 与其他未跟踪文件零裹挟（git status 仅余 `?? .gsd/`，与本 worktree 起始态一致）

## 门禁实跑结果

| 门 | 命令 | 结果 |
|---|---|---|
| vet | `go vet ./pkg/lesson/...` | **零报告**（VET_OK） |
| 全量测试（替代口径） | `go test -count=1 ./pkg/lesson/` | **ok 0.902s**（7 测试全绿：3 新＋既有 4 零改动） |
| race | `go test -race -count=1 ./pkg/lesson/` | **本机不可达**（见 Deviations） |
| 全仓编译 | `go build ./...` | BUILD_OK |
| 全仓测试（范围保险） | `go test -count=1 ./...` | **16 包全 ok**（含 pkg/rt 25.2s——AdvanceWord 唯二调用方所在层零改动全绿） |
| 提交形状 | diff-tree 比对 + 主题前缀 grep + trailer grep | **ALL_GREEN** |

**-race 不可达原因（环境事实，非代码缺陷）**：Windows race 检测需 CGO_ENABLED=1 + C 工具链；本机
CGO_ENABLED=0 且 PATH 无 gcc（`cgo: C compiler "gcc" not found`，实跑 `CGO_ENABLED=1 go test -race`
复现构建失败）。与规划期基线同口径——计划自身的基线实证即为非 race 全量（0.885s ok）。采用
`go test -count=1 ./pkg/lesson/`（0.902s ok）替代并如实落档；race 全量留给具备 C 工具链的环境复跑。
gsd_run 在本 quick 任务环境不可用（U-9 前例同况），unrun-verify 未能写入 WINDOWS.md 台账——本节
记录即落档处。

## must_haves 逐条对照

**truths**

1. 「同一天删课行重建（版本归 1）后，旧 tool_requests 记录判定过期、推进真实执行」→ ✅
   TestAdvanceWordStaleRecordAfterLessonReset 先 RED（Idempotent:true 吞推进、version 停 1）后 GREEN
   （Idempotent:false、version 1→2、index 0→1）；turn.go 首提交分支照常播下一词（rt 层零改动，全仓
   pkg/rt 测试全绿佐证）。U-2 台账期望行为锁定。
2. 「合法幂等回放零变化」→ ✅ 既有 TestAdvanceWordIdempotent 零改动通过（stored == current 原样回放）；
   新增 TestAdvanceWordIdempotentReplayWhenLessonAdvanced 钉死 stored(2) < current(3) 也走回放且
   version 不动——守卫无误伤面。
3. 「去重命中而课行已不存在 → ErrLessonNotFound」→ ✅ TestAdvanceWordStaleRecordLessonMissing 先 RED
   （nil error＋回放）后 GREEN（errors.Is 命中 ErrLessonNotFound）。
4. 「过期判定时 Error 级响亮日志（component/lesson_id/request_id/stored_new_version/
   current_lesson_version 五字段），绝不静默；重执行走首提交路径，word_announced 正常发出」→ ✅
   五字段日志落码且消息含 stale 字样；captureLogger 断言 Error 级条目存在（RED 期该断言亦红）；
   word_announced 由 rt 层首提交路径自然发出，本任务未触碰 timing.go 的「idempotent replays never
   emit」注释所辖路径。
5. 「提交后 vet 零报告 + 全量测试全绿」→ ✅ vet 零报告；非 race 全量 ok（race 门环境不可达，替代
   口径如实落档，不静默降级）。

**artifacts**

1. 「恰一个 fix(13-12) 提交恰含 pkg/lesson 2 文件，主题 `fix(13-12): U-2` 开头，末行 Co-Authored-By」
   → ✅ 7fdfac7，diff-tree 断言恰 2 项，主题/trailer grep 均过。
2. 「SUMMARY 写盘不提交（docs 提交由 orchestrator 负责）」→ ✅ 本文件即写盘态，执行器未提交。

**key_links**

1. 「idempotentHit 版本倒退守卫 ↔ turn.go advanceTarget 幂等分支」→ ✅ 守卫把过期结果挡在 executor
   层（staleRecordError 触发 purge 重试，重试产出真实推进或错误），幂等分支只会收到 stored ≤
   current 的合法回放——turn.go:1038-1047 三分支互斥注释仍为真，rt 层零改动。
2. 「purge＋重执行同事务原子 ↔ L-3 单连接池前提」→ ✅ DELETE 按主键仅一条、仅在事务内 INSERT 之前，
   purge/重占/推进/存结果一体提交一体回滚；AdvanceWord doc 的 L-3 段补记「purge 增加的仍是一条按主键
   短语句，事务时长前提不变」。
3. 「Error 级五字段日志＋首提交 word_announced ↔ U-2 台账响亮告警＋用户可感知口径」→ ✅ 日志五字段
   （component/lesson_id/request_id/stored_new_version/current_lesson_version）＋消息含 stale；重执行
   返回 Idempotent:false，turn.go 首提交分支播报下一词——不静默、可听感。

## Deviations from Plan

**1. [Rule 3 - 阻塞门替代] `go test -race` 在本机不可达，以非 race 全量替代**
- **Found during:** Task 2 门禁
- **Issue:** 计划 Task 2 门含 `-race`；Windows race 检测需 CGO_ENABLED=1 + gcc，本机无 C 工具链
  （实跑复现 `cgo: C compiler "gcc" not found`）。此为规划期已存在的事实——计划自身的规划期基线
  实证即为非 race 口径（0.885s ok）。
- **Fix:** 门以 `go test -count=1 ./pkg/lesson/`（0.902s ok，全绿）＋全仓 `go test -count=1 ./...`
  （16 包全 ok）替代并如实落档；未尝试安装 C 工具链（属用户机器级变更，未获授权）。改动本身无新
  goroutine/锁（纯事务内语句顺序＋一次重试），单连接池串行写事务下无并发竞争面，风险低但非零。
- **Commit:** 7fdfac7（正文已记此偏差）；unrun-verify 已尝试登记 WINDOWS.md——gsd_run 不可用未能
  写台账，本 SUMMARY 即落档处（U-9 前例 261006-u2o 同口径同处置）。

其余：计划按文执行，无架构偏差、无范围外裹挟（turn.go / dictation.go / mimo_adapters.go / pkg/rt
零触碰；AdvanceWord 签名与 AdvanceResult 契约不动；重试单次即止；无 13.1 功能代码）。Task 1 测试
零适配即通过 Task 2 门（计划预留的「编译/断言微调」无需动用）。认证门：无。已知 Stub：无（所有
数据路径已接线，无占位返回）。Threat Flags：无（purge DELETE 严格限定主键单条＋过期判定成立后＋
同事务原子，均落在计划 threat_model T-261007-01/02/03 之内，mitigation 全部落码）。

## 用户后续手工步骤（不阻塞本任务收口）

1. **活跃课复测「删课行重开→推进正常」**：下一节活跃课中，若同一天内删除课行重开（或 E2E 隔离
   环境复现同路径），确认开场报词后逐词推进不再被「已经默写过了」吞掉——第 2 词正常播报；服务端
   日志应出现 `stale dedupe record (lesson reset/rolled back), purging and re-executing` 的 Error 级
   条目（五字段可核对）。
2. **E2E 隔离环境**（第一层缺陷修复第三项落地后）：以隔离库跑 E2E 全流程，确认不再触碰生产课行的
   过期记录路径。
3. **提示 orchestrator**：更新 `.planning/phases/13-realtime-minimal-loop/13-UAT-ISSUES-20261006.md`
   U-2 条目状态为已修（commit 7fdfac7，2026-10-07）；第一层缺陷修复进度：U-9 ✅（f948b17）、U-2 ✅
   （7fdfac7）、E2E 隔离规范待办。
4. 环境级后续：在具备 C 工具链的环境（如装 mingw 后的本机或 VPS）复跑
   `go test -race -count=1 ./pkg/lesson/` 补齐 race 门。

## Self-Check: PASSED

- [x] SUMMARY 存在：`.planning/quick/261007-0ct-u-2-pkg-lesson-executor-go/261007-0ct-SUMMARY.md`（本文件）
- [x] 提交存在：`git log` 含 7fdfac77adee5523c29d7b4c62e798757a3a6e0c（恰 1 个提交，ab94b41..HEAD，
      rev-list 计数=1，与 ledger gsd-plan-head-before-261007-0ct 一致）
- [x] 2 个计划文件均在 HEAD 中且工作树干净（仅 `?? .gsd/` 未跟踪，符合约束与起始态）
- [x] 三新测试＋既有 4 测试全绿（0.902s ok）；vet 零报告；全仓 16 包测试全 ok；提交形状 ALL_GREEN
