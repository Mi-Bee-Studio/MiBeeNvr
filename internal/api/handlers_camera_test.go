package api

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// --- cameraRowForAPI tests ---

func TestCameraRowForAPI_ONVIFEndpoint(t *testing.T) {
	t.Helper()
	t.Parallel()
	row := &storage.CameraRow{Protocol: "onvif", ONVIFEndpoint: "http://192.168.1.100/onvif/device_service", URL: ""}
	cameraRowForAPI(row)
	require.Equal(t, "http://192.168.1.100/onvif/device_service", row.URL)
}

func TestCameraRowForAPI_NonONVIFUnchanged(t *testing.T) {
	t.Helper()
	t.Parallel()
	row := &storage.CameraRow{Protocol: "rtsp", URL: "rtsp://192.168.1.10/stream", ONVIFEndpoint: ""}
	cameraRowForAPI(row)
	require.Equal(t, "rtsp://192.168.1.10/stream", row.URL)
}

func TestCameraRowForAPI_ONVIFWithURLAlreadySet(t *testing.T) {
	t.Helper()
	t.Parallel()
	row := &storage.CameraRow{Protocol: "onvif", URL: "http://already-set", ONVIFEndpoint: "http://192.168.1.100/onvif"}
	cameraRowForAPI(row)
	require.Equal(t, "http://already-set", row.URL)
}

func TestCameraRowForAPI_ONVIFNoEndpoint(t *testing.T) {
	t.Helper()
	t.Parallel()
	row := &storage.CameraRow{Protocol: "onvif", URL: "", ONVIFEndpoint: ""}
	cameraRowForAPI(row)
	require.Equal(t, "", row.URL)
}

// --- stripScheme tests ---

func TestStripScheme_RTSPWithPort(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "192.168.1.10:554", stripScheme("rtsp://192.168.1.10:554/stream"))
}

func TestStripScheme_RTSPNoPort(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "192.168.1.10:554", stripScheme("rtsp://192.168.1.10/stream"))
}

func TestStripScheme_HTTPWithPort(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "192.168.1.10:8080", stripScheme("http://192.168.1.10:8080/capture"))
}

func TestStripScheme_HTTPS(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "camera.example.com:443", stripScheme("https://camera.example.com:443/stream"))
}

func TestStripScheme_NoScheme(t *testing.T) {
	t.Helper()
	t.Parallel()
	result := stripScheme("192.168.1.10:554")
	require.Contains(t, result, "192.168.1.10:554")
}

func TestStripScheme_RTSPDefaultPort(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "10.0.0.1:554", stripScheme("rtsp://10.0.0.1/path"))
}

func TestStripScheme_RTSPWithCredentials(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "192.168.1.10:554", stripScheme("rtsp://admin:secret@192.168.1.10/stream"))
}

func TestStripScheme_RTSPWithCredentialsAndPort(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "192.168.1.10:8554", stripScheme("rtsp://user:pass@192.168.1.10:8554/stream"))
}

// --- handleTestConnection validation tests ---

func TestTestConnection_MissingURL(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"protocol":"rtsp","url":""}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestTestConnection_InvalidBody(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", bytes.NewReader([]byte("not json")), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestTestConnection_RTSPConnectionRefused(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"protocol":"rtsp","url":"rtsp://192.168.255.255:554/stream"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	parseJSON(t, rr, &resp)
	require.Equal(t, false, resp["success"])
}

func TestTestConnection_HTTPConnectionFailed(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"protocol":"http","url":"http://127.0.0.1:1/capture"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	parseJSON(t, rr, &resp)
	require.Equal(t, false, resp["success"])
}

func TestTestConnection_InvalidURLFormat(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"protocol":"http","url":"http://127.0.0.1:1/capture"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}

// --- handleCreateCamera validation tests ---

func TestCreateCamera_MissingName(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"protocol":"rtsp","url":"rtsp://192.168.1.10/stream"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCreateCamera_MissingProtocol(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"name":"Test","url":"rtsp://192.168.1.10/stream"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCreateCamera_InvalidProtocol(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"name":"Test","protocol":"ftp","url":"ftp://192.168.1.10/stream"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCreateCamera_MissingURL(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"name":"Test","protocol":"rtsp"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCreateCamera_InvalidURL(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"name":"Test","protocol":"rtsp","url":"not-a-url"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCreateCamera_InvalidBody(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", bytes.NewReader([]byte("not json")), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCreateCamera_ONVIFMissingEndpoint(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"name":"Test","protocol":"onvif"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCreateCamera_LegacyProtocol(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"name":"Test","protocol":"rtsp_h264","url":"rtsp://192.168.1.10/stream"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", bytes.NewReader([]byte(body)), "", "")
	// Will fail because camMgr is nil, but protocol parsing should succeed
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestCreateCamera_NilCamMgr(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"name":"Test","protocol":"rtsp","url":"rtsp://192.168.1.10/stream","encoding":"h264"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

// --- validateURL tests ---

func TestValidateURL_ValidRTSP(t *testing.T) {
	t.Parallel()
	require.True(t, validateURL("rtsp://192.168.1.10:554/stream"))
}

func TestValidateURL_ValidHTTP(t *testing.T) {
	t.Parallel()
	require.True(t, validateURL("http://192.168.1.10/capture"))
}

func TestValidateURL_Empty(t *testing.T) {
	t.Parallel()
	require.False(t, validateURL(""))
}

func TestValidateURL_NoScheme(t *testing.T) {
	t.Parallel()
	require.False(t, validateURL("192.168.1.10:554/stream"))
}

func TestValidateURL_NoHost(t *testing.T) {
	t.Parallel()
	require.False(t, validateURL("rtsp://"))
}

func TestValidateURL_Invalid(t *testing.T) {
	t.Parallel()
	require.False(t, validateURL("://"))
}

// --- isImageFile tests ---

func TestIsImageFile_JPG(t *testing.T) {
	t.Parallel()
	require.True(t, isImageFile("frame001.jpg"))
}

func TestIsImageFile_JPEG(t *testing.T) {
	t.Parallel()
	require.True(t, isImageFile("frame001.jpeg"))
}

func TestIsImageFile_PNG(t *testing.T) {
	t.Parallel()
	require.True(t, isImageFile("frame001.png"))
}

func TestIsImageFile_Uppercase(t *testing.T) {
	t.Parallel()
	require.True(t, isImageFile("frame001.JPG"))
}

func TestIsImageFile_MP4(t *testing.T) {
	t.Parallel()
	require.False(t, isImageFile("video.mp4"))
}

func TestIsImageFile_NoExtension(t *testing.T) {
	t.Parallel()
	require.False(t, isImageFile("frame"))
}

// --- handleListCameras tests ---

func TestCameraList_EmptyList(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var cameras []interface{}
	parseJSON(t, rr, &cameras)
	require.Equal(t, 0, len(cameras))
}

// --- handleGetCamera tests ---

func TestGetCamera_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// --- handleDeleteCamera tests ---

func TestDeleteCamera_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/nonexistent", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// --- handleStartCamera tests ---

func TestStartCamera_NoManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-cam/start", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// --- handleStopCamera tests ---

func TestStopCamera_NoManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-cam/stop", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// --- handleUpdateCamera tests ---

func TestUpdateCamera_NoManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"name":"Updated"}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/cameras/test-cam", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestUpdateCamera_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "PUT", "/api/cameras/test-cam", bytes.NewReader([]byte("not json")), "", "")
	// camMgr is nil, so returns 500 before parsing body
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestUpdateCamera_InvalidURL(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"url":"not-a-valid-url"}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/cameras/test-cam", bytes.NewReader([]byte(body)), "", "")
	// camMgr is nil, so returns 500 before URL validation
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

// --- validProtocols map test ---

func TestValidProtocols(t *testing.T) {
	t.Parallel()
	for _, proto := range []string{"rtsp", "http", "onvif", "xiaomi", "rtsp_h264", "rtsp_h265", "rtsp_mjpeg", "http_jpeg"} {
		require.True(t, validProtocols[proto], "expected %q to be valid", proto)
	}
	require.False(t, validProtocols["ftp"])
	require.False(t, validProtocols[""])
}

// --- extractDIDFromURL tests ---

func TestExtractDIDFromURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "12345678", extractDIDFromURL("xiaomi://12345678"))
}

func TestExtractDIDFromURL_Empty(t *testing.T) {
	t.Parallel()
	require.Equal(t, "", extractDIDFromURL(""))
}

func TestExtractDIDFromURL_Invalid(t *testing.T) {
	t.Parallel()
	require.Equal(t, "", extractDIDFromURL("://bad"))
}

// --- strPtr helper ---

func TestStrPtr(t *testing.T) {
	t.Parallel()
	s := "hello"
	p := strPtr(s)
	require.NotNil(t, p)
	require.Equal(t, "hello", *p)
}
