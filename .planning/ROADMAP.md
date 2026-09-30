# Roadmap: Home Tutor Agent

## v2.0 realtime-tutor — ACTIVE

目标：浏览器自然实时家教，按 RT-01～RT-10 验收。架构已由 [ADR-0002](../docs/adr/0002-browser-realtime-tutor.md) Accepted 锁定：自研实时交互，MiMo ASR/LLM/TTS 各自可替换，先 WebSocket、后 WebRTC（Go pion），已有 Go/Python/SQLite 业务复用。

**唯一下一项工作：Phase 13（R0）。** GSD 使用数字编号，R0～R3 是产品阶段名称，对应 13～16。旧阶段 1～12 已关闭归档；Phase 10～12 为 superseded，不是暂停等待恢复。旧计划完成只算组件证据。

## Phases

- [ ] **Phase 13: R0 真机最小闭环** — 3 词自然对话、读写工具、真实打断与时序证据
- [ ] **Phase 14: R1 对话接真实课堂** — 完整课堂工具、上下文、书写等待及幂等
- [ ] **Phase 15: R2 学习闭环** — 词库、提交批改、家长确认、FSRS 和恢复
- [ ] **Phase 16: R3 部署与容量** — pion WebRTC、全量真机验收、资源与费用

## Phase Details

### Phase 13: R0 真机最小闭环

**Goal**: 学生在真实 iPad 的 HTTPS 网页一次授权后，通过自然对话发起 3 词练习、追问、推进一词并打断老师；进度与工具结果一致。
**Depends on**: None
**Requirements**: RT-01, RT-02, RT-03, RT-04, RT-05, RT-07, RT-10
**Success Criteria** (what must be TRUE):

1. 独立自然家教入口经用户现有证书部署 VPS；真 iPad、真实网络访问。一次必要点击授权后至少 10 轮无需按住录音。
2. 新 WebSocket 交互传输与 MiMo ASR、LLM、TTS 各有独立替换接口；采集→VAD 切句→整句 ASR→LLM→流式 TTS→收到即播放，不等待整课或整句合成完成。先接 3 个固定词，不前置完整词库、OCR 或容量工程。
3. MiMo LLM 发起真实 get_lesson_state 读取，以及 advance_word 或等效写工具；服务端实际修改该课堂的状态。写入携带 lesson_id、request_id、expected_version，同一请求重试只推进一次，重复请求返回原执行结果，旧版本冲突可见。不能用提示词内置 JSON 或模型口头回应替代工具执行。
4. 学生开口能停止当前播放、清空旧排队音频、取消或丢弃旧轮次输出；下一轮以实际播放到的位置为上下文。要实测扬声器回声不会触发老师自我打断。
5. “刚才那个是什么意思”“慢一点”“还没写完”“不会写”有上下文响应；沉默写字不自动推进，求助不默认跳词。
6. 记录 RT-04/RT-05 全部实际播放时序；第一周成环与第六周延迟达标是不同检查。R0 可在闭环通过且测量已齐备后进入 R1，但未达标项仍保持未验收，第六周切换条件继续生效。
7. 故障原因可见、可结束释放会话；按钮备用明确标为降级，不计语音通过。

**Plans**: 0 plans (ready for planning)

Plans:
- [ ] TBD — /gsd-plan-phase 13，依据 [13-CONTEXT.md](phases/13-realtime-minimal-loop/13-CONTEXT.md) 编写实施计划

### Phase 14: R1 对话接真实课堂

**Goal**: 完整对话课堂支持 start/get/repeat/advance/pause/resume/hint/finish，先 3～5 词后扩大至 30 词；学生以自然交流控速，字幕、进度与口头反馈一致。
**Depends on**: Phase 13
**Requirements**: RT-03, RT-06, RT-07, RT-10
**Success Criteria** (what must be TRUE):

1. 至少 20 条含上下文场景正确响应，含发起、指代、问义、慢读、求助及等待书写。
2. 课堂状态独立于 listening/thinking/speaking；沉默不跳词，求助先提示或澄清。
3. 按钮和语音调用同一业务服务；工具重试、并发旧版本、打断中的写操作保持幂等，已提交结果不因取消语音生成而被误删。
4. 首词按需、短距预取不阻塞进入；无整课 TTS 生成后才开始的等待。

**Plans**: 0 plans

Plans:
- [ ] TBD — /gsd-plan-phase 14

### Phase 15: R2 学习闭环

**Goal**: 接真实词库、拍照或人工提交、批改、家长确认与 FSRS，并按课堂 ID 恢复同一节课。
**Depends on**: Phase 14
**Requirements**: RT-08, RT-09
**Success Criteria** (what must be TRUE):

1. 完成练习、提交、批改、家长确认/覆盖评分、写入结果及下一次 FSRS 选词；重复确认不重复更新。
2. 15 分钟真实练习可完成；切后台或断网有明确状态，恢复原课堂进度，不偷偷另建课造成跳词。
3. 吸收旧 Phase 10 家长反馈和旧 Phase 11 恢复意图；保留已有数据，不做与体验无直接关系的业务重写。

