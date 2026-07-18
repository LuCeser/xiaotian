# Phase 1: Resource-Constrained Infrastructure - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2025-07-18
**Phase:** 01-Resource-Constrained Infrastructure
**Areas discussed:** Service Architecture, Connection Management, Memory Budgeting, Backpressure Strategy

---

## Service Architecture

Extensive discussion covering 50+ architectural decisions for the foundational Go service infrastructure.

### Package & Project Structure
| Option | Description | Selected |
|--------|-------------|----------|
| Layered (cmd/pkg/internal) | Starts minimal, adds packages as needed | ✓ |
| Domain-oriented | Prepares for future complexity | |
| You decide | Defer detailed module breakdown | |

**User's choice:** Layered (cmd/pkg/internal)

### Infrastructure Modules
| Option | Description | Selected |
|--------|-------------|----------|
| Single infrastructure | One module for infrastructure concerns | ✓ |
| Split by concern | Separate packages for memory, connections, logging, config | |
| You decide | Defer detailed module breakdown | |

**User's choice:** Single infrastructure

### Configuration Management
| Option | Description | Selected |
|--------|-------------|----------|
| env-based | Standard library approach with environment variables | ✓ |
| Config library | Use Viper for rich config sources | |
| You decide | Balance simplicity with future extensibility | |

**User's choice:** env-based

### Configuration Layering
| Option | Description | Selected |
|--------|-------------|----------|
| Layered config | Defaults → file → env override hierarchy | ✓ |
| Single source | Simpler, less confusion about precedence | |
| You decide | Config flexibility vs simplicity | |

**User's choice:** Layered config

### Configuration Schema
| Option | Description | Selected |
|--------|-------------|----------|
| Typed config | Strongly typed config struct | ✓ |
| Map config | More flexible, dynamic configuration | |
| You decide | Type safety vs config flexibility | |

**User's choice:** Typed config

### Error Handling
| Option | Description | Selected |
|--------|-------------|----------|
| Enhanced errors | Use pkg/errors for stack traces | ✓ |
| Standard errors | Standard Go error wrapping | |
| You decide | Detailed stack traces vs simplicity | |

**User's choice:** Enhanced errors

### Build Output
| Option | Description | Selected |
|--------|-------------|----------|
| Static binary | Single binary with embedded configuration | ✓ |
| Config separate | Binary with external config files | |
| You decide | Deployment simplicity vs runtime flexibility | |

**User's choice:** Static binary

### Dependencies
| Option | Description | Selected |
|--------|-------------|----------|
| Rich ecosystem | Include commonly-used Go libraries upfront | ✓ |
| Minimal deps | Use only what's essential for Phase 1 | |
| You decide | Simplicity vs future extensibility | |

**User's choice:** Rich ecosystem

### Project Layout
| Option | Description | Selected |
|--------|-------------|----------|
| Standard layout | golang-standards/project-layout | ✓ |
| Simplified layout | Fewer directories, simpler structure | |
| You decide | Community standards vs Phase 1 simplicity | |

**User's choice:** Standard layout

### Build System
| Option | Description | Selected |
|--------|-------------|----------|
| Make | Make for build automation | ✓ |
| Go native | Go's built-in build tools | |
| You decide | Team familiarity and Make features | |

**User's choice:** Make

### Containerization
| Option | Description | Selected |
|--------|-------------|----------|
| Dockerfile included | Include Dockerfile from the start | |
| Bare metal first | Skip containerization for Phase 1 | ✓ |
| You decide | Container benefits vs complexity | |

**User's choice:** Bare metal first

### Security
| Option | Description | Selected |
|--------|-------------|----------|
| Security basics | Include basic security from the start | ✓ |
| Deferred security | Focus on functionality first | |
| You decide | Security essential for infrastructure foundation | |

**User's choice:** Security basics

### Observability
| Option | Description | Selected |
|--------|-------------|----------|
| Logging + metrics | Structured logging + basic metrics | ✓ |
| Logging only | Simple structured logging for Phase 1 | |
| You decide | Active monitoring vs debugging capability | |

**User's choice:** Logging + metrics

### Health Checks
| Option | Description | Selected |
|--------|-------------|----------|
| Health endpoint | Include HTTP health check endpoint | ✓ |
| Deferred health | Skip health checks for Phase 1 | |
| You decide | External monitoring need | |

