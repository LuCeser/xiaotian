---
phase: 02-websocket-communication-layer
reviewed: 2026-07-25T00:00:00Z
depth: standard
files_reviewed: 17
files_reviewed_list:
  - internal/wstest/helpers.go
  - pkg/crc16/crc16.go
  - pkg/ws/codec.go
  - pkg/ws/codec_test.go
  - pkg/ws/connection.go
  - pkg/ws/connection_test.go
  - pkg/ws/heartbeat.go
  - pkg/ws/heartbeat_test.go
  - pkg/ws/hub.go
  - pkg/ws/hub_test.go
  - pkg/ws/integration_test.go
  - pkg/ws/protocol.go
  - pkg/ws/protocol_test.go
  - pkg/ws/router.go
  - pkg/ws/router_test.go
  - pkg/ws/server.go
  - pkg/ws/server_test.go
findings:
  critical: 7
  warning: 10
  info: 4
  total: 21
status: issues_found
---

# Phase 02: Code Review Report

**Reviewed:** 2026-07-25
**Depth:** standard
**Files Reviewed:** 17
**Status:** issues_found

## Summary

The Phase 2 WebSocket layer delivers a working binary frame protocol (CRC-16-CCITT verified against the standard `0x29B1` test vector), a hub with capacity enforcement, a per-message-type router with worker pools and panic recovery, and a gorilla/websocket upgrade server with Origin whitelisting. The codec and protocol packages are sound.

However, the connection lifecycle and server layer contain **multiple blocker-class defects** that will break production behavior on a 1C/1G VPS:

- `readPump`'s `select { default: ReadMessage() }` pattern defeats context cancellation — blocked reads cannot be interrupted.
- `SetReadDeadline` is set exactly once at handshake time, so every connection is force-closed at 60 seconds regardless of activity.
- `Heartbeat` is implemented and tested but **never invoked** anywhere in `server.go`; `HeartbeatInterval` is dead config.
- `Client.send` is a private channel with no public `Send` method — the server has no way to push audio/TTS frames back to the client, breaking the streaming-TTS core flow described in CLAUDE.md.
- `CheckOrigin` returns `true` for an empty `Origin` header, contradicting the D-24 "never return-true" rule from PATTERNS.md.
- `writePump` performs writes with no write deadline, allowing a slow/frozen client to pin a goroutine indefinitely under the 150MB budget.

Several tests also assert the opposite polarity of what their comments describe, so they pass by accident while proving nothing.

## Critical Issues

### CR-01: `readPump` `select` with `default` makes context cancellation ineffective

**File:** `pkg/ws/connection.go:119-150`
**Issue:**

```go
for {
    select {
    default:
        msgType, data, err := c.conn.ReadMessage()  // blocks here
        ...
    case <-ctx.Done():
        return
    }
}
```

Go's `select` semantics: when a `default` branch is present and no other case is *immediately* ready, `default` runs at once. The result is that the loop falls into `default` on every iteration and then **blocks inside `ReadMessage`**. `ctx.Done()` is only re-evaluated after `ReadMessage` returns, and even then, because `default` is always ready, the scheduler may pick `default` again and re-enter another blocking read.

Concretely: if a client opens a WebSocket and sends nothing, `ReadMessage` blocks forever, `ctx.Done()` cannot interrupt it, and shutdown/cancel of that connection relies entirely on the underlying `conn.Close()` being called from another path. On the resource-constrained VPS this means hub shutdown, graceful server restart, and per-connection cancel all leak the read goroutine until the TCP stack notices the peer is gone.

This also makes the `case <-ctx.Done(): return` branch effectively unreachable while a read is in flight, defeating D-12 (context-based lifecycle).

**Fix:** Drop the `select`/`default` shape and either (a) spawn a close-on-cancel watcher, or (b) drive cancellation through `SetReadDeadline` refreshed each loop iteration:

```go
func (c *Client) readPump(ctx context.Context) {
    defer c.hub.Unregister(c)
    defer func() {
        if r := recover(); r != nil {
            c.logger.Error("readPump panic recovered",
                zap.Any("recovered", r), zap.Stack("stack"))
        }
    }()

    // Close the conn when ctx is cancelled, unblocking ReadMessage.
    stop := make(chan struct{})
    defer close(stop)
    go func() {
        select {
        case <-ctx.Done():
            _ = c.conn.Close()
        case <-stop:
        }
    }()

    for {
        msgType, data, err := c.conn.ReadMessage()
        if err != nil {
            return
        }
        _ = msgType
        header, payload, err := DecodeFrame(data)
        if err != nil {
            c.logger.Warn("frame decode failed, skipping", zap.Error(err))
            continue
        }
        if err := c.handler.Route(ctx, c, header, payload); err != nil {
            c.logger.Warn("handler route failed", zap.Error(err))
        }
    }
}
```

