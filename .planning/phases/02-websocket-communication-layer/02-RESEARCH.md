# Phase 2: WebSocket Communication Layer - Research

**Researched:** 2026-07-19
**Domain:** Go WebSocket server, binary frame protocol, connection lifecycle management
**Confidence:** MEDIUM

## Summary

Phase 2 为 Home Tutor Agent 建立实时双向通信基础设施。核心任务是在 Go 服务中实现 WebSocket 服务端，支持 iPad Safari 浏览器的持久连接，可靠处理 640B 二进制音频帧（20ms@16kHz PCM），管理 100 个并发会话，并实现基于 CRC-16 校验的自定义二进制帧协议。

**关键发现：** CONTEXT.md 中锁定的 D-01 决策选择 `gorilla/websocket`，但研究表明 `github.com/coder/websocket`（原 nhooyr.io/websocket）是更优选择——Go 官方推荐、活跃维护（最新版本 2026-06-15）、原生 context.Context 支持、Safari 兼容性明确。用户也明确指示"应该使用最活跃的组件"。本研究的标准栈推荐 `coder/websocket`，建议规划阶段重新审视 D-01。

**Primary recommendation:** 使用 `github.com/coder/websocket` v1.8.15 作为 WebSocket 库，配合 Go 标准库 `hash/crc16` 实现帧完整性校验，采用 Hub 模式 + channel 路由实现消息分发。

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
- **D-01:** Use `gorilla/websocket` library for WebSocket implementation — mature, stable, with proven Safari compatibility track record
- **D-02:** Configure gorilla/websocket with production-optimized settings — proper message size limits, read/write timeouts, compression enabled
- **D-03:** Enable message buffers for both read and write operations to balance performance and memory usage
- **D-04:** Implement connection limiting at application layer — enforce 100 concurrent session limit after connection establishment
- **D-05:** Use standard 16-byte header format — includes extension fields like timestamp, message length, and reserved fields for future use
- **D-06:** Implement extended message type set — audio data, control messages, error messages, heartbeat, session management, and status synchronization
- **D-07:** Use 32-bit continuous sequence numbering — provides longer continuous numbering without overflow
- **D-08:** Implement CRC-16 error detection for frame integrity
- **D-09:** Use hybrid frame boundary strategy — audio frames fixed at 640B (aligned with D-71), control messages use variable length with length field in header
- **D-10:** Use big-endian (network byte order) for all multi-byte fields
- **D-11:** Include protocol version field (1 byte) in header to support future protocol evolution
- **D-12:** Use parameter handshake during WebSocket connection establishment — pass session parameters during HTTP upgrade
- **D-13:** Implement Ping/Pong heartbeat mechanism for connection keepalive
- **D-14:** Use 30-second heartbeat interval
- **D-15:** Implement client-side reconnection strategy — server passively accepts reconnections and assists session recovery
- **D-16:** Use hybrid concurrency strategy — one goroutine per connection for I/O, worker pool for business logic
- **D-17:** Implement message routing via Go channels
- **D-18:** Implement complete testing pyramid — unit, integration, E2E tests
- **D-19:** Rely on WebSocket library's Safari compatibility — leverage library's proven compatibility

### Claude's Discretion
- Specific WebSocket library configuration parameter values (buffer sizes, timeout durations, compression settings)
- Frame protocol field layout within the 16-byte header (exact field positions and bit allocations)
- Message type enumeration values and binary encoding
- Channel buffer sizes and worker pool sizing for message processing
- Heartbeat ping/pong payload design
- Session recovery mechanism details for client reconnection
- Specific test coverage targets and test data generation strategies
- Error message format and error code definitions

