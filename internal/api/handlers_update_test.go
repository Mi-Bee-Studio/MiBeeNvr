package api

// Tests for handlers_update.go (#578). The nil-checker paths are the
// hermetic surface: a live update.Checker would dial GitHub, which is not
// CI-safe. updateChecker stays nil throughout (never mutated here — it is
// a package var and tests run in parallel).

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdate_NoCheckerDegradesGracefully(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	routes := h.Routes()

	rr := doRequest(t, routes, http.MethodGet, "/api/version", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"current":""`)
	require.Contains(t, rr.Body.String(), `"deployment"`)

	rr = doRequest(t, routes, http.MethodGet, "/api/update/check", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"update_available":false`)

	// POST forces a refresh; with no checker it must still answer, not 5xx.
	req := httptest.NewRequest(http.MethodPost, "/api/update/check", nil)
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"deployment"`)
}
