# Phase 9: Parent Vocabulary Management - Context

**Gathered:** 2026-09-27
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 9 delivers the **parent-facing vocabulary management interface**: a
responsive web page (`parent.html`) backed by HTTP REST endpoints on the
existing Go service, through which the parent adds / edits / organizes
dictation words (units) and configures FSRS scheduler parameters. All word
mutations flow through the Python bridge to `vocab_manager.py` per the
write-separation ADR; all reads (word lists, unit grouping, counts) are served
by Go reading the `cards` table directly.

Covers: VOC-01 (add words with translation/example — pronunciation
reinterpreted, see D-05), VOC-02 (organize into units/lessons — flat naming,
see D-09), VOC-03 (FSRS parameter configuration — retention + max interval,
see D-13), WEB-01 (parent web interface).

Out of scope: parent review/rating UI (Phase 10), progress statistics beyond
word counts (FEED-04, Phase 10), auth/PIN (rejected — private family VPS),
WebSocket reconnection (Phase 11), photo OCR entry (v2).

</domain>

<decisions>
## Implementation Decisions

### API channel & page entry

- **D-01: HTTP REST + fetch, NOT WebSocket.** Parent CRUD operations are
  request/response — mount JSON endpoints (e.g. `/api/v1/*`) on the existing
  `publicMux` in `cmd/service/main.go`. The WebSocket channel stays dedicated
  to student real-time dictation sessions. No correlation-id/timeout/retry
  machinery needs to be reinvented over WS. — **Reversibility:** reversible —
  additive routes; a later WS migration would not require data changes.
- **D-02: `parent.html` in the SAME static dir, served by the existing `infra.StaticFileServer` at the same port.** No new config, no new mount,
  no SPA merge. — **Reversibility:** reversible.
- **D-03: Responsive design covering phone → tablet → desktop** (CSS media
  queries, no framework, consistent with the Phase 8 vanilla-JS/no-build
  constraint). No single primary device is assumed.
- **D-04: NO parent entry point on the student page.** The parent navigates
  directly to `/parent.html` by URL. No PIN/auth gate — private family VPS;
  hiding the entry from the child is sufficient. A PIN gate was considered
  and rejected for Phase 9.

### Word fields & the pronunciation gap

- **D-05: NO pronunciation column.** VOC-01's "pronunciations" is
  reinterpreted as the TTS read-aloud capability (already delivered in
  Phase 6 via Mimo). Zero schema change, zero Python change. If
  pronunciation overrides (multi-pronunciation chars, proper nouns) become
  real needs, that is a future ADR-gated schema extension. —
  **Reversibility:** reversible — adding a column later follows the ADR
  schema-modification process.
- **D-06: Category is a REQUIRED choice on add** (english | chinese — it
  drives TTS language). List view filters by category. Closes UAT gap C4
  (library currently has only 7 English words). The `category` column and
  CLI `--category` flag already exist.
