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
> Seeded by plan-phase 2026-10-01；2026-10-04 交互模型转向（状态机默写助手，STATE.md 决策）重规划后按存活计划集 13-07～13-13（7 计划 / 5 波，wave 3–7）重建本映射；执行时逐行更新 Status。已执行历史计划 13-01～13-06 的旧行随重规划移除（执行状态见各自 SUMMARY）。

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
| 13-07-01 | 07 | 3 | RT-03 | T-13-11 | ASR 空转写与坏响应哨兵分离（互斥断言双向） | unit | `go build ./... && go test ./pkg/mimo/ -short -count=1 -run "TestASR\|TestTranscribe" -v` | ✅ 改既有 | ⬜ pending |
| 13-07-02 | 07 | 3 | RT-07 | T-13-12 | lessons.cards 幂等迁移 + EnsureLessonCards 首内容优先 | unit | `go test ./pkg/lesson/ -count=1 -v` | ✅ 改既有 | ⬜ pending |
| 13-07-03 | 07 | 3 | RT-03/RT-07 | — | 协议词表 paused/lesson_unavailable/结束原因 + 哨兵映射（wire 零漂移） | unit | `go test ./pkg/rt/ -run "TestProtocol\|TestErrorCode" -count=1 && go build ./... && go vet ./pkg/rt/` | ✅ 改既有 | ⬜ pending |
| 13-08-01 | 08 | 4 | RT-02/RT-04/RT-07 | T-13-13/T-13-14 | FSM tracer：开场即首问候/确认幂等推进 + main.go 等编译对端同 commit 收窄 | unit | `go build ./... && go vet ./pkg/rt/ && go test ./pkg/rt/ -count=1 -run "TestDictation" -v` | ✅ 改既有（dictation.go 新建） | ⬜ pending |
| 13-08-02 | 08 | 4 | RT-03/RT-04 | T-13-15 | 意图分类契约（intent 白名单/reply 上限）+ 完结先 SubmitReview 后播报、失败口头如实告知 | unit | `go test ./pkg/rt/ -count=1 -run "TestClassify\|TestCompletion" -v` | ✅ 改既有 | ⬜ pending |
| 13-08-03 | 08 | 4 | RT-02/RT-04 | — | tool 环测试清算 + 整链集成双用例（happy path 全链 + 打断无迟到帧） | unit(integration) | `go test ./pkg/rt/ -count=1 && go test ./pkg/rt/ -count=1 -run "TestDictationOverConn" -v` | ✅ 改既有 | ⬜ pending |
| 13-11-01 | 11 | 4 | RT-01/RT-10 | T-13-22 | paused/完结/无到期词呈现 + lesson_unavailable 人话映射 + innerHTML 负门 | unit(node)+负门 | `npm test` + rg innerHTML 负门（四 JS + index.html） | ✅ 改既有 | ⬜ pending |
| 13-11-02 | 11 | 4 | RT-02 | T-13-23 | mock 服务默写流脚本逐字段镜像协议词表 | syntax | `node --check tests/e2e-rt/mock-rt-server.js` | ✅ 改既有 | ⬜ pending |
| 13-11-03 | 11 | 4 | RT-01/RT-02 | T-13-23 | 三用例（全课单授权/打断/暂停结束）+ 存量套件隔离 | e2e(mock) | `npx playwright test -c playwright.rt.config.js` + `npx playwright test --list` 不含 rt-loop | ✅ 改既有 | ⬜ pending |
| 13-09-01 | 09 | 5 | RT-03 | T-13-17 | 四类口令确定性模板（slow 不逐字拼写、hint 不泄整词）+ 暂停继续广播 | unit | `go test ./pkg/rt/ -count=1 -run "TestIntent" -v` | ✅ 改既有 | ⬜ pending |
| 13-09-02 | 09 | 5 | RT-03/RT-07 | T-13-16 | 60s 词计时器锁内四重校验 + 超时/确认竞争幂等只推进一次（EventTimeoutFired） | unit | `go test ./pkg/rt/ -count=1 -run "TestWordTimer\|TestWordTimeout\|TestStaleTimer\|TestTimeoutConfirmRace" -v` | ✅ 改既有 | ⬜ pending |
| 13-09-03 | 09 | 5 | RT-03/RT-05 | T-13-18 | VAD 双静默期门控 + 空转写零用户可见报错 + invalid response 单次重试 | unit(+integration) | `go test ./pkg/rt/ -count=1 -run "TestVAD\|TestASR\|TestDictationOverConn" -v && go vet ./pkg/rt/` | ✅ 改既有 | ⬜ pending |
| 13-10-01 | 10 | 5 | RT-10 | T-13-19 | RTConfig 转向字段集（lessonId 可空/wordTimeoutMs）+ env 绑定无静默失效键 | unit | `go test ./pkg/infra/ -short -count=1 -run "TestRT\|TestTLS\|TestLoadConfig" -v` | ✅ 改既有 | ⬜ pending |
| 13-10-02 | 10 | 5 | RT-10 | T-13-20 | PyFSRS 适配器：非 peek RunDue 取词、空 results 跳过 RunReview | unit | `go test ./pkg/rt/ -count=1 -run "TestPyFSRS" -v && go build ./...` | ❌ 本计划创建 | ⬜ pending |
| 13-10-03 | 10 | 5 | RT-10 | T-13-21 | main.go PyFSRS/WordTimeout 注线 + wiring 测试 + DEPLOYMENT 词源前提与排障 | unit(build)+docs | `go build ./... && go vet ./cmd/service/ ./pkg/rt/ && go test ./cmd/service/ -short -count=1 && go test ./pkg/rt/ ./pkg/infra/ -short -count=1` | ✅ 改既有 | ⬜ pending |
| 13-12-01 | 12 | 6 | RT-02/RT-10 | T-13-26 | 全量预检 + 8443 通道新构建重启 + 真实 RunDue 取词冒烟（词数 >0） | unit/e2e/smoke | `go test ./pkg/... ./cmd/... -short -count=1 && npm test && npx playwright test -c playwright.rt.config.js` | n/a（CHECKPOINTS 记录） | ⬜ pending |
| 13-12-02 | 12 | 6 | RT-01~RT-05/RT-07/RT-10 | T-13-24/T-13-25 | 真 iPad 本机默写课（口令/超时/打断/完结记录）+ 评级映射用户追认 + 三缺陷零复发复核 | manual(checkpoint) | 13-CHECKPOINTS 本机预验收记录 + resume-signal | n/a | ⬜ pending |
| 13-12-03 | 12 | 6 | RT-04 | T-13-24 | 时序报告管线自检 + 本机样本报告（不作第六周证据）+ 脱敏落档 | script | `node scripts/rt-timing-report.js --self-test && git diff --cached --stat` | ✅ 既有 | ⬜ pending |
| 13-13-01 | 13 | 7 | RT-01 | T-13-27 | 预检 + Linux 部署包（CGO=0，sha256）+ 配置差异/FSRS 前提清单 | unit/e2e/build | `go test ./pkg/... ./cmd/... -short -count=1 && npm test && node scripts/rt-timing-report.js --self-test && sha256sum bin/home-tutor-agent-linux` | n/a（产物不入库） | ⬜ pending |
| 13-13-02 | 13 | 7 | RT-01 | T-13-27/T-13-28 | VPS 部署（用户执行）+ Claude 公网探针核验可信入口 | manual(checkpoint) | 13-CHECKPOINTS 部署记录 + /tutor/ 2xx + /health | n/a | ⬜ pending |
| 13-13-03 | 13 | 7 | RT-01~RT-05/RT-07/RT-10 | T-13-29 | 真 iPad 正式验收：完整默写课/回声自检/≥30×2 样本（含失败计数） | manual(checkpoint) | 13-CHECKPOINTS 检查记录 + JSONL 清单 | n/a | ⬜ pending |
| 13-13-04 | 13 | 7 | RT-04/RT-05 | T-13-27 | 第六周口径报告（clock_offset 校准）+ 第一周检查行如实落档 + 脱敏提交 | script | `node scripts/rt-timing-report.js --self-test && git diff --cached --stat` | ✅ 既有 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

