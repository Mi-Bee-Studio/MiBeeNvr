package camera

// This file holds the lock-free config/manager accessor methods — small reads
// over the immutable snapshot (configs map) plus typed recorder casts and
// sub-manager getters. None of these perform I/O or take a registry lock.
//
// Extracted from manager.go (#225).

import (
	"context"
	"fmt"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
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

// GB28181SessionEnder tears down a channel's media session end-to-end
// (SIP BYE + local receiver + recorder state). Implemented by the SIP server.
type GB28181SessionEnder interface {
	ByeChannelByID(channelID string) error
}

// SetGB28181SessionEnder wires the session recycler used when a GB28181
// recorder restarts: the old session's AU callback still points at the
// replaced recorder, so the session is recycled before the fresh auto-INVITE.
func (cm *CameraManager) SetGB28181SessionEnder(ender GB28181SessionEnder) {
	cm.gb28181SessionEnder = ender
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

// GB28181AudioWriter returns the recorder's audio-frame callback for a
// GB28181 camera, or nil. The SIP server bridges demuxed PS audio frames
// (G.711/AAC) into the recorder for MP4 muxing and live hub broadcast.
func (cm *CameraManager) GB28181AudioWriter(cameraID string) func(codec string, data, config []byte, ptsTicks int64, samples int) {
	rec := cm.GetGB28181Recorder(cameraID)
	if rec == nil {
		return nil
	}
	return rec.WriteAudio
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

// NewGB28181PlaybackSink creates a dedicated recorder that muxes a fetched
// device-side recording (playback INVITE, #337) into the normal recordings
// pipeline — segments on disk, recordings rows, SegmentCompleted events —
// attributed to cameraID. Independent of the live recorder so live streaming
// and playback fetching coexist on the same camera.
func (cm *CameraManager) NewGB28181PlaybackSink(cameraID string) (gb28181.AUWriter, error) {
	cam := cm.snapshotConfig(cameraID)
	if cam == nil || cam.Protocol != string(model.ProtoGB28181) {
		return nil, fmt.Errorf("camera %q is not a GB28181 camera", cameraID)
	}
	enc := cam.Encoding
	if enc == "" {
		enc = string(model.FormatH264)
	}
	segDur := recorder.DefaultSegmentDur
	if d, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration); err == nil {
		segDur = d
	}
	rec := recorder.NewGB28181Recorder(recorder.GB28181Config{
		CameraID:      cameraID,
		Encoding:      enc,
		SegmentDur:    segDur,
		Store:         cm.store,
		DB:            cm.db,
		Metrics:       cm.metrics,
		EventBus:      cm.eventBus,
		RecordEnabled: true,
		AudioEnabled:  cam.AudioEnabled,
	}, nil)
	rec.Hub = model.NewStreamHub()
	rec.Hub.SetCameraID(cameraID)
	if err := rec.Start(context.Background()); err != nil {
		return nil, err
	}
	rec.OnInvite()
	return rec, nil
}

// GB28181PlaybackAudioWriter adapts a playback sink into an audio writer.
// The sink recorder implements this itself; kept for interface symmetry with
// the live path (GB28181AudioWriter).
func (cm *CameraManager) GB28181PlaybackAudioWriter(cameraID string) func(codec string, data, config []byte, ptsTicks int64, samples int) {
	return nil
}

// UpdateGB28181DeviceMeta backfills Brand/Model on the cameras bound to a
// GB28181 device from its DeviceInfo response. Empty camera fields only —
// user-entered values always win. Non-fatal: a failed update leaves the
// camera unchanged.
func (cm *CameraManager) UpdateGB28181DeviceMeta(deviceID, manufacturer, modelName string) error {
	cm.configMu.Lock()
	camIDs := make([]string, 0, 4)
	for i := range cm.cfg.Cameras {
		c := &cm.cfg.Cameras[i]
		if c.Protocol == "gb28181" && c.GB28181.DeviceID == deviceID {
			camIDs = append(camIDs, c.ID)
		}
	}
	cm.configMu.Unlock()
	if len(camIDs) == 0 || cm.db == nil {
		return nil
	}

	// Brand/Model are DB-only fields (CameraRow) — read each row and fill
	// the empty ones, leaving user-entered values untouched.
	updated := 0
	for _, id := range camIDs {
		row, err := cm.db.GetCamera(context.Background(), id)
		if err != nil || row == nil {
			continue
		}
		brand, mdl := row.Brand, row.Model
		if brand == "" && manufacturer != "" {
			brand = manufacturer
		}
		if mdl == "" && modelName != "" {
			mdl = modelName
		}
		if brand == row.Brand && mdl == row.Model {
			continue
		}
		if err := cm.db.UpdateCameraMetadata(context.Background(), id,
			row.Description, row.Location, brand, mdl, row.SerialNumber, row.RetentionDays); err != nil {
			return err
		}
		updated++
	}
	if updated > 0 {
		logger.Info("gb28181: camera meta backfilled from device info",
			"device_id", deviceID, "cameras", updated)
	}
	return nil
}
