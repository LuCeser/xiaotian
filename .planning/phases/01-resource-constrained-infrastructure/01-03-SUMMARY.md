---
phase: 01-resource-constrained-infrastructure
plan: 03
subsystem: infra/observability
tags: [logging, zap, structured-logging, middleware, panic-recovery, observability]
requires:
  - phase: 01-resource-constrained-infrastructure
    provides: "01-02 LogConfig struct (Level/Format/Output) drives NewLogger; 01-01 go.mod skeleton"
provides:
  - "pkg/infra.Field (D-40 structured-logging field value type)"
  - "pkg/infra.Logger interface (Debug/Info/Warn/Error/With/Sync — D-25, D-43, D-47)"
  - "pkg/infra.ZapLogger (production logger backed by go.uber.org/zap)"
  - "pkg/infra.NewLogger(LogConfig) (Logger, error) — startup constructor (D-19, D-42)"
  - "pkg/infra.NewNoOpLogger() — silent test-only Logger (D-42 zero value)"
  - "pkg/infra.F / pkg/infra.ErrField — field helpers (D-25 conventional keys)"
  - "pkg/infra.RecoveryMiddleware(logger, next) — panic-recovering HTTP middleware (D-37, D-49)"
  - "pkg/infra.sanitizeString — log-injection defense (T-01-09 mitigation)"
affects:
  - "Wave 3+ plans (04 memory, 05 buffer, 06 connection, 07 worker) accept Logger as a constructor parameter"
  - "Plan 08 (server main) wires zap logger via NewLogger and wraps HTTP routes in RecoveryMiddleware"
  - "Plan 08 main_test.go uses NewNoOpLogger() to test service wiring without log output"

tech-stack:
  added:
    - "go.uber.org/zap v1.28.0 (was already in go.mod from prior work; this plan consumes it)"
  patterns:
    - "Small focused Logger interface (D-43 SRP) so tests can swap NoOpLogger without importing zap"
    - "Constructor DI (D-42): every Wave 3+ struct takes Logger as a constructor parameter, never reads a package global"
    - "Field-sanitization gate at the boundary (T-01-09): toZapFields routes every string-typed Field through sanitizeString before emission"
    - "Manual fake (D-48) over mocking framework: middleware_test.go's fakeLogger records into a shared state slice"
    - "Defense-in-depth recovery middleware: log full stack to operator sink, return generic 500 to client (T-01-10)"

key-files:
  created:
    - path: pkg/infra/logging.go
      lines: 303
      purpose: Field, Logger interface, ZapLogger, NewLogger, NewNoOpLogger, F, ErrField, sanitizeString, newLoggerWithWriter test seam
    - path: pkg/infra/logging_test.go
      lines: 261
      purpose: 6 behavior tests (StructuredLogging, LogLevels, ContextFieldPropagation, FieldSanitization, NoOpLogger, LevelFiltering) + parseLogLines helper
    - path: pkg/infra/middleware.go
      lines: 62
      purpose: RecoveryMiddleware — panic recovery, structured Error event, HTTP 500 generic body
    - path: pkg/infra/middleware_test.go
      lines: 275
      purpose: 6 recovery tests (HandlesPanic, LogsPanic, PreservesHealthyHandler, Emits5xxNot200, BodyDoesNotLeakPanic, IncludesRequestID) + fakeLogger manual fake
  modified: []

decisions:
  - "Defined a domain Field type (D-40) instead of leaking zap.Field into the public API, so downstream packages and tests can construct fields without importing zap."
  - "Logger interface deliberately omits Fatal (action-block spec). Library code should not decide to os.Exit; callers that need fatal semantics call logger.Error then os.Exit themselves. The plan's success-criteria text mentions Fatal but the interface contract in the action block does not; following the interface contract as binding."
  - "Route every string-typed Field through sanitizeString (T-01-09 defense-in-depth). zap's JSON encoder already escapes control bytes; sanitizeString makes the contract independent of encoder behavior so a future switch to e.g. console format cannot regress log-injection defense."
  - "Track baseFields on ZapLogger even though zap.Logger.With already accumulates them — preserves an inspectable logical context per D-25 and costs nothing."
  - "RecoveryMiddleware adds method and path fields alongside recovered_panic/error/stack so operators can see at a glance which endpoint panicked, without grepping request logs."
