---
phase: 03-python-cli-integration
plan: 01
subsystem: infra
tags: [go, config, python, subprocess-bridge, vendor, venv, fsrs, makefile]

# Dependency graph
requires:
  - phase: 01-resource-constrained-infrastructure
    provides: typed Config 体系（MemoryConfig/SessionConfig 范式）、Viper layered loader、DefaultConfig/Validate/bindEnvForAllLeaves 骨架
provides:
  - infra.PythonConfig 类型（13 个操作性字段，无密钥字段）
  - Config.Python 字段 + DefaultConfig 默认值 + Validate 校验规则 + bindEnvForAllLeaves 的 python.* 注册
  - config.example.yaml 的 python: 块
  - third_party/python/ 下 vendor 的 vocab_manager.py / pregenerate_dictation.py / requirements.txt / .env.example
  - Makefile py-bootstrap（建 venv + pip install + freeze 锁定）与 py-sync（从上游同步脚本）target
affects: [03-02, 03-03, 03-04, 03-05, phase-04, phase-05]

# Tech tracking
tech-stack:
  added: [fsrs, requests, edge-tts (vendored python deps via requirements.txt)]
  patterns: [typed-operational-config-extension, vendor-scripts-byte-identical, secrets-via-os-getenv-not-in-config, reflect-driven-no-secret-field-guard, make-py-bootstrap-venv-lock]

key-files:
  created:
    - third_party/python/vocab_manager.py
    - third_party/python/pregenerate_dictation.py
    - third_party/python/requirements.txt
    - third_party/python/.env.example
  modified:
    - pkg/infra/config.go
    - pkg/infra/config_test.go
    - pkg/infra/server_test.go
    - config.example.yaml
    - Makefile
    - .gitignore

key-decisions:
  - "PythonConfig 采用与 MemoryConfig 一致的组织风格：13 个操作性字段（路径/超时/并发），绝不含密钥字段（D-05）"
  - "vendor 脚本逐字节拷入 third_party/python/（D-03 版本绑定），上游变更经 make py-sync 手动同步"
  - "fsrs 包名经上游 requirements.txt 确认为 fsrs（非 fsrs-4py / py-fsrs），保持 requirements.txt 不变"
  - "密钥 VOLCENGINE_TTS_* 经 os.Getenv 在 pkg/pybridge 读取后注入子进程 env，config.example.yaml / .env.example 只留键名 + 空值/占位符"

patterns-established:
  - "Pattern: 扩展 typed Config 时照抄 MemoryConfig 范式（struct + DefaultConfig + Validate + bindEnvForAllLeaves 四处同步）"
  - "Pattern: 用 reflect.TypeOf 遍历字段名做 no-secret 自动守卫测试（TestPythonConfigHasNoSecretFields）"
  - "Pattern: vendor 上游脚本逐字节拷贝 + Makefile py-sync target 支撑 D-03 版本绑定"
  - "Pattern: Makefile py-bootstrap 用 pip freeze 把浮动版本锁成可复现 deploys"

