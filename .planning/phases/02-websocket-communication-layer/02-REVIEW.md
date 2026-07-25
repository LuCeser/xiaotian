---
phase: 02-websocket-communication-layer
reviewed: 2026-07-26T00:00:00Z
depth: standard
files_reviewed: 12
files_reviewed_list:
  - internal/wstest/helpers.go
  - pkg/ws/connection.go
  - pkg/ws/connection_test.go
  - pkg/ws/heartbeat.go
  - pkg/ws/heartbeat_test.go
  - pkg/ws/hub.go
  - pkg/ws/hub_test.go
  - pkg/ws/integration_test.go
  - pkg/ws/router.go
  - pkg/ws/router_test.go
  - pkg/ws/server.go
  - pkg/ws/server_test.go
findings:
  critical: 1
  warning: 9
  info: 6
  total: 16
status: issues_found
---

# Phase 02: Code Review Report

**Reviewed:** 2026-07-26T00:00:00Z
**Depth:** standard
**Files Reviewed:** 12
**Status:** issues_found

## Summary

The Phase 02 WebSocket layer is generally well-structured: bounded channels throughout, a clean pump-per-connection model, close-safe `Send` via RWMutex, CRC validation at the codec boundary, and origin whitelisting. The previous review round (CR-/WR- prefixed comments inline) addressed the worst immediate defects (send-on-closed-channel, write timeouts, Safari compression, etc.).

That said, this adversarial pass still finds one Critical and several Warnings. The single most important defect is the **Hub goroutine leak on shutdown**: `Register`/`Unregister` are blocking sends on channels that `Hub.Run` stops draining the moment its context cancels, with no `Close` to drain or close them. Under the documented 150 MB / 1C VPS budget this is the kind of defect that silently accumulates live goroutines during deployment rollovers and connection bursts. Beyond that, the Router's panic-recovery loop, the lack of any shutdown timeout on `Router.Close`, silent writePump exits, and several API-contract lies (non-blocking claims, `Close` always returning `nil`) all degrade production readiness. None of the Critical/Warning findings are stylistic; each can cause observable misbehavior under realistic load or shutdown conditions.

Concurrency correctness was traced through the full lifecycle (upgrade -> register -> pumps -> heartbeat -> unregister), including the `readPump` watcher goroutine, the `Send`/`CloseSend` RWMutex handoff, and the Router's worker-pool backpressure path.

## Critical Issues

### CR-01: Hub goroutine leak on shutdown - blocking Register/Unregister with no drain, no Close, no channel close

**File:** `pkg/ws/hub.go:95-150`, `pkg/ws/connection.go:128-130`, `pkg/ws/server.go:175-201`

**Issue:**
Three facts compose into a single critical defect:

1. `Hub.Register` and `Hub.Unregister` are **blocking** channel sends:
   ```go
   func (h *Hub) Register(c *Client)   { h.register <- c }   // blocks if buffer full
   func (h *Hub) Unregister(c *Client) { h.unregister <- c } // blocks if buffer full
   ```
   The doc comments claim *"This is a non-blocking operation - the request is queued"* - that is false. The channels are buffered (`RegisterBuffer`/`UnregisterBuffer`) but once the buffer fills the caller blocks indefinitely.

2. `Hub.Run` returns immediately on `ctx.Done()` **without draining** the register/unregister channels and **without closing** them:
   ```go
   case <-ctx.Done():
       return  // pending requests abandoned; channels left open and full
   ```

3. `Client.readPump` unconditionally calls `c.hub.Unregister(c)` in a `defer` on every exit path (including panics and ctx cancel). `Server.HandleWebSocket` calls `s.hub.Register(client)` before starting pumps, and then blocks on `<-done`.

**Leak path A - shutdown with the wrong order (the order the integration test itself uses):**
`hubCancel()` -> `Hub.Run` returns -> connections still alive -> HTTP server stops -> per-connection ctx cancels -> `readPump` exits -> `defer c.hub.Unregister(c)` executes -> `h.unregister <- c` **blocks forever** because nothing drains the channel. Each blocked `readPump` pins its `HandleWebSocket` goroutine on `<-done`. With >`UnregisterBuffer` (default 32) live connections at shutdown time, goroutine count grows without bound until process exit. `TestIntegration_ServerShutdownCleansAllGoroutines` masks this by using only 5 connections against a 32-buffer channel - exactly under the threshold where the leak triggers.