### CR-02: Read deadline set exactly once at upgrade time — every connection dies at 60s

**File:** `pkg/ws/server.go:136`
**Issue:**

```go
conn.SetReadDeadline(time.Now().Add(60 * time.Second))
```

`SetReadDeadline` sets an *absolute* deadline, not an idle timeout. The deadline is never refreshed (there is no pong handler, no per-read refresh in `readPump`). After 60 seconds, every read fails with `i/o timeout` and the connection is torn down — regardless of whether the client is actively streaming audio.

On a real dictation session that lasts more than a minute (the common case for elementary-school tutoring), the connection will be killed mid-session. This is a correctness/data-loss bug for the core use case.

The standard gorilla/websocket pattern is to refresh the deadline on each pong (or each read):

```go
conn.SetPongHandler(func(string) error {
    conn.SetReadDeadline(time.Now().Add(60 * time.Second))
    return nil
})
```

Without also wiring the heartbeat (see CR-03), there is no pong to refresh against.

**Fix:** Either set a per-read deadline inside `readPump` before each `ReadMessage`, or install a pong handler that refreshes the deadline, and pair it with an active Heartbeat (CR-03).

### CR-03: `Heartbeat` is implemented and tested but never wired into `Server`

**File:** `pkg/ws/server.go:75-77, 123-177`, `pkg/ws/heartbeat.go:29`
**Issue:**

`ServerConfig.HeartbeatInterval` is parsed and defaulted, but `Heartbeat(...)` is never launched by `HandleWebSocket` or anywhere else. `grep -n "Heartbeat\|HeartbeatInterval"` shows only config plumbing and tests — no production call site.

Effect:

1. No PING frames are sent, so Safari background-tab silent drops (RESEARCH.md Pitfall 2) are not mitigated — the explicit reason `Heartbeat` exists.
2. Without PINGs, there is no pong path to refresh `SetReadDeadline` (CR-02), compounding the 60-second-kill issue.
3. The 100-concurrent-session budget cannot be reclaimed from half-open TCP connections because nothing probes liveness.

The PATTERNS.md "Heartbeat" section explicitly requires the ticker to run with the 30s `DefaultHeartbeatInterval`. The function is correct; it is just never called.

**Fix:** In `HandleWebSocket`, after launching `readPump`/`writePump`, also launch a heartbeat goroutine bound to the connection context:

```go
if s.cfg.HeartbeatInterval > 0 {
    go Heartbeat(ctx, conn, s.cfg.HeartbeatInterval, s.logger)
}
```

The doc comment in `heartbeat.go` warning that Heartbeat "should not run concurrently with writePump" is also incorrect (see WR-06) — gorilla/websocket permits concurrent `WriteControl` (PING) alongside a single-writer `WriteMessage`, so this wiring is safe.

### CR-04: No public API to send messages to a client — `Client.send` is private and write-only from tests

**File:** `pkg/ws/connection.go:54-94, 153-185`
**Issue:**

`Client.send` is a private `chan []byte`. `writePump` reads from it, but the only writers in the entire codebase are test files (`client.send <- []byte("message")` in `connection_test.go`). There is no `Client.Send([]byte)` method, no `Client.SendAudio(...)`, nothing exposed for handlers.

The CLAUDE.md core-value statement requires streaming TTS audio back to the student. The router/handler signature is `func(ctx, c *Client, h *FrameHeader, payload []byte) error` — handlers receive `*Client` but have no way to push a response. The integration tests even acknowledge this:

```go
// integration_test.go:34-37
ws.MsgTypeAudio: func(...) error {
    // Just verify handler was called - we can't write to private send channel from here
    return nil
},
```

For Phase 2's scope ("communication layer"), this is a blocker: the upstream direction (server→client) is implemented at the wire/protocol layer (`EncodeFrame`, `writePump`) but unreachable from any handler.

**Fix:** Expose a thread-safe send method. Pick one of:

