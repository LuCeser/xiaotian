# 07 — Multi-User Session Isolation and Scaling

**What to build:** System can handle multiple concurrent student sessions with proper resource isolation, preparing for classroom deployment beyond the initial single-user deployment.

**Blocked by:** 06 — Automatic Grading and Detailed Analysis Reports

**Status:** implemented (2026-09-10, commits: 见 git log 'ticket 07'; review pending)

- [x] Multiple students can have active dictation sessions simultaneously — 会话按 connID（*ws.Client 指针）在 Manager 注册表隔离，天然多连接并发；同一 category 两名学生各拿互斥词组（fetch-lock 卡片级排他，词汇池足够时天然分流）；50 并发负载测试钉死（TestLoad_50ConcurrentSessions：全部到 FEEDBACK、注册表清空、42ms）
- [x] Session data properly isolated between users with proper data boundaries — 核心隔离 bug 修复：会话退出原本按 category 全量解锁，会把其他学生进行中的锁一并打掉（第三个会话可能拿到第二名学生正在听写的词）——改为按卡片 ID 范围解锁（cmd_unlock --ids / UnlockOpts.IDs 全链路；NewSession 捕获锁定的卡片 ID，D-10 退出与 TTS 失败两条路径都范围释放；真实 vocab_manager.py+SQLite 冒烟验证）；语音/拍照 buffer 按 connID 隔离（既有）；审计行按 connection_id 归属
- [x] Memory usage stays within 150MB Go service budget under expected load — 50 并发负载后 HeapAlloc 1.9MB（测试内松护栏 100MB，附日志）；既有保障延续：MemoryMonitor 150MB 负载脱落（LoadShedAt 95%）、TTS 缓存 10min 清理、seqCounters 会话退出即清
- [x] Resource pooling and cleanup prevent memory leaks and resource exhaustion — 负载测试逐会话断言 ClearSeqCounter 调用数=会话数（Pitfall 5 在并发下的泄漏护栏）；注册表 onExit 全退出路径清空（断言 drain=0）；WorkerPool/pybridge 信号量（D-02）+ pregenSem cap=1 既有
- [x] Performance testing validates 50 concurrent student capacity — TestLoad_50ConcurrentSessions：真实 Manager+FSM+sweeper（FSRS/TTS/streamer 为假件），每会话 IDLE→DICTATING(词1)→DICTATING(词2)→WAITING_PARENT→FEEDBACK→退出；断言完成率、清空、计数器、审计行去重、耗时、堆
- [x] Session isolation extends to database, WebSocket connections, and service integrations — 数据库：Go 拥有 sessions/session_events（写分离 ADR 不变），新 batch_graded 事件走 SessionRepository；WS：每连接独立 *ws.Client/connID，Hub 容量 100（D-04 拒绝）；服务集成：Mimo 客户端共享但调用无会话粘性；FSRS：范围解锁（本票核心）
- [x] Graceful degradation when system approaches capacity limits — SessionConfig.MaxConcurrent（默认 100）此前从未被检查：Manager.NewSession 现在在触碰 FSRS 锁/TTS 之前用 ErrManagerAtCapacity 拒绝（槽位经 onExit 自动释放，测试钉死），handleStartSession 映射为友好提示「现在学习的人有点多，请稍等一分钟再试」；上游既有：Hub 满拒连（D-04）、内存脱落（D-18）

## Completion note

- 验证方式：`go test ./... -count=1` 全 12 包 ok + Python 冒烟（scoped/sweep/坏输入/空 ids 四类）+ 双测试层容量用例（pkg/session Manager 级 + cmd/service 双连接 handler 级）
- 架构备注：范围解锁的意义——fetch-lock 是卡片级（due 只排除已锁卡），两名学生同 category 并发听写本就成立（各拿不同词组）；但原解锁是 category 级 UPDATE，先完成的学生会清掉后进学生的锁。--ids 解锁为增量能力，category sweep 保留（测试工具/管理员路径）；Go 侧 IDs 优先于 category/unit
- 容量语义：MaxConcurrent 与 HubLimit(100) 对齐（D-04 parity）；拒绝发生在任何资源获取之前（先到先得，无队列——教室场景重试提示足够）
- 范围取舍：负载测试为 Go 侧形状（FSRS/TTS 假件）——真实 pybridge/音频的端到端压测归 ticket 08（E2E）；不做连接排队或会话优先级
- 已知边界：两名学生并发时词汇池需 ≥ 两人词数之和，否则第二人 ErrNoDueWords（FSRS 固有设计：同一张卡不给两人同时听写）；教室规模（>2 学生同 category）依赖词池规模
- 遗留 UAT：真实 iPad×2 双设备并发全流程（互相不串词、先后退出后词池恢复正常）；1C/1G VPS 上 50 连接真实压测（内存/延迟达标）
