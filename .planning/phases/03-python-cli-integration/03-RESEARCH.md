# Phase 3: Python CLI Integration - Research

**Researched:** 2026-07-24
**Domain:** Go ↔ Python 子进程桥接（subprocess bridge）、os/exec 进程组管理、JSON stdout 契约解析
**Confidence:** HIGH

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions（锁定决策，研究 THESE 而非替代方案）

**Python Process Model**
- **D-01: spawn-per-call。** 每次调用 fork 一个全新 Python 进程，结束即释放。Python 内存是瞬时的（无常驻 sidecar），保护 1C/1G VPS 上的 150MB Go 预算。冷启动成本（vocab_manager ~100-200ms；pregenerate 因 `edge-tts`/`requests` 导入更重）可接受，因为两个脚本都是低频——大致每个听写会话一次（pregenerate 接收 `--words` JSON 数组，整批一次生成）。
- **D-02: 全局有界信号量上限。** 一个小信号量（常量 N，由 research/planning 决定）限制同时运行的 Python 子进程，使 50-100 并发会话无法 fork-bomb 1C/1G 机器。溢出排队，由 Phase 1 worker-pool/backpressure 处理（复用 D-10 worker pool、D-72 backpressure）。选"有上限并发"而非"串行"，为吞吐量。

**Python Asset Location & Environment**
- **D-03: vendor 两个脚本进本仓库。** `vocab_manager.py` 和 `pregenerate_dictation.py` 拷入仓库（精确目录由 Claude 定）。绑定脚本版本到 Go 代码，部署可复现，移除对外部 `xiaotian-skills` checkout 的运行时依赖。上游变更手动同步（Make target）。
- **D-04: venv 承载 Python 解释器与依赖。** virtualenv 持有 `requests`、`edge-tts` 等；typed Config 指向 venv 的 python。可复现、隔离，不污染系统 Python。依赖锁定。
- **D-05: 密钥经 `os.Getenv` 传递，绝不经 Config。** 一致于 Phase 1 D-24：桥读取 `VOLCENGINE_TTS_API_KEY` / `VOLCENGINE_TTS_APP_ID` + `VOLCENGINE_TTS_ACCESS_KEY` 并转发给子进程。Config 结构体不新增 `APIKey`/`Secret`/`Password` 字段——只有操作性字段（路径、解释器）。

**TTS Delivery Boundary（Phase 3 → 6 接缝）**
- **D-06: pregenerate 返回文件路径 / manifest。** `pregenerate_dictation.py` 把 mp3 写入缓存目录并发出 manifest；Go 调用返回后持有路径/manifest 列表（非音频字节）。Phase 6 之后读这些文件并切成 640B 帧。
- **D-07: "full streaming TTS" 重定义为交付层属性。** PROJECT.md 的约束指 Phase 6 把音频帧流式推给客户端；不要求逐帧实时生成。生成用 pregenerate 的预生成 + 缓存模型（Phase 3），Go 服务不自建缓存层。
- **D-08: 阶段接缝严格。** mp3 → PCM → 640B 帧切片与 WebSocket 流式是 Phase 6；决定何时触发生成（eager 批 vs lazy 逐词）是 Phase 7。

**FSRS / DB Ownership（Phase 3 → 4/5 接缝）**
- **D-09: 长期复用 Python FSRS + SQLite。** `vocab_manager.py` 的内置 FSRS 引擎及其 SQLite DB（`DICTATION_DB_PATH` / `vocab.db`）是永久引擎。Phase 5 是 Go 服务层编排，调用本桥的 `due`/`review`——不在 Go 里重写 FSRS。因此 Phase 3 桥是持久接口，必须设计良好、测试充分，非一次性。
- **D-10: Phase 4 必须与 vocab_manager 现有 schema 协调。** `vocab_manager` 的 `vocab.db` 是 FSRS/词汇数据的真值源。

**Bridge Scope**
- **D-11: 通用包装器，三条交付路径。** 桥构建为能调用任意 `vocab_manager.py` 子命令与 `pregenerate_dictation.py` 的通用包装器，但 Phase 3 只实现并测试 `due`、`review`、`pregenerate` 三条路径。次要命令留给后续阶段。

### Claude's Discretion（研究自由度）
- 并发信号量上限 N 的具体值与子进程超时。
- 精确 vendor 目录位置与依赖版本锁定策略。
- 新增 `python.*` 操作性 Config 字段（解释器、脚本目录、data/cache/manifest/db 路径）。
- `Executor` 接口设计（构造器 DI D-42/D-47，手动 fake D-48）。
- vocab_manager 输出契约（stdout JSON；错误经 `print("Error: ...")` + 非零退出码）的解析/归一化与错误分类。
- stdout/stderr 分离与桥发出的结构化日志字段。

### Deferred Ideas（范围外，完全忽略）
- 生成时机（eager 整批 vs lazy 逐词）→ Phase 7。
- mp3 → PCM → 640B 帧切片 + WS 流式 → Phase 6。
- vocab_manager 次要命令（add/stats/list/export/rename-unit/delete-words/merge-units）→ Phase 9 及以后。
- DB schema 统一细节 → Phase 4（受 D-10 约束）。
- FSRS 在 Go 中重写 → 明确不计划（D-09）。
- `easy` rating 支持 → 已在本次研究验证（见下）；如需映射决策属 Phase 10。
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| PY-01 | 集成现有 `vocab_manager.py` CLI | 通用 `Executor` 包装器（`pkg/pybridge`）经 `os/exec` spawn venv 中的 python，vendor 脚本路径与 `--db` 全局参数经 typed Config 注入；契约见"Architecture Patterns"。 |
| PY-02 | 执行 `due` 子命令返回选词结果 | `RunDue(ctx, DueOpts)` → 解析 stdout JSON 数组（`ensure_ascii=False, indent=2`）；处理 `--peek`/`--limit`/`--unit`/`--category`/`--exclude`/`--fetch-lock-hours` 透传；fetch-lock 副作用契约见 Pattern 2。 |
| PY-03 | 执行 `review` 子命令带反馈参数 | `RunReview(ctx, ReviewOpts)` → `--results` JSON 字符串或 `--file`（大负载走文件避免命令行长度/审批问题）；解析 `{"updated":[...]}`；rating 透传 `good\|hard\|again\|easy`（4 种均已验证支持）。 |
| PY-04 | 集成 `pregenerate_dictation.py` 生成 TTS | `RunPregenerate(ctx, PregenOpts)` → `generate --words '[...]' --language ...`；stdout 按行解析 mp3 绝对路径（非 JSON）；stderr 为日志；ffmpeg/disk 前置检查；fcntl flock 全局单实例契约见 Pitfall 6。 |
| SC-4 | 子进程错误捕获并送入日志系统 | 5 类失败模式分类（非零退出/超时/JSON 解析/空 stdout/stderr 非空但 exit 0）映射到 Zap 结构化字段（command/args/exit_code/duration_ms/error_type）；复用 `infra.Logger` + `infra.F()`/`infra.ErrField()`。 |
</phase_requirements>

## Summary

本阶段交付一个 **Go ↔ Python 子进程桥**（`pkg/pybridge`），让 Go 服务以 spawn-per-call（D-01）方式执行 vendor 进来的 `vocab_manager.py`（`due`/`review`）与 `pregenerate_dictation.py`（`generate`），解析其 stdout 契约，并把子进程错误纳入既有的 Zap 日志体系（成功标准 4）。它是后续所有阶段触达既有 Python 业务逻辑（FSRS 引擎、词汇存储、TTS 生成）的持久、可复用访问层（D-09：FSRS 长期留在 Python）。

**最关键的发现：两个 Python CLI 的 stdout 契约不对称，必须分别处理。** `vocab_manager.py` 把成功 JSON、`Warning:` 警告、`Error:` 错误**全部打到 stdout**（默认 `print`，不走 stderr），靠退出码区分成败，`review` 的警告行还与最终 JSON 混在同一流；而 `pregenerate_dictation.py` 是干净的 stdout=mp3 路径（按行，非 JSON）/ stderr=日志 分离。桥不能用统一假设对待两者——`due`/`review` 必须"从混合 stdout 里提取最后一个 JSON 对象 + 看退出码"，`pregenerate` 必须"stdout 按行切路径 + stderr 整体进日志"。这一不对称性是本阶段最大的正确性风险，已从源码逐行确认 `[VERIFIED: 源码 vocab_manager.py / pregenerate_dictation.py]`。

