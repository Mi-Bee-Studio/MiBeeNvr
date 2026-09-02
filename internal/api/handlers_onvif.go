package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/go-chi/chi/v5"
	"github.com/mickeyzzc/gb28181-go/platform"
)

// --- ONVIF camera management endpoints ---

func (h *Handler) handleONVIFCameraProfiles(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	client, err := h.camMgr.GetONVIFClient(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}

	profiles, err := client.GetProfiles(r.Context())
	if err != nil {
		logger.Error("failed to get profiles", "camera_id", cameraID, "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to get profiles")
		return
	}

	caps, err := client.GetCapabilities(r.Context())
	if err != nil {
		caps = &onvif.DeviceCapabilitiesDetailed{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"profiles":     profiles,
		"capabilities": caps,
	})
}

func (h *Handler) handleONVIFCapabilities(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	client, err := h.camMgr.GetONVIFClient(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}

	detailed, err := client.GetCapabilities(r.Context())
	if err != nil {
		slog.Debug("failed to get capabilities from device, using default capabilities",
			"camera_id", cameraID, "error", err)
		detailed = &onvif.DeviceCapabilitiesDetailed{}
	}

	// Attach cached device info (lazy — fetched once, cached in camera manager)
	deviceInfo := h.camMgr.GetCachedDeviceInfo(r.Context(), cameraID)
	detailed.DeviceInfo = deviceInfo

	writeJSON(w, http.StatusOK, detailed)
}

// --- ONVIF discovery endpoints ---

// handleONVIFProbe probes a single ONVIF device by sending a WS-Discovery
// probe via HTTP POST directly to host:port (no multicast needed).
func (h *Handler) handleONVIFProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host    string `json:"host"`
		Port    int    `json:"port"`
		Timeout int    `json:"timeout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Timeout = 5
	}
	if req.Host == "" {
		WriteError(w, http.StatusBadRequest, "host is required")
		return
	}
	if !validateIP(req.Host) {
		WriteError(w, http.StatusBadRequest, "invalid IP address format")
		return
	}
	if req.Port <= 0 {
		req.Port = 80
	}
	if req.Timeout <= 0 {
		req.Timeout = 5
	}
	if req.Timeout > 30 {
		WriteError(w, http.StatusBadRequest, "timeout must be between 1 and 30 seconds")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.Timeout)*time.Second)
	defer cancel()

	device, err := onvif.ProbeDevice(ctx, req.Host, req.Port, time.Duration(req.Timeout)*time.Second)
	if err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("probe failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"device": device,
	})
}

func (h *Handler) handleONVIFDiscover(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Timeout int `json:"timeout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Timeout = 5
	}
	if req.Timeout <= 0 {
		req.Timeout = 5
	}
	if req.Timeout > 30 {
		WriteError(w, http.StatusBadRequest, "timeout must be between 1 and 30 seconds")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.Timeout)*time.Second)
	defer cancel()

	result := onvif.Discover(ctx, time.Duration(req.Timeout)*time.Second)
	if result.Devices == nil {
		result.Devices = []onvif.DiscoveredDevice{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleONVIFDeviceDetail(w http.ResponseWriter, r *http.Request) {
	ip := chi.URLParam(r, "ip")
	if ip == "" {
		WriteError(w, http.StatusBadRequest, "IP address is required")
		return
	}
	if !validateIP(ip) {
		WriteError(w, http.StatusBadRequest, "invalid IP address format")
		return
	}
	ctx := r.Context()
	client := onvif.NewClient(fmt.Sprintf("http://%s/onvif/device_service", ip), "", "")
	if err := client.Connect(ctx); err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("failed to connect to device: %v", err))
		return
	}
	info, err := client.GetDeviceInformation(ctx)
	if err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("failed to get device info: %v", err))
		return
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		profiles = nil
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"device_info": info,
		"profiles":    profiles,
	})
}

func (h *Handler) requireONVIF(w http.ResponseWriter, r *http.Request) bool {
	if h.db == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return false
	}
	cameraID := chi.URLParam(r, "id")
	camera, err := h.db.GetCamera(r.Context(), cameraID)
	if err != nil || camera == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return false
	}
	if camera.Protocol != "onvif" {
		WriteError(w, http.StatusBadRequest, "PTZ control is only available for ONVIF cameras")
		return false
	}
	return true
}

