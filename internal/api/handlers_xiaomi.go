package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// --- Xiaomi cloud endpoints ---

func (h *Handler) handleXiaomiAuth(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		WriteError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Region   string `json:"region,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		WriteError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	region := req.Region
	if region == "" {
		region = "cn"
	}

	result, verification, err := h.cloudProxy.SignIn(r.Context(), req.Username, req.Password, region)
	if err != nil {
		WriteError(w, http.StatusUnauthorized, fmt.Sprintf("authentication failed: %v", err))
		return
	}

	if verification != nil {
		writeJSON(w, http.StatusAccepted, verificationToResponse(verification))
		return
	}

	// Store token in config
	h.saveXiaomiToken(result)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"user_id": result.UserID,
	})
}

func (h *Handler) handleXiaomiCaptcha(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		WriteError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	var req struct {
		SessionID   string `json:"session_id"`
		CaptchaCode string `json:"captcha_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" || req.CaptchaCode == "" {
		WriteError(w, http.StatusBadRequest, "session_id and captcha_code are required")
		return
	}

	result, verification, err := h.cloudProxy.SubmitCaptcha(r.Context(), req.SessionID, req.CaptchaCode)
	if err != nil {
		WriteError(w, http.StatusUnauthorized, fmt.Sprintf("captcha verification failed: %v", err))
		return
	}

	if verification != nil {
		writeJSON(w, http.StatusAccepted, verificationToResponse(verification))
		return
	}

	// Store token in config
	h.saveXiaomiToken(result)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"user_id": result.UserID,
	})
}

