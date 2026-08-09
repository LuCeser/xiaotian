---
status: complete
phase: 07-session-state-management
source: [07-01-SUMMARY.md, 07-02-SUMMARY.md, 07-03-SUMMARY.md]
started: 2026-08-09T12:00:00Z
updated: 2026-08-09T14:25:00Z
---

## Current Test
<!-- OVERWRITE each test - shows where we are -->

[testing complete]

## Tests

### 1. pkg/session 包骨架导出 (D1)
expected: FrameSender, EventType, SessionEvent, sentinel errors, Session, NewSession, transition(), Manager stub, audit 方法均导出可用
result: pass
source: automated
coverage_id: D1

### 2. FSM 转换表 (D2)
expected: transition() 覆盖所有用户/系统驱动的转换，拒绝无效对并返回包装后的 ErrInvalidTransition
result: pass
source: automated
coverage_id: D2

### 3. NewSession 获取 FSRS 锁 (D3)
expected: NewSession 通过 SelectDueCards 获取 fetch-lock，捕获 lockCategory/lockUnit，TTS 失败时通过非 defer 路径释放锁，空结果返回 ErrNoDueWords
result: pass
source: automated
coverage_id: D3

### 4. 会话超时配置 (D4)
expected: SessionTimeoutConfig 含 5 个 per-state 超时，接入 Config.Validate() 与 5 个 env 绑定（HTA_SESSIONTIMEOUT_*）
result: pass
source: automated
coverage_id: D4

### 5. Session.run() 事件循环 (D5)
expected: 事件循环经 FSM 处理事件，驱动 D-13 系统转换，超时/取消时退出
result: pass
source: automated
coverage_id: D5

### 6. D-10 退出 defer 链 (D6)
expected: 每条退出路径（正常/超时/取消）均触发 defer 链：UnlockCards + CleanupSession + hub.Unregister
result: pass
source: automated
coverage_id: D6

### 7. releaseFSRSLock 使用新鲜 context (D7)
expected: UnlockCards 收到鲜活 ctx，即使 run-ctx 已被取消（Pitfall 1 规避）
result: pass
source: automated
coverage_id: D7

### 8. 无评分提交不变量 (D8)
expected: 所有退出路径均不调用 UpdateRatings — 无部分进度提交
result: pass
source: automated
coverage_id: D8

### 9. Manager 清理器 (D9)
expected: 清理器取消泄漏会话（deadline > 2× state timeout），全生命周期无 goroutine 泄漏
result: pass
source: automated
coverage_id: D9

### 10. Hub 指标流入 ServerSnapshot (D10)
expected: Hub 原子计数器（totalAccepted/Rejected/Cleaned + Active）经 Stats() 与 SetConnectionStatsProvider 注入 ServerSnapshot.Connections，/metrics 契约不变
result: pass
source: automated
coverage_id: D10
verified_in_uat: TestHub_Stats + TestSnapshot_HubConnections + TestSnapshot_ConnectionStatsFallback 全绿

### 11. 审计经 WorkerPool.Submit 分发 (D11)
expected: DB 审计写入通过 pool.Submit 派发（首个真实 Submit 调用者，P1-3 解决），池满时 Warn+drop
result: pass
source: automated
coverage_id: D11
verified_in_uat: TestAuditViaWorkerPool + WorkerPoolFull 子测试全绿

### 12. 处理器委托到 Manager (D12)
expected: handleStartSession 委托 manager.NewSession；handlePlayWord 推送事件（D-13 Session 拥有 TTS，无直接 streamer.StreamWord 调用）
result: pass
source: automated
coverage_id: D12
verified_in_uat: go build ./... 通过；handlers.go 中 manager.NewSession=1、streamer.StreamWord=0

