package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// --- GB28181 Device Handlers ---

// handleListGB28181Devices returns all registered GB28181 devices with ETag support.
func (h *Handler) handleListGB28181Devices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse limit query param
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

	devices, err := h.db.ListGB28181Devices(ctx)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list devices")
		return
	}
	if devices == nil {
		devices = []storage.GB28181Device{}
	}

	// Apply limit
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
}

// handleListGB28181Channels returns the channels for a specific GB28181 device.
func (h *Handler) handleListGB28181Channels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	deviceID := chi.URLParam(r, "id")

	if deviceID == "" {
		WriteError(w, http.StatusBadRequest, "device ID is required")
		return
	}

	// Verify device exists
	dev, err := h.db.GetGB28181Device(ctx, deviceID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to query device")
		return
	}
	if dev == nil {
		WriteError(w, http.StatusNotFound, "device not found")
		return
	}

	channels, err := h.db.ListGB28181Channels(ctx, deviceID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list channels")
		return
	}

	resp := make([]gb28181ChannelResponse, 0, len(channels))
	for _, ch := range channels {
		out := gb28181ChannelResponse{GB28181Channel: ch}
		if h.gb28181DeviceMgr != nil {
			if live, ok := h.gb28181DeviceMgr.FindChannel(deviceID, ch.ID); ok {
				out.PTZType = live.PTZType
			}
		}
		resp = append(resp, out)
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleCatalogRefresh triggers a SIP MESSAGE catalog request to the device.
// Returns 202 Accepted as the catalog refresh is asynchronous.
func (h *Handler) handleCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	deviceID := chi.URLParam(r, "id")

	if deviceID == "" {
		WriteError(w, http.StatusBadRequest, "device ID is required")
		return
	}

	// Verify device exists
	dev, err := h.db.GetGB28181Device(ctx, deviceID)
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
	if h.gb28181Catalog == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 catalog controller not available")
		return
	}
	if err := h.gb28181Catalog.RequestCatalog(deviceID); err != nil {
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
func (h *Handler) handleInviteChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")

	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}

	// Find the channel by scanning all registered devices.
	var deviceID string
	var found bool
	if h.gb28181DeviceMgr != nil {
		for _, dev := range h.gb28181DeviceMgr.AllDevices() {
			if _, ok := h.gb28181DeviceMgr.FindChannel(dev.ID, channelID); ok {
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

	// Check if device is online
	dev, ok := h.gb28181DeviceMgr.Device(deviceID)
	if !ok {
		WriteError(w, http.StatusNotFound, "device not found")
		return
	}

	if dev.Status.Load() == gb28181.DeviceOffline {
		WriteError(w, http.StatusConflict, "device is offline, cannot invite channel")
		return
	}

	// Send the SIP INVITE via the SIP server's InviteChannel method.
	if h.gb28181Inviter == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 invite not available")
		return
	}
	slog.Info("GB28181 channel invite requested", "device_id", deviceID, "channel_id", channelID)
	if err := h.gb28181Inviter.InviteChannel(deviceID, channelID); err != nil {
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
func (h *Handler) handleByeChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")

	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}

	if h.gb28181Bye == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 BYE not available")
		return
	}

	if err := h.gb28181Bye.ByeChannelByID(channelID); err != nil {
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
func (h *Handler) handlePTZChannel(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}

	if h.gb28181PTZ == nil {
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

	if err := h.gb28181PTZ.SendPTZ(channelID, req.Direction, req.Speed); err != nil {
		switch {
		case errors.Is(err, gb28181.ErrChannelNotFound):
			WriteError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, gb28181.ErrDeviceOffline):
			WriteError(w, http.StatusConflict, err.Error())
		case errors.Is(err, gb28181.ErrPTZUnsupported), errors.Is(err, gb28181.ErrZoomUnsupported):
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

func (h *Handler) handleGB28181PTZGetPresets(w http.ResponseWriter, r *http.Request, cameraID string) {
	presets := []gb28181PresetResponse{}
	if h.db != nil {
		rows, err := h.db.ListPTZPresets(r.Context(), cameraID)
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

func (h *Handler) handleGB28181PTZCreatePreset(w http.ResponseWriter, r *http.Request, cameraID string) {
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
	channelID := h.gb28181ChannelID(cameraID)
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "camera is not bound to a GB28181 channel")
		return
	}
	if h.gb28181PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	token, ok := h.db.NextPTZPresetToken(r.Context(), cameraID)
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
	if err := h.gb28181PTZ.SendPTZPreset(channelID, gb28181.PresetSet, byte(presetNum)); err != nil &&
		!errors.Is(err, gb28181.ErrPTZUnsupported) {
		h.mapGB28181PTZError(w, err)
		return
	}
	if err := h.db.UpsertPTZPreset(r.Context(), storage.PTZPreset{
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

func (h *Handler) handleGB28181PTZGoToPreset(w http.ResponseWriter, r *http.Request, cameraID, token string) {
	channelID := h.gb28181ChannelID(cameraID)
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "camera is not bound to a GB28181 channel")
		return
	}
	if h.gb28181PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}
	presetNum, err := strconv.Atoi(token)
	if err != nil || presetNum < 1 || presetNum > 255 {
		WriteError(w, http.StatusBadRequest, "invalid preset token")
		return
	}
	if err := h.gb28181PTZ.SendPTZPreset(channelID, gb28181.PresetCall, byte(presetNum)); err != nil {
		h.mapGB28181PTZError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleGB28181PTZDeletePreset(w http.ResponseWriter, r *http.Request, cameraID, token string) {
	channelID := h.gb28181ChannelID(cameraID)
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "camera is not bound to a GB28181 channel")
		return
	}
	if h.gb28181PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}
	presetNum, err := strconv.Atoi(token)
	if err != nil || presetNum < 1 || presetNum > 255 {
		WriteError(w, http.StatusBadRequest, "invalid preset token")
		return
	}
	if err := h.gb28181PTZ.SendPTZPreset(channelID, gb28181.PresetDelete, byte(presetNum)); err != nil &&
		!errors.Is(err, gb28181.ErrPTZUnsupported) {
		h.mapGB28181PTZError(w, err)
		return
	}
	if h.db != nil {
		if err := h.db.DeletePTZPreset(r.Context(), cameraID, token); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to delete preset")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// mapGB28181PTZError maps PTZ controller errors to HTTP status codes.
func (h *Handler) mapGB28181PTZError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gb28181.ErrChannelNotFound):
		WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, gb28181.ErrDeviceOffline):
		WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, gb28181.ErrPTZUnsupported), errors.Is(err, gb28181.ErrZoomUnsupported):
		WriteError(w, http.StatusNotFound, "PTZ not supported")
	default:
		slog.Error("failed to send GB28181 PTZ command", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to send PTZ command")
	}
}

// gb28181DeviceRecord is the JSON shape of one device-side record entry.
type gb28181DeviceRecord struct {
	Name      string `json:"name"`
	FilePath  string `json:"file_path,omitempty"`
	StartTime string `json:"start_time"` // RFC3339
	EndTime   string `json:"end_time"`   // RFC3339
}

// parseGB28181Time parses a device-supplied timestamp. GB/T 28181 uses
// "2006-01-02T15:04:05"; some vendors use a space separator. Local device
// time is interpreted in the NVR's local timezone (devices lack TZ info).
func parseGB28181Time(v string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// resolveChannelDevice finds the device owning channelID across all
// registered devices ("" when unknown).
func (h *Handler) resolveChannelDevice(channelID string) (string, bool) {
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

// handleChannelRecords returns the device-side recording index for a time
// range (RecordInfo query + paged response correlation, #337).
func (h *Handler) handleChannelRecords(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	if startStr == "" || endStr == "" {
		WriteError(w, http.StatusBadRequest, "start and end query params are required")
		return
	}
	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "start must be RFC3339")
		return
	}
	end, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "end must be RFC3339")
		return
	}
	if !end.After(start) || end.Sub(start) > 7*24*time.Hour {
		WriteError(w, http.StatusBadRequest, "invalid range: end must be after start, max 7 days")
		return
	}

	deviceID, ok := h.resolveChannelDevice(channelID)
	if !ok {
		WriteError(w, http.StatusNotFound, "channel not found")
		return
	}
	if h.gb28181Media == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 device media API not available")
		return
	}

	items, err := h.gb28181Media.QueryChannelRecords(deviceID, channelID, start, end)
	if err != nil {
		slog.Warn("GB28181 record query failed", "channel_id", channelID, "error", err)
		WriteError(w, http.StatusBadGateway, "device record query failed: "+err.Error())
		return
	}

	records := make([]gb28181DeviceRecord, 0, len(items))
	for _, it := range items {
		st, okSt := parseGB28181Time(it.StartTime)
		et, okEt := parseGB28181Time(it.EndTime)
		if !okSt || !okEt {
			continue // malformed device entries are dropped, not fatal
		}
		records = append(records, gb28181DeviceRecord{
			Name:      it.Name,
			FilePath:  it.FilePath,
			StartTime: st.UTC().Format(time.RFC3339),
			EndTime:   et.UTC().Format(time.RFC3339),
		})
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
func (h *Handler) handleChannelPlaybackStart(w http.ResponseWriter, r *http.Request) {
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
	if !end.After(start) || end.Sub(start) > 24*time.Hour {
		WriteError(w, http.StatusBadRequest, "invalid range: end must be after start, max 24 hours")
		return
	}

	deviceID, ok := h.resolveChannelDevice(channelID)
	if !ok {
		WriteError(w, http.StatusNotFound, "channel not found")
		return
	}
	if h.gb28181Media == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 device media API not available")
		return
	}

	slog.Info("GB28181 channel playback requested", "channel_id", channelID, "start", req.Start, "end", req.End)
	if err := h.gb28181Media.StartPlayback(deviceID, channelID, start, end); err != nil {
		slog.Warn("GB28181 playback start failed", "channel_id", channelID, "error", err)
		WriteError(w, http.StatusBadGateway, "playback start failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "playback_started",
		"channel_id": channelID,
	})
}

