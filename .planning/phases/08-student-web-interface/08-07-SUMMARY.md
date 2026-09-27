---
phase: 08-student-web-interface
plan: 07
subsystem: student-web-interface
tags: [uat, ipad-safari, e2e, verification-closeout, manual-verification]
requires:
  - "Phase 8 Plan 05: static file serving fix (unblocked the 6 configuration-issue tests)"
  - "Phase 8 Plan 06: backend progress_update/word_complete/session_complete emission (unblocked progress display)"
  - "Commit 599c2a4 (2026-09-27): Playwright browser-E2E layer (`npm run e2e`, 4/4 green ×3 independent runs)"
  - "2026-09-24 real-device iPad Safari UAT session (STATE.md stopped_at + .scratch/speech-recognition-redesign/UAT-CHECKLIST.md)"
provides:
  - "Final UAT statuses for the 9 previously blocked iPad-verification tests (7 passed, 2 deferred, 0 failed)"
  - "G-08-4 close-out: 08-UAT.md re-execution results + overall iPad compatibility assessment"
  - "Named residual real-device dependency list handed off to the standing UAT-CHECKLIST"
affects:
  - ".planning/phases/08-student-web-interface/08-UAT.md (final statuses + 08-07 UAT Re-execution Results section)"
tech-stack:
  added: []
  patterns:
    - "Automated browser-E2E as human-verify gate evidence: trusted-click gesture path (audio unlock) and computed-style media-query discriminators (iPad layouts)"
key-files:
  created:
    - .planning/phases/08-student-web-interface/08-07-SUMMARY.md
  modified:
    - .planning/phases/08-student-web-interface/08-UAT.md
decisions:
  - "Deviation (user-directed 2026-09-27): Tasks 1-2 human-verify gates resolved via automated browser-E2E (599c2a4, 4/4 green ×3 runs) + prior 2026-09-24 real-device evidence instead of a fresh manual iPad pass"
  - "Test 9 (app-switch recovery) deferred, not passed: button pause/resume verified on device but the Home-key/visibilitychange round-trip was explicitly left pending (UAT-CHECKLIST A4 sub-item)"
  - "Test 10 (gapless) deferred: the continuous auto-advance stream premise was superseded by the 2026-08-23 student-paced redesign; inter-word gaps are now intentional and verified audible on device"
  - "Test 8 (single-tap unlock) passed on unlock logic + real-device session start; the <1.5s first-word latency sub-item is explicitly carved out to UAT-CHECKLIST A2 (掐表)"
metrics:
  duration: documentation close-out
  completed: 2026-09-27
  tasks: 3
  files: 2
status: complete
---

# Phase 8 Plan 07: iPad Safari Manual UAT — Documentation Close-out Summary

Gap G-08-4 close-out: Phase 08 UAT had 9 of 12 tests BLOCKED (6 by the static-serving configuration issue
fixed in 08-05, 3 by physical-device requirements). All 9 now have final statuses. **Result: 7 passed,
2 deferred, 0 failed.** No `blocked_by: configuration-issue` item remains anywhere in 08-UAT.md.

## Deviation from plan (user-directed 2026-09-27)

Tasks 1 and 2 were typed `checkpoint:human-verify` with a blocking gate, expecting a fresh manual iPad pass.
They were instead resolved with two evidence sources, per user direction:

1. **Automated browser-E2E (new, 2026-09-27):** commit 599c2a4 added a Playwright layer
   (`tests/e2e/ipad-layout.spec.js`, `tests/e2e/audio-unlock.spec.js`, `tests/e2e/dictation-flow.spec.js`);
   `npm run e2e` ran 4/4 green ×3 independent runs with real Mimo TTS audio frames streamed. Playwright
   drives real rendered viewports at the iPad sizes and dispatches trusted user gestures, so layout and
   unlock-logic assertions run against the same DOM/CSS/gesture code paths a real finger would.
2. **Prior real-device session (2026-09-24):** iPad Safari session recorded in STATE.md stopped_at and
   `.scratch/speech-recognition-redesign/UAT-CHECKLIST.md` (A1, A4 四按钮, A5 主路径 checked; 暂停恢复冒烟
   v5 PASS; Mimo TTS 真接线).

**Consequences, stated honestly:** no fresh screenshots were captured (the plan asked for portrait/landscape
screenshot evidence) — computed-style E2E discriminators and the 2026-09-24 device session stand in as
evidence. Two tests are deferred rather than passed because neither source genuinely covered their intent
(see below). No test was over-claimed.

## UAT re-execution results (all 9)