**User's choice:** Health endpoint

### Performance Optimization
| Option | Description | Selected |
|--------|-------------|----------|
| Benchmarking included | Include performance benchmarking from the start | |
| Deferred benchmarking | Focus on correctness first | ✓ |
| You decide | Performance validation vs speed | |

**User's choice:** Deferred benchmarking

### Concurrency Pattern
| Option | Description | Selected |
|--------|-------------|----------|
| Worker pools | Goroutine pools for worker tasks | ✓ |
| Goroutine limits | Launch goroutines as needed with limits | |
| You decide | Structured concurrency need | |

**User's choice:** Worker pools

### Error Recovery
| Option | Description | Selected |
|--------|-------------|----------|
| Panic recovery | Include panic recovery middleware | ✓ |
| Fail-fast | Let panics crash the service initially | |
| You decide | Crash resilience vs speed | |

**User's choice:** Panic recovery

### Resource Cleanup
| Option | Description | Selected |
|--------|-------------|----------|
| Defer patterns | Explicit defer patterns for resource cleanup | |
| Context cleanup | Context-based cancellation for cleanup | ✓ |
| You decide | Traditional vs modern context usage | |

**User's choice:** Context cleanup

### Testing Approach
| Option | Description | Selected |
|--------|-------------|----------|
| Table-driven tests | Table-driven tests for infrastructure components | ✓ |
| Example-based | Example-based tests for common cases | |
| You decide | Comprehensive coverage vs test simplicity | |

**User's choice:** Table-driven tests

### Test Coverage
| Option | Description | Selected |
|--------|-------------|----------|
| Coverage threshold | Set minimum coverage threshold in CI | |
| Coverage tracked | Track coverage but don't enforce minimum | ✓ |
| You decide | Coverage enforcement vs quality | |

**User's choice:** Coverage tracked

### Development Workflow
| Option | Description | Selected |
|--------|-------------|----------|
| Pre-commit hooks | Include pre-commit hooks for formatting and linting | ✓ |
| CI-only checks | CI handles all quality checks | |
| You decide | Local enforcement vs development speed | |

**User's choice:** Pre-commit hooks

### Logging Detail
| Option | Description | Selected |
|--------|-------------|----------|
| Rich context logs | Structured logging with context (request ID, component) | ✓ |
| Basic structured logs | Simple structured logging (level, message, error) | |
| You decide | Detailed context vs simplicity | |

**User's choice:** Rich context logs

### Metrics Format
| Option | Description | Selected |
|--------|-------------|----------|
| Prometheus | Prometheus format for metrics exposure | |
| Simple format | Simple JSON or text format | |
| You decide | Integration vs implementation simplicity | |

**User's choice:** You decide

### Dependency Injection
| Option | Description | Selected |
|--------|-------------|----------|
| Constructor DI | Constructor-based dependency injection | ✓ |
| Global state | Global variables or singletons | |
| You decide | Testability vs simplicity | |

**User's choice:** Constructor DI

### Interface Design
| Option | Description | Selected |
|--------|-------------|----------|
| Small interfaces | Define small, focused interfaces (SRP) | ✓ |
| Large interfaces | Define larger, feature-complete interfaces | |
| You decide | Interface granularity | |

**User's choice:** Small interfaces

### Error Types
| Option | Description | Selected |
|--------|-------------|----------|
| Custom errors | Define custom error types for different categories | ✓ |
| Standard errors | Use standard errors and context | |
| You decide | Structured error handling vs simplicity | |

**User's choice:** Custom errors

### Validation
| Option | Description | Selected |
|--------|-------------|----------|
| Validation layer | Centralized validation layer | ✓ |
| Embedded validation | Embedded validation in each component | |
| You decide | Consistency vs locality | |

**User's choice:** Validation layer

### Code Quality
| Option | Description | Selected |
|--------|-------------|----------|
| Static analysis | Include static analysis (golangci-lint) in CI | ✓ |
| Basic linting | Basic linting only in CI | |
| You decide | Comprehensive quality vs CI speed | |

**User's choice:** Static analysis

