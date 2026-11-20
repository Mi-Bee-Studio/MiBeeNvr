package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/health"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/license"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// StabilityProvider provides camera stability quality data.
type StabilityProvider interface {
	GetAllStability() map[string]*health.StabilityData
	GetStability(cameraID string) *health.StabilityData
}

// HealthManager provides camera health status data.
type HealthManager interface {
	GetAllHealth() map[string]*model.CameraHealth
	GetCameraHealth(cameraID string) *model.CameraHealth
}

// handleGetHealthStatus returns the current health status for all monitored cameras.
func (h *Handler) handleGetHealthStatus(w http.ResponseWriter, r *http.Request) {
	if h.healthMgr == nil {
		writeJSON(w, http.StatusOK, map[string]*model.CameraHealth{})
		return
	}
	health := h.healthMgr.GetAllHealth()
	if health == nil {
		health = map[string]*model.CameraHealth{}
	}
	writeJSON(w, http.StatusOK, health)
}

// handleGetHealthEvents returns paginated health events from the database.
func (h *Handler) handleGetHealthEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := storage.HealthEventsFilter{
		CameraID:  q.Get("camera_id"),
		EventType: q.Get("event_type"),
		Since:     q.Get("since"),
	}

	if v := q.Get("limit"); v != "" {
		limit, err := strconv.Atoi(v)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "invalid limit parameter")
			return
		}
		filter.Limit = limit
	}

	if v := q.Get("offset"); v != "" {
		offset, err := strconv.Atoi(v)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "invalid offset parameter")
			return
		}
		filter.Offset = offset
	}

	events, total, err := h.db.ListHealthEvents(r.Context(), filter)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list health events")
		return
	}

	if events == nil {
		events = []model.HealthEvent{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"events": events,
		"total":  total,
	})
}

// handleGetCameraHealth returns the health status for a single camera.
// handleGetStability returns stability quality data for all cameras.
func (h *Handler) handleGetStability(w http.ResponseWriter, r *http.Request) {
	if h.stabilityProvider == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"cameras": map[string]*health.StabilityData{}})
		return
	}
	all := h.stabilityProvider.GetAllStability()
	if all == nil {
		all = map[string]*health.StabilityData{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"cameras": all})
}

// handleGetCameraStability returns stability quality data for a single camera.
func (h *Handler) handleGetCameraStability(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "missing camera id")
		return
	}

	if h.stabilityProvider == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	data := h.stabilityProvider.GetStability(cameraID)
	if data == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	writeJSON(w, http.StatusOK, data)
}

// registerHealthRoutes registers health monitoring status/events/stability routes.
func (h *Handler) registerHealthRoutes(r chi.Router) {
	r.Get("/api/health/status", h.handleGetHealthStatus)
	r.Get("/api/health/events", h.handleGetHealthEvents)
	r.Get("/api/health/stability", h.handleGetStability)
	r.Get("/api/health/stability/{id}", h.handleGetCameraStability)
}

// Default goroutine tripwire coefficients — observed workload (recorder +
// streamhub + reconnect loops ≈ 85 goroutines per recording camera; M5
// 16-camera fleet idles at ~1365). The line sits at roughly 2x steady
// state: small boxes keep their sensitivity, a runaway leak still crosses.
// Operators tune both via health.goroutine_baseline / goroutine_per_camera
// (heavier per-camera workloads: sub-stream consumers, cascade, relays).
const (
	defaultGoroutineBaseline  = 300
	defaultGoroutinePerCamera = 150
)

// resolveGoroutinePolicy returns the effective coefficients, falling back
// to the defaults when config is absent or the values were left at zero
// (zero must not collapse the rail to nothing).
func resolveGoroutinePolicy(cfg *config.Config) (baseline, perCamera int) {
	baseline, perCamera = defaultGoroutineBaseline, defaultGoroutinePerCamera
	if cfg == nil {
		return
	}
	if cfg.Health.GoroutineBaseline > 0 {
		baseline = cfg.Health.GoroutineBaseline
	}
	if cfg.Health.GoroutinePerCamera > 0 {
		perCamera = cfg.Health.GoroutinePerCamera
	}
	return
}