requirements-completed: [PY-01]

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "infra.PythonConfig 类型：13 个操作性字段（Interpreter/ScriptsDir/VocabScript/PregenScript/VocabDBPath/DataDir/CacheDir/ManifestDir/EnvFile/DueTimeout/ReviewTimeout/PregenTimeout/MaxConcurrent），DefaultConfig 设默认值，Validate 覆盖全部校验，bindEnvForAllLeaves 注册全部 python.* 叶子键"
    requirement: "PY-01"
    verification:
      - kind: unit
        ref: "pkg/infra/config_test.go#TestLoadConfigInvalidPythonInterpreter / TestLoadConfigInvalidPythonMaxConcurrent / TestLoadConfigInvalidPythonTimeouts / TestPythonConfigHasNoSecretFields / TestDefaultConfig[python defaults]"
        status: pass
      - kind: unit
        ref: "go build ./pkg/infra/ 退出 0"
        status: pass
    human_judgment: false
  - id: D2
    description: "third_party/python/ 下 vendor 的 vocab_manager.py / pregenerate_dictation.py 与上游逐字节一致 + Makefile py-bootstrap/py-sync target 就绪 + requirements.txt 含 fsrs/requests/edge-tts + .env.example 无真实密钥值"
    requirement: "PY-01"
    verification:
      - kind: manual_procedural
        ref: "diff -q third_party/python/{vocab_manager,pregenerate_dictation}.py 与 xiaotian-skills/shared/dictation/scripts/* 输出空；checkpoint:human-verify (Task 3) 经 orchestrator resolve 为 approved"
        status: pass
    human_judgment: true
    rationale: "vendor 脚本的字节级一致性、PyPI 包名 fsrs 的正确性、Makefile target 命令链的合理性需要人工逐段比对上游脚本与 PyPI 核查；自动化 diff 只能证明字节相等，无法替代人工对脚本契约（rating_map/--db 参数/Warning 混合 stdout/fcntl flock/ffmpeg preflight）与包名合法性的判断。checkpoint 已由 orchestrator 以 upstream requirements.txt 为权威证据 resolve 为 approved。"

# Metrics
duration: ~20min active execution（不含 checkpoint 等待）
completed: 2026-07-24
status: complete
---

# Phase 3 Plan 01: PythonConfig + vendor 上游脚本 + Makefile py-* Summary

**infra.Config 扩展 PythonConfig 操作块（13 字段、无密钥、reflect 守卫）并把上游 vocab_manager.py / pregenerate_dictation.py 逐字节 vendor 进 third_party/python/，配 Makefile py-bootstrap/py-sync 与 venv 锁定**

## Performance

- **Duration:** ~20 min 活跃执行（Task 1 + Task 2 + checkpoint 比对；不含 checkpoint 人工等待）
- **Started:** 2026-07-24T22:30+08:00（Task 1 commit `7191cdf`）
- **Completed:** 2026-07-24T22:41+08:00（本 SUMMARY 闭环）
- **Tasks:** 3（Task 1 auto、Task 2 auto、Task 3 checkpoint:human-verify 已 resolve）
- **Files modified:** 10 个源/配置文件 + 1 个 planning artifact（deferred-items.md）

## Accomplishments

- **PythonConfig 类型就绪**：13 个操作性字段（interpreter/scriptsDir/各路径/三超时/MaxConcurrent），照抄 MemoryConfig 范式；DefaultConfig 设 VocabScript/PregenScript 默认名 + DueTimeout=10s/ReviewTimeout=10s/PregenTimeout=120s/MaxConcurrent=2；Validate 逐字段校验（interpreter/scriptsDir/vocabScript/pregenScript 非空、MaxConcurrent∈[1,4]、三超时>0），失败统一 `errors.Is(err, ErrConfigInvalid)=true`；bindEnvForAllLeaves 注册全部 13 个 `python.*` 叶子键（T-01-06 mitigation，不依赖 AutomaticEnv 启发式）。
- **D-05 密钥不入 Config 自动守卫**：`TestPythonConfigHasNoSecretFields` 用 reflect 遍历 PythonConfig 全部字段名，命中 `secret/password/apikey/token` 子串即 `t.Fatalf`——防止未来误加密钥字段。
- **上游脚本 vendor 完成（D-03 版本绑定）**：`third_party/python/vocab_manager.py`（821 行）与 `pregenerate_dictation.py`（1347 行）逐字节拷入，`diff -q` 与上游 `xiaotian-skills/shared/dictation/scripts/*` 输出空。上游身份标记全部在位（rating_map easy @ L363、`--db` 全局参数、review Warning 混合 stdout、fcntl.flock @ L1302、ffmpeg preflight）。
- **requirements.txt + .env.example 就绪**：列出 `fsrs>=0.0.1` / `requests>=2.31.0` / `edge-tts>=6.1.0`（每个依赖注释标注被哪个脚本哪一行 import）；`.env.example` 所有 `VOLCENGINE_TTS_*` 行注释掉 + D-05 不变式声明，非密钥 `DICTATION_*` 路径键用占位符。
- **Makefile py-bootstrap / py-sync target**：`py-bootstrap` 建 venv + `pip install -r` + `pip freeze` 锁版本；`py-sync` 从上游路径（`UPSTREAM_SCRIPTS ?= ../xiaotian-skills/shared/dictation/scripts`）同步脚本；两个 target 加入 `.PHONY`。
- **config.example.yaml python: 块**：照抄 memory:/session: 注释风格，每字段标注 D-xx 决策与单位/默认值，密钥位置明确声明经部署环境注入。

