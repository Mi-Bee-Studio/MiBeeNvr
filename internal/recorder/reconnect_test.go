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
