---
created: 2026-07-25T05:01:02.742Z
title: "Phase 4 前定 FSRS + SQLite 的 single-source-of-truth 所有权（需 ADR）"
area: planning
files:
  - .planning/ROADMAP.md:128
  - .planning/ROADMAP.md:144
  - third_party/python/vocab_manager.py
  - pkg/pybridge/executor.go:90
---

## Problem

**所有权歧义**——FSRS 算法与 SQLite schema 的 single source of truth 尚未拍板：

- Phase 3 已 vendor 了 `third_party/python/vocab_manager.py`，该脚本**自己就实现了 FSRS + SQLite**（CLAUDE.md 记录 822 行：纯 Python FSRS + SQLite CLI）。
- `pybridge` 现有态度明确：**Python 是权威的**——`pkg/pybridge/executor.go:90` 的 `DueCard`/`UpdatedCard` 字段是"对着脚本真实 JSON 契约逐字核对"的（id/word/meaning/example/unit/state/due/stability/difficulty）。
- **但 ROADMAP 把 Phase 4（"SQLite schema 设计"，`.planning/ROADMAP.md:128`）和 Phase 5（"FSRS 核心实现"，`:144`）写得像从零做 Go 侧实现。

若 Phase 4/5 真用 Go 重写 schema + FSRS，会出现**两套真相源**（Python 一份、Go 一份），迟早漂移；若是想让 Go 直连同一个 SQLite，则 schema 归 Python 管，Go 只是读——Phase 4/5 的范围需重新定义为"Go 只读访问层 / 编排层"，而非"设计 schema / 实现 FSRS"。

不定下来，Phase 4/5 计划会建立在错误前提上。

## Solution

进 Phase 4 **规划前**写一个 ADR，明确 single source of truth。从现有代码走向看，阻力最小且不重复造轮子的路线是：

> **Python 持有 schema + FSRS 真相；Go 走 `pybridge`（已就绪）或只读直连 SQLite**，Phase 4/5 重新定义为编排 / 只读访问层。

ADR 需回答：schema 变更流程归谁、Go 是否直连 SQLite（CGO-free 驱动选型）、FSRS 参数更新是否必须经 `pybridge.RunReview`。源自 2026-07-25 架构评审 P1-4。

## Resolution

**Status:** Resolved  
**Resolved at:** 2026-08-01  
**Resolution:** ADR created at `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md`  
**Decision:** Python `vocab_manager.py` is the canonical FSRS+SQLite authority; Go extends with auxiliary tables only. Write separation enforced: Python owns `cards`/`review_logs`, Go owns `sessions`/`session_events`. Schema changes require ADR update first. Conflict resolution: Python schema takes precedence on drift.
