---
phase: 06
slug: tts-audio-streaming
status: verified
# threats_open = count of OPEN threats at or above workflow.security_block_on severity (the blocking gate)
threats_open: 0
asvs_level: 1
created: 2026-08-08
---

# Phase 06 — Security

> Per-phase security contract: threat register, accepted risks, and audit trail.

---

## Trust Boundaries

| Boundary | Description | Data Crossing |
|----------|-------------|---------------|
| MP3 file path → ffmpeg argv | Paths from pybridge.RunPregenerate output cross into subprocess argv | filesystem paths (low) |
| Session cache dir → filesystem | Session ID and word ID form file paths on disk | identifiers (medium — path traversal surface) |
| pybridge.Executor → Python subprocess | Word list crosses into pregenerate_dictation.py argv | word strings (low) |
| Cache PCM bytes → WS wire | PCM data crosses from server filesystem to client network | audio bytes (low — non-secret playback audio) |
| Sender channel → WS write | Frames queue in a bounded channel before serial write | framed bytes (low) |
| Client WebSocket → control frame JSON | Untrusted client input parsed into commands | JSON (medium — injection surface) |
| Session ID → filesystem path | Client-supplied session ID forms cache directory path | identifier (medium — path traversal surface) |

---

## Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation | Status |
|-----------|----------|-----------|----------|-------------|------------|--------|
| T-06-01 | Tampering | MP3ToPCM mp3Path arg | medium | mitigate | `exec.CommandContext(ctx, argv[0], argv[1:]...)` argv slice in `pkg/tts/codec.go:47` — no shell string, path metacharacters cannot inject | closed |
| T-06-02 | Tampering | SessionCache.Store/Load sessionID/wordID | high | mitigate | `validateIdent` allow-list `^[A-Za-z0-9_-]+$` enforced in `pkg/tts/cache.go` Store+Load (rejects `..`, `/`, `\`, dots, shell metacharacters, empty); test `TestSessionCache_RejectsPathTraversal_T0602` | closed |
| T-06-03 | Information Disclosure | ffmpeg stderr in error | low | accept | stderr included in ErrPCMConversionFailed for diagnostics; safe (no secrets ride in argv/stderr per D-05 secret-via-env) | closed |
| T-06-04 | Tampering | PregenOpts.Words injection | low | accept | Words originate from FSRS DueCard.Word; bridge passes as argv slice per T-03-07 | closed |
| T-06-05 | Denial of Service | MaxRetries exhaustion under load | medium | mitigate | `TTSConfig.Validate` caps MaxRetries to `[1,10]` (`pkg/infra/config.go:359`); pregenSem serializes pregenerate calls | closed |
| T-06-06 | Repudiation | Skipped words not logged | medium | mitigate | Every skipped word emits structured log with word_id, error_type, component (`pkg/tts/generator.go`) | closed |
| T-06-07 | Denial of Service | Frame flooding overwhelms client | medium | mitigate | 20ms ticker paces frames at 50/sec (verified: 117×640B frames in UAT T3); ws.Client.Send bounded at 16 messages drop-oldest (Phase 1 D-73) | closed |
| T-06-08 | Information Disclosure | PCM audio content to wrong client | high | mitigate | `Streamer.StreamWord(ctx, client Sender, sessionID, wordID, startSeq)` takes explicit sessionID+Sender (`pkg/tts/streamer.go:53`); handler passes requesting client only | closed |
| T-06-09 | Tampering | Frame sequence manipulation | low | accept | Phase 2 CRC-16 header integrity check; server-generated incrementing sequence numbers | closed |
| T-06-10 | Tampering | Control frame JSON injection | medium | mitigate | Handler validates JSON via typed unmarshal; unknown types rejected with error frame (`cmd/service/handlers.go:143-148`); no eval/exec path | closed |
| T-06-11 | Tampering | session_id path traversal | high | mitigate | Session ID server-generated `fmt.Sprintf("session-%d", time.Now().UnixMilli())` (`handlers.go:186`), matches `^session-[0-9]+$` by construction; T-06-02 validation also guards | closed |
| T-06-12 | Spoofing | Unauthorized play_word from wrong client | medium | mitigate | handlePlayWord uses requesting client's Sender only (`handlers.go:235`); Phase 7 binds session-to-connection | closed |
| T-06-13 | Denial of Service | Concurrent play_word flooding | medium | mitigate | ws.Router per-type worker concurrency (default 4) + drop-oldest backpressure (Phase 2 D-73); streamer runs in goroutine, handler returns immediately | closed |
| T-06-14 | Information Disclosure | Error messages to client | low | mitigate | Error frames contain generic messages ("generation failed", "no due words"); detailed errors logged server-side only (`handlers.go` sendErrorFrame) | closed |
| T-06-SC | Tampering | No new npm/pip/cargo packages | low | accept | Zero external Go dependencies added across all 4 plans; ffmpeg is existing system binary | closed |

*Status: open · closed · open — below high threshold (non-blocking)*
*Severity: critical > high > medium > low — only open threats at or above workflow.security_block_on (high) count toward threats_open*
*Disposition: mitigate (implementation required) · accept (documented risk) · transfer (third-party)*

---

## Accepted Risks Log

| Risk ID | Threat Ref | Rationale | Accepted By | Date |
|---------|------------|-----------|-------------|------|
| AR-06-01 | T-06-03 | ffmpeg stderr contains no secrets (D-05 secret-via-env); needed for diagnostics | secure-phase audit | 2026-08-08 |
| AR-06-02 | T-06-04 | Words are FSRS-controlled, not client-supplied; bridge uses argv slice | secure-phase audit | 2026-08-08 |
| AR-06-03 | T-06-09 | CRC-16 integrity check + server-generated sequence numbers already mitigate; client can detect but not inject | secure-phase audit | 2026-08-08 |
| AR-06-04 | T-06-SC | Zero new external dependencies; supply-chain surface unchanged | secure-phase audit | 2026-08-08 |

*Accepted risks do not resurface in future audit runs.*

---

## Security Audit Trail

| Audit Date | Threats Total | Closed | Open | Run By |
|------------|---------------|--------|------|--------|
| 2026-08-08 | 15 | 15 | 0 | orchestrator (secure-phase, L1 grep-depth per ASVS 1) |

**Notes:**
- Register authored at plan time (4 of 4 PLAN files contained `<threat_model>` blocks); `register_authored_at_plan_time: true`.
- ASVS level 1 → L1 grep-depth verification sufficient (short-circuit applies once threats_open: 0).
- T-06-02 was initially OPEN (mitigation promised in 06-01-PLAN absent from cache.go); fix implemented (validateIdent allow-list) and verified (`TestSessionCache_RejectsPathTraversal_T0602` passes) before closing.
- All other mitigate-disposition threats verified present in implementation at L1 depth.

---

## Sign-Off

- [x] All threats have a disposition (mitigate / accept / transfer)
- [x] Accepted risks documented in Accepted Risks Log
- [x] `threats_open: 0` confirmed
- [x] `status: verified` set in frontmatter

**Approval:** verified 2026-08-08
