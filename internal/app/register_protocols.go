package app

// Protocol service registration: GB28181, ONVIF, Xiaomi and the remaining
// protocol-adjacent wiring. Split out of register.go.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/discovery"
)


// registerProtocolServices — rtmp, whip, rtsp, srt, gb28181-cascade,
// gb28181, discovery, mdns.
func registerProtocolServices(a *App, deps *appDeps) error {
	// 10. rtmp (optional)
	if deps.rtmpServer != nil {
		if err := a.Register(&serviceFunc{
			name: "rtmp",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := deps.rtmpServer.Start(ctx); err != nil {
						slog.Error("rtmp", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				_ = deps.rtmpServer.Stop()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register rtmp: %w", err)
		}
	}

	// 10b. whip (optional) — rides the main HTTP listener; no listener to
	// start, but Stop must tear publisher sessions down cleanly.
	if deps.whipServer != nil {
		if err := a.Register(&serviceFunc{
			name: "whip",
			startFunc: func(ctx context.Context) error {
				deps.whipServer.Start(ctx)
				return nil
			},
			stopFunc: func() error {
				deps.whipServer.Stop()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register whip: %w", err)
		}
	}

	// 10c. rtsp output server (#522, optional — default on). Start runs in a
	// goroutine: a bind failure (port taken by e.g. MediaMTX) must log, not
	// take the app down.
	if deps.rtspServer != nil {
		if err := a.Register(&serviceFunc{
			name: "rtsp",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := deps.rtspServer.Start(ctx); err != nil {
						slog.Error("rtsp", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				_ = deps.rtspServer.Stop()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register rtsp: %w", err)
		}
	}

	// 10d. SNTP server (#time-sync path B, default on). Same leniency as
	// rtsp: a bind failure (unprivileged :123 without capabilities, port
	// taken) logs and the rest of time-sync keeps working — path A does not
	// depend on it.
	if deps.sntpServer != nil && deps.cfg.TimeSync.SNTP.EnabledOrDefault() {
		if err := a.Register(&serviceFunc{
			name: "sntp",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := deps.sntpServer.Start(ctx); err != nil {
						slog.Error("sntp server", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				return deps.sntpServer.Stop()
			},
		}); err != nil {
			return fmt.Errorf("register sntp: %w", err)
		}
	}

	// 11. srt (optional)
	if deps.srtListener != nil {
		if err := a.Register(&serviceFunc{
			name: "srt",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := deps.srtListener.Start(); err != nil {
						slog.Error("srt", "error", err)
					}
				}()
				if err := deps.srtListener.StartCallers(); err != nil {
					slog.Error("srt callers", "error", err)
				}
				return nil
			},
			stopFunc: func() error {
				_ = deps.srtListener.Stop()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register srt: %w", err)
		}
	}
	// 11.45. gb28181-cascade (optional) — GB/T 28181 lower-level client:
	// registers to an upper platform and forwards local cameras on INVITE
	// (#364). Registered after the gb28181 platform server so its Stop
	// (reverse order) runs before the camera service tears down the hubs it
	// subscribes to.
	if deps.gb28181Cascade != nil {
		cascadeSvc := deps.gb28181Cascade
		if err := a.Register(&serviceFunc{
			name: "gb28181-cascade",
			startFunc: func(ctx context.Context) error {
				if err := cascadeSvc.Start(ctx); err != nil {
					slog.Error("gb28181-cascade", "error", err)
				}
				return nil
			},
			stopFunc: func() error { return cascadeSvc.Stop() },
		}); err != nil {
			return fmt.Errorf("register gb28181-cascade: %w", err)
		}
	}
	// 11.5. gb28181 (optional) — GB/T 28181 SIP platform server. Registered
	// after srt and before ws so its Stop (reverse order) runs before the
	// streaming managers tear down. The SIP stack only handles signaling;
	// media sessions are delegated to the session-manager hooks.
	if deps.gb28181Server != nil {
		if err := a.Register(&serviceFunc{
			name: "gb28181",
			startFunc: func(ctx context.Context) error {
				if err := deps.gb28181Server.Start(ctx); err != nil {
					slog.Error("gb28181", "error", err)
				}
				return nil
			},
			stopFunc: func() error {
				// BYE active dialogs FIRST (while the SIP stack is still up)
				// so single-stream devices release their dialog, resume
				// re-registering, and stop streaming into recycled ports.
				deps.gb28181Server.ByeAllSessions()
				_ = deps.gb28181Server.Stop()
				// Reap all media sessions (UDP sockets, receiver goroutines,
				// port pool) before process exit — the SIP stack alone does
				// not own them.
				deps.gb28181SessionMgr.StopAll()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register gb28181: %w", err)
		}
	}

	// 11.7. discovery (optional) — UDP broadcast responder (MIBEE-NVR-DISC/v1)
	// for LAN clients on multicast-restricted networks. Bind failures are
	// logged, never fatal: discovery is a convenience path. Treated as enabled
	// when the pointer is nil so manually-constructed configs (tests) behave
	// like ApplyDefaults output. Port 0 binds an ephemeral port (tests);
	// ApplyDefaults supplies 49090 for real configs.
	if deps.cfg.Server.Discovery.UDP.Enabled == nil || *deps.cfg.Server.Discovery.UDP.Enabled {
		responder := discovery.NewUDPResponder(
			deps.cfg.Server.DeviceID,
			deps.cfg.Server.DeviceName,
			discovery.ParseAPIPort(deps.cfg.Server.Listen),
			discovery.APISpeaksTLS(deps.cfg.Server.Listen, deps.cfg.Server.TLSListen),
			deps.cfg.Server.Discovery.UDP.Port,
		)
		if err := a.Register(&serviceFunc{
			name: "discovery",
			startFunc: func(ctx context.Context) error {
				if err := responder.Start(ctx); err != nil {
					slog.Error("discovery: udp responder disabled", "error", err)
				}
				return nil
			},
			stopFunc: func() error {
				return responder.Stop()
			},
		}); err != nil {
			return fmt.Errorf("register discovery: %w", err)
		}
	}

	// 11.8. mdns (optional) — DNS-SD service registration (_mibee-nvr._tcp)
	// for fast LAN discovery; same non-fatal failure policy as the UDP
	// responder (a resident avahi on 5353 must not block startup).
	if deps.cfg.Server.Discovery.MDNS.Enabled == nil || *deps.cfg.Server.Discovery.MDNS.Enabled {
		registrar := discovery.NewMDNSRegistrar(
			deps.cfg.Server.DeviceID,
			deps.cfg.Server.DeviceName,
			discovery.ParseAPIPort(deps.cfg.Server.Listen),
			discovery.APISpeaksTLS(deps.cfg.Server.Listen, deps.cfg.Server.TLSListen),
		)
		if err := a.Register(&serviceFunc{
			name: "mdns",
			startFunc: func(ctx context.Context) error {
				if err := registrar.Start(ctx); err != nil {
					slog.Error("discovery: mdns registration disabled", "error", err)
				}
				return nil
			},
			stopFunc: func() error {
				return registrar.Stop()
			},
		}); err != nil {
			return fmt.Errorf("register mdns: %w", err)
		}
	}

	return nil
}

// registerEgressServices — ws, webrtc, hls, api-handler, remoteLog.
func registerEgressServices(a *App, deps *appDeps) error {
	// 12. ws
	if err := a.Register(&serviceFunc{
		name: "ws",
		startFunc: func(_ context.Context) error {
			return nil
		},
		stopFunc: func() error {
			deps.wsMgr.StopAll()
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register ws: %w", err)
	}

	// 13. webrtc (optional)
	if deps.webrtcMgr != nil {
		if err := a.Register(&serviceFunc{
			name: "webrtc",
			startFunc: func(_ context.Context) error {
				return nil
			},
			stopFunc: func() error {
				deps.webrtcMgr.StopAll()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register webrtc: %w", err)
		}
	}

	// 14. hls
	if err := a.Register(&serviceFunc{
		name: "hls",
		startFunc: func(_ context.Context) error {
			return nil
		},
		stopFunc: func() error {
			deps.hlsMgr.StopAll()
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register hls: %w", err)
	}

	// 15. api-handler — close tracked timelapse-merge goroutines spawned by
	// handleTimelapseMerge / handleTimelapseBatchMerge. Registered after the
	// streaming services so its Stop (reverse order) runs BEFORE db close but
	// AFTER ws/webrtc/hls — by then the HTTP server (stopped by main.go before
	// a.Stop) has drained in-flight requests, so no new merges can start, and
	// we only wait for already-running merges to finish. See #143 / #125.
	if err := a.Register(&serviceFunc{
		name: "api-handler",
		startFunc: func(_ context.Context) error {
			return nil
		},
		stopFunc: func() error {
			deps.handler.Close()
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register api-handler: %w", err)
	}

	// 16. remoteLog (optional) — registered last so it stops first
	if deps.remoteLogH != nil {
		if err := a.Register(&serviceFunc{
			name: "remoteLog",
			startFunc: func(_ context.Context) error {
				return nil
			},
			stopFunc: func() error {
				deps.remoteLogH.Close()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register remoteLog: %w", err)
		}
	}

	return nil
}
