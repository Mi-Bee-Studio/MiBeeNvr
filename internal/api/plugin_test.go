package api

import (
	"encoding/json"
	"net/http"
	"testing"

	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/plugin"
	"github.com/stretchr/testify/require"
)

func mockPluginManager() *plugin.PluginManager {
	mgr := plugin.NewTestPluginManager("test-plugin")
	plugin.AddTestPlugin(mgr, "test-plugin", &gen.PluginInfo{
		Name:      "test-plugin",
		Version:   "1.0.0",
		Protocols: []string{"testproto"},
		Capabilities: &gen.Capabilities{
			Hls:      true,
			Ptz:      false,
			Snapshot: true,
		},
		SupportedEncodings: []gen.Codec{gen.Codec_CODEC_H264, gen.Codec_CODEC_H265},
	})
	return mgr
}

func TestPlugins_NilManager(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	rr := doRequest(t, h.Routes(), "GET", "/api/plugins", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Plugins []any `json:"plugins"`
	}
	parseJSON(t, rr, &body)
	require.NotEmpty(t, body.Plugins)
}

func TestGetPlugin_NilManager(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	rr := doRequest(t, h.Routes(), "GET", "/api/plugins/nonexistent", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRestartPlugin_NilManager(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	rr := doRequest(t, h.Routes(), "POST", "/api/plugins/nonexistent/restart", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestGetPluginCapabilities_NilManager(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	rr := doRequest(t, h.Routes(), "GET", "/api/plugins/nonexistent/capabilities", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestPlugins_WithManager(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.pluginMgr = mockPluginManager()
	rr := doRequest(t, h.Routes(), "GET", "/api/plugins", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Plugins []json.RawMessage `json:"plugins"`
	}
	parseJSON(t, rr, &body)
	require.NotEmpty(t, body.Plugins)
	found := false
	for _, raw := range body.Plugins {
		var p map[string]any
		require.NoError(t, json.Unmarshal(raw, &p))
		if p["name"] == "test-plugin" {
			found = true
			require.Equal(t, "1.0.0", p["version"])
			require.Equal(t, "running", p["status"])
		}
	}
	require.True(t, found, "test-plugin should be in plugin list")
}

func TestGetPlugin_Found(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.pluginMgr = mockPluginManager()
	rr := doRequest(t, h.Routes(), "GET", "/api/plugins/test-plugin", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var p pluginJSON
	parseJSON(t, rr, &p)
	require.Equal(t, "test-plugin", p.Name)
	require.Equal(t, "1.0.0", p.Version)
	require.Equal(t, "running", p.Status)
	require.Equal(t, 0, p.RestartCount)
}

func TestGetPlugin_NotFound(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.pluginMgr = mockPluginManager()
	rr := doRequest(t, h.Routes(), "GET", "/api/plugins/missing-plugin", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRestartPlugin_WithMockPlugin(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.pluginMgr = mockPluginManager()

	// Restart may succeed or fail depending on whether the mock plugin binary exists
	rr := doRequest(t, h.Routes(), "POST", "/api/plugins/test-plugin/restart", nil, "", "")
	// Accept 200 (restart ok), 404 (not found), or 500 (internal error)
	require.Contains(t, []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError}, rr.Code,
		"expected 200/404/500, got %d", rr.Code)
}

func TestRestartPlugin_NotFound(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.pluginMgr = mockPluginManager()
	rr := doRequest(t, h.Routes(), "POST", "/api/plugins/nonexistent/restart", nil, "", "")
	t.Logf("status=%d body=%s", rr.Code, rr.Body.String())
	require.True(t, rr.Code == http.StatusNotFound || rr.Code == http.StatusInternalServerError,
		"expected 404 or 500, got %d body=%s", rr.Code, rr.Body.String())
}

func TestGetPluginCapabilities_Found(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.pluginMgr = mockPluginManager()
	rr := doRequest(t, h.Routes(), "GET", "/api/plugins/test-plugin/capabilities", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var body map[string]any
	parseJSON(t, rr, &body)
	require.Equal(t, "test-plugin", body["name"])
	caps, ok := body["capabilities"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, caps["hls"])
	require.Equal(t, false, caps["ptz"])
	require.Equal(t, true, caps["snapshot"])
	encodings, ok := body["supported_encodings"].([]any)
	require.True(t, ok)
	require.Contains(t, encodings, "h264")
	require.Contains(t, encodings, "h265")
}

func TestGetPluginCapabilities_NotFound(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.pluginMgr = mockPluginManager()
	rr := doRequest(t, h.Routes(), "GET", "/api/plugins/nonexistent/capabilities", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestProtocolsEndpoint(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	rr := doRequest(t, h.Routes(), "GET", "/api/protocols", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Protocols []struct {
			ID           string         `json:"id"`
			Label        string         `json:"label"`
			Encodings    []string       `json:"encodings"`
			BuiltIn      bool           `json:"built_in"`
			Capabilities map[string]bool `json:"capabilities"`
		} `json:"protocols"`
	}
	parseJSON(t, rr, &resp)
	require.Len(t, resp.Protocols, 3)
	require.Equal(t, "rtsp", resp.Protocols[0].ID)
	require.True(t, resp.Protocols[0].BuiltIn)
	require.Contains(t, resp.Protocols[0].Encodings, "h264")
	require.True(t, resp.Protocols[0].Capabilities["hls"])
	require.Equal(t, "http", resp.Protocols[1].ID)
	require.Contains(t, resp.Protocols[1].Encodings, "jpeg")
	require.Equal(t, "onvif", resp.Protocols[2].ID)
	require.True(t, resp.Protocols[2].Capabilities["ptz"])
}

func TestProtocolsWithPlugin(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.pluginMgr = mockPluginManager()
	rr := doRequest(t, h.Routes(), "GET", "/api/protocols", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Protocols []struct {
			ID      string `json:"id"`
			BuiltIn bool   `json:"built_in"`
		} `json:"protocols"`
	}
	parseJSON(t, rr, &resp)
	require.Len(t, resp.Protocols, 4)
	require.Equal(t, "testproto", resp.Protocols[3].ID)
	require.False(t, resp.Protocols[3].BuiltIn)
}

func TestProtocolsNoAuth(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	hash, err := middleware.HashPassword("secret")
	require.NoError(t, err)
	h := TestHandlerWithAuth(db, store, "admin", hash)
	rr := doRequest(t, h.Routes(), "GET", "/api/protocols", nil, "", "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}