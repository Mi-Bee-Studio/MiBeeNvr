package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func thermalFrameReq(t *testing.T, id string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/"+id+"/thermal/frame", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return httptest.NewRecorder(), req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// The spot-temperature grid proxy: a thermal camera with a configured
// thermal_readout relays the device's ?grid=1 payload (auth header applied);
// visible channels, unknown cameras, and erroring devices are each refused.
func TestThermalFrameProxy(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "1", r.URL.Query().Get("grid"), "grid variant requested")
		require.Equal(t, "Bearer tok-1", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"max":{"value_c":45.9},"grid":{"values":[28,29,45]}}}`))
	}))
	t.Cleanup(up.Close)

	h := setupGB28181TestHandler(t)
	h.config = &config.Config{Cameras: []config.CameraConfig{
		{
			ID: "gb-hot", Protocol: "gb28181", ImagingType: "thermal",
			ThermalReadout: &config.ThermalReadoutConfig{URL: up.URL, AuthHeader: "Authorization", AuthValue: "Bearer tok-1"},
		},
		{
			ID: "gb-vis", Protocol: "gb28181",
			ThermalReadout: &config.ThermalReadoutConfig{URL: up.URL},
		},
	}}
	require.NoError(t, h.db.UpsertCamera(context.Background(), "gb-hot", "hot", "gb28181", "", "", "", "", "", "", "", ""))
	require.NoError(t, h.db.UpsertCamera(context.Background(), "gb-vis", "vis", "gb28181", "", "", "", "", "", "", "", ""))

	rec, req := thermalFrameReq(t, "gb-hot")
	h.handleThermalFrame(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"values":[28,29,45]`, "grid payload relayed verbatim")

	// Visible channel with a readout configured still refuses (thermal only).
	rec, req = thermalFrameReq(t, "gb-vis")
	h.handleThermalFrame(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)

	// Unknown camera.
	rec, req = thermalFrameReq(t, "nope")
	h.handleThermalFrame(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestThermalFrameUpstreamFailure(t *testing.T) {
	t.Parallel()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(dead.Close)

	h := setupGB28181TestHandler(t)
	h.config = &config.Config{Cameras: []config.CameraConfig{
		{
			ID: "gb-hot", Protocol: "gb28181", ImagingType: "thermal",
			ThermalReadout: &config.ThermalReadoutConfig{URL: dead.URL},
		},
	}}
	require.NoError(t, h.db.UpsertCamera(context.Background(), "gb-hot", "hot", "gb28181", "", "", "", "", "", "", "", ""))
	rec, req := thermalFrameReq(t, "gb-hot")
	h.handleThermalFrame(rec, req)
	require.Equal(t, http.StatusBadGateway, rec.Code)
}
