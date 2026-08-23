---
phase: 260823-sdx
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - pkg/session/types.go
  - pkg/session/session.go
  - pkg/session/session_test.go
autonomous: true
requirements: []
must_haves:
  truths:
    - repeat 与 next_word 交错后，被取代旧流推送的过期 WordComplete（generation ≠ 当前值）被事件循环丢弃，会话停留在 LISTENING，单词不被跳过
    - 当前 generation 的 WordComplete 仍正常驱动 LISTENING→DICTATING，pkg/session 既有事件循环测试（TestEventLoop_WordComplete / LastWord / MultiWordFlow / StreamWordCompletion）零回归
    - streamGen 仅在事件循环 goroutine 内读写，spawn 出的 goroutine 只使用按值捕获的代数副本——全程不为 streamGen 加 s.mu、不用 atomic、不在持锁期间做 channel 发送或调用回调（历史死锁不复发）
    - go test ./... 与 go vet ./... 全部通过，go test -race ./pkg/session/ 干净
  artifacts:
    - pkg/session/session_test.go（新增 gatedStreamer 门控 fake + TestEventLoop_Repeat_StaleWordCompleteDropped）
    - pkg/session/types.go（SessionEvent 新增 StreamGen uint64 字段及注释更新）
    - pkg/session/session.go（Session 新增 streamGen 字段、spawnStreamWord 代数捕获、run() 过期事件丢弃）
  key_links:
    - spawnStreamWord 递增 s.streamGen 并捕获局部 gen → goroutine 推送携带 StreamGen: gen 的 WordComplete → run() 事件接收臂比较 ev.StreamGen 与 s.streamGen，不等即丢弃——三段全部位于事件循环 goroutine 或经 go 语句按值传递，形成无锁数据流闭环
    - transition()（pkg/session/fsm.go）保持纯状态机、不含代数判断；fsm_test.go 直接调用 transition 的既有用例不受本修复影响
---

# 快速任务：修复 session.go 过期 WordComplete 事件竞态（stream generation）

<objective>
pkg/session/session.go 存在过期事件竞态：DICTATING/PAUSED 下 repeat（或 LISTENING 下再次 play_word）时，spawnStreamWord() 先取消旧流再启动新流，但旧流的 goroutine 可能已完成 StreamWord 并把 WordComplete 推入缓冲 events channel（容量 32，session.go:27）。事件循环消费这个过期事件，在错误的时机驱动 FSM 转换——最坏时序下新单词的 LISTENING 被旧单词的 WordComplete 提前切成 DICTATING（听写音频被跳过）。

修复方案（用户指定）：Session 增加 stream generation 计数器；spawnStreamWord 递增并捕获代数；推送 WordComplete 的 goroutine 携带自己的代数；事件循环丢弃代数不等于当前值的 WordComplete 事件。

Purpose: 消除 repeat/next_word 交错时的跳词竞态，同时不引入任何新锁（该区域曾因 onTransition 持写锁 + timeoutFor→CurrentWord 取 RLock 死锁）。
Output: TDD 两步走——先写"过期 WordComplete 被丢弃"的失败测试（RED），再实现 generation 计数器使其通过（GREEN）；全部改动单个原子提交。
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@pkg/session/session.go
@pkg/session/fsm.go
@pkg/session/types.go
@pkg/session/session_test.go
@pkg/session/fakes_test.go
</context>

<hard_constraints>
1. **锁纪律（历史死锁警告）**：本区域曾因 onTransition 持有 s.mu 写锁期间调用 timeoutFor → CurrentWord 取 RLock 而死锁。本修复必须做到：streamGen 的读写全部发生在事件循环 goroutine 内（run → handleEvent → onTransition → spawnStreamWord 及 run 的事件接收臂同属一个 goroutine），spawn 出的 goroutine 只使用按值捕获的局部代数副本。禁止为 streamGen 加 s.mu 或 sync/atomic，禁止在持有 s.mu 期间向 events channel 发送或调用任何回调。
2. **FSM 保持纯粹**：代数过滤只放在事件循环层（run 的事件接收臂），pkg/session/fsm.go 的 transition() 一行不改——fsm_test.go 直接调用 transition 的既有用例必须原样通过。
3. **不得用 wordID 作为判废依据**：repeat 重播的是同一个单词，wordID 无法区分新旧流；generation 是唯一正确的判别器。
4. **提交**：整个任务一个原子提交（测试 + 实现同 commit），提交信息用中文、带 260823-sdx 标识（风格见 git log：type(scope): 描述）。Task 1 完成后不得单独提交。
5. 本任务无新依赖、无 go.mod 变更。
</hard_constraints>

