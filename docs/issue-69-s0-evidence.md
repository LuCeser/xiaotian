# Issue #69 rethink3 路径 B：S0 固定基准与七项证据清单

> 钉在 `442d734`（分支 `fix/issue-56-pathB-steps5plus`，推送基准 `bfafb40`，本地 R2 为 `3a1ea63`+`442d734`，线性、ahead 2）。
> 行号均为 442d734；S1+ 每步完成后更新「状态」列。完整分析与行号漂移对照见 `/workspace/xiaotian-plans/issue-69-s0-report.md`。
> 来源：第三轮熔断审查（codex-review-69r01fix.md，4 P1 + 3 P2）、rethink3 计划 §五 S0。

## 基准要点

- `3a1ea63`（feat）：正式 `check_submit` 单次 `tm.mu` 临界区接纳（`pkg/rt/check.go:599-668`）；`classroomScope.formalSubmit`+`formalCheckSubmitOp`+按 op 清槽（`pkg/rt/classroom_scope.go:9-39`）；删连接级 `formalCheckSubmitPending`（turn.go −7 行，其后行号整体上移 7）。
- `442d734`（test）：T5 槽位单元用例；`ttFormalSubmitSettled` 与浏览器 settled 改读 `activeScope.formalSubmit`（该观察按计划须在 S1 替换为逐任务完成信号）。
- 已确认成立、不得回退的生产修复：安装临界区内 interrupt_ack 先于 session_ready（`classroom_scope.go:71-78`）、普通开场/无词结尾捕获安装后 epoch/cancel、检查恢复用安装后 epoch、hold 幂等 release。

## 七项清单（生产入口 — 确定性交错 — 业务断言 — 对应变异 — 运行命令）

