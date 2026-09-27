# Phase 9: Parent Vocabulary Management - Pattern Map

**Mapped:** 2026-09-27
**Files analyzed:** 19 (7 modify, 12 new)
**Analogs found:** 19 / 19 (16 exact/strong, 3 role-match — none without an in-repo anchor)

Inputs consumed: `09-CONTEXT.md` (D-01..D-15), `09-RESEARCH.md` (Patterns 1-4,
Pitfalls 1-12), `09-UI-SPEC.md` (Component Inventory — authoritative frontend
surface: `parent.html`, `styles/parent.css`, `js/parent-app.js` +
`parent-modes.js` + `parent-browse.js` + `parent-batch-parser.js`).

---

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `pkg/pybridge/vocab_mgmt.go` (NEW) | service | subprocess request-response | `pkg/pybridge/vocab.go` | exact |
| `pkg/pybridge/executor.go` (MODIFY) | interface/contract | request-response | itself (`RunDue`/`RunUnlock` entries) | exact |
| `pkg/database/card_repo.go` (MODIFY) | repository (read-only) | CRUD (reads) | itself (`ListByUnit`) | exact |
| `pkg/web/api/handlers.go` (NEW) | controller | request-response | `pkg/infra/health.go` + `cmd/service/main.go:495-520` | role-match |
| `pkg/web/api/validate.go` (NEW) | middleware/utility | request-response | `pkg/infra/config.go` `FSRSConfig.Validate` | role-match |
| `pkg/web/api/fsrs_config.go` (NEW) | controller + file-I/O | file-I/O | `pkg/pybridge/vocab.go:254-277` (temp-file handling) | role-match |
| `cmd/service/main.go` (MODIFY) | route wiring/config | request-response | itself (`publicMux` block) | exact |
| `third_party/python/vocab_manager.py` (MODIFY) | CLI service | CRUD + file-I/O | itself (`cmd_rename_unit`/`cmd_reset`/`cmd_add`) | exact |
| `pkg/web/static/parent.html` (NEW) | page/component | request-response (fetch) | `pkg/web/static/index.html` | exact |
| `pkg/web/static/styles/parent.css` (NEW) | config (styles) | — | `pkg/web/static/styles/main.css` | exact |
| `pkg/web/static/js/parent-app.js` (NEW) | component (entry) | request-response | `pkg/web/static/js/app.js` | exact |
| `pkg/web/static/js/parent-modes.js` (NEW) | component | request-response (forms) | `pkg/web/static/js/ui-components.js` (`ManualAnswerForm`/`BatchSubmissionForm`) | exact |
| `pkg/web/static/js/parent-browse.js` (NEW) | component | request-response (list render) | `pkg/web/static/js/ui-components.js` (`BatchSubmissionForm.show` row builder) | exact |
| `pkg/web/static/js/parent-batch-parser.js` (NEW) | utility (pure) | transform | `ui-components.js` statics + `state-manager.js` UMD tail | exact |
| `pkg/pybridge/vocab_mgmt_test.go` (NEW) | test | — | `pkg/pybridge/vocab_test.go` | exact |
| `pkg/web/api/handlers_test.go` (+`fsrs_config_test.go`) (NEW) | test | — | `pkg/database/card_repo_test.go` (fakes + in-memory) | role-match→exact |
| `pkg/database/card_repo_query_test.go` (NEW) | test | — | `pkg/database/card_repo_test.go` | exact |
| `tests/test-parent-batch-parser.js` (NEW) | test | transform | `tests/test-batch-form.js` | exact |
| `tests/e2e/parent-management.spec.js` (NEW) | test (e2e) | request-response | `tests/e2e/dictation-flow.spec.js` + `ipad-layout.spec.js` | exact |

---

## Pattern Assignments

### `pkg/pybridge/vocab_mgmt.go` (service, subprocess request-response)

**Analog:** `pkg/pybridge/vocab.go` — copy the `dueArgs`/`RunDue` and
`reviewArgs`/`RunReview` shapes exactly. This is the single most important
pattern in the phase.

**Package-doc + imports pattern** (`pkg/pybridge/vocab.go:1-38`) — mirror the
file-header comment block that documents the argv contract, then:

```go
import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)
```

**Core argv-builder pattern** (`vocab.go:65-89` — the `dueArgs` shape every new
builder copies; note `--db` BEFORE the subcommand):

```go
func dueArgs(cfg infra.PythonConfig, opts DueOpts) []string {
	args := []string{cfg.VocabScript, "--db", cfg.VocabDBPath, "due"}
	if opts.Category != "" {
		args = append(args, "--category", opts.Category)
	}
	if opts.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(opts.Limit))
	}
	...
	return args
}
```

