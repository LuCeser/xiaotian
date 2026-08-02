---
phase: 05-fsrs-core-implementation
reviewed: 2026-08-02T00:00:00Z
depth: standard
files_reviewed: 5
files_reviewed_list:
  - pkg/infra/config.go
  - pkg/infra/config_test.go
  - pkg/fsrs/service.go
  - pkg/fsrs/service_test.go
  - cmd/service/main.go
findings:
  critical: 0
  warning: 2
  info: 2
  total: 4
status: issues_found
---

# Phase 05: Code Review Report

**Reviewed:** 2026-08-02
**Depth:** standard
**Files Reviewed:** 5
**Status:** issues_found

## Summary

The Phase 5 FSRS orchestration slice (config layer, FSRSService, and main wiring) is
functionally sound. The orchestration layer in `pkg/fsrs/service.go` correctly delegates
to `pybridge.Executor` without reimplementing the FSRS algorithm, the default-merging
logic is correct, and the config validation chain is properly wired end-to-end. The
test suite covers the documented contract: config-default merging, opt overrides, error
passthrough, and the empty-locked-set case.

No Critical issues were found. Two Warnings and two Info items were identified, all
related to code quality / maintainability rather than correctness or security:

- One struct doc-comment was misplaced during a refactor, leaving the security-relevant
  `WSConfig` type without a godoc and contaminating `DatabaseConfig`'s doc.
- `NewFSRSService` discards the field-specific validation error from `cfg.Validate()`,
  replacing it with a generic message that defeats the package's stated "rich error
  handling" design goal.
- Two pieces of dead code (`fmt` import kept alive artificially; a misleading helper
  that claims to trigger driver registration but does nothing).

Cross-module verification performed:
- `fsrs.FSRSService` correctly consumes `pybridge.Executor` (interface satisfied by
  `*pybridge.Bridge` per the compile-time check at `pkg/pybridge/bridge.go:45`).
- `pybridge.DueOpts.Limit <= 0` semantics ("omit the flag") are not triggered by
  `SelectDueCards`, because the merge logic in `service.go:92-95` always produces a
  positive limit (validated `[1, 100]`).
- The `http.Server.WriteTimeout` on the public WS listener (`main.go:297`) does NOT
  break long-lived WS connections: gorilla/websocket v1.5.3's `Upgrader.Upgrade` calls
  `netConn.SetDeadline(time.Time{})` after Hijack (`server.go:251` in the module cache),
  clearing any deadline set by `net/http`. This was explicitly verified.

## Critical Issues

None.

## Warnings

### WR-01: WSConfig type-level godoc misplaced to DatabaseConfig

**File:** `pkg/infra/config.go:133-156`
**Issue:**

The doc comment block at lines 133-149 was written for `WSConfig` (lines 133-143
describe the wildcard-origin rule, double-validation, and the zero-sentinel convention)
but is now contiguous with `DatabaseConfig`'s intended doc (lines 144-149). Because Go
associates a contiguous `//`-comment block with the type declaration immediately
following it, the ENTIRE block (133-149) becomes `DatabaseConfig`'s godoc.

Result:
1. `type WSConfig struct` at line 158 has NO godoc comment at all — it inherits nothing.
2. `DatabaseConfig`'s rendered godoc starts with "WSConfig tunes the Go service's
   WebSocket runtime (P0-1)..." which is wrong.
3. `WSConfig` is the struct that owns the `AllowedOrigins` CSRF/origin protection and
   the D-24 wildcard rejection — security-relevant surface that deserves accurate
   documentation for any future contributor touching origin handling.

This appears to be an editing accident: `DatabaseConfig` was inserted between the
`WSConfig` doc and the `WSConfig` struct declaration without splitting the comment
block or moving the doc.

**Fix:** Move each type's doc immediately above its own `type` keyword, with a blank
line separating the two comment blocks.

```go
// WSConfig tunes the Go service's WebSocket runtime (P0-1). It carries only
// operational fields; the wildcard origin "*" is forbidden at BOTH this layer
// and ws.ServerConfig.Validate (D-24). DefaultConfig supplies dev-safe origins
// [localhost, 127.0.0.1] so the service boots without an operator-supplied ws:
// block — there is intentionally NO "origins required" check here (the defaults
// always win when the operator leaves it empty).
//
// The MaxMessageBytes / *BufferSize fields default to ZERO (not 65551/4096) so
// ws.NewServer applies its own semantic defaults via its "treat 0 as
// use-default" branch. This keeps infra as the default/override layer and ws as
// the semantic-default layer (double validation per the P0-1 design decision).
type WSConfig struct {
    // ... fields ...
}

// DatabaseConfig tunes the SQLite storage layer (Phase 4). Holds only the
// operational path; pragmas (WAL, busy_timeout, foreign_keys) are NOT
// configurable here — they are correctness-critical and owned by
// database.Open (D-09). The value SHOULD match PythonConfig.VocabDBPath
// so Go and Python hit the same file (D-03). Empty path defaults to
// $HOME/.home-tutor-agent/vocab.db via database.Open.
type DatabaseConfig struct {
    Path string `json:"path" mapstructure:"path"`
}
```

### WR-02: NewFSRSService discards the field-specific validation error context

**File:** `pkg/fsrs/service.go:78`
**Issue:**

The constructor catches the error from `cfg.Validate()` but discards it, wrapping
the `ErrFSRSInvalid` sentinel with a generic message instead:

```go
if err := cfg.Validate(); err != nil {
    return nil, errors.Wrap(ErrFSRSInvalid, "fsrs config validation failed")
}
```

