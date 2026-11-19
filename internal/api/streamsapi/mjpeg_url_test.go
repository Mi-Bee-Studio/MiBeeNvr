package streamsapi

// Unit tests for the MJPEG URL extractor (moved with handlers_mjpeg.go).

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/stretchr/testify/require"
)

func TestGetMjpegURLFromRecorder_NonHTTPJPEG(t *testing.T) {
	t.Parallel()
	// A non-HTTPJPEG recorder (and non-delegater) yields no URL.
	require.Empty(t, getMjpegURLFromRecorder(nil))
	require.Empty(t, getMjpegURLFromRecorder("not-a-recorder"))
}

func TestGetMjpegURLFromRecorder_HTTPJPEG(t *testing.T) {
	t.Parallel()
	rec := recorder.NewHTTPJPEGRecorder(recorder.HTTPJPEGConfig{
		CameraID: "cam-1",
		URL:      "http://example/stream",
	}, fakeSegmentStore{})
	require.Equal(t, "http://example/stream", getMjpegURLFromRecorder(rec))
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