argv contract (documented verbatim at `vocab.go:15-24`): argv MUST be
`[vocabScript, "--db", dbPath, <subcommand>, ...]` because vocab_manager.py
declares `--db` on the TOP-LEVEL parser (`vocab_manager.py:762`). Subcommand
tokens are **hyphenated**: `rename-unit` / `delete-words`
(`vocab_manager.py:817, 823`) — CONTEXT.md's `rename_unit`/`delete_words` are
the Python function names, NOT argv tokens.

**Payload builder with inline-vs-temp-file** (`vocab.go:239-283` — `addArgs`
copies this for the `--words` JSON batch, RESEARCH Pitfall 5):

```go
var cleanup func()
if len(jsonBytes) <= reviewInlineByteCap && len(results) <= reviewInlineEntryCap {
	args = append(args, "--results", string(jsonBytes))
	cleanup = func() {} // no temp file to remove
} else {
	tmp, err := os.CreateTemp("", "hta-review-*.json")
	if err != nil {
		return nil, nil, errors.Wrap(err, "create review results temp file")
	}
	if _, err := tmp.Write(jsonBytes); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, nil, errors.Wrap(err, "write review results temp file")
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return nil, nil, errors.Wrap(err, "close review results temp file")
	}
	args = append(args, "--file", tmp.Name())
	cleanup = func() { _ = os.Remove(tmp.Name()) }
}
```

Caps at `vocab.go:291-294`: `reviewInlineByteCap = 4096`,
`reviewInlineEntryCap = 100`. `add --file` already exists on the Python side
(`vocab_manager.py:770`).

**Run-method lifecycle + sentinel classification** (`vocab.go:165-206`,
`RunDue`; `RunUnlock` at `vocab.go:387-428` shows the argErr-then-run shape for
builders that return `([]string, error)`):

```go
func (b *Bridge) RunDue(ctx context.Context, opts DueOpts) (DueResult, error) {
	start := time.Now()
	args := dueArgs(b.cfg, opts)
	stdout, stderr, exitCode, err := b.run(ctx, b.cfg.DueTimeout,
		b.cfg.Interpreter, args...)
	defer b.logResult("due", start, stdout, stderr, exitCode, err)

	if err != nil && (errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)) {
		return DueResult{}, errors.Wrap(ErrPythonTimeout, "run due")
	}
	if exitCode != 0 {
		return DueResult{}, &ExitError{
			Cmd: "due", ExitCode: exitCode, Stderr: string(stderr), Err: ErrPythonExit,
		}
	}
	if len(stdout) == 0 {
		return DueResult{}, errors.Wrap(ErrPythonEmpty, "run due")
	}
	raw, parseErr := extractTrailingJSON(stdout)
	if parseErr != nil {
		return DueResult{}, errors.Wrap(ErrPythonJSONParse, "run due")
	}
	var result DueResult
	if err := json.Unmarshal(raw, &result.Cards); err != nil {
		return DueResult{}, errors.Wrap(ErrPythonJSONParse, "run due")
	}
	return result, nil
}
```

Per-command contract notes for the five new methods:
- `RunAdd`: always marshal **list-mode** JSON entries
  `[{"word","meaning","example"}]` so examples survive (`vocab_manager.py:166-179`,
  RESEARCH Pitfall 4); result `{"added":N,"skipped":N}` from
  `vocab_manager.py:206`.
- `RunEdit`: new subcommand; single-JSON-object result mirroring
  `{"reset": card_id}` (`vocab_manager.py:638`).
- `RunRenameUnit`: argv `rename-unit --old-unit --new-unit --category`;
  result `{"renamed","from","to"}` (`vocab_manager.py:663`).
- `RunDeleteWords`: argv `delete-words` + ALWAYS `--confirm` (Pitfall 1:
  without it the script prints a non-JSON preview and exits 0 at
  `vocab_manager.py:684-688`, which breaks `extractTrailingJSON`); result
  `{"deleted":N,...}` (`vocab_manager.py:698, 731`).
- There is NO `RunList` — reads go through `CardRepository` (D-12).

**Error-handling caveat to carry into the plan (Pitfall 2):**
`rename-unit`'s "No cards found..." diagnostic goes to STDOUT with exit 1
(`vocab_manager.py:654-656`), but `ExitError.Stderr` captures only stderr.
New methods should surface captured stdout in the typed error or the handler
should map exit-1 to a UI-friendly message. Keep `errors.Is` sentinel contract.

