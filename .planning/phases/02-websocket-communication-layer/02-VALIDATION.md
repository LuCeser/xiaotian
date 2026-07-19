---
phase: 02
slug: websocket-communication-layer
# status lifecycle: draft (seeded by plan-phase) → validated (set by validate-phase §6)
# audit-milestone §5.5 distinguishes NOT-VALIDATED (draft) from PARTIAL (validated + nyquist_compliant: false) (#2117)
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-07-19
---

# Phase 02 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go test |
| **Config file** | none — Wave 0 installs |
| **Quick run command** | `go test ./pkg/websocket/... -v -short` |
| **Full suite command** | `go test ./... -v -race` |
| **Estimated runtime** | ~30 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./pkg/websocket/... -v -short`
- **After every plan wave:** Run `go test ./... -v -race`
- **Before `/gsd-verify-work`:** Full suite must be green
- **Max feedback latency:** 30 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| {N}-01-01 | 01 | 1 | REQ-{XX} | T-{N}-01 / — | {expected secure behavior or "N/A"} | unit | `{command}` | ✅ / ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `pkg/websocket/handlers_test.go` — WebSocket handler stubs for WS-01, WS-02, WS-03, WS-04
- [ ] `pkg/websocket/protocol_test.go` — Frame protocol validation stubs
- [ ] `pkg/websocket/registry_test.go` — Session registry test stubs
- [ ] Go test framework installed (Phase 1 skeleton)

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| iPad Safari WebSocket connection establishment | WS-01 | Requires real Safari browser testing | Open WebSocket connection from iPad Safari, verify handshake success |
| Background tab reconnection behavior | WS-04 | Safari background tab behavior is device-dependent | Test connection lifecycle when switching tabs on iPad |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
