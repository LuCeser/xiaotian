# ADR-0002：以浏览器自然实时家教重新组织交互架构

**Status:** Accepted（2026-09-30 用户拷问讨论拍板；实现与真机验收按 R0 三条硬护栏执行）  
**Date:** 2026-09-30  
**Deciders:** 项目用户，以真实 iPad 体验验收；开发者执行实现与技术评估

## Context

最初目标是浏览器中的自然、实时家教交流。现有 Go＋Python＋SQLite 已交付学习业务和网页听写，但主路径为整批 TTS 预生成、按住录音后的整段 ASR 和有限命令分类。历史一次 10 词会话就绪耗时 23.906 秒；本次隔离启动到 HTTP 200 为 0.520～1.782 秒。需要重做的是实时交互链路，不能仅从“Go 启动慢”推导更换所有语言。

用户在 2026-09-30 明确澄清：按钮与少量命令是退化验证手段，不能替代自然家教目标。完整证据与新验收见 [项目复盘](../PROJECT-REVIEW-2026-09-30.md)。

约束：iPad Safari 可用；首次必要授权后持续听说；支持中文教学和英语单词；等待纸笔书写；真实打断和上下文交流；保留已有词库/FSRS 数据；优先单学生体验；旧 1C1G/150MB 约束适用于已测业务服务，不作为一切实时媒体组件的既定可行结论。

2026-09-30 拷问讨论的三项关键输入：

1. **业主本人是实时音视频领域工程师。** 自研 RTC 层的练手价值计入收益，复杂度定价因此改变——对一般团队过重的自研媒体工作，对本项目同时是产品路径与学习目标。
2. **MiMo 现有 key 覆盖 ASR＋LLM＋TTS 全链路，但 MiMo ASR 官方接口当前为整段音频输入。** 输出流式不等于输入持续双向，需要自研采集→VAD 切句→整句提交的管线适配（见 Decision）。
3. **绿地自建新交互层不等于在旧 PCM 协议上修补。** 旧协议为单向播放设计（20ms 定时推帧、整词缓存播放），在其上补全双工对话需同时重写采集、传输语义与打断取消三层（复盘第 7 节、pi 评审 1.1）。

## Decision

采用三层组合，各层均以可替换接口封装：

1. **自研 RTC 层。** 传输两步走：先用 WebSocket 成环，后换 WebRTC（Go pion）；传输层封装为可替换接口。理由：业主为该领域工程师，自研 RTC 的练手价值计入收益，复杂度定价改变；绿地自建新交互层不等于在旧协议上修补。
2. **语音全家桶用小米 MiMo。** ASR＋LLM＋TTS 全部 MiMo（现有 key），每层封装为可替换接口，换供应商的成本限制在适配层。
3. **业务与工具继续 Go/Python＋SQLite。** 词库、FSRS、批改与学习记录保留复用，不进行与体验无直接关系的语言迁移。

**MiMo 整段式 ASR 的适配路径：** 浏览器采集 → 服务端 VAD 切句 → 整句提交 MiMo ASR → 流式收文本；判停、轮次与打断由自研管线负责，turn manager 从第一天按「工具＋幂等」设计（硬护栏 c）。

## Options Considered

### A：继续扩展旧 Go 二进制 PCM 协议（否决）

| Dimension | Assessment |
|---|---|
| Complexity | 高：需要补持续识别、VAD、播放、回声、轮次取消和上下文 |
| Cost | 少一个托管媒体服务，开发与维护成本较高 |
| Scalability | 原连接上限不能证明连续语音性能，需要重新测量 |
| Familiarity | 最接近现有代码 |

优点：复用现有 Go 通信组件。缺点：旧客户端整词缓存、数据/控制路由时序、有限指令语义与其单向播放设计与全双工对话目标冲突；绿地自建不等于在旧协议上修补。否决。

### B：火山 RTC＋支持工具的语音 Agent（Plan B，触发条件见硬护栏 b）

