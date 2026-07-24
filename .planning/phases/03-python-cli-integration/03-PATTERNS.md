# Phase 3: Python CLI Integration - Pattern Map

**Mapped:** 2026-07-24
**Files analyzed:** 16 (11 new Go/test + 4 vendor/config + 1 modified Go + Makefile/main.go/config edits)
**Analogs found:** 13 / 16 (3 greenfield — see "No Analog Found")

## Greenfield Status Notice

> Phase 1 (`pkg/infra/*`) 与 Phase 2 (`pkg/ws/*`) 均已落地为真实 Go 代码。本阶段的"形状类比"（构造器 DI、typed sentinel 错误、`context.WithTimeout`、表驱动测试、`infra.Logger` 包装）都能从既有代码直接摘抄。但以下两块是**真·greenfield**：
>
> 1. **`os/exec` 子进程执行** —— 全仓 `Grep("os/exec|exec\.Command")` 命中 0 个 `.go` 文件（仅 RESEARCH.md 提及）。无任何既有 subprocess 代码可抄。规划器须让实现直接遵循 Go stdlib + RESEARCH.md Pattern 1（进程组杀死）。
> 2. **基于 `chan struct{}` 的有界信号量** —— 全仓无任何信号量实例（既有 `chan struct{}` 仅用于 worker.go / server.go 的 `done` 信号，非容量限制器）。规划器须用 RESEARCH.md Pattern 5 + `pkg/infra/worker.go` 的 `Submit` select 惯用法。
>
> 此外 `syscall.SysProcAttr{Setpgid:true}` 在全仓 0 命中，是 Linux 专属字段，必须用 `//go:build linux` 与 `//go:build !linux` 双实现隔离（与 `cmd/service/main.go` 第 12-13 行记录的 Windows dev 不支持 `syscall.SIGTERM` 同一类约束）。

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `pkg/pybridge/executor.go` | service (interface) | request-response | `pkg/infra/worker.go` (Task interface + sentinels) + `pkg/infra/logging.go` (Logger interface) | exact (role-match, same package convention) |
| `pkg/pybridge/bridge.go` | service | subprocess (file/proc I/O) | `pkg/infra/connection.go` (`NewConnectionRegistry` 构造器 DI 范式) + RESEARCH.md Pattern 1（无 exec 代码，stdlib idiom） | role-match (struct shape) + greenfield (exec core) |
| `pkg/pybridge/bridge_linux.go` / `bridge_other.go` | service | subprocess | `cmd/service/main.go` 第 12-13 行 Windows dev 注释（同类 build-tag 隔离） | role-match (build-tag convention) |
| `pkg/pybridge/vocab.go` | service | request-response (JSON 契约解析) | `pkg/ws/codec.go` (`DecodeFrame` 字节→typed struct+校验) | role-match (parse pattern) |
| `pkg/pybridge/pregenerate.go` | service | request-response (按行路径解析) | `pkg/ws/codec.go` (parse bytes + error 分类) | role-match |
| `pkg/pybridge/parse.go` | utility | transform (提取末尾 JSON) | RESEARCH.md Pattern 2（无既有 JSON-from-mixed-stream 代码） | reference (greenfield) |
| `pkg/pybridge/semaphore.go` | utility | concurrency-limiter | `pkg/infra/worker.go` 第 228-240 行 `Submit` (select-on-channel idiom) + RESEARCH.md Pattern 5 | role-match (select idiom) + greenfield (semaphore) |
| `pkg/pybridge/executor_test.go` | test | request-response | `pkg/infra/worker_test.go` (手动 fake + `waitOrTimeout` + ctx 取消) + `pkg/infra/config_test.go` (errors.Is 校验) | exact |
| `pkg/pybridge/parse_test.go` | test | transform | `pkg/infra/config_test.go` (表驱动 + 子测试) | exact |
| `pkg/pybridge/bridge_linux_test.go` | test (integration) | subprocess | `pkg/ws/integration_test.go` (httptest + `context.WithTimeout(5s)`) | role-match (集成测结构) |
| `pkg/pybridge/testdata/*.json` | test fixture | n/a | 无既有 fixture 目录（Phase 2 用 `internal/wstest/helpers.go` 内联生成） | greenfield (新建 testdata/) |
| `internal/pytest/fake_vocab.sh`, `fake_prege.sh` | test utility (shell) | request-response | `internal/wstest/helpers.go` (Phase 2 D-48 手动 fake) | role-match (manual-fake 概念) |
| `third_party/python/vocab_manager.py`, `pregenerate_dictation.py` | vendor asset | n/a | 拷自 `D:/workspace/ai/xiaotian-skills/shared/dictation/scripts/*`（上游源） | vendor copy |
| `third_party/python/requirements.txt`, `.env.example` | config | n/a | `config.example.yaml`（注释风格 + "密钥不入库"约定） | exact (style) |
| `pkg/infra/config.go` (modified) | config | n/a (扩展) | **自身** —— `MemoryConfig`/`SessionConfig` 范式 + `DefaultConfig` + `Validate` + `bindEnvForAllLeaves` | exact (self) |
| `pkg/infra/config_test.go` (modified) | test | n/a | **自身** —— `TestLoadConfigInvalidMemoryLimit` 等 errors.Is(ErrConfigInvalid) 模式 | exact (self) |
| `config.example.yaml` (modified) | config | n/a | **自身** —— `memory:`/`session:` 块的 YAML 注释风格 | exact (self) |
| `cmd/service/main.go` (modified) | controller | request-response (接线) | **自身** —— 第 80-100 行 构造器 DI + bootstrap log + 错误包装模式 | exact (self) |
| `Makefile` (modified) | config | n/a | **自身** —— `test`/`build`/`lint` target 风格 | exact (self) |

