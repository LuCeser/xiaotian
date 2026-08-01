# Phase 5: FSRS Core Implementation - Research

**Researched:** 2026-08-01
**Domain:** Go service-layer orchestration of Python FSRS engine via pybridge
**Confidence:** HIGH

## Summary

Phase 5 构建一个 **Go 编排层**（`pkg/fsrs`），通过 Phase 3 已交付的 `pybridge.Executor` 接口调用 Python `vocab_manager.py` 中的 FSRS 引擎，实现自动到期单词选择（FSRS-01/02）、fetch-lock 会话隔离（FSRS-03）和反馈驱动的参数更新（FSRS-04）。

**关键边界**：本阶段 **不** 在 Go 中重新实现 FSRS 算法。Phase 3 D-09 和 ADR（2026-08-01）已锁定 Python `vocab_manager.py` 为 FSRS+SQLite 的永久唯一权威。Go 层是纯编排——构造参数、转发调用、传递结果。所有 FSRS 状态变更（stability/difficulty/due）必须流经 `pybridge.Executor.RunReview`，Go 绝不直接写 cards 表。

**主要建议**：创建 `pkg/fsrs` 包，包含 `FSRSService` 接口 + 具体实现，构造函数 `NewFSRSService(exec pybridge.Executor, logger infra.Logger, cfg infra.FSRSConfig)` 遵循 Phase 1 DI 范式。扩展 `pkg/infra/config.go` 添加 `FSRSConfig` 结构体。无需安装任何新的 Go 依赖包。

**Primary recommendation:** 按 CONTEXT D-01 单层服务架构实现，使用 pybridge 类型作为返回值（避免类型映射样板代码），复用 Phase 3 全局信号量（不引入新并发原语），fetch-lock 委托 Python 侧（Go 仅传 `FetchLockHours` 参数）。

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

**D-01: 单层服务架构** — 创建 `pkg/fsrs`，`FSRSService` 接口直接暴露 `SelectDueCards(opts SelectOpts) ([]DueCard, error)` 和 `UpdateRatings(ratings map[string]string) (UpdateResult, error)`。内部调用 `pybridge.Executor.RunDue` 和 `RunReview`。遵循 Phase 1 DI 构造范式 `NewFSRSService(exec Executor, logger Logger, cfg Config)`。

**D-02: Fetch Lock 委托 Python** — 利用 Python 侧 fetch lock，不在 Go 侧重新实现。`vocab_manager.py` 已有 `fetch_locked_until` 列和 `--fetch-lock-hours` 标志。Go 层通过 `DueOpts.FetchLockHours` 参数控制锁定时长，**不**实现独立的锁逻辑。尊重 Phase 4 写分离契约（Python 拥有 cards 表写入）。

**D-02-a: Fetch Lock 行为契约** — 当卡片被锁定时（`fetch_locked_until > NOW`），`RunDue` 返回空数组而非错误。空结果是"另一个会话持有锁"的信号，不是系统故障。

**D-03: 默认配置策略** — 扩展 Phase 1 `Config` 结构体，添加 `fsrs.*` 节下的 FSRS 操作字段：`DefaultWordCount`（默认 25，范围 20-30）、`FetchLockHours`（默认 24.0）、`DefaultUnit`（默认 ""）、`DefaultCategory`（默认 "english"）。遵循 Phase 1 D-52/D-53 分层加载器范式（defaults → file → env，`HTA_FSRS_` 前缀）。

**D-04: 复用 Phase 3 全局信号量** — 不引入新的 FSRS 专用并发限制。`pybridge.Executor` 已限制并发 Python 进程数（D-02 全局有界信号量）。`FSRSService` 方法共享此限制。

**D-05: 错误处理遵循 Phase 1 结构化日志范式** — 所有来自 `RunDue`/`RunReview` 的错误用上下文包装（"fsrs service: select due cards"）并通过 `infra.Logger` 记录。语义错误（timeout/empty/parse）保留 pybridge sentinel 类型供调用方分支。FSRS 层无重试逻辑。

### Claude's Discretion

CONTEXT.md 未标记任何 Claude 自由裁量区域——所有 5 个决策均已锁定。

### Deferred Ideas (OUT OF SCOPE)

- **高级重试逻辑** → Phase 7 会话编排。Phase 5 暴露原始错误，调用方决定重试策略。
- **FSRS 参数调优 UI** → Phase 9（家长词汇管理）。Phase 5 仅传递配置。
- **分析/指标** → Phase 10（FEED-04 需求）。Phase 5 仅聚焦核心编排。
- **替代 FSRS 引擎** → 明确不计划（Phase 3 D-09）。Python vocab_manager.py 是永久引擎。
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| FSRS-01 | System automatically selects due words based on spaced repetition algorithm | `pybridge.Executor.RunDue` 已实现（Phase 3）；FSRSService.SelectDueCards 委托调用，将 SelectOpts 映射为 DueOpts 转发至 Python FSRS 引擎 |
| FSRS-02 | System supports configurable word count limits (20-30 words default) | FSRSConfig.DefaultWordCount（默认 25）流入 SelectDueCards 作为 Limit 默认值；SelectOpts.Limit 可覆盖；Config.Validate 强制范围 [1, 100] |
| FSRS-03 | System implements fetch-lock pattern for session isolation | Python 侧 `fetch_locked_until` 列 + `--fetch-lock-hours` 已实现；FSRSService 通过 DueOpts.FetchLockHours 传递配置值；锁激活时返回空数组（D-02-a） |
| FSRS-04 | System updates FSRS parameters based on parent feedback ratings | `pybridge.Executor.RunReview` 已实现（Phase 3）；FSRSService.UpdateRatings 委托调用，ratings map 原样转发（good/hard/again/easy 四级已验证） |
</phase_requirements>

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| 到期单词选择（FSRS-01） | Go API / Service (`pkg/fsrs`) | Python Engine (`vocab_manager.py`) | Go 层编排参数构造和结果传递；FSRS 算法计算在 Python（D-09/ADR 锁定） |
| 单词数量限制（FSRS-02） | Go API / Service (`pkg/fsrs`) | Go Config (`pkg/infra`) | Go 层从 Config 读取默认值并应用；Python 接收 `--limit` 标志执行截断 |
| Fetch-lock 会话隔离（FSRS-03） | Python Engine (`vocab_manager.py`) | Go API / Service (`pkg/fsrs`) | 锁逻辑（`BEGIN IMMEDIATE` + `fetch_locked_until` 更新）在 Python 侧；Go 仅传递 `FetchLockHours` 参数 |
| FSRS 参数更新（FSRS-04） | Python Engine (`vocab_manager.py`) | Go API / Service (`pkg/fsrs`) | FSRS 状态计算（stability/difficulty/due）在 Python；Go 原样转发 ratings 并传递更新结果 |
| 配置管理 | Go Config (`pkg/infra`) | — | FSRSConfig 属于 Go 操作性配置，无密钥，遵循 D-24 规则 |
| 错误分类与日志 | Go API / Service (`pkg/fsrs`) | Go Infra (`pkg/infra`) | 错误包装+上下文化在 Go 侧；结构化日志通过 infra.Logger |

