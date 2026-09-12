package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCameraGroupEndpoints covers the v37 group registry API: create (with
// validation), list, rename that walks member cameras (drag-free rename is
// server-side atomic), and delete that ungroups members.
func TestCameraGroupEndpoints(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	ctx := context.Background()

	// Seed one camera, grouped via the per-camera field.
	require.NoError(t, db.UpsertCamera(ctx, "cam-g1", "G1", "onvif", "", "", "", "", "", "", "", ""))
	require.NoError(t, db.UpdateCameraGroup(ctx, "cam-g1", "旧组"))

	// Create: validation + happy path.
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/groups", bytes.NewReader([]byte(`{"name":"  "}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/groups", bytes.NewReader([]byte(`{"name":"`+string(make([]byte, 70))+`"}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/groups", bytes.NewReader([]byte(`{"name":" 新组 "}`)), "", "")
	require.Equal(t, http.StatusCreated, rr.Code)

	// List.
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/groups", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var groups []string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &groups))
	require.Equal(t, []string{"新组"}, groups)

	// Rename the DERIVED group (no registry row): cameras must follow.
	rr = doRequest(t, h.Routes(), "PUT", "/api/cameras/groups/"+url.PathEscape("旧组"), bytes.NewReader([]byte(`{"name":"后院"}`)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	row, err := db.GetCamera(ctx, "cam-g1")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "后院", row.GroupName)

	// Rename onto itself → 400.
	rr = doRequest(t, h.Routes(), "PUT", "/api/cameras/groups/"+url.PathEscape("后院"), bytes.NewReader([]byte(`{"name":"后院"}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// Delete: member becomes ungrouped; response reports the count.
	rr = doRequest(t, h.Routes(), "DELETE", "/api/cameras/groups/"+url.PathEscape("后院"), nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var delResp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &delResp))
	require.Equal(t, float64(1), delResp["ungrouped"])
	row, err = db.GetCamera(ctx, "cam-g1")
	require.NoError(t, err)
	require.Equal(t, "", row.GroupName)
}

// TestCameraGroupsOrderEndpoint covers PUT /api/cameras/groups/order (v38):
// validation (dupes, empty names) and the persisted order.
func TestCameraGroupsOrderEndpoint(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "PUT", "/api/cameras/groups/order",
		bytes.NewReader([]byte(`{"names":["乙","甲"]}`)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doRequest(t, h.Routes(), "PUT", "/api/cameras/groups/order",
		bytes.NewReader([]byte(`{"names":["甲","甲"]}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, h.Routes(), "PUT", "/api/cameras/groups/order",
		bytes.NewReader([]byte(`{"names":["  "]}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/groups", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var groups []string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &groups))
	require.Equal(t, []string{"乙", "甲"}, groups)
}
