---
status: complete
phase: 08-student-web-interface
source: [08-01-SUMMARY.md, 08-02-SUMMARY.md, 08-03-SUMMARY.md, 08-04-SUMMARY.md, 08-05-SUMMARY.md, 08-06-SUMMARY.md, 08-07-SUMMARY.md]
started: 2026-08-10T02:17:01Z
updated: 2026-09-27T09:00:00Z
---

## Current Test
<!-- OVERWRITE each test - shows where we are -->

[08-07 close-out 2026-09-27 — of the 9 previously blocked iPad-verification tests: 7 passed, 2 deferred; 1 test remains blocked as cross-phase work owned by Phase 11]

Plan 08-07 (final plan of Phase 08) closed gap G-08-4. All 9 previously blocked tests now have final
statuses (see "08-07 UAT Re-execution Results" below). Evidence basis: automated browser-E2E introduced in
commit 599c2a4 (2026-09-27, `npm run e2e` 4/4 green ×3 independent runs, real Mimo TTS audio frames streamed)
plus the 2026-09-24 real-device iPad Safari session (STATE.md stopped_at + .scratch/speech-recognition-redesign/
UAT-CHECKLIST.md). Deviation (user-directed 2026-09-27): the plan's human-verify gates were resolved via
automated-E2E + prior real-device evidence instead of a fresh manual iPad pass. Test 8 (progress display) was
additionally unblocked by 08-06 backend frame emission; test 7 (reconnection/session-resume) remains blocked
and is owned by Phase 11. G-08-3 remains resolved (bc7a5bd).

## Tests

### 1. Static file serving loads the student interface
expected: Start the service from repo root. In a browser, open http://localhost:8080/. The student interface loads (NOT the debug test-interface.html): HTML with the "I want to dictate 30 words" button. CSS (/styles/main.css) and JS (/js/app.js, /js/websocket-client.js) all return 200 with correct MIME types (text/css, text/javascript). Path traversal attempts return 404.
result: pass
reported: "pi-agent: Static files served with correct MIME types, path traversal blocked. NOTE: service had to be restarted first — the running process (PID 10044) was stale (pre-08-05), still serving the debug test-interface.html. config.yaml already had correct staticDir values; purely a stale-process issue."

### 2. WebSocket connection indicator
expected: Open the interface and observe the connection status indicator. It transitions correctly through connecting -> connected states, and the /ws endpoint responds (HTTP 400 to a malformed upgrade request is correct — the handler is live).
result: pass
reported: "pi-agent: WebSocket connection indicator reaches 已连接 (connected)."

### 3. Session request button — DICT-01 label & feedback
expected: The page shows a prominent button labeled "I want to dictate 30 words" (DICT-01). Clicking it shows immediate "Starting session..." feedback, then transitions to "Session in progress..." when the backend responds (session_ready frame).
result: pass
reported: "G-08-3 fixed in bc7a5bd (frontend now sends Phase-2 binary-framed control frames). Live Go probe against http://localhost:8080/ws sent a binary-framed start_session and received: {\"type\":\"session_ready\",\"session_id\":\"bfc53249...\",\"words\":[\"hello\",\"world\",\"water\",\"book\",\"apple\"]}. Full round-trip works; in a browser the button now reaches 'Session in progress...'."
resolved_by: bc7a5bd

### 4. Session button state machine (double-click protection)
expected: Clicking the button disables it immediately to prevent double-clicks. It transitions idle -> starting -> active -> complete, with status text updating in real-time.
result: pass
reported: "pi-agent: Button disabled after click, double-click prevented."

### 5. iPad responsive layout — portrait & landscape
expected: On an iPad (or responsive viewport 768-1024px), the layout adapts to both portrait and landscape orientations. Text remains readable (22px+), spacing is comfortable, and the page does not zoom-on-focus when tapping buttons.
result: pass
Re-executed: 2026-09-27 (automated browser-E2E, viewport-emulated iPad 768×1024 + 1024×768)
evidence: "tests/e2e/ipad-layout.spec.js (commit 599c2a4, `npm run e2e` 4/4 green ×3 independent runs): the iPad media query (min/max-device-width 768..1024) fires in BOTH orientations, and the orientation switch is PROVEN by computed-style discriminators — .progress-container height 24px (portrait) vs 18px (landscape) and .container justify-content normal (portrait) vs flex-start (landscape). Themed styles actually applied: dictation-flow.spec.js asserts the body gradient and the session-CTA gradient via getComputedStyle, not raw HTML presence."
reported: "Supersedes the 2026-08-10 desktop-CSS-only verification. Real-iPad Safari rendering of these media queries was exercised in the 2026-09-24 device session (A1 连接 + A5 批量主路径 passed)."
note: per-orientation screenshots were not captured in either evidence pass — computed-style discriminators from live rendered viewports stand in as evidence (see 08-07 deviation note).