```go
// Option A: non-blocking send with backpressure signal.
func (c *Client) Send(msg []byte) error {
    select {
    case c.send <- msg:
        return nil
    default:
        return ErrSendBufferFull
    }
}

// Option B: blocking send with ctx.
func (c *Client) Send(ctx context.Context, msg []byte) error {
    select {
    case c.send <- msg:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

Option A is more consistent with the router's drop-oldest semantics (D-73). The companion issue — handlers sending after `Hub.Unregister` closes `c.send` — must also be addressed (a `sendClosed` flag guarded by `closeOnce`, or recovering from the resulting panic).

### CR-05: `CheckOrigin` accepts requests with no `Origin` header

**File:** `pkg/ws/server.go:108-115`
**Issue:**

```go
CheckOrigin: func(r *http.Request) bool {
    origin := r.Header.Get("Origin")
    if origin == "" {
        return true // Allow requests with no Origin header
    }
    return allowedOrigins[origin]
},
```

PATTERNS.md D-24 explicitly forbids wildcard or always-true origin checks ("OriginPatterns whitelist enforced (D-24) — never '*' or return-true"). Allowing empty Origin is functionally close to `return true` because any non-browser client (curl, urllib, an attacker's script) can omit the header. Browser WebSocket clients always send `Origin` on cross-origin requests, so legitimate Safari/Chrome traffic is unaffected by rejecting empty Origins.

For a children's tutoring service (CLAUDE.md target audience: elementary school students), the CSRF/cross-origin risk is real — a malicious page could open a WebSocket to this server and stream audio or trigger billing.

**Fix:**

```go
CheckOrigin: func(r *http.Request) bool {
    origin := r.Header.Get("Origin")
    if origin == "" {
        return false // browsers always send Origin; reject non-browser clients
    }
    return allowedOrigins[origin]
},
```

If a server-to-server client genuinely needs to connect without a browser Origin, require a shared secret header or mTLS instead of weakening the browser path.

### CR-06: `writePump` performs writes with no write deadline — slow clients pin goroutines

**File:** `pkg/ws/connection.go:160-185`
**Issue:**

```go
if err := c.conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
    return
}
```

No `conn.SetWriteDeadline(...)` before the write. A malicious or slow client that advertises a tiny TCP receive window can stall this write indefinitely, pinning one goroutine per connection. Combined with the 100-session budget and 150MB memory ceiling, this is an exhaustion vector: an attacker opens 100 connections, never ACKs, and 100 goroutines + their pending send-buffers (16KB each) stay resident forever.

Gorilla/websocket's `WriteMessage` honors a write deadline only if one is set; otherwise it blocks on the underlying `net.Conn`.

**Fix:**

```go
for {
    select {
    case msg, ok := <-c.send:
        if !ok {
            return
        }
        _ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
        if err := c.conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
            return
        }
    case <-ctx.Done():
        return
    }
}
```

A reasonable write timeout for streaming audio on a 1.5s-latency target is 5–10 seconds.

### CR-07: Hub reads `len(h.clients)` after releasing the lock — data race

**File:** `pkg/ws/hub.go:112-114, 124-125`
**Issue:**

```go
h.mu.Lock()
h.clients[c] = true
h.mu.Unlock()
h.logger.Info("hub registered client",
    zap.Int("total", len(h.clients)))   // <-- read without holding the lock

// Same pattern in the unregister branch:
h.mu.Unlock()
close(c.send)
h.logger.Info("hub unregistered client",
    zap.Int("total", len(h.clients)))   // <-- read without holding the lock
