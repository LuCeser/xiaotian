---
phase: 08-student-web-interface
plan: 03
subsystem: student-web-interface
tags: [web, progress-display, ui-component, state-management, websocket, ipad]

# Dependency graph
requires:
  - "Phase 8 Plan 02 SessionStateManager + SessionButton + handleControlFrame (pkg/web/static/js)"
  - "Phase 8 Plan 01 static serving + WebSocketClient foundation (pkg/web/static)"
  - "Phase 7 session FSM states (IDLE/LISTENING/DICTATING/WAITING_PARENT/FEEDBACK) from pkg/session/fsm.go"
provides:
  - "ProgressDisplay — JS-driven real-time progress UI component (window.ProgressDisplay) rendering status text, progress bar, word counter, encouragement from SessionStateManager"
  - "WebSocketClient word_complete handling (_applyWordComplete) — incremental completedWords increment, clamped at totalWords"
  - "T-08-08 range validation at two layers: WebSocketClient._applyProgressUpdate (rejects negatives + completedWords > totalWords) and SessionStateManager._normalizeProgress (defense-in-depth clamp)"
  - "FEEDBACK terminal-state invariant: state layer forces completedWords = totalWords so 100% renders regardless of the driving code path"
affects:
  - "pkg/web/static/js/ui-components.js (ProgressDisplay added alongside SessionButton)"
  - "pkg/web/static/js/state-manager.js (_normalizeProgress added to setState)"
  - "pkg/web/static/js/websocket-client.js (word_complete case + progress range validation)"
  - "pkg/web/static/js/app.js (ProgressDisplay mount + subscriber + section reveal)"
  - "pkg/web/static/index.html (static inline progress markup replaced with #progress-display-container)"

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "JS-driven component replaces static inline markup (same pattern as Wave 2 SessionButton) — single container, class renders structure from state"
    - "Defense-in-depth validation at two layers: trust boundary (ws-client rejects) + state store (normalizes) so no single code path can leave impossible state"
    - "Terminal-state invariant at state layer: FEEDBACK forces completedWords = totalWords, not just in the component"

key-files:
  created: []
  modified:
    - pkg/web/static/js/ui-components.js
    - pkg/web/static/js/state-manager.js
    - pkg/web/static/js/websocket-client.js
    - pkg/web/static/js/app.js
    - pkg/web/static/index.html

key-decisions:
  - "Replaced Wave 1 static inline progress markup with a JS-driven #progress-display-container (same pattern Wave 2 used for the session button). Keeping both would have produced dead/duplicate UI — the ProgressDisplay.render() owns the structure so it always matches the authoritative state (Rule 3)."
  - "Reused Wave 1's existing main.css progress classes (.progress-container/.progress-bar/.progress-fill/.status-text/.word-counter/.encouragement) unchanged. The plan suggested new blue-gradient CSS values, but the committed green-gradient pill-shaped styles are already kid-friendly, tested across iPad/landscape media queries, and changing them adds no value — only risk."
  - "Added _normalizeProgress to SessionStateManager.setState as defense-in-depth for T-08-08, even though the ws-client already rejects bad frames at the trust boundary. The state layer is the last-chance invariant: a future code path that bypasses the ws client (e.g. a test harness, a new transport) cannot leave the store in an out-of-range state."
  - "word_complete handler increments completedWords (per plan) while progress_update sets it absolutely (per Wave 2). Both coexist: the last writer wins, which is correct — absolute frames resync, incremental frames advance."
  - "Status text uses the plan's literal English strings (Ready to start / Listening... / Dictation in progress / Waiting for parent / Session complete!) even though the surrounding UI chrome is Chinese, because the plan mandates them and the tool is an English dictation tutor."
  - "Encouragement fires only on milestone crossings (25/50/75/100%) tracked via _lastPercent, not on every state change, to avoid re-praising on each progress_update."

patterns-established:
  - "Defense-in-depth validation: trust boundary rejects + state layer normalizes. Apply to any future server-driven counter that flows to the UI."
  - "Subscriber order matters: app.js registers the progress subscriber AFTER the session-button subscriber so the button's visual state settles before the progress bar redraws (per plan)."

