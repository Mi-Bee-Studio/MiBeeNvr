package recorder

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// runReconnectLoopHarness wires runReconnectLoop to record every observable
// side effect (connect attempts, status transitions, error-counter bumps).
type runReconnectLoopHarness struct {
	mu        sync.Mutex
	attempts  int
	statuses  []model.RecorderStatus
	errTypes  []string
	connect   func(attempt int) (error, bool)
	connectMu sync.Mutex
}

func (h *runReconnectLoopHarness) recordAttempt() int {
	h.connectMu.Lock()
	defer h.connectMu.Unlock()
	h.attempts++
	return h.attempts
}

func (h *runReconnectLoopHarness) Connect(_ context.Context) (error, bool) {
	return h.connect(h.recordAttempt())
}

func (h *runReconnectLoopHarness) RecordError(t string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errTypes = append(h.errTypes, t)
}

func (h *runReconnectLoopHarness) SetStatus(s model.RecorderStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.statuses = append(h.statuses, s)
}

func (h *runReconnectLoopHarness) recordedStatuses() []model.RecorderStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]model.RecorderStatus(nil), h.statuses...)
}

func TestRunReconnectLoopRetriesUntilContextDone(t *testing.T) {
	h := &runReconnectLoopHarness{
		connect: func(int) (error, bool) { return errors.New("boom"), false },
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel while the loop is sleeping in its first backoff.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	runReconnectLoop(ctx, reconnectDeps{
		CameraID:    "test-cam",
		Log:         slog.Default(),
		Connect:     h.Connect,
		RecordError: h.RecordError,
		SetStatus:   h.SetStatus,
	})
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("loop did not exit promptly on ctx cancel (took %s)", elapsed)
	}
	if h.attempts < 1 {
		t.Fatalf("expected at least one connect attempt, got %d", h.attempts)
	}
	statuses := h.recordedStatuses()
	if len(statuses) == 0 || statuses[len(statuses)-1] != model.StatusReconnecting {
		t.Fatalf("expected StatusReconnecting transitions, got %v", statuses)
	}
	for _, e := range h.errTypes {
		if e != "connection" {
			t.Fatalf("unexpected error counter type %q", e)
		}
	}
	if len(h.errTypes) == 0 {
		t.Fatal("expected RecordError to be called")
	}
}

func TestRunReconnectLoopConnectedAttemptDoesNotRecordError(t *testing.T) {
	h := &runReconnectLoopHarness{
		connect: func(attempt int) (error, bool) {
			if attempt == 1 {
				// First attempt "connected" but the stream later died with a
				// ctx cancellation — the loop must exit without any failure
				// bookkeeping.
				return ctxErrAdapter{}, true
			}
			return errors.New("unreachable"), false
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: first Connect returns, loop sees ctx.Err() and exits
	runReconnectLoop(ctx, reconnectDeps{
		CameraID:    "test-cam",
		Log:         slog.Default(),
		Connect:     h.Connect,
		RecordError: h.RecordError,
		SetStatus:   h.SetStatus,
	})
	if len(h.errTypes) != 0 {
		t.Fatalf("cancelled ctx must not record errors, got %v", h.errTypes)
	}
	if len(h.recordedStatuses()) != 0 {
		t.Fatalf("cancelled ctx must not transition status, got %v", h.recordedStatuses())
	}
	if h.attempts != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", h.attempts)
	}
}

// ctxErrAdapter returns a non-nil error from Connect while ctx is already
// cancelled — the loop keys its exit on ctx.Err(), not on err == nil.
type ctxErrAdapter struct{}

func (ctxErrAdapter) Error() string { return "cancelled during connect" }

// nextBackoff floors the shared tiered backoff at a recorder-specific minimum
// (#711): ESP32-class MJPEG cameras treat sub-5s reconnects as hammering —
// the camera-side anti-hammer guard answers 503 with its own exponential
// backoff — so the HTTP JPEG puller raises the floor to 5s.
func TestNextBackoffFloor(t *testing.T) {
	t.Parallel()

	t.Run("default keeps tier 1 range", func(t *testing.T) {
		t.Parallel()
		for range 25 {
			b := nextBackoff(1, false, 0)
			if b < time.Second || b >= 2*time.Second {
				t.Fatalf("attempt-1 backoff without floor must stay in [1s,2s), got %s", b)
			}
		}
	})

	t.Run("floor raises low tiers only", func(t *testing.T) {
		t.Parallel()
		const floor = 5 * time.Second
		for range 25 {
			if b := nextBackoff(1, false, floor); b < min {
				t.Fatalf("attempt-1 backoff must respect the %s floor, got %s", floor, b)
			}
			if b := nextBackoff(4, false, floor); b < min {
				t.Fatalf("attempt-4 backoff must respect the %s floor, got %s", floor, b)
			}
		}
		// High tiers already exceed the floor — untouched.
		if b := nextBackoff(25, false, floor); b < time.Minute {
			t.Fatalf("attempt-25 backoff must stay at the 60s tier, got %s", b)
		}
	})

	t.Run("storage backoff is never shortened", func(t *testing.T) {
		t.Parallel()
		for range 25 {
			if b := nextBackoff(1, true, 5*time.Second); b < time.Minute {
				t.Fatalf("storage backoff must stay ~60s regardless of floor, got %s", b)
			}
		}
	})
}

// TestRunReconnectLoopAppliesMinBackoff proves the LOOP (not just the pure
// helper) sleeps at least MinBackoff between attempts: floor 2.5s exceeds the
// tier-1 ceiling (1s + ≤1s jitter = <2s), so any inter-attempt gap ≥2.5s can
// only come from the floor being applied.
func TestRunReconnectLoopAppliesMinBackoff(t *testing.T) {
	var mu sync.Mutex
	attempts := make([]time.Time, 0, 2)
	h := &runReconnectLoopHarness{
		connect: func(int) (error, bool) {
			mu.Lock()
			attempts = append(attempts, time.Now())
			mu.Unlock()
			// Exit is driven by the gap watcher below (cancelling from inside
			// Connect would race the loop's own ctx read).
			return errors.New("boom"), false
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runReconnectLoop(ctx, reconnectDeps{
			CameraID:    "test-cam",
			Log:         slog.Default(),
			Connect:     h.Connect,
			RecordError: h.RecordError,
			SetStatus:   h.SetStatus,
			MinBackoff:  2500 * time.Millisecond,
		})
	}()
	// Wait for the second attempt, then cancel so the loop exits promptly.
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(attempts)
		mu.Unlock()
		if n >= 2 {
			cancel()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("second connect attempt never happened")
		}
		time.Sleep(20 * time.Millisecond)
	}
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(attempts) < 2 {
		t.Fatalf("expected 2 attempts, got %d", len(attempts))
	}
	if gap := attempts[1].Sub(attempts[0]); gap < 2500*time.Millisecond {
		t.Fatalf("inter-attempt gap must respect the 2.5s floor, got %s", gap)
	}
}