// goroutineCheckStatus maps a goroutine count to the health-check verdict,
// scaling the tripwire with the camera fleet: production shows ~85
// goroutines per recording camera, so a fixed 1000 flagged every >=12-camera
// box as permanently unhealthy (M5: 16 cameras idle at ~1365; the Docker
// HEALTHCHECK inherits the verdict). threshold = baseline + perCamera×N.
func goroutineCheckStatus(count, numCameras, baseline, perCamera int) (status, message string) {
	if numCameras < 0 {
		numCameras = 0
	}
	if baseline <= 0 {
		baseline = defaultGoroutineBaseline
	}
	if perCamera <= 0 {
		perCamera = defaultGoroutinePerCamera
	}
	threshold := baseline + perCamera*numCameras
	if count > threshold {
		return "error", fmt.Sprintf("%d goroutines (threshold: %d, cameras: %d)", count, threshold, numCameras)
	}
	return "ok", fmt.Sprintf("%d goroutines (threshold: %d, cameras: %d)", count, threshold, numCameras)
}

// The hard cap is a runaway-leak backstop; production fleets sit far below
// it (an 18-camera box measured ~390). Raised from 5000: the API unit-test
// binary itself crosses 5000 goroutines at peak parallelism on CI runners
// (observed 2026-11-10 as recurring TestReadyz_OK flakes on unrelated
// branches — the test process, not the product, was tripping the wire).
const goroutineHardFail = 10000

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{Checks: make(map[string]HealthCheck)}
	hasWarning, hasError := false, false

	// Database check
	if h.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		err := h.db.DB().PingContext(ctx)
		if err != nil {
			resp.Checks["database"] = HealthCheck{Status: "error", Message: err.Error()}
			hasError = true
		} else {
			resp.Checks["database"] = HealthCheck{Status: "ok"}
		}
	} else {
		resp.Checks["database"] = HealthCheck{Status: "error", Message: "database not configured"}
		hasError = true
	}

	// Storage check — combines disk usage with I/O health state.
	if h.store != nil {
		total, used, err := h.store.GetDiskUsage()
		if err != nil {
			resp.Checks["storage"] = HealthCheck{Status: "error", Message: err.Error()}
			hasError = true
		} else {
			pct := 0
			if total > 0 {
				pct = int(float64(used) / float64(total) * 100)
			}
			msg := fmt.Sprintf("%d%% used (%d / %d bytes)", pct, used, total)

			// Check I/O health state — per-camera health aggregated.
			storageFailed := h.store.StorageFailedLegacy()
			var healthMsg string
			if storageFailed {
				healthMsg = " I/O errors detected — some cameras may fail to record"
				hasError = true
			}

			if storageFailed {
				resp.Checks["storage"] = HealthCheck{Status: "error", Message: msg + healthMsg}
			} else if pct > 95 {
				resp.Checks["storage"] = HealthCheck{Status: "error", Message: msg}
				hasError = true
			} else if pct > 90 {
				resp.Checks["storage"] = HealthCheck{Status: "warning", Message: msg}
				hasWarning = true
			} else {
				resp.Checks["storage"] = HealthCheck{Status: "ok", Message: msg}
			}
		}
	} else {
		resp.Checks["storage"] = HealthCheck{Status: "error", Message: "storage not configured"}
		hasError = true
	}
	// Camera health aggregation (computed first: the goroutine tripwire
	// scales with the fleet size).
	var camHealth *CameraHealthSummary
	if h.healthMgr != nil {
		camHealth = h.aggregateCameraHealth(r)
		// The per-camera detail stays behind auth (/api/health/cameras,
		// #879): a public endpoint must not leak the camera fleet's names
		// and topology. camHealth is still computed for the goroutine
		// tripwire scaling below — and projected into the response as
		// bare counters (HealthCameraCounts), which old LAN clients
		// (MiBeeScout discovery/manual connect) require to identify the
		// NVR (#899).
		if camHealth != nil {
			resp.Cameras = &HealthCameraCounts{
				Total:        camHealth.Total,
				Recording:    camHealth.Recording,
				Reconnecting: camHealth.Reconnecting,
				Error:        camHealth.Error,
				Offline:      camHealth.Offline,
			}
		}
	}

	// Goroutine check — threshold scales with the camera fleet.
	numCameras := 0
	if camHealth != nil {
		numCameras = camHealth.Total
	}
	baseline, perCamera := resolveGoroutinePolicy(h.config)
	gStatus, gMessage := goroutineCheckStatus(runtime.NumGoroutine(), numCameras, baseline, perCamera)
	resp.Checks["goroutines"] = HealthCheck{Status: gStatus, Message: gMessage}
	if gStatus == "error" {
		hasError = true
	}

	if h.healthMgr != nil {
		if camHealth != nil {
			if camHealth.Error > 0 {
				hasWarning = true // any camera in error = degraded
			}
			if camHealth.Reconnecting > 0 {
				hasWarning = true // any reconnecting = degraded
			}
			if camHealth.Total > 0 && camHealth.Offline > camHealth.Total/2 {
				hasError = true // majority offline = error
			}
		}
	}

	// License gate summary (#49): only when licensing is enforced — a
	// non-required deployment must not advertise license machinery, and the
	// incident shape (doc wiped, required stays) still shows up as
	// state=none/allowed=false. A blocking or soon-expiring gate degrades
	// overall status: core NVR duty (recording) is unaffected, but remote
	// access is down and operators must see it on the standard probe.
	if h.licenseMgr != nil {
		if st := h.licenseMgr.Status(); st.Required {
			sum := &HealthLicense{
				State:       string(st.State),
				Required:    st.Required,
				Allowed:     st.Allowed,
				TimeLeftSec: st.TimeLeftSec,
			}
			if st.Document != nil && !st.Document.ExpiresAt.IsZero() {
				sum.ExpiresAt = st.Document.ExpiresAt.UTC().Format(time.RFC3339)
			}
			resp.License = sum
			switch {
			case !st.Allowed:
				resp.Checks["license"] = HealthCheck{Status: "warning", Message: "license gate blocking gated capabilities (e.g. remote access): " + string(st.State)}
				hasWarning = true
			case st.State == license.StateExpiringSoon:
				resp.Checks["license"] = HealthCheck{Status: "warning", Message: fmt.Sprintf("license expiring soon (%ds left)", st.TimeLeftSec)}
				hasWarning = true
			default:
				resp.Checks["license"] = HealthCheck{Status: "ok"}
			}
		}
	}

	// Overall status
	switch {
	case hasError:
		resp.Status = "unhealthy"
	case hasWarning:
		resp.Status = "degraded"
	default:
		resp.Status = "ok"
	}

	// Uptime
	resp.Uptime = formatUptime(time.Since(appStartTime))

	// SetupRequired — true when no password is configured
	resp.SetupRequired = h.config != nil && h.config.Auth.PasswordHash == "" && h.config.Auth.Password == ""

	// LocalAccess — true only when the request is genuinely from a browser on
	// the NVR host machine: a loopback connection with a loopback Host header,
	// no proxy/gateway headers, AND the operator enabled auth.local_bypass.
	// Reuses middleware.IsBypassEligible so this cannot drift from the auth
	// middleware's own gate. Kept strict for reverse-proxy and Docker published-
	// port deployments, where every request arrives from 127.0.0.1 and would
	// otherwise be misreported as local (and the frontend would skip the login
	// page for remote users).
	localBypass := h.config != nil && h.config.Auth.LocalBypass != nil && *h.config.Auth.LocalBypass
	resp.LocalAccess = localBypass && middleware.IsBypassEligible(r)

	// Gateway — whether this request carried a unified-gateway-verified NAS
	// identity (context only ever set on the gateway listener). The frontend
	// suppresses its Authorization header exactly when this is true: the
	// gateway claims any Authorization header as its own credential, while
	// direct access to the same listener requires the Bearer token (#938).
	if gi := middleware.GatewayIdentityFromContext(r.Context()); gi != nil {
		resp.Gateway = true
		// GatewayAdmin — the identity is specifically an ADMIN one. The setup
		// wizard hides its first-boot code field for these sessions: the
		// gateway-admin path is exempt from the code (#1032), so asking for
		// one would send NAS admins hunting for a code they don't need.
		resp.GatewayAdmin = gi.Admin
	}

	// Stable device identity for LAN clients (#330)
	if h.config != nil {
		resp.DeviceID = h.config.Server.DeviceID
		resp.DeviceName = h.config.Server.DeviceName
		resp.NotifyReceivers = notifyReceiverOrigins(h.config.Notifications)
	}
	writeJSON(w, http.StatusOK, resp)
}

