---
status: needs_attention
files_reviewed: 56
critical: 6
warning: 26
info: 18
total: 50
depth: standard
phase: "08"
phase_name: "student-web-interface"
lanes:
  - A: pkg/session + pkg/ws (Go concurrency core, 19 files)
  - B: pkg/infra + cmd/service + pkg/pybridge + pkg/tts + pkg/fsrs (Go resource/IO, 20 files)
  - C: pkg/web/static + test pages (Frontend JS/HTML/CSS, 10 files)
  - D: third_party/python + glue (Python + config/docs, 7 files)
---

# Code Review: Phase 8 — Student Web Interface

## Summary

Phase 8 ships a generally well-structured student web interface with solid concurrency discipline (no memory races in `pkg/session`/`pkg/ws`), a correct G-08-3 binary-frame codec parity fix, and defensively-hardened Python scripts (no shell injection, parameterized SQL, path-traversal defended). **However, it is NOT ship-ready.** Six Critical findings must be fixed first — most importantly **CR-3: the audio routing chain is completely dead** (`state.currentWordId` is initialized to `null` and never updated, so every audio frame is dropped at `audio-controller.js:157`). This single defect breaks the project's Minimum End-to-End Flow — *"系统朗读单词"* — the core value validation point. Compounding it are session-leak, sample-rate, and TTS-cache-disk-exhaustion issues that will cripple the 1C/1G VPS within hours. The 26 Warnings are mostly resource-bounds and defence-in-depth gaps; the 18 Infos are style/nits. **Recommendation: fix all 6 Criticals before declaring Phase 8 complete.**

### Lane health

- **Lane A (Go session+ws concurrency):** Lock discipline is consistent and careful — no memory data races found. The CR-01 goroutine-leak fix (closeMu + closed flag), CR-07 (capture total under lock), and the G-08-3 binary-frame regression test are solid. Four likely-bug issues lurk in the session event loop (stale-WordComplete race on replay, Push() blocking against its docstring, a missing `defer s.cancel()` goroutine leak, and an unguarded overwrite in Manager.NewSession).
- **Lane B (Go infra/service/pybridge/tts/fsrs):** Static-serving wiring is correct and well-validated (path-traversal test, MIME patching, route-collision rule, boot-time dir Stat-check). But the TTS cache has NO periodic cleanup wired in `main.go` (`CleanupStale` exists but is never invoked), which on a 1C1G VPS will fill `/tmp/tts_sessions` within hours. Boot-time construction failure after `server.Start()` leaks monitor+worker goroutines. Smaller issues: bypass of structured logging, no backoff/ctx-check in retry loop, unbounded prefetch map, missing input validation at WS boundary.
- **Lane C (Frontend JS/HTML/CSS, iPad Safari):** The G-08-3 frame-codec parity with `pkg/ws` is correct (CRC-16-CCITT over bytes 0-13, big-endian, version 0x01, audio=640B), and the WebSocket state machine + iPad gesture-unlock pattern are sound. However, the audio-routing chain is broken in production (CR-3), compounded by a session-leak counter that is never reset, a sample-rate bug that will chipmunk audio on iOS renegotiation, and a connection-timeout socket leak.
- **Lane D (Python + glue):** Python scripts are well-hardened — no `shell=True`, no pickle/eval, SQL is parameterized, path traversal defended, and the Go bridge contract is correctly satisfied. The real issues are glue/docs: `run.ps1` hints at the debug page (`/test-interface.html`) instead of the new student interface at `/`, `README.md` is stuck in Phase 1, and `docs/DEPLOYMENT.md` ships an OOM-prone `MemoryMax=200M` (Go 150MB + Python subprocesses exceed it under concurrent load).

---

## Critical

### CR-1: TTS cache has no periodic cleanup; disk fills on 1C/1G VPS
- **File:** pkg/tts/cache.go:132 + cmd/service/main.go:171-198
- **Severity:** Critical
- **Lane:** B (resource/IO)
- **Description:** `SessionCache.CleanupStale` exists (cache.go:132) but is never invoked anywhere in main.go. Each dictation session writes ~25 PCM files at ~160KB/s·16kHz·s16le (a 5s word ≈160KB; 25 words ≈4MB/session). On the strict 150MB memory/1GB disk VPS, even 50 un-cleaned sessions = ~200MB of orphan files. Crashed sessions (subprocess killed mid-pregen, WS disconnect before `CleanupSession` fires) leave PCM behind with no sweeper. Even when `Generator.CleanupSession` is wired by the session manager, it requires the session to reach a clean exit — there is no defence-in-depth.
- **Recommendation:** In `cmd/service/main.go` after `cache, err := tts.NewSessionCache(...)`, start a goroutine: `go func() { ticker := time.NewTicker(10*time.Minute); defer ticker.Stop(); for { select { case <-ctx.Done(): return; case <-ticker.C: if err := cache.CleanupStale(30*time.Minute); err != nil { logger.Warn("tts cache stale cleanup error", infra.ErrField(err)) } } } }()`. Verify the session.Manager also calls `Generator.CleanupSession` on session end.

### CR-2: Goroutine leak on early-construction failure after server.Start
- **File:** cmd/service/main.go:330,346,354
- **Severity:** Critical
- **Lane:** B (resource/IO)
- **Description:** `server.Start(ctx)` (line 263) spawns MemoryMonitor polling and WorkerPool goroutines. If `session.NewManager` (line 330), `ws.NewRouter` (line 346), or `ws.NewServer` (line 354) fails, `return 1` is hit without calling `server.Shutdown`. The mem-monitor ticker and workers leak until process exit; on the resource-constrained VPS this starves the next restart attempt. Same gap exists if `go hub.Run(ctx)` is registered but shutdown order is bypassed.
- **Recommendation:** After `if err := server.Start(ctx); err != nil {…}` succeeds, wrap the post-Start wiring (lines 271-441) in a closure that defers `server.Shutdown(shutdownCtx)`, OR call `server.Shutdown(context.Background())` explicitly on each early-return between line 269 and `<-ctx.Done()` (line 444).

