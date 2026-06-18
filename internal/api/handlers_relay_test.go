package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
	"github.com/stretchr/testify/require"
)

func TestRelayPresets_ListReturnsFive(t *testing.T) {
	handler := newTestRelayHandler(t)
	rr := doRequest(t, handler, http.MethodGet, "/api/relay-presets", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var presets []relay.Preset
	err := json.NewDecoder(rr.Body).Decode(&presets)
	require.NoError(t, err)
	require.Len(t, presets, 5)
}

func TestRelayPresets_GetYouTube(t *testing.T) {
	handler := newTestRelayHandler(t)
	rr := doRequest(t, handler, http.MethodGet, "/api/relay-presets/youtube", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var preset relay.Preset
	err := json.NewDecoder(rr.Body).Decode(&preset)
	require.NoError(t, err)
	require.Equal(t, "youtube", preset.Name)
	require.Equal(t, "YouTube Live", preset.Description)
	require.Equal(t, 4500, preset.VideoBitrateKbps)
}

func TestRelayPresets_GetNotFound(t *testing.T) {
	handler := newTestRelayHandler(t)
	rr := doRequest(t, handler, http.MethodGet, "/api/relay-presets/nonexistent", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRelayPresets_NoRelayManager(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/relay-presets", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var presets []interface{}
	err := json.NewDecoder(rr.Body).Decode(&presets)
	require.NoError(t, err)
	require.Empty(t, presets)
}

// newTestRelayHandler creates a handler wired to a relay.Manager with built-in presets.
func newTestRelayHandler(t *testing.T) http.Handler {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })

	rm := relay.NewManager(nil, nil)
	rm.SetPresetRegistry(relay.NewPresetRegistry())

	h := TestHandler(db, store)
	h.SetRelayManager(rm)
	return h.Routes()
}
