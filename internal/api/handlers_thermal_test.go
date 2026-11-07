package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
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

func thermalHistoryReq(t *testing.T, id, query string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/"+id+"/thermal/history"+query, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return httptest.NewRecorder(), req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// The P4 history endpoint: minute samples in the requested window, decimated
// to `points` buckets with extremes preserved; non-thermal cameras and bad
// params are refused.
func TestThermalHistoryEndpoint(t *testing.T) {
	t.Parallel()
	h := setupGB28181TestHandler(t)
	h.config = &config.Config{Cameras: []config.CameraConfig{
		{ID: "gb-hot", Protocol: "gb28181", ImagingType: "thermal"},
		{ID: "gb-vis", Protocol: "gb28181"},
	}}
	ctx := context.Background()
	require.NoError(t, h.db.UpsertCamera(ctx, "gb-hot", "hot", "gb28181", "", "", "", "", "", "", "", ""))
	require.NoError(t, h.db.UpsertCamera(ctx, "gb-vis", "vis", "gb28181", "", "", "", "", "", "", "", ""))

	base := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	for i := range 10 {
		require.NoError(t, h.db.InsertThermalMinute(ctx, "gb-hot", base.Add(time.Duration(i)*time.Minute), f(float64(30+i)), f(float64(20+i)), ""))
	}

	q := "?from=" + base.Format(time.RFC3339) + "&to=" + base.Add(10*time.Minute).Format(time.RFC3339)
	rec, req := thermalHistoryReq(t, "gb-hot", q)
	h.handleThermalHistory(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Points []storage.ThermalPoint `json:"points"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Points, 10)
	require.InDelta(t, 39.0, *body.Points[9].MaxC, 1e-9)

	// Decimation: 10 rows → 3 buckets keeps the extremes (39 max, 20 min).
	rec, req = thermalHistoryReq(t, "gb-hot", q+"&points=3")
	h.handleThermalHistory(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Points, 3)
	var maxV, minV float64 = -1, 1e9
	for _, p := range body.Points {
		if p.MaxC != nil && *p.MaxC > maxV {
			maxV = *p.MaxC
		}
		if p.MinC != nil && *p.MinC < minV {
			minV = *p.MinC
		}
	}
	require.InDelta(t, 39.0, maxV, 1e-9, "hot spike survives decimation")
	require.InDelta(t, 20.0, minV, 1e-9)

	// Non-thermal camera → 404; bad params → 400.
	rec, req = thermalHistoryReq(t, "gb-vis", q)
	h.handleThermalHistory(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec, req = thermalHistoryReq(t, "gb-hot", "?from=not-a-time")
	h.handleThermalHistory(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec, req = thermalHistoryReq(t, "gb-hot", q+"&points=9999")
	h.handleThermalHistory(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// kind: hikvision — the frame proxy decodes the ISAPI radiometric multipart
// server-side and serves the native matrix with its geometry plus the
// device's thermal→visible registration (the SPA probe/mirror contract).
func TestThermalFrameHikvisionKind(t *testing.T) {
	t.Parallel()
	const cols, rows = 4, 3
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/ISAPI/Thermal/channels/2/thermometry/jpegPicWithAppendData", r.URL.Path)
		meta := `{"JpegPictureWithAppendData":{"jpegPicWidth":4,"jpegPicHeight":3,"p2pDataLen":48,` +
			`"isFreezedata":false,"temperatureDataLength":4,"VisibleValidRect":{"height":0.858,"width":0.646,"x":0.149,"y":0.11}}}`
		var b bytes.Buffer
		b.WriteString("--boundary\r\nContent-Type: application/json\r\n\r\n" + meta + "\r\n")
		b.WriteString("--boundary\r\nContent-Type: application/octet-stream\r\n\r\n")
		for i := range cols * rows {
			v := float32(20 + i) // 20..31, hottest last
			_ = binary.Write(&b, binary.LittleEndian, math.Float32bits(v))
		}
		b.WriteString("\r\n--boundary--\r\n")
		w.Header().Set("Content-Type", "multipart/form-data; boundary=boundary")
		_, _ = w.Write(b.Bytes())
	}))
	t.Cleanup(up.Close)

	h := setupGB28181TestHandler(t)
	h.config = &config.Config{Cameras: []config.CameraConfig{{
		ID: "cam-hik-t", Protocol: "onvif", ImagingType: "thermal",
		ONVIFEndpoint: up.URL + "/onvif/device_service", Username: "admin", Password: "pw",
		ThermalReadout: &config.ThermalReadoutConfig{Kind: "hikvision"},
	}}}
	require.NoError(t, h.db.UpsertCamera(context.Background(), "cam-hik-t", "hik", "onvif", "", "", "", "", "", "", "", ""))

	rec, req := thermalFrameReq(t, "cam-hik-t")
	h.handleThermalFrame(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Grid struct {
			Cols   int       `json:"cols"`
			Rows   int       `json:"rows"`
			Values []float64 `json:"values"`
		} `json:"grid"`
		Max struct {
			ValueC float64 `json:"value_c"`
			X      int     `json:"x"`
			Y      int     `json:"y"`
		} `json:"max"`
		VisibleRect map[string]float64 `json:"visible_rect"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, cols, got.Grid.Cols)
	require.Equal(t, rows, got.Grid.Rows)
	require.Len(t, got.Grid.Values, cols*rows)
	require.InDelta(t, 31, got.Max.ValueC, 1e-9)
	require.Equal(t, 3, got.Max.X)
	require.Equal(t, 2, got.Max.Y)
	require.InDelta(t, 0.149, got.VisibleRect["x"], 1e-9)
}