**Plans**: 0 plans

Plans:
- [ ] TBD — /gsd-plan-phase 15

### Phase 16: R3 部署与容量

**Goal**: 完成 pion WebRTC 传输替换、生产真机全量验收、单会话资源与费用测量，再决定并发和旧代码清理。
**Depends on**: Phase 15
**Requirements**: RT-01, RT-02, RT-03, RT-04, RT-05, RT-06, RT-07, RT-08, RT-09, RT-10
**Success Criteria** (what must be TRUE):

1. 默认自研方案从 WebSocket 替换为 pion WebRTC；课堂工具和 ASR/LLM/TTS 接口保持不变，真实网络重新测 RT-04/RT-05。若已触发 Plan B，按新传输完成同样验收，不再强制投入已放弃的 pion 实现。
2. 真 iPad、用户网络全量 RT-01～RT-10 通过；旧 Phase 9 两项人工视觉/触控验收在这里逐项补验，不能借归档记成通过。
3. 实测单会话 CPU、内存、带宽、费用和目标并发；旧 50/100 连接测试不等于语音并发容量。
4. 完成部署、恢复、错误可见、会话释放；按实际用途决定旧页保留或删除。

**Plans**: 0 plans

Plans:
- [ ] TBD — /gsd-plan-phase 16

## Progress

执行顺序：13（R0）→14（R1）→15（R2）→16（R3）；时间检查跨阶段持续有效。

| Phase | Plans Complete | Status | Completed |
|---|---|---|---|
| 13. R0 真机最小闭环 | 0/0 | Ready to plan | - |
| 14. R1 对话接真实课堂 | 0/0 | Not started | - |
| 15. R2 学习闭环 | 0/0 | Not started | - |
| 16. R3 部署与容量 | 0/0 | Not started | - |

## 时间检查（Asia/Shanghai）

起算日 T0 = **2026-09-30**。从本轮 R0 规划启动计时，规划、接口准备和部署等待均计入；不得因阶段迁移、换传输或供应商重新计时。截止时间统一为当日 23:59（UTC+08:00）。

| 检查 | 截止日期 | 必须提供的证据 | 未达成的处置 |
|---|---|---|---|
| 第一周 | 2026-10-07 | 真 iPad、可信 HTTPS、真实网络下 3 词闭环；持续听说、可打断；真实读工具和写工具执行记录 | 记录失败原因和修复安排，停止非闭环工作；第三周切换日期不延后 |
| 第三周 | 2026-10-21 | 同一闭环真机通过，含语音发起、推进一词、重试不重复推进 | 未通过即切火山混合编排 Plan B，不继续无限期自研 |
| 第六周 | 2026-11-11 | RT-04 与 RT-05 真机实测达标：至少 30 次响应/打断样本、原始时序、P50/P95，验证旧音频不复活 | 任一不达标即切 Plan B；不得把服务端收帧代替实际可听时刻 |

检查记录由当前执行者写入 [执行检查表](phases/13-realtime-minimal-loop/13-CHECKPOINTS.md)。截至本次补充，检查尚未执行，新实时语音尚未实现。

## 旧方案处置

v1.0 在 2026-09-30 以 superseded / override_closeout 关闭，产品验收未通过，无发布标签。历史记录见 [归档路线图](milestones/v1.0-ROADMAP.md)、[归档需求](milestones/v1.0-REQUIREMENTS.md)、[关闭记录](milestones/v1.0-CLOSEOUT.md)。原阶段文件保留原路径供追溯；当前阶段筛选只包含 13～16，不能继续执行旧计划。

| 原范围 | 最终处置 | 接收阶段 |
|---|---|---|
| Phase 10 家长反馈 | superseded | Phase 15，RT-09 |
| Phase 11 错误处理与恢复 | superseded | Phase 13/15/16，RT-07/08/10 |
| Phase 12 端到端验证 | superseded | Phase 16，RT-01～RT-10 全量 |

书面 [Plan B](../docs/REALTIME-TUTOR-PLAN-B.md) 只在条件触发时实施；切换不改变产品目标、业务数据和验收阈值。

## Backlog

### Phase 999.1: go build/test 性能优化 — 超时测试导致构建慢 (BACKLOG)

**Goal:** 排查并修复导致 `go build` / `go test` 执行缓慢的根因
**Requirements:** TBD
**Plans:** 0 plans

Plans:

- [ ] TBD (promote with /gsd-review-backlog when ready)

**问题描述：** `go build` 和 `go test` 执行时间异常长，怀疑代码中存在设置了长时间超时条件的测试用例（如 `time.Sleep`、`time.After`、自定义超时），导致每次构建/测试都需要等待超时才能完成。需要专题排查所有测试文件中的 time-related 阻塞模式，将其改为可控的 mock 或缩短超时。

历史待办，未经主动提升不阻塞 Phase 13。单独 go build 不运行测试；具体慢点须分别测构建、测试、服务就绪和首个可听响应，不能仅凭测试超时推断构建慢。