**第二个关键发现：Go `exec.CommandContext` 默认只杀直接子进程，不杀进程组。** 已从本地 Go 1.26 stdlib 源码（`GOROOT/src/os/exec/exec.go` 第 251-303 行）确认：默认 `Cancel` 调用 `Process.Kill()` 只命中直接子进程，且 `WaitDelay` 默认为 0 时 I/O 管道会一直读到 EOF——"which might not occur until orphaned subprocesses of the command have also closed their descriptors"。`pregenerate_dictation.py` 内部会 spawn `ffmpeg` 子进程，若不设进程组杀死，超时后会留下孤儿 ffmpeg。必须用 `syscall.SysProcAttr{Setpgid: true}` + 自定义 `Cancel: syscall.Kill(-pid, SIGKILL)` + 非零 `WaitDelay` `[VERIFIED: Go stdlib + GOROOT/src/syscall/exec_linux.go]`。

**Primary recommendation:** 构建 `pkg/pybridge` 包，暴露 `Executor` 接口（`RunDue`/`RunReview`/`RunPregenerate`）+ 手动 fake；内部用 `exec.CommandContext` + 进程组杀死 + `WaitDelay`；为 `due`/`review` 写"提取末尾 JSON"解析器、为 `pregenerate` 写"按行路径"解析器；用 `chan struct{}`（容量 N=2）做全局信号量（D-02）；扩展 `infra.Config` 加 `Python` 操作块（不含密钥，D-05）；全部错误经 typed sentinel 包裹 exit_code/stderr 并经 `infra.Logger` 落结构化日志。

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| 子进程 spawn / 超时 / 进程组杀死 | API / Backend（Go 服务） | — | 进程生命周期是 Go 服务职责；Python 是被调用的瞬态工作者（D-01） |
| FSRS 算法与调度 | Python（`vocab_manager.py`） | — | D-09 锁定：FSRS 永久留在 Python，Go 不重写 |
| 词汇/FSRS 持久化 | Database（SQLite `vocab.db`） | — | `vocab_manager` 的 DB 是真值源（D-10）；Go 不直接读写 |
| 选词（due）执行 | API / Backend（Go 桥） | Python（计算） | Go 透传参数并解析结果；Python 计算 due 集合 + fetch-lock 副作用 |
| 反馈（review）执行 | API / Backend（Go 桥） | Python（FSRS 更新） | Go 透传 ratings；Python 更新 FSRS 状态并写 DB |
| TTS 音频生成 | Python（`pregenerate_dictation.py`） | — | 生成完全在 Python 侧（含 ffmpeg、Volcengine API、缓存） |
| 音频字节读取/切片/流式 | —（Phase 6） | — | D-08：Phase 3 只产 mp3 文件路径，不碰字节 |
| 生成时机编排 | —（Phase 7） | — | D-08：eager/lazy 决策属会话编排 |
| 错误捕获与结构化日志 | API / Backend（Go 桥） | infra（Logger） | 成功标准 4；复用 Phase 1 `infra.Logger` |
| 密钥注入 | API / Backend（Go 桥，env 透传） | — | D-05：`os.Getenv` 读 `VOLCENGINE_TTS_*` 注入子进程 env，不入 Config |

## Standard Stack

### Core（全部 Go stdlib + 既有依赖——本阶段不引入任何新 Go 包）

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `os/exec` | go1.26 stdlib | spawn Python 子进程、`CommandContext`、`Output()`/`CombinedOutput()`、`Start()`/`Wait()` | 子进程执行的唯一标准方式 `[VERIFIED: GOROOT/src/os/exec/exec.go]` |
| `syscall` | go1.26 stdlib | `SysProcAttr{Setpgid:true}` 进程组、`Kill(-pgid, SIGKILL)` 杀整组（Linux） | 进程组杀死是防孤儿 ffmpeg 的唯一可靠手段 `[VERIFIED: GOROOT/src/syscall/exec_linux.go 第 75-91,393-395 行]` |
| `context` | go1.26 stdlib | 每次调用超时 + 取消传播给 `CommandContext` | <1.5s 延迟目标与 D-12 context 生命周期 |
| `encoding/json` | go1.26 stdlib | 解析 `due`/`review` 的 stdout JSON（`json.Unmarshal` 进 typed struct） | 标准；脚本输出已是 JSON |
| `os` | go1.26 stdlib | `os.Getenv` 读密钥（D-05）、构造子进程 env | 密钥不进 Config 的实现方式 |
| `sync` / `chan struct{}` | go1.26 stdlib | 全局有界信号量（D-02） | 无需第三方 semaphore 库 |
| `time` | go1.26 stdlib | 超时、duration 计费（日志字段 duration_ms） | — |
| `github.com/pkg/errors` | v0.9.1（已在 go.mod） | `errors.Wrap`/`Is` 包裹 typed sentinel | Phase 1 既定错误包装约定 |
| `go.uber.org/zap` | v1.28.0（已在 go.mod） | 经 `infra.Logger` 接口落结构化日志 | Phase 1 已建（`pkg/infra/logging.go`） |
| `github.com/spf13/viper` | v1.21.0（已在 go.mod） | 经 `infra.Config` 加载 `python.*` 块 | Phase 1 已建（`pkg/infra/config.go`） |

### Supporting（Python venv 侧——非 Go 模块，由 D-04 venv 管理）

| Package | Purpose | When to Use |
|---------|---------|-------------|
| `fsrs` (py) | `vocab_manager.py` `from fsrs import Scheduler, Card, Rating, State` —— FSRS 引擎 | venv 必装，否则 vocab_manager 无法启动 `[VERIFIED: vocab_manager.py 第 33 行]` |
| `requests` (py) | `pregenerate_dictation.py` Volcengine HTTP TTS | venv 必装 `[VERIFIED: pregenerate_dictation.py 第 167 行]` |
| `edge-tts` (py) | TTS fallback provider | venv 必装（fallback 路径）`[VERIFIED: pregenerate_dictation.py 第 275 行]` |
| `ffmpeg` (系统二进制) | 静音裁剪、片段拼接、静音文件生成 | 系统级依赖，pregenerate preflight 强制检查 `[VERIFIED: pregenerate_dictation.py 第 1012-1013 行]` |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| spawn-per-call (D-01) | 常驻 Python JSON-RPC sidecar | D-01 已否决：~40-80MB 常驻挤压 150MB 预算 |
| 自建 TTS 缓存层 | pregenerate 既有缓存 | D-07 已决：Go 不建缓存层，复用 Python 侧缓存 |
| 在 Go 里重写 FSRS | 调 Python | D-09 已决：长期复用 Python FSRS |
| `exec.Command` + 手动 goroutine 超时 | `exec.CommandContext` | CommandContext 是标准且与 ctx 取消集成，无需手写 |

**Installation（Go 侧无新包；Python venv 侧）：**
```bash
# Go 侧：无需 go get，全部依赖已在 go.mod（Phase 1/2 引入）
go mod tidy  # 仅整理

# Python venv 侧（部署时，由 Make target 驱动，见"Recommended Project Structure"）
python3 -m venv third_party/python/.venv
third_party/python/.venv/bin/pip install fsrs requests edge-tts
third_party/python/.venv/bin/pip freeze > third_party/python/requirements.txt
```

**Version verification:** Go 依赖已在 `go.mod` 确认（`go 1.26`，pkg/errors v0.9.1，zap v1.28.0，viper v1.21.0）。Python 包版本应在 venv 建立时 `pip freeze` 锁定到 `third_party/python/requirements.txt`（D-04 版本锁定策略，Claude discretion）。

## Package Legitimacy Audit

> 本阶段**不引入任何新 Go 模块**。下表仅审计新增/复用的依赖。

