package app

// builders_streaming.go — buildAppDeps phase 3: egress streaming managers
// (HLS/WebRTC/FLV/WS), relay, and the ingest/protocol servers (RTMP, WHIP,
// RTSP output, SRT, GB28181 platform + cascade).

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mickeyzzc/gb28181-go/platform"
	gbcascade "github.com/mickeyzzc/gb28181-go/platform/cascade"
	gbsip "github.com/mickeyzzc/gb28181-go/platform/sip"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/rtmp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/rtsp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/srt"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/whip"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
)

// buildStreamingDeps mutates deps with the streaming/ingest domain and returns
// the two values that intentionally do NOT live in appDeps:
//   - flvMgr: consumed by the stream registry + API wiring only, never a
//     service (FLV has no Stop lifecycle);
//   - gbLibEvents: the gb28181-go library event bus, shared with the GB28181
//     snapshot session manager wired later in the HTTP phase.
//
// The only error path is the GB 35114 security seam (boot-fatal by design).
func buildStreamingDeps(deps *appDeps) (flvMgr *flv.Manager, gbLibEvents *gbsip.EventBus, err error) {
	cfg := deps.cfg
	db := deps.db
	m := deps.metrics
	camMgr := deps.camMgr

	// Step 7: HLS manager
	// HLS is a transient live-stream cache — keep it on the data volume so
	// the recordings volume holds only recordings (and survives root switches).
	hlsDataDir := filepath.Join(cfg.Storage.RootDir, "hls")
	if dd := dataDir(); dd != "" {
		hlsDataDir = filepath.Join(dd, "hls")
	}
	hlsMgr := hls.NewManagerWithOpts(context.Background(), hlsDataDir, cfg.HLS.WriteBufferSize, cfg.HLS.SegmentMaxSizeMB*1024*1024, cfg.HLS.SegmentCount, m)
	// Low-Latency HLS is wired to hls.low_latency (#772): it defaults to on
	// (the SPA's hls.js mounts with lowLatencyMode and native iOS/AVPlayer
	// players consume parts); an explicit `low_latency: false` switches egress
	// to classic segment playlists (H264→MPEG-TS, H265→fMP4) for plain-HLS
	// clients. Takes effect at startup.
	partDur, _ := time.ParseDuration(cfg.HLS.PartMinDuration)
	hlsMgr.SetLowLatency(cfg.HLS.LowLatencyEnabled(), partDur)
	deps.hlsMgr = hlsMgr

	// Step 7.5: WebRTC manager (H.264 only)
	if cfg.Streaming.WebRTC.Enabled != nil && *cfg.Streaming.WebRTC.Enabled {
		idleTimeout, _ := time.ParseDuration(cfg.Streaming.WebRTC.IdleTimeout)
		deps.webrtcMgr = webrtc.NewManager(
			webrtc.WithMaxPeers(cfg.Streaming.WebRTC.MaxViewers),
			webrtc.WithIdleTimeout(idleTimeout),
			webrtc.WithMetrics(m),
			webrtc.WithICEServers(webrtcICEServers(cfg.Streaming.WebRTC.ICEServers)),
		)
		// Per-session teardown anchor for quality=sub WHEP sessions (#513):
		// the handler acquires one sub-stream reference per session (WHEP
		// sessions outlive their HTTP request); this hook releases it when
		// the session is deleted (viewer DELETE, connection-state failure, or
		// the idle watchdog). Main-key sessions carry no suffix and no-op.
		deps.webrtcMgr.SetOnSessionEnd(func(streamKey string) {
			if id, isSub := strings.CutSuffix(streamKey, streamhub.SubStreamKeySuffix); isSub {
				camMgr.ReleaseSubStream(id)
			}
		})
		slog.Info(
			"WebRTC manager initialized",
			"max_viewers", cfg.Streaming.WebRTC.MaxViewers,
			"ice_servers", len(cfg.Streaming.WebRTC.ICEServers),
		)
	}

	// Step 7.6: FLV manager (constructed for the stream registry; not registered as a service)
	if cfg.Streaming.FLV.Enabled != nil && *cfg.Streaming.FLV.Enabled {
		flvMgr = flv.NewManager(
			flv.WithMaxViewers(cfg.Streaming.FLV.MaxViewers),
			flv.WithMetrics(m),
		)
		slog.Info("FLV manager initialized", "max_viewers", cfg.Streaming.FLV.MaxViewers)
	}
	// NOTE: flvMgr is intentionally NOT stored in appDeps — it is only consumed
	// by the stream registry below and never registered as a service. Keeping
	// it local preserves the original behavior (FLV has no Stop lifecycle).

	// Step 7.7: WebSocket stream manager (always available)
	wsMgr := wsstream.NewManager(
		wsstream.WithMaxViewers(cfg.WebSocket.MaxViewers),
		wsstream.WithWriteBufSize(cfg.WebSocket.WriteBufSize),
		wsstream.WithIdleTimeout(cfg.WebSocket.IdleTimeout),
		wsstream.WithMetrics(m),
	)
	slog.Info("WebSocket stream manager initialized", "max_viewers", cfg.WebSocket.MaxViewers, "write_buf_size", cfg.WebSocket.WriteBufSize, "idle_timeout", cfg.WebSocket.IdleTimeout)
	deps.wsMgr = wsMgr

	// Step 7.6b: Relay (push-out) manager. Always constructed when cameras may
	// have push_targets — it's nil-safe and only runs goroutines for cameras
	// that actually have enabled targets. Wired to the camera manager so Add/
	// Update/Remove reconcile targets automatically.
	relayMgr := relay.NewManager(camMgr.GetHub, camMgr.GetSPS)
	// Codec info (video VPS/SPS/PPS + audio params) for relay targets — the
	// H.265 passthrough policy (#433) needs the VPS alongside SPS/PPS, and
	// audio-aware targets use the audio fields.
	relayMgr.SetCodecInfoProvider(camMgr.GetCodecInfo)
	camMgr.SetRelayManager(relayMgr)

	// Step 7.6c: sub-stream recycle callback (#513). When the on-demand
	// puller for a camera is torn down (idle/failed/camera lifecycle), drop
	// the egress entries registered under the camera's "/sub" key so stale
	// entries don't sit subscribed to a dead hub. The hub comparison gates a
	// race where a new viewer re-acquires and rebinds the entry between the
	// recycle decision and this callback — an entry already on a FRESH hub
	// belongs to the new pull generation and must survive. WS/FLV
	// unregister their own hub consumers; the HLS entry does not track its
	// hub consumer ID, so it is unsubscribed explicitly here. WHEP sessions
	// ride the same gate: their hub subscription is dropped so a new viewer
	// re-registers on the fresh pull (existing sessions idle-timeout).
	if subMgr := camMgr.SubStreams(); subMgr != nil {
		subMgr.SetOnRecycle(func(cameraID string, recycledHub *streamhub.StreamHub) {
			key := cameraID + streamhub.SubStreamKeySuffix
			if wsMgr.ActiveHub(key) == recycledHub {
				wsMgr.UnregisterStream(key)
			}
			if flvMgr != nil && flvMgr.ActiveHub(key) == recycledHub {
				flvMgr.UnregisterStream(key)
			}
			if hlsMgr.ActiveHub(key) == recycledHub {
				hlsMgr.StopStream(key)
				recycledHub.Unsubscribe("hls")
			}
			if deps.webrtcMgr != nil && deps.webrtcMgr.RegisteredHub(key) == recycledHub {
				deps.webrtcMgr.UnregisterStream(key)
			}
		})
	}

	// Wire transcoding dependencies for relay targets (H.265→H.264 transcode).
	// These must be set before Start so targets can resolve presets and hardware caps.
	relayFFmpegPath := cfg.Transcoding.FFmpegPath
	relayHwCap := transcoding.ProbeHardwareCapabilities(relayFFmpegPath)
	slog.Info(
		"relay: hardware capabilities",
		"arch", relayHwCap.Arch,
		"h264_encoder", relayHwCap.H264EncoderType,
		"ffmpeg_available", relayHwCap.FFmpegAvailable,
	)

	// Warn about software-only encoding on ARM (H.265→H.264 transcode will be slow).
	if (relayHwCap.Arch == "arm" || relayHwCap.Arch == "arm64") &&
		relayHwCap.H264EncoderType == transcoding.EncoderSoftware {
		slog.Warn("relay: ARM architecture with software-only H.264 encoder — H.265→H.264 transcode will be very slow",
			"arch", relayHwCap.Arch)
	}
	relayMgr.SetFFmpegPath(relayFFmpegPath)
	relayMgr.SetHardwareCap(relayHwCap)
	// Wire the source-codec resolver so push targets fail fast on MJPEG/JPEG
	// sources instead of engaging the H.265 transcode path that can never
	// decode them (#423).
	relayMgr.SetSourceCodecProvider(camMgr.GetSourceCodec)
	// Wire the source-URL resolver used by FFmpeg relay mode. Without this,
	// connectViaFFmpeg() sees an empty provider, cannot resolve the camera's
	// RTSP URL, and returns errPermanent on every retry (the 'permanent relay
	// error (no retry)' log spam). Native (gortmplib) relay is unaffected — it
	// subscribes to the StreamHub directly and never needs the source URL.
	relayMgr.SetStreamURLProvider(camMgr.GetStreamURL)

	// Load optional relay preset overrides from deploy/relay-presets.yaml.
	// Falls back to built-in defaults on any error (missing file, invalid YAML).
	relayPresets := relay.NewPresetRegistry()
	if err := relayPresets.Load("deploy/relay-presets.yaml"); err != nil {
		slog.Warn("relay: cannot load preset overrides, using built-in defaults", "error", err)
	}
	relayMgr.SetPresetRegistry(relayPresets)
	deps.relayMgr = relayMgr

	// Step 7.7: RTMP server (optional)
	if cfg.RTMP.Enabled != nil && *cfg.RTMP.Enabled {
		deps.rtmpServer = rtmp.NewServer(
			rtmp.Config{Addr: fmt.Sprintf(":%d", cfg.RTMP.Port)},
			// StreamKeyResolver: LIVE lookup (reflects cameras added at runtime,
			// not just those present at startup).
			camMgr.ResolveStreamKey,
			// CameraHubProvider: hand the publisher the SAME hub the recorder owns.
			camMgr.GetOrCreateHub,
			// OnPublisherConnect: mark the IngestRecorder as streaming.
			func(cameraID string, _ *streamhub.StreamHub) {
				if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
					ir.WriteConnected()
				}
			},
			// OnPublisherDisconnect: close in-flight segment, return to Idle.
			func(cameraID string) {
				if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
					ir.OnDisconnect()
				}
			},
		)
		// NALUProvider: forward each access unit to the IngestRecorder for MP4 recording.
		deps.rtmpServer.NALUProvider = func(cameraID string) rtmp.NALUCallback {
			ir := camMgr.GetIngestRecorder(cameraID)
			if ir == nil {
				return nil
			}
			return func(au [][]byte, ptsTicks int64, isIDR bool) {
				ir.WriteNALU(au, ptsTicks, isIDR)
			}
		}
		slog.Info("RTMP server configured", "port", cfg.RTMP.Port)
	}

	// Step 7.7b: WHIP push-in ingest over the main HTTP listener (#369).
	// Mirrors the RTMP wiring: stream key → camera, same IngestRecorder
	// lifecycle hooks. Opus audio reaches the recorder via SetAudioFormat +
	// WriteAudio (SRT/RTMP have no audio path today).
	if cfg.WHIP.Enabled != nil && *cfg.WHIP.Enabled {
		deps.whipServer = whip.NewServer(
			camMgr.ResolveWHIPKey,
			camMgr.GetOrCreateHub,
			func(cameraID string, _ *streamhub.StreamHub) {
				if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
					ir.WriteConnected()
				}
			},
			func(cameraID string) {
				if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
					ir.OnDisconnect()
				}
			},
			nil,
		)
		deps.whipServer.NALUProvider = func(cameraID string) whip.NALUCallback {
			ir := camMgr.GetIngestRecorder(cameraID)
			if ir == nil {
				return nil
			}
			return func(au [][]byte, ptsTicks int64, isIDR bool) {
				ir.WriteNALU(au, ptsTicks, isIDR)
			}
		}
		deps.whipServer.AudioFormatter = func(cameraID string, codec string, sampleRate, channels int) {
			if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
				ir.SetAudioFormat(codec, sampleRate, channels)
			}
		}
		deps.whipServer.AudioProvider = func(cameraID string) whip.AudioCallback {
			ir := camMgr.GetIngestRecorder(cameraID)
			if ir == nil {
				return nil
			}
			return func(codec string, ptsTicks int64, data []byte, dur time.Duration) {
				ir.WriteAudio(codec, ptsTicks, data, dur)
			}
		}
		slog.Info("WHIP ingest endpoint enabled", "path", "/whip/{streamKey}")
	}

	// Step 7.7c: RTSP output server (#522/#499) — serves
	// rtsp://<host>:<port>/<camera_id> pull URLs for third-party platforms.
	// Enabled by default; a bind failure only logs (see register.go).
	if cfg.Server.RTSP.Enabled != nil && *cfg.Server.RTSP.Enabled {
		deps.rtspServer = rtsp.NewServer(
			rtsp.Config{
				Addr:     fmt.Sprintf(":%d", cfg.Server.RTSP.Port),
				Username: cfg.Server.RTSP.Username,
				Password: cfg.Server.RTSP.Password,
			},
			rtspStreamProvider(camMgr),
		)
		slog.Info("RTSP output server configured", "port", cfg.Server.RTSP.Port,
			"auth", cfg.Server.RTSP.Username != "" || cfg.Server.RTSP.Password != "")
	}

	// Step 7.8: SRT listener (optional)
	if cfg.SRT.Enabled != nil && *cfg.SRT.Enabled {
		// Merge per-camera SRT push params into cfg.SRT.Streams so the listener's
		// passphrase/streamid lookup covers both configuration styles.
		for _, sc := range camMgr.SRTStreamConfigs() {
			found := false
			for i := range cfg.SRT.Streams {
				if cfg.SRT.Streams[i].CameraID == sc.CameraID {
					if sc.Passphrase != "" {
						cfg.SRT.Streams[i].Passphrase = sc.Passphrase
					}
					if sc.StreamID != "" {
						cfg.SRT.Streams[i].StreamID = sc.StreamID
					}
					found = true
					break
				}
			}
			if !found {
				cfg.SRT.Streams = append(cfg.SRT.Streams, sc)
			}
		}
		deps.srtListener = srt.NewListener(cfg.SRT)
		deps.srtListener.HubProvider = camMgr.GetOrCreateHub
		deps.srtListener.OnConnect = func(cameraID string, _ *streamhub.StreamHub) {
			if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
				ir.WriteConnected()
			}
		}
		deps.srtListener.OnDisconnect = func(cameraID string) {
			if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
				ir.OnDisconnect()
			}
		}
		deps.srtListener.NALUProvider = func(cameraID string) func(au [][]byte, ptsTicks int64, isIDR bool) {
			ir := camMgr.GetIngestRecorder(cameraID)
			if ir == nil {
				return nil
			}
			return func(au [][]byte, ptsTicks int64, isIDR bool) {
				ir.WriteNALU(au, ptsTicks, isIDR)
			}
		}
		slog.Info("SRT listener configured", "port", cfg.SRT.Port)
	}
	// Step 7.9: GB28181 SIP platform server (optional). Constructed after the
	// ingest listeners (SRT) and before cleanup; registered as the "gb28181"
	// service between srt and ws. The DeviceManager heartbeat checker is owned
	// by the SIP server's service lifecycle.
	if cfg.GB28181.Enabled {
		if len(cfg.GB28181.AllowedDeviceIDs) == 0 {
			slog.Warn("gb28181: allowed_device_ids 未配置——知晓共享口令的任意设备均可注册;列出设备 ID 以限制接入 / allowed_device_ids is empty: any device holding the shared password can register")
		}
		heartbeatInterval, err := time.ParseDuration(cfg.GB28181.HeartbeatInterval)
		if err != nil {
			heartbeatInterval = platform.DefaultHeartbeatInterval
		}
		deps.gb28181DevMgr = platform.NewDeviceManager(heartbeatInterval)
		deps.gb28181DevMgr.SetOfflineCallback(func(id string) {
			if err := db.MarkDeviceOffline(context.Background(), id); err != nil {
				slog.Warn("gb28181: failed to mark device offline in DB", "device", id, "error", err)
			}
			// Tear down the device's media sessions and flip its cameras'
			// recorders to Reconnecting (they recover on the next re-REGISTER).
			deps.gb28181Server.OnDeviceOffline(id)
		})
		deps.gb28181SessionMgr = newGB28181SessionManager(cfg.GB28181)
		sipCfg := gb28181.SIPConfig(cfg.GB28181)
		// GB 35114 A-level seam (#707): no-op in default builds, wires the
		// SM2 REGISTER authenticator under -tags gb35114. Boot-fatal when the
		// section is enabled but the certificates don't load — a half-configured
		// security layer must not come up silently.
		if err := gb28181.ApplySecurity35114(&sipCfg, cfg.GB28181); err != nil {
			return nil, nil, err
		}
		deps.gb28181Server = gbsip.NewServer(sipCfg, deps.gb28181DevMgr, deps.gb28181SessionMgr, gb28181.NewDeviceStore(deps.db))
		// Alarm notifications surface on the event bus (SSE /api/events).
		gbLibEvents = gb28181.NewEventBridge(deps.eventBus)
		deps.gb28181Server.SetEventBus(gbLibEvents)
		slog.Info("GB28181 SIP server configured", "sip_listen", cfg.GB28181.SIPListen)
	}

	// Step 7.95: GB28181 cascade client (optional, #364). The NVR registers
	// to the configured upper platform as a lower-level device; cameras are
	// aggregated into its catalog and forwarded (PS mux) on INVITE.
	if cfg.GB28181Cascade.Enabled {
		deps.gb28181Cascade = gbcascade.New(gb28181.CascadeConfig(cfg.GB28181Cascade), camera.NewCascadeSource(camMgr, db), gb28181.NewCascadeStore(db))
		deps.gb28181Cascade.SetSegmentParser(gb28181.SegmentParser())
		deps.gb28181Cascade.SetSubStreamAcquirer(camera.NewCascadeSubAcquirer(camMgr))
		// Multi-level cascade (#451): an upper INVITE for a not-currently-
		// recording GB child camera starts a bounded on-demand local INVITE.
		deps.gb28181Cascade.SetHubActivator(camera.NewCascadeHubActivator(camMgr))
		slog.Info("GB28181 cascade client configured",
			"upper", cfg.GB28181Cascade.ServerAddr, "device", cfg.GB28181Cascade.LocalDeviceID)
	}

	return flvMgr, gbLibEvents, nil
}

// newGB28181SessionManager builds the media SessionManager from config. The
// port pool is parsed from PortRange ("start-end"); on parse failure the
// default pool 30000-30050 is used (config validation already rejects bad
// ranges, so this is defensive only).
func newGB28181SessionManager(cfg config.GB28181ServerConfig) *platform.SessionManager {
	start, end := uint16(30000), uint16(30050)
	if parts := strings.SplitN(cfg.PortRange, "-", 2); len(parts) == 2 {
		if s, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
			start = uint16(s)
		}
		if e, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
			end = uint16(e)
		}
	}
	return platform.NewSessionManager(platform.NewPortManager(start, end), cfg.ServerID)
}