patterns-established:
  - "Logger interface contract: every Wave 3+ package accepts `logger infra.Logger` as a constructor parameter (D-42). Tests pass NewNoOpLogger(); production passes a parent logger."
  - "Field helper usage: `F(\"key\", value)` everywhere; `ErrField(err)` for errors. No raw zap.Field import outside pkg/infra."
  - "RecoveryMiddleware wraps every public HTTP handler chain (D-49). Recovery happens before any handler logic — panics never escape to net/http."

requirements-completed:
  - ERR-04

coverage:
  - id: D1
    description: "Structured JSON logger with Debug/Info/Warn/Error levels, context-field propagation via With(...), and log-injection sanitization (ERR-04, D-25, T-01-09)"
    requirement: ERR-04
    verification:
      - kind: unit
        ref: "pkg/infra/logging_test.go#TestStructuredLogging"
        status: pass
      - kind: unit
        ref: "pkg/infra/logging_test.go#TestLogLevels"
        status: pass
      - kind: unit
        ref: "pkg/infra/logging_test.go#TestContextFieldPropagation"
        status: pass
      - kind: unit
        ref: "pkg/infra/logging_test.go#TestFieldSanitization"
        status: pass
      - kind: unit
        ref: "pkg/infra/logging_test.go#TestNoOpLogger"
        status: pass
      - kind: unit
        ref: "pkg/infra/logging_test.go#TestLevelFiltering"
        status: pass
    human_judgment: false
  - id: D2
    description: "Panic recovery HTTP middleware: catches panics, logs structured Error event with stack trace, writes HTTP 500 with generic body (D-37, D-49, T-01-10)"
    verification:
      - kind: unit
        ref: "pkg/infra/middleware_test.go#TestRecoveryMiddlewareHandlesPanic"
        status: pass
      - kind: unit
        ref: "pkg/infra/middleware_test.go#TestRecoveryMiddlewareLogsPanic"
        status: pass
      - kind: unit
        ref: "pkg/infra/middleware_test.go#TestRecoveryMiddlewarePreservesHealthyHandler"
        status: pass
      - kind: unit
        ref: "pkg/infra/middleware_test.go#TestRecoveryMiddlewareEmits5xxNot200"
        status: pass
      - kind: unit
        ref: "pkg/infra/middleware_test.go#TestRecoveryMiddlewareBodyDoesNotLeakPanic"
        status: pass
      - kind: unit
        ref: "pkg/infra/middleware_test.go#TestRecoveryMiddlewareIncludesRequestID"
        status: pass
    human_judgment: false

duration: 21 min
completed: 2026-07-20
status: complete
---

# Phase 01 Plan 03: Structured Logging and Panic Recovery Summary

Logger interface + zap-backed implementation (Debug/Info/Warn/Error/With/Sync) with log-injection sanitization, plus panic-recovery HTTP middleware that emits structured stack-traced Error events and returns HTTP 500 without leaking the panic value.

## Performance

- **Duration:** ~21 min
- **Started:** 2026-07-20T14:30:12Z
- **Completed:** 2026-07-20T14:51:21Z
- **Tasks:** 2 (both TDD)
- **Files modified:** 4 (all new under pkg/infra/)

## Accomplishments

- Logger interface + ZapLogger implementation satisfy ERR-04: every log call emits a JSON line with keys `msg`, `level`, `ts`, plus caller-supplied fields
- D-25 context propagation: `Logger.With(F("request_id","r-1")).With(F("op","fetch"))` accumulates — grandchild entries carry both fields, verified by TestContextFieldPropagation
- T-01-09 log-injection defense: `sanitizeString` replaces raw `\n`/`\r`/`\t` with literal escape sequences before zap encodes the value; TestFieldSanitization asserts zero raw control bytes in output
- D-49 panic recovery from the start: `RecoveryMiddleware(logger, next)` recovers any panic, logs `recovered_panic=true` + error + stack + method + path at Error level, and writes HTTP 500 with a generic body
- T-01-10 information-disclosure defense: TestRecoveryMiddlewareBodyDoesNotLeakPanic asserts neither the panic value nor the stack-trace keyword "goroutine" appears in the response body
- Dependency-injection ready for Wave 3+: NoOpLogger is the test zero value; production callers inject a zap-backed Logger through every constructor

## Task Commits

Each task followed TDD RED→GREEN. Per-task commits:

1. **Task 1 RED: failing tests for Logger interface** — `3644467` (`test`)
2. **Task 1 GREEN: Logger + ZapLogger implementation** — `273d26c` (`feat`)
3. **Task 2 RED: failing tests for recovery middleware** — `80f3906` (`test`)
4. **Task 2 GREEN: RecoveryMiddleware implementation** — `289442b` (`feat`)

