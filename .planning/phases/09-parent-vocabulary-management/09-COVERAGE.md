# Phase 9 — API Coverage Decision (contribution: ai-integration)

**Detector output:** `detected: true` — but both matched signals are stale
ROADMAP prose from *earlier* phases ("Web Audio API" from Phase 07.1/08-04
scope), not Phase 9 scope.

**Declaration:** No external API integration: Phase 9 calls only its own local
Python CLI subprocess (`vocab_manager.py`) over the in-repo `pybridge` seam —
an internal process boundary, not an external API/SDK/service. No third-party
HTTP clients, OAuth, webhooks, or cloud SDKs are introduced; the phase adds
zero external packages (RESEARCH Package Legitimacy Audit). The full-coverage
matrix does not apply; an INTEGRATE-by-default baseline would be vacuous.

**Re-evaluate if:** Phase 9 scope ever grows an actual external API (e.g. a
dictionary/lookup service for auto-fill) — re-run the detector over the phase
plans and produce the COVERAGE matrix before sealing.
