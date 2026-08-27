package api

// Tests for handlers_mjpeg.go (#578): stream-URL resolution + latest-frame
// polling with ETag. Hermetic via stub recorders and a constructed (never
// started) HTTPJPEGRecorder.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/stretchr/testify/require"
)

// frameStub serves a fixed latest frame.
type frameStub struct {
	stubRecorder
	frame []byte
}

func (f *frameStub) LatestFrame() []byte { return f.frame }

func mjpegEnv(t *testing.T, rec model.Recorder, cams []config.CameraConfig) http.Handler {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{Storage: config.StorageConfig{RootDir: store.RootDir()}, Cameras: cams}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	if rec != nil {
		camMgr.SetTestRecorder("cam-1", rec)
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil)
	return h.Routes()
}

func TestMjpegStreamURL(t *testing.T) {
	t.Parallel()

	// No camera manager at all → 404.
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	rr := doRequest(t, TestHandler(db, store).Routes(), http.MethodGet, "/api/cameras/cam-1/stream.mjpeg", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Unknown camera (no recorder, no http config) → 404.
	rr = doRequest(t, mjpegEnv(t, nil, nil), http.MethodGet, "/api/cameras/cam-1/stream.mjpeg", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// HTTPJPEG recorder supplies its URL directly.
	jpegRec := recorder.NewHTTPJPEGRecorder(recorder.HTTPJPEGConfig{
		CameraID: "cam-1", URL: "http://192.168.63.225:81/stream",
	}, nil)
	rr = doRequest(t, mjpegEnv(t, jpegRec, nil), http.MethodGet, "/api/cameras/cam-1/stream.mjpeg", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "http://192.168.63.225:81/stream")

	// Fallback: http-protocol camera config URL.
	cams := []config.CameraConfig{{ID: "cam-1", Protocol: "http", URL: "http://10.0.0.5/mjpeg"}}
	rr = doRequest(t, mjpegEnv(t, nil, cams), http.MethodGet, "/api/cameras/cam-1/stream.mjpeg", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "http://10.0.0.5/mjpeg")
}

func TestGetMjpegURLFromRecorder(t *testing.T) {
	t.Parallel()

	jpegRec := recorder.NewHTTPJPEGRecorder(recorder.HTTPJPEGConfig{URL: "http://x:81/stream"}, nil)
	require.Equal(t, "http://x:81/stream", getMjpegURLFromRecorder(jpegRec))

	// Delegate layers are unwrapped before the type check.
	wrapped := &delegatingStub{inner: jpegRec}
	require.Equal(t, "http://x:81/stream", getMjpegURLFromRecorder(wrapped))

	// RTSP MJPEG recorders have no HTTP URL.
	require.Empty(t, getMjpegURLFromRecorder(stubRecorder{}))
}

func TestLatestFrame_ETagFlow(t *testing.T) {
	t.Parallel()
	frame := []byte("fake-jpeg-bytes")
	routes := mjpegEnv(t, &frameStub{frame: frame}, nil)

	rr := doRequest(t, routes, http.MethodGet, "/api/cameras/cam-1/latest-frame", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "image/jpeg", rr.Header().Get("Content-Type"))
	require.Equal(t, frame, rr.Body.Bytes())
	etag := rr.Header().Get("ETag")
	require.NotEmpty(t, etag)

	// Conditional request with the matching ETag → 304, no body.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/api/cameras/cam-1/latest-frame", nil)
	req.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotModified, w.Code)
	require.Empty(t, w.Body.Bytes())

	// Delegating recorder unwraps to the frame provider.
	routes = mjpegEnv(t, &delegatingStub{inner: &frameStub{frame: frame}}, nil)
	rr = doRequest(t, routes, http.MethodGet, "/api/cameras/cam-1/latest-frame", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, frame, rr.Body.Bytes())
}

func TestLatestFrame_Guards(t *testing.T) {
	t.Parallel()

	// Unknown camera → 404.
	rr := doRequest(t, mjpegEnv(t, nil, nil), http.MethodGet, "/api/cameras/cam-1/latest-frame", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Recorder without latest-frame support → 404.
	rr = doRequest(t, mjpegEnv(t, stubRecorder{}, nil), http.MethodGet, "/api/cameras/cam-1/latest-frame", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Recorder present but no frame captured yet → 404.
	jpegRec := recorder.NewHTTPJPEGRecorder(recorder.HTTPJPEGConfig{CameraID: "cam-1"}, nil)
	rr = doRequest(t, mjpegEnv(t, jpegRec, nil), http.MethodGet, "/api/cameras/cam-1/latest-frame", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}