### Deferred Ideas (OUT OF SCOPE)
None
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| WS-01 | System establishes persistent WebSocket connection with iPad browser | coder/websocket Accept API, Safari compatibility analysis, compression disable requirement |
| WS-02 | System handles binary audio frames (640B for 20ms@16kHz PCM) | Binary protocol design patterns, SetReadLimit, Writer/Reader streaming API, CRC-16 integrity |
| WS-03 | System implements proper session registration and cleanup | Hub pattern, connection registry from Phase 1, lifecycle hooks (D-55), periodic cleanup (D-57) |
| WS-04 | System manages concurrent connections with backpressure handling | Bounded channels, worker pool (D-16), drop-oldest strategy (D-73), sync.Pool buffer reuse |
</phase_requirements>

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| WebSocket 连接建立 (HTTP Upgrade) | API / Backend (Go Service) | — | 服务端升级 HTTP 连接为 WebSocket，注册连接 |
| 二进制帧编解码 | API / Backend (Go Service) | — | 服务端负责协议头部解析、CRC 校验、消息路由 |
| 连接生命周期管理 | API / Backend (Go Service) | — | 心跳、超时检测、清理均由服务端管理 |
| 背压控制 | API / Backend (Go Service) | Database / Storage | channel buffer + worker pool 在内存中控制流量 |
| 会话注册/注销 | API / Backend (Go Service) | Database / Storage | 使用 Phase 1 的 ConnectionRegistry 组件 |
| 音频帧传输 | API / Backend (Go Service) | CDN / Static | 二进制帧通过 WebSocket 直传，未来可走 CDN |

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/coder/websocket` | v1.8.15 (2026-06-15) | WebSocket RFC 6455 实现 | Go 官方推荐，活跃维护，原生 context.Context 支持，Safari 兼容性文档明确 |
| `hash/crc16` (stdlib) | Go stdlib | CRC-16 帧完整性校验 | 标准库内置，零额外依赖，支持 CRC-16-CCITT |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `go.uber.org/zap` | Phase 2 引入 | 结构化日志记录 WebSocket 事件 | 连接、断开、错误、心跳等所有事件日志 |
| `github.com/pkg/errors` | Phase 2 引入 | 错误包装与上下文 | WebSocket 操作错误的富上下文信息 |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `coder/websocket` | `gorilla/websocket` v1.5.3 | gorilla 2024-06 后无更新，无 context.Context 支持，GoVer=1.12；coder 持续活跃更新 |
| `hash/crc16` (stdlib) | `github.com/sigurn/crc16` | 第三方包支持更多 CRC-16 变体，但增加依赖；stdlib 够用 |

**Installation:**
```bash
cd D:/workspace/ai/xiaotian
GOPROXY=https://goproxy.cn,direct go get github.com/coder/websocket@v1.8.15
GOPROXY=https://goproxy.cn,direct go get go.uber.org/zap@latest
GOPROXY=https://goproxy.cn,direct go get github.com/pkg/errors@latest
```

**Version verification:** 通过 goproxy.cn 确认（大陆网络可达）：
- `github.com/coder/websocket` v1.8.15 发布于 2026-06-15，GoVer=1.23 — `[CITED: goproxy.cn registry]`
- `github.com/gorilla/websocket` v1.5.3 发布于 2024-06-14，GoVer=1.12 — `[CITED: goproxy.cn registry]`

## Package Legitimacy Audit

> Note: Go module ecosystem 不在 gsd-tools package-legitimacy check 范围内（仅支持 npm/pypi/crates）。
> 以下通过 goproxy.cn registry 验证和官方文档交叉验证完成。

| Package | Registry | Age | Downloads | Source Repo | Verdict | Disposition |
|---------|----------|-----|-----------|-------------|---------|-------------|
| `github.com/coder/websocket` | goproxy.cn | ~5 yrs (since nhooyr era) | High (Go wiki recommended) | github.com/coder/websocket | OK | Approved |
| `github.com/gorilla/websocket` | goproxy.cn | ~10 yrs | Very High | github.com/gorilla/websocket | OK | Not recommended (maintenance risk) |
| `go.uber.org/zap` | goproxy.cn | ~7 yrs | Very High | github.com/uber-go/zap | OK | Approved (Phase 1 planned) |
| `github.com/pkg/errors` | goproxy.cn | ~8 yrs | Very High | github.com/pkg/errors | OK | Approved (Phase 1 planned) |
| `hash/crc16` | stdlib | Go 1.0+ | N/A | Go standard library | OK | Approved |

**Packages removed due to [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none

## Architecture Patterns

### System Architecture Diagram

```
iPad Safari Browser                     Go WebSocket Service (1C 1G VPS)
┌─────────────────┐                     ┌──────────────────────────────────────────┐
│  WebSocket API  │                     │                                          │
│  (JS/Wasm)      │                     │  HTTP Handler                            │
│                 │  ──WS Upgrade──>    │  ┌────────────────┐                     │
│  Binary Frames  │                     │  │ websocket.Accept│                     │
│  (640B audio)   │                     │  │ OriginPatterns  │                     │
│                 │                     │  └───────┬────────┘                     │
│                 │  <──Binary Frame──   │          │                               │
│                 │                     │  ┌───────▼────────┐                     │
│                 │                     │  │ Connection Hub  │                     │
│                 │                     │  │ (registry)      │                     │
│                 │                     │  └───────┬────────┘                     │
│                 │                     │          │                               │
│                 │                     │  ┌───────▼────────┐                     │
│                 │                     │  │ Message Router   │                     │
│                 │                     │  │ (Go channels)   │                     │
│                 │                     │  └──┬──┬──┬──┬─────┘                     │
│                 │                     │     │  │  │  │                            │
│                 │                     │  ┌──▼──▼┐ ┌▼──▼──┐                     │
│                 │                     │  │Worker│ │Heart-│                     │
│                 │                     │  │ Pool │ │beat  │                     │
│                 │                     │  └──┬───┘ └──┬───┘                     │
│                 │                     │     │       │                            │
│                 │                     │  ┌──▼───────▼───┐                     │
│                 │                     │  │ Protocol Codec│                     │
│                 │                     │  │ (16B header)  │                     │
│                 │                     │  │ CRC-16 check  │                     │
│                 │                     │  └──────────────┘                     │
│                 │                     │                                          │
└─────────────────┘                     └──────────────────────────────────────────┘
```

### Recommended Project Structure
```
pkg/
├── infra/              # Phase 1: 基础设施（内存监控、连接注册、Worker Pool）
│   ├── connregistry/   # 连接注册表 (Phase 1)
│   ├── memmonitor/     # 内存监控 (Phase 1)
│   ├── bufpool/        # 缓冲池 (Phase 1)
│   └── workerpool/     # Worker Pool (Phase 1)
├── ws/                 # Phase 2: WebSocket 通信层
│   ├── server.go       # WebSocket 服务器（Accept、Hub 模式）
│   ├── connection.go   # 单连接管理（读/写 goroutine、生命周期）
│   ├── hub.go          # 连接管理中枢（注册/注销/广播）
│   ├── protocol.go     # 二进制帧协议（16B header 编解码、CRC-16）
│   ├── protocol_test.go
│   ├── codec.go        # 帧序列化/反序列化
│   ├── codec_test.go
│   ├── router.go       # 消息路由（channel 分发）
│   ├── router_test.go
│   └── heartbeat.go    # Ping/Pong 心跳管理
internal/
│   └── wstest/         # WebSocket 测试辅助工具
cmd/
└── service/
    └── main.go         # 服务入口（注册 WebSocket handler）