**Underlying exec engine (do NOT re-implement):** `Bridge.run` at
`pkg/pybridge/bridge.go:268-308` (semaphore → timeout → `exec.CommandContext`
argv slice, no shell → `childEnv` → capture). `childEnv` already forwards
`DICTATION_DATA_DIR=b.cfg.DataDir` (`bridge.go:208-210`) — the fsrs_config.json
env plumbing (D-14/Pitfall 8) is already in place.

**Parser (do NOT re-implement):** `extractTrailingJSON` at
`pkg/pybridge/parse.go:41-96` peels the trailing JSON off mixed
Warning/JSON stdout.

---

### `pkg/pybridge/executor.go` (interface/contract — MODIFY)

**Analog:** itself. Add 4 methods + typed Opts/Result structs following the
existing entries.

**Interface pattern** (`executor.go:54-59`):

```go
type Executor interface {
	RunDue(ctx context.Context, opts DueOpts) (DueResult, error)
	RunReview(ctx context.Context, opts ReviewOpts) (ReviewResult, error)
	RunPregenerate(ctx context.Context, opts PregenOpts) (PregenResult, error)
	RunUnlock(ctx context.Context, opts UnlockOpts) (UnlockResult, error)
}
```

Add `RunAdd/RunEdit/RunRenameUnit/RunDeleteWords` here. Two compile-time
satisfaction checks must keep passing: `bridge.go:45`
(`var _ Executor = (*Bridge)(nil)`) and every fake in tests.

**Opts/result struct pattern** (`executor.go:65-90` `DueOpts`,
`executor.go:204-223` `UnlockOpts` + `UnlockResult`):

```go
type UnlockResult struct {
	Unlocked int `json:"unlocked"`
}
```

JSON tags align exactly with the script's json keys (see `DueCard`
`executor.go:97-107` for the verified-tags convention). `AddResult` mirrors
`{"added":N,"skipped":N}`; `DeleteWordsResult` mirrors `{"deleted":N}`.

**Sentinel errors** (`executor.go:229-252` — reuse, do not add):
`ErrPythonExit`, `ErrPythonTimeout`, `ErrPythonJSONParse`, `ErrPythonEmpty`.
`ExitError` typed struct at `executor.go:265-283` with `Unwrap()`.

---

### `pkg/database/card_repo.go` (repository, CRUD reads — MODIFY)

**Analog:** itself. Add `List(ctx, filter)` / `CountByUnit(ctx, category)` /
`ListUnits(ctx, category)` as NEW methods; do not overload `ListByUnit`
(its callers depend on current semantics — RESEARCH Pitfall 7).

**Interface + write-separation invariant** (`card_repo.go:39-59`):

```go
type CardRepository interface {
	GetByID(ctx context.Context, id int64) (Card, error)
	ListByUnit(ctx context.Context, unit string) ([]Card, error)
}
// var _ CardRepository = (*sqliteCardRepository)(nil)  // line 59 — keep
```

**Explicit column list** (`card_repo.go:86` — never `SELECT *`):

```go
const cardColumns = "id, word, meaning, example, unit, category, fsrs_card, created_at, fetch_locked_until"
```

**Query-builder + scan pattern** (`card_repo.go:140-182` `ListByUnit` — the
shape for `List(filter)`; add category as REQUIRED param + escaped LIKE for
search, RESEARCH Pitfall 6):

```go
func (r *sqliteCardRepository) ListByUnit(ctx context.Context, unit string) ([]Card, error) {
	query := fmt.Sprintf("SELECT %s FROM cards", cardColumns)
	args := []any{}
	if unit != "" {
		query += " WHERE unit = ?"
		args = append(args, unit)
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		r.logger.Error("card list by unit failed",
			infra.F("component", "database/card_repo"),
			infra.F("unit", unit),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "card list by unit")
	}
	defer rows.Close()
	// ... scanCard loop (lines 160-181)
}
```

Shared `scanCard` helper at `card_repo.go:90-114`. Error convention:
`errors.Wrap` + `infra.F("component", ...)` structured logging;
`ErrCardNotFound` sentinel at `card_repo.go:14-18`.

For `CountByUnit`/`ListUnits`, the SQL shape exists Python-side at
`vocab_manager.py:557-562` (`SELECT unit, COUNT(*) ... GROUP BY unit ORDER BY
unit`) — port it parameterized into the repo.

---

### `pkg/web/api/handlers.go` (controller, request-response — NEW)

**Analog:** `pkg/infra/health.go` for the handler+JSON-encode shape;
`cmd/service/main.go:495-520` for route mounting.

**Handler + JSON encode pattern** (`health.go:41-72` — the only in-repo
JSON-emitting HTTP handler; note Content-Type BEFORE WriteHeader):