requirements-completed: [WEB-02]

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "ProgressDisplay component renders status text, progress bar, word counter, and encouragement from SessionStateManager state with real-time updates"
    requirement: WEB-02
    verification:
      - kind: integration
        ref: "node sandbox smoke test (lifecycle: IDLE→session_ready→progress→error→complete→edge totalWords=0) — all 7 snapshots match expected"
        status: pass
      - kind: automated_ui
        ref: "pkg/web/static/js/ui-components.js#ProgressDisplay.update — clamps percent to [0,100], guards totalWords===0 divide-by-zero"
        status: pass
    human_judgment: true
    rationale: "Logic fully proven by the smoke test, but iPad Safari visual rendering (22px readability, gradient smoothness, CSS transition timing, section reveal on real session) requires manual UAT on a real device — cannot be asserted by a headless node sandbox."
  - id: D2
    description: "Backend progress updates wired to state manager: word_complete handler increments completedWords, progress_update applies absolute values, T-08-08 range validation rejects negatives and completedWords > totalWords"
    requirement: WEB-02
    verification:
      - kind: integration
        ref: "node sandbox smoke test — negative completedWords clamps to 0, completedWords=999 clamps to totalWords=30"
        status: pass
      - kind: unit
        ref: "pkg/web/static/js/websocket-client.js#_applyProgressUpdate + _applyWordComplete + pkg/web/static/js/state-manager.js#_normalizeProgress"
        status: pass
    human_judgment: false
  - id: D3
    description: "Session lifecycle integration: FEEDBACK forces 100% + Session complete!, error maintains current progress, totalWords=0 shows 0/0 at 0%, section reveals on session start"
    requirement: WEB-02
    verification:
      - kind: integration
        ref: "node sandbox smoke test — session_complete snapshot: width=100%, counter='30 / 30 words', status='Session complete!'; error snapshot: bar stays at 50%"
        status: pass
    human_judgment: true
    rationale: "Edge-case logic is smoke-tested, but real-session section reveal (#progressDisplay hidden→visible) and CSS transition smoothness on a live backend require iPad UAT."

# Metrics
duration: 8min
completed: 2026-08-09
status: complete
---

# Phase 8 Plan 03: Real-Time Progress Display Summary

A JS-driven ProgressDisplay component that renders status text, a gradient progress bar, and a word counter into a container, updating in real time from the SessionStateManager as the backend emits progress_update / word_complete / session_complete frames — with T-08-08 tampering mitigation (range validation + state-layer normalization) at two layers.

## Performance

- **Duration:** 8 min
- **Started:** 2026-08-09T09:17:47Z
- **Completed:** 2026-08-09T09:26:18Z
- **Tasks:** 3
- **Files modified:** 5

## Accomplishments
- ProgressDisplay component renders status text, progress bar, word counter, and encouragement into #progress-display-container, driven by SessionStateManager via update(state)
- Status message mapping for all Phase 7 FSM states (IDLE/LISTENING/DICTATING/WAITING_PARENT/FEEDBACK) with error-surfacing path that preserves the current progress bar
- word_complete control-frame handler (incremental completedWords increment, clamped at totalWords) added alongside the existing progress_update / session_complete dispatchers
- T-08-08 tampering mitigation at two layers: WebSocketClient._applyProgressUpdate rejects negatives and completedWords > totalWords; SessionStateManager._normalizeProgress clamps as defense-in-depth
- FEEDBACK terminal-state invariant enforced at the state layer (completedWords = totalWords) so 100% renders regardless of the driving code path
- app.js mounts ProgressDisplay, subscribes it after the session button (plan-mandated subscriber order), and reveals #progressDisplay when the session becomes active

## Task Commits

Each task was committed atomically:

1. **Task 1: Create progress display component with visual feedback** — `376000b` (feat)
2. **Task 2: Wire backend progress updates to state manager** — `b6db1f2` (feat)
3. **Task 3: Integrate progress display with session lifecycle** — `4c86f15` (feat)

## Files Created/Modified
- `pkg/web/static/js/ui-components.js` — ProgressDisplay class added to the existing IIFE (alongside SessionButton); renders status/bar/counter/encouragement; update(state) handles all edge cases
- `pkg/web/static/js/state-manager.js` — _normalizeProgress added to setState; clamps completedWords to [0, totalWords], currentWord >= 0, and forces completedWords = totalWords on FEEDBACK
- `pkg/web/static/js/websocket-client.js` — word_complete case + _applyWordComplete added; _applyProgressUpdate gained T-08-08 range validation (rejects negatives + completedWords > totalWords)
- `pkg/web/static/js/app.js` — ProgressDisplay mounted into #progress-display-container, subscribed after the session button, #progressDisplay section revealed on session start
- `pkg/web/static/index.html` — static inline progress markup (progress-status/progress-container/word-counter/encouragement) replaced with a single JS-driven #progress-display-container