### Context Usage
| Option | Description | Selected |
|--------|-------------|----------|
| Context everywhere | Pass context through all infrastructure functions | |
| Context where needed | Use context where needed (long-running operations) | ✓ |
| You decide | Consistent context handling vs simplicity | |

**User's choice:** Context where needed

### Middleware Pattern
| Option | Description | Selected |
|--------|-------------|----------|
| Middleware pattern | Include middleware pattern for cross-cutting concerns | ✓ |
| Inline handling | Handle cross-cutting concerns inline | |
| You decide | Clean separation vs simplicity | |

**User's choice:** Middleware pattern

### Constants Organization
| Option | Description | Selected |
|--------|-------------|----------|
| Scoped constants | Scoped constants per package | ✓ |
| Constants file | Centralized constants file | |
| You decide | Centralization vs encapsulation | |

**User's choice:** Scoped constants

### Mocking Strategy
| Option | Description | Selected |
|--------|-------------|----------|
| Mocking library | Use mocking library for testing dependencies | ✓ |
| Manual fakes | Manual test doubles/fakes | |
| You decide | Mocking convenience vs dependency management | |

**User's choice:** Mocking library

### Type Safety
| Option | Description | Selected |
|--------|-------------|----------|
| Custom types | Custom types for domain concepts (SessionID, MemoryBytes) | ✓ |
| Primitives | Use primitive types (string, int64) | |
| You decide | Type safety vs simplicity | |

**User's choice:** Custom types

### Build Tags
| Option | Description | Selected |
|--------|-------------|----------|
| Build tags | Include build tags for test variants and optional features | ✓ |
| Standard builds | Standard builds only | |
| You decide | Build flexibility vs simplicity | |

**User's choice:** Build tags

### Race Detection
| Option | Description | Selected |
|--------|-------------|----------|
| Race detector in CI | Enable race detector in CI tests | ✓ |
| Manual race checks | Run race detector manually only | |
| You decide | Concurrency safety vs CI speed | |

**User's choice:** Race detector in CI

### Documentation
| Option | Description | Selected |
|--------|-------------|----------|
| Godoc generation | Generate docs from godoc comments | ✓ |
| Manual docs | Manual documentation separate from code | |
| You decide | Automatic docs vs manual curation | |

**User's choice:** Godoc generation

### Memory Tracking
| Option | Description | Selected |
|--------|-------------|----------|
| Runtime tracking | Runtime memory tracking with metrics | ✓ |
| Estimation | Estimate based on allocation patterns | |
| You decide | Real-time monitoring need | |

**User's choice:** Runtime tracking

### Buffer Management
| Option | Description | Selected |
|--------|-------------|----------|
| sync.Pool | sync.Pool for buffer reuse | ✓ |
| Manual pool | Pre-allocated buffer pool with manual management | |
| You decide | Standard approach vs control | |

**User's choice:** sync.Pool

### Component State
| Option | Description | Selected |
|--------|-------------|----------|
| Thread-safe components | Thread-safe components with mutex protection | ✓ |
| Single-threaded actors | Single-threaded components with goroutine per component | |
| You decide | Thread safety vs actor model | |

**User's choice:** Thread-safe components

### Config Reload
| Option | Description | Selected |
|--------|-------------|----------|
| Startup-only config | Config only on startup | ✓ |
| Runtime reload | Support runtime config reload | |
| You decide | Runtime flexibility vs complexity | |

**User's choice:** Startup-only config

### Service Discovery
| Option | Description | Selected |
|--------|-------------|----------|
| Single instance | Single instance only | ✓ |
| Service discovery | Include service discovery mechanism | |
| You decide | Multi-instance deployment need | |

**User's choice:** Single instance

### Graceful Exit
| Option | Description | Selected |
|--------|-------------|----------|
| Graceful drain | Drain connections before shutdown | ✓ |
| Immediate shutdown | Immediate shutdown with cleanup | |
| You decide | Session cleanliness vs shutdown speed | |

**User's choice:** Graceful drain

### Timeout Configuration
| Option | Description | Selected |
|--------|-------------|----------|
| Configurable timeouts | Configurable timeouts for all operations | ✓ |
| Hardcoded timeouts | Hardcoded timeout constants | |
| You decide | Operational flexibility vs simplicity | |

**User's choice:** Configurable timeouts

