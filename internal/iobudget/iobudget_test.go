package iobudget

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeClock is an injectable monotonic clock: Wait's refill math reads it via
// Bucket.now, and tests advance it to simulate the passage of time without
// real sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recordingSleep records every requested sleep and advances the fake clock by
// the requested duration, simulating instant completion of the wait.
type recordingSleep struct {
	clock *fakeClock
	mu    sync.Mutex
	calls []time.Duration
}

func (s *recordingSleep) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	s.calls = append(s.calls, d)
	s.mu.Unlock()
	s.clock.Advance(d)
	return nil
}

func (s *recordingSleep) total() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total time.Duration
	for _, d := range s.calls {
		total += d
	}
	return total
}

// newTestBucket builds a Bucket wired to the fake clock/sleep seams and
// pre-filled to capacity (fresh buckets start full by definition: New sets
// last=now and tokens=capacity).
func newTestBucket(t *testing.T, rate, capacity int64) (*Bucket, *fakeClock, *recordingSleep) {
	t.Helper()
	clock := newFakeClock()
	sleep := &recordingSleep{clock: clock}
	b := New(rate, capacity)
	if b == nil {
		t.Fatalf("New(%d, %d) = nil, want usable bucket", rate, capacity)
	}
	b.now = clock.Now
	b.sleepFn = sleep.sleep
	return b, clock, sleep
}

func TestNew_DisabledReturnsNil(t *testing.T) {
	if got := New(0, 1<<20); got != nil {
		t.Errorf("New(rate=0) = %v, want nil (disabled)", got)
	}
	if got := New(-100, 1<<20); got != nil {
		t.Errorf("New(rate<0) = %v, want nil (disabled)", got)
	}
	if got := New(1<<20, -5); got != nil {
		t.Errorf("New(capacity<0) = %v, want nil (disabled)", got)
	}
	if got := New(1<<20, 1<<20); got == nil {
		t.Errorf("New(valid) = nil, want bucket")
	}
	// Capacity 0 = one second of rate (documented config default).
	b := New(1<<20, 0)
	if b == nil {
		t.Fatal("New(rate, capacity=0) = nil, want default-burst bucket")
	}
	if b.Capacity() != 1<<20 {
		t.Errorf("default capacity = %d, want %d (one second of rate)", b.Capacity(), 1<<20)
	}
}

func TestWait_NilReceiver(t *testing.T) {
	var b *Bucket
	if err := b.Wait(t.Context(), ConsumerMerge, 1<<20); err != nil {
		t.Errorf("nil Bucket Wait = %v, want nil (zero-overhead disabled path)", err)
	}
}

func TestWait_ZeroChargeIsFree(t *testing.T) {
	b, _, sleep := newTestBucket(t, 1000, 1000)
	var charged []int64
	b.onCharge = func(_ string, n int64) { charged = append(charged, n) }
	if err := b.Wait(t.Context(), ConsumerCleanup, 0); err != nil {
		t.Fatalf("Wait(0) = %v, want nil", err)
	}
	if len(charged) != 0 {
		t.Errorf("Wait(0) charged %v, want no charge", charged)
	}
	if sleep.total() != 0 {
		t.Errorf("Wait(0) slept %v, want no sleep", sleep.total())
	}
}

func TestWait_ImmediateWhenTokensAvailable(t *testing.T) {
	b, _, sleep := newTestBucket(t, 1000, 1000)
	if err := b.Wait(t.Context(), ConsumerMerge, 600); err != nil {
		t.Fatalf("Wait(600) on full bucket = %v, want nil", err)
	}
	if sleep.total() != 0 {
		t.Errorf("Wait(600) slept %v, want immediate", sleep.total())
	}
	if got := b.Tokens(); got != 400 {
		t.Errorf("tokens after Wait(600) = %d, want 400", got)
	}
}