## Domain Understanding (FSRS Algorithm)

### 什么是 FSRS

FSRS（Free Spaced Repetition Scheduler）是由 Jarrett Ye 开发的现代间隔重复算法，基于三组件记忆模型（Three-Component Model of Memory），使用科学记忆研究优化复习调度。它是 Anki 传统 SM-2 算法的替代方案，已内置于 Anki 23.10+。`[ASSUMED]` — 基于 Python 代码中的 `from fsrs import Scheduler, Card, Rating, State` 和训练知识；Web 搜索受限未能在线验证。

### 核心概念（本阶段仅作编排，无需在 Go 中实现）

以下概念在 **Python** `vocab_manager.py` 中实现。Go 层仅需要理解它们以正确传递参数和解释结果。

| 概念 | 说明 | Go 层可见性 |
|------|------|------------|
| **Stability (S)** | 记忆稳定性。值越高=遗忘越慢=下次复习间隔越长。成功回忆时增加，遗忘时大幅减少。 | DueCard.Stability (float64)、UpdatedCard.Stability (float64) — 只读传递 |
| **Difficulty (D)** | 卡片难度（约 1-10）。挣扎（again/hard）时增加，轻松（easy）时减少。调制 stability 增长速度。 | DueCard.Difficulty (float64) — 只读传递 |
| **Retention (R)** | 在给定时刻能回忆起卡片的概率。由 stability 和经过时间通过幂律遗忘曲线计算。 | 不可见（Python Scheduler 内部）；通过 `desired_retention=0.85` 配置间接影响 |
| **State** | 卡片状态：Learning（学习中）、Review（复习中）、Relearning（重新学习中） | DueCard.State (string)、UpdatedCard.State (string) — 只读传递 |
| **Rating** | 评价等级：Again（忘了）、Hard（困难）、Good（良好）、Easy（简单） | ReviewOpts.Results map[string]string — 原样转发，Go 不验证 |

### Python FSRS 引擎配置（已锁定，本阶段不修改）

`[VERIFIED: codebase]` — `third_party/python/vocab_manager.py` lines 78-84:

```python
SCHEDULER = Scheduler(
    desired_retention=0.85,       # 为小学生降低压力
    learning_steps=(timedelta(minutes=1), timedelta(minutes=10)),
    relearning_steps=(timedelta(minutes=5),),
    maximum_interval=180,          # 最长 6 个月
    enable_fuzzing=False,
)
```

### 四级评分验证

`[VERIFIED: codebase]` — `vocab_manager.py` lines 359-364 确认所有四级评分均被接受：

```python
rating_map = {
    "good": Rating.Good,
    "hard": Rating.Hard,
    "again": Rating.Again,
    "easy": Rating.Easy,   # 已验证——Phase 3 Pitfall 4 的关注点已消除
}
```

Go 层 **不** 验证也不映射评分——原样转发（Phase 3 executor.go 契约）。任何未知评分导致 Python 输出 Warning 行并跳过该卡片，整体调用仍 exit 0 成功。

## Standard Stack

### Core

