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

**Next:** 本地开发机验收 Phase 13 与 13.1，发现问题先修复；VPS 部署拆为独立 Phase 13.2，等待本地人工通过后再规划和启动。日期检查仍如实记录，不强行进入部署。

## Requirements

### Validated

- ✓ **家长端成果可读与家庭首页（RT-09 成果可读部分）** — Phase 13.3（两个只读接口＋学习记录 tab＋诚实保存回执四态；真机 UAT 10/10、验证 7/7；家长确认/覆盖评分仍留 Phase 15）

### Active

- [ ] **浏览器自然实时家教（产品验收 RT-01~RT-10）** - R0 真机最小闭环：FSRS 到期词（上限 10）→服务端状态机报词/确认或 60 秒推进→四类口令→完结学习记录；采集→VAD→MiMo ASR/LLM 意图识别/TTS→流式播放→可打断，含幂等推进与真实写入
- [ ] **自研 RTC 层** - 先 WebSocket 成环、后 WebRTC（Go pion），传输层可替换接口
- [ ] **词库管理与 FSRS** - 已交付组件（词库/单元、FSRS 选词与参数更新）；R0 接入到期取词与课末 review，R2 再接批改、家长确认和 durable 恢复闭环
- [ ] **家长反馈** - R2 交付（吸收旧 Phase 10 的 FEED-01~04：批改确认/覆盖与进度统计）
- [ ] **iPad Safari 真机 + 业主证书 HTTPS** - 独立 Phase 13.2，在 Phase 13/13.1 本地人工验收通过后部署 VPS 并完成正式复验（RT-01）；当前不部署
- [ ] **资源受限部署** - 1C 1G VPS 上高效运行（150MB 限定口径见 Constraints）
- [ ] **按钮听写页（RT-10 降级模式）** - 功能冻结，只修阻塞 bug；作为备用按钮路径保留，不再作为产品主交互演进
- [ ] **音频时延（RT-04 口径）** - 以「首个实际可听响应」衡量：授权完成至首次可听问候 ≤3 秒；句末至首个实际可听响应 P50≤1.5 秒、P95≤2.5 秒
- [ ] **复用既有 Python 资产** - vocab_manager.py / pregenerate_dictation.py 经 subprocess 桥继续服务词库与 FSRS 业务

### Out of Scope

- **Photo OCR（家长确认/批改闭环）** — 2026-10-06 决策修订：课堂必经的检查阶段（拍照 OCR＋人工校正，三段式课堂）提前至 Phase 13.1；家长确认/覆盖评分与恢复仍留 Phase 15。
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
| 家庭入口无门禁＋三 tab 家长页（Phase 13.3） | 防不方便≠防孩子（fusion-consensus 决策 2）；学习记录默认 tab 承载诚实四态保存回执 | Delivered（Phase 13.3，真机验收 10/10） |
| 诚实保存回执：回执≠FSRS 写（Phase 13.3） | 「已提交」≠「已写入」；四态（pending/saved/failed/无行=unknown）＋回执元数据失败降级不阻塞可读性（WR-01） | Delivered（Phase 13.3，崩溃窗口真机复现验证） |

## Evolution

**2026-10-11 — Phase 13.3（家长端融合）complete。** 家庭首页两扇门＋parent.html 三 tab（学习记录默认）＋两个只读成果接口＋诚实保存回执四态全部落地并真机验收（UAT 10/10、VERIFICATION 7/7、SECURITY 22 项威胁全闭环、VALIDATION 0 缺口、UI 审计 20/24）。RT-09 成果可读部分入 Validated。用户方向登记：UAT 验收项应尽量端到端自动化（ROADMAP Backlog 999.2，并入四层 E2E 的 L3 主战场）。G-13.1-local-4（再练一组）在 13.3 真机 UAT-5 中全链路通过，建议重测关闭。下一步：Phase 13/13.1 本地验收收口。

**2026-10-11 — 用户方向登记（使用中问题收集）。** 用户使用中收集到的问题已记录于 [docs/qa-issues-2026-10-11.md](../docs/qa-issues-2026-10-11.md)（学科混默、口语确认不推进、默写推进反应慢），待逐个深入讨论后分类为 bug 或优化点并生成规格/Ticket；下一步计划「先本机 Windows 作为使用服务器（暂缓远端 VPS/Linux）：稳定分发包、本地便捷启动、正常导入词汇、走正常更新流程，使用稳定后再考虑远端 Linux」已登记 ROADMAP Backlog 999.3。

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
*Last updated: 2026-10-11 — Phase 13.3 完结演进（RT-09 成果可读入 Validated；家庭入口/回执决策入 Key Decisions）*
