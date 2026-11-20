package gbapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/httpx"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/mickeyzzc/gb28181-go/platform"
)

// --- GB28181 Device Handlers ---

// handleListGB28181Devices returns all registered GB28181 devices with ETag support.
func (s *server) handleListGB28181Devices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil {
			limit = parsed
		}
	}
	// Clamp limit: default 50, max 500
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	devices, err := s.deps.DB.ListGB28181Devices(ctx)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list devices")
		return
	}
	if devices == nil {
		devices = []storage.GB28181Device{}
	}

	if len(devices) > limit {
		devices = devices[:limit]
	}

	// Build ETag hash from device signatures (status, last_keepalive)
	var etagHash uint32
	for _, d := range devices {
		etagHash = crc32.Update(etagHash, crc32.IEEETable, []byte(d.ID))
		etagHash = crc32.Update(etagHash, crc32.IEEETable, []byte(d.Status))
		if !d.LastKeepalive.IsZero() {
			ts := d.LastKeepalive.Format(time.RFC3339)
			etagHash = crc32.Update(etagHash, crc32.IEEETable, []byte(ts))
		}
	}
	etag := fmt.Sprintf(`"gb28181-devices-%d-%d"`, len(devices), etagHash)
	w.Header().Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	writeJSON(w, http.StatusOK, devices)
}

// gb28181ChannelResponse wraps the persisted channel row with the live PTZ
// capability overlaid from the in-memory device catalog. The DB schema has no
// PTZType column — it arrives in the device's Catalog response and lives on
// the DeviceManager's Channel — so it is added here rather than in storage.
type gb28181ChannelResponse struct {
	storage.GB28181Channel
	PTZType int `json:"PTZType"`
	// EnrollBlocked is non-empty when auto-enroll suppressed this channel via
	// the cross-protocol dedup (#596) — another camera already streams from
	// the device's IP. Computed at display time (mirrors
	// EnsureGB28181Camera's L1 check); empty when enrolled, no collision, or
	// gb28181.allow_same_ip_enroll is set.
	EnrollBlocked string `json:"enroll_blocked,omitempty"`
}

// handleListGB28181Channels returns the channels for a specific GB28181 device.
func (s *server) handleListGB28181Channels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	deviceID := chi.URLParam(r, "id")

	if deviceID == "" {
		WriteError(w, http.StatusBadRequest, "device ID is required")
		return
	}

	dev, err := s.deps.DB.GetGB28181Device(ctx, deviceID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to query device")
		return
	}
	if dev == nil {
		WriteError(w, http.StatusNotFound, "device not found")
		return
	}

	channels, err := s.deps.DB.ListGB28181Channels(ctx, deviceID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list channels")
		return
	}

	resp := make([]gb28181ChannelResponse, 0, len(channels))
	for _, ch := range channels {
		out := gb28181ChannelResponse{GB28181Channel: ch}
		if s.deps.DeviceMgr != nil {
			if live, ok := s.deps.DeviceMgr.FindChannel(deviceID, ch.ID); ok {
				out.PTZType = live.PTZType
			}
		}
		out.EnrollBlocked = s.gbChannelEnrollBlocked(deviceID, ch.ID)
		resp = append(resp, out)
	}

	writeJSON(w, http.StatusOK, resp)
}

// gbChannelEnrollBlocked explains why auto-enroll has not created a camera
// for the channel ("" when it has, or when nothing blocks it). Mirrors the
// L1 IP-collision branch of camera.EnsureGB28181Camera so the UI can surface
// suppressed channels instead of leaving them invisible (#596).
func (s *server) gbChannelEnrollBlocked(deviceID, channelID string) string {
	if s.deps.Cam == nil {
		return ""
	}
	if _, enrolled := s.deps.Cam.GB28181CameraIDByChannel(deviceID, channelID); enrolled {
		return ""
	}
	if s.deps.Config != nil && s.deps.Config.GB28181.AllowSameIPEnroll {
		return ""
	}
	srcIP := s.gbDeviceSourceIP(deviceID)
	if srcIP == "" {
		return ""
	}
	if existingID, ok := s.deps.Cam.CameraIDByHostIP(srcIP); ok {
		return fmt.Sprintf("another camera (%s) already streams from this device IP — set gb28181.allow_same_ip_enroll to enroll both", existingID)
	}
	return ""
}

