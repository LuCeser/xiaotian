# 03 — Voice Command Recognition and Intent Analysis

**What to build:** Students can control dictation using natural voice commands ("好了", "重读", "暂停", "不会写") with intelligent intent understanding. This replaces the unreliable Web Speech API with production-proven Xiaomi Mimo ASR + LLM analysis.

**Blocked by:** 01 — Xiaomi Mimo Service Integration Layer, 02 — Core Dictation Flow with Xiaomi Mimo TTS

**Status:** done (2026-08-31, commits: b178d99, 825d40d, 4594e59, c554f8a, 76ef58f, e598c60, 22a9ddc; live 验证 2026-09-08: e3e7736, ab9f762)

- [x] System captures audio via Web Audio API on iPad Safari — 取舍备注：VoiceCommandController（getUserMedia + ScriptProcessorNode，AudioContext 在按住手势内创建避免 iOS suspended；16kHz Int16、640 字节帧、末块零填充）经 headless Node 测试验证采样路径；真实 iPad Safari 采集待 UAT 实测（注意 getUserMedia 需 https 或 localhost，LAN http 测试麦克风会被浏览器禁用→按钮回退）
- [x] Xiaomi Mimo ASR transcribes student speech commands accurately — 取舍备注：mimoTranscriber 适配 voice.Transcriber seam，WAV(44字节头+PCM 16kHz mono) 契约由测试钉死；2026-09-08 换新 key 后活体验证通过（TestVoiceCommand_Live*：真实 TTS→ASR→分类全链路，规则路径 ~1.5s）。已知边界：两字超短命令的 TTS→ASR 往返偶发误听（"暂停"→"正题"一次），真实儿童语音更长更自然，风险低
- [x] LLM intent analysis correctly identifies the 4 core commands from natural expressions — 取舍备注：关键词快速路径（零 LLM 延迟）+ LLM 回退混合分类器（825d40d，含宽松 JSON 解析）；Pipeline 对空转写短路（静音绝不触发 LLM 调用）；LLM 侧经 mock Chatter 验证，live mimo-v2.5 待 key 刷新复验
- [x] Voice commands trigger appropriate session state transitions — Action.ControlVerb 映射 next_word/repeat/pause/resume（cannot_write→next_word，听写老师行为），voice_command 处理器经 manager.Push 进会话事件循环；TestVoiceCommand_PauseReachesRealFSM 走真实 Manager + 真实 FSM（DICTATING→PAUSED→客户端收到 paused 帧 + 状态断言），5 次重复稳定
- [x] System handles failed recognition and provides fallback to button controls — 每条路径（识别成功/不是命令/失败）都回 voice_result 帧；action=none 时前端提示"没听清，请再试一次，或点下面的按钮"；麦克风不可用/被拒时 mic 组件整体隐藏，Ticket 02 按钮始终可用；无 MIMO_API_KEY 时语音禁用（音频帧静默丢弃、触发返回错误帧）
- [ ] Voice control works reliably for children's voices (ages 6-12) — 取舍备注：需真实儿童语音样本 + 有效 API key 实测；已尽工程侧手段（AGC/noiseSuppression/echoCancellation 采集约束、LLM 提示词声明 6-12 岁非标准表达按意图理解），无法在自动化内验证
- [x] Natural language understanding handles varied expressions beyond fixed phrases — 取舍备注：关键词表未命中即升级 LLM；活体验证（TestVoiceCommand_LiveLLMPath）抓到真实缺陷并已修：提示词的 cannot_write 例句缺"抱怨太难"语义（关键词表有"太难了"而提示词没有），真实 LLM 判成 none——提示词补齐后 "老师，我觉得这个有点难。"→cannot_write 稳定通过（思考模型单次分类实测 ~8s，已记入 voice.requestTimeout 注释：ASR ~2s + LLM ~8s ≈ 10s/15s 预算）

## Completion note

- 验证方式：`go test ./pkg/voice/ ./pkg/infra/ ./cmd/service/ -count=1` + `npm test`（headless 采样路径）
- 2026-08-31 实测：三包全绿 + Node 双套件通过；cmd/service 六个语音测试（含经真实 router+httptest socket 的端到端规则快速路径、取消中毒回归、溢出废弃、禁用模式）
- 2026-09-04 补强（端到端盲区）：①TestMainBootsWithVoiceEnabled 覆盖带 MIMO_API_KEY 的开机构造路径（此前只有禁用态开机）；②TestVoiceCommand_PauseReachesRealFSM 打通语音→真实 Manager→真实 FSM→paused 帧回客户端的完整环路（此前止步于 fake pusher）；为此 NewManager 的 gen/streamer 参数收窄为 NewSession 同款接口（D-05，main.go 调用点零改动）。AI 接缝（ASR/LLM/TTS）仍为 fake——真实 ASR/LLM 依赖有效 key，见遗留 UAT
- 双轴 code review（Standards + Spec，9ac9005...HEAD）：Spec 发现 1 项正确性缺陷——取消的录音会污染下一条命令（客户端序列号重置+服务端 assembler 吸收"重复"序列→旧音频排在新音频前）——已修复（voice_cancel 控制帧 Drop 缓冲 + voice_cancel_ok ack 解决跨路由乱序；回归测试 TestVoiceCancel_DiscardsRecording）；Standards 6 项 judgement call 处理 4 项（Transcriber seam 收窄、脚手架去重、错误注释×2）、2 项接受（infra/mimo 默认值复制已文档化、WAV 断言跨包不复用）
- 架构备注：pkg/voice 保持零 mimo 依赖（Transcriber/Chatter 双 func seam，cmd/service 适配）；语音禁用态用 nil *voiceService 方法接收者表达（音频路由 no-op、触发报错）；取消/溢出路径均通知服务端废弃，_handleAudio 在超限后停止累积
- 遗留 UAT：真实 iPad Safari 麦克风采集（需 https 或 localhost；活体测试的音频来自真实 TTS，采集端到浏览器仍待真机）、儿童语音可靠性（验收第 6 条未勾选）。真实 ASR/LLM 已于 2026-09-08 经 TestVoiceCommand_Live*（gated on MIMO_API_KEY）活体验证，另修两处真实缺陷：LLM 提示词语义缺口（上）与 settlePCM 早触发竞态（触发帧先于首块音频被处理时误判空录音，现先轮询缓冲出现再等静默窗，20x 压力复现→修复→30x+全量稳定）
