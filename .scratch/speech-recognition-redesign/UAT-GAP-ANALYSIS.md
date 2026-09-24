# UAT 差距分析 — 定义能力 vs 实际实现

**日期：** 2026-09-13（首次真机 iPad UAT 后）
**触发：** 用户实测 A 段流程，报告 4 项症状：①语音语气奇怪 ②应播两遍只播一遍 ③音频未播完即被切 ④词与词间隔几乎为零
**方法：** REQUIREMENTS.md / ROADMAP 8.1 / ticket 02 规格 ↔ 代码审计（explorer 全栈追踪 + orchestrator 逐行核验关键证据）

---

## 一、结论摘要

1. **听写音频源从未切换到小米 Mimo TTS。** mimo.Synthesize / SynthesizeStream / PresetDictationTeacher 在生产代码中**零调用**（仅测试引用）。实际听写音频 = Phase 6 旧管线（pkg/tts → Python pregenerate → Volcengine seed-tts）。ticket 02 标题即 "Core Dictation Flow with Xiaomi Mimo TTS"，其 "done 2026-08-23" 状态与 DICT-03 校准注记（"音频源由 Python-bridge TTS 迁移至小米 Mimo 直连"）**名不副实**。
2. **前端存在破坏性节奏 bug：** `_applyWordComplete` 无条件发送 `next_word`（websocket-client.js:590），不区分批量/逐词模式 → 词背靠背连播、间隔≈0、逐词语音作答从不启动。
3. **双遍朗读在生成层实现**（Python 拼 "word. word." 单文件），但被 ffmpeg 静音裁剪和自动推进 bug 破坏 → 听感只播一遍 + 截断。
4. **本次测试服务启动时未加载 .env**（orchestrator 失误）：MIMO_API_KEY 缺失 → 语音命令/拍照 OCR 被禁用。此失误不影响上述结论——听写音频源本就是 Volcengine。

## 二、症状 → 根因映射

| 症状 | 根因 | 证据 |
|------|------|------|
| ① 语气奇怪 | Volcengine emo 声音（en_female_skye_emo_v2_mars_bigtts）读拼接文本 "word. word." + 课堂语境指令 + word_speed_rate=-30% | pregenerate_dictation.py:117-137, 683-689 |
| ② 只播一遍 | (a) `_trim_audio_edges` ffmpeg silenceremove stop_silence=2.0 可裁掉第二遍；(b) 自动推进使下一词叠上来 | pregenerate_dictation.py:428-458, 500；websocket-client.js:590 |
| ③ 播放截断 | 每词播完即发 next_word，尾音被下一词覆盖 | websocket-client.js:584-591 |
| ④ 间隔≈0 | 同上——主根因。`batchMode` 在 websocket-client.js 中零引用（仅存在于 state-manager.js:70 / app.js） | websocket-client.js:565-592 |

**级联效应：** 会话在播放结束前已推进到下一词的 LISTENING 态，`setOnPlaybackEnd` 的 `status === 'DICTATING'` 门（app.js:173）永假 → **逐词语音作答（A3）从未启动过**。整场会话变成"连播录音"而非听写。

## 三、能力差距总表

