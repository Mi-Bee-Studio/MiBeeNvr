// Package iobudget implements a process-level token bucket that paces
// background batch I/O (segment merges, cleanup/repair deletes, timelapse
// frame extraction) so it yields to foreground work (recording writes, API,
// SQLite), which never waits on the bucket.
//
// Background and foreground traffic share one kernel I/O queue; when they
// compete as equals, production boxes measured ~60% PSI io-some with multi-
// second API stalls (#751). Go's M:N threading makes per-thread ioprio
// unreliable and cgroup io controllers cannot separate goroutines inside one
// process, so an application-level bucket is the only mechanism that behaves
// identically across every supported deployment (RPi / Armbian / Debian /
// Docker).
//
// A nil *Bucket (or a Limiter holding one) is valid and disabled — Wait
// returns immediately, costing one nil check. Callers therefore never need
// their own nil guards beyond the package helpers.
package iobudget

import (
	"context"
	"sync"
	"time"
)

// Consumer labels identify the billing party in metrics. Bounded enum — do
// not extend with unbounded values (metrics AGENTS).
const (
	ConsumerMerge     = "merge"
	ConsumerCleanup   = "cleanup"
	ConsumerRepair    = "repair"
	ConsumerTimelapse = "timelapse"
)

// Limiter is the consumer-facing surface of a Bucket. Declared so dependent
// packages (merge, cleanup, timelapse, repair CLI) can inject fakes in tests
// without importing a concrete type.
type Limiter interface {
	Wait(ctx context.Context, consumer string, n int64) error
}

// Bucket is a refill-rate token bucket in an abstract unit — bytes for the
// I/O budget, files for the unlink guardrail (#755). Tokens accrue at rate
// per second up to capacity. New returns nil for disabled configurations
// (rate <= 0 or capacity <= 0); the nil receiver's Wait is a no-op.
type Bucket struct {
	rate     int64 // tokens per second
	capacity int64 // maximum burst

	mu     sync.Mutex
	tokens int64
	last   time.Time

	// now and sleepFn are seams for tests; set only before first use.
	now     func() time.Time
	sleepFn func(ctx context.Context, d time.Duration) error

	// onWait/onCharge are optional observers (metrics wiring). They are set
	// once at construction and never mutated afterwards.
	onWait   func(consumer string, waited time.Duration)
	onCharge func(consumer string, n int64)
}

// Option customizes a Bucket.
type Option func(*Bucket)

// WithObservers attaches metric hooks: onWait fires once per successful Wait
// with the total time that call spent parked; onCharge fires per charge with
// the requested (not clamped) amount.
func WithObservers(onWait func(consumer string, waited time.Duration), onCharge func(consumer string, n int64)) Option {
	return func(b *Bucket) {
		b.onWait = onWait
		b.onCharge = onCharge
	}
}

// New creates a Bucket refilling rate units/second with the given burst
// capacity, starting full. It returns nil when disabled (rate <= 0 or
// capacity <= 0) — callers store the result directly and nil means
// "budgeting off, zero overhead".
func New(rate, capacity int64, opts ...Option) *Bucket {
	if rate <= 0 || capacity <= 0 {
		return nil
	}
	b := &Bucket{
		rate:     rate,
		capacity: capacity,
		tokens:   capacity,
		now:      time.Now,
		sleepFn:  sleepContext,
	}
	for _, opt := range opts {
		opt(b)
	}
	b.last = b.now()
	return b
}

// Rate returns the refill rate (0 for a nil bucket).
func (b *Bucket) Rate() int64 {
	if b == nil {
		return 0
	}
	return b.rate
}

// Capacity returns the burst capacity (0 for a nil bucket).
func (b *Bucket) Capacity() int64 {
	if b == nil {
		return 0
	}
	return b.capacity
}

// Enabled reports whether the bucket rate-limits.
func (b *Bucket) Enabled() bool {
	return b != nil && b.rate > 0
}

// Wait blocks until n units can be charged to consumer, or until ctx is done.
// Charges larger than the capacity are split into capacity-sized batches so a
// single oversized bill (e.g. deleting a 5GB segment under a 128MB budget)
// paces instead of deadlocking; between batches other consumers interleave,
// which keeps the split fair. A ctx cancellation mid-bill keeps tokens
// already consumed (no refund) — pacing slightly overcharges, never
// undercharges.
func (b *Bucket) Wait(ctx context.Context, consumer string, n int64) error {
	if b == nil || n <= 0 {
		return nil
	}
	start := b.now()
	remaining := n
	for remaining > 0 {
		batch := min(remaining, b.capacity)
		if err := b.waitBatch(ctx, consumer, batch); err != nil {
			return err
		}
		remaining -= batch
	}
	if b.onCharge != nil {
		b.onCharge(consumer, n)
	}
	if b.onWait != nil {
		if waited := b.now().Sub(start); waited > 0 {
			b.onWait(consumer, waited)
		}
	}
	return nil
}

// waitBatch acquires one capacity-bounded batch, sleeping in ctx-cancellable
// increments until refill covers the deficit.
func (b *Bucket) waitBatch(ctx context.Context, consumer string, batch int64) error {
	for {
		b.mu.Lock()
		b.refillLocked()
		if b.tokens >= batch {
			b.tokens -= batch
			b.mu.Unlock()
			return nil
		}
		deficit := batch - b.tokens
		b.mu.Unlock()

		d := ceilDivSeconds(deficit, b.rate)
		if err := b.sleepFn(ctx, d); err != nil {
			return err
		}
	}
}

// refillLocked advances the token balance to the present, capped at capacity.
// Callers hold b.mu.
func (b *Bucket) refillLocked() {
	nowT := b.now()
	elapsed := nowT.Sub(b.last)
	if elapsed <= 0 {
		return
	}
	refill := int64(elapsed) * b.rate / int64(time.Second)
	if refill <= 0 {
		return
	}
	b.tokens = min(b.capacity, b.tokens+refill)
	b.last = nowT
}

// Tokens returns the current balance (white-box testing aid; capacity for a
// full bucket).
func (b *Bucket) Tokens() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	return b.tokens
}

// ceilDivSeconds converts a token deficit at a given rate into a duration of
// at least one nanosecond (so the sleep seam is always observable), rounding
// up so pacing never under-waits.
func ceilDivSeconds(deficit, rate int64) time.Duration {
	if deficit <= 0 {
		return time.Nanosecond
	}
	ns := (deficit*int64(time.Second) + rate - 1) / rate
	return time.Duration(max(ns, 1))
}

// sleepContext parks for d or until ctx is done.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
