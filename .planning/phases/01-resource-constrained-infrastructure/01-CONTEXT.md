# Phase 1: Resource-Constrained Infrastructure - Context

**Gathered:** 2025-07-18
**Status:** Ready for planning

## Phase Boundary

This phase delivers the foundational Go service infrastructure with resource management capabilities — the base upon which all other phases will build. This includes process lifecycle, memory budgeting (150MB limit), connection limiting (100 concurrent sessions), and logging infrastructure. The focus is on establishing a production-ready service that can operate within strict 1C 1G VPS constraints while providing the platform for WebSocket communication, Python integration, and real-time audio streaming in subsequent phases.

## Implementation Decisions

### Service Architecture
- **D-01:** Layered package structure (cmd/pkg/internal) for clean separation of concerns
- **D-02:** Single infrastructure package for Phase 1 to keep resource management cohesive
- **D-03:** Environment-based configuration with layered sources (defaults → file → env) and typed schema
- **D-04:** Startup validation for configuration — fail fast with clear error messages
- **D-05:** Enhanced error handling using pkg/errors library with rich context and custom error types
- **D-06:** Static binary output for deployment simplicity
- **D-07:** Make-based build system for Go service compilation
- **D-08:** Rich Go ecosystem dependencies (logging libraries, error wrapping, etc.)
- **D-09:** Standard Go project layout following community conventions
- **D-10:** Worker pool pattern for concurrency control
- **D-11:** Thread-safe components with mutex protection
- **D-12:** Context-based resource cleanup and cancellation
- **D-13:** Runtime memory tracking for 150MB budget enforcement
- **D-14:** sync.Pool for buffer reuse and memory efficiency
- **D-15:** Circuit breaker pattern for component failure resilience
- **D-16:** Component redundancy for high availability
- **D-17:** Centralized state management across components
- **D-18:** Proactive load shedding when approaching resource limits
- **D-19:** Startup-only configuration (no runtime reload for Phase 1)
- **D-20:** Single instance deployment (no service discovery for Phase 1)
- **D-21:** Graceful drain on shutdown with explicit signal handling (SIGTERM, SIGINT)
- **D-22:** Configurable timeouts for all operations
- **D-23:** Explicit module dependency graph (no circular dependencies)
- **D-24:** Security basics included from the start (input validation, no hardcoded secrets)
- **D-25:** Rich context logging with structured fields (request ID, component, operation)
- **D-26:** Service-level metrics (not per-component for Phase 1)
- **D-27:** Detailed health checks for each infrastructure component
- **D-28:** Table-driven tests for infrastructure components
- **D-29:** Integration tests included for Phase 1
- **D-30:** Coverage tracked but not enforced (focus on critical paths)
- **D-31:** Pre-commit hooks for formatting and linting
- **D-32:** GitHub Actions for CI/CD with static analysis
- **D-33:** Race detector enabled in CI tests
- **D-34:** Combined documentation approach (godoc + README files)
- **D-35:** Semantic versioning for releases
- **D-36:** Deferred performance benchmarking (focus on correctness first)
- **D-37:** Middleware pattern for cross-cutting concerns (logging, metrics, recovery)
- **D-38:** Scoped constants per package for better encapsulation
- **D-39:** Mocking library for testing infrastructure components
- **D-40:** Custom types for domain concepts (SessionID, MemoryBytes) for type safety
- **D-41:** Build tags for conditional compilation
- **D-42:** Constructor-based dependency injection for testability
- **D-43:** Small, focused interfaces following Single Responsibility Principle
- **D-44:** Centralized validation layer for configuration and inputs
- **D-45:** Context usage where needed (long-running operations)
- **D-46:** Bare metal deployment first (no containerization for Phase 1)
- **D-47:** Constructor DI pattern over global state
- **D-48:** Manual fakes for testing (no heavy mocking frameworks)
- **D-49:** Panic recovery middleware from the start
- **D-50:** Godoc generation for API documentation
- **D-51:** Prometheus format for metrics exposure
- **D-52:** Layered configuration sources with precedence
- **D-53:** Typed configuration struct for type safety

### Connection Management
- **D-54:** Map-based connection registry for O(1) connection lookup
- **D-55:** Lifecycle hooks (onConnect, onDisconnect, onError) for connection events
- **D-56:** Hard limit of 100 concurrent sessions with HTTP 429 rejection when at capacity
- **D-57:** Periodic cleanup routine (every 30s) for stale/dead connections
- **D-58:** UUID v4 for connection/session IDs
- **D-59:** Fixed timeout for all connections (configurable)
- **D-60:** State tracking for connections (connecting, active, closing)
- **D-61:** Aggregate connection metrics (total connections, throughput)
- **D-62:** On-accept protocol validation
- **D-63:** Connection pooling and reuse with fixed pool sizes
- **D-64:** Binary priority support (active vs new connections)
- **D-65:** Token-based authentication (stateless, scalable)
- **D-66:** Per-connection throttling with token bucket algorithm
- **D-67:** Automatic reconnection with exponential backoff and jitter

### Memory Budgeting
- **D-68:** Active monitoring with runtime memory tracking for 150MB budget enforcement
- **D-69:** Pre-allocated buffers and pools for predictable memory usage
- **D-70:** Multiple alert thresholds (80%, 90%, 95%) for early warning
- **D-71:** Fixed buffer sizes (640B for audio frames) for simplicity and predictability

### Backpressure Strategy
- **D-72:** Explicit backpressure signals to clients
- **D-73:** Drop oldest strategy when buffer pool is exhausted (aligns with RES-04)
- **D-74:** Graceful rejection with error responses when under backpressure

### Claude's Discretion
The following areas were left to Claude's discretion during discussion:
- Service lifecycle management (cmd/main vs server package)
- Interface design timing (define early vs emergent)
- Main entry point structure
- Go module naming
- Code organization within infrastructure
- Graceful shutdown approach
- Performance optimization timing
- Concurrency pattern choice
- Error recovery timing
- Resource cleanup approach
- Testing structure (co-located vs test packages)
- Metrics format selection
- Build tag needs
- Configuration validation strictness
- Signal handling approach
- Timeout configuration needs
- Dependency injection granularity
- Interface design size
- Error categorization approach
- Validation strategy
- Code quality enforcement level
- Context usage extent
- Middleware necessity
- Constants organization
- Mocking strategy
- Type safety level for domain concepts
- Conditional compilation needs
- Component failure handling
- High availability approach
- State synchronization method
- Load shedding strategy
- Observability depth
- Health check detail level
- Metrics granularity
- Connection pool sizing
- Protocol validation timing
- Authentication security level
- Rate limiting algorithm choice
- Reconnection backoff strategy
- Memory allocation strategy
- Buffer sizing approach
- Alert threshold configuration
- Protocol signaling support

## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project Requirements
- `.planning/REQUIREMENTS.md` — Complete v1 requirements for Home Tutor Agent, including Phase 1 requirements (RES-01, RES-03, RES-04, ERR-04)
- `.planning/PROJECT.md` — Project context, constraints, and key technical decisions (Go WebSocket service, 150MB memory budget, 1C 1G VPS deployment)

### Roadmap & Phase Definition
- `.planning/ROADMAP.md` — Phase 1 goal, success criteria, and requirement mapping
- `.planning/ROADMAP.md#phase-1-resource-constrained-infrastructure` — Specific Phase 1 definition with 4 success criteria

### Technical Constraints
- `.planning/PROJECT.md#constraints` — Infrastructure constraints (1C 1G VPS, 150MB Go service, 50MB audio buffer, 100 concurrent sessions, SQLite storage, <1.5s latency target)

## Existing Code Insights

### Reusable Assets
No existing codebase assets — this is a greenfield project starting from scratch.

### Established Patterns
No established patterns in the codebase — this is the foundational phase that will establish patterns for future phases.

### Integration Points
No existing integration points — this phase creates the infrastructure that subsequent phases will integrate with.

## Specific Ideas

No specific requirements or "I want it like X" moments captured — discussion focused on standard infrastructure approaches for resource-constrained Go services.

## Deferred Ideas

None — discussion stayed within Phase 1 scope (Resource-Constrained Infrastructure). All discussions focused on foundational infrastructure decisions without scope creep into future phases (WebSocket communication, Python integration, TTS streaming, etc.).

---

*Phase: 01-Resource-Constrained Infrastructure*
*Context gathered: 2025-07-18*