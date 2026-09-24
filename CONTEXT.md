# Home Tutor Agent — Context

## Core Value

Students receive personalized, real-time dictation practice with immediate feedback while parents get detailed progress tracking and the ability to influence the learning algorithm through their reviews.

## Project Philosophy

**快速反馈周期优先** (Fast Feedback Cycle First)
- 任何阻碍快速验证想法的事情都是最高优先级
- 必须能本地快速验证改动，而不是推到VPS测试
- 必须能看到端到端的用户流程能跑起来，而不是各组件独立工作

## Key Concepts

### Minimum End-to-End Flow
**最小端到端流程** — 最简单听写会话：
> 学生打开网页 → 系统朗读单词 → 学生语音回答 → 系统判断对错 → 显示反馈

这是核心价值验证点。如果这个流程跑不通，其他工作都是在沙滩上盖楼。

### Component Isolation
**组件孤岛问题** — 当前的技术债务：
- ✅ WebSocket运行时独立工作
- ✅ Python桥接层独立工作  
- ✅ 配置系统独立工作
- ❌ 但整个系统连起来能否服务真实听写会话？未知

各组件在各自进度推进，但缺少端到端验证。

### Quick Feedback Validation
**快速反馈验证** — 即将实施的极简前端：
> 打开网页 → WebSocket连接成功 → 点击按钮发测试消息 → 后端回复 → 页面显示回复

目的：验证整个技术栈真正连通，而不是各自孤立的组件。

## Technical Context

### Current Stack
- **Backend**: Go service with WebSocket runtime
- **Python Integration**: Subprocess bridge for FSRS/vocab logic
- **Infrastructure**: 1C1G VPS, 150MB Go memory budget
- **Target**: iPad web browser (Safari)

### Project Status
- Phase 01 (Resource-constrained infrastructure): ✅ Complete
- Phase 02 (WebSocket communication layer): ✅ Complete
- Phase 03 (Python CLI integration): ✅ Complete, pending verification
- Frontend: ❌ Completely missing
- WebSocket handlers: ❌ All no-op placeholders

## Priority Framework

**Prioritization Hierarchy:**
1. **Fast Feedback** — Local debug environment > all
2. **End-to-End Visibility** — Minimum flow > component perfection
3. **Infrastructure as Needed** — Build just enough to support #1 and #2

This framework emerged from the realization that the project had invested heavily in infrastructure (WebSocket, Python bridge, config) without building the user-facing value (frontend, TTS, audio processing, session logic).
