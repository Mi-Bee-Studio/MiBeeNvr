package api

// Long-tail GB28181 handler tests (#569): nil-service guards, request
// validation, pure helpers (time parsing, PTZ error mapping), channel
// resolution, and the playback-control request surface. Deterministic —
// services left unwired, so every path is a guard/validation branch.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mickeyzzc/gb28181-go/platform"
	"github.com/stretchr/testify/require"
)

// gbReq builds a request with a chi {id} route param bound.
func gbReq(t *testing.T, method, path, id string, body string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return httptest.NewRecorder(), req
}

func TestGB28181ParseTime(t *testing.T) {
	h := setupGB28181TestHandler(t)

	for _, ok := range []string{"2026-08-27T10:00:00", "2026-08-27 10:00:00", "2026-08-27T10:00:00Z"} {
		_, valid := h.parseGB28181Time(ok)
		require.True(t, valid, "layout must parse: %s", ok)
	}
	_, valid := h.parseGB28181Time("not-a-time")
	require.False(t, valid)
}

func TestGB28181ResolveChannelDevice(t *testing.T) {
	h := setupGB28181TestHandler(t)

	// No channel registered → unresolved.
	_, ok := h.resolveChannelDevice("34020000001320000011")
	require.False(t, ok)

	// Register a device + channel → resolves to the owning device.
	h.gb28181DeviceMgr.Register(&platform.Device{ID: "dev1", NetAddr: "127.0.0.1:9"})
	dev, _ := h.gb28181DeviceMgr.Device("dev1")
	if dev != nil {
		dev.Status.Store(platform.DeviceOnline)
	}
	h.gb28181DeviceMgr.RegisterChannel("dev1", &platform.Channel{
		ID: "34020000001320000011", Name: "Front",
	})
	got, ok := h.resolveChannelDevice("34020000001320000011")
	require.True(t, ok)
	require.Equal(t, "dev1", got)
}

func TestGB28181PlaybackControlValidation(t *testing.T) {
	h := setupGB28181TestHandler(t)

	// Invalid JSON body.
	w, req := gbReq(t, http.MethodPost, "/x", "ch1", "{bad json")
	h.handleChannelPlaybackControl(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Unsupported action.
	w, req = gbReq(t, http.MethodPost, "/x", "ch1", `{"action":"rewind"}`)
	h.handleChannelPlaybackControl(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Valid action but no media service wired → 503.
	w, req = gbReq(t, http.MethodPost, "/x", "ch1", `{"action":"pause"}`)
	h.handleChannelPlaybackControl(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestGB28181PlaybackStatusStopNoMedia(t *testing.T) {
	h := setupGB28181TestHandler(t)

	w, req := gbReq(t, http.MethodGet, "/x", "ch1", "")
	h.handleChannelPlaybackStatus(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)

	w, req = gbReq(t, http.MethodPost, "/x", "ch1", "")
	h.handleChannelPlaybackStop(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestGB28181TalkAndAlarmsNoMedia(t *testing.T) {
	h := setupGB28181TestHandler(t)

	w, req := gbReq(t, http.MethodGet, "/x", "cam1", "")
	h.handleGB28181TalkStatus(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var talk struct {
		Active bool `json:"active"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &talk))
	require.False(t, talk.Active)

	w, req = gbReq(t, http.MethodGet, "/x", "dev1", "")
	h.handleGB28181DeviceAlarms(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	w, req = gbReq(t, http.MethodGet, "/x", "dev1", "")
	h.handleGB28181DevicePositions(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestGB28181CascadeStatusUnwired(t *testing.T) {
	h := setupGB28181TestHandler(t)
	w := httptest.NewRecorder()
	h.handleGB28181CascadeStatus(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, false, resp["enabled"])
	require.Equal(t, false, resp["online"])
}

func TestGB28181ChannelRecordsValidation(t *testing.T) {
	h := setupGB28181TestHandler(t)

	// Missing start/end.
	w, req := gbReq(t, http.MethodGet, "/x", "ch1", "")
	h.handleChannelRecords(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Bad start format.
	w, req = gbReq(t, http.MethodGet, "/x?start=notatime&end=2026-08-27T00:00:00Z", "ch1", "")
	h.handleChannelRecords(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGB28181DeviceControlValidation(t *testing.T) {
	h := setupGB28181TestHandler(t)

	// Bad body.
	w, req := gbReq(t, http.MethodPost, "/x", "ch1", "{oops")
	h.handleChannelDeviceControl(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Unknown command.
	w, req = gbReq(t, http.MethodPost, "/x", "ch1", `{"command":"party"}`)
	h.handleChannelDeviceControl(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// TeleBoot without confirm is refused.
	w, req = gbReq(t, http.MethodPost, "/x", "ch1", `{"command":"reboot"}`)
	h.handleChannelDeviceControl(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "destructive commands require confirm")
}

func TestGB28181PTZPresetHandlers(t *testing.T) {
	h := setupGB28181TestHandler(t)

	// List: DB-backed, empty preset set.
	w := httptest.NewRecorder()
	h.handleGB28181PTZGetPresets(w, httptest.NewRequest(http.MethodGet, "/x", nil), "cam-gb")
	require.Equal(t, http.StatusOK, w.Code)
	var list struct {
		Presets []struct {
			Token string `json:"token"`
		} `json:"presets"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Empty(t, list.Presets)

	// Create requires a GB-bound camera (the harness has no camera manager).
	w = httptest.NewRecorder()
	h.handleGB28181PTZCreatePreset(w, httptest.NewRequest(http.MethodPost, "/x",
		bytes.NewReader([]byte(`{"name":"P1"}`))), "cam-gb")
	require.Equal(t, http.StatusBadRequest, w.Code, "unbound camera must 400")

	// Name is required.
	w = httptest.NewRecorder()
	h.handleGB28181PTZCreatePreset(w, httptest.NewRequest(http.MethodPost, "/x",
		bytes.NewReader([]byte(`{}`))), "cam-gb")
	require.Equal(t, http.StatusBadRequest, w.Code)

	// GoTo/Delete with an unbound camera → 400 (no GB channel).
	w = httptest.NewRecorder()
	h.handleGB28181PTZGoToPreset(w, httptest.NewRequest(http.MethodPost, "/x", nil), "cam-gb", "t1")
	require.Equal(t, http.StatusBadRequest, w.Code)

	w = httptest.NewRecorder()
	h.handleGB28181PTZDeletePreset(w, httptest.NewRequest(http.MethodDelete, "/x", nil), "cam-gb", "t1")
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestMapGB28181PTZError(t *testing.T) {
	h := setupGB28181TestHandler(t)

	for _, tc := range []struct {
		err  error
		want int
	}{
		{platform.ErrChannelNotFound, http.StatusNotFound},
		{platform.ErrDeviceOffline, http.StatusConflict},
		{platform.ErrPTZUnsupported, http.StatusNotFound},
		{platform.ErrZoomUnsupported, http.StatusNotFound},
		{errors.New("generic"), http.StatusInternalServerError},
	} {
		w := httptest.NewRecorder()
		h.mapGB28181PTZError(w, tc.err)
		require.Equal(t, tc.want, w.Code, "error %v", tc.err)
	}
}
