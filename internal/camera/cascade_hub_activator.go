package camera

// Cascade main-stream activation (#451, Slice 2): the adapter behind the
// cascade client's HubActivator seam. An upper platform's INVITE for a GB
// child camera that is not currently recording starts a bounded on-demand
// local INVITE through the same substream machinery as the sub tier
// (#513), keyed apart so the camera's sub entry (if any) coexists. Only
// gb28181-protocol cameras are eligible — every other protocol's hub
// lives with its recorder, and activating those is not the cascade's
// business (the seam falls back to the legacy 500).

import (
	"context"
	"fmt"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
	"github.com/mickeyzzc/gb28181-go/platform"
	gbcascade "github.com/mickeyzzc/gb28181-go/platform/cascade"
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
	if cam.Protocol != string(model.ProtoGB28181) {
		return nil, nil, fmt.Errorf(
			"camera %q is protocol %s — only gb28181 cameras activate on demand",
			cameraID, cam.Protocol)
	}
	target := substream.Target{
		Kind:        substream.KindGB28181,
		GBDeviceID:  strings.TrimSpace(cam.GB28181.DeviceID),
		GBChannelID: strings.TrimSpace(cam.GB28181.ChannelID),
	}
	if target.GBDeviceID == "" || target.GBChannelID == "" {
		return nil, nil, fmt.Errorf("camera %q has no gb28181 device/channel binding", cameraID)
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