### CR-3: Audio routing chain dead — student hears no audio
- **File:** pkg/web/static/js/app.js:235 + state-manager.js:62 + audio-controller.js:157
- **Severity:** Critical
- **Lane:** C (Frontend)
- **Description:** `state.currentWordId` is initialized to `null` in `initialState()` and NEVER updated by any handler (grep confirms 2 reads, 0 writes via setState). app.js:235 reads it → `wordId=null` → audio-controller.js:157 rejects every frame ("Frame received with no wordId — ignored"). No code path sends `play_word`/`next_word` either, so backend never streams. **This breaks the core dictation value prop entirely** — the project's Minimum End-to-End Flow ("系统朗读单词") does not work end-to-end.
- **Recommendation:** Wire audio routing: in `websocket-client.js _applySessionReady`, set `state.currentWordId = payload.words[0]` and send a `play_word` control frame for it; in `_applyWordComplete`, set `currentWordId` to the next word in `state.words` and send `next_word`. Alternatively, add a `word_started` control-frame handler that the backend fires before streaming, and `setState({currentWordId: payload.word_id})` from it.

### CR-4: AudioController.lastSeq never reset between sessions
- **File:** pkg/web/static/js/audio-controller.js:51,147,244
- **Severity:** Critical
- **Lane:** C (Frontend)
- **Description:** `lastSeq` starts at 0 and is only reset in `stop()` and `cleanup()` — neither is ever called between sessions (grep: zero callers of `audioController.stop()`). Backend sequences are session-monotonic, so session 2 starts at seq=1 but `this.lastSeq` is still ~N from session 1 → `sequence <= this.lastSeq` rejects every frame. Even if CR-3 is fixed, session 2+ audio is silently dropped.
- **Recommendation:** Call `audioController.stop()` (or at minimum `audioController.lastSeq = 0`) inside `WebSocketClient._applySessionReady` after the `stateManager.setState`, and also reset `scheduledTime` so the audio clock doesn't carry over.

### CR-5: createBuffer uses audioCtx.sampleRate → chipmunk audio on iOS renegotiation
- **File:** pkg/web/static/js/audio-controller.js:204
- **Severity:** Critical
- **Lane:** C (Frontend)
- **Description:** PCM data is 16kHz samples, but `createBuffer(1, numSamples, this.audioCtx.sampleRate)` declares the buffer at the runtime rate. The browser resamples `buffer.sampleRate → ctx.sampleRate` for output. If iOS honors the `{sampleRate:16000}` request this works, but the code itself logs (line 94-97) that iOS "may renegotiate." On a device that reports 48000, the buffer is declared 48k, so 16000 source samples play in 333ms (3× speed, chipmunk). D-02's "always use audioCtx.sampleRate (never 16000)" is backwards: createBuffer's rate is the SOURCE rate, not the output rate.
- **Recommendation:** Change line 204 to `this.audioCtx.createBuffer(1, numSamples, 16000)` — declare the source rate; the browser resamples to ctx.sampleRate. Works correctly whether iOS honors 16k or renegotiates. Apply the same fix to `test-interface.html:1023`.

### CR-6: Connection timeout orphans the WebSocket → reconnect FSM race
- **File:** pkg/web/static/js/websocket-client.js:130-136
- **Severity:** Critical
- **Lane:** C (Frontend)
- **Description:** The 10s `_connectionTimer` fires `_onClose()` to trigger reconnect but never closes `this.ws` or nulls its handlers. The original socket stays alive with `onopen`/`onclose` bound to `this`; if it later fires `onopen` (slow success at t=12s), `_onOpen` runs against `this.ws` which now points at the reconnect socket ws-2, corrupting state (marks CONNECTED based on ws-1, flushes queue into ws-2).
- **Recommendation:** Inside the timer callback, before `_setState(STATE_ERROR)`: `if (this.ws) { try { this.ws.close(); } catch(e){} this.ws.onopen=this.ws.onclose=this.ws.onmessage=this.ws.onerror=null; }` then null `this.ws`. Belt-and-braces: gate each handler with `if (this.ws !== expectedSocket) return;` by capturing the socket in a closure variable at construction.

---

## Warning

### WR-1: spawnStreamWord stale WordComplete drives spurious FSM transition on replay
- **File:** pkg/session/session.go:388
- **Severity:** Warning
- **Lane:** A (Go concurrency)
- **Description:** On LISTENING+play_word (replay), `spawnStreamWord` cancels the prior streamCtx and starts a new stream, but the prior goroutine unconditionally pushes EventWordComplete when StreamWord returns (line 388-392). The event loop receives that stale WordComplete while still in LISTENING and transitions LISTENING→DICTATING, cutting the replay short before the new stream finishes. End-state happens to be correct but the user sees a premature DICTATING state and the new stream's own WordComplete is then dropped as ErrInvalidTransition (log noise). Under the documented single-stream invariant this is the one path where two WordCompletes can race.
- **Recommendation:** Tag each spawn with a monotonically-incrementing generation counter on the Session; capture it in the goroutine closure and skip the WordComplete push (`if gen != s.streamGen { return }`) when a newer stream has superseded it. Alternatively, have spawnStreamWord wait on a per-stream done channel before pushing, so cancellation suppresses the stale signal at the source.

