---
task_id: 260823-sdx
slug: session-go-wordcomplete-stream-generation
status: complete
date: 2026-08-23
commit: 3a1fc59
---

# 快速任务完成摘要：修复 session.go 过期 WordComplete 事件竞态（stream generation）

## 完成概要

按 TDD 两步走完成修复：Session 增加 stream generation 计数器，spawnStreamWord 递增并按值捕获代数，推送的 WordComplete 事件携带自己的代数；事件循环在接收臂丢弃代数不等于当前值的过期事件。repeat/next_word 交错时被取代旧流（已取消但仍完成推送）的 WordComplete 不再把新单词的 LISTENING 提前切成 DICTATING（跳过听写音频阶段的竞态消除）。

- 提交：`3a1fc59` — `fix(session): 260823-sdx 修复 repeat 竞态跳词——按流代数丢弃过期 WordComplete 事件`（单个原子提交，恰好含 types.go / session.go / session_test.go 三文件）
- 执行时长：约 12 分钟（2026-08-23T12:37Z–12:49Z，含基线验证）

## TDD 证据

**RED（Task 1，未提交，只改 session_test.go）**

```
--- FAIL: TestEventLoop_Repeat_StaleWordCompleteDropped (0.11s)
    session_test.go:658: stale WordComplete drove a transition: state=DICTATING, want=LISTENING
```

- go test 退出码 1，失败精确命中步骤 5 的中间状态断言（非编译错误、非超时挂死），失败点正是无修复时过期 WordComplete 驱动了 LISTENING→DICTATING。
- gatedStreamer fake 故意不 select ctx.Done()，确定性复现"取消已发出但流仍完成并推送"的最坏时序；release 幂等（含 released 标记），t.Cleanup 统一放行所有门，防提前失败时 goroutine 泄漏。

**GREEN（Task 2，单原子提交 3a1fc59）**

```
--- PASS: TestEventLoop_Repeat_StaleWordCompleteDropped (0.11s)
ok  github.com/xiaotian/home-tutor-agent/pkg/session
```

## 关键变更

### pkg/session/types.go
- `SessionEvent` 新增 `StreamGen uint64` 字段 + 注释：仅对 EventWordComplete 有意义（系统内部事件，客户端帧无法注入），事件循环用它丢弃被取代旧流的过期事件。

### pkg/session/session.go（三处最小改动，零新锁）
1. `Session` 事件_loop 段新增 `streamGen uint64`（streamCancel 旁），注释明确：仅事件循环 goroutine 读写（D-02 单写者，同 wordQueue/wordIndex，无锁），spawn 出的 goroutine 只用 go 语句按值捕获的副本。
2. `spawnStreamWord`：创建 streamCtx/cancel 处 `s.streamGen++` 后局部捕获 `gen := s.streamGen`；goroutine 推送字面量改为 `SessionEvent{Type: EventWordComplete, WordID: wordID, StreamGen: gen}`。取消旧流、currentWordID 记录、runCtx.Done 守卫全部不动。
3. `run()` 的 `case ev := <-s.events:` 臂：紧跟 EventTimeout/EventCancel 守卫之后新增守卫——`ev.Type == EventWordComplete && ev.StreamGen != s.streamGen` 时 Debug 日志（component/session、session_id、word_id、两侧代数）并 `continue` 跳过 handleEvent；否则照旧。

### pkg/session/session_test.go
- 新增 `gatedStreamer`（含编译期断言 `var _ session.SessionStreamer = (*gatedStreamer)(nil)`，D-43）与 `waitForStreamCalls` 轮询 helper（2ms 间隔 / 2s 超时；repeat 无状态变化，spawn 副作用是唯一同步信号）。
- 新增 `TestEventLoop_Repeat_StaleWordCompleteDropped`：play_word(gen1 apple)→DICTATING → repeat(gen2 apple)→门控阻塞 → next_word(gen3 banana，cancel gen2 但 fake 忽略)→LISTENING → 放行 gen2（过期事件）→ **断言仍 LISTENING 且 CurrentWord=="banana"（判别性中间状态断言）** → 放行 gen3→正常 DICTATING；收尾断言 calls()==3、wordAt 序列 apple/apple/banana、cancel 后 waitDone。