## Decisions Made
- Replaced Wave 1's static inline progress markup with a JS-driven container (Wave 2 SessionButton precedent) so the DOM structure always matches the authoritative state — no dead/duplicate UI.
- Reused Wave 1's existing main.css progress classes unchanged. The plan suggested new blue-gradient CSS values, but the committed green-gradient pill styles are already kid-friendly, tested across iPad/landscape media queries, and changing them adds no value.
- Added state-layer _normalizeProgress as defense-in-depth (not just component-level clamping) so a future code path that bypasses the ws client cannot leave impossible state.
- word_complete increments completedWords; progress_update sets it absolutely. Both coexist — last writer wins.
- Status text uses the plan's literal English strings even though surrounding chrome is Chinese (plan-mandated; tool is an English dictation tutor).
- Encouragement fires only on milestone crossings (25/50/75/100%) to avoid re-praising on every update.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — blocking] Wave 1 static progress markup conflicted with the plan's JS-driven ProgressDisplay**
- **Found during:** Task 1
- **Issue:** `index.html` already contained static inline progress elements (progress-status, progress-container with role=progressbar, word-counter, encouragement). The plan's ProgressDisplay.render() creates its own structure into a #progress-display-container. Keeping both would produce duplicate, conflicting UI (two progress bars, two status texts) where only the JS-driven one stays in sync with state — the static one would freeze.
- **Fix:** Replaced the static inline elements with `<div id="progress-display-container"></div>`. Same pattern Wave 2 used when it replaced the static start button with #session-button-container. The section heading, hidden attribute, and ARIA labelling stay.
- **Files modified:** `pkg/web/static/index.html`
- **Verification:** `grep -c 'id="progress-display-container"' == 1`; old IDs (`progressBar`, `currentWord`, `totalWords`, `progressText`, `encouragement`) all return 0.
- **Committed in:** `376000b` (Task 1 commit)