func TestWait_BlocksUntilRefill(t *testing.T) {
	// rate 1000 tokens/s, capacity 1000. Spend 1000, then ask for 600:
	// 600 tokens need 600ms of refill.
	b, clock, sleep := newTestBucket(t, 1000, 1000)
	if err := b.Wait(t.Context(), ConsumerMerge, 1000); err != nil {
		t.Fatalf("initial Wait(1000): %v", err)
	}

	err := b.Wait(t.Context(), ConsumerMerge, 600)
	if err != nil {
		t.Fatalf("Wait(600) after refill = %v, want nil", err)
	}
	want := 600 * time.Millisecond
	if got := sleep.total(); got < want {
		t.Errorf("total sleep = %v, want >= %v", got, want)
	}
	_ = clock
	if got := b.Tokens(); got != 0 {
		t.Errorf("tokens = %d, want 0 (600 spent, 600 refilled)", got)
	}
}

func TestWait_ContextCancelAborts(t *testing.T) {
	b, _, _ := newTestBucket(t, 1000, 1000)
	if err := b.Wait(t.Context(), ConsumerMerge, 1000); err != nil {
		t.Fatalf("initial Wait: %v", err)
	}

	// Sleep seam parks until the context is cancelled, mimicking a real
	// blocked timer.
	b.sleepFn = func(ctx context.Context, _ time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() { errCh <- b.Wait(ctx, ConsumerRepair, 500) }()

	// Give the waiter a moment to enter the sleep, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Wait under cancelled ctx = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after ctx cancel")
	}
	if got := b.Tokens(); got != 0 {
		t.Errorf("tokens after aborted Wait = %d, want 0 (no refund, no deduction)", got)
	}
}

func TestWait_RefillCapsAtCapacity(t *testing.T) {
	b, clock, _ := newTestBucket(t, 1000, 1000)
	clock.Advance(10 * time.Second) // 10000 tokens of refill into a 1000 cap
	if err := b.Wait(t.Context(), ConsumerMerge, 1000); err != nil {
		t.Fatalf("Wait at capacity: %v", err)
	}
	if err := b.Wait(t.Context(), ConsumerMerge, 1); err != nil {
		t.Fatalf("Wait(1) should need refill, got %v — refill cap broken?", err)
	}
	if got := b.Tokens(); got != 0 {
		t.Errorf("tokens = %d, want 0 (capped at 1000, spent 1001 total… wait: 1000 spent, 1 more after cap)", got)
	}
}