| Package | Registry | Age | Downloads | Source Repo | Verdict | Disposition |
|---------|----------|-----|-----------|-------------|---------|-------------|
| `os/exec`/`syscall`/`encoding/json`/`context`/`os`/`sync`/`time` | Go stdlib | — | — | go.googlesource.com/go | OK (stdlib) | Approved — 不可移除 |
| `github.com/pkg/errors` | Go | 已在 go.mod | — | github.com/pkg/errors | OK | Approved（Phase 1 引入） |
| `go.uber.org/zap` | Go | 已在 go.mod | — | github.com/uber-go/zap | OK | Approved（Phase 1 引入） |
| `github.com/spf13/viper` | Go | 已在 go.mod | — | github.com/spf13/viper | OK | Approved（Phase 1 引入） |
| `fsrs` (py) | PyPI | `[ASSUMED]` | `[ASSUMED]` | `[ASSUMED]` | `[ASSUMED]` | 既有上游脚本依赖；非 Go 模块；规划器在 venv 建立任务加 `checkpoint:human-verify` 确认 PyPI 上的正确包名（`fsrs` vs `fsrs-4py` 等）`[ASSUMED]` |
| `requests`/`edge-tts` (py) | PyPI | 极成熟 | 极高 | github.com/psf/requests, github.com/rany2/edge-tts | `[ASSUMED]` | 上游既有依赖；规划器在 venv 任务加 `checkpoint:human-verify` |

**Packages removed due to [SLOP] verdict:** none（无新增 Go 包可审计）。
**Packages flagged as suspicious [SUS]:** none（Go 侧）。

> *Python 包是上游 `xiaotian-skills` 脚本的既有运行时依赖，非本阶段新决策；WebSearch 配额耗尽（至 2026-08-13）且 PyPI 非 npm 生态，未对本研做 `gsd-tools query package-legitimacy`（该工具面向 npm/pypi/crates 但当前会话以 Go 生态为主）。Python 包名与版本应在 venv 建立任务中由人工确认（`pip install` 失败即暴露错误包名）。所有 Python 包在本研中标记 `[ASSUMED]`。*

## Architecture Patterns

### System Architecture Diagram

```
                      Go 服务进程（150MB 预算）
 ┌──────────────────────────────────────────────────────────────┐
 │  调用方（Phase 5 FSRS 编排 / Phase 7 会话）                    │
 │     │  RunDue(ctx, opts)  RunReview(ctx, opts)                │
 │     │  RunPregenerate(ctx, opts)                              │
 │     ▼                                                         │
 │  ┌─────────────────────── pkg/pybridge ───────────────────┐   │
 │  │ Executor 接口（DI，手动 fake 可测）                       │   │
 │  │   │                                                     │   │
 │  │   ▼                                                     │   │
 │  │ 全局信号量（chan struct{}, cap N=2）—— D-02              │   │
 │  │   │ acquire（满则排队/由 worker-pool backpressure 处理）  │   │
│  │   ▼                                                     │   │
│  │ exec.CommandContext(ctx, venvPython, script, args...)    │   │
│  │   • env: PYTHONUTF8=1, PYTHONIOENCODING=utf-8,           │   │
│  │         DICTATION_DB_PATH, VOLCENGINE_TTS_*（os.Getenv） │   │
│  │   • SysProcAttr{Setpgid:true}（Linux 进程组）             │   │
│  │   • Cancel: syscall.Kill(-pid, SIGKILL)  ← 杀整组        │   │
│  │   • WaitDelay: 兜底防孤儿 ffmpeg 管道不闭                  │   │
│  │   │                                                     │   │
│  │   ▼  cmd.Output() / 分离 Stdout+Stderr 管道              │   │
│  │ 解析器：                                                  │   │
│  │   • due/review: 从混合 stdout 提取末尾 JSON + 看退出码    │   │
│  │   • pregenerate: stdout 按行切 mp3 路径                   │   │
│  │   │                                                     │   │
│  │   ▼  失败分类 → typed sentinel（包裹 exit_code/stderr）   │   │
│  └───┼─────────────────────────────────────────────────────┘   │
│      │                                                         │
│      ▼                                                         │
│  infra.Logger.Error/Warn(F("command"),F("exit_code"),          │
│       F("duration_ms"),F("error_type")) ← 成功标准 4            │
└──────────────────────────────────┬───────────────────────────┘
                                    │ fork（spawn-per-call, D-01）
                    ┌───────────────┴────────────────┐
                    ▼ (vocab_manager.py)             ▼ (pregenerate_dictation.py generate)
          ┌─────────────────────┐          ┌────────────────────────┐
          │ fsrs + sqlite3      │          │ requests + edge-tts    │
          │ → SQLite vocab.db   │          │ + ffmpeg 子进程        │
          │   (FSRS 真值源 D-10) │          │ → mp3 文件 + manifest  │
          │ stdout: JSON        │          │ stdout: mp3 路径(按行) │
          │ stderr: 未用        │          │ stderr: 进度日志       │
          └─────────────────────┘          └────────────────────────┘
                    ▲                                ▲
                    │ fcntl.flock（全局单实例锁）    │
                    │ 仅 Linux（Windows dev 不可用） │
```

读者可从上至下追踪一次 `RunDue` 调用：调用方 → Executor → 信号量 → CommandContext（env+进程组+Cancel+WaitDelay）→ spawn Python → 解析 stdout → 失败分类 → 日志。`pregenerate` 路径多一层 fcntl flock 全局串行。

### Recommended Project Structure

```
third_party/python/                  # D-03 vendor 目录（Claude discretion 选此名）
├── vocab_manager.py                 # 从 xiaotian-skills 拷入，版本绑定 Go
├── pregenerate_dictation.py         # 同上
├── requirements.txt                 # D-04 pip freeze 锁定（fsrs/requests/edge-tts）
└── .env.example                     # 非密钥的 DICTATION_* 路径示例（密钥不入库）
pkg/pybridge/                        # 桥实现（cmd→pkg 依赖规则，复用 infra）
├── executor.go                      # Executor 接口 + typed 错误 sentinel
├── bridge.go                        # *Bridge 实现（CommandContext/进程组/WaitDelay）
├── vocab.go                         # RunDue/RunReview + due/review stdout 解析器
├── pregenerate.go                   # RunPregenerate + 按行路径解析器
├── parse.go                         # 提取末尾 JSON 的通用助手 + 路径切分
├── semaphore.go                     # 全局有界信号量（chan struct{}, cap N）
├── executor_test.go                 # 手动 fake（D-48）+ 表驱动单测
├── parse_test.go                    # 黄金 JSON fixture（due/review/pregenerate）
├── bridge_linux_test.go             # #go:build linux 集成测（真 venv python）
└──testdata/                         # *.json golden fixtures
internal/pytest/                     # 可选：跨平台 fake python 脚本（无 fcntl）
└── fake_vocab.sh / fake_prege.sh    # 测试用 echo JSON 的 shell fake
config.example.yaml                  # 新增 python: 块（操作性字段）
```

### Pattern 1: 进程组杀死（防孤儿 ffmpeg）—— Linux

**What:** `exec.CommandContext` 默认 `Cancel` 只 `Kill` 直接子进程；Python 若 spawn 了 ffmpeg，超时后 ffmpeg 变孤儿继续耗 CPU/磁盘。
**When to use:** 每次 spawn（尤其 pregenerate，内部有 `subprocess.run(["ffmpeg",...])`）。
**Example:**
```go
// 来源: [VERIFIED: GOROOT/src/os/exec/exec.go 第 251-303 行 Cancel/WaitDelay 文档；
//        GOROOT/src/syscall/exec_linux.go 第 75-91 行 Setpgid 文档]
import (
    "context"
    "os/exec"
    "syscall"
    "time"
    "github.com/pkg/errors"
)

func (b *Bridge) run(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, []byte, int, error) {
    cctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    cmd := exec.CommandContext(cctx, name, args...)
    // Linux: 让子进程成为新进程组组长，PGID == 子 PID
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
    // 自定义 Cancel：杀整个进程组（负 PID = 组），覆盖默认的"只杀直接子"
    cmd.Cancel = func() error {
        if cmd.Process == nil {
            return nil
        }
        // -pid 投递到进程组；SIGKILL 不可被忽略
        return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
    }
    // WaitDelay 兜底：即便 Cancel 没让 Wait 返回（管道仍开），到时强制关管道
    // 文档原话："might not occur until orphaned subprocesses ... closed their descriptors"
    cmd.WaitDelay = 3 * time.Second

    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr

    start := time.Now()
    err := cmd.Run()
    duration := time.Since(start)

    exitCode := 0
    if cmd.ProcessState != nil {
        exitCode = cmd.ProcessState.ExitCode()
    }
    return stdout.Bytes(), stderr.Bytes(), exitCode, err
    // err 可能是: nil | *exec.ExitError(非零退出) | context.DeadlineExceeded(超时) | ErrWaitDelay
}
```

