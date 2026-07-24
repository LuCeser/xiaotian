# Phase 3: Python CLI Integration - Context

**Gathered:** 2026-07-24
**Status:** Ready for planning

<domain>
## Phase Boundary

This phase delivers a **Go ↔ Python subprocess bridge**: the Go service executes
`vocab_manager.py` (the `due` command for word selection, the `review` command
for FSRS feedback updates) and `pregenerate_dictation.py` (batch TTS audio
generation), parses their JSON stdout, and captures subprocess errors into the
Zap logging system. It is the durable, reusable access layer through which all
later phases reach the existing Python business logic (FSRS engine, vocabulary
store, TTS generation).

**In scope (PY-01, PY-02, PY-03, PY-04):**
- PY-01: integrate with the existing `vocab_manager.py` CLI via subprocess.
- PY-02: execute the `due` subcommand and return word-selection results.
- PY-03: execute the `review` subcommand with feedback parameters.
- PY-04: integrate with `pregenerate_dictation.py` for TTS audio generation.
- Subprocess error capture surfaced to the logging system (success criterion 4).

**Out of scope (explicitly deferred — do NOT pull in):**
- WebSocket session orchestration / when generation is triggered → Phase 7.
- Converting mp3 → PCM → 640B frame slicing and streaming delivery → Phase 6.
- Canonical DB schema design → Phase 4.
- FSRS algorithm logic → stays in Python (see D-09); Phase 5 only orchestrates.
- `vocab_manager.py` secondary commands (add/stats/list/export/rename-unit/
  delete-words/merge-units) → Phase 9 and later phases that need them.

</domain>

<decisions>
## Implementation Decisions

### Python Process Model
- **D-01: spawn-per-call.** Each invocation forks a fresh Python process and
  releases it when done. Python memory is transient (no resident sidecar),
  which protects the 150MB Go budget on the 1C/1G VPS. The cold-start cost
  (vocab_manager ~100-200ms; pregenerate heavier due to `edge-tts`/`requests`
  imports) is acceptable because both scripts are low-frequency — roughly once
  per dictation session (pregenerate takes a `--words` JSON *array*, so the
  whole word list is generated in one batched call).
- **D-02: global bounded semaphore caps concurrent Python processes.** A small
  semaphore (constant N, decided in research/planning) limits simultaneously
  running Python subprocesses so that 50-100 concurrent sessions cannot
  fork-bomb the 1C/1G box. Overflow queues and is handled by the Phase 1
  worker-pool / backpressure signals (reuses D-10 worker pool, D-72
  backpressure). Serialize-vs-cap was considered; capped concurrency chosen
  for throughput.