## Pattern Assignments

### `pkg/pybridge/executor.go` (service interface, request-response)

**Analog:** `pkg/infra/worker.go` 第 16-44 行（`Task` 接口 + 4 个 sentinel 错误）+ `pkg/infra/logging.go` 第 61-86 行（`Logger` 接口）。

**Imports + 接口 + sentinel 模式**（直接照抄 worker.go 的接口/sentinel 命名与组织）:
```go
// 来源: pkg/infra/worker.go 第 12-44 行 [VERIFIED 代码]
package pybridge

import (
    "context"
    "errors"
)

// Task 是 worker.go 的范式：小聚焦接口（D-43），ctx 透传（D-45）。
// Executor 照抄此形状：
type Executor interface {
    RunDue(ctx context.Context, opts DueOpts) (DueResult, error)
    RunReview(ctx context.Context, opts ReviewOpts) (ReviewResult, error)
    RunPregenerate(ctx context.Context, opts PregenOpts) (PregenResult, error)
}

// sentinel 错误命名风格照抄 worker.go 第 22-44 行（ErrXxx，每个带句号）：
var (
    ErrPythonExit      = errors.New("python subprocess exited non-zero")
    ErrPythonTimeout   = errors.New("python subprocess timed out")
    ErrPythonJSONParse = errors.New("python stdout JSON parse failed")
    ErrPythonEmpty     = errors.New("python stdout empty")
)
```

**注释约定**：worker.go 每个 sentinel 上方都有"Callers MUST detect them via `errors.Is`, never by string matching (D-04 ...)"的文档块。Executor 的 sentinel 必须照抄此约定。

**typed ExitError**：参考 worker.go 第 191-217 行 `runTask` 的 panic-recovery 包装结构（`Cmd`/`ExitCode`/`Stderr` 字段 + `Unwrap()`），以及 RESEARCH.md Pattern 4（第 362-370 行）。

---

### `pkg/pybridge/bridge.go` (service, subprocess I/O)

**Analog（结构形状）:** `pkg/infra/connection.go` 第 251-272 行 `NewConnectionRegistry`（构造器 DI + cfg 校验 + nil-logger 替换 + errors.Wrap(ErrConfigInvalid, ...)）。

**构造器模式**（照抄 connection.go 第 251-272 行）:
```go
// 来源: pkg/infra/connection.go 第 251-272 行 [VERIFIED 代码]
func NewConnectionRegistry(cfg SessionConfig, hooks LifecycleHooks, logger Logger) (*ConnectionRegistry, error) {
    if cfg.MaxConcurrent <= 0 {
        return nil, errors.Wrap(ErrConfigInvalid, "session.maxConcurrent must be > 0 for ConnectionRegistry")
    }
    // ... 逐字段校验 ...
    if logger == nil {
        logger = NewNoOpLogger()   // D-42 DI 零值；照抄此 nil-替换
    }
    return &ConnectionRegistry{ /* ... */ }, nil
}
// Bridge 照抄：NewBridge(cfg PythonConfig, logger infra.Logger) (*Bridge, error)
//   - 逐字段校验 cfg（Interpreter/ScriptsDir/Timeouts/MaxConcurrent）
//   - logger == nil → NewNoOpLogger()
//   - 内部建 semaphore（cap cfg.MaxConcurrent）
```

