package camera

// On-demand sub-stream ingest wiring (#513): the camera manager owns the
// substream.Manager, resolves pull targets from camera configs (manual
// sub_stream_url, or ONVIF GetStreamUri on the discovered secondary profile),
// and ties its lifecycle to camera start/stop/remove/update. Egress endpoints
// (WS/FLV/HLS quality=sub) reach it through AcquireSubStream/ReleaseSubStream.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
	"github.com/mickeyzzc/gb28181-go/platform"
	gbcascade "github.com/mickeyzzc/gb28181-go/platform/cascade"
)

// resolveSubTarget implements the substream.Resolver contract against camera
// configs:
//
//   - rtsp protocol: sub_stream_url must be set manually.
//
//   - onvif protocol: sub_stream_url wins when present; otherwise the
//     auto-discovered sub_profile_token (#512) is resolved via GetStreamUri,
//     with the same DHCP-stale host rewriting the main stream path applies.
//
//   - gb28181 protocol: the probed sub_channel_id (#560) is served by the GB
//     pull path (SIP INVITE to the vendor-convention sub-channel code).
//
// Everything else (push ingest, xiaomi) is not supported yet — ok=false →
// ErrNoSubStream → the caller falls back to main.
func (cm *CameraManager) resolveSubTarget(ctx context.Context, cameraID string) (substream.Target, bool, error) {
	cam := cm.snapshotConfig(cameraID)
	if cam == nil {
		return substream.Target{}, false, nil
	}
	switch cam.Protocol {
	case string(model.ProtoRTSP):
		if strings.TrimSpace(cam.SubStreamURL) == "" {
			return substream.Target{}, false, nil
		}
		return substream.Target{URL: cam.SubStreamURL, Username: cam.Username, Password: cam.Password}, true, nil

	case string(model.ProtoGB28181):
		// Probed sub-channel (#560): the persisted code is INVITE'd on
		// demand by the GB pull path. No code → ErrNoSubStream → main.
		if sub := strings.TrimSpace(cam.GB28181.SubChannelID); sub != "" {
			return substream.Target{
				Kind:        substream.KindGB28181,
				GBDeviceID:  cam.GB28181.DeviceID,
				GBChannelID: sub,
			}, true, nil
		}
		return substream.Target{}, false, nil

	case string(model.ProtoONVIF):
		if strings.TrimSpace(cam.SubStreamURL) != "" {
			return substream.Target{URL: cam.SubStreamURL, Username: cam.Username, Password: cam.Password}, true, nil
		}
		token := strings.TrimSpace(cam.SubProfileToken)
		if token == "" {
			return substream.Target{}, false, nil
		}
		endpoint := cam.ONVIFEndpoint
		if endpoint == "" {
			endpoint = cam.URL
		}
		client := cm.reuseOrCreateONVIFClient(cameraID, endpoint, cam.Username, cam.Password)
		uriCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		// reuseOrCreateONVIFClient hands out cached UNCONNECTED clients —
		// Connect is idempotent and shares the recorder's session.
		if !client.IsReady() {
			if cerr := client.Connect(uriCtx); cerr != nil {
				return substream.Target{}, false, fmt.Errorf("onvif connect for sub-stream URI: %w", cerr)
			}
		}
		info, err := client.GetStreamURI(uriCtx, token)
		if err != nil {
			return substream.Target{}, false, fmt.Errorf("onvif GetStreamUri(sub profile): %w", err)
		}
		if info == nil || strings.TrimSpace(info.URI) == "" {
			return substream.Target{}, false, nil
		}
		uri := recorder.RewriteStaleStreamHost(info.URI, endpoint)
		return substream.Target{URL: uri, Username: cam.Username, Password: cam.Password}, true, nil
	}
	return substream.Target{}, false, nil
}

// AcquireSubStream starts (or joins) the camera's on-demand sub-stream pull
// and returns its source. Each success must be balanced with
// ReleaseSubStream. Returns substream.ErrNoSubStream when the camera has no
// usable sub-stream configuration — callers treat that as "negotiate down to
// main".
func (cm *CameraManager) AcquireSubStream(ctx context.Context, cameraID string) (*substream.Source, error) {
	if cm.subStreams == nil {
		return nil, substream.ErrNoSubStream
	}
	return cm.subStreams.Acquire(ctx, cameraID)
}

// ReleaseSubStream drops one reference; the pull stops after the idle timeout.
func (cm *CameraManager) ReleaseSubStream(cameraID string) {
	if cm.subStreams != nil {
		cm.subStreams.Release(cameraID)
	}
}

// SubStreams exposes the manager for app-level wiring (recycle callback,
// observability). May be nil in reduced test constructors.
func (cm *CameraManager) SubStreams() *substream.Manager { return cm.subStreams }

// newSubStreamManager builds the manager from server config. Nil-safe against
// reduced configs (tests construct managers with a nil cfg).
func newSubStreamManager(cfg substream.Config, idleTimeoutS, readyTimeoutS int) *substream.Manager {
	if idleTimeoutS > 0 {
		cfg.IdleTimeout = time.Duration(idleTimeoutS) * time.Second
	}
	if readyTimeoutS > 0 {
		cfg.ReadyTimeout = time.Duration(readyTimeoutS) * time.Second
	}
	return substream.NewManager(cfg)
}

func subIdleTimeoutS(cfg *config.Config) int {
	if cfg == nil {
		return 0
	}
	return cfg.Server.SubStream.IdleTimeoutS
}

func subReadyTimeoutS(cfg *config.Config) int {
	if cfg == nil {
		return 0
	}
	return cfg.Server.SubStream.ReadyTimeoutS
}

// NewCascadeSubAcquirer adapts the manager's sub-stream tier to the cascade
// client's SubStreamAcquirer (#512): one cascade INVITE holds one reference.
func NewCascadeSubAcquirer(cm *CameraManager) gbcascade.SubStreamAcquirer {
	return cascadeSubAcquirer{cm: cm}
}

type cascadeSubAcquirer struct{ cm *CameraManager }

func (a cascadeSubAcquirer) AcquireSubHub(ctx context.Context, cameraID string) (*platform.FrameHub, func(), error) {
	src, err := a.cm.AcquireSubStream(ctx, cameraID)
	if err != nil {
		return nil, nil, err
	}
	// The sub hub is short-lived (one INVITE holds one reference): the
	// forwarding bridge detaches with the sub-stream release.
	libHub, detachBridge := gb28181.BridgeSubHub(src.Hub(), "cascade-sub-"+cameraID)
	release := func() {
		detachBridge()
		a.cm.ReleaseSubStream(cameraID)
	}
	return libHub, release, nil
}
