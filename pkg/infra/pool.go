package infra

import (
	"math"
	"sync"
	"sync/atomic"

	"github.com/pkg/errors"
)

// AudioFrame is the buffer unit callers operate on (D-40). It is a []byte
// newtype so an audio frame cannot be accidentally assigned to a non-audio
// byte slice (or vice versa). The capacity is locked to
// BufferConfig.AudioFrameBytes (640 in production — 20ms of 16kHz mono PCM
// per D-71 / WebSocket protocol requirement); the length varies as the
// caller appends PCM samples.
type AudioFrame []byte

// PoolStats is the point-in-time snapshot returned by AudioBufferPool.Stats.
// Each field is loaded atomically; the snapshot reflects a coherent read of
// every counter at the cost of being a point-in-time observation
// (subsequent operations may change any field).
//
// Plan 08 (Prometheus metrics endpoint) consumes TotalDrops as the
// `infra_pool_drops_total` counter — the canonical RES-04 metric.
type PoolStats struct {
	// InFlight is the number of buffers currently handed out via Get and
	// not yet returned via Put. Negative values are impossible by
	// construction (Put only decrements after a matching Get).
	InFlight int64 `json:"in_flight"`

	// Capacity is the max in-flight count before drop-oldest kicks in.
	// Equals BufferConfig.PoolSize at construction; immutable afterward.
	Capacity int `json:"capacity"`

	// TotalGets is the cumulative count of Get calls since construction.
	// Monotonically increasing.
	TotalGets uint64 `json:"total_gets"`

	// TotalPuts is the cumulative count of Put calls that passed
	// validation (oversized/nil rejections do NOT increment this counter
	// — they fail before accounting runs). Monotonically increasing.
	TotalPuts uint64 `json:"total_puts"`

	// TotalDrops is the cumulative count of overflow drops (Put calls
	// that returned ErrBufferDropped). This is the canonical RES-04
	// health metric — a non-zero, growing value signals sustained
	// overload.
	TotalDrops uint64 `json:"total_drops"`
}

// AudioBufferPool wraps sync.Pool for fixed-size audio buffers (RES-04,
// D-14, D-71, D-73). It maintains an atomic in-flight counter; when an
// overflow Put arrives (inFlight > capacity), the supplied buffer is
// dropped — let GC reclaim it — and ErrBufferDropped is returned to the
// caller. The pool never panics on overflow and never blocks on Get/Put
// (sync.Pool's Get is non-blocking by construction).
//
// Lifecycle contract:
//
//	p, err := NewAudioBufferPool(cfg.Buffer, logger)
//	if err != nil { ... }              // construction-time validation
//	buf := p.Get()                     // hand a buffer to a worker
//	*buf = append(*buf, pcm...)        // worker fills it
//	err := p.Put(buf)                  // worker returns it
//	if errors.Is(err, ErrBufferDropped) {
//	    // soft signal: buffer was dropped under overflow; nothing else to do
//	}
//
// Thread safety: AudioBufferPool is safe for concurrent use by any number
// of goroutines. The atomic counters and sync.Pool are the synchronization
// primitives; no mutex is required.
type AudioBufferPool struct {
	// pool is the underlying sync.Pool. Its New func returns a *AudioFrame
	// with cap == frameBytes and len == 0; Get still defends against
	// unexpected sizes (sync.Pool may clear pooled objects between GC
	// cycles, and a future refactor that changes the New func must not be
	// able to violate the cap invariant silently).
	pool sync.Pool

	// frameBytes is the locked buffer capacity (D-71). Cached on the struct
	// so Get/Put can validate without re-reading the original BufferConfig.
	frameBytes int

	// capacity is the max in-flight count before drop-oldest kicks in.
	// Stored as int32 to match the width of inFlight's atomic.Int32 —
	// comparisons stay narrow. Immutable after construction.
	capacity int32

	// inFlight counts buffers handed out via Get and not yet returned via
	// Put. May temporarily exceed capacity under burst load; Put relieves
	// the overflow by dropping.
	inFlight atomic.Int32

	// totalGets/totalPuts/totalDrops are cumulative counters exposed via
	// Stats for observability (Plan 08 metrics endpoint).
	totalGets  atomic.Uint64
	totalPuts  atomic.Uint64
	totalDrops atomic.Uint64

	// logger receives Warn events on overflow drops. Required by D-42
	// (constructor DI); nil is substituted with NoOpLogger at construction
	// so callers that do not care about drop telemetry pay no overhead.
	logger Logger
}