<repro_timeline>
测试必须构造的确定性时序（gatedStreamer 的 fake 故意忽略 ctx 取消，模拟"取消已调用但流仍完成并推送"的最坏情形）：

1. play_word → LISTENING，spawn gen1（apple），门控阻塞 → 放行 gen1 → WordComplete(gen1) → DICTATING。
2. push repeat → repeatPending → onTransition 在 DICTATING 下 spawn gen2（apple），门控阻塞。
3. push next_word → wordIndex=1 → LISTENING，spawn gen3（banana）并 cancel gen2——但 fake 忽略取消，gen2 仍阻塞在自己的门上。
4. 放行 gen2 → 其 goroutine 推送 WordComplete(gen2)=过期事件（当前代数已是 gen3）。
5. 无修复：事件循环在 LISTENING 消费它 → 过早 DICTATING（banana 的听写音频阶段被跳过）。有修复：丢弃，停留 LISTENING。
6. 放行 gen3 → WordComplete(gen3) 为当前代数 → 正常 LISTENING→DICTATING。

注意：无论有无修复，序列走完后的**最终**状态都是 DICTATING（无修复时 gen3 的事件因 DICTATING+WordComplete 属无效转换被 FSM 丢弃）。因此断言必须打在第 5 步的中间状态上，禁止把测试改写成只断言最终状态。
</repro_timeline>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: RED——写过期 WordComplete 被丢弃的失败测试（gatedStreamer + TestEventLoop_Repeat_StaleWordCompleteDropped）</name>
  <files>pkg/session/session_test.go</files>
  <behavior>
    - Test 1（核心）: 步骤 5 时序点，过期 WordComplete(gen2) 已投递但 state 必须仍是 database.StateListening；无修复时实际为 StateDictating，测试以消息 "stale WordComplete drove a transition" 失败
    - Test 2（补充）: 过期事件不得推进 wordIndex——s.CurrentWord() 仍为 "banana"（第二个词）
    - Test 3（GREEN 后生效）: 放行 gen3 后 waitForState 达到 StateDictating；gatedStreamer 共 3 次调用，wordAt 序列为 apple/apple/banana
  </behavior>
  <action>只改 pkg/session/session_test.go（package session_test），不触碰任何生产代码、不提交。文件头 import 增加 "sync" 与 "github.com/xiaotian/home-tutor-agent/pkg/tts"。

一、新增门控 fake `gatedStreamer`（定义在 session_test.go，靠近新测试；不改 fakes_test.go），实现 session.SessionStreamer：
- 内部用 sync.Mutex 保护三个切片：words（每次调用的 wordID）、gates（每次调用的放行 chan struct{}）、returned（每次调用的返回通知 chan struct{}）。
- StreamWord(ctx, client, sessionID, wordID, startSeq)：进入时加锁登记本次调用（追加 wordID、新建并追加 gate 与 returned 两个 channel）并解锁；然后阻塞在本次 gate 上——**故意不 select ctx.Done()**，模拟取消已发出但流照样完成推送的最坏时序（这是复现竞态的关键，写明注释）；被放行后 close 本次 returned channel 并返回 nil。
- ClearSeqCounter 为 no-op（满足接口）。
- 辅助方法：release(i) 关闭第 i 个 gate；waitReturned(t, i) 在超时（2s）内等第 i 个 returned 关闭，超时 t.Fatalf；calls() 返回已登记调用数；wordAt(i) 返回第 i 次调用的 wordID。
- 编译期断言 var _ session.SessionStreamer = (*gatedStreamer)(nil)（仓库 D-43 约定）。