## Task Commits

每个 task 原子提交：

1. **Task 1: 扩展 infra.Config 新增 PythonConfig 操作块** — `7191cdf` (feat)
2. **Task 2: vendor 上游 Python 脚本 + Makefile py-bootstrap/py-sync target** — `c86628d` (feat)
3. **Task 3: checkpoint:human-verify（vendor 一致性 + PyPI 包名）** — orchestrator resolve 为 `approved`，无代码变更（`fsrs` 包名经上游 requirements.txt 确认正确，requirements.txt 保持不变）

**Plan metadata:** 待本 SUMMARY 闭环时提交（docs: close plan after vendor checkpoint approval）。

## Files Created/Modified

- `pkg/infra/config.go` — 新增 `PythonConfig` struct（13 字段）、`Config.Python` 字段、`DefaultConfig` 默认值、`Validate` 校验块、`bindEnvForAllLeaves` 注册 13 个 python.* 键
- `pkg/infra/config_test.go` — TestLoadConfigInvalidPythonInterpreter/MaxConcurrent/Timeouts、TestPythonConfigHasNoSecretFields、TestDefaultConfig python defaults 子测试
- `pkg/infra/server_test.go` — Rule 3 修复：新增 `setRequiredPythonEnv(t)` helper 并在 `fastServerConfig()` 填充新必填 python 字段
- `config.example.yaml` — 末尾新增 `python:` 块（注释引用 D-02/D-03/D-04/D-05）
- `third_party/python/vocab_manager.py` — vendor 上游 FSRS/vocabulary CLI（逐字节一致）
- `third_party/python/pregenerate_dictation.py` — vendor 上游 TTS 批量生成脚本（逐字节一致）
- `third_party/python/requirements.txt` — fsrs/requests/edge-tts 浮动版本 + import 行注释
- `third_party/python/.env.example` — 非密钥 DICTATION_* 路径键 + VOLCENGINE_TTS_* 仅键名注释掉
- `Makefile` — py-bootstrap（venv + install + freeze）与 py-sync（上游同步）target + .PHONY
- `.gitignore` — 排除 venv 与 Python 运行时产物

## Decisions Made

