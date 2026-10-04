# Home Tutor Agent

> **2026-09-30 产品验收重定基：** [ADR-0002](../docs/adr/0002-browser-realtime-tutor.md) 已转正 **Accepted**——自研 RTC 层（先 WebSocket 后 WebRTC/Go pion，传输可替换接口）＋ 小米 MiMo ASR/LLM/TTS 全家桶（每层可替换接口），附三条硬护栏与火山书面 Plan B。当前主线 = **R0–R3**（见 ROADMAP「R0–R3 当前主线」章节）；产品验收 = **RT-01~RT-10**（见 REQUIREMENTS「产品验收（最高优先级）」组）；旧 Phase 1–9 完成记录 = **组件证据**。最新事实见 [项目复盘](../docs/PROJECT-REVIEW-2026-09-30.md)。

## What This Is

A web-based real-time dictation tutor agent that provides natural, interactive learning experiences for elementary school students. The system uses conversational AI to deliver personalized vocabulary practice through voice dictation sessions, with parent feedback integration that drives FSRS (Free Spaced Repetition Scheduler) algorithm updates.

## Core Value

Students receive personalized, real-time dictation practice with immediate feedback while parents get detailed progress tracking and the ability to influence the learning algorithm through their reviews.

## Business Context

- **Customer**: Elementary school students and their parents
- **Revenue model**: Not yet defined (educational research phase)
- **Success metric**: Student engagement and vocabulary retention improvement
- **Strategy notes**: Phase 1 focuses on technical MVP; broader market strategy to be determined

## Current Milestone: v2.0 realtime-tutor

**Goal:** 浏览器自然实时家教，RT-01～RT-10 验收。

**Target features:** 自研交互＋MiMo 可替换接口，Phase 13～16（R0～R3）；先 WebSocket，Phase 16 替换 pion WebRTC，触发时间检查时切火山 Plan B。

**Next:** /gsd-execute-phase 13；日期及切换步骤见 ROADMAP 和 docs/REALTIME-TUTOR-PLAN-B.md。

## Requirements

### Validated

(None yet — ship to validate)

### Active

- [ ] **浏览器自然实时家教（产品验收 RT-01~RT-10）** - R0 真机最小闭环：FSRS 到期词（上限 10）→服务端状态机报词/确认或 60 秒推进→四类口令→完结学习记录；采集→VAD→MiMo ASR/LLM 意图识别/TTS→流式播放→可打断，含幂等推进与真实写入
- [ ] **自研 RTC 层** - 先 WebSocket 成环、后 WebRTC（Go pion），传输层可替换接口
- [ ] **词库管理与 FSRS** - 已交付组件（词库/单元、FSRS 选词与参数更新）；R0 接入到期取词与课末 review，R2 再接批改、家长确认和 durable 恢复闭环
- [ ] **家长反馈** - R2 交付（吸收旧 Phase 10 的 FEED-01~04：批改确认/覆盖与进度统计）
- [ ] **iPad Safari 真机 + 业主证书 HTTPS** - 业主现有证书部署 VPS，iPad 走真实网络验收（RT-01）
- [ ] **资源受限部署** - 1C 1G VPS 上高效运行（150MB 限定口径见 Constraints）
- [ ] **按钮听写页（RT-10 降级模式）** - 功能冻结，只修阻塞 bug；作为备用按钮路径保留，不再作为产品主交互演进
- [ ] **音频时延（RT-04 口径）** - 以「首个实际可听响应」衡量：授权完成至首次可听问候 ≤3 秒；句末至首个实际可听响应 P50≤1.5 秒、P95≤2.5 秒
- [ ] **复用既有 Python 资产** - vocab_manager.py / pregenerate_dictation.py 经 subprocess 桥继续服务词库与 FSRS 业务

### Out of Scope

- **Photo OCR** — 已有组件，Phase 15 接入学习闭环；Phase 13 不前置 OCR。
- **Digital avatar integration** — Planned for Phase 3, requires Phase 1 real-time foundation
- **Mobile native apps** — iPad web browser approach reduces development complexity
- **更多教学语言** — 本里程碑先验证中文交流和英语词汇教学；其他语言另行决定。
- **Advanced analytics dashboard** — Basic statistics for Phase 1, detailed analytics later

## Context

