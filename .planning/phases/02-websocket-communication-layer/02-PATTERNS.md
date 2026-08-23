# Phase 2: WebSocket Communication Layer - Pattern Map

**Mapped:** 2026-07-19
**Files analyzed:** 15 (14 new + 1 modified)
**Analogs found:** 15 / 15 (all rely on Phase 1 plan documents or RESEARCH.md reference code — no existing Go source files)

## Greenfield Status Notice

> The repository currently contains NO Go source code. Phase 1 executed only Plan 01 (project skeleton): `go.mod`, `Makefile`, `.golangci.yml`, `.pre-commit-config.yaml`, `.github/workflows/ci.yml`, plus `.gitkeep` markers in `cmd/service/`, `pkg/infra/`, `internal/`. Phase 1 plans 02-08 (memory.go, pool.go, connection.go, worker.go, server.go, health.go, main.go) are documented in detail but NOT yet implemented.
>
> As a result, the "analogs" for Phase 2 files fall into three categories:
> 1. **Phase 1 plan documents** — the canonical Go patterns the project has committed to (e.g., `01-06-PLAN.md` for `ConnectionRegistry`). Phase 2 must match these exactly so integration works once Plan 02-08 land. Read these plans before writing code.
> 2. **RESEARCH.md code examples** — verified patterns from `coder/websocket` official docs and stdlib.
> 3. **Existing config files** — `go.mod`, `Makefile`, `.golangci.yml`, CI workflow — for build, lint, and test conventions.
>
> **Planner implication:** Every Phase 2 plan must (a) require reading the relevant Phase 1 PLAN.md as a primary reference, (b) replicate the conventions those plans define, and (c) be self-contained where Phase 1 components don't yet exist (see RESEARCH.md Open Question 3 — minimal viable versions or "blocked-on-Phase-1" markers).

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `pkg/ws/protocol.go` | model + utility | transform | RESEARCH.md "Pattern 2: 二进制帧协议" + Phase 1 `01-06-PLAN.md` custom-types pattern | reference (no existing code) |
| `pkg/ws/protocol_test.go` | test | transform | Phase 1 `01-06-PLAN.md` test-list pattern + RESEARCH.md "Test WebSocket 连接" | reference |
| `pkg/ws/codec.go` | utility | transform | RESEARCH.md CRC-16 + EncodeHeader example | reference |
| `pkg/ws/codec_test.go` | test | transform | Phase 1 `01-06-PLAN.md` (TestSessionIDIsUUIDv4, TestLookupByID) table-driven style | reference |
| `pkg/ws/hub.go` | store (registry) | event-driven (register/unregister) | Phase 1 `01-06-PLAN.md` (ConnectionRegistry) + RESEARCH.md "Pattern 1: Hub 模式" | intended-pattern |
| `pkg/ws/hub_test.go` | test | event-driven | Phase 1 `01-06-PLAN.md` TestSessionLimit / TestRegisterRejectsAtCapacity | intended-pattern |
| `pkg/ws/connection.go` | service (per-conn I/O) | streaming (read/write pumps) | RESEARCH.md "Pattern 3: Per-Connection Read/Write Goroutines" | reference |
| `pkg/ws/server.go` | controller (HTTP upgrade) | request-response | RESEARCH.md "WebSocket 连接接受 (coder/websocket)" + Phase 1 `01-08-PLAN.md` main.go pattern | reference |
| `pkg/ws/server_test.go` | test | request-response | RESEARCH.md "测试 WebSocket 连接 (httptest)" | reference |
| `pkg/ws/router.go` | service | pub-sub (channel fan-out) | Phase 1 `01-07-PLAN.md` (WorkerPool channel pattern) + RESEARCH.md Pattern 1 | intended-pattern |
| `pkg/ws/router_test.go` | test | pub-sub | Phase 1 `01-07-PLAN.md` TestWorkerPoolProcessesTasks | intended-pattern |
| `pkg/ws/heartbeat.go` | service | event-driven (ticker) | RESEARCH.md "心跳 Ping/Pong" + Phase 1 `01-04-PLAN.md` MemoryMonitor tick pattern | reference |
| `internal/wstest/helpers.go` | test utility | request-response | Phase 1 `01-04-PLAN.md` (fakeMemSource, recordingLogger) — D-48 manual fakes | intended-pattern |
| `cmd/service/main.go` (modified) | controller | request-response | Phase 1 `01-08-PLAN.md` (cmd/service/main.go specification) | intended-pattern (file does not exist yet) |
| `go.mod` (modified) | config | n/a | Existing `go.mod` (module github.com/xiaotian/home-tutor-agent, go 1.26) | exact |

## Pattern Assignments

### `pkg/ws/protocol.go` (model + utility, transform)

**Analog:** RESEARCH.md lines 229-297 ("Pattern 2: 二进制帧协议") + Phase 1 `01-06-PLAN.md` lines 101-116 (custom domain types per D-40).

