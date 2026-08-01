---
phase: quick
plan: 260801-fh3
subsystem: local-dev-environment
tags: [windows, local-dev, config, venv, flaky-test]
status: complete
requires:
  - go1.26 windows/amd64
  - uv 0.10.4
provides:
  - "Windows-local runnable service (go run ./cmd/service boots, :8081/health -> 200)"
  - "Python 3.12 venv at third_party/python/.venv for pybridge debugging"
  - "Local config.yaml (gitignored) wiring interpreter + .hta-data paths"
affects:
  - pkg/infra/config.go
  - pkg/ws/hub_test.go
tech-stack:
  added:
    - "github.com/go-viper/mapstructure/v2 v2.4.0 (promoted indirect->direct)"
  patterns:
    - "viper DecodeHook must compose default hooks when custom hook supplied"
key-files:
  created:
    - config.yaml
    - third_party/python/.venv/
    - .hta-data/cache/audio/
  modified:
    - .gitignore
    - pkg/ws/hub_test.go
    - pkg/infra/config.go
    - pkg/infra/config_test.go
    - go.mod
decisions:
  - "D-quick-1: Rule 1 fix — viper custom DecodeHook silently disables default StringToTimeDurationHookFunc; compose hooks to restore duration parsing (plan diagnosis was incomplete, only flagged the empty-interpreter issue)"
metrics:
  duration: "~17 min"
  tasks: 2
  files: 4 git-tracked + local artifacts
  completed: 2026-08-01
---

# Phase quick Plan 260801-fh3: Home Tutor Agent Windows Local-Dev Baseline Summary

Established a minimal, repeatable Windows 11 local-debug baseline so `go run ./cmd/service` boots and passes the `:8081/health` probe, the full `go test ./...` is green (pkg/ws flake gone), and a local Python 3.12 venv + gitignored config.yaml are in place for ongoing pybridge work.

## What Was Done

### Task 1 — Local environment ready (venv + config.yaml + .gitignore hygiene)