```

Today this happens to be safe only because `Hub.Run` is the sole writer. But `Clients()` acquires `RLock` and other code paths read the map. The pattern violates the rule established in PATTERNS.md ("Thread Safety (D-11) … Always pair Lock/Unlock with defer. Never hold a lock across a channel send or a blocking call") and is one refactor away from a `-race` failure: any future addition of a second writer (e.g., a periodic stale-connection sweeper that mutates `clients`) will turn this into a genuine data race.

It is also the kind of bug `go test -race` won't currently catch only because no test exercises `Clients()` concurrently with `Hub.Run` logging.

**Fix:** Capture the count while the lock is held:

```go
h.mu.Lock()
h.clients[c] = true
total := len(h.clients)
h.mu.Unlock()
h.logger.Info("hub registered client", zap.Int("total", total))
```

## Warnings

### WR-01: CRC only covers the 14-byte header — payload corruption is undetected

**File:** `pkg/ws/codec.go:19-32`, `pkg/ws/protocol.go:96-98`
**Issue:** `ValidateFrame` computes the CRC over `data[0:14]` only. A single-bit flip in a 640-byte audio payload passes validation. For a real-time dictation tutor feeding PCM to a child, silent payload corruption produces pops/clicks without any integrity alarm. PATTERNS.md describes this as by design ("bytes 0-13"), but for the audio path specifically it is a quality/correctness risk worth flagging. At minimum, document the limitation in the protocol contract; preferably extend the CRC to cover the payload.

### WR-02: Test assertions inverted — `TestClientReadPumpDecodesValidFrame` and `TestClientReadPumpIgnoresInvalidFrame` error on the *expected* outcome

**File:** `pkg/ws/connection_test.go:388-390, 436-438`
**Issue:**

```go
// Verify unregistration was called
if fakeHub.Clients() == 0 {
    t.Error("readPump should unregister on exit")
}
```

The comment and message claim "verify unregistration was called", but the assertion fires when `Clients() == 0` — i.e., exactly when unregistration *did* happen. The polarity is reversed. The companion test `TestClientReadPumpPanicRecovery` (line 522) and `TestClientReadPumpUnregistersOnError` (line 464) both use the correct polarity (`!= 0` / `> 0`). These tests currently pass only because timing windows leave the client in the map when the check runs — they assert nothing useful and mask regressions.

**Fix:** Invert to `if fakeHub.Clients() != 0 { t.Error(...) }` in both tests.

### WR-03: `TestClientReadPumpUnregistersOnError` does not start `Hub.Run`, so it cannot verify unregistration

**File:** `pkg/ws/connection_test.go:442-469`
**Issue:** The test never calls `fakeHub.Register(client)` nor `go fakeHub.Run(ctx)`. The readPump's deferred `c.hub.Unregister(c)` puts a client on the unregister channel that nobody drains. `fakeHub.Clients()` returns 0 simply because the map was always empty. The "unregistration was called" assertion passes trivially and would continue to pass even if `readPump` skipped the defer entirely. This is a test-reliability bug.

**Fix:** Start `go fakeHub.Run(ctx)`, call `fakeHub.Register(client)`, wait for registration, then verify `Clients()==1`, then let readPump error out, then verify `Clients()==0`.

### WR-04: `EncodeFrame`/`EncodeHeader` mutate the caller's `*FrameHeader`

**File:** `pkg/ws/codec.go:36-41`, `pkg/ws/protocol.go:83-103`
**Issue:**

```go
func EncodeFrame(h *FrameHeader, payload []byte) []byte {
    h.PayloadLen = uint16(len(payload))   // mutates input
    header := EncodeHeader(h)             // EncodeHeader also writes h.CRC16
    ...
}
```

The encode path silently overwrites two caller-supplied fields. This is surprising (the function is named "Encode", not "NormalizeThenEncode") and creates a footgun: a caller encoding the same header twice with different payloads will see `PayloadLen` from the most recent call, and `CRC16` always reflects the latest encoding. If the caller stored the header for logging or comparison, the values are now wrong.

**Fix:** Either document this clearly in the doc comment, or compute the encoded fields into a local copy:

```go
func EncodeFrame(h *FrameHeader, payload []byte) []byte {
    local := *h
    local.PayloadLen = uint16(len(payload))
    header := EncodeHeader(&local)
    ...
}
```

(If you choose to keep the mutation, at least rename to signal the side effect.)

### WR-05: Default `MaxMessageBytes` (65536) is smaller than the protocol's own maximum frame size (65551)

**File:** `pkg/ws/server.go:79`, `pkg/ws/protocol.go:14-23`
**Issue:** `PayloadLen` is `uint16`, allowing up to 65535 bytes. With the 16-byte header, a frame can be 65551 bytes — but the server sets `conn.SetReadLimit(65536)`, so any control frame with payload > 65520 bytes is rejected by gorilla/websocket before `DecodeFrame` ever runs. The protocol contract and the enforcement are out of sync. A client sending a maximally-large control frame (which the wire format permits) will be silently dropped. Either tighten the protocol (e.g., document a 65KB practical cap) or raise `MaxMessageBytes` to 65551+.

### WR-06: `Heartbeat` doc comment incorrectly forbids concurrent use with `writePump`

**File:** `pkg/ws/heartbeat.go:26-28`
**Issue:**

> Integration note: Heartbeat should not run concurrently with writePump on the same websocket.Conn, as gorilla/websocket does not allow concurrent writes.

This is inaccurate. gorilla/websocket explicitly supports one concurrent `WriteMessage` writer plus any number of `WriteControl` callers (PING/CLOSE). The doc note is the apparent reason Heartbeat is never wired into `Server` (the comment scared the integrator away from the obvious call site). Correct the doc and remove the false blocker so CR-03 can be fixed without confusion.

### WR-07: `DecodeFrame` performs no `MsgType` or `Version` validation

**File:** `pkg/ws/codec.go:57-88`
**Issue:** The decoder returns a `*FrameHeader` whose `MsgType` may be `0xFF` and `Version` may be `0xFE` — both pass decoding as long as the CRC happens to match. `ProtocolVersion` is never checked against `h.Version`, and `MsgType` validity is only enforced later by `Router.Route` (which returns `ErrUnknownMessageType`). A frame with version 0xFF and a known `MsgType` will be silently accepted and dispatched to a handler. This is a protocol-confusion risk; future versioned changes to the wire format will be unable to distinguish old/new frames at the codec layer. Reject unknown versions at the codec boundary.

### WR-08: `readPump` claims an invariant (`_ = msgType // We only process binary messages`) it does not enforce

