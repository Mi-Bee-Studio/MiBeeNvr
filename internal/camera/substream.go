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
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
)

// resolveSubTarget implements the substream.Resolver contract against camera
// configs:
//
//   - rtsp protocol: sub_stream_url must be set manually.
//   - onvif protocol: sub_stream_url wins when present; otherwise the
//     auto-discovered sub_profile_token (#512) is resolved via GetStreamUri,
//     with the same DHCP-stale host rewriting the main stream path applies.
//
// Everything else (gb28131 sub-channels, push ingest, xiaomi) is not
// supported yet — ok=false → ErrNoSubStream → the caller falls back to main.
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