| 08-07 Plan Test | 08-UAT entry | Test | Final status | Evidence basis | Re-executed |
|-----------------|--------------|------|--------------|----------------|-------------|
| 3 | #5 | iPad Safari responsive design | passed | E2E ipad-layout.spec.js: iPad MQ (768..1024 device-width) fires; CSS applied (body gradient, themed CTA — computed styles, not raw HTML) | 2026-09-27 |
| 5 | #5 | Portrait orientation layout | passed | E2E portrait 768×1024: `.progress-container` 24px, justify-content normal, kid ≥72px / session ≥88px targets | 2026-09-27 |
| 6 | #5 | Landscape orientation layout | passed | E2E landscape 1024×768 — orientation switch PROVEN: `.progress-container` height 24px (portrait) vs 18px (landscape) + container justify-content `normal` vs `flex-start` | 2026-09-27 |
| 7 | #6 | Touch target sizes | passed | E2E: kid-button ≥72px, session-button ≥88px computed min-height + rendered `boundingBox` ≥88px, both orientations | 2026-09-27 |
| 8 | #9 | iOS 17+ single-tap unlock | passed | E2E audio-unlock.spec.js unlock LOGIC: AudioContext absent before click (lazy D-03 construct), ONE trusted click → `state === 'running'` + `isAudioCtxReady() === true`; plus 2026-09-24 real-device single-tap session start with audible Mimo TTS. <1.5s latency sub-item open (UAT-CHECKLIST A2) | 2026-09-27 |
| 9 | #11 | App-switch recovery | **deferred** | Partial: 2026-09-24 pause/resume verified (WS smoke v5 PASS — pause → paused ack → resume → 恰好一次重播; A4 四按钮) but Home-key switch-away round-trip explicitly 待测 (A4 sub-item). Residual: real iOS Safari visibilitychange audition (auto-resume or tap-to-resume overlay) | 2026-09-24 (partial) |
| 10 | #10 | Audio plays gaplessly (20-30 words) | **deferred** | Premise superseded by the 2026-08-23 student-paced redesign — inter-word gaps now intentional and verified audible ("批量模式词间停顿 = 自适应公式 + 双遍播放时长，明显可感"; 收尾过渡四色区分); within-word frame/双遍 continuity exercised 2026-09-24. Residual: real-iPad frame-boundary gapless audition (UAT-CHECKLIST A5 details) | 2026-09-24 (partial) |
| 11 | #5 | No zoom-on-focus | passed | E2E: `touch-action: manipulation` (body + session-button) + `visualViewport.scale === 1` before/after input focus | 2026-09-27 |
| 12 | #2 | Connection indicator states | passed | E2E dictation-flow.spec.js END-STATE: `#connectionStatus[data-state]` → `connected`, `.status-dot` `rgb(16, 185, 129)`, text 已连接; plus minimum end-to-end flow (open → WS connected → start → DICTATING → progress UI → AudioContext running). Upgrades the original 2026-08-10 live pass | 2026-09-27 |

Bonus close-out recorded in 08-UAT.md: doc test #8 (real-time progress display — formerly blocked by
prior-phase) was unblocked by 08-06's backend frame emission and is now **passed** (E2E progress assertions
2026-09-27 + 2026-09-24 real-device batch full session reaching FEEDBACK with encouragement audio). Doc test
#7 (reconnection/session-resume) remains **blocked** as cross-phase work owned by Phase 11.

## iOS-specific behavior verification results

- **Single-tap unlock (iOS 17+):** the D-03 3-condition unlock (construct + resume inside one gesture) is
  proven at the logic level by a trusted click; real-device sessions on 2026-09-24 started with one tap and
  played Mimo TTS immediately. True iOS Safari gesture-queue semantics remain device-specific — noted, not
  asserted.
- **App-switch recovery:** `handleVisibilityChange` + tap-to-resume overlay are implemented; only the
  button-driven sibling (pause/resume) was verified on device — the visibilitychange-triggered path awaits a
  real Home-key round-trip (deferred).
- **Audio continuity:** per-word double-play, adaptive inter-word pacing, and finishing transitions verified
  on device; frame-boundary click/gap audition deferred (and the original gapless continuous-stream premise
  is design-superseded).
- **Touch ergonomics:** 72px kid / 88px session targets asserted in both orientations at computed and
  rendered-box level; `touch-action: manipulation` guards double-tap zoom.

## iPad Safari compatibility confirmation

Confirmed on both evidence sources: media-query layout genuinely switches between portrait and landscape
(computed-style discriminators, not just "page renders"), the primary CTA renders at ≥88px, focus never
scales the viewport, the connection indicator reaches its themed connected state, and a full student session
(open → connect → start → DICTATING → progress → FEEDBACK) runs end-to-end — the last proven on the real
device on 2026-09-24 (user: 「符合描述」). Screenshot caveat: no image artifacts were captured in either
evidence pass (see Deviation); the E2E asserts computed styles on live rendered viewports instead.

## Remaining dependencies (none block Phase 08)

Carried by `.scratch/speech-recognition-redesign/UAT-CHECKLIST.md`:

1. Real iOS Safari visibilitychange / Home-key app-switch round-trip (A4 sub-item — deferred doc #11).
2. Real-iPad frame-boundary gapless audition (A5 details — deferred doc #10).
3. First-word latency <1.5s 掐表 (A2 — doc #9 sub-item).
4. Cross-phase: reconnection/session-resume (doc #7) — owned by Phase 11 per ROADMAP.
5. Broader matrix: A3 出声作答路径、A6-A8、B 儿童段、C4 中文词表、D1/D2+F(VPS).

## Phase 08 completion recommendation

**Recommend Phase 08 as complete.** Every verification item executable without additional backend work is
closed: G-08-4's 9 tests all have final statuses (7 passed / 2 deferred / 0 failed), G-08-3 was resolved
(bc7a5bd), and the doc-level regression count is 9 passed / 2 deferred / 1 blocked (the blocked item owned
by Phase 11). The deferred items are narrow real-device auditions with named owners in the standing
checklist, not compatibility failures; no code defect is open in the phase. Suggest the verifier confirm
phase completion (the ROADMAP checkbox is verification-gated by design) and restate the three residual
checklist items verbatim so the next iPad session closes A2 / A4-sub-item / A5-details in one pass.
