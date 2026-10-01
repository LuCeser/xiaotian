---
phase: 13-realtime-minimal-loop
plan: 01
subsystem: database
tags: [sqlite, go, idempotency, optimistic-concurrency, modernc-sqlite, tdd]

# Dependency graph
requires:
  - phase: 04 (v1.0 component evidence)
    provides: database.Open WAL DSN + idempotent Migrate statement-list pattern + session_repo repository conventions (nil-guard, NoOpLogger, sentinels)
provides:
  - "pkg/lesson 包：lesson.Migrate、lesson.NewStore、lesson.NewExecutor、Store.EnsureLesson、Store.GetLesson、Executor.GetLessonState、Executor.AdvanceWord"
  - "新表 lessons / tool_requests + 索引 idx_tool_requests_lesson"
  - "哨兵错误 ErrLessonNotFound / ErrVersionConflict / ErrInvalidLesson + ConflictError{CurrentVersion}"
affects: [13-04 (turn manager tool loop), 13-06 (rt.LessonExecutor adapter), R2 learning loop]

actuals:
  tokens: 6600      # chars/4 over the realized diff (26388 chars across 4 new files)
  tasks: 2
  commits: 3        # measured: git rev-list --count bd0a4b4..HEAD
plan_head_before: bd0a4b4aa3faa29e45fa74b221810d19dabe1adf
plan_head_after: dcd97a82f68195e07606c88c108ee3938bc7ece8

tech-stack:
  added: []         # modernc.org/sqlite v1.55.0 already locked in go.mod (Phase 04)
  patterns:
    - "Idempotent tool write (RESEARCH Pattern 5): request_id PK claim + first-stored-result replay + expected_version optimistic concurrency, all inside one BeginTx"
    - "Typed SQLite constraint detection: errors.As(*sqlite.Error) + Code() against lib constants, never error-string matching"

key-files:
  created:
    - pkg/lesson/store.go
    - pkg/lesson/executor.go
    - pkg/lesson/store_test.go
    - pkg/lesson/executor_test.go
  modified: []

key-decisions:
  - "Constraint check extended with SQLITE_CONSTRAINT_PRIMARYKEY(1555): modernc surfaces 1555 for the request_id PK violation, not the plan-anticipated UNIQUE(2067); all three codes (1555/2067/19) are checked, keeping the typed no-string-matching contract intact (Rule 1, commit dcd97a8)"
  - "Plan text's 'pkg/errs 库' resolved to github.com/pkg/errors — pkg/errs does not exist in this repo; the plan's own anchor ('与 session_repo 相同的包装风格') pins the pkg/errors Wrap verb-phrase convention"
  - "TDD RED carried compile stubs in executor.go (typed signatures returning 'not implemented') so the 4 behavior tests ran and failed on assertions — RED verified RED_EVIDENCE_OK via check tdd-red-evidence; stubs deleted in GREEN"

patterns-established:
  - "Idempotent tool execution: INSERT-or-conflict claim of request_id, rollback on conflict, replay of first stored result (empty stored result => ErrInvalidLesson, never success)"

requirements-completed: [RT-07]

coverage:
  - id: D1
    description: "lessons/tool_requests DDL + idempotent Migrate + Store (EnsureLesson first-content-wins via INSERT OR IGNORE, GetLesson with ErrLessonNotFound)"
    requirement: RT-07
    verification:
      - kind: unit
        ref: "pkg/lesson/store_test.go#TestStoreMigrateIdempotent"
        status: pass
      - kind: unit
        ref: "pkg/lesson/store_test.go#TestStoreEnsureLesson"
        status: pass
      - kind: unit
        ref: "pkg/lesson/store_test.go#TestStoreGetLessonNotFound"
        status: pass
      - kind: unit
        ref: "pkg/lesson/store_test.go#TestStoreNewStoreGuards"
        status: pass
    human_judgment: false
  - id: D2
    description: "Executor.AdvanceWord idempotent transaction (request_id replay) + expected_version optimistic concurrency (ConflictError.CurrentVersion) + GetLessonState delegation"
    requirement: RT-07
    verification:
      - kind: unit
        ref: "pkg/lesson/executor_test.go#TestAdvanceWordIdempotent"
        status: pass
      - kind: unit
        ref: "pkg/lesson/executor_test.go#TestAdvanceWordVersionConflict"
        status: pass
      - kind: unit
        ref: "pkg/lesson/executor_test.go#TestAdvanceWordLessonChange"
        status: pass
      - kind: unit
        ref: "pkg/lesson/executor_test.go#TestAdvanceWordNotFound"
        status: pass
    human_judgment: false

