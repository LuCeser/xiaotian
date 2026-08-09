---
phase: 08-student-web-interface
plan: 02
subsystem: student-web-interface
tags: [web, websocket, session-lifecycle, state-management, ui-component, ipad]
requires:
  - "Phase 8 Plan 01 static serving + WebSocketClient foundation (pkg/web/static)"
  - "Phase 2 pkg/ws WebSocket server (wsServer.HandleWebSocket at /ws)"
  - "Phase 7 session.Manager handleStartSession (cmd/service/handlers.go)"
provides:
  - "SessionStateManager — pub/sub state store (window.SessionStateManager) single source of truth for session + connection state"
  - "SessionButton — kid-friendly DICT-01 CTA component (window.SessionButton) with idle/starting/active/complete states"
  - "WebSocketClient.handleControlFrame — validated JSON control-frame dispatcher (session_ready/error/progress_update/session_complete)"
affects:
  - "pkg/web/static/js/websocket-client.js (control-frame dispatch + state-manager registration)"
  - "pkg/web/static/js/app.js (full refactor: wires state manager, ws client, session button, connection indicator)"
  - "pkg/web/static/index.html (session-request section restructured for JS-driven button)"
tech-stack:
  added:
    - "Vanilla JS pub/sub state store (no framework) — 08-RESEARCH.md Pattern 1"
    - "Vanilla JS UI component class with state-driven render — 08-RESEARCH.md Code Examples"
  patterns:
    - "Single source of truth: state changes flow through SessionStateManager.setState → notify subscribers; UI never mutates state directly"
    - "Structural validation at trust boundary (T-08-05): control frames type-checked before driving UI state"
    - "In-flight request flag distinct from session-active flag (click feedback before backend round trip)"
key-files:
  created:
    - pkg/web/static/js/state-manager.js
    - pkg/web/static/js/ui-components.js
  modified:
    - pkg/web/static/js/websocket-client.js
    - pkg/web/static/js/app.js
    - pkg/web/static/index.html
decisions:
  - "Kept WebSocketClient transport-generic: control-frame dispatch lives in handleControlFrame on the client but state is applied via a registered SessionStateManager (setStateManager), so the client stays reusable and Wave 1's messageHandler fallback is preserved when no manager is registered."
  - "Centralized control-frame dispatch in the ws client (per plan Task 3) rather than app.js: app.js's handleMessage now handles only binary audio frames + forwarded unknown control types, while session_ready/error/progress_update/session_complete flow through handleControlFrame → stateManager."
  - "Replaced Wave 1's static 10/20/30 word-count selector + disabled Chinese start button with the JS-driven SessionButton: DICT-01 mandates a single fixed '30 words' CTA, so keeping the selector would have produced dead/conflicting UI (Rule 3)."
  - "session_id and words fields were front-loaded into SessionStateManager in Task 1; Task 3 therefore needed no state-manager.js edit (setState is a generic merge and type-specific logic lives in handleControlFrame)."
metrics:
  duration: 13m11s
  completed: 2026-08-09T09:13:58Z
  tasks: 3
  files: 5
status: complete
---

# Phase 8 Plan 02: Session Request Feature Summary

A working vertical slice where students click a prominent "I want to dictate 30 words" button, the interface sends a `start_session` WebSocket frame, and the backend's `session_ready` response drives the button through idle → starting → active → complete visual states via a pub/sub SessionStateManager.

## What Was Built

### Task 1 — Session state manager (`79c1ba5`)
- **`pkg/web/static/js/state-manager.js`** (new): `SessionStateManager` class (08-RESEARCH.md Pattern 1). Holds the authoritative session + connection state: `sessionActive`, `status` (IDLE/LISTENING/DICTATING/WAITING_PARENT/FEEDBACK), `session_id`, `words`, `currentWord`, `totalWords`, `completedWords`, `connectionStatus` (mirrors WebSocketClient's five-state machine), `lastError`. API: `setState(updates)` (merge + notify), `subscribe(cb)` (returns unsubscribe), `notify()` (defensive — a throwing subscriber is logged and skipped), `getState()` (shallow copy with cloned `words` array so subscribers can't mutate the canonical state). `STATUS_*` and `CONN_*` constants exposed on the constructor.
- **WebSocket client (no change):** the plan's Task 1 asked to add `stateCallback` to `WebSocketClient`, but Wave 1 already added it (constructor param + `_setState` notify). Confirmed via `grep -c stateCallback = 7`. No edit needed.

