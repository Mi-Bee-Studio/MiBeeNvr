package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewMetrics(t *testing.T) {
	t.Helper()
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	m := NewMetrics()
	require.NotNil(t, m.WebRTCActivePeers)
	require.NotNil(t, m.WebRTCFramesSent)
	require.NotNil(t, m.WebRTCFramesDropped)
	require.NotNil(t, m.FLVActiveStreams)
	require.NotNil(t, m.FLVFramesSent)
	require.NotNil(t, m.FLVFramesDropped)
	require.NotNil(t, m.FLVGOPCacheHits)
	require.NotNil(t, m.FLVGOPCacheMisses)
}

func TestNewMetricsRegistersStreamingMetrics(t *testing.T) {
	t.Helper()
	t.Parallel()
	m := NewMetrics()

	// Touch all streaming metrics to ensure they appear in registry
	m.WebRTCActivePeers.WithLabelValues("cam1").Set(1)
	m.WebRTCFramesSent.WithLabelValues("cam1").Inc()
	m.WebRTCFramesDropped.WithLabelValues("cam1").Inc()
	m.FLVActiveStreams.WithLabelValues("cam1").Set(1)
	m.FLVFramesSent.WithLabelValues("cam1").Inc()
	m.FLVFramesDropped.WithLabelValues("cam1").Inc()
	m.FLVGOPCacheHits.WithLabelValues("cam1").Inc()
	m.FLVGOPCacheMisses.WithLabelValues("cam1").Inc()

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
	require.True(t, names["nvr_flv_gop_cache_misses_total"])
}

func TestTranscodingMetrics_Registration(t *testing.T) {
	t.Helper()
	t.Parallel()
	m := NewMetrics()
	require.NotNil(t, m.TranscodingJobsTotal)
	require.NotNil(t, m.TranscodingActiveJobs)
	require.NotNil(t, m.TranscodingDurationSeconds)
	require.NotNil(t, m.TranscodingBytesProcessed)
	require.NotNil(t, m.TranscodingFFmpegStatus)
}

func TestTranscodingMetrics_CounterIncrement(t *testing.T) {
	t.Helper()
	t.Parallel()
	m := NewMetrics()
	m.TranscodingJobsTotal.WithLabelValues("h264", "hevc", "libx265", "28", "completed").Inc()
	m.TranscodingJobsTotal.WithLabelValues("h264", "hevc", "libx265", "28", "completed").Add(4)
	m.TranscodingJobsTotal.WithLabelValues("hevc", "h264", "libx264", "23", "failed").Inc()

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "nvr_transcoding_jobs_total" {
			require.Len(t, f.GetMetric(), 2)
			return
		}
	}
	t.Fatal("expected nvr_transcoding_jobs_total metric family")
}

func TestTranscodingMetrics_GaugeUpdate(t *testing.T) {
	t.Helper()
	t.Parallel()
	m := NewMetrics()
	m.TranscodingActiveJobs.Set(3)

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "nvr_transcoding_active_jobs" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(3), f.GetMetric()[0].GetGauge().GetValue())
			return
		}
	}
	t.Fatal("expected nvr_transcoding_active_jobs metric family")
}

func TestTranscodingMetrics_HistogramObserve(t *testing.T) {
	t.Helper()
	t.Parallel()
	m := NewMetrics()
	m.TranscodingDurationSeconds.WithLabelValues("h264", "hevc", "libx265").Observe(42.5)

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "nvr_transcoding_duration_seconds" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, uint64(1), f.GetMetric()[0].GetHistogram().GetSampleCount())
			return
		}
	}
	t.Fatal("expected nvr_transcoding_duration_seconds metric family")
}

func TestTranscodingMetrics_BytesCounter(t *testing.T) {
	t.Helper()
	t.Parallel()
	m := NewMetrics()
	m.TranscodingBytesProcessed.Add(1048576)

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "nvr_transcoding_bytes_processed" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(1048576), f.GetMetric()[0].GetCounter().GetValue())
			return
		}
	}
	t.Fatal("expected nvr_transcoding_bytes_processed metric family")
}