**Analog（context + run 循环）:** `pkg/ws/heartbeat.go` 第 29-49 行（`time.NewTicker` + `defer ticker.Stop()` + `select { case <-ctx.Done(): return; case <-ticker.C: ... }`）。Bridge 的 `run()` 不是循环，但 `context.WithTimeout(ctx, timeout)` + `defer cancel()` 的形状与 heartbeat 完全一致。

**核心 exec 模式（greenfield，无既有代码）:** 直接遵循 RESEARCH.md Pattern 1（第 236-281 行）的 `exec.CommandContext` + `SysProcAttr{Setpgid:true}` + `cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }` + `cmd.WaitDelay = 3*time.Second`。这是 Go stdlib 惯用法，非项目既有代码——规划器须把 RESEARCH.md Pattern 1 作为权威参考。

---

### `pkg/pybridge/bridge_linux.go` / `bridge_other.go` (build-tag 隔离)

**Analog:** `cmd/service/main.go` 第 12-13 行注释（"Windows dev hosts do not support syscall.Kill with SIGTERM"）—— 同一类平台隔离约束。Phase 2 的 `pkg/ws/heartbeat.go` 也用 `WriteControl` 而非直接 syscall。

**Build-tag 模式（greenfield，但 Phase 2 已确立 build-tag 约定）:**
```go
// pkg/pybridge/bridge_linux.go
//go:build linux
package pybridge
import "syscall"
func setProcessGroup(cmd *exec.Cmd) {
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
    cmd.Cancel = func() error {
        if cmd.Process == nil { return nil }
        return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
    }
}

// pkg/pybridge/bridge_other.go
//go:build !linux
package pybridge
func setProcessGroup(cmd *exec.Cmd) {
    // Windows/macOS dev：无 Setpgid 字段，仅依赖 ctx Kill + WaitDelay 兜底
}
```
规划器须照 RESEARCH.md 第 283 行（Windows dev 注意）让两个文件同时存在，否则 Windows 编译失败。

---

### `pkg/pybridge/vocab.go` (service, JSON 契约解析)

**Analog:** `pkg/ws/codec.go` 的 `DecodeFrame` —— 把原始字节解析为 typed struct + CRC 校验失败时返回 wrapped sentinel。Bridge 的 `RunDue`/`RunReview` 照此形状：`json.Unmarshal` 进 typed `DueResult`/`ReviewResult`，失败包 `ErrPythonJSONParse`。

**契约不对称性（RESEARCH.md Pattern 2，第 285-315 行）:** `vocab_manager.py` 把成功 JSON、`Warning:` 行、`Error:` 行全打到 stdout，靠 exit code 区分。**绝不能** `json.Unmarshal(stdout)` 整段——必须先调 `parse.go` 的 `extractTrailingJSON`。规划器须把 RESEARCH.md Pitfall 2 作为强制验收点。

**`--db` 全局参数顺序**（RESEARCH.md 第 541-556 行）: `python vocab_manager.py --db <path> due ...`，`--db` 必须在子命令前，否则 argparse 报错。这是 vocab_manager 特有契约，照抄 RESEARCH.md Code Example。

---

### `pkg/pybridge/pregenerate.go` (service, 按行路径解析)

**Analog:** `pkg/ws/codec.go` 的 parse + error 分类。但**契约与 vocab.go 不同**（RESEARCH.md Pattern 3，第 317-338 行）：stdout 按行是 mp3 绝对路径（非 JSON），stderr 干净分离。用 `strings.Split(string(stdout), "\n")` + 过滤空行。**绝不能** `json.Unmarshal`——否则报 "invalid character '/'"（RESEARCH.md Pitfall 3）。

**fcntl flock 串行性（RESEARCH.md Pitfall 6）:** pregenerate 对 `/tmp/pregenerate_dictation.lock` 取 `LOCK_EX|LOCK_NB`，第二个实例 `sys.exit(0)` 且 stdout 空。规划器须让桥对 pregenerate 路径用 cap=1 串行，或检测 exit0+空输出重试。

---

### `pkg/pybridge/parse.go` (utility, transform)

**Analog:** greenfield（无既有"从混合流提取末尾 JSON"代码）。参考 RESEARCH.md Pattern 2 第 305-315 行的 `extractTrailingJSON` 策略（从尾部回扫找最后一个 `}` 或 `]`，向前找匹配开括号，stack 计数处理嵌套）。

`splitNonEmptyLines` 是简单工具，照 `pkg/ws/codec.go` 的工具函数组织风格（小、聚焦、带文档注释）。

---

### `pkg/pybridge/semaphore.go` (utility, concurrency-limiter)

**Analog:** `pkg/infra/worker.go` 第 228-240 行 `Submit` 的 select-on-channel idiom + RESEARCH.md Pattern 5（第 376-390 行）。

**select idiom 照抄**:
```go
// 来源: pkg/infra/worker.go 第 228-240 行 [VERIFIED 代码]
func (p *WorkerPool) Submit(task Task) error {
    select {
    case p.tasks <- task:
        return nil
    default:                                    // ← worker.go 用 default 立即返回 ErrWorkerPoolFull
        p.totalBackpressure.Add(1)
        return ErrWorkerPoolFull
    }
}
// semaphore 照此形状但用 ctx.Done() 分支（RESEARCH.md Pattern 5）：
func (s semaphore) acquire(ctx context.Context) error {
    select {
    case s <- struct{}{}:
        return nil
    case <-ctx.Done():
        return ctx.Err()   // 排队期间超时/取消 → 调用方转 backpressure
    }
}
```
注意区别：worker.go `Submit` 是 non-blocking（满即拒），semaphore `acquire` 是 blocking-until-ctx（满则排队）。两者共享 `select { case ch <- ... : case <-ctx.Done()/default: }` 的 Go 惯用形状。

**计数器模式**：可选加 `atomic.Int32` inFlight 计数器，照 `pkg/infra/pool.go` 第 92 行 `inFlight atomic.Int32` + Stats() 暴露。规划器决定是否暴露 Stats（Phase 3 不强制）。

---

### `pkg/pybridge/executor_test.go` + `parse_test.go` (test)

**Analog:** `pkg/infra/worker_test.go` + `pkg/infra/config_test.go`。

**测试框架约定**（照抄 worker_test.go 第 1-43 行）:
```go
// 来源: pkg/infra/worker_test.go 第 1-43 行 [VERIFIED 代码]
package pybridge  // 同包，黑盒+白盒混合

import (
    "context"
    "errors"
    "sync"
    "sync/atomic"
    "testing"
    "time"
)

// 照抄 waitOrTimeout helper（worker_test.go 第 31-43 行）：
func waitOrTimeout(t *testing.T, wg *sync.WaitGroup, timeout time.Duration, what string) {
    t.Helper()
    done := make(chan struct{})
    go func() { wg.Wait(); close(done) }()
    select {
    case <-done:
    case <-time.After(timeout):
        t.Fatalf("%s within %v", what, timeout)
    }
}
```

**表驱动 + errors.Is 校验模式**（照抄 config_test.go 第 223-272 行）:
```go
// 来源: pkg/infra/config_test.go 第 223-235 行 [VERIFIED 代码]
func TestLoadConfigInvalidMemoryLimit(t *testing.T) {
    // ... 构造坏 config ...
    if err := LoadConfig(path); err == nil {
        t.Fatalf("LoadConfig returned nil error, want ErrConfigInvalid for memory.limitBytes=0")
    }
    if !errors.Is(err, ErrConfigInvalid) {
        t.Errorf("LoadConfig error = %v, want errors.Is(err, ErrConfigInvalid)=true", err)
    }
}
// Bridge 测照抄：errors.Is(err, ErrPythonExit) / ErrPythonTimeout / ErrPythonJSONParse / ErrPythonEmpty
```

**手动 fake（D-48，无 testify/mock）**：照 worker_test.go 用 `NewNoOpLogger()` + 内联 `fakeLogger`（见 `pkg/infra/middleware_test.go` 的 fakeLogger/findField helpers，worker_test.go 第 25 行引用）。Executor 的 fake 是实现接口的 struct，返回预设的 stdout 字节切片（不 spawn 真 Python）。

**子测试命名**：照 config_test.go 用 `t.Run("subcase name", ...)`（第 22-69 行的 `t.Run("memory defaults ...", ...)`）。

---

### `pkg/pybridge/bridge_linux_test.go` (integration test)

**Analog:** `pkg/ws/integration_test.go`（`context.WithTimeout(context.Background(), 5*time.Second)` + httptest 服务器）。

