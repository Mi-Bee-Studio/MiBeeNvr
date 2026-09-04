package api

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// --- GET /api/settings/rtsp-output (#686) ---

func TestGetRtspOutputSettings_MasksPassword(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	enabled := true
	cfg := &config.Config{
		Server: config.ServerConfig{RTSP: config.RTSPOutputConfig{
			Enabled: &enabled, Port: 8554, Username: "admin", Password: "s3cret",
		}},
	}
	h := newHandlerWithConfig(db, store, cfg)

	rr := doRequest(t, h.Routes(), "GET", "/api/settings/rtsp-output", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, `"enabled":true`)
	require.Contains(t, body, `"port":8554`)
	require.Contains(t, body, `"username":"admin"`)
	require.Contains(t, body, `"password_configured":true`)
	require.NotContains(t, body, "s3cret", "password must never be returned over the API")
}

func TestGetRtspOutputSettings_DefaultEnabled(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{}
	h := newHandlerWithConfig(db, store, cfg)

	rr := doRequest(t, h.Routes(), "GET", "/api/settings/rtsp-output", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	// Enabled is *bool default-true: nil resolves to true.
	require.Contains(t, rr.Body.String(), `"enabled":true`)
}

func TestGetRtspOutputSettings_NilConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/settings/rtsp-output", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

// --- PUT /api/settings/rtsp-output (#686) ---

// newRtspSettingsHandler wires a Handler whose configPath points at a temp
// yaml, so PUTs exercise the real config.Save persistence path.
func newRtspSettingsHandler(t *testing.T, db *storage.DB, store *storage.Manager, cfg *config.Config) *Handler {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "mibee-nvr.yaml")
	return NewHandler(db, store, noopAuthMW(), cfg, nil, nil, configPath, nil, nil, nil, nil, nil)
}

func TestUpdateRtspOutputSettings_PersistsAndRequiresRestart(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{}
	configPath := filepath.Join(t.TempDir(), "mibee-nvr.yaml")
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, configPath, nil, nil, nil, nil, nil)

	body := `{"enabled":true,"port":9554,"username":"u1","password":"p1"}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/rtsp-output", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"restart_required":true`)

	require.NotNil(t, cfg.Server.RTSP.Enabled)
	require.True(t, *cfg.Server.RTSP.Enabled)
	require.Equal(t, 9554, cfg.Server.RTSP.Port)
	require.Equal(t, "u1", cfg.Server.RTSP.Username)
	require.Equal(t, "p1", cfg.Server.RTSP.Password)

	// The yaml file on disk carries the section (atomic write via config.Save).
	raw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), "rtsp:")
}

func TestUpdateRtspOutputSettings_PortValidation(t *testing.T) {
	t.Parallel()
	for _, port := range []int{0, -1, 70000} {
		db, store := setupTestDB(t)
		cfg := &config.Config{}
		h := newHandlerWithConfig(db, store, cfg)
		body := `{"port":` + itoa(int64(port)) + `}`
		rr := doRequest(t, h.Routes(), "PUT", "/api/settings/rtsp-output", bytes.NewReader([]byte(body)), "", "")
		require.Equal(t, http.StatusBadRequest, rr.Code, "port %d must be rejected", port)
		db.Close()
	}
}

func TestUpdateRtspOutputSettings_BlankPasswordKeepsCurrent(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Server: config.ServerConfig{RTSP: config.RTSPOutputConfig{
		Username: "admin", Password: "old",
	}}}
	h := newRtspSettingsHandler(t, db, store, cfg)

	// The GET never returns the password, so the UI round-trips an empty field
	// when unchanged — an empty password must NOT wipe the stored one.
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/rtsp-output",
		bytes.NewReader([]byte(`{"password":""}`)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "old", cfg.Server.RTSP.Password)
}

func TestUpdateRtspOutputSettings_ClearCredentials(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Server: config.ServerConfig{RTSP: config.RTSPOutputConfig{
		Username: "admin", Password: "old",
	}}}
	h := newRtspSettingsHandler(t, db, store, cfg)

	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/rtsp-output",
		bytes.NewReader([]byte(`{"clear_credentials":true}`)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "", cfg.Server.RTSP.Username)
	require.Equal(t, "", cfg.Server.RTSP.Password)
}

func TestUpdateRtspOutputSettings_UsernameClearableDirectly(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Server: config.ServerConfig{RTSP: config.RTSPOutputConfig{
		Username: "admin", Password: "old",
	}}}
	h := newRtspSettingsHandler(t, db, store, cfg)

	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/rtsp-output",
		bytes.NewReader([]byte(`{"username":""}`)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "", cfg.Server.RTSP.Username)
}

// --- rtspEndpointFor (#686: MJPEG servable + credentials in URL) ---

func rtspTestConfig(enabled bool, user, pass string) *config.Config {
	e := enabled
	return &config.Config{Server: config.ServerConfig{RTSP: config.RTSPOutputConfig{
		Enabled: &e, Port: 8554, Username: user, Password: pass,
	}}}
}

func TestRtspEndpointFor_MJPEGServable(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := newHandlerWithConfig(db, store, rtspTestConfig(true, "", ""))

	// #658 made the RTSP output server serve MJPEG; the endpoint detail must
	// agree (previously reported "codec not servable").
	detail := h.rtspEndpointFor(newReqHost(t, "192.168.1.5:9090"), "cam-1", model.FormatMJPEG)
	require.NotNil(t, detail)
	require.True(t, detail.Available)
	require.Equal(t, "rtsp://192.168.1.5:8554/cam-1", detail.URL)
}

func TestRtspEndpointFor_CredentialsInURL(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := newHandlerWithConfig(db, store, rtspTestConfig(true, "admin", "p@ss"))

	detail := h.rtspEndpointFor(newReqHost(t, "192.168.1.5:9090"), "cam-1", model.FormatH264)
	require.NotNil(t, detail)
	require.True(t, detail.Available)
	require.Equal(t, "rtsp://admin:p%40ss@192.168.1.5:8554/cam-1", detail.URL)
}

func TestRtspEndpointFor_DisabledNil(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := newHandlerWithConfig(db, store, rtspTestConfig(false, "", ""))

	require.Nil(t, h.rtspEndpointFor(newReqHost(t, "192.168.1.5:9090"), "cam-1", model.FormatH264))
}

func TestRtspEndpointFor_UnservableCodec(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := newHandlerWithConfig(db, store, rtspTestConfig(true, "", ""))

	detail := h.rtspEndpointFor(newReqHost(t, "192.168.1.5:9090"), "cam-1", model.Format("mpeg4"))
	require.NotNil(t, detail)
	require.False(t, detail.Available)
	require.NotEmpty(t, detail.Reason)
}

// newReqHost builds a GET request whose Host matches a real browser request to
// hostport — rtspEndpointFor derives the pull URL's host from it.
func newReqHost(t *testing.T, hostport string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+hostport+"/", nil)
	require.NoError(t, err)
	return req
}