// gbDeviceSourceIP returns the host part of the device's SIP source address.
func (s *server) gbDeviceSourceIP(deviceID string) string {
	if s.deps.DeviceMgr == nil {
		return ""
	}
	d, ok := s.deps.DeviceMgr.Device(deviceID)
	if !ok {
		return ""
	}
	d.Mu.RLock()
	addr := d.NetAddr
	d.Mu.RUnlock()
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" {
		return host
	}
	return addr
}

// handleCatalogRefresh triggers a SIP MESSAGE catalog request to the device.
// Returns 202 Accepted as the catalog refresh is asynchronous.
func (s *server) handleCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	deviceID := chi.URLParam(r, "id")

	if deviceID == "" {
		WriteError(w, http.StatusBadRequest, "device ID is required")
		return
	}

	dev, err := s.deps.DB.GetGB28181Device(ctx, deviceID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to query device")
		return
	}
	if dev == nil {
		WriteError(w, http.StatusNotFound, "device not found")
		return
	}

	// Check device status - can only refresh online devices
	if dev.Status != "online" {
		WriteError(w, http.StatusConflict, "device is offline, cannot refresh catalog")
		return
	}

	// Send the SIP MESSAGE Catalog query to the device. The device responds
	// asynchronously with its channel list, which the SIP server's
	// handleMessage parses and persists to the DB.
	if s.deps.Catalog == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 catalog controller not available")
		return
	}
	if err := s.deps.Catalog.RequestCatalog(deviceID); err != nil {
		slog.Error("failed to send GB28181 catalog query", "device_id", deviceID, "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to send catalog query")
		return
	}
	slog.Info("GB28181 catalog refresh sent", "device_id", deviceID)

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":    "catalog_refresh_requested",
		"device_id": deviceID,
	})
}

// --- GB28181 Channel Session Handlers ---

// handleInviteChannel invites a channel to send media (INVITE).
// Returns 202 Accepted as the session establishment is asynchronous.
func (s *server) handleInviteChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")

	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}

	var deviceID string
	var found bool
	if s.deps.DeviceMgr != nil {
		for _, dev := range s.deps.DeviceMgr.AllDevices() {
			if _, ok := s.deps.DeviceMgr.FindChannel(dev.ID, channelID); ok {
				deviceID = dev.ID
				found = true
				break
			}
		}
	}

	if !found {
		WriteError(w, http.StatusNotFound, "channel not found")
		return
	}

	dev, ok := s.deps.DeviceMgr.Device(deviceID)
	if !ok {
		WriteError(w, http.StatusNotFound, "device not found")
		return
	}

	if dev.Status.Load() == platform.DeviceOffline {
		WriteError(w, http.StatusConflict, "device is offline, cannot invite channel")
		return
	}

	// Send the SIP INVITE via the SIP server's InviteChannel method.
	if s.deps.Inviter == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 invite not available")
		return
	}
	slog.Info("GB28181 channel invite requested", "device_id", deviceID, "channel_id", channelID)
	if err := s.deps.Inviter.InviteChannel(deviceID, channelID); err != nil {
		slog.Error("failed to send GB28181 INVITE", "channel_id", channelID, "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to send INVITE")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "invite_sent",
		"device_id":  deviceID,
		"channel_id": channelID,
	})
}

// handleByeChannel stops a channel's media session: transmits a SIP BYE to
// the device, tears down the local receiver, and flips the bound camera's
// recorder back to Reconnecting. Requires the SIP server (bye sender).
func (s *server) handleByeChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")

	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}

	if s.deps.Bye == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 BYE not available")
		return
	}

	if err := s.deps.Bye.ByeChannelByID(channelID); err != nil {
		slog.Error("failed to stop GB28181 session", "channel_id", channelID, "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to stop session")
		return
	}

	slog.Info("GB28181 channel session stopped", "channel_id", channelID)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "bye_sent",
		"channel_id": channelID,
	})
}

// ptzChannelRequest is the body of POST /api/gb28181/channels/{id}/ptz.
// Per the plan, the contract is {direction, zoom, preset}; speed remains an
// optional field defaulting to 0 (device default).
type ptzChannelRequest struct {
	Direction string `json:"direction"` // up/down/left/right/up-left/... / stop
	Zoom      int    `json:"zoom"`      // zoom speed/amount (0 = none)
	Preset    int    `json:"preset"`    // preset number (reserved for future use)
	Speed     byte   `json:"speed"`     // optional 0-255; 0 uses the device default
}