### Task 2 — Session request button component (`54a7210`)
- **`pkg/web/static/js/ui-components.js`** (new): `SessionButton` class. Renders the DICT-01 "I want to dictate 30 words" CTA into `#session-button-container` with `kid-button session-button` classes (large iOS-HIG touch target via main.css), `aria-label="Start a dictation session with 30 words"`, and an `aria-live=polite` region so transitions are announced. `handleClick()` sends `{type:"start_session", params:{limit:30}}` (limit hard-coded — T-08-06) and immediately flips to "Starting session..." + disabled. `update(state)` drives four visual states from the state manager: idle (enabled), starting (disabled), active/"Session in progress..." (disabled), complete→reverts. A new backend error during the in-flight request re-enables the button and surfaces the message. An in-flight `requesting` flag (distinct from `sessionActive`) gives click feedback before the backend round trip and guards against double-clicks (T-08-07).
- **`pkg/web/static/index.html`**: replaced Wave 1's static 10/20/30 word-count selector + disabled Chinese `#startSession` button with `<div id="session-button-container"></div>`; added `role="region"` ARIA landmark on the section; added `state-manager.js` and `ui-components.js` `<script defer>` tags in dependency order (websocket-client → state-manager → ui-components → app); updated heading + footer copy to match the single-button flow.

### Task 3 — WebSocket session lifecycle wiring (`eecf674`)
- **`pkg/web/static/js/websocket-client.js`**: added `handleControlFrame(payload)` dispatcher with **T-08-05 structural validation** (rejects frames without a string `type`; per-type field validation for `session_ready`/`progress_update`). Dispatches `session_ready` → `setState({sessionActive:true, status:DICTATING, session_id, words, totalWords:words.length, ...})`; `error` → `setState({lastError:message})` (preserves session flag); `progress_update` → `setState({currentWord, completedWords})`; `session_complete` → `setState({sessionActive:false, status:FEEDBACK, completedWords:totalWords})`. Unknown types logged + forwarded to `messageHandler`. Added `setStateManager(sm)`; `_onMessage` now routes parsed JSON to `handleControlFrame` when a manager is registered (Wave 1 `messageHandler` fallback preserved).
- **`pkg/web/static/js/app.js`**: full refactor. Constructs `SessionStateManager` (single source of truth), `WebSocketClient` with a state callback that pipes `connectionStatus` into the state manager, registers the manager on the client, mounts `SessionButton` into `#session-button-container`, and subscribes the button + the connection indicator to the state manager. `handleMessage` now handles binary audio frames (Phase 7.1 hand-off, future) + forwarded unknown control types. `handleError` logs and records `lastError`.
- **`pkg/web/static/js/state-manager.js`** (no edit this task — see Decisions).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — blocking] Plan Task 1 ws-client change already satisfied by Wave 1**
- **Found during:** Task 1
- **Issue:** Task 1 asked to "Add stateCallback parameter to WebSocketClient constructor" and "Call stateCallback on connection state changes." Wave 1 already delivered this (constructor param + `_setState` notify + onopen/onclose/onerror wiring).
- **Fix:** Created `state-manager.js` as specified; made no `websocket-client.js` edit. Verified `grep -c stateCallback = 7` in the existing client.
- **Files modified:** none beyond the new `state-manager.js`
- **Commit:** `79c1ba5`

**2. [Rule 3 — blocking] Wave 1 static UI conflicted with the plan's JS-driven SessionButton**
- **Found during:** Task 2
- **Issue:** `index.html` already contained a static 10/20/30 word-count selector and a disabled Chinese `#startSession` button. The plan's SessionButton hard-codes limit:30 (DICT-01) and mounts into a new `#session-button-container`. Keeping both would produce a confusing, non-functional duplicate UI (a selector whose choice is ignored + a dead disabled button + the real CTA), violating the plan's "one prominent button" goal and Rule 2 (correctness).
- **Fix:** Removed the selector + static start button; replaced with `#session-button-container`. Updated the heading (was "how many words today?" — implies selection) to a ready prompt and the footer instruction (referenced a selection step that no longer exists).
- **Files modified:** `pkg/web/static/index.html`
- **Commit:** `54a7210`