二、新增测试 TestEventLoop_Repeat_StaleWordCompleteDropped，复用现有 helper（waitForState、waitDone）与 multiWordTimeoutCfg()（5s 状态超时，给门控留足时间）：
1. 构造：twoCards（apple/banana，同 TestEventLoop_MultiWordFlow）、gatedStreamer、fakeHub、fakeFSRSService、fakeTTSGenerator、newFakeSessionRepository；NewSession(client 传 nil，同既有测试)；t.Cleanup 中 release 所有已登记 gate（防提前失败时 goroutine 泄漏堆积）；Start(ctx) + defer cancel()。
2. push play_word → 轮询等待 calls()>=1（新增本地 helper waitForStreamCalls，2ms 间隔轮询 gatedStreamer.calls()，2s 超时）→ release(0) → waitReturned(0) → waitForState(StateDictating)。
3. push repeat（EventControlFrame + ControlType "repeat"）→ waitForStreamCalls(2)（确保 gen2 已 spawn 且阻塞在门上）。
4. push next_word → waitForState(StateListening) → waitForStreamCalls(3)（gen3 已 spawn；此间 gen2 的 ctx 已被 cancel，但 fake 忽略之）。
5. release(1) → waitReturned(1) → time.Sleep(100ms)（等事件循环消费过期事件；循环空闲，消费在微秒级，100ms 裕量充足）。
6. 断言（RED 判据）：s.State() == database.StateListening，失败消息用 t.Errorf("stale WordComplete drove a transition: state=%v, want=%v", ...)；并断言 s.CurrentWord() == "banana"。
7. release(2) → waitReturned(2) → waitForState(StateDictating)；收尾断言 calls()==3 且 wordAt(0..2) 依次为 "apple","apple","banana"。
8. defer cancel() 触发后 waitDone(t, s, 2*time.Second)。

三、运行 RED 验证（见 verify），确认失败原因正是步骤 6 的断言（无修复时 state 为 StateDictating），而非编译错误、panic 或超时挂死——失败形态不对时先修测试本身。完成后不提交（约束 4）。</action>
  <verify>
    <automated>out=$(cd "D:/workspace/ai/xiaotian" && go test ./pkg/session/ -run 'TestEventLoop_Repeat_StaleWordCompleteDropped' -count=1 2>&1); rc=$?; printf '%s\n' "$out"; [ $rc -ne 0 ] && printf '%s\n' "$out" | grep -q 'stale WordComplete drove a transition'</automated>
  </verify>
  <done>gatedStreamer 与 TestEventLoop_Repeat_StaleWordCompleteDropped 已就位且编译通过；`go test ./pkg/session/ -run 'TestEventLoop_Repeat_StaleWordCompleteDropped' -count=1` 以非零退出失败，输出含 "stale WordComplete drove a transition"（失败点为步骤 6 的中间状态断言）；未改动生产代码；未提交。</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: GREEN——stream generation 计数器实现（StreamGen 字段 + spawn 捕获 + 事件循环丢弃）+ 单原子提交</name>
  <files>pkg/session/types.go, pkg/session/session.go</files>
  <behavior>
    - SessionEvent 新增 StreamGen uint64：仅 EventWordComplete 使用，由 spawnStreamWord 生成，客户端帧不可注入
    - spawnStreamWord 每次调用递增 s.streamGen 并把捕获值写入其 goroutine 推送的 WordComplete 事件
    - run() 事件接收臂丢弃 StreamGen 与 s.streamGen 不等的 WordComplete（Debug 日志），当前代数事件行为不变
  </behavior>
  <action>三处最小改动（严禁加锁，见 hard_constraints 1）：

一、pkg/session/types.go：SessionEvent 结构体新增字段 `StreamGen uint64`（置于 Payload 之前或之后均可，keyed 字面量不受顺序影响），并更新结构体注释：StreamGen 携带 spawnStreamWord 的流代数，仅对 EventWordComplete 有意义（系统内部事件，非客户端输入），事件循环用它丢弃被取代旧流的过期事件。

二、pkg/session/session.go 三步：
1. Session 结构体事件_loop 段（streamCancel 旁）新增 `streamGen uint64` 字段，注释说明：spawnStreamWord 的单调递增流代数；仅事件循环 goroutine 读写（D-02 单写者，同 wordQueue/wordIndex，无需锁）；spawn 出的 goroutine 经 go 语句按值捕获，绝不直接读本字段。
2. spawnStreamWord：在创建 streamCtx/cancel 处递增代数——`s.streamGen++` 后立刻 `gen := s.streamGen`（局部捕获）；goroutine 内推送处把字面量改为 SessionEvent{Type: EventWordComplete, WordID: wordID, StreamGen: gen}。其余逻辑（取消旧流、currentWordID 记录、runCtx.Done 守卫）不动。
3. run() 的 `case ev := <-s.events:` 臂，紧跟现有 EventTimeout/EventCancel 守卫之后、s.handleEvent(ev) 之前，新增守卫：若 ev.Type == EventWordComplete 且 ev.StreamGen != s.streamGen，则以 s.logger.Debug 记录（component=session、session_id、事件携带的 word_id 与两侧代数）并跳过 handleEvent（该 for 循环内 continue 即回到 select）；否则照旧调用 handleEvent。注释说明：这是被 repeat/resume/next_word 取代的旧流在取消后仍完成推送的过期事件，属预期竞态产物，静默丢弃；当前代数事件的 FSM 行为（含 DICTATING+WordComplete 走 ErrInvalidTransition 被丢弃）不变。

