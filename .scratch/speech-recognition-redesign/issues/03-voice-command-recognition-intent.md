# 03 — Voice Command Recognition and Intent Analysis

**What to build:** Students can control dictation using natural voice commands ("好了", "重读", "暂停", "不会写") with intelligent intent understanding. This replaces the unreliable Web Speech API with production-proven Xiaomi Mimo ASR + LLM analysis.

**Blocked by:** 01 — Xiaomi Mimo Service Integration Layer, 02 — Core Dictation Flow with Xiaomi Mimo TTS

**Status:** done (2026-08-31, commits: b178d99, 825d40d, 4594e59, c554f8a, 76ef58f, e598c60, 22a9ddc)

- [x] System captures audio via Web Audio API on iPad Safari — 取舍备注：VoiceCommandController（getUserMedia + ScriptProcessorNode，AudioContext 在按住手势内创建避免 iOS suspended；16kHz Int16、640 字节帧、末块零填充）经 headless Node 测试验证采样路径；真实 iPad Safari 采集待 UAT 实测（注意 getUserMedia 需 https 或 localhost，LAN http 测试麦克风会被浏览器禁用→按钮回退）
- [x] Xiaomi Mimo ASR transcribes student speech commands accurately — 取舍备注：mimoTranscriber 适配 voice.Transcriber seam，WAV(44字节头+PCM 16kHz mono) 契约由测试钉死；真实 ASR 准确率未实测——MIMO_API_KEY 在 .env 已于 2026-08-23 后过期（见 b178d99 备注），需换 key 后用真机复验
- [x] LLM intent analysis correctly identifies the 4 core commands from natural expressions — 取舍备注：关键词快速路径（零 LLM 延迟）+ LLM 回退混合分类器（825d40d，含宽松 JSON 解析）；Pipeline 对空转写短路（静音绝不触发 LLM 调用）；LLM 侧经 mock Chatter 验证，live mimo-v2.5 待 key 刷新复验
- [x] Voice commands trigger appropriate session state transitions — Action.ControlVerb 映射 next_word/repeat/pause/resume（cannot_write→next_word，听写老师行为），voice_command 处理器经 manager.Push 进会话事件循环；端到端测试断言 FSM 事件推送
- [x] System handles failed recognition and provides fallback to button controls — 每条路径（识别成功/不是命令/失败）都回 voice_result 帧；action=none 时前端提示"没听清，请再试一次，或点下面的按钮"；麦克风不可用/被拒时 mic 组件整体隐藏，Ticket 02 按钮始终可用；无 MIMO_API_KEY 时语音禁用（音频帧静默丢弃、触发返回错误帧）
- [ ] Voice control works reliably for children's voices (ages 6-12) — 取舍备注：需真实儿童语音样本 + 有效 API key 实测；已尽工程侧手段（AGC/noiseSuppression/echoCancellation 采集约束、LLM 提示词声明 6-12 岁非标准表达按意图理解），无法在自动化内验证
- [x] Natural language understanding handles varied expressions beyond fixed phrases — 取舍备注：关键词表未命中即升级 LLM（如"老师我觉得这个有点难"），HybridClassifier 测试覆盖变体表达；live 验证同上待 key

## Completion note

- 验证方式：`go test ./pkg/voice/ ./pkg/infra/ ./cmd/service/ -count=1` + `npm test`（headless 采样路径）
- 2026-08-31 实测：三包全绿 + Node 双套件通过；cmd/service 六个语音测试（含经真实 router+httptest socket 的端到端规则快速路径、取消中毒回归、溢出废弃、禁用模式）
- 双轴 code review（Standards + Spec，9ac9005...HEAD）：Spec 发现 1 项正确性缺陷——取消的录音会污染下一条命令（客户端序列号重置+服务端 assembler 吸收"重复"序列→旧音频排在新音频前）——已修复（voice_cancel 控制帧 Drop 缓冲 + voice_cancel_ok ack 解决跨路由乱序；回归测试 TestVoiceCancel_DiscardsRecording）；Standards 6 项 judgement call 处理 4 项（Transcriber seam 收窄、脚手架去重、错误注释×2）、2 项接受（infra/mimo 默认值复制已文档化、WAV 断言跨包不复用）
- 架构备注：pkg/voice 保持零 mimo 依赖（Transcriber/Chatter 双 func seam，cmd/service 适配）；语音禁用态用 nil *voiceService 方法接收者表达（音频路由 no-op、触发报错）；取消/溢出路径均通知服务端废弃，_handleAudio 在超限后停止累积
- 遗留 UAT：真实 iPad Safari 麦克风采集（需 https 或 localhost）、真实 Mimo ASR/LLM 准确率（需有效 MIMO_API_KEY，.env 现键已过期）、儿童语音可靠性（验收第 6 条未勾选）