**集成测模式**（照 ws/integration_test.go 第 103/179/244 行的 5s timeout 约定）:
```go
//go:build linux
package pybridge
import (
    "context"
    "testing"
    "time"
)
// 真 venv python 跑 due/review/pregenerate。
// 每个 case 用 ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
// 照 ws/integration_test.go 第 103 行的 5s 约定。
// CI Linux job 覆盖；Windows dev 跳过（//go:build linux）。
```

**testdata/ 黄金 fixture**：Phase 2 无既有 testdata 目录（`internal/wstest/helpers.go` 内联生成字节）。规划器新建 `pkg/pybridge/testdata/*.json`，放：(a) due 数组样本；(b) review `{"updated":[...]}`；(c) 含 `Warning:` 前缀的 review stdout 混合流（Pitfall 2 验收用）；(d) pregenerate 按行路径样本。

---

### `internal/pytest/fake_vocab.sh` / `fake_prege.sh` (test utility, shell)

**Analog:** `internal/wstest/helpers.go`（Phase 2 的 D-48 手动 fake，但是 Go 代码）。这里是 shell 脚本（echo JSON / echo 路径），让 Windows dev 无 Python 也能测桥的解析/超时/错误分类逻辑。概念照 `internal/wstest/` 的"测试帮手放 internal/"约定（Phase 2 已确立 internal/ 用于测试基建）。

---

### `pkg/infra/config.go` (modified — 扩展 PythonConfig)

**Analog:** **自身** —— `MemoryConfig`/`SessionConfig` 是直接模板。

**新增结构体照抄**（config.go 第 102-139 行 MemoryConfig/SessionConfig 范式）:
```go
// 来源: pkg/infra/config.go 第 73-97 行（Config struct）+ 第 102-122 行（MemoryConfig） [VERIFIED 代码]
type Config struct {
    // ... 既有 8 块 ...
    Python PythonConfig `json:"python" mapstructure:"python"`   // 新增
}

type PythonConfig struct {
    Interpreter   string        // 照 ServerConfig.Addr 的非空校验
    ScriptsDir    string        // 同上
    VocabDBPath   string        // 同上
    // ... DataDir/CacheDir/ManifestDir/EnvFile/VocabScript/PregenScript ...
    DueTimeout    time.Duration // 照 ServerConfig.ReadTimeout 的 >0 校验
    ReviewTimeout time.Duration
    PregenTimeout time.Duration
    MaxConcurrent int           // 照 SessionConfig.MaxConcurrent 的范围校验（这里 [1,4]）
}
```

**3 处必须同步扩展**（缺一即不一致）:
1. `DefaultConfig()` 第 242-284 行 —— 加 `Python: PythonConfig{ DueTimeout: 10*time.Second, ..., MaxConcurrent: 2, ... }`。
2. `Validate()` 第 361-463 行 —— 加 `c.Python.Interpreter == ""` 等校验，每条 `errors.Wrap(ErrConfigInvalid, "...")`。
3. `bindEnvForAllLeaves()` 第 507-536 行 —— 在 `keys` 切片加 `"python.interpreter"`, `"python.scriptsDir"`, ..., `"python.maxConcurrent"`（T-01-06 mitigation，绝不依赖 AutomaticEnv）。

**密钥规则（D-05，强约束）:** 照抄 config.go 第 67-72 行的 Config 文档注释——"`VOLCENGINE_TTS_*` 绝不在此结构体；只经 `os.Getenv` 在 `pkg/pybridge` 内读取"。PythonConfig 不得有 `APIKey`/`Secret`/`Password`/`AccessToken` 字段。

---

### `pkg/infra/config_test.go` (modified)

**Analog:** **自身** —— 第 223-272 行的 `TestLoadConfigInvalid*` 系列。为 PythonConfig 加同形态测试：`TestLoadConfigInvalidPythonInterpreter` / `TestLoadConfigInvalidPythonMaxConcurrent` 等，每个用 `errors.Is(err, ErrConfigInvalid)` 断言。并加 `TestDefaultConfig` 第 19 行的子测试块（`t.Run("python defaults ...", ...)`）。

---

### `config.example.yaml` (modified)

**Analog:** **自身** —— 照 `memory:` / `server:` 块的 YAML 注释风格（每字段上方一行 `#` 说明 + 引用 D-xx）。

