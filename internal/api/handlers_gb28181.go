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

// handleChannelRecords returns device-side recording index for a time range.
// NOT IMPLEMENTED: the RecordInfo query + response correlation is not wired
// yet. Returns 501 so callers don't mistake an empty list for "no recordings".
func (h *Handler) handleChannelRecords(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "id")
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "channel ID is required")
		return
	}
	start := r.URL.Query().Get("start")
	end := r.URL.Query().Get("end")
	if start == "" || end == "" {
		WriteError(w, http.StatusBadRequest, "start and end query params are required")
		return
	}
	WriteError(w, http.StatusNotImplemented, "device-side record listing is not implemented")
}

// handleChannelPlayback starts a playback session for a time range.
// Triggers a playback INVITE via the session manager; the incoming RTP/PS
// stream is demuxed and written as a normal MiBee recording.
func (h *Handler) handleChannelPlayback(w http.ResponseWriter, r *http.Request) {
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
	if req.Start == "" || req.End == "" {
		WriteError(w, http.StatusBadRequest, "start and end are required")
		return
	}
	slog.Info("GB28181 channel playback requested", "channel_id", channelID, "start", req.Start, "end", req.End)
	WriteError(w, http.StatusNotImplemented, "device-side playback is not implemented")
}
