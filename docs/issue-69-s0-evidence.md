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
| 1 | P1 / S2 / T2 | `subject.go:164` takeSubjectAsk 锁内消费消息；`:205-208` finalizeSubjectAsk 二次加锁读共享 `pendingAskInstallGen`；预留 `turn.go:676/690` | A 语音 goroutine 在两半之间暂停；读循环处理 B 的 auto init 覆盖该字段；恢复 A | A 不借 B 的 gen 安装（无迟到 ready）；B 的预留不被清；B 能完成初始化 | generation 获取恢复为续接读共享字段 | `CGO_ENABLED=1 go test -race -count=1 ./pkg/rt -run '<S2 新用例>'` | 待修 |
| 2 | P1 / S3 / T1(关闭半边) | `turn.go:920` gen 检查与 `:927` tryTerminalClose 分离；`claimCloseLocked`（`:1364`）只查 closed/可选 epoch。同类：`check.go:751/997`（S5/S6） | A 过 920 检查后、927 关闭前暂停；B 安装并完成自然开场；恢复 A | B 保持活动；连接不提前结束；连接主动关闭仍有效；事件单次 | 只撤 claim 内 scope 校验 | `CGO_ENABLED=1 go test -race -count=1 ./pkg/rt -run '<S3 新用例>'` | 待修 |
| 3 | P1 / S1 / §5.1 | 测试侧：`speech_browser_test.go` TestPassageSubmitBrowser/close seals：driver 断言 `:2637-2640`、读库 Fatal `:2652-2661` 均早于 `fallbackHold.release()`（`:2663`） | 兜底停在 uncheckedBefore（`:1043-1045`）持点不放，触发断言失败 | 失败须先释放→等任务与 driver→回收→再断言；goroutine 内 Fatal 不算红 | 测试协议证明：人为制造断言失败验证先回收 | `go test -tags=rtbrowser -count=1 ./pkg/rt -run 'TestPassageSubmitBrowser/close_seals_before_the_submit'` | 待修 |
| 4 | P1 / S1 / T8,T9 | 测试观察：`speechServerTasksSettled`（`speech_browser_test.go:969`）依赖 `latchInFlight`（`:973`，tracker 在门内层，兜底停门上未计入）、`checkWrites`、`activeScope.formalSubmit`（`:982`）——兜底三者皆不在 | 连接退出（driver 已返回）后兜底仍持点；轮询 settled | settled 须为 false；释放后须观察到兜底整个任务退出（taskDone：结果/事件/清理） | 完成发布提前到 CompleteCheck 返回；观察缩回活动 scope | `go test -tags=rtbrowser -count=1 ./pkg/rt -run 'TestPassageSubmitBrowser'` | 待修（R2 settled 须替换） |
| 5 | P2 / S1 / T4 | 测试观察：`check_submit_inflight_test.go:68` ttFormalSubmitSettled（returned≥calls＋activeScope.formalSubmit==nil）——漏被替换课堂任务与未进执行器的新任务 | 撤去重下第二条 goroutine 未执行即清槽；或 A 换课后 A 任务在途 | 逐任务等待：启动前登记、关闭决策预留句柄；全部退出后才断言 | 删 scope 去重（M4 线索）；观察缩回活动 scope | `CGO_ENABLED=1 go test -race -count=1 ./pkg/rt -run 'TestCheckSubmitInFlightBlocksSameConnectionRetry|TestFormalCheckSubmitFreezesWordListAtAdmission|TestHoldFirstCompleteCheckProtocol'` | 待修（R2 部分吸收） |
| 6 | P2 / S4 / T1,T2 | 生产已修：`classroom_scope.go:71-78` interrupt_ack 先行；检查恢复安装后 epoch。测试缺口：`classroom_scope_init_test.go` 全用 captureSender；T1 现版 `:177-186` 复用唯一 fake 行、`:187` 后未完成 B 自然开场；`ttAckLast` 于 `:250/:312` | 真实 Handler/writer/生产页面/播放器；A、B 独立真实课堂行；自然 ACK；同 lessonID 重入另设 | A 保存归属正确；B 状态/播放/连接不受污染；换课停旧音频且播放器自然 ACK B；迟到初始化无输出 | 撤 epoch 通知；撤回写 scope 校验；撤初始化 gen 校验；重放 version=-6900/checking=true | `go test -tags=rtbrowser -count=1 ./pkg/rt -run '<S4 新用例>'` | 生产已修、验收待证明 |
| 7 | P2 / S4,S8+§六 / §5.3 | 事实：`speech_browser_test.go`/`speech_real_save_test.go` 带 `//go:build rtbrowser`，而 r0fix1/r1fix1 报告命令未带标签（主要用例未随报运行）；R0:35 承认变异可能仍绿；R1:62 ACK 变异描述与测试不符 | —— | 浏览器命令必须带标签并证明用例实际运行（含子用例，Skip 不计）；每项修复交固定版本/变异 diff/交错/回收/失败断言/恢复绿灯/race | —— | `go test -tags=rtbrowser -count=1 -v ./pkg/rt -run '<用例>'` ＋ `CGO_ENABLED=1 go test -race -count=1 ./pkg/rt` | 待证明（贯穿） |

## R2 吸收与遗留线索

- 保留：3a1ea63 原子接纳与 scope 在途方向；442d734 T5 单元用例。
- 改造：op 须明确持有所属 scope（S5）；settled 观察须替换为逐任务完成信号（S1，覆盖 #4/#5）。
- 待复验/待关闭：M2（scopeGen 锁外捕获仍绿，T3 证明 S5 关）；M1/M4（S5 复跑）；cmd/service 两项既有失败（S8 与 bfafb40 同环境比较）。
- 同 lessonID 重入超时：按可恢复初始化错误语义，S7 实现。
