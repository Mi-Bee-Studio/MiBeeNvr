package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
)

// GB/T 28181-2022 platform-side on-demand snapshot + manual record (#708).
//
//	POST /api/gb28181/channels/{id}/snapshot   {snap_num, interval}
//	POST /api/gb28181/channels/{id}/record     {action: start|stop}
//	POST /api/gb28181/snapshot/upload?session= (public — device POSTs JPEGs)
//
// The snapshot exchange: SnapShotCmd carries an UploadURL pointing at THIS
// server plus a random SessionID; the device POSTs each captured JPEG to the
// upload endpoint (the SessionID in the URL is the credential — devices
// cannot BasicAuth, same threat model as WHIP stream keys), then reports
// UploadSnapShotFinished. Sessions close on the completion notify or on
// timeout (spec: no notify within the window = failure).

// gbCommander is the DeviceControl surface these handlers need. Satisfied by
// *platform.PTZController; narrow interface keeps the handlers testable.
type gbCommander interface {
	SendSnapShotCmd(channelID string, cmd manscdp.SnapShotCmd) error
	StartManualRecord(channelID string) error
	StopManualRecord(channelID string) error
}

var gbSnapshotLogger = slogx.Component("gb28181-snapshot")

// snapshotUploadMaxBytes caps one uploaded frame. Real MJPEG/JPEG frames are
// ≤4MB at 4K; 8MB leaves margin while keeping a hostile poster honest.
const snapshotUploadMaxBytes = 8 << 20

// SetGB28181Commander wires the DeviceControl sender for snapshot/record.
func (h *Handler) SetGB28181Commander(c gbCommander) {
	h.gb28181Commander = c
}

// SetGB28181SnapshotManager wires the snapshot session registry.
func (h *Handler) SetGB28181SnapshotManager(m *gb28181.SnapshotSessionManager) {
	h.gb28181SnapMgr = m
}

// locateGBChannel resolves a channel ID to (deviceID, ok) across registered
// devices — same scan the invite handler uses.
func (h *Handler) locateGBChannel(channelID string) (string, bool) {
	if h.gb28181DeviceMgr == nil {
		return "", false
	}
	for _, dev := range h.gb28181DeviceMgr.AllDevices() {
		if _, ok := h.gb28181DeviceMgr.FindChannel(dev.ID, channelID); ok {
			return dev.ID, true
		}
	}
	return "", false
}

// gbCameraIDForChannel derives the storage/event camera ID for a channel.
// GB cameras enroll as gb-<channelID> (the camera-manager convention), so
// most channels map exactly; channels without an enrolled camera still get
// the derived ID — snapshots persist under a namespaced directory and the
// event simply references an unknown camera to UI consumers.
func (h *Handler) gbCameraIDForChannel(channelID string) string {
	camID := "gb-" + channelID
	if h.camMgr != nil && h.camMgr.GetCameraConfig(camID) == nil {
		gbSnapshotLogger.Debug("snapshot for channel without enrolled camera", "channel_id", channelID)
	}
	return camID
}

type gbSnapshotRequest struct {
	SnapNum  int `json:"snap_num"` // 1..10 (spec A.2.1.24), default 1
	Interval int `json:"interval"` // seconds between frames, >=1, 0 = single
}

