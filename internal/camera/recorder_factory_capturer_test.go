// Regression tests for the dual-mode live-capturer guard on the
// recorder-rebuild path: with recording_enabled=true, the latest_frame
// timelapse poller must NOT start (frames come from PeriodicMergeManager).
// Before the fix, every reconnect of a disconnect-heavy MJPEG camera started
// a poller that spammed ~150s/6-frame timelapse fragments — 206 segments in
// 9h observed on production M5.
package camera

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestMJPEGServer serves a multipart/x-mixed-replace MJPEG stream so an
// http/jpeg camera can actually connect and start recording.
func newTestMJPEGServer(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	closed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if closed {
			mu.Unlock()
			return
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary=frame")
		flusher := w.(http.Flusher)
		frame := []byte{0xFF, 0xD8, 0xFF, 0xD9} // minimal JPEG
		for range 200 {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
			w.Write(frame)
			w.Write([]byte("\r\n"))
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	t.Cleanup(func() {
		mu.Lock()
		closed = true
		mu.Unlock()
		srv.Close()
	})
	return srv
}

func (cm *CameraManager) hasFramePollerForTest(cameraID string) bool {
	cm.auxMu.Lock()
	defer cm.auxMu.Unlock()
	_, ok := cm.framePollers[cameraID]
	return ok
}

// TestStartRecorder_LatestFramePollerSkippedWhenRecordingEnabled: the
// reconnect/rebuild path must honor the dual-mode skip — recording_enabled
// (nil or true) + timelapse enabled → NO live latest-frame poller.
func TestStartRecorder_LatestFramePollerSkippedWhenRecordingEnabled(t *testing.T) {
	t.Helper()
	for _, recEnabled := range []*bool{nil, boolPtrForTest(true)} {
		label := "true"
		if recEnabled == nil {
			label = "nil(default)"
		}
		t.Run(label, func(t *testing.T) {
			srv := newTestMJPEGServer(t)
			tmpDir := t.TempDir()
			cfg := &config.Config{
				Storage: config.StorageConfig{
					RootDir:         filepath.Join(tmpDir, "storage"),
					SegmentDuration: "10m",
				},
				Cameras: []config.CameraConfig{{
					ID:               "cam-capturer-guard",
					Name:             "Capturer Guard",
					Protocol:         "http",
					Encoding:         "jpeg",
					URL:              srv.URL + "/stream",
					RecordingEnabled: recEnabled,
					Timelapse: &config.CameraTimelapseConfig{
						Enabled:     true,
						Interval:    "30s",
						FrameSource: "auto",
					},
				}},
			}
			require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))
			store, err := storage.NewManager(cfg.Storage.RootDir)
			require.NoError(t, err)
			t.Cleanup(func() { store.CleanupTempFiles() })

			mgr := NewCameraManager(cfg, store, nil, "")
			t.Cleanup(func() { _ = mgr.Stop() })

			segDur, _ := time.ParseDuration(cfg.Storage.SegmentDuration)
			err = mgr.startRecorder(context.Background(), cfg.Cameras[0], segDur)
			require.NoError(t, err, "http/jpeg camera against local stub must start")

			assert.False(t, mgr.hasFramePollerForTest("cam-capturer-guard"),
				"latest_frame poller must NOT run when recording_enabled=%s (frames come from PeriodicMergeManager)", label)
		})
	}
}

// TestStartRecorder_LatestFramePollerRunsWhenRecordingDisabled: the skip must
// not break the legitimate case — recording off + timelapse on → poller runs.
func TestStartRecorder_LatestFramePollerRunsWhenRecordingDisabled(t *testing.T) {
	t.Helper()
	srv := newTestMJPEGServer(t)
	tmpDir := t.TempDir()
	falseVal := false
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
		Cameras: []config.CameraConfig{{
			ID:               "cam-capturer-guard-off",
			Name:             "Capturer Guard Off",
			Protocol:         "http",
			Encoding:         "jpeg",
			URL:              srv.URL + "/stream",
			RecordingEnabled: &falseVal,
			Timelapse: &config.CameraTimelapseConfig{
				Enabled:     true,
				Interval:    "30s",
				FrameSource: "auto",
			},
		}},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))
	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, nil, "")
	t.Cleanup(func() { _ = mgr.Stop() })

	segDur, _ := time.ParseDuration(cfg.Storage.SegmentDuration)
	err = mgr.startRecorder(context.Background(), cfg.Cameras[0], segDur)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return mgr.hasFramePollerForTest("cam-capturer-guard-off")
	}, 10*time.Second, 100*time.Millisecond, "poller should run when recording is disabled")
}

func boolPtrForTest(v bool) *bool { return &v }