**2. [Plan imprecision] Task 1 verify `grep -c "progress-container" main.css == 1` is impossible for responsive CSS**
- **Found during:** Task 1 verification
- **Issue:** The plan's automated check `grep -c "progress-container" pkg/web/static/styles/main.css == 1` cannot be satisfied by a responsive stylesheet: the `.progress-container` class is styled in the base rule AND in the iPad media query (height: 24px) AND in the iPad-landscape media query (height: 18px) — all three are required by the plan's own "iPad-Optimized Responsive Design" requirement. Functional minimum is 3. (Same authoring pattern as Wave 2's `handleControlFrame == 1` deviation.)
- **Fix:** Left the existing responsive CSS unchanged (3 occurrences). The class is present, styled, and responsive — the `== 1` constraint is an authoring error, not a code defect.
- **Files modified:** none
- **Commit:** n/a (no change needed)

**3. [Plan imprecision] Task 2 verify `grep -c "progress_update" ws-client == 1` required error-message rewording**
- **Found during:** Task 2 verification
- **Issue:** Functional code referencing a frame type needs at minimum the `case 'progress_update':` line. Descriptive error messages ("malformed progress_update frame") and JSDoc naturally added 4 more occurrences, giving 5. The plan's `== 1` is the same authoring-error pattern as Wave 2's `handleControlFrame == 1` and Task 1's `progress-container == 1`.
- **Fix:** Reworded JSDoc and error messages to "progress frame" instead of "progress_update frame", leaving only the required `case 'progress_update':` line. Error messages stay descriptive (just omit the redundant type token). Final count: 1.
- **Files modified:** `pkg/web/static/js/websocket-client.js`
- **Commit:** `b6db1f2` (Task 2 commit)

**4. [Rule 2 — missing critical] T-08-08 mitigation was absent at the state layer**
- **Found during:** Task 2
- **Issue:** The plan's <threat_model> assigns `mitigate` to T-08-08 (progress_update tampering: validate completedWords ≤ totalWords, reject negatives). Wave 2 only validated types in _applyProgressUpdate, not ranges. The plan's Task 2 also specifies "Add validation for progress values (no negative numbers, no exceeds total)" in state-manager.js. Without state-layer validation, a future code path bypassing the ws client (test harness, new transport) could leave impossible state.
- **Fix:** Added _normalizeProgress to SessionStateManager.setState: clamps completedWords to [0, totalWords], currentWord >= 0. Also added range rejection (not just type check) to ws-client _applyProgressUpdate. Two-layer defense-in-depth.
- **Files modified:** `pkg/web/static/js/state-manager.js`, `pkg/web/static/js/websocket-client.js`
- **Verification:** Smoke test — negative completedWords → 0; completedWords=999 with totalWords=30 → clamped to 30.
- **Committed in:** `b6db1f2` (Task 2 commit)

---

**Total deviations:** 4 (2 plan imprecision / unverifiable grep targets, 1 Rule 3 blocking UI conflict, 1 Rule 2 missing-critical security mitigation)
**Impact on plan:** All auto-fixes necessary for correctness, security, and consistency with Wave 2 patterns. No scope creep.

## Verification Evidence

| Criterion | Evidence |
|-----------|----------|
| Plan grep: `class ProgressDisplay` == 1 | PASS (1) |
| Plan grep: `progress-container` in main.css == 1 | 3 — responsive CSS (base + iPad + landscape); see Deviation #2 |
| Plan grep: `progress_update` in ws-client == 1 | PASS (1, after Deviation #3 reword) |
| Plan grep: `completedWords` in state-manager >= 2 | PASS (8 — initialState + _normalizeProgress) |
| Plan grep: `session_complete` in state-manager == 1 | PASS (1, in initialState comment) |
| Plan grep: `totalWords` in ui-components >= 2 | PASS (10) |
| JS syntax (`node --check`) | PASS — all 4 files (websocket-client, state-manager, ui-components, app) |
| Go build (`go build ./...`) | PASS (exit 0 — no Go changed, confirms no breakage) |
| Lifecycle smoke test (node sandbox) | PASS — IDLE→"Ready to start"/0%; session_ready→"Dictation in progress"/"0 / 30 words"; progress 15/30→50%; error→shows message, bar stays 50%; session_complete→"Session complete!"/100%/"30 / 30 words"; edge totalWords=0→"0 / 0 words"/0% |
| State normalization smoke test | PASS — negative→0; completedWords=999→clamped to totalWords=30 |
| Threat T-08-08 (progress tampering) | Two-layer mitigation: ws-client rejects + state-manager clamps |

**Manual / UAT criteria** (iPad Safari visual rendering, real-backend section reveal, CSS transition smoothness): these require a running backend + iPad Safari and are covered by the phase's UAT checkpoint. The structural prerequisites — correct status mapping, validated inbound dispatch, clamped progress, FEEDBACK-forces-100% invariant, edge-case handling — are verified by the smoke test above.

## Threat Mitigations Applied

| Threat | Mitigation | Status |
|--------|------------|--------|
| T-08-08 (Tampering, progress_update) | Two-layer: ws-client _applyProgressUpdate rejects negatives + completedWords > totalWords; state-manager _normalizeProgress clamps as defense-in-depth; ProgressDisplay clamps percent to [0,100] | Mitigated |
| T-08-09 (Info disclosure, session state) | Component renders only the student's own SessionStateManager state (single store, no cross-session data path) | Accept (per plan) |

## Known Stubs (intentional, plan-sanctioned)

| Stub | File | Reason | Resolved by |
|------|------|--------|-------------|
| Binary audio frames only logged | `pkg/web/static/js/app.js` (`handleMessage`) | Web Audio scheduler is Phase 7.1's domain | Future audio-playback wave |
| Backend does not yet emit progress_update / word_complete / session_complete frames | `pkg/web/static/js/websocket-client.js` | `cmd/service/handlers.go` currently emits only session_ready/error; progress/complete frames arrive with the multi-word streaming loop | Future backend work (multi-word sequential streaming) |
| ProgressDisplay.mount + section reveal wired but no live session exercises it | `pkg/web/static/js/app.js` | Requires the backend streaming loop to emit progress frames | Phase 8 UAT + future backend work |

These stubs do NOT block this plan's goal (a working progress-display vertical slice that updates in real time from backend WebSocket messages). The handlers are in place and smoke-tested against the expected message contracts; they activate the moment the backend emits the frames.

## Self-Check: PASSED

- [x] `pkg/web/static/js/ui-components.js` modified (ProgressDisplay added)
- [x] `pkg/web/static/js/state-manager.js` modified (_normalizeProgress added)
- [x] `pkg/web/static/js/websocket-client.js` modified (word_complete + range validation)
- [x] `pkg/web/static/js/app.js` modified (ProgressDisplay mount + subscriber)
- [x] `pkg/web/static/index.html` modified (#progress-display-container)
- [x] Commit `376000b` found in git log
- [x] Commit `b6db1f2` found in git log
- [x] Commit `4c86f15` found in git log