// cameraProtocol returns the camera's protocol ("" when unknown).
func (h *Handler) cameraProtocol(r *http.Request, cameraID string) string {
	if h.db == nil {
		return ""
	}
	camera, err := h.db.GetCamera(r.Context(), cameraID)
	if err != nil || camera == nil {
		return ""
	}
	return camera.Protocol
}

// handleGB28181PTZMove maps an ONVIF-style continuous-move vector onto the
// GB/T 28181 PTZ direction bits (one axis at a time, matching the direction
// pad semantics) and sends it via the channel PTZ controller.
func (h *Handler) handleGB28181PTZMove(w http.ResponseWriter, r *http.Request, cameraID string, pan, tilt, zoom float64) {
	channelID := h.gb28181ChannelID(cameraID)
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "camera is not bound to a GB28181 channel")
		return
	}
	if h.gb28181PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}
	const speed = byte(128)
	switch {
	case zoom > 0:
		h.sendGB28181PTZ(w, channelID, platform.DirZoomIn, speed)
	case zoom < 0:
		h.sendGB28181PTZ(w, channelID, platform.DirZoomOut, speed)
	case tilt > 0:
		h.sendGB28181PTZ(w, channelID, platform.DirUp, speed)
	case tilt < 0:
		h.sendGB28181PTZ(w, channelID, platform.DirDown, speed)
	case pan > 0:
		h.sendGB28181PTZ(w, channelID, platform.DirRight, speed)
	case pan < 0:
		h.sendGB28181PTZ(w, channelID, platform.DirLeft, speed)
	default:
		h.sendGB28181PTZ(w, channelID, platform.DirStop, 0)
	}
}

// handleGB28181PTZStop sends the GB/T 28181 PTZ stop command.
func (h *Handler) handleGB28181PTZStop(w http.ResponseWriter, r *http.Request, cameraID string) {
	channelID := h.gb28181ChannelID(cameraID)
	if channelID == "" {
		WriteError(w, http.StatusBadRequest, "camera is not bound to a GB28181 channel")
		return
	}
	if h.gb28181PTZ == nil {
		WriteError(w, http.StatusServiceUnavailable, "GB28181 PTZ controller not available")
		return
	}
	h.sendGB28181PTZ(w, channelID, platform.DirStop, 0)
}

// gb28181ChannelID resolves the camera's configured GB28181 channel binding.
func (h *Handler) gb28181ChannelID(cameraID string) string {
	if h.camMgr == nil {
		return ""
	}
	cam := h.camMgr.GetCameraConfig(cameraID)
	if cam == nil || cam.Protocol != "gb28181" {
		return ""
	}
	return cam.GB28181.ChannelID
}

