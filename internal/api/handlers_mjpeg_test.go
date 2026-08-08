package api

// Tests for handlers_mjpeg.go — MJPEG stream URL resolution + latest-frame
// snapshot endpoint (#232). The success paths require a live recorder; here we
// cover the nil-manager / no-camera error paths and the pure URL extractor.

import (
	"net/http"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/stretchr/testify/require"
)

// fakeSegmentStore is a no-op recorder.SegmentStore for constructing recorders
// in tests without a real storage manager.
type fakeSegmentStore struct{}

func (fakeSegmentStore) CreateSegment(_ string, _ string) (string, string, error) {
	return "", "", nil
}
func (fakeSegmentStore) WriteFrame(_ string, _ []byte) (int, error) { return 0, nil }
func (fakeSegmentStore) CloseSegment(_, _ string) error             { return nil }

func TestMjpeg_StreamURL_NotAvailable(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // camMgr is nil → resolveMjpegURL returns ""

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-1/stream.mjpeg", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestMjpeg_LatestFrame_NotAvailable(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // camMgr is nil → no frame

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-1/latest-frame", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestGetMjpegURLFromRecorder_NonHTTPJPEG(t *testing.T) {
	t.Parallel()
	// A non-HTTPJPEG recorder (and non-delegater) yields no URL.
	require.Empty(t, getMjpegURLFromRecorder(nil))
	require.Empty(t, getMjpegURLFromRecorder("not-a-recorder"))
}

func TestGetMjpegURLFromRecorder_HTTPJPEG(t *testing.T) {
	t.Parallel()
	// Construct an HTTPJPEGRecorder and verify the URL is extracted.
	rec := recorder.NewHTTPJPEGRecorder(recorder.HTTPJPEGConfig{
		CameraID: "cam-1",
		URL:      "http://example/stream",
	}, fakeSegmentStore{})
	require.Equal(t, "http://example/stream", getMjpegURLFromRecorder(rec))
}
