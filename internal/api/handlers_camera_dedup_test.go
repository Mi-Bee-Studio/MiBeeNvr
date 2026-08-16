package api

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/stretchr/testify/require"
)

// setupDedupHandler builds a handler with a real camera manager (rtsp/void —
// rtsp recorders never connect in these tests; camera creation with
// Enabled=false avoids recorder start) plus a GB28181 device manager holding
// one registered device at a fixed IP.
func setupDedupHandler(t *testing.T) *Handler {
	t.Helper()
	db, store := setupTestDB(t)
	cfg := &config.Config{Storage: config.StorageConfig{SegmentDuration: "30s"}}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, camMgr.Start(ctx))
	t.Cleanup(func() { _ = camMgr.Stop() })

	deviceMgr := gb28181.NewDeviceManager(60 * time.Second)
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, deviceMgr, nil)
	deviceMgr.Register(&gb28181.Device{
		ID:      "34020000001310000001",
		NetAddr: "192.168.63.240:5060",
	})
	return h
}

// TestHandleCreateCamera_GBDuplicateRefused: adding a pull camera (rtsp/onvif)
// whose host IP belongs to a registered GB28181 device is refused with 409 —
// same physical camera, one entry. allow_duplicate=true overrides.
func TestHandleCreateCamera_GBDuplicateRefused(t *testing.T) {
	h := setupDedupHandler(t)

	body := `{"name":"Front","protocol":"rtsp","url":"rtsp://192.168.63.240:554/stream","enabled":false}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusConflict, rr.Code, "body: %s", rr.Body.String())

	// Same host via onvif_endpoint is refused too.
	body = `{"name":"Front ONVIF","protocol":"onvif","onvif_endpoint":"http://192.168.63.240/onvif/device_service","enabled":false}`
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusConflict, rr.Code)

	// allow_duplicate overrides the guard.
	body = `{"name":"Front","protocol":"rtsp","url":"rtsp://192.168.63.240:554/stream","enabled":false,"allow_duplicate":true}`
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())
}

// TestHandleCreateCamera_NoGBDeviceAllowsNormalCreate: with no GB28181 device
// at that IP, creation is unaffected.
func TestHandleCreateCamera_NoGBDeviceAllowsNormalCreate(t *testing.T) {
	h := setupDedupHandler(t)

	body := `{"name":"Other","protocol":"rtsp","url":"rtsp://192.168.63.9:554/stream","enabled":false}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())
}

// TestHandleCreateCamera_NilDeviceMgrNoop: the dedup guard must not fire when
// the GB28181 device manager is absent (gb28181 disabled / older wiring).
func TestHandleCreateCamera_NilDeviceMgrNoop(t *testing.T) {
	db, store := setupTestDB(t)
	camMgr := camera.NewCameraManager(&config.Config{}, store, db, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, camMgr.Start(ctx))
	defer camMgr.Stop()

	h := NewHandler(db, store, noopAuthMW(), nil, camMgr, nil, "", nil, nil, nil, nil, nil)
	require.Nil(t, h.gb28181DeviceMgr)

	body := `{"name":"Front","protocol":"rtsp","url":"rtsp://192.168.63.240:554/stream","enabled":false}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusCreated, rr.Code)
}