### 6. Touch targets meet minimum size
expected: All interactive elements (buttons) meet at least 44x44px (iOS HIG). The kid-button and session-button styles specify 60px+ / 72px+ minimums. Tappable by a child without precision aiming.
result: pass
Re-executed: 2026-09-27 (automated browser-E2E)
evidence: "tests/e2e/ipad-layout.spec.js (commit 599c2a4): kid-button computed min-height ≥72px and session-button ≥88px in BOTH orientations (portrait 768×1024 and landscape 1024×768); the actually-rendered session CTA boundingBox ≥88px. touch-action: manipulation confirmed on body and session-button (double-tap-zoom hygiene). Supersedes the CSS-only 2026-08-10 reading with computed-style + boundingBox assertions."

### 7. WebSocket reconnection with backoff
expected: If the connection drops, the indicator reflects it and the client reconnects with exponential backoff, never leaving the user guessing about connection state. Session state is preserved across reconnection.
result: blocked
blocked_by: prior-phase
reason: "Re-examined 2026-08-10: STILL blocked. Reconnection code (exponential backoff BASE × 2^attempt, capped) is present in websocket-client.js, but there is NO session resume protocol. Registry is keyed by connection pointer (manager.go:34, handlers.go:256-258); a disconnect cancels the per-connection ctx → run() exits (session.go:297-298) → session removed (manager.go:177-185). Frontend never re-sends start_session after reconnect (only flushes messages queued while disconnected, websocket-client.js:292-296), leaving stale sessionActive:true state. D-06 startSeq resume exists in streamer but spawnStreamWord hardcodes 0 (session.go:377). Cross-phase backend work — no owner phase."
note: 08-07 close-out 2026-09-27: confirmed STILL blocked — NOT part of G-08-4's 9 iPad-verification tests; owner is Phase 11 (Error Handling & Resilience, success criterion #1) per the ROADMAP.md Phase 8.1 note.

### 8. Real-time progress display during a session
expected: During a session, a progress bar fills 0% -> 100%, a word counter advances word-by-word, and status text transitions DICTATING -> FEEDBACK. Encouragement appears at 25/50/75/100% milestones.
result: pass
Re-executed: 2026-09-27 (automated browser-E2E) + 2026-09-24 (real device)
evidence: "tests/e2e/dictation-flow.spec.js (commit 599c2a4): after one click, body[data-session-state] → DICTATING, #progressDisplay becomes visible with 听写进行中 + word counter 已完成 N / M 个词, and the authoritative state snapshot agrees (sessionActive:true, status:DICTATING, totalWords === words.length, currentWordId === words[0]). 2026-09-24 real device: a full batch session reached FEEDBACK — 表单出现 → 提交 → 逐行 ✓/🟡/✗ + 报告 + 语音鼓励 (UAT-CHECKLIST A5 主路径 checked)."
reported: "Unblocked by 08-06: backend now emits progress_update/word_complete/session_complete at FSM transitions (the exact gap cited in the 2026-08-10 blocking note). Milestone encouragement covered by the 2026-09-24 batch full session."

### 9. Audio playback — single-tap unlock (iOS)
expected: On iPad Safari iOS 17+, tapping the session button unlocks audio on the FIRST tap (no second tap needed). AudioContext unlock uses the 3-condition synchronous approach (construct + resume + silent buffer.start(0)). First word latency <1.5s.
result: pass
Re-executed: 2026-09-27 (automated browser-E2E) + 2026-09-24 (real device)
previously blocked by: physical-device
evidence: "tests/e2e/audio-unlock.spec.js (commit 599c2a4) proves the unlock LOGIC: no AudioContext exists before any gesture (lazy D-03 construction, audioCtx === null), then ONE trusted click on the session button → audioCtx.state polls to 'running' and isAudioCtxReady() === true — construct + resume happened inside that single user gesture. Real device 2026-09-24: sessions were started with a single tap and Mimo TTS audio played immediately (暂停恢复冒烟 v5 PASS; A1/A5 checked on iPad Safari)."
note: the <1.5s first-word latency sub-item is NOT covered here — it remains open under UAT-CHECKLIST A2 (掐表). Real iOS Safari gesture semantics remain device-specific; the E2E exercises the identical gesture code path via Playwright's trusted click.