```yaml
# 新增块（照 config.example.yaml 第 10-37 行 memory/buffer 块风格）：
python:
  # venv python 绝对路径（D-04）。部署 VPS 上 /opt/hta/venv/bin/python。
  interpreter: /opt/hta/venv/bin/python
  # D-03 vendor 目录（含 vocab_manager.py / pregenerate_dictation.py）。
  scriptsDir: third_party/python
  # ... 其余操作性字段；密钥（VOLCENGINE_TTS_*）绝不在此 —— 经部署环境注入（D-05）...
  dueTimeout: 10s
  pregenTimeout: 120s
  maxConcurrent: 2   # D-02 信号量 N（推荐 2，见 RESEARCH Pitfall 7 测算）
```

---

### `cmd/service/main.go` (modified — 接线 Bridge)

**Analog:** **自身** —— 第 80-100 行的构造器 DI + 错误处理模式。

**接线模式**（照 main.go 第 80-100 行）:
```go
// 来源: cmd/service/main.go 第 80-100 行 [VERIFIED 代码]
logger, err := infra.NewLogger(cfg.Log)
if err != nil { /* bootstrap log + return 1 */ }
defer func() { _ = logger.Sync() }()

// 新增：照 infra.NewServer 的构造 + 错误处理形状
pyBridge, err := pybridge.NewBridge(cfg.Python, logger)
if err != nil {
    logger.Error("pybridge construction failed",
        infra.F("component", "infra.main"),
        infra.ErrField(err),
    )
    return 1
}
_ = pyBridge   // Phase 5/7 调用方接入前先持有引用
```
**注意**：main.go 用的是 `infra.Logger` + `infra.F()` + `infra.ErrField()`（D-25 canonical），**不是** `pkg/ws/*` 的裸 `*zap.Logger`。Phase 3 必须遵循 main.go/`pkg/infra` 这一支（详见 Shared Patterns "日志包装"）。import 加 `"github.com/xiaotian/home-tutor-agent/pkg/pybridge"`。

---

### `Makefile` (modified)

**Analog:** **自身** —— 第 10-14 行的 `test` / `test-short` target 风格。新增 `py-bootstrap` / `py-sync` target（RESEARCH.md 第 122-131 行的 venv 建立命令）。`.PHONY` 行同步加新 target 名。

---

### `third_party/python/*` (vendor)

**Analog:** 无（拷自上游 `D:/workspace/ai/xiaotian-skills/shared/dictation/scripts/vocab_manager.py` + `pregenerate_dictation.py`）。规划器须在第一个 vendor 任务加 `checkpoint:human-verify` 比对上游版本一致性。`requirements.txt` 是 `pip freeze` 输出；`.env.example` 照 `config.example.yaml` 的"密钥不入库"注释风格。

## Shared Patterns

### 构造器 DI（D-42 / D-47）
**Source:** `pkg/infra/connection.go` 第 251-272 行；`pkg/infra/pool.go` 第 142-176 行；`pkg/infra/worker.go` 第 116-133 行
**Apply to:** `NewBridge`（pybridge）
- 签名 `(cfg, logger) (*T, error)`；cfg 逐字段校验返回 `errors.Wrap(ErrXxx, "...")`；`logger == nil` 替换为 `NewNoOpLogger()`；绝不读全局。

### 日志包装：用 `infra.Logger`，不用裸 `*zap.Logger`
**Source:** `pkg/infra/logging.go` 第 61-86 行（Logger 接口）+ 第 34-44 行（`F` / `ErrField`）+ `cmd/service/main.go` 第 95-98 行
**Apply to:** 所有 `pkg/pybridge/*.go`
- **重要分歧提示：** 仓内存在两支日志用法。`pkg/infra/*` + `cmd/service/main.go` 用 `infra.Logger` + `infra.F("component", "...")` + `infra.ErrField(err)`（16 处命中 main.go）；`pkg/ws/*` 用裸 `*zap.Logger` + `zap.String("component", ...)`（Phase 2 选择）。
- Phase 3 RESEARCH.md（第 156、187-189、559-581 行）明确要求用 **`infra.Logger`** 那一支（"复用 infra.Logger / F() / ErrField()"）。
- 字段命名约定：`component` 值用 `"pybridge"`（或 `"pybridge.due"` 等子标签），snake_case；`duration_ms` 用 `time.Since(start).Milliseconds()`；`error_type` ∈ {`nonzero_exit`,`timeout`,`json_parse`,`empty`,`other`}。
- **密钥绝不进日志**（V7）：日志字段白名单 `command`/`exit_code`/`duration_ms`/`error_type`/`stderr`；禁记 env，禁记 `VOLCENGINE_TTS_*`。

