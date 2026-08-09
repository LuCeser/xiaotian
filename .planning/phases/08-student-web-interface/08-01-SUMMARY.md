---
phase: 08-student-web-interface
plan: 01
subsystem: student-web-interface
tags: [web, static-serving, websocket-client, ipad, go-http]
requires:
  - "Phase 2 pkg/ws WebSocket server (wsServer.HandleWebSocket at /ws)"
  - "Phase 1 pkg/infra Server/Config framework"
provides:
  - "Go static file server (infra.StaticFileServer) serving the student interface"
  - "ServerConfig.StaticDir / StaticRoute configuration knobs"
  - "Responsive iPad-optimized HTML/CSS shell (pkg/web/static)"
  - "WebSocketClient with exponential-backoff reconnection (window.WebSocketClient)"
affects:
  - "cmd/service/main.go public mux (root handler now mounts FileServer when staticDir set)"
  - "pkg/infra/config.go ServerConfig (two new fields + validation)"
tech-stack:
  added:
    - "Go net/http.FileServer wrapped as infra.StaticFileServer (mime.AddExtensionType for .js/.css)"
    - "Vanilla JS (ES6 class) WebSocket client — no framework, no build step"
  patterns:
    - "Handler-function pattern (matches infra.HealthHandler/MetricsHandler)"
    - "Mobile-first responsive CSS with iPad 768-1024px media query + landscape rules"
    - "Exponential backoff reconnection (08-RESEARCH.md Pattern 3, Phase 7.1-verified)"
key-files:
  created:
    - pkg/infra/static.go
    - pkg/infra/static_test.go
    - pkg/web/static/index.html
    - pkg/web/static/styles/main.css
    - pkg/web/static/js/app.js
    - pkg/web/static/js/websocket-client.js
    - pkg/web/static/js/.gitkeep
  modified:
    - pkg/infra/config.go
    - cmd/service/main.go
decisions:
  - "Static serving defaults to DISABLED (StaticDir empty), following Database.Path precedent; operators opt in via server.staticDir. Avoids breaking DefaultConfig-based tests and cross-CWD boot."
  - "infra.Server owns no HTTP mux (mux lives in main.go), so static serving is a package-level constructor (infra.StaticFileServer) matching the HealthHandler/MetricsHandler pattern rather than a method on Server."
  - "WebSocket URL derived from window.location (ws/wss auto-selected) so the same build works regardless of the configured server port."
metrics:
  duration: 26m30s
  completed: 2026-08-09T08:52:22Z
  tasks: 3
  files: 9
status: complete
---

# Phase 8 Plan 01: Student Interface Foundation Summary

iPad-optimized student interface served by a new Go static file server, plus a reconnection-capable vanilla-JS WebSocket client that drives a connection-status indicator.

## What Was Built

### Task 1 — Static file serving on the Go HTTP server (`f6d9986`)
- **`pkg/infra/config.go`**: Added `StaticDir` and `StaticRoute` fields to `ServerConfig`. `StaticDir` defaults to empty (disabled); `StaticRoute` defaults to `"/"`. Validation enforces route format and rejects `/ws` collisions ONLY when static serving is opted in. Added explicit env bindings (`HTA_SERVER_STATICDIR`, `HTA_SERVER_STATICROUTE`) — Pitfall 6 mitigation.
- **`pkg/infra/static.go`** (new): `StaticFileServer(dir, logger)` constructor wraps `http.FileServer` with: correct `.js/.css/.mjs` MIME types via `mime.AddExtensionType` (iOS Safari refuses to run scripts served as text/plain), a no-cache header scoped to HTML entry documents (so deploys reach shared iPads while assets keep ETag caching), and `http.Dir` path-traversal containment (threat T-08-03).
- **`cmd/service/main.go`**: Stat-checks the static directory at boot (fail-fast on misconfiguration) and mounts the FileServer at `StaticRoute` when configured. Preserves the legacy repo-root debug HTML harnesses at their explicit paths so developer workflows survive. Logs the mount point.
- **`pkg/infra/static_test.go`** (new): defaults, route-format validation (5 cases incl. `/ws` collision), env override, empty-dir 404, MIME types for html/js/css, HTML no-cache scoping, and path-traversal rejection.