### 13. 端到端会话生命周期冒烟测试
expected: |
  准备 Python venv + TTS 模型，启动服务（go run ./cmd/service），打开 test-interface.html：
  1. 点击 start_session — 服务返回 session_ready，客户端收到单词列表，服务日志显示 IDLE 状态
  2. 点击 play_word — 服务日志显示状态流转 IDLE → LISTENING → DICTATING，客户端能听到单词朗读
  3. 任其超时 — 服务日志显示 FSRS 锁释放（UnlockCards 调用），会话正常退出，无 goroutine 泄漏
result: pass
note: |
  核心 expected 满足：start_session → session_ready ✓；hello 经 play_word 走 IDLE→LISTENING→DICTATING 并正常播放 ✓；
  超时后 TTS cleanup + hub unregister 正常退出 ✓。冒烟过程中发现并修复 2 个真实缺陷（见 Gaps G-07-13），
  接通了 next_word 多词顺序流转路径（07-03 handler rewiring 漏接）。book 音频秒播完为前端音频层独立问题，记 deferred。

## Summary

total: 13
passed: 13
issues: 0
pending: 0
skipped: 0
blocked: 0

## Gaps

- gap_id: G-07-13
  truth: "浏览器能建立 WebSocket 连接，会话状态机能多词顺序流转 IDLE→LISTENING→DICTATING→LISTENING..."
  status: resolved
  resolved_at: 2026-08-09T14:25:00Z
  resolved_by: "verify-work inline fix (pkg/ws/server.go CheckOrigin 端口容错 + cmd/service/handlers.go next_word 路由 + test-interface.html 首词 play_word/后续 next_word)"
  reason: "冒烟测试发现端到端会话流程无法启动，根因拆解为两个独立缺陷，均已就地修复。"
  severity: blocker
  test: 13
  root_cause: |
    两个独立缺陷：
    (a) CheckOrigin 端口处理：pkg/ws/server.go:144 精确匹配白名单，浏览器 Origin 带端口(http://localhost:8080)
        而白名单展开无端口(http://localhost)，所有浏览器连接被 403 拒绝。
    (b) next_word handler 路由缺失：FSM(fsm.go:69) 支持 DICTATING+next_word→LISTENING，但 07-03 handler
        rewiring 只接了 start_session/play_word，漏接 next_word，导致多词顺序播放路径断裂；测试界面只发
        play_word，第二词起撞 DICTATING 被拒（07-RESEARCH 明确 defer 的 optional 边界）。
  artifacts:
    - path: "pkg/ws/server.go"
      fix: "CheckOrigin 加 strip-port fallback（保留精确匹配优先）；更新 D-24 注释；新增 stripOriginPort 辅助函数"
    - path: "pkg/ws/server_test.go"
      fix: "新增 TestServerUpgraderCheckOriginWithPort（带端口 Origin 接受 + 非 whitelist host 仍拒绝）"
    - path: "cmd/service/handlers.go"
      fix: "新增 case \"next_word\" + handleNextWord，路由到 session EventControlFrame(ControlType:next_word)"
    - path: "test-interface.html"
      fix: "firstWordPlayed 标志：首词发 play_word(IDLE→LISTENING)，后续发 next_word(DICTATING→LISTENING)；session_ready 重置"
  missing: []
  verification: |
    go build ./... 通过；pkg/ws TestServerUpgraderCheckOrigin* 全绿；服务重启后浏览器 Origin http://localhost:8080
    不再 403；next_word book 服务端无 invalid transition，状态机多词流转正常。

## Deferred Follow-Ups

- test: 13
  idea: "book（及其它非首词）音频 92ms 秒播完 — 前端 HTMLAudioElement 收到 ~1 秒 PCM(50 frames) 却瞬间 onended，听不到声音。服务端无错、TTS 缓存有效(book.mp3 4653 bytes)、状态机正常。属前端 wav 组装/音频元素竞态（Phase 6 TTS / 前端音频层范畴），非会话状态机问题。"
  deferred_at: 2026-08-09T14:25:00Z
  suggested_phase: "Phase 06 TTS / Phase 07.1 iOS audio / Phase 08 frontend"