- **D-07: Add an `edit` subcommand to `vocab_manager.py`** — update
  word/meaning/example/unit by card id WITHOUT touching `fsrs_card`
  (preserves learning progress). The CLI currently has no edit path
  (delete+re-add would reset FSRS state, violating VOC-01's edit intent).
  pybridge gains a matching method following the existing
  RunDue/RunReview pattern. This is the one Python-side schema-adjacent
  change in this phase (no table DDL change — `edit` only UPDATEs existing
  columns).
- **D-08: Only `word` is required** on the add form; meaning/example are
  optional (DB already defaults them to empty string). Keeps entry friction
  low for "copy from textbook" sessions.

### Unit organization & entry workflow

- **D-09: Units stay FLAT strings with naming conventions** (e.g.
  "三年级上-L10"). No hierarchy tables, no two-level tree model. The
  frontend may visually group by prefix; storage and commands stay
  one-level. Compatible with existing schema + rename_unit/merge_units. —
  **Reversibility:** costly to reverse later toward real hierarchy — but
  the decision is to NOT build hierarchy, so the cost is only incurred if
  requirements change.
- **D-10: Dual entry modes — single-word form AND batch paste.** Batch
  paste: one word per line (word + optional meaning separated by tab/space),
  parsed to a preview table, confirmed once, then committed via the existing
  `add --words` JSON batch path. Typical parent flow is "one textbook lesson
  = 10–20 words in one go".
- **D-11: Exposed organize operations — change unit (via edit), rename unit (`rename_unit`), delete words (`delete_words`).** `merge_units` is NOT
  exposed in the UI (low-frequency, dangerous bulk op; achievable via two
  renames if ever needed).
- **D-12: Default browse view = collapsible unit groups + top search (word/meaning) + category filter.** Unit headers show word counts computed
  by Go reading `cards` directly via `CardRepository` — NOT via Python
  subprocess calls (1C1G VPS: every Python invocation costs a process
  spawn). FSRS-state-per-unit stats were considered and deferred (would
  require Python `stats` enhancement per unit).

### FSRS parameter exposure (VOC-03)

- **D-13: Expose exactly TWO knobs: `desired_retention` (0.7–0.95 safe slider) and `maximum_interval` (days).** Learning/relearning steps and
  fuzzing stay internal. Note: FSRS "difficulty" is per-card emergent state
  (in `fsrs_card` JSON), not a global parameter — VOC-03's "difficulty,
  retention interval" maps to these two scheduler knobs. Parameter changes
  affect FUTURE scheduling only (existing card states are not rewritten).
- **D-14: Parameters live in an `fsrs_config.json` file** (data directory;
  absent file = current hard-coded defaults 0.85/180). `vocab_manager.py`
  reads it at startup and constructs `SCHEDULER` from it; the Go endpoint
  writes the file on save; the next subprocess invocation picks it up (no
  restart, no state in the subprocess). No schema change, no new CLI config
  subcommand. Concurrency note for planner: single-writer (parent UI) makes
  races unlikely, but the write should be atomic (temp file + rename).
- **D-15: Config UI = an embedded "学习设置" card on the parent page** — two
  sliders with recommended-range labels, current-vs-default display, a
  restore-defaults button, and plain-language copy (e.g. "higher retention =
  more frequent review"). NOT a separate settings page.

### the agent's Discretion

- Exact REST route names / request-response JSON shapes (planner decides;
  suggest `/api/v1/...` prefix).
- HTTP error response format (structured error codes vs messages).
- Batch paste parsing details (exact separator rules, per-line error
  tolerance/reporting in the preview step).
- `edit` subcommand CLI surface (`--id` + optional fields) and its JSON
  output contract.
- Exact location/nesting of `fsrs_config.json` (data dir vs script dir) and
  its validation behavior on invalid content (fall back to defaults + warn).
- Search implementation (SQL LIKE via CardRepository vs in-memory filter).
- Collapse/expand grouping implementation and all visual design details —
  route through `/gsd-ui-phase 9` before implementation (roadmap UI hint:
  yes).

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Locked architecture decisions
- `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` — write-separation
  contract: Python is sole writer of `cards`/`review_logs`; Go owns
  `sessions`/`session_events`; Go may read `cards` via WAL. All Phase 9
  write paths MUST go through pybridge → vocab_manager.py. Schema changes
  require ADR-update-first process (NOT triggered by this phase: D-05/D-07
  avoid DDL changes).
- `.planning/ROADMAP.md` §Phase 9 — goal, depends-on (Phase 4, 8),
  requirements VOC-01/02/03 + WEB-01, success criteria.
- `.planning/REQUIREMENTS.md` — VOC-01..03, WEB-01 definitions; §Real-time
  Dictation calibration note (2026-08-23) for Mimo TTS context; C4 relates
  to the Chinese-wordlist gap closed by D-06.
- `CONTEXT.md` (repo root) — 快速反馈周期优先 philosophy; 1C1G/150MB
  constraints; iPad Safari target.

### Python side (the canonical vocabulary engine)
- `third_party/python/vocab_manager.py` — canonical schema `init_db`
  (~L99-128: cards/review_logs DDL + fetch_locked_until migration); CLI
  subcommands (~L762-801: add/due/unlock/new/review/stats/list/export/
  reset/rename_unit/delete_words/merge_units) — Phase 9 uses add/list/
  rename_unit/delete_words and ADDS `edit` (D-07); `SCHEDULER` constant
  (~L74-86) to be made config-driven (D-14); `get_db` WAL setup (~L89).

### Go side integration points
- `cmd/service/main.go` (~L491-520) — publicMux where `/api/v1/*` mounts
  (D-01); StaticFileServer mount (D-02); debug-file serving pattern to avoid
  route collisions.
- `pkg/infra/static.go` (L62 `StaticFileServer`) — existing static serving.
- `pkg/pybridge/vocab.go` — RunDue/RunReview argv-construction +
  trailing-JSON parsing pattern; new RunAdd/RunList/RunEdit/RunRenameUnit/
  RunDeleteWords follow this pattern.
- `pkg/pybridge/executor.go` (L54 `Executor` interface) — the bridge seam
  new methods must satisfy.
- `pkg/database/card_repo.go` — read-only CardRepository with explicit
  column list; D-12 list/count queries extend here (never `SELECT *`, never
  writes).
- `pkg/database/schema.go` — Go-owned tables + write-separation contract
  documented in package comment.

### Frontend pattern reference
- `pkg/web/static/index.html` + `pkg/web/static/js/` (app.js,
  state-manager.js, ui-components.js) — Phase 8 vanilla-JS no-build pattern
  the parent page must follow; `parent.html` joins the same static dir.

### Prior phase context (do not re-litigate)
- `.planning/phases/03-python-cli-integration/03-CONTEXT.md` — pybridge
  decisions (D-02 semaphore, D-03 vendor scripts, JSON contract rules).
- `.planning/phases/08.1-multi-word-dictation-flow-inserted/08.1-CONTEXT.md`
  — superseded status note (student-paced model is current); frame protocol
  LOCKED (16-byte header).

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- **vocab_manager.py management CLI** — `add --unit --category --words
  (JSON batch)` / `list --unit --category` / `rename_unit` / `delete_words`
  already exist; Phase 9's backend write surface is mostly wiring these
  through pybridge (plus one new `edit` command, D-07).
- **pybridge.Bridge pattern** — RunDue/RunReview/RunPregenerate demonstrate
  argv construction, semaphore serialization, sentinel errors, and
  trailing-JSON extraction; new vocab commands copy this shape
  (`pkg/pybridge/vocab.go`).
- **infra.StaticFileServer + Server.StaticDir/StaticRoute config** —
  serving `parent.html` requires zero new infrastructure (D-02).
- **CardRepository (Go, read-only)** — explicit column list + schema-drift
  test; powers D-12 list/group/count reads without Python subprocess cost.
- **Phase 8 frontend modules** — state-manager / ui-components patterns to
  mirror in parent page code organization.

### Established Patterns
- **Write separation (ADR)** — Python owns cards writes; Go reads via WAL;
  never cross-write. Phase 9 is the first consumer of the
  management-command path (Phase 5 used due/review only).
- **Vanilla JS, no framework, no build step** — locked by Phase 8 +
  1C1G VPS constraints.
- **Subprocess-per-invocation** — Python processes are stateless; config
  must therefore live in a file read at each startup (D-14), not in
  process memory.
- **publicMux route registration** — HTTP handlers mount on the public mux
  alongside `/ws` and static; debug-file pattern shows how to avoid route
  collisions.

### Integration Points
- **`publicMux` (main.go ~L491)** — mount `/api/v1/*` REST routes (D-01).
- **Static dir** — add `parent.html` (+ shared CSS/JS as needed) next to
  `index.html` (D-02).
- **pybridge Executor** — extend with vocab management methods; reuse
  D-02 global semaphore for subprocess serialization.
- **`fsrs_config.json`** — new file in the Python data directory; written
  by the Go settings endpoint, read by vocab_manager.py at startup (D-14).

</code_context>

<specifics>
## Specific Ideas

- The parent's dominant workflow is **"textbook lesson capture"**: sit with
  the课本, batch-paste one lesson (10–20 words) in one go, confirm a
  preview, done (D-10). Single-word form is the secondary path.
- FSRS settings UI copy should be plain-language for a non-technical parent
  (e.g. "保持率越高,复习越频繁"), with current-vs-default display and a
  restore-defaults button (D-15).
- Parameter changes must NOT rewrite existing card states — they only shape
  future scheduling (D-13).
- The child uses the same iPad; keeping the management page unreachable from
  the student UI (no link, URL-only access) is the accepted protection
  level (D-04).

</specifics>

<deferred>
## Deferred Ideas

- **Pronunciation override column** (multi-pronunciation chars, proper
  nouns) — rejected for Phase 9 (D-05); future ADR-gated schema extension
  if a real need emerges.
- **merge_units in the UI** — Python command exists; exposing it was
  rejected (D-11) as a dangerous low-frequency bulk op.
- **FSRS-state-per-unit stats in the browse view** (due counts per unit) —
  considered and deferred; would need Python `stats` enhancement per unit
  (subprocess cost) — natural fit for Phase 10 (FEED-04 progress metrics).
- **PIN/auth gate for the parent page** — rejected (D-04); revisit only if
  the deployment context changes beyond private family use.
- **Real unit→lesson hierarchy model** — rejected (D-09); naming convention
  carries the semantics.

</deferred>

---

*Phase: 9-Parent Vocabulary Management*
*Context gathered: 2026-09-27*
