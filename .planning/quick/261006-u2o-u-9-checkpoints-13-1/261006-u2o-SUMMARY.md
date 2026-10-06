---
phase: 261006-u2o
plan: 01
type: quick
subsystem: pkg/rt（默写 FSM 评级推导与事件流）
tags: [u-9, rating-matrix, hint-count, tdd, 13-12, fsrs]
requires:
  - U-9 裁定（用户 2026-10-06；13-CHECKPOINTS「评级映射追认项」段；13-UAT-ISSUES-20261006.md U-9 条目）
provides:
  - wordRating(outcome, hints) 纯函数——评级矩阵单一引用点（Phase 13.1 未核对兜底路径直接引用，不得分叉）
  - DictationTutor.perWordHints 逐词提示计数（独立于 outcome 终态覆盖存活）
  - EventHintGiven 事件（word_index + hint_count 运行计数）
  - review_written 新增 hint_words / hint_total 聚合字段
affects:
  - Phase 13.1（三段式课堂：未核对兜底评级必须经 wordRating；「没听清提示/多音字」人工综合口径以 (outcome, hints) 签名为预留接缝）
  - 13-13 VPS 验收（学习记录评级落库核对以本映射为准）
tech-stack:
  added: []
  patterns: [TDD RED→GREEN（行为级断言失败先证伪现行代码）, 表驱动纯函数矩阵单测]
key-files:
  modified:
    - pkg/rt/dictation.go   # outcomeRating map 删除 → ratingGood/Hard/Again 常量 + wordRating 纯函数；perWordHints 字段 + recordHint + hintAccounting；ratings() 改查矩阵；触碰的 struct 字段块按 gofmt 对齐
    - pkg/rt/turn.go        # intentHint 分支换 recordHint（锁内取计数、锁外发射 EventHintGiven）；enterCompletion 增 hint 聚合并入 review_written
    - pkg/rt/timing.go      # 事件词汇表新增 EventHintGiven；EventReviewWritten doc 同步
    - pkg/rt/turn_test.go   # 五个新测试（两条 FSM 裁定 + 矩阵 + 计数 + 完结载荷）；既有测试零改动
decisions:
  - hint_given 仅在 recordHint 实际计数时发射（守卫失败不发 n=0 事件）——事件流绝不伪造「已提示」证据（T-261006-03 诚实口径）
  - outcomeCounts 的 hinted 口径不变：只数终态仍为 outcomeHint 的词；被 confirm/timeout 覆盖预记的词计入 confirmed/timeout，其提示历史由 perWordHints / hint_words / hint_total 承载（完结总结话术因此不变，plan 范围守卫）
  - Task 2 的 -race 门在本机不可达，以计划自记的非 race 全量基线口径替代（详见 Deviations）
metrics:
  duration: 约 15 分钟（13:54:57Z → 14:09Z）
  completed: 2026-10-06
  commits: 1
  plan_head_before: 64960f78f69dea95aee3966ee405ec4a8d35aeb2
  plan_head_after: f948b17f834e80234486ded39856877c122303d4
actuals:
  tokens: 4518        # chars/4 over realized diff（18071 chars；91+/18− dictation.go、7+/1− timing.go、19+/5− turn.go、182+/0− turn_test.go）
  tasks: 2
  commits: 1
status: complete
---

# Quick Task 261006-u2o: U-9 评级映射裁定落码 Summary

**One-liner:** hint 感知评级矩阵落码（提示后 confirm=hard、提示后 timeout/仍错=again、无提示路径不变）＋逐词提示计数（hint_given 逐词运行计数 + review_written 聚合），wordRating 纯函数成为 13.1 未核对兜底路径的单一引用点。

## 任务完成情况

### Task 1（TDD RED，无提交——按计划「全程不改生产代码、不提交」）

严格两步执行完毕：

**第一步（行为级 RED——「现行代码与裁定相反」的落档证据）**：先加两条仅引用既有符号的 FSM 测试
（TestHintThenConfirmRatesHard / TestHintThenTimeoutRatesAgain），实跑输出（逐字采捕）：

```
=== RUN   TestHintThenConfirmRatesHard
    turn_test.go:1747: rating after hint+confirm = "good", want hard (U-9 裁定 2026-10-06)
--- FAIL: TestHintThenConfirmRatesHard (0.02s)
=== RUN   TestHintThenTimeoutRatesAgain
    turn_test.go:1787: rating after hint+timeout = "hard", want again (U-9 裁定 2026-10-06)
--- FAIL: TestHintThenTimeoutRatesAgain (0.02s)
```

两个失败方向与计划预判完全一致（got "good" want "hard" / got "hard" want "again"）——现行终态覆盖
（hint 预记 outcomeHint 后被 confirm/timeout 首提交覆盖）确凿与裁定相反。非「意外绿」，未触发
inconclusive 分支，未改判裁定。

