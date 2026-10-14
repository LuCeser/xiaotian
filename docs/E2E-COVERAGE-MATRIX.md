# E2E Coverage Matrix

Updated: 2026-10-14. Source of truth for which acceptance items are automated
where. **Default gate is browser E2E** (Playwright L2 / hermetic L3 browser).
iPad / device-only rows are residual and do **not** block `npm run regress`.

Layer legend: see [E2E-ISOLATION-SPEC.md](E2E-ISOLATION-SPEC.md).

| Tag | Meaning |
|-----|---------|
| L1 | Go in-process (`go test`) |
| L2 | Playwright vs mock (`npm run e2e:rt`) |
| L3b | Playwright vs isolated real stack (`e2e` / `e2e:parent` / `e2e:family`) |
| L3s | Synthetic client vs isolated real stack (`npm run e2e:rt-real`) |
| L4 | Optional human; not a regress gate |
| covered | Already asserted; no new work |
| gap | Planned automation still incomplete |

## Product RT-01…RT-10

| ID | Target | Evidence | Status |
|----|--------|----------|--------|
| RT-01 HTTPS entry / HTTP mic hint | L3b (local HTTP secure-context) + L4 optional public HTTPS | `home-entry`, tutor boot copy | covered (local); L4 optional |
| RT-02 continuous listen ≥10 turns | L2 + L3s | `rt-loop.spec.js`, `driver-rt-lesson.mjs` | covered |
| RT-03 natural dialogue scenarios | L2 partial + L4 optional listen | mock intent paths; full scene set later | covered (automable subset) |
| RT-04 latency numbers | L3s timeline + `rt-report` | driver JSONL / `npm run rt-report` | covered (capture); hear-feel L4 optional |
| RT-05 interrupt stop / no revive | L2 + L3s | `rt-loop`, `g_interrupt_*` | covered |
| RT-06 pacing / hint / pause | L2 + L3s | `e1–e4`, `f_pause_*`, `h_timeout_*` | covered |
| RT-07 data consistency | L1 + L2 | marked complete in REQUIREMENTS | covered |
| RT-08 15‑min stability / resume | L1 partial + L4 optional long soak | reconnect units; soak not in regress | residual L4 optional |
| RT-09 learning loop / FSRS | L1 + L3s + L3b lessons UI | REQUIREMENTS complete; driver `i_*`/`m_*` | covered |
| RT-10 degraded fallback | L3b | `home-entry.spec.js` banner on `/` | covered |

## Phase 13.3 UAT (999.2 subset)

| ID | Target | Evidence | Status |
|----|--------|----------|--------|
| UAT-1 family journey | L3b + L3s | `family-journey.spec.js`, parent add toast, L3s lesson+lessons API `f_*` | covered |
| UAT-2 read-zero-write | L3b | `family-journey` review_logs before/after browse | covered |
| UAT-3 no word leak in-progress | L2 + L3b | `parent-lessons.spec.js`, `family-journey` | covered |
| UAT-4 unchecked close | L2 + L3b | mock + seeded hermetic row「未核对」 | covered |
| UAT-5 practice isolation | L2 + L3b | mock practice note + seeded「加练」row | covered |
| UAT-6 pending receipt | L2 + L3b | mock「待核实」+ seeded pending receipt | covered |
| UAT-7 forceStudentMode | L1 + L3b | config unit test + `family-force-mode.spec.js` | covered |
| UAT-8 home button + degraded `/` | L2 + L3b | `home-button.spec.js`, `home-entry.spec.js` | covered |
| UAT-9 back-nav / hit targets / viewport | L3b | `family-nav.spec.js` (browser equivalents) | covered |
| UAT-10 negative cut list | L2 + L3b | parent-lessons read-only actions + `family-nav` | covered |

## Phase 13 / 13.1 (high-signal)

| ID | Target | Evidence | Status |
|----|--------|----------|--------|
| 13 cold start / tutor page | L3b / L2 | tutor load via mock & hermetic | covered |
| 13 commands reread/slow/hint/meaning | L2 + L3s | `rt-loop`, driver `e_*` | covered |
| 13 interrupt / echo path | L2 + L3s | `g_interrupt_*` | covered |
| 13 timeout advance once | L2 + L3s | `h_timeout_*` | covered |
| 13.1 three-stage check + submit | L2 + L3s | `rt-loop` check act, driver `m_*` | covered |
| 13.1 unchecked fallback | L1 + L2 | Go + mock | covered |
| 13.1 practice「再练一组」 | L2 | `home-button` practice path | covered |
| 13.1 voice verdict intents | L2 | `rt-loop` | covered |
| Child copy / audible quality | L4 optional | — | residual |

## Phase 08 / 09

| ID | Target | Evidence | Status |
|----|--------|----------|--------|
| 08 static + WS indicator + session button | L3b | `dictation-flow`, `audio-unlock` | covered |
| 08 layout / touch targets | L3b | `ipad-layout.spec.js` (viewport, not device) | covered |
| 08 gapless audition / app-switch | L4 optional | deferred historically | residual |
| 09 parent add/browse/edit/batch/photo | L3b | `parent-*.spec.js` via `e2e:parent` | covered |

## Early phases 01–07

| Bucket | Target | Evidence | Status |
|--------|--------|----------|--------|
| Infra / WS / Python / schema / session FSM / TTS units | L1 (+ `make py-e2e` where tagged) | `go test ./...`, tagged e2e | covered |

## Regress command mapping

`npm run regress` runs (fail-fast):

1. `go test ./... -count=1 -short` (L1; skips env-sensitive `TestRunProcessGroupKill` / `TestE2E_Story_*`)
2. `npm test` (Node unit)
3. `npm run e2e:parent` (L3b hermetic parent + family-journey)
4. `npm run e2e:rt` (L2 mock tutor + family-nav + rt-observables)
5. `npm run e2e:family` (L3b forceStudentMode)
6. `npm run e2e` when PowerShell launcher available (L3b student vs dev server)
7. `npm run e2e:rt-real` when `.env` present (L3s); `--require-real` makes it mandatory

Cloud/Linux subset verified 2026-10-14: steps 1–5 green (`--skip-real --skip-student`).

Isolation iron rule unchanged: no suite may default to a production port or production vocab DB.
