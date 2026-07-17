package autodiscover

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

func TestService_Name(t *testing.T) {
	t.Helper()
	s := New(&config.AutoDiscoverConfig{}, nil, nil, nil)
	if got := s.Name(); got != "autodiscover" {
		t.Errorf("Name() = %q, want autodiscover", got)
	}
}

func TestService_StartStopIdempotent(t *testing.T) {
	t.Helper()
	// Disabled listener (no real network) + a scanner that exits on ctx cancel.
	// ListenForHello is left default-true but NewHelloListener will fail to bind
	// a real multicast socket in CI; Service must degrade to scanner-only rather
	// than error. We assert Start returns nil and Stop is clean.
	cfg := &config.AutoDiscoverConfig{
		ScanIntervalSeconds: 30,
	}
	s := New(cfg, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start returned error: %v (listener bind failure must degrade, not error)", err)
	}
	// Double Start is a no-op (started flag).
	if err := s.Start(ctx); err != nil {
		t.Fatalf("second Start returned error: %v", err)
	}
	// Give the scanner a moment to enter its loop, then stop.
	time.Sleep(50 * time.Millisecond)
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	// Double Stop is a no-op.
	if err := s.Stop(); err != nil {
		t.Fatalf("second Stop returned error: %v", err)
	}
}

func TestNewScanner_EnforcesMinimumInterval(t *testing.T) {
	t.Helper()
	// ScanIntervalSeconds below 30 must be clamped to 30 — RPi-3B safety.
	adder := NewAdder(&config.AutoDiscoverConfig{}, nil, nil, nil)
	sc := NewScanner(adder, 5)
	if sc.interval != 30*time.Second {
		t.Errorf("interval = %v, want 30s (clamped floor)", sc.interval)
	}
}

func TestNewScanner_RespectsValidInterval(t *testing.T) {
	t.Helper()
	adder := NewAdder(&config.AutoDiscoverConfig{}, nil, nil, nil)
	sc := NewScanner(adder, 60)
	if sc.interval != 60*time.Second {
		t.Errorf("interval = %v, want 60s", sc.interval)
	}
}
