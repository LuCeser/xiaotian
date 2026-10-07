# Roadmap: Home Tutor Agent

## v2.0 realtime-tutor — ACTIVE

目标：浏览器自然实时家教，按 RT-01～RT-10 验收。架构已由 [ADR-0002](../docs/adr/0002-browser-realtime-tutor.md) Accepted 锁定：自研实时交互，MiMo ASR/LLM/TTS 各自可替换，先 WebSocket、后 WebRTC（Go pion），已有 Go/Python/SQLite 业务复用。

**唯一下一项工作：Phase 13（R0）。** GSD 使用数字编号，R0～R3 是产品阶段名称，对应 13～16。旧阶段 1～12 已关闭归档；Phase 10～12 为 superseded，不是暂停等待恢复。旧计划完成只算组件证据。

## Phases

- [ ] **Phase 13: R0 真机最小闭环** — FSRS 到期词状态机默写、幂等写与学习记录、真实打断与时序证据（2026-10-03 交互模型转向：状态机默写助手）
- [ ] **Phase 14: R1 对话接真实课堂** — 完整课堂工具、上下文、书写等待及幂等
- [ ] **Phase 15: R2 学习闭环** — 词库、提交批改、家长确认、FSRS 和恢复
- [ ] **Phase 16: R3 部署与容量** — pion WebRTC、全量真机验收、资源与费用

## Phase Details

### Phase 13: R0 真机最小闭环

**Goal**: 学生在真实 iPad 的 HTTPS 网页一次授权后，与「状态机默写助手」完成一节 FSRS 到期词听写课：老师开场报词、确认或 60 秒超时推进、四类口令（重读/慢读、不会写提示、词义解释、暂停/继续）正确响应、可打断老师、完结总结写入真实学习记录；进度与记录一致。（2026-10-03 交互模型转向：课堂节奏由服务端状态机拥有，LLM 仅做意图识别与话术生成）
**Depends on**: None
**Requirements**: RT-01, RT-02, RT-03, RT-04, RT-05, RT-07, RT-10
**Success Criteria** (what must be TRUE):

1. 独立自然家教入口经用户现有证书部署 VPS；真 iPad、真实网络访问。一次必要点击授权后持续听说，完整课堂无需反复按住录音。
2. 新 WebSocket 交互传输与 MiMo ASR、LLM、TTS 各有独立替换接口；采集→VAD 切句→整句 ASR→LLM 意图识别→流式 TTS→收到即播放，不等待整课或整句合成完成。课堂词来自 FSRS 到期获取（上限 10，不足取全部；0 词优雅结束），不前置完整词库、OCR 或容量工程。
3. 课堂推进由服务端状态机经 pkg/lesson 幂等执行（lesson_id、request_id、expected_version）：确认与超时对同一目标竞争只推进一次，重试/重放不双跳；完结总结先按 pybridge 实际 Updated 集合写学习记录，部分/零更新如实可见且不盲重试，再完整播报后结束。
4. 学生开口能停止当前播放、清空旧排队音频、取消或丢弃旧轮次输出；下一轮以实际播放到的位置为上下文。summary/no_due 以匹配的真实播放完成 ACK 作为 session_ended 前置条件；真机实测扬声器播放结束后不会触发老师自我转写、误确认或推进。
5. 「好了/可以了/下一个」确认推进；重读、慢读、不会写（先部分提示不泄题）、词义解释（据词库 ground truth）、暂停/继续各有正确响应；普通沉默仅在 60 秒计时到点推进（转向决策口径）。
6. 记录 RT-04/RT-05 全部实际播放时序；第一周成环与第六周延迟达标是不同检查。R0 可在闭环通过且测量已齐备后进入 R1，但未达标项仍保持未验收，第六周切换条件继续生效。
7. 故障原因可见（含 lesson_unavailable 取词失败）、可结束释放会话；按钮备用明确标为降级，不计语音通过。首问候缺失、VAD 0 秒误触发、MiMo invalid API response ×4 三个伴随缺陷修复并真机复核为零。

**Plans**: 6/13 plans executed（13-01～13-06 已执行；2026-10-03 交互模型转向后 13-07 起为重规划新约：13-04 的 LLM 工具环运行时路径被 13-08 状态机取代，wire 支持保留）

Plans:
**Wave 1**（已执行）

- [x] 13-01-PLAN.md — pkg/lesson 课堂状态存储与幂等工具执行（request_id + expected_version）
- [x] 13-02-PLAN.md — pkg/mimo 工具调用扩展（tools/thinking 关闭/tool_calls wire；转向后运行时不再使用，wire 支持保留）
- [x] 13-03-PLAN.md — pkg/rt 基础：协议、供应商接口、时序事件、/rt WSS 传输

**Wave 2**（已执行）

- [x] 13-04-PLAN.md — pkg/rt 轮次管理：能量 VAD、epoch 取消、逐句流式 TTS（MiMo 工具环部分被 13-08 取代）
- [x] 13-05-PLAN.md — tutor 前端：持续采集、收即播、打断通道、mock e2e
- [x] 13-06-PLAN.md — 服务接线：/rt 与 /tutor/ 挂载、Go 原生 TLS、降级模式

**Wave 3**（2026-10-03 转向后重规划；以下为现行合同）

- [ ] 13-07-PLAN.md — 转向地基：ASR 空转写哨兵 + lesson 词卡列 + 协议词表（播放完成 ACK/超时、paused、错误与结束原因）