**Imports pattern** (project convention from Phase 1 plans):
```go
package ws

import (
    "encoding/binary"
    "errors"
    "fmt"
    "hash/crc16"
)
```
Note: stdlib-only — `hash/crc16` is preferred over `github.com/sigurn/crc16` per RESEARCH.md "Don't Hand-Roll" (line 356). External deps (`github.com/pkg/errors`, `go.uber.org/zap`) are reserved for files that touch I/O or logging.

**Custom domain types pattern** (Phase 1 `01-06-PLAN.md` lines 103-105, D-40 type safety):
```go
// From 01-06-PLAN.md — apply the same typed-enum + String() pattern to MessageType.
type SessionID string         // Phase 1 contract — reuse, do NOT redefine in pkg/ws
type ConnectionState int      // defined in pkg/infra/connection.go

// Phase 2 mirrors the pattern:
type MessageType uint8        // D-06 extended message type set
type ProtocolVersion uint8    // D-11 protocol version field
```

**Core pattern — header encode/decode** (RESEARCH.md lines 284-296):
```go
const (
    HeaderSize      = 16
    AudioFrameSize  = 640 // 20ms @ 16kHz PCM, aligned with Phase 1 D-71
    ProtocolVersion = 0x01
)

// EncodeHeader writes the 16-byte big-endian (D-10) header and computes
// CRC-16-CCITT (D-08) over bytes [0:14].
func EncodeHeader(h *FrameHeader) []byte {
    buf := make([]byte, HeaderSize)
    buf[0] = h.Version
    buf[1] = uint8(h.MsgType)
    binary.BigEndian.PutUint32(buf[2:6], h.Sequence)
    binary.BigEndian.PutUint32(buf[6:10], h.Timestamp)
    binary.BigEndian.PutUint16(buf[10:12], h.PayloadLen)
    binary.BigEndian.PutUint16(buf[12:14], h.Reserved)
    crc := crc16.ChecksumCCITT(buf[0:14])
    binary.BigEndian.PutUint16(buf[14:16], crc)
    return buf
}
```

**Validation pattern** (Phase 1 `01-06-PLAN.md` D-44 centralized validation; lines 88-96 of `01-06-PLAN.md` show error-wrapping convention):
```go
var (
    ErrInvalidFrame       = errors.New("invalid frame")
    ErrCRCChecksumFailed  = errors.New("crc-16 checksum failed")
    ErrUnknownMessageType = errors.New("unknown message type")
)
// Validation fails fast on header-too-short / CRC mismatch / unknown MsgType.
// Wrap with errors.Wrap when propagating: return errors.Wrap(ErrCRCChecksumFailed, "decode frame")
```

**Error handling:** sentinel errors + `errors.Wrap` (Phase 1 plans use `github.com/pkg/errors` for all wrapping).

---

### `pkg/ws/protocol_test.go` (test, transform)

**Analog:** Phase 1 `01-06-PLAN.md` lines 87-96 (behavior-spec test list) + RESEARCH.md "Validation Architecture" (line 580).

**Test naming convention** (Phase 1 VALIDATION.md — required test names appear in plans as TestXxx):
```go
package ws

import (
    "testing"
)
// Required subtests per RESEARCH.md line 583-588:
//   TestBinaryFrameEncodeDecode  (WS-02)
//   TestFrameCRC16               (WS-02, D-08)
//   TestCRC16                    (D-08 standalone)
```

**Table-driven style** (Phase 1 plan pattern across `01-04/06/07/08`):
```go
// Pattern: define cases as a slice of structs, loop with t.Run(name, ...)
func TestBinaryFrameEncodeDecode(t *testing.T) {
    cases := []struct {
        name string
        in   FrameHeader
        want []byte
    }{
        /* ... */
    }
    for _, c := range cases {
        t.Run(c.name, func(t *testing.T) { /* ... */ })
    }
}
```