> **Windows dev 注意：** `syscall.SysProcAttr{Setpgid:true}` 是 Linux 专属字段；Windows 的 `syscall.SysProcAttr` 无此字段，编译失败。桥的进程组逻辑必须用 `//go:build linux` 构建 tag 隔离，并提供一个 `//go:build !linux` 的回退实现（不设 Setpgid，仅依赖 WaitDelay + ctx Kill）。这与 Phase 2 已记录的"Windows dev 不支持 syscall.SIGTERM"（main.go 注释第 12-13 行）同一类约束。集成测（真 Python）应 #go:build linux 且跳过 Windows。

### Pattern 2: vocab_manager 的 stdout 契约（混合流，必须提取末尾 JSON）

**What:** `vocab_manager.py` 的 `due`/`review` 把一切打到 stdout：成功 JSON、`Warning:` 行、`Error:` 行。靠退出码（0=成功，1=错误）区分。
**When to use:** 解析 `due`/`review` 输出。
**Example（契约证据）：**
```python
# 来源: [VERIFIED: vocab_manager.py 源码]
# cmd_due 第 291 行 —— 成功，纯 JSON 数组到 stdout（indent=2 多行）：
print(json.dumps(due_cards, ensure_ascii=False, indent=2))

# cmd_review 第 375-377 行 —— Warning 行先到 stdout，与最终 JSON 混在同一流：
print(f"Warning: unknown rating '{rating_str}' for '{key}', skipping")  # ← 默认 stdout!
# ... 循环结束 ...
print(json.dumps({"updated": updated}, ensure_ascii=False, indent=2))   # ← 仍是 stdout

# 错误（各处，如 cmd_add 第 143 行）—— print 到 stdout + sys.exit(1)：
print(f"Error: file not found: {args.file}")
sys.exit(1)
```

```go
// 来源: 基于上述源码契约推导的解析策略 [VERIFIED 源码契约]
// 策略：不要假设 stdout 是纯 JSON。用"找最后一个完整 JSON 值"或"逐字符从尾部回扫"。
//   exit_code==0 → 在 stdout 里定位末尾 JSON（可能是 {...} 或 [...]）
//   exit_code!=0 → stdout 通常是 "Error: ..." 文本；整体作为错误信息上送
func extractTrailingJSON(stdout []byte) (json.RawMessage, error) {
    s := bytes.TrimRight(stdout, " \t\r\n")
    // 从尾部回扫找最后一个 } 或 ]，再向前找匹配的开括号
    // （实现见 parse.go；用 stack 计数处理嵌套，忽略字符串内的括号）
}
```

### Pattern 3: pregenerate 的 stdout 契约（按行路径，干净分离）

**What:** `pregenerate_dictation.py generate` 把 mp3 绝对路径按行打到 stdout，所有进度/错误打到 stderr。完全干净的 stdout/stderr 分离，但 stdout **不是 JSON**。
**Example（契约证据）：**
```python
# 来源: [VERIFIED: pregenerate_dictation.py 源码]
# _cli_generate 第 1147-1148 行 —— 成功，路径按行到 stdout：
for p in audio_files:
    print(os.path.abspath(p))

# 进度/诊断一律到 stderr（第 624, 715, 907, 1069 行等）：
print(f"  Intros: generated {generated}, cached {count - generated}", file=sys.stderr)
print(f"ERROR: invalid --words JSON: {e}", file=sys.stderr)
sys.exit(1)
```
```go
// 来源: 基于上述源码契约 [VERIFIED]
// 策略：用 cmd.Output()（仅捕 stdout，stderr 自动进 ExitError.Stderr）；
//       exit==0 → strings.Split(stdout, "\n") 过滤空行即 mp3 路径列表
//       exit!=0 → ExitError.Stderr 是诊断文本，整体进日志
paths := splitNonEmptyLines(stdout)
```

### Pattern 4: Executor 接口（DI + 手动 fake，D-42/D-47/D-48）

**What:** 桥暴露接口而非具体类型，测试注入手动 fake，绝不 spawn 真 Python（Windows dev 无 Python、CI 无 venv）。
**Example:**
```go
// 来源: Phase 1 D-42/D-47/D-48 约定（见 pkg/infra logging.go Logger 接口范式）[CITED: 项目代码]
package pybridge

type Executor interface {
    RunDue(ctx context.Context, opts DueOpts) (DueResult, error)         // PY-02
    RunReview(ctx context.Context, opts ReviewOpts) (ReviewResult, error) // PY-03
    RunPregenerate(ctx context.Context, opts PregenOpts) (PregenResult, error) // PY-04
}

// typed 错误：包裹 exit_code + stderr，让调用方区分"用户/FSRS 错误"vs"基础设施错误"
var (
    ErrPythonExit      = errors.New("python subprocess exited non-zero")
    ErrPythonTimeout   = errors.New("python subprocess timed out")
    ErrPythonJSONParse = errors.New("python stdout JSON parse failed")
    ErrPythonEmpty     = errors.New("python stdout empty")
)

type ExitError struct {
    Cmd      string
    ExitCode int
    Stderr   string
    Err      error
}
func (e *ExitError) Unwrap() error { return e.Err }
func (e *ExitError) Error() string { /* ... */ }
```

### Pattern 5: 全局有界信号量（D-02，复用 worker-pool 风格）

**What:** cap N 的 `chan struct{}` 限制并发 Python 进程；满则阻塞（由调用方/worker-pool 决定 backpressure）。
**Example:**
```go
// 来源: D-02 + sync 标准模式 [CITED: 项目决策]
type semaphore chan struct{}
func newSemaphore(n int) semaphore { return make(semaphore, n) }
func (s semaphore) acquire(ctx context.Context) error {
    select {
    case s <- struct{}{}:
        return nil
    case <-ctx.Done():
        return ctx.Err()  // 排队期间超时/取消，返回 ctx 错误（调用方转 backpressure）
    }
}
func (s semaphore) release() { <-s }
// 推荐 N=2：见 Common Pitfalls 的内存测算
```

### Anti-Patterns to Avoid

- **用 `cmd.CombinedOutput()` 解析 `due`/`review`：** `[VERIFIED: GOROOT exec.go 第 1037 行]` CombinedOutput 把 stdout+stderr 合并；vocab_manager 的错误/警告本就在 stdout，合并后更难定位。改用分离管道或 `Output()`。
- **假设 `due`/`review` 的 stdout 是纯 JSON：** review 的 `Warning:` 行混在 JSON 前，直接 `json.Unmarshal` 整段 stdout 会失败（Pitfall 2）。
- **假设 `pregenerate` 的 stdout 是 JSON：** 它是按行路径，`json.Unmarshal` 必失败（Pitfall 3）。
- **不设进程组杀死就依赖超时：** 默认 Cancel 只杀 Python 直接进程，ffmpeg 孤儿继续跑，且 WaitDelay=0 时管道读到 EOF 会无限阻塞（Pitfall 1）。
- **把 `VOLCENGINE_TTS_*` 塞进 Config：** 违反 D-05；密钥经 `os.Getenv` 读后注入 `cmd.Env`。
- **桥直接读写 `vocab.db`：** 违反 D-09/D-10；DB 是 Python 的真值源，Go 只通过 CLI 交互。
- **在 Windows dev 上跑 pregenerate 集成测：** `import fcntl`（第 12 行）是 Unix 专属，Windows 直接 ImportError（Pitfall 5）。
- **无界信号量或 N 过大：** 50-100 并发会话同时 fork Python 会 fork-bomb 1C/1G VPS（Pitfall 7）。

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| 进程超时 + 取消 | 手写 goroutine + timer | `exec.CommandContext(ctx,...)` + `context.WithTimeout` | stdlib 标准，与 ctx 取消集成 `[VERIFIED: Go stdlib]` |
| 进程组杀死 | 手写 `setpgid` C 调用 | `syscall.SysProcAttr{Setpgid:true}` + `cmd.Cancel` | stdlib 跨字段已处理 `[VERIFIED: Go stdlib]` |
| JSON 解析 | 手写 tokenizer | `encoding/json` + `json.RawMessage` | 标准；脚本输出即 JSON |
| 结构化日志 | `fmt.Printf` 或自建 | `infra.Logger`（Phase 1 已建，zap 后端） | 一致性 + D-25 约定 `[CITED: pkg/infra/logging.go]` |
| 配置加载 | 手写 YAML/env 解析 | `infra.Config` + Viper 扩展 `Python` 块 | Phase 1 已建 layered loader `[CITED: pkg/infra/config.go]` |
| 有界并发 | 手写 mutex+counter | `chan struct{}`（容量 N） | Go 惯用、零依赖 |
| 音频生成 | Go 调 TTS API 自建 | `pregenerate_dictation.py`（已有缓存/重试/裁剪/ffmpeg 拼接） | 上游成熟，重写高风险（D-07） |
| FSRS 调度 | Go 重写算法 | `vocab_manager.py` 的 `fsrs` | D-09 锁定，重写高险 |

