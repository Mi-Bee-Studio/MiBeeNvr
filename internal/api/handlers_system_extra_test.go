package api

// Tests for the uncovered long tail of handlers_system.go (#578):
// transcoding + auto-discover settings, system stats, feature toggles,
// backup listing/creation, and the /proc readers. The /proc readers run
// only where /proc exists (Linux — CI and the RPi target are Linux).

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// systemEnv builds a Handler with a live config + config file in a temp dir.
func systemEnv(t *testing.T) (*Handler, http.Handler) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mibee-nvr.yaml")
	cfg := &config.Config{
		Storage:      config.StorageConfig{RootDir: store.RootDir()},
		AutoDiscover: config.AutoDiscoverConfig{ScanIntervalSeconds: 60},
		Transcoding:  config.TranscodingConfig{Enabled: false, MaxWorkers: 2},
	}
	require.NoError(t, config.Save(cfgPath, cfg))
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, cfgPath, nil, nil, nil, nil, nil)
	return h, h.Routes()
}

func TestTranscodingSettings(t *testing.T) {
	t.Parallel()
	_, routes := systemEnv(t)

	rr := doRequest(t, routes, http.MethodGet, "/api/settings/transcoding", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"max_workers":2`)

	// Update within bounds persists.
	rr = doRequest(t, routes, http.MethodPut, "/api/settings/transcoding",
		strings.NewReader(`{"enabled":true,"max_workers":3}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rr = doRequest(t, routes, http.MethodGet, "/api/settings/transcoding", nil, "", "")
	require.Contains(t, rr.Body.String(), `"enabled":true`)
	require.Contains(t, rr.Body.String(), `"max_workers":3`)

	// Out-of-bounds workers → 400; bad JSON → 400.
	rr = doRequest(t, routes, http.MethodPut, "/api/settings/transcoding",
		strings.NewReader(`{"max_workers":9}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, http.MethodPut, "/api/settings/transcoding",
		strings.NewReader(`{"max_workers":0}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, http.MethodPut, "/api/settings/transcoding",
		strings.NewReader(`{bad`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// Nil-config handler degrades to 500.
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	nilCfg := TestHandler(db, store)
	rr = doRequest(t, nilCfg.Routes(), http.MethodGet, "/api/settings/transcoding", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestAutoDiscoverSettings(t *testing.T) {
	t.Parallel()
	h, routes := systemEnv(t)

	// GET never leaks the password, only the has_default_password flag.
	h.config.AutoDiscover.DefaultPassword = "secret"
	rr := doRequest(t, routes, http.MethodGet, "/api/settings/auto-discover", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotContains(t, rr.Body.String(), "secret")
	require.Contains(t, rr.Body.String(), `"has_default_password":true`)

	// PUT: floor scan interval to 30, set fields, empty password = unchanged.
	body := `{"enabled":true,"scan_interval":5,"listen_for_hello":false,
		"network_interface":"eth0","default_username":"admin","default_password":"","ignore_scopes":["lan"]}`
	rr = doRequest(t, routes, http.MethodPut, "/api/settings/auto-discover", strings.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, 30, h.config.AutoDiscover.ScanIntervalSeconds)
	require.Equal(t, "admin", h.config.AutoDiscover.DefaultUsername)
	require.Equal(t, "secret", h.config.AutoDiscover.DefaultPassword, "empty password must not wipe")

	// Non-empty password updates it.
	rr = doRequest(t, routes, http.MethodPut, "/api/settings/auto-discover",
		strings.NewReader(`{"default_password":"new"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "new", h.config.AutoDiscover.DefaultPassword)

	// Bad JSON → 400; nil config → 500.
	rr = doRequest(t, routes, http.MethodPut, "/api/settings/auto-discover",
		strings.NewReader(`{bad`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	nilCfg := TestHandler(db, store)
	rr = doRequest(t, nilCfg.Routes(), http.MethodGet, "/api/settings/auto-discover", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	rr = doRequest(t, nilCfg.Routes(), http.MethodPut, "/api/settings/auto-discover",
		strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestSystemStats(t *testing.T) {
	t.Parallel()
	_, routes := systemEnv(t)
	rr := doRequest(t, routes, http.MethodGet, "/api/stats/system", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"cpu"`)
	require.Contains(t, rr.Body.String(), `"memory"`)
	require.Contains(t, rr.Body.String(), `"uptime"`)
}

func TestProcReaders(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("/proc readers are Linux-only")
	}
	total, idle, err := readCPURaw()
	require.NoError(t, err)
	require.Positive(t, total)
	require.GreaterOrEqual(t, total, idle)

	memTotal, memAvail, err := readMemoryInfo()
	require.NoError(t, err)
	require.Positive(t, memTotal)
	require.Positive(t, memAvail)

	sent, recv, err := readNetworkInfo()
	require.NoError(t, err)
	// Loopback always has some traffic on a live host.
	require.GreaterOrEqual(t, sent+recv, uint64(0))
	require.Positive(t, readProcessRSS())
}

func TestFeatureToggles(t *testing.T) {
	t.Parallel()
	_, routes := systemEnv(t)

	rr := doRequest(t, routes, http.MethodPut, "/api/features",
		strings.NewReader(`{"protocols":{"onvif":true,"rtsp":false}}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"onvif":true`)
	require.Contains(t, rr.Body.String(), `"rtsp":false`)

	rr = doRequest(t, routes, http.MethodGet, "/api/features", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"onvif":true`)

	// Bad JSON → 400.
	rr = doRequest(t, routes, http.MethodPut, "/api/features", strings.NewReader(`{bad`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestBackupCreateAndList(t *testing.T) {
	t.Parallel()
	h, routes := systemEnv(t)

	// Empty listing first.
	rr := doRequest(t, routes, http.MethodGet, "/api/backups", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "[]")

	rr = doRequest(t, routes, http.MethodPost, "/api/backup", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "nvr-backup-")

	rr = doRequest(t, routes, http.MethodGet, "/api/backups", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), ".db")

	// The backup file really exists next to the config.
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(h.configPath), "backups"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestFormatUptime(t *testing.T) {
	t.Parallel()
	cases := map[time.Duration]string{
		90 * time.Second:             "1m 30s",
		2*time.Hour + 15*time.Minute: "2h 15m 0s",
		42 * time.Second:             "42s",
		0:                            "0s",
	}
	for d, want := range cases {
		require.Equal(t, want, formatUptime(d), d.String())
	}
}
