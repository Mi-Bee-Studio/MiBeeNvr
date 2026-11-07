package camera

// Cascade main-stream activation (#451, Slice 2 / #1061 B-plan): the adapter
// behind the cascade client's HubActivator seam. An upper platform's INVITE
// for a channel that is not currently recording starts a bounded on-demand
// pull through the same substream machinery as the sub tier (#513), keyed
// apart so the camera's sub entry (if any) coexists.
//
// Eligible sources: gb28181 cameras (local INVITE via the GB pull path) and
// dialable main streams — rtsp cameras dial their configured URL directly,
// onvif cameras resolve the main profile via GetStreamUri (a direct rtsp://
// URL in the config wins when no profile token is set). Protocols without a
// dialable main stream (push ingest, xiaomi, …) still refuse — the seam
// falls back to the legacy 500.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
	"github.com/mickeyzzc/gb28181-go/platform"
	gbcascade "github.com/mickeyzzc/gb28181-go/platform/cascade"
	"time"
)

const cascadeMainKeyPrefix = "cascade-main:"

// CascadeMainKey is the substream-manager entry key for a camera's
// on-demand main-stream pull: one cascade INVITE holds one reference.
func CascadeMainKey(cameraID string) string { return cascadeMainKeyPrefix + cameraID }

// NewCascadeHubActivator wires the on-demand pull machinery to the cascade
// client's HubActivator (#451). Nil substream manager (reduced test
// constructors) makes every activation error — the cascade answers 500.
func NewCascadeHubActivator(cm *CameraManager) gbcascade.HubActivator {
	return cascadeHubActivator{cm: cm}
}

type cascadeHubActivator struct{ cm *CameraManager }

func (a cascadeHubActivator) EnsureHubActive(
	ctx context.Context, cameraID string,
) (*platform.FrameHub, func(), error) {
	cam := a.cm.snapshotConfig(cameraID)
	if cam == nil {
		return nil, nil, fmt.Errorf("camera %q not found", cameraID)
	}
	target, err := a.cascadeMainTarget(ctx, cam)
	if err != nil {
		return nil, nil, err
	}
	if a.cm.subStreams == nil {
		return nil, nil, substream.ErrNoSubStream
	}

	src, err := a.cm.subStreams.AcquireTarget(ctx, CascadeMainKey(cameraID), target)
	if err != nil {
		return nil, nil, fmt.Errorf("activate main stream: %w", err)
	}
	// The activated hub is short-lived (one INVITE holds one reference): the
	// forwarding bridge detaches with the reference drop.
	libHub, detachBridge := gb28181.BridgeSubHub(src.Hub(), "cascade-main-"+cameraID)
	release := func() {
		detachBridge()
		a.cm.subStreams.Release(CascadeMainKey(cameraID))
	}
	return libHub, release, nil
}

// cascadeMainTarget maps a camera config to its on-demand main-stream pull
// target. GB cameras use the SIP pull path; rtsp/onvif cameras dial the same
// RTSP machinery as the sub tier (#1061 B-plan). The caller's deadline
// bounds any ONVIF resolution.
func (a cascadeHubActivator) cascadeMainTarget(
	ctx context.Context, cam *config.CameraConfig,
) (substream.Target, error) {
	switch cam.Protocol {
	case string(model.ProtoGB28181):
		target := substream.Target{
			Kind:        substream.KindGB28181,
			GBDeviceID:  strings.TrimSpace(cam.GB28181.DeviceID),
			GBChannelID: strings.TrimSpace(cam.GB28181.ChannelID),
		}
		if target.GBDeviceID == "" || target.GBChannelID == "" {
			return substream.Target{}, fmt.Errorf(
				"camera %q has no gb28181 device/channel binding", cam.ID)
		}
		return target, nil

	case string(model.ProtoRTSP):
		if strings.TrimSpace(cam.URL) == "" {
			return substream.Target{}, fmt.Errorf(
				"camera %q has no rtsp url to dial on demand", cam.ID)
		}
		return substream.Target{URL: cam.URL, Username: cam.Username, Password: cam.Password}, nil

	case string(model.ProtoONVIF):
		return a.onvifMainTarget(ctx, cam)
	}
	return substream.Target{}, fmt.Errorf(
		"camera %q is protocol %s — no dialable main stream for on-demand activation",
		cam.ID, cam.Protocol)
}

// onvifMainTarget resolves an onvif camera's main stream: the main profile
// token via GetStreamUri (same stale-host rewrite as the recorder), falling
// back to a direct rtsp:// URL when the config carries no token.
func (a cascadeHubActivator) onvifMainTarget(
	ctx context.Context, cam *config.CameraConfig,
) (substream.Target, error) {
	if token := strings.TrimSpace(cam.ProfileToken); token != "" {
		endpoint := cam.ONVIFEndpoint
		if endpoint == "" {
			endpoint = cam.URL
		}
		client := a.cm.reuseOrCreateONVIFClient(cam.ID, endpoint, cam.Username, cam.Password)
		uriCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		// reuseOrCreateONVIFClient hands out cached UNCONNECTED clients —
		// Connect is idempotent and shares the recorder's session.
		if !client.IsReady() {
			if cerr := client.Connect(uriCtx); cerr != nil {
				return substream.Target{}, fmt.Errorf("onvif connect for main-stream URI: %w", cerr)
			}
		}
		info, err := client.GetStreamURI(uriCtx, token)
		if err != nil {
			return substream.Target{}, fmt.Errorf("onvif GetStreamUri(main profile): %w", err)
		}
		if info == nil || strings.TrimSpace(info.URI) == "" {
			return substream.Target{}, fmt.Errorf(
				"camera %q: onvif main profile resolved no stream uri", cam.ID)
		}
		uri, _ := recorder.ResolveStreamHost(info.URI, endpoint)
		return substream.Target{URL: uri, Username: cam.Username, Password: cam.Password}, nil
	}
	if strings.HasPrefix(strings.TrimSpace(cam.URL), "rtsp://") {
		return substream.Target{URL: cam.URL, Username: cam.Username, Password: cam.Password}, nil
	}
	return substream.Target{}, fmt.Errorf(
		"camera %q has neither an onvif profile token nor a direct rtsp url", cam.ID)
}
