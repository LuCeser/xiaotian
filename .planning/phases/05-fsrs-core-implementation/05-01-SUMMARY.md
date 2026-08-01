---
phase: 05-fsrs-core-implementation
plan: 01
subsystem: infra
tags: [config, fsrs, viper, go, validation]

# Dependency graph
requires:
  - phase: 01-resource-constrained-infrastructure
    provides: typed Config struct, D-52 layered loader (defaults < file < env), bindEnvForAllLeaves, ErrConfigInvalid sentinel
provides:
  - "infra.FSRSConfig struct (DefaultWordCount, FetchLockHours, DefaultUnit, DefaultCategory)"
  - "func (c FSRSConfig) Validate() error — independently callable from NewFSRSService"
  - "Config.FSRS field wired into the layered loader (fsrs.* config block, HTA_FSRS_* env vars)"
  - "D-03 defaults: wordCount=25, fetchLockHours=24.0, defaultUnit=\"\", defaultCategory=english"
affects: [05-02-fsrs-orchestration-service, fsrs, pybridge]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Sub-struct Validate delegation: Config.Validate calls c.FSRS.Validate() so the sub-validator is independently callable from NewFSRSService"
    - "Explicit BindEnv leaf registration for every new fsrs.* key (Pitfall 6 — omitting leaves makes HTA_FSRS_* silently inert)"
    - "D-24 no-secret reflect guard extended per-struct (mirrors PythonConfig guard) so the invariant is enforced automatically for new config structs"

key-files:
  created: []
  modified:
    - pkg/infra/config.go
    - pkg/infra/config_test.go

key-decisions:
  - "Followed the actual file's PythonConfig/WSConfig pattern rather than the plan's 'DatabaseConfig' anchor, which does not exist in this worktree's base (plan line anchors were prospective)."
  - "Placed the FSRSConfig type definition after PythonConfig to co-locate the two 'operational, no-secret' domain configs."
  - "Kept DefaultUnit empty string as the valid 'all units' default — Validate intentionally does NOT reject it."

patterns-established:
  - "Sub-struct Validate delegation for new Config sub-structs (FSRSConfig.Validate called from Config.Validate and independently from constructors)"
  - "Per-struct D-24 no-secret reflect guard copied verbatim for each new operational config struct"

requirements-completed:
  - FSRS-02
  - FSRS-03

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "FSRSConfig struct with four operational fields plus a Validate method enforcing [1,100] / >0 / non-empty rules"
    requirement: FSRS-02
    verification:
      - kind: unit
        ref: "pkg/infra/config_test.go#TestDefaultFSRSConfig"
        status: pass
      - kind: unit
        ref: "pkg/infra/config_test.go#TestFSRSConfigValidate"
        status: pass
    human_judgment: false
  - id: D2
    description: "FSRSConfig wired into the D-52 layered loader — Config.FSRS field, DefaultConfig defaults, Config.Validate delegation, and bindEnvForAllLeaves for the four fsrs.* leaves"
    requirement: FSRS-03
    verification:
      - kind: integration
        ref: "pkg/infra/config_test.go#TestLoadConfigInvalidFSRSWordCount"
        status: pass
      - kind: integration
        ref: "pkg/infra/config_test.go#TestLoadConfigFSRSWordCountEnvOverride"
        status: pass
    human_judgment: false
  - id: D3
    description: "D-24 no-secret invariant enforced for FSRSConfig via a reflect guard mirroring the PythonConfig guard"
    requirement: FSRS-02
    verification:
      - kind: unit
        ref: "pkg/infra/config_test.go#TestFSRSConfigHasNoSecretFields"
        status: pass
    human_judgment: false

# Metrics
duration: 11min
completed: 2026-08-01
status: complete
---

# Phase 5 Plan 01: FSRSConfig Summary

**Typed FSRSConfig sub-struct (4 operational fields + Validate) wired into the layered Config loader so Plan 05-02's NewFSRSService can read validated fsrs.* defaults and HTA_FSRS_* overrides**

## Performance

- **Duration:** ~11 min
- **Started:** 2026-08-01T13:16:15Z
- **Completed:** 2026-08-01T13:26:45Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments
- FSRSConfig struct (DefaultWordCount, FetchLockHours, DefaultUnit, DefaultCategory) with a self-contained Validate method that wraps ErrConfigInvalid for out-of-range/empty values (D-03/D-04)
- Config.FSRS field wired so LoadConfig populates cfg.FSRS from the fsrs.* block; DefaultConfig supplies the FSRS-02/FSRS-03 defaults (wordCount=25, fetchLockHours=24.0, category=english)
- All five config-layer sync points updated together: struct field, type+Validate, DefaultConfig, Config.Validate delegation, and bindEnvForAllLeaves (Pitfall 6 — HTA_FSRS_* env vars are NOT silently inert)
- D-24 no-secret invariant extended to FSRSConfig via a reflect guard; full pkg/infra suite green with no regressions