duration: 2h 54m
completed: 2026-10-01
status: complete
---

# Phase 13 Plan 01: pkg/lesson — 课堂状态存储 + 幂等工具执行层 Summary

**SQLite lessons/tool_requests 两表 + AdvanceWord 单事务幂等执行：request_id 主键防重放、expected_version 乐观并发防旧版本写，全部在工具执行端闭环（不依赖 LLM 记忆）**

## Performance

- **Duration:** 2h 54m wall clock（含约 2 小时 API 配额中断等待；有效工作时间远短于此）
- **Started:** 2026-10-01T12:43:15Z
- **Completed:** 2026-10-01T15:37:36Z
- **Tasks:** 2
- **Files created:** 4（777 insertions）

## Accomplishments

- `pkg/lesson` 新包落地：`Migrate` 幂等建 `lessons` + `tool_requests` + `idx_tool_requests_lesson`，DDL 逐字取自 13-PATTERNS
- `Store.EnsureLesson` INSERT OR IGNORE 语义：同 ID 二次调用返回首次内容、version 不动；`GetLesson` 哨兵 `ErrLessonNotFound` 经 errors.Is 命中
- `Executor.AdvanceWord` 单事务（BeginTx）按 RESEARCH Pattern 5 顺序：INSERT tool_requests 占位 → UPDATE lessons WHERE version=? → 回写 result JSON → Commit
- request_id 重放返回首次存储结果（Idempotent=true）且 lessons 只推进一次；expected_version 过期返回 `*ConflictError{CurrentVersion}`，lessons 行与 tool_requests 均不变
- 约束冲突类型化判定（评审 L-2）：`errors.As(*sqlite.Error)` + `Code()` 对 lib 常量，源码零错误文本匹配
- 事务上方固定 L-3 前提注释：`SetMaxOpenConns(1)` 单连接下事务短暂阻塞 /ws 冻结栈写，加长事务必须先重审

## TDD Gate Compliance

| Gate | Commit | Evidence |
|------|--------|----------|
| RED | `b8e5519` (test) | 4 个 TestAdvanceWord* 全部在首条行为断言失败；`check tdd-red-evidence` 判定 `RED_EVIDENCE_OK`（4 tests / 0 pass / 4 fail，target=TestAdvanceWordIdempotent） |
| GREEN | `dcd97a8` (feat) | `go test ./pkg/lesson/ ./pkg/database/ -count=1` 全绿 |
| REFACTOR | —（无需） | GOLDEN 实现即最终形态；事务步骤保持线性可读，未做额外清理 |

RED 证据记录（command/exitCode/output/targetTest）保存在 worktree git-dir：`tdd-red-13-01.json` + `tdd-red-raw.txt`。

## Task Commits

1. **Task 1: pkg/lesson/store.go — lessons/tool_requests 表 + Migrate + EnsureLesson** - `e779f63` (feat)
2. **Task 2: pkg/lesson/executor.go — AdvanceWord 幂等事务 + 版本冲突哨兵** - `b8e5519` (test, RED) + `dcd97a8` (feat, GREEN)

## Files Created/Modified

- `pkg/lesson/store.go` - DDL 常量、Migrate、Lesson 类型、Store（NewStore nil-guard + NoOpLogger 兜底、EnsureLesson/GetLesson、哨兵 ErrLessonNotFound/ErrInvalidLesson）
- `pkg/lesson/executor.go` - Executor（NewExecutor、GetLessonState、AdvanceWord 单事务、idempotentHit 回放路径、ConflictError/ErrVersionConflict、L-3 前提注释）
- `pkg/lesson/store_test.go` - TestStore* 四组：迁移幂等/两表存在、EnsureLesson IGNORE 语义与非法参数拒写、未知名哨兵、构造器守卫
- `pkg/lesson/executor_test.go` - TestAdvanceWord* 四组：幂等重放、版本冲突、顺序推进、未知名

## Must-Have Truths Verification

