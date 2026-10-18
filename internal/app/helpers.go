package app

import (
	"context"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	pionwebrtc "github.com/pion/webrtc/v4"
)

// serviceFunc wraps a pair of start/stop functions as a Service.
type serviceFunc struct {
	name      string
	startFunc func(ctx context.Context) error
	stopFunc  func() error
}

func (s *serviceFunc) Name() string                    { return s.name }
func (s *serviceFunc) Start(ctx context.Context) error { return s.startFunc(ctx) }
func (s *serviceFunc) Stop() error                     { return s.stopFunc() }

// aiConfigFromConfig converts the public AIConfig type to the internal ai.Config.
func aiConfigFromConfig(cfg config.AIConfig) ai.Config {
	return ai.Config{
		Enabled:             cfg.Enabled,
		EnabledCameras:      cfg.EnabledCameras,
		ModelURL:            cfg.ModelURL,
		Zones:               cfg.Zones,
		FrameSkipRate:       cfg.FrameSkipRate,
		ConfidenceThreshold: cfg.ConfidenceThreshold,
		EmaAlpha:            cfg.EmaAlpha,
		MaxAge:              cfg.MaxAge,
		EnabledClasses:      cfg.EnabledClasses,
	}
}

// webrtcICEServers converts the config-layer ICE server list to pion's type.
// Returns nil when empty so the WebRTC Manager falls back to LAN-only behavior
// (no STUN/TURN, mDNS host candidates only) — preserving the legacy default.
func webrtcICEServers(servers []config.ICEServerConfig) []pionwebrtc.ICEServer {
	if len(servers) == 0 {
		return nil
	}
	out := make([]pionwebrtc.ICEServer, 0, len(servers))
	for _, s := range servers {
		ic := pionwebrtc.ICEServer{URLs: s.URLs}
		if s.Username != "" {
			ic.Username = s.Username
			ic.Credential = s.Credential
			ic.CredentialType = pionwebrtc.ICECredentialTypePassword
		}
		out = append(out, ic)
	}
	return out
}
