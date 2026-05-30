package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
		caps = &onvif.DeviceCapabilities{}
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

	caps, err := client.GetCapabilities(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get capabilities: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, onvif.DeviceCapabilitiesDetailed{
		PTZ:       caps.PTZ,
		Streaming: caps.Streaming,
	})
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