### 错误处理（D-04 / D-44）
**Source:** `pkg/infra/config.go` 第 52 行（`ErrConfigInvalid`）+ 第 361-463 行（Validate）；`pkg/infra/worker.go` 第 21-44 行（sentinel 群）
**Apply to:** 所有 `pkg/pybridge/*.go`
- 每包定义 sentinel：`var ErrPythonExit = errors.New("...")` 等，照 worker.go 命名（句号结尾）。
- 包装用 `github.com/pkg/errors`（go.mod 第 7 行已引入 v0.9.1）：`errors.Wrap(ErrPythonExit, "run due")` / `errors.Wrapf(ErrPythonJSONParse, "offset %d", n)`。
- 调用方检测用 `errors.Is(err, ErrPythonTimeout)`，绝不字符串匹配。
- typed `ExitError` struct（含 `Cmd`/`ExitCode`/`Stderr`/`Err` + `Unwrap()`）让调用方区分"FSRS/用户错误"vs"基础设施错误"。

### context.Context 生命周期（D-12 / D-45）
**Source:** `pkg/ws/heartbeat.go` 第 29-49 行（`select <-ctx.Done() return`）；`pkg/ws/integration_test.go` 第 103 行（`context.WithTimeout(5s)`）；`cmd/service/main.go` 第 150 行（`context.WithTimeout(context.Background(), cfg.Shutdown.Timeout)`）
**Apply to:** `Bridge.run` + 所有 `RunXxx`
- 每次调用：`cctx, cancel := context.WithTimeout(ctx, cfg.XxxTimeout); defer cancel()` → 传给 `exec.CommandContext(cctx, ...)`。
- 测试约定：ctx 取消后 goroutine 50-500ms 内退出（照 Phase 1 `TestXxxStopsOnContextCancel`）。

### 表驱动 + errors.Is 测试（D-28 / D-29 / D-48）
**Source:** `pkg/infra/config_test.go` 第 19-69 行（`t.Run` 子测试）+ 第 223-272 行（errors.Is 校验）；`pkg/infra/worker_test.go` 第 31-43 行（`waitOrTimeout`）+ 第 48-88 行（手动 fake + `NewNoOpLogger()`）
**Apply to:** 所有 `pkg/pybridge/*_test.go`
- 框架：仅 Go stdlib `testing`，无 testify/mock（D-48）。fake 是手写实现 Executor 接口的 struct。
- 测试文件与源码同目录同包（`package pybridge`）。
- 命令：`go test -v -short ./pkg/pybridge/...`（fake，秒级）；`go test -race -v ./...`（全仓，含 race）。

### Module + Import 约定（D-23）
**Source:** `go.mod` 第 1 行；`cmd/service/main.go` 第 28 行；Makefile 第 21 行（`goimports -local github.com/xiaotian/home-tutor-agent`）
**Apply to:** 所有新 `.go` 文件
- Module: `github.com/xiaotian/home-tutor-agent`；go 1.26。
- 包名 = 目录名：`package pybridge` 在 `pkg/pybridge/`。
- Import 图：`cmd → pkg/pybridge → pkg/infra → 外部库`。无环。
- Import 分组：stdlib → 空行 → 第三方 → 空行 → 项目内（`github.com/xiaotian/home-tutor-agent/...`）。由 `.golangci.yml` + Makefile `fmt` target 强制。

### 密钥经 os.Getenv，绝不入 Config（D-05 / D-24）
**Source:** `pkg/infra/config.go` 第 67-72 行 Config 文档注释（"there is intentionally no field named Token, Secret, Password, or APIKey"）
**Apply to:** `bridge.go`（childEnv 构造）+ PythonConfig 结构体
- `VOLCENGINE_TTS_API_KEY` / `VOLCENGINE_TTS_APP_ID` / `VOLCENGINE_TTS_ACCESS_KEY` / `VOLCENGINE_TTS_RESOURCE_ID` 在 `pkg/pybridge` 内 `os.Getenv` 读后注入 `cmd.Env`。
- PythonConfig 不得有密钥字段。

## No Analog Found