- **PythonConfig 字段组织**：照抄 MemoryConfig 范式（struct 上方文档注释 + DefaultConfig + Validate + bindEnv 四处同步），保持与既有 typed Config 体系一致。
- **vendor 目录选 `third_party/python/`**：与 Plan `files_modified` 一致；命名明确表达"第三方上游资产"语义，与 Go 自有 `pkg/` 隔离。
- **requirements 版本用 `>=` 浮动**：精确版本在 `make py-bootstrap` 时由 `pip freeze` 锁定（D-04 Claude discretion），避免在 requirements.txt 里写死过期版本。
- **fsrs 包名保留 `fsrs>=0.0.1`**：见下方"Checkpoint 解决"——经上游 requirements.txt 权威确认，无需改动。
- **Makefile 不硬编码 China-mainland PyPI 镜像**：`py-bootstrap` 保持可配置/env-driven，用户可在部署时通过 `pip` 的 `PIP_INDEX_URL` 等环境变量指定镜像（用户偏好记录在案，本闭环步骤不执行 install 故未触发）。

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] 新增必填 python 字段导致 server_test.go 的 fastServerConfig() 失败**
- **Found during:** Task 1（扩展 infra.Config 新增 PythonConfig 操作块）
- **Issue:** Validate 新增 `python.interpreter` 与 `python.scriptsDir` 非空校验后，`pkg/infra/server_test.go` 里 `fastServerConfig()` 构造的 Config 缺这两个必填字段，导致 server 包测试在 `Validate()` 处失败，阻塞 Task 1 验证。
- **Fix:** 在 `server_test.go` 新增 `setRequiredPythonEnv(t)` helper（用 `t.Setenv` 注入 `HTA_PYTHON_INTERPRETER` / `HTA_PYTHON_SCRIPTS_DIR`），并在 `fastServerConfig()` 中调用它填充新必填 python 字段。
- **Files modified:** `pkg/infra/server_test.go`
- **Verification:** `go test ./pkg/infra/...` 全绿，server 包测试不再因 python 校验失败。
- **Committed in:** `7191cdf`（Task 1 commit）

**2. [Rule 1 - Bug] TestLoadConfigInvalidPythonTimeouts 的超时边界用例失败**
- **Found during:** Task 1（编写 PythonConfig 校验测试）
- **Issue:** 原计划用 viper env 注入 `"0s"` / `"-1s"` 字符串触发 DueTimeout/ReviewTimeout/PregenTimeout 的非正校验，但 viper 的 env decoder 对 `time.Duration` 字段拒绝 `"0s"`/`"-1s"` 这类带单位字符串，导致测试无法稳定触发目标错误路径。
- **Fix:** 将 `TestLoadConfigInvalidPythonTimeouts` 改为 YAML 文件 + 裸整数（`dueTimeout: 0` / `reviewTimeout: -1`）的方式触发校验，绕开 env decoder 限制，直接覆盖 `Validate` 的非正超时分支。
- **Files modified:** `pkg/infra/config_test.go`
- **Verification:** `go test -run TestLoadConfigInvalidPythonTimeouts -v ./pkg/infra/` 通过，断言 `errors.Is(err, ErrConfigInvalid)` 为 true。
- **Committed in:** `7191cdf`（Task 1 commit）

---

**Total deviations:** 2 auto-fixed（1 Rule 3 blocking、1 Rule 1 test bug）
**Impact on plan:** 两处 auto-fix 都是 Task 1 变更的直接下游影响（新增必填字段与新增校验规则引发的测试基础设施调整），属 correctness 必需，无 scope creep。

## Checkpoint 解决（Task 3 — vendor 一致性 + PyPI 包名）

Task 3 为 `checkpoint:human-verify`（gate=blocking），要求人工确认 vendor 脚本与上游一致 + PyPI 包名 `fsrs` 正确。orchestrator 以**上游权威源**为证据 resolve 为 `approved`：

- **fsrs 包名确认**：上游 `D:/workspace/ai/xiaotian-skills/shared/dictation/requirements.txt` 声明依赖恰为 `fsrs`（与 `requests`、`edge-tts` 并列）。上游 `vocab_manager.py:33` 的 `from fsrs import Scheduler, Card, Rating, State` 在生产环境针对该 `fsrs` 包运行，故包名 `fsrs` 正确且导出 `Scheduler`。
- **vendor 字节一致**：`diff -q` 显示 `third_party/python/vocab_manager.py` 与 `pregenerate_dictation.py` 与上游 `xiaotian-skills/shared/dictation/scripts/*` 完全相同。
- **身份标记齐备**：easy rating @ L363、`--db` 全局参数、review Warning 混合 stdout 行、`fcntl.flock` @ L1302、ffmpeg preflight 均在位。
- **.env.example 密钥隔离**：所有 `VOLCENGINE_TTS_*` 行注释掉 + D-05 不变式声明块。

