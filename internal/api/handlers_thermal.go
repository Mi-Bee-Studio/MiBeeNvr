package api

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/thermal"
	"github.com/go-chi/chi/v5"
)

// GET /api/cameras/{id}/thermal/frame — spot-temperature data path for the
// dual-spectrum live view (#1092 follow-up): relays the device-side readout
// with the full 32×24 grid (?grid=1) from the thermal camera's configured
// thermal_readout source. The NVR proxies so the browser never needs the
// device address or credentials (and no CORS). Pass-through: the SPA unwraps
// the family envelope the same way the SSE chip data does.
//
// kind: hikvision sources are decoded server-side instead: the ISAPI
// multipart radiometric frame (Digest-authenticated with the camera's own
// credentials) becomes {"grid":{"cols","rows","values"},"max","min",
// "visible_rect"} — the native matrix (160×120 on a DS-2TB2617) plus the
// device's thermal→visible registration for the dual-pane mirror.
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
	src, ok := thermal.SourceForCamera(thermalCameraConfig(h.config, id))
	if !ok {
		WriteError(w, http.StatusNotFound, "no thermal readout configured")
		return
	}
	if src.Kind == thermal.KindHikvision {
		client := thermal.NewHikvisionClient(src.Username, src.Password, 8*time.Second)
		frame, err := thermal.FetchHikvisionFrame(ctx, client, src.URL)
		if err != nil {
			logger.Warn("thermal frame: hikvision readout failed", "camera_id", id, "error", err)
			WriteError(w, http.StatusBadGateway, "readout endpoint error")
			return
		}
		if frame.VisibleRect == nil {
			frame.VisibleRect = src.VisibleRect // configured registration
		}
		writeJSON(w, http.StatusOK, frame.FramePayload())
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

// GET /api/cameras/{id}/thermal/history?from=&to=&points= — the P4 playback
// temperature curve (#38 Tier 2): minute-aggregated scene extremes stored by
// the thermal-history service, decimated to at most `points` buckets for
// charting. Thermal cameras only (same config gate as the frame proxy).
func (h *Handler) handleThermalHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	if !thermalCameraIs(h.config, id) {
		WriteError(w, http.StatusNotFound, "not a thermal camera")
		return
	}

	to := time.Now().UTC()
	from := to.Add(-24 * time.Hour)
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "from must be RFC3339")
			return
		}
		from = t.UTC()
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "to must be RFC3339")
			return
		}
		to = t.UTC()
	}
	if !from.Before(to) {
		WriteError(w, http.StatusBadRequest, "from must be before to")
		return
	}
	points := 720
	if v := r.URL.Query().Get("points"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 2000 {
			WriteError(w, http.StatusBadRequest, "points must be 1..2000")
			return
		}
		points = n
	}

	rows, err := h.db.QueryThermalReadings(ctx, id, from, to)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to query thermal history")
		return
	}
	pts := decimateThermalPoints(rows, points)
	writeJSON(w, http.StatusOK, map[string]any{
		"camera_id": id,
		"from":      from.Format(time.RFC3339),
		"to":        to.Format(time.RFC3339),
		"points":    pts,
	})
}

// decimateThermalPoints folds the minute rows into at most n chart buckets,
// keeping the extremes within each bucket (a hot spike spanning <1px of chart
// width must survive). Empty input decimates to empty.
func decimateThermalPoints(rows []storage.ThermalPoint, n int) []storage.ThermalPoint {
	if len(rows) == 0 || n <= 0 {
		return []storage.ThermalPoint{}
	}
	if len(rows) <= n {
		return rows
	}
	out := make([]storage.ThermalPoint, 0, n)
	step := float64(len(rows)) / float64(n)
	for i := range n {
		lo := int(float64(i) * step)
		hi := int(float64(i+1) * step)
		if i == n-1 || hi > len(rows) {
			hi = len(rows)
		}
		if lo >= hi {
			lo = hi - 1
		}
		b := storage.ThermalPoint{Minute: rows[lo].Minute}
		for _, p := range rows[lo:hi] {
			if p.MaxC != nil && (b.MaxC == nil || *p.MaxC > *b.MaxC) {
				v := *p.MaxC
				b.MaxC = &v
			}
			if p.MinC != nil && (b.MinC == nil || *p.MinC < *b.MinC) {
				v := *p.MinC
				b.MinC = &v
			}
		}
		out = append(out, b)
	}
	return out
}

// thermalCameraConfig finds a camera's yaml config (the source of truth for
// imaging_type and thermal_readout — the DB row never carries them), the
// same config the boot-wired poller resolves its sources from.
func thermalCameraConfig(cfg *config.Config, cameraID string) *config.CameraConfig {
	if cfg == nil {
		return nil
	}
	for i := range cfg.Cameras {
		if cfg.Cameras[i].ID == cameraID {
			return &cfg.Cameras[i]
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