// handlePTZChannel sends a GB/T 28181 DeviceControl PTZ command to a channel.
func (s *server) handlePTZChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}

	if s.deps.PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}

	var req ptzChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Direction == "" {
		WriteError(w, http.StatusBadRequest, "direction is required")
		return
	}

	slog.Info("GB28181 channel PTZ requested", "channel_id", channelID, "direction", req.Direction)

	if err := s.deps.PTZ.SendPTZ(channelID, req.Direction, req.Speed); err != nil {
		switch {
		case errors.Is(err, platform.ErrChannelNotFound):
			WriteError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, platform.ErrDeviceOffline):
			WriteError(w, http.StatusConflict, err.Error())
		case errors.Is(err, platform.ErrPTZUnsupported), errors.Is(err, platform.ErrZoomUnsupported):
			WriteError(w, http.StatusNotFound, "PTZ not supported")
		default:
			slog.Error("failed to send GB28181 PTZ command", "channel_id", channelID, "error", err)
			WriteError(w, http.StatusInternalServerError, "failed to send PTZ command")
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "ptz_sent",
		"channel_id": channelID,
		"direction":  req.Direction,
	})
}

// lensControlRequest is the body of POST /api/gb28181/channels/{id}/lens
// (#341 lens extension): an FI instruction — iris-open | iris-close |
// focus-near | focus-far | stop.
type lensControlRequest struct {
	Action string `json:"action"`
	Speed  byte   `json:"speed"` // optional 0-255 lens-axis speed; 0 defaults to 0x40 (mid-range)
}

// handleChannelLensControl sends an FI lens instruction (GB/T 28181-2022
// § A.3.3 — iris/focus) to a channel via the PTZCmd transport.
func (s *server) handleChannelLensControl(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}
	if s.deps.PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}
	var req lensControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Action == "" {
		WriteError(w, http.StatusBadRequest, "action is required")
		return
	}
	switch req.Action {
	case platform.FIIrisClose, platform.FIIrisOpen, platform.FIFocusNear, platform.FIFocusFar, platform.FILensStop:
	default:
		WriteError(w, http.StatusBadRequest, "unknown action: iris-open, iris-close, focus-near, focus-far, stop")
		return
	}
	// 0x00 is a valid (slowest) FI speed, so an omitted speed defaults to
	// mid-range instead of creeping like the PTZ default passthrough does.
	if req.Speed == 0 && req.Action != platform.FILensStop {
		req.Speed = 0x40
	}

	slog.Info("GB28181 channel lens control requested", "channel_id", channelID, "action", req.Action)

	if err := s.deps.PTZ.SendFI(channelID, req.Action, req.Speed); err != nil {
		switch {
		case errors.Is(err, platform.ErrChannelNotFound):
			WriteError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, platform.ErrDeviceOffline):
			WriteError(w, http.StatusConflict, err.Error())
		case errors.Is(err, platform.ErrPTZUnsupported):
			WriteError(w, http.StatusNotFound, "PTZ not supported")
		case errors.Is(err, platform.ErrZoomUnsupported):
			WriteError(w, http.StatusNotFound, "lens control not supported")
		default:
			slog.Error("failed to send GB28181 lens command", "channel_id", channelID, "error", err)
			WriteError(w, http.StatusInternalServerError, "failed to send lens command")
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "lens_sent",
		"channel_id": channelID,
		"action":     req.Action,
	})
}

// handleChannelAuxSwitch toggles a channel auxiliary switch (GB/T 28181-2022
// § A.3.7 — wiper/light). switch is the switch number 1-255 (1 = wiper per
// the standard; 2 = light by convention).
func (s *server) handleChannelAuxSwitch(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}
	if s.deps.PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}
	var req struct {
		Switch int  `json:"switch"`
		On     bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Switch < 1 || req.Switch > 255 {
		WriteError(w, http.StatusBadRequest, "switch must be 1-255")
		return
	}

	slog.Info("GB28181 channel aux switch requested", "channel_id", channelID, "switch", req.Switch, "on", req.On)

	if err := s.deps.PTZ.SendAuxSwitch(channelID, byte(req.Switch), req.On); err != nil {
		switch {
		case errors.Is(err, platform.ErrChannelNotFound):
			WriteError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, platform.ErrDeviceOffline):
			WriteError(w, http.StatusConflict, err.Error())
		default:
			slog.Error("failed to send GB28181 aux switch command", "channel_id", channelID, "error", err)
			WriteError(w, http.StatusInternalServerError, "failed to send aux switch command")
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "aux_switch_sent",
		"channel_id": channelID,
		"switch":     req.Switch,
		"on":         req.On,
	})
}