| # | 定义的能力（出处） | 实现现状 | 判定 |
|---|------|------|------|
| 1 | 听写音频源 = Mimo TTS 直连（DICT-03 校准注记 / ROADMAP 8.1 成功标准 1 / ticket 02 标题） | Volcengine 旧管线；mimo TTS 代码存在但未接线 | **未实现** |
| 2 | 传统听写节奏：一词一停（ticket 02 验收 6） | 前端自动推进 bug 摧毁节奏 | **破坏** |
| 3 | 每词读两遍（传统教师行为，Python 语境指令明示 "exactly twice / 朗读两遍"） | 生成层拼接实现，播放层常丢失 | 受损 |
| 4 | 自适应书写时间 8-15s（DICT-06 / ticket 02 验收 2） | AdaptiveTimeout 已实现（pkg/session/timing.go:12-30） | 已实现（被 #2 架空） |
| 5 | 逐词语音作答（A3 流程 / VOICE 路径） | 依赖 DICTATING 态，被 #2 压制，从不触发 | 被压制 |
| 6 | 学生控速按钮：下一词/重读/暂停（DICT-07 / ticket 02 验收 3） | 后端 FSM + 前端按钮已交付并有测试 | 已实现 |
| 7 | 语音命令 ASR + 意图（VOICE-01..04 / ticket 03） | voice.go 真实接线（mimo.Transcribe + mimo.Chat） | 已接线（本次被 .env 失误禁用，待真机复验） |
| 8 | 拍照 OCR（SUBMIT-01..03 / ticket 04） | photo.go 真实接线（mimo.ChatVision） | 已接线（同上） |
| 9 | 批量提交/自动批改/多用户隔离（ticket 05/06/07） | 本次审计未覆盖，鉴于 #1 的教训需降低"已验证"置信度 | 待复核 |
| 10 | <1.5s 音频延迟（DICT-05） | 从未真机测量（ticket 02 验收 5 亦未勾） | 未验证 |

## 四、系统性根因（为什么"做了很久"却"什么都没变"）

**验证文化问题：** "done" 的判定标准是单元测试 + fakeStreamer 断言（ticket 02 Completion note 自述："会话级流式 TTS 管线经 fakeStreamer 测试验证…真实 iPad 音频播放待 UAT 实测"）。测试证明的是**管线连通**，不是**用户体验成立**：

- fakeStreamer 断言"有流"，不问流里是什么声音、播几遍、间隔多久
- 前端节奏 bug 在单测视野之外（websocket-client.js 无对应测试覆盖 auto-advance 门控）
- "Mimo 直连"写进了 REQUIREMENTS/ROADMAP 的完成注记，但代码从未接线——**文档声明超前于实现**

这与 CONTEXT.md 的项目哲学（"最小端到端流程 > 组件完美"、"如果这个流程跑不通，其他工作都是在沙滩上盖楼"）直接冲突。2026-08-02 的快速反馈验证曾践行过该哲学，此后 6 周的 ticket 交付重新滑回"组件各自绿"的模式。

## 五、失误与责任记录

- **orchestrator（本次）：** Start-Process 启动服务未加载 .env → MIMO_API_KEY 缺失 → 语音命令/OCR 禁用 + 启动告警被忽略未上报。修复：启动脚本统一从 .env 注入环境变量。
- **历史交付（ticket 02）：** 以 fakeStreamer 验证宣告 "Mimo TTS 听写流" 完成，实际音频源未迁移。

## 六、修复建议（优先级序）

| 级 | 修复项 | 说明 |
|----|--------|------|
| **P0-1** | websocket-client.js `_applyWordComplete` 加 `batchMode` 门控 | 逐词模式不自动发 next_word——恢复学生控速、词间停顿、语音作答触发。一处小改，四症状去其二（③④） |
| **P0-2** | 听写音频源切换 Mimo Synthesize | 兑现 ticket 01/02 原始设计；pkg/mimo TTS 代码已存在待接线。症状①的根本解 |
| **P1-1** | 双遍朗读改为可靠机制 | 方案 A：前端调度单遍音频播放两次（间隔 1-2s）；方案 B：修 silenceremove 参数保住拼接双遍。症状② |
| **P1-2** | 服务启动脚本带 .env 注入 | 防再犯 |
| **P2** | tickets 03-07 交付真实性复核 | 按 #1 的教训抽验"文档声明 vs 代码接线" |
| **P2** | 全部修完后重跑 A 段 UAT | 更新 UAT-CHECKLIST.md |

---

*审计执行：explorer（全栈追踪）+ orchestrator（关键证据逐行核验：websocket-client.js:565-592、pkg/mimo 生产调用点 grep、app.js:900-901 均已亲眼确认）*