| Dimension | Assessment |
|---|---|
| Complexity | 中：SDK/鉴权/工具回调/课堂状态；避免自行维护全部音频链路 |
| Cost | RTC、ASR/TTS/模型或端到端服务分别核实；暂未测算实际账单 |
| Scalability | 媒体托管，业务服务按真实工具调用负载扩展 |
| Familiarity | 新 RTC 能力需要学习；可复用火山服务经验 |

已核实能力清单：接入自定义 ASR/TTS、第三方 LLM、Function Calling、打断、判停。

原为首个候选，2026-09-30 重定基后降级为**书面 Plan B**：硬护栏 b 触发（3 周内无真机最小环、或 6 周内 RT-04/RT-05 不达标）时切换到火山 RTC 混合编排。切换前仅保留本表与 Sources 供核对，不预先投入实现。

### C：原生实时模型 WebRTC（对照候选记录）

| Dimension | Assessment |
|---|---|
| Complexity | 中：实时会话＋服务端工具，最小原型较短 |
| Cost | 根据语音、上下文和工具使用实测，不沿用文本聊天估价 |
| Scalability | 媒体和模型托管，应用负责业务工具 |
| Familiarity | 现有项目没有该集成 |

优点：持续对话、上下文和音频传输由模型服务配合 WebRTC 处理。缺点：访问资格/区域、目标网络可达性和具体工具协议需确认；不能假定当前 key 可用。保留为对照候选记录。

### D：LiveKit Agents（对照候选记录）

| Dimension | Assessment |
|---|---|
| Complexity | 中到高：托管模式较轻，自托管涉及媒体与 Agent 生命周期 |
| Cost | 托管费用或独立计算资源，加各模型调用费用 |
| Scalability | 标准框架，随房间和 Agent 任务扩展 |
| Familiarity | 新 Node/Python Agent 运行方式，原业务通过 API 复用 |

优点：成熟 SDK 提供流式处理、轮次检测和打断，易切换模型。缺点：完整自托管栈的资源与维护不应强塞进现有 1C1G；官方一般配置建议不是本项目的实测容量。保留为对照候选记录。

### E：自研 RTC 层＋MiMo 全家桶可替换接口（已选主路线）

| Dimension | Assessment |
|---|---|
| Complexity | 中到高：自研采集/VAD/轮次/打断管线；传输先 WebSocket 后 WebRTC（pion） |
| Cost | 无托管 RTC 账单；MiMo 按用量计费；开发时间成本由硬护栏约束 |
| Scalability | 按实际组件评估，不沿用旧 150MB Go 预算结论 |
| Familiarity | 业主为实时音视频领域工程师，自研练手为目标之一 |

优点：复杂度定价因业主背景改变；传输层与每层模型接口均可替换，供应商锁定成本限制在适配层；与 Go 业务同栈。缺点：判停、回声、打断、播放调度、取消与跨路由时序全部自担（A 表所列复杂度仍真实存在，由硬护栏约束风险敞口）。

## 硬护栏（kill criteria）

1. **a) 垂直切片纪律：** 第 1 周必须在真 iPad 上跑通 3 词最小闭环（采集→VAD→MiMo ASR→MiMo LLM→MiMo TTS→流式播放→可打断）。
2. **b) 时间盒与 kill criteria：** 3 周内无真机最小环、或 6 周内 RT-04/RT-05 不达标 → 切换书面 Plan B（火山 RTC 混合编排，见 Options B）。
3. **c) 工具调用环：** 最小环必须含至少一个真实工具调用（如 get_lesson_state 读 3 个固定词），turn manager 从第一天按「工具＋幂等」设计。

## Trade-off Analysis

维护已有数据和业务接口的收益较高；保留整个媒体协议的收益较低。把 Go 换成 Node 或把 Python 改写为 Go，均不会自行产生自然交流、打断或首音频低延迟，因此首期不进行与体验无直接关系的语言迁移。

实时对话管线（采集/VAD/判停/轮次/打断）由自研层负责，业务工具负责可验证的课程推进。先验证「语音→工具→结果→语音」完整环，再接完整词库和批改。固定词音可以按需缓存与短距预取；禁止整课合成阻塞对话开始。