本阶段 **不安装任何新的 Go 依赖包**。所有功能基于现有依赖：

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/pkg/errors` | v0.9.1 | 错误包装 + 上下文 (`errors.Wrap`) | 全代码库统一使用（pybridge/database/infra）`[VERIFIED: go.mod]` |
| `github.com/xiaotian/home-tutor-agent/pkg/infra` | (internal) | Logger 接口、Config 结构体、Field/ErrField | Phase 1 DI + 结构化日志基础设施 `[VERIFIED: codebase]` |
| `github.com/xiaotian/home-tutor-agent/pkg/pybridge` | (internal) | Executor 接口、DueOpts/ReviewOpts/DueCard 等类型 | Phase 3 已交付的 Python 桥接层 `[VERIFIED: codebase]` |
| `github.com/spf13/viper` | v1.21.0 | 配置加载（FSRSConfig 扩展） | Phase 1 已使用，仅添加新结构体字段 `[VERIFIED: go.mod]` |

### Supporting

无辅助库。本阶段是纯 Go 编排逻辑。

### Alternatives Considered

无需考虑替代方案——所有决策已由 CONTEXT.md 锁定。

**Installation:**
```bash
# 无需安装——所有依赖已在 go.mod 中
```

**Version verification:** 所有包已在 go.mod 中验证，版本固定。

## Package Legitimacy Audit

本阶段 **不安装任何新的外部包**。无 Go 包需审计。Python `fsrs>=0.0.1` 包已在 Phase 3 审计并通过（`requirements.txt` line 12），本阶段不修改 Python 依赖。

**Packages removed due to [SLOP] verdict:** 不适用
**Packages flagged as suspicious [SUS]:** 不适用

## Architecture Patterns

### System Architecture Diagram

```
                    Phase 5 FSRSService 编排数据流

  ┌─────────────────────────────────────────────────────────────────┐
  │                     调用方（Phase 7/10）                         │
  │              SelectDueCards(ctx, SelectOpts)                    │
  │              UpdateRatings(ctx, ratings)                        │
  └─────────────────────────┬───────────────────────────────────────┘
                            │
                            ▼
  ┌─────────────────────────────────────────────────────────────────┐
  │                   pkg/fsrs (本阶段交付)                          │
  │                                                                 │
  │  FSRSService 接口                                               │
  │    ├─ SelectDueCards: 合并 SelectOpts + Config 默认值            │
  │    │   → 映射为 pybridge.DueOpts                                 │
  │    │   → 调用 exec.RunDue                                       │
  │    │   → 返回 []pybridge.DueCard（含空数组=锁激活信号）          │
  │    │                                                             │
  │    └─ UpdateRatings: 映射 ratings → pybridge.ReviewOpts          │
  │        → 调用 exec.RunReview                                    │
  │        → 返回 pybridge.ReviewResult                             │
  │                                                                 │
  │  错误处理: 包装 pybridge sentinel + infra.Logger 结构化日志     │
  └─────────────────────────┬───────────────────────────────────────┘
                            │ pybridge.Executor 接口（Phase 3 已交付）
                            ▼
  ┌─────────────────────────────────────────────────────────────────┐
  │              pkg/pybridge (Phase 3 交付，不修改)                 │
  │                                                                 │
  │  Bridge.RunDue  ──→  D-02 全局信号量（MaxConcurrent=2）         │
  │    ├─ 构造 argv [--db, path, due, --limit, ...]                 │
  │    ├─ exec.CommandContext (argv, 无 shell)                      │
  │    ├─ 捕获 stdout/stderr + 分类为 sentinel 错误                 │
  │    └─ extractTrailingJSON → json.Unmarshal → DueResult          │
  │                                                                 │
  │  Bridge.RunReview ──→  D-02 全局信号量                          │
  │    ├─ reviewArgs（自动选 --results vs --file）                  │
  │    ├─ ratings 原样转发（无验证、无映射）                         │
  │    └─ extractTrailingJSON → json.Unmarshal → ReviewResult       │
  └─────────────────────────┬───────────────────────────────────────┘
                            │ subprocess (python3 vocab_manager.py)
                            ▼
  ┌─────────────────────────────────────────────────────────────────┐
  │         third_party/python/vocab_manager.py (永久 FSRS 引擎)     │
  │                                                                 │
  │  cmd_due:  BEGIN IMMEDIATE → SELECT due cards                   │
  │    → 过滤 fetch_locked_until > NOW（锁激活时返回空）             │
  │    → Card.from_json → 检查 due <= now                           │
  │    → 排序: Learning 优先, 再按 due date                          │
  │    → UPDATE fetch_locked_until = NOW + lock_hours               │
  │    → 输出 JSON 数组                                              │
  │                                                                 │
  │  cmd_review: 遍历 ratings → rating_map 查找                     │
  │    → SCHEDULER.review_card(card, rating, now)                   │
  │    → UPDATE cards.fsrs_card + INSERT review_logs                │
  │    → 输出 {"updated": [...]} JSON                               │
  └─────────────────────────┬───────────────────────────────────────┘
                            │ sqlite3 (WAL mode)
                            ▼
  ┌─────────────────────────────────────────────────────────────────┐
  │           SQLite vocab.db (Python 拥有写权限)                    │
  │  cards 表 (fsrs_card JSON, fetch_locked_until)                  │
  │  review_logs 表                                                 │
  │  Go 仅通过 CardRepository 读取（本阶段不使用）                   │
  └─────────────────────────────────────────────────────────────────┘
```

### Recommended Project Structure

```
pkg/
├── fsrs/                    # [新建] FSRS 编排服务层
│   ├── service.go           # FSRSService 接口 + fsrsService 实现 + NewFSRSService 构造函数
│   ├── service_test.go      # 表驱动测试 + 手动 fake Executor (D-48)
│   └── doc.go               # 包文档（可选，遵循 pybridge 范式）
├── infra/
│   └── config.go            # [修改] 添加 FSRSConfig + DefaultConfig + Validate + bindEnv
├── pybridge/                # [不修改] Phase 3 已交付
│   ├── executor.go          # Executor 接口 + DueOpts/ReviewOpts/DueResult/ReviewResult
│   ├── vocab.go             # RunDue/RunReview + dueArgs/reviewArgs
│   └── bridge.go            # Bridge 实现 + D-02 信号量
└── database/
    └── card_repo.go         # [不修改] 本阶段不直接使用
cmd/
└── service/
    └── main.go              # [修改] 构造 FSRSService 并接线（替换 _ = pyBridge 占位）
```

### Pattern 1: DI 构造函数范式

**What:** 构造函数接受依赖接口 + 配置 + logger，逐字段验证，nil-logger 替换。
**When to use:** 所有 pkg/ 下新包的构造函数。
**Source:** Phase 1 D-42/D-47，镜像 `pybridge.NewBridge` 和 `database.NewCardRepository`。

```go
// Source: pkg/pybridge/bridge.go NewBridge (lines 127-170) — 遵循此范式
// FSRSService 应接受 infra.FSRSConfig（子结构体），而非整个 infra.Config，
// 与 NewBridge(cfg PythonConfig) 保持一致