// Sentinel errors for AudioBufferPool. Callers MUST detect via errors.Is
// rather than string matching — the wrapped text is for human-readable
// debugging only.
var (
	// ErrInvalidBufferConfig is returned by NewAudioBufferPool when the
	// supplied BufferConfig fails validation (AudioFrameBytes <= 0,
	// PoolSize <= 0, or Strategy != StrategyDropOldest).
	ErrInvalidBufferConfig = errors.New("invalid buffer pool configuration")

	// ErrNilBuffer is returned by Put when the caller passes a nil
	// *AudioFrame. The in-flight counter is unchanged.
	ErrNilBuffer = errors.New("buffer is nil")

	// ErrOversizedBuffer is returned by Put when the supplied buffer's
	// capacity does not match the pool's frameBytes (T-01-18 mitigation).
	// The in-flight counter is unchanged.
	ErrOversizedBuffer = errors.New("buffer capacity exceeds pool's frame size")

	// ErrBufferDropped is returned by Put when the supplied buffer was
	// dropped due to overflow (inFlight > capacity). This is a SOFT
	// signal: the buffer is gone (GC will reclaim it), the in-flight
	// counter has been decremented, and the caller needs to take no
	// further action. Callers may safely ignore this error.
	ErrBufferDropped = errors.New("buffer dropped due to overflow")
)

// NewAudioBufferPool constructs an AudioBufferPool from BufferConfig. The
// logger receives Warn events on overflow drops; if nil, NoOpLogger is
// substituted so the constructor is always non-nil.
//
// Returns a wrapped ErrInvalidBufferConfig (detectable via errors.Is) on
// validation failure:
//
//   - AudioFrameBytes <= 0 (production default is 640 per D-71)
//   - PoolSize <= 0
//   - Strategy != StrategyDropOldest (only supported strategy in Phase 1)
func NewAudioBufferPool(cfg BufferConfig, logger Logger) (*AudioBufferPool, error) {
	if cfg.AudioFrameBytes <= 0 {
		return nil, errors.Wrapf(ErrInvalidBufferConfig,
			"AudioFrameBytes must be > 0, got %d", cfg.AudioFrameBytes)
	}
	if cfg.PoolSize <= 0 {
		return nil, errors.Wrapf(ErrInvalidBufferConfig,
			"PoolSize must be > 0, got %d", cfg.PoolSize)
	}
	if cfg.Strategy != StrategyDropOldest {
		return nil, errors.Wrapf(ErrInvalidBufferConfig,
			"Strategy must be %q, got %q", StrategyDropOldest, cfg.Strategy)
	}
	if cfg.PoolSize > math.MaxInt32 {
		return nil, errors.Wrapf(ErrInvalidBufferConfig,
			"PoolSize %d exceeds int32", cfg.PoolSize)
	}
	if logger == nil {
		logger = NewNoOpLogger()
	}

	frameBytes := cfg.AudioFrameBytes
	p := &AudioBufferPool{
		frameBytes: frameBytes,
		capacity:   int32(cfg.PoolSize),
		logger:     logger,
	}
	// sync.Pool.New is invoked when Get finds no cached object. Returns a
	// *AudioFrame (pointer so the caller can mutate the slice header
	// without the mutation being lost across the sync.Pool boundary).
	// make([]byte, 0, n) gives len 0 cap n — never make([]byte, n) which
	// would give a fully-zeroed, full-length buffer (Pitfall 2 in the
	// phase research, and the plan's explicit acceptance criterion).
	p.pool.New = func() any {
		b := make(AudioFrame, 0, frameBytes)
		return &b
	}
	return p, nil
}