**Leak path B - steady-state burst:** if `Hub.Run` is momentarily slow (e.g., a synchronous zap sink, GC pause, or a registration spike) and more than `RegisterBuffer` callers call `Register` concurrently, `HandleWebSocket` hangs at `s.hub.Register(client)` - and it does **not** respect the request context, so even after the HTTP client gives up the goroutine stays. `Unregister` has the same property in `readPump`'s defer.

On a 1C/1G VPS with a 150 MB Go-service budget, an unbounded goroutine leak during every deploy/rollover is operationally critical. Each leaked goroutine retains its stack (~8 KB minimum) plus the `Client`, its `send` channel buffer (~16 msgs), and the `*websocket.Conn` buffers.

**Fix:** make the hub APIs genuinely non-blocking and add a real `Close`:

```go
func (h *Hub) Register(c *Client) bool {
    select {
    case h.register <- c:
        return true
    default:
        return false // shed; caller should reject the connection
    }
}

func (h *Hub) Unregister(c *Client) {
    select {
    case h.unregister <- c:
    default:
        // hub is not draining (shutting down or saturated); the client
        // will be GC'd once its pumps exit. Do not block readPump's defer.
        h.logger.Warn("unregister dropped: hub not draining")
    }
}

func (h *Hub) Close() {
    close(h.register)   // callers must handle the panic via recover, or
    close(h.unregister) // use a closed-flag guard like Client.CloseSend
}
```

And in `Hub.Run`, drain on `ctx.Done()` before returning:
```go
case <-ctx.Done():
    // drain any pending unregister so blocked readPumps can exit
    for {
        select {
        case c := <-h.unregister:
            c.CloseSend()
        default:
            return
        }
    }
```

`Server.HandleWebSocket` must also stop blocking on `Register` - drop or reject the connection if it returns false, and respect `r.Context()` so a disconnected HTTP client does not pin a goroutine.

## Warnings

### WR-01: Router worker re-enters the loop after a handler panic - CPU/log amplification on 1C VPS

**File:** `pkg/ws/router.go:141-173`

**Issue:**
The worker wraps `handler(...)` in an inner `func(){ defer recover(); handler(...) }()`. This is good for isolation, but after recovery the outer `for` loop immediately reads the next task. If a handler bug causes a panic on every frame for a given `MsgType`, the cycle is: client streams frames -> `readPump` queues them -> worker pops task -> panic -> `zap.Stack("stack")` (which calls `runtime.Stack`, very expensive) -> recover -> next task -> repeat. One malicious or buggy client can pin the single CPU core and emit GB of stack-trace logs. Contrast with `readPump`, where the deferred recover correctly *exits* the connection on first panic - much smaller blast radius.

**Fix:** add a per-worker (or per-handler) panic rate limiter / circuit breaker, and avoid `zap.Stack` on the hot path:

```go
var panics atomic.Int32
// inside the inner func:
if rec := recover(); rec != nil {
    if panics.Add(1) > 8 {          // trip the breaker
        r.logger.Error("handler panic threshold exceeded; disabling route",
            zap.String("msg_type", mt.String()))
        // stop draining this route; or back off exponentially
        select { case <-r.ctx.Done(): return }
    }
    r.logger.Error("handler panic recovered",
        zap.String("msg_type", mt.String()), zap.Any("recovered", rec))
    // do NOT capture zap.Stack on every panic - log it once on the first one
}
```

### WR-02: `Router.Close` has no shutdown timeout - a stuck handler blocks shutdown forever

**File:** `pkg/ws/router.go:176-179`

**Issue:**
```go
func (r *Router) Close() {
    r.cancel()
    r.wg.Wait() // unbounded
}
```
`r.cancel()` only fires `ctx.Done()`; it cannot interrupt a running `handler(task.ctx, ...)`. If a handler is blocked on a slow downstream (TTS API, DB, `time.Sleep`), `wg.Wait` blocks until that handler returns. There is no upper bound. On a constrained VPS being rolled, this prevents clean process exit and may force SIGKILL, losing in-flight work. (This is compounded by WR-08: the handler runs under the *connection* ctx, so `r.cancel()` does not even deliver cancellation to the in-flight handler.)

**Fix:** bound the wait and report:
```go
func (r *Router) Close() error {
    r.cancel()
    done := make(chan struct{})
    go func() { r.wg.Wait(); close(done) }()
    select {
    case <-done:
        return nil
    case <-time.After(5 * time.Second):
        r.logger.Error("router.Close timed out waiting for workers")
        return errors.New("router shutdown timed out")
    }
}
```

### WR-03: `Client.Close` always returns `nil` - hides close-frame delivery failures

**File:** `pkg/ws/connection.go:277-289`

**Issue:**
```go
func (c *Client) Close(code int, reason string) error {
    c.closeOnce.Do(func() {
        _ = c.conn.WriteControl(websocket.CloseMessage, ...)  // error discarded
        _ = c.conn.Close()                                     // error discarded
    })
    return nil  // always
}
```
The Hub calls this on the capacity-rejection path (`c.Close(websocket.CloseTryAgainLater, "server full")`). If `WriteControl` fails (client already gone, write deadline exceeded), the "server full" close frame is **never sent** but the caller believes the close was clean. The signature advertises an error that can never be observed, which is an API-contract lie.

**Fix:** return the WriteControl error (or at least log it), and document that `nil` only means "close frame accepted by the kernel buffer":

```go
var writeErr error
c.closeOnce.Do(func() {
    writeErr = c.conn.WriteControl(
        websocket.CloseMessage,
        websocket.FormatCloseMessage(code, reason),
        time.Now().Add(time.Second),
    )
    if cerr := c.conn.Close(); writeErr == nil {
        writeErr = cerr
    }
})
return writeErr
```

### WR-04: `writePump` exits silently on write errors and timeouts - no production observability

**File:** `pkg/ws/connection.go:206-236`

**Issue:**
Both write-failure exit paths swallow the error:
```go
if err := c.conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
    return  // no log
}
```
A connection that repeatedly drops writes (slow client, network partition) is indistinguishable from a clean shutdown in the logs. Under the 150 MB budget, `writePump` exit due to `SetWriteDeadline` expiry (the CR-06 protection) is the *expected* slow-client-kill mechanism - but it is invisible. Combined with WR-05 (Heartbeat logs at Debug), the service has almost no signal for connection-drop root cause.

**Fix:**
```go
if err := c.conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
    c.logger.Info("writePump exit: write failed",
        zap.Error(err),
        zap.Bool("timeout", errors.Is(err, os.ErrDeadlineExceeded)))
    return
}
```

### WR-05: Heartbeat logs ping-failure exit at `Debug` - too low for production

**File:** `pkg/ws/heartbeat.go:45-49`

**Issue:**
```go
if err := c.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
    logger.Debug("heartbeat ping failed, exiting", zap.Error(err))
    return
}
```
Ping failure is the primary signal of a stale/dead connection (the whole reason `Heartbeat` exists per its own doc comment). A `Debug` log is dropped in any production zap config running at Info+. This makes stale-connection diagnosis impossible without reconfiguring log levels, and means the only signal of a Safari background-tab silent drop (the documented Phase-02 failure mode, RESEARCH.md Pitfall 2) is invisible.

**Fix:** `logger.Info("heartbeat ping failed; exiting", zap.Error(err))` - or at minimum `Warn`.

### WR-06: `Heartbeat` panics on non-positive interval - exported function, no validation

**File:** `pkg/ws/heartbeat.go:32-34`

**Issue:**
```go
func Heartbeat(ctx context.Context, c WebSocketConn, interval time.Duration, logger *zap.Logger) {
    ticker := time.NewTicker(interval) // panics if interval <= 0
```
`time.NewTicker` documents: *"NewTicker panics for d <= 0"*. The only thing protecting callers today is `Server.HandleWebSocket`'s `if s.cfg.HeartbeatInterval > 0` guard (server.go:196). But `Heartbeat` is an exported symbol; any future caller (a test helper, a different server type, a restart path) that passes a zero or negative interval takes the process down with an unrecovered panic in a per-connection goroutine. `readPump`'s deferred recover would not catch this because `Heartbeat` is launched in its own goroutine by `Server.HandleWebSocket` with no recover wrapper.

**Fix:** validate at the boundary:
```go
func Heartbeat(ctx context.Context, c WebSocketConn, interval time.Duration, logger *zap.Logger) {
    if interval <= 0 {
        logger.Warn("heartbeat disabled: non-positive interval", zap.Duration("interval", interval))
        return
    }
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    // ...
}
```

### WR-07: `ServerConfig.Validate` skips HeartbeatInterval, buffer sizes, and handshake timeout

**File:** `pkg/ws/server.go:31-44`

**Issue:**
`Validate` only checks `AllowedOrigins` and `MaxMessageBytes`. `HeartbeatInterval`, `ReadBufferSize`, `WriteBufferSize`, and `HandshakeTimeout` are not validated. The `NewServer` defaulting logic only treats `== 0` as "use default" - a negative value passes through unchanged:
- `HeartbeatInterval = -1` -> not zeroed -> server.go:149 computes `readTimeout := -1 * 2 = -2`, which falls into the `<= 0` fallback. The guard at line 196 then never launches `Heartbeat`. Result: silently broken heartbeat + an effective 60s fixed read deadline, contradicting the operator's intent.
- `ReadBufferSize` / `WriteBufferSize` negative is passed straight to `websocket.Upgrader`, which has its own undocumented behavior.
- `HandshakeTimeout` negative -> `websocket.Upgrader.HandshakeTimeout` semantics undefined.

**Fix:** extend `Validate`:
```go
if c.HeartbeatInterval < 0 {
    return errors.Wrap(ErrInvalidServerConfig, "heartbeat interval must be >= 0")
}
if c.ReadBufferSize < 0 || c.WriteBufferSize < 0 {
    return errors.Wrap(ErrInvalidServerConfig, "buffer sizes must be >= 0")
}
if c.HandshakeTimeout < 0 {
    return errors.Wrap(ErrInvalidServerConfig, "handshake timeout must be >= 0")
}
```

### WR-08: Router worker runs handlers under the *connection* context, not the router context

**File:** `pkg/ws/router.go:98-130, 163`

**Issue:**
`Route` is called from `readPump` with `readPump`'s `ctx` (the per-connection context). That `ctx` is stored in `frameTask` and passed to `handler(task.ctx, ...)` by the worker. Consequences:
1. `Router.Close()` calling `r.cancel()` does **not** interrupt an in-flight handler - only the connection ctx cancelling does. So WR-02's timeout is the only defense, and even then the handler keeps running after `Router.Close` returns.
2. A handler that legitimately needs the router's lifetime (e.g., to flush state when the router shuts down) has no way to observe it.
3. After a client disconnects, its `ctx` is cancelled, so any queued tasks for that client become no-ops - but they still occupy the channel and the worker still dequeues them. Benign, but worth documenting.

**Fix:** derive a per-task context from **both** so either parent cancels it:
```go
taskCtx, cancel := context.WithCancel(r.ctx)        // router shutdown
defer cancel()
// then also derive from the connection ctx so client disconnect cancels too:
ctx, _ := context.WithCancel(taskCtx)
// or simply: taskCtx, _ := context.WithCancel(ctx) and select r.ctx.Done in the worker
```
At minimum, document that handlers observe the *connection* context, not the router's.

### WR-09: No coordinated `Server.Close` - operator must manually order HTTP -> Hub -> Router shutdown

**File:** `pkg/ws/server.go` (whole file), `pkg/ws/hub.go` (no `Close`)

**Issue:**
There is no single entry point that shuts the WebSocket subsystem down cleanly. `Server` holds `hub` and `handler` but exposes no `Close`/`Shutdown`. To avoid the CR-01 leak the caller must know to: (1) stop the HTTP server and wait for in-flight handlers, (2) only then cancel the Hub ctx, (3) call `router.Close()`. Getting the order wrong - which `TestIntegration_ServerShutdownCleansAllGoroutines` itself does (`hubCancel()` *before* `testServer.Close()`) - triggers the CR-01 leak. The "right order" is tribal knowledge with no compiler or test enforcement.

**Fix:** add a coordinated shutdown:
```go
func (s *Server) Shutdown(ctx context.Context) error {
    // 1. Stop accepting; wait for HandleWebSocket goroutines to finish
    //    (requires the http.Server, which Server doesn't currently hold).
    // 2. Cancel hub ctx and wait for Hub.Run to exit + drain (needs Hub.Close from CR-01).
    // 3. router.Close() with a bounded wait.
}
```
Even without the full refactor, adding the method (and making it call a future `Hub.Close`) documents the required order.

## Info

### IN-01: Dead code - `fakeHub` struct in connection_test.go never instantiated

**File:** `pkg/ws/connection_test.go:176-189`

**Issue:**
The type `fakeHub` (with `Unregister`/`Register` methods) is defined but every test that names a variable `fakeHub` assigns it the result of `NewHub(...)` - a real `*Hub`. The `fakeHub` type is never used as a type. This is dead test code that implies the tests exercise a fake when they actually exercise the real Hub.

