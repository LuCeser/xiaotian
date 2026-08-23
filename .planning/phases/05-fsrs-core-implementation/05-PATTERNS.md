# Phase 5: FSRS Core Implementation - Pattern Map

**Mapped:** 2026-08-01
**Files analyzed:** 6 (3 new, 3 modified)
**Analogs found:** 6 / 6

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `pkg/fsrs/service.go` (NEW) | service | request-response | `pkg/pybridge/bridge.go` + `pkg/database/card_repo.go` | exact (DI constructor + interface + impl) |
| `pkg/fsrs/service_test.go` (NEW) | test | request-response | `pkg/pybridge/bridge_test.go` + `pkg/pybridge/vocab_test.go` | exact (manual fake + table-driven) |
| `pkg/fsrs/doc.go` (NEW) | doc | n/a | `pkg/pybridge/executor.go` lines 1-17 | exact (package-doc header comment) |
| `pkg/infra/config.go` (MODIFY) | config | n/a | `pkg/infra/config.go` PythonConfig block | exact (extend existing pattern in-file) |
| `pkg/infra/config_test.go` (MODIFY) | test | n/a | `pkg/infra/config_test.go` PythonConfig tests | exact (extend existing pattern in-file) |
| `cmd/service/main.go` (MODIFY) | wiring | n/a | `cmd/service/main.go` pyBridge construction | exact (extend existing wiring in-file) |

All six files have exact analogs. No "no analog" entries.

---

## Pattern Assignments

### `pkg/fsrs/service.go` (service, request-response)

**Analogs (composite):**
- `pkg/pybridge/executor.go` — interface declaration style + sentinel error pattern
- `pkg/pybridge/bridge.go` lines 40-45, 127-170 — compile-time interface check + NewBridge DI constructor
- `pkg/database/card_repo.go` lines 14-18, 48-79 — sentinel error + interface + DI constructor (closest single-file analog)
- `pkg/pybridge/vocab.go` lines 63-87, 119-160 — the exact `pybridge.DueOpts`/`RunDue` signatures this service delegates to

**Imports pattern** — follow `pkg/database/card_repo.go` lines 1-9:
```go
package fsrs

import (
    "context"
    "fmt"

    "github.com/pkg/errors"

    "github.com/xiaotian/home-tutor-agent/pkg/infra"
    "github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)
```
Note: `context` is required for every public method (Pitfall 5 — ctx is first param). `fmt` is needed for the Validate error messages (see config.go pattern). No `os/exec` import — FSRSService never spawns subprocesses directly (Pitfall 2 warning sign).

**Sentinel error pattern** — copy `pkg/database/card_repo.go` lines 13-18 exactly:
```go
// Sentinel errors for FSRSService. Callers MUST branch on these via
// errors.Is rather than string matching (D-05 rich error handling).
var (
    // ErrFSRSInvalid is returned by NewFSRSService when constructor
    // arguments fail validation (nil executor, invalid config).
    ErrFSRSInvalid = errors.New("fsrs service configuration invalid")
)
```
The single sentinel mirrors `ErrCardNotFound`. pybridge sentinel errors (`ErrPythonTimeout`, `ErrPythonExit`, etc.) are preserved through wrapping — do NOT re-classify them (D-05).

**Compile-time interface check** — copy `pkg/database/card_repo.go` line 59 / `pkg/pybridge/bridge.go` line 45:
```go
// Compile-time interface satisfaction check (D-43).
var _ FSRSService = (*fsrsService)(nil)
```

**Interface declaration** — mirror `pkg/pybridge/executor.go` lines 53-57 (small focused interface, D-43):
```go
type FSRSService interface {
    SelectDueCards(ctx context.Context, opts SelectOpts) ([]pybridge.DueCard, error)
    UpdateRatings(ctx context.Context, ratings map[string]string, category string) (pybridge.ReviewResult, error)
}
```
Return pybridge types directly (`pybridge.DueCard`, `pybridge.ReviewResult`) — RESEARCH A3: avoid type-mapping boilerplate.