**Key insight:** 本阶段几乎不需要"造轮子"——Go 侧全 stdlib + 既有 infra；Python 侧全是上游既有资产。桥的核心价值在"正确包裹与契约解析"，而非新算法。

## Runtime State Inventory

> 本阶段**非 rename/refactor/migration**（是新增 vendor + 新建包），但仍核查是否有运行时状态需迁移。

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | `vocab.db`（FSRS/词汇 SQLite）在 `xiaotian-skills/shared/dictation/data/vocab.db`（上游）。本仓库尚无该 DB。 | 无迁移：D-10 规定 DB 由 Python 拥有；部署时 `DICTATION_DB_PATH` 指向运行时路径。Phase 3 仅透传路径，不碰数据。 |
| Live service config | 上游 `.env`（`DICTATION_*` + `VOLCENGINE_TTS_*`）在 `xiaotian-skills/shared/dictation/.env`。 | 不入 git（含密钥）。仓库内放 `.env.example`（仅非密钥 `DICTATION_*` 路径）；密钥经部署环境注入。 |
| OS-registered state | 无（Python 脚本无 systemd/launchd 注册；pregenerate 的 `/tmp/pregenerate_dictation.lock` 是运行时 flock，非持久注册）。 | 无。 |
| Secrets/env vars | `VOLCENGINE_TTS_API_KEY` / `VOLCENGINE_TTS_APP_ID` / `VOLCENGINE_TTS_ACCESS_KEY` 经 `os.Getenv` 读取并转发子进程（D-05）。 | 代码读取即可，无密钥迁移。 |
| Build artifacts | 无（新仓库，无旧 egg-info/二进制）。 | 无。 |

**结论：** 无运行时状态迁移。唯一部署时操作：建 venv、拷/同步脚本、配 `DICTATION_*` 路径与密钥 env。

## Common Pitfalls

### Pitfall 1: exec.CommandContext 不杀进程组（孤儿 ffmpeg）
**What goes wrong:** pregenerate 内部 `subprocess.run(["ffmpeg",...])`（`_trim_audio_edges` 第 444 行、`_concat_fragments` 第 799 行）。若 Go 超时取消，默认 `Cancel` 只 `Kill(Python PID)`，ffmpeg 变孤儿继续占 CPU/磁盘，且其 stdout/stderr 管道保持打开，使 Go 的 `cmd.Wait()` 因 `WaitDelay=0` 无限阻塞（"might not occur until orphaned subprocesses ... closed their descriptors"）。
**Why it happens:** Go stdlib 默认行为只追踪直接子 PID `[VERIFIED: GOROOT exec.go 第 251-254, 300-302 行]`。
**How to avoid:** `SysProcAttr{Setpgid:true}` + 自定义 `Cancel: syscall.Kill(-pid,SIGKILL)` + 非零 `WaitDelay`（见 Pattern 1）。
**Warning signs:** 超时后 Go goroutine 数不降、VPS load 飙升、`ps` 见残留 ffmpeg。

### Pitfall 2: vocab_manager stdout 是混合流（非纯 JSON）
**What goes wrong:** `review` 在 JSON 前打 `Warning:` 行（未知 rating/重复 card/找不到 card）到 stdout；错误也打 `Error:` 到 stdout + exit 1。直接 `json.Unmarshal(stdout)` 会因前缀文本失败。
**Why it happens:** Python `print()` 默认写 stdout，脚本未用 `file=sys.stderr` 分流错误/警告 `[VERIFIED: vocab_manager.py 第 375-377, 411-418, 143 行]`。
**How to avoid:** 按退出码分流：exit 0 → 从 stdout 提取末尾 JSON（从尾部回扫匹配括号，见 Pattern 2）；exit!=0 → stdout 整体作错误文本。
**Warning signs:** `json.Unmarshal` 报 "invalid character 'W'/'E'"；review 部分卡被 skip 但整体看起来失败。

### Pitfall 3: pregenerate stdout 是按行路径，不是 JSON
**What goes wrong:** 误把 pregenerate 当 JSON 解析（与 vocab_manager 一致假设），`json.Unmarshal` 必失败。
**Why it happens:** `for p in audio_files: print(os.path.abspath(p))` 输出每行一个绝对路径 `[VERIFIED: pregenerate_dictation.py 第 1147-1148 行]`。
**How to avoid:** 用 `strings.Split` + 过滤空行（见 Pattern 3）。
**Warning signs:** `json.Unmarshal` 报 "invalid character '/'"（路径首字符）。

### Pitfall 4: `easy` rating 支持的混淆（已澄清）
**What:** CONTEXT 研究注记提到 `review` help 文本写 `good|hard|again`（3 种），但 REQUIREMENTS FEED-02 期望 4 种（含 `easy`）。
**核查结果：** `[VERIFIED: vocab_manager.py 第 358-364 行]` rating_map 明确含 `"easy": Rating.Easy`。help 文本（第 746 行）只是过时。**Phase 3 桥直接透传 4 种 rating，不做语义改写。** 映射决策（如需）属 Phase 10。
**How to avoid:** 桥的 ReviewOpts.Ratings 用 `map[string]string`（word→rating）原样序列化进 `--results` JSON，不校验/不改写。

### Pitfall 5: pregenerate 是 Unix-only（fcntl），Windows dev 不可运行
**What goes wrong:** `import fcntl`（第 12 行）+ `fcntl.flock`（第 1302 行）是 Unix 专属；Windows 上 Python 直接 `ModuleNotFoundError`。
**Why it happens:** 上游脚本面向 Linux 部署设计。
**How to avoid:** 集成测用 `//go:build linux` 隔离；Windows dev 只测桥的解析/错误分类逻辑（用 fake 脚本，不 spawn 真 Python）。CI 应在 Linux 跑真集成测。
**Warning signs:** Windows 上 `python pregenerate_dictation.py generate ...` 报 fcntl ImportError。

### Pitfall 6: pregenerate 的全局 flock 串行化所有调用
**What goes wrong:** `_acquire_global_lock`（第 1297 行）对 `/tmp/pregenerate_dictation.lock` 取 `LOCK_EX|LOCK_NB`；第二个实例 `sys.exit(0)` 且 **stdout 空**。Go 若并发 fork 多个 pregenerate，除一个外其余静默退出 0 无输出。
**Why it happens:** 上游防并发写同一缓存。
**How to avoid:** 桥对 pregenerate 调用自身串行化（信号量对 pregenerate 路径设 cap=1），或检测 exit 0 + 空 stdout 视为"被锁占"重试/排队。`[VERIFIED: pregenerate_dictation.py 第 1297-1313, 1320-1326 行]`。
**Warning signs:** 并发 pregenerate 偶发返回空路径列表但无错误。

### Pitfall 7: 冷启动成本与 1C/1G 预算
**What goes wrong:** 每 spawn 一次 Python 都付解释器启动 + 导入成本。vocab_manager 导入 `fsrs`（纯 Python，~100-200ms 估）；pregenerate 导入 `requests`/`edge-tts`/`asyncio`/`zoneinfo`（更重，可能 300-500ms）。1C/1G 上 50-100 并发会话同时 fork 会内存/CPU 雪崩。
**Why it happens:** D-01 spawn-per-call 的固有代价。
**How to avoid:** (a) 信号量 N=2（见测算）；(b) pregenerate 是每会话一次批调用（`--words` 数组），非每词一次，频率低；(c) 可选冷启动缓解：Python 端 `-S` 标志或延迟 import（但 D-01 禁止常驻 sidecar，不可用 daemon）；(d) <1.5s 延迟目标主要针对**音频交付**（Phase 6），生成可异步预热（Phase 7 编排）。
**Warning signs:** 单次 due 调用 >500ms；并发下 OOM/swap。