// handleChannelPlaybackStatus reports the running fetch (404 when idle).
func (h *Handler) handleChannelPlaybackStatus(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if h.gb28181Media == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 device media API not available")
		return
	}
	st, ok := h.gb28181Media.PlaybackStatusFor(channelID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"active": false, "channel_id": channelID})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleChannelPlaybackStop stops a running fetch (SIP BYE + finalize the
// partially-fetched recording).
func (h *Handler) handleChannelPlaybackStop(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if h.gb28181Media == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 device media API not available")
		return
	}
	if err := h.gb28181Media.StopPlayback(channelID); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to stop playback")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "playback_stopped", "channel_id": channelID})
}

// handleChannelPlaybackControl drives a running fetch via SIP INFO
// MANSRTSP: pause / resume / seek (optional scale + position seconds).
func (h *Handler) handleChannelPlaybackControl(w http.ResponseWriter, r *http.Request) {
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
	if h.gb28181Media == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 device media API not available")
		return
	}
	if err := h.gb28181Media.PlaybackControl(channelID, req.Action, req.Scale, req.Position); err != nil {
		WriteError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "control_sent", "action": req.Action})
}

// --- GB28181 Talk (语音对讲) / Alarms / MobilePosition handlers (#341) ---

// gbTalkUpgrader upgrades talk WebSocket connections (browser mic → server).
var gbTalkUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// handleGB28181TalkWS streams the browser microphone to a GB28181 camera's
// speaker: GET /api/cameras/{id}/gb28181/talk upgrades to a WebSocket whose
// binary frames carry G.711 A-law chunks (encoded browser-side). The INVITE
// handshake runs on connect; closing the socket BYEs the session.
func (h *Handler) handleGB28181TalkWS(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if h.gb28181Media == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 device media API not available")
		return
	}
	cam := h.camMgr.GetCameraConfig(cameraID)
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

	if err := h.gb28181Media.StartTalk(cameraID, deviceID, channelID); err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("talk setup failed: %v", err))
		return
	}
	defer func() {
		if err := h.gb28181Media.StopTalk(channelID); err != nil {
			slog.Warn("gb28181: stop talk on WS close", "camera", cameraID, "error", err)
		}
	}()

	conn, err := gbTalkUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		h.gb28181Media.WriteTalkAudio(channelID, data)
	}
}