### Task 2 — Responsive HTML/CSS shell (`f4d85ce`)
- **`pkg/web/static/index.html`**: HTML5 shell with `session-request-section` (word-count selector + start button) and `progress-display-section` (progress bar, word counter, encouragement). Semantic ARIA roles/labels, `role="progressbar"` with `aria-valuenow`, `aria-live` regions, connection-status indicator. Viewport locked to prevent zoom; Chinese kid-friendly copy.
- **`pkg/web/static/styles/main.css`**: Mobile-first. `-apple-system` font, `box-sizing: border-box`, global `touch-action: manipulation`, transparent `-webkit-tap-highlight-color` (Pitfall 1). `kid-button` (60px+ targets, iOS HIG), `session-button` gradient CTA, `progress-container`/`progress-bar`/`progress-fill`, fixed connection indicator with `data-state` color dots. iPad media query `768-1024px` (22px status text per plan), landscape orientation rules, `prefers-reduced-motion` support.
- **`pkg/web/static/js/app.js`**: JSDoc-documented entry-point scaffold (Task 3 fleshes out the WebSocket wiring).
- **`pkg/web/static/js/.gitkeep`**: Tracks the `js/` directory.

### Task 3 — WebSocket client foundation (`7dd5daf`)
- **`pkg/web/static/js/websocket-client.js`** (new): `WebSocketClient` class (08-RESEARCH.md Pattern 3). Exponential backoff reconnection (`1s * 2^attempt`, capped 30s, max 10 attempts — threat T-08-04). Bounded message queue (max 100, drops oldest — Pitfall 2). Binary `ArrayBuffer` + JSON control dispatch (Phase 7.1 audio-ready, `binaryType='arraybuffer'`). Five-state machine surfaced via `stateCallback`. Intentional `disconnect()` suppresses auto-reconnect.
- **`pkg/web/static/js/app.js`**: Constructs `WebSocketClient` against the location-derived `/ws` URL, drives `#connectionStatus` from state changes (Pitfall 4: never leave the student guessing), logs the lifecycle to the console. Full session-flow wiring deferred to Wave 2 per the plan.
- **`pkg/web/static/index.html`**: Added `websocket-client.js` script tag before `app.js` (`defer` preserves order).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — blocking] Plan referenced a non-existent `HTTPConfig` struct**
- **Found during:** Task 1
- **Issue:** The plan said "Add StaticDir field to HTTPConfig struct", but no `HTTPConfig` exists in `pkg/infra/config.go`. The actual HTTP/WS listener config struct is `ServerConfig`.
- **Fix:** Added `StaticDir`/`StaticRoute` to `ServerConfig` (which already holds `Addr`, `ReadTimeout`, `WriteTimeout`, `HealthAddr`).
- **Files modified:** `pkg/infra/config.go`
- **Commit:** `f6d9986`

**2. [Rule 3 — blocking] Static-directory existence check relocated from Validate() to main.go**
- **Found during:** Task 1
- **Issue:** The plan wanted `Validate()` to reject a non-existent static directory. With the plan's default `"./pkg/web/static"`, this would break `DefaultConfig().Validate()` (and `TestLoadConfigDefaults`) because `go test ./pkg/infra/` runs with CWD `pkg/infra/`, where the relative path does not resolve. It would also prevent cross-CWD boot.
- **Fix:** Followed the `Database.Path` precedent — empty `StaticDir` means "disabled"; `Config.Validate()` checks route string format only; `main.go` Stat-checks the directory at boot and fails fast with a clear log line. Default changed from `"./pkg/web/static"` to `""` (opt-in).
- **Files modified:** `pkg/infra/config.go`, `cmd/service/main.go`, `pkg/infra/static_test.go`
- **Commit:** `f6d9986`

**3. [Rule 3 — blocking] Plan's `FileServer()` method / `RegisterRoutes()` did not fit the architecture**
- **Found during:** Task 1
- **Issue:** The plan said "Add `FileServer()` method to `pkg/infra/server.go`" and "Update `RegisterRoutes()`". But `infra.Server` is the lifecycle coordinator for memory/pool/registry/workers — it owns NO HTTP mux (the `http.ServeMux` lives in `cmd/service/main.go`). Forcing a mux into `Server` would be an architectural change (Rule 4 territory) with no benefit.
- **Fix:** Followed the established infra handler-function pattern (`HealthHandler`, `MetricsHandler`, `RecoveryMiddleware`): added a package-level `infra.StaticFileServer(dir, logger)` constructor returning `http.Handler`, wired into `main.go`'s public mux. This is the smaller, in-pattern fix (Rule 3), not an architectural change.
- **Files modified:** `pkg/infra/static.go`, `cmd/service/main.go`
- **Commit:** `f6d9986`

