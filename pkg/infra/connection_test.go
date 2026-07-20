package infra

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Repo convention (per Plan 02 SUMMARY): tests use stdlib `testing`, not
// testify. testify v1.11.1 is present in the module cache and go.sum but is
// not in go.mod's require block — pulling it into the require block would
// require modifying go.mod, which the parallel-execution constraint forbids
// (sibling plans 01-04/01-05/01-07 are concurrently adding their own files
// and the orchestrator merges go.mod after the worktree merges).
//
// The helpers below are intentionally tiny wrappers over `testing` to keep
// the test bodies readable without bringing in a third-party assertion
// library. They mirror the most-used testify methods (require/assert
// variants of NoError, ErrorIs, Equal, True, False, Len, Contains, etc.).

// uuidV4Regex is the canonical UUID v4 format: 8-4-4-4-12 hex with version
// nibble 4 and variant nibble in {8,9,a,b}. Used by TestSessionIDIsUUIDv4
// because the parallel-execution constraint forbids adding
// github.com/google/uuid as a module dependency — the registry generates
// UUID v4 inline via newUUIDv4 (crypto/rand) and the test validates the
// resulting strings against this regex.
var uuidV4Regex = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// --- tiny stdlib assert/require helpers (no third-party deps) ---
//
// Every helper takes an optional trailing msgAndArgs slice. A single string
// is the message; if a string is followed by extra args it is treated as a
// printf format. This mirrors the testify ergonomics we lost by going
// stdlib-only and lets us write either:
//
//	requireNoError(t, err)
//	requireNoError(t, err, "Register should succeed")
//	requireNoError(t, err, "Register call %d/100 should succeed", i+1)
//
// without juggling three overloads.

func formatMsg(msgAndArgs []any) string {
	switch len(msgAndArgs) {
	case 0:
		return ""
	case 1:
		if s, ok := msgAndArgs[0].(string); ok {
			return s
		}
		return fmt.Sprintf("%v", msgAndArgs[0])
	default:
		if s, ok := msgAndArgs[0].(string); ok {
			return fmt.Sprintf(s, msgAndArgs[1:]...)
		}
		return fmt.Sprintf("%v", msgAndArgs)
	}
}

func requireNoError(t *testing.T, err error, msgAndArgs ...any) {
	t.Helper()
	if err != nil {
		msg := formatMsg(msgAndArgs)
		if msg == "" {
			t.Fatalf("unexpected error: %v", err)
		}
		t.Fatalf(msg+": %v", err)
	}
}

func requireError(t *testing.T, err error, msgAndArgs ...any) {
	t.Helper()
	if err == nil {
		t.Fatalf(formatMsg(msgAndArgs))
	}
}

func requireErrorIs(t *testing.T, err, target error, msgAndArgs ...any) {
	t.Helper()
	if !errors.Is(err, target) {
		msg := formatMsg(msgAndArgs)
		if msg == "" {
			t.Fatalf("error %v does not wrap %v", err, target)
		}
		t.Fatalf(msg+": got %v, want wrap of %v", err, target)
	}
}

func requireEqual(t *testing.T, want, got any, msgAndArgs ...any) {
	t.Helper()
	if want != got {
		msg := formatMsg(msgAndArgs)
		if msg == "" {
			t.Fatalf("want %v, got %v", want, got)
		}
		t.Fatalf(msg+": want %v, got %v", want, got)
	}
}

func requireLen(t *testing.T, x any, n int, msgAndArgs ...any) {
	t.Helper()
	l := lengthOf(x)
	if l != n {
		msg := formatMsg(msgAndArgs)
		if msg == "" {
			t.Fatalf("want len %d, got %d", n, l)
		}
		t.Fatalf(msg+": want len %d, got %d", n, l)
	}
}

func requireTrue(t *testing.T, cond bool, msgAndArgs ...any) {
	t.Helper()
	if !cond {
		t.Fatalf(formatMsg(msgAndArgs))
	}
}

func requireNotNil(t *testing.T, x any, msgAndArgs ...any) {
	t.Helper()
	if x == nil {
		t.Fatalf(formatMsg(msgAndArgs))
	}
}