**内存测算（信号量 N 建议值）：**
- 单个 vocab_manager 进程（fsrs+sqlite3）：~25-40MB RSS `[ASSUMED]`
- 单个 pregenerate 进程（requests+edge-tts+ffmpeg 子）：~40-60MB RSS `[ASSUMED]`，ffmpeg 另加 ~20-30MB
- 150MB Go 预算 + 瞬时 Python：若 N=2 同时跑 pregenerate，峰值 ~120MB Python + 150MB Go = 270MB，超 1G 但在 OS swap 容忍内（低频）；若 N=4 则峰值 ~480MB Python，逼近 1G 危险。
- **推荐 N=2**：平衡吞吐与安全。due/review 极快（~200ms），pregenerate 慢但每会话一次；2 并发足以平滑峰值。

## Code Examples

### Config 扩展（操作性字段，无密钥——D-05）
```go
// 来源: 复用 pkg/infra/config.go 的 Config + Viper 约定 [CITED: pkg/infra/config.go]
// 在 infra.Config 加：
type Config struct {
    // ... 既有字段 ...
    Python PythonConfig `json:"python" mapstructure:"python"`
}

type PythonConfig struct {
    Interpreter    string        // venv python 绝对路径，如 /opt/hta/venv/bin/python
    ScriptsDir     string        // vendor 目录，如 third_party/python
    VocabScript    string        // vocab_manager.py 文件名（默认 vocab_manager.py）
    PregenScript   string        // pregenerate_dictation.py 文件名
    VocabDBPath    string        // DICTATION_DB_PATH 值（运行时 DB）
    DataDir        string        // DICTATION_DATA_DIR
    CacheDir       string        // DICTATION_CACHE_DIR（pregenerate mp3 缓存）
    ManifestDir    string        // DICTATION_MANIFEST_DIR
    EnvFile        string        // DICTATION_ENV_FILE（非密钥 .env，密钥走进程 env）
    DueTimeout     time.Duration // 默认 10s
    ReviewTimeout  time.Duration // 默认 10s
    PregenTimeout  time.Duration // 默认 120s（TTS + ffmpeg 较慢）
    MaxConcurrent  int           // 信号量 N（默认 2）
}
// Validate: Interpreter 非空、ScriptsDir 存在、MaxConcurrent in [1,4]、超时 >0
// bindEnvForAllLeaves 加: python.interpreter, python.scriptsDir, ... python.maxConcurrent
// 密钥（VOLCENGINE_TTS_*）绝不在此结构体——D-05
```

### env 注入（密钥经 os.Getenv，D-05）
```go
// 来源: D-05 + D-24（Phase 1 密钥规则）[CITED: 项目决策]
func (b *Bridge) childEnv(extra []string) []byte {
    env := os.Environ()  // 继承父进程（含部署时注入的 VOLCENGINE_TTS_*）
    // 强制 UTF-8（中文词汇，Pitfall 防 mojibake）
    env = append(env, "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
    // DICTATION_* 路径
    env = append(env,
        "DICTATION_DB_PATH="+b.cfg.VocabDBPath,
        "DICTATION_DATA_DIR="+b.cfg.DataDir,
        "DICTATION_CACHE_DIR="+b.cfg.CacheDir,
        "DICTATION_MANIFEST_DIR="+b.cfg.ManifestDir,
    )
    if b.cfg.EnvFile != "" {
        env = append(env, "DICTATION_ENV_FILE="+b.cfg.EnvFile)
    }
    // VOLCENGINE_TTS_* 经 os.Getenv 读后显式注入（确保子进程可见，即使父 env 缺失）
    for _, k := range []string{"VOLCENGINE_TTS_API_KEY", "VOLCENGINE_TTS_APP_ID", "VOLCENGINE_TTS_ACCESS_KEY", "VOLCENGINE_TTS_RESOURCE_ID"} {
        if v := os.Getenv(k); v != "" {
            env = append(env, k+"="+v)
        }
    }
    env = append(env, extra...)
    return []byte(strings.Join(env, "\n"))
}
```

### `--db` 全局参数顺序（vocab_manager 特有）
```go
// 来源: [VERIFIED: vocab_manager.py 第 717-718, 797 行]
// main() 第 718 行: parser.add_argument("--db", ...) 是全局参数；
// 第 719 行: subparsers 在 --db 之后。
// 因此命令必须是: python vocab_manager.py --db <path> due --limit 30
// （而非: python vocab_manager.py due --db <path> --limit 30 ← --db 会被 argparse 当 due 的未知参数报错）
func dueArgs(cfg PythonConfig, opts DueOpts) []string {
    return []string{
        cfg.VocabScript, "--db", cfg.VocabDBPath,  // 全局 --db 必须在子命令前
        "due",
        "--category", opts.Category,
        "--limit", strconv.Itoa(opts.Limit),
        // 可选: --unit/--units/--exclude/--peek/--fetch-lock-hours
    }
}
```

### 错误分类 → 结构化日志（成功标准 4）
```go
// 来源: infra.Logger / F() / ErrField() 约定 [CITED: pkg/infra/logging.go]
func (b *Bridge) logResult(method string, start time.Time, stdout, stderr []byte, exit int, err error) {
    fields := []infra.Field{
        infra.F("component", "pybridge"),
        infra.F("command", method),
        infra.F("exit_code", exit),
        infra.F("duration_ms", time.Since(start).Milliseconds()),
    }
    switch {
    case err == nil && exit == 0:
        b.logger.Debug("python subprocess ok", fields...)
    case errors.Is(err, context.DeadlineExceeded):
        fields = append(fields, infra.F("error_type", "timeout"))
        b.logger.Error("python subprocess timed out", append(fields, infra.F("stderr", string(stderr)))...)
    case exit != 0:
        fields = append(fields, infra.F("error_type", "nonzero_exit"))
        b.logger.Error("python subprocess failed", append(fields, infra.F("stderr", string(stderr)), infra.F("stdout", string(stdout)))...)
    default:
        fields = append(fields, infra.F("error_type", "other"), infra.ErrField(err))
        b.logger.Error("python subprocess error", fields...)
    }
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `exec.Command` + 手动 goroutine 超时 | `exec.CommandContext` + `Cmd.Cancel`/`WaitDelay` | Go 1.20（2023-02） | 取消语义内建；可自定义 kill 整进程组；WaitDelay 防管道不闭阻塞 `[VERIFIED: GOROOT exec.go]` |
| 手写 `setpgid` syscalls | `syscall.SysProcAttr{Setpgid:true}` | Go 早期 | 跨平台字段；Linux 上 PGID==子 PID 便于 `-pid` 组杀 `[VERIFIED: exec_linux.go]` |
| Python 默认 locale 编码 | `PYTHONUTF8=1`（PEP 540 UTF-8 Mode） | Python 3.7（2018） | 强制 stdin/stdout/open 默认 UTF-8，消除 Windows GBK mojibake `[ASSUMED]` |
| FSRS 手写调度 | `fsrs` PyPI 包（Wikipedia 同款算法） | 上游既有 | 算法成熟，勿重写（D-09） |

**Deprecated/outdated:**
- `vocab_manager.py review` 的 help 文本 `good|hard|again`（3 种）已过时——代码实际接受 4 种含 `easy` `[VERIFIED: 第 358-364 行]`。
- pregenerate 的 `--run-legacy-pregenerate` 维护模式路径——Phase 3 只用 `generate` 子命令。

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | vocab_manager 进程 RSS ~25-40MB；pregenerate ~40-60MB；ffmpeg ~20-30MB | Pitfall 7 | 若实际更高，N=2 仍可能超预算；需在真 VPS 实测后调 N（部署期 checkpoint） |
| A2 | Python `fsrs`/`requests`/`edge-tts` 在 PyPI 上的包名与版本可用 | Standard Stack / Package Audit | 若包名错（如 `fsrs` vs `fsrs-4py`），venv 建立失败；规划器加 `checkpoint:human-verify` |
| A3 | `PYTHONUTF8=1` + `PYTHONIOENCODING=utf-8` 足以保证中文不乱码 | Code Examples / Pitfall | 仅基于训练知识（WebSearch 配额耗尽未验证官方文档）；Go 侧 `bytes.Buffer` 读 stdout 后 `string()` 默认按 UTF-8 解释，应无碍；部署期验收 |
| A4 | 冷启动 vocab_manager ~100-200ms；pregenerate ~300-500ms | Pitfall 7 | 估算值；真 1C VPS 上可能更慢；Phase 12 性能验证校准 |
| A5 | Go 1.26 的 `Cmd.Cancel`/`WaitDelay` 语义与本地 GOROOT 源码一致 | Pattern 1 | 低风险——已在本地 go1.26.0 stdlib 源码逐行确认 `[VERIFIED]`，部署环境同版本 |

**说明：** A5 实为已验证（本地 Go 1.26.0 stdlib 源码）。A1-A4 因 WebSearch 配额耗尽（至 2026-08-13）且无法 fetch 外部文档，基于训练知识与源码推导，标 `[ASSUMED]`，需在部署/集成阶段由真环境确认。

## Open Questions

1. **Python venv 在 1C/1G VPS 上的实际内存占用**
   - What we know: 上游脚本依赖 `fsrs`/`requests`/`edge-tts`；这些导入有内存成本。
   - What's unclear: 真 VPS 上单进程 RSS 峰值（A1 估算）。
   - Recommendation: 规划器在部署任务加 `checkpoint:human-verify` 实测 RSS，据之调 N。

2. **`fsrs` PyPI 包名**
   - What we know: vocab_manager 第 33 行 `from fsrs import ...`。
   - What's unclear: PyPI 上是 `fsrs` 还是 `fsrs-4py`/`py-fsrs`（A2）。
   - Recommendation: venv 建立任务先 `pip install fsrs` 验证，失败则查上游 `requirements`。

3. **并发信号量与 pregenerate flock 的交互**
   - What we know: pregenerate 自带 `/tmp` flock 全局串行（Pitfall 6）。
   - What's unclear: Go 信号量 N=2 时，第二个 pregenerate 调用是排队（Go 侧）还是被 flock 拒绝（Python 侧 exit 0 空输出）。
   - Recommendation: 桥对 pregenerate 路径单独用 cap=1 串行，避免与 flock 冲突；或检测 exit0+空输出重试。

4. **Windows dev 上的集成测覆盖**
   - What we know: Windows 无 Python（本机探测确认）、无 fcntl。
   - What's unclear: CI 是否在 Linux 跑全集成测。
   - Recommendation: 桥单测全平台（fake）；集成测 `//go:build linux` 且需 venv；CI Linux job 覆盖。