### WR-2: Push() blocks on full events channel — contradicts docstring and stalls router workers
- **File:** pkg/session/session.go:305
- **Severity:** Warning
- **Lane:** A (Go concurrency)
- **Description:** The eventBufferSize doc (line 22-26) promises "a flood drops events (logged) rather than blocking the Router worker," but `Push()` (line 305-310) is a two-arm select with NO `default:` — it blocks until either the channel drains or runCtx fires. `manager.Push` is invoked from router handlers `handlePlayWord`/`handleNextWord` (cmd/service/handlers.go:217,244), so a session whose event loop has stalled back-pressures the router worker. With per-type WorkerConcurrency as low as 1 (as in the G-08-3 test config), one stuck session freezes control-frame dispatch for every other connection on that route. On a 1C/1G VPS this is a real head-of-line blocking risk.
- **Recommendation:** Add a `default:` arm that logs a Warn and drops the event (honouring the docstring), OR explicitly bound the wait with a short time.After and drop on timeout. Either way the docstring and the implementation must agree; today the docstring is a lie that hides a stall vector.

### WR-3: run() defer chain never cancels runCtx — spawnStreamWord goroutine can leak on timeout/cancel exit
- **File:** pkg/session/session.go:243
- **Severity:** Warning
- **Lane:** A (Go concurrency)
- **Description:** The six-defer chain (close(done), recover, onExit, releaseFSRSLock, cleanupTTS, unregisterFromHub) does NOT include `s.cancel()`. runCtx is only canceled by the parent per-connection ctx (WS disconnect) or by the sweeper calling s.cancel externally. If run() exits via the per-state deadline timer (line 294) or an EventCancel while the WS connection stays open (student idle but still connected), runCtx stays live. An in-flight spawnStreamWord goroutine blocked on `s.events <- ...` (line 389) with a full buffer then waits on `s.runCtx.Done()` (line 390) which never fires — pinning the goroutine until the client finally disconnects. On the 100-concurrent/150MB budget these pinned goroutines accumulate.
- **Recommendation:** Add `defer s.cancel()` as the FIRST defer in `run()` (so it fires last, after `close(done)`). One line, defence-in-depth: it guarantees every Push/spawnStreamWord select that gates on `runCtx.Done` unblocks immediately when the loop exits, regardless of exit cause.

