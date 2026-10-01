---
phase: 13
slug: realtime-minimal-loop
# status lifecycle: draft (seeded by plan-phase) → validated (set by validate-phase §6)
# audit-milestone §5.5 distinguishes NOT-VALIDATED (draft) from PARTIAL (validated + nyquist_compliant: false) (#2117)
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-10-01
---

# Phase 13 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Seeded by plan-phase 2026-10-01（7 计划 / 4 波）；执行时逐行更新 Status。

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test（go 1.26）+ node 直跑 JS 单测 + Playwright（@playwright/test ^1.63） |
| **Config file** | Makefile（test/test-short）、package.json scripts、playwright.config.js、playwright.rt.config.js（13-05 新增） |
| **Quick run command** | `go test ./pkg/rt/... ./pkg/lesson/ -short -count=1 && npm test` |
| **Full suite command** | `make test && npm test && npx playwright test -c playwright.rt.config.js` |
| **Estimated runtime** | ~120 秒（短套秒级；e2e 视 mock 环境另计） |

---

## Sampling Rate

- **After every task commit:** Run `go test ./pkg/rt/... ./pkg/lesson/ -short -count=1 && npm test`
- **After every plan wave:** Run `make test && npm test && npx playwright test -c playwright.rt.config.js`
- **Before `/gsd-verify-work`:** Full suite must be green
- **Max feedback latency:** 120 秒

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 13-01-01 | 01 | 1 | RT-07 | T-13-03b | DDL 幂等 + EnsureLesson 不覆盖 | unit | `go test ./pkg/lesson/ -run TestStore -count=1` | ❌ W0（本计划创建） | ⬜ pending |
| 13-01-02 | 01 | 1 | RT-07 | T-13-04 | request_id 幂等 + 版本冲突事务 | unit | `go test ./pkg/lesson/ -count=1` | ❌ W0（本计划创建） | ⬜ pending |
| 13-02-01 | 02 | 1 | RT-03/RT-04 | T-13-05 | tools/thinking wire 字段 | unit | `go test ./pkg/mimo/ -short -count=1` | ✅ 改既有 | ⬜ pending |
| 13-02-02 | 02 | 1 | RT-03/RT-04 | T-13-05 | tool_calls 解析 + role:tool 往返 | unit | `go test ./pkg/mimo/ -run TestChat -count=1` | ❌ W0（本计划创建） | ⬜ pending |
| 13-02-03 | 02 | 1 | RT-04 | A4 | 真 key 烟测（无 key skip） | live | `go test ./pkg/mimo/ -run TestLiveChatTools -v` | ❌ W0（本计划创建） | ⬜ pending |
| 13-03-01 | 03 | 1 | RT-02 | — | session_init 协商校验矩阵 | unit | `go test ./pkg/rt/ -run TestProtocol -count=1` | ❌ W0（本计划创建） | ⬜ pending |
| 13-03-02 | 03 | 1 | RT-04/RT-10 | T-13-06b | 时序事件词表 + 脱敏字段 | unit | `go test ./pkg/rt/ -run "TestTiming\|TestFakes" -count=1` | ❌ W0（本计划创建） | ⬜ pending |
| 13-03-03 | 03 | 1 | RT-10 | T-13-01/T-13-02 | Origin/读限/心跳/会话释放 | unit | `go test ./pkg/rt/ -count=1` | ❌ W0（本计划创建） | ⬜ pending |
| 13-04-01 | 04 | 2 | RT-03 | — | VAD 切句五行为（30s 硬切） | unit | `go test ./pkg/rt/ -run TestVAD -count=1` | ❌ W0（本计划创建） | ⬜ pending |
| 13-04-02 | 04 | 2 | RT-03/RT-05/RT-07 | T-13-03/T-13-05b/T-13-04b | 工具环先执行后播报/epoch 丢弃/历史裁剪/门控/参数校验 | unit | `go test ./pkg/rt/ -count=1` | ❌ W0（本计划创建） | ⬜ pending |
| 13-05-01 | 05 | 2 | RT-01/RT-10 | — | 安全上下文提示 + 降级横幅 + 协议客户端 | unit(node) | `node tests/test-rt-timing.js` | ❌ W0（本计划创建） | ⬜ pending |
| 13-05-02 | 05 | 2 | RT-02/RT-05 | T-13-06c | 降采样/令牌/RMS/playedUntil 纯逻辑 | unit(node) | `node -e "…parse-ok"` + Task3 | ❌ W0（本计划创建） | ⬜ pending |
| 13-05-03 | 05 | 2 | RT-02/RT-05 | — | mock WSS 10 轮环 + 打断 + 结束 | e2e(mock) | `npx playwright test -c playwright.rt.config.js` | ❌ W0（本计划创建） | ⬜ pending |
| 13-06-01 | 06 | 3 | RT-01 | T-13-01c | TLS/RT 配置校验 | unit | `go test ./pkg/infra/ -run "TestTLS\|TestRT" -count=1` | ✅ 改既有 | ⬜ pending |
| 13-06-02 | 06 | 3 | RT-10 | — | /rt、/tutor/、TLS、lesson 接线 | unit(build) | `go build ./... && go vet ./cmd/service/ ./pkg/rt/` | ✅ 改既有 | ⬜ pending |
| 13-06-03 | 06 | 3 | RT-01 | T-13-06 | 自签证书 HTTPS 接线 + 缺证书失败 | unit | `go test ./cmd/service/ -run TestRunTLS -count=1 -v` | ❌ W0（本计划创建） | ⬜ pending |
| 13-07-01 | 07 | 4 | RT-04/RT-05 | — | 全量预检 + 构建产物 + 分析脚本 | unit/e2e | `make test && npm test && node scripts/rt-timing-report.js --self-test` | ❌ W0（本计划创建） | ⬜ pending |
| 13-07-02 | 07 | 4 | RT-01/RT-02 | T-13-06d | VPS 证书部署 + iPad 一次授权 | manual(checkpoint) | 13-CHECKPOINTS 记录 | n/a | ⬜ pending |
| 13-07-03 | 07 | 4 | RT-01~RT-07/RT-10 | T-13-06d | 真机回声/打断/场景/采样 ≥30 | manual(checkpoint) | 13-CHECKPOINTS 记录 + JSONL | n/a | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `pkg/lesson/*_test.go` — RT-07 幂等/冲突（13-01 建包即建测试）
- [ ] `pkg/mimo/chat_tools_test.go` + `chat_tools_live_test.go` — 工具 wire 与真 key 烟测
- [ ] `pkg/rt/protocol_test.go`/`timing_test.go`/`conn_test.go`/`providers_fake_test.go` — RT-02/04/10 单测基建（fake providers 是 13-04 前置）
- [ ] `pkg/rt/vad_test.go`/`turn_test.go` — RT-03/05 行为测试（fake 驱动）
- [ ] `tests/test-rt-playback.js`/`test-rt-timing.js` — 前端纯逻辑
- [ ] `tests/e2e/mock-rt-server.js` + `rt-loop.spec.js` + `playwright.rt.config.js` — 10 轮 mock 环
- [ ] `cmd/service/rt_wiring_test.go` — RT-01 TLS 接线

*测试文件全部由对应计划自建（绿地方案，无需单独 Wave 0 计划）。*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| VPS 证书部署 + HTTPS 入口 + iPad 锁标/一次授权 | RT-01/RT-02 | 证书/VPS/iPad 为用户资产，无法模拟 | 13-07 Task 2（7 步清单） |
| 扬声器回声不自我打断 + 学生打断即时停/不复活 | RT-05 | 真机扬声器与 AEC 行为不可模拟（A1/A3） | 13-07 Task 3 步骤 1-2（含阈值 URL 参数标定） |
| 3 词自然对话场景正确响应 | RT-03（R0 子集） | 真实语音 + MiMo 全链 | 13-07 Task 3 步骤 3-4 |
| ≥30 响应/打断时序样本与报告 | RT-04/RT-05 | 以真实可听音频计量，mock 不算证据 | 13-07 Task 3 步骤 6 + scripts/rt-timing-report.js |
| 故障可见与降级标注抽查 | RT-10 | 断网/错 key 的真实设备表现 | 13-07 Task 3 步骤 7 |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 120s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