**Wave 4**

- [ ] 13-08-PLAN.md — 状态机默写主环（tracer）：FSRS 取词、互斥提交/重放/冲突、播放 ACK 门、按实际 Updated 完结
- [ ] 13-11-PLAN.md — tutor 按句播放 ACK、自然结束不截音、暂停显示 + mock e2e（与 13-08 同波并行）

**Wave 5**

- [ ] 13-09-PLAN.md — 四类口令 + ACK 后 60 秒计时 + 独立定时器代际 + 暂停/回声/空转写修复
- [ ] 13-10-PLAN.md — RTConfig + PyFSRS 实际 Updated/FetchLockHours=1 + main.go 注线与部署文档（与 13-09 同波并行）

**Wave 6**

- [ ] 13-12-PLAN.md — 本机预验收：真 iPad 播放完成/回声/暂停恢复/部分更新与评级优先级追认

**Wave 7**

- [ ] 13-13-PLAN.md — VPS 脚本/锁时效核验 + 真机正式验收（播放、回声、打断、跨日 ≥30×2 样本）+ 第六周报告

### Phase 13.1: 三段式默写课堂（默写→检查→统一反馈） (INSERTED)

**Goal**: 把 R0「完结即按行为写评级」的模型升级为三段式课堂：默写（「好了」=写完信号，只记行为数据）→ 检查（拍照 OCR 逐词比对＋人工校正必经）→ 统一提交 FSRS。同时交付学生模式界面、「再练一组」与完结/无到期词状态区分。用户 2026-10-06 决策：本阶段在 13-13 VPS 部署之前执行（三段式先行；第一周检查记未达成）。
**Requirements**: RT-09（提前实现检查+统一提交部分；家长确认/覆盖评分仍留 Phase 15）, RT-07
**Depends on**: Phase 13
**Success Criteria** (what must be TRUE):

1. 完整课走三段：默写（「好了」仅表写完，逐词记提示次数/超时）→ 检查（拍照 OCR 逐词比对，人工可改判每个词——点「对/错」或语音说，语音可携带实际写法）→ 统一提交 FSRS（检查对+无提示=good、检查对+有提示=hard、检查错=again）。
2. 超时未检查的课按行为降一档落库（无提示→hard、有提示→again）＋「未核对」标记，事后补拍照可修正评级。
3. 学生模式为默认（URL 参数切换）：默写阶段屏幕不出现未默词文本（未默=序号占位、当前=序号高亮、已默=显词+绿勾），对话区目标词打码（报词字幕显「第N个词」、识别文本词→•••），检查阶段解除打码逐词展开；测试模式（URL 参数）保持全显+trace。
4. 对话区双列 trace（老师左/学生右、学生识别文本常驻）两种模式都有，作为家长回放最小形态。
5. 完结/无到期词状态区分＋「再练一组」入口：词库随机避开当日已默、≤10 不足全给、同走三段式，结果单独记录不进 FSRS 主调度。
6. OCR 判错可被人工改判覆盖（OCR 必错是前提），提交的评级以人工改判为准。

**Plans:** 3/7 plans executed（2026-10-07 规划；wave 1～7 顺序执行——turn.go/check.go/index.html 单文件瓶颈决定不可并行）

Plans:
**Wave 1**

- [x] 13.1-01-PLAN.md — tracer：三段式主环端到端（D-01/D-02/D-04/D-07）——契约（check_* 协议＋评级纯函数＋lessons 阶段态迁移/幂等 stage 写）＋手动改判最小闭环＋战果总结（2026-10-07 完成：RED→GREEN→前端→mock e2e 四提交，go test＋npm run e2e:rt 7/7 全绿）

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 13.1-02-PLAN.md — 学生模式（D-08/D-09/D-10）：服务端发送侧打码、三态词表、双列 trace、检查阶段全量揭示

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 13.1-03-PLAN.md — 拍照 OCR 检查环（D-05/D-06①）：photo 分块通道（base64 JSON）、多词识别、GradeWord 比对预填、人工改判覆盖＋逐词人工确认提交闸（拍照 mock e2e 延期至 13.1-07）

**Wave 4** *(blocked on Wave 3 completion)*

- [x] 13.1-04-PLAN.md — 未核对兜底＋跨会话恢复＋事后修正（D-03）：关闭即降档落库、resume/correction 路径、差异重写（2026-10-07 完成：RED→GREEN→mock e2e 五提交，go test 两包＋npm run e2e:rt 12/12 全绿）

**Wave 5** *(blocked on Wave 4 completion)*

- [ ] 13.1-05-PLAN.md — 再练一组＋收尾状态区分（D-11/D-12/D-13）：随机避开当日、practice_logs 独立存储零 FSRS 污染、两种收尾文案

**Wave 6** *(blocked on Wave 5 completion)*

- [ ] 13.1-06-PLAN.md — 检查阶段语音改判口令（D-06②）：11 值意图白名单、index/text 载荷、语音与点按同路径

**Wave 7** *(blocked on Wave 6 completion)*

- [ ] 13.1-07-PLAN.md — 阶段收口：落地 13.1-03 延期拍照 mock 用例＋全量回归＋真通道隔离冒烟（e2e:rt-real）＋13-13 交接清单

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
| 13. R0 真机最小闭环 | 6/7 | In Progress|  |
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
