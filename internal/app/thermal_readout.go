package app

import (
	"context"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/thermal"
)

// thermalReadoutService polls the device-side temperature readout endpoints
// configured on thermal cameras (cameras[].thermal_readout) and republishes
// the readings on the SSE bus under thermal.temperature — the live-view
// temperature chip lights up with no player changes. Boot-wired: adding or
// changing a readout source applies on restart.
type thermalReadoutService struct {
	sources map[string]thermal.ReadoutSource
	bus     *event.EventBus
}

// newThermalReadoutService collects the poller sources from camera configs —
// thermal cameras carrying a usable thermal_readout block only (SPEC JSON
// endpoint, or kind: hikvision — the ISAPI radiometric frame).
func newThermalReadoutService(cfg *config.Config, bus *event.EventBus) *thermalReadoutService {
	sources := make(map[string]thermal.ReadoutSource)
	if cfg == nil {
		return &thermalReadoutService{bus: bus}
	}
	for i := range cfg.Cameras {
		if src, ok := thermal.SourceForCamera(&cfg.Cameras[i]); ok {
			sources[cfg.Cameras[i].ID] = src
		}
	}
	return &thermalReadoutService{sources: sources, bus: bus}
}

func (s *thermalReadoutService) Name() string { return "thermal-readout" }

func (s *thermalReadoutService) Start(ctx context.Context) error {
	// A nil bus makes every publish a no-op — still correct (the chip just
	// stays in its waiting state), so the service starts unconditionally.
	thermal.StartReadoutPoller(ctx, thermal.NewFeed(s.bus), s.sources, nil)
	return nil
}

func (s *thermalReadoutService) Stop() error { return nil } // ctx cancel stops the loops