| Truth | Evidence | Status |
|-------|----------|--------|
| 同一 request_id 双执行只推进一个词，第二次返回首次存储原结果（Idempotent=true） | TestAdvanceWordIdempotent：version 断言 == 2 | PASS |
| expected_version 不匹配写入被拒，冲突错误携带当前 version，lessons 行不变 | TestAdvanceWordVersionConflict：errors.Is + errors.As + CurrentVersion==2 + current_index 不变 + tool_requests 无该行 | PASS |
| 两表经 lesson.Migrate 幂等创建，重复调用无副作用 | TestStoreMigrateIdempotent：连调两次、表/索引计数各 1 | PASS |
| 幂等与乐观并发都在工具执行端（SQLite 单事务），不依赖 LLM 记忆 | executor.go 全部逻辑在 BeginTx 事务内；pkg/lesson 仅 import pkg/infra（无 mimo/rt/ws） | PASS |
| （禁令）advance_word 幂等不得依赖 LLM 记忆或对话上下文 | 实现路径为纯 SQLite 唯一键 + 事务，无任何对话上下文输入 | PASS |

## Decisions Made

- 见 frontmatter key-decisions（1555 主键约束码扩展、pkg/errs 解释为 github.com/pkg/errors、RED 编译桩策略）。

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] 约束冲突码需含 SQLITE_CONSTRAINT_PRIMARYKEY(1555)**
- **Found during:** Task 2（GREEN 首跑）
- **Issue:** 计划预期唯一键冲突返回 SQLITE_CONSTRAINT_UNIQUE(2067) 或 SQLITE_CONSTRAINT(19)；实测 modernc 驱动对 `tool_requests.request_id`（PRIMARY KEY）违规返回 1555，原判定集漏掉该码，幂等命中路径从不触发（TestAdvanceWordIdempotent 失败）
- **Fix:** 类型化判定扩展为三码：`serr.Code() == lib.SQLITE_CONSTRAINT_PRIMARYKEY || SQLITE_CONSTRAINT_UNIQUE || SQLITE_CONSTRAINT`，并在源码注释记录 1555 为真实主因、另两码为纵深防御；仍然零字符串匹配（L-2 契约保持）
- **Files modified:** pkg/lesson/executor.go
- **Verification:** `go test ./pkg/lesson/ -count=1` 全绿；grep 确认源码无 "UNIQUE constraint"/"constraint failed" 文本匹配
- **Committed in:** `dcd97a8`（GREEN commit）

---

**Total deviations:** 1 auto-fixed (1 bug)
**Impact on plan:** 判定集合扩展不改变计划语义——计划要求的是"类型化判定、禁止字符串匹配"，三码并检是其对 SQLite 真实返回码的完整覆盖。无范围蔓延。

## Issues Encountered

None（除执行中途的 API 配额中断——与代码无关，恢复后原样续跑）。

## Authentication Gates

None — 无外部服务依赖。

## User Setup Required

None - no external service configuration required.

## Threat Mitigations Delivered

| Threat | Disposition | Where |
|--------|-------------|-------|
| T-13-04 (Tampering, 工具重放/重复推进, high) | mitigate | request_id PK 幂等：重复执行回放首次结果，绝不二次 UPDATE（executor.go 步骤 a + idempotentHit） |
| T-13-03b (Tampering, 并发/旧版本写, high) | mitigate | expected_version 乐观并发：0 行即拒绝并携带当前版本（executor.go 步骤 b） |
| T-13-01b (Elevation, 幻觉/注入 lesson_id, medium) | mitigate | 空 lesson_id/request_id、expected_version<1 返回 ErrInvalidLesson 不落库（AdvanceWord 入口守卫）；更深参数白名单留给 13-04 turn.go |

## Next Phase Readiness

- `pkg/lesson` 就绪，13-04 轮次管理器可直接以 `lesson.Executor` 执行 get_lesson_state / advance_word 工具；13-06 以 `var _ rt.LessonExecutor` 适配
- RT-07 由 13-01/13-04/13-07 共同声明，本计划完成后 REQUIREMENTS.md 勾选被 shared-ID 门槛正确挂起，待全部声明计划完成
- 存量 `go test -short ./pkg/...` 全绿（14 包），无回归

## Self-Check: PASSED

- pkg/lesson/store.go、executor.go、store_test.go、executor_test.go 均存在（`ls` 验证）
- 提交 e779f63 / b8e5519 / dcd97a8 均在 git log（`--all` 验证）
- commits: 3 为实测值（`git rev-list --count bd0a4b4..HEAD`），与 narrated 一致

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-01*