**File:** `pkg/ws/connection.go:129`
**Issue:** The comment says "we only process binary messages", but no `if msgType != websocket.BinaryMessage` check exists. A client sending `TextMessage` will have its payload fed to `DecodeFrame`, where it will (usually) fail CRC and be silently dropped — but the comment claims an invariant the code does not uphold. Either enforce the invariant (reject non-binary) or delete the misleading comment.

### WR-09: `TestIntegration_BackpressureSignalPath` measures client write errors, not server overload

**File:** `pkg/ws/integration_test.go:592-609`
**Issue:**

```go
err = conn.WriteMessage(websocket.BinaryMessage, frame)
if err != nil {
    overloadedCount++
} else {
    successCount++
}
```

This counts *client-side* write errors as "backpressure". The server's `Router.Route` returns `ErrRouterOverloaded` internally, but that error is logged and dropped — it never reaches the client at the WebSocket layer. So `overloadedCount` will be 0 unless the TCP buffer fills up (which won't happen with 5 small frames). The test therefore does not actually exercise the backpressure signal path it claims to validate. It passes vacuously. To truly test this, expose a server-side counter of `ErrRouterOverloaded` and assert it is non-zero.

### WR-10: `fakeConn.mu` declared but never used; non-atomic fields read/written without synchronization

**File:** `pkg/ws/connection_test.go:43-58`
**Issue:** The struct has a `mu sync.Mutex` field that is never acquired anywhere, while fields like `readMessageResult`, `readMessageError`, `onWriteMessage` are mutated by the test goroutine and read inside `fakeConn` methods called from `readPump`/`writePump` goroutines. Under `-race`, the unused mutex is dead code; the unsynchronized field accesses are latent data races that today happen not to fire because of timing. Either delete the unused `mu`, or actually use it to guard the non-atomic fields.

## Info

### IN-01: Heavy reliance on `time.Sleep` makes the test suite scheduler-sensitive

**File:** `pkg/ws/connection_test.go`, `pkg/ws/hub_test.go`, `pkg/ws/integration_test.go` (numerous)
**Issue:** Dozens of `time.Sleep(50 * time.Millisecond)` / `200 * time.Millisecond` waits in place of deterministic synchronization. The hub_test.go author already discovered this and added `waitForGoroutineIncrease`/`waitForGoroutineDrain` pollers — extend that pattern to the rest of the suite. CI under load (Windows runner, parallel packages) will otherwise flake.

### IN-02: `Router.Close` cancels context but never closes route channels

**File:** `pkg/ws/router.go:161-165`
**Issue:** Channels are left open; workers exit via `<-r.ctx.Done()`. This works but is atypical — closing the channels would let workers take the `!ok` branch and exit without relying on the context winning the select race. Not a bug; readability improvement.

### IN-03: `fakeFrameHandler` struct field alignment inconsistent

**File:** `pkg/ws/connection_test.go:111-119`
**Issue:** Alignment of field tags varies (some aligned, some not). `gofmt` will rewrite this on next run; the file as committed is not gofmt-clean.

### IN-04: `Router.Route` reads `r.handlers[mt]` under `RLock` but then accesses `r.routes[mt]` after release

**File:** `pkg/ws/router.go:101-124`
**Issue:** The lock guards the existence check, then releases before the channel send. Today both maps are immutable post-construction, so this is safe. The locking suggests mutability that doesn't exist; either drop the lock (and document immutability) or hold it for the channel send. Cosmetic but misleading to future maintainers.

---

_Reviewed: 2026-07-25_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