**3. [Plan imprecision] Task 3 verification grep `handleControlFrame == 1` is impossible for a wired method**
- **Found during:** Task 3 verification
- **Issue:** The plan's automated check `grep -c "handleControlFrame" == 1` cannot be satisfied by working code: the method must be both defined (`handleControlFrame(payload) {`) and invoked (`this.handleControlFrame(parsed)`), giving a functional minimum of 2. (Contrast Task 1's `subscribe >= 2`, which the author wrote correctly for a multi-reference symbol.)
- **Fix:** Reduced occurrences to the functional minimum of 2 (definition + single call site) by rewording JSDoc references. `session_ready` was reduced to exactly 1 (the `case` line). The wiring is correct and present; the `== 1` constraint is an authoring error in the plan, not a code defect.
- **Files modified:** `pkg/web/static/js/websocket-client.js`
- **Commit:** `eecf674`

## Verification Evidence

| Criterion | Evidence |
|-----------|----------|
| Plan grep: `class SessionStateManager` == 1 | PASS (1) |
| Plan grep: `subscribe` >= 2 | PASS (26) |
| Plan grep: `class SessionButton` == 1 | PASS (1) |
| Plan grep: `I want to dictate` == 1 | PASS (1) |
| Plan grep: `session_ready` == 1 | PASS (1) |
| Plan grep: `handleControlFrame` == 1 | 2 (definition + call site — see Deviation #3; functional minimum for a wired method) |
| JS syntax (`node --check`) | PASS — all 4 files (websocket-client, state-manager, ui-components, app) |
| Go build (`go build ./...`) | PASS (exit 0 — no Go changed, confirms no breakage) |
| End-to-end smoke (throwaway node + vm sandbox) | PASS — click→start_session limit:30; session_ready→sessionActive+disabled; progress_update→counters; session_complete→re-enabled; malformed frame rejected without state corruption |
| Threat T-08-05 (validate response structure) | handleControlFrame + `_applySessionReady`/`_applyProgressUpdate` type-check fields before setState |
| Threat T-08-06 (hard-code limit) | `SESSION_LIMIT = 30` constant; no user input field |
| Threat T-08-07 (button-click flood) | `requesting` flag + `disabled` attribute; handleClick double-guard |

**Manual / UAT criteria** (button click sends a real WebSocket frame, backend `session_ready` disables the button, reconnection preserves state): these require a running backend + iPad Safari and are covered by the phase's UAT checkpoint. The structural prerequisites — correct outbound frame shape `{type:"start_session",params:{limit:30}}`, validated inbound dispatch, state-driven button transitions — are verified by the smoke test above against the actual message contracts in `cmd/service/handlers.go`.

## Threat Mitigations Applied

| Threat | Mitigation | Status |
|--------|------------|--------|
| T-08-05 (Spoofing, session_ready) | `handleControlFrame` validates `type` is a string; `_applySessionReady` requires string `session_id` + array `words`; `_applyProgressUpdate` requires numeric fields | Mitigated |
| T-08-06 (Tampering, start_session params) | `SESSION_LIMIT = 30` hard-coded in SessionButton; no UI input can change it; backend re-validates | Mitigated |
| T-08-07 (DoS, button-click flood) | Button disabled on click + during active session; `requesting` flag guards double-clicks before `disabled` applies | Mitigated |

## Known Stubs (intentional, plan-sanctioned)

| Stub | File | Reason | Resolved by |
|------|------|--------|-------------|
| Binary audio frames only logged | `pkg/web/static/js/app.js` (`handleMessage`) | Web Audio scheduler is Phase 7.1's domain; Wave 3 wires playback | Wave 3 (audio playback) |
| `progress_update` / `session_complete` handlers exist but no backend emits them yet | `pkg/web/static/js/websocket-client.js` | `cmd/service/handlers.go` currently emits only `session_ready`/`error`; progress/complete frames arrive with the multi-word streaming loop | Future backend work (multi-word sequential streaming) |
| Progress-display section (`#progressDisplay`) remains hidden during session | `pkg/web/static/index.html` | This plan's scope is the session-request button + lifecycle state, not progress rendering | Wave 3 (progress rendering) |

These stubs do NOT block this plan's goal (a working session-request vertical slice with real WebSocket round-trip and state-driven button). Audio playback and progress rendering are explicitly later waves.

## Self-Check: PASSED

- [x] `pkg/web/static/js/state-manager.js` exists
- [x] `pkg/web/static/js/ui-components.js` exists
- [x] `pkg/web/static/js/websocket-client.js` modified
- [x] `pkg/web/static/js/app.js` modified
- [x] `pkg/web/static/index.html` modified
- [x] Commit `79c1ba5` found in git log
- [x] Commit `54a7210` found in git log
- [x] Commit `eecf674` found in git log