func TestTranscodingMetrics_FFmpegStatus(t *testing.T) {
	t.Helper()
	t.Parallel()
	m := NewMetrics()

	// status 0 = not_installed, 1 = downloading, 2 = available
	m.TranscodingFFmpegStatus.Set(0)
	m.TranscodingFFmpegStatus.Set(1)
	m.TranscodingFFmpegStatus.Set(2)

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "nvr_transcoding_ffmpeg_status" {
			// gauge always overwrites, so only 1 value (the last set)
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(2), f.GetMetric()[0].GetGauge().GetValue())
			return
		}
	}
	t.Fatal("expected nvr_transcoding_ffmpeg_status metric family")
}

func TestStreamMetrics_Registration(t *testing.T) {
	t.Helper()
	t.Parallel()
	m := NewMetrics()
	require.NotNil(t, m.StreamFPS)
	require.NotNil(t, m.StreamBitrateKbps)
	require.NotNil(t, m.StreamIDRIntervalSeconds)
}

func TestStreamMetrics_GaugeSet(t *testing.T) {
	t.Helper()
	t.Parallel()
	m := NewMetrics()
	m.StreamFPS.WithLabelValues("cam1").Set(25.5)
	m.StreamBitrateKbps.WithLabelValues("cam1").Set(2048.0)
	m.StreamIDRIntervalSeconds.WithLabelValues("cam1").Set(2.0)

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}
	require.True(t, names["nvr_stream_fps"])
	require.True(t, names["nvr_stream_bitrate_kbps"])
	require.True(t, names["nvr_stream_idr_interval_seconds"])
}

func TestCameraConnectionMetrics_Registration(t *testing.T) {
	t.Helper()
	t.Parallel()
	m := NewMetrics()
	require.NotNil(t, m.CameraConnectionErrorsTotal)
	require.NotNil(t, m.CameraReconnectAttemptsTotal)
	require.NotNil(t, m.CameraReconnectBackoffSeconds)
}

func TestCameraConnectionMetrics_CounterInc(t *testing.T) {
	t.Helper()
	t.Parallel()
	m := NewMetrics()
	m.CameraConnectionErrorsTotal.WithLabelValues("cam1", "timeout").Inc()
	m.CameraConnectionErrorsTotal.WithLabelValues("cam1", "auth").Inc()
	m.CameraConnectionErrorsTotal.WithLabelValues("cam2", "network").Inc()
	m.CameraReconnectAttemptsTotal.WithLabelValues("cam1").Inc()
	m.CameraReconnectAttemptsTotal.WithLabelValues("cam1").Add(4)
	m.CameraReconnectBackoffSeconds.WithLabelValues("cam1").Set(5.0)

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}
	require.True(t, names["nvr_camera_connection_errors_total"])
	require.True(t, names["nvr_camera_reconnect_attempts_total"])
	require.True(t, names["nvr_camera_reconnect_backoff_seconds"])

	// Verify counter values
	for _, f := range families {
		if f.GetName() == "nvr_camera_connection_errors_total" {
			require.Len(t, f.GetMetric(), 3) // 3 distinct label combos
			return
		}
	}
	t.Fatal("expected nvr_camera_connection_errors_total metric family")
}

func TestMergeMetrics_Registration(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	require.NotNil(t, m.MergeAttemptsTotal)
	require.NotNil(t, m.MergeSuccessesTotal)
	require.NotNil(t, m.MergeFailuresTotal)
	require.NotNil(t, m.MergeDurationSeconds)
	require.NotNil(t, m.MergeSizeBytes)
	require.NotNil(t, m.MergePendingSegments)
}

func TestMergeMetrics_RecordMergeSuccess(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.RecordMergeSuccess(5*time.Second, 10485760)

	families, err := m.Registry.Gather()
	require.NoError(t, err)

	// verify nvr_merge_attempts_total incremented
	found := false
	for _, f := range families {
		if f.GetName() == "nvr_merge_attempts_total" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(1), f.GetMetric()[0].GetCounter().GetValue())
			found = true
			break
		}
	}
	require.True(t, found, "expected nvr_merge_attempts_total metric family")

	// verify nvr_merge_successes_total incremented
	found = false
	for _, f := range families {
		if f.GetName() == "nvr_merge_successes_total" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(1), f.GetMetric()[0].GetCounter().GetValue())
			found = true
			break
		}
	}
	require.True(t, found, "expected nvr_merge_successes_total metric family")

	// verify nvr_merge_duration_seconds histogram observed
	found = false
	for _, f := range families {
		if f.GetName() == "nvr_merge_duration_seconds" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, uint64(1), f.GetMetric()[0].GetHistogram().GetSampleCount())
			found = true
			break
		}
	}
	require.True(t, found, "expected nvr_merge_duration_seconds metric family")

	// verify nvr_merge_size_bytes histogram observed
	found = false
	for _, f := range families {
		if f.GetName() == "nvr_merge_size_bytes" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, uint64(1), f.GetMetric()[0].GetHistogram().GetSampleCount())
			found = true
			break
		}
	}
	require.True(t, found, "expected nvr_merge_size_bytes metric family")
}

func TestMergeMetrics_RecordMergeFailure(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.RecordMergeFailure("parse_error")

	families, err := m.Registry.Gather()
	require.NoError(t, err)

	// verify nvr_merge_attempts_total incremented
	found := false
	for _, f := range families {
		if f.GetName() == "nvr_merge_attempts_total" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(1), f.GetMetric()[0].GetCounter().GetValue())
			found = true
			break
		}
	}
	require.True(t, found, "expected nvr_merge_attempts_total metric family")

	// verify nvr_merge_failures_total with reason label
	found = false
	for _, f := range families {
		if f.GetName() == "nvr_merge_failures_total" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, "parse_error", f.GetMetric()[0].GetLabel()[0].GetValue())
			require.Equal(t, float64(1), f.GetMetric()[0].GetCounter().GetValue())
			found = true
			break
		}
	}
	require.True(t, found, "expected nvr_merge_failures_total metric family")
}

func TestMergeMetrics_UpdateMergePending(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.UpdateMergePending("cam-1", 5.0)

	families, err := m.Registry.Gather()
	require.NoError(t, err)

	found := false
	for _, f := range families {
		if f.GetName() == "nvr_merge_pending_segments" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, "cam-1", f.GetMetric()[0].GetLabel()[0].GetValue())
			require.Equal(t, float64(5.0), f.GetMetric()[0].GetGauge().GetValue())
			found = true
			break
		}
	}
	require.True(t, found, "expected nvr_merge_pending_segments metric family")
}

func TestMergeMetrics_NilSafety(t *testing.T) {
	t.Parallel()
	var m *Metrics
	// All three methods must not panic on nil receiver
	m.RecordMergeSuccess(time.Second, 1024)
	m.RecordMergeFailure("some_error")
	m.UpdateMergePending("cam-1", 1.0)
}

func TestMergeMetrics_FailureReasons(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.RecordMergeFailure("parse_error")
	m.RecordMergeFailure("io_error")
	m.RecordMergeFailure("parse_error")
	m.RecordMergeFailure("timeout")

	families, err := m.Registry.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() == "nvr_merge_failures_total" {
			require.Len(t, f.GetMetric(), 3)
			for _, metric := range f.GetMetric() {
				reason := metric.GetLabel()[0].GetValue()
				val := metric.GetCounter().GetValue()
				switch reason {
				case "parse_error":
					require.Equal(t, float64(2), val)
				case "io_error":
					require.Equal(t, float64(1), val)
				case "timeout":
					require.Equal(t, float64(1), val)
				default:
					t.Fatalf("unexpected reason label: %s", reason)
				}
			}
			return
		}
	}
	t.Fatal("expected nvr_merge_failures_total metric family")
}
