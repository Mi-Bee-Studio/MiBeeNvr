package metrics

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewMetrics(t *testing.T) {
	t.Helper()
	m := NewMetrics()
	require.NotNil(t, m)
	require.NotNil(t, m.Registry)
	require.NotNil(t, m.RecordingBytesTotal)
	require.NotNil(t, m.ActiveCameras)
	require.NotNil(t, m.ActiveRecordings)
	require.NotNil(t, m.SegmentsCreated)
	require.NotNil(t, m.CleanupDeleted)
	require.NotNil(t, m.StorageUsedBytes)
	require.NotNil(t, m.StorageTotalBytes)
	require.NotNil(t, m.RecordingCount)
	require.NotNil(t, m.CameraErrors)
}

func TestNewMetricsRegistersGoCollector(t *testing.T) {
	t.Helper()
	m := NewMetrics()
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	found := false
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "go_") {
			found = true
			break
		}
	}
	require.True(t, found, "expected Go runtime metrics in registry")
}

func TestNewMetricsRegistersProcessCollector(t *testing.T) {
	t.Helper()
	m := NewMetrics()
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	found := false
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "nvr_process_") {
			found = true
			break
		}
	}
	require.True(t, found, "expected process collector metrics in registry")
}

func TestCounterInc(t *testing.T) {
	t.Helper()
	m := NewMetrics()
	m.RecordingBytesTotal.WithLabelValues("cam1", "h264").Inc()
	m.RecordingBytesTotal.WithLabelValues("cam1", "h264").Add(100)
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "nvr_recording_bytes_total" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(101), f.GetMetric()[0].GetCounter().GetValue())
			return
		}
	}
	t.Fatal("expected nvr_recording_bytes_total metric family")
}

func TestGaugeSet(t *testing.T) {
	t.Helper()
	m := NewMetrics()
	m.ActiveCameras.Set(42)
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "nvr_active_cameras" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(42), f.GetMetric()[0].GetGauge().GetValue())
			return
		}
	}
	t.Fatal("expected nvr_active_cameras metric family")
}

func TestLabeledCounter(t *testing.T) {
	t.Helper()
	m := NewMetrics()
	m.CameraErrors.WithLabelValues("cam1", "connection").Inc()
	m.CameraErrors.WithLabelValues("cam1", "decode").Inc()
	m.CameraErrors.WithLabelValues("cam2", "connection").Inc()
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "nvr_camera_errors_total" {
			// 3 distinct label combinations
			require.Len(t, f.GetMetric(), 3)
			return
		}
	}
	t.Fatal("expected nvr_camera_errors_total metric family")
}

func TestRegistryGather(t *testing.T) {
	t.Helper()
	m := NewMetrics()
	m.ActiveCameras.Set(5)
	m.StorageUsedBytes.Set(1024)
	m.SegmentsCreated.WithLabelValues("cam1", "h264").Inc()
	m.RecordingBytesTotal.WithLabelValues("cam1", "h264").Add(1)
	m.ActiveRecordings.Set(1)
	m.CleanupDeleted.WithLabelValues("retention").Inc()
	m.StorageTotalBytes.Set(2048)
	m.RecordingCount.Set(3)
m.CameraErrors.WithLabelValues("cam1", "timeout").Inc()
	m.HLSFramesDropped.WithLabelValues("cam1").Inc()

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, families)

	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}

	// Verify all custom metrics are registered
	require.True(t, names["nvr_active_cameras"])
	require.True(t, names["nvr_storage_used_bytes"])
	require.True(t, names["nvr_segments_created_total"])
	require.True(t, names["nvr_recording_bytes_total"])
	require.True(t, names["nvr_active_recordings"])
	require.True(t, names["nvr_cleanup_deleted_total"])
	require.True(t, names["nvr_storage_total_bytes"])
	require.True(t, names["nvr_recording_count"])
	require.True(t, names["nvr_camera_errors_total"])
	require.True(t, names["nvr_hls_frames_dropped_total"])
}

func TestHLSFramesDroppedCounter(t *testing.T) {
	t.Helper()
	m := NewMetrics()
	require.NotNil(t, m.HLSFramesDropped)

	m.HLSFramesDropped.WithLabelValues("cam1").Inc()
	m.HLSFramesDropped.WithLabelValues("cam1").Add(5)
	m.HLSFramesDropped.WithLabelValues("cam2").Inc()

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "nvr_hls_frames_dropped_total" {
			require.Len(t, f.GetMetric(), 2) // cam1 and cam2
			return
		}
	}
	t.Fatal("expected nvr_hls_frames_dropped_total metric family")
}

func TestNewStreamingMetrics(t *testing.T) {
	t.Helper()
	m := NewMetrics()
	require.NotNil(t, m.WebRTCActivePeers)
	require.NotNil(t, m.WebRTCFramesSent)
	require.NotNil(t, m.WebRTCFramesDropped)
	require.NotNil(t, m.FLVActiveStreams)
	require.NotNil(t, m.FLVFramesSent)
	require.NotNil(t, m.FLVFramesDropped)
	require.NotNil(t, m.FLVGOPCacheHits)
}

func TestNewMetricsRegistersStreamingMetrics(t *testing.T) {
	t.Helper()
	m := NewMetrics()

	// Touch all streaming metrics to ensure they appear in registry
	m.WebRTCActivePeers.WithLabelValues("cam1").Set(1)
	m.WebRTCFramesSent.WithLabelValues("cam1").Inc()
	m.WebRTCFramesDropped.WithLabelValues("cam1").Inc()
	m.FLVActiveStreams.WithLabelValues("cam1").Set(1)
	m.FLVFramesSent.WithLabelValues("cam1").Inc()
	m.FLVFramesDropped.WithLabelValues("cam1").Inc()
	m.FLVGOPCacheHits.WithLabelValues("cam1").Inc()

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}
	require.True(t, names["nvr_webrtc_active_peers"])
	require.True(t, names["nvr_webrtc_frames_sent_total"])
	require.True(t, names["nvr_webrtc_frames_dropped_total"])
	require.True(t, names["nvr_flv_active_streams"])
	require.True(t, names["nvr_flv_frames_sent_total"])
	require.True(t, names["nvr_flv_frames_dropped_total"])
	require.True(t, names["nvr_flv_gop_cache_hits_total"])
}
