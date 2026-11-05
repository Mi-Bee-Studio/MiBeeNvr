package api

import (
	"io"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/go-chi/chi/v5"
)

// GET /api/cameras/{id}/thermal/frame — spot-temperature data path for the
// dual-spectrum live view (#1092 follow-up): relays the device-side readout
// with the full 32×24 grid (?grid=1) from the thermal camera's configured
// thermal_readout source. The NVR proxies so the browser never needs the
// device address or credentials (and no CORS). Pass-through: the SPA unwraps
// the family envelope the same way the SSE chip data does.
func (h *Handler) handleThermalFrame(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	row, err := h.db.GetCamera(ctx, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if row == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}
	// imaging_type lives in the yaml config (the DB row never carries it),
	// so both the thermal gate and the readout source read from there —
	// the same source of truth the boot-wired poller uses.
	src := thermalReadoutOf(h.config, id)
	if src == nil || !thermalCameraIs(h.config, id) {
		WriteError(w, http.StatusNotFound, "no thermal readout configured")
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL+"?grid=1", nil)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad readout url")
		return
	}
	if src.AuthHeader != "" {
		req.Header.Set(src.AuthHeader, src.AuthValue)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "readout endpoint unreachable")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		WriteError(w, http.StatusBadGateway, "readout endpoint error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

// thermalReadoutOf finds the configured readout source for a camera ID
// (yaml-backed, the same source of truth the boot-wired poller reads).
func thermalReadoutOf(cfg *config.Config, cameraID string) *config.ThermalReadoutConfig {
	if cfg == nil {
		return nil
	}
	for i := range cfg.Cameras {
		if cfg.Cameras[i].ID == cameraID {
			return cfg.Cameras[i].ThermalReadout
		}
	}
	return nil
}

// thermalCameraIs reports whether the camera is imaging_type=thermal per config.
func thermalCameraIs(cfg *config.Config, cameraID string) bool {
	if cfg == nil {
		return false
	}
	for i := range cfg.Cameras {
		if cfg.Cameras[i].ID == cameraID {
			return cfg.Cameras[i].ImagingType == config.ImagingTypeThermal
		}
	}
	return false
}
