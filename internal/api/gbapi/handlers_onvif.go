package gbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/httpx"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/go-chi/chi/v5"
)

// --- ONVIF camera management endpoints ---

func (s *server) handleONVIFCameraProfiles(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !s.requireONVIF(w, r) {
		return
	}
	if s.deps.Cam == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	client, err := s.deps.Cam.GetONVIFClient(r.Context(), cameraID)
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

func (s *server) handleONVIFCapabilities(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !s.requireONVIF(w, r) {
		return
	}
	if s.deps.Cam == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	client, err := s.deps.Cam.GetONVIFClient(r.Context(), cameraID)
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
	deviceInfo := s.deps.Cam.GetCachedDeviceInfo(r.Context(), cameraID)
	detailed.DeviceInfo = deviceInfo

	writeJSON(w, http.StatusOK, detailed)
}

// --- ONVIF discovery endpoints ---

// handleONVIFProbe probes a single ONVIF device by sending a WS-Discovery
// probe via HTTP POST directly to host:port (no multicast needed).
func (s *server) handleONVIFProbe(w http.ResponseWriter, r *http.Request) {
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
	if !httpx.ValidateIP(req.Host) {
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
	if device == nil {
		// Reachable (the TCP pre-check inside ProbeDevice passed) but no
		// ONVIF answer: non-ONVIF server, auth-walled device, or firmware
		// that answers the probe envelopes with empty/404 bodies. A bare
		// device:null was indistinguishable from offline in the UI (#1125
		// suggestion 3) — carry a structured reason the frontend maps to a
		// specific message ("still try adding via the endpoint URL").
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"device": nil,
			"reason": "no_onvif_response",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"device": device,
	})
}

func (s *server) handleONVIFDiscover(w http.ResponseWriter, r *http.Request) {
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

func (s *server) handleONVIFDeviceDetail(w http.ResponseWriter, r *http.Request) {
	ip := chi.URLParam(r, "ip")
	if ip == "" {
		WriteError(w, http.StatusBadRequest, "IP address is required")
		return
	}
	if !httpx.ValidateIP(ip) {
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

func (s *server) requireONVIF(w http.ResponseWriter, r *http.Request) bool {
	if s.deps.DB == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return false
	}
	cameraID := chi.URLParam(r, "id")
	camera, err := s.deps.DB.GetCamera(r.Context(), cameraID)
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
func (s *server) cameraProtocol(r *http.Request, cameraID string) string {
	if s.deps.DB == nil {
		return ""
	}
	camera, err := s.deps.DB.GetCamera(r.Context(), cameraID)
	if err != nil || camera == nil {
		return ""
	}
	return camera.Protocol
}