// --- GB28181 PTZ presets (local registry + device commands, #339) ---
// GB28181 has no device-side preset query — the platform picks preset numbers
// (1-255) and devices only understand set/call/delete commands
// (GB/T 28181-2016 § A.3.4). Presets are therefore tracked in the local
// camera_ptz_presets table and mirrored to the device on every operation.

// gb28181PresetResponse mirrors the ONVIF PTZPreset shape consumed by the
// frontend (token + name).
type gb28181PresetResponse struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

func (s *server) handleGB28181PTZGetPresets(w http.ResponseWriter, r *http.Request, cameraID string) {
	presets := []gb28181PresetResponse{}
	if s.deps.DB != nil {
		rows, err := s.deps.DB.ListPTZPresets(r.Context(), cameraID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to list presets")
			return
		}
		for _, p := range rows {
			presets = append(presets, gb28181PresetResponse{Token: p.Token, Name: p.Name})
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"presets": presets})
}

func (s *server) handleGB28181PTZCreatePreset(w http.ResponseWriter, r *http.Request, cameraID string) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	channelID := s.gb28181ChannelID(cameraID)
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "camera is not bound to a GB28181 channel")
		return
	}
	if s.deps.PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}
	if s.deps.DB == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	token, ok := s.deps.DB.NextPTZPresetToken(r.Context(), cameraID)
	if !ok {
		WriteError(w, http.StatusConflict, "all 255 preset slots are in use")
		return
	}
	presetNum, err := strconv.Atoi(token)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "invalid preset token")
		return
	}
	// Best-effort SET on the device: the local row is created regardless —
	// devices without firmware preset support still get goto/delete commands
	// once the firmware catches up.
	if err := s.deps.PTZ.SendPTZPreset(channelID, platform.PresetSet, byte(presetNum)); err != nil &&
		!errors.Is(err, platform.ErrPTZUnsupported) {
		s.mapGB28181PTZError(w, err)
		return
	}
	if err := s.deps.DB.UpsertPTZPreset(r.Context(), storage.PTZPreset{
		CameraID:  cameraID,
		Token:     token,
		Name:      req.Name,
		CreatedAt: time.Now(),
	}); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save preset")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s *server) handleGB28181PTZGoToPreset(w http.ResponseWriter, r *http.Request, cameraID, token string) {
	channelID := s.gb28181ChannelID(cameraID)
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "camera is not bound to a GB28181 channel")
		return
	}
	if s.deps.PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}
	presetNum, err := strconv.Atoi(token)
	if err != nil || presetNum < 1 || presetNum > 255 {
		WriteError(w, http.StatusBadRequest, "invalid preset token")
		return
	}
	if err := s.deps.PTZ.SendPTZPreset(channelID, platform.PresetCall, byte(presetNum)); err != nil {
		s.mapGB28181PTZError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) handleGB28181PTZDeletePreset(w http.ResponseWriter, r *http.Request, cameraID, token string) {
	channelID := s.gb28181ChannelID(cameraID)
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "camera is not bound to a GB28181 channel")
		return
	}
	if s.deps.PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}
	presetNum, err := strconv.Atoi(token)
	if err != nil || presetNum < 1 || presetNum > 255 {
		WriteError(w, http.StatusBadRequest, "invalid preset token")
		return
	}
	if err := s.deps.PTZ.SendPTZPreset(channelID, platform.PresetDelete, byte(presetNum)); err != nil &&
		!errors.Is(err, platform.ErrPTZUnsupported) {
		s.mapGB28181PTZError(w, err)
		return
	}
	if s.deps.DB != nil {
		if err := s.deps.DB.DeletePTZPreset(r.Context(), cameraID, token); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to delete preset")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// mapGB28181PTZError maps PTZ controller errors to HTTP status codes.
func (s *server) mapGB28181PTZError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, platform.ErrChannelNotFound):
		WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, platform.ErrDeviceOffline):
		WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, platform.ErrPTZUnsupported), errors.Is(err, platform.ErrZoomUnsupported):
		WriteError(w, http.StatusNotFound, "PTZ not supported")
	default:
		slog.Error("failed to send GB28181 PTZ command", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to send PTZ command")
	}
}