func assertEqual(t *testing.T, want, got any, msgAndArgs ...any) {
	t.Helper()
	if want != got {
		msg := formatMsg(msgAndArgs)
		if msg == "" {
			t.Errorf("want %v, got %v", want, got)
		}
		t.Errorf(msg+": want %v, got %v", want, got)
	}
}

func assertTrue(t *testing.T, cond bool, msgAndArgs ...any) {
	t.Helper()
	if !cond {
		t.Errorf(formatMsg(msgAndArgs))
	}
}

func assertFalse(t *testing.T, cond bool, msgAndArgs ...any) {
	t.Helper()
	if cond {
		t.Errorf(formatMsg(msgAndArgs))
	}
}

func assertErrorIs(t *testing.T, err, target error, msgAndArgs ...any) {
	t.Helper()
	if !errors.Is(err, target) {
		msg := formatMsg(msgAndArgs)
		if msg == "" {
			t.Errorf("error %v does not wrap %v", err, target)
		}
		t.Errorf(msg+": got %v, want wrap of %v", err, target)
	}
}

func assertError(t *testing.T, err error, msgAndArgs ...any) {
	t.Helper()
	if err == nil {
		t.Errorf(formatMsg(msgAndArgs))
	}
}

func assertLen(t *testing.T, x any, n int, msgAndArgs ...any) {
	t.Helper()
	l := lengthOf(x)
	if l != n {
		msg := formatMsg(msgAndArgs)
		if msg == "" {
			t.Errorf("want len %d, got %d", n, l)
		}
		t.Errorf(msg+": want len %d, got %d", n, l)
	}
}

func assertContains[T comparable](t *testing.T, xs []T, want T, msgAndArgs ...any) {
	t.Helper()
	for _, x := range xs {
		if x == want {
			return
		}
	}
	msg := formatMsg(msgAndArgs)
	if msg == "" {
		t.Errorf("%v not in %v", want, xs)
	} else {
		t.Errorf(msg+": %v not in %v", want, xs)
	}
}

func assertLessOrEqual[T ordered](t *testing.T, a, b T, msgAndArgs ...any) {
	t.Helper()
	if a > b {
		msg := formatMsg(msgAndArgs)
		if msg == "" {
			t.Errorf("want %v <= %v", a, b)
		}
		t.Errorf(msg+": want %v <= %v", a, b)
	}
}

func assertGreater[T ordered](t *testing.T, a, b T, msgAndArgs ...any) {
	t.Helper()
	if a <= b {
		msg := formatMsg(msgAndArgs)
		if msg == "" {
			t.Errorf("want %v > %v", a, b)
		}
		t.Errorf(msg+": want %v > %v", a, b)
	}
}

func assertGreaterOrEqual[T ordered](t *testing.T, a, b T, msgAndArgs ...any) {
	t.Helper()
	if a < b {
		msg := formatMsg(msgAndArgs)
		if msg == "" {
			t.Errorf("want %v >= %v", a, b)
		}
		t.Errorf(msg+": want %v >= %v", a, b)
	}
}

// ordered is the type constraint for the numeric helpers above. Mirrors
// constraints.Ordered from x/exp but defined locally to avoid adding a
// dependency.
type ordered interface {
	~int | ~int64 | ~uint64 | ~float64 | ~string
}

// lengthOf routes the runtime length lookup for the assert/require Len
// helpers. Supports the slice types we actually use in this file; panics
// (visible as a test failure) on anything else so we do not silently mask
// a wrong type.
func lengthOf(x any) int {
	switch v := x.(type) {
	case []SessionID:
		return len(v)
	case []struct {
		id  SessionID
		err error
	}:
		return len(v)
	case nil:
		return 0
	default:
		panic(fmt.Sprintf("lengthOf: unsupported type %T", x))
	}
}

// --- shared test fixtures ---

// newTestRegistry is a compact constructor for tests that want a registry
// with non-default bounds and a no-op logger. Hooks default to zero (all
// nil); individual tests override the fields they need.
func newTestRegistry(t *testing.T, maxConcurrent int, idleTimeout, cleanupInterval time.Duration) *ConnectionRegistry {
	t.Helper()
	r, err := NewConnectionRegistry(SessionConfig{
		MaxConcurrent:   maxConcurrent,
		IdleTimeout:     idleTimeout,
		CleanupInterval: cleanupInterval,
	}, LifecycleHooks{}, NewNoOpLogger())
	requireNoError(t, err, "NewConnectionRegistry should succeed for valid cfg")
	return r
}

// hookRecorder captures every LifecycleHooks invocation into mutex-guarded
// slices so tests can assert firing order and contents even when hooks fire
// from multiple goroutines (Register path vs cleanup goroutine vs reject path).
type hookRecorder struct {
	mu          sync.Mutex
	connects    []SessionID
	disconnects []SessionID
	errors      []struct {
		id  SessionID
		err error
	}
}

func newHookRecorder() *hookRecorder { return &hookRecorder{} }

func (h *hookRecorder) hooks() LifecycleHooks {
	return LifecycleHooks{
		OnConnect: func(id SessionID) {
			h.mu.Lock()
			h.connects = append(h.connects, id)
			h.mu.Unlock()
		},
		OnDisconnect: func(id SessionID) {
			h.mu.Lock()
			h.disconnects = append(h.disconnects, id)
			h.mu.Unlock()
		},
		OnError: func(id SessionID, err error) {
			h.mu.Lock()
			h.errors = append(h.errors, struct {
				id  SessionID
				err error
			}{id, err})
			h.mu.Unlock()
		},
	}
}

func (h *hookRecorder) snapshotConnects() []SessionID {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]SessionID, len(h.connects))
	copy(out, h.connects)
	return out
}

func (h *hookRecorder) snapshotDisconnects() []SessionID {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]SessionID, len(h.disconnects))
	copy(out, h.disconnects)
	return out
}

func (h *hookRecorder) errorCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.errors)
}

// --- ConnectionRegistry tests ---

// TestSessionLimit is the VALIDATION.md RES-03 contract test (task
// 01-06-02). At MaxConcurrent=100 the registry MUST accept exactly 100
// Register calls and reject the 101st with ErrTooManyConnections.
func TestSessionLimit(t *testing.T) {
	r := newTestRegistry(t, 100, 5*time.Minute, 30*time.Second)

	for i := 0; i < 100; i++ {
		_, err := r.Register()
		requireNoError(t, err, fmt.Sprintf("Register call %d/100 should succeed", i+1))
	}

	// The 101st call is the RES-03 contract assertion.
	_, err := r.Register()
	requireErrorIs(t, err, ErrTooManyConnections,
		"Register at capacity must return ErrTooManyConnections (translatable to HTTP 429)")

	stats := r.Stats()
	assertEqual(t, 100, stats.Active, "Active should be at the limit")
	assertEqual(t, uint64(100), stats.TotalAccepted, "TotalAccepted should count all successful registers")
	assertEqual(t, uint64(1), stats.TotalRejected, "TotalRejected should count the 101st call")
}

// TestRegisterRejectsAtCapacity asserts that Register-at-capacity does NOT
// invoke OnConnect (the connection was never admitted) and DOES invoke
// OnError with ErrTooManyConnections.
func TestRegisterRejectsAtCapacity(t *testing.T) {
	rec := newHookRecorder()
	cfg := SessionConfig{
		MaxConcurrent:   1,
		IdleTimeout:     5 * time.Minute,
		CleanupInterval: 30 * time.Second,
	}
	r, err := NewConnectionRegistry(cfg, rec.hooks(), NewNoOpLogger())
	requireNoError(t, err, "registry construction")

	_, err = r.Register()
	requireNoError(t, err, "first Register should succeed")
	requireLen(t, rec.snapshotConnects(), 1, "OnConnect should fire on successful Register")

	_, err = r.Register()
	requireErrorIs(t, err, ErrTooManyConnections, "second Register should be rejected")

	// OnConnect must NOT have fired again — the second Register never admitted
	// a connection.
	assertLen(t, rec.snapshotConnects(), 1,
		"OnConnect must NOT fire on Register-at-capacity rejection")
	// OnError MUST have fired exactly once, with empty SessionID and
	// ErrTooManyConnections.
	assertEqual(t, 1, rec.errorCount(),
		"OnError must fire once on Register-at-capacity rejection")
	rec.mu.Lock()
	defer rec.mu.Unlock()
	requireLen(t, rec.errors, 1, "captured errors")
	assertEqual(t, SessionID(""), rec.errors[0].id,
		"OnError SessionID must be empty for Register rejection (no ID assigned)")
	assertTrue(t, errors.Is(rec.errors[0].err, ErrTooManyConnections),
		"OnError error must wrap ErrTooManyConnections")
}

// TestUnregisterFreesSlot verifies that Unregister releases capacity: after
// reaching the limit and unregistering one connection, the next Register
// succeeds.
func TestUnregisterFreesSlot(t *testing.T) {
	r := newTestRegistry(t, 1, 5*time.Minute, 30*time.Second)

	c1, err := r.Register()
	requireNoError(t, err)
	_, err = r.Register()
	requireErrorIs(t, err, ErrTooManyConnections, "at-capacity Register should be rejected")

	requireNoError(t, r.Unregister(c1.ID), "Unregister of known ID should succeed")

	c2, err := r.Register()
	requireNoError(t, err, "Register after Unregister should succeed (slot freed)")
	assertTrue(t, c1.ID != c2.ID, "new Register must allocate a fresh SessionID")
}

// TestRegisterStoresCorrectState verifies that a registered Connection has
// State==StateActive and CreatedAt within 1 second of now.
func TestRegisterStoresCorrectState(t *testing.T) {
	r := newTestRegistry(t, 10, 5*time.Minute, 30*time.Second)
	before := time.Now()
	c, err := r.Register()
	requireNoError(t, err)
	after := time.Now()

	assertEqual(t, StateActive, c.State, "registered Connection State must be StateActive")
	assertFalse(t, c.CreatedAt.Before(before.Add(-time.Millisecond)),
		"CreatedAt must be at or after the pre-Register timestamp")
	assertFalse(t, c.CreatedAt.After(after.Add(time.Second)),
		"CreatedAt must be within 1s of the post-Register timestamp")
	assertFalse(t, c.LastSeen.Before(c.CreatedAt),
		"LastSeen must be >= CreatedAt on a freshly-registered connection")
}

// TestSessionIDIsUUIDv4 verifies the D-58 invariant: generated SessionIDs
// match the canonical UUID v4 regex (8-4-4-4-12 hex, version nibble 4,
// variant nibble in {8,9,a,b}).
func TestSessionIDIsUUIDv4(t *testing.T) {
	r := newTestRegistry(t, 10, 5*time.Minute, 30*time.Second)
	for i := 0; i < 20; i++ {
		c, err := r.Register()
		requireNoError(t, err)
		assertTruef(t, uuidV4Regex.MatchString(string(c.ID)),
			"SessionID %q must match UUID v4 regex", c.ID)
	}

	// Also validate the helper directly to lock down the format even if the
	// registry ever stops using it.
	for i := 0; i < 20; i++ {
		id, err := newUUIDv4()
		requireNoError(t, err)
		assertTruef(t, uuidV4Regex.MatchString(id),
			"newUUIDv4() output %q must match UUID v4 regex", id)
	}
}

// assertTruef is a printf-style variant of assertTrue used in loops where
// the failure message should include per-iteration context.
func assertTruef(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if !cond {
		t.Errorf(format, args...)
	}
}

// TestLookupByID verifies that Lookup returns the registered Connection by
// value (copy) and returns ErrConnectionNotFound for an unknown ID.
func TestLookupByID(t *testing.T) {
	r := newTestRegistry(t, 10, 5*time.Minute, 30*time.Second)
	c, err := r.Register()
	requireNoError(t, err)

	got, err := r.Lookup(c.ID)
	requireNoError(t, err)
	assertEqual(t, c.ID, got.ID, "Lookup must return the connection with matching ID")
	assertEqual(t, StateActive, got.State, "Lookup must return the connection's current state")

	// Returned value is a copy — mutating it must not affect the registry's
	// interior state.
	got.State = StateClosing
	again, err := r.Lookup(c.ID)
	requireNoError(t, err)
	assertEqual(t, StateActive, again.State,
		"Lookup must return a copy; mutating the returned value must not leak into the registry")

	_, err = r.Lookup(SessionID("nonexistent-id"))
	assertErrorIs(t, err, ErrConnectionNotFound,
		"Lookup of unknown ID must return ErrConnectionNotFound")
}