测试基建已由已执行历史计划（13-01～13-06 与 2026-10-03 部署通道工作）建成并在工作树：`pkg/lesson`、`pkg/mimo`、`pkg/rt`（protocol/timing/conn/vad/turn 及集成测试、providers_fake）、`tests/e2e-rt` + `playwright.rt.config.js`、`tests/test-rt-*.js`、`cmd/service/rt_wiring_test.go`、`scripts/rt-timing-report.js`。

存活计划自建缺口（绿地方案，无需单独 Wave 0 计划）：

- [ ] `pkg/rt/dictation.go`（含 WordSource 接口）— 13-08 Task 1 新建
- [ ] `pkg/rt/providers_fake_test.go` 增 fakeWordSource — 13-08 Task 1（改既有）
- [ ] `pkg/rt/mimo_adapters_test.go` — 13-10 Task 2 新建（fake pybridge Executor）

无 MISSING 引用：存活计划全部 verify 指向既有或自建文件。

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| 真 iPad 本机预验收（完整默写课/回声/打断/四类口令/缺陷复核/评级映射追认） | RT-01~RT-05/RT-07/RT-10 | 真麦克风/扬声器/儿童语音不可自动化；本机通道见 13-CHECKPOINTS「本机预验收通道」段 | 13-12 Task 2（1-9 步） |
| VPS 证书部署 + 公网可信 HTTPS 入口 | RT-01 | 证书/VPS 为用户资产，Claude 无 SSH 访问权 | 13-13 Task 2（13-CHECKPOINTS 命令清单 1-8 步） |
| 真 iPad 正式验收：完整默写课 + 回声自检 + ≥30×2 时序样本 | RT-01~RT-05/RT-07/RT-10 | 真机扬声器/AEC/真实网络不可模拟；mock 与本机样本不计正式证据 | 13-13 Task 3（步骤 1-5） |
| 时序报告与第一周检查行落档 | RT-04/RT-05 | 以真实可听音频计量；跨机时钟须 clock_offset 校准口径 | 13-13 Task 4 + scripts/rt-timing-report.js |
| 故障可见与降级标注抽查 | RT-10 | 断网/错 key 的真实设备表现 | 13-12 Task 2 步骤 6；13-13 Task 3 步骤 4 |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 120s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
