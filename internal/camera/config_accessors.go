package camera

// This file holds the lock-free config/manager accessor methods — small reads
// over the immutable snapshot (configs map) plus typed recorder casts and
// sub-manager getters. None of these perform I/O or take a registry lock.
//
// Extracted from manager.go (#225).

import (
	"context"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// GB28181Inviter sends a SIP INVITE to start a media session for a GB28181
// channel. Implemented by the SIP server; called by the camera manager to
// auto-INVITE when a GB28181 recorder starts.
type GB28181Inviter interface {
	InviteChannel(deviceID, channelID string) error
}

// SetGB28181Inviter wires the SIP server for auto-INVITE. When set, starting
// a GB28181 recorder automatically triggers an INVITE to pull the media stream.
func (cm *CameraManager) SetGB28181Inviter(inviter GB28181Inviter) {
	cm.gb28181Inviter = inviter
}

// CameraCount returns the number of configured cameras (O(1), no DB query).
// Used by stats endpoints that only need the count, avoiding a redundant
// ListCameras DB round-trip per request.
func (cm *CameraManager) CameraCount() int {
	return len(cm.loadSnapshot().configs)
}

// GetCameraConfig returns the config for the given camera ID, or nil if not found.
// Lock-free read from the immutable snapshot.
func (cm *CameraManager) GetCameraConfig(cameraID string) *config.CameraConfig {
	return cm.snapshotConfig(cameraID)
}

// GetIngestRecorder returns the IngestRecorder for a camera if it is one, else
// nil. Convenience for the SRT/RTMP servers that need to call WriteNALU /
// OnDisconnect on push cameras.
func (cm *CameraManager) GetIngestRecorder(cameraID string) *recorder.IngestRecorder {
	rec, ok := cm.GetRecorder(cameraID).(*recorder.IngestRecorder)
	if !ok {
		return nil
	}
	return rec
}

// GetTimelapseMergeMgr returns the timelapse rolling merge manager, or nil if not set.
func (cm *CameraManager) GetTimelapseMergeMgr() *timelapse.RollingMergeManager {
	return cm.timelapseMergeMgr
}

// GetGB28181Recorder returns the GB28181Recorder for a camera if it is one, else
// nil. Convenience for the GB28181 SessionManager that needs to call OnInvite/
// OnBye on GB28181 cameras.
func (cm *CameraManager) GetGB28181Recorder(cameraID string) *recorder.GB28181Recorder {
	rec, ok := cm.GetRecorder(cameraID).(*recorder.GB28181Recorder)
	if !ok {
		return nil
	}
	return rec
}

// EnsureGB28181Camera creates a camera entry for a GB28181 channel if one
// doesn't already exist. Called by the SIP server on REGISTER (device pseudo-
// channel) and on Catalog receipt (real video channels) so GB28181 cameras
// auto-appear in the Cameras list — matching ONVIF auto-add. Dedup is by
// channel: a multi-channel device (NVR) gets one camera per video channel.
// Idempotent: if a camera with matching GB28181.ChannelID exists, returns nil.
func (cm *CameraManager) EnsureGB28181Camera(deviceID, channelID, name string) error {
	// Check if a camera for this channel already exists.
	if _, ok := cm.GB28181CameraIDByChannel(deviceID, channelID); ok {
		return nil // Already enrolled
	}

	cameraName := name
	if cameraName == "" {
		cameraName = "GB28181 " + channelID
	}
	cam := config.CameraConfig{
		ID:       "gb-" + channelID,
		Name:     cameraName,
		Protocol: string(model.ProtoGB28181),
		GB28181: config.GB28181ChannelConfig{
			DeviceID:  deviceID,
			ChannelID: channelID,
		},
	}
	_, err := cm.AddCamera(context.Background(), cam)
	return err
}

// GB28181CameraIDByChannel resolves the MiBee camera bound to a GB28181
// device/channel pair by scanning camera configs — independent of the
// "gb-<channelID>" naming convention so manually created cameras resolve too.
func (cm *CameraManager) GB28181CameraIDByChannel(deviceID, channelID string) (string, bool) {
	snap := cm.loadSnapshot()
	for _, cfg := range snap.configs {
		if cfg.Protocol == string(model.ProtoGB28181) &&
			cfg.GB28181.DeviceID == deviceID &&
			cfg.GB28181.ChannelID == channelID {
			return cfg.ID, true
		}
	}
	return "", false
}

// GB28181NALUWriter returns the recorder's AU callback for a GB28181 camera,
// or nil. The SIP server uses this to bridge RTP receiver output directly
// into the recorder pipeline at access-unit granularity.
func (cm *CameraManager) GB28181NALUWriter(cameraID string) func(au [][]byte, ptsTicks int64, isIDR bool) {
	rec := cm.GetGB28181Recorder(cameraID)
	if rec == nil {
		return nil
	}
	return rec.WriteNALU
}

// OnGB28181Invite transitions the recorder to Recording state.
func (cm *CameraManager) OnGB28181Invite(cameraID string) {
	if rec := cm.GetGB28181Recorder(cameraID); rec != nil {
		rec.OnInvite()
	}
}

// OnGB28181Bye transitions the recorder to Reconnecting state.
func (cm *CameraManager) OnGB28181Bye(cameraID string) {
	if rec := cm.GetGB28181Recorder(cameraID); rec != nil {
		rec.OnBye()
	}
}