// handleGB28181TalkStatus reports whether a camera's intercom is active.
func (h *Handler) handleGB28181TalkStatus(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if h.gb28181Media == nil {
		writeJSON(w, http.StatusOK, gb28181.TalkStatus{Active: false})
		return
	}
	writeJSON(w, http.StatusOK, h.gb28181Media.TalkStatusFor(cameraID))
}

// handleGB28181DeviceAlarms returns a device's recent alarm notifications.
func (h *Handler) handleGB28181DeviceAlarms(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if h.gb28181Media == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, h.gb28181Media.GB28181Alarms(deviceID))
}

// handleGB28181DevicePositions returns a device's recent mobile positions.
func (h *Handler) handleGB28181DevicePositions(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if h.gb28181Media == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, h.gb28181Media.GB28181Positions(deviceID))
}

// handleGB28181CascadeStatus reports the lower-level cascade client's
// registration state (Settings → GB28181 status card).
func (h *Handler) handleGB28181CascadeStatus(w http.ResponseWriter, _ *http.Request) {
	if h.gb28181Cascade == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":  false,
			"online":   false,
			"forwards": 0,
		})
		return
	}
	resp := map[string]any{
		"enabled":  true,
		"online":   h.gb28181Cascade.Online(),
		"forwards": h.gb28181Cascade.ForwardCount(),
	}
	if since, ok := h.gb28181Cascade.RegistrationSince(); ok {
		resp["registered_for_seconds"] = int(since.Seconds())
	}
	writeJSON(w, http.StatusOK, resp)
}