**第二步（符号级 RED）**：续加 TestWordRatingMatrix / TestHintCountIncrementsAndEvents /
TestCompletionReviewCarriesHintAccounting（引用尚不存在的 wordRating / perWordHints /
EventHintGiven / review_written 新字段），实跑确认整个测试包编译失败——交予 Task 2 的正常 RED：

```
pkg\rt\turn_test.go:1811:14: undefined: wordRating
pkg\rt\turn_test.go:1832:16: tm.tutor.perWordHints undefined (type *DictationTutor has no field or method perWordHints)
pkg\rt\turn_test.go:1837:33: undefined: EventHintGiven
```

计划 RED 门（五测试选择运行非绿）：`RED_CONFIRMED`。测试注释一律引用「U-9 裁定（2026-10-06）」。

### Task 2（TDD GREEN＋单一提交 f948b17）

四文件按序实现，五个新测试全绿，既有测试零改动通过。单提交：

- **Commit:** `f948b17f834e80234486ded39856877c122303d4`
- **Subject:** `fix(13-12): U-9 评级映射裁定落码——提示后对=hard、仍错/超时=again＋逐词提示计数`
- **Files（diff-tree 比对恰此 4 项）:** pkg/rt/dictation.go（91+/18−）、pkg/rt/timing.go（7+/1−）、pkg/rt/turn.go（19+/5−）、pkg/rt/turn_test.go（182+/0−）
- **Trailer:** `Co-Authored-By: Claude Code <noreply@anthropic.com>` 在末行
- 无意外文件删除；`.gsd/` 与其他未跟踪文件零裹挟（git status 仅余 `?? .gsd/`）

## 门禁实跑结果（GREEN）

| 门 | 命令 | 结果 |
|---|---|---|
| vet | `go vet ./pkg/rt/...` | **零报告**（VET_CLEAN） |
| 全量测试（替代口径） | `go test -count=1 ./pkg/rt/` | **ok 25.195s**（含 5 个新测试 + 全部既有测试零改动；与规划基线 25.059s 一致） |
| race | `go test -race -count=1 ./pkg/rt/` | **本机不可达**（见 Deviations） |
| 全仓编译 | `go build ./...` | BUILD_OK |
| 提交形状 | diff-tree 比对 + 主题前缀 + trailer grep | **ALL_GREEN** |

**-race 不可达原因（环境事实，非代码缺陷）**：Windows 上 race 检测器要求 CGO_ENABLED=1 + C 工具链；
本机 `go env CGO_ENABLED=0`，PATH 无 gcc/clang/zig/mingw/msys（`cgo: C compiler "gcc" not found`），
WSL 内亦无 Go。实跑 `CGO_ENABLED=1 go test -race` 复现该错。**替代口径**：采用计划自身记录的规划期
基线方法论（非 race 全量 `-count=1`），与 planning 实证同口径；race 全量留给具备 C 工具链的环境复跑。
该缺口已按 unrun-verify 尝试登记 WINDOWS.md 台账（gsd_run 在本 quick 任务环境不可用，台账未写——
此记录即落档处）。

## must_haves 逐条对照

**truths**

1. 「hint 后 confirm 的词评级为 hard」→ ✅ TestHintThenConfirmRatesHard 先 RED（got "good"）后 GREEN。
2. 「hint 后 timeout 的词评级为 again」→ ✅ TestHintThenTimeoutRatesAgain 先 RED（got "hard"）后 GREEN。
3. 「无提示路径不变」→ ✅ TestTimeoutConfirmRaceConfirmWins / TestTimeoutConfirmRaceTimeoutWins 既有
   断言零改动且全量套件继续通过（good/hard 由 wordRating(confirmed,0)/(timeout,0) 保持）。
4. 「每次 hint dispatch 逐词递增计数并发出 hint_given（word_index + hint_count）；同一词连发两次计 2」→ ✅
   TestHintCountIncrementsAndEvents（hint_given 恰 2 次，hint_count 依序 [1,2]，word_index [0,0]）。
5. 「review_written 新增 hint_words 与 hint_total 聚合」→ ✅ TestCompletionReviewCarriesHintAccounting
   （hint_words==[1]、hint_total==[1]）；其余 requested/updated/missing 与诚实话术未动。
6. 「提交后 vet 与 race 全量全绿」→ vet ✅、非 race 全量 ✅；race 门以替代口径完成（环境不可达，已在
   Deviations 与用户后续动作中如实记录——不静默降级）。

**artifacts**

1. 「恰一个 fix(13-12) 提交恰含 4 文件，主题以 `fix(13-12): U-9` 开头，末行 Co-Authored-By」
   → ✅ f948b17，diff-tree 断言恰 4 项，主题/trailer grep 均过。