// handleGB28181ChannelSnapshot sends DeviceControl(SnapShotCmd) — the device
// captures SnapNum frames and POSTs them to the session's upload URL.
func (h *Handler) handleGB28181ChannelSnapshot(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("id")
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}
	var body gbSnapshotRequest
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	deviceID, ok := h.locateGBChannel(channelID)
	if !ok {
		WriteError(w, http.StatusNotFound, "channel not found")
		return
	}
	if dev, found := h.gb28181DeviceMgr.Device(deviceID); !found || dev.Status.Load() == platform.DeviceOffline {
		WriteError(w, http.StatusConflict, "device is offline, cannot snapshot channel")
		return
	}
	if h.gb28181Commander == nil || h.gb28181SnapMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 device control not available")
		return
	}

	sess, err := h.gb28181SnapMgr.CreateSession(channelID, deviceID, h.gbCameraIDForChannel(channelID), body.SnapNum)
	if err != nil {
		gbSnapshotLogger.Error("create snapshot session failed", "channel_id", channelID, "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to create snapshot session")
		return
	}

	cmd := manscdp.SnapShotCmd{
		SnapNum:   sess.SnapNum,
		UploadURL: "http://" + r.Host + "/api/gb28181/snapshot/upload?session=" + sess.ID,
		SessionID: sess.ID,
	}
	if body.Interval > 0 {
		cmd.Interval = body.Interval
	}
	if err := h.gb28181Commander.SendSnapShotCmd(channelID, cmd); err != nil {
		// The session dies by its own timeout sweep; the command itself
		// failed synchronously (transport or encoding) — report it now.
		gbSnapshotLogger.Error("send SnapShotCmd failed", "channel_id", channelID, "error", err)
		WriteError(w, http.StatusBadGateway, "failed to send snapshot command to device")
		return
	}
	gbSnapshotLogger.Info("snapshot command sent",
		"channel_id", channelID, "device_id", deviceID, "session_id", sess.ID, "snap_num", sess.SnapNum)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":      "snapshot_requested",
		"session_id":  sess.ID,
		"channel_id":  channelID,
		"snap_num":    sess.SnapNum,
		"upload_path": "/api/gb28181/snapshot/upload?session=" + sess.ID,
	})
}

type gbRecordRequest struct {
	Action string `json:"action"` // start | stop
}

// handleGB28181ChannelRecord sends DeviceControl(RecordCmd) start/stop.
// start = device-side manual recording begins; the platform-side stream
// capture is the existing INVITE path (invite endpoint / auto-invite on
// recording-enabled cameras).
func (h *Handler) handleGB28181ChannelRecord(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("id")
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}
	var body gbRecordRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	deviceID, ok := h.locateGBChannel(channelID)
	if !ok {
		WriteError(w, http.StatusNotFound, "channel not found")
		return
	}
	if dev, found := h.gb28181DeviceMgr.Device(deviceID); !found || dev.Status.Load() == platform.DeviceOffline {
		WriteError(w, http.StatusConflict, "device is offline, cannot control recording")
		return
	}
	if h.gb28181Commander == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 device control not available")
		return
	}

	var err error
	var status string
	switch body.Action {
	case "start":
		err = h.gb28181Commander.StartManualRecord(channelID)
		status = "record_cmd_sent"
	case "stop":
		err = h.gb28181Commander.StopManualRecord(channelID)
		status = "stop_cmd_sent"
	default:
		WriteError(w, http.StatusBadRequest, "action must be start or stop")
		return
	}
	if err != nil {
		gbSnapshotLogger.Error("send RecordCmd failed", "channel_id", channelID, "action", body.Action, "error", err)
		WriteError(w, http.StatusBadGateway, "failed to send record command to device")
		return
	}
	gbSnapshotLogger.Info("record command sent", "channel_id", channelID, "action", body.Action)
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     status,
		"channel_id": channelID,
	})
}

// handleGB28181SnapshotUpload receives one JPEG frame from a device (public
// endpoint — the session ID in the URL is the credential). Mounted on the
// rate-limited public group.
func (h *Handler) handleGB28181SnapshotUpload(w http.ResponseWriter, r *http.Request) {
	if h.gb28181SnapMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "snapshot receiving not available")
		return
	}
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		WriteError(w, http.StatusBadRequest, "session parameter is required")
		return
	}
	jpeg, err := io.ReadAll(http.MaxBytesReader(w, r.Body, snapshotUploadMaxBytes))
	if err != nil {
		WriteError(w, http.StatusRequestEntityTooLarge, "frame exceeds size limit")
		return
	}
	path, err := h.gb28181SnapMgr.Receive(sessionID, jpeg)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"status": "stored", "path": path})
	case errors.Is(err, gb28181.ErrSnapshotSessionUnknown):
		WriteError(w, http.StatusNotFound, "unknown or expired snapshot session")
	case errors.Is(err, gb28181.ErrSnapshotSessionClosed):
		WriteError(w, http.StatusConflict, "snapshot session already finished")
	case errors.Is(err, gb28181.ErrSnapshotNotJPEG):
		WriteError(w, http.StatusBadRequest, "body is not a JPEG frame")
	default:
		gbSnapshotLogger.Error("snapshot upload persist failed", "session_id", sessionID, "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to store frame")
	}
}