| # | 级别/归属 | 生产入口（442d734） | 确定性交错 | 业务断言 | 对应变异 | 运行命令 | 状态 |
|---|---|---|---|---|---|---|---|
| 1 | P1 / S2 / T2 | `subject.go:164` takeSubjectAsk 锁内消费消息；`:205-208` finalizeSubjectAsk 二次加锁读共享 `pendingAskInstallGen`；预留 `turn.go:676/690` | A 语音 goroutine 在两半之间暂停；读循环处理 B 的 auto init 覆盖该字段；恢复 A | A 不借 B 的 gen 安装（无迟到 ready）；B 的预留不被清；B 能完成初始化 | generation 获取恢复为续接读共享字段 | `CGO_ENABLED=1 go test -race -count=1 ./pkg/rt -run 'TestLate.*Binding'` | **S2 已修**（65084b1/ac6727c：classroomInitRequest 一次临界区取走消息+学科+generation；迟到 epoch 通知现值门、行权威镜像准入门、加练分支同源；M1–M4 变异红） |
| 2 | P1 / S3 / T1(关闭半边) | `tryTerminalCloseForScope` + `claimCloseLocked` gateScope（`turn.go`）；课堂任务终态关闭入口；`tryTerminalCloseAt` 仍带 epoch。同类：`check.go` 重复提交/更正终态 | A completed 在 scoped 关闭前暂停；B 安装并完成自然开场；恢复 A | B 保持活动；连接不提前结束；客户端 goodbye 仍关闭；事件单次 | 只撤 claim 内 scope 校验 | `CGO_ENABLED=1 go test -race -count=1 ./pkg/rt -run 'TestStaleCompletedCloseCannotEndReplacementClassroom|TestClientGoodbyeStillClosesLiveClassroom'`；浏览器：`go test -tags=rtbrowser -count=1 ./pkg/rt -run TestStaleCompletedCloseBrowser` | **S3 已修 + P2-8 浏览器**（9dc6bc2/32c878e 单元；684c57c Case X 真实页面/自然 ACK/ B 自提交；M-scope 变异红见 issue-69-p1fix1-report.md） |
| 3 | P1 / S1 / §5.1 | 测试侧：`speech_browser_test.go` TestPassageSubmitBrowser/close seals：driver 断言 `:2637-2640`、读库 Fatal `:2652-2661` 均早于 `fallbackHold.release()`（`:2663`） | 兜底停在 uncheckedBefore（`:1043-1045`）持点不放，触发断言失败 | 失败须先释放→等任务与 driver→回收→再断言；goroutine 内 Fatal 不算红 | 测试协议证明：人为制造断言失败验证先回收 | `go test -tags=rtbrowser -count=1 ./pkg/rt -run 'TestPassageSubmitBrowser/close_seals_before_the_submit'` | **S1 已修**（320419a：三处 close-seals 重排为记录→释放→等待→断言；reclaim 顺序协议 M-C 变异红） |
| 4 | P1 / S1 / T8,T9 | 测试观察：`speechServerTasksSettled`（`speech_browser_test.go:969`）依赖 `latchInFlight`（`:973`，tracker 在门内层，兜底停门上未计入）、`checkWrites`、`activeScope.formalSubmit`（`:982`）——兜底三者皆不在 | 连接退出（driver 已返回）后兜底仍持点；轮询 settled | settled 须为 false；释放后须观察到兜底整个任务退出（taskDone：结果/事件/清理） | 完成发布提前到 CompleteCheck 返回；观察缩回活动 scope | `go test -tags=rtbrowser -count=1 ./pkg/rt -run 'TestPassageSubmitBrowser'` | **S1 已修**（7ed3174：settled 改读 task_watch 整任务观察，兜底句柄在 claim 预留；M-A/M-B 变异红；passage close-seals 增「兜底停车时 settled 必为 false」断言） |
| 5 | P2 / S1 / T4 | 测试观察：`check_submit_inflight_test.go:68` ttFormalSubmitSettled（returned≥calls＋activeScope.formalSubmit==nil）——漏被替换课堂任务与未进执行器的新任务 | 撤去重下第二条 goroutine 未执行即清槽；或 A 换课后 A 任务在途 | 逐任务等待：启动前登记、关闭决策预留句柄；全部退出后才断言 | 删 scope 去重（M4 线索）；观察缩回活动 scope | `CGO_ENABLED=1 go test -race -count=1 ./pkg/rt -run 'TestCheckSubmitInFlightBlocksSameConnectionRetry|TestFormalCheckSubmitFreezesWordListAtAdmission|TestHoldFirstCompleteCheckProtocol'` | **S1 已修**（7ed3174/320419a：ttFormalSubmitSettled 改整任务观察；TestReplacedClassroomTasksWaitedIndividually 逐任务等待，M-B 变异红；M4 去重变异复验仍留 S5） |
| 6 | P2 / S4 / T1,T2 | 生产已修：`classroom_scope.go:71-78` interrupt_ack 先行；检查恢复安装后 epoch。浏览器验收：`classroom_switch_browser_test.go` 六用例 + `driver.js` 轮次登记；单元补 `TestSameLessonReinitDuringInflightSaveRefused` | 真实 Handler/writer/生产页面/播放器；A、B 独立真实课堂行；自然 ACK；同 lessonID 重入另设 | A 保存归属正确；B 状态/播放/连接不受污染；换课停旧音频且播放器自然 ACK B；迟到初始化无输出 | 撤 epoch 通知；撤回写 scope 校验；撤 initialization gen 校验；重放 version=-6900/checking=true | `GOTOOLCHAIN=go1.26.9 go test -tags=rtbrowser -count=1 ./pkg/rt -run 'TestClassroom|TestLateAsk|TestCheckResume|TestSameLessonReinitDuringSubmit'` | **S4 已修**（a3e3637 生产门 + 3932eca 浏览器证明；M-epoch/M-scope/M-gen/M-6900 变异红，见 issue-69-s4-report.md） |
| 7 | P2 / S4,S8+§六 / §5.3 | 事实：`speech_browser_test.go`/`speech_real_save_test.go` 带 `//go:build rtbrowser`，而 r0fix1/r1fix1 报告命令未带标签（主要用例未随报运行）；R0:35 承认变异可能仍绿；R1:62 ACK 变异描述与测试不符 | —— | 浏览器命令必须带标签并证明用例实际运行（含子用例，Skip 不计）；每项修复交固定版本/变异 diff/交错/回收/失败断言/恢复绿灯/race | —— | `go test -tags=rtbrowser -count=1 -v ./pkg/rt -run '<用例>'` ＋ `CGO_ENABLED=1 go test -race -count=1 ./pkg/rt` | **S4 阶段**：S4 报告已带 `-tags=rtbrowser` 全量 `./pkg/rt/...`（291s）；S8 仍须 CI/全仓 lint 与 cmd/service 基线对照 |