```go
func HealthHandler(server *Server, logger Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ... build resp
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(httpStatus)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(resp); err != nil {
			logger.Warn("health handler: encode response failed",
				F("component", "infra.health"),
				ErrField(err),
			)
		}
	})
}
```

Response struct convention (`health.go:18-26`): unexported struct with
snake_case json tags.

**Constructor convention** (`static.go:1-11` documents it): "infra exposes
pure constructors returning http.Handler, and cmd/service/main.go owns the
single http.ServeMux that wires them." The new `pkg/web/api` package should
follow the same: a constructor like `NewHandler(deps...) http.Handler` (or a
`Handler` struct with methods), wired in main.go.

**Route-mount pattern** (`cmd/service/main.go:495-496, 505-517` — where
`/api/v1/*` mounts; ServeMux prefers longer patterns over the `/` static
route, and the debug-file block shows collision avoidance):

```go
publicMux := http.NewServeMux()
publicMux.HandleFunc("/ws", wsServer.HandleWebSocket)
if cfg.Server.StaticDir != "" {
	publicMux.Handle(cfg.Server.StaticRoute, infra.StaticFileServer(cfg.Server.StaticDir, logger))
	// Debug interfaces remain reachable at their explicit paths; ServeMux
	// prefers these longer patterns over the "/" prefix ...
	for _, dbg := range debugInterfaces {
		publicMux.HandleFunc("/"+dbg, serveRepoRootFile(dbg))
	}
}
```

New routes register as Go 1.22+ method patterns (stdlib, project on Go 1.26 —
`go.mod:3`): `publicMux.HandleFunc("GET /api/v1/words", h.listWords)` etc.,
with `r.PathValue("id")` for `PUT /api/v1/words/{id}`. No in-repo method-
pattern usage exists yet; route names are planner discretion (RESEARCH
Pattern 2 + Code Examples). Mind trailing-slash semantics (Pitfall 11) and
`http.MaxBytesReader` ≤64KiB on bodies (Pitfall 12).

**Panic-recovery convention** (`main.go:545-546`): existing handlers wrap in
`infra.RecoveryMiddleware(logger, ...)` — apply to mutation endpoints.

**Dependency wiring anchor** (`main.go:150` `pyBridge, err :=
pybridge.NewBridge(cfg.Python, logger)` and `main.go:257` `cardRepo, err :=
database.NewCardRepository(db, logger)`; note `main.go:273`
`_ = cardRepo // consumed by Phase 5 FSRS orchestration` — this line gets
replaced by the real handler wiring).

---

### `pkg/web/api/validate.go` (middleware/utility — NEW)

**Analog:** `pkg/infra/config.go:453-468` `FSRSConfig.Validate` — the in-repo
bounds-validation style (fail-fast, wrapped `ErrConfigInvalid`):

```go
func (c FSRSConfig) Validate() error {
	if c.DefaultWordCount < 1 || c.DefaultWordCount > 100 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("fsrs.defaultWordCount must be in [1, 100], got %d",
				c.DefaultWordCount))
	}
	if c.FetchLockHours <= 0 {
		return errors.Wrap(ErrConfigInvalid,
			fmt.Sprintf("fsrs.fetchLockHours must be > 0, got %v",
				c.FetchLockHours))
	}
	...
}
```

Apply the same shape to: category enum `english|chinese` (D-06; column default
`vocab_manager.py:109`, `chinese` in live use per `vocab_manager.py:792` help),
`desired_retention` ∈ [0.7, 0.95] float (D-13), `maximum_interval` positive
int (D-13), word/meaning/example length caps, batch entry cap ≤100 (matching
`reviewInlineEntryCap`, `vocab.go:293`). Handler-side errors map to a
structured `{"error":{"code","message"}}` body (RESEARCH Open Question 2
recommendation — planner locks it).

---

### `pkg/web/api/fsrs_config.go` (controller + file-I/O — NEW)

**Analog (partial):** `pkg/pybridge/vocab.go:254-277` — the only in-repo
create-temp-file/write/close/remove discipline (used there for argv payloads).
The atomic temp+rename dance itself has NO in-repo precedent; use RESEARCH
"Atomic write" example verbatim:

```go
tmp, err := os.CreateTemp(filepath.Dir(path), ".fsrs_config-*.tmp")
// ... write bytes, sync, close ...
err = os.Rename(tmp.Name(), path)  // atomic replace
// on any error: remove tmp
```