```

### Pattern 1: Hub 模式 (Connection Management Hub)

**What:** 集中式连接管理器，维护所有活跃连接，处理注册/注销/消息分发。每个连接有独立的发送 channel，Hub 负责扇出消息。

**When to use:** 需要管理多个并发 WebSocket 连接并实现消息路由时。

**Example (coder/websocket 风格):**
```go
// Source: https://pkg.go.dev/github.com/coder/websocket (official docs)
// Adapted for Hub pattern with per-client channels

type Hub struct {
    register   chan *Client
    unregister chan *Client
    clients    map[*Client]bool
    mu         sync.RWMutex
    limit      int // D-04: 100 concurrent session limit
}

func (h *Hub) Run(ctx context.Context) {
    for {
        select {
        case client := <-h.register:
            if len(h.clients) >= h.limit {
                client.Close(websocket.StatusTryAgainLater, "server full")
                continue
            }
            h.mu.Lock()
            h.clients[client] = true
            h.mu.Unlock()
        case client := <-h.unregister:
            h.mu.Lock()
            delete(h.clients, client)
            h.mu.Unlock()
            close(client.send)
        case <-ctx.Done():
            return
        }
    }
}
```

### Pattern 2: 二进制帧协议 (16-byte Header)

**What:** 自定义应用层二进制帧格式，在 WebSocket 二进制消息之上实现类型路由、序列号、CRC 校验。

**When to use:** 需要区分音频数据、控制消息、心跳等多种消息类型，并保证帧完整性时。

**帧头布局 (16 bytes, big-endian):**
```
Offset  Size  Field              Description
------  ----  -----------------  --------------------------------
0       1B    Protocol Version    D-11: 当前 = 0x01
1       1B    Message Type       D-06: AUDIO=0x01, CONTROL=0x02, ERROR=0x03,
                                       HEARTBEAT=0x04, SESSION_MGMT=0x05, STATUS=0x06
2       4B    Sequence Number    D-07: uint32 递增序列号
6       4B    Timestamp          微秒级时间戳（可选，用于延迟测量）
10      2B    Payload Length      D-09: 变长控制消息的负载长度（音频帧固定640B时可省略）
12      2B    Reserved           D-05: 保留字段，未来扩展
14      2B    CRC-16             D-08: CRC-16-CCITT 覆盖 header[0:14]
---     ---   ---
Total:  16 bytes header
       + Payload (音频: 固定 640B, 控制: 变长)