// writeGBRecordError maps DeviceRecordService errors onto the typed-error
// track; deviceFailStatus is the status a device-side failure (INVITE/query/
// BYE/control rejected by the device) maps to for this endpoint (502 for
// fetch starts and queries, 409 for control, 500 for stop) (#1009).
func writeGBRecordError(w http.ResponseWriter, err error, deviceFailStatus int) {
	var (
		nf  *gb28181.ChannelNotFoundError
		una *gb28181.MediaUnavailableError
		rg  *gb28181.InvalidRangeError
	)
	switch {
	case errors.As(err, &una):
		httpx.WriteAPIError(w, http.StatusServiceUnavailable, err)
	case errors.As(err, &nf):
		httpx.WriteAPIError(w, http.StatusNotFound, err)
	case errors.As(err, &rg):
		httpx.WriteAPIError(w, http.StatusBadRequest, err)
	default:
		httpx.WriteAPIError(w, deviceFailStatus, err)
	}
}

// handleChannelRecords returns the device-side recording index for a time
// range (RecordInfo query + paged response correlation, #337).
func (s *server) handleChannelRecords(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}
	start, err := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "start must be RFC3339")
		return
	}
	end, err := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "end must be RFC3339")
		return
	}

	records, err := s.deps.Records.Records(channelID, start, end)
	if err != nil {
		writeGBRecordError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channel_id": channelID,
		"count":      len(records),
		"records":    records,
	})
}

// handleChannelPlaybackStart starts a device-recording fetch: a playback
// INVITE whose RTP/PS stream is muxed into a normal MiBee recording for the
// bound camera. One fetch per channel — a running fetch is replaced.
func (s *server) handleChannelPlaybackStart(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}
	var req struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	start, err := time.Parse(time.RFC3339, req.Start)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "start must be RFC3339")
		return
	}
	end, err := time.Parse(time.RFC3339, req.End)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "end must be RFC3339")
		return
	}

	if err := s.deps.Records.StartPlayback(channelID, start, end); err != nil {
		writeGBRecordError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "playback_started",
		"channel_id": channelID,
	})
}

// handleChannelDownloadStart starts a device-recording download (#378): an
// s=Download INVITE — the device streams at file speed and the media is muxed
// into the bound camera's recordings. One fetch per channel.
func (s *server) handleChannelDownloadStart(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}
	var req struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	start, err := time.Parse(time.RFC3339, req.Start)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "start must be RFC3339")
		return
	}
	end, err := time.Parse(time.RFC3339, req.End)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "end must be RFC3339")
		return
	}

	if err := s.deps.Records.StartDownload(channelID, start, end); err != nil {
		writeGBRecordError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "download_started",
		"channel_id": channelID,
	})
}

// handleChannelPlaybackStatus reports the running fetch (404 when idle).
func (s *server) handleChannelPlaybackStatus(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	st, ok, err := s.deps.Records.PlaybackStatus(channelID)
	if err != nil {
		writeGBRecordError(w, err, http.StatusServiceUnavailable)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"active": false, "channel_id": channelID})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleChannelPlaybackStop stops a running fetch (SIP BYE + finalize the
// partially-fetched recording).
func (s *server) handleChannelPlaybackStop(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if err := s.deps.Records.StopPlayback(channelID); err != nil {
		writeGBRecordError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "playback_stopped", "channel_id": channelID})
}

