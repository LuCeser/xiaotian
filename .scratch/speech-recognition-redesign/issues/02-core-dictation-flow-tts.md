# 02 — Core Dictation Flow with Xiaomi Mimo TTS

**What to build:** Students can hear words read aloud one by one using Xiaomi Mimo TTS, with adaptive timing (8-15 seconds based on word difficulty) and basic button controls (Next Word, Repeat Word, Pause/Resume). This implements the traditional teacher-student dictation model at the core.

**Blocked by:** 01 — Xiaomi Mimo Service Integration Layer

**Status:** done (2026-08-23, commits: be653a4, 3a1fc59, 799de7c, f8e5e31, d6c046b, 45435bb)

- [x] Student can request dictation session and hear words read via Xiaomi Mimo TTS — 取舍备注：会话级流式 TTS 管线经 fakeStreamer 测试验证（StreamCalls 断言）；真实 iPad 音频播放待 UAT 实测
- [x] System provides adaptive writing time (8-15 seconds) based on word length and complexity
- [x] Basic button controls work: Next Word, Repeat Word, Pause/Resume — 取舍备注：后端 handlePause/handleResume/handleRepeat 与前端 DictationControls（60px+ 触控目标）经自动化验证；真实触控交互待 UAT
- [x] Session state machine handles LISTENING → DICTATING → PAUSED transitions
- [ ] Audio quality and timing meet <1.5s latency requirement — 取舍备注：未测量，需真实 iPad 端到端实测，留待 UAT（流式 TTS 与帧下发路径已就绪，无已知系统性阻塞）
- [x] Traditional dictation rhythm preserved: one word at a time with natural pauses
- [x] System maintains existing WebSocket protocol and session management patterns

## Completion note

- 验证方式：`go test ./pkg/session/ ./cmd/service/ -count=1`
- 2026-08-23 实测：两包全绿——ok github.com/xiaotian/home-tutor-agent/pkg/session 1.498s、ok github.com/xiaotian/home-tutor-agent/cmd/service 2.268s（-v 复核共 45 个测试函数全部 PASS、0 失败）；其中 timing 12 个表驱动用例（TestAdaptiveTimeout 覆盖 0-20 字词）、fsm 11 个测试（pause/resume、repeat、PAUSED 态 next_word/最后一词/非法事件、prevState 追踪）、session 集成含 fakeStreamer StreamCalls 断言（流式 TTS 管线在会话层打通）
- 双轴 code review（Standards + Spec）发现 5 项问题已全部修复（快速任务 260823-sdx/t13/ti8/tyq/u8t）
- 相关提交：be653a4（实现：AdaptiveTimeout + FSM pause/resume/repeat + DictationControls）、3a1fc59（#1/5 repeat 竞态跳词——按流代数丢弃过期 WordComplete 事件）、799de7c（#2/5 PAUSED 超时提取到 SessionTimeout.PausedStateTimeout，默认 5 分钟）、f8e5e31（#3/5 文档化 onTransition 无锁读 repeatPending/prevState 的单写者不变量）、d6c046b（#4/5 补全 DictationControls 六个方法 JSDoc）、45435bb（#5/5 DictationControls 触控高度对齐 iOS HIG 60px、TestPrevState_Tracked 补 transition 错误检查、_applyPaused 补 payload 防护、sendPausedFrame godoc 补 Sequence TODO 说明）
- 遗留 UAT：<1.5s 延迟实测与真实 iPad 音频播放/触控交互（验收第 5 条未勾选，说明见该条行内备注）