## Task Commits

Each task was committed atomically:

1. **Task 1 (RED): FSRSConfig behavior tests** - `e121e4c` (test)
2. **Task 1 (GREEN): FSRSConfig implementation in config.go** - `54f6853` (feat)
3. **Task 2: FSRS env-binding + no-secret guard tests** - `826cc48` (test)

_Note: Task 1 was tdd="true"; the behavior tests (defaults + Validate) were written RED before the implementation, then GREEN. Task 2 added the integration-level env-binding tests and the D-24 reflect guard._

## Files Created/Modified
- `pkg/infra/config.go` - FSRSConfig type + Validate method, Config.FSRS field, DefaultConfig block, Config.Validate delegation, four fsrs.* bindEnv leaves
- `pkg/infra/config_test.go` - TestDefaultFSRSConfig, TestFSRSConfigValidate, TestLoadConfigInvalidFSRSWordCount, TestLoadConfigFSRSWordCountEnvOverride, TestFSRSConfigHasNoSecretFields

## Decisions Made
- **Mirrored the real file's pattern, not the plan's anchor.** The plan instructs to "mirror the immediately preceding DatabaseConfig block" and cites line anchors (~189, ~467, ~798). No DatabaseConfig exists in this worktree's base commit (the anchors were prospective, written against an anticipated Phase 4 state not yet merged here). I followed the actual file's established PythonConfig/WSConfig pattern (the real "operational, no-secret" analog) and wired all five sync points against the live file structure. The behavior and intent of the plan were honored exactly; only the literal anchor struct differed.
- **FSRSConfig type placement.** Declared after PythonConfig to co-locate the two operational-no-secret domain configs.
- **DefaultUnit empty string is valid.** Validate intentionally does not reject DefaultUnit="" (it is the "all units" default); only DefaultCategory is required non-empty.

## Deviations from Plan

### Adaptations (no code-fix needed, no scope change)

**1. [Rule 3 - Blocking adaptation] Plan's DatabaseConfig anchor did not exist**
- **Found during:** Task 1 (reading config.go before editing)
- **Issue:** The plan's `<read_first>` and `<action>` cite DatabaseConfig as "the closest 4-field analog" and line anchors (~189, ~467, ~798) that correspond to a DatabaseConfig none of which exists in pkg/infra/config.go at base commit 8092ff8. Following them literally would have inserted code at the wrong locations.
- **Fix:** Used the actual file's established pattern. PythonConfig (the densest "operational, no-secret" doc-comment style) and WSConfig were the real analogs. All five sync points were placed at the structurally correct locations: Config.FSRS after WS (last field), FSRSConfig type after PythonConfig, DefaultConfig FSRS block after the WS block, Config.Validate block after the WS allowedOrigins loop (before `return nil`), and the four fsrs.* leaves after the ws.* leaves in bindEnvForAllLeaves.
- **Files modified:** none beyond the planned files (config.go, config_test.go)
- **Verification:** go build + go vet + full pkg/infra test suite pass (exit 0)
- **Impact on plan:** None on behavior or intent. The plan's must_haves truths are all satisfied exactly. Only the literal reference struct/line-numbers were adapted to the real codebase.

---

**Total deviations:** 1 adaptation (blocking-context, no scope change)
**Impact on plan:** Zero impact on delivered behavior. All must_haves truths and success criteria met verbatim.

## Issues Encountered
- None. Baseline was green; RED→GREEN cycle was clean; the full suite stayed green throughout.

## User Setup Required
None - no external service configuration required. HTA_FSRS_* env vars are optional overrides (defaults are populated by DefaultConfig).

## Next Phase Readiness
- Plan 05-02 can `import` the `infra` package, accept a `cfg.FSRS infra.FSRSConfig` in `NewFSRSService`, and call `cfg.FSRS.Validate()` independently of `Config.Validate`.
- Config keys available to operators: `fsrs.defaultWordCount`, `fsrs.fetchLockHours`, `fsrs.defaultUnit`, `fsrs.defaultCategory` (file) and `HTA_FSRS_DEFAULTWORDCOUNT` etc. (env).

## Self-Check: PASSED

- Files exist: pkg/infra/config.go, pkg/infra/config_test.go, .planning/phases/05-fsrs-core-implementation/05-01-SUMMARY.md
- Commits exist: e121e4c (test/RED), 54f6853 (feat/GREEN), 826cc48 (test/Task2)
- go build ./pkg/infra/... passes
- go vet ./pkg/infra/... clean
- go test ./pkg/infra/... full suite green (exit 0)

---
*Phase: 05-fsrs-core-implementation*
*Completed: 2026-08-01*
