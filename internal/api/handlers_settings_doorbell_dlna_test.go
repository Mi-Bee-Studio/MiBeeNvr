package api

import (
	"bytes"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/doorbell"
)

func setupSettingsHandler(t *testing.T) *Handler {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{Cameras: []config.CameraConfig{}}
	cfg.ApplyDefaults()
	h := newHandlerWithConfig(db, store, cfg)
	h.configPath = filepath.Join(t.TempDir(), "cfg.yaml")
	return h
}

func TestSettings_DoorbellRoundTrip(t *testing.T) {
	t.Parallel()
	h := setupSettingsHandler(t)
	cfg := h.config

	cfg.Doorbell.Users = []config.DoorbellUserConfig{
		{Username: "door1", Name: "Front door", CameraID: "cam-1", Secret: "s3cret"},
	}

	// GET: section present, secrets masked.
	rr := doRequest(t, h.Routes(), "GET", "/api/settings", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, `"secret_configured":true`)
	require.NotContains(t, body, "s3cret", "the GET must never return doorbell secrets")
	require.Contains(t, body, `"outbound":null`)

	// PUT: whole-section replace; blank user secret keeps the stored one.
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings",
		bytes.NewReader([]byte(`{"doorbell":{
			"enabled":true,"listen":":5080","realm":"home","transport":"tcp",
			"lan_only":false,"ring_timeout":"45s",
			"users":[{"username":"door1","name":"Front","camera_id":"cam-2","secret":""},
			         {"username":"door2","name":"Gate","camera_id":"","secret":"new-secret"}],
			"outbound":{"enabled":true,"server":"pbx.example.com:5060","username":"nvr","secret":"pbx-secret","expires":"600s"}}
		}`)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"restart_required":true`)

	require.True(t, cfg.Doorbell.Enabled)
	require.Equal(t, ":5080", cfg.Doorbell.Listen)
	require.False(t, cfg.Doorbell.LANOnlyEnabled())
	require.Len(t, cfg.Doorbell.Users, 2)
	require.Equal(t, "s3cret", cfg.Doorbell.Users[0].Secret, "blank secret must keep the stored one")
	require.Equal(t, "cam-2", cfg.Doorbell.Users[0].CameraID)
	require.Equal(t, "new-secret", cfg.Doorbell.Users[1].Secret)
	require.NotNil(t, cfg.Doorbell.Outbound)
	require.Equal(t, "pbx.example.com:5060", cfg.Doorbell.Outbound.Server)
	require.Equal(t, "pbx-secret", cfg.Doorbell.Outbound.Secret)

	// The saved GET view reflects the merge and masks the new secrets.
	rr = doRequest(t, h.Routes(), "GET", "/api/settings", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotContains(t, rr.Body.String(), "new-secret")
	require.NotContains(t, rr.Body.String(), "pbx-secret")

	// A dlna-only save must not touch the doorbell section.
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings",
		bytes.NewReader([]byte(`{"dlna":{"enabled":false}}`)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.True(t, cfg.Doorbell.Enabled, "doorbell untouched by a dlna-only save")

	// outbound.enabled=false removes mode B; users survive.
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings",
		bytes.NewReader([]byte(`{"doorbell":{"outbound":{"enabled":false}}}`)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Nil(t, cfg.Doorbell.Outbound)
	require.Len(t, cfg.Doorbell.Users, 2)
}

func TestSettings_DoorbellValidationGate(t *testing.T) {
	t.Parallel()
	h := setupSettingsHandler(t)
	cfg := h.config

	// Enabled with a bogus transport: 400 with the load-side message, and the
	// section rolls back — no partial commit (#867 discipline).
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings",
		bytes.NewReader([]byte(`{"doorbell":{"enabled":true,"transport":"sctp"}}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "doorbell.transport")
	require.False(t, cfg.Doorbell.Enabled)
	require.Equal(t, "udp", cfg.Doorbell.Transport)

	// Enabled with neither users nor outbound — nothing could ever ring.
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings",
		bytes.NewReader([]byte(`{"doorbell":{"enabled":true}}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "no users and no outbound")
	require.False(t, cfg.Doorbell.Enabled)

	// Enabled with an outbound that lacks credentials.
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings",
		bytes.NewReader([]byte(`{"doorbell":{"enabled":true,"outbound":{"enabled":true,"server":"pbx.example.com"}}}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.False(t, cfg.Doorbell.Enabled)
}

func TestSettings_DlnaRoundTrip(t *testing.T) {
	t.Parallel()
	h := setupSettingsHandler(t)
	cfg := h.config

	require.Equal(t, 49152, cfg.DLNA.Port, "ApplyDefaults fills the DLNA port")

	rr := doRequest(t, h.Routes(), "PUT", "/api/settings",
		bytes.NewReader([]byte(`{"dlna":{"enabled":true,"port":8200,"interfaces":["eth0","enp3s0"],"friendly_name":"Living room NVR"}}`)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"restart_required":true`)

	require.True(t, cfg.DLNA.Enabled)
	require.Equal(t, 8200, cfg.DLNA.Port)
	require.Equal(t, []string{"eth0", "enp3s0"}, cfg.DLNA.Interfaces)
	require.Equal(t, "Living room NVR", cfg.DLNA.FriendlyName)

	// GET reflects the section.
	rr = doRequest(t, h.Routes(), "GET", "/api/settings", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"friendly_name":"Living room NVR"`)

	// Invalid port while enabled: 400 + rollback.
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings",
		bytes.NewReader([]byte(`{"dlna":{"port":99999}}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "dlna.port")
	require.Equal(t, 8200, cfg.DLNA.Port)

	// Empty interface name while enabled: 400 + rollback.
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings",
		bytes.NewReader([]byte(`{"dlna":{"interfaces":["eth0","  "]}}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Equal(t, []string{"eth0", "enp3s0"}, cfg.DLNA.Interfaces)
}

func TestDoorbellStatus_AbsentAndPresent(t *testing.T) {
	t.Parallel()
	h := setupSettingsHandler(t)
	h.config.Doorbell.Enabled = true

	// No service wired — the state between a settings save and the restart
	// it needs; unlike the call endpoints, status stays 200 and meaningful.
	rr := doRequest(t, h.Routes(), "GET", "/api/doorbell/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"enabled":true`)
	require.Contains(t, rr.Body.String(), `"running":false`)
	require.Contains(t, rr.Body.String(), `"registered":[]`)

	f := &fakeDoorbell{stations: []doorbell.StationStatus{
		{User: "door1", Source: "192.0.2.50:5060", ExpiresAt: time.Now().Add(time.Minute)},
	}}
	h.SetDoorbellService(f)
	rr = doRequest(t, h.Routes(), "GET", "/api/doorbell/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"running":true`)
	require.Contains(t, rr.Body.String(), `"user":"door1"`)
	require.Contains(t, rr.Body.String(), "192.0.2.50:5060")
}
