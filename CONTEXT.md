# Home Tutor Agent — Context

Updated: 2026-09-30（北京时间）。完整事实与交付顺序见 [项目复盘](docs/PROJECT-REVIEW-2026-09-30.md)；已接受的架构决策见 [ADR-0002](docs/adr/0002-browser-realtime-tutor.md)。

## Core Value

学生在浏览器与小天进行自然、实时家教交流。小天理解上下文、组织听写、回答追问、提供帮助，并允许学生打断；学生在纸上书写。词库、FSRS 和家长反馈支持这一学习体验。

用户在 2026-09-30 明确确认：按钮驱动听写和识别少量语音指令是效果未达标时的退化验证手段，不能将其替代为最终需求。

## Project Philosophy

**快速反馈周期优先** (Fast Feedback Cycle First)
- 任何阻碍快速验证想法的事情都是最高优先级
- 本地快速验证用于开发反馈，产品验收必须在 VPS HTTPS、真实网络和 iPad 上完成
- 必须能看到端到端的用户流程能跑起来，而不是各组件独立工作

## Key Concepts

### Minimum End-to-End Flow
**最小端到端流程** — 最简单自然家教会话：
> 学生打开 HTTPS 网页并授权音频 → 语音发起三个词的练习 → 家教引导纸笔书写 → 学生自然追问或打断 → 家教正确接话并保持课程进度

这是核心价值验证点。如果这个流程跑不通，其他工作都是在沙滩上盖楼。

### Component Isolation
**组件孤岛问题** — 当前的技术债务：
- ✅ WebSocket运行时独立工作
- ✅ Python桥接层独立工作  
- ✅ 配置系统独立工作
- ✅ 已有网页听写与学习业务链路
- ❌ 持续自然对话、上下文追问与自然打断尚未实现

组件与按钮听写验收已有进展，但自然家教目标仍缺少真正贯通的实现和验收。

### Quick Feedback Validation
**快速反馈验证** — 下一步独立 R0 入口：
> 打开真实 iPad 浏览器 → 一次授权后连续交流至少十轮 → 理解上一词与求助 → 播放中可打断

先使用固定三个词证明实时语音体验，再接词库、FSRS 和批改；完整验收指标 RT-01～RT-10 见项目复盘。

## Technical Context

### Current Stack
- **Backend**: Go service with WebSocket runtime
- **Python Integration**: Subprocess bridge for FSRS/vocab logic
- **Infrastructure**: 1C1G VPS, 150MB Go memory budget
- **Target**: iPad web browser (Safari)

### Project Status
- 已有学生与家长网页、WebSocket 处理器、词库、FSRS、TTS、提交批改和记录代码。
- 主语音路径仍为按住录音后识别有限指令，没有完整自然对话循环。
- 历史日志一次 10 词会话准备耗时 23.906 秒；整批逐词合成阻塞会话就绪。
- 本次隔离启动到 HTTP 200 为 0.520～1.782 秒；已有编译缓存下构建 1.830 秒，未测冷构建。
- 普通局域网 HTTP 不适合作为 iPad 麦克风验收入口，需可信 HTTPS。
- 产品验收未通过；阶段完成比例只反映历史计划交付。
- 推荐重做实时交互层，保留 Go/Python/SQLite 学习业务；新方案仍为 Proposed，待 R0 真机验证。

## Priority Framework

**Prioritization Hierarchy:**
1. **Fast Feedback** — Local debug environment > all
2. **End-to-End Visibility** — Minimum flow > component perfection
3. **Infrastructure as Needed** — Build just enough to support #1 and #2

This framework emerged from the realization that the project had invested heavily in infrastructure (WebSocket, Python bridge, config) without building the user-facing value (frontend, TTS, audio processing, session logic).