// handleChannelPlaybackControl drives a running fetch via SIP INFO
// MANSRTSP: pause / resume / seek (optional scale + position seconds).
func (s *server) handleChannelPlaybackControl(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	var req struct {
		Action   string  `json:"action"`
		Scale    float64 `json:"scale"`
		Position float64 `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch req.Action {
	case "pause", "resume", "seek":
	default:
		WriteError(w, http.StatusBadRequest, "action must be pause, resume, or seek")
		return
	}
	if err := s.deps.Records.PlaybackControl(channelID, req.Action, req.Scale, req.Position); err != nil {
		writeGBRecordError(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "control_sent", "action": req.Action})
}

// --- GB28181 Talk (语音对讲) / Alarms / MobilePosition handlers (#341) ---

// gbTalkUpgrader upgrades talk WebSocket connections (browser mic → server).
var gbTalkUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     middleware.WSOriginAllowed,
}

// handleGB28181TalkWS streams the browser microphone to a GB28181 camera's
// speaker: GET /api/cameras/{id}/gb28181/talk upgrades to a WebSocket whose
// binary frames carry G.711 A-law chunks (encoded browser-side). The INVITE
// handshake runs on connect; closing the socket BYEs the session.
func (s *server) handleGB28181TalkWS(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if s.deps.Media == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 device media API not available")
		return
	}
	cam := s.deps.Cam.GetCameraConfig(cameraID)
	if cam == nil || cam.Protocol != "gb28181" {
		WriteError(w, http.StatusBadRequest, "camera is not a GB28181 camera")
		return
	}
	channelID := cam.GB28181.ChannelID
	deviceID := cam.GB28181.DeviceID
	if channelID == "" || deviceID == "" {
		WriteError(w, http.StatusBadRequest, "camera is not bound to a GB28181 device/channel")
		return
	}

	if err := s.deps.Media.StartTalk(cameraID, deviceID, channelID); err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("talk setup failed: %v", err))
		return
	}
	defer func() {
		if err := s.deps.Media.StopTalk(channelID); err != nil {
			slog.Warn("gb28181: stop talk on WS close", "camera", cameraID, "error", err)
		}
	}()

	conn, err := gbTalkUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Full duplex (#341 + gb28181-go #141): the device's return audio
	// (sendrecv uplink leg) streams back over this same socket as binary
	// frames. A nil channel (no registry wired) keeps the socket send-only.
	upCh, unsub := s.deps.Media.SubscribeTalkUplink(channelID)
	var writerDone <-chan struct{}
	if upCh != nil {
		done := make(chan struct{})
		writerDone = done
		go func() {
			defer close(done)
			for payload := range upCh {
				if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
					return
				}
			}
		}()
	}
	defer func() {
		unsub()
		if writerDone != nil {
			<-writerDone
		}
	}()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		s.deps.Media.WriteTalkAudio(channelID, data)
	}
}

// handleGB28181TalkStatus reports whether a camera's intercom is active.
func (s *server) handleGB28181TalkStatus(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if s.deps.Media == nil {
		writeJSON(w, http.StatusOK, platform.TalkStatus{Active: false})
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Media.TalkStatusFor(cameraID))
}

// handleGB28181DeviceAlarms returns a device's recent alarm notifications.
func (s *server) handleGB28181DeviceAlarms(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if s.deps.Media == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Media.GB28181Alarms(deviceID))
}

// handleGB28181DevicePositions returns a device's recent mobile positions.
func (s *server) handleGB28181DevicePositions(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if s.deps.Media == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Media.GB28181Positions(deviceID))
}

// handleGB28181CascadeStatus reports the lower-level cascade client's
// registration state (Settings → GB28181 status card).
func (s *server) handleGB28181CascadeStatus(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Cascade == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":  false,
			"online":   false,
			"forwards": 0,
		})
		return
	}
	resp := map[string]any{
		"enabled":  true,
		"online":   s.deps.Cascade.Online(),
		"forwards": s.deps.Cascade.ForwardCount(),
	}
	if since, ok := s.deps.Cascade.RegistrationSince(); ok {
		resp["registered_for_seconds"] = int(since.Seconds())
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleChannelDeviceControl sends a non-PTZ DeviceControl instruction to a
// channel (#379): remote record start/stop, arm/disarm, alarm reset, reboot,
// home-position reset. TeleBoot requires confirm=true (destructive).
func (s *server) handleChannelDeviceControl(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	var req struct {
		Command string `json:"command"`
		Confirm bool   `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	type ctrl struct {
		element, value string
		needsConfirm   bool
	}
	cmds := map[string]ctrl{
		"record":        {element: "RecordCmd", value: "Record"},
		"stop_record":   {element: "RecordCmd", value: "StopRecord"},
		"set_guard":     {element: "GuardCmd", value: "SetGuard"},
		"reset_guard":   {element: "GuardCmd", value: "ResetGuard"},
		"reset_alarm":   {element: "AlarmCmd", value: "ResetAlarm"},
		"home_position": {element: "HomePosition", value: "1"},
		"tele_boot":     {element: "TeleBoot", value: "Boot", needsConfirm: true},
	}
	c, ok := cmds[req.Command]
	if !ok {
		WriteError(w, http.StatusBadRequest, "unknown command: record, stop_record, set_guard, reset_guard, reset_alarm, home_position, tele_boot")
		return
	}
	if c.needsConfirm && !req.Confirm {
		WriteError(w, http.StatusBadRequest, "tele_boot reboots the device — retry with confirm=true")
		return
	}
	if s.deps.PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}
	if err := s.deps.PTZ.SendDeviceControl(channelID, c.element, c.value); err != nil {
		slog.Warn("GB28181 device control failed", "channel_id", channelID, "command", req.Command, "error", err)
		WriteError(w, http.StatusBadGateway, "device control failed: "+err.Error())
		return
	}
	slog.Info("GB28181 device control sent", "channel_id", channelID, "command", req.Command)
	writeJSON(w, http.StatusOK, map[string]string{"status": "control_sent", "command": req.Command})
}