Config keys (`desired_retention`, `maximum_interval`; defaults 0.85/180) come
verbatim from the `SCHEDULER` constructor at `vocab_manager.py:78-84`. Target
path: `cfg.Python.DataDir` (`config.go:381-383` `DataDir` field) — env
`DICTATION_DATA_DIR` is already forwarded to every subprocess by
`bridge.go:208-210`, and Python resolves `DEFAULT_DATA_DIR` from the same env
at `vocab_manager.py:68`. That symmetry is the single-resolution rule for
Pitfall 8.

---

### `cmd/service/main.go` (route wiring — MODIFY)

**Analog:** itself. See handlers.go section above for the `publicMux` excerpt
(`main.go:495-520`). Change set: register `/api/v1/*` method-pattern routes
between the `/ws` registration (line 496) and the static mount (line 505);
construct the api handler with `pyBridge` (line 150) + `cardRepo` (line 257)
and delete the `_ = cardRepo` placeholder (line 273).

---

### `third_party/python/vocab_manager.py` (CLI service, CRUD — MODIFY)

**Analog:** itself. Two surgical changes.

**Change 1 — config-driven SCHEDULER (D-14).** Current constant at
`vocab_manager.py:78-84`:

```python
SCHEDULER = Scheduler(
    desired_retention=0.85,
    learning_steps=(timedelta(minutes=1), timedelta(minutes=10)),
    relearning_steps=(timedelta(minutes=5),),
    maximum_interval=180,
    enable_fuzzing=False,
)
```

Referenced at exactly ONE site: `vocab_manager.py:466`
(`fsrs_card, review_log = SCHEDULER.review_card(fsrs_card, rating, now)`).
Replace the constant with a loader that reads
`$DICTATION_DATA_DIR/fsrs_config.json` (env already resolved at line 68),
falls back to 0.85/180 + `Warning:` line on missing/invalid file (Pitfall 9 —
the bridge tolerates Warning lines via `extractTrailingJSON`). File-reading
convention to copy: the `_load_env()` try/open pattern at
`vocab_manager.py:39-66`.

**Change 2 — `edit` subcommand (D-07).** Copy three existing shapes:

1. *Subparser registration* (`vocab_manager.py:817-820`, hyphenated names,
   `--category` default):

```python
p_rename = subparsers.add_parser("rename-unit", help="Rename a unit")
p_rename.add_argument("--old-unit", required=True, help="Current unit name")
p_rename.add_argument("--new-unit", required=True, help="New unit name")
p_rename.add_argument("--category", default="english", help="Category (default: english)")
```

2. *Command function with not-found exit* (`cmd_reset`,
   `vocab_manager.py:627-638` — the exact shape for `cmd_edit`; note not-found
   prints then `sys.exit(1)`; single-JSON-object result):

```python
def cmd_reset(db, args):
    card_id = args.card_id
    row = db.execute("SELECT id FROM cards WHERE id = ?", (card_id,)).fetchone()
    if not row:
        print(f"Card {card_id} not found")
        sys.exit(1)
    fsrs_card = Card()
    db.execute("UPDATE cards SET fsrs_card = ? WHERE id = ?", (fsrs_card.to_json(), card_id))
    db.commit()
    print(json.dumps({"reset": card_id}))
```

`cmd_edit` differs only in: UPDATE the four text columns
(`word/meaning/example/unit` — DDL at `vocab_manager.py:103-113`) via
COALESCE-with-existing-row, and NEVER write `fsrs_card` (D-07: preserves
learning progress).

3. *Dispatch registration* (`vocab_manager.py:850-863`): add
   `"edit": cmd_edit` to the `commands` dict.

Reference for batch-add behavior the Go layer rides on:
`cmd_add` (`vocab_manager.py:135-206`) — list-mode entries with example
(166-179), unit/category from CLI flags (196-197), duplicate skip via
`sqlite3.IntegrityError` (202-203), result `{"added","skipped"}` (206).

---

### `pkg/web/static/parent.html` (page — NEW)

**Analog:** `pkg/web/static/index.html` (exact). Copy:

- Head block: charset/viewport (with `maximum-scale=1.0` + note that
  `touch-action: manipulation` in CSS is the primary zoom defense)
  (`index.html:11-25`).
- `lang="zh-CN"`, skip-to-content link (`index.html:28`).
- Section skeleton: `<main id="main-content">` with `role="region"` +
  `aria-labelledby` + mount-point `<div id="...-container">` per component
  (`index.html:58-63`).
- Script block with `defer` and explicit load-order comment
  (`index.html:169-192`):

```html
<script src="js/frame-codec.js?v=20260924" defer></script>
...
<script src="js/ui-components.js?v=20260924" defer></script>
<script src="js/app.js?v=20260924" defer></script>
```