### Python Asset Location & Environment
- **D-03: vendor both scripts into this repo.** `vocab_manager.py` and
  `pregenerate_dictation.py` are copied into the repository (exact directory is
  Claude's discretion — e.g. `third_party/python/` or `internal/pybridge/scripts/`).
  This binds script version to Go code, makes deployment reproducible, and removes
  the runtime dependency on an external `xiaotian-skills` checkout. Upstream
  changes are synced manually (a Make target is the likely mechanism).
- **D-04: venv for Python interpreter and dependencies.** A virtualenv holds
  `requests`, `edge-tts`, etc.; the typed Config points the bridge at the venv's
  Python. Reproducible, isolated, does not pollute system Python. Dependencies
  are pinned (version-locking strategy is Claude's discretion).
- **D-05: secrets pass through via `os.Getenv`, never via Config.** Consistent
  with Phase 1 D-24: the bridge reads `VOLCENGINE_TTS_API_KEY` /
  `VOLCENGINE_TTS_APP_ID` + `VOLCENGINE_TTS_ACCESS_KEY` from the process
  environment and forwards them to the spawned subprocess. There is **no**
  `APIKey`/`Secret`/`Password` field added to the Config struct — only
  operational fields (paths, interpreter) belong there.

### TTS Delivery Boundary (Phase 3 → 6 seam)
- **D-06: pregenerate returns file paths / manifest.** `pregenerate_dictation.py`
  writes mp3 files to its cache directory and emits a manifest; after the Go call
  returns, Go holds the path/manifest list (not audio bytes). Phase 6 later reads
  those files and slices them into 640B frames for streaming. This reuses
  pregenerate's existing cache/retry/trim logic and matches its pre-generate
  nature.
- **D-07: "full streaming TTS" is redefined as a delivery-layer property.** The
  PROJECT.md constraint "Full streaming TTS (consistency over caching)" means
  Phase 6 streams audio frames to the client; it does **not** require live
  per-frame generation. Generation uses pregenerate's pre-generate + cache model
  (Phase 3), and the Go service does **not** build its own caching layer (the
  cache lives on the Python side). This resolves the stated constraint vs. the
  chosen asset.
- **D-08: phase seam is strict.** mp3 → PCM → 640B frame slicing and WebSocket
  streaming are Phase 6 (codec layer) work; Phase 3 produces mp3 files only.
  Deciding *when* to trigger generation (eager whole-session batch vs. lazy
  per-word) is Phase 7 session orchestration.

### FSRS / DB Ownership (Phase 3 → 4/5 seam)
- **D-09: long-term reuse of the Python FSRS + SQLite.** `vocab_manager.py`'s
  built-in FSRS engine and its SQLite DB (`DICTATION_DB_PATH` / `vocab.db`) are
  the **permanent** engine. Phase 5 ("FSRS Core Implementation") is a Go
  service-layer orchestration that calls `due`/`review` through this bridge —
  it does **not** re-implement FSRS in Go. Therefore the Phase 3 bridge is a
  **durable interface**: it must be well-designed and well-tested, not throwaway.
- **D-10: Phase 4 must reconcile with vocab_manager's existing schema.** Because
  the canonical FSRS/vocabulary store is `vocab_manager`'s `vocab.db`, Phase 4
  ("Database Schema Design") must treat that schema as the source of truth for
  FSRS and vocabulary data and must **not** design a conflicting parallel
  schema without explicit reconciliation. Phase 4's fresh schema work (if any)
  is confined to Go-owned auxiliary concerns (e.g. session bookkeeping).

### Bridge Scope
- **D-11: general-purpose wrapper, three delivered paths.** The bridge is built
  as a general wrapper capable of invoking any `vocab_manager.py` subcommand and
  `pregenerate_dictation.py`, but Phase 3 **only implements and tests** the
  `due`, `review`, and `pregenerate` paths (PY-02/03/04). Secondary commands are
  left for the phases that need them.

### Claude's Discretion
- Concrete value of the concurrency semaphore cap N and subprocess timeouts.
- Exact vendor directory location and the requirements-pinning strategy.
- New `python.*` operational fields added to the typed Config (interpreter,
  scripts dir, data/cache/manifest/db paths).
- `Executor` interface design for testability — constructor DI per Phase 1
  D-42/D-47, with manual fakes per D-48 (no heavy mocking frameworks).
- Parsing/normalization of the vocab_manager output contract (JSON on stdout;
  errors via `print("Error: ...")` + non-zero exit code) and error categorization.
- stdout/stderr separation and the structured-log fields the bridge emits.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Roadmap & Phase Definition
- `.planning/ROADMAP.md` — Phase 3 goal, success criteria, requirement mapping (PY-01..PY-04).
- `.planning/ROADMAP.md#phase-3-python-cli-integration` — Phase 3 definition (4 success criteria, depends on Phase 2).

### Project Requirements & Context
- `.planning/REQUIREMENTS.md` — v1 requirements; PY-01..PY-04 (Phase 3). Note FEED-02 rates performance as good/hard/again/easy (4 ratings) — see research note below.
- `.planning/PROJECT.md` — project context, constraints (1C/1G VPS, 150MB Go, 50MB audio pool, 50-100 concurrent sessions), key decisions (Go WebSocket + Python microservices; "Full streaming TTS vs caching" — see D-07 for the resolution).
- `.planning/PROJECT.md#constraints` — infrastructure constraints the bridge must respect.

### Prior Phase Context
- `.planning/phases/01-resource-constrained-infrastructure/01-CONTEXT.md` — Phase 1 decisions the bridge reuses: typed Config + Viper layered loader (D-52/D-53), worker pool (D-10), buffer pool (D-14), backpressure (D-72), secrets-via-os.Getenv-not-in-Config (D-24), constructor DI (D-42/D-47), manual fakes (D-48), table-driven + integration tests (D-28/D-29).
- `.planning/phases/02-websocket-communication-layer/02-CONTEXT.md` — Phase 2 decisions; `pkg/ws` codec produces the 640B frames (D-09) that Phase 6 will fill with Phase 3's generated audio.

### Python Assets (source — to be vendored per D-03)
- `D:/workspace/ai/xiaotian-skills/shared/dictation/scripts/vocab_manager.py` — the FSRS/vocabulary CLI. Researcher/planner MUST read its `main()` / argparse (subcommands: `add`, `due`, `new`, `review`, `stats`, `list`, `export`, `reset`, `rename-unit`, `delete-words`, `merge-units`) and its JSON stdout contract to map the bridge. Env: `DICTATION_DB_PATH`; constants `FETCH_LOCK_HOURS`.
- `D:/workspace/ai/xiaotian-skills/shared/dictation/scripts/pregenerate_dictation.py` — the TTS batch generator. Researcher/planner MUST read its argparse (`--words` JSON array, `--language`, `--words-per-segment`, `--prefix`, `--voice`, provider args) and its file/manifest output model. Env: `DICTATION_DATA_DIR`, `DICTATION_CACHE_DIR`, `DICTATION_MANIFEST_DIR`, `DICTATION_PYTHON`, `DICTATION_ENV_FILE`; TTS via Volcengine (`VOLCENGINE_TTS_*`) with edge-tts fallback.

### ⚠ Research note (verify, do not assume)
- `vocab_manager.py` `review --results` documents ratings as `good|hard|again` (3 values), but REQUIREMENTS **FEED-02** expects four (`good/hard/again/easy`). The researcher MUST confirm whether `easy` is accepted by `review`. If not, a rating-mapping decision is needed — but that lands at Phase 10 (FEED), not Phase 3; for Phase 3 the bridge must simply forward whatever ratings it is given without altering semantics.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `pkg/infra/config.go` — typed `Config` + Viper layered loader (defaults < file < env, prefix `HTA_`). The bridge extends this with a `python.*` operational block (interpreter + script/data paths); secrets stay out (D-05).
- `pkg/infra/logging.go` — Zap logger with `infra.F()` / `infra.ErrField()` helpers. Every bridge log line (component tag e.g. `pybridge`) uses these; subprocess errors flow here (success criterion 4).
- `pkg/infra/worker.go` + `pkg/infra/pool.go` — worker pool and buffer pool from Phase 1. The subprocess concurrency cap (D-02) plugs into the worker-pool/backpressure machinery rather than inventing a new limiter.
- Phase 1 DI + fake conventions (D-42/D-47/D-48) — the bridge exposes an `Executor` interface so tests inject a manual fake instead of spawning real Python.

### Established Patterns
- Module `github.com/xiaotian/home-tutor-agent` (go 1.26); layered `cmd/pkg/internal` layout; new bridge package lives under `pkg/` (e.g. `pkg/pybridge`) per the cmd→pkg dependency rule.
- Table-driven unit tests + integration tests; golangci-lint (govet/staticcheck/gosec) and race-enabled CI.
- Config secret rule: secrets via `os.Getenv` in the consuming package, never in the Config struct.

### Integration Points
- `cmd/service/main.go` — the bridge is constructed and wired here (constructor DI), alongside the existing infra/ws components.
- Future `pkg/ws` streaming (Phase 6) consumes the audio files/manifests Phase 3 produces.
- Future FSRS orchestration (Phase 5) and session flow (Phase 7) call `due`/`review`/`pregenerate` through this bridge.

### Python CLI Contracts (discovered during scout)
- `vocab_manager.py`: `--db <path>` then a subcommand. All commands print **JSON to stdout** (`ensure_ascii=False`): `due` → array of due cards; `review` → `{"updated":[...]}`. Errors are `print("Error: ...")` + non-zero exit. Notable `due` flags: `--unit/--units`, `--category`, `--limit`, `--exclude`, `--peek` (no side-effects), `--fetch-lock-hours`. `review` flags: `--results` (JSON) or `--file`, `--category`.
- `pregenerate_dictation.py`: `--words '[...]'` (JSON array, required), `--language english|chinese` (required), `--words-per-segment`, `--prefix`, `--voice`, plus provider/output-format args. Writes mp3 to `DICTATION_CACHE_DIR` and a manifest to `DICTATION_MANIFEST_DIR`. TTS via Volcengine HTTP (`VOLCENGINE_TTS_*`) with edge-tts fallback. Has a `generate` subcommand shim and a `--run-legacy-pregenerate` flag.

</code_context>

<specifics>
## Specific Ideas

No "I want it like X" references beyond the decisions above. The one redefinition worth restating: the project's "full streaming TTS" constraint is interpreted (D-07) as a **delivery-layer** property (Phase 6 streams frames to the client), with generation handled by pregenerate's pre-generate + cache model — not as live per-frame generation. This interpretation is the user-confirmed resolution of the constraint-vs-asset tension.

</specifics>

<deferred>
## Deferred Ideas

- **Generation timing** (eager whole-session batch vs. lazy per-word) → Phase 7 (session orchestration).
- **mp3 → PCM → 640B frame slicing + WebSocket streaming** → Phase 6 (codec layer).
- **vocab_manager secondary commands** (add/stats/list/export/rename-unit/delete-words/merge-units) → Phase 9 (parent vocabulary management) and later phases as needed.
- **DB schema unification detail** → Phase 4, constrained by D-10 (reconcile with `vocab_manager`'s existing schema).
- **FSRS reimplementation in Go** → explicitly NOT planned (D-09). If ever reconsidered, it is a future-milestone decision, not a phase task.
- **`easy` rating support** → verify at research time; mapping decision (if needed) belongs to Phase 10 (FEED), not Phase 3.

</deferred>

---

*Phase: 03-Python CLI Integration*
*Context gathered: 2026-07-24*
