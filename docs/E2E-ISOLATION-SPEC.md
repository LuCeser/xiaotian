# E2E Isolation Spec — Four-Layer Design (U-1 Prevention)

Any end-to-end test of the realtime tutor defaults to an ISOLATED facility.
It must never touch a production port or the production vocabulary DB. When
adding a test against a real channel: first assign it to a layer below, then
use that layer's isolation facility — never improvise a fourth wiring.

Incident this spec prevents (U-1, 2026-10-06): the 13-12 scratch E2E driver
hardcoded the production instance (TLS port + production DB + the daily
lesson-key derivation), completed the user's real lesson of the day, and left
advance-dedup rows that silently swallowed the evening acceptance session.
Full evidence and cleanup: `13-UAT-ISSUES-20261006.md` U-1. Root cause class:
an E2E driver pointing at production infrastructure instead of an isolated
facility. The fix is structural — four layers, each with its own isolation.

## L1 — Go integration layer (in-process protocol/state machine)

- Assets: `pkg/rt/turn_integration_test.go` (and sibling `pkg/rt`/`pkg/lesson`
  tests).
- Reach: protocol framing, FSM transitions, lesson advance/dedup logic —
  everything up to but not including real provider audio.
- Use when: regression-testing protocol/state-machine logic. Fastest loop;
  no ports, no DB side effects beyond test fixtures.

## L2 — UI vs mock server (browser E2E, mock /rt)

- Assets: `tests/e2e-rt/` (`rt-loop.spec.js`, `mock-rt-server.js`) +
  `playwright.rt.config.js` (baseURL `127.0.0.1:8099`).
- Reach: real browser (mic permission, audio pipeline, playback UI) against a
  scripted mock of the /rt protocol.
- Use when: frontend interaction changes that must not depend on the real
  service. Run: `npm run e2e:rt`. The mock server owns its port; the real
  backend never boots.

## L3 — Real-service synthetic client (isolated second instance)

- Assets: `tests/e2e-rt/real/` — `config.e2e.yaml` (isolation template),
  `seed-words.json` (fixed word list), `fixtures/` (13 student-utterance
  WAVs), `driver-rt-lesson.mjs` (synthetic client + assertion matrix),
  `run-real.ps1` (launcher). One command: `npm run e2e:rt-real`.
- Reach: the full production stack — real WS handshake, real TTS/ASR
  providers, real intent classification, real FSRS writes — driven by a
  synthetic browser-faithful client. This is the highest-fidelity layer that
  is still fully automated.

### The three isolation primitives (all pure config, zero code)

| Primitive | Value | Mechanism |
|---|---|---|
| Independent ports | `127.0.0.1:8790` (service) / `:8791` (health) | `server.addr` + `server.healthAddr` in config.e2e.yaml — BOTH independent; a default healthAddr would collide with a running production instance |
| Independent DB | `.scratch/e2e-rt-data/vocab.db` (+ data/cache/manifest dirs under it) | `python.vocabDBPath` etc.; Go falls back to this path when `database.path` is empty, so Go lesson tables and the Python FSRS side share the isolated file only |
| Pinned lesson key | `e2e-rt-lesson` | `rt.lessonId` — a non-empty id is used as-is, bypassing the daily `r0-YYYYMMDD` derivation that caused the U-1 collision |

Additional guards: origins pinned to the isolated port
(`rt.allowedOrigins`), word timer shortened to 20 s
(`rt.wordTimeoutMs`) so the timeout-advance assertion does not wait a minute.

### Net-zero lifecycle (every run starts clean, leaves nothing)

- The launcher deletes and recreates `.scratch/e2e-rt-data/` before each run
  (fresh DB ⇒ fresh lesson, no completed-resume short-circuit, idempotent
  across consecutive runs).
- Teardown stops ONLY the PID the launcher recorded at Start-Process — never
  by port, never `bin/service.exe`. The isolated binary is
  `bin/service-e2e.exe` so a rebuild can never overwrite a running
  production instance's executable.

### Runbook

1. Prerequisites: `.env` with the TTS/ASR keys and a venv python
   interpreter (searched at the repo root, then the git main checkout when
   this repo is a linked worktree; or pass `-EnvFile` / `-Python`).
   Ports 8790/8791 free; `go` and `node` on PATH.
2. Run `npm run e2e:rt-real` — about 4 minutes (10-word lesson at
   real-time pace: seeding, greeting, VAD probes, four commands,
   pause/resume, interrupt, timeout advance, completion).
3. Evidence lands in `.scratch/e2e-rt-data/` (gitignored):
   `e2e-results.json` (assertion matrix), JSONL timelines, server log.
   Raw evidence never enters git (T-13-30 discipline: transcripts and
   machine evidence stay local).
4. Read the result: exit 0 = all hard assertions pass; the b2 noise probe is
   a recorded observation (13-12 rt-7 calibration finding) and does not flip
   the exit code. The `j_isolation_*` group proves the run's zero production
   contact: production `lessons`/`review_logs` counts identical before/after,
   zero `e2e-%` rows in production, pinned row present in the isolated DB.

## L4 — Human certification (real device, scripted protocol)

- Asset: `13-CHECKPOINTS.md` (ten-step channel checklist; the echo
  two-tier silence observation protocol).
- Reach: what only a human on real hardware can certify — speaker/mic
  realism, latency feel, intelligibility.
- Use when: pre-acceptance gates (13-13). This spec only points at the
  checklist; the checklist itself is the source of truth.

---

Iron rule: no layer of E2E testing may connect to a production port or the
production DB by default. New real-channel tests are layered first, then
routed into that layer's isolation facility.
