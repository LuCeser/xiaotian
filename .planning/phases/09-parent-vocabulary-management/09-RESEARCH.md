# Phase 9: Parent Vocabulary Management - Research

**Researched:** 2026-09-27
**Domain:** Parent-facing CRUD web UI on existing Go service; REST endpoints bridging to Python vocab CLI; FSRS scheduler config file; vanilla-JS responsive frontend
**Confidence:** HIGH (codebase-grounded; external claims minimal and cited)

## Summary

Phase 9 is a **wiring phase, not a build-from-scratch phase**. The Python vocabulary engine (`vocab_manager.py`) already ships `add` / `list` / `rename-unit` / `delete-words` subcommands; the Go side already has the `pybridge.Bridge` argv/trailing-JSON pattern, a read-only `CardRepository`, a `publicMux` with static serving, and a Phase 8 vanilla-JS frontend pattern to copy. The work is: (1) one new Python subcommand `edit` (D-07), (2) config-driven `SCHEDULER` from `fsrs_config.json` (D-14) — a surgical change, `SCHEDULER` is referenced at exactly one call site (`cmd_review`), (3) new `RunAdd/RunList/RunEdit/RunRenameUnit/RunDeleteWords` bridge methods copying the `dueArgs` shape, (4) `/api/v1/*` REST handlers on `publicMux` using Go 1.22+ method patterns (project is Go 1.26), and (5) `parent.html` in the existing static dir.

