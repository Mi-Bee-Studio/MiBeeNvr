package app

// p2p_service.go adapts the internal/p2p device-role Manager onto the App
// service lifecycle. The manager itself owns the supervision loop; this file
// only handles construction (local addr derivation, token state path) and
// start/stop plumbing.

import (
	"context"
	"log/slog"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2p"
)

// registerP2PService wires the P2P device agent as an App service.
func registerP2PService(a *App, deps *appDeps) error {
	cfg := deps.cfg.P2P
	mgr := p2p.New(cfg, cfg.LocalAddrFromListen(deps.cfg.Server.Listen),
		p2p.StatePathFor(deps.configPath), slog.Default())

	return a.Register(&serviceFunc{
		name: "p2p",
		startFunc: func(ctx context.Context) error {
			mgr.Start(ctx)
			return nil
		},
		stopFunc: func() error {
			mgr.Stop() // App.Stop 不取消 start ctx，必须显式终结
			return nil
		},
	})
}