## R2 吸收与遗留线索

- 保留：3a1ea63 原子接纳与 scope 在途方向；442d734 T5 单元用例。
- 改造：op 须明确持有所属 scope（S5）；settled 观察须替换为逐任务完成信号（**S1 已完成**：`pkg/rt/task_watch.go` 整任务观察，覆盖 #4/#5）。
- 待复验/待关闭：M2（scopeGen 锁外捕获仍绿，T3 证明 S5 关）；M1/M4（S5 复跑）；cmd/service 两项既有失败（S8 与 bfafb40 同环境比较）。
- 同 lessonID 重入超时：按可恢复初始化错误语义，S7 实现。

## S1 完成记录（钉在 320419a）

- 整任务观察设施：`pkg/rt/task_watch.go`（serverTaskTracker/Handle，测试 seam，生产 nil no-op）；句柄在 goroutine 启动前登记（兜底在 claim 临界区内随关闭决策预留、锁外启动），只在整任务结束（执行器/FSRS/回执/事件尾部之后）释放。
- 覆盖启动点：正式提交、更正/古诗/短文/加练接纳保存、检查恢复/无词/开场 starter、选科/古诗/短文开场播报、拍照检查、两处关闭兜底（handleEndSession / Close）。
- 观察替换：`ttFormalSubmitSettled` 与浏览器 `speechServerTasksSettled` 不再读 `activeScope.formalSubmit` / `checkWrites`。
- 协议证明（去掉修复就红，均为测试设施协议、非生产业务证明）：M-A 完成发布提前到 CompleteCheck 返回 → fsfs/receipt/summary 三个尾部 park 红「尚未结束却已报告完成」；M-B 观察缩回活动 scope → 「遗漏 A」＋两兜底入口红；M-C 撤掉回收等待 → 「断言时点早于真实退出」红。
- 未做（按计划留待后续步骤）：S2 初始化身份完整传递、S3 关闭 claim 的 scope 校验、S4 真实浏览器验收、M4 去重变异复验（S5）、processTurn/read_word 等会话回合任务未纳入观察（不在 S1 命名范围）。

## S2 完成记录（钉在 65084b1/ac6727c）