The dominant risk area is **contract friction between components that already exist**: hyphenated CLI subcommand names, `delete-words` dry-run-by-default behavior (prints non-JSON preview, exit 0 — breaks the bridge's trailing-JSON parser), rename-unit error diagnostics on stdout, `add` silently skipping duplicates, and `ListByUnit` lacking a category filter. All are documented below with verbatim evidence.

Second risk: **fsrs_config.json path resolution must be identical on both sides**. The env plumbing already exists (`childEnv` forwards `DICTATION_DATA_DIR`), but manual CLI runs without the Go service fall back to a script-relative default — the planner must pick DataDir + env-forwarding as the single resolution rule.

**Primary recommendation:** Mount method-pattern REST routes (`"GET /api/v1/words"` etc.) on `publicMux`; route all mutations through new pybridge methods that copy `dueArgs`/`RunDue` exactly; serve reads from `CardRepository` extended with category+search+count queries; write `fsrs_config.json` atomically into `cfg.Python.DataDir`; build `parent.html` as vanilla JS mirroring the Phase 8 module pattern.

<user_constraints>
## User Constraints (from CONTEXT.md)

> Repo-root CONTEXT.md philosophy applies with equal force: 快速反馈周期优先 — local end-to-end verification beats component perfection; 1C1G VPS / 150MB Go budget; iPad Safari target.

### Locked Decisions

- **D-01: HTTP REST + fetch, NOT WebSocket.** Parent CRUD operations are request/response — mount JSON endpoints (e.g. `/api/v1/*`) on the existing `publicMux` in `cmd/service/main.go`. The WebSocket channel stays dedicated to student real-time dictation sessions. No correlation-id/timeout/retry machinery over WS. — Reversible.
- **D-02: `parent.html` in the SAME static dir, served by the existing `infra.StaticFileServer` at the same port.** No new config, no new mount, no SPA merge. — Reversible.
- **D-03: Responsive design covering phone → tablet → desktop** (CSS media queries, no framework, consistent with Phase 8 vanilla-JS/no-build constraint). No single primary device assumed.
- **D-04: NO parent entry point on the student page.** Parent navigates directly to `/parent.html` by URL. No PIN/auth gate — private family VPS. PIN gate considered and rejected for Phase 9.
- **D-05: NO pronunciation column.** VOC-01's "pronunciations" reinterpreted as TTS read-aloud (delivered Phase 6 via Mimo). Zero schema change. Pronunciation overrides = future ADR-gated schema extension. — Reversible.
- **D-06: Category is a REQUIRED choice on add** (english | chinese — drives TTS language). List view filters by category. Closes UAT gap C4. `category` column and CLI `--category` flag already exist.
- **D-07: Add an `edit` subcommand to `vocab_manager.py`** — update word/meaning/example/unit by card id WITHOUT touching `fsrs_card` (preserves learning progress). pybridge gains a matching method following the RunDue/RunReview pattern. No table DDL change — `edit` only UPDATEs existing columns.
- **D-08: Only `word` is required** on the add form; meaning/example optional (DB defaults them to empty string).
- **D-09: Units stay FLAT strings with naming conventions** (e.g. "三年级上-L10"). No hierarchy tables. Frontend may visually group by prefix; storage and commands stay one-level.
- **D-10: Dual entry modes — single-word form AND batch paste.** Batch paste: one word per line (word + optional meaning separated by tab/space), parsed to a preview table, confirmed once, committed via the existing `add --words` JSON batch path. Typical flow: one textbook lesson = 10–20 words.
- **D-11: Exposed organize operations: change unit (via edit), rename unit (`rename_unit`), delete words (`delete_words`).** `merge_units` NOT exposed in the UI.
- **D-12: Default browse view = collapsible unit groups + top search (word/meaning) + category filter.** Unit headers show word counts computed by Go reading `cards` directly via `CardRepository` — NOT via Python subprocess (1C1G: every Python invocation costs a process spawn). FSRS-state-per-unit stats deferred.
- **D-13: Expose exactly TWO knobs: `desired_retention` (0.7–0.95 safe slider) and `maximum_interval` (days).** Learning/relearning steps and fuzzing stay internal. FSRS "difficulty" is per-card emergent state, not a global parameter. Parameter changes affect FUTURE scheduling only.
- **D-14: Parameters live in an `fsrs_config.json` file** (data directory; absent file = current hard-coded defaults 0.85/180). `vocab_manager.py` reads it at startup and constructs `SCHEDULER` from it; Go endpoint writes the file on save; next subprocess invocation picks it up. Write must be atomic (temp file + rename).
- **D-15: Config UI = an embedded "学习设置" card on the parent page** — two sliders with recommended-range labels, current-vs-default display, restore-defaults button, plain-language copy (e.g. "higher retention = more frequent review"). NOT a separate settings page.

### the agent's Discretion

- Exact REST route names / request-response JSON shapes (planner decides; suggest `/api/v1/...` prefix).
- HTTP error response format (structured error codes vs messages).
- Batch paste parsing details (exact separator rules, per-line error tolerance/reporting in the preview step).
- `edit` subcommand CLI surface (`--id` + optional fields) and its JSON output contract.
- Exact location/nesting of `fsrs_config.json` (data dir vs script dir) and its validation behavior on invalid content (fall back to defaults + warn).
- Search implementation (SQL LIKE via CardRepository vs in-memory filter).
- Collapse/expand grouping implementation and all visual design details — route through `/gsd-ui-phase 9` before implementation (roadmap UI hint: yes).

### Deferred Ideas (OUT OF SCOPE)

- Pronunciation override column (multi-pronunciation chars, proper nouns) — rejected (D-05); future ADR-gated schema extension.
- merge_units in the UI — Python command exists; exposing rejected (D-11).
- FSRS-state-per-unit stats in browse view — deferred to Phase 10 (FEED-04).
- PIN/auth gate for parent page — rejected (D-04).
- Real unit→lesson hierarchy model — rejected (D-09).
- Parent review/rating UI (Phase 10), progress statistics beyond word counts (Phase 10), WebSocket reconnection (Phase 11), photo OCR entry (v2).
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| VOC-01 | Parent can add vocabulary words with Chinese translations, pronunciations, and example sentences | `add` subcommand exists with `--words` JSON batch path (list-mode carries example); pronunciation reinterpreted as Phase 6 TTS (D-05); `edit` subcommand added for VOC-01's edit intent (D-07); duplicate-skip contract documented (Pitfall 4) |
| VOC-02 | Parent can organize words into units and lessons | Flat units via `unit` column (D-09); `rename-unit` / `delete-words` subcommands exist (hyphenated argv verified); change-unit via new `edit`; browse grouping + counts via Go `CardRepository` extension (D-12) |
| VOC-03 | Parent can configure FSRS parameters (difficulty, retention interval) | Maps to `desired_retention` + `maximum_interval` scheduler kwargs (D-13) — both verified in installed fsrs-6.3.1 `Scheduler.__init__`; `fsrs_config.json` file channel (D-14) with env plumbing already in `childEnv`; single `SCHEDULER` call-site makes the change surgical |
| WEB-01 | Parent interface for vocabulary management (add/edit/organize) | `parent.html` joins existing static dir served by `infra.StaticFileServer` (D-02) — zero new infra; `/api/v1/*` on `publicMux` via Go 1.22+ method patterns; Phase 8 vanilla-JS module pattern to mirror |
</phase_requirements>

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Parent page UI (forms, batch paste preview, unit grouping, sliders) | Browser / Client | — | Vanilla JS, no build step (Phase 8 constraint, 1C1G VPS) |
| Static serving of `parent.html` | CDN / Static (existing `infra.StaticFileServer`) | — | Already mounted at `staticRoute`; any `.html` gets no-cache headers automatically |
| REST endpoints `/api/v1/*` | API / Backend (Go, `publicMux`) | — | Request/response CRUD; D-01; method-pattern ServeMux on Go 1.26 |
| Word list / search / unit counts (reads) | Database tier (Go `CardRepository`, read-only WAL) | — | ADR write-separation: Go may read `cards`; avoids Python spawn cost (D-12) |
| Word mutations (add/edit/rename/delete) | Python tier via `pybridge` subprocess | API / Backend (Go handlers validate + dispatch) | ADR: Python is sole writer of `cards`/`review_logs`; Go NEVER writes these tables |
| FSRS config persistence | Filesystem (`fsrs_config.json` in DataDir) | API / Backend (Go writes atomically), Python tier (reads at startup) | D-14; subprocess-per-invocation means config must live in a file, not memory |
| FSRS scheduling math | Python tier (`fsrs` 6.3.1 package) | — | ADR: Go never reimplements FSRS; `fsrs_card` JSON opaque to Go |

## Standard Stack

### Core

No new packages. This phase is deliberately zero-dependency (vanilla JS locked by Phase 8; Go stdlib routing; Python deps already vendored).

| Component | Version | Purpose | Why Standard |
|-----------|---------|---------|--------------|
| Go stdlib `net/http` ServeMux | Go 1.26 (`go.mod`) | REST routing with method patterns | Go 1.22+ enhanced patterns eliminate any router library need [CITED: go.dev/blog/routing-enhancements] |
| `encoding/json` | stdlib | Request/response codecs | Existing `HealthHandler` pattern uses `json.NewEncoder` [VERIFIED: pkg/infra/health.go:61-63] |
| `pybridge.Bridge` | in-repo | All Python mutations | Executor seam; new methods copy `dueArgs`/`RunDue` shape [VERIFIED: pkg/pybridge/vocab.go:65-89,165-206] |
| `database.CardRepository` | in-repo | All reads of `cards` | Read-only, explicit columns, never `SELECT *` [VERIFIED: pkg/database/card_repo.go:48-56,86] |
| `infra.StaticFileServer` | in-repo | Serving `parent.html` | MIME-fix + HTML no-cache + traversal containment already handled [VERIFIED: pkg/infra/static.go:62-87] |
| Python `fsrs` package | 6.3.1 (venv-pinned) | FSRS `Scheduler` | `desired_retention`/`maximum_interval` kwargs verified in installed source [VERIFIED: third_party/python/.venv/Lib/site-packages/fsrs/scheduler.py:162-183] |
| Vanilla JS (ES5-style IIFE modules) | — | Parent page | Phase 8 pattern: globals + `window.X` availability checks + `defer` script order [VERIFIED: pkg/web/static/js/app.js:33-66] |

### Supporting

| Component | Purpose | When to Use |
|-----------|---------|-------------|
| Playwright (`@playwright/test` ^1.63) | E2E for parent UI flows | `make e2e`; webServer already boots the real service via `run.ps1` [VERIFIED: Makefile e2e target, package.json] |
| Plain `node` test runner | JS unit tests | `npm test` runs `node tests/test-*.js` — batch-paste parser and UI modules test here [VERIFIED: package.json scripts.test] |
| `make test-short` / `make test` | Go unit/integration | `go test -v -short ./...` / `go test -race -v ./...` [VERIFIED: Makefile] |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| stdlib ServeMux | chi / gin / gorilla-mux | Rejected: stdlib method patterns sufficient for ~7 routes; zero-dep policy; no middleware needs beyond JSON encode |
| SQL LIKE search | FTS5 / in-memory filter | LIKE + escape is enough at elementary-school word-library scale (hundreds of words); FTS5 adds schema surface Python doesn't own |
| Separate settings page | embedded card (D-15) | Locked by D-15 — embedded "学习设置" card |

**Installation:**
```bash
# No installs. Existing toolchain only:
make test-short   # Go unit
npm test          # JS unit
make e2e          # Playwright (needs one-time: npx playwright install chromium)
```

**Version verification:** `go.mod` reads `go 1.26` [VERIFIED: go.mod:3]. Venv contains `fsrs-6.3.1.dist-info` [VERIFIED: third_party/python/.venv/Lib/site-packages listing]. `requirements.txt` pins `fsrs>=0.0.1` with pip-freeze as lock authority [VERIFIED: third_party/python/requirements.txt].

## Package Legitimacy Audit

**This phase installs zero new external packages.** All capabilities are covered by the Go standard library, in-repo packages, the vendored Python venv, and vanilla browser JS. Existing devDependency `@playwright/test` (^1.63.0) and dependency `ws` (^8.21.3) were verified present in `package.json` [VERIFIED: package.json] — no new installs required, so no SLOP/SUS vectors exist for this phase.

## Architecture Patterns

### System Architecture Diagram

```
 Parent's phone/tablet/desktop browser (responsive, D-03)
        │  URL /parent.html (no link from student page, D-04)
        ▼
┌─ Go service :8080 ─────────────────────────────────────────────┐
│ publicMux (cmd/service/main.go:495)                            │
│  ├─ /ws ................... WebSocket (student sessions)       │
│  ├─ /  .................... StaticFileServer(staticDir)        │
│  │      └─ /parent.html .... served like index.html (D-02)     │
│  │      └─ /js/* /styles/* ... shared assets                   │
│  └─ /api/v1/* (NEW, method patterns, D-01)                     │
│       ├─ GET  /api/v1/words?category&unit&q ──┐ reads          │
│       ├─ GET  /api/v1/units?category  ────────┤ (no Python)    │
│       │                                       ▼                │
│       │                    CardRepository (read-only, WAL)     │
│       │                    ← SQLite cards table (Python-owned) │
│       ├─ POST /api/v1/words (single + batch paste, D-10)       │
│       ├─ PUT  /api/v1/words/{id} (edit, D-07)                  │
│       ├─ POST /api/v1/units/rename (D-11)                      │
│       ├─ DELETE /api/v1/words (D-11)      │ writes             │
│       │                                   ▼                    │
│       │                    pybridge.Bridge (semaphore D-02)    │
│       │                    argv: [vocabScript, "--db", path,   │
│       │                      <subcommand>, ...flags]           │
│       │                                   ▼                    │
│       │            subprocess: vocab_manager.py                │
│       │            add | edit(NEW) | rename-unit | delete-words│
│       │            (SOLE writer of cards/review_logs — ADR)    │
│       ├─ GET  /api/v1/settings/fsrs  ── read fsrs_config.json  │
│       └─ PUT  /api/v1/settings/fsrs  ── atomic write           │
│                        (temp file + rename, D-14)              │
│                                  │                             │
│                    DataDir/fsrs_config.json (NEW file)         │
│                      ▲ read at startup by                      │
│                      └─ every vocab_manager.py invocation      │
│                         (env DICTATION_DATA_DIR forwarded by   │
│                          Bridge.childEnv — already in place)   │
│                         → constructs SCHEDULER                 │
└────────────────────────────────────────────────────────────────┘
```

Trace the primary use case: parent batch-pastes a lesson → JS parses lines to preview table (D-10) → `POST /api/v1/words` → handler validates (category required, D-06) → `RunAdd` → `vocab_manager.py add --unit --category --words '[...]'` → `{"added":N,"skipped":N}` → UI surfaces skipped → browse view re-fetched via `GET /api/v1/words` (Go reads SQLite directly — no subprocess).

### Recommended Project Structure

```
third_party/python/vocab_manager.py     # + cmd_edit, edit subparser, config-driven SCHEDULER
pkg/pybridge/vocab_mgmt.go              # NEW: addArgs/editArgs/renameUnitArgs/deleteWordsArgs
                                        #      + RunAdd/RunEdit/RunRenameUnit/RunDeleteWords
pkg/pybridge/executor.go                # Executor interface += 4 methods (compile-time check)
pkg/database/card_repo.go               # += List(category, unit, q), CountByUnit(category), unit list
pkg/web/api/                            # NEW package: parent REST handlers
│   ├── handlers.go                     #    route table + handler funcs (json encode pattern)
│   ├── validate.go                     #    category enum, retention bounds, body size
│   └── fsrs_config.go                  #    GET/PUT fsrs_config.json + atomic write
pkg/web/static/parent.html              # NEW page (same dir as index.html, D-02)
pkg/web/static/js/parent-app.js         # entry; parent-modes.js (add/batch), parent-browse.js
pkg/web/static/js/parent-batch-parser.js # pure function — node-testable
pkg/web/static/styles/parent.css        # or extend main.css; media queries phone→desktop (D-03)
cmd/service/main.go                     # publicMux += "/api/v1/" routes; wire pyBridge+cardRepo
tests/e2e/parent-management.spec.js     # Playwright: add→group→edit→rename→delete→settings
tests/test-parent-batch-parser.js       # node unit test (npm test chain)
```

### Pattern 1: pybridge command method (copy `dueArgs` + `RunDue` exactly)

**What:** Every new vocab command = a pure `xxxArgs(cfg, opts) []string` argv builder + a `RunXxx` method classifying into the four sentinels.
**When to use:** All five mutation paths (add/edit/rename/delete; list is NOT needed — reads go through `CardRepository` per D-12).
**Contract (verbatim):** argv shape is `[vocabScript, "--db", dbPath, <subcommand>, ...]` with `--db` BEFORE the subcommand — [VERIFIED: pkg/pybridge/vocab.go:66 `args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "due"}`] — because argparse declares `--db` on the top-level parser [VERIFIED: third_party/python/vocab_manager.py:762 `parser.add_argument("--db", default=DB_PATH, help="Database path")`].

Subcommand tokens are **hyphenated**: [VERIFIED: third_party/python/vocab_manager.py:817-828 — verbatim:
```
p_rename = subparsers.add_parser("rename-unit", help="Rename a unit")
...
p_delete = subparsers.add_parser("delete-words", help="Delete words by unit or word list (dry-run by default)")
```
CONTEXT.md writes `rename_unit`/`delete_words` (the Python function names); **argv must use `rename-unit`/`delete-words`**.

### Pattern 2: REST handler (Go 1.22+ method patterns)

**What:** Method-scoped routes with wildcard PathValue; JSON responses via `json.NewEncoder` (mirrors HealthHandler).
**When to use:** All `/api/v1/*` mounts on `publicMux`.
[VERIFIED: cmd/service/main.go:495-496 — verbatim:
```
publicMux := http.NewServeMux()
publicMux.HandleFunc("/ws", wsServer.HandleWebSocket)
```
Mount point for new routes. Precedence: `/api/v1/...` patterns are more specific than the `/` static route — no collision (verified ServeMux precedence rule).]

### Pattern 3: fsrs_config.json two-sided contract

**What:** Go writes `DataDir/fsrs_config.json` atomically; Python reads it at every startup.
**Verified anchor points:**
- Defaults to preserve — [VERIFIED: third_party/python/vocab_manager.py:78-84 — verbatim:
```
SCHEDULER = Scheduler(
    desired_retention=0.85,
    learning_steps=(timedelta(minutes=1), timedelta(minutes=10)),
    relearning_steps=(timedelta(minutes=5),),
    maximum_interval=180,
    enable_fuzzing=False,
)
```
- `SCHEDULER` used at exactly ONE site — [VERIFIED: third_party/python/vocab_manager.py:466 `fsrs_card, review_log = SCHEDULER.review_card(fsrs_card, rating, now)`] — the config-driven refactor is surgical.
- Library kwargs exist and are NOT range-validated (our side must validate) — [VERIFIED: fsrs/scheduler.py:162-183 — verbatim: `def __init__(... desired_retention: float = 0.9, ... maximum_interval: int = 36500, ...)` followed by `self._validate_parameters(parameters=parameters)` which validates only the 21 weights].
- Env plumbing already forwards the dir — [VERIFIED: pkg/pybridge/bridge.go:209 `env = append(env, "DICTATION_DATA_DIR="+b.cfg.DataDir)`] and Python side [VERIFIED: third_party/python/vocab_manager.py:68 `DEFAULT_DATA_DIR = os.environ.get("DICTATION_DATA_DIR", os.path.join(SKILL_DIR, "data"))`].
- Config keys: use exactly `desired_retention` and `maximum_interval` (values present in the SCHEDULER quote above; D-13 names the same two knobs).

### Pattern 4: Frontend module pattern (Phase 8 copy)

**What:** IIFE modules exposing `window.X`, dependency availability checks, `defer` script order, Chinese UI copy, 16px+ inputs / `touch-action: manipulation` for iOS.
[VERIFIED: pkg/web/static/js/app.js:33-48 — verbatim: `(function appEntry() { 'use strict'; if (!window.WebSocketClient) { console.error('[app] WebSocketClient unavailable — client script did not load.');`]
**Batch parser as a separate pure-function module** (`parent-batch-parser.js`) so `node tests/test-parent-batch-parser.js` covers it without a DOM.

### Anti-Patterns to Avoid

- **Serving reads via Python `list` subprocess** — every browse/search keystroke would spawn a process on a 1C1G VPS; D-12 mandates Go-side reads.
- **Go writing `cards` directly** because "it's just an UPDATE" — violates the ADR write-separation that keeps FSRS state coherent.
- **Building unit hierarchy tables** — D-09 locked flat strings; visual grouping by prefix only.
- **Rendering vocabulary text via `innerHTML`** — parent-entered text flows to pages; use `textContent`/`createElement` (see Security).
- **A router/middleware library** for seven routes — stdlib patterns suffice (Go 1.26).

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| REST routing | Custom method-dispatch switch | stdlib ServeMux `"GET /path/{id}"` + `req.PathValue` | Go 1.22+ feature; conflict-checking at registration [CITED: go.dev/blog/routing-enhancements] |
| FSRS scheduling/interval math | Any Go reimplementation | `fsrs` 6.3.1 via vocab_manager.py | ADR-locked single engine; per-card state is emergent |
| Vocabulary persistence | New Go tables/ORM | existing `cards` table + Python CLI | Schema is Python-owned; ADR schema-modification process |
| Search | In-memory filter over full list (or FTS) | SQL LIKE via CardRepository + wildcard escaping | DB does the work; scale is hundreds of words |
| Static serving / MIME / caching | Custom file handler | `infra.StaticFileServer` | Already fixes iOS Safari MIME + no-cache + traversal |
| State management in UI | Framework (React/Vue) | Phase 8 vanilla modules | Locked: no build step, 1C1G VPS |
| Atomic file write | Direct `os.WriteFile` on config | temp file + `os.Rename` | D-14 explicit; avoids Python reading a half-written file between spawns |

**Key insight:** every deceptively complex problem here (FSRS, schema, MIME quirks, scheduling) already has an in-repo or stdlib solution; Phase 9's only genuinely new logic is ~200 lines of glue (argv builders, handlers, parser, page).

## Common Pitfalls

### Pitfall 1: `delete-words` is dry-run by default — non-JSON stdout on exit 0
**What goes wrong:** Without `--confirm` the script prints a preview and exits 0 with NO JSON: [VERIFIED: third_party/python/vocab_manager.py:684-688 — verbatim: `if not args.confirm: print(f"Would delete {len(rows)} card(s). Add --confirm to proceed:")` ... `sys.exit(0)`]. The bridge's `extractTrailingJSON` then fails → `ErrPythonJSONParse` masquerading as a crash.
**How to avoid:** The delete endpoint ALWAYS passes `--confirm`; the UI owns the confirmation dialog. Never call delete-words without the flag.
**Warning signs:** "python stdout JSON parse failed" on a delete that "succeeded".

### Pitfall 2: error diagnostics go to STDOUT, not stderr
**What goes wrong:** `rename-unit` on a missing unit: [VERIFIED: third_party/python/vocab_manager.py:654-656 — verbatim: `if count == 0: print(f"No cards found with unit '{old_name}' in category '{args.category}'") sys.exit(1)`] — the message is on stdout; the bridge's `ExitError` carries `Stderr` (empty). The UI would show a generic error.
**How to avoid:** New bridge methods should include captured stdout in the typed error (or map this exit-1 path to a UI-friendly "unit not found" 404). Extend `ExitError` or add a field — keep `errors.Is` sentinel contract.
**Warning signs:** empty error text for failed renames.

### Pitfall 3: `add` silently skips duplicates
**What goes wrong:** `UNIQUE(word, category)` [VERIFIED: vocab_manager.py:112 `UNIQUE(word, category)`]; duplicates are counted, not errors — [VERIFIED: vocab_manager.py:202-206 — verbatim: `except sqlite3.IntegrityError: skipped += 1` ... `print(json.dumps({"added": added, "skipped": skipped}, ensure_ascii=False))`]. A parent re-adding a lesson sees "success" with fewer words than pasted; adding an existing word to a NEW unit silently does nothing (no move).
**How to avoid:** UI must render `added`/`skipped` counts prominently after batch commit; optionally pre-check pasted words against the loaded list in the preview step (data already client-side).
**Warning signs:** "added 18" after pasting 20 with no explanation.

### Pitfall 4: batch `add` supports `example` only in list-mode JSON; one unit per call
**What goes wrong:** dict-mode `{"word":"meaning"}` cannot carry example; list-mode can — [VERIFIED: vocab_manager.py:166-179 — verbatim: `entries.append({ "word": word.strip(), "meaning": str(item.get("meaning", "")) ..., "example": str(item.get("example", "")) ... })`]. Unit comes from the CLI flag for the whole batch: [VERIFIED: vocab_manager.py:196 `args.unit or ""`].
**How to avoid:** Bridge `add` always marshals list-mode JSON; batch paste UI assigns one unit per submit (matching D-10's "one lesson = one batch"). Changing a word's unit afterwards = `edit`.
**Warning signs:** examples missing after import; `--words '{"a":"b"}'` dict format in new code.

### Pitfall 5: argv length on Windows / large batch JSON
**What goes wrong:** 20 words with meanings/examples as inline `--words` JSON can approach uncomfortable argv sizes; Windows limit 32KiB.
**How to avoid:** Copy the `reviewArgs` inline-vs-temp-file pattern verbatim — [VERIFIED: pkg/pybridge/vocab.go:291-294 — verbatim: `reviewInlineByteCap  = 4096 / reviewInlineEntryCap = 100`] with `os.CreateTemp` + `--file`. `add` already supports `--file` [VERIFIED: vocab_manager.py:770 `p_add.add_argument("--file", help="JSON file with words")`].
**Warning signs:** batch add failing only for large lessons.

### Pitfall 6: SQL LIKE wildcards unescaped in search
**What goes wrong:** Parent searching `%` or `_` gets wrong results (`_` matches any char).
**How to avoid:** Escape `%`, `_`, and the escape char in the query builder before `LIKE ? ESCAPE '\'`; keep it inside `CardRepository` (parameterized — never string-concatenated SQL).
**Warning signs:** search for "_" returning the whole library.

### Pitfall 7: `CardRepository.ListByUnit` has no category filter
**What goes wrong:** [VERIFIED: pkg/database/card_repo.go:141-147 — verbatim: `query := fmt.Sprintf("SELECT %s FROM cards", cardColumns)` ... `if unit != "" { query += " WHERE unit = ?"`] — filtering by unit alone mixes english+chinese words that share a unit name (D-09 naming convention spans both categories).
**How to avoid:** Add new methods (`List(ctx, filter)` with category+unit+search, `CountByUnit(ctx, category)`, `ListUnits(ctx, category)`) with category as a required parameter; don't overload the existing method (its callers depend on current semantics).
**Warning signs:** Chinese words appearing in the English browse view.

### Pitfall 8: fsrs_config.json path divergence between Go and manual CLI runs
**What goes wrong:** Go writes `cfg.Python.DataDir/fsrs_config.json`; a manual `vocab_manager.py` run without `DICTATION_DATA_DIR` set reads `SKILL_DIR/data` instead — two different config files, settings "not taking effect".
**How to avoid:** Single resolution rule (recommendation): Python reads `$DICTATION_DATA_DIR/fsrs_config.json` when env set, else `<script>/../data/fsrs_config.json` (matching the DB default rule at L68-69); Go always writes `cfg.Python.DataDir` and the bridge always forwards the env (already true). Document in the ADR-adjacent code comment; e2e test asserts the round-trip through the real subprocess.
**Warning signs:** settings save succeeds but scheduling unchanged after manual CLI use.

### Pitfall 9: invalid fsrs_config.json crashes every subprocess
**What goes wrong:** If Python `json.load`s the file and raises, ALL vocab commands (due/review included — student path!) break.
**How to avoid:** D-14 discretion already fixes the behavior: wrap load in try/except → fall back to 0.85/180 defaults + `Warning:` line on stdout (bridge already tolerates Warning lines). Go-side PUT validates bounds before writing (retention 0.7–0.95, max_interval positive int days).
**Warning signs:** student dictation failing after parent saved weird settings.

### Pitfall 10: XSS via vocabulary text
**What goes wrong:** Parent-entered word/meaning/example rendered with `innerHTML` — script injection into the shared-iPad browser (and into the student page later).
**How to avoid:** `textContent`/`createElement` everywhere; no string-built HTML for data values. Note `StaticFileServer` protects serving, not DOM rendering.
**Warning signs:** any `innerHTML =` with variable interpolation in parent-*.js.

### Pitfall 11: ServeMux trailing-slash surprises
**What goes wrong:** Pattern `"/api/v1/words/"` (trailing slash) is a subtree match — `/api/v1/words/anything` hits it; exact match requires `{$}`. `"/api/v1/words"` matches ONLY the exact path.
**How to avoid:** Decide per route: collection routes exact (`"GET /api/v1/words"`), item routes with wildcard (`"PUT /api/v1/words/{id}"`). Go's registration-time conflict detection catches overlaps early [CITED: go.dev/blog/routing-enhancements].

### Pitfall 12: unbounded request body on a 150MB-budget service
**What goes wrong:** A huge JSON POST (accidental or malicious) allocates without limit.
**How to avoid:** `http.MaxBytesReader` (e.g. 64KiB — a 20-word batch with examples fits comfortably) + `json.Decoder` with `DisallowUnknownFields` for strictness. Existing `ReadTimeout: 10s` bounds slow writes [VERIFIED: config.example.yaml:43].

## Code Examples

### Bridge argv builder for a new command (copy of dueArgs shape)
```go
// Source: pattern copied from pkg/pybridge/vocab.go:65-89 (verified this session)
func addArgs(cfg infra.PythonConfig, opts AddOpts) ([]string, func(), error) {
	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "add"}
	if opts.Unit != "" {
		args = append(args, "--unit", opts.Unit)
	}
	args = append(args, "--category", opts.Category) // required (D-06); validate non-empty
	// list-mode JSON so example fields survive (Pitfall 4):
	// [{"word":"apple","meaning":"苹果","example":"..."}]
	jsonBytes, err := json.Marshal(opts.Entries)
	if err != nil {
		return nil, nil, errors.Wrap(err, "marshal add entries")
	}
	if len(jsonBytes) <= reviewInlineByteCap { // 4096, vocab.go:292
		args = append(args, "--words", string(jsonBytes))
		return args, func() {}, nil
	}
	// large batch: temp file + --file (Pitfall 5), mirroring reviewArgs
	tmp, err := os.CreateTemp("", "hta-add-*.json")
	// ... write, close, append "--file", tmp.Name(); return cleanup
	panic("elided — mirror reviewArgs vocab.go:258-277")
}
```
Field names `word`/`meaning`/`example` and flags `--unit`/`--category`/`--words`/`--file` are verbatim-verified [VERIFIED: vocab_manager.py:166-179,196-197,767-770]. `--category` values: the column default is `english` [VERIFIED: vocab_manager.py:109 `category TEXT NOT NULL DEFAULT 'english'`]; `chinese` is the other value in live use [VERIFIED: vocab_manager.py:792 help text `use chinese for Chinese dictation`]. Restrict the enum to `english|chinese` in Go validation (D-06).

### REST route registration (Go 1.22+ patterns)
```go
// Source: CITED go.dev/blog/routing-enhancements + go1.22 release notes (Context7 /golang/website)
publicMux.HandleFunc("GET /api/v1/words", h.listWords)        // ?category=&unit=&q=
publicMux.HandleFunc("POST /api/v1/words", h.addWords)        // single or batch
publicMux.HandleFunc("PUT /api/v1/words/{id}", h.editWord)    // id := r.PathValue("id")
publicMux.HandleFunc("DELETE /api/v1/words", h.deleteWords)   // ?ids= or body
publicMux.HandleFunc("POST /api/v1/units/rename", h.renameUnit)
publicMux.HandleFunc("GET /api/v1/settings/fsrs", h.getFSRS)
publicMux.HandleFunc("PUT /api/v1/settings/fsrs", h.putFSRS)
```
Method patterns + `req.PathValue` are cited stdlib behavior; route names are a planner-discretion proposal (D-01 discretion).

### edit subcommand (D-07 — proposed contract)
```python
# Proposed addition to vocab_manager.py main() subparsers (D-07 discretion):
#   p_edit = subparsers.add_parser("edit", help="Edit a card without touching FSRS state")
#   p_edit.add_argument("--id", type=int, required=True)
#   p_edit.add_argument("--word"); p_edit.add_argument("--meaning")
#   p_edit.add_argument("--example"); p_edit.add_argument("--unit")
# UPDATE cards SET word=?, meaning=?, example=?, unit=? WHERE id=?  (COALESCE with existing row;
# never write fsrs_card). Output: {"edited": {"id": N, "word": ...}} — mirrors existing
# per-command single-JSON-object contract (e.g. {"reset": card_id}, vocab_manager.py:638).
```
Updated columns `word/meaning/example/unit` are verbatim from the cards DDL — [VERIFIED: vocab_manager.py:103-113 — verbatim:
```
CREATE TABLE IF NOT EXISTS cards (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    word TEXT NOT NULL,
    meaning TEXT NOT NULL DEFAULT '',
    example TEXT NOT NULL DEFAULT '',
    unit TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT 'english',
    fsrs_card TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(word, category)
);
```
All CLI flag spellings and the JSON output key `edited` are proposals (agent discretion) — planner locks them.

### fsrs_config.json (D-14)
```json
{ "desired_retention": 0.85, "maximum_interval": 180 }
```
Keys verbatim from the SCHEDULER constructor quote (vocab_manager.py:78-84); defaults 0.85/180 per D-14 "absent file = current hard-coded defaults 0.85/180".

### Atomic write (Go side, D-14)
```go
// tmp in the SAME directory as the target so rename is atomic on one filesystem
tmp, err := os.CreateTemp(filepath.Dir(path), ".fsrs_config-*.tmp")
// ... write bytes, sync, close ...
err = os.Rename(tmp.Name(), path)  // atomic replace
// on any error: remove tmp
```

### Batch paste parsing (pure function, node-testable)
```js
// Proposed (D-10 discretion): one word per line; "word<TAB>meaning" preferred;
// fallback: split at FIRST whitespace run — meaning may contain spaces.
// Lines starting with '#' skipped; empty lines skipped; per-line errors collected
// for the preview table (never abort the whole batch on one bad line).
function parseBatch(text) { /* returns {rows: [{word, meaning}], errors: [{line, reason}]} */ }
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| gorilla/mux or chi for REST | stdlib ServeMux method+wildcard patterns | Go 1.22 (project on 1.26) | No router dependency; registration-time conflict checks |
| Global FSRS params hard-coded | Config-file-driven scheduler (this phase) | — | D-14: subprocess-per-invocation requires file-based config |
| Anki-style retention 0.9 default | Tuned 0.85 for young student, exposed 0.7–0.95 | vocab_manager.py:74-84 | D-13 slider bounds match official guidance [CITED: github.com/open-spaced-repetition/fsrs4anki docs/tutorial.md — "permissible range ... 0.70 to 0.97 ... 80-95% is reasonable ... Higher retention leads to shorter intervals and more reviews per day"] |

**Deprecated/outdated:** none in-repo relevant to this phase.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | fsrs 6.3.1 `Scheduler` kwargs semantics match usage (found by reading installed source; runtime behavior not executed this session) | Standard Stack / Pattern 3 | Low — vocab_manager.py already constructs Scheduler with these kwargs in production |
| A2 | Route names `/api/v1/words`, `/api/v1/units/rename`, `/api/v1/settings/fsrs` | Architecture Diagram / Code Examples | None — planner discretion per D-01; rename freely |
| A3 | `edit` CLI surface (`--id` + optional `--word/--meaning/--example/--unit`) and output key `edited` | Code Examples | None — agent discretion per D-07; planner locks the contract |
| A4 | Batch paste separator rules (tab-first, first-whitespace fallback, `#` comments) | Code Examples | Low — agent discretion per D-10; confirm in UI spec pass |
| A5 | `category` enum is exactly `english|chinese` in live data | Code Examples | Low-Medium — DB column is free-text TEXT; a rogue value would fail Go enum validation. Mitigation: log-and-reject unknown categories in list endpoint rather than crash |
| A6 | fsrs_config.json location = `cfg.Python.DataDir` (env `DICTATION_DATA_DIR`) | Pattern 3 / Pitfall 8 | Medium — if planner chooses script-relative instead, the manual-CLI divergence in Pitfall 8 inverts; either way ONE rule must be documented and e2e-tested |
| A7 | Playwright suite can drive parent.html against the real service on this Windows dev host (`make e2e` previously used for student UI) | Validation Architecture | Low — same webServer boot path as existing specs; if chromium not installed, one-time `npx playwright install chromium` |

## Open Questions

1. **fsrs_config.json canonical location** (A6)
   - What we know: both resolution inputs exist (`cfg.Python.DataDir` Go-side; `DICTATION_DATA_DIR` env forwarded; Python default `SKILL_DIR/data`).
   - What's unclear: whether manual CLI usage on the VPS (outside the service) needs the same file honored.
   - Recommendation: DataDir + env rule (Pitfall 8), document in code comment; planner decides — both are cheap.

2. **Error response format** (D-01 discretion)
   - What we know: no in-repo REST error convention yet (health endpoint is success-only).
   - Recommendation: `{"error":{"code":"...","message":"..."}}` with codes `invalid_request|not_found|conflict|python_failed`; map bridge sentinels via `errors.Is` (ErrPythonExit→python_failed with stdout diagnostic per Pitfall 2).

3. **delete-words granularity in the UI** (D-11)
   - What we know: CLI supports by-unit and by-word-list delete; both need `--confirm`.
   - What's unclear: whether parent deletes individual words, whole units, or both in v1.
   - Recommendation: both (word-row delete + unit-header delete with count in the confirm dialog); the endpoint maps 1:1 to the two CLI modes.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | all Go work | ✓ | 1.26 (go.mod) | — |
| Python venv | vocab CLI runs | ✓ | third_party/python/.venv (fsrs 6.3.1) | `make py-bootstrap` recreates |
| Node.js | JS unit tests, Playwright | ✓ | package.json scripts in active use | — |
| Playwright chromium | parent UI e2e | ✓ (suite exists) | @playwright/test ^1.63 | `npx playwright install chromium` if missing |
| SQLite DB | cards reads/writes | ✓ | data dir per config.yaml (machine-specific) | — |
| Local service boot | e2e / manual UAT | ✓ | `make run` / run.ps1 (loads .env, frees :8080) | — |

**Missing dependencies with no fallback:** none.
**Missing dependencies with fallback:** none.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go testing (`go test`) + plain `node` test scripts + Playwright |
| Config file | none needed (Makefile targets; playwright.config.js at repo root) |
| Quick run command | `go test -v -short ./...` |
| Full suite command | `go test -race -v ./... && npm test && make e2e` |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| VOC-01 | addArgs/editArgs argv construction (pure) | unit | `go test ./pkg/pybridge/ -run TestAddArgs -short -v` | ❌ Wave 0 |
| VOC-01 | add/edit endpoint validation + bridge dispatch | unit (fake Executor) | `go test ./pkg/web/api/ -run TestAddWords -short -v` | ❌ Wave 0 |
| VOC-01 | real `edit` subcommand preserves fsrs_card | integration (linux tag, like pkg/pybridge/e2e_test.go) | `go test ./pkg/pybridge/ -run TestEditKeepsFSRS -v` (tags: integration) | ❌ Wave 0 |
| VOC-01 | batch paste parser | unit (node) | `node tests/test-parent-batch-parser.js` | ❌ Wave 0 |
| VOC-02 | rename/delete endpoints; List/CountByUnit queries | unit | `go test ./pkg/database/ -run TestCardRepoList -short -v` + api tests | ❌ Wave 0 |
| VOC-03 | settings GET/PUT: bounds validation, atomic write, invalid-file fallback | unit + integration | `go test ./pkg/web/api/ -run TestFSRSConfig -short -v`; python fallback via integration test | ❌ Wave 0 |
| WEB-01 | parent page: add→preview→commit→group→edit→rename→delete→settings | e2e (Playwright, real service) | `npx playwright test tests/e2e/parent-management.spec.js` | ❌ Wave 0 |
| WEB-01/04 | responsive layout phone/tablet/desktop | e2e viewport matrix (mirror ipad-layout.spec.js approach) | same spec, 3 viewports | ❌ Wave 0 |

### Sampling Rate
- **Per task commit:** `go test -v -short ./... && npm test`
- **Per wave merge:** `go test -race -v ./... && npm test && make e2e`
- **Phase gate:** full suite green before `/gsd-verify-work`; manual UAT: batch-paste a real 课文 lesson (C4 closure needs Chinese words — D-06).

### Wave 0 Gaps
- [ ] `pkg/pybridge/vocab_mgmt_test.go` — argv builders + sentinel classification (fake exec)
- [ ] `pkg/web/api/handlers_test.go` (+ `fsrs_config_test.go`) — handler unit tests with fake Executor/CardRepository
- [ ] `pkg/database/card_repo_query_test.go` — category/search/count queries (in-memory sqlite)
- [ ] `tests/test-parent-batch-parser.js` — add to `package.json` scripts.test chain
- [ ] `tests/e2e/parent-management.spec.js` — full parent flow; consider a `tests/e2e-wip/` scratch if service boot is flaky on Windows, promote before phase gate
- [ ] Python `edit` coverage: extend the linux-tagged pybridge integration pattern rather than standing up pytest (no Python test infra exists in-repo)

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no (by design) | D-04 rejected PIN — private family VPS, URL-only access; documented accepted risk, revisit trigger recorded |
| V3 Session Management | no | No sessions; stateless JSON endpoints |
| V4 Access Control | yes (boundary-level) | No auth on `/api/v1/*` — same accepted risk as D-04; mutation endpoints limited to parent-owned domain objects; `delete-words` requires UI confirm + CLI `--confirm` (two-step) |
| V5 Input Validation | yes | Go handler validation: category enum (`english|chinese`), `desired_retention` 0.7–0.95 float, `maximum_interval` positive int, word/meaning/example length caps, `http.MaxBytesReader` (≤64KiB), `json.Decoder.DisallowUnknownFields`; card id via `strconv.ParseInt` on PathValue |
| V6 Cryptography | no | No secrets handled by this phase |
| V12 File Handling | yes | fsrs_config.json: fixed config-derived path (never user input), atomic temp+rename, Go-side bounds before write, Python fallback-to-defaults on invalid content |
| V14 Configuration | yes | fsrs_config.json is effectively scheduler config: validate-then-write, warn-on-fallback, current-vs-default display in UI (D-15) |

### Known Threat Patterns for {Go REST + subprocess bridge + vanilla JS}

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| XSS via parent-entered vocabulary text | Tampering | `textContent`/`createElement` only; no `innerHTML` with data; batch preview table built via DOM APIs |
| SQL injection via search/filter params | Tampering | Parameterized queries in CardRepository; LIKE wildcard escaping (`ESCAPE '\'`) — never string-built SQL |
| Command injection via unit/word names in argv | Elevation | `exec.CommandContext` argv slice, no shell (existing T-03-07 pattern); no user input ever reaches a shell string |
| Destructive bulk delete misfire | Tampering/Repudiation | UI confirm dialog showing count + CLI `--confirm` required (Pitfall 1); dry-run never exposed as an endpoint behavior |
| Unbounded request body / batch size | DoS | `http.MaxBytesReader` + entry-count cap on batch (e.g. ≤100 rows, matching `reviewInlineEntryCap` precedent) |
| Half-written config file read by subprocess | Tampering | Atomic write (D-14) + Python fallback-to-defaults + Warning (Pitfall 9) |
| Path traversal via static serving | Elevation | Already mitigated: `http.Dir` containment in StaticFileServer [VERIFIED: pkg/infra/static.go:50-52] |
| Stale/rogue category values stored historically | Tampering | Go read endpoints log-and-filter unknown categories (A5); write endpoints enforce the enum |

## Sources

### Primary (HIGH confidence)
- Codebase files read this session (all VERIFIED tags): `third_party/python/vocab_manager.py` (full read), `pkg/pybridge/vocab.go`, `pkg/pybridge/executor.go`, `pkg/database/card_repo.go`, `pkg/infra/static.go`, `cmd/service/main.go:440-569`, `pkg/infra/config.go` (grep of PythonConfig fields), `pkg/pybridge/bridge.go` (grep of childEnv), `config.example.yaml`, `Makefile`, `go.mod`, `package.json`, `third_party/python/requirements.txt`
- Installed `fsrs-6.3.1` source: `third_party/python/.venv/Lib/site-packages/fsrs/scheduler.py:162-204` — Scheduler kwargs + validation scope
- `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` — write-separation contract
- `.planning/phases/09-parent-vocabulary-management/09-CONTEXT.md` — decisions D-01..D-15

### Secondary (MEDIUM confidence)
- Context7 `/golang/website` — Go 1.22 routing enhancements (method patterns, PathValue, precedence, `{$}`) — cross-checked against two official Go sources in one fetch
- FSRS desired-retention guidance — [CITED: github.com/open-spaced-repetition/fsrs4anki docs/tutorial.md + wiki The-Optimal-Retention] — official project docs, two pages agree

### Tertiary (LOW confidence)
- None — no websearch-only claims were promoted to the body without an official-source anchor

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — zero new deps; everything verified in-repo this session
- Architecture: HIGH — all integration points opened and quoted (publicMux, bridge, repo, static, env)
- Pitfalls: HIGH — every pitfall backed by a verbatim quote from a file read this session; FSRS range guidance MEDIUM (official external docs)
- Validation: HIGH — existing frameworks confirmed (Makefile, npm test chain, Playwright suite)

**Research date:** 2026-09-27
**Valid until:** 2026-10-27 (stable, codebase-grounded; re-check only if vocab_manager.py or pybridge change first)