## Environment Availability

> 已在本机实测（`go env`、版本探测、which 命令）。

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | 编译桥 | ✓ | go1.26.0 windows/amd64 | — |
| Python 解释器 | spawn 子进程（运行时 + 集成测） | ✗（本机未装） | — | 部署 VPS 需装；本机集成测用 fake 脚本 + `//go:build linux` 跳过 |
| `fsrs` (py) | vocab_manager | ✗（系统 python 不可导入） | — | venv 建立（D-04）；集成测在 Linux CI |
| `requests`/`edge-tts` (py) | pregenerate | ✗ | — | 同上 venv |
| ffmpeg | pregenerate（裁剪/拼接/静音） | ✓（本机） | 6.1 full_build | 部署 VPS 必须装；preflight 会检查 `[VERIFIED: pregenerate 第 1012 行]` |
| Linux 内核（Setpgid/fcntl） | 进程组杀死 / pregenerate flock | ✗（本机 MINGW64/Windows） | — | 桥用 build tag 隔离；真部署在 Linux VPS |

**Missing dependencies with no fallback:**
- **Python 解释器 + venv**：本机无，部署 VPS 必须有。本机集成测无法跑真 Python——必须用 fake 脚本（`internal/pytest/fake_*.sh`）+ 表驱动单测覆盖解析/分类逻辑；真集成测放 Linux CI。
- **Linux 内核特性**：进程组杀死（Setpgid）与 pregenerate flock 是 Linux 专属。Windows dev 用 build tag 回退实现。

**Missing dependencies with fallback:**
- Python 缺失 → fake 脚本（echo JSON）测桥的解析/超时/错误分类（非 spawn 真进程路径）。

> **规划器必须处理的阻塞项：**
> 1. 部署 VPS 安装 Python 3 + 建 venv（Make target `py-bootstrap`）。
> 2. CI Linux job 跑真集成测（含 venv）。
> 3. 桥代码用 `//go:build linux` 与 `//go:build !linux` 双实现，Windows 可编译可单测。

## Validation Architecture

> `workflow.nyquist_validation: true`（config.json 未显式 false）——本节必填。

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing`（Phase 1 D-28/D-29 约定，无 testify/mock——D-48 手动 fake） |
| Config file | 无独立配置；`.golangci.yml`（govet/staticcheck/gosec）+ `.pre-commit-config.yaml` |
| Quick run command | `go test -v -short ./pkg/pybridge/...` |
| Full suite command | `go test -race -v ./...`（D-33 race detector；Linux CI 加 `//go:build linux` 集成测） |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| PY-01 | Executor 接口 + Bridge 构造（DI、Config 校验） | unit | `go test -run TestNewBridge -v ./pkg/pybridge/` | ❌ Wave 0 |
| PY-02 | RunDue 解析 due JSON 数组（混合 stdout） | unit | `go test -run TestRunDue -v ./pkg/pybridge/` | ❌ Wave 0 |
| PY-02 | due 透传 --limit/--unit/--category/--exclude/--peek/--fetch-lock-hours | unit | `go test -run TestDueArgs -v ./pkg/pybridge/` | ❌ Wave 0 |
| PY-02 | --db 全局参数顺序正确（在子命令前） | unit | `go test -run TestVocabDBArgOrder -v ./pkg/pybridge/` | ❌ Wave 0 |
| PY-03 | RunReview 解析 `{"updated":[...]}`（含 Warning 前缀） | unit | `go test -run TestRunReview -v ./pkg/pybridge/` | ❌ Wave 0 |
| PY-03 | review 透传 4 种 rating（good/hard/again/easy）不改写 | unit | `go test -run TestReviewRatings -v ./pkg/pybridge/` | ❌ Wave 0 |
| PY-03 | review 大负载走 --file 而非 --results | unit | `go test -run TestReviewLargePayload -v ./pkg/pybridge/` | ❌ Wave 0 |
| PY-04 | RunPregenerate 按行解析 mp3 路径（非 JSON） | unit | `go test -run TestRunPregenerate -v ./pkg/pybridge/` | ❌ Wave 0 |
| PY-04 | pregenerate 透传 --words/--language/--prefix/--voice | unit | `go test -run TestPregenArgs -v ./pkg/pybridge/` | ❌ Wave 0 |
| SC-4 | 非零退出 → ErrPythonExit + 日志 error_type=nonzero_exit | unit | `go test -run TestExitNonZero -v ./pkg/pybridge/` | ❌ Wave 0 |
| SC-4 | 超时 → ErrPythonTimeout + 杀进程组 | unit | `go test -run TestTimeout -v ./pkg/pybridge/` | ❌ Wave 0 |
| SC-4 | JSON 解析失败 → ErrPythonJSONParse | unit | `go test -run TestJSONParseFailure -v ./pkg/pybridge/` | ❌ Wave 0 |
| SC-4 | 空 stdout → ErrPythonEmpty | unit | `go test -run TestEmptyOutput -v ./pkg/pybridge/` | ❌ Wave 0 |
| PY-* | 进程组杀死孤儿子进程（防 ffmpeg 泄漏） | integration (linux) | `go test -race -run TestProcessGroupKill -tags=linux -v ./pkg/pybridge/` | ❌ Wave 0 |
| D-02 | 信号量 cap N 限流 + 溢出排队 | unit | `go test -run TestSemaphore -v ./pkg/pybridge/` | ❌ Wave 0 |
| PY-01/02/03 | 真 venv python 跑 due/review（Linux CI） | integration (linux) | `go test -race -run TestIntegrationVocab -tags=linux,integration -v ./pkg/pybridge/` | ❌ Wave 0 |
| PY-04 | 真 venv python 跑 pregenerate（需 ffmpeg+密钥，Linux CI） | integration (linux) | `go test -race -run TestIntegrationPregenerate -tags=linux,integration -v ./pkg/pybridge/` | ❌ Wave 0 |

### Sampling Rate
- **Per task commit:** `go test -v -short ./pkg/pybridge/...`（fake 为主，秒级）
- **Per wave merge:** `go test -race -v ./...`（全仓，含 race）
- **Phase gate:** Full suite 绿 + Linux CI 集成测（需 venv）绿，方可 `/gsd-verify-work`