**Technical Environment:**
- Existing project located at `D:\workspace\ai\xiaotian`
- Existing reusable assets at `D:\workspace\ai\xiaotian-skills\shared\dictation\scripts\`
- vocab_manager.py (822 lines): FSRS algorithm + SQLite CLI
- pregenerate_dictation.py (1348 lines): Sophisticated TTS pipeline
- Target deployment: 1C 1G VPS (resource-constrained environment)

**Prior Work:**
- Extensive planning completed including architecture design, implementation strategy, and verification approach
- Technical stack research completed: Go WebSocket + Python microservices architecture
- WebSocket protocol designed with binary audio frames (640B for 20ms@16kHz PCM)
- Resource management strategy defined: 150MB memory budget for Go service, 100 concurrent sessions max

**User Research:**
- Current Hermes + WeChat gateway approach has 1-2 minute delays and poor interaction experience
- Target users: elementary school students requiring simple, engaging interface
- Parents need: vocabulary management, progress tracking, and feedback mechanisms

**Known Issues:**
- Current solution lacks real-time interaction capabilities
- WeChat restrictions prevent rich interactive experiences
- No foundation for future digital avatar integration
- Resource constraints require careful memory and connection management

## Technology Stack

- **Go** — 业务服务 + 自研传输层；WebRTC 阶段引入 pion
- **Python** — 既有 FSRS/词库 CLI，经 subprocess 桥复用
- **SQLite** — 词库、FSRS 与学习记录存储
- **小米 MiMo ASR + LLM + TTS** — 现有 key，每层可替换接口
- **传输** — WebSocket → WebRTC（可替换传输接口）
- **HTTPS** — 业主现有证书部署 VPS
- **目标设备** — iPad Safari 真实网络

## Constraints

- **Infrastructure**: 1C 1G VPS with strict memory limits (150MB for Go service, 50MB audio buffer pool)——150MB 仅适用于 Go 业务服务；自研 RTC/语音组件按实际组件评估，不沿用旧预算结论
- **Target Platform**: iPad web browser (Safari compatibility required)
- **Audio Technology**: 小米 MiMo ASR/LLM/TTS 全家桶（每层可替换接口）
- **Language Stack**: Go for WebSocket service, Python for existing business logic integration
- **Storage**: SQLite database (sufficient for current needs, avoids PostgreSQL overhead)
- **Latency Target**: RT-04 口径——授权完成至首次可听问候 ≤3 秒；句末至首个实际可听响应 P50≤1.5 秒、P95≤2.5 秒（以真实可听音频衡量）
- **传输**: 两步走 WebSocket → WebRTC（Go pion），传输层可替换接口
- **HTTPS/验收环境**: 业主现有证书部署 VPS，iPad 真实网络验收
- **Architecture**: Must support future digital avatar integration (Phase 3)
- **Concurrency**: Support 50-100 concurrent sessions within resource constraints（容量结论待 R3 按实测重估）

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| ADR-0002：自研 RTC 层（WS→WebRTC/pion）+ MiMo ASR/LLM/TTS 可替换接口 | 业主为领域工程师（练手目标，复杂度定价改变）+ 绿地自建≠旧协议修补 + 三条硬护栏与火山书面 Plan B | Accepted（2026-09-30），R0 spike 验证中 |
| Go WebSocket + Python microservices | Go provides efficient WebSocket handling with lower memory; Python reuse existing FSRS and TTS logic | Delivered（Phases 1–9 组件证据，业务层保留） |
| Full streaming TTS vs caching | Consistency and simplicity over complex caching strategies for Phase 1 | Superseded（实际交付预生成＋缓存；R0 重回真流式，按 RT-04 验收） |
| SQLite vs PostgreSQL | SQLite sufficient for current scale, avoids resource overhead on 1C 1G VPS | Delivered |
| Binary WebSocket protocol (640B frames) | Optimized for 20ms@16kHz PCM audio with proper sequence management | Delivered（功能冻结为 RT-10 降级模式） |
| Resource budgeting (150MB Go + 50MB audio) | Prevents memory overflow on 1C 1G VPS while supporting 50-100 concurrent sessions | Delivered（Phase 1；150MB 限定 Go 业务服务） |

## Evolution

**2026-09-30 — 正式关闭旧方案、启动 v2.0。** v1.0 为 superseded / override_closeout；Phase 10～12 不再等待恢复。Phase 9 两项人工验收未执行，结转 Phase 16。新阶段数字编号 13～16；第一周/第三周/第六周检查分别为 10-07/10-21/11-11，规划时间计入，切换不重置计时。

This document evolves at phase transitions and milestone boundaries.

**2026-09-30 — 产品验收重定基。** 复盘（[PROJECT-REVIEW-2026-09-30](../docs/PROJECT-REVIEW-2026-09-30.md)）+ 拷问讨论拍板：ADR-0002 转正 Accepted（自研 RTC + MiMo 可替换接口 + 三条硬护栏 + 火山书面 Plan B）；Phase 1–9 完成记录降为组件证据；主线 R0–R3；产品验收 = RT-01~RT-10。本文件 Active / Technology Stack / Constraints / Key Decisions 同步改写。

**2026-09-27 — Phase 08 (Student Web Interface) complete.** DICT-01 / WEB-02 / WEB-04 validated (goal-backward verification passed: 28/31 must-have truths verified, 2 device-audition deferrals, VPS rehearsal → Phase 12). New browser-E2E layer: Playwright (`make e2e` / `npm run e2e`, commit 599c2a4). Phases 8.1–8.6 were delivered directly as speech-recognition-redesign tickets 01–07; next actionable phase with pending work: **9 (Parent Vocabulary Management)**. Carried residuals: UAT-CHECKLIST A2/A4-sub/A5 (next iPad session), session-resume-across-reconnect → Phase 11, VPS deployment → Phase 12.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd-complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-10-04 — 同步 2026-10-03 状态机默写助手转向（FSRS 到期词、60 秒推进、LLM 仅意图/话术）*