### 10. Audio playback — gapless 20-30 words
expected: During playback, 20-30 words play with no perceptible gaps between words (gapless scheduling via Web Audio API).
result: deferred
Re-executed: 2026-09-24 (real device, partial) — disposition 2026-09-27 (08-07 close-out)
previously blocked by: physical-device
reason: "Deferred, not passed — do not over-claim. The test's premise (a continuous auto-advanced 20-30-word stream) was superseded by the 2026-08-23 user decision: the student-paced model intentionally inserts perceptible inter-word gaps, which were verified audible on real device ('批量模式词间停顿 = 自适应公式 + 双遍播放时长，明显可感'; '收尾有明确过渡（批量→填表交卷；逐词→生成小结），状态主题四色区分'). Within-word playback continuity (Mimo frame streaming, 每词双遍, 暂停恢复) was exercised 2026-09-24. Residual dependency: a dedicated real-iPad audition for clicks/gaps at PCM frame boundaries — folded into UAT-CHECKLIST A5 detail confirmation."

### 11. App-switch recovery
expected: During audio playback, switch to another app and return. Audio resumes automatically OR a tap-to-resume overlay appears.
result: deferred
Re-executed: 2026-09-24 (real device, partial) — disposition 2026-09-27 (08-07 close-out)
previously blocked by: physical-device
reason: "Partial coverage only — do not over-claim. 2026-09-24 real device verified button-driven pause/resume (WS smoke v5 PASS: 词1 complete → pause → paused ack → resume → 恰好一次重播, 7.63s 无挂死无循环) and the four controls (A4 四按钮 已验), but the actual app-switch round-trip was explicitly left pending ('Home 键切走回归子项待测', UAT-CHECKLIST A4). Residual dependency: real iOS Safari visibilitychange audition — switch away during playback, return, and observe auto-resume OR the tap-to-resume overlay appearing and working."

### 12. Touch targets on real device / Production error handling
expected: When something goes wrong (connection timeout, oversized message, invalid state), the interface shows a user-friendly error message (in Chinese) rather than crashing or showing a blank screen. Global error handlers catch uncaught errors.
result: pass
reported: "pi-agent: orca console --json shows zero errors — all 13 messages type:log. After session button click, UI degrades gracefully: button disabled, shows 'Starting session...', no blank screen, no uncaught exceptions across page load, WS connect, button click, and reload cycles. The stuck 'Starting session...' state (G-08-3) is a UX issue but not an uncaught error — JS error-handling paths are clean."
note: touch-target real-device ergonomics folded into blocked tests 9-11

## Summary

total: 12
passed: 9
issues: 0
pending: 0
skipped: 0
deferred: 2
blocked: 1

## 08-07 UAT Re-execution Results

Plan 08-07 (final plan of Phase 08) closed gap G-08-4: the 9 UAT tests that were BLOCKED during Phase 08
execution all have final statuses as of 2026-09-27.

Numbering note: the 08-07 plan references the blocked tests by an earlier doc numbering (its Tests
3/5/6/7/11/12 were the configuration-issue group; 8/9/10 the physical-device group). The table below
cross-references each to the current doc entry.