- 初始化身份成为完整请求对象 `classroomInitRequest{category, msg, installGen}`：选科临界区一次取走消息＋学科＋预留 generation（subject.go takeSubjectAsk），续接（finalize→continueSessionInit→各 starter）只携带局部对象，不再读共享身份字段；`pendingAskInstallGen` 删除。
- 迟到续接防扰 B：interrupt_ack 仅在 epoch 迁移仍是当前值时发送（rt-playback onEpochBump 盲采 new_epoch，过期对会把客户端 epoch 拉回并停播）；既有课堂行权威学科镜像改为 `classroomInstallAllowed(req.installGen)` 准入门控；加练分支（handlePracticeInit）改由请求对象取学科与 generation。
- 测试（subject_request_test.go，四条确定性交错，-race）：迟到绑定不借 B 预约且 B 完成（验收原文）；迟到 epoch 通知跳过；加练学科同源；行权威不改活课堂学科。按 id 存行的 ttPerIDExecutor 修正 fake 单行串课。
- 变异证明（去掉修复就红，业务断言、非超时）：M1 generation 恢复续接读共享字段→四用例红（迟到 ready/二 ack/借安装/学科改写）；M2 学科恢复读共享字段→fetch=auto、行 id 错、加练 auto 红；M3 撤 ack 现值门→二 ack 红；M4 撤行权威门→共享学科=chinese 红。
- 顺带（d416a21）：S1 starterInterleave seam 的负载竞态（基线 d5915cd 全量 -race 同样报）——runStarterTask 快照加 mu、S1 用例挂载同锁；测试设施修复，非生产行为。
- 未做（留后续步骤）：S4 真实浏览器验收、迟到续接 IO 早退/错误出口的请求门（failLessonInit、practice 降级 sendError 仍为会话门，归 S5/S6 结果消息身份）、同 lessonID 重入（S7）。

## S3 完成记录（钉在 32c878e）

- 统一关闭 claim 新增 `gateScope`/`scopeGen`：资格判断与密封、快照、closed 标记仍在同一 `claimCloseLocked` 临界区；连接 goodbye/transport 关闭保持 scope-free。
- 课堂任务终态入口 `tryTerminalCloseForScope`：`startCompletedSession`、正式/更正重复提交终态；播报有效性路径仍用 `tryTerminalCloseAt(epoch)`。
- `completedCloseInterleave` 测试 seam + 已完成重连在 scoped 关闭前释放 per-lesson `lockLessonInit`，使同连接换课交错可确定性复现。
- 测试 `close_scope_claim_test.go`（-race）：过期 completed 关闭不得结束 B（pong 仍响应）；客户端 goodbye 仍单次关闭。M1 撤 claim scope 门→「session_ended after A's stale close = 1, want 0 — the superseded scope must not close B's live classroom」。
- 终态入队失败单次语义：沿用 `TestCloseClaim`（`close_claim_test.go`），本步未改行为。
- 未做：S4+、同 lessonID 重入（S7）。

## S4 完成记录（钉在 3932eca，生产修复 a3e3637）

- 生产：`checkWriteAdmission` 携带 lessonID；`continueSessionInit` 在既有行仍有在途接纳保存时返回可恢复 `ErrCodeLessonUnavailable`（不装旧状态课堂）。单元 `TestSameLessonReinitDuringInflightSaveRefused`。
- 浏览器：`classroom_switch_browser_test.go` 六用例（Case V/S/E/G/R/L）+ **Case X** `TestStaleCompletedCloseBrowser`（P2-8 陈旧 completed 关闭 vs B 自然开场，684c57c）经真实 `/rt`、connWriter、`/tutor/` 页面与播放器；换课由 Go 侧 `HandleControl(session_init)` 注入同连接 writer；`driver.js` 轮次/`playedBeforeAck` 登记，不用消息数量代替播放器行为。
- 变异（去掉修复就红，业务断言）：撤 install `interrupt_ack` → `secondRoundVerdict = "rejected"`；撤 `scopeWritebackAllowedLocked` → B tutor 被 A 写回污染；撤 `classroomInstallAllowed` gen 门 → `session_ready count = 2`；`version=-6900/checking=true` 重放 → writeback 断言失败（自然提交与跨课堂 Case S）。
- 未做（按计划）：S5+（含 R2 完整 T3、M2/M4 去重）、S7 persistDone 有界等待升级、S8 综合与 cmd/service 8080/8081 占用基线复验。
- **第一阶段 fix1**（Codex @29ae4bd 八项）：钉在 `684c57c`，报告 `/workspace/xiaotian-plans/issue-69-p1fix1-report.md`（含 P1-1..P2-8 提交与变异摘要）。