## 验证结果

| 检查 | 结果 |
|------|------|
| 定向新测试（RED，修复前） | FAIL，消息精确命中 `stale WordComplete drove a transition`，退出码 1 |
| 定向新测试（GREEN，修复后） | PASS（0.11s） |
| `go test ./... -count=1`（10 包，与基线 be653a4 一致全绿） | 全部 ok（cmd/service、database、fsrs、infra、mimo、pybridge、session、tts、ws） |
| `go vet ./...` | 零告警（退出码 0） |
| `go test ./pkg/session/ -count=5`（稳定性加压） | ok（3.8s，无 flake） |
| `git show --name-only`（HEAD） | 恰好 3 个文件：session.go / session_test.go / types.go |
| `git diff HEAD~1 -- pkg/session/fsm.go` | 空（FSM 零改动，hard_constraint 2） |

既有回归面（TestEventLoop_WordComplete / LastWord / MultiWordFlow / StreamWordCompletion 及 fsm_test 直调 transition 用例）零回归；无 TestEventLoop_WordComplete 挂死（约束中警示的嵌套锁模式未出现——本修复根本不加锁）。

## 偏差与说明

1. **[环境限制] `go test -race ./pkg/session/` 无法在本机执行**：Windows 主机无 C 工具链（gcc 不在 PATH，CGO_ENABLED=0），`-race` 需要 cgo。此为宿主机既有限制（Makefile 的 race target 在本机同样不可用），非本次改动引入。补偿控制：
   - 代数无数据竞态是**构造性保证**：`s.streamGen` 的全部读写都在事件循环 goroutine 内（spawnStreamWord 仅经 run→handleEvent→onTransition 到达；接收臂守卫在 run() 内），goroutine 只捕获按值副本 `gen`，全仓库生产代码仅 spawnStreamWord 一处构造 EventWordComplete（已 grep 复核）；
   - `go vet` 干净 + 全量测试绿 + `-count=5` 加压无 flake。
   - 建议在 Linux/CI（具备 cgo）侧跑一次 `go test -race ./pkg/session/` 作最终确认。
2. **[机制偏差] 计划 verify 命令中的 `cd "D:/workspace/ai/xiaotian"` 改为在 worktree 根执行**：本次执行运行于隔离 worktree（`D:/workspace/ai/xiaotian/.claude/worktrees/agent-ab131fbf08edd9d93`），所有 go 命令必须针对 worktree 内的改动运行。验证语义不变。
3. gatedStreamer 的 `release` 做成幂等（计划只要求"关闭第 i 个 gate"）：t.Cleanup 会在测试已手动放行部分门后再次统一放行，不幂等会 close 已关闭 channel 而 panic，掩盖真实测试结果。属计划文本内的实现细节补全。

## 威胁面核对

- T-sdx-01（Tampering）：StreamGen 仅服务端生成，外部伪造推送携带零值代数将被丢弃——行为收紧，已按 mitigate 落实。
- T-sdx-02（DoS）：events 通道容量 32 与 flood-drop 语义未动，接受现状。
- T-sdx-03（回归面）：全量测试 + vet + 加压覆盖既有事件循环用例，mitigate 落实（race 项见偏差 1）。

## Self-Check: PASSED

- 文件存在：pkg/session/session.go、pkg/session/types.go、pkg/session/session_test.go、本 SUMMARY.md
- 提交存在：3a1fc59（worktree-agent-ab131fbf08edd9d93 分支 HEAD）
- 文档工件（SUMMARY.md/STATE.md/PLAN.md）未提交，按约束留给编排器统一处理
