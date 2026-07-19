# Phase 2: WebSocket Communication Layer - Context

**Gathered:** 2026-07-19
**Status:** Ready for planning

## Phase Boundary

This phase delivers the WebSocket communication infrastructure that enables real-time, bidirectional communication between iPad Safari browsers and the Go backend service. The focus is on establishing persistent WebSocket connections that can reliably handle 640B binary audio frames (20ms@16kHz PCM) with proper session registration, connection lifecycle management, and backpressure handling. This layer forms the communication foundation for subsequent phases that will build session management, TTS streaming, and real-time dictation workflows on top of this persistent connection infrastructure.

## Implementation Decisions

### WebSocket Library Selection
- **D-01:** Use `gorilla/websocket` library for WebSocket implementation — mature, stable, with proven Safari compatibility track record
- **D-02:** Configure gorilla/websocket with production-optimized settings — proper message size limits, read/write timeouts, compression enabled
- **D-03:** Enable message buffers for both read and write operations to balance performance and memory usage
- **D-04:** Implement connection limiting at application layer — consistent with D-56 decision from Phase 1, enforce 100 concurrent session limit after connection establishment

### Binary Frame Protocol Design  
- **D-05:** Use standard 16-byte header format — includes extension fields like timestamp, message length, and reserved fields for future use
- **D-06:** Implement extended message type set — audio data, control messages, error messages, heartbeat, session management, and status synchronization
- **D-07:** Use 32-bit continuous sequence numbering — provides longer continuous numbering without overflow, supports future protocol evolution
- **D-08:** Implement CRC-16 error detection for frame integrity — balances error detection capability with computational overhead
- **D-09:** Use hybrid frame boundary strategy — audio frames fixed at 640B (aligned with D-71), control messages use variable length with length field in header
- **D-10:** Use big-endian (network byte order) for all multi-byte fields — standard network protocol convention
- **D-11:** Include protocol version field (1 byte) in header to support future protocol evolution and backward compatibility

### Connection Lifecycle Management
- **D-12:** Use parameter handshake during WebSocket connection establishment — pass session parameters (user ID, device info) during HTTP upgrade to simplify subsequent session flow
- **D-13:** Implement Ping/Pong heartbeat mechanism for connection keepalive — periodic ping/pong frames to ensure connection remains active
- **D-14:** Use 30-second heartbeat interval — balances network overhead with connection detection speed, aligns with D-57 periodic cleanup routine
- **D-15:** Implement client-side reconnection strategy — clients handle reconnection attempts, server passively accepts reconnections and assists session recovery

### Message Processing and Routing
- **D-16:** Use hybrid concurrency strategy — one goroutine per connection for I/O (read/write), worker pool for business logic processing to leverage D-10 worker pool pattern
- **D-17:** Implement message routing via Go channels — natural Go idiom for message distribution between goroutines
- **D-18:** Implement complete testing pyramid — unit tests for core functionality, integration tests for end-to-end flows, E2E tests for full system validation
- **D-19:** Rely on gorilla/websocket library's Safari compatibility — leverage library's proven compatibility without additional Safari-specific workarounds

### Claude's Discretion
The following areas were left to Claude's discretion during discussion:
- Specific gorilla/websocket configuration parameter values (buffer sizes, timeout durations, compression settings)
- Frame protocol field layout within the 16-byte header (exact field positions and bit allocations)
- Message type enumeration values and binary encoding
- Channel buffer sizes and worker pool sizing for message processing
- Heartbeat ping/pong payload design
- Session recovery mechanism details for client reconnection
- Specific test coverage targets and test data generation strategies
- Error message format and error code definitions

## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project Requirements
- `.planning/REQUIREMENTS.md` — Complete v1 requirements for Home Tutor Agent, including Phase 2 requirements (WS-01, WS-02, WS-03, WS-04)
- `.planning/PROJECT.md` — Project context, constraints, and key technical decisions (Go WebSocket service, 150MB memory budget, 1C 1G VPS deployment)

### Roadmap & Phase Definition
- `.planning/ROADMAP.md` — Phase 2 goal, success criteria, and requirement mapping
- `.planning/ROADMAP.md#phase-2-websocket-communication-layer` — Specific Phase 2 definition with 4 success criteria

### Technical Constraints
- `.planning/PROJECT.md#constraints` — Infrastructure constraints (1C 1G VPS, 150MB Go service, 50MB audio buffer, 100 concurrent sessions, SQLite storage, <1.5s latency target)

### Prior Phase Context
- `.planning/phases/01-resource-constrained-infrastructure/01-CONTEXT.md` — Phase 1 decisions that inform Phase 2 implementation, especially connection management (D-54 through D-67), memory budgeting (D-68 through D-71), and backpressure strategy (D-72 through D-74)

## Existing Code Insights

### Reusable Assets
No existing WebSocket codebase assets — this is a greenfield WebSocket layer building on Phase 1 infrastructure foundation.

### Established Patterns
From Phase 1 implementation (01-01-SUMMARY.md):
- **Go module structure:** `github.com/xiaotian/home-tutor-agent` with go 1.26, layered cmd/pkg/internal directory layout
- **Build system:** Makefile-based build system with static binary output at `bin/service`
- **Import dependency rule:** Module dependency graph with imports flowing cmd → pkg/infra → external libraries (per D-23)
- **Concurrency pattern:** Worker pool pattern for concurrency control (D-10), thread-safe components with mutex protection (D-11)
- **Resource management:** Context-based resource cleanup and cancellation (D-12), sync.Pool for buffer reuse (D-14)
- **CI/CD:** GitHub Actions pipeline with vet, lint, race-enabled tests, and build verification
- **Code quality:** golangci-lint with security linters (gosec, govet, staticcheck), pre-commit hooks for formatting and linting

### Integration Points
- **pkg/infra package:** Phase 1 infrastructure components (memory monitoring, buffer pools, connection registry, worker pools) that Phase 2 WebSocket layer will integrate with
- **cmd/service entry point:** Main service entry point where WebSocket server will be initialized and registered
- **Configuration system:** Layered configuration system (defaults → file → env) that WebSocket configuration will extend
- **Logging infrastructure:** Structured logging system (Zap) that WebSocket events will utilize

## Specific Ideas

No specific external references or "I want it like X" moments were captured during discussion — all decisions focused on standard WebSocket implementation approaches adapted to the project's resource-constrained, iPad Safari-optimized requirements.

## Deferred Ideas

None — discussion stayed within Phase 2 scope (WebSocket Communication Layer). All discussions focused on persistent connection infrastructure, binary frame protocol design, and connection lifecycle management without scope creep into future phases (Python integration, TTS streaming, session state management, etc.).

---

*Phase: 02-WebSocket Communication Layer*
*Context gathered: 2026-07-19*
