package infra

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testBufferCfg returns a BufferConfig with the given frame size and pool
// size, pre-set to the only Phase 1 supported strategy (drop-oldest). Used
// to keep pool tests compact.
func testBufferCfg(frameBytes, poolSize int) BufferConfig {
	return BufferConfig{
		AudioFrameBytes: frameBytes,
		PoolSize:        poolSize,
		Strategy:        StrategyDropOldest,
	}
}

// TestBufferPoolGetReturns640Bytes verifies D-71: buffers retrieved from the
// pool are pre-sized to exactly 640 bytes with length 0. Both guarantees
// are independent — cap==640 alone is not enough; len MUST be 0 so callers
// never see stale data from a previous use.
func TestBufferPoolGetReturns640Bytes(t *testing.T) {
	cfg := testBufferCfg(640, 10)
	p, err := NewAudioBufferPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewAudioBufferPool: unexpected error: %v", err)
	}
	buf := p.Get()
	if buf == nil {
		t.Fatal("Get returned nil buffer")
	}
	if got := len(*buf); got != 0 {
		t.Errorf("len: got %d, want 0", got)
	}
	if got := cap(*buf); got != 640 {
		t.Errorf("cap: got %d, want 640", got)
	}
}

// TestBufferPoolPutResetsLength verifies T-01-21 mitigation: Put resets buf
// length to 0 (capacity preserved), so the next Get returns a clean buffer
// with no stale data from the previous user.
func TestBufferPoolPutResetsLength(t *testing.T) {
	cfg := testBufferCfg(640, 10)
	p, err := NewAudioBufferPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewAudioBufferPool: unexpected error: %v", err)
	}

	// Get a buffer, write 100 bytes into it.
	buf1 := p.Get()
	*buf1 = append(*buf1, make([]byte, 100)...)
	if got := len(*buf1); got != 100 {
		t.Fatalf("before Put: len: got %d, want 100", got)
	}

	// Put it back; then Get another buffer and verify it is clean.
	if err := p.Put(buf1); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	buf2 := p.Get()
	if got := len(*buf2); got != 0 {
		t.Errorf("after Put: len: got %d, want 0 (buffer not reset)", got)
	}
	if got := cap(*buf2); got != 640 {
		t.Errorf("after Put: cap: got %d, want 640 (capacity lost)", got)
	}
}

// TestDropOldestStrategy verifies RES-04 / D-73: when the pool is at capacity
// and an overflow Put arrives, the buffer is dropped (GC-reclaimed) rather
// than causing a panic, block, or memory growth. The sentinel
// ErrBufferDropped signals the drop to the caller.
//
// Contract (per the plan's Put action): the first overflow Put drops. With
// capacity=2 and 3 buffers in flight, the first Put observes
// inFlight(3) > capacity(2), drops the incoming buffer, and returns
// ErrBufferDropped. Subsequent Puts see inFlight back at capacity and
// succeed normally. Total drops == 1.
//
// This is the VALIDATION.md-mandated test name for the RES-04 contract;
// `go test -run TestDropOldestStrategy` MUST resolve to this test.
func TestDropOldestStrategy(t *testing.T) {
	cfg := testBufferCfg(640, 2) // capacity = 2
	p, err := NewAudioBufferPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewAudioBufferPool: unexpected error: %v", err)
	}

	// Get three buffers — inFlight is now 3, over capacity (2). Get does
	// not reject the third (liveness over correctness per D-73); the
	// overflow is resolved at Put time.
	b1 := p.Get()
	b2 := p.Get()
	b3 := p.Get()
	before := p.Stats()
	if before.InFlight != 3 {
		t.Fatalf("after 3 Gets: InFlight: got %d, want 3", before.InFlight)
	}

	// Put all three back. The first Put observes inFlight=3 > capacity=2
	// and drops; the next two Puts succeed.
	err1 := p.Put(b1)
	err2 := p.Put(b2)
	err3 := p.Put(b3)

	dropsObserved := 0
	for i, e := range []error{err1, err2, err3} {
		if errors.Is(e, ErrBufferDropped) {
			dropsObserved++
			continue
		}
		if e != nil {
			t.Errorf("Put #%d returned unexpected error: %v", i+1, e)
		}
	}
	if dropsObserved != 1 {
		t.Errorf("drop count among Puts: got %d, want exactly 1 (one overflow drop expected)",
			dropsObserved)
	}

	stats := p.Stats()
	if stats.TotalDrops != 1 {
		t.Errorf("Stats.TotalDrops: got %d, want 1", stats.TotalDrops)
	}
	if stats.InFlight != 0 {
		t.Errorf("Stats.InFlight after all Puts: got %d, want 0", stats.InFlight)
	}
}

// TestBufferPoolDropCounterIncrements drives multiple overflow bursts and
// asserts TotalDrops increments by exactly the count of overflow events.
func TestBufferPoolDropCounterIncrements(t *testing.T) {
	cfg := testBufferCfg(640, 1) // capacity = 1 — easy to overflow
	p, err := NewAudioBufferPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewAudioBufferPool: unexpected error: %v", err)
	}

	// Three bursts, each overflowing capacity by 1 → 1 drop per burst.
	const bursts = 3
	for i := 0; i < bursts; i++ {
		b1 := p.Get()
		b2 := p.Get() // inFlight = 2 > capacity = 1

		dropsThisBurst := 0
		for _, b := range []*AudioFrame{b1, b2} {
			e := p.Put(b)
			if errors.Is(e, ErrBufferDropped) {
				dropsThisBurst++
			} else if e != nil {
				t.Errorf("burst %d: unexpected Put error: %v", i, e)
			}
		}
		if dropsThisBurst != 1 {
			t.Errorf("burst %d: expected 1 drop, got %d", i, dropsThisBurst)
		}
	}

	stats := p.Stats()
	if stats.TotalDrops != uint64(bursts) {
		t.Errorf("Stats.TotalDrops: got %d, want %d", stats.TotalDrops, bursts)
	}
	if stats.InFlight != 0 {
		t.Errorf("Stats.InFlight after all bursts: got %d, want 0", stats.InFlight)
	}
}

// TestBufferPoolRejectsOversizedPut verifies T-01-18 mitigation: Put on a
// buffer whose capacity does not match the pool's frame size is rejected
// with ErrOversizedBuffer, and the in-flight counter is NOT decremented
// (the buffer never belonged to this pool, so Put must not perturb pool
// accounting).
func TestBufferPoolRejectsOversizedPut(t *testing.T) {
	cfg := testBufferCfg(640, 10)
	p, err := NewAudioBufferPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewAudioBufferPool: unexpected error: %v", err)
	}
	// Hold one buffer in flight so a counter change would be detectable.
	_ = p.Get()
	before := p.Stats()

	// Construct a buffer with cap > 640 and try to put it.
	oversized := AudioFrame(make([]byte, 0, 1024))
	err = p.Put(&oversized)
	if !errors.Is(err, ErrOversizedBuffer) {
		t.Errorf("Put error: got %v, want ErrOversizedBuffer", err)
	}
	after := p.Stats()
	if after.InFlight != before.InFlight {
		t.Errorf("InFlight changed on rejected Put: before %d, after %d (counter must not move)",
			before.InFlight, after.InFlight)
	}
	if after.TotalPuts != before.TotalPuts {
		t.Errorf("TotalPuts changed on rejected Put: before %d, after %d",
			before.TotalPuts, after.TotalPuts)
	}
}

// TestBufferPoolConcurrent stress-tests the pool under the race detector.
// 100 goroutines × 1000 Get/Put cycles must complete without panic, with
// final InFlight == 0 and the cumulative counters matching the expected
// totals. The race detector is the primary guarantee of correctness for the
// atomic counter updates.
func TestBufferPoolConcurrent(t *testing.T) {
	cfg := testBufferCfg(640, 10)
	p, err := NewAudioBufferPool(cfg, NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewAudioBufferPool: unexpected error: %v", err)
	}

	const goroutines = 100
	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	var panicOccurred atomic.Bool

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicOccurred.Store(true)
				}
			}()
			for j := 0; j < iterations; j++ {
				b := p.Get()
				// Touch time.Now to discourage the compiler from
				// optimizing the loop away and to introduce a tiny
				// scheduling seam that encourages goroutine interleaving.
				_ = time.Now()
				_ = p.Put(b)
			}
		}()
	}
	wg.Wait()

	if panicOccurred.Load() {
		t.Fatal("panic observed during concurrent Get/Put")
	}

	stats := p.Stats()
	if stats.InFlight != 0 {
		t.Errorf("InFlight: got %d, want 0 after all Puts", stats.InFlight)
	}
	want := uint64(goroutines * iterations)
	if stats.TotalGets != want {
		t.Errorf("TotalGets: got %d, want %d", stats.TotalGets, want)
	}
	if stats.TotalPuts != want {
		t.Errorf("TotalPuts: got %d, want %d", stats.TotalPuts, want)
	}
}