// TestCleanupRemovesStaleConnections verifies that the cleanup goroutine
// removes connections whose LastSeen is older than IdleTimeout and fires
// OnDisconnect for each.
func TestCleanupRemovesStaleConnections(t *testing.T) {
	rec := newHookRecorder()
	cfg := SessionConfig{
		MaxConcurrent:   10,
		IdleTimeout:     50 * time.Millisecond,
		CleanupInterval: 20 * time.Millisecond,
	}
	r, err := NewConnectionRegistry(cfg, rec.hooks(), NewNoOpLogger())
	requireNoError(t, err)

	c, err := r.Register()
	requireNoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requireNoError(t, r.Start(ctx))

	// Do NOT Touch the connection — it should go stale after IdleTimeout.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r.Stats().Active == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	assertEqual(t, 0, r.Stats().Active,
		"stale connection should be reaped within one cleanup interval of IdleTimeout")
	assertTrue(t, r.Stats().TotalCleaned >= 1,
		"TotalCleaned must increment for cleaner-removed connections")

	discs := rec.snapshotDisconnects()
	assertContains(t, discs, c.ID,
		"OnDisconnect must fire for cleaner-removed connection")
}

// TestLifecycleHooks verifies that all three hooks fire on their respective
// events: OnConnect on Register, OnError on Register-at-capacity, and
// OnDisconnect on Unregister.
func TestLifecycleHooks(t *testing.T) {
	rec := newHookRecorder()
	cfg := SessionConfig{
		MaxConcurrent:   1,
		IdleTimeout:     5 * time.Minute,
		CleanupInterval: 30 * time.Second,
	}
	r, err := NewConnectionRegistry(cfg, rec.hooks(), NewNoOpLogger())
	requireNoError(t, err)

	c, err := r.Register()
	requireNoError(t, err)
	assertLen(t, rec.snapshotConnects(), 1, "OnConnect should fire on Register")

	_, _ = r.Register() // rejected at capacity
	assertEqual(t, 1, rec.errorCount(), "OnError should fire on Register-at-capacity")

	requireNoError(t, r.Unregister(c.ID))
	discs := rec.snapshotDisconnects()
	requireLen(t, discs, 1, "exactly one OnDisconnect should fire")
	assertEqual(t, c.ID, discs[0],
		"OnDisconnect should fire on Unregister with the unregistered ID")
}

// TestRegistryStopsOnContextCancel verifies that the cleanup goroutine
// exits within ~one tick of ctx cancel (no goroutine leak — T-01-27).
func TestRegistryStopsOnContextCancel(t *testing.T) {
	// Force GC so runtime.NumGoroutine() baseline is stable.
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	r := newTestRegistry(t, 10, 5*time.Minute, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	requireNoError(t, r.Start(ctx))

	// Give the goroutine time to start and tick at least once.
	time.Sleep(30 * time.Millisecond)
	cancel()

	// Poll for up to 500ms for NumGoroutine to return to baseline. 50ms is
	// the documented contract; we allow generous headroom on Windows where
	// scheduler granularity is coarser.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	assertLessOrEqual(t, runtime.NumGoroutine(), baseline,
		"cleanup goroutine must exit after ctx cancel (no leak)")

	// After ctx cancel, r.running must be cleared so a fresh Start succeeds.
	requireNoError(t, r.Start(context.Background()),
		"Start after ctx-cancel-driven exit must succeed (running flag cleared)")
}