三、GREEN 验证（见 verify：新测试通过 + go test ./... + go vet ./... + pkg/session -race 全绿）。全部通过后做本任务唯一的原子提交，同时包含 pkg/session/types.go、pkg/session/session.go、pkg/session/session_test.go 三个文件，提交信息形如：`fix(session): 260823-sdx 修复 repeat 竞态跳词——按流代数丢弃过期 WordComplete 事件`。</action>
  <verify>
    <automated>cd "D:/workspace/ai/xiaotian" && go test ./pkg/session/ -run 'TestEventLoop_Repeat_StaleWordCompleteDropped' -count=1 && go test ./... -count=1 && go vet ./... && go test -race ./pkg/session/ -count=1 && git show --name-only --format= HEAD | sort | grep -c '^pkg/session/\(session\|types\)\.go$\|^pkg/session/session_test\.go$'</automated>
  </verify>
  <done>新测试通过（过期事件被丢弃、停留 LISTENING、当前代数事件正常驱动 DICTATING）；go test ./... 与 go vet ./... 零失败；go test -race ./pkg/session/ 干净；fsm.go 零改动、未引入任何 s.mu/atomic 于 streamGen、持锁期间无 channel 发送；存在且仅存在一个以 260823-sdx 标识的原子提交，恰好含上述三个文件。</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| （无新增） | 纯服务端内部并发逻辑修复；客户端→WS 的控制帧边界已存在且本次不改动其解析或信任面。StreamGen 由服务端 spawnStreamWord 生成，客户端帧无法注入该字段 |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-sdx-01 | Tampering | SessionEvent.StreamGen / 事件循环丢弃逻辑 | low | mitigate | StreamGen 只在服务端生成；WordComplete 本就是系统内部事件，外部伪造推送（携带零值代数）将被丢弃——行为收紧而非放宽，且生产代码中该事件仅 spawnStreamWord 一处产生（已 grep 确认） |
| T-sdx-02 | Denial of Service | events channel（容量 32） | low | accept | 过期事件仍占用缓冲槽直至被消费，但产生频率受 StreamWord 完成节奏约束；通道容量与既有 flood-drop 语义（T-07-05）不变 |
| T-sdx-03 | 拒绝服务（回归面） | pkg/session 既有事件循环测试 | medium | mitigate | Task 2 verify 强制 go test ./... 全量 + -race，覆盖 TestEventLoop_WordComplete/LastWord/MultiWordFlow/StreamWordCompletion 等回归面 |

（本任务无包安装，不涉及 Package Legitimacy Gate。）
</threat_model>

<verification>
1. Task 1 的 RED 验证：新测试失败且失败消息精确命中 "stale WordComplete drove a transition"（失败形态为断言失败，非编译错误/挂死）。
2. Task 2 的 GREEN 验证链全部零退出：定向新测试 → `go test ./... -count=1` → `go vet ./...` → `go test -race ./pkg/session/ -count=1`。
3. `git log --oneline -1` 为以 fix(session) 开头、含 260823-sdx 的单个原子提交；`git show --name-only` 恰含三个 pkg/session 文件，无其他文件混入。
4. `git diff HEAD~1 -- pkg/session/fsm.go` 为空（FSM 零改动）。
</verification>

<success_criteria>
- 过期 WordComplete（代数 ≠ 当前值）在事件循环层被丢弃，repeat/next_word 交错不再跳词（中间状态断言为证）
- 当前代数 WordComplete 行为不变：pkg/session 全部既有测试零回归
- 无新增锁/atomic，streamGen 单 goroutine 读写 + 按值捕获；-race 干净；历史死锁模式不复发
- 一个中文原子提交（260823-sdx），恰好覆盖 types.go / session.go / session_test.go
</success_criteria>

<output>
Create `.planning/quick/260823-sdx-session-go-wordcomplete-stream-generatio/260823-sdx-SUMMARY.md` when done
</output>
