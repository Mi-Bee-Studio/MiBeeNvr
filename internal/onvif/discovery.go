package onvif

import (
	"context"
	"fmt"
	"time"
)

// Discover performs WS-Discovery to find ONVIF devices on the local network.
func Discover(ctx context.Context, timeout time.Duration) ([]DiscoveredDevice, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// TODO: Implement with onvif-go discovery.Discover(ctx, timeout)
	// For now, return empty result
	logger.Info("starting ONVIF device discovery", "timeout", timeout)

	return nil, fmt.Errorf("WS-Discovery not yet implemented")
}