**Fix:** delete the `fakeHub` struct and its methods, or rename the variables (`hub` instead of `fakeHub`) to stop shadowing.

### IN-02: Stale comment - `MaxMessageBytes` documented as "Default 65536", actual default is 65551

**File:** `pkg/ws/server.go:22-23`, `pkg/ws/server.go:83-85`

**Issue:**
```go
MaxMessageBytes int64 // Default 65536 (64KB, RESEARCH.md Anti-Pattern)
```
But `DefaultMaxMessageBytes = HeaderSize + 0xFFFF = 16 + 65535 = 65551` and `NewServer` sets `cfg.MaxMessageBytes = DefaultMaxMessageBytes` when zero. The field comment contradicts the code and will mislead operators sizing their buffers.

**Fix:** update the field comment to `// Default 65551 (HeaderSize + max uint16 payload; WR-05)`.

### IN-03: Origin whitelist does not account for ports - operator configuration gotcha

**File:** `pkg/ws/server.go:93-102, 113-124`

**Issue:**
The whitelist is built as exact strings with optional `http://`/`https://` prefixes. A browser sends `Origin: http://tutor.example.com:8080` (with port), but a config of `AllowedOrigins: ["tutor.example.com"]` only adds `tutor.example.com`, `http://tutor.example.com`, `https://tutor.example.com` - never the `:8080` variant. The check fails closed (good for security), but the operator gets a mysteriously rejected browser with no diagnostic. WebSocket same-origin comparison is scheme+host+port; this implementation silently ignores the port dimension.

**Fix:** either parse origins as `url.URL` and compare scheme+host+port explicitly, or document loudly that operators must include the port in `AllowedOrigins` (e.g., `"localhost:8080"`).

### IN-04: `TestClientReadPumpPanicRecovery` uses a fixed `time.Sleep` instead of the `waitFor` polling helper

**File:** `pkg/ws/connection_test.go:536-591`

**Issue:**
The other `readPump` tests in this file were fixed (WR-02) to poll via `waitFor(t, 200*time.Millisecond, ...)`. This one still uses `time.Sleep(100 * time.Millisecond); if fakeHub.Clients() != 0` - the same scheduler-sensitive pattern that flaked the others under `-race`/CI load. The test's own comment acknowledges relying on `cancel()` then `Sleep`.

**Fix:**
```go
readCancel()
if !waitFor(t, 200*time.Millisecond, "panic recovery unregisters", func() bool {
    return fakeHub.Clients() == 0
}) {
    t.Error("readPump should unregister on exit despite handler panic")
}
```

### IN-05: `Router.mu` is taken but `handlers` is never mutated after construction

**File:** `pkg/ws/router.go:42-53, 106-108`

**Issue:**
`Router` has a `mu sync.RWMutex` and `Route` takes `r.mu.RLock()` to read `r.handlers[h.MsgType]`. There is no writer of `handlers` anywhere in the package - `NewRouter` populates it once and it is never mutated. The lock protects nothing and adds `RLock`/`RUnlock` overhead on every `Route` call (the read-pump hot path).

**Fix:** either drop the lock and read the map directly (it is immutable after `NewRouter` returns), or document that the lock exists for a future `AddRoute`/`RemoveRoute` API. If keeping the lock, snapshot the map into a `map[MessageType]bool` lookup once at construction to avoid the map lookup under lock.

### IN-06: Storing `context.Context` and `context.CancelFunc` in the `Router` struct

**File:** `pkg/ws/router.go:42-53, 73-81`

**Issue:**
`Router.ctx` and `Router.cancel` are stored as struct fields. The Go team explicitly documents "Do not store Contexts inside a struct type; instead pass a Context explicitly to each function that needs it" (`go.dev/blog/context`). For a long-lived component this is a common, defensible pattern, but it is worth flagging because `router_test.go` and other tests construct `Router` values via `&Router{}` directly (e.g., `server_test.go:50` does `handler := &Router{}`), which leaves `ctx`/`cancel` as their zero values (`nil` context, `nil` cancel). Calling `Close()` or `Route()` on a zero-value `Router` would nil-deref `r.cancel` (calling a nil func) or read from a nil `r.ctx`. Today this only happens in tests that immediately pass the zero-value to `NewServer` for config validation, but it is a latent footgun.

**Fix:** either guard `Close`/`Route` against the zero value (`if r.cancel != nil`), or stop constructing bare `&Router{}` in tests and always go through `NewRouter`.

---

_Reviewed: 2026-07-26T00:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
