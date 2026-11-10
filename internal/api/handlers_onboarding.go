package api

// Zero-touch camera onboarding API (#1120). The service is optional — the
// handler answers 409 when the feature is disabled (default) or not wired.

import (
	"encoding/json"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onboarding"
	"github.com/go-chi/chi/v5"
)

// OnboardingService is the surface the API needs from the onboarding
// manager (nil = feature not wired).
type OnboardingService interface {
	Status() onboarding.Status
	StartScanJob() (string, error)
	StartAdoptJob(req onboarding.AdoptRequest) (string, error)
	Job(id string) (onboarding.Job, bool)
}

func (h *Handler) SetOnboardingService(s OnboardingService) {
	h.onboarding = s
}

// onboardingReady answers with 409 when the feature is unavailable.
func (h *Handler) onboardingReady(w http.ResponseWriter) *OnboardingService {
	if h.onboarding == nil {
		WriteError(w, http.StatusConflict, "camera onboarding is not enabled (camera_onboarding.enabled)")
		return nil
	}
	return &h.onboarding
}

func (h *Handler) handleOnboardingStatus(w http.ResponseWriter, r *http.Request) {
	svc := h.onboardingReady(w)
	if svc == nil {
		return
	}
	writeJSON(w, http.StatusOK, (*svc).Status())
}

func (h *Handler) handleOnboardingScan(w http.ResponseWriter, r *http.Request) {
	svc := h.onboardingReady(w)
	if svc == nil {
		return
	}
	id, err := (*svc).StartScanJob()
	if err != nil {
		WriteError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": id})
}

func (h *Handler) handleOnboardingAdopt(w http.ResponseWriter, r *http.Request) {
	svc := h.onboardingReady(w)
	if svc == nil {
		return
	}
	var req onboarding.AdoptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid adopt request body")
		return
	}
	if req.IP == "" {
		WriteError(w, http.StatusBadRequest, "ip is required")
		return
	}
	id, err := (*svc).StartAdoptJob(req)
	if err != nil {
		WriteError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": id})
}

func (h *Handler) handleOnboardingJob(w http.ResponseWriter, r *http.Request) {
	svc := h.onboardingReady(w)
	if svc == nil {
		return
	}
	id := chi.URLParam(r, "id")
	job, ok := (*svc).Job(id)
	if !ok {
		WriteError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// registerOnboardingRoutes registers the zero-touch onboarding endpoints.
// The handlers answer 409 when the feature is disabled — the routes exist
// so the UI can probe capability, but nothing runs without the config.
func (h *Handler) registerOnboardingRoutes(r chi.Router) {
	r.Get("/api/onboarding/status", h.handleOnboardingStatus)
	r.Post("/api/onboarding/scan", h.handleOnboardingScan)
	r.Post("/api/onboarding/adopt", h.handleOnboardingAdopt)
	r.Get("/api/onboarding/jobs/{id}", h.handleOnboardingJob)
}
