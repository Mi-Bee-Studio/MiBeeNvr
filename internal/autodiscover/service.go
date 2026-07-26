package autodiscover

import (
	"context"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// Service orchestrates background ONVIF auto-discovery. It implements
// pkg/app.Service and wires together the passive HelloListener and the active
// Scanner, both feeding the shared Adder.
//
// Lifecycle: Start() launches the configured modes (listener and/or scanner) on
// background goroutines honoring the passed context; Stop() tears them down.
// Service is registered in pkg/app/run.go after the camera manager (so
// camMgr.AddCamera is available) and stops before it on shutdown.
type Service struct {
	cfg     *config.AutoDiscoverConfig
	adder   *Adder
	scanner *Scanner

	mu       sync.Mutex
	listener *onvif.HelloListener
	cancel   context.CancelFunc
	started  bool
}

// New constructs a Service from its dependencies. cfg is the effective
// AutoDiscoverConfig (ApplyDefaults already applied). camMgr is typed as
// CameraEnroller (an interface satisfied by *camera.CameraManager) so the adder
// can be unit-tested with a fake; db/bus are the shared instances from
// pkg/app/run.go, bus may be nil.
func New(cfg *config.AutoDiscoverConfig, camMgr CameraEnroller, db *storage.DB, bus *event.EventBus) *Service {
	adder := NewAdder(cfg, camMgr, db, bus)
	return &Service{
		cfg:     cfg,
		adder:   adder,
		scanner: NewScanner(adder, cfg.ScanIntervalSeconds),
	}
}

// Name implements pkg/app.Service.
func (s *Service) Name() string { return "autodiscover" }

// Start launches the discovery modes. If ListenForHello is enabled, a resident
// UDP 3702 listener is started (best-effort: a bind failure falls back to
// scanner-only mode with a warning, since the scanner still works). The active
// scanner always runs. Returns an error only for programming errors, never for
// network/discovery issues — those are degraded, not fatal.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mu.Unlock()

	// Passive mode: resident Hello listener. Degrades gracefully on bind failure
	// (e.g. port 3702 unavailable, no multicast interface) — the scanner still
	// provides discovery, just with higher latency.
	if s.cfg.ListenForHelloEnabled() {
		listener, err := onvif.NewHelloListener(s.cfg.NetworkInterface, s.handleDiscovered)
		if err != nil {
			logger.Warn("Hello listener construction failed; falling back to scanner-only mode",
				"error", err, "interface", s.cfg.NetworkInterface)
		} else if err := listener.Start(ctx); err != nil {
			logger.Warn("Hello listener start failed; falling back to scanner-only mode",
				"error", err, "interface", s.cfg.NetworkInterface)
		} else {
			s.mu.Lock()
			s.listener = listener
			s.mu.Unlock()
		}
	} else {
		logger.Info("auto-discover passive listener disabled by config (scanner-only mode)")
	}

	// Active mode: periodic Probe sweep. Always runs — it is the backstop for
	// devices that don't send Hello and for listener bind failures.
	go s.scanner.Run(ctx)

	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	logger.Info("auto-discover service started",
		"scan_interval_s", s.cfg.ScanIntervalSeconds,
		"hello_listener", s.cfg.ListenForHelloEnabled(),
		"interface", s.cfg.NetworkInterface)
	return nil
}

// Stop tears down both modes. Idempotent.
func (s *Service) Stop() error {
	s.mu.Lock()
	cancel := s.cancel
	listener := s.listener
	s.listener = nil
	s.cancel = nil
	s.started = false
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if listener != nil {
		listener.Stop()
	}
	return nil
}

// handleDiscovered is the callback wired into the HelloListener. It offloads
// work to a goroutine because the listener runs on a single recv loop and must
// not block on enrichment/DB writes. The scanner already offloads per-device,
// so this mirrors that contract.
func (s *Service) handleDiscovered(dev onvif.DiscoveredDevice) {
	go s.adder.HandleDiscovered(context.Background(), dev)
}

// AdderForTest exposes the Adder for tests that want to drive enrollment
// directly without the listener/scanner. Production code should go through
// Start(). Kept unexported-by-convention via the ForTest suffix to signal intent.
func (s *Service) AdderForTest() *Adder { return s.adder }