### Module Dependencies
| Option | Description | Selected |
|--------|-------------|----------|
| Explicit deps | Clear dependency graph between infrastructure modules | ✓ |
| Allowed circular | Allow circular dependencies with careful state management | |
| You decide | Strict dependency management vs flexibility | |

**User's choice:** Explicit deps

### Error Context
| Option | Description | Selected |
|--------|-------------|----------|
| Rich context | Rich error context with operation details | ✓ |
| Minimal context | Minimal error context | |
| You decide | Debugging detail vs error simplicity | |

**User's choice:** Rich context

### Integration Tests
| Option | Description | Selected |
|--------|-------------|----------|
| Integration tests | Include integration tests for infrastructure components | ✓ |
| Unit tests only | Unit tests only for Phase 1 | |
| You decide | Integration validation vs test complexity | |

**User's choice:** Integration tests

### Benchmark Scope
| Option | Description | Selected |
|--------|-------------|----------|
| Throughput benchmarks | Benchmark operation throughput | ✓ |
| Memory benchmarks | Benchmark memory allocations specifically | |
| You decide | Memory vs performance focus | |

**User's choice:** Throughput benchmarks

### Failure Modes
| Option | Description | Selected |
|--------|-------------|----------|
| Circuit breaker | Include circuit breaker pattern for failing components | ✓ |
| Fail and restart | Let components fail and restart | |
| You decide | Resilience patterns vs simplicity | |

**User's choice:** Circuit breaker

### Redundancy
| Option | Description | Selected |
|--------|-------------|----------|
| Component redundancy | Design for component redundancy | ✓ |
| Single instance | Single instance components | |
| You decide | High availability need | |

**User's choice:** Component redundancy

### State Sync
| Option | Description | Selected |
|--------|-------------|----------|
| Centralized state | Centralized state management | ✓ |
| Distributed state | Distributed state with synchronization | |
| You decide | State distribution need | |

**User's choice:** Centralized state

### Load Shedding
| Option | Description | Selected |
|--------|-------------|----------|
| Proactive shedding | Proactive load shedding when approaching limits | ✓ |
| Capacity-based reject | Reject requests when at capacity | |
| You decide | Proactive management vs simplicity | |

**User's choice:** Proactive shedding

### Observability Depth
| Option | Description | Selected |
|--------|-------------|----------|
| Basic obs | Basic observability (metrics + logging) | ✓ |
| Deep tracing | Deep observability with request tracing | |
| You decide | Detailed tracing vs overhead | |

**User's choice:** Basic obs

### Health Depth
| Option | Description | Selected |
|--------|-------------|----------|
| Detailed health | Detailed health checks for each component | ✓ |
| Basic liveness | Basic liveness check | |
| You decide | Component-level health need | |

**User's choice:** Detailed health

### Metrics Granularity
| Option | Description | Selected |
|--------|-------------|----------|
| Service metrics | Service-level metrics | ✓ |
| Component metrics | Component-level metrics | |
| You decide | Component-level visibility need | |

**User's choice:** Service metrics

## Connection Management

Discussion focused on connection lifecycle, limits, authentication, and resilience.

### Connection Tracking
| Option | Description | Selected |
|--------|-------------|----------|
| Map registry | Map-based connection registry with mutex | ✓ |
| Slice pool | Slice-based with connection pool | |
| You decide | Lookup speed vs iteration efficiency | |

**User's choice:** Map registry

### Connection Lifecycle
| Option | Description | Selected |
|--------|-------------|----------|
| Lifecycle hooks | Explicit lifecycle hooks (onConnect, onDisconnect, onError) | ✓ |
| Inline handling | Inline connection handling | |
| You decide | Connection extensibility need | |

**User's choice:** Lifecycle hooks

### Connection Limits
| Option | Description | Selected |
|--------|-------------|----------|
| Hard limit | Hard limit at 100 connections | ✓ |
| Soft limit | Soft limit with graceful degradation | |
| You decide | Strict capacity enforcement | |

**User's choice:** Hard limit

### Connection Cleanup
| Option | Description | Selected |
|--------|-------------|----------|
| Periodic cleanup | Periodic cleanup routine (every 30s) | ✓ |
| Event cleanup | Event-driven cleanup on disconnect | |
| You decide | Reliable reclamation vs immediacy | |

**User's choice:** Periodic cleanup

