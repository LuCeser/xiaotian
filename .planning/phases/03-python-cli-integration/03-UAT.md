---
status: testing
phase: 03-python-cli-integration
source: [03-VERIFICATION.md]
started: 2026-07-25T10:40:00Z
updated: 2026-07-25T10:40:00Z
---

# Phase 3 — Human Verification (UAT)

> Code-level verification PASSED (4/4 must-haves verified, 29 tests green, cross-OS builds clean).
> This single item requires deploy-time human testing on the 1C/1G VPS before Phase 3 can be marked complete.

## Current Test

number: 1
name: VPS 端到端 — pybridge 触发 vocab_manager.py 与 pregenerate_dictation.py 真运行时
expected: |
  在 VPS 上 `make py-bootstrap` 建立 venv（pip 安装 fsrs/requests/edge-tts，必要时用中国大陆镜像源
  如 `-i https://pypi.tuna.tsinghua.edu.cn/simple`），配置 `VOLCENGINE_TTS_*` 密钥后，经 pybridge
  触发：
    - `RunDue` → 返回 `DueResult.Cards`（数组）
    - `RunReview` → 返回 `ReviewResult.Updated`（数组）
    - `RunPregenerate` → 返回 `PregenResult.Paths`（mp3 绝对路径列表）
  超时 / 非零退出 / 空 stdout 经 `infra.Logger` 落结构化日志（component=pybridge，
  error_type=timeout/nonzero_exit/other）；`VOLCENGINE_TTS_*` 密钥不出现在任何日志字段。
awaiting: user response（部署期执行）

## Tests

### 1. VPS 端到端 — pybridge 触发 vocab_manager.py 与 pregenerate_dictation.py 真运行时

**前置：**
- VPS 上 `make py-bootstrap` 建 venv（fsrs / requests / edge-tts；pip 用中国大陆镜像源，例如
  `-i https://pypi.tuna.tsinghua.edu.cn/simple`）
- 配置 `VOLCENGINE_TTS_APP_ID` / `VOLCENGINE_TTS_API_KEY` / `VOLCENGINE_TTS_ACCESS_KEY`（D-05：经
  `os.Getenv` 透传给子进程，不入 Config/日志）
- `HTA_PYTHON_INTERPRETER` 指向 venv 的 python，`HTA_PYTHON_SCRIPTSDIR=third_party/python`

**步骤：**
1. 触发 `RunDue`（选词）→ 断言返回 `DueResult.Cards` 非空数组
2. 触发 `RunReview`（FSRS 反馈，rating∈{again,good,hard,easy}）→ 断言返回 `ReviewResult.Updated`
3. 触发 `RunPregenerate`（批量 TTS，`--words` JSON 数组）→ 断言返回 `PregenResult.Paths`（mp3 绝对路径）
4. 制造一次超时 / 非零退出 → 断言 `infra.Logger` 落结构化日志（含 `error_type`）
5. 全程检查日志 → 断言 `VOLCENGINE_TTS_*` 密钥不出现在任何字段（D-05 / T-03-08）

**expected:** 见 Current Test.expected
**why_human:** Windows dev 机无 venv / ffmpeg / Volcengine 密钥；CI 用 POSIX shell fake
（`internal/pytest/fake_*.sh`）+ 黄金 fixture 验证桥的解析 / 超时 / 错误分类逻辑，但真 Python 解释器在
venv 内对 vendored 脚本的端到端行为只能由部署 UAT 触达（vendored 脚本已与上游逐字节 `diff -q` 为空，
per 03-01 checkpoint）。
**result:** [pending]

## Summary

total: 1
passed: 0
issues: 0
pending: 1
skipped: 0
blocked: 0

## Gaps