func (h *Handler) handleXiaomiVerify(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		WriteError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	var req struct {
		SessionID string `json:"session_id"`
		Ticket    string `json:"ticket"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" || req.Ticket == "" {
		WriteError(w, http.StatusBadRequest, "session_id and ticket are required")
		return
	}

	result, verification, err := h.cloudProxy.SubmitVerify(r.Context(), req.SessionID, req.Ticket)
	if err != nil {
		WriteError(w, http.StatusUnauthorized, fmt.Sprintf("verification failed: %v", err))
		return
	}

	if verification != nil {
		writeJSON(w, http.StatusAccepted, verificationToResponse(verification))
		return
	}

	// Store token in config
	h.saveXiaomiToken(result)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"user_id": result.UserID,
	})
}

func (h *Handler) handleXiaomiDevices(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		WriteError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	// Get stored token from config
	if h.config == nil || h.config.Xiaomi.Token == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"devices": []CloudDeviceInfo{},
			"message": "not authenticated",
		})
		return
	}

	devices, err := h.cloudProxy.ListDevices(r.Context())
	if err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("failed to get devices: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"devices": devices,
	})
}

// handleXiaomiSync syncs Xiaomi cloud device info (name, model, MAC) to NVR camera config.
// It fetches all devices from Xiaomi cloud, matches them to existing NVR cameras by DID,
// and updates name, model, brand, and serial_number (MAC). Returns count of synced cameras.
func (h *Handler) handleXiaomiSync(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		WriteError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}
	if h.config == nil || h.config.Xiaomi.Token == "" {
		WriteError(w, http.StatusUnauthorized, "xiaomi cloud not authenticated")
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	devices, err := h.cloudProxy.ListDevices(r.Context())
	if err != nil {
		WriteError(w, http.StatusBadGateway, fmt.Sprintf("failed to get devices: %v", err))
		return
	}

	// Build DID → CloudDeviceInfo lookup
	deviceByDID := make(map[string]*CloudDeviceInfo, len(devices))
	for i := range devices {
		deviceByDID[devices[i].DID] = &devices[i]
	}

	synced := 0
	for i := range h.config.Cameras {
		cam := &h.config.Cameras[i]
		if cam.Protocol != "xiaomi" {
			continue
		}

		did := cam.DID
		if did == "" {
			did = extractDIDFromURL(cam.URL)
		}
		if did == "" {
			continue
		}

		dev, ok := deviceByDID[did]
		if !ok {
			continue
		}

		updates := camera.CameraUpdate{
			Name:         &dev.Name,
			Model:        &dev.Model,
			Brand:        strPtr("Xiaomi"),
			SerialNumber: &dev.MAC,
		}

		if _, err := h.camMgr.UpdateCamera(context.Background(), cam.ID, updates); err != nil {
			logger.Warn("failed to sync xiaomi camera", "camera_id", cam.ID, "did", did, "error", err)
			continue
		}
		synced++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"synced": synced,
		"total":  len(deviceByDID),
	})
}

func (h *Handler) handleCheckVendor(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"vendor":     "unknown",
			"compatible": true,
		})
		return
	}
	if h.config == nil || h.config.Xiaomi.Token == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"vendor":     "unknown",
			"compatible": true,
		})
		return
	}

	did := r.URL.Query().Get("did")
	if did == "" {
		WriteError(w, http.StatusBadRequest, "did parameter required")
		return
	}

	vendor, err := h.cloudProxy.CheckVendor(r.Context(), did)
	if err != nil {
		// For errors, return unknown/compatible (don't block on uncertainty)
		writeJSON(w, http.StatusOK, map[string]any{
			"vendor":     "unknown",
			"compatible": true,
		})
		return
	}

	// Both CS2 and TUTK vendors are supported as of v0.9.0 (TUTK transport ported
	// from go2rtc — see internal/tutk/). Only return compatible=false for vendors
	// we genuinely cannot handle. Resolves issue #64: the pre-add gate was still
	// blocking TUTK cameras with a "not supported" message even though recording
	// worked once the user bypassed the warning dialog.
	writeJSON(w, http.StatusOK, map[string]any{
		"vendor":     vendor,
		"compatible": true,
	})
}

// saveXiaomiToken persists auth result to config file.
func (h *Handler) saveXiaomiToken(result *CloudAuthResult) {
	if h.config == nil || result == nil {
		return
	}
	h.config.Xiaomi.UserID = result.UserID
	h.config.Xiaomi.Token = result.PassToken
	h.config.Xiaomi.Region = result.Region
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save xiaomi config", "error", err)
	}

	// Also push to the cloud proxy so it has the latest credentials
	if h.cloudProxy != nil {
		_ = h.cloudProxy.SetCloudConfig(context.Background(), result.UserID, result.PassToken, result.Region)
	}
}

// verificationToResponse converts a CloudVerificationRequired to an API response map.
func verificationToResponse(v *CloudVerificationRequired) map[string]any {
	resp := map[string]any{
		"status": "verification_required",
	}
	if len(v.Captcha) > 0 {
		resp["captcha"] = base64.StdEncoding.EncodeToString(v.Captcha)
	}
	if v.VerifyPhone != "" {
		resp["verify_phone"] = v.VerifyPhone
	}
	if v.VerifyEmail != "" {
		resp["verify_email"] = v.VerifyEmail
	}
	if v.CaptchaSessionID != "" {
		resp["session_id"] = v.CaptchaSessionID
	}
	return resp
}

// registerXiaomiRoutes registers Xiaomi cloud auth, device discovery, and
// two-way audio upstream WebSocket routes.
func (h *Handler) registerXiaomiRoutes(r chi.Router) {
	r.Route("/api/xiaomi", func(r chi.Router) {
		r.Post("/auth", h.handleXiaomiAuth)
		r.Post("/captcha", h.handleXiaomiCaptcha)
		r.Post("/verify", h.handleXiaomiVerify)
		r.Get("/devices", h.handleXiaomiDevices)
		r.Post("/sync", h.handleXiaomiSync)
		r.Get("/check-vendor", h.handleCheckVendor)
	})
	// Xiaomi two-way audio upstream WebSocket
	r.Get("/api/ws/camera/{id}/audio-upstream", h.handleAudioUpstreamWS)
}