// Get returns a *AudioFrame with len == 0 and cap == p.frameBytes. Get
// never blocks and never panics; sync.Pool allocates if no cached buffer
// is available.
//
// Defensive reallocation: if sync.Pool returns a buffer whose cap does not
// match frameBytes (the runtime may have cleared pooled objects between GC
// cycles, or a future refactor changed the New func), Get reallocates a
// fresh buffer of the correct capacity. This guards T-01-18 (buffer
// overflow via unexpectedly sized pooled objects) at the read side; Put
// guards it at the write side.
func (p *AudioBufferPool) Get() *AudioFrame {
	p.inFlight.Add(1)
	p.totalGets.Add(1)

	buf := p.pool.Get().(*AudioFrame)
	if cap(*buf) != p.frameBytes {
		// sync.Pool handed back an unexpected size — reallocate. The
		// stale buffer becomes garbage.
		*buf = make(AudioFrame, 0, p.frameBytes)
	}
	// Always reset length to 0 (T-01-21: stale data must not leak
	// between callers). Capacity is preserved.
	*buf = (*buf)[:0]
	return buf
}

// Put returns a buffer to the pool.
//
// If the pool is at capacity (inFlight > capacity) — i.e. the system is in
// an overflow state — the supplied buffer is dropped (GC reclaims it) and
// Put returns ErrBufferDropped. The caller may treat this as a soft
// signal: the buffer is gone, no further action is required.
//
// Put rejects oversized buffers (cap != frameBytes) with ErrOversizedBuffer
// and nil buffers with ErrNilBuffer. In both cases the in-flight counter
// is unchanged — the caller's buffer never belonged to this pool.
//
// Validation runs BEFORE counter mutation so a rejected Put has no
// observable side effect on pool accounting.
func (p *AudioBufferPool) Put(buf *AudioFrame) error {
	if buf == nil {
		return ErrNilBuffer
	}
	if cap(*buf) != p.frameBytes {
		return errors.Wrap(ErrOversizedBuffer,
			"buffer capacity does not match pool's frame size")
	}

	// Counter accounting: TotalPuts only counts Puts that passed
	// validation (rejected Puts are caller bugs, not pool activity).
	p.totalPuts.Add(1)

	// Overflow check: if inFlight is already over capacity, dropping the
	// incoming buffer (rather than returning it to sync.Pool) brings
	// inFlight back down by one. This is the drop-oldest strategy per
	// D-73 — we shed load at the return side rather than the acquire
	// side, prioritising liveness for the Get path.
	current := p.inFlight.Load()
	if current > p.capacity {
		p.totalDrops.Add(1)
		p.inFlight.Add(-1)
		p.logger.Warn("buffer dropped (overflow)",
			F("component", "infra.pool"),
			F("drops", p.totalDrops.Load()),
			F("capacity", p.capacity))
		return ErrBufferDropped
	}

	// Reset length to 0 (T-01-21) before returning to the pool, so the
	// next Get always sees a clean buffer even if Put is the only reset
	// path that ran (Get also resets defensively — both sides guard).
	*buf = (*buf)[:0]
	p.pool.Put(buf)
	p.inFlight.Add(-1)
	return nil
}

// Stats returns a point-in-time snapshot of the pool's counters. Fields
// are loaded atomically; the snapshot is not transactional across fields
// (concurrent operations may change any field between individual loads),
// but each field's value is internally consistent.
//
// For Plan 08's Prometheus endpoint, map the fields as:
//
//	infra_pool_in_flight      gauge     stats.InFlight
//	infra_pool_capacity       gauge     stats.Capacity
//	infra_pool_gets_total     counter   stats.TotalGets
//	infra_pool_puts_total     counter   stats.TotalPuts
//	infra_pool_drops_total    counter   stats.TotalDrops
func (p *AudioBufferPool) Stats() PoolStats {
	return PoolStats{
		InFlight:   int64(p.inFlight.Load()),
		Capacity:   int(p.capacity),
		TotalGets:  p.totalGets.Load(),
		TotalPuts:  p.totalPuts.Load(),
		TotalDrops: p.totalDrops.Load(),
	}
}