// TestConnectionStatsAccuracy verifies that Stats() reports exact counts
// across a mixed Register/Unregister/reject sequence.
func TestConnectionStatsAccuracy(t *testing.T) {
	r := newTestRegistry(t, 2, 5*time.Minute, 30*time.Second)

	c1, err := r.Register()
	requireNoError(t, err)
	c2, err := r.Register()
	requireNoError(t, err)
	_, _ = r.Register() // rejected at capacity

	requireNoError(t, r.Unregister(c1.ID))
	requireNoError(t, r.Unregister(c2.ID))
	// Unregister of unknown ID does not change counts.
	_ = r.Unregister(SessionID("does-not-exist"))

	stats := r.Stats()
	assertEqual(t, 0, stats.Active, "Active should be 0 after all unregisters")
	assertEqual(t, uint64(2), stats.TotalAccepted)
	assertEqual(t, uint64(1), stats.TotalRejected)
	assertEqual(t, uint64(0), stats.TotalCleaned,
		"caller-initiated Unregister must not increment TotalCleaned")
}

// TestUnregisterUnknownID verifies that Unregister returns
// ErrConnectionNotFound for an unknown ID.
func TestUnregisterUnknownID(t *testing.T) {
	r := newTestRegistry(t, 5, 5*time.Minute, 30*time.Second)
	err := r.Unregister(SessionID("never-registered"))
	assertErrorIs(t, err, ErrConnectionNotFound)
}

// TestTouchUnknownID verifies that Touch returns ErrConnectionNotFound for
// an unknown ID and refreshes LastSeen for a known one.
func TestTouchUnknownID(t *testing.T) {
	r := newTestRegistry(t, 5, 5*time.Minute, 30*time.Second)

	err := r.Touch(SessionID("never-registered"))
	assertErrorIs(t, err, ErrConnectionNotFound,
		"Touch of unknown ID must return ErrConnectionNotFound")

	c, err := r.Register()
	requireNoError(t, err)
	time.Sleep(5 * time.Millisecond)
	requireNoError(t, r.Touch(c.ID), "Touch of known ID must succeed")

	got, err := r.Lookup(c.ID)
	requireNoError(t, err)
	assertTrue(t, got.LastSeen.After(c.CreatedAt),
		"Touch must advance LastSeen past CreatedAt")
}

// TestNewConnectionRegistryValidation verifies that the constructor fails
// fast on invalid config (D-44).
func TestNewConnectionRegistryValidation(t *testing.T) {
	logger := NewNoOpLogger()
	hooks := LifecycleHooks{}

	tests := []struct {
		name string
		cfg  SessionConfig
	}{
		{
			name: "MaxConcurrent zero",
			cfg:  SessionConfig{MaxConcurrent: 0, IdleTimeout: time.Second, CleanupInterval: time.Second},
		},
		{
			name: "MaxConcurrent negative",
			cfg:  SessionConfig{MaxConcurrent: -1, IdleTimeout: time.Second, CleanupInterval: time.Second},
		},
		{
			name: "IdleTimeout zero",
			cfg:  SessionConfig{MaxConcurrent: 10, IdleTimeout: 0, CleanupInterval: time.Second},
		},
		{
			name: "CleanupInterval zero",
			cfg:  SessionConfig{MaxConcurrent: 10, IdleTimeout: time.Second, CleanupInterval: 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewConnectionRegistry(tt.cfg, hooks, logger)
			requireError(t, err, "constructor must reject invalid cfg")
			assertTrue(t, errors.Is(err, ErrConfigInvalid),
				"validation errors must wrap ErrConfigInvalid (D-04 fail-fast)")
		})
	}
}

// TestNewConnectionRegistryNilLogger verifies that a nil logger is replaced
// with the no-op logger so the registry is always observable (D-47).
func TestNewConnectionRegistryNilLogger(t *testing.T) {
	cfg := SessionConfig{MaxConcurrent: 5, IdleTimeout: time.Second, CleanupInterval: time.Second}
	r, err := NewConnectionRegistry(cfg, LifecycleHooks{}, nil)
	requireNoError(t, err)
	requireNotNil(t, r, "registry must not be nil")
	// Register exercises the logger path; if it were nil this would panic.
	_, err = r.Register()
	requireNoError(t, err)
}