func (h *Handler) sendGB28181PTZ(w http.ResponseWriter, channelID, direction string, speed byte) {
	if err := h.gb28181PTZ.SendPTZ(channelID, direction, speed); err != nil {
		switch {
		case errors.Is(err, platform.ErrChannelNotFound):
			WriteError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, platform.ErrDeviceOffline):
			WriteError(w, http.StatusConflict, err.Error())
		case errors.Is(err, platform.ErrPTZUnsupported), errors.Is(err, platform.ErrZoomUnsupported):
			WriteError(w, http.StatusNotFound, "PTZ not supported")
		default:
			logger.Error("failed to send GB28181 PTZ command", "channel_id", channelID, "error", err)
			WriteError(w, http.StatusInternalServerError, "failed to send PTZ command")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- PTZ control endpoints ---

func (h *Handler) handlePTZMove(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	h.suppressPTZMotion(cameraID)
	var req struct {
		Mode string  `json:"mode"`
		Pan  float64 `json:"pan"`
		Tilt float64 `json:"tilt"`
		Zoom float64 `json:"zoom"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Mode != "continuous" && req.Mode != "absolute" && req.Mode != "relative" {
		WriteError(w, http.StatusBadRequest, "mode must be continuous, absolute, or relative")
		return
	}
	// GB28181 cameras: translate the continuous-move vector into a GB/T 28181
	// DeviceControl PTZ command via the SIP MESSAGE transport.
	if h.cameraProtocol(r, cameraID) == "gb28181" {
		h.handleGB28181PTZMove(w, r, cameraID, req.Pan, req.Tilt, req.Zoom)
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	vec := onvif.PTZVector{Pan: req.Pan, Tilt: req.Tilt, Zoom: req.Zoom}
	switch req.Mode {
	case "continuous":
		err = ptz.ContinuousMove(r.Context(), vec)
	case "absolute":
		err = ptz.AbsoluteMove(r.Context(), vec)
	case "relative":
		err = ptz.RelativeMove(r.Context(), vec)
	}
	if err != nil {
		logger.Error("PTZ command failed", "camera_id", cameraID, "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "PTZ command failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handlePTZStop(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	h.suppressPTZMotion(cameraID)
	if h.cameraProtocol(r, cameraID) == "gb28181" {
		h.handleGB28181PTZStop(w, r, cameraID)
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	if err := ptz.Stop(r.Context(), true, true); err != nil {
		logger.Error("PTZ stop failed", "camera_id", cameraID, "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "PTZ stop failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (h *Handler) handlePTZStatus(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	pos, moving, err := ptz.GetStatus(r.Context())
	if err != nil {
		// Some devices (NVR/encoders, fixed-lens cameras) advertise PTZ in
		// GetCapabilities but have no PTZ node — GetStatus returns a SOAP Fault
		// (HTTP 400 + Envelope) or "not supported". Return a default idle status
		// instead of 500 so the UI doesn't error out; the PTZ panel is already
		// gated by capabilities upstream.
		msg := err.Error()
		if strings.Contains(msg, "not supported") || strings.Contains(msg, "<Fault>") ||
			strings.Contains(msg, "Fault>") || isSOAPFaultStatus(msg) {
			slog.Debug("PTZ GetStatus rejected by device, returning default status",
				"camera_id", cameraID, "error", err)
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"pan": 0.0, "tilt": 0.0, "zoom": 0.0, "moving": false,
			})
			return
		}
		logger.Error("get PTZ status failed", "camera_id", cameraID, "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "get PTZ status failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pan":    pos.Pan,
		"tilt":   pos.Tilt,
		"zoom":   pos.Zoom,
		"moving": moving,
	})
}

// --- PTZ preset endpoints ---

func (h *Handler) handlePTZGetPresets(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if h.cameraProtocol(r, cameraID) == "gb28181" {
		h.handleGB28181PTZGetPresets(w, r, cameraID)
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	presets, err := ptz.GetPresets(r.Context())
	if err != nil {
		// Limited ONVIF devices (e.g. ESP32 MiBeeCam) don't implement PTZ.
		// Return empty list instead of 500.
		msg := err.Error()
		if strings.Contains(msg, "not supported") || strings.Contains(msg, "<Fault>") {
			slog.Debug("PTZ not supported by device, returning empty presets", "camera_id", cameraID, "error", err)
			writeJSON(w, http.StatusOK, map[string]interface{}{"presets": []onvif.PTZPreset{}})
			return
		}
		logger.Error("get PTZ presets failed", "camera_id", cameraID, "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "get PTZ presets failed")
		return
	}
	if presets == nil {
		presets = []onvif.PTZPreset{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"presets": presets,
	})
}

func (h *Handler) handlePTZCreatePreset(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if h.cameraProtocol(r, cameraID) == "gb28181" {
		h.handleGB28181PTZCreatePreset(w, r, cameraID)
		return
	}
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
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	token, err := ptz.SetPreset(r.Context(), req.Name)
	if err != nil {
		logger.Error("create PTZ preset failed", "camera_id", cameraID, "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "create PTZ preset failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (h *Handler) handlePTZGoToPreset(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	h.suppressPTZMotion(cameraID)
	token := chi.URLParam(r, "token")
	if h.cameraProtocol(r, cameraID) == "gb28181" {
		h.handleGB28181PTZGoToPreset(w, r, cameraID, token)
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	if err := ptz.GoToPreset(r.Context(), token); err != nil {
		logger.Error("go to PTZ preset failed", "camera_id", cameraID, "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "go to PTZ preset failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handlePTZDeletePreset(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	token := chi.URLParam(r, "token")
	if h.cameraProtocol(r, cameraID) == "gb28181" {
		h.handleGB28181PTZDeletePreset(w, r, cameraID, token)
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	if err := ptz.RemovePreset(r.Context(), token); err != nil {
		logger.Error("delete PTZ preset failed", "camera_id", cameraID, "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "delete PTZ preset failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// isSOAPFaultStatus reports whether err represents a SOAP Fault returned with an
// HTTP error status (400/500/501). Such responses indicate the device received
// the request but rejected the operation (e.g. a PTZ GetStatus on a device that
// advertises PTZ in capabilities but has no PTZ node). Distinguished from
// isAuthError (which also matches status 400) by requiring a SOAP envelope/Fault
// body — pure auth rejections carry no envelope.
func isSOAPFaultStatus(errStr string) bool {
	hasStatus := strings.Contains(errStr, "status 400") ||
		strings.Contains(errStr, "status 500") ||
		strings.Contains(errStr, "status 501")
	hasBody := strings.Contains(errStr, "Envelope") ||
		strings.Contains(errStr, "Fault") ||
		strings.Contains(errStr, "SOAP-ENV")
	return hasStatus && hasBody
}

// handleONVIFPTZError maps ONVIF PTZ controller errors to appropriate HTTP responses.
func handleONVIFPTZError(w http.ResponseWriter, cameraID string, err error) {
	switch {
	case errors.As(err, new(*model.CameraNotFoundError)):
		writeAPIError(w, http.StatusNotFound, err)
	case errors.As(err, new(*model.ONVIFNotCameraError)):
		writeAPIError(w, http.StatusBadRequest, err)
	case errors.As(err, new(*model.ONVIFConnectionError)):
		writeAPIError(w, http.StatusBadGateway, err)
	case errors.As(err, new(*model.ONVIFNoProfilesError)):
		writeAPIError(w, http.StatusNotFound, err)
	default:
		logger.Error("PTZ operation failed", "camera_id", cameraID, "error", err)
		WriteError(w, http.StatusInternalServerError, "PTZ operation failed")
	}
}

// --- Snapshot URI endpoint ---

// handleSnapshotGetUri returns the ONVIF snapshot URI for a camera.
func (h *Handler) handleSnapshotGetUri(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	provider, err := h.camMgr.GetSnapshotProvider(r.Context(), cameraID)
	if err != nil {
		handleONVIFSnapshotError(w, cameraID, err)
		return
	}
	uri, err := provider.GetSnapshotUri(r.Context())
	if err != nil {
		// Limited ONVIF devices (e.g. ESP32 MiBeeCam) don't implement GetSnapshotUri.
		msg := err.Error()
		if strings.Contains(msg, "not supported") || strings.Contains(msg, "<Fault>") {
			slog.Debug("snapshot URI not supported by device", "camera_id", cameraID, "error", err)
			WriteError(w, http.StatusNotFound, "snapshot not supported by this camera")
			return
		}
		logger.Error("get snapshot URI failed", "camera_id", cameraID, "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "get snapshot URI failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"uri": uri})
}

// --- Imaging endpoints ---

// handleImagingGetSettings returns current imaging settings for a camera.
func (h *Handler) handleImagingGetSettings(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	img, err := h.camMgr.GetImagingController(r.Context(), cameraID)
	if err != nil {
		handleONVIFImagingError(w, cameraID, err)
		return
	}
	settings, err := img.GetImagingSettings(r.Context())
	if err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("get imaging settings failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// handleImagingSetSettings applies imaging parameter changes for a camera.
func (h *Handler) handleImagingSetSettings(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	var req onvif.ImagingSettings
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	img, err := h.camMgr.GetImagingController(r.Context(), cameraID)
	if err != nil {
		handleONVIFImagingError(w, cameraID, err)
		return
	}
	if err := img.SetImagingSettings(r.Context(), req); err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("set imaging settings failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleImagingGetOptions returns supported imaging parameter ranges for a camera.
func (h *Handler) handleImagingGetOptions(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	img, err := h.camMgr.GetImagingController(r.Context(), cameraID)
	if err != nil {
		handleONVIFImagingError(w, cameraID, err)
		return
	}
	options, err := img.GetImagingOptions(r.Context())
	if err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("get imaging options failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, options)
}

// handleONVIFImagingError maps ONVIF imaging controller errors to appropriate HTTP responses.
func handleONVIFImagingError(w http.ResponseWriter, cameraID string, err error) {
	switch {
	case errors.As(err, new(*model.CameraNotFoundError)):
		writeAPIError(w, http.StatusNotFound, err)
	case errors.As(err, new(*model.ONVIFNotCameraError)):
		writeAPIError(w, http.StatusBadRequest, err)
	case errors.As(err, new(*model.ONVIFConnectionError)):
		writeAPIError(w, http.StatusBadGateway, err)
	case errors.As(err, new(*model.ONVIFNoProfilesError)):
		writeAPIError(w, http.StatusNotFound, err)
	default:
		logger.Error("imaging operation failed", "camera_id", cameraID, "error", err)
		WriteError(w, http.StatusInternalServerError, "imaging operation failed")
	}
}

// handleONVIFSnapshotError maps ONVIF snapshot provider errors to appropriate HTTP responses.
// Mirrors handleONVIFPTZError/handleONVIFImagingError but reports "snapshot operation failed"
// (not the misleading "PTZ operation failed") so the client sees an accurate context.
func handleONVIFSnapshotError(w http.ResponseWriter, cameraID string, err error) {
	switch {
	case errors.As(err, new(*model.CameraNotFoundError)):
		writeAPIError(w, http.StatusNotFound, err)
	case errors.As(err, new(*model.ONVIFNotCameraError)):
		writeAPIError(w, http.StatusBadRequest, err)
	case errors.As(err, new(*model.ONVIFConnectionError)):
		writeAPIError(w, http.StatusBadGateway, err)
	case errors.As(err, new(*model.ONVIFNoProfilesError)):
		writeAPIError(w, http.StatusNotFound, err)
	default:
		logger.Error("snapshot operation failed", "camera_id", cameraID, "error", err)
		WriteError(w, http.StatusInternalServerError, "snapshot operation failed")
	}
}

// --- Device Management endpoints ---

// handleONVIFReboot reboots the target ONVIF camera.
func (h *Handler) handleONVIFReboot(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	if err := dm.SystemReboot(r.Context()); err != nil {
		if errors.Is(err, onvif.ErrUnsupported) {
			WriteError(w, http.StatusNotImplemented, "reboot not supported by device")
			return
		}
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("reboot failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleONVIFGetNetwork returns network interface configuration.
func (h *Handler) handleONVIFGetNetwork(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	ifaces, err := dm.GetNetworkInterfaces(r.Context())
	if err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("get network interfaces failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"interfaces": ifaces})
}

// handleONVIFSetNetwork configures network interfaces on the target camera.
func (h *Handler) handleONVIFSetNetwork(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	var req struct {
		Interfaces []onvif.NetworkInterface `json:"interfaces"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	if err := dm.SetNetworkInterfaces(r.Context(), req.Interfaces); err != nil {
		if errors.Is(err, onvif.ErrUnsupported) {
			WriteError(w, http.StatusNotImplemented, "set network interfaces not supported by device")
			return
		}
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("set network interfaces failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleONVIFGetUsers returns user accounts on the target camera.
func (h *Handler) handleONVIFGetUsers(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	users, err := dm.GetUsers(r.Context())
	if err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("get users failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"users": users})
}

// handleONVIFCreateUsers creates user accounts on the target camera.
func (h *Handler) handleONVIFCreateUsers(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	var req struct {
		Users []onvif.ONVIFUser `json:"users"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	if err := dm.CreateUsers(r.Context(), req.Users); err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("create users failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleONVIFDeleteUsers deletes user accounts from the target camera.
func (h *Handler) handleONVIFDeleteUsers(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	var req struct {
		Usernames []string `json:"usernames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	if err := dm.DeleteUsers(r.Context(), req.Usernames); err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("delete users failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleONVIFSetUser modifies a user account on the target camera.
func (h *Handler) handleONVIFSetUser(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	username := chi.URLParam(r, "username")
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	if err := dm.SetUser(r.Context(), username, req.Password); err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("set user failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleONVIFDeviceMgmtError maps ONVIF device management errors to appropriate HTTP responses.
func handleONVIFDeviceMgmtError(w http.ResponseWriter, cameraID string, err error) {
	switch {
	case errors.As(err, new(*model.CameraNotFoundError)):
		writeAPIError(w, http.StatusNotFound, err)
	case errors.As(err, new(*model.ONVIFNotCameraError)):
		writeAPIError(w, http.StatusBadRequest, err)
	case errors.As(err, new(*model.ONVIFConnectionError)):
		writeAPIError(w, http.StatusBadGateway, err)
	default:
		logger.Error("device management operation failed", "camera_id", cameraID, "error", err)
		WriteError(w, http.StatusInternalServerError, "device management operation failed")
	}
}

// registerONVIFRoutes registers ONVIF discovery and probe routes.
func (h *Handler) registerONVIFRoutes(r chi.Router) {
	r.Post("/api/onvif/discover", h.handleONVIFDiscover)
	r.Get("/api/onvif/discover/{ip}", h.handleONVIFDeviceDetail)
	r.Post("/api/onvif/probe", h.handleONVIFProbe)
}