## Files Created/Modified

- `pkg/infra/logging.go` (303 lines) — `Field`, `Logger` interface, `ZapLogger`, `NewLogger`, `NewNoOpLogger`, `F`, `ErrField`, `sanitizeString`, `toZapFields`, `selectWriter`, `newLoggerWithWriter` (test seam), `nopLogger`
- `pkg/infra/logging_test.go` (261 lines) — 6 behavior tests + `parseLogLines` helper
- `pkg/infra/middleware.go` (62 lines) — `RecoveryMiddleware(logger, next) http.Handler`
- `pkg/infra/middleware_test.go` (275 lines) — 6 recovery tests + `fakeLogger` manual fake + `findField` helper

No files modified outside pkg/infra/. go.mod already contained `go.uber.org/zap v1.28.0` (added by prior work — this plan is the first to consume it).

## TDD Gate Compliance

Both tasks have `tdd="true"`. Gate sequence verified in git log:

1. `test(01-03): add failing tests for Logger interface` — RED `3644467` (Task 1)
2. `feat(01-03): implement Logger interface and ZapLogger` — GREEN `273d26c` (Task 1)
3. `test(01-03): add failing tests for panic recovery middleware` — RED `80f3906` (Task 2)
4. `feat(01-03): implement panic recovery middleware` — GREEN `289442b` (Task 2)

RED phases confirmed failing (undefined symbols — package did not compile) before GREEN implementations were written. No REFACTOR commit — GREEN code was already minimal and idiomatic.

## Decisions Made

- **Field is a domain type, not `zap.Field`.** Tests, fakes, and downstream packages construct `infra.Field{...}` without importing `go.uber.org/zap`. `toZapFields` is the only place that crosses the boundary. This is D-40 (domain types for distinct concepts) applied to logging.
- **No `Fatal` in the Logger interface.** The action-block interface spec omits Fatal even though success-criteria text mentions it. Library code should not call `os.Exit`; callers that need fatal semantics log at Error then exit themselves. Following the explicit interface contract as binding.
- **Sanitize at the Field boundary, not at the encoder.** `toZapFields` routes every string-typed Field through `sanitizeString` before handing it to zap. Defense-in-depth: zap's JSON encoder already escapes control bytes, but our sanitization makes the contract independent of encoder choice.
- **RecoveryMiddleware adds `method` and `path` fields.** The plan only requires `recovered_panic`, `error`, `stack`. Adding `method` and `path` (from the request) costs nothing and lets operators see which endpoint panicked without correlating request logs — a modest DX improvement that does not change any contract.
- **Manual fake over testify.** `fakeLogger` in middleware_test.go is a hand-written Logger implementation with a shared state buffer between parent and child (so `fake.With(...)` accumulation is observable). D-48 explicitly prefers this over a mocking framework; also matches the stdlib-`testing` pattern Phase 2 established.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 — RED-test bug] Removed incorrect round-trip assertion in TestFieldSanitization**
- **Found during:** Task 1 GREEN (first test run)
- **Issue:** I had added an extra assertion that `lines[0]["payload"] == malicious` (the original raw-newline string), assuming sanitization preserves byte-equality through JSON round-trip. It does not — sanitization intentionally mutates the value (raw newline → literal backslash-n). The plan's behavior contract is "no raw newline bytes in output", which the count assertions already prove; the round-trip assertion was my own incorrect addition during RED, not in the spec.
- **Fix:** Replaced the round-trip assertion with an assertion that the decoded value equals the literal sanitized form (`line1\nline2\rinjected` as a two-char-escape string), matching what sanitizeString actually produces.
- **Files modified:** pkg/infra/logging_test.go
- **Committed in:** `273d26c` (Task 1 GREEN — the fix was needed to make the tests pass)

---

**Total deviations:** 1 auto-fixed (1 RED-test bug)
**Impact on plan:** Zero scope creep. The fix corrected a wrong assertion in my own RED test that did not match the plan's behavior contract. No production code behavior was changed; the contract is intact and proven.

## Threat Model Verification

All 5 threats from the plan's `<threat_model>` are mitigated:

| Threat | Mitigation in code | Verified by |
|--------|-------------------|-------------|
| T-01-09 Tampering (log injection) | `sanitizeString` in `toZapFields` strips `\n`/`\r`/`\t` before emission | TestFieldSanitization |
| T-01-10 Info Disclosure (panic value to client) | RecoveryMiddleware writes generic "internal server error"; panic value and stack go only to logs | TestRecoveryMiddlewareBodyDoesNotLeakPanic |
| T-01-11 DoS (verbose logging) | `LogConfig.Level` configurable; production default is "info" | TestLevelFiltering (warn drops debug/info) |
| T-01-12 Repudiation (no request correlation) | `Logger.With(F("request_id",...))` propagates into recovered-panic events | TestRecoveryMiddlewareIncludesRequestID |
| T-01-13 Info Disclosure (stacks in prod logs) | Accepted — stack traces needed for debugging; sink access controls out of scope | (Documented acceptance in threat register) |

No NEW threats introduced. No new network endpoints, auth paths, or filesystem access patterns beyond stderr/stdout writes via zap.

## Known Stubs

None. Every interface method has a real implementation; NoOpLogger's no-op methods are intentional (the contract is "silent zero value"), not stubs.

## Verification

```
$ go test -count=1 -v ./pkg/infra/ -run 'TestStructuredLogging|TestLogLevels|TestContextFieldPropagation|TestFieldSanitization|TestNoOpLogger|TestLevelFiltering'
PASS (all 6 logging tests, including TestLogLevels/4 subtests)

$ go test -count=1 -v ./pkg/infra/ -run 'TestRecoveryMiddleware'
PASS (all 6 middleware tests)

$ go test -count=1 ./pkg/infra/
ok  	github.com/xiaotian/home-tutor-agent/pkg/infra  0.476s
(22 top-level tests, all pass; includes the 17 config tests from Plan 02, unaffected)

$ go vet ./pkg/infra/
exit 0

$ gofmt -l pkg/infra/logging.go pkg/infra/logging_test.go pkg/infra/middleware.go pkg/infra/middleware_test.go
(clean — only pre-existing config.go/config_test.go flagged for CRLF, out of scope)
```

**Note on `-race`:** the Makefile's `test` target uses `go test -race`. Locally `gcc` is not installed, so `-race` cannot run in this worktree. CI has `gcc` and will run with `-race`. The pkg/infra code added here has no shared mutable state, no goroutines, and no concurrent map access — the race detector would not find anything. The fakeLogger in middleware_test.go is used sequentially within a single test goroutine (httptest.NewRecorder + ServeHTTP are synchronous).

**Note on `make lint`:** golangci-lint is not installed locally; CI runs it. Enabled linters (gosec, govet, staticcheck, misspell, ineffassign, unused, errcheck, gofmt, goimports) all expected to pass — the code follows the patterns Phase 2 already established; stdlib/external/local import grouping is correct; all returned errors are handled or explicitly discarded.

**Note on `go build ./...`:** reveals a pre-existing `pkg/ws/server.go:180` redeclaration error that was present in the worktree base (`M pkg/ws/server.go` in initial git status) and is unrelated to this plan. Phase 2 is complete and explicitly out of scope. Logged to `deferred-items.md` for a future Phase 02 cleanup. `go build ./pkg/infra/` exits 0 cleanly.

## Self-Check

Files created/modified exist:
- pkg/infra/logging.go — FOUND (303 lines)
- pkg/infra/logging_test.go — FOUND (261 lines)
- pkg/infra/middleware.go — FOUND (62 lines)
- pkg/infra/middleware_test.go — FOUND (275 lines)

Commits exist:
- 3644467 (test: Logger RED) — FOUND
- 273d26c (feat: Logger GREEN) — FOUND
- 80f3906 (test: middleware RED) — FOUND
- 289442b (feat: middleware GREEN) — FOUND

All 12 plan-specified tests pass (6 logging + 6 middleware). Plan complete.

## Next Phase Readiness

- Logger contract is frozen: Wave 3+ plans (04 memory, 05 buffer, 06 connection, 07 worker) can now accept `logger infra.Logger` as a constructor parameter and pass `infra.NewNoOpLogger()` in their unit tests
- Plan 08 (server main) can wire zap via `infra.NewLogger(cfg.Log)` and wrap every HTTP route in `infra.RecoveryMiddleware`
- `infra.F(...)` and `infra.ErrField(...)` are the canonical field helpers for every downstream log call
- Reserved log keys for downstream convention: `request_id`, `component`, `operation`, `error`, `stack`, `recovered_panic`, plus zap defaults `msg`/`level`/`ts`

---
*Phase: 01-resource-constrained-infrastructure*
*Completed: 2026-07-20*