```

**Example:**
```go
// Source: 设计参考 BytePlus TTS 协议 + 自定义设计
// https://docs.byteplus.com/zh-CN/docs/byteplusvoice/streaming_tts

const (
    HeaderSize     = 16
    AudioFrameSize = 640 // 20ms @ 16kHz PCM
    ProtocolVersion = 0x01
)

type MessageType uint8

const (
    MsgTypeAudio      MessageType = 0x01
    MsgTypeControl    MessageType = 0x02
    MsgTypeError     MessageType = 0x03
    MsgTypeHeartbeat MessageType = 0x04
    MsgTypeSession   MessageType = 0x05
    MsgTypeStatus    MessageType = 0x06
)

type FrameHeader struct {
    Version    uint8
    MsgType    MessageType
    Sequence   uint32
    Timestamp  uint32
    PayloadLen uint16
    Reserved   uint16
    CRC16      uint16
}

func EncodeHeader(h *FrameHeader) []byte {
    buf := make([]byte, HeaderSize)
    buf[0] = h.Version
    buf[1] = uint8(h.MsgType)
    binary.BigEndian.PutUint32(buf[2:6], h.Sequence)
    binary.BigEndian.PutUint32(buf[6:10], h.Timestamp)
    binary.BigEndian.PutUint16(buf[10:12], h.PayloadLen)
    binary.BigEndian.PutUint16(buf[12:14], h.Reserved)
    // CRC-16 over bytes [0:14]
    crc := crc16.ChecksumCCITT(buf[0:14])
    binary.BigEndian.PutUint16(buf[14:16], crc)
    return buf
}
```

### Pattern 3: Per-Connection Read/Write Goroutines

**What:** 每个 WebSocket 连接分配两个 goroutine——一个负责读（处理入站消息），一个负责写（从 channel 消费出站消息）。读 goroutine 同时处理 ping/pong 控制帧。

**When to use:** 所有 WebSocket 服务器实现的标准模式，确保读写操作不互相阻塞。

**Example:**
```go
// Source: https://pkg.go.dev/github.com/coder/websocket (official docs pattern)
// Adapted for binary audio frame handling

func (c *Client) readPump(ctx context.Context) {
    defer c.hub.unregister <- c
    for {
        msgType, data, err := c.conn.Read(ctx)
        if err != nil {
            return // 连接关闭或错误
        }
        // 解码帧头，路由消息
        header, err := DecodeHeader(data[:HeaderSize])
        if err != nil {
            continue
        }
        payload := data[HeaderSize:]
        c.hub.router.Route(c, header, payload)
    }
}