| File | Role | Data Flow | Reason | Planner Guidance |
|------|------|-----------|--------|------------------|
| `pkg/pybridge/bridge.go`（exec 核心） | service | subprocess | 全仓 0 处 `os/exec` 使用 | 直接遵循 RESEARCH.md Pattern 1（第 236-281 行）+ Go stdlib `exec.CommandContext` 文档。构造器形状照抄 `NewConnectionRegistry`。 |
| `pkg/pybridge/semaphore.go` | utility | concurrency-limiter | 全仓 0 处 `chan struct{}` 容量信号量（既有 `chan struct{}` 仅用于 `done` 信号） | 直接遵循 RESEARCH.md Pattern 5（第 376-390 行）。select idiom 照抄 worker.go `Submit`（第 228-240 行）但用 `ctx.Done()` 分支替代 `default`。 |
| `pkg/pybridge/parse.go`（extractTrailingJSON） | utility | transform | 无既有"从混合 stdout 提取末尾 JSON"代码 | 直接遵循 RESEARCH.md Pattern 2（第 305-315 行）。黄金 fixture 见 testdata/。 |
| `syscall.SysProcAttr{Setpgid:true}` | (字段) | subprocess | 全仓 0 命中；Linux 专属字段 | RESEARCH.md Pattern 1 + 第 283 行 Windows dev 注意。必须 `//go:build linux` 隔离。 |

## Metadata

**Analog search scope:**
- `D:/workspace/ai/xiaotian/` 全仓（排除 `.git` / `.claude/worktrees/*` / `.agents`）
- `.planning/phases/01-resource-constrained-infrastructure/*.md`（Phase 1 决策引用）
- `.planning/phases/02-websocket-communication-layer/02-PATTERNS.md`（Phase 2 既定模式）
- `.planning/phases/03-python-cli-integration/03-CONTEXT.md` + `03-RESEARCH.md`

**Existing Go source files scanned:** 35 个 `.go` 文件（`pkg/infra/*` 14 + `pkg/ws/*` 12 + `pkg/crc16/*` 1 + `cmd/service/*` 2 + `internal/wstest/*` 1 + 测试文件若干）。全部为 Phase 1/2 已落地代码。

**Key Grep evidence:**
- `os/exec|exec\.Command` → 0 命中（仅 RESEARCH.md）→ exec greenfield
- `SysProcAttr|Setpgid` → 0 命中 → 进程组杀死 greenfield（Linux 专属）
- `chan struct{}` → 5 命中，全为 `done`/`blockCh` 信号，无容量信号量 → semaphore greenfield
- `context.WithTimeout` → 20+ 命中（ws/integration_test、worker_test、main.go、health_test）→ 超时模式成熟
- `infra.F(|infra.ErrField(|infra.NewNoOpLogger` → 47 命中（main.go 16、logging.go、infra/* 各处、PLAN 文档）→ `infra.Logger` 包装是 D-25 canonical
- `zap.String("component"` → 8 命中（主要在 pkg/ws + PLAN 文档）→ Phase 2 选了裸 zap，Phase 3 **不要**跟这一支

**Pattern extraction date:** 2026-07-24

**Key constraints surfaced for planner:**
1. **exec / 信号量 / 进程组杀死 三块 greenfield** —— 实现直接照 RESEARCH.md Patterns 1/2/5 + Go stdlib，非既有代码。
2. **双日志用法分歧** —— Phase 3 必须用 `infra.Logger`+`infra.F()`（main.go/infra 那一支），不用 `pkg/ws` 的裸 `*zap.Logger`。
3. **build-tag 隔离强制** —— `bridge_linux.go` + `bridge_other.go` 双实现，否则 Windows dev 编译失败（与 main.go 第 12-13 行记录的 SIGTERM 约定同类）。
4. **契约不对称是最大正确性风险** —— vocab_manager（混合 stdout + exit code，提取末尾 JSON）vs pregenerate（按行路径，干净 stderr）。两类解析器绝不能混用（RESEARCH Pitfall 2/3）。
5. **`--db` 全局参数顺序** —— vocab_manager 命令必须是 `python vocab_manager.py --db <path> due ...`，`--db` 在子命令前（RESEARCH 第 541-556 行）。
6. **密钥规则（D-05）** —— `VOLCENGINE_TTS_*` 经 `os.Getenv` 注入 `cmd.Env`，绝不进 PythonConfig / 不进日志 / 不进 git。
7. **测试基础设施缺口** —— 需新建 `pkg/pybridge/testdata/` 目录（Phase 2 用 internal/wstest 内联生成，无既有 testdata 惯例）+ `internal/pytest/` 目录放 shell fake。