完整课堂状态独立于语音 speaking/listening/thinking。孩子沉默写字属于正常教学行为；判停策略与书写等待不能共用一个短超时。求助优先提示或澄清，不再固定映射跳词。

## Consequences

- 旧按钮听写页、`/ws` PCM 协议与 `pkg/voice` 命令栈功能冻结为 RT-10 降级模式：只修阻塞性 bug，不加新特性。
- 每层 MiMo 接口（ASR/LLM/TTS）与传输层均为可替换接口；换供应商、换传输的成本限制在适配层。
- WebRTC（Go pion）为第二阶段：先用 WebSocket 成环，传输层接口保持可替换。
- iPad 必须可信 HTTPS：业主现有证书，部署 VPS，iPad 走真实网络验收；localhost 成功不能证明局域网 HTTP 可用。
- 可保留词库、FSRS、SQLite、批改和记录，不要求重建全部数据；必须处理课堂 ID、工具幂等、版本、打断后的音频取消和真实已听内容，模型口头承诺不能代替工具成功。
- 火山 RTC 混合编排降级为书面 Plan B，触发条件见硬护栏 b；已核实能力清单保留在 Options B 供切换时核对。
- 旧阶段完成标记保留为历史组件记录；最终产品是否完成改由 RT-01～RT-10 验收决定（见 REQUIREMENTS「产品验收」组）。
- 新路线不达标时按硬护栏 b 切换 Plan B，不以延长超时或隐藏语音入口替代验收。

## Action Items

1. [ ] R0：MiMo 整段式 ASR 适配——自研管线：浏览器采集→服务端 VAD 切句→整句提交 MiMo ASR→流式收文本。
2. [ ] R0：HTTPS 证书部署核对——业主现有证书部署到 VPS，iPad 真实网络可信访问（RT-01）。
3. [ ] R0：工具调用环——get_lesson_state 读 3 个固定词；turn manager 从第一天按「工具＋幂等」设计（硬护栏 c）。
4. [ ] R0：第 1 周真 iPad 3 词最小闭环（采集→VAD→MiMo ASR→MiMo LLM→MiMo TTS→流式播放→可打断），WebSocket 传输成环；按复盘第 10 节「必须记录」清单采集 RT-04/RT-05 时序。
5. [ ] R1：接入 start/get/repeat/advance/pause/resume/hint/finish 工具和确定性课堂状态；request_id/版本幂等与旧响应取消；首词按需、余下短距预取；正常写字不自动跳词。
6. [ ] R2：接真实词库、提交批改、家长确认与 FSRS，验证整个学习闭环。
7. [ ] R3：测试恢复、真实部署、单会话资源与费用，再决定容量与旧代码清理。
8. [ ] 硬护栏 b 触发时切换火山 Plan B（以 Options B 能力清单为核对起点）。

## Sources

- [火山 RTC 端到端及混合编排](https://docs.volcengine.com/docs/real_time_communication/Accessend-to-endreal-timespeechlargemodel?lang=zh)：现在服务 Plan B 核对（工具编排模式及受限控制接口）。
- [火山 Web SDK](https://docs.volcengine.com/docs/real_time_communication/WebSDK?lang=zh)：浏览器接入能力；Plan B 切换时目标 Safari 兼容性仍需真机验证。
- [OpenAI Realtime WebRTC](https://developers.openai.com/api/docs/guides/voice-webrtc?voice-api=realtime)：对照候选 C 的浏览器音频与服务端会话初始化方式。
- [LiveKit Agents](https://docs.livekit.io/agents/) / [轮次与打断](https://docs.livekit.io/agents/logic/turns/tuning/) / [部署资源](https://docs.livekit.io/deploy/custom/deployments/)：对照候选 D 的 SDK 能力与部署边界。
- [MiMo ASR](https://mimo.mi.com/docs/en-US/quick-start/usage-guide/audio/Speech-Recognition)：整段音频输入与流式文本输出的区别，服务 R0 整段式适配。
- [MDN getUserMedia](https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getUserMedia)：安全上下文与麦克风访问规则（HTTPS 部署核对依据）。