### WR-4: NewSession overwrites duplicate connID without guard — leaks the prior session
- **File:** pkg/session/manager.go:187
- **Severity:** Warning
- **Lane:** A (Go concurrency)
- **Description:** NewSession Lock/mu, assigns `m.sessions[connID] = s` (line 188), Unlock, then `go s.run`. There is no check for an existing entry under the same connID. If the composition root ever calls NewSession twice for the same connID (e.g. a start_session handler that doesn't gate on an existing session, or a Client pointer address reused by the GC after disconnect since clientConnID is `fmt.Sprintf("%p", client)` at handlers.go:256-258), the first Session is orphaned: it is no longer reachable from the map so the sweeper can never cancel it, and its FSRS fetch-lock is only released when its own run() exits via its private ctx timing out. Result: leaked goroutine + held FSRS lock until the 24h TTL self-expires.
- **Recommendation:** Return an error (or log+cancel the prior entry) if `_, exists := m.sessions[connID]; exists` under the Lock. Even cheaper: have the composition root track active session per client and refuse re-start, but the Manager should still be defensive — the cost is a single map lookup.

### WR-5: sendErrorFrame uses fmt.Printf, bypassing structured logger
- **File:** cmd/service/handlers.go:381-389
- **Severity:** Warning
- **Lane:** B (resource/IO)
- **Description:** `sendErrorFrame` calls `fmt.Printf("failed to send error frame: %v\n", err)` instead of the injected logger. On a production VPS this writes to stdout unstructured, breaking log aggregation and eluding the level/format/output config. Other paths in the same file use `logger.Warn`.
- **Recommendation:** Pass `logger infra.Logger` into `sendErrorFrame` (or make it a method on a struct that holds the logger) and replace the Printf with `logger.Warn("failed to send error frame", infra.ErrField(err))`.

### WR-6: retryConvert ignores ctx cancellation and uses no backoff
- **File:** pkg/tts/generator.go:146-198
- **Severity:** Warning
- **Lane:** B (resource/IO)
- **Description:** The retry loop neither checks `ctx.Err()` at iteration boundaries nor sleeps between attempts. On the 1C VPS, three back-to-back ffmpeg invocations on a corrupt MP3 (`ErrPCMConversionFailed`) burn CPU for nothing; if the parent ctx was cancelled mid-retry (session disconnect), the loop still spawns two more subprocesses. Test `TestRetryConvert_Timeout_SkipsAfterRetries` confirms the wasted iterations.
- **Recommendation:** At the top of the for-loop body: `if err := ctx.Err(); err != nil { return PCMData{}, err }`. Between attempts add `select { case <-time.After(backoffFor(attempt)): case <-ctx.Done(): return PCMData{}, ctx.Err() }` with e.g. `100ms * (1 << (attempt-1))` capped at 1s. `ErrFFmpegNotFound` already short-circuits before this so the backoff is skipped when it would never help.

### WR-7: Prefetch map unbounded; cfg.PrefetchCount never consulted
- **File:** pkg/tts/streamer.go:25,197-220
- **Severity:** Warning
- **Lane:** B (resource/IO)
- **Description:** `PrefetchNext` stores every requested word into `s.prefetch` with no size cap and no check against `s.cfg.PrefetchCount` (validated [0,10] at config load but unused here). A buggy or malicious caller passing 1000 wordIDs loads ~160MB of PCM into the heap on a 150MB-budget process. ClearPrefetch exists but is caller-driven only.
- **Recommendation:** At the top of PrefetchNext: `maxPrefetch := s.cfg.PrefetchCount; if maxPrefetch <= 0 { return }`. Inside the loop: `if len(s.prefetch) >= maxPrefetch { s.logger.Debug("prefetch cap reached", …); break }`. Also skip prefetch when cap is 0 (prefetch disabled).

### WR-8: StreamWord sends first frame without checking ctx.Err()
- **File:** pkg/tts/streamer.go:108-121
- **Severity:** Warning
- **Lane:** B (resource/IO)
- **Description:** The first frame (`i == startIndex`) bypasses the `select { case <-ctx.Done() }` gate. `TestStreamWord_ContextCancellation` documents this race. With a pre-cancelled ctx the client still receives one audio frame after disconnect, which is wasteful and racy — the caller treats StreamWord as cancelled but the receiver observes a frame.
- **Recommendation:** Before the for-loop: `if err := ctx.Err(); err != nil { s.sendWordComplete(ctx, client, wordID); return err }` (or return ctx.Err() without the complete notification, depending on contract). Also fold the i==startIndex fast-path into the select by reordering.

### WR-9: http.FileServer exposes directory listings when index.html absent
- **File:** pkg/infra/static.go:73
- **Severity:** Warning
- **Lane:** B (resource/IO)
- **Description:** `http.FileServer(http.Dir(dir))` auto-generates an HTML directory listing for any subdirectory lacking an index.html. If the student interface dir contains subdirectories (e.g. /styles, /js, /assets) without indexes, a GET to `/styles/` returns the full file listing, leaking the asset layout. Low-severity for a public student interface but inconsistent with the "tight static-serving" intent.
- **Recommendation:** Wrap fileServer in a handler that checks `d, err := os.Stat(filepath.Join(dir, r.URL.Path)); err == nil && d.IsDir() { if _, err := os.Stat(filepath.Join(dir, r.URL.Path, "index.html")); err != nil { http.NotFound(w, r); return } }` before delegating. Or use a custom `http.FileSystem` that returns os.ErrNotExist for directories without an index.

### WR-10: handlePlayWord/handleNextWord push unvalidated input to event channel
- **File:** cmd/service/handlers.go:209-251
- **Severity:** Warning
- **Lane:** B (resource/IO)
- **Description:** `handlePlayWord` and `handleNextWord` unmarshal `playWordParams` and immediately `manager.Push(...)` without checking that `p.WordID` is non-empty or matches the identPattern used by pkg/tts/cache.go. An empty or path-traversal-crafted WordID reaches the session manager and the cache.Store/Load boundary, where validation fires — but defence-in-depth at the WS handler is missing and the failure mode (cache.Store returns "wordID contains invalid characters" mid-session) is confusing.
- **Recommendation:** After unmarshal, validate: `if p.WordID == "" || len(p.WordID) > 64 { sendErrorFrame(client, "invalid word_id"); return nil }`. Optionally reuse the same `^[A-Za-z0-9_-]+$` regex from tts/cache.go (export it) so the WS boundary and FS boundary agree.

### WR-11: clientConnID uses pointer address; reuse after GC may collide
- **File:** cmd/service/handlers.go:253-258
- **Severity:** Warning
- **Lane:** B (resource/IO)
- **Description:** `fmt.Sprintf("%p", client)` produces an ID tied to the *ws.Client heap address. If a Client is GC'd and the allocator reuses the address for a later Client, both map to the same connID in `manager.NewSession`/`manager.Push`. The 100-session budget makes collision unlikely per-second but possible across a long-running process. ws.Client should expose a stable uint64 ID (e.g. atomic counter at construction).
- **Recommendation:** Either (a) use an existing ws.Client.ID() method if present, or (b) coordinate with pkg/ws to add a monotonic ID field and use that. Failing that, prefix the pointer with a connection-creation counter so reuse cannot collide: maintain a package-level `var connSeq atomic.Uint64` and build connID as `fmt.Sprintf("c%d", connSeq.Add(1))` captured at the ws.OnConnect hook.

### WR-12: json.Unmarshal on Python stdout has no size cap at call site
- **File:** pkg/pybridge/vocab.go:189,347,405
- **Severity:** Warning
- **Lane:** B (resource/IO)
- **Description:** RunDue/RunReview/RunUnlock call `json.Unmarshal(raw, ...)` where `raw` is the trailing JSON extracted from Python stdout. Bridge.run (out of scope for this lane) bounds stdout capture, but the unmarshal site has no defensive size check — a runaway Python print loop (e.g., pregenerate printing 1M paths) can produce multi-MB JSON. On a 1C1G VPS this spikes heap. (RunPregenerate uses splitNonEmptyLines, not unmarshal, so it is less affected but still lacks a cap.)
- **Recommendation:** Before each `json.Unmarshal`, check `if len(raw) > maxBridgeJSONBytes { return …, errors.Wrap(ErrPythonJSONParse, "stdout exceeded size cap") }` with a const maxBridgeJSONBytes = 1<<20 (1MB, generous for any realistic due/review/unlock payload). Defence-in-depth even if Bridge.run caps stdout.

### WR-13: Static dir follows symlinks (http.FileServer uses os.Stat)
- **File:** pkg/infra/static.go:73
- **Severity:** Warning
- **Lane:** B (resource/IO)
- **Description:** `http.Dir.Open` uses `os.Open`/`os.Stat`, which follow symlinks. If the operator's static directory contains a symlink pointing outside (e.g. /opt/hta/web/logs -> /var/log), `http.FileServer` will serve the target file. Operator-controlled so not exploitable by end users, but worth flagging for the deployment doc. The 08-05 deployment fix relies on CWD-relative paths which amplifies the risk of accidental symlinks.
- **Recommendation:** Document in `docs/DEPLOYMENT.md` that the static directory MUST NOT contain symlinks; optionally add a boot-time filepath.Walk check in main.go after the existing Stat (line 109) that fails fast if any symlink is found below cfg.Server.StaticDir.

### WR-14: Exponential backoff has no jitter → thundering herd on server restart
- **File:** pkg/web/static/js/websocket-client.js:309
- **Severity:** Warning
- **Lane:** C (Frontend)
- **Description:** `delay = min(1000 * 2^attempt, 30000)` is fully deterministic. When the Go service restarts, every connected iPad schedules reconnect at identical offsets (1s, 2s, 4s...) and they all retry in lockstep, hammering the 1C/1G VPS simultaneously.
- **Recommendation:** Add full jitter: `delay = Math.random() * Math.min(BASE_BACKOFF_MS * Math.pow(2, this.reconnectAttempts), MAX_BACKOFF_MS);` — distributes load across the reconnecting cohort.

### WR-15: Two window 'error' listeners registered — duplicate logs and confusing DX
- **File:** pkg/web/static/js/app.js:51,74
- **Severity:** Warning
- **Lane:** C (Frontend)
- **Description:** Lines 51-54 register a bubble-phase listener; lines 74-79 register the same event with capture:true. Both fire on every uncaught error, so each error is logged twice with different prefixes. The capture-phase one adds nothing functional.
- **Recommendation:** Delete the second addEventListener block (lines 74-79) — its body is a strict subset of the first handler's.

### WR-16: Error/unhandledrejection handlers reference stateManager before it is declared
- **File:** pkg/web/static/js/app.js:53,58
- **Severity:** Warning
- **Lane:** C (Frontend)
- **Description:** Both handlers close over `stateManager` (var-hoisted as undefined) but `var stateManager = new ...` lives at line 99. Any error thrown synchronously between lines 51 and 99 (e.g. a framework-injected polyfill failure) would call `undefined.setState()` → TypeError inside the error handler. The beforeunload handler at line 62 correctly null-checks; these don't.
- **Recommendation:** Move the three addEventListener calls to after `var stateManager` is assigned (after line 99), or add `if (!stateManager) return;` at the top of each handler.

### WR-17: window.audioController global is fragile cross-script coupling
- **File:** pkg/web/static/js/app.js:214 + websocket-client.js:509
- **Severity:** Warning
- **Lane:** C (Frontend)
- **Description:** app.js exposes audioController on window solely so websocket-client.js can call `window.audioController.playWordComplete` from `_applyWordComplete`. This hides the dependency, breaks testability (can't inject a mock), and means any other script clobbering window.audioController silently breaks audio.
- **Recommendation:** Pass audioController into WebSocketClient via the constructor or a `setAudioController(ac)` method (mirroring setStateManager), store as `this.audioController`, and call `this.audioController.playWordComplete` in `_applyWordComplete`.

### WR-18: appendFeedback injects server-controlled strings via innerHTML (XSS)
- **File:** test-interface.html:977
- **Severity:** Warning
- **Lane:** C (Frontend)
- **Description:** `fb.innerHTML += '<span ...>' + text + '</span>'` where text = ' → next: ' + card.next_due + ' (' + card.state + ')'. `card.next_due` and `card.state` come from the WS server (ultimately from Python/DB). A malicious or corrupted value like `<img src=x onerror=alert(1)>` executes. Same file's `displaySessionWords` correctly uses escapeHtml for words — this sink was missed.
- **Recommendation:** Use textContent + createElement, or run text through the existing `escapeHtml()` helper before concatenating into innerHTML.

### WR-19: pendingPcm memory leak when AudioContext not running
- **File:** pkg/web/static/js/audio-controller.js:184-190
- **Severity:** Warning
- **Lane:** C (Frontend)
- **Description:** `playWordComplete` deletes `pendingPcm[wordId]` ONLY on the success path (line 190, after the isAudioCtxReady check). If AudioContext is suspended (common after iPad app-switch), the early return at line 187 leaves the chunks in the Map forever — neither played nor freed. Over a 30-word session interrupted by backgrounding, this leaks ~2MB of PCM per session until page reload.
- **Recommendation:** Move `this.pendingPcm.delete(wordId);` to immediately after the `var chunks = this.pendingPcm.get(wordId);` line (before the isAudioCtxReady check). The chunks cannot be played later anyway (scheduling clock has moved on), so retaining them buys nothing.

### WR-20: src.onerror is not a valid handler on AudioBufferSourceNode
- **File:** pkg/web/static/js/audio-controller.js:217
- **Severity:** Warning
- **Lane:** C (Frontend)
- **Description:** AudioBufferSourceNode fires no 'error' event (MDN: only 'ended'). Assigning `src.onerror` is a silent no-op — playback errors from start() are caught by the surrounding try/catch, but source-arm errors (e.g. corrupted buffer) go undetected.
- **Recommendation:** Remove the dead `src.onerror = ...` assignment, or replace with `src.onended = function() { /* track completion for isPlaying flip */ };` which is actually useful for resetting `this.isPlaying` (currently set to true at line 229 and never reset when the buffer finishes).

### WR-21: iPad URL hint points to debug page
- **File:** run.ps1:16,89
- **Severity:** Warning
- **Lane:** D (glue)
- **Description:** After Phase 8 the student interface lives at `/` (pkg/web/static/index.html). The hint `http://<host-LAN-IP>:8080/test-interface.html` loads the pre-Phase-8 repo-root debug harness (still registered in main.go:508-513 debugInterfaces), so an operator who follows the hint never sees the Phase 8 interface and assumes the deploy failed. Static-dir check at run.ps1:39-43 already guarantees the student interface is the intended target.
- **Recommendation:** Change both lines to `http://<host-LAN-IP>:8080/` and mention opening `/test-interface.html` only as a debug fallback.

### WR-22: Hardcoded Volcengine X-Api-App-Key
- **File:** third_party/python/pregenerate_dictation.py:201
- **Severity:** Warning
- **Lane:** D (Python)
- **Description:** `headers["X-Api-App-Key"] = "aGjiRDfUWi"` cannot be overridden without editing source, tying every deployment to one Volcengine app account and violating the D-05 principle documented in config.example.yaml:98-101 that all deploy-specific values flow through env.
- **Recommendation:** Read via `os.environ.get("VOLCENGINE_TTS_APP_KEY", "aGjiRDfUWi")` alongside the other VOLCENGINE_TTS_* env reads at lines 176-178.

### WR-23: Duplicated _load_env with identical logic
- **File:** third_party/python/pregenerate_dictation.py:41-73 + third_party/python/vocab_manager.py:39-63
- **Severity:** Warning
- **Lane:** D (Python)
- **Description:** Both files carry a byte-identical _load_env (including the redundant double-add of DICTATION_ENV_FILE at lines 47-53 / 43-47 and the nested-chain resolution). A bug fix or new candidate path must be applied in two places or the scripts silently diverge.
- **Recommendation:** Extract a shared `env_loader.py` in third_party/python/ and `from env_loader import load_env` in both, or at minimum have vocab_manager import the function from pregenerate_dictation.

### WR-24: README.md stale Phase-1-era claims
- **File:** README.md:64,70,76,88
- **Severity:** Warning
- **Lane:** D (docs)
- **Description:** Line 64 says "main.go added in a later plan" but cmd/service/main.go is 19KB and active; line 70 says "No third-party dependencies are introduced in Plan 01" but go.mod lists 11+ modules; line 76 says "Phase 1 ships as v0.1.0" and line 88 says "Phase 1 — in progress" while CONTEXT.md confirms Phase 8. Misleads new contributors about project state.
- **Recommendation:** Rewrite the Project Layout / Dependencies / Phase Status sections to reflect Phase 8: main.go exists, deps are listed in go.mod, current phase is 08-student-web-interface.

### WR-25: MemoryMax=200M is OOM-prone
- **File:** docs/DEPLOYMENT.md:102
- **Severity:** Warning
- **Lane:** D (docs)
- **Description:** systemd MemoryMax applies to the cgroup, which includes the Python subprocess children. config.example.yaml:141-145 documents up to 2 concurrent pregenerate calls × ~100MB Python each = 200MB, plus the 150MB Go budget = 350MB peak. 200M cap kills the service under concurrent dictation load with no diagnostic other than a restart loop.
- **Recommendation:** Raise to `MemoryMax=500M` (or add an explicit comment that the value must be Go-budget + maxConcurrent × ~100MB Python headroom) and note that the 150MB figure in config.example.yaml is Go-internal only.

### WR-26: N+1 COUNT(*) per row
- **File:** third_party/python/vocab_manager.py:323-335,510-513,576-578
- **Severity:** Warning
- **Lane:** D (Python)
- **Description:** cmd_new, cmd_stats, and cmd_export each loop over every card in a category and fire a `SELECT COUNT(*) FROM review_logs WHERE card_id = ?` per row. On a mature deployment (years of accumulated vocab) cmd_export over a category becomes hundreds of round-trips.
- **Recommendation:** Replace with a single `SELECT card_id, COUNT(*) FROM review_logs WHERE card_id IN (...) GROUP BY card_id` (or a LEFT JOIN) built into a `{card_id: count}` map before iterating cards.

---

## Info

### IN-1: auditState/auditEvent always pass s.dbID=0
- **File:** pkg/session/audit.go:48
- **Severity:** Info
- **Lane:** A (Go concurrency)
- **Description:** `s.dbID` is never written after construction (auditCreate's doc at audit.go:16-20 explicitly notes the async-write-back is deferred). Every auditState call therefore issues `repo.UpdateState(ctx, 0, newState)`, associating ALL state-change rows across ALL sessions with row id 0 — the Phase-10 replay trail cannot distinguish sessions. Documented as a Phase-7 limitation; flag for the phase that needs an accurate trail.
- **Recommendation:** When accurate dbID is needed, either (a) make auditCreate synchronous in NewSession and capture the returned id into s.dbID before returning, or (b) add a channel-based write-back that the event loop drains before issuing any auditState. Until then, consider commenting out the auditState/auditEvent Submit calls so operators don't mistake the rows for a real audit trail.

### IN-2: Hub reject-at-capacity churns HandleWebSocket goroutines
- **File:** pkg/ws/hub.go:127
- **Severity:** Info
- **Lane:** A (Go concurrency)
- **Description:** At capacity, Run still accepts the client from the register channel, THEN calls `c.Close(CloseTryAgainLater)` (line 130). Register had already returned true, so HandleWebSocket has already spawned readPump/writePump/Heartbeat (server.go:251-263) which now race the close frame to exit. Functionally safe (the pumps unwind via the close), but under a connection flood it means each rejected client costs ~3 short-lived goroutines plus a handshake. Cheaper to reject before accepting into the register channel — but the current design (channel-then-check) makes that hard, so this is acceptable, just worth noting.
- **Recommendation:** No fix required; acceptable cost. Noted for awareness.

### IN-3: TestHubRegisterChannelBounded increments successCount unconditionally
- **File:** pkg/ws/hub_test.go:282
- **Severity:** Info
- **Lane:** A (Go concurrency)
- **Description:** Line 283 does `successCount.Add(1)` regardless of whether hub.Register returned true or false, then the assertion at line 290 expects all 100 to succeed. The test therefore passes even if every Register returned false (saturated) — it doesn't actually verify the bounded-channel property it claims to. The `-race` run still has value, but the functional assertion is a no-op.
- **Recommendation:** Capture the bool: `if hub.Register(client) { successCount.Add(1) }` and document that some may legitimately return false under scheduling, so assert `successCount > 0 && successCount <= 100` instead of `== 100`.

### IN-4: Misplaced "FEEDBACK is terminal" comment
- **File:** pkg/session/fsm.go:89
- **Severity:** Info
- **Lane:** A (Go concurrency)
- **Description:** The comment block at line 89-91 ("FEEDBACK is terminal — no transitions out...") sits inside the `case database.StateWaitingParent:` arm, but actually describes the (absent) `case database.StateFeedback:` arm. StateFeedback correctly has no case so any event falls through to ErrInvalidTransition at line 94 — the behaviour is right, the comment location just confuses a reader tracing the WAITING_PARENT logic.
- **Recommendation:** Move the comment above the switch or add an explicit empty `case database.StateFeedback: // terminal — fall through to ErrInvalidTransition` arm for documentation.

### IN-5: hasSuffix duplicates strings.HasSuffix
- **File:** pkg/infra/static.go:104-106
- **Severity:** Info
- **Lane:** B (resource/IO)
- **Description:** The local helper `hasSuffix(s, suffix string) bool` reimplements `strings.HasSuffix` to avoid an import. Importing `strings` is essentially free (already pulled by config.go in the same package).
- **Recommendation:** Replace `hasSuffix(path, ".html")` with `strings.HasSuffix(path, ".html")` and delete the helper; add `"strings"` to the import block.

### IN-6: homeDir silent fallback to "." masks HOME-not-set
- **File:** cmd/service/main.go:496-501
- **Severity:** Info
- **Lane:** B (resource/IO)
- **Description:** `homeDir()` returns "." when `os.UserHomeDir()` errors, causing vocab.db to be written to the process CWD silently. On a misconfigured systemd unit this writes to `/` or `/root`. Better to fail fast at boot.
- **Recommendation:** Either return ("", error) and have the caller log+exit, or log a warning when the fallback is used: `logger.Warn("HOME not set; db path defaulting to CWD")`.

### IN-7: StaticRoute validation does not enforce trailing-slash convention
- **File:** pkg/infra/config.go:860-874
- **Severity:** Info
- **Lane:** B (resource/IO)
- **Description:** Validation accepts both `/static` and `/static/` even though Go's ServeMux treats them differently (exact-match vs subtree). Mounting `cfg.Server.StaticRoute` directly to `publicMux.Handle` (main.go:382) means `/static` (no slash) only matches exactly that path; the operator sees broken asset URLs. The default `/` works because ServeMux treats `/` as subtree.
- **Recommendation:** Either normalise: if StaticDir set and StaticRoute != "/" and !strings.HasSuffix(StaticRoute, "/") { reject } OR document the requirement in the field doc comment.

### IN-8: Server.Shutdown's idempotency relies on the running flag under mu
- **File:** pkg/infra/server.go:262-305
- **Severity:** Info
- **Lane:** B (resource/IO)
- **Description:** The running flag is set/cleared under mu but read again on re-entry — if Shutdown is called concurrently from two goroutines (e.g. signal handler + main), both observe running=false after the first completes; the second returns nil. Correct, but the doc comment could note this. IN — no fix needed, just doc tightening.
- **Recommendation:** Optional: add "Concurrency: safe for concurrent invocation" to the Shutdown doc comment.

### IN-9: Inconsistent JS dialect: ES6 class in some files, ES5 var/IIFE in others
- **File:** pkg/web/static/js/state-manager.js:78 vs app.js:33
- **Severity:** Info
- **Lane:** C (Frontend)
- **Description:** Mixed styles (ES6 class in state-manager/ui-components/audio-controller but ES5 var/IIFE in app.js and websocket-client.js for the same role) raise the cognitive cost of moving between files and make lint config awkward. Not a bug.
- **Recommendation:** Standardize on ES6 (let/const, arrow fns) across all six JS files since the target (iPad Safari iOS 17+) supports it fully; drop the `var`/`function` patterns in app.js.

### IN-10: apple-mobile-web-app-capable is deprecated
- **File:** pkg/web/static/index.html:19
- **Severity:** Info
- **Lane:** C (Frontend)
- **Description:** Apple renamed to `mobile-web-app-capable` (both still work on iOS, but the legacy form has subtle standalone-mode differences in iPadOS 17).
- **Recommendation:** Replace `<meta name="apple-mobile-web-app-capable" content="yes">` with `<meta name="mobile-web-app-capable" content="yes">` (keep the apple-mobile-web-app-status-bar-style meta as-is).

### IN-11: user-scalable=no, maximum-scale=1.0 ignored since iOS 10
- **File:** pkg/web/static/index.html:18
- **Severity:** Info
- **Lane:** C (Frontend)
- **Description:** The comment claims this "prevents accidental pinch-zoom," but iOS Safari has ignored these attributes for accessibility since iOS 10 — pinch-zoom still works. The real defense is `touch-action: manipulation` (already in CSS).
- **Recommendation:** Drop maximum-scale/user-scalable from the viewport meta (keep width + initial-scale), and rely on the existing CSS rule. Update the comment to reflect reality.

### IN-12: All three stateManager.subscribe() calls discard the returned unsubscribe function
- **File:** pkg/web/static/js/app.js:155,183,207
- **Severity:** Info
- **Lane:** C (Frontend)
- **Description:** The subscribe() contract promises cleanup on teardown, but app.js never captures the unsubscribe fns. Currently harmless (singletons live for the page lifetime), but the documented invariant ("no leak across reconnects") is unenforced and would bite if components ever get re-mounted on reconnect.
- **Recommendation:** Capture each return value into a variable and wire them into the beforeunload handler alongside audioController.cleanup() and wsClient.disconnect() — costs 3 lines, makes the cleanup contract real.

### IN-13: config-init is not in .PHONY
- **File:** Makefile:15
- **Severity:** Info
- **Lane:** D (glue)
- **Description:** If a file or directory named `config-init` ever exists in the repo root, make will consider the target up-to-date and skip the copy.
- **Recommendation:** Add `config-init` to the `.PHONY` line.

### IN-14: Inconsistent lock-release pattern in pregenerate
- **File:** third_party/python/pregenerate_dictation.py:1382-1410
- **Severity:** Info
- **Lane:** D (Python)
- **Description:** The `generate` path uses `_release_global_lock(lock_file)` (line 1390) but the legacy `--run-legacy-pregenerate` path manually calls `fcntl.flock(lock_file, fcntl.LOCK_UN)` inside the inner try (line 1407-1408) then closes in the outer finally. Both are correct, but the divergence is a maintenance footgun.
- **Recommendation:** Use `_release_global_lock` in both branches; drop the manual LOCK_UN call.

### IN-15: Streaming JSON decode failures silently skipped
- **File:** third_party/python/pregenerate_dictation.py:247-250
- **Severity:** Info
- **Lane:** D (Python)
- **Description:** `except json.JSONDecodeError: continue` on each response chunk can mask a non-JSON API error body (e.g. HTML 500 page) — the loop ends with empty audio_chunks and the user only sees "no audio data received".
- **Recommendation:** Increment a skip counter and include it in the "no audio data received" error message, or treat the first non-JSON chunk as a hard error.

### IN-16: _preflight_check verifies CACHE_DIR but not MANIFEST_DIR
- **File:** third_party/python/pregenerate_dictation.py:1048-1064
- **Severity:** Info
- **Lane:** D (Python)
- **Description:** Line 1051 `os.makedirs(MANIFEST_DIR, exist_ok=True)` runs before the writability check at line 1055 which only tests CACHE_DIR. If MANIFEST_DIR points at a read-only mount, the failure surfaces later as a mid-run write error instead of a clean preflight abort.
- **Recommendation:** Add `if not os.access(MANIFEST_DIR, os.W_OK): raise RuntimeError(...)` after the makedirs.

### IN-17: Cargo-cult double-commit in cmd_due
- **File:** third_party/python/vocab_manager.py:284-289
- **Severity:** Info
- **Lane:** D (Python)
- **Description:** After the UPDATE+commit at line 282, the block at 284-289 calls `db.commit()` again inside a `try/except sqlite3.OperationalError: pass`. `commit()` on a clean transaction is a no-op and never raises OperationalError, so the except is dead code; the real purpose (closing the BEGIN IMMEDIATE when due_cards was empty) is obscured.
- **Recommendation:** Track an `committed` flag, or drop the try/except and keep only the second unconditional `db.commit()` after the `if not peek` block.

### IN-18: Falsy meaning collapses to empty string
- **File:** third_party/python/vocab_manager.py:165
- **Severity:** Info
- **Lane:** D (Python)
- **Description:** `str(meaning) if meaning else ""` turns `0`, `False`, or `0.0` JSON values into `""`. Unlikely for vocab data but silently drops legitimate numeric/boolean meanings during add.
- **Recommendation:** Use `str(meaning) if meaning is not None else ""` (or `if meaning != ""`).

---

## Appendix: Review methodology

This review was produced by parallel lane specialists after the `gsd-code-reviewer` agent proved undisposable on this runtime (its definition pins a model `gpt-5.6-terra` not available here, causing dispatch to fail). Four `general` agents reviewed non-overlapping file sets at standard depth:

- **Lane A** (19 files): `pkg/session/*`, `pkg/ws/*`
- **Lane B** (20 files): `pkg/infra/*`, `cmd/service/*`, `pkg/pybridge/*`, `pkg/tts/*`, `pkg/fsrs/*`
- **Lane C** (10 files): `pkg/web/static/*`, `test-interface.html`, `tests/test-pcm-conversion.js`
- **Lane D** (7 files): `third_party/python/*`, `Makefile`, `README.md`, `config.example.yaml`, `docs/DEPLOYMENT.md`, `run.ps1`

File scope was computed per the GSD code-review workflow: SUMMARY.md extraction (16 files from 5 plan summaries) merged with the #2666 git-diff cross-check (correct base `bcc8140^`, the first Phase-8 commit; the workflow's default `--grep="08"` was too loose and matched an unrelated hash-prefixed commit, so it was manually corrected), yielding 57 files (56 reviewed + 1 empty `.gitkeep` dropped).

Cross-cutting concerns (e.g. WS frame codec parity between Go `pkg/ws` and JS `frame-codec.js`) were verified independently by both Lane A (Go side) and Lane C (JS side); both confirmed the G-08-3 fix is correct.