Differences per UI-SPEC: no connection indicator (REST page), no link from
student page (D-04), section order header → 添加单词 → 浏览词库 → 学习设置,
two-column ≥1024px. Query-string cache-busting (`?v=...`) convention: bump per
release.

---

### `pkg/web/static/styles/parent.css` (styles — NEW)

**Analog:** `pkg/web/static/styles/main.css` (exact — mobile-first
min-width MQs, same conventions; per UI-SPEC do NOT import/extend the
kid-themed main.css, it is a NEW file reusing its ramp `#1f2937/#6b7280/
#e5e7eb` + font stack).

Copy these blocks:
- `:root` custom-property pattern (`main.css:34-38`).
- Body base: identical font stack, `touch-action: manipulation`,
  `-webkit-tap-highlight-color: transparent`, `overscroll-behavior: none`,
  16px/1.5 (`main.css:66-94`).
- Accessibility utilities `.skip-to-content` / `.visually-hidden`
  (`main.css:98-125`).
- Mobile-first media-query escalation + `prefers-reduced-motion` (UI-SPEC
  cites the student precedent).

Deviation (UI-SPEC): flat `#f5f7fa` background — no kid gradient; accent
`#2563eb`; destructive `#dc2626`; success `#059669`; warning `#d97706`;
44px/48px touch targets; radius 16px cards.

---

### `pkg/web/static/js/parent-app.js` (component entry — NEW)

**Analog:** `pkg/web/static/js/app.js` (exact).

**IIFE + dependency availability checks** (`app.js:33-48`):

```js
(function appEntry() {
  'use strict';
  if (!window.WebSocketClient) {
    console.error('[app] WebSocketClient unavailable — client script did not load.');
    setConnectionState('failed');
    return;
  }
  if (!window.SessionStateManager || !window.SessionButton || ...) { ... }
```

**Optional-component flags** (`app.js:52-66`): `var hasX = !!window.X;` —
apply to parent-modes/parent-browse/parser modules.

**Global error handlers + beforeunload cleanup** (`app.js:69-101`) and the
debug exposure `window.__app = {...}` (`app.js:569-573`) — same shape minus
the WS/audio concerns; a REST page instead owns a small fetch wrapper
(timeout/error-normalization) and 300ms search debounce (UI-SPEC).

---

### `pkg/web/static/js/parent-modes.js` + `parent-browse.js` (components — NEW)

**Analog:** `pkg/web/static/js/ui-components.js` — class-per-component
mounted into a container, `render()` clears via
`while (container.firstChild) removeChild` (`ui-components.js:78-103`), and
ALL data rendering uses `document.createElement` + `.textContent` — never
`innerHTML` with values (Pitfall 10; verified across the whole file, e.g.
lines 84-95, 1600-1637).

**Row-builder pattern** (`ui-components.js:1600-1637`, `BatchSubmissionForm.show`
— the closest analog for the batch preview table and browse word rows):

```js
this.words.forEach(function (word, i) {
  var row = document.createElement('div');
  row.className = 'batch-row';
  var label = document.createElement('span');
  label.textContent = '第 ' + (i + 1) + ' 题';
  var input = document.createElement('input');
  input.type = 'text';
  input.setAttribute('aria-label', '第 ' + (i + 1) + ' 题答案');
  ...
  row.appendChild(label); row.appendChild(input);
  self.rowsHost.appendChild(row);
});
```

**Loading/disabled button narration** (`ui-components.js:1571-1593`):
`this.submitBtn.textContent = '⏳ 批改中…'` then restore — matches UI-SPEC
"Loading: disabled + label kept".

**Class exposure convention** (`ui-components.js` module tail, same as
`state-manager.js:295`):

```js
global.SessionStateManager = SessionStateManager;
})(typeof window !== 'undefined' ? window : this);
```

The `typeof window !== 'undefined' ? window : this` tail is what makes the
module `require()`-able from Node tests (see
`tests/test-batch-form.js:9-11`).

parent-browse additionally needs: unit-group collapse with `aria-expanded`
(`<button>` headers per UI-SPEC) and `localStorage` persistence under
`parent.units.collapsed` — no in-repo localStorage precedent; trivial vanilla
(`localStorage.getItem/setItem`), treat as new-but-standard.

---

### `pkg/web/static/js/parent-batch-parser.js` (utility, pure — NEW)

**Analog:** the static pure-method pattern on `ui-components.js` classes +
the UMD-ish tail of `state-manager.js:295-296` (window-or-module-global), so
`node tests/test-parent-batch-parser.js` can exercise it without a DOM
(exactly how `tests/test-batch-form.js:11` requires
`../pkg/web/static/js/ui-components.js` and calls
`BatchSubmissionForm.buildAnswers(...)` at line 17).