**4. [Rule 3 — blocking] Plan Task 3 omitted `index.html` from the file list**
- **Found during:** Task 3
- **Issue:** The plan's Task 3 listed only `websocket-client.js` and `app.js`, but `app.js` references `window.WebSocketClient` which must be loaded first. Without a `<script>` tag for `websocket-client.js` in `index.html`, the client never loads and `app.js` falls into its "unavailable" error branch.
- **Fix:** Added `<script src="js/websocket-client.js" defer>` before the `app.js` tag in `index.html` (`defer` preserves execution order).
- **Files modified:** `pkg/web/static/index.html`
- **Commit:** `7dd5daf`

## Verification Evidence

| Criterion | Evidence |
|-----------|----------|
| Go server starts and logs static mount point | Smoke test log: `static interface directory verified`, `public server static files mounted` |
| `/` serves index.html | `curl /` → `200 OK`, `Content-Type: text/html; charset=utf-8`, `Cache-Control: no-cache, must-revalidate` |
| `/styles/main.css` MIME | `200 OK`, `Content-Type: text/css; charset=utf-8` |
| `/js/app.js` + `/js/websocket-client.js` MIME | `200 OK`, `Content-Type: text/javascript; charset=utf-8` (iOS Safari will execute) |
| `/ws` coexists with static (no conflict) | `curl /ws` → `400 Bad Request` + `Sec-Websocket-Version: 13` (upgrade handler active, NOT served by FileServer) |
| Plan grep: `class WebSocketClient` | count = 1 (PASS) |
| Plan grep: `exponential backoff` | count = 4 (PASS) |
| JS syntax | `node --check` passes both `websocket-client.js` and `app.js` |
| Phase 7.1 migration (no `HTMLAudioElement`) | grep across `pkg/web/static/` → no matches (PASS) |
| No regressions | `go test ./pkg/infra/` → ok (1.509s); `go build ./...` → exit 0 |
| New tests | 9 focused tests in `static_test.go` (defaults, route validation x5 incl. /ws collision, env override, empty-dir 404, MIME types, no-cache scoping, traversal rejection) — all pass |

**Note on criteria 2-4 (iPad Safari responsive layout, touch targets, no zoom-on-focus):** these require manual verification on a real iPad / Safari Responsive Design Mode and are covered by the phase's UAT checkpoint. The structural prerequisites (viewport meta, `touch-action: manipulation`, 60px+ `.kid-button`, iPad media query, `-webkit-tap-highlight-color: transparent`) are all present in the committed files.

## Threat Mitigations Applied

| Threat | Mitigation | Status |
|--------|------------|--------|
| T-08-01 (Spoofing, WS) | Origin validation handled by Phase 2 `CheckOrigin` (pre-existing) | Inherited |
| T-08-02 (Tampering, static) | Read-only static files, no user input processed | Accept (per plan) |
| T-08-03 (Info disclosure, traversal) | `http.Dir` containment + dedicated traversal test | Mitigated + tested |
| T-08-04 (DoS, reconnect flood) | `maxReconnectAttempts = 10` in client + connection limits in server | Mitigated |

## Known Stubs (intentional, plan-sanctioned)

| Stub | File | Reason | Resolved by |
|------|------|--------|-------------|
| `handleMessage` only logs binary/control frames | `pkg/web/static/js/app.js` | Plan: "Defer full wiring to Wave 2" | Wave 2 (session flow) |
| Word-count selector & start button not wired to outgoing WS messages | `pkg/web/static/js/app.js` | Plan: "Defer full wiring to Wave 2" | Wave 2 |
| `progress-display-section` stays hidden until session start | `pkg/web/static/index.html` | Wave 2 reveals it via JS | Wave 2 |

These stubs do NOT block the plan's goal (a loadable, iPad-optimized interface scaffold with a connecting WebSocket client) — they are explicitly deferred by the plan.

## Self-Check: PASSED

- [x] `pkg/infra/static.go` exists
- [x] `pkg/infra/static_test.go` exists
- [x] `pkg/web/static/index.html` exists
- [x] `pkg/web/static/styles/main.css` exists
- [x] `pkg/web/static/js/app.js` exists
- [x] `pkg/web/static/js/websocket-client.js` exists
- [x] Commit `f6d9986` found in git log
- [x] Commit `f4d85ce` found in git log
- [x] Commit `7dd5daf` found in git log