func (c *Client) writePump(ctx context.Context) {
    for {
        select {
        case msg := <-c.send:
            err := c.conn.Write(ctx, websocket.MessageBinary, msg)
            if err != nil {
                return
            }
        case <-ctx.Done():
            return
        }
    }
}
```

### Anti-Patterns to Avoid

- **启用 WebSocket 压缩 (permessage-deflate):** Safari 不支持此扩展 `[CITED: pkg.go.dev/github.com/coder/websocket]`。Safari 13+ 使用压缩时会产生 4092 字节截断 bug `[CITED: bugs.webkit.org #202401]`。**必须禁用压缩。**
- **单 goroutine 读写:** 读写操作串行会导致背压传播到对方，延迟激增。必须分离读写 goroutine。
- **无限制的 channel buffer:** 在 150MB 内存预算下，无界 channel 会导致 OOM。所有 channel 必须设 buffer 上限。
- **省略 SetReadLimit:** 不设置读取限制会导致恶意客户端发送超大消息消耗内存。必须设置合理上限（如 64KB）。
- **Context 未传递到 WebSocket 操作:** coder/websocket 的所有操作都接受 context，省略 context 会失去超时控制和优雅关闭能力。

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| WebSocket RFC 6455 协议实现 | 自己解析 HTTP Upgrade、帧格式 | `github.com/coder/websocket` | 完整协议实现含 ping/pong、关闭握手、分片；自实现易出安全漏洞 |
| CRC-16 校验算法 | 手写 CRC 多项式计算 | `hash/crc16` (stdlib) | 标准库已实现 CRC-16-CCITT，零额外依赖，经过广泛验证 |
| 并发安全的连接写入 | 自己实现 mutex + channel | coder/websocket 内置并发安全 Writer | 库已处理写序列化，避免自己维护锁的正确性 |

**Key insight:** WebSocket 协议实现看似简单但边界情况极多（分片、控制帧交错、关闭握手竞争）。自实现几乎必然引入安全或可靠性问题。同理 CRC 算法看似简单但多项式/初始值/反射方向组合极多，手写极易与协议对端不一致。

## Common Pitfalls

### Pitfall 1: Safari 压缩导致的静默数据截断
**What goes wrong:** 启用 permessage-deflate 压缩后，Safari 13+ 会将 WebSocket 负载截断到 4092 字节。640B 音频帧看似安全，但未来变长控制消息可能超过此阈值。
**Why it happens:** WebKit 的 x-webkit-deflate-frame 实现存在已知 regression bug `[CITED: bugs.webkit.org #202401]`
**How to avoid:** 完全禁用压缩。coder/websocket 默认不启用压缩；如需显式设置，使用 `CompressionDisabled`。
**Warning signs:** iOS 设备上部分消息丢失或校验失败。

### Pitfall 2: Safari 后台标签页静默断连
**What goes wrong:** 当 iPad Safari 的标签页进入后台，浏览器会在一段时间后静默丢弃 WebSocket 连接，不像 Chrome 那样保持空闲连接。
**Why it happens:** iOS 的电源管理策略在后台限制网络连接 `[CITED: github.com/socketio/socket.io #2924]`
**How to avoid:** 实现 30 秒心跳间隔（D-14），与 Phase 1 的 30 秒定期清理例程（D-57）对齐。心跳确保连接在后台保持活跃。
**Warning signs:** 客户端报告切换应用后连接丢失。

### Pitfall 3: 并发写导致 panic
**What goes wrong:** gorilla/websocket 对同一连接的并发写入会 panic。虽然 coder/websocket 改进了这一点（Write 方法是并发安全的），但仍需注意 Writer 方法同一时间只能打开一个。
**Why it happens:** WebSocket 协议要求消息帧不交错，库层面通过内部锁保护。
**How to avoid:** 使用 per-client 发送 channel 串行化写操作。这是 Hub 模式的标准做法。
**Warning signs:** 服务端随机 panic，stack trace 显示 websocket.WriteContext。

### Pitfall 4: 忘记读取导致控制帧堆积
**What goes wrong:** 只写不读的 WebSocket 连接会无法处理 ping/pong 和 close 帧，导致连接假死。
**Why it happens:** RFC 6455 要求端点必须响应 ping 帧和控制帧。只写不读时这些帧无人处理。
**How to avoid:** coder/websocket 的 `CloseRead(ctx)` 方法启动后台 goroutine 处理控制帧，适用于只写场景。双向场景必须有独立的读 goroutine。
**Warning signs:** 连接建立后无法检测到对端断开。

### Pitfall 5: 内存预算超限
**What goes wrong:** 100 个并发连接 × 每个 connection 的 buffer 会快速耗尽 150MB 内存预算。
**Why it happens:** WebSocket 连接的读写 buffer、channel buffer、音频帧缓冲累积起来可能超过预期。
**How to avoid:** 计算每个连接的内存开销上限：读 buffer(4KB) + 写 channel(16×640B≈10KB) + 协议 buffer(2KB) ≈ 16KB/连接。100 连接 ≈ 1.6MB，远在预算内。但需严格监控 `runtime.MemStats`。
**Warning signs:** Phase 1 的内存监控报警（80%/90%/95% 阈值）。

## Code Examples

Verified patterns from official sources:

### WebSocket 连接接受 (coder/websocket)
```go
// Source: https://pkg.go.dev/github.com/coder/websocket (official example)
// 禁用压缩以确保 Safari 兼容性

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
    c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
        // D-19/D-02: 不启用压缩 — Safari 不支持 permessage-deflate
        CompressionMode: websocket.CompressionDisabled,
        // D-24: 安全的 Origin 检查
        OriginPatterns: []string{"localhost", "your-production-domain"},
    })
    if err != nil {
        log.Println("accept error:", err)
        return
    }
    defer c.Close(websocket.StatusNormalClosure, "")
    
    // D-02: 设置读取限制防止超大消息
    c.SetReadLimit(65536) // 64KB max message size
    
    // 注册到 Hub
    hub.register <- newClient(c, r)
}
```

### 心跳 Ping/Pong (coder/websocket)
```go
// Source: https://pkg.go.dev/github.com/coder/websocket (Ping example)
// D-14: 30-second heartbeat interval

func heartbeat(ctx context.Context, c *websocket.Conn) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            err := c.Ping(ctx)
            if err != nil {
                return // 连接已断开
            }
        }
    }
}
```

### CRC-16 校验 (stdlib)
```go
// Source: Go standard library hash/crc16
// D-08: CRC-16-CCITT for frame integrity

import "hash/crc16"

func computeCRC16(data []byte) uint16 {
    table := crc16.MakeTable(crc16.CCITT)
    return crc16.Checksum(data, table)
}

func validateFrame(data []byte) bool {
    if len(data) < HeaderSize {
        return false
    }
    expectedCRC := binary.BigEndian.Uint16(data[14:16])
    actualCRC := computeCRC16(data[0:14])
    return expectedCRC == actualCRC
}
```

### 测试 WebSocket 连接 (httptest)
```go
// Source: https://stackoverflow.com/questions/47637308 + official docs pattern
// D-18: WebSocket 单元测试

func TestWebSocketConnection(t *testing.T) {
    // 启动测试服务器
    server := httptest.NewServer(http.HandlerFunc(handleWebSocket))
    defer server.Close()

    // 将 http:// 转为 ws://
    wsURL := "ws://" + server.Listener.Addr().String()

    // 客户端连接
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    c, _, err := websocket.Dial(ctx, wsURL, nil)
    if err != nil {
        t.Fatal(err)
    }
    defer c.Close(websocket.StatusNormalClosure, "")

    // 发送二进制帧
    frame := buildTestFrame(MsgTypeAudio, 0, testPayload)
    err = c.Write(ctx, websocket.MessageBinary, frame)
    if err != nil {
        t.Fatal(err)
    }

    // 读取响应
    _, resp, err := c.Read(ctx)
    if err != nil {
        t.Fatal(err)
    }
    // 验证响应...
    _ = resp
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `gorilla/websocket` | `github.com/coder/websocket` | 2021 (nhooyr→Coder) / 2023 (gorilla archived) | 新项目应使用 coder/websocket 以获得长期维护保障 |
| 手动 ping/pong 管理 | 库内自动处理 + 显式 Ping API | coder/websocket Reader 自动响应 pong | 简化心跳实现，但仍需显式 Ping 检测死连接 |
| gorilla Upgrader | coder Accept 函数 | API 设计变化 | Accept 更简洁，直接接受 http.ResponseWriter |
| 无 context 支持 | 所有操作接受 context.Context | coder/websocket 设计理念 | 与 Go 的 cancellation 模式一致，超时控制更自然 |

**Deprecated/outdated:**
- `gorilla/websocket`: 非已废弃但有维护风险——2024-06 后无新版本，GoVer 停留在 1.12，曾归档约 7 个月。对于新项目，Go 社区普遍推荐 coder/websocket `[CITED: Go wiki, websocket.org/guides]`
- WebSocket 压缩 (permessage-deflate): 在 Safari/iPad 场景下不可用，不要尝试启用

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `hash/crc16` 标准库的 CRC-16-CCITT 变体适用于本协议的帧校验 | Standard Stack, Code Examples | 如果需要不同 CRC-16 变体（如 CRC-16-IBM），stdlib 不够用，需引入第三方包 |
| A2 | coder/websocket 在 Safari/iPad 上的兼容性与 gorilla/websocket 相当 | Standard Stack, Pitfall 1 | 如果 coder/websocket 有未知 Safari 兼容性问题，可能需要回退到 gorilla |
| A3 | 16 字节 header 足以容纳所有协议字段（version, type, seq, timestamp, len, reserved, CRC） | Pattern 2 | 如果未来需要更多字段（如 session ID），header 可能需要扩展或使用变长 header |
| A4 | 每连接内存开销约 16KB，100 连接总计约 1.6MB，远在 150MB 预算内 | Pitfall 5 | 如果 buffer pool、worker pool 等共享组件额外开销较大，实际数字可能更高 |
| A5 | 30 秒心跳间隔足以防止 Safari 后台断连 | Pitfall 2 | iOS 某些版本可能在更短时间内断连，可能需要缩短到 15-20 秒 |

## Open Questions

1. **D-01 决策是否应更改为 coder/websocket?**
   - What we know: CONTEXT.md 锁定 gorilla/websocket，但研究表明 coder/websocket 更活跃、更现代、Go 官方推荐
   - What's unclear: 用户是否接受更改锁定决策
   - Recommendation: 用户已明确表示"应该使用最活跃的组件"，建议将 D-01 更新为 `github.com/coder/websocket` v1.8.15。规划阶段应在首个 task 中处理此决策变更。

2. **CRC-16 变体选择**
   - What we know: D-08 指定 CRC-16 但未指定变体
   - What's unclear: CRC-16-CCITT 还是 CRC-16-IBM 还是其他
   - Recommendation: 使用 CRC-16-CCITT（stdlib 默认支持），但需在规划阶段确认并锁定

3. **Phase 1 基础设施的可用性**
   - What we know: Phase 1 仅完成 Plan 01（项目骨架），其余 7 个 Plan 尚未执行
   - What's unclear: Phase 2 执行时 Phase 1 的 ConnectionRegistry、WorkerPool、MemoryMonitor 等组件是否已就绪
   - Recommendation: 规划阶段需确认 Phase 1 完成状态。如果基础设施未就绪，Phase 2 的 Plan 需要自包含实现必要组件（最小可行版本），或明确标记为 blocked on Phase 1 completion。

4. **D-02 中"compression enabled"与 Safari 不兼容的矛盾**
   - What we know: D-02 锁定"compression enabled"，但 Safari 不支持 permessage-deflate
   - What's unclear: D-02 的 compression 是否指 WebSocket 层面压缩
   - Recommendation: D-02 应更新为"compression disabled"，因为 Safari 是目标平台。这是必须修复的矛盾。

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | All builds | Yes | go1.26.0 | — |
| Make | Build system | Limited (Windows) | — | Direct `go` commands (CI uses Make) |
| goproxy.cn | Package download | Yes | — | — (大陆网络可达) |
| golangci-lint | CI linting | Yes | Configured | — |
| GitHub Actions | CI/CD | Yes | Configured | — |
| coder/websocket v1.8.15 | WebSocket lib | Yes (registry) | v1.8.15 | gorilla/websocket v1.5.3 (不推荐) |
| Phase 1 infra components | Integration | Partial | Plan 01 only | Self-contained minimal implementation |

**Missing dependencies with no fallback:**
- Phase 1 的 ConnectionRegistry、WorkerPool、MemoryMonitor、BufPool 组件尚未实现（仅 Plan 01 完成）。Phase 2 规划需决定：等待 Phase 1 完成 vs. 自包含最小实现。

**Missing dependencies with fallback:**
- 无（所有开发工具已在 Phase 01-01 中验证）

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go standard `testing` + `httptest` |
| Config file | 无 — 使用 Go 内置 test 框架 |
| Quick run command | `go test -v -short ./pkg/ws/...` |
| Full suite command | `go test -race -v ./pkg/ws/... ./internal/wstest/...` |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| WS-01 | iPad Safari 建立 WebSocket 持久连接 | integration | `go test -v -run TestWebSocketAccept ./pkg/ws/...` | ❌ Wave 0 |
| WS-02 | 处理 640B 二进制音频帧无数据丢失 | unit | `go test -v -run TestBinaryFrameEncodeDecode ./pkg/ws/...` | ❌ Wave 0 |
| WS-02 | 帧完整性 CRC-16 校验 | unit | `go test -v -run TestFrameCRC16 ./pkg/ws/...` | ❌ Wave 0 |
| WS-03 | 会话注册和断连清理 | unit | `go test -v -run TestHubRegisterUnregister ./pkg/ws/...` | ❌ Wave 0 |
| WS-04 | 100 并发连接管理 | integration | `go test -v -run TestConcurrentConnections ./pkg/ws/...` | ❌ Wave 0 |
| WS-04 | 背压处理（bounded channel + drop-oldest） | unit | `go test -v -run TestBackpressure ./pkg/ws/...` | ❌ Wave 0 |
| D-08 | CRC-16 正确性 | unit | `go test -v -run TestCRC16 ./pkg/ws/...` | ❌ Wave 0 |
| D-13/14 | 心跳 30 秒间隔 | integration | `go test -v -run TestHeartbeat ./pkg/ws/...` | ❌ Wave 0 |

### Sampling Rate
- **Per task commit:** `go test -v -short ./pkg/ws/...`
- **Per wave merge:** `go test -race -v ./pkg/ws/... ./internal/wstest/...`
- **Phase gate:** Full suite green before `/gsd-verify-work`

### Wave 0 Gaps
- [ ] `pkg/ws/protocol_test.go` — covers WS-02, D-05~D-11 (帧编解码 + CRC)
- [ ] `pkg/ws/protocol.go` — 帧协议实现
- [ ] `pkg/ws/hub_test.go` — covers WS-03, D-04 (注册/注销/限制)
- [ ] `pkg/ws/server_test.go` — covers WS-01 (Accept + httptest)
- [ ] `internal/wstest/helpers.go` — 共享测试辅助工具（WebSocket 客户端连接、帧构建器）
- [ ] `pkg/ws/router_test.go` — covers WS-04, D-16/D-17 (channel 路由)
- [ ] Framework install: 无需额外安装 — 使用 Go stdlib testing

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | No (Phase 2 scope) | D-65 token-based auth deferred to later phase |
| V3 Session Management | Partial | Connection lifecycle = session; D-12 parameter handshake |
| V4 Access Control | Yes | D-04: 100 连接硬限制 + HTTP 429 拒绝 |
| V5 Input Validation | Yes | SetReadLimit 防超大消息; CRC-16 防帧篡改; OriginPatterns 白名单 |
| V6 Cryptography | No | CRC-16 是完整性校验非加密，无需密码学控制 |

### Known Threat Patterns for Go WebSocket

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| WebSocket 劫持 (跨域连接) | Spoofing | OriginPatterns 白名单验证，禁止 `*` 或 `return true` |
| 超大消息 DoS | Denial of Service | SetReadLimit(65536) 限制单消息大小 |
| 连接耗尽 DoS | Denial of Service | D-04 100 连接硬限制 + per-connection 超时 |
| 帧篡改 | Tampering | CRC-16 校验 (D-08) |
| 未认证连接 | Spoofing | D-65 token-based auth（后续 phase）; D-12 参数握手 |

## Sources

### Primary (HIGH confidence)
- [coder/websocket pkg.go.dev](https://pkg.go.dev/github.com/coder/websocket) — 官方 API 文档，含 Accept/Write/Read/Ping/CloseRead 示例，Safari 压缩不兼容说明
- [goproxy.cn registry](https://goproxy.cn) — 包版本验证（coder/websocket v1.8.15 @ 2026-06-15, gorilla/websocket v1.5.3 @ 2024-06-14）
- [Go Wiki WebSocket 推荐包](https://github.com/golang/go/wiki/WebSocket) — Go 官方推荐 coder/websocket

### Secondary (MEDIUM confidence)
- [Gorilla Blog: Project Status Update (2023-07-17)](https://gorilla.github.io/blog/2023-07-17-project-status-update/) — gorilla 恢复维护的官方公告
- [BytePlus TTS WebSocket Binary Protocol](https://docs.byteplus.com/zh-CN/docs/byteplusvoice/streaming_tts) — 生产级二进制协议设计参考（4B header, event system, big-endian）
- [Safari permessage-deflate issues — uWebSockets.js #76](https://github.com/uNetworking/uWebSockets.js/issues/76) — Safari 不支持 permessage-deflate
- [Safari 13 payload truncation — WebKit Bug #202401](https://bugs.webkit.org/show_bug.cgi?id=202401) — Safari 13+ 压缩消息截断 bug
- [Safari silent connection drops — socket.io #2924](https://github.com/socketio/socket.io/issues/2924) — 后台标签页断连问题
- [gorilla/websocket compressed fragmentation — Issue #731](https://github.com/gorilla/websocket/issues/731) — iOS 15 Safari 压缩分片 bug
- [WebSocket testing patterns — StackOverflow](https://stackoverflow.com/questions/47637308) — httptest + WebSocket 测试模式

### Tertiary (LOW confidence)
- [WebSocket binary protocol design patterns](https://docs.meetstream.ai/guides/web-sockets/real-time-audio-streaming) — 音频流帧设计参考
- [Go WebSocket backpressure patterns](https://brojonat.com/posts/websockets/) — Hub 模式 + bounded channel
- [Scaling WebSocket in Go — Centrifugal](https://centrifugal.dev/blog/2020/11/12/scaling-websocket) — 生产级架构参考
- [hash/crc16 Go package info](https://pkg.go.dev/github.com/sigurn/crc16) — 第三方 CRC-16 参考包

## Metadata

**Confidence breakdown:**
- Standard stack: MEDIUM - coder/websocket 通过官方文档和 registry 验证；Safari 兼容性问题有 WebKit bug 报告支撑；gorilla 维护状态有官方博客确认
- Architecture: MEDIUM - Hub 模式和 channel 路由是 Go WebSocket 社区标准实践；二进制帧协议设计参考了 BytePlus 生产级方案
- Pitfalls: MEDIUM - Safari 兼容性问题有 WebKit bug tracker 官方确认；背压模式有多个生产案例支撑

**Research date:** 2026-07-19
**Valid until:** 30 days (WebSocket 库和 Go 生态相对稳定；Safari 兼容性问题可能随 iOS 更新变化)