// notifyReceiverOrigins extracts the deduplicated origins
// (scheme://host:port) of the configured notification sinks. The sink path
// and token never enter the result: this feeds the PUBLIC /api/health
// payload, which is opt-in via notifications.expose_origin (#1121). Sinks
// whose URL fails to parse into a usable origin are skipped; returns nil
// when exposure is off or nothing usable remains.
func notifyReceiverOrigins(nc config.NotificationsConfig) []string {
	if !nc.ExposeOrigin || len(nc.Sinks) == 0 {
		return nil
	}

	var origins []string
	seen := make(map[string]bool, len(nc.Sinks))
	for _, sink := range nc.Sinks {
		u, err := url.Parse(sink.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		origin := u.Scheme + "://" + u.Host
		if seen[origin] {
			continue
		}
		seen[origin] = true
		origins = append(origins, origin)
	}
	return origins
}

// aggregateCameraHealth builds a CameraHealthSummary from the health manager and camera DB.

// aggregateCameraHealth builds a CameraHealthSummary from the health manager and camera DB.
func (h *Handler) aggregateCameraHealth(r *http.Request) *CameraHealthSummary {
	allHealth := h.healthMgr.GetAllHealth()
	if allHealth == nil {
		allHealth = map[string]*model.CameraHealth{}
	}

	// Active-camera lookup from DB. ListCameras excludes archived rows, so it
	// doubles as the filter that keeps retired cameras out of the aggregate:
	// an archived camera (e.g. the auto-enrolled device-self pseudo-channel
	// camera retired once its catalog arrived, #416) keeps its last health
	// entry forever and would pin the system degraded eternally (#420). When
	// the lookup is unavailable (no DB / query error) we count everything
	// rather than reporting an empty system.
	var active map[string]string // nil = unknown, count all
	if h.db != nil {
		if cameras, err := h.db.ListCameras(r.Context()); err == nil {
			active = make(map[string]string, len(cameras))
			for _, c := range cameras {
				active[c.ID] = c.Name
			}
		}
	}

	summary := &CameraHealthSummary{}
	for id, ch := range allHealth {
		if active != nil {
			if _, ok := active[id]; !ok {
				continue
			}
		}
		detail := CameraHealthDetail{
			ID:     id,
			Name:   active[id],
			Score:  ch.Score,
			Status: ch.LatestStatus,
		}
		summary.Details = append(summary.Details, detail)

		switch ch.LatestStatus {
		case "healthy", "recording":
			summary.Recording++
		case "reconnecting":
			summary.Reconnecting++
		case "error", "unhealthy":
			summary.Error++
		default:
			summary.Offline++
		}
	}
	summary.Total = len(summary.Details)

	return summary
}

// handleHealthCameras returns the full camera health map with scores.
// Behind the auth middleware (#879): camera names/status are fleet topology,
// not liveness data.
func (h *Handler) handleHealthCameras(w http.ResponseWriter, r *http.Request) {
	if h.healthMgr == nil {
		writeJSON(w, http.StatusOK, map[string]*model.CameraHealth{})
		return
	}
	health := h.healthMgr.GetAllHealth()
	if health == nil {
		health = map[string]*model.CameraHealth{}
	}
	writeJSON(w, http.StatusOK, health)
}

func (h *Handler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	checks := make(map[string]HealthCheck)

	// Database must be ok
	allOK := true
	if h.db == nil {
		checks["database"] = HealthCheck{Status: "error", Message: "database not configured"}
		allOK = false
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.db.DB().PingContext(ctx); err != nil {
			checks["database"] = HealthCheck{Status: "error", Message: err.Error()}
			allOK = false
		} else {
			checks["database"] = HealthCheck{Status: "ok"}
		}
	}

	// Storage must be < 95%
	if h.store == nil {
		checks["storage"] = HealthCheck{Status: "error", Message: "storage not configured"}
		allOK = false
	} else {
		total, used, err := h.store.GetDiskUsage()
		if err != nil {
			checks["storage"] = HealthCheck{Status: "error", Message: err.Error()}
			allOK = false
		} else {
			pct := 0
			if total > 0 {
				pct = int(float64(used) / float64(total) * 100)
			}
			if pct >= 95 {
				checks["storage"] = HealthCheck{Status: "error", Message: fmt.Sprintf("%d%% used (threshold: 95%%)", pct)}
				allOK = false
			} else {
				checks["storage"] = HealthCheck{Status: "ok"}
			}
		}
	}

	// Goroutines must be < 5000 — deliberately NOT the fleet-scaled
	// tripwire from /api/health: readiness answers "is the process
	// fundamentally broken", where transient stream-setup spikes must not
	// flap the probe.
	numGoroutines := runtime.NumGoroutine()
	if numGoroutines >= goroutineHardFail {
		checks["goroutines"] = HealthCheck{Status: "error", Message: fmt.Sprintf("%d goroutines (threshold: %d)", numGoroutines, goroutineHardFail)}
		allOK = false
	} else {
		checks["goroutines"] = HealthCheck{Status: "ok"}
	}

	if allOK {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	} else {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not ready", "checks": checks})
	}
}