- **Python 3.12 venv created** via `uv venv --python 3.12 third_party/python/.venv`. uv located CPython 3.12.0 (not the App Store stub). Interpreter at `third_party/python/.venv/Scripts/python.exe`.
- **Dependencies installed** with Tsinghua mirror (`UV_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple`): 17 packages (fsrs 6.3.1, edge-tts 7.2.8, requests, tabulate, aiohttp, etc.). `requirements.txt` was NOT modified (no `uv pip freeze` writeback — verified `git diff` clean).
- **Local `config.yaml` written** at repo root (gitignored). Derived from `config.example.yaml`; only the `python` block changed to local-friendly relative paths. All other blocks (memory/session/buffer/server/log/worker/connection/shutdown) match the example exactly. Per **D-05**, the file contains no `VOLCENGINE_TTS_*` secret values (the 3 grep matches are comment lines that name the invariant, identical to `config.example.yaml`'s style — no secret values present).
  - `interpreter: third_party/python/.venv/Scripts/python.exe`
  - `vocabDBPath: ./.hta-data/vocab.db`
  - `dataDir: ./.hta-data`
  - `cacheDir: ./.hta-data/cache/audio`
  - `manifestDir: ./.hta-data/cache`
  - `envFile: ./.hta-data/dictation.env`
- **Runtime data dirs created**: `.hta-data/` and `.hta-data/cache/audio/`.
- **`.gitignore` updated**: added `/config.yaml` under the Environment section (with explanatory comment). `third_party/python/.venv/` was already ignored (lines 18-19).
- **Commit:** `92ae8ea` — `chore: gitignore local config.yaml for Windows dev` (only `.gitignore` enters git; config.yaml/.venv/.hta-data are gitignored).

### Task 2 — hub_test.go flake fix + full local verification

- **Flaky fix:** `pkg/ws/hub_test.go` lines 208 & 218 — `500*time.Millisecond` → `2*time.Second` for both `waitForGoroutineIncrease(...)` and `waitForGoroutineDrain(...)` call sites. Helper signatures (lines 323/337) and all other `time.Sleep` sites untouched. Diagnosis: single-binary `-count` runs always passed, confirming a Windows-scheduler timing flake, not a logic bug.
- **Commit:** `077e77d` — `test(ws): bump goroutine wait timeout to fix Windows scheduler flake`.

### Deviation (Rule 1) — config duration-parsing bug

During Task 2's service-startup verification, the service failed to boot with `'<field>' cannot parse value as 'time.Duration': strconv.ParseInt: invalid syntax` for every Duration field. Root cause in `pkg/infra/config.go`: supplying a custom `viper.DecodeHook(decodeHook)` makes viper's internal `v.decodeHook` non-nil, so viper **skips** its default hook chain (`StringToTimeDurationHookFunc`). The latent bug was hidden because the existing YAML-file tests (`TestLoadConfigFileOverride`, `TestLoadConfigPrecedence`) only overrode integer fields and never exercised the string→Duration path.

- **Fix:** compose the custom hook with the standard hooks so duration + slice parsing runs as the `decodeHook` comment promises:
  `mapstructure.ComposeDecodeHookFunc(decodeHook, mapstructure.StringToTimeDurationHookFunc(), mapstructure.StringToSliceHookFunc(" "))`.
- **Regression test:** added `TestLoadConfigFileDurations` — writes duration-bearing YAML and asserts all 10 Duration fields parse. Closes the coverage gap that hid the bug.
- **go.mod:** `go mod tidy` promoted `github.com/go-viper/mapstructure/v2 v2.4.0` from indirect to direct (only change).
- **Commit:** `4401bbd` — `fix(infra): restore viper duration hook so config.yaml parses`.

This is a deviation from the plan's stated commit scope (plan said Task 2 commits only `hub_test.go`), driven by Rule 1: the plan's diagnosis was incomplete (it only flagged the empty-interpreter issue), and the duration bug directly blocked the task's own must_have ("service starts + health 200"). The fix is localized (one call site + one test), no schema/API/architecture change.

## Verification Results

### Hard gates (must be green)

```
go build ./...   -> BUILD OK
go vet ./...     -> VET OK
go test ./...    -> all PASS (exit 0)
  ok  cmd/service   1.474s
  ok  pkg/infra     1.292s   (incl. new TestLoadConfigFileDurations)
  ok  pkg/pybridge  0.606s
  ok  pkg/ws        (cached; 6.020s fresh)  -- TestHubStopsOnContextCancel stable
```

Target flake test run 3x to confirm stability:
```
go test -count=3 -run 'TestHubStopsOnContextCancel' ./pkg/ws/  -> ok 0.496s (exit 0)
```

### Service startup + health probe (must_have)

Fresh binary (`go build -a -o /tmp/hta-svc.exe ./cmd/service`) reading repo-root `config.yaml`:
```
curl -s -m 2 http://127.0.0.1:8081/health  -> HTTP_STATUS=200
{
  "status": "ok", "uptime": "2.887095s", "load_shedding": false,
  "memory":  { "used_bytes": 11554816, "limit_bytes": 157286400, "percent": 7 },
  "pool":    { "capacity": 1000, ... },
  "connections": { "active": 0, ... },
  "workers": { "queue_capacity": 256, ... }
}
```
Service log confirmed clean boot: `memory monitor started`, `server started` (addr 0.0.0.0:8080, health_addr 127.0.0.1:8081), `health server listening`. Process then force-killed via `taskkill //F //PID` (Git Bash `kill` targets the shell wrapper, not the .exe; the real Windows PID was found via `netstat -ano | grep :8081`). Post-kill probe returned 000 (connection refused) — clean shutdown.

### pybridge e2e (`go test -tags=e2e ./pkg/pybridge/`) — all green, exit 0

Core (non-TTS) paths PASS:
- `TestE2E_RunDueRealPython` — PASS (0.29s)
- `TestE2E_RunReviewRealPython` — PASS (0.22s)
- `TestE2E_ErrorClassification` (timeout + nonzero_exit) — PASS
- `TestE2E_SecretNoLeak` — PASS (no secret leakage)
- `TestChildEnvSecrets`, `TestLogResultNoSecretLeak` — PASS
- All sentinel-error / validation / classification unit tests — PASS

**Known exception (anticipated by plan):** `TestE2E_PregenerateEdgeTTS` — **SKIP** (edge-tts/Volcengine TTS external-service dependency). This is the documented Phase 3 VERIFICATION `human_needed` exception; not a regression.

## Known Stubs

None. No stubs introduced.

## Threat Flags

None. The only security-relevant change is the `.gitignore` hardening (T-quick-01: `/config.yaml` ignored) which is a mitigation, not new surface. D-05 invariant verified: no `VOLCENGINE_TTS_*` secret values in `config.yaml`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Restored viper time.Duration parsing in LoadConfig**
- **Found during:** Task 2 Step 3 (service startup)
- **Issue:** Custom `viper.DecodeHook(decodeHook)` made viper skip its default `StringToTimeDurationHookFunc`, so every Duration field in config.yaml failed via `strconv.ParseInt` and the service could not boot. Coverage gap (YAML tests only overrode ints) hid it.
- **Fix:** Composed `decodeHook` with `StringToTimeDurationHookFunc` + `StringToSliceHookFunc` in the `v.Unmarshal` call; added `TestLoadConfigFileDurations` regression test.
- **Files modified:** pkg/infra/config.go, pkg/infra/config_test.go, go.mod
- **Commit:** `4401bbd`

No other deviations. Task 1 and the hub_test.go flake fix executed exactly as planned.

## Self-Check: PASSED

- Artifacts exist: `third_party/python/.venv/Scripts/python.exe`, `config.yaml`, `.hta-data/cache/audio` — all FOUND.
- Commits exist: `92ae8ea`, `4401bbd`, `077e77d` — all FOUND.
- Edits in place: hub_test.go `2*time.Second` (lines 208, 218), config.go `ComposeDecodeHookFunc` — both FOUND.
- Final working tree (task-relevant tracked files): clean — all committed. Remaining untracked items (`research-plan.json`, `02-PATTERNS.md`) are pre-existing planning artifacts, not produced by this task.
