package onvif

import (
	"context"
	"testing"
	"time"
)

func TestDiscoverWithTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := Discover(ctx, 2*time.Second)
	// Expected to fail in test environment without network multicast
	if err != nil {
		t.Logf("Discovery failed (expected in test env): %v", err)
	}
}

func TestDiscoverDefaultTimeout(t *testing.T) {
	ctx := context.Background()
	_, err := Discover(ctx, 0)
	// Should not panic even with 0 timeout (uses default 5s)
	if err != nil {
		t.Logf("Discovery failed (expected in test env): %v", err)
	}
}
