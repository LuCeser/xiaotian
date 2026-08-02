---
phase: 05-fsrs-core-implementation
fixed_at: 2026-08-02T06:20:00Z
review_path: .planning/phases/05-fsrs-core-implementation/05-REVIEW.md
iteration: 1
findings_in_scope: 2
fixed: 2
skipped: 0
status: all_fixed
---

# Phase 05: Code Review Fix Report

**Fixed at:** 2026-08-02T06:20:00Z
**Source review:** .planning/phases/05-fsrs-core-implementation/05-REVIEW.md
**Iteration:** 1
**Fix scope:** critical_warning (Critical + Warning only; 2 Info findings out of scope)

**Summary:**
- Findings in scope: 2
- Fixed: 2
- Skipped: 0

Both in-scope Warning findings were fixed. Verification: `go build`, `go vet`, and
`go test` all pass for `pkg/infra` and `pkg/fsrs` after each fix. The existing test
`TestNewFSRSServiceValidation` continues to pass (it asserts `errors.Is(err,
ErrFSRSInvalid)`, which the WR-02 multi-`%w` fix preserves via the second `%w`).

## Fixed Issues

### WR-01: WSConfig type-level godoc misplaced to DatabaseConfig

**Files modified:** `pkg/infra/config.go`
**Commit:** 87f82aa
**Applied fix:** Moved the WSConfig doc comment block so each type owns its own
godoc, separated by a blank line. Previously the WSConfig doc (describing the
wildcard-origin rule, double-validation, and the zero-sentinel convention) sat
contiguously above `type DatabaseConfig struct`, causing Go to attach the entire
block to `DatabaseConfig` and leave `WSConfig` (the struct owning the
CSRF/origin-protection surface) with no godoc. Now:
  - `DatabaseConfig`'s doc sits directly above `type DatabaseConfig struct`.
  - `WSConfig`'s doc sits directly above `type WSConfig struct`, separated by a
    blank line so the two comment blocks are not merged.

This is a doc-placement change only; no behavior change.

### WR-02: NewFSRSService discards the field-specific validation error context

**Files modified:** `pkg/fsrs/service.go`
**Commit:** 50a22c7
**Applied fix:** Changed the constructor's `cfg.Validate()` error branch from
`errors.Wrap(ErrFSRSInvalid, "fsrs config validation failed")` (which dropped the
underlying error entirely) to a Go 1.20+ multi-`%w` `fmt.Errorf` that wraps BOTH
the underlying validation error and the `ErrFSRSInvalid` sentinel:

```go
return nil, fmt.Errorf("fsrs config validation failed: %w (sentinel %w)",
    err, ErrFSRSInvalid)
```

This preserves three properties simultaneously:
  1. The field-specific message (e.g. `fsrs.defaultWordCount must be in [1, 100],
     got 0`) survives in `err.Error()`, restoring operator-facing diagnostics
     surfaced via `infra.ErrField(err)` in `main.go`.
  2. `errors.Is(err, infra.ErrConfigInvalid)` returns `true` (via the first `%w`,
     since `FSRSConfig.Validate()` wraps `infra.ErrConfigInvalid` on every path),
     restoring the sentinel chain the package doc advertises (D-05).
  3. `errors.Is(err, ErrFSRSInvalid)` returns `true` (via the second `%w`), so the
     existing `TestNewFSRSServiceValidation` assertions still hold.

Added the `fmt` import. The `github.com/pkg/errors` import remains in use by the
nil-executor branch and the two delegation error paths. This is a logic-affecting
change (error-wrapping semantics), so flagged for human verification of the
sentinel-chain claims above — though all existing tests pass unchanged.

## Skipped Issues

None. Both in-scope (Warning) findings were fixed.

The two Info findings (IN-01 dead `fmt` import in `main.go`; IN-02 misleading
`importSQLiteDriver` helper in `config_test.go`) are outside the `critical_warning`
fix scope and were intentionally not addressed.

---

_Fixed: 2026-08-02T06:20:00Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
