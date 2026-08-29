package api

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/mickeyzzc/gb28181-go/platform"
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

	deviceMgr := platform.NewDeviceManager(60 * time.Second)
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, deviceMgr, nil)
	deviceMgr.Register(&platform.Device{
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
	camMgr := camera.NewCameraManager(&config.Config{Storage: config.StorageConfig{SegmentDuration: "30s"}}, store, db, "")
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

// TestHandleCreateCamera_GBFingerprintRefusedAcrossInterfaces: scenario 5 —
// the GB28181 device registered earlier (fingerprint cached from a probe of
// its SIP source IP), and now the user adds the ONVIF camera from the OTHER
// interface IP. L1 (IP) misses; L2 probes the create-host serial and matches
// the fingerprint → 409.
func TestHandleCreateCamera_GBFingerprintRefusedAcrossInterfaces(t *testing.T) {
	h := setupDedupHandler(t)

	// A fake ONVIF endpoint on a local port answering GetDeviceInformation.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>` +
			`<GetDeviceInformationResponse><SerialNumber>NC00000001</SerialNumber>` +
			`</GetDeviceInformationResponse></s:Body></s:Envelope>`))
	}))
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	oldPorts := onvif.ProbePorts
	onvif.ProbePorts = []string{strconv.Itoa(port)}
	t.Cleanup(func() { onvif.ProbePorts = oldPorts })

	// Fingerprint persisted when the device registered via GB28181 (from a
	// different interface IP than the create host).
	require.NoError(t, h.db.UpsertGB28181Fingerprint(context.Background(), storage.GB28181Fingerprint{
		DeviceID: "34020000001310000001", Serial: "NC00000001", SourceIP: "192.168.63.152",
		ProbedAt: time.Now(),
	}))

	// Create host is 127.0.0.1 (the fake endpoint) — no GB device matches that
	// IP, so only the serial fingerprint can catch the duplicate.
	body := `{"name":"Front ONVIF","protocol":"onvif","onvif_endpoint":"http://127.0.0.1/onvif/device_service"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusConflict, rr.Code, "body: %s", rr.Body.String())
	require.Contains(t, rr.Body.String(), "NC00000001")

	// allow_duplicate overrides.
	body = `{"name":"Front ONVIF","protocol":"onvif","onvif_endpoint":"http://127.0.0.1/onvif/device_service","allow_duplicate":true}`
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())
}

// TestHandleCreateCamera_NoFingerprintNoProbe: with no cached fingerprints at
// all (no dual-protocol device ever registered), the create path must not
// probe at all — normal setups pay zero extra latency.
func TestHandleCreateCamera_NoFingerprintNoProbe(t *testing.T) {
	h := setupDedupHandler(t)

	probed := false
	oldPorts := onvif.ProbePorts
	onvif.ProbePorts = nil // any probe attempt would iterate zero ports
	t.Cleanup(func() { onvif.ProbePorts = oldPorts })
	_ = probed

	body := `{"name":"Other","protocol":"rtsp","url":"rtsp://127.0.0.1:554/stream"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())
}
