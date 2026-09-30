# Phase 13: R0 真机最小闭环 — Context

**Date:** 2026-09-30
**Status:** Ready for planning
**Milestone:** v2.0 realtime-tutor

## Phase Boundary

以 3 个固定词完成真 iPad HTTPS 自然实时家教。产品目标已确认，不重新讨论按钮主交互或托管 RTC 为默认方案。默认自研交互、MiMo ASR/LLM/TTS 各自可替换、WebSocket 成环；pion WebRTC 在 Phase 16 替换。真实写工具和幂等在本阶段进入最小环。

## Implementation Decisions

- 独立自然家教入口；旧按钮页仅为标明的备用模式。一次点击用于平台音频授权，之后持续听说，不每句按住。
- ASR 使用 MiMo 整段接口时，由 VAD 切句提交；输出流式不能冒充持续流式输入。开始采集时协商真实采样率和音频格式，不假设 iPad 采集就是 16kHz。
- 传输只负责音频和控制消息，轮次管理负责 VAD、判停、上下文、取消；供应商适配各自实现 ASR/LLM/TTS。保持小模块，不先做通用 RTC 框架。
- 独立课堂状态存于服务端；3 个固定词可作为测试课，不依赖选词、整课预生成和 OCR。
- MiMo LLM 真实请求 get_lesson_state + advance_word（或同等写工具）。先执行工具再形成确认反馈；request_id、lesson_id、expected_version 在工具执行端校验，同请求相同结果且只写一次，不以 LLM 记忆实现幂等。
- 音频播放不能等整句收齐。打断时立即停播并丢弃旧轮次的缓冲与迟到输出；取消语音生成不撤销已经提交的课堂工具结果。字幕与上下文要反映实际已播放内容。
- 回声是最低真机检查之一：开扬声器播放时，老师不自我打断；学生插话能生效。
- 普通沉默是书写时间；“不会写”先帮助或澄清，不直接 advance_word。

## Acceptance and Evidence

第一周 2026-10-07 闭环，第三周 2026-10-21 未成环切 Plan B，第六周 2026-11-11 延迟未达标切 Plan B。均北京时间当日 23:59，计时从本轮规划 2026-09-30 起，换方案不重置。

必须有真实麦克风、MiMo 三层调用、LLM 工具请求/结果及实际课堂写入、真 iPad 播放和打断。录制真机流程并保留脱敏时序日志，按 RT 指标记录首个实际播放时刻；至少 30 次响应/打断样本用于延迟检查。模拟供应商和单元测试只能作组件证据。

第一周最小环允许性能仍待调优，但不能缺持续听说、真实读写工具和打断。进入 R1 不取消第六周检查，未达标 RT 项不勾选完成。

## Deferred Ideas

完整词库、30 词课堂、OCR/批改、家长统计、大规模并发、数字人暂不前置。pion WebRTC 为 Phase 16 的已安排任务；时间条件触发后采用 [Plan B](../../../docs/REALTIME-TUTOR-PLAN-B.md)。

## Next

/gsd-plan-phase 13。先做端到端最小环，再做必要的补强；详见 [检查表](13-CHECKPOINTS.md)。