func NewFSRSService(exec pybridge.Executor, logger infra.Logger, cfg infra.FSRSConfig) (FSRSService, error) {
    if exec == nil {
        return nil, errors.Wrap(ErrFSRSInvalid, "pybridge.Executor must not be nil")
    }
    // FSRSConfig.Validate 已在 LoadConfig 时执行，此处是防御性二次验证
    if err := cfg.Validate(); err != nil {
        return nil, errors.Wrap(ErrFSRSInvalid, "fsrs config validation failed")
    }
    if logger == nil {
        logger = infra.NewNoOpLogger() // D-47: 永远非 nil
    }
    return &fsrsService{exec: exec, logger: logger, cfg: cfg}, nil
}
```

### Pattern 2: 编译期接口满足检查

**What:** `var _ Interface = (*Concrete)(nil)` 在编译期验证实现满足接口。
**Source:** Phase 1 D-43，pybridge/bridge.go line 45、database/card_repo.go line 59。

```go
// 编译期检查——接口漂移时编译失败而非运行时 panic
var _ FSRSService = (*fsrsService)(nil)
```

### Pattern 3: SelectOpts → DueOpts 默认值合并

**What:** 调用方通过 SelectOpts 覆盖配置默认值。零值字段使用 Config 默认。
**When to use:** SelectDueCards 方法入口处。

```go
// SelectOpts 是 fsrs 包的输入类型——不暴露 pybridge.DueOpts 给调用方
// 调用方只需要语义字段，不需要了解 pybridge 内部映射
type SelectOpts struct {
    Limit          int      // <= 0 → 使用 cfg.DefaultWordCount
    Units          []string // nil/empty → 使用 cfg.DefaultUnit（若配置）
    Category       string   // empty → 使用 cfg.DefaultCategory
    Exclude        []string // 原样传递
    Peek           bool     // 原样传递
    FetchLockHours float64  // <= 0 → 使用 cfg.FetchLockHours
}

// 合并逻辑：SelectOpts 覆盖 Config 默认值
func (s *fsrsService) SelectDueCards(ctx context.Context, opts SelectOpts) ([]pybridge.DueCard, error) {
    limit := opts.Limit
    if limit <= 0 {
        limit = s.cfg.DefaultWordCount // FSRS-02: 默认 25
    }
    category := opts.Category
    if category == "" {
        category = s.cfg.DefaultCategory
    }
    fetchLock := opts.FetchLockHours
    if fetchLock <= 0 {
        fetchLock = s.cfg.FetchLockHours
    }
    units := opts.Units
    if len(units) == 0 && s.cfg.DefaultUnit != "" {
        units = []string{s.cfg.DefaultUnit}
    }

    dueOpts := pybridge.DueOpts{
        Category:       category,
        Limit:          limit,
        Units:          units,
        Exclude:        opts.Exclude,
        Peek:           opts.Peek,
        FetchLockHours: fetchLock,
    }

    result, err := s.exec.RunDue(ctx, dueOpts)
    if err != nil {
        s.logger.Error("fsrs service: select due cards failed",
            infra.F("component", "fsrs"),
            infra.ErrField(err),
        )
        return nil, errors.Wrap(err, "fsrs service: select due cards")
    }
    return result.Cards, nil
}
```

### Pattern 4: UpdateRatings 原样转发

**What:** ratings map 直接映射为 ReviewOpts.Results，原样转发。无验证、无映射。

```go
func (s *fsrsService) UpdateRatings(ctx context.Context, ratings map[string]string, category string) (pybridge.ReviewResult, error) {
    reviewOpts := pybridge.ReviewOpts{
        Results:  ratings,  // 原样转发——good/hard/again/easy 由 Python 验证
        Category: category, // 可选，用于多类别消歧
    }

    result, err := s.exec.RunReview(ctx, reviewOpts)
    if err != nil {
        s.logger.Error("fsrs service: update ratings failed",
            infra.F("component", "fsrs"),
            infra.F("rating_count", len(ratings)),
            infra.ErrField(err),
        )
        return pybridge.ReviewResult{}, errors.Wrap(err, "fsrs service: update ratings")
    }
    return result, nil
}
```

### Pattern 5: 配置扩展范式

**What:** 向 Config 结构体添加新子结构体，遵循 PythonConfig/DatabaseConfig 的既定模式。
**Source:** `pkg/infra/config.go` — PythonConfig (lines 195-256)、DatabaseConfig (lines 177-189)。

四处必须同步修改：
1. `Config` 结构体添加 `FSRS FSRSConfig` 字段
2. `FSRSConfig` 类型定义（含 json/mapstructure 标签 + 文档）
3. `DefaultConfig()` 添加 FSRS 默认值
4. `Validate()` 添加 FSRS 字段验证规则
5. `bindEnvForAllLeaves()` 添加 `fsrs.*` 叶子节点 env 绑定

```go
// Source: 遵循 pkg/infra/config.go 中 PythonConfig 的模式
type FSRSConfig struct {
    // DefaultWordCount is the default number of due cards to select per
    // session when SelectOpts.Limit is not specified (FSRS-02). Default 25,
    // within the 20-30 range from ROADMAP.md. MUST be in [1, 100].
    DefaultWordCount int `json:"defaultWordCount" mapstructure:"defaultWordCount"`

    // FetchLockHours is the default fetch-lock duration in hours forwarded
    // to vocab_manager.py's --fetch-lock-hours flag (FSRS-03). Default 24.0.
    // MUST be > 0. Zero/negative means "use Python default" but infra layer
    // enforces a positive value for predictability.
    FetchLockHours float64 `json:"fetchLockHours" mapstructure:"fetchLockHours"`

    // DefaultUnit filters due cards to a specific unit when SelectOpts.Units
    // is empty. Default "" means all units.
    DefaultUnit string `json:"defaultUnit" mapstructure:"defaultUnit"`

    // DefaultCategory selects the vocabulary category (FSRS-01). Default
    // "english" matching vocab_manager.py's default.
    DefaultCategory string `json:"defaultCategory" mapstructure:"defaultCategory"`
}