### Wave 0 Gaps
- [ ] `pkg/pybridge/executor.go` + `executor_test.go` —— Executor 接口 + 手动 fake（覆盖 PY-01）
- [ ] `pkg/pybridge/parse.go` + `parse_test.go` —— 提取末尾 JSON + 按行路径解析 + 黄金 fixture（覆盖 PY-02/03/04 解析）
- [ ] `pkg/pybridge/bridge.go` + `bridge_linux_test.go` —— CommandContext/进程组/WaitDelay（覆盖 SC-4 超时/exit）
- [ ] `pkg/pybridge/vocab.go` —— RunDue/RunReview + `--db` 参数顺序（覆盖 PY-02/03）
- [ ] `pkg/pybridge/pregenerate.go` —— RunPregenerate 按行路径（覆盖 PY-04）
- [ ] `pkg/pybridge/semaphore.go` + 测 —— 全局信号量 N=2（覆盖 D-02）
- [ ] `pkg/pybridge/testdata/*.json` —— due 数组、review `{"updated":[]}`、含 Warning 前缀的 review stdout、pregenerate 路径列表黄金样本
- [ ] `internal/pytest/fake_vocab.sh`、`fake_prege.sh` —— 跨平台测试用 echo JSON 的 shell fake
- [ ] `pkg/infra/config.go` 扩展 + `config_test.go` 加 PythonConfig 校验用例
- [ ] `config.example.yaml` 加 `python:` 块示例
- [ ] Make target `py-bootstrap`（建 venv）+ `py-sync`（同步上游脚本）

*(无既有测试基础设施覆盖本 phase——pkg/pybridge 是全新包)*

## Security Domain

> `security_enforcement` 未在 config.json 显式 false → 视为启用。

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no | 桥不涉及用户认证（session 认证由 Phase 1 处理） |
| V3 Session Management | no | 非本阶段职责 |
| V4 Access Control | no | 桥不直接鉴权；调用方负责 |
| V5 Input Validation | yes | `--results`/`--words` JSON 透传前须校验是合法 JSON；`--prefix` 须校验 ASCII 安全字符（上游 `_prefix_is_safe` 已做，Go 侧可复用或信任）；防注入 CLI 参数（绝不 `shell=True`，全用 `exec.Command(name, args...)` 参数数组形式） |
| V6 Cryptography | no | 无自研加密；密钥透传不存储 |
| V7 Error Handling & Logging | yes | 成功标准 4；错误结构化落日志，但**不得把密钥写入日志**（`VOLCENGINE_TTS_*` 绝不出现在 log fields；只记 command/exit_code/duration/error_type，stderr 可能含上游诊断但不含密钥） |
| V8 Data Protection | yes | 密钥经 `os.Getenv` 不入 Config/不入日志/不入 git（D-05）；`.env` 不入库 |
| V9 Communications | yes | TTS 出站走 HTTPS（Volcengine URL `https://...` `[VERIFIED: pregenerate 第 74 行]`）；Go 桥自身不出站 |
| V12 Files & Resources | yes | 写入 `CACHE_DIR`/`MANIFEST_DIR` 前上游已做 preflight（可写性、磁盘 >100MB `[VERIFIED: 第 1009-1018 行]`）；桥可复用或补充检查 |

### Known Threat Patterns for Go↔Python subprocess bridge

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| 命令注入（恶意 word 含 shell 元字符） | Tampering | 全用参数数组 `exec.Command(name, args...)`，**绝不** `exec.Command("sh","-c",...)` 拼接；argparse 侧 `--results`/`--words` 是值而非命令 |
| 路径遍历（恶意 `--prefix` 写出缓存目录） | Tampering | 上游 `_prefix_is_safe`（第 1031-1035 行）已校验 ASCII `[a-zA-Z0-9_.-]`；Go 侧 ReviewOpts/PregenOpts 校验 prefix 同规则 |
| 密钥泄漏到日志 | Information Disclosure | 日志字段白名单（command/exit_code/duration_ms/error_type）；stderr 整体记录但上游脚本本身不打密钥；禁记 env |
| 孤儿进程 DoS（ffmpeg 泄漏耗尽 1C/1G） | Denial of Service | 进程组杀死 + WaitDelay（Pitfall 1）+ 信号量 N=2（Pitfall 7） |
| 资源耗尽（fork-bomb） | Denial of Service | D-02 全局信号量 cap；复用 Phase 1 backpressure（D-72） |
| 子进程继承过多 env | Information Disclosure | 显式构造 childEnv（白名单 DICTATION_* + VOLCENGINE_TTS_*），非全量 `os.Environ()`（可选收紧） |
| JSON 解析耗内存（巨型 due 输出） | Denial of Service | `--limit` 上游强 positive_int；Go 侧 `bytes.Buffer` 有上限或用 `io.LimitReader` |

## Sources

### Primary (HIGH confidence)
- `GOROOT/src/os/exec/exec.go`（本地 go1.26.0 stdlib 源码）—— `Cmd.Cancel`/`WaitDelay`/`Process`/`Output()`/`CombinedOutput()` 字段与方法文档（第 125-128, 239, 251-303, 628, 641, 922, 1014-1042 行）`[VERIFIED]`
- `GOROOT/src/syscall/exec_linux.go`（本地 stdlib 源码）—— `SysProcAttr.Setpgid`/`Pgid` 字段与 SETPGID syscall（第 75-91, 393-395 行）`[VERIFIED]`
- `D:/workspace/ai/xiaotian-skills/shared/dictation/scripts/vocab_manager.py`（源码逐行）—— `due`/`review`/rating_map(含 easy)/错误契约/stdout 混合流/`--db` 全局参数/schema `[VERIFIED]`
- `D:/workspace/ai/xiaotian-skills/shared/dictation/scripts/pregenerate_dictation.py`（源码逐行）—— `generate` 子命令/按行路径 stdout/干净 stderr/fcntl flock/ffmpeg preflight/Volcengine HTTPS `[VERIFIED]`
- `D:/workspace/ai/xiaotian/pkg/infra/config.go`、`logging.go`、`worker.go`、`pool.go`、`cmd/service/main.go`（项目源码）—— Config/Logger/WorkerPool 约定 `[VERIFIED]`
- `D:/workspace/ai/xiaotian/go.mod`、`config.example.yaml`（项目源码）—— 模块路径/依赖版本/配置约定 `[VERIFIED]`
- 本机环境探测（`go version`、ffmpeg 探测、python 缺失、MINGW64 平台）`[VERIFIED]`

### Secondary (MEDIUM confidence)
- `.planning/phases/02-websocket-communication-layer/02-PATTERNS.md` —— Phase 1/2 既定模式（DI、Logger 用法、build tag 约定、Windows dev 限制）`[CITED]`
- `.planning/phases/01-resource-constrained-infrastructure/01-CONTEXT.md` —— D-10/D-14/D-24/D-42/D-47/D-48/D-72 等复用决策 `[CITED]`
- `.planning/phases/03-python-cli-integration/03-CONTEXT.md` + `03-DISCUSSION-LOG.md` —— D-01..D-11 锁定决策与备选理由 `[CITED]`

### Tertiary (LOW confidence)
- WebSearch 返回的 fallback 摘要（配额耗尽，非本会话验证）—— 仅用于交叉印证 Go 进程组杀死模式与 Python UTF-8 env，不作为唯一依据；关键点已由本地 stdlib 源码覆盖。

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH —— 全 Go stdlib + 既有 go.mod 依赖，无新包；本地源码与 go.mod 逐项确认。
- Architecture: HIGH —— 基于实际 Python 源码契约（逐行）与 Go stdlib 源码（逐行）推导；CONTEXT D-01..D-11 锁定方向。
- Pitfalls: HIGH —— Pitfall 1/2/3/4/5/6 均从源码直接定位行号；Pitfall 7 内存数值为 ASSUMED（A1）但定性结论（需限流）稳固。
- Python 包名/版本/内存占用: LOW-MEDIUM（A1/A2/A3/A4）—— 需部署期实测。

**Research date:** 2026-07-24
**Valid until:** 2026-08-23（30 天；Go stdlib API 稳定，Python 上游脚本变更时需重核契约）
**Tool 限制说明：** 本次 WebSearch/WebFetch 因配额耗尽或网络策略不可用；所有 Go 技术断言改由本地 GOROOT stdlib 源码（最高权威主源）验证，Python 契约由源码逐行验证。外部文档引用（如 PEP 540 PYTHONUTF8、Go issue #23044）未在本会话联网核验，标 `[ASSUMED]`。
