# Phase 3: Python CLI Integration - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-07-24
**Phase:** 03-python-cli-integration
**Areas discussed:** Python process model, Python asset location & environment, TTS delivery boundary, FSRS/DB ownership

---

## Python Process Model

### Q1 — Execution model

| Option | Description | Selected |
|--------|-------------|----------|
| spawn-per-call | Each call forks a fresh Python process, released on completion. Transient memory protects the 150MB Go budget; cold-start cost (vocab_manager ~100-200ms; pregenerate heavier) is acceptable since both scripts are low-frequency (~once per session). | ✓ |
| 常驻 sidecar 进程 | One long-lived Python process, Go talks stdin/stdout JSON-RPC. Fast calls (no cold start) but ~40-80MB resident continuously, squeezing the 1C/1G budget. | |
| 你定 | Defer to Claude/research; default leans spawn-per-call. | |

**User's choice:** spawn-per-call (推荐)
**Notes:** Key observation shared before the question — both scripts are low-frequency: `due`/`review` run ~once per session each, and `pregenerate_dictation.py` takes a `--words` JSON *array*, so the whole word list is generated in one batched call. This made transient-memory spawn-per-call clearly preferable to a resident sidecar.

### Q2 — Concurrency limiting

| Option | Description | Selected |
|--------|-------------|----------|
| 全局信号量上限 | Bounded semaphore caps simultaneous Python processes at a small constant N (e.g. 2-4). Overflow queues, handled by Phase 1 worker-pool/backpressure. Reuses D-10 / D-72. | ✓ |
| 串行化（一次一个） | All Python calls through a single slot. Safest (one Python process at a time) but lowest throughput. | |
| 你定 | Defer to research/planning; default leans global semaphore. | |

**User's choice:** 全局信号量上限 (推荐)
**Notes:** Needed so 50-100 concurrent sessions can't fork-bomb the 1C/1G VPS.

---

## Python Asset Location & Environment

### Q1 — Script location

| Option | Description | Selected |
|--------|-------------|----------|
| vendor 进本仓库 | Copy both .py into the repo. Version bound to Go, reproducible deploy, no external-checkout dependency; upstream sync is manual (Make target). | ✓ |
| 运行时依赖外部路径 | Go reads external xiaotian-skills script path via config. Single source of truth with upstream, but fragile/unreproducible on deploy. | |
| 你定 | Defer to research; default leans vendor. | |

**User's choice:** vendor 进本仓库 (推荐)
**Notes:** Scripts currently live in a sibling repo `D:/workspace/ai/xiaotian-skills/shared/dictation/scripts/`, unversioned w.r.t. this repo.

### Q2 — Interpreter & dependencies

| Option | Description | Selected |
|--------|-------------|----------|
| venv | Virtualenv holds requests/edge-tts etc.; Config points at venv python. Reproducible, isolated. | ✓ |
| 系统 python | Depend on host python + global packages. Simple but unreproducible, breakable by system upgrades. | |
| 你定 | Defer to research; default leans venv. | |

**User's choice:** venv (推荐)
**Notes:** Secrets (`VOLCENGINE_TTS_*`) flow via os.Getenv per Phase 1 D-24, not into Config — recorded as D-05.

---

## TTS Delivery Boundary (Phase 3 → 6 seam)

### Q1 — What Go receives from pregenerate

| Option | Description | Selected |
|--------|-------------|----------|
| 文件路径/manifest | pregenerate writes mp3 to cache dir + manifest; Go gets path list. Phase 6 reads files, slices into 640B frames. Reuses pregenerate cache/retry/trim; consistent with "Go builds no cache of its own". | ✓ |
| Go 读字节进内存 | After generation Go reads mp3 bytes into the Phase 1 buffer pool; Phase 6 streams from memory. No disk intermediate, but memory-heavy and diverges from pregenerate's cache philosophy. | |
| 逐词流式生成 | Rework pregenerate into per-word streaming calls. Closest to "full streaming" literally, but pregenerate is batch-designed — costly rework and repeated TTS API calls. | |
| 你定 | Defer to research; default leans files/manifest. | |

**User's choice:** 文件路径/manifest (推荐)
**Notes:** This resolves the stated tension: PROJECT.md says "Full streaming TTS (consistency over caching)" but `pregenerate_dictation.py` is a pre-generate + disk-cache model. Resolution (D-07): "full streaming" is a *delivery-layer* property (Phase 6 streams frames to the client); generation uses pregenerate's pre-generate + cache (Phase 3). Strict seam (D-08): mp3→PCM→640B slicing/streaming is Phase 6; generation timing is Phase 7.

---

## FSRS / DB Ownership (Phase 3 → 4/5 seam)

### Q1 — Bridge is durable architecture or throwaway shim

| Option | Description | Selected |
|--------|-------------|----------|
| 长期复用 Python FSRS | Python FSRS+DB is the permanent engine; Phase 5 is Go orchestration calling due/review via the bridge. Phase 3 bridge is a durable, well-tested interface. Phase 4 schema serves vocab management; FSRS metadata stays in Python. Matches "reuse existing assets". | ✓ |
| Phase 5 用 Go 重写 | Python is an MVP shim; Phase 5 ports FSRS to Go, Phase 4 designs a unified schema, Phase 3 bridge is retired. Phase 3 written lightweight. | |
| 混合 | Phase 3 runs on Python FSRS; Phase 5 decides Go reimplementation based on perf/migration cost. Bridge interface designed backend-replaceable. | |
| 你定 | Defer to research; default leans long-term reuse. | |

**User's choice:** 长期复用 Python FSRS (推荐)
**Notes:** Every REQUIREMENTS/PROJECT.md signal says "reuse vocab_manager.py / pregenerate_dictation.py"; re-implementing a working FSRS in Go on 1C/1G is high-risk/low-value. `vocab_manager.py` already implements the fetch-lock pattern (FSRS-03). Downstream consequence (D-10): Phase 4 must treat vocab_manager's `vocab.db` as the source of truth and not design a conflicting parallel schema.

---

## Claude's Discretion

- Semaphore cap N value and subprocess timeouts.
- Exact vendor directory location; requirements version-pinning strategy.
- New `python.*` operational Config fields (interpreter, scripts dir, data/cache/manifest/db paths).
- `Executor` interface design for testability (constructor DI D-42/D-47, manual fakes D-48).
- Parsing/normalization of vocab_manager output contract (JSON stdout; errors via `print("Error: ...")` + non-zero exit) and error categorization.
- stdout/stderr separation and structured-log fields.

## Deferred Ideas

- Generation timing (eager batch vs. lazy per-word) → Phase 7.
- mp3→PCM→640B slicing + WS streaming → Phase 6.
- vocab_manager secondary commands (add/stats/list/export/rename-unit/delete-words/merge-units) → Phase 9+.
- DB schema unification detail → Phase 4.
- FSRS Go reimplementation → explicitly NOT planned (D-09).
- `easy` rating support — verify at research; mapping decision (if needed) → Phase 10 (FEED).
