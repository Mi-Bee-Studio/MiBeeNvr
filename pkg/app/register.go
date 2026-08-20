package app

// register.go contains registerServices — the registration phase of RunFree.
// It registers every constructed manager as an App service in the exact
// start/stop order. Start order = registration order; Stop is the reverse.
// This order is load-bearing and verified by TestRunFree_ServiceOrder.
//
// The split is purely structural (#138): the closures are identical to the
// historical RunFree body, only reading from deps.* instead of local vars.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/autodiscover"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/discovery"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// registerServices registers all services on the App in start/stop order,
// reading constructed managers from deps. Returns an error if any registration
// fails (the caller invokes deps' cleanup func on failure).
func registerServices(a *App, deps *appDeps) error {
	// 1. db — registered first so it stops last
	if err := a.Register(&serviceFunc{
		name: "db",
		startFunc: func(_ context.Context) error {
			return nil
		},
		stopFunc: func() error {
			deps.db.Close()
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register db: %w", err)
	}

	// 2. startup-bg — join the two background goroutines launched in buildAppDeps
	// (CleanupTempFiles + ReconcileOrphanedFiles). Registered right after db so
	// its Stop runs just before db.Close in the reverse-order teardown: cancel
	// their ctx + wait for them to exit, so they're not still walking/writing
	// the storage tree (t.TempDir) when cleanup proceeds. See #143 / #125.
	if err := a.Register(&serviceFunc{
		name: "startup-bg",
		startFunc: func(_ context.Context) error {
			return nil
		},
		stopFunc: func() error {
			deps.startupBgCancel()
			deps.startupBgWG.Wait()
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register startup-bg: %w", err)
	}

	// 3. camera — relay folded in (relay starts before camera, stops before camera)
	if err := a.Register(&serviceFunc{
		name: "camera",
		startFunc: func(ctx context.Context) error {
			if deps.relayMgr != nil {
				deps.relayMgr.Start(ctx)
			}
			go func() {
				if err := deps.camMgr.Start(ctx); err != nil {
					slog.Error("camera manager", "error", err)
				}
			}()
			return nil
		},
		stopFunc: func() error {
			if deps.relayMgr != nil {
				deps.relayMgr.Stop()
			}
			if err := deps.camMgr.Stop(); err != nil {
				slog.Warn("camera manager stop error", "error", err)
			}
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register camera: %w", err)
	}

	// 3. health (always present)
	if err := a.Register(&serviceFunc{
		name: "health",
		startFunc: func(ctx context.Context) error {
			if err := deps.healthMgr.Start(ctx); err != nil {
				slog.Error("health manager", "error", err)
			}
			return nil
		},
		stopFunc: func() error {
			deps.healthMgr.Stop()
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register health: %w", err)
	}

	// 3.5. auto-discover (optional, default OFF). Continuously discovers ONVIF
	// devices (passive Hello listener + periodic Probe sweep) and auto-enrolls
	// them. Registered after camera+health so camMgr.AddCamera is available, and
	// stops before camera on shutdown (reverse order) so it can't add a camera
	// while the manager is tearing down.
	if deps.cfg.AutoDiscover.AutoDiscoverEnabled() {
		adSvc := autodiscover.New(&deps.cfg.AutoDiscover, deps.camMgr, deps.db, deps.eventBus)
		if err := a.Register(adSvc); err != nil {
			return fmt.Errorf("register autodiscover: %w", err)
		}
	}

	// 4. merge — run in background goroutine with its own cancel
	{
		var mergeCancel context.CancelFunc
		var mergeDone chan struct{}
		if err := a.Register(&serviceFunc{
			name: "merge",
			startFunc: func(ctx context.Context) error {
				var runCtx context.Context
				runCtx, mergeCancel = context.WithCancel(ctx)
				mergeDone = make(chan struct{})
				go func() {
					defer close(mergeDone)
					if deps.cfg.Merge.Enabled {
						deps.mergeMgr.Run(runCtx)
						slog.Info("merge-manager stopped")
					}
				}()
				return nil
			},
			stopFunc: func() error {
				if mergeCancel != nil {
					mergeCancel()
				}
				// Join the Run goroutine so it's not still walking/writing the
				// storage tree after App.Stop returns (#143 / #125 class).
				if mergeDone != nil {
					<-mergeDone
				}
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register merge: %w", err)
		}
	}

	// 4.1. rolling-merge — event-driven quasi-real-time merge
	{
		var rollingCancel context.CancelFunc
		if err := a.Register(&serviceFunc{
			name: "rolling-merge",
			startFunc: func(ctx context.Context) error {
				var runCtx context.Context
				runCtx, rollingCancel = context.WithCancel(ctx)
				return deps.recordRollingMergeMgr.Start(runCtx)
			},
			stopFunc: func() error {
				if rollingCancel != nil {
					rollingCancel()
				}
				deps.recordRollingMergeMgr.Stop()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register rolling-merge: %w", err)
		}
	}

	// 4.2. vision-push — NVR→Vision segment push coordinator (optional)
	if deps.visionMgr != nil {
		var visionCancel context.CancelFunc
		if err := a.Register(&serviceFunc{
			name: "vision-push",
			startFunc: func(ctx context.Context) error {
				var runCtx context.Context
				runCtx, visionCancel = context.WithCancel(ctx)
				return deps.visionMgr.Start(runCtx)
			},
			stopFunc: func() error {
				if visionCancel != nil {
					visionCancel()
				}
				deps.visionMgr.Stop()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register vision-push: %w", err)
		}
	}

	// 4.3. motion-score — offline compressed-domain activity scoring
	// (issue #435). Subscribes to SegmentCompleted; started before cleanup so
	// scores exist by the time disk-threshold ordering consults them, and
	// stopped (reverse order) before the merge services it observes.
	if deps.motionAnalyzer != nil {
		if err := a.Register(deps.motionAnalyzer); err != nil {
			return fmt.Errorf("register motion-score: %w", err)
		}
	}

	// 5. transcode (optional)
	if deps.transcodeMgr != nil {
		if err := a.Register(&serviceFunc{
			name: "transcode",
			startFunc: func(ctx context.Context) error {
				go deps.transcodeMgr.Run(ctx)
				return nil
			},
			stopFunc: func() error {
				deps.transcodeMgr.Stop()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register transcode: %w", err)
		}
	}

	// 6. mergeScheduler
	if err := a.Register(&serviceFunc{
		name: "mergeScheduler",
		startFunc: func(ctx context.Context) error {
			deps.mergeScheduler.Start(ctx)
			return nil
		},
		stopFunc: func() error {
			deps.mergeScheduler.Stop()
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register mergeScheduler: %w", err)
	}

	// 7. cleanup — run in background goroutine with its own cancel
	{
		var cleanupCancel context.CancelFunc
		var cleanupDone chan struct{}
		if err := a.Register(&serviceFunc{
			name: "cleanup",
			startFunc: func(ctx context.Context) error {
				var runCtx context.Context
				runCtx, cleanupCancel = context.WithCancel(ctx)
				cleanupDone = make(chan struct{})
				go func() {
					defer close(cleanupDone)
					deps.cleanupMgr.Run(runCtx)
				}()
				return nil
			},
			stopFunc: func() error {
				if cleanupCancel != nil {
					cleanupCancel()
				}
				// Join the Run goroutine so it's not still deleting files under
				// the storage tree after App.Stop returns (#143 / #125 class).
				if cleanupDone != nil {
					<-cleanupDone
				}
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register cleanup: %w", err)
		}
	}

	// 7.5. archive-deleter — background cleanup of deleted archived cameras
	if err := a.Register(&serviceFunc{
		name: "archive-deleter",
		startFunc: func(ctx context.Context) error {
			return deps.archiveDeleter.Start(ctx)
		},
		stopFunc: func() error {
			return deps.archiveDeleter.Stop()
		},
	}); err != nil {
		return fmt.Errorf("register archive-deleter: %w", err)
	}

	// 8. mqtt (optional)
	if deps.mqttClient != nil {
		if err := a.Register(&serviceFunc{
			name: "mqtt",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := deps.mqttClient.Start(ctx); err != nil {
						slog.Error("mqtt", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				if err := deps.mqttClient.Stop(); err != nil {
					slog.Warn("MQTT stop error", "error", err)
				}
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register mqtt: %w", err)
		}
	}

	// 9. ftp (optional)
	if deps.ftpServer != nil {
		if err := a.Register(&serviceFunc{
			name: "ftp",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := deps.ftpServer.Start(ctx); err != nil {
						slog.Error("ftp", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				deps.ftpServer.Close()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register ftp: %w", err)
		}
	}

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
			deps.cfg.Server.TLSListen != "",
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
			deps.cfg.Server.TLSListen != "",
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

// registerValues exposes typed handles for out-of-module consumers (e.g.
// MiBeeNvrP2P) via a.Value(...). Order is not load-bearing but is preserved
// from the historical RunFree for stability.
func registerValues(a *App, deps *appDeps) error {
	if err := a.RegisterValue("camera-manager", deps.camMgr.AsPublic()); err != nil {
		return fmt.Errorf("register camera-manager value: %w", err)
	}
	if err := a.RegisterValue("relay-manager", deps.relayMgr); err != nil {
		return fmt.Errorf("register relay-manager value: %w", err)
	}
	if err := a.RegisterValue("eventbus", event.NewBusAdapter(deps.eventBus)); err != nil {
		return fmt.Errorf("register eventbus value: %w", err)
	}
	if err := a.RegisterValue("config", deps.cfg); err != nil {
		return fmt.Errorf("register config value: %w", err)
	}
	if err := a.RegisterValue("http-router", deps.router); err != nil {
		return fmt.Errorf("register http-router value: %w", err)
	}
	if err := a.RegisterValue("http-server", deps.httpServer); err != nil {
		return fmt.Errorf("register http-server value: %w", err)
	}
	return nil
}