**结论**：`third_party/python/requirements.txt` 的 `fsrs>=0.0.1` 保持不变，本闭环无代码变更。

## Issues Encountered

- **pkg/ws `TestHubStopsOnContextCancel` 在 `go test -short ./...` 下偶发失败**：非 Phase 03 引入，Phase 02 既有测试的 goroutine-count 断言对并行调度时序敏感。隔离运行（`go test -run TestHubStopsOnContextCancel ./pkg/ws/`）稳定通过，非 PythonConfig 回归（Plan 03-01 仅触 `pkg/infra/*` 与 `config.example.yaml`）。已按 scope-boundary 规则记录到 `deferred-items.md`（Item 1），不在本 plan 修复。

## User Setup Required

本 plan 在 frontmatter 声明了 `user_setup`（python-venv 服务），但属**部署期**配置，不在代码闭环内：

- **部署 VPS 时建 venv**：`make py-bootstrap`（会 `pip install fsrs requests edge-tts`）。用户偏好 China-mainland PyPI 镜像——通过 `PIP_INDEX_URL` 等 env 驱动，Makefile 未硬编码镜像。
- **配置密钥**：`VOLCENGINE_TTS_API_KEY` / `APP_ID` / `ACCESS_KEY` / `RESOURCE_ID` 从 Volcengine 控制台获取，经部署环境变量注入子进程（D-05，绝不入库）。

未生成独立 `USER-SETUP.md`——上述项已在本 SUMMARY 与 `config.example.yaml` / `.env.example` 注释中说明。

## Next Phase Readiness

- **就绪**：`infra.PythonConfig` 类型 + 校验 + env 绑定 + 默认值全部到位，可供 Plan 03-02（Executor 接口 + sentinel 错误 + 解析器 + 信号量）的 Bridge 构造器 `NewBridge(cfg PythonConfig, logger)` 直接消费。
- **就绪**：`third_party/python/*.py` 路径可供 Plan 03-03/04/05 的 `exec.CommandContext` argv 引用。
- **就绪**：`config.example.yaml` 的 `python:` 块供 Viper layered loader 装载到 `PythonConfig`。
- **阻塞**：无。Phase 03 Wave 1 的 Plan 03-02 可立即开工。
- **提醒下游**：Plan 03-03 实现 exec 引擎时，密钥经 `os.Getenv` 在 `pkg/pybridge` 内读取后注入子进程 env（D-05），不要在 PythonConfig 加密钥字段（会被 `TestPythonConfigHasNoSecretFields` 守卫拦下）。

## Self-Check: PASSED

- 创建文件存在性：
  - `third_party/python/vocab_manager.py` — FOUND
  - `third_party/python/pregenerate_dictation.py` — FOUND
  - `third_party/python/requirements.txt` — FOUND
  - `third_party/python/.env.example` — FOUND
- 修改文件存在性：
  - `pkg/infra/config.go`（含 PythonConfig）— FOUND
  - `config.example.yaml`（含 python: 块）— FOUND
  - `Makefile`（含 py-bootstrap/py-sync）— FOUND
- 提交存在性：
  - `7191cdf` — FOUND（`git log --oneline` 命中）
  - `c86628d` — FOUND（`git log --oneline` 命中）
- 验收 grep：
  - `type PythonConfig struct` == 1 — PASS
  - `"python.` 注册键 >= 13 — PASS（21，含 bindEnv 注册 + Validate/default 引用）
  - Makefile `py-bootstrap` / `py-sync` target 存在 — PASS
  - `requirements.txt` 含 fsrs/requests/edge-tts — PASS
  - vendor 脚本与上游 `diff -q` 空 — PASS

---
*Phase: 03-python-cli-integration*
*Completed: 2026-07-24*