Contract (RESEARCH Code Examples + UI-SPEC): one word per line;
`word<TAB>meaning` preferred, first-whitespace-run fallback; `#` comments and
empty lines skipped; per-line errors collected — never abort the batch.
Return `{rows: [{word, meaning}], errors: [{line, reason}]}`.

---

### `pkg/pybridge/vocab_mgmt_test.go` (test — NEW)

**Analog:** `pkg/pybridge/vocab_test.go` (exact).

**argv-position helpers** (`vocab_test.go:35-62`) — reuse or copy:

```go
func argvFind(argv []string, want string) int { ... }

func assertDBBeforeSubcommand(t *testing.T, argv []string, subcommand string) {
	t.Helper()
	if argv[1] != "--db" { t.Errorf(...) }
	if argv[3] != subcommand { t.Errorf(...) }
}
```

**Table-driven argv test** (`vocab_test.go:67-130` `TestDueArgs`): plain
`infra.PythonConfig{...}` literal + `[]struct{ name; opts; extraArgs }`
cases + `t.Run` subtests; asserts hyphenated tokens (`rename-unit`,
`delete-words`) and `--confirm` presence for delete. Also cover the inline-vs-
temp-file threshold like `TestReviewArgs` (`vocab_test.go:194+`).

Real-Python coverage (edit preserving `fsrs_card`; fsrs_config.json
fallback): extend the `//go:build e2e` tagged suite per
`pkg/pybridge/e2e_test.go:1-36` (TestMain venv bootstrap; tests reuse
package fakes; skip cleanly when no interpreter).

---

### `pkg/web/api/handlers_test.go` / `fsrs_config_test.go` (test — NEW)

**Analog:** `pkg/database/card_repo_test.go` — manual fakes, no testify:
constructor nil-guards (`card_repo_test.go:69-85`), table-driven subtests,
`infra.NewNoOpLogger()`. Fake `pybridge.Executor` + fake `CardRepository`
(D-48 manual-fake convention, same as pybridge's fakes in `bridge_test.go`).
Body/validation cases assert status codes + decoded JSON error envelope.

---

### `pkg/database/card_repo_query_test.go` (test — NEW)

**Analog:** `pkg/database/card_repo_test.go` (exact). Reuse/extend
`seedCardsTable` + `newTestDBWithCards` (`card_repo_test.go:15-66`) —
in-memory `:memory:` SQLite via `newTestDB(t)` (`db_test.go:16`,
modernc.org/sqlite, CGO-free). Add chinese-category + shared-unit-name
fixtures to prove the category filter (Pitfall 7) and LIKE-escaping cases
(`%`/`_`/`\`, Pitfall 6).

---

### `tests/test-parent-batch-parser.js` (test — NEW)

**Analog:** `tests/test-batch-form.js` (exact):

```js
'use strict';
const assert = require('node:assert');
const { parseBatch } = require('../pkg/web/static/js/parent-batch-parser.js');
// numbered { ... } blocks of assert.deepStrictEqual / assert.strictEqual
console.log('test-parent-batch-parser.js: all assertions passed');
```

Register in `package.json` `scripts.test` chain (existing chain runs
`node tests/test-*.js` files — RESEARCH Standard Stack).

---

### `tests/e2e/parent-management.spec.js` (e2e test — NEW)

**Analog:** `tests/e2e/dictation-flow.spec.js` (flow style) +
`tests/e2e/ipad-layout.spec.js` (viewport matrix).

From `dictation-flow.spec.js`: `// @ts-check`, `require('@playwright/test')`,
`page.goto('/')` (→ `/parent.html`), Chinese-literal selectors from real
frontend copy, generous timeouts, `page.evaluate` for state inspection
(`dictation-flow.spec.js:23-55, 87-93`). Soft-skip annotation pattern for
environment-dependent assertions (`dictation-flow.spec.js:115-129`).

From `ipad-layout.spec.js`: computed-style discriminators proving media
queries actually fire (`ipad-layout.spec.js:30-58` `readLayout` +
`window.matchMedia` checks) — apply at 3 viewports (phone/tablet/desktop per
UI-SPEC Responsive Behavior).

Coverage: add→preview→commit→group→edit→rename→delete→settings round-trip
through the real service (`make e2e` webServer boots it via run.ps1).

---

## Shared Patterns

### 1. pybridge argv contract (applies to all new bridge methods)

**Source:** `pkg/pybridge/vocab.go:15-24, 65-66` +
`third_party/python/vocab_manager.py:762, 817-828`

argv = `[vocabScript, "--db", dbPath, <subcommand>, ...flags]`; `--db` BEFORE
subcommand (top-level argparse); subcommand tokens hyphenated
(`rename-unit`, `delete-words`, `merge-units`); delete ALWAYS carries
`--confirm`. Zero-valued opts omit the flag.

### 2. Sentinel error classification via errors.Is (bridge methods + handlers)

**Source:** `pkg/pybridge/executor.go:229-283`; applied at `vocab.go:172-206`

Timeout→`ErrPythonTimeout`; exit≠0→`ExitError{Err: ErrPythonExit}`;
empty stdout→`ErrPythonEmpty`; parse fail→`ErrPythonJSONParse`. Handlers map
these to HTTP error codes/envelope. Known gap to handle: stdout diagnostics
on exit-1 (Pitfall 2).

### 3. JSON HTTP response encode (all /api/v1/* handlers)

**Source:** `pkg/infra/health.go:61-64`

`Content-Type: application/json; charset=utf-8` → `WriteHeader` →
`json.NewEncoder(w)`; response structs carry snake_case json tags; wrap
mutation handlers in `infra.RecoveryMiddleware` (main.go:545 convention).

### 4. XSS-safe DOM rendering (all parent-*.js)

**Source:** `pkg/web/static/js/ui-components.js` (file-wide; e.g. 84-95,
1600-1637)

`document.createElement` + `.textContent` only; never `innerHTML` with
data-derived strings (Pitfall 10). Clear containers with the
firstChild-removeChild loop.

### 5. Parameterized SQL + explicit columns (CardRepository extensions)

**Source:** `pkg/database/card_repo.go:86, 140-147`

`cardColumns` const; `query += " WHERE ..."` + `args = append(args, ...)`;
never string-concatenate user input; escape `%`/`_` for LIKE (Pitfall 6);
Go never writes `cards`/`review_logs` (ADR write-separation, `card_repo.go:39-47`).

### 6. IIFE module + window.X + defer order (all parent-*.js + parent.html)

**Source:** `pkg/web/static/js/app.js:33`, `state-manager.js:25, 295-296`,
`index.html:169-192`

`(function (global) { 'use strict'; ... global.X = X; })
(typeof window !== 'undefined' ? window : this)`; entry checks
`!!window.X` per dependency; scripts load `defer` in dependency order with a
documented order comment.

### 7. Structured logging convention (all new Go code)

**Source:** `pybridge`/`database` throughout, e.g. `card_repo.go:151-156`,
`bridge.go:333-339`

`logger.Error("short message", infra.F("component", "..."), infra.ErrField(err))`;
component string per package (`web/api`, `pybridge`, `database/card_repo`).

---

## No Analog Found

| File / Concern | Role | Data Flow | Reason | Planner Guidance |
|----------------|------|-----------|--------|------------------|
| Atomic config write in `pkg/web/api/fsrs_config.go` | controller | file-I/O | No in-repo atomic temp+rename precedent | Use RESEARCH "Atomic write" snippet verbatim; nearest discipline analog is the temp-file handling in `vocab.go:254-277` |
| Go 1.22 method-pattern routes in `cmd/service/main.go` | route | request-response | All existing registrations are plain paths (`/ws`, `/`, `/health`) | Stdlib-verified per RESEARCH Pattern 2 (go.dev routing-enhancements); mount per Code Examples block; registration-time conflict checking is the safety net |
| localStorage collapse persistence in `parent-browse.js` | component | — | No localStorage use in the student frontend | Trivial vanilla getItem/setItem under key `parent.units.collapsed` (UI-SPEC auto-decision) |
| 300ms debounce in `parent-app.js` | component | — | Student page has no debounced input (event-driven WS) | Vanilla `setTimeout`/`clearTimeout` guard; UI-SPEC Interaction |

All four have RESEARCH.md examples or trivial standard-library implementations;
none block planning.

---

## Metadata

**Analog search scope:** `pkg/**` (pybridge, database, infra, web/static, ws),
`cmd/service`, `third_party/python/vocab_manager.py`, `tests/**`, `package.json`,
`Makefile`, `go.mod`
**Files scanned:** ~30 read (17 fully); greps over `pkg/web/static/js`,
`pkg/database`, `cmd/service`, `pkg/infra`
**Pattern extraction date:** 2026-09-27
**Key line-number caveats:** `vocab_manager.py` is 872 lines; RESEARCH cites
some older offsets (e.g. subcommands at ~L762-828 → now 760-868; SCHEDULER
call site L466 → still 466). Line numbers in THIS document were re-verified
against the working tree on 2026-09-27.