**DI constructor pattern** — copy `pkg/database/card_repo.go` lines 68-79 (nil-guard + nil-logger substitution):
```go
func NewFSRSService(exec pybridge.Executor, logger infra.Logger, cfg infra.FSRSConfig) (FSRSService, error) {
    if exec == nil {
        return nil, errors.Wrap(ErrFSRSInvalid, "pybridge.Executor must not be nil")
    }
    if err := cfg.Validate(); err != nil {
        return nil, errors.Wrap(ErrFSRSInvalid, "fsrs config validation failed")
    }
    if logger == nil {
        logger = infra.NewNoOpLogger()
    }
    return &fsrsService{exec: exec, logger: logger, cfg: cfg}, nil
}
```
This is a PURE constructor — no subprocess spawn, no file IO (mirrors NewBridge contract, `pkg/pybridge/bridge.go` lines 118-121).

**Core method pattern (SelectDueCards)** — the default-merge logic is the Phase-5-specific novel part; the delegation + error wrapping mirrors `pkg/database/card_repo.go` lines 118-136:
```go
func (s *fsrsService) SelectDueCards(ctx context.Context, opts SelectOpts) ([]pybridge.DueCard, error) {
    // Merge: SelectOpts zero-values fall back to cfg defaults (Pitfall 4 guard).
    limit := opts.Limit
    if limit <= 0 {
        limit = s.cfg.DefaultWordCount
    }
    // ... (category, fetchLock, units merge per RESEARCH Pattern 3) ...

    result, err := s.exec.RunDue(ctx, pybridge.DueOpts{...})
    if err != nil {
        s.logger.Error("fsrs service: select due cards failed",
            infra.F("component", "fsrs"),
            infra.ErrField(err),
        )
        return nil, errors.Wrap(err, "fsrs service: select due cards")
    }
    return result.Cards, nil  // empty slice on fetch-lock (D-02-a), NOT an error
}
```
The `infra.F("component", "fsrs")` + `infra.ErrField(err)` structured-log fields mirror `pkg/database/card_repo.go` lines 127-131 exactly. Return `result.Cards` (possibly empty) on success — never return an error for the locked case (D-02-a / Pitfall 1).

**UpdateRatings pattern** — verbatim forwarding, same error-wrap shape (see RESEARCH Pattern 4 for full body).

---

### `pkg/fsrs/service_test.go` (test, request-response)

**Analogs (composite):**
- `pkg/pybridge/bridge_test.go` lines 47-90 — `fakeLogger` manual fake (D-48, no testify)
- `pkg/pybridge/vocab_test.go` lines 67-188 — `TestDueArgs` table-driven test structure
- `pkg/database/card_repo_test.go` lines 68-231 — `t.Run` subtests with `errors.Is` assertions

**Test file header** — copy `pkg/pybridge/vocab_test.go` lines 1-20 (package doc + stdlib-only note):
```go
// service_test.go exercises FSRSService orchestration: config-default merging,
// pybridge delegation, and error passthrough. Test framework: stdlib `testing`
// only, table-driven + t.Run subtests, manual fakes (D-48, no testify/mock).
// Mirrors vocab_test.go / card_repo_test.go style.
package fsrs

import (
    "context"
    "errors"
    "testing"

    "github.com/xiaotian/home-tutor-agent/pkg/infra"
    "github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)
```

**Manual fakeExecutor pattern** — the fake must satisfy `pybridge.Executor` (3 methods: RunDue/RunReview/RunPregenerate). Mirror the `fakeLogger` struct-and-methods style from `pkg/pybridge/bridge_test.go` lines 50-79:
```go
type fakeExecutor struct {
    dueResult    pybridge.DueResult
    dueErr       error
    reviewResult pybridge.ReviewResult
    reviewErr    error
    pregenResult pybridge.PregenResult
    pregenErr    error

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
func (f *fakeExecutor) RunPregenerate(_ context.Context, _ pybridge.PregenOpts) (pybridge.PregenResult, error) {
    return f.pregenResult, f.pregenErr  // unused by fsrs, but required by interface
}
```
RESEARCH "Code Examples / 手动 Fake Executor" provides the full reference. The `dueCalls`/`reviewCalls` slices enable assertion on forwarded opts (Pitfall 4 — verify config defaults flowed through).