**Test data:** 640-byte audio payload fixtures live in `internal/wstest/helpers.go` (see that file's pattern).

---

### `pkg/ws/codec.go` (utility, transform)

**Analog:** RESEARCH.md lines 444-464 (CRC-16 validation).

**Imports pattern:**
```go
package ws

import (
    "encoding/binary"
    "errors"
    "hash/crc16"
)
```

**Core pattern — full frame encode/decode with CRC validation** (RESEARCH.md lines 451-463):
```go
// ComputeCRC16 computes CRC-16-CCITT (D-08) over the given bytes.
func ComputeCRC16(data []byte) uint16 {
    return crc16.ChecksumCCITT(data) // stdlib default = CCITT, no extra dep
}

// ValidateFrame returns true if the embedded CRC-16 matches the computed value.
func ValidateFrame(data []byte) bool {
    if len(data) < HeaderSize {
        return false
    }
    expected := binary.BigEndian.Uint16(data[14:16])
    actual := ComputeCRC16(data[0:14])
    return expected == actual
}
```

**Anti-pattern to avoid** (RESEARCH.md lines 343-348): do NOT hand-roll CRC polynomial math, do NOT skip the `len(data) < HeaderSize` guard, do NOT confuse CRC-16-CCITT with CRC-16-IBM (RESEARCH.md Assumption A1).

**Codec wrapper** (split encode/decode for symmetric API):
```go
// EncodeFrame combines EncodeHeader + payload, returning a contiguous slice.
// Caller may pass a pre-allocated dst (Phase 1 D-14 sync.Pool buffer reuse) to avoid allocs.
func EncodeFrame(h *FrameHeader, payload []byte) []byte { /* ... */ }
func DecodeFrame(raw []byte) (*FrameHeader, []byte, error) { /* ... */ }
```

---

### `pkg/ws/codec_test.go` (test, transform)

**Analog:** Phase 1 `01-06-PLAN.md` table-driven style. Reuse `internal/wstest/helpers.go` for fixture frames.

---

### `pkg/ws/hub.go` (store, event-driven — register/unregister)

**Analog:** Phase 1 `01-06-PLAN.md` (ConnectionRegistry spec) + RESEARCH.md "Pattern 1: Hub 模式" (lines 194-226).

**Critical integration note:** Phase 1 `pkg/infra/connection.go` (Plan 06) defines `ConnectionRegistry` with `Register/Unregister/Lookup`, `LifecycleHooks` (D-55), 100-session hard limit (D-56), periodic 30s cleanup (D-57), UUID v4 IDs (D-58). The Phase 2 `Hub` is the **WebSocket-aware facade over ConnectionRegistry** — it should NOT reimplement registry semantics. Plan must decide: (a) embed `*infra.ConnectionRegistry`, (b) accept it via constructor DI (D-42), or (c) self-contained minimal version if Plan 06 not landed (see RESEARCH.md Open Question 3).

**Imports pattern:**
```go
package ws

import (
    "context"
    "sync"
    "time"

    "github.com/xiaotian/home-tutor-agent/pkg/infra"   // D-23 import graph: ws → infra
    "github.com/coder/websocket"
    "github.com/pkg/errors"
    "go.uber.org/zap"
)
```

**Struct + constructor (DI) pattern** (Phase 1 `01-06-PLAN.md` + D-42):
```go
// Hub is the WebSocket-aware facade over infra.ConnectionRegistry (D-17 centralized state).
type Hub struct {
    registry *infra.ConnectionRegistry // injected; nil => self-contained minimal version
    register   chan *Client
    unregister chan *Client
    clients    map[*Client]bool
    mu         sync.RWMutex            // D-11 thread-safe component
    limit      int                     // D-04: 100 concurrent session limit
    logger     *zap.Logger             // structured logging per D-25
}

// NewHub follows Phase 1 constructor convention: validate cfg, return *Hub, error.
func NewHub(registry *infra.ConnectionRegistry, cfg HubConfig, logger *zap.Logger) (*Hub, error) {
    if cfg.Limit <= 0 {
        return nil, errors.Wrap(infra.ErrInvalidConfig, "hub limit must be > 0")
    }
    if logger == nil { logger = zap.NewNop() }
    return &Hub{
        registry:   registry,
        register:   make(chan *Client, 32),
        unregister: make(chan *Client, 32),
        clients:    make(map[*Client]bool),
        limit:      cfg.Limit,
        logger:     logger,
    }, nil
}
```

**Core pattern — Run loop with context cancellation** (RESEARCH.md lines 206-226 + Phase 1 D-12):
```go
func (h *Hub) Run(ctx context.Context) {
    for {
        select {
        case c := <-h.register:
            if len(h.clients) >= h.limit {
                c.Close(websocket.StatusTryAgainLater, "server full")  // D-04 graceful reject
                continue
            }
            h.mu.Lock()
            h.clients[c] = true
            h.mu.Unlock()
        case c := <-h.unregister:
            h.mu.Lock()
            delete(h.clients, c)
            h.mu.Unlock()
            close(c.send) // signal writePump to exit
        case <-ctx.Done(): // D-12: ctx-driven cleanup, no goroutine leak
            return
        }
    }
}
```

**Anti-patterns to avoid** (RESEARCH.md lines 343-348):
- Unbounded channel buffers under 150MB memory budget — use bounded `make(chan, 32)` or similar.
- Missing `ctx.Done()` branch in select — would leak the Run goroutine past shutdown.

---

### `pkg/ws/hub_test.go` (test, event-driven)

**Analog:** Phase 1 `01-06-PLAN.md` lines 87-96 (TestSessionLimit, TestRegisterRejectsAtCapacity, TestUnregisterFreesSlot, TestLifecycleHooks, TestRegistryStopsOnContextCancel).

**Required subtests per RESEARCH.md line 599:**
```go
// TestHubRegisterUnregister — covers WS-03, D-04 (register/unregister/limit)
// TestHubRejectsAtCapacity — at limit, register closes client with StatusTryAgainLater
// TestHubStopsOnContextCancel — Run goroutine exits within ~50ms of ctx cancel (no leak)
```

**Context-cancel pattern** (Phase 1 `01-04-PLAN.md` TestMemoryMonitorStopsOnContextCancel):
```go
ctx, cancel := context.WithCancel(context.Background())
hub := mustNewHub(t, 100)
go hub.Run(ctx)
// ... assertions ...
cancel()
// Assert goroutine count returns to baseline within 50-500ms (Phase 1 convention).
```

---

### `pkg/ws/connection.go` (service, streaming — per-connection read/write pumps)

**Analog:** RESEARCH.md "Pattern 3: Per-Connection Read/Write Goroutines" (lines 299-339) + Phase 1 `01-06-PLAN.md` Connection struct.

**Imports pattern:**
```go
package ws

import (
    "context"

    "github.com/coder/websocket"
    "github.com/pkg/errors"
    "go.uber.org/zap"
)
```

**Struct pattern** (mirrors Phase 1 Connection domain type but adds WebSocket-specific fields):
```go
// Client wraps a single WebSocket connection. Read/write pumps run as separate
// goroutines per RESEARCH.md Pattern 3 — never combine into one goroutine (anti-pattern line 345).
type Client struct {
    conn   *websocket.Conn
    hub    *Hub
    router *Router
    send   chan []byte    // bounded, hub closes this on unregister (signals writePump exit)
    logger *zap.Logger
}
```

**Core pattern — read pump** (RESEARCH.md lines 310-325):
```go
func (c *Client) readPump(ctx context.Context) {
    defer func() { c.hub.unregister <- c }() // D-55 onDisconnect hook
    for {
        msgType, data, err := c.conn.Read(ctx)
        if err != nil {
            return // ctx cancelled or peer closed
        }
        _ = msgType // coder/websocket returns MessageBinary or MessageText
        header, payload, err := DecodeFrame(data)
        if err != nil {
            c.logger.Warn("frame decode failed", zap.Error(err))
            continue
        }
        c.router.Route(ctx, c, header, payload) // D-17 channel routing
    }
}
```

**Write pump with context + panic recovery** (RESEARCH.md lines 327-339 + Phase 1 D-49):
```go
func (c *Client) writePump(ctx context.Context) {
    for {
        select {
        case msg, ok := <-c.send:
            if !ok {
                return // channel closed by Hub on unregister
            }
            if err := c.conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
                return
            }
        case <-ctx.Done():
            return
        }
    }
}
```

**Anti-patterns** (RESEARCH.md lines 343-348):
- Single goroutine doing both read and write — serializes I/O, kills backpressure.
- Missing read goroutine on a write-only connection — control frames pile up (use `CloseRead(ctx)` for write-only).

---

### `pkg/ws/server.go` (controller, request-response — HTTP upgrade)

**Analog:** RESEARCH.md "WebSocket 连接接受 (coder/websocket)" (lines 396-419) + Phase 1 `01-08-PLAN.md` Server spec.

**Imports pattern:**
```go
package ws

import (
    "context"
    "net/http"

    "github.com/coder/websocket"
    "github.com/pkg/errors"
    "go.uber.org/zap"
)
```

**Core pattern — Accept with Safari-safe options** (RESEARCH.md lines 401-419):
```go
// HandleWebSocket upgrades HTTP to WebSocket. Bound to http.ServeMux at /ws by main.go.
//
// Safari compatibility (RESEARCH.md Pitfall 1, line 363):
//   - CompressionMode MUST be CompressionDisabled (Safari truncates >4092B with permessage-deflate).
//   - OriginPatterns whitelist enforced (D-24) — never "*" or return-true.
func (s *Server) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
    c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
        CompressionMode: websocket.CompressionDisabled, // D-19 + RESEARCH.md Pitfall 1
        OriginPatterns:  s.cfg.AllowedOrigins,          // D-24 whitelist
    })
    if err != nil {
        s.logger.Warn("ws accept failed", zap.Error(err))
        return
    }
    defer c.Close(websocket.StatusNormalClosure, "")

    c.SetReadLimit(s.cfg.MaxMessageBytes) // D-02, default 65536 (64KB) per RESEARCH.md line 415

    // D-12 parameter handshake — read userID/device from query string
    client := newClient(c, r, s.hub, s.router, s.logger)
    s.hub.register <- client
}
```

**Server struct + constructor (DI)** (Phase 1 `01-08-PLAN.md` Server spec + D-42):
```go
type Server struct {
    hub     *Hub
    router  *Router
    cfg     ServerConfig
    logger  *zap.Logger
}

func NewServer(hub *Hub, router *Router, cfg ServerConfig, logger *zap.Logger) (*Server, error) {
    if err := cfg.Validate(); err != nil { // D-44 centralized validation
        return nil, errors.Wrap(err, "ws server config")
    }
    if logger == nil { logger = zap.NewNop() }
    return &Server{hub: hub, router: router, cfg: cfg, logger: logger}, nil
}
```

---

### `pkg/ws/server_test.go` (test, request-response)

**Analog:** RESEARCH.md "测试 WebSocket 连接 (httptest)" (lines 467-503).

**Required subtests per RESEARCH.md line 600:**
```go
// TestWebSocketAccept — covers WS-01 (httptest + ws:// dial)
```

**Core pattern — httptest + ws:// dial** (RESEARCH.md lines 471-503):
```go
func TestWebSocketAccept(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(srv.HandleWebSocket))
    defer server.Close()

    wsURL := "ws://" + server.Listener.Addr().String()
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    c, _, err := websocket.Dial(ctx, wsURL, nil)
    if err != nil { t.Fatal(err) }
    defer c.Close(websocket.StatusNormalClosure, "")

    frame := mustBuildTestFrame(t, MsgTypeAudio, 0, testPayload) // helper from internal/wstest
    if err := c.Write(ctx, websocket.MessageBinary, frame); err != nil {
        t.Fatal(err)
    }
    // ... read + assert response ...
}
```

---

### `pkg/ws/router.go` (service, pub-sub — channel fan-out)

**Analog:** Phase 1 `01-07-PLAN.md` (WorkerPool channel pattern + Submit contract) + RESEARCH.md Pattern 1.

**Imports pattern:**
```go
package ws

import (
    "context"

    "github.com/xiaotian/home-tutor-agent/pkg/infra" // D-23: ws → infra for WorkerPool.Task
    "github.com/pkg/errors"
    "go.uber.org/zap"
)
```

**Core pattern — channel-based message routing (D-17)** (RESEARCH.md Pattern 1 + Phase 1 `01-07-PLAN.md` Submit contract):
```go
// Router dispatches decoded frames to per-MsgType handlers via Go channels.
// Worker-pool-backed for business logic per D-16 (one goroutine for I/O, worker pool for logic).
type Router struct {
    pool    *infra.WorkerPool // injected; nil => spawn goroutine per message (fallback)
    routes  map[MessageType]Handler
    logger  *zap.Logger
}

type Handler func(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error

// Route submits a frame to the appropriate handler. On pool-full, returns
// infra.ErrWorkerPoolFull so the read pump can emit a backpressure frame to the client (D-73 drop-oldest).
func (r *Router) Route(ctx context.Context, c *Client, h *FrameHeader, payload []byte) error {
    handler, ok := r.routes[h.MsgType]
    if !ok {
        return errors.Wrap(ErrUnknownMessageType, "route")
    }
    task := func(taskCtx context.Context) error {
        return handler(taskCtx, c, h, payload)
    }
    if r.pool != nil {
        return r.pool.Submit(task) // surfaces ErrWorkerPoolFull on backpressure
    }
    go task(ctx) // minimal-version fallback if Phase 1 WorkerPool not yet available
    return nil
}
```

**Pitfall to avoid** (RESEARCH.md lines 374-379, "并发写导致 panic"): all writes to `c.send` MUST go through the hub or writePump — never call `c.conn.Write` directly from a handler. The per-client `send` channel serializes writes.

---

### `pkg/ws/router_test.go` (test, pub-sub)

**Analog:** Phase 1 `01-07-PLAN.md` (TestWorkerPoolProcessesTasks, TestWorkerPoolBackpressureSignal).

**Required subtests per RESEARCH.md line 602:**
```go
// TestRouterDispatchesByType — covers WS-04, D-16/D-17
// TestRouterBackpressurePropagates — pool full => ErrWorkerPoolFull surfaces to read pump
```

---

### `pkg/ws/heartbeat.go` (service, event-driven — 30s ticker)

**Analog:** RESEARCH.md "心跳 Ping/Pong (coder/websocket)" (lines 423-441) + Phase 1 `01-04-PLAN.md` MemoryMonitor tick pattern (lines 125-148).

**Imports pattern:**
```go
package ws

import (
    "context"
    "time"

    "github.com/coder/websocket"
    "go.uber.org/zap"
)
```

**Core pattern — ticker + context-cancel + D-14 30s interval** (RESEARCH.md lines 427-441):
```go
// Heartbeat pings every 30s (D-14) to keep connections alive against Safari
// background-tab silent drops (RESEARCH.md Pitfall 2, line 368).
func Heartbeat(ctx context.Context, c *websocket.Conn, interval time.Duration, logger *zap.Logger) {
    ticker := time.NewTicker(interval) // default 30s (D-14), aligned with Phase 1 D-57 cleanup
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return // D-12: clean goroutine exit, no leak
        case <-ticker.C:
            if err := c.Ping(ctx); err != nil {
                logger.Debug("heartbeat ping failed", zap.Error(err))
                return // connection lost; read pump will detect via Hub unregister
            }
        }
    }
}
```

**Pattern alignment with Phase 1:** Phase 1 `01-04-PLAN.md` MemoryMonitor uses the identical `time.NewTicker + ctx.Done() + defer ticker.Stop()` shape. Mirror it exactly for consistency.

---

### `internal/wstest/helpers.go` (test utility, request-response)

**Analog:** Phase 1 `01-04-PLAN.md` lines 199-202 (fakeMemSource, recordingLogger — D-48 manual fakes).

**Imports pattern:**
```go
package wstest

import (
    "context"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/coder/websocket"
)
```

**Core pattern — manual fakes + frame builder** (Phase 1 D-48 — no testify/mock):
```go
// MustBuildTestFrame builds a complete binary frame for tests.
// Panics on invalid input — keeps test bodies concise.
func MustBuildTestFrame(t *testing.T, msgType MessageType, seq uint32, payload []byte) []byte {
    t.Helper()
    h := &FrameHeader{
        Version:    ProtocolVersion,
        MsgType:    msgType,
        Sequence:   seq,
        Timestamp:  uint32(time.Now().UnixMicro()),
        PayloadLen: uint16(len(payload)),
    }
    return EncodeFrame(h, payload)
}

// DialTestServer wraps the RESEARCH.md httptest+ws:// pattern (lines 471-487).
func DialTestServer(t *testing.T, h http.HandlerFunc) (*websocket.Conn, func()) {
    t.Helper()
    server := httptest.NewServer(h)
    wsURL := "ws://" + server.Listener.Addr().String()
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    c, _, err := websocket.Dial(ctx, wsURL, nil)
    if err != nil {
        cancel(); server.Close()
        t.Fatal(err)
    }
    cleanup := func() {
        _ = c.Close(websocket.StatusNormalClosure, "")
        cancel(); server.Close()
    }
    return c, cleanup
}

// TestAudioPayload returns a 640-byte PCM fixture (20ms@16kHz).
func TestAudioPayload() []byte { /* deterministic 640-byte slice */ }
```

---

### `cmd/service/main.go` (modified, controller)

**Analog:** Phase 1 `01-08-PLAN.md` (full cmd/service/main.go specification).

**Critical dependency note:** This file does NOT exist yet — it is delivered by Phase 1 Plan 08, which has not run. Phase 2 has three options for this file:
1. **(Preferred) Block on Phase 1 Plan 08:** If main.go must exist before Phase 2 ends, Phase 1 Plan 08 must be executed first.
2. **Create main.go from scratch:** Phase 2 implements the Phase 1 Plan 08 spec AND wires the WebSocket handler. This is a scope expansion — flag in the plan.
3. **Defer wiring:** Phase 2 delivers `pkg/ws/*` with its own integration test binary; production wiring lands in Phase 1 Plan 08.

The planner must pick one explicitly.

**Imports pattern (when main.go is created/wired)** — extend Phase 1 `01-08-PLAN.md` main.go with:
```go
package main

import (
    "context"
    "net/http"
    "os"
    "os/signal"
    "syscall"

    "github.com/xiaotian/home-tutor-agent/pkg/infra"
    "github.com/xiaotian/home-tutor-agent/pkg/ws"     // NEW in Phase 2
)
```

**Core wiring pattern** (extends Phase 1 `01-08-PLAN.md` signal.NotifyContext + http.ServeMux):
```go
func main() {
    cfg := infra.MustLoadConfig(os.Getenv("CONFIG_PATH"))
    logger := infra.NewLogger(cfg.Logging)
    defer logger.Sync()

    // Phase 1 components (per 01-08-PLAN.md):
    memMonitor, _ := infra.NewMemoryMonitor(cfg.Memory, /* ... */)
    registry, _   := infra.NewConnectionRegistry(cfg.Session, /* ... */)
    pool, _       := infra.NewWorkerPool(cfg.Worker, logger)
    srv, _        := infra.NewServer(memMonitor, registry, pool, /* ... */, logger)

    // Phase 2 components (NEW):
    hub, _    := ws.NewHub(registry, cfg.WebSocket.Hub, logger)
    router, _ := ws.NewRouter(pool, cfg.WebSocket.Router, logger)
    wsSrv, _  := ws.NewServer(hub, router, cfg.WebSocket, logger)

    mux := http.NewServeMux()
    mux.HandleFunc("/ws", wsSrv.HandleWebSocket)
    // mux.Handle("/healthz", srv.HealthHandler()) — from Phase 1 Plan 08

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
    defer stop()

    go hub.Run(ctx)
    go srv.Start(ctx)
    logger.Info("listening", zap.String("addr", cfg.Server.ListenAddr))
    err := http.ListenAndServe(cfg.Server.ListenAddr, mux)
    if err != nil && err != http.ErrServerClosed {
        logger.Fatal("server error", zap.Error(err))
    }
}
```

---

### `go.mod` (modified, config)

**Analog:** Existing `go.mod` (lines 1-3).

**Current state:**
```
module github.com/xiaotian/home-tutor-agent
go 1.26
```

**Phase 2 additions** (RESEARCH.md "Installation", lines 96-101):
```
require (
    github.com/coder/websocket v1.8.15
    github.com/pkg/errors v0.9.1     // Phase 1 Wave 2 dep — may already be present
    go.uber.org/zap v1.27.0          // Phase 1 Wave 2 dep — may already be present
)
```

**Install command** (RESEARCH.md line 98, includes mainland-China goproxy fallback):
```bash
cd D:/workspace/ai/xiaotian
GOPROXY=https://goproxy.cn,direct go get github.com/coder/websocket@v1.8.15
GOPROXY=https://goproxy.cn,direct go get go.uber.org/zap@latest
GOPROXY=https://goproxy.cn,direct go get github.com/pkg/errors@latest
go mod tidy
```

**Open question for planner:** RESEARCH.md recommends `coder/websocket` over CONTEXT.md's locked `gorilla/websocket` (D-01). The plan must resolve this contradiction in its first task — see RESEARCH.md Open Question 1 (line 531) and the `user_constraints` block (lines 18-37) where D-01 remains "gorilla/websocket." Pick one and update D-01 (and D-02 "compression enabled" → "compression disabled" per RESEARCH.md Pitfall 1) in the plan's decisions.

---

## Shared Patterns

### Module + Package Conventions
**Source:** `go.mod` line 1; Phase 1 `01-01-SUMMARY.md`; README.md
**Apply to:** ALL new `.go` files.
- Module path: `github.com/xiaotian/home-tutor-agent`
- Package name matches directory basename: `package ws` in `pkg/ws/`, `package wstest` in `internal/wstest/`
- Import graph (D-23): `cmd → pkg/ws → pkg/infra → external libs`. No cycles.
- Local import grouping: stdlib first, blank line, external third-party, blank line, project-internal (`github.com/xiaotian/home-tutor-agent/...`) last. Enforced by `goimports -local github.com/xiaotian/home-tutor-agent` in `.golangci.yml` and `.pre-commit-config.yaml`.

### Thread Safety (D-11)
**Source:** Phase 1 `01-04-PLAN.md` line 24, `01-06-PLAN.md` line 41-47
**Apply to:** `hub.go`, `router.go`, `server.go`.
```go
// Use sync.RWMutex for read-heavy structures (client maps), sync.Mutex for write-heavy (state flags).
// Always pair Lock/Unlock with defer. Never hold a lock across a channel send or a blocking call.
type Hub struct {
    mu sync.RWMutex
    clients map[*Client]bool
}
func (h *Hub) has(c *Client) bool {
    h.mu.RLock(); defer h.mu.RUnlock()
    return h.clients[c]
}
```

### Context-Based Lifecycle (D-12)
**Source:** Phase 1 `01-04-PLAN.md` lines 25, 125-148; `01-06-PLAN.md` line 96
**Apply to:** Every long-running goroutine (`Hub.Run`, `Client.readPump`, `Client.writePump`, `Heartbeat`, `Router`).
```go
// Required shape — every goroutine MUST exit on ctx.Done():
for {
    select {
    case <-ctx.Done():
        return
    case x := <-someChan:
        // do work
    }
}
// Test contract (Phase 1): "goroutine exits within 50ms of ctx cancel"
```

### Constructor-Based DI (D-42, D-47)
**Source:** Phase 1 `01-04-PLAN.md` line 118 (NewMemoryMonitor signature); `01-06-PLAN.md`; `01-07-PLAN.md` line 107
**Apply to:** `NewHub`, `NewRouter`, `NewServer`, `NewClient`.
```go
// Signature: (cfg, dependencies..., logger) (*T, error)
// Validate cfg inside the constructor (D-44). Return wrapped sentinel on invalid.
// Substitute NoOp/Nop for nil optional deps (logger, etc.).
// Never read from globals; everything passed in.
```

### Custom Domain Types (D-40)
**Source:** Phase 1 `01-06-PLAN.md` lines 103-105
**Apply to:** `protocol.go` (MessageType, ProtocolVersion), `hub.go` (Client), `router.go` (Handler).
```go
// Typed strings and typed enums — prefer over raw string/int.
type MessageType uint8
type SessionID string // shared from pkg/infra, NOT redefined
// Add String() method on every typed enum for structured logging (zap.String("type", mt.String())).
```

### Structured Logging (D-25)
**Source:** Phase 1 `01-04-PLAN.md` lines 26, 146
**Apply to:** All `.go` files in `pkg/ws/`.
```go
// Use go.uber.org/zap. Every log carries component + key fields:
logger.Info("ws accept", zap.String("component", "ws.server"), zap.String("session_id", string(id)))
// Field name convention: snake_case. Component value: "ws.<subpkg-feature>" e.g. "ws.hub", "ws.server".
```

### Error Handling (D-44 + Phase 1 wrapping convention)
**Source:** Phase 1 `01-06-PLAN.md` line 156; `01-07-PLAN.md`
**Apply to:** All `.go` files.
```go
import "github.com/pkg/errors"

// Sentinel errors per package:
var ErrCRCChecksumFailed = errors.New("crc-16 checksum failed")

// Wrap at boundaries with descriptive context:
return errors.Wrap(ErrCRCChecksumFailed, "decode frame header")

// Constructor validation returns wrapped sentinels:
if cfg.Limit <= 0 {
    return nil, errors.Wrap(ErrInvalidHubConfig, "limit must be > 0")
}
```

### Panic Recovery (D-49)
**Source:** Phase 1 `01-07-PLAN.md` lines 128-153
**Apply to:** Worker callbacks in `router.go` (and read pumps if not pool-backed).
```go
defer func() {
    if r := recover(); r != nil {
        logger.Error("handler panic recovered",
            zap.String("component", "ws.router"),
            zap.Any("recovered", r),
            zap.Stack("stack"))
        // Do NOT re-panic. Worker pool survives. Metric: totalPanics++.
    }
}()
```

### Testing Conventions (D-29, D-33, D-48)
**Source:** Phase 1 `01-06-PLAN.md` test list; `01-04-PLAN.md` lines 199-202 (manual fakes); RESEARCH.md "Validation Architecture" lines 570-602
**Apply to:** All `_test.go` files.
- Framework: Go stdlib `testing` only. `httptest` for HTTP/WS. No testify/mock — D-48 mandates manual fakes.
- Test files co-located with source (`protocol_test.go` next to `protocol.go`).
- Subtest names: `TestXxx_Subcase` via `t.Run`. Required test names from RESEARCH.md table (lines 581-589) are mandatory.
- Quick run: `go test -v -short ./pkg/ws/...`; full: `go test -race -v ./pkg/ws/... ./internal/wstest/...` (D-33 race detector).
- Context-cancel tests: assert goroutine count returns to baseline within 50-500ms.

### Safari / Resource Constraints (RESEARCH.md Pitfalls + Phase 1 budget)
**Source:** RESEARCH.md lines 343-391; Phase 1 D-68 (150MB budget)
**Apply to:** `server.go`, `connection.go`, `heartbeat.go`, `hub.go`.
- `CompressionMode: websocket.CompressionDisabled` — mandatory (Safari truncation bug).
- `SetReadLimit(65536)` — 64KB ceiling prevents oversized-message DoS.
- All channels bounded (RESEARCH.md line 347). Memory per connection ≈ 16KB; 100 connections ≈ 1.6MB.
- Heartbeat 30s interval aligns with Phase 1 D-57 cleanup (RESEARCH.md Pitfall 2).

## No Analog Found

None — every Phase 2 file has either an intended-pattern analog (Phase 1 plan doc) or a reference analog (RESEARCH.md). The table below flags files where the "analog" is documentation, not running code, so the planner understands the higher confidence risk:

| File | Risk | Mitigation |
|------|------|------------|
| `pkg/ws/hub.go` | Phase 1 `pkg/infra/connection.go` (Plan 06) does not exist yet | Plan must either (a) block on Plan 06 execution, or (b) ship a self-contained minimal registry inline (RESEARCH.md Open Question 3) |
| `pkg/ws/router.go` | Phase 1 `pkg/infra/worker.go` (Plan 07) does not exist yet | Plan must either (a) block on Plan 07, or (b) ship a minimal `go`-per-message fallback (see `Route` fallback branch above) |
| `cmd/service/main.go` | Phase 1 Plan 08 has not run — file does not exist | Plan must explicitly choose: block, scope-expand to deliver Plan 08, or defer production wiring |
| `pkg/ws/server.go` | No prior HTTP handler in repo | Use RESEARCH.md "WebSocket 连接接受" + Phase 1 `01-08-PLAN.md` http.ServeMux pattern as the intended reference |
| All `pkg/ws/*` files | D-01 contradiction (CONTEXT locks gorilla/websocket, RESEARCH recommends coder/websocket) | First task of first plan must resolve: pick library, update D-01 and D-02 ("compression disabled") in plan decisions |

## Metadata

**Analog search scope:**
- `D:/workspace/ai/xiaotian/` (full repo, excluding `.git`, `.claude`, `.agents`)
- `.planning/phases/01-resource-constrained-infrastructure/*.md` (Phase 1 plan documents — 8 PLAN files, 1 SUMMARY, 1 CONTEXT, 1 RESEARCH, 1 VALIDATION)
- `.planning/phases/02-websocket-communication-layer/02-CONTEXT.md`, `02-RESEARCH.md`

**Existing Go source files scanned:** 0 (greenfield — only `.gitkeep` markers present in `cmd/service/`, `pkg/infra/`, `internal/`).

**Config files used as analogs:** `go.mod`, `Makefile`, `.golangci.yml`, `.pre-commit-config.yaml`, `.github/workflows/ci.yml`, `README.md`, `.gitignore`.

**Pattern extraction date:** 2026-07-19

**Key constraints surfaced:**
1. Greenfield — no running Go code to copy from; all analogs are documented intentions or external reference code.
2. Phase 1 Plan 06 (`pkg/infra/connection.go`) and Plan 07 (`pkg/infra/worker.go`) are blocked dependencies — planner must explicitly handle.
3. D-01 library contradiction (gorilla vs coder/websocket) must be resolved in the first plan task.
4. D-02 "compression enabled" must be updated to "compression disabled" — Safari incompatibility (RESEARCH.md Pitfall 1).