func TestWait_ClampsOversizeCharges(t *testing.T) {
	// A 5×capacity bill must complete via successive capacity-sized batches
	// (never deadlock), taking ~4×capacity/rate of sleep after the initial
	// full bucket covers the first batch.
	rate := int64(1000)
	capacity := int64(1000)
	b, _, sleep := newTestBucket(t, rate, capacity)

	start := time.Now()
	if err := b.Wait(t.Context(), ConsumerMerge, 5*capacity); err != nil {
		t.Fatalf("Wait(5×capacity) = %v, want nil (clamped batching)", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("fake-clock Wait took %v wall time, want instant", elapsed)
	}
	want := 4 * time.Second // 4 refill batches of 1000 tokens at 1000/s
	if got := sleep.total(); got < want {
		t.Errorf("total sleep = %v, want >= %v", got, want)
	}
}

func TestWait_ObserversSeeConsumerLabel(t *testing.T) {
	b, _, _ := newTestBucket(t, 1000, 1000)
	var chargeConsumer string
	var chargeN int64
	var waitConsumer string
	var waitD time.Duration
	b.onCharge = func(c string, n int64) { chargeConsumer, chargeN = c, n }
	b.onWait = func(c string, d time.Duration) { waitConsumer, waitD = c, d }

	if err := b.Wait(t.Context(), ConsumerMerge, 1000); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	if err := b.Wait(t.Context(), ConsumerMerge, 100); err != nil {
		t.Fatalf("second Wait: %v", err)
	}

	if chargeConsumer != ConsumerMerge {
		t.Errorf("charge consumer = %q, want %q", chargeConsumer, ConsumerMerge)
	}
	if chargeN != 100 {
		t.Errorf("charged n = %d, want 100", chargeN)
	}
	if waitConsumer != ConsumerMerge {
		t.Errorf("wait consumer = %q, want %q", waitConsumer, ConsumerMerge)
	}
	if waitD < 100*time.Millisecond {
		t.Errorf("observed wait = %v, want >= 100ms of accrued wait", waitD)
	}
}

// TestWait_ConcurrentConservation: with the clock frozen (no refill), N
// goroutines together charging exactly the initial capacity must all succeed,
// and tokens + Σcharged must equal capacity — no token creation or loss under
// contention.
func TestWait_ConcurrentConservation(t *testing.T) {
	const goroutines = 8
	b, _, _ := newTestBucket(t, 1000, 1000) // clock stays frozen: no refill

	var charged atomicInt64
	b.onCharge = func(_ string, n int64) { charged.Add(n) }

	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 3 {
				if err := b.Wait(t.Context(), ConsumerCleanup, 40); err != nil {
					errs[i] = err
					return
				}
			}
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d Wait failed: %v", i, err)
		}
	}
	if got := charged.Load(); got != 960 {
		t.Errorf("Σcharged = %d, want 960", got)
	}
	if got := b.Tokens(); got != 40 {
		t.Errorf("tokens = %d, want 40 (balance + Σcharged == capacity)", got)
	}
}

type atomicInt64 struct {
	mu sync.Mutex
	n  int64
}

func (a *atomicInt64) Add(n int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n += n
}

func (a *atomicInt64) Load() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

func TestWait_FairnessUnderContention(t *testing.T) {
	// Two consumers alternate small charges against a slow refill; with the
	// recording sleep both must make progress (no starvation).
	b, _, sleep := newTestBucket(t, 100, 100)
	if err := b.Wait(t.Context(), ConsumerMerge, 100); err != nil {
		t.Fatalf("drain: %v", err)
	}

	var wg sync.WaitGroup
	for g := range 2 {
		consumer := ConsumerMerge
		if g == 1 {
			consumer = ConsumerCleanup
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				if err := b.Wait(t.Context(), consumer, 20); err != nil {
					t.Errorf("%s Wait: %v", consumer, err)
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("contended consumers starved (no progress in 5s)")
	}
	// 200 total tokens needed, 100 drained from a full bucket → ≥100 tokens
	// must come from refill at 100 tokens/s = ≥1s of accrued sleep.
	if got := sleep.total(); got < time.Second {
		t.Errorf("total sleep = %v, want >= 1s (deficit pacing)", got)
	}
}

func TestRateAndCapacity(t *testing.T) {
	b := New(1<<20, 4<<20)
	if b.Rate() != 1<<20 || b.Capacity() != 4<<20 {
		t.Errorf("Rate/Capacity = %d/%d, want %d/%d", b.Rate(), b.Capacity(), 1<<20, 4<<20)
	}
}

func TestWait_RejectsNegativeCharge(t *testing.T) {
	b, _, _ := newTestBucket(t, 1000, 1000)
	if err := b.Wait(t.Context(), ConsumerMerge, -5); err != nil {
		t.Logf("negative charge → %v", err)
	}
	// Either nil (treated as no-op) or an error is acceptable; it must NOT
	// credit tokens.
	if got := b.Tokens(); got != 1000 {
		t.Errorf("tokens after negative charge = %d, want 1000", got)
	}
}

func ExampleLimiter() {
	var l Limiter = New(50<<20, 64<<20) // ~25% of a 200MB/s SD card
	if err := l.Wait(context.Background(), ConsumerMerge, 1<<20); err != nil {
		fmt.Println("aborted:", err)
	}
}