**Table-driven test pattern** — copy `pkg/pybridge/vocab_test.go` lines 73-188 structure (cases slice + `t.Run` + `errors.Is`):
```go
func TestSelectDueCardsConfigDefaults(t *testing.T) {
    cases := []struct {
        name      string
        cfg       infra.FSRSConfig
        opts      SelectOpts
        wantLimit int
        wantCat   string
    }{
        {"opts override config", ...},
        {"config defaults when opts zero", ...},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            fake := &fakeExecutor{dueResult: pybridge.DueResult{Cards: []pybridge.DueCard{{ID: 1}}}}
            svc, err := NewFSRSService(fake, nil, tc.cfg)
            // ... assert fake.dueCalls[0].Limit == tc.wantLimit ...
        })
    }
}
```

**Error-passthrough test** — mirror `pkg/database/card_repo_test.go` lines 130-141 (`errors.Is` on sentinel):
```go
func TestSelectDueCardsErrorPassthrough(t *testing.T) {
    fake := &fakeExecutor{dueErr: pybridge.ErrPythonTimeout}
    svc, _ := NewFSRSService(fake, nil, validCfg())
    _, err := svc.SelectDueCards(context.Background(), SelectOpts{})
    if !errors.Is(err, pybridge.ErrPythonTimeout) {
        t.Errorf("error = %v, want errors.Is ErrPythonTimeout", err)
    }
}
```

**Required test cases** (from RESEARCH Validation Architecture table):
- `TestSelectDueCards` (FSRS-01 delegation)
- `TestSelectDueCardsError` (FSRS-01 sentinel passthrough)
- `TestSelectDueCardsConfigDefaults` (FSRS-02 opts override + config defaults)
- `TestSelectDueCardsFetchLock` (FSRS-03 FetchLockHours flow)
- `TestSelectDueCardsLockedReturnsEmpty` (FSRS-03 / D-02-a empty-not-error)
- `TestUpdateRatings` (FSRS-04 verbatim forwarding)
- `TestUpdateRatingsError` (FSRS-04 sentinel passthrough)
- `TestNewFSRSServiceValidation` (nil exec + invalid cfg)

---

### `pkg/fsrs/doc.go` (doc, n/a)

