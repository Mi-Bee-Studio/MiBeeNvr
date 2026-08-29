package camera

// This file holds the lock-free config/manager accessor methods — small reads
// over the immutable snapshot (configs map) plus typed recorder casts and
// sub-manager getters. None of these perform I/O or take a registry lock.
//
// Extracted from manager.go (#225).

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/mickeyzzc/gb28181-go/platform"
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
//
// Cross-protocol dedup (dual-protocol cameras, e.g. ONVIF + GB28181 both
// enabled — possibly on DIFFERENT interface IPs of the same device): before
// creating, the manager resolves the device's ONVIF serial (fingerprint cache
// → DB → live probe of the SIP source IP) and skips when a camera with that
// serial already exists. sourceIP "" (unknown) skips dedup entirely. Setting
// gb28181.allow_same_ip_enroll disables both cross-protocol checks (#596 —
// deliberate dual-protocol setups); manual camera creation (API/web, with
// allow_duplicate) remains the escape hatch otherwise.
func (cm *CameraManager) EnsureGB28181Camera(deviceID, channelID, name, sourceIP string) error {
	// Check if a camera for this channel already exists.
	if _, ok := cm.GB28181CameraIDByChannel(deviceID, channelID); ok {
		return nil // Already enrolled
	}
	if sourceIP != "" && !cm.gbAllowSameIPEnroll() {
		// L1: an existing pull camera streams from the same IP.
		if existingID, ok := cm.CameraIDByHostIP(sourceIP); ok {
			slog.Info("gb28181: auto-enroll skipped — another camera already streams from the device IP",
				"device", deviceID, "channel", channelID, "source_ip", sourceIP, "existing_camera", existingID)
			return nil
		}
		// L2: cross-IP identity — dual-NIC devices register GB28181 from one
		// interface and may stream ONVIF from another. The ONVIF serial is
		// interface-independent; probe (cache → DB → live) and match camera
		// stable IDs / serial numbers.
		if serial, ok := cm.resolveGBDeviceSerial(deviceID, sourceIP); ok {
			if existingID, ok := cm.CameraIDBySerial(serial); ok {
				slog.Info("gb28181: auto-enroll skipped — camera with matching device serial exists (cross-interface dedup)",
					"device", deviceID, "channel", channelID, "source_ip", sourceIP,
					"serial", serial, "existing_camera", existingID)
				return nil
			}
		}
	} else if sourceIP != "" {
		slog.Info("gb28181: cross-protocol dedup bypassed by allow_same_ip_enroll",
			"device", deviceID, "channel", channelID, "source_ip", sourceIP)
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

// gbAllowSameIPEnroll reports the gb28181.allow_same_ip_enroll opt-in (#596).
func (cm *CameraManager) gbAllowSameIPEnroll() bool {
	return cm.cfg != nil && cm.cfg.GB28181.AllowSameIPEnroll
}

// gbSerialCache memoizes probed device serials (device_id → serial) for the
// process lifetime; guarded by gbSerialMu.
var (
	gbSerialMu    sync.Mutex
	gbSerialCache = make(map[string]string)
	// probeGBSerial is the ONVIF serial probe used by cross-protocol dedup;
	// a var so tests can stub the network round-trip.
	probeGBSerial = onvif.ProbeSerial
)

// resolveGBDeviceSerial resolves the ONVIF serial of a GB28181 device by
// probing its SIP source IP (dual-protocol cameras answer GetDeviceInformation
// without auth on every interface). Results are cached in memory and the DB
// (gb28181_fingerprints) — the DB copy also serves the reverse dedup path
// (camera create). ok=false when the device has no reachable unauthenticated
// ONVIF endpoint (pure-GB cameras) — callers fall through to normal enroll.
func (cm *CameraManager) resolveGBDeviceSerial(deviceID, sourceIP string) (string, bool) {
	gbSerialMu.Lock()
	if serial, ok := gbSerialCache[deviceID]; ok {
		gbSerialMu.Unlock()
		return serial, true
	}
	gbSerialMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serial, ok := probeGBSerial(ctx, sourceIP)
	if !ok {
		return "", false
	}

	gbSerialMu.Lock()
	gbSerialCache[deviceID] = serial
	gbSerialMu.Unlock()
	if cm.db != nil {
		if err := cm.db.UpsertGB28181Fingerprint(ctx, storage.GB28181Fingerprint{
			DeviceID: deviceID, Serial: serial, SourceIP: sourceIP, ProbedAt: time.Now(),
		}); err != nil {
			slog.Debug("gb28181: persist device fingerprint failed", "device", deviceID, "error", err)
		}
	}
	return serial, true
}

// CameraIDByHostIP resolves an active non-GB28181 camera whose pull URL or
// ONVIF endpoint points at the given IP. Used by GB28181 auto-enroll to keep
// one camera per physical device when a dual-protocol camera registers.
// GB28181 cameras are excluded (their dedup key is the channel ID, not IP).
func (cm *CameraManager) CameraIDByHostIP(ip string) (string, bool) {
	if ip == "" {
		return "", false
	}
	snap := cm.loadSnapshot()
	for _, cfg := range snap.configs {
		if cfg.Protocol == string(model.ProtoGB28181) {
			continue
		}
		if cameraHostIP(cfg) == ip {
			return cfg.ID, true
		}
	}
	return "", false
}

// CameraIDBySerial resolves a camera by its stable hardware serial — the
// cross-protocol identity for dual-NIC dedup. ONVIF cameras auto-populate
// StableID from the device serial on first connection (crud.go reverse
// lookup), making it the natural join key against the GB28181 fingerprint.
func (cm *CameraManager) CameraIDBySerial(serial string) (string, bool) {
	if serial == "" {
		return "", false
	}
	snap := cm.loadSnapshot()
	for _, cfg := range snap.configs {
		if cfg.Protocol == string(model.ProtoGB28181) {
			continue
		}
		if cfg.StableID == serial {
			return cfg.ID, true
		}
	}
	return "", false
}

// cameraHostIP extracts the host from a camera's URL or ONVIF endpoint
// ("" when neither carries one — push/ingest cameras, GB28181, xiaomi P2P).
func cameraHostIP(cfg *config.CameraConfig) string {
	for _, raw := range []string{cfg.URL, cfg.ONVIFEndpoint} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
		if host, _, err := net.SplitHostPort(raw); err == nil && host != "" {
			return host
		}
	}
	return ""
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

// ArchiveGB28181Camera soft-removes the camera auto-enrolled for a channel —
// used when a catalog supersedes the device-self pseudo-channel (#352).
// Archives preserve recordings; a no-op when no camera is bound.
func (cm *CameraManager) ArchiveGB28181Camera(deviceID, channelID string) error {
	cameraID, ok := cm.GB28181CameraIDByChannel(deviceID, channelID)
	if !ok {
		return nil
	}
	return cm.ArchiveCamera(context.Background(), cameraID)
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
func (cm *CameraManager) NewGB28181PlaybackSink(cameraID string) (platform.AUWriter, error) {
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
	rec.Hub = streamhub.New()
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

// GB28181RecordingWanted reports whether the camera bound to a channel wants
// recording (nil RecordingEnabled = record by default). The alarm linkage
// leaves such sessions to the recorder's own reconnect loop (#355).
func (cm *CameraManager) GB28181RecordingWanted(deviceID, channelID string) bool {
	cameraID, ok := cm.GB28181CameraIDByChannel(deviceID, channelID)
	if !ok {
		return false // no camera → alarm linkage owns the session lifecycle
	}
	cam := cm.snapshotConfig(cameraID)
	if cam == nil {
		return false
	}
	return cam.RecordingEnabled == nil || *cam.RecordingEnabled
}

// GB28181SubChannelID returns the persisted sub-channel code for the camera
// bound to a main channel ("" when unbound or none), #560.
func (cm *CameraManager) GB28181SubChannelID(deviceID, channelID string) string {
	cameraID, ok := cm.GB28181CameraIDByChannel(deviceID, channelID)
	if !ok {
		return ""
	}
	cam := cm.snapshotConfig(cameraID)
	if cam == nil {
		return ""
	}
	return cam.GB28181.SubChannelID
}

// SetGB28181SubChannel persists the probed sub-channel code on the camera
// bound to a main channel (#560). Fill-once: an existing value (manual or a
// raced probe) always wins. YAML is the source of truth for GB28181 bindings
// (the DB row injects it at API response time), so persisting means the
// config file. The camera's on-demand sub puller re-resolves on its next
// Acquire — no restart needed for a newly probed code.
func (cm *CameraManager) SetGB28181SubChannel(deviceID, channelID, subChannelID string) error {
	cameraID, ok := cm.GB28181CameraIDByChannel(deviceID, channelID)
	if !ok {
		return fmt.Errorf("camera bound to device %s channel %s not found", deviceID, channelID)
	}
	cm.configMu.Lock()
	set := false
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			if cm.cfg.Cameras[i].GB28181.SubChannelID != "" { // raced a manual write
				cm.configMu.Unlock()
				return nil
			}
			cm.cfg.Cameras[i].GB28181.SubChannelID = subChannelID
			set = true
			break
		}
	}
	cm.configMu.Unlock()
	if !set {
		return fmt.Errorf("camera %s not found in config", cameraID)
	}
	if err := cm.persistConfig(); err != nil {
		return fmt.Errorf("persist config: %w", err)
	}
	slog.Info("gb28181: persisted probed sub_channel_id", "camera", cameraID, "sub_channel_id", subChannelID)
	return nil
}