### Connection IDs
| Option | Description | Selected |
|--------|-------------|----------|
| UUID v4 | UUID v4 for connection IDs | ✓ |
| Sequential IDs | Sequential integer IDs | |
| You decide | Uniqueness vs compactness | |

**User's choice:** UUID v4

### Connection Timeouts
| Option | Description | Selected |
|--------|-------------|----------|
| Fixed timeout | Fixed timeout for all connections | ✓ |
| Configurable timeout | Configurable per-connection timeout | |
| You decide | Uniform behavior vs flexibility | |

**User's choice:** Fixed timeout

### Connection Rejection
| Option | Description | Selected |
|--------|-------------|----------|
| HTTP 429 | Return HTTP 429 Too Many Requests | ✓ |
| Silent close | Close connection without response | |
| You decide | Client feedback matters | |

**User's choice:** HTTP 429

### Connection State
| Option | Description | Selected |
|--------|-------------|----------|
| State tracking | Track connection state (connecting, active, closing) | ✓ |
| Binary state | Binary connection state (connected/disconnected) | |
| You decide | State visibility vs simplicity | |

**User's choice:** State tracking

### Connection Metrics
| Option | Description | Selected |
|--------|-------------|----------|
| Aggregate metrics | Aggregate metrics only (total connections, throughput) | ✓ |
| Per-conn metrics | Per-connection metrics (bytes sent/received, duration) | |
| You decide | Detailed visibility vs memory overhead | |

**User's choice:** Aggregate metrics

### Connection Validation
| Option | Description | Selected |
|--------|-------------|----------|
| On-accept validate | Validate connections on acceptance (protocol handshake) | ✓ |
| Continuous validate | Validate during connection lifecycle | |
| You decide | Early detection vs ongoing validation | |

**User's choice:** On-accept validate

### Connection Pooling
| Option | Description | Selected |
|--------|-------------|----------|
| Connection pool | Support connection pooling and reuse | ✓ |
| One-per-client | One connection per client | |
| You decide | Resource efficiency vs complexity | |

**User's choice:** Connection pool

### Pool Size
| Option | Description | Selected |
|--------|-------------|----------|
| Fixed size | Fixed pool size per client | ✓ |
| Dynamic sizing | Dynamic pool sizing | |
| You decide | Predictability vs efficiency | |

**User's choice:** Fixed size

### Connection Priority
| Option | Description | Selected |
|--------|-------------|----------|
| Priority support | Support connection prioritization | ✓ |
| FIFO only | FIFO connection handling | |
| You decide | Session quality vs simplicity | |

**User's choice:** Priority support

### Priority Levels
| Option | Description | Selected |
|--------|-------------|----------|
| Binary priority | Binary priority (active vs new) | ✓ |
| Multi-level | Multiple priority levels (high, medium, low) | |
| You decide | Granular prioritization need | |

**User's choice:** Binary priority

### Authentication
| Option | Description | Selected |
|--------|-------------|----------|
| Basic auth | Basic authentication from start | ✓ |
| No auth (Phase 1) | No authentication for Phase 1 | |
| You decide | Security essential for Phase 1 | |

**User's choice:** Basic auth

### Auth Method
| Option | Description | Selected |
|--------|-------------|----------|
| Token auth | Token-based authentication | ✓ |
| API keys | API key authentication | |
| You decide | Stateless scalability vs simplicity | |

**User's choice:** Token auth

### Connection Throttling
| Option | Description | Selected |
|--------|-------------|----------|
| Per-conn throttle | Per-connection rate limiting | ✓ |
| Global throttle | Global rate limiting only | |
| You decide | Individual connection control need | |

**User's choice:** Per-conn throttle

### Rate Limiting Algorithm
| Option | Description | Selected |
|--------|-------------|----------|
| Token bucket | Token bucket algorithm | ✓ |
| Fixed window | Fixed window counter | |
| You decide | Smooth rate limiting vs simplicity | |

**User's choice:** Token bucket

### Reconnection
| Option | Description | Selected |
|--------|-------------|----------|
| Auto reconnect | Automatic reconnection with exponential backoff | ✓ |
| Manual reconnect | Manual reconnection only | |
| You decide | Client experience vs complexity | |

**User's choice:** Auto reconnect