**Analogs:**
- `pkg/pybridge/executor.go` lines 1-17 — package doc as header comment block (the project's established convention)
- `pkg/database/card_repo.go` — no separate doc.go; package doc lives in the primary file

**Pattern:** RESEARCH lists doc.go as optional. The project convention (pybridge, database, infra) is to put the `// Package <name> ...` comment at the top of the primary source file, NOT a separate doc.go. Two options for the planner:

**Option A (recommended — matches project convention):** Skip doc.go. Put the package doc at the top of `service.go`:
```go
// Package fsrs orchestrates FSRS spaced-repetition operations by delegating
// to the Python vocab_manager.py engine via pybridge.Executor (Phase 3 D-09).
// Go NEVER reimplements the FSRS algorithm—it only composes parameters,
// forwards calls, and passes results. All FSRS state mutations flow through
// RunReview; Go never writes to the cards table directly (ADR write-separation).
package fsrs
```
This mirrors `pkg/pybridge/executor.go` lines 1-17 exactly.

**Option B (if planner prefers separation):** Create a minimal `doc.go` containing only the package comment above. No analog exists in the codebase for this — the project has never used standalone doc.go files.

**Recommendation:** Option A. Do not create doc.go.

---

### `pkg/infra/config.go` (MODIFY, config)

**Analogs (in-file — extend the existing pattern):**
- `pkg/infra/config.go` lines 177-189 — `DatabaseConfig` struct (closest 4-field analog to FSRSConfig)
- `pkg/infra/config.go` lines 191-256 — `PythonConfig` struct (the rich doc-comment + json/mapstructure tag style to copy)
- `pkg/infra/config.go` lines 401-469 — `DefaultConfig()` (where FSRS defaults go)
- `pkg/infra/config.go` lines 560-706 — `Config.Validate()` (where FSRS validation rules go)
- `pkg/infra/config.go` lines 750-803 — `bindEnvForAllLeaves()` (where `fsrs.*` env keys go)

**Five sync points (Pitfall 6 — all five MUST be modified together):**

**1. Config struct field** — add after `Database` at line 121, mirroring the Python/WS/Database block:
```go
    // FSRS tunes the FSRS orchestration service (Phase 5). Holds only
    // operational fields; no secrets (D-24). Defaults flow into
    // FSRSService.SelectDueCards when SelectOpts fields are zero.
    FSRS FSRSConfig `json:"fsrs" mapstructure:"fsrs"`
```

**2. FSRSConfig struct definition** — place after `DatabaseConfig` (line 189), copying its 4-field shape. Each field needs json+mapstructure tags + doc comment (copy PythonConfig's verbose style at lines 195-256):
```go
type FSRSConfig struct {
    DefaultWordCount int     `json:"defaultWordCount" mapstructure:"defaultWordCount"`
    FetchLockHours   float64 `json:"fetchLockHours" mapstructure:"fetchLockHours"`
    DefaultUnit      string  `json:"defaultUnit" mapstructure:"defaultUnit"`
    DefaultCategory  string  `json:"defaultCategory" mapstructure:"defaultCategory"`
}

func (c FSRSConfig) Validate() error {
    if c.DefaultWordCount < 1 || c.DefaultWordCount > 100 {
        return errors.Wrap(ErrConfigInvalid,
            fmt.Sprintf("fsrs.defaultWordCount must be in [1, 100], got %d", c.DefaultWordCount))
    }
    // ... FetchLockHours > 0, DefaultCategory required ...
    return nil
}
```
RESEARCH Pattern 5 provides the full struct + Validate body. The `errors.Wrap(ErrConfigInvalid, ...)` wrapping mirrors every other validation rule in `Config.Validate()` (lines 560-706).

**3. DefaultConfig()** — add `FSRS:` block after `Database:` (line 467), copying the struct-literal style:
```go
        FSRS: FSRSConfig{
            DefaultWordCount: 25,        // FSRS-02: within 20-30 range
            FetchLockHours:   24.0,      // FSRS-03: 24h default
            DefaultUnit:      "",        // all units
            DefaultCategory:  "english", // matches vocab_manager.py default
        },
```

**4. Config.Validate()** — add FSRS block before the final `return nil` (line 705). Either inline the checks (like the Python block at lines 664-689) OR call `c.FSRS.Validate()`:
```go
    // FSRS orchestration (Phase 5). FSRSConfig.Validate enforces field ranges.
    if err := c.FSRS.Validate(); err != nil {
        return err
    }
```

**5. bindEnvForAllLeaves()** — add four keys to the `keys` slice before `"database.path"` (line 798):
```go
        "fsrs.defaultWordCount",
        "fsrs.fetchLockHours",
        "fsrs.defaultUnit",
        "fsrs.defaultCategory",
```
These produce env vars `HTA_FSRS_DEFAULTWORDCOUNT` etc. (Pitfall 6 warning sign — if these are missing, `HTA_FSRS_*` env vars silently do nothing).

---

### `pkg/infra/config_test.go` (MODIFY, test)

**Analogs (in-file — extend existing test groups):**
- `pkg/infra/config_test.go` lines 142-168 — `TestDefaultConfig` "python defaults" subtest (add "fsrs defaults" subtest here)
- `pkg/infra/config_test.go` lines 474-484 — `TestLoadConfigInvalidPythonMaxConcurrent` (template for `TestLoadConfigInvalidFSRSWordCount`)
- `pkg/infra/config_test.go` lines 532-554 — `TestPythonConfigHasNoSecretFields` (template for `TestFSRSConfigHasNoSecretFields`)
- `pkg/infra/config_test.go` lines 570-581 — `TestLoadConfigDatabasePathEnvOverride` (template for `TestLoadConfigFSRSEnvOverride`)

**Test additions required:**

1. **Default assertion** — add a subtest to `TestDefaultConfig` mirroring the "python defaults" block (lines 142-168):
```go
t.Run("fsrs defaults per FSRS-02/03", func(t *testing.T) {
    if cfg.FSRS.DefaultWordCount != 25 { ... }
    if cfg.FSRS.FetchLockHours != 24.0 { ... }
    if cfg.FSRS.DefaultCategory != "english" { ... }
})
```

2. **Validation range tests** — copy `TestLoadConfigInvalidPythonMaxConcurrent` (lines 474-484) shape for `DefaultWordCount` bounds:
```go
func TestLoadConfigInvalidFSRSWordCount(t *testing.T) {
    setRequiredPythonEnv(t)
    t.Setenv("HTA_FSRS_DEFAULTWORDCOUNT", "0")  // below [1,100]
    _, err := LoadConfig("")
    // assert errors.Is(err, ErrConfigInvalid)
}
```

3. **No-secret reflect guard** — copy `TestPythonConfigHasNoSecretFields` (lines 532-554) verbatim, swapping `PythonConfig{}` for `FSRSConfig{}`:
```go
func TestFSRSConfigHasNoSecretFields(t *testing.T) {
    secretSubstrings := []string{"secret", "password", "apikey", "token"}
    typ := reflect.TypeOf(FSRSConfig{})
    // ... same loop ...
}
```
This enforces D-24 automatically — if a future field trips a substring, rename the field.

4. **Env-binding test** — copy `TestLoadConfigDatabasePathEnvOverride` (lines 570-581) shape:
```go
func TestLoadConfigFSRSWordCountEnvOverride(t *testing.T) {
    setRequiredPythonEnv(t)
    t.Setenv("HTA_FSRS_DEFAULTWORDCOUNT", "30")
    cfg, err := LoadConfig("")
    // assert cfg.FSRS.DefaultWordCount == 30
}
```

---

### `cmd/service/main.go` (MODIFY, wiring)

**Analogs (in-file — extend existing wiring):**
- `cmd/service/main.go` lines 115-123 — `pyBridge` construction (the exact pattern to replicate for fsrsSvc)
- `cmd/service/main.go` lines 162-179 — `cardRepo`/`sessionRepo` construction + `_ = repo` placeholder (the placeholder-disposal pattern to replicate)

**Pattern:** Add `fsrs` to the import block (after `database`, line 31), then construct fsrsSvc immediately after the pyBridge block (line 123), replacing the `_ = pyBridge` placeholder:

```go
import (
    // ... existing ...
    "github.com/xiaotian/home-tutor-agent/pkg/database"
    "github.com/xiaotian/home-tutor-agent/pkg/fsrs"   // NEW
    "github.com/xiaotian/home-tutor-agent/pkg/infra"
    "github.com/xiaotian/home-tutor-agent/pkg/pybridge"
    // ...
)
```

**Wiring block** — copy the pyBridge construction shape (lines 115-123). The error-log fields (`infra.F("component", "infra.main")` + `infra.ErrField(err)`) mirror every other construction error in run():
```go
// Phase 5 (D-01): FSRS orchestration service. Composes pyBridge via the
// Executor interface. NewFSRSService is a pure constructor — no subprocess
// spawned, same contract as NewBridge.
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
Note: `pyBridge` is a `*pybridge.Bridge` which satisfies `pybridge.Executor` (compile-time checked at `pkg/pybridge/bridge.go` line 45). Passing it as the first arg to `NewFSRSService(exec pybridge.Executor, ...)` works implicitly. Remove the existing `_ = pyBridge` line at 123 since pyBridge is now consumed by fsrsSvc.

The `_ = fsrsSvc` placeholder mirrors `_ = cardRepo` / `_ = sessionRepo` at lines 178-179 — Phase 7 is the consumer.

---

## Shared Patterns

### Constructor DI (Phase 1 D-42/D-47)
**Source:** `pkg/pybridge/bridge.go` lines 127-170, `pkg/database/card_repo.go` lines 68-79
**Apply to:** `pkg/fsrs/service.go` (NewFSRSService)
**Contract:** (1) nil-dependency guard returning wrapped sentinel error; (2) config Validate() call; (3) nil-logger → `infra.NewNoOpLogger()` substitution; (4) pure constructor — no IO, no goroutine spawn. Return `(Interface, error)`, not `(*Concrete, error)`, so the concrete type stays unexported.

### Compile-time interface check (Phase 1 D-43)
**Source:** `pkg/pybridge/bridge.go` line 45, `pkg/database/card_repo.go` line 59
**Apply to:** `pkg/fsrs/service.go`
```go
var _ FSRSService = (*fsrsService)(nil)
```

### Sentinel errors with errors.Is (Phase 1 D-04/D-05)
**Source:** `pkg/pybridge/executor.go` lines 198-221, `pkg/database/card_repo.go` lines 14-18
**Apply to:** `pkg/fsrs/service.go`
**Contract:** Sentinel errors declared as package-level `var (... = errors.New("..."))`. Callers detect via `errors.Is`, never string matching. The pybridge sentinels (`ErrPythonTimeout`/`ErrPythonExit`/`ErrPythonJSONParse`/`ErrPythonEmpty`) are preserved through `errors.Wrap` — FSRSService does NOT re-classify them (D-05).

### Structured logging via infra.Logger
**Source:** `pkg/database/card_repo.go` lines 127-131, `pkg/pybridge/bridge.go` lines 333-362
**Apply to:** `pkg/fsrs/service.go` (every error path)
**Contract:** `s.logger.Error(msg, infra.F("component", "fsrs"), infra.ErrField(err), ...)`. The `"component"` field value for the fsrs package is `"fsrs"` (mirrors `"pybridge"` / `"database/card_repo"`). Field names are lowercase snake_case per `pkg/infra/logging.go` lines 20-23.

### Config extension (Phase 1 D-52/D-53)
**Source:** `pkg/infra/config.go` — the five sync points (struct field / type+Validate / DefaultConfig / Config.Validate / bindEnvForAllLeaves)
**Apply to:** `pkg/infra/config.go` FSRSConfig addition
**Contract:** All five points MUST be modified together (Pitfall 6). json+mapstructure tags on every field. Validation uses `errors.Wrap(ErrConfigInvalid, fmt.Sprintf(...))`. Env prefix `HTA_` + dotted-key-with-underscores yields `HTA_FSRS_DEFAULTWORDCOUNT`.

### Manual fakes (Phase 1 D-48)
**Source:** `pkg/pybridge/bridge_test.go` lines 47-90 (`fakeLogger`), `pkg/database/card_repo_test.go`
**Apply to:** `pkg/fsrs/service_test.go` (`fakeExecutor`)
**Contract:** No testify/mock. Struct with preset return values + call-capture slices. Must satisfy the full `pybridge.Executor` interface (all 3 methods, even if RunPregenerate is unused).

---

## No Analog Found

None. All six files have exact in-codebase analogs.

---

## Metadata

**Analog search scope:**
- `pkg/pybridge/*.go` (executor.go, bridge.go, vocab.go + their _test.go files)
- `pkg/database/card_repo.go` + `card_repo_test.go`
- `pkg/infra/config.go` + `config_test.go`, `logging.go`, `worker.go`
- `cmd/service/main.go`

**Files scanned:** 14 source files (8 production + 6 test)
**Pattern extraction date:** 2026-08-01
**Key insight:** Phase 5 is an in-pattern phase — every new construct (DI constructor, interface+sentinel, config sub-struct, table-driven test, main.go wiring) has a direct, recently-built analog in Phase 3/4 deliverables. The only novel logic is the SelectOpts→DueOpts default-merge (RESEARCH Pattern 3), which is pure Go with no external pattern to copy.