**Evidence sources:**
- **Automated browser-E2E (2026-09-27):** Playwright layer introduced in commit 599c2a4 ("test(e2e): add
  Playwright browser-E2E layer for student UI"); `npm run e2e` 4/4 green ×3 independent runs with real Mimo
  TTS audio frames streamed (lastSeq > 0). Specs: `tests/e2e/ipad-layout.spec.js`,
  `tests/e2e/audio-unlock.spec.js`, `tests/e2e/dictation-flow.spec.js`.
- **Real-device iPad Safari (2026-09-24):** per STATE.md stopped_at — "Mimo TTS 真接线/暂停恢复冒烟 v5
  PASS/逐词沉默不再自动推进/批量词间间隔/收尾过渡均验证；UAT-CHECKLIST 勾 A1、A4(四按钮)、A5(主路径)" —
  and `.scratch/speech-recognition-redesign/UAT-CHECKLIST.md` 2026-09-24 验证记录.

| 08-07 Plan Test | Doc entry | Test | Final status | Evidence basis | Re-executed |
|-----------------|-----------|------|--------------|----------------|-------------|
| 3 | #5 | iPad Safari responsive design | passed | E2E ipad-layout.spec.js: iPad MQ (768..1024 device-width) fires at both orientations; CSS applied (body gradient, themed CTA per dictation-flow.spec.js computed styles) | 2026-09-27 |
| 5 | #5 | Portrait orientation layout | passed | E2E ipad-layout.spec.js portrait 768×1024: `.progress-container` 24px, container justify-content normal, targets ≥72/≥88px | 2026-09-27 |
| 6 | #5 | Landscape orientation layout | passed | E2E ipad-layout.spec.js landscape 1024×768 — orientation switch PROVEN: `.progress-container` height 24px (portrait) vs 18px (landscape) + container justify-content normal vs flex-start | 2026-09-27 |
| 7 | #6 | Touch target sizes | passed | E2E ipad-layout.spec.js: kid-button ≥72px, session-button ≥88px (computed min-height + rendered boundingBox ≥88px), both orientations | 2026-09-27 |
| 8 | #9 | iOS 17+ single-tap unlock | passed | E2E audio-unlock.spec.js unlock LOGIC (lazy AudioContext null before click; single trusted click → state 'running' + isAudioCtxReady() true) + 2026-09-24 real-device single-tap session start with audible Mimo TTS. <1.5s latency sub-item open under UAT-CHECKLIST A2 | 2026-09-27 |
| 9 | #11 | App-switch recovery | deferred | Partial: 2026-09-24 pause/resume verified (WS smoke v5 PASS, A4 四按钮) but Home-key app-switch round-trip explicitly 待测 (A4 sub-item). Residual: real iOS Safari visibilitychange audition (auto-resume or tap-to-resume overlay) | 2026-09-24 (partial) |
| 10 | #10 | Audio plays gaplessly (20-30 words) | deferred | Premise superseded by 2026-08-23 student-paced redesign — inter-word gaps now intentional and verified audible ("批量模式词间停顿 = 自适应公式 + 双遍播放时长，明显可感"; 收尾过渡四色区分). Within-word frame/double-play continuity exercised 2026-09-24. Residual: real-iPad frame-boundary gapless audition (UAT-CHECKLIST A5 details) | 2026-09-24 (partial) |
| 11 | #5 | No zoom-on-focus | passed | E2E ipad-layout.spec.js: `touch-action: manipulation` (body + session-button) and `visualViewport.scale === 1` before/after input focus | 2026-09-27 |
| 12 | #2 | Connection indicator states | passed | E2E dictation-flow.spec.js END-STATE: `#connectionStatus[data-state]` → `connected`, `.status-dot` rgb(16, 185, 129), text 已连接; plus minimum end-to-end flow (open → WS connected → start → DICTATING → progress UI → AudioContext running). Upgrades the original 2026-08-10 live pass | 2026-09-27 |

**Result: 7 passed, 2 deferred, 0 failed.**

### Overall iPad-compatibility assessment

The student interface is iPad-Safari-compatible to the extent assertable without a device in hand: every
layout/touch/connection concern in the original 9 is covered by a green automated assertion at the real iPad
viewports (both orientations, computed-style discriminators proving the media queries actually switch layout,
88px primary CTA, no zoom-on-focus), and the 2026-09-24 real-device session exercised the full student flow
end-to-end — Mimo TTS 真接线、单点开始出声、暂停恢复、逐词沉默引导、批量词间节奏、收尾过渡 — with the
user confirming 「符合描述」. No rendering defect, touch-target deficiency, or zoom-on-focus behavior was
reported on either evidence source. The two deferred items are narrow, device-gesture-specific auditions, not
compatibility failures; no code defect is suspected (the handlers are implemented and the button-driven
siblings of both behaviors passed on device).

### Remaining dependencies

Not blocking Phase 08 completion; carried by the standing UAT-CHECKLIST
(`.scratch/speech-recognition-redesign/UAT-CHECKLIST.md`) unless a verifier pulls them forward:

1. **App-switch round-trip (deferred, doc #11 / plan Test 9):** real iOS Safari visibilitychange audition —
   Home-key switch away during playback → return → auto-resume or tap-to-resume overlay (UAT-CHECKLIST A4
   sub-item, explicitly 待测).
2. **Frame-boundary gapless audition (deferred, doc #10 / plan Test 10):** real-iPad listen for clicks/gaps
   at PCM frame boundaries; the original continuous-stream premise is design-superseded (UAT-CHECKLIST A5
   detail confirmation).
3. **First-word latency <1.5s (doc #9 sub-item):** 掐表 measurement on device (UAT-CHECKLIST A2).
4. **Cross-phase (outside the 9):** doc #7 reconnection/session-resume — owned by Phase 11; doc #8's
   milestone cadence beyond the verified batch full session if finer per-word progression auditing is ever
   wanted.
5. **Broader device/UAT matrix (outside the 9):** A3 出声作答路径、A6-A8、B 儿童段、C4 中文词表、D1/D2+F(VPS).

### Phase 08 completion recommendation

**Recommend Phase 08 as complete.** All executable verification for the phase's own scope is done: the 9
G-08-4 tests are closed (7 passed, 2 deferred with named residual dependencies), no failed tests remain,
and the two remaining doc-level items are external prerequisites (Phase 11 owns reconnection resume) or
standing post-launch checklist items. Suggest the verifier confirm phase completion (the ROADMAP phase
checkbox remains gated on verification status by design); the deferred items should be re-stated verbatim in
the phase verification report so the next iPad session can close A2/A4-sub-item/A5-details in one pass.

## Gaps

- gap_id: G-08-3
  truth: "Clicking the session button transitions to 'Session in progress...' when the backend responds (session_ready frame)"
  status: resolved
  resolved_by: bc7a5bd
  resolved_at: 2026-08-10
  reason: "pi-agent verification: Button label correct, immediate 'Starting session...' feedback works, but the button never reached 'Session in progress...'. Server log: 'non-binary ws message ignored', msg_type:1. The start_session message was silently dropped at the server boundary."
  severity: blocker
  test: 3
  root_cause: "WebSocket text-vs-binary protocol mismatch. Frontend pkg/web/static/js/websocket-client.js sends JSON as WebSocket TEXT frames (ws.send(JSON.stringify(...))). Backend pkg/ws/connection.go only handles websocket.BinaryMessage and silently ignores TextMessage (gorilla msg_type:1 = TextMessage). No message ever reaches the session handler, so the entire student session flow (DICT-01) is dead."
  artifacts:
    - path: "pkg/web/static/js/websocket-client.js"
      issue: "sends client messages (start_session, etc.) as JSON text frames"
    - path: "pkg/ws/connection.go"
      issue: "read loop only accepts BinaryMessage; TextMessage is logged-and-dropped ('non-binary ws message ignored')"
  missing:
    - "Decide the wire contract: either frontend sends binary (wrap JSON in Uint8Array) OR backend accepts text frames alongside binary. Frontend change is the smaller, self-contained fix and matches the binary-frame protocol designed in Phase 2 (WS-02)."
    - "Add an integration test that sends a start_session control frame and asserts the backend receives it (prevents silent regression)."

## Blocked Items (not code gaps — external prerequisites)

- test 7: blocked_by prior-phase — confirmed STILL blocked at 08-07 close-out (2026-09-27). Reconnection code (exponential backoff) is present in websocket-client.js, but there is NO session resume protocol (registry keyed by conn pointer; disconnect cancels per-connection ctx → session removed; frontend never re-sends start_session after reconnect). Cross-phase backend work — owner: Phase 11 (Error Handling & Resilience, success criterion #1) per the ROADMAP.md Phase 8.1 note. NOT part of G-08-4's 9 iPad tests.
- test 8 (formerly blocked_by prior-phase): unblocked and passed — 08-06 delivered the missing backend progress_update/word_complete/session_complete emission; verified by E2E progress assertions (2026-09-27) and the 2026-09-24 real-device batch full session reaching FEEDBACK with encouragement audio.
- tests 9, 10, 11 (formerly blocked_by physical-device): closed by plan 08-07 (2026-09-27) — #9 passed (E2E unlock logic + 2026-09-24 real-device single-tap session start), #10/#11 deferred with named residual real-device dependencies (see "08-07 UAT Re-execution Results" above). All frontend code (AudioController, gapless scheduling, app-switch recovery, 60px+ touch targets) is present and was exercised by the evidence passes; no code defect open.
