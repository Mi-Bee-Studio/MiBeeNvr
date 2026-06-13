package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"log/slog"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/go-chi/chi/v5"
)

// --- ONVIF camera management endpoints ---

func (h *Handler) handleONVIFCameraProfiles(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	client, err := h.camMgr.GetONVIFClient(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}

	profiles, err := client.GetProfiles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get profiles: %v", err))
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
		writeError(w, http.StatusInternalServerError, "camera manager not available")
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
		writeError(w, http.StatusBadRequest, "host is required")
		return
	}
	if !validateIP(req.Host) {
		writeError(w, http.StatusBadRequest, "invalid IP address format")
		return
	}
	if req.Port <= 0 {
		req.Port = 80
	}
	if req.Timeout <= 0 {
		req.Timeout = 5
	}
	if req.Timeout > 30 {
		writeError(w, http.StatusBadRequest, "timeout must be between 1 and 30 seconds")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.Timeout)*time.Second)
	defer cancel()

	device, err := onvif.ProbeDevice(ctx, req.Host, req.Port, time.Duration(req.Timeout)*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("probe failed: %v", err))
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
		writeError(w, http.StatusBadRequest, "timeout must be between 1 and 30 seconds")
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
		writeError(w, http.StatusBadRequest, "IP address is required")
		return
	}
	if !validateIP(ip) {
		writeError(w, http.StatusBadRequest, "invalid IP address format")
		return
	}
	ctx := r.Context()
	client := onvif.NewClient(fmt.Sprintf("http://%s/onvif/device_service", ip), "", "")
	if err := client.Connect(ctx); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to connect to device: %v", err))
		return
	}
	info, err := client.GetDeviceInformation(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to get device info: %v", err))
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
		writeError(w, http.StatusNotFound, "camera not found")
		return false
	}
	cameraID := chi.URLParam(r, "id")
	camera, err := h.db.GetCamera(r.Context(), cameraID)
	if err != nil || camera == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return false
	}
	if camera.Protocol != "onvif" {
		writeError(w, http.StatusBadRequest, "PTZ control is only available for ONVIF cameras")
		return false
	}
	return true
}

// --- PTZ control endpoints ---

func (h *Handler) handlePTZMove(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	var req struct {
		Mode string  `json:"mode"`
		Pan  float64 `json:"pan"`
		Tilt float64 `json:"tilt"`
		Zoom float64 `json:"zoom"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Mode != "continuous" && req.Mode != "absolute" && req.Mode != "relative" {
		writeError(w, http.StatusBadRequest, "mode must be continuous, absolute, or relative")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
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
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("PTZ command failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handlePTZStop(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	if err := ptz.Stop(r.Context(), true, true); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("PTZ stop failed: %v", err))
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
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	pos, moving, err := ptz.GetStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get PTZ status failed: %v", err))
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
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	presets, err := ptz.GetPresets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get PTZ presets failed: %v", err))
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
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	token, err := ptz.SetPreset(r.Context(), req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("create PTZ preset failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (h *Handler) handlePTZGoToPreset(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	token := chi.URLParam(r, "token")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	if err := ptz.GoToPreset(r.Context(), token); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("go to PTZ preset failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handlePTZDeletePreset(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	token := chi.URLParam(r, "token")
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	if err := ptz.RemovePreset(r.Context(), token); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("delete PTZ preset failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("PTZ operation failed for camera %q: %v", cameraID, err))
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
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	provider, err := h.camMgr.GetSnapshotProvider(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	uri, err := provider.GetSnapshotUri(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get snapshot URI failed: %v", err))
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
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	img, err := h.camMgr.GetImagingController(r.Context(), cameraID)
	if err != nil {
		handleONVIFImagingError(w, cameraID, err)
		return
	}
	settings, err := img.GetImagingSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("get imaging settings failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// handleImagingSetSettings applies imaging parameter changes for a camera.
func (h *Handler) handleImagingSetSettings(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	var req onvif.ImagingSettings
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	img, err := h.camMgr.GetImagingController(r.Context(), cameraID)
	if err != nil {
		handleONVIFImagingError(w, cameraID, err)
		return
	}
	if err := img.SetImagingSettings(r.Context(), req); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("set imaging settings failed: %v", err))
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
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	img, err := h.camMgr.GetImagingController(r.Context(), cameraID)
	if err != nil {
		handleONVIFImagingError(w, cameraID, err)
		return
	}
	options, err := img.GetImagingOptions(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("get imaging options failed: %v", err))
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
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("imaging operation failed for camera %q: %v", cameraID, err))
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
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	if err := dm.SystemReboot(r.Context()); err != nil {
		if errors.Is(err, onvif.ErrUnsupported) {
			writeError(w, http.StatusNotImplemented, "reboot not supported by device")
			return
		}
		writeError(w, http.StatusBadGateway, fmt.Sprintf("reboot failed: %v", err))
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
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	ifaces, err := dm.GetNetworkInterfaces(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("get network interfaces failed: %v", err))
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
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	if err := dm.SetNetworkInterfaces(r.Context(), req.Interfaces); err != nil {
		if errors.Is(err, onvif.ErrUnsupported) {
			writeError(w, http.StatusNotImplemented, "set network interfaces not supported by device")
			return
		}
		writeError(w, http.StatusBadGateway, fmt.Sprintf("set network interfaces failed: %v", err))
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
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	users, err := dm.GetUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("get users failed: %v", err))
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
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	if err := dm.CreateUsers(r.Context(), req.Users); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("create users failed: %v", err))
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
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	if err := dm.DeleteUsers(r.Context(), req.Usernames); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("delete users failed: %v", err))
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
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.requireONVIF(w, r) {
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	dm, err := h.camMgr.GetDeviceManager(r.Context(), cameraID)
	if err != nil {
		handleONVIFDeviceMgmtError(w, cameraID, err)
		return
	}
	if err := dm.SetUser(r.Context(), username, req.Password); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("set user failed: %v", err))
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
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("device management operation failed for camera %q: %v", cameraID, err))
	}
}
