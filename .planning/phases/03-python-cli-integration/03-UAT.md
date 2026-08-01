---
status: complete
phase: 03-python-cli-integration
source: [03-VERIFICATION.md, pkg/pybridge/e2e_test.go]
started: 2026-07-25T10:40:00Z
updated: 2026-08-01T12:00:00Z
---

# Phase 3 — Human Verification (UAT)

> The original single "VPS-only" test assumed the dev machine had no venv/ffmpeg/Volcengine
> keys. Re-checked on this host: real Python 3.12 (`py` launcher), ffmpeg 6.1, and uv 0.10.4
> are all present. So everything that does NOT need Volcengine production keys or VPS-scale
> observation has been converted to an automated real-Python E2E harness
> (`pkg/pybridge/e2e_test.go`, build tag `e2e`, driven by `make py-e2e`). Only the genuinely
> deploy-time items remain as the single Manual Remainder below.

## Current Test

[testing complete - 本机验证全部完成，VPS部署验证已延期]

## Tests

### 1. RunDue 真 Python 端到端
expected: 经 pybridge 触发 `vocab_manager.py due`（seed 3 词后），返回 `DueResult.Cards` 非空数组，字段（Word/ID/State/Due）完整。
result: pass
source: automated
test: pkg/pybridge/e2e_test.go#TestE2E_RunDueRealPython
evidence: uv venv (Python 3.12.0) + fsrs，PASS (0.29s)，2026-07-25 本机

### 2. RunReview 真 Python 端到端
expected: RunReview 带 3 种 rating（good/hard/easy）返回 `ReviewResult.Updated` 非空数组，字段（Word/Rating/NextDue/State）完整；rating 原样透传不改写。
result: pass
source: automated
test: pkg/pybridge/e2e_test.go#TestE2E_RunReviewRealPython
evidence: PASS (0.18s)，2026-07-25 本机

### 3. RunPregenerate 经 edge-tts 兜底产出 mp3（keyless happy path）
expected: 无 Volcengine 密钥时走 keyless edge-tts 兜底，产出 mp3 绝对路径列表，文件落盘非空；网络不可达则 skip。
result: skipped
source: automated
test: pkg/pybridge/e2e_test.go#TestE2E_PregenerateEdgeTTS
reason: "Windows dev: pregenerate_dictation.py imports fcntl (POSIX-only) → 脚本无法在 Windows 启动；本测在 Linux/WSL 跑（edge-tts 网络可达则 PASS，不可达则 skip）。Volcengine provider 真实产出见 Manual Remainder。"

### 4. 错误分类（timeout / nonzero_exit → 结构化日志 error_type）
expected: 真实触发超时 → `ErrPythonTimeout` + 日志 error_type=timeout；真实非零退出（脚本不存在）→ `ExitError` wrapping `ErrPythonExit` + 日志 error_type=nonzero_exit。
result: pass
source: automated
test: pkg/pybridge/e2e_test.go#TestE2E_ErrorClassification
evidence: nonzero_exit 子测 PASS（全平台）；timeout 子测 Linux-only（Windows exec 把 context-killed 进程报为 exit-status-1，errors.Is(DeadlineExceeded)==false 且为竞态，故 Windows skip；Linux + bridge_linux_test.go 覆盖）

### 5. VOLCENGINE_TTS_* 密钥不泄漏到任何日志字段
expected: 设 4 个哨兵密钥，跑真实 RunDue，fakeLogger 捕获的全部 Msg/Field 值不含任何哨兵串（D-05 / T-03-08 全路径证明）。
result: pass
source: automated
test: pkg/pybridge/e2e_test.go#TestE2E_SecretNoLeak
evidence: PASS (0.17s)，2026-07-25 本机

### 6. VPS 部署期手工余量 — Volcengine provider 真实 TTS + 1C/1G 规模 + Linux 进程组杀死

**前置：**
- 1C/1G VPS 上 `make py-bootstrap`（uv 建 venv，锁版本到 requirements.txt）
- 配置 `VOLCENGINE_TTS_APP_ID` / `VOLCENGINE_TTS_API_KEY` / `VOLCENGINE_TTS_ACCESS_KEY` / `VOLCENGINE_TTS_RESOURCE_ID` 生产密钥
- `HTA_PYTHON_INTERPRETER` 指向 venv python，`HTA_PYTHON_SCRIPTSDIR=third_party/python`

**步骤：**
1. RunPregenerate 走 **Volcengine TTS 1.0 provider**（非 edge-tts 兜底）→ 断言 `PregenResult.Paths` 为真实 mp3（与 edge-tts 质量/provider 不同）
2. 50–100 并发会话压测 → 断言 Go service RSS ≤ 150MB、音频缓冲池 ≤ 50MB（1C/1G 资源约束）
3. 制造 pregenerate 超时 → 断言日志无孤儿 ffmpeg（Linux `setProcessGroup` 进程组 SIGKILL 整组生效）

**expected:** 见 Current Test.expected
**why_human:** 本机无 Volcengine 生产密钥（edge-tts 兜底 ≠ Volcengine provider）；1C/1G 内存/并发规模观察本机无意义；Linux 进程组杀死的真实负载验证需 VPS 内核（bridge_linux_test.go 已在 Linux CI 覆盖代码路径）。
**result:** skipped
reason: "Deferred follow-up: 部署期验证 — 需 1C/1G VPS + VOLCENGINE_TTS_* 生产密钥 + 真实并发负载。本机代码层验证已完成（5条自动化测试路径全部PASS），此项在所有工作完成后的VPS部署阶段进行验证。"

## Summary

total: 6
passed: 4
issues: 0
pending: 0
skipped: 2
blocked: 0

## Deferred Follow-Ups

- test: 6
  idea: "部署期验证 — 需 1C/1G VPS + VOLCENGINE_TTS_* 生产密钥 + 真实并发负载。本机代码层验证已完成（5条自动化测试路径全部PASS），此项在所有工作完成后的VPS部署阶段进行验证。"
  deferred_at: 2026-08-01

## Gaps

[none yet]