2. 「SUMMARY 写盘不提交（docs 提交由 orchestrator 负责）」→ ✅ 本文件即写盘态，执行器未提交。

**key_links**

1. 「评级推导全链在服务端内部状态，LLM 输出只触发计数、绝不能直接设定评级」→ ✅ dispatchTurn intentHint
   仅调 recordHint（计数 + outcomeHint 预记）；评级只在完结时由 wordRating(perWordOutcome,
   perWordHints) 派生；分类 JSON 无任何直达评级的路径（T-261006-01 mitigate 落实）。
2. 「wordRating 是评级矩阵单一引用点 ↔ 13.1 未核对兜底路径，注释写明引用关系」→ ✅ 函数 doc 注释明示
   13.1 不得分叉第二份矩阵、(outcome, hints) 签名即人工综合口径的全部预留接缝；TestWordRatingMatrix
   表驱动钉死矩阵。
3. 「新事件字段守住 T-13-06b 脱敏边界」→ ✅ 全部新字段为计数类（word_index/hint_count/hint_words/
   hint_total），词文本/转写正文零入事件流；EventHintGiven doc 注释写明边界与 JSONL 冻结标识符身份。

## Deviations from Plan

**1. [Rule 3 - 阻塞门替代] `go test -race` 在本机不可达，以非 race 全量替代**
- **Found during:** Task 2 门禁
- **Issue:** 计划 Task 2 门含 `-race`；Windows race 检测需 CGO_ENABLED=1 + gcc，本机无任何 C 工具链
  （gcc/clang/zig/mingw 全缺，WSL 无 Go），race 构建物理不可达。此为规划期已存在的事实——计划自身
  的规划期基线实证即为非 race 口径（25.059s ok）。
- **Fix:** 门以 `go test -count=1 ./pkg/rt/`（25.195s ok，全绿）替代并如实落档；未尝试静默安装 C
  工具链（属用户机器级变更，未获授权）。
- **影响:** race 级数据竞争覆盖留空；改动全部为单锁纪律内的 map 字段与方法（无新 goroutine/锁），
  风险低但非零。
- **Commit:** f948b17（正文已记此偏差）

**2. [测试侧适配] 完结载荷测试的等待句数 prev+3 → prev+2**
- **Found during:** Task 2 首跑（turn_test.go:1874 teacher sentences = 6, want >= 7）
- **Issue:** Task 1 拍脑袋按「带提示行 = 3 句总结」估算；实际该路径 outcomeCounts 的 hinted=0
  （hint 预记已被 confirm 覆盖，hinted 口径只数终态仍为 hint 的词），总结为 2 句。
- **Fix:** 仅改等待数（prev+2，附口径注释）；被测行为断言（评级 hard + 事件字段 [1]）未动。属计划
  Task 2 明文允许的「断言适配微调」，非生产代码改动。

其余：计划按文执行，无架构偏差、无范围外裹挟。认证门：无。已知 Stub：无（所有数据路径已接线）。
Threat Flags：无（新增事件面均落在计划 threat_model T-261006-01/02/03 之内）。

## 用户后续手工步骤（活跃课复测，不阻塞本任务收口）

下一节活跃课（新到期词）逐项核对评级落库（对照导出 JSONL 的 hint_given / review_written 与 FSRS 侧
review_logs）：

1. **「提示后写对 → hard」**：对某词说「不会写」，听完提示后说「好了」→ 完结后该词评级应为 **hard**；
   JSONL 中该词应有 hint_given（hint_count=1）且 review_written 的 hint_words/hint_total ≥ 1。
2. **「提示后静默到点 → again」**：对某词说「不会写」后不再应答，等 60 秒超时推进 → 完结后该词评级应为
   **again**。
3. 顺带观察无提示词不受影响：直接说「好了」的词仍记 **good**、静默超时仍记 **hard**。

另留一项环境级后续：在具备 C 工具链的环境（如装 mingw 后的本机或 VPS）复跑
`go test -race -count=1 ./pkg/rt/` 补齐 race 门。

## Self-Check: PASSED

- [x] SUMMARY 存在：`.planning/quick/261006-u2o-u-9-checkpoints-13-1/261006-u2o-SUMMARY.md`（本文件）
- [x] 提交存在：`git log` 含 f948b17f834e80234486ded39856877c122303d4（恰 1 个提交，64960f7..HEAD）
- [x] 4 个计划文件均在 HEAD 中且工作树干净（仅 `?? .gsd/` 未跟踪，符合约束）
- [x] 五个新测试 + 全量套件实跑绿（25.195s ok）；vet 零报告；提交形状 ALL_GREEN
