package api


import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// --- Xiaomi cloud endpoints ---

func (h *Handler) handleXiaomiAuth(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		writeError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Region   string `json:"region,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	region := req.Region
	if region == "" {
		region = "cn"
	}

	result, verification, err := h.cloudProxy.SignIn(r.Context(), req.Username, req.Password, region)
	if err != nil {
		writeError(w, http.StatusUnauthorized, fmt.Sprintf("authentication failed: %v", err))
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
		writeError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	var req struct {
		SessionID   string `json:"session_id"`
		CaptchaCode string `json:"captcha_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" || req.CaptchaCode == "" {
		writeError(w, http.StatusBadRequest, "session_id and captcha_code are required")
		return
	}

	result, verification, err := h.cloudProxy.SubmitCaptcha(r.Context(), req.SessionID, req.CaptchaCode)
	if err != nil {
		writeError(w, http.StatusUnauthorized, fmt.Sprintf("captcha verification failed: %v", err))
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
		writeError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	var req struct {
		SessionID string `json:"session_id"`
		Ticket    string `json:"ticket"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" || req.Ticket == "" {
		writeError(w, http.StatusBadRequest, "session_id and ticket are required")
		return
	}

	result, verification, err := h.cloudProxy.SubmitVerify(r.Context(), req.SessionID, req.Ticket)
	if err != nil {
		writeError(w, http.StatusUnauthorized, fmt.Sprintf("verification failed: %v", err))
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
		writeError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
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
		writeAPIError(w, http.StatusUnauthorized, &model.AuthFailedError{Reason: err.Error()})
	} else {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to get devices: %v", err))
	}
	return

	writeJSON(w, http.StatusOK, map[string]any{
		"devices": devices,
	})
}

// handleXiaomiSync syncs Xiaomi cloud device info (name, model, MAC) to NVR camera config.
// It fetches all devices from Xiaomi cloud, matches them to existing NVR cameras by DID,
// and updates name, model, brand, and serial_number (MAC). Returns count of synced cameras.
func (h *Handler) handleXiaomiSync(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		writeError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}
	if h.config == nil || h.config.Xiaomi.Token == "" {
		writeError(w, http.StatusUnauthorized, "xiaomi cloud not authenticated")
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	devices, err := h.cloudProxy.ListDevices(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to get devices: %v", err))
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