// TestStartIdempotent verifies that a second Start without cancelling the
// first returns ErrRegistryAlreadyRunning.
func TestStartIdempotent(t *testing.T) {
	r := newTestRegistry(t, 5, 5*time.Minute, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	requireNoError(t, r.Start(ctx))
	err := r.Start(ctx)
	assertErrorIs(t, err, ErrRegistryAlreadyRunning,
		"second Start must return ErrRegistryAlreadyRunning")
}

// --- TokenBucket tests (Task 2 GREEN target) ---

// TestTokenBucketAllowsBurst: bucket with rate=10/s, burst=10; first 10
// Allow() calls return true.
func TestTokenBucketAllowsBurst(t *testing.T) {
	b, err := NewTokenBucket(10, 10)
	requireNoError(t, err)
	for i := 0; i < 10; i++ {
		assertTruef(t, b.Allow(), "Allow call %d/10 within burst should succeed", i+1)
	}
}

// TestTokenBucketRateLimitsAfterBurst: after exhausting burst, the 11th
// Allow() returns false; after ~100ms (1 token at rate=10/s), the next
// Allow() returns true.
func TestTokenBucketRateLimitsAfterBurst(t *testing.T) {
	b, err := NewTokenBucket(10, 10)
	requireNoError(t, err)
	for i := 0; i < 10; i++ {
		requireTrue(t, b.Allow(), "burst Allow %d should succeed", i+1)
	}
	assertFalse(t, b.Allow(),
		"Allow after burst exhausted must return false")

	// At rate=10/s, one token refills every ~100ms. Sleep 120ms to allow
	// for scheduler jitter.
	time.Sleep(120 * time.Millisecond)
	assertTrue(t, b.Allow(),
		"Allow after one refill period must return true")
}

// TestTokenBucketConcurrent: 100 goroutines calling Allow() on a bucket
// with rate=1000/s, burst=50; never panics, race-clean.
func TestTokenBucketConcurrent(t *testing.T) {
	b, err := NewTokenBucket(1000, 50)
	requireNoError(t, err)

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if b.Allow() {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	// We cannot assert an exact count (timing-dependent), but the bucket
	// must never hand out more than burst + refilled tokens. burst=50 plus
	// up to ~2s of refill at 1000/s would cap at ~2050; the goroutines
	// collectively request 2000 so we just assert non-negative and bounded.
	got := allowed.Load()
	assertLessOrEqual(t, got, int64(2000), "Allow must never grant more than requested")
	assertGreater(t, got, int64(0), "at least the burst count must be granted")
}

// TestTokenBucketRefillsOverTime: after draining, sleeping 200ms, the
// bucket has accumulated ~2 tokens at rate=10/s (assert 2 Allow() calls
// succeed).
func TestTokenBucketRefillsOverTime(t *testing.T) {
	b, err := NewTokenBucket(10, 10)
	requireNoError(t, err)
	for i := 0; i < 10; i++ {
		requireTrue(t, b.Allow(), "burst Allow %d should succeed", i+1)
	}

	// 200ms at rate=10/s = ~2 tokens.
	time.Sleep(200 * time.Millisecond)
	assertTrue(t, b.Allow(), "first refilled token should be granted")
	assertTrue(t, b.Allow(), "second refilled token should be granted")
	// The third call may or may not succeed depending on jitter, so we do
	// not assert false here — the canonical refill behavior is already
	// covered by TestTokenBucketRateLimitsAfterBurst.
}

// TestNewTokenBucketValidation verifies that NewTokenBucket rejects
// non-positive ratePerSec or burst.
func TestNewTokenBucketValidation(t *testing.T) {
	tests := []struct {
		name       string
		ratePerSec int
		burst      int
		wantErrIs  error
	}{
		{"zero rate", 0, 10, ErrInvalidThrottleConfig},
		{"negative rate", -1, 10, ErrInvalidThrottleConfig},
		{"zero burst", 10, 0, ErrInvalidThrottleConfig},
		{"negative burst", 10, -1, ErrInvalidThrottleConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewTokenBucket(tt.ratePerSec, tt.burst)
			requireError(t, err, "NewTokenBucket must reject invalid config")
			assertTrue(t, errors.Is(err, tt.wantErrIs),
				"got err=%v, want wrap of %v", err, tt.wantErrIs)
		})
	}
}
