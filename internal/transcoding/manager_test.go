package transcoding

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// newManagerTestDB creates a fresh SQLite DB with schema initialized.
func newManagerTestDB(t *testing.T) *storage.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	return db
}

// createMockFFmpeg creates a mock ffmpeg binary that succeeds.
func createMockFFmpeg(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := `#!/bin/sh
# Mock FFmpeg: write a small file to the last argument (output path)
output=""
for arg in "$@"; do
	output="$arg"
done
echo "test content" > "$output"
exit 0
`
	err := os.WriteFile(mockFFmpeg, []byte(mockScript), 0755)
	require.NoError(t, err)

	// Also create mock ffprobe
	mockFFprobe := filepath.Join(dir, "ffprobe")
	ffprobeScript := `#!/bin/sh
echo '{"streams":[{"codec_name":"h264","duration":10.0,"width":1920,"height":1080}]}'
exit 0
`
	err = os.WriteFile(mockFFprobe, []byte(ffprobeScript), 0755)
	require.NoError(t, err)

	return mockFFmpeg
}

func TestManager_NewTranscodeManager_Success(t *testing.T) {
	t.Helper()
	ResetProbe()

	db := newManagerTestDB(t)
	mockFFmpeg := createMockFFmpeg(t)
	m := metrics.NewMetrics()

	cfg := ManagerConfig{
		Transcoding: config.TranscodingConfig{
			Enabled:    true,
			FFmpegPath: mockFFmpeg,
			MaxWorkers: 1,
		},
		DataDir:    t.TempDir(),
		FFmpegPath: mockFFmpeg,
		MaxWorkers: 1,
	}

	mgr, err := NewTranscodeManager(db, cfg, m)
	require.NoError(t, err)
	require.NotNil(t, mgr)
	require.NotNil(t, mgr.caps)
	require.True(t, mgr.caps.FFmpegAvailable)
	require.NotNil(t, mgr.queue)
	require.NotNil(t, mgr.downloader)
}

func TestManager_NewTranscodeManager_NoFFmpeg(t *testing.T) {
	t.Helper()
	ResetProbe()

	db := newManagerTestDB(t)
	m := metrics.NewMetrics()

	cfg := ManagerConfig{
		Transcoding: config.TranscodingConfig{
			Enabled:    true,
			FFmpegPath: "/nonexistent/ffmpeg",
			MaxWorkers: 1,
		},
		DataDir:    t.TempDir(),
		FFmpegPath: "/nonexistent/ffmpeg",
		MaxWorkers: 1,
	}

	mgr, err := NewTranscodeManager(db, cfg, m)
	require.Error(t, err)
	require.Nil(t, mgr)
	require.Contains(t, err.Error(), "hardware insufficient")
}

func TestManager_RunAndStop(t *testing.T) {
	t.Helper()
	ResetProbe()

	db := newManagerTestDB(t)
	mockFFmpeg := createMockFFmpeg(t)
	m := metrics.NewMetrics()

	cfg := ManagerConfig{
		Transcoding: config.TranscodingConfig{
			Enabled:    true,
			FFmpegPath: mockFFmpeg,
			MaxWorkers: 1,
		},
		DataDir:        t.TempDir(),
		FFmpegPath:     mockFFmpeg,
		MaxWorkers:     1,
	}

	mgr, err := NewTranscodeManager(db, cfg, m)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		mgr.Run(ctx)
		close(done)
	}()

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	// Stop should cancel and return
	cancel()
	select {
	case <-done:
		// Good, Run returned
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestManager_StopNil(t *testing.T) {
	t.Helper()
	// Nil-safe Stop should not panic
	var mgr *TranscodeManager
	mgr.Stop() // should not panic
}

func TestManager_EnqueueRecording(t *testing.T) {
	t.Helper()
	ResetProbe()

	db := newManagerTestDB(t)
	mockFFmpeg := createMockFFmpeg(t)
	m := metrics.NewMetrics()

	cfg := ManagerConfig{
		Transcoding: config.TranscodingConfig{
			Enabled:    true,
			FFmpegPath: mockFFmpeg,
			MaxWorkers: 1,
		},
		DataDir:        t.TempDir(),
		FFmpegPath:     mockFFmpeg,
		MaxWorkers:     1,
	}

	mgr, err := NewTranscodeManager(db, cfg, m)
	require.NoError(t, err)

	// Create a fake input file
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "test-segment.mp4")
	err = os.WriteFile(inputPath, []byte("fake mp4 data"), 0644)
	require.NoError(t, err)

	err = mgr.EnqueueRecording("cam-front-door", "rec-123", inputPath, "h265", "h264")
	require.NoError(t, err)

	// Verify the task was inserted into the DB
	tasks, err := db.GetTasksByStatus(context.Background(), "pending")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "cam-front-door", tasks[0].CameraID)
	require.Equal(t, "rec-123", tasks[0].RecordingID)
	require.Equal(t, inputPath, tasks[0].InputPath)
	require.Equal(t, "h265", tasks[0].InputFormat)
	require.Equal(t, "h264", tasks[0].OutputFormat)
}

func TestManager_EnqueueRecording_NilManager(t *testing.T) {
	t.Helper()
	// Nil-safe EnqueueRecording should not panic
	var mgr *TranscodeManager
	err := mgr.EnqueueRecording("cam-1", "rec-1", "/tmp/test.mp4", "h264", "h265")
	require.NoError(t, err)
}

func TestManager_GetStatus(t *testing.T) {
	t.Helper()
	ResetProbe()

	db := newManagerTestDB(t)
	mockFFmpeg := createMockFFmpeg(t)
	m := metrics.NewMetrics()

	cfg := ManagerConfig{
		Transcoding: config.TranscodingConfig{
			Enabled:    true,
			FFmpegPath: mockFFmpeg,
			MaxWorkers: 1,
		},
		DataDir:        t.TempDir(),
		FFmpegPath:     mockFFmpeg,
		MaxWorkers:     1,
	}

	mgr, err := NewTranscodeManager(db, cfg, m)
	require.NoError(t, err)

	status := mgr.GetStatus()
	require.True(t, status.Enabled)
	require.NotNil(t, status.Hardware)
	require.Equal(t, 0, status.ActiveJobs)

	var nilMgr *TranscodeManager
	nilStatus := nilMgr.GetStatus()
	require.False(t, nilStatus.Enabled)
	require.Equal(t, "", nilStatus.DisabledReason)
}

func TestManager_GetStatus_NilWithReason(t *testing.T) {
	t.Helper()
	SetDisabledReason("hardware insufficient: no H.264 encoder")
	t.Cleanup(func() { SetDisabledReason("") })

	var nilMgr *TranscodeManager
	status := nilMgr.GetStatus()
	require.False(t, status.Enabled)
	require.Equal(t, "hardware insufficient: no H.264 encoder", status.DisabledReason)
}

func TestManager_HardwareInfo(t *testing.T) {
	t.Helper()
	ResetProbe()

	db := newManagerTestDB(t)
	mockFFmpeg := createMockFFmpeg(t)
	m := metrics.NewMetrics()

	cfg := ManagerConfig{
		Transcoding: config.TranscodingConfig{
			Enabled:    true,
			FFmpegPath: mockFFmpeg,
			MaxWorkers: 1,
		},
		DataDir:        t.TempDir(),
		FFmpegPath:     mockFFmpeg,
		MaxWorkers:     1,
	}

	mgr, err := NewTranscodeManager(db, cfg, m)
	require.NoError(t, err)

	caps := mgr.HardwareInfo()
	require.NotNil(t, caps)
	require.True(t, caps.FFmpegAvailable)

	// Nil-safe
	var nilMgr *TranscodeManager
	require.Nil(t, nilMgr.HardwareInfo())
}

func TestManager_Downloader(t *testing.T) {
	t.Helper()
	ResetProbe()

	db := newManagerTestDB(t)
	mockFFmpeg := createMockFFmpeg(t)
	m := metrics.NewMetrics()

	cfg := ManagerConfig{
		Transcoding: config.TranscodingConfig{
			Enabled:    true,
			FFmpegPath: mockFFmpeg,
			MaxWorkers: 1,
		},
		DataDir:        t.TempDir(),
		FFmpegPath:     mockFFmpeg,
		MaxWorkers:     1,
	}

	mgr, err := NewTranscodeManager(db, cfg, m)
	require.NoError(t, err)

	dl := mgr.Downloader()
	require.NotNil(t, dl)

	// Nil-safe
	var nilMgr *TranscodeManager
	require.Nil(t, nilMgr.Downloader())
}

func TestManager_Queue(t *testing.T) {
	t.Helper()
	ResetProbe()

	db := newManagerTestDB(t)
	mockFFmpeg := createMockFFmpeg(t)
	m := metrics.NewMetrics()

	cfg := ManagerConfig{
		Transcoding: config.TranscodingConfig{
			Enabled:    true,
			FFmpegPath: mockFFmpeg,
			MaxWorkers: 1,
		},
		DataDir:        t.TempDir(),
		FFmpegPath:     mockFFmpeg,
		MaxWorkers:     1,
	}

	mgr, err := NewTranscodeManager(db, cfg, m)
	require.NoError(t, err)

	queue := mgr.Queue()
	require.NotNil(t, queue)

	// Nil-safe
	var nilMgr *TranscodeManager
	require.Nil(t, nilMgr.Queue())
}

func TestManager_UpdateFFmpegStatus(t *testing.T) {
	t.Helper()

	tests := []struct {
		name     string
		expected float64
		setup    func(t *testing.T) *TranscodeManager
	}{
		{
			name:     "not_installed",
			expected: 0,
			setup: func(t *testing.T) *TranscodeManager {
				t.Helper()
				m := metrics.NewMetrics()
				dl := NewDownloader(t.TempDir(), nil)
				return &TranscodeManager{
					downloader: dl,
					m:          m,
				}
			},
		},
		{
			name:     "downloading",
			expected: 1,
			setup: func(t *testing.T) *TranscodeManager {
				t.Helper()
				m := metrics.NewMetrics()
				dl := NewDownloader(t.TempDir(), nil)
				dl.mu.Lock()
				dl.status = DownloadStatus{Status: "downloading", Progress: 0.5}
				dl.mu.Unlock()
				return &TranscodeManager{
					downloader: dl,
					m:          m,
				}
			},
		},
		{
			name:     "available",
			expected: 2,
			setup: func(t *testing.T) *TranscodeManager {
				t.Helper()
				ResetProbe()
				dataDir := t.TempDir()
				toolsDir := filepath.Join(dataDir, "tools")
				require.NoError(t, os.MkdirAll(toolsDir, 0755))
				mockFFmpeg := filepath.Join(toolsDir, "ffmpeg")
				mockScript := `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo 'ffmpeg version 7.0-static'
  exit 0
fi
exit 0
`
				require.NoError(t, os.WriteFile(mockFFmpeg, []byte(mockScript), 0755))

				db := newManagerTestDB(t)
				m := metrics.NewMetrics()
				cfg := ManagerConfig{
					Transcoding: config.TranscodingConfig{
						Enabled:    true,
						FFmpegPath: mockFFmpeg,
						MaxWorkers: 1,
					},
					DataDir:    dataDir,
					FFmpegPath: mockFFmpeg,
					MaxWorkers: 1,
				}
				mgr, err := NewTranscodeManager(db, cfg, m)
				require.NoError(t, err)
				require.NotNil(t, mgr)
				return mgr
			},
		},
		{
			name:     "failed",
			expected: 3,
			setup: func(t *testing.T) *TranscodeManager {
				t.Helper()
				m := metrics.NewMetrics()
				dl := NewDownloader(t.TempDir(), nil)
				dl.mu.Lock()
				dl.status = DownloadStatus{Status: "failed", Error: "test download error"}
				dl.mu.Unlock()
				return &TranscodeManager{
					downloader: dl,
					m:          m,
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := tt.setup(t)
			mgr.updateFFmpegStatus()

			families, err := mgr.m.Registry.Gather()
			require.NoError(t, err)

			var found bool
			for _, f := range families {
				if f.GetName() == "nvr_transcoding_ffmpeg_status" {
					found = true
					require.Len(t, f.GetMetric(), 1)
					require.Equal(t, tt.expected, f.GetMetric()[0].GetGauge().GetValue())
					break
				}
			}
			require.True(t, found, "expected nvr_transcoding_ffmpeg_status metric to be present")
		})
	}
}
