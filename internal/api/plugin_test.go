package api

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/stretchr/testify/require"
)

func TestProtocolsEndpoint(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	rr := doRequest(t, h.Routes(), "GET", "/api/protocols", nil, "", "")
	require.Equal(t, 200, rr.Code)
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
	require.Len(t, resp.Protocols, 4)
	require.Equal(t, "rtsp", resp.Protocols[0].ID)
	require.True(t, resp.Protocols[0].BuiltIn)
	require.Contains(t, resp.Protocols[0].Encodings, "h264")
	require.True(t, resp.Protocols[0].Capabilities["hls"])
	require.Equal(t, "http", resp.Protocols[1].ID)
	require.Contains(t, resp.Protocols[1].Encodings, "jpeg")
	require.Equal(t, "onvif", resp.Protocols[2].ID)
	require.True(t, resp.Protocols[2].Capabilities["ptz"])
	require.Equal(t, "xiaomi", resp.Protocols[3].ID)
	require.True(t, resp.Protocols[3].BuiltIn)
	require.True(t, resp.Protocols[3].Capabilities["hls"])
}

func TestProtocolsNoAuth(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	hash, err := middleware.HashPassword("secret")
	require.NoError(t, err)
	h := testHandlerWithAuth(db, store, "admin", hash)
	rr := doRequest(t, h.Routes(), "GET", "/api/protocols", nil, "", "")
	require.Equal(t, 401, rr.Code)
}