### Backoff Strategy
| Option | Description | Selected |
|--------|-------------|----------|
| Exponential+jitter | Exponential backoff with jitter | ✓ |
| Fixed delay | Fixed delay retries | |
| You decide | Robust reconnection vs simplicity | |

**User's choice:** Exponential+jitter

## Memory Budgeting

Discussion focused on enforcing the 150MB memory constraint with monitoring and allocation strategies.

### Budget Enforcement
| Option | Description | Selected |
|--------|-------------|----------|
| Active monitoring | Runtime memory tracking with alarms | ✓ |
| Periodic sampling | Periodic memory sampling | |
| You decide | Real-time visibility need | |

**User's choice:** Active monitoring

### Allocation Strategy
| Option | Description | Selected |
|--------|-------------|----------|
| Pre-allocated | Pre-allocate buffers and pools | ✓ |
| On-demand pooled | Allocate on-demand with pooling | |
| You decide | Predictability vs efficiency | |

**User's choice:** Pre-allocated

### Memory Alerts
| Option | Description | Selected |
|--------|-------------|----------|
| Multiple thresholds | Multiple alert thresholds (80%, 90%, 95%) | ✓ |
| Single threshold | Single critical threshold (95%) | |
| You decide | Early warning time value | |

**User's choice:** Multiple thresholds

### Buffer Sizing
| Option | Description | Selected |
|--------|-------------|----------|
| Fixed sizes | Fixed buffer sizes (640B for audio frames) | ✓ |
| Dynamic sizing | Dynamic buffer sizing based on load | |
| You decide | Simplicity vs efficiency | |

**User's choice:** Fixed sizes

## Backpressure Strategy

Discussion focused on handling resource overflow and client communication under pressure.

### Backpressure Signaling
| Option | Description | Selected |
|--------|-------------|----------|
| Explicit signals | Explicit backpressure signals to clients | ✓ |
| Implicit slow | Implicit backpressure (slow response) | |
| You decide | Protocol support existence | |

**User's choice:** Explicit signals

### Overflow Handling
| Option | Description | Selected |
|--------|-------------|----------|
| Drop oldest | Drop oldest buffers when pool exhausted | ✓ |
| Drop newest | Drop newest (reject current) | |
| You decide | Data freshness vs completeness | |

**User's choice:** Drop oldest (aligns with RES-04 requirement)

### Rejection Behavior
| Option | Description | Selected |
|--------|-------------|----------|
| Graceful reject | Graceful rejection with error response | ✓ |
| Silent drop | Silent drop (no response) | |
| You decide | Client feedback matters | |

**User's choice:** Graceful reject

## Claude's Discretion

The following areas were deferred to Claude's discretion during the Service Architecture discussion:
- Service lifecycle management approach
- Interface design timing (early vs emergent)
- Main entry point structure
- Go module naming conventions
- Code organization within infrastructure packages
- Graceful shutdown implementation details
- Performance optimization timing and approach
- Concurrency pattern selection
- Error recovery strategy timing
- Resource cleanup mechanism selection
- Testing file organization (co-located vs test packages)
- Metrics format selection (Prometheus vs simple)
- Build tag requirements and usage
- Configuration validation strictness level
- Signal handling implementation approach
- Timeout configuration scope and defaults
- Dependency injection granularity
- Interface sizing strategy
- Error categorization approach
- Validation layer design
- Code quality enforcement thresholds
- Context usage scope and patterns
- Middleware pattern necessity
- Constants organization approach
- Mocking strategy selection
- Type safety level for domain concepts
- Conditional compilation needs
- Component failure handling approach
- High availability strategy
- State synchronization mechanism
- Load shedding strategy selection
- Observability depth requirements
- Health check detail level
- Metrics granularity decisions
- Connection pool sizing strategy
- Protocol validation timing
- Authentication security level
- Rate limiting algorithm choice
- Reconnection backoff strategy
- Memory allocation strategy
- Buffer sizing approach
- Alert threshold configuration
- Protocol signaling support

## Deferred Ideas

None — discussion stayed within Phase 1 scope (Resource-Constrained Infrastructure). All discussions focused on foundational infrastructure decisions without scope creep into future phases.

---

*Phase: 01-Resource-Constrained Infrastructure*
*Discussion date: 2025-07-18*