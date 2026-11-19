package api

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/relayapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
)

// relayDeps adapts *relay.Manager to relayapi.RelayManager, tolerating a nil
// manager (relay disabled). Returning a nil interface (not a typed nil) is what
// keeps the extracted handlers' `deps.Relay == nil` check working.
func relayDeps(m *relay.Manager) relayapi.RelayManager {
	if m == nil {
		return nil
	}
	return m
}
