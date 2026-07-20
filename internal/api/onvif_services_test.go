package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// --- Helper: set up ONVIF camera in test DB ---

func setupONVIFCamera(t *testing.T) (*storage.DB, *storage.Manager) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	store, err := storage.NewManager(filepath.Join(dir, "storage"))
	require.NoError(t, err)
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "", "onvif://host/stream", "admin", "pass", "", "", "", ""))
	return db, store
}

// --- Snapshot URI endpoint tests ---

func TestSnapshotGetUri_NoCamMgr(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/onvif-cam/snapshot/uri", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "camera manager not available")
}

func TestSnapshotGetUri_CameraNotFound(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/nonexistent/snapshot/uri", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestSnapshotGetUri_NonONVIFRejected(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-cam", "RTSP Camera", "rtsp_h264", "", "rtsp://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/rtsp-cam/snapshot/uri", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Imaging endpoint tests ---

func TestImagingGetSettings_NoCamMgr(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/onvif-cam/imaging/settings", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "camera manager not available")
}

func TestImagingGetSettings_CameraNotFound(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/nonexistent/imaging/settings", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestImagingSetSettings_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodPut, "/api/cameras/onvif-cam/imaging/settings", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestImagingSetSettings_NoCamMgr(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)
	body := `{"brightness": 0.5, "contrast": 0.3}`

	req := httptest.NewRequest(http.MethodPut, "/api/cameras/onvif-cam/imaging/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "camera manager not available")
}

func TestImagingGetOptions_NoCamMgr(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/onvif-cam/imaging/options", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestImagingGetOptions_CameraNotFound(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/nonexistent/imaging/options", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

// --- Device Management endpoint tests ---

func TestONVIFReboot_NoCamMgr(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodPost, "/api/cameras/onvif-cam/onvif/reboot", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "camera manager not available")
}

func TestONVIFReboot_CameraNotFound(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/cameras/nonexistent/onvif/reboot", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestONVIFGetNetwork_NoCamMgr(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/onvif-cam/onvif/network", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestONVIFGetNetwork_CameraNotFound(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/nonexistent/onvif/network", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestONVIFSetNetwork_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodPut, "/api/cameras/onvif-cam/onvif/network", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestONVIFSetNetwork_NoCamMgr(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)
	body := `{"interfaces": [{"name": "eth0"}]}`

	req := httptest.NewRequest(http.MethodPut, "/api/cameras/onvif-cam/onvif/network", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestONVIFGetUsers_NoCamMgr(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/onvif-cam/onvif/users", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestONVIFGetUsers_CameraNotFound(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/nonexistent/onvif/users", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestONVIFCreateUsers_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodPost, "/api/cameras/onvif-cam/onvif/users", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestONVIFCreateUsers_NoCamMgr(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)
	body := `{"users": [{"username": "test", "level": "User"}]}`

	req := httptest.NewRequest(http.MethodPost, "/api/cameras/onvif-cam/onvif/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestONVIFDeleteUsers_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodDelete, "/api/cameras/onvif-cam/onvif/users", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestONVIFDeleteUsers_NoCamMgr(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)
	body := `{"usernames": ["olduser"]}`

	req := httptest.NewRequest(http.MethodDelete, "/api/cameras/onvif-cam/onvif/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestONVIFSetUser_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodPut, "/api/cameras/onvif-cam/onvif/users/admin", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestONVIFSetUser_NoCamMgr(t *testing.T) {
	t.Parallel()
	db, store := setupONVIFCamera(t)
	h := TestHandler(db, store)
	body := `{"password": "newpass"}`

	req := httptest.NewRequest(http.MethodPut, "/api/cameras/onvif-cam/onvif/users/admin", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestONVIFSetUser_CameraNotFound(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)
	body := `{"password": "newpass"}`

	req := httptest.NewRequest(http.MethodPut, "/api/cameras/nonexistent/onvif/users/admin", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

// --- Non-ONVIF camera rejected for device management ---

func TestONVIFReboot_NonONVIFRejected(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-cam", "RTSP Camera", "rtsp_h264", "", "rtsp://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodPost, "/api/cameras/rtsp-cam/onvif/reboot", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestONVIFGetNetwork_NonONVIFRejected(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-cam", "RTSP Camera", "rtsp_h264", "", "rtsp://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/rtsp-cam/onvif/network", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestONVIFGetUsers_NonONVIFRejected(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-cam", "RTSP Camera", "rtsp_h264", "", "rtsp://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/rtsp-cam/onvif/users", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Non-ONVIF camera rejected for imaging ---

func TestImagingGetSettings_NonONVIFRejected(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-cam", "RTSP Camera", "rtsp_h264", "", "rtsp://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/rtsp-cam/imaging/settings", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestImagingSetSettings_NonONVIFRejected(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-cam", "RTSP Camera", "rtsp_h264", "", "rtsp://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)
	body := `{"brightness": 0.5}`

	req := httptest.NewRequest(http.MethodPut, "/api/cameras/rtsp-cam/imaging/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestImagingGetOptions_NonONVIFRejected(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-cam", "RTSP Camera", "rtsp_h264", "", "rtsp://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/rtsp-cam/imaging/options", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Error-classification helpers ---

func TestIsSOAPFaultStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  string
		want bool
	}{
		// Real device responses (captured from 视通 NVR/encoder PTZ GetStatus)
		{"status400 with SOAP-ENV envelope", `raw GetStatus failed: SOAP request failed with status 400: <?xml version="1.0"?><SOAP-ENV:Envelope`, true},
		{"status400 with Fault", `SOAP request failed with status 400: <s:Fault><s:Text>no ptz</s:Text>`, true},
		{"status500 with envelope", `failed with status 500: <Envelope xmlns=`, true},
		// Not a SOAP fault
		{"auth 401 no body", `SOAP request failed with status 401`, false},
		{"network error", `Post "http://x": dial tcp: connection refused`, false},
		{"plain error", `get PTZ status failed: EOF`, false},
		{"empty", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isSOAPFaultStatus(tc.err))
		})
	}
}

// TestHandleONVIFSnapshotError_ErrorMessage ensures the snapshot endpoint reports
// a snapshot-appropriate message (regression: it previously called the PTZ error
// handler and returned the misleading "PTZ operation failed").
func TestHandleONVIFSnapshotError_ErrorMessage(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	// A generic error (not a typed sentinel) hits the default branch.
	handleONVIFSnapshotError(w, "cam-1", errGeneric("boom"))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.NotContains(t, w.Body.String(), "PTZ", "snapshot error must not say PTZ")
	require.Contains(t, w.Body.String(), "snapshot")
}

type errGeneric string

func (e errGeneric) Error() string { return string(e) }