// Validate enforces FSRSConfig field constraints (mirrors the inline
// validation pattern in Config.Validate for Python/WS blocks).
func (c FSRSConfig) Validate() error {
    if c.DefaultWordCount < 1 || c.DefaultWordCount > 100 {
        return errors.Wrap(ErrConfigInvalid,
            fmt.Sprintf("fsrs.defaultWordCount must be in [1, 100], got %d",
                c.DefaultWordCount))
    }
    if c.FetchLockHours <= 0 {
        return errors.Wrap(ErrConfigInvalid, "fsrs.fetchLockHours must be > 0")
    }
    if c.DefaultCategory == "" {
        return errors.Wrap(ErrConfigInvalid, "fsrs.defaultCategory is required")
    }
    return nil
}
```

### Anti-Patterns to Avoid

- **在 Go 中重新实现 FSRS 算法** — Phase 3 D-09 和 ADR 明确禁止。Go 仅编排。所有 stability/difficulty/due 计算在 Python。
- **在 Go 中验证或映射评分** — Phase 3 executor.go 契约明确：ratings 原样转发。映射决策属于 Phase 10（FEED）。
- **引入 FSRS 专用并发限制** — D-04 明确复用 Phase 3 全局信号量。新并发原语违反资源约束设计。
- **直接写 cards 表** — ADR 写分离契约：Go 绝不写 Python 拥有的表。所有写操作经 RunReview。
- **在 FSRS 层添加重试逻辑** — D-05 明确禁止。重试是调用方（Phase 7）的职责。
- **解析 fsrs_card JSON 字段** — Phase 4 Pitfall 5：`Card.FsrsCard` 是不透明 JSON 字符串。Go 绝不解析为类型化字段。需要 FSRS 数据时调用 RunDue。

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| 到期单词选择 | Go SQL 查询 + FSRS 算法 | `pybridge.Executor.RunDue` | FSRS 算法已在 Python 实现（D-09）；Go 查询会绕过 Python 的 `fetch_locked_until` 过滤和 `BEGIN IMMEDIATE` 原子性 |
| FSRS 参数计算 | Go stability/difficulty 数学 | `pybridge.Executor.RunReview` | FSRS Scheduler 是 Python fsrs 库的核心，17-19 个学习参数，Go 重写不可能精确复制 |
| Fetch-lock 逻辑 | Go mutex / 数据库锁 | Python `fetch_locked_until` + `--fetch-lock-hours` | 锁已用 `BEGIN IMMEDIATE` 原子实现在 Python；Go 侧锁会造成双重锁竞态 |
| 并发控制 | FSRS 专用信号量 / channel | Phase 3 D-02 全局信号量（`pybridge.Bridge.sem`） | 已限制 MaxConcurrent=2 Python 进程；额外限制只会串行化已有串行路径 |
| 子进程调用 | os/exec 直接调用 | `pybridge.Executor` 接口 | 桥接层已处理 argv 构造、信号量、超时、进程组杀死、JSON 解析、sentinel 错误分类 |
| 错误分类 | 字符串匹配 stderr | `errors.Is(err, pybridge.ErrPythonTimeout)` 等 | D-04 sentinel 模式：调用方用 errors.Is 分支，不解析文本 |

**Key insight:** Phase 5 的全部价值在于将 pybridge.Executor 的低级 subprocess 契约提升为面向业务的语义接口（SelectDueCards/UpdateRatings），合并配置默认值，添加结构化日志。任何"在 Go 中做更多"的冲动都违反 D-09/ADR。

## Common Pitfalls

### Pitfall 1: 空数组 vs 错误的语义混淆

**What goes wrong:** 调用方将 `SelectDueCards` 返回的空 `[]DueCard` 误判为系统故障（"没有单词可用"）而非正常信号（"卡片被另一个会话锁定"）。
**Why it happens:** Python `cmd_due` 在卡片被锁时过滤掉它们，返回空数组（exit 0），不返回错误。
**How to avoid:** 按 D-02-a 契约文档化：空数组是合法结果。FSRSService.SelectDueCards 返回 `([]DueCard, nil)` 而非 `(nil, ErrLocked)`。Phase 7 负责区分"无到期卡"和"全部被锁"。
**Warning signs:** 如果 SelectDueCards 在空结果时返回 error，说明违反了 D-02-a 契约。

### Pitfall 2: `--db` 参数顺序错误（继承自 Phase 3）

**What goes wrong:** `vocab_manager.py` 的 argparse 在顶层解析器声明 `--db`，必须在子命令 token **之前**。如果 argv 顺序错误，argparse 拒绝 `--db` 为未知子命令标志。
**Why it happens:** `dueArgs` 已正确处理（vocab.go line 64: `args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "due"}`），但 FSRSService **不直接构造 argv**——它通过 DueOpts 委托给 RunDue。只要不绕过 pybridge，此 pitfall 已被 Phase 3 消除。
**How to avoid:** FSRSService 永远通过 `pybridge.Executor.RunDue/RunReview` 调用 Python，永远不直接构造 argv 或 exec.Command。
**Warning signs:** 如果 fsrs 包导入 `os/exec`，说明架构违反。

### Pitfall 3: Warning 行混入 JSON 输出（继承自 Phase 3）

**What goes wrong:** `vocab_manager.py` 的 review 命令在未知评分/缺失卡片/多类别歧义时输出 `Warning: ...` 行到 stdout，**在** JSON payload 之前。这不是错误（exit 0）。
**Why it happens:** Python 脚本的设计——Warning 行是诊断，JSON 是结果。
**How to avoid:** `extractTrailingJSON`（Phase 3 已实现）从尾部向后扫描剥离 JSON。FSRSService 通过 RunReview 自动受益于此。如果 RunReview 返回 `ErrPythonJSONParse`，检查是否 Python 脚本崩溃。
**Warning signs:** `ReviewResult.Updated` 为空但 exit 0 + 非空 stdout——检查日志中 logResult 捕获的 stdout 是否有 Warning 行。

### Pitfall 4: Config 默认值未流入 SelectOpts

**What goes wrong:** SelectDueCards 直接透传 SelectOpts 的零值字段（Limit=0, Category=""），绕过 Config 默认值。
**Why it happens:** 零值在 Go 中是合法值——`int` 零值是 0 而非"未设置"。如果没有显式的默认值合并逻辑，0 会被传给 Python（vocab_manager 将 limit<=0 解释为"不传 --limit"=无限制）。
**How to avoid:** 在 SelectDueCards 入口处显式检查每个零值字段并用 cfg 默认替换（见 Pattern 3 代码示例）。
**Warning signs:** 实际返回的卡片数远超 25——说明 Limit=0 被透传，Python 未截断。

### Pitfall 5: context.Context 未传播

**What goes wrong:** FSRSService 方法签名遗漏 `context.Context`，导致无法取消正在进行的 Python 调用。
**Why it happens:** 接口设计时忘记 ctx 是第一个参数的 Go 惯例。
**How to avoid:** 所有公共方法签名以 `ctx context.Context` 开头，并转发给 `exec.RunDue(ctx, ...)` / `exec.RunReview(ctx, ...)`。pybridge 的信号量 acquire 和 per-call timeout 都依赖此 ctx。
**Warning signs:** 如果 RunDue 在服务关闭时仍阻塞——说明 ctx 未传播。

### Pitfall 6: FSRSConfig 验证遗漏

**What goes wrong:** 新增 FSRSConfig 字段但忘记在 `Config.Validate()` 或 `bindEnvForAllLeaves()` 中注册。
**Why it happens:** config.go 有四处需同步修改（结构体/默认值/验证/env 绑定），遗漏任一处导致静默行为异常。
**How to avoid:** 使用检查清单：(1) Config 结构体字段 (2) FSRSConfig 类型+文档 (3) DefaultConfig() (4) Validate() (5) bindEnvForAllLeaves()。添加 `TestFSRSConfig*` 表驱动测试验证范围规则。
**Warning signs:** `HTA_FSRS_DEFAULTWORDCOUNT=30` 环境变量不生效——说明 bindEnv 遗漏。

## Code Examples

### 完整 FSRSService 接口定义

```go
// Source: 遵循 pybridge.Executor (executor.go) + database.CardRepository (card_repo.go) 接口范式
package fsrs

import (
    "context"

    "github.com/xiaotian/home-tutor-agent/pkg/infra"
    "github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// FSRSService orchestrates FSRS spaced-repetition operations by delegating to
// the Python vocab_manager.py engine via pybridge.Executor (Phase 3 D-09).
// Go NEVER reimplements the FSRS algorithm—it only composes parameters,
// forwards calls, and passes results. All FSRS state mutations flow through
// RunReview; Go never writes to the cards table directly (ADR write-separation).
type FSRSService interface {
    // SelectDueCards returns due cards for a dictation session (FSRS-01/02/03).
    // opts overrides config defaults; zero-value fields use FSRSConfig defaults.
    // Returns an empty slice (not an error) when cards are fetch-locked (D-02-a).
    SelectDueCards(ctx context.Context, opts SelectOpts) ([]pybridge.DueCard, error)

    // UpdateRatings submits parent feedback ratings to the FSRS engine (FSRS-04).
    // ratings map word->rating (good/hard/again/easy) forwarded verbatim.
    // category disambiguates words existing in multiple categories.
    UpdateRatings(ctx context.Context, ratings map[string]string, category string) (pybridge.ReviewResult, error)
}
```

### 手动 Fake Executor（测试用）

```go
// Source: Phase 1 D-48 手动 fake 范式，镜像 executor_test.go 的测试风格
package fsrs

import (
    "context"

    "github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// fakeExecutor is a manual fake pybridge.Executor for table-driven tests.
// No mocking framework (D-48)—just a struct that records calls and returns
// preset values.
type fakeExecutor struct {
    // Preset return values
    dueResult    pybridge.DueResult
    dueErr       error
    reviewResult pybridge.ReviewResult
    reviewErr    error
    pregenResult pybridge.PregenResult
    pregenErr    error

    // Call capture (for assertion)
    dueCalls    []pybridge.DueOpts
    reviewCalls []pybridge.ReviewOpts
}

func (f *fakeExecutor) RunDue(_ context.Context, opts pybridge.DueOpts) (pybridge.DueResult, error) {
    f.dueCalls = append(f.dueCalls, opts)
    return f.dueResult, f.dueErr
}

func (f *fakeExecutor) RunReview(_ context.Context, opts pybridge.ReviewOpts) (pybridge.ReviewResult, error) {
    f.reviewCalls = append(f.reviewCalls, opts)
    return f.reviewResult, f.reviewErr
}

func (f *fakeExecutor) RunPregenerate(_ context.Context, opts pybridge.PregenOpts) (pybridge.PregenResult, error) {
    return f.pregenResult, f.pregenErr
}
```

### 表驱动测试范式

```go
// Source: Phase 1 D-28/D-29 表驱动测试范式，镜像 vocab_test.go TestDueArgs 风格
func TestSelectDueCardsConfigDefaults(t *testing.T) {
    cases := []struct {
        name      string
        cfg       infra.FSRSConfig
        opts      SelectOpts
        wantLimit int
        wantCat   string
    }{
        {
            name:      "opts override config",
            cfg:       infra.FSRSConfig{DefaultWordCount: 25, DefaultCategory: "english", FetchLockHours: 24},
            opts:      SelectOpts{Limit: 30, Category: "chinese"},
            wantLimit: 30,
            wantCat:   "chinese",
        },
        {
            name:      "config defaults when opts zero",
            cfg:       infra.FSRSConfig{DefaultWordCount: 25, DefaultCategory: "english", FetchLockHours: 24},
            opts:      SelectOpts{}, // all zero values
            wantLimit: 25,
            wantCat:   "english",
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            fake := &fakeExecutor{
                dueResult: pybridge.DueResult{Cards: []pybridge.DueCard{{ID: 1, Word: "test"}}},
            }
            svc, err := NewFSRSService(fake, nil, tc.cfg)
            if err != nil {
                t.Fatalf("NewFSRSService: %v", err)
            }
            _, err = svc.SelectDueCards(context.Background(), tc.opts)
            if err != nil {
                t.Fatalf("SelectDueCards: %v", err)
            }
            if len(fake.dueCalls) != 1 {
                t.Fatalf("expected 1 RunDue call, got %d", len(fake.dueCalls))
            }
            if fake.dueCalls[0].Limit != tc.wantLimit {
                t.Errorf("Limit: got %d, want %d", fake.dueCalls[0].Limit, tc.wantLimit)
            }
            if fake.dueCalls[0].Category != tc.wantCat {
                t.Errorf("Category: got %q, want %q", fake.dueCalls[0].Category, tc.wantCat)
            }
        })
    }
}
```

### main.go 接线范式

```go
// Source: cmd/service/main.go 现有 pyBridge 构造（lines 115-123）的延续
// 替换现有的 _ = pyBridge 占位（line 123）

pyBridge, err := pybridge.NewBridge(cfg.Python, logger)
if err != nil {
    logger.Error("pybridge construction failed",
        infra.F("component", "infra.main"),
        infra.ErrField(err),
    )
    return 1
}

// Phase 5: FSRS orchestration service.
// Composes pyBridge via the Executor interface. No subprocess spawned at
// construction—same pure-constructor contract as NewBridge.
fsrsSvc, err := fsrs.NewFSRSService(pyBridge, logger, cfg.FSRS)
if err != nil {
    logger.Error("fsrs service construction failed",
        infra.F("component", "infra.main"),
        infra.ErrField(err),
    )
    return 1
}
_ = fsrsSvc // consumed by Phase 7 session orchestration
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| ROADMAP 原计划 Go 实现 FSRS | Python 永久引擎 + Go 编排（D-09/ADR） | Phase 3 / Phase 4 ADR (2026-08-01) | Phase 5 范围从"算法实现"缩减为"编排层"；工作量大幅降低 |
| cards 表 Go 可写 | 严格写分离（ADR） | Phase 4 ADR (2026-08-01) | Go 绝不写 cards/review_logs；所有 FSRS 变更经 RunReview |
| `--db` 任意位置 | `--db` 必须在子命令前 | Phase 3 RESEARCH 发现 | dueArgs/reviewArgs 已正确处理；FSRSService 不直接构造 argv |

**Deprecated/outdated:**
- vocab_manager.py help text 中 review 命令的 `good|hard|again` 描述 — 实际代码（lines 359-364）已支持 `easy`（Phase 5 FINAL-REVIEW 已验证）

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | FSRS 概念描述（stability/difficulty/retention）基于训练知识，Web 搜索受限未在线验证 | Domain Understanding | 低风险——概念仅供理解，Phase 5 不实现算法；Python 代码是唯一权威 |
| A2 | `NewFSRSService` 应接受 `infra.FSRSConfig` 而非整个 `infra.Config`（推断自 NewBridge 范式） | Pattern 1/5 | 低风险——planner 可调整为 `cfg infra.Config`；功能等价 |
| A3 | FSRSService 返回 `pybridge.ReviewResult` 而非新建 `UpdateResult` 类型 | Pattern 4 | 低风险——planner 可选择创建包装类型；增加样板代码但无功能差异 |
| A4 | `UpdateRatings` 需要额外的 `category` 参数用于多类别消歧（推断自 ReviewOpts.Category） | Pattern 4 | 低风险——可改为从 Config 读取默认 category |

**If this table is empty:** 不适用——有 4 个 [ASSUMED] 标记，均属低风险设计选择，planner 可调整。

## Open Questions

1. **SelectOpts 是否需要 `Context`/`TraceID` 字段？**
   - What we know: Phase 7 会话编排会调用 SelectDueCards；会话可能有 trace ID
   - What's unclear: Phase 5 是否需要在 SelectOpts 中传递会话上下文（session ID/trace ID）用于日志关联
   - Recommendation: 不添加——context.Context 已传播（Pitfall 5），结构化日志可由调用方在调用前后添加会话字段。Phase 7 可按需扩展。

2. **FSRSService 是否需要 metrics 指标？**
   - What we know: Phase 1 有 infra.MetricsHandler；FEED-04（分析/指标）deferred 到 Phase 10
   - What's unclear: Phase 5 是否应暴露 due-card-count / review-count 计数器
   - Recommendation: 不添加——D-03 deferred analytics 到 Phase 10。Phase 5 仅记录结构化日志（已含 duration_ms 字段）。

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` (go 1.26) |
| Config file | 无（Makefile `test` target: `go test -race -v ./...`） |
| Quick run command | `go test -v -short ./pkg/fsrs/... ./pkg/infra/...` |
| Full suite command | `make test` (= `go test -race -v ./...`) |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| FSRS-01 | SelectDueCards 委托 RunDue 返回到期卡片 | unit | `go test -v -run TestSelectDueCards ./pkg/fsrs/...` | ❌ Wave 0 |
| FSRS-01 | SelectDueCards 错误透传 pybridge sentinel | unit | `go test -v -run TestSelectDueCardsError ./pkg/fsrs/...` | ❌ Wave 0 |
| FSRS-02 | Config 默认值流入 DueOpts.Limit | unit | `go test -v -run TestSelectDueCardsConfigDefaults ./pkg/fsrs/...` | ❌ Wave 0 |
| FSRS-02 | SelectOpts.Limit 覆盖 Config 默认 | unit | `go test -v -run TestSelectDueCardsConfigDefaults ./pkg/fsrs/...` | ❌ Wave 0 |
| FSRS-03 | FetchLockHours 从 Config 流入 DueOpts | unit | `go test -v -run TestSelectDueCardsFetchLock ./pkg/fsrs/...` | ❌ Wave 0 |
| FSRS-03 | 空数组返回（锁激活信号，非错误） | unit | `go test -v -run TestSelectDueCardsLockedReturnsEmpty ./pkg/fsrs/...` | ❌ Wave 0 |
| FSRS-04 | UpdateRatings 委托 RunReview 原样转发 ratings | unit | `go test -v -run TestUpdateRatings ./pkg/fsrs/...` | ❌ Wave 0 |
| FSRS-04 | UpdateRatings 错误透传 | unit | `go test -v -run TestUpdateRatingsError ./pkg/fsrs/...` | ❌ Wave 0 |
| CFG | FSRSConfig.DefaultWordCount 范围验证 [1,100] | unit | `go test -v -run TestFSRSConfigValidate ./pkg/infra/...` | ❌ Wave 0 |
| CFG | FSRSConfig env 绑定 (HTA_FSRS_*) | unit | `go test -v -run TestFSRSConfigEnvBinding ./pkg/infra/...` | ❌ Wave 0 |
| CFG | Config 无密钥字段反射守卫 | unit | `go test -v -run TestFSRSConfigHasNoSecretFields ./pkg/infra/...` | ❌ Wave 0 |

### Sampling Rate
- **Per task commit:** `go test -v -short ./pkg/fsrs/... ./pkg/infra/...`
- **Per wave merge:** `make test` (full suite with -race)
- **Phase gate:** Full suite green before `/gsd-verify-work`

### Wave 0 Gaps
- [ ] `pkg/fsrs/service_test.go` — 覆盖 FSRS-01/02/03/04（手动 fake Executor + 表驱动）
- [ ] `pkg/infra/config_test.go` 扩展 — 覆盖 FSRSConfig 验证 + env 绑定 + 无密钥守卫
- [ ] 无框架安装需要——Go stdlib `testing` 已在 go.mod 中

*(共享 fixtures: fakeExecutor 结构定义在 service_test.go 中，遵循 D-48 手动 fake 范式)*

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no | 本阶段无认证逻辑 |
| V3 Session Management | no | 会话管理是 Phase 7 |
| V4 Access Control | no | 本阶段无访问控制 |
| V5 Input Validation | yes | SelectOpts 字段 + ratings map 通过 pybridge argv 传递；pybridge 已用 argv slice（无 shell）防止注入（T-03-07） |
| V6 Cryptography | no | 本阶段无加密需求 |

### Known Threat Patterns for Go→Python orchestration

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Shell 注入（恶意 word 值） | Tampering | pybridge 使用 exec.CommandContext argv slice，无 shell 解释（T-03-07）；`[VERIFIED: bridge.go line 285]` |
| 评分值注入 | Tampering | ratings 原样转发，Python rating_map 仅接受 4 个已知值；未知值被跳过（Warning，非崩溃）`[VERIFIED: vocab_manager.py lines 375-377]` |
| argv 长度溢出 | DoS | pybridge reviewArgs 自动切换 --results vs --file（>4096 字节或 >100 条目用临时文件）`[VERIFIED: vocab.go lines 209-248]` |
| 密钥泄露 | Information Disclosure | FSRSConfig 无密钥字段（D-24）；TTS 密钥经 os.Getenv 注入子进程（D-05），不在 Config/日志/ExitError 中 `[VERIFIED: bridge.go pythonSecretEnvKeys]` |

**本阶段无新攻击面** — 所有安全控制继承自 Phase 3（pybridge）和 Phase 1（config）。FSRSService 仅添加业务编排逻辑，不引入新的外部输入路径。

## Sources

### Primary (HIGH confidence)
- `pkg/pybridge/executor.go` — Executor 接口、DueOpts/ReviewOpts/DueResult/ReviewResult 类型定义、sentinel 错误（直接读取验证）
- `pkg/pybridge/vocab.go` — RunDue/RunReview 实现、dueArgs/reviewArgs argv 构造逻辑（直接读取验证）
- `pkg/pybridge/bridge.go` — Bridge 实现、D-02 信号量、NewBridge DI 范式、childEnv/secret forwarding（直接读取验证）
- `pkg/pybridge/semaphore.go` — 全局有界信号量实现（直接读取验证）
- `pkg/infra/config.go` — Config 结构体、PythonConfig/DatabaseConfig 范式、Validate/bindEnvForAllLeaves（直接读取验证）
- `pkg/database/card_repo.go` — CardRepository 只读接口、写分离契约（直接读取验证）
- `third_party/python/vocab_manager.py` — FSRS 引擎、cmd_due（fetch-lock 逻辑）、cmd_review（rating_map 四级验证）、Scheduler 配置（直接读取验证）
- `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` — Python 拥有 FSRS+SQLite 权威、写分离契约（直接读取验证）
- `cmd/service/main.go` — pyBridge 构造接线、cardRepo/sessionRepo 占位（直接读取验证）

### Secondary (MEDIUM confidence)
- `.planning/phases/05-fsrs-core-implementation/05-CONTEXT.md` — D-01~D-05 锁定决策、代码上下文
- `.planning/phases/05-fsrs-core-implementation/05-FINAL-REVIEW.md` — easy 评分验证、fetch-lock 空数组契约确认
- `go.mod` — 依赖版本确认（pkg/errors v0.9.1, viper v1.21.0, zap v1.28.0）

### Tertiary (LOW confidence)
- FSRS 算法概念描述（stability/difficulty/retention） — 基于训练知识 `[ASSUMED]`，Web 搜索受限（rate limit exhausted，reset 2026-08-13）；不影响实现，Python 代码是唯一权威

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — 全部依赖已在 go.mod 验证，无新包
- Architecture: HIGH — 所有模式直接从 codebase 提取（DI/接口/表驱动测试/信号量）
- Pitfalls: HIGH — 6 个 pitfalls 中 5 个直接验证自 codebase，1 个（FSRS 概念）标记 [ASSUMED]
- Config extension: HIGH — PythonConfig/DatabaseConfig 范式直接可循

**Research date:** 2026-08-01
**Valid until:** 2026-08-31（稳定——无外部 API 依赖，纯 codebase 模式）
