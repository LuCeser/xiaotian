# Phase 13 — External API Coverage Matrix

> Detector verdict: `detected: true`（阶段范围含外部 API 集成：MiMo HTTP API）。矩阵于计划修订轮（2026-10-01）落盘。
> 基线为全量覆盖（INTEGRATE 为默认）；每个 OPT-OUT 都带一行理由。

| capability | decision | reason |
|---|---|---|
| chat.completions 非流式（messages/system/max_tokens 等） | INTEGRATE | 13-02 wire 扩展 + 13-04 工具环主路径 |
| chat.completions function calling（`tools`/`tool_choice`/`tool_calls`/`role:"tool"`/`finish_reason`） | INTEGRATE | RT-03/RT-07 读写工具核心（get_lesson_state/advance_word） |
| chat.completions `thinking` 开关（`{"type":"disabled"}`） | INTEGRATE | RT-04 延迟大头（RESEARCH Pitfall 1），WithThinkingDisabled |
| chat.completions 流式 SSE（chat.completion.chunk） | OPT-OUT | R0 非流式 LLM + 逐句流式 TTS 已满足成环；第六周不达标再评估（RESEARCH Alternatives 已记录升级路径） |
| 图像理解（ChatVision / image-understanding） | OPT-OUT | 照片 OCR 属存量冻结栈（photo 管线），CONTEXT Deferred 明确 OCR/批改不前置 |
| ASR 整段识别（wav/mp3 base64 DataURL ≤10MB） | INTEGRATE | 13-06 MimoASR 适配器（voice.WrapWAV 封装，zh） |
| ASR SSE 流式文本输出 | OPT-OUT | CONTEXT 锁定「输出流式不能冒充持续流式输入」；R0 由服务端 VAD 切句后整段提交 |
| ASR `asr_options.language` 请求选项 | INTEGRATE | WithASRLanguage("zh") 经 13-06 适配器传入 |
| TTS 流式输出（pcm16 SSE chunk） | INTEGRATE | 13-04 逐句合成收即播（pkg/mimo.SynthesizeStream 存量实现） |
| TTS 非流式输出（整段音频） | OPT-OUT | CONTEXT 明令不等整句收齐再播；存量 Synthesize 留给旧降级栈，rt 路径不用 |
| TTS `messages` 双角色模式与 voice 选择 | INTEGRATE | mimo.PresetDictationTeacher 选项集经 13-06 适配器附加 |

范围声明：本阶段唯一外部 API 为 MiMo（api.xiaomimimo.com/v1）；认证沿用存量 MIMO_API_KEY env 注入（不入 git/config），无 OAuth/webhook 面。TLS 证书属用户自有资产而非本阶段集成的外部 API 能力。