`errors.Wrap(ErrFSRSInvalid, msg)` produces an error whose `Error()` is
`"fsrs config validation failed: fsrs service configuration invalid"` and whose
`Unwrap()` returns `ErrFSRSInvalid`. The original `err` (e.g.
`"... fsrs.defaultWordCount must be in [1, 100], got 0": configuration invalid`,
which itself wraps `infra.ErrConfigInvalid`) is dropped on the floor.

Two consequences:

1. **Operator-facing diagnostics lose the field name.** In `cmd/service/main.go:131-134`
   the construction error is logged via `infra.ErrField(err)` (which calls
   `err.Error()`). An operator starting the service with a malformed `HTA_FSRS_*` env
   var would see `"fsrs service construction failed: ... fsrs config validation failed:
   fsrs service configuration invalid"` and have no idea WHICH field failed. They would
   need to re-run `infra.LoadConfig` (whose `Config.Validate` DOES preserve the
   field-specific message via `c.FSRS.Validate()` at `config.go:779`) to find the cause.

2. **The `ErrConfigInvalid` sentinel chain is broken** at this constructor boundary:
   `errors.Is(err, infra.ErrConfigInvalid)` returns `false` for an error that
   fundamentally IS a config-validation failure. Only `errors.Is(err, ErrFSRSInvalid)`
   works. This contradicts the package doc comment at `service.go:18-23` which
   advertises "rich error handling (D-05)".

The `exec == nil` branch immediately above (`service.go:74-76`) does preserve its own
specific message, making the contrast explicit. In practice this path is dead in
production because `infra.LoadConfig` already validated `cfg.FSRS` before
`NewFSRSService` is called, but the constructor is documented as independently
callable ("NewFSRSService in Plan 05-02 also calls this directly" per `config.go:299`),
so the loss of context matters for any caller that bypasses `LoadConfig`.

**Fix:** Wrap the underlying error so both the field-specific message and the
`ErrConfigInvalid` chain survive, while keeping `ErrFSRSInvalid` reachable. The
simplest fix is to wrap `err` and chain `ErrFSRSInvalid` via a multi-`%w` format
(Go 1.20+):

```go
if err := cfg.Validate(); err != nil {
    return nil, fmt.Errorf("fsrs config validation failed: %w (sentinel %w)",
        err, ErrFSRSInvalid)
}
```

Or, if the `errors.Wrap` style must be preserved, at least keep the underlying error
as the wrapped value so `errors.Is(err, infra.ErrConfigInvalid)` continues to work:

```go
if err := cfg.Validate(); err != nil {
    return nil, errors.Wrap(err, "fsrs service configuration invalid")
}
```

The latter requires updating `TestNewFSRSServiceValidation` to assert against
`infra.ErrConfigInvalid` instead of (or in addition to) `ErrFSRSInvalid`.

## Info

### IN-01: Dead `fmt` import kept alive by a dummy reference

**File:** `cmd/service/main.go:22` (import) and `cmd/service/main.go:386` (`var _ = fmt.Sprintf`)
**Issue:**

`fmt` is imported but never actually used in `main.go`. The only reference is the
defensive dummy `var _ = fmt.Sprintf`, with a comment claiming it "ensure[s] fmt is
referenced even if future refactors remove the only call site. Cheap to keep, painful
to forget." There IS no other call site — the import exists solely to suppress Go's
"imported and not used" error. `goimports` / `gofmt`-aware linters flag this pattern.

**Fix:** Delete both the import (line 22) and the dummy declaration (lines 384-386).

```go
// In the import block, remove:
//   "fmt"

// At the bottom of the file, remove:
// ensure fmt is referenced even if future refactors remove the only call
// site. Cheap to keep, painful to forget.
// var _ = fmt.Sprintf
```

### IN-02: `importSQLiteDriver` is misleading dead code

**File:** `pkg/infra/config_test.go:627` (call site) and `pkg/infra/config_test.go:646-652` (function)
**Issue:**

The helper `importSQLiteDriver` is documented as importing the modernc.org/sqlite
driver "for its side effect (driver registration)":

```go
// importSQLiteDriver imports the modernc.org/sqlite driver for its side
// effect (driver registration). Separated into a function to isolate the
// import and avoid polluting the test file's import block with a blank import
// that might confuse goimports.
func importSQLiteDriver() struct{} {
    //nolint:unused // This function exists solely for the side effect of importing.
    return struct{}{}
}
```

This comment is wrong. Calling a function that returns an empty struct does NOT trigger
any import. The actual driver registration is performed by the package-level blank
import at `config_test.go:18` (`_ "modernc.org/sqlite"`), which fires the driver's
`init()` when the test binary loads. `importSQLiteDriver()` does nothing — its body
just constructs an empty struct. `TestSQLiteDriverRegistration` passes regardless of
the `_ = importSQLiteDriver()` call at line 627.

A future contributor reading this helper could easily believe that removing the
package-level blank import is safe as long as `importSQLiteDriver()` is still called,
which would silently break the driver registration.

**Fix:** Delete the helper and its call site. Keep only the package-level blank import
(which is the standard, idiomatic way to register a `database/sql` driver) and let the
existing `//nolint` comment go with it.

```go
// In TestSQLiteDriverRegistration, replace:
//   _ = importSQLiteDriver()
// with a comment pointing at the real mechanism:
//   // Driver registration happens via the blank import at the top of this file:
//   //   _ "modernc.org/sqlite"

// Delete the importSQLiteDriver function entirely (lines 642-652).
```

---

_Reviewed: 2026-08-02_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
