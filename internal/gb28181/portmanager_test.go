package gb28181

import (
	"errors"
	"sync"
	"testing"
)

func TestPortManager_BasicAlloc(t *testing.T) {
	pm := NewPortManager(30000, 30010)
	for range 5 {
		p, err := pm.Get()
		if err != nil {
			t.Fatalf("Get() unexpected error: %v", err)
		}
		if p < 30000 || p > 30010 {
			t.Fatalf("port %d outside range [30000, 30010]", p)
		}
	}
}

func TestPortManager_Recycle(t *testing.T) {
	pm := NewPortManager(30000, 30002)
	for range 3 {
		if _, err := pm.Get(); err != nil {
			t.Fatalf("Get() unexpected error: %v", err)
		}
	}
	// Full range allocated: next Get must fail, not deadlock or panic.
	if _, err := pm.Get(); !errors.Is(err, ErrNoAvailablePorts) {
		t.Fatalf("exhausted pool: expected ErrNoAvailablePorts, got %v", err)
	}
	// Recycle one port, then the next Get must return exactly it.
	pm.Recycle(30001)
	p, err := pm.Get()
	if err != nil {
		t.Fatalf("Get() after recycle unexpected error: %v", err)
	}
	if p != 30001 {
		t.Fatalf("expected recycled port 30001, got %d", p)
	}
}

func TestPortManager_Exhaust(t *testing.T) {
	pm := NewPortManager(30000, 30004)
	for range 5 {
		if _, err := pm.Get(); err != nil {
			t.Fatalf("Get() unexpected error: %v", err)
		}
	}
	for range 3 {
		if _, err := pm.Get(); !errors.Is(err, ErrNoAvailablePorts) {
			t.Fatalf("expected ErrNoAvailablePorts, got %v", err)
		}
	}
}

func TestPortManager_ConcurrentGetRecycle(t *testing.T) {
	pm := NewPortManager(30000, 30020)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				p, err := pm.Get()
				if err != nil {
					t.Errorf("Get() unexpected error: %v", err)
					return
				}
				if p < 30000 || p > 30020 {
					t.Errorf("port %d outside range [30000, 30020]", p)
				}
				pm.Recycle(p)
			}
		}()
	}
	wg.Wait()
}
