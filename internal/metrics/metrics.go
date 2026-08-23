package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics holds all Prometheus collectors and a custom registry for the NVR.
type Metrics struct {
	Registry *prometheus.Registry

	RecordingBytesTotal          *prometheus.CounterVec // labels: camera_id, codec
	ActiveCameras                prometheus.Gauge
	ActiveRecordings             prometheus.Gauge
	SegmentsCreated              *prometheus.CounterVec // labels: camera_id, codec
	CleanupDeleted               *prometheus.CounterVec // labels: reason
	StorageUsedBytes             prometheus.Gauge
	StorageTotalBytes            prometheus.Gauge
	RecordingCount               prometheus.Gauge
	CameraErrors                 *prometheus.CounterVec   // labels: camera_id, error_type
	StorageWriteErrors           prometheus.Counter       // total storage write I/O errors
	HLSFramesDropped             *prometheus.CounterVec   // labels: camera_id
	HLSWriteErrors               *prometheus.CounterVec   // labels: camera_id
	HLSMuxerRestarts             *prometheus.CounterVec   // labels: camera_id
	HLSActiveStreams             *prometheus.GaugeVec     // labels: camera_id
	HLSSegmentSizeBytes          *prometheus.HistogramVec // labels: camera_id
	HLSIdleEvictions             *prometheus.CounterVec   // labels: camera_id
	WebRTCActivePeers            *prometheus.GaugeVec     // labels: camera_id
	WebRTCFramesSent             *prometheus.CounterVec   // labels: camera_id
	WebRTCFramesDropped          *prometheus.CounterVec   // labels: camera_id
	WebRTCConnectionStateChanges *prometheus.CounterVec   // labels: camera_id, state
	FLVActiveStreams             *prometheus.GaugeVec     // labels: camera_id
	FLVFramesSent                *prometheus.CounterVec   // labels: camera_id
	FLVFramesDropped             *prometheus.CounterVec   // labels: camera_id
	FLVGOPCacheHits              *prometheus.CounterVec   // labels: camera_id
	FLVGOPCacheMisses            *prometheus.CounterVec   // labels: camera_id
	XiaomiDisconnects            *prometheus.CounterVec   // labels: camera_id, reason
	XiaomiReconnects             *prometheus.CounterVec   // labels: camera_id
	TranscodingJobsTotal         *prometheus.CounterVec   // labels: codec_from, codec_to, encoder, crf, status
	TranscodingActiveJobs        prometheus.Gauge
	TranscodingDurationSeconds   *prometheus.HistogramVec // labels: codec_from, codec_to, encoder
	TranscodingBytesProcessed    prometheus.Counter
	TranscodingFFmpegStatus      prometheus.Gauge
	RemoteLogSentTotal           prometheus.Counter
	RemoteLogDroppedTotal        prometheus.Counter
	RemoteLogBatchSize           prometheus.Histogram
	StreamHubFramesDropped       *prometheus.CounterVec // labels: camera_id, consumer, is_idr
	StreamHubBufferDepth         *prometheus.GaugeVec   // labels: camera_id, consumer
	StreamHubFramesInTotal       *prometheus.CounterVec // labels: camera_id
	AudioFramesTotal             *prometheus.CounterVec // labels: camera_id, codec
	AudioFramesDroppedTotal      *prometheus.CounterVec // labels: camera_id
	// Periodic-flush hub metrics (#469): per-consumer sends/bytes/dwell are
	// accumulated in StreamHub atomics on the hot path and exported here by the
	// camera manager's flusher goroutine (never per-frame).
	StreamHubFramesSentTotal       *prometheus.CounterVec // labels: camera_id, consumer
	StreamHubBytesInTotal          *prometheus.CounterVec // labels: camera_id
	StreamHubHopDwellAvgMS         *prometheus.GaugeVec   // labels: camera_id, consumer
	StreamHubHopDwellMaxMS         *prometheus.GaugeVec   // labels: camera_id, consumer
	StreamHubDropRateExceededTotal *prometheus.CounterVec // labels: camera_id, consumer — drop rate crossed warn threshold
	WSActiveStreams                *prometheus.GaugeVec   // labels: camera_id
	WSFramesSent                   *prometheus.CounterVec // labels: camera_id
	WSFramesDropped                *prometheus.CounterVec // labels: camera_id
	JitterBufferDepth              *prometheus.GaugeVec   // labels: camera_id
	JitterBufferReordersTotal      *prometheus.CounterVec // labels: camera_id
	JitterBufferFlushesTotal       *prometheus.CounterVec // labels: camera_id
	RecorderRingBufferDropsTotal   *prometheus.CounterVec // labels: camera_id

	// Playback telemetry (#469 Phase 3) — aggregated from browser beacons
	// (POST /api/telemetry), the only source of true end-to-end live latency.
	PlaybackLiveLatencyMS *prometheus.GaugeVec   // labels: camera_id, protocol
	PlaybackStallsTotal   *prometheus.CounterVec // labels: camera_id, protocol

	// Segment write + audit metrics (#469 Phase 5)
	SegmentWriteDurationSeconds *prometheus.HistogramVec // labels: camera_id — SD-card degradation early warning
	RecordingAuditTotal         *prometheus.CounterVec   // labels: camera_id, result (ok|zero_duration|probe_error)
	RecordingDeepCheckTotal     *prometheus.CounterVec   // labels: camera_id, result (ok|decode_error) — ffmpeg decode sampling, ≤1/h/camera (#489)
	// Health→Prometheus bridge metrics (stream stats)
	StreamFPS                *prometheus.GaugeVec // labels: camera_id
	StreamBitrateKbps        *prometheus.GaugeVec // labels: camera_id
	StreamIDRIntervalSeconds *prometheus.GaugeVec // labels: camera_id
	// Camera connection metrics
	CameraConnectionErrorsTotal   *prometheus.CounterVec // labels: camera_id, error_type
	CameraReconnectAttemptsTotal  *prometheus.CounterVec // labels: camera_id
	CameraReconnectBackoffSeconds *prometheus.GaugeVec   // labels: camera_id
	// Merge metrics
	MergeAttemptsTotal   prometheus.Counter
	MergeSuccessesTotal  prometheus.Counter
	MergeFailuresTotal   *prometheus.CounterVec // labels: reason
	MergeDurationSeconds prometheus.Histogram
	MergeSizeBytes       prometheus.Histogram
	MergePendingSegments *prometheus.GaugeVec // labels: camera_id

	// Rolling merge metrics (quasi-real-time, event-driven)
	RollingMergeLatencySeconds *prometheus.HistogramVec // labels: camera_id — time from segment close to merge complete
	RollingMergeBucketSegments *prometheus.GaugeVec     // labels: camera_id — segments in current bucket

	// Auth metrics — track login attempts for security monitoring
	AuthAttemptsTotal    *prometheus.CounterVec // labels: result (success/failure/no_password)
	AuthRateLimitedTotal prometheus.Counter     // total requests blocked by rate limiter

	// AI event metrics — MiBeeVision collaboration (0.8.0)
	AIEventsReceivedTotal *prometheus.CounterVec // labels: camera_id, event_type
	AIEventsErrorsTotal   prometheus.Counter     // total write/processing errors

	// Timeline metrics — DVR-style recording browsing (0.8.0 M6)
	TimelineSeeksTotal *prometheus.CounterVec // labels: camera_id, type (segment/intra)

	// SQLite database health metrics
	SQLiteWALSizeBytes         prometheus.Gauge
	SQLiteDBSizeBytes          prometheus.Gauge
	SQLiteFragmentationRatio   prometheus.Gauge
	SQLiteQueryDurationSeconds *prometheus.HistogramVec // labels: query_name
	SQLiteBusyErrorsTotal      prometheus.Counter       // SQLITE_BUSY retries across all queries
	CleanupDurationSeconds     prometheus.Histogram
	SQLiteOpenConnections      prometheus.Gauge   // writer pool
	SQLiteInUseConnections     prometheus.Gauge   // writer pool
	SQLiteReadOpenConnections  prometheus.Gauge   // read pool (separate, query_only)
	SQLiteReadInUseConnections prometheus.Gauge   // read pool
	SQLiteReadWaitCount        prometheus.Counter // read pool: times a conn was unavailable
	SQLiteReadWaitDuration     prometheus.Gauge   // read pool: cumulative seconds waited for a conn

	// Codec probe metrics — observability for the H.265 codec-probe pipeline
	// (recorder probe → DB persist → /protocols → orchestrator → player). The
	// chain is the project's largest complexity/defect source (#112 black-screen
	// was a silent probe failure). These make probe outcome + latency visible to
	// Prometheus so stale-encoding issues surface before users see black video.
	CodecProbeTotal           *prometheus.CounterVec   // labels: camera_id, encoding, result (ok|fail|unsupported)
	CodecProbeDurationSeconds *prometheus.HistogramVec // labels: camera_id — RTSP DESCRIBE probe latency
	ResolvedEncoding          *prometheus.GaugeVec     // labels: camera_id, encoding — last persisted encoding (alert on drift)
}

// NewMetrics creates a new Metrics instance with a custom registry,
// Go runtime collectors (memstats only for RPi 3B), and all custom NVR metrics.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	reg.MustRegister(collectors.NewGoCollector(
		collectors.WithGoCollections(collectors.GoRuntimeMemStatsCollection),
	))
	reg.MustRegister(collectors.NewProcessCollector(
		collectors.ProcessCollectorOpts{
			Namespace: "nvr",
		},
	))

	recordingBytesTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_recording_bytes_total",
		Help: "Total bytes recorded, partitioned by camera and codec.",
	}, []string{"camera_id", "codec"})

	activeCameras := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_active_cameras",
		Help: "Number of currently active cameras.",
	})

	activeRecordings := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_active_recordings",
		Help: "Number of currently active recording sessions.",
	})

	segmentsCreated := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_segments_created_total",
		Help: "Total number of recording segments created, partitioned by camera and codec.",
	}, []string{"camera_id", "codec"})

	cleanupDeleted := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_cleanup_deleted_total",
		Help: "Total number of recordings deleted by cleanup, partitioned by reason.",
	}, []string{"reason"})

	storageUsedBytes := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_storage_used_bytes",
		Help: "Storage space used by recordings in bytes.",
	})

	storageTotalBytes := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_storage_total_bytes",
		Help: "Total storage space available in bytes.",
	})

	recordingCount := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_recording_count",
		Help: "Current number of recordings in the database.",
	})

	cameraErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_camera_errors_total",
		Help: "Total camera errors, partitioned by camera and error type.",
	}, []string{"camera_id", "error_type"})

	storageWriteErrors := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nvr_storage_write_errors_total",
		Help: "Total number of storage write I/O errors across all cameras.",
	})
	hlsFramesDropped := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_hls_frames_dropped_total",
		Help: "Total HLS frames dropped due to buffer full, partitioned by camera.",
	}, []string{"camera_id"})
	hlsWriteErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_hls_write_errors_total",
		Help: "Total HLS muxer write errors, partitioned by camera.",
	}, []string{"camera_id"})
	hlsMuxerRestarts := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_hls_muxer_restarts_total",
		Help: "Total HLS muxer restarts due to write errors, partitioned by camera.",
	}, []string{"camera_id"})
	hlsActiveStreams := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_hls_active_streams",
		Help: "Number of currently active HLS streams, partitioned by camera.",
	}, []string{"camera_id"})
	hlsSegmentSizeBytes := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nvr_hls_segment_size_bytes",
		Help:    "Size of HLS segments in bytes, partitioned by camera.",
		Buckets: []float64{64 * 1024, 128 * 1024, 256 * 1024, 512 * 1024, 1024 * 1024, 2 * 1024 * 1024, 4 * 1024 * 1024, 8 * 1024 * 1024, 16 * 1024 * 1024},
	}, []string{"camera_id"})
	hlsIdleEvictions := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_hls_idle_evictions_total",
		Help: "Total HLS streams evicted due to idle timeout, partitioned by camera.",
	}, []string{"camera_id"})

	webrtcActivePeers := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_webrtc_active_peers",
		Help: "Active WebRTC PeerConnections, partitioned by camera.",
	}, []string{"camera_id"})
	webrtcFramesSent := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_webrtc_frames_sent_total",
		Help: "Total WebRTC frames sent, partitioned by camera.",
	}, []string{"camera_id"})
	webrtcFramesDropped := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_webrtc_frames_dropped_total",
		Help: "Total WebRTC frames dropped due to buffer full, partitioned by camera.",
	}, []string{"camera_id"})

	webrtcConnectionStateChanges := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_webrtc_connection_state_changes_total",
		Help: "Total WebRTC connection state changes, partitioned by camera and state.",
	}, []string{"camera_id", "state"})
	flvActiveStreams := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_flv_active_streams",
		Help: "Active FLV streams, partitioned by camera.",
	}, []string{"camera_id"})
	flvFramesSent := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_flv_frames_sent_total",
		Help: "Total FLV frames sent, partitioned by camera.",
	}, []string{"camera_id"})
	flvFramesDropped := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_flv_frames_dropped_total",
		Help: "Total FLV frames dropped due to buffer full, partitioned by camera.",
	}, []string{"camera_id"})
	flvGOPCacheHits := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_flv_gop_cache_hits_total",
		Help: "Total FLV GOP cache hits, partitioned by camera.",
	}, []string{"camera_id"})

	flvGOPCacheMisses := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_flv_gop_cache_misses_total",
		Help: "Total FLV GOP cache misses (new viewer with no cached GOP), partitioned by camera.",
	}, []string{"camera_id"})

	xiaomiDisconnects := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_xiaomi_disconnects_total",
		Help: "Total Xiaomi camera disconnects, partitioned by camera and reason.",
	}, []string{"camera_id", "reason"})
	xiaomiReconnects := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_xiaomi_reconnects_total",
		Help: "Total Xiaomi camera reconnects, partitioned by camera.",
	}, []string{"camera_id"})

	transcodingJobsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_transcoding_jobs_total",
		Help: "Total number of transcoding jobs by codec conversion, encoder, crf and status",
	}, []string{"codec_from", "codec_to", "encoder", "crf", "status"})

	transcodingActiveJobs := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_transcoding_active_jobs",
		Help: "Number of currently active transcoding jobs",
	})

	transcodingDurationSeconds := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nvr_transcoding_duration_seconds",
		Help:    "Duration of transcoding jobs in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"codec_from", "codec_to", "encoder"})

	transcodingBytesProcessed := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nvr_transcoding_bytes_processed",
		Help: "Total bytes processed by transcoding jobs",
	})

	transcodingFFmpegStatus := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_transcoding_ffmpeg_status",
		Help: "FFmpeg status: 0=not_installed, 1=downloading, 2=available",
	})

	remoteLogSentTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nvr_remote_log_sent_total",
		Help: "Total number of successful remote log batch sends.",
	})
	remoteLogDroppedTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nvr_remote_log_dropped_total",
		Help: "Total number of remote log batches dropped due to send failure.",
	})
	remoteLogBatchSize := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "nvr_remote_log_batch_size",
		Help:    "Distribution of remote log batch sizes.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 8), // 1, 2, 4, 8, 16, 32, 64, 128
	})

	streamHubFramesDropped := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_streamhub_frames_dropped_total",
		Help: "Total StreamHub frames dropped due to buffer full, partitioned by camera, consumer, and whether it was an IDR frame.",
	}, []string{"camera_id", "consumer", "is_idr"})

	streamHubBufferDepth := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_streamhub_consumer_buffer_depth",
		Help: "Current buffer depth (number of frames) for each StreamHub consumer, partitioned by camera and consumer.",
	}, []string{"camera_id", "consumer"})

	streamHubFramesInTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_streamhub_frames_in_total",
		Help: "Total frames broadcast into StreamHub, partitioned by camera.",
	}, []string{"camera_id"})

	audioFramesTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_audio_frames_total",
		Help: "Total audio frames broadcast into StreamHub, partitioned by camera and codec.",
	}, []string{"camera_id", "codec"})

	audioFramesDroppedTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_audio_frames_dropped_total",
		Help: "Total audio frames dropped due to buffer overflow, partitioned by camera.",
	}, []string{"camera_id"})

	// Periodic-flush hub metrics (#469): the hot path only touches StreamHub
	// atomics; this camera-manager flusher exports them to Prometheus.
	streamHubFramesSentTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_streamhub_frames_sent_total",
		Help: "Total frames delivered to a hub consumer, partitioned by camera and consumer (periodic flush from hub atomics).",
	}, []string{"camera_id", "consumer"})

	streamHubBytesInTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_streamhub_bytes_in_total",
		Help: "Total video bytes broadcast into StreamHub, partitioned by camera (periodic flush from hub atomics).",
	}, []string{"camera_id"})

	streamHubHopDwellAvgMS := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_streamhub_hop_dwell_ms_avg",
		Help: "Average enqueue→drain dwell in a consumer's queue (ms), by camera and consumer.",
	}, []string{"camera_id", "consumer"})

	streamHubHopDwellMaxMS := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_streamhub_hop_dwell_ms_max",
		Help: "Maximum enqueue→drain dwell in a consumer's queue (ms), by camera and consumer.",
	}, []string{"camera_id", "consumer"})

	streamHubDropRateExceededTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_streamhub_drop_rate_exceeded_total",
		Help: "Times a consumer's drop rate crossed the warn threshold, partitioned by camera and consumer.",
	}, []string{"camera_id", "consumer"})

	wsActiveStreams := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_ws_active_streams",
		Help: "Active WebSocket streams, partitioned by camera.",
	}, []string{"camera_id"})

	wsFramesSent := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_ws_frames_sent_total",
		Help: "Total WebSocket frames sent, partitioned by camera.",
	}, []string{"camera_id"})

	wsFramesDropped := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_ws_frames_dropped_total",
		Help: "Total WebSocket frames dropped due to buffer full, partitioned by camera.",
	}, []string{"camera_id"})

	jitterBufferFlushesTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_jitter_buffer_flushes_total",
		Help: "Total jitter buffer flushes (capacity or timeout reached), partitioned by camera.",
	}, []string{"camera_id"})

	playbackLiveLatencyMS := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_playback_live_latency_ms",
		Help: "Player-reported end-to-end live latency (ms): hub ingest wallclock relayed via WS vs. browser clock, by camera and protocol.",
	}, []string{"camera_id", "protocol"})

	playbackStallsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_playback_stalls_total",
		Help: "Player-reported playback stalls (buffering/freezes), by camera and protocol.",
	}, []string{"camera_id", "protocol"})

	segmentWriteDurationSeconds := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nvr_segment_write_duration_seconds",
		Help:    "Duration of MP4 segment file writes, partitioned by camera — SD-card degradation early warning.",
		Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"camera_id"})

	recordingAuditTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_recording_audit_total",
		Help: "Recording integrity audit outcomes (mediaprobe on closed segments), by camera and result (ok|zero_duration|probe_error).",
	}, []string{"camera_id", "result"})

	recordingDeepCheckTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_recording_deepcheck_total",
		Help: "Decode-level deep check outcomes (ffmpeg -v error sampling, ≤1/hour/camera, #489), by camera and result (ok|decode_error). Absent entirely when no ffmpeg is configured.",
	}, []string{"camera_id", "result"})

	jitterBufferDepth := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_jitter_buffer_depth",
		Help: "Current number of frames in the jitter buffer, partitioned by camera.",
	}, []string{"camera_id"})

	jitterBufferReordersTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_jitter_buffer_reorders_total",
		Help: "Total number of out-of-order frames detected by the jitter buffer, partitioned by camera.",
	}, []string{"camera_id"})

	recorderRingBufferDropsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_recorder_ring_buffer_drops_total",
		Help: "Total frames dropped due to recorder ring buffer overflow, partitioned by camera.",
	}, []string{"camera_id"})

	// Health→Prometheus bridge gauges
	streamFPS := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_stream_fps",
		Help: "Current frames per second for a camera stream.",
	}, []string{"camera_id"})
	streamBitrateKbps := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_stream_bitrate_kbps",
		Help: "Current bitrate in kbps for a camera stream.",
	}, []string{"camera_id"})
	streamIDRIntervalSeconds := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_stream_idr_interval_seconds",
		Help: "Seconds since last IDR frame for a camera stream.",
	}, []string{"camera_id"})
	// Camera connection metrics
	cameraConnectionErrorsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_camera_connection_errors_total",
		Help: "Total camera connection errors, partitioned by camera and error type (timeout, auth, network, unknown).",
	}, []string{"camera_id", "error_type"})
	cameraReconnectAttemptsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_camera_reconnect_attempts_total",
		Help: "Total camera reconnection attempts, partitioned by camera.",
	}, []string{"camera_id"})
	cameraReconnectBackoffSeconds := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_camera_reconnect_backoff_seconds",
		Help: "Current reconnect backoff duration in seconds for a camera.",
	}, []string{"camera_id"})

	mergeAttemptsTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nvr_merge_attempts_total",
		Help: "Total number of merge attempts.",
	})
	mergeSuccessesTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nvr_merge_successes_total",
		Help: "Total number of successful merges.",
	})
	mergeFailuresTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_merge_failures_total",
		Help: "Total number of failed merges, partitioned by reason.",
	}, []string{"reason"})
	mergeDurationSeconds := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "nvr_merge_duration_seconds",
		Help:    "Duration of merge operations in seconds.",
		Buckets: []float64{0.5, 1, 5, 10, 30, 60, 300, 600},
	})
	mergeSizeBytes := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "nvr_merge_size_bytes",
		Help:    "Size of merged output in bytes.",
		Buckets: []float64{10485760, 52428800, 104857600, 524288000, 1073741824, 3221225472},
	})
	mergePendingSegments := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_merge_pending_segments",
		Help: "Number of segments pending merge, partitioned by camera.",
	}, []string{"camera_id"})

	// Rolling merge metrics (quasi-real-time, event-driven)
	rollingMergeLatencySeconds := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nvr_rolling_merge_latency_seconds",
		Help:    "Time from segment close to rolling merge completion, partitioned by camera.",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30},
	}, []string{"camera_id"})
	rollingMergeBucketSegments := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_rolling_merge_bucket_segments",
		Help: "Number of segments accumulated in the current rolling merge window bucket.",
	}, []string{"camera_id"})

	// Auth metrics — track login attempts for security monitoring
	authAttemptsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_auth_attempts_total",
		Help: "Total authentication attempts, partitioned by result.",
	}, []string{"result"})
	authRateLimitedTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nvr_auth_rate_limited_total",
		Help: "Total requests blocked by auth rate limiter.",
	})

	// AI event metrics — MiBeeVision collaboration (0.8.0)
	aiEventsReceivedTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_ai_events_received_total",
		Help: "Total AI events received from MiBeeVision, partitioned by camera and event type.",
	}, []string{"camera_id", "event_type"})
	aiEventsErrorsTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nvr_ai_events_errors_total",
		Help: "Total errors when receiving or processing AI events.",
	})

	// Timeline seek metrics — DVR-style recording browsing (0.8.0 M6)
	timelineSeeksTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_timeline_seeks_total",
		Help: "Total timeline seek operations, partitioned by camera and seek type.",
	}, []string{"camera_id", "type"})

	// SQLite database health metrics
	sqliteWALSizeBytes := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_sqlite_wal_size_bytes",
		Help: "SQLite WAL file size in bytes.",
	})
	sqliteDBSizeBytes := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_sqlite_db_size_bytes",
		Help: "SQLite database file size in bytes.",
	})
	sqliteFragmentationRatio := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_sqlite_fragmentation_ratio",
		Help: "SQLite fragmentation ratio (freelist_count / page_count).",
	})
	sqliteQueryDurationSeconds := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nvr_sqlite_query_duration_seconds",
		Help:    "SQLite query duration in seconds, partitioned by query name.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"query_name"})
	sqliteBusyErrorsTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nvr_sqlite_busy_errors_total",
		Help: "Total SQLITE_BUSY errors retried across all database operations.",
	})
	cleanupDurationSeconds := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "nvr_cleanup_duration_seconds",
		Help:    "Cleanup cycle duration in seconds.",
		Buckets: []float64{1, 5, 10, 30, 60, 300, 600},
	})
	sqliteOpenConnections := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_sqlite_open_connections",
		Help: "SQLite open connections from writer connection pool.",
	})
	sqliteInUseConnections := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_sqlite_in_use_connections",
		Help: "SQLite in-use connections from writer connection pool.",
	})
	sqliteReadOpenConnections := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_sqlite_read_open_connections",
		Help: "SQLite open connections from the read-only pool (query_only, concurrent with writer under WAL).",
	})
	sqliteReadInUseConnections := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_sqlite_read_in_use_connections",
		Help: "SQLite in-use connections from the read-only pool.",
	})
	sqliteReadWaitCount := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nvr_sqlite_read_wait_count_total",
		Help: "Total number of times the read pool had no connection available and the caller waited. Nonzero sustained growth means SetReadPoolSize should be raised.",
	})
	sqliteReadWaitDuration := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nvr_sqlite_read_wait_duration_seconds",
		Help: "Total seconds callers waited for a read-pool connection (cumulative since start).",
	})

	// Codec probe metrics — see the Metrics struct comment for rationale (#140).
	codecProbeTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_codec_probe_total",
		Help: "Total codec probes by camera, resolved encoding, and result. result=ok: live RTSP DESCRIBE resolved the codec; unsupported: probe empty, fell back to a claimed/default encoding; fail: probe empty AND no fallback (defaulted to H264 — highest stale-encoding risk, see #112).",
	}, []string{"camera_id", "encoding", "result"})
	codecProbeDurationSeconds := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nvr_codec_probe_duration_seconds",
		Help:    "Latency of the RTSP DESCRIBE codec probe in seconds, partitioned by camera. High p99 indicates slow/unreachable devices.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"camera_id"})
	resolvedEncoding := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvr_resolved_encoding",
		Help: "Last codec resolved for a camera and persisted to DB (value is always 1; the encoding label is the signal). Compare against the recorder's live codec to detect stale persisted encoding — a persistent mismatch indicates a persistence bug.",
	}, []string{"camera_id", "encoding"})

	reg.MustRegister(
		recordingBytesTotal,
		activeCameras,
		activeRecordings,
		segmentsCreated,
		cleanupDeleted,
		storageUsedBytes,
		storageTotalBytes,
		recordingCount,
		cameraErrors,
		storageWriteErrors,
		hlsFramesDropped,
		hlsWriteErrors,
		hlsMuxerRestarts,
		hlsActiveStreams,
		hlsSegmentSizeBytes,
		hlsIdleEvictions,
		webrtcActivePeers,
		webrtcFramesSent,
		webrtcFramesDropped,
		webrtcConnectionStateChanges,
		flvActiveStreams,
		flvFramesSent,
		flvFramesDropped,
		flvGOPCacheHits,
		flvGOPCacheMisses,
		streamHubFramesSentTotal,
		streamHubBytesInTotal,
		streamHubHopDwellAvgMS,
		streamHubHopDwellMaxMS,
		streamHubDropRateExceededTotal,
		wsActiveStreams,
		wsFramesSent,
		wsFramesDropped,
		jitterBufferFlushesTotal,
		playbackLiveLatencyMS,
		playbackStallsTotal,
		segmentWriteDurationSeconds,
		recordingAuditTotal,
		recordingDeepCheckTotal,
		xiaomiDisconnects,
		xiaomiReconnects,
		transcodingJobsTotal,
		transcodingActiveJobs,
		transcodingDurationSeconds,
		transcodingBytesProcessed,
		transcodingFFmpegStatus,
		remoteLogSentTotal,
		remoteLogDroppedTotal,
		remoteLogBatchSize,
		streamHubFramesDropped,
		streamHubBufferDepth,
		streamHubFramesInTotal,
		audioFramesTotal,
		audioFramesDroppedTotal,
		jitterBufferDepth,
		jitterBufferReordersTotal,
		recorderRingBufferDropsTotal,
		streamFPS,
		streamBitrateKbps,
		streamIDRIntervalSeconds,
		cameraConnectionErrorsTotal,
		cameraReconnectAttemptsTotal,
		cameraReconnectBackoffSeconds,
		mergeAttemptsTotal,
		mergeSuccessesTotal,
		mergeFailuresTotal,
		mergeDurationSeconds,
		mergeSizeBytes,
		mergePendingSegments,
		rollingMergeLatencySeconds,
		rollingMergeBucketSegments,
		authAttemptsTotal,
		authRateLimitedTotal,
		aiEventsReceivedTotal,
		aiEventsErrorsTotal,
		timelineSeeksTotal,
		sqliteWALSizeBytes,
		sqliteDBSizeBytes,
		sqliteFragmentationRatio,
		sqliteQueryDurationSeconds,
		sqliteBusyErrorsTotal,
		cleanupDurationSeconds,
		sqliteOpenConnections,
		sqliteInUseConnections,
		sqliteReadOpenConnections,
		sqliteReadInUseConnections,
		sqliteReadWaitCount,
		sqliteReadWaitDuration,
		codecProbeTotal,
		codecProbeDurationSeconds,
		resolvedEncoding,
	)

	return &Metrics{
		Registry:                       reg,
		RecordingBytesTotal:            recordingBytesTotal,
		ActiveCameras:                  activeCameras,
		ActiveRecordings:               activeRecordings,
		SegmentsCreated:                segmentsCreated,
		CleanupDeleted:                 cleanupDeleted,
		StorageUsedBytes:               storageUsedBytes,
		StorageTotalBytes:              storageTotalBytes,
		RecordingCount:                 recordingCount,
		CameraErrors:                   cameraErrors,
		StorageWriteErrors:             storageWriteErrors,
		HLSFramesDropped:               hlsFramesDropped,
		HLSWriteErrors:                 hlsWriteErrors,
		HLSMuxerRestarts:               hlsMuxerRestarts,
		HLSActiveStreams:               hlsActiveStreams,
		HLSSegmentSizeBytes:            hlsSegmentSizeBytes,
		HLSIdleEvictions:               hlsIdleEvictions,
		WebRTCActivePeers:              webrtcActivePeers,
		WebRTCFramesSent:               webrtcFramesSent,
		WebRTCFramesDropped:            webrtcFramesDropped,
		WebRTCConnectionStateChanges:   webrtcConnectionStateChanges,
		FLVActiveStreams:               flvActiveStreams,
		FLVFramesSent:                  flvFramesSent,
		FLVFramesDropped:               flvFramesDropped,
		FLVGOPCacheHits:                flvGOPCacheHits,
		FLVGOPCacheMisses:              flvGOPCacheMisses,
		XiaomiDisconnects:              xiaomiDisconnects,
		XiaomiReconnects:               xiaomiReconnects,
		TranscodingJobsTotal:           transcodingJobsTotal,
		TranscodingActiveJobs:          transcodingActiveJobs,
		TranscodingDurationSeconds:     transcodingDurationSeconds,
		TranscodingBytesProcessed:      transcodingBytesProcessed,
		TranscodingFFmpegStatus:        transcodingFFmpegStatus,
		RemoteLogSentTotal:             remoteLogSentTotal,
		RemoteLogDroppedTotal:          remoteLogDroppedTotal,
		RemoteLogBatchSize:             remoteLogBatchSize,
		StreamHubFramesDropped:         streamHubFramesDropped,
		StreamHubBufferDepth:           streamHubBufferDepth,
		StreamHubFramesInTotal:         streamHubFramesInTotal,
		AudioFramesTotal:               audioFramesTotal,
		AudioFramesDroppedTotal:        audioFramesDroppedTotal,
		StreamHubFramesSentTotal:       streamHubFramesSentTotal,
		StreamHubBytesInTotal:          streamHubBytesInTotal,
		StreamHubHopDwellAvgMS:         streamHubHopDwellAvgMS,
		StreamHubHopDwellMaxMS:         streamHubHopDwellMaxMS,
		StreamHubDropRateExceededTotal: streamHubDropRateExceededTotal,
		WSActiveStreams:                wsActiveStreams,
		WSFramesSent:                   wsFramesSent,
		WSFramesDropped:                wsFramesDropped,
		JitterBufferFlushesTotal:       jitterBufferFlushesTotal,
		PlaybackLiveLatencyMS:          playbackLiveLatencyMS,
		PlaybackStallsTotal:            playbackStallsTotal,
		SegmentWriteDurationSeconds:    segmentWriteDurationSeconds,
		RecordingAuditTotal:            recordingAuditTotal,
		RecordingDeepCheckTotal:        recordingDeepCheckTotal,
		JitterBufferDepth:              jitterBufferDepth,
		JitterBufferReordersTotal:      jitterBufferReordersTotal,
		RecorderRingBufferDropsTotal:   recorderRingBufferDropsTotal,
		StreamFPS:                      streamFPS,
		StreamBitrateKbps:              streamBitrateKbps,
		StreamIDRIntervalSeconds:       streamIDRIntervalSeconds,
		CameraConnectionErrorsTotal:    cameraConnectionErrorsTotal,
		CameraReconnectAttemptsTotal:   cameraReconnectAttemptsTotal,
		CameraReconnectBackoffSeconds:  cameraReconnectBackoffSeconds,
		MergeAttemptsTotal:             mergeAttemptsTotal,
		MergeSuccessesTotal:            mergeSuccessesTotal,
		MergeFailuresTotal:             mergeFailuresTotal,
		MergeDurationSeconds:           mergeDurationSeconds,
		MergeSizeBytes:                 mergeSizeBytes,
		MergePendingSegments:           mergePendingSegments,
		RollingMergeLatencySeconds:     rollingMergeLatencySeconds,
		RollingMergeBucketSegments:     rollingMergeBucketSegments,
		AuthAttemptsTotal:              authAttemptsTotal,
		AuthRateLimitedTotal:           authRateLimitedTotal,
		AIEventsReceivedTotal:          aiEventsReceivedTotal,
		AIEventsErrorsTotal:            aiEventsErrorsTotal,
		TimelineSeeksTotal:             timelineSeeksTotal,
		SQLiteWALSizeBytes:             sqliteWALSizeBytes,
		SQLiteDBSizeBytes:              sqliteDBSizeBytes,
		SQLiteFragmentationRatio:       sqliteFragmentationRatio,
		SQLiteQueryDurationSeconds:     sqliteQueryDurationSeconds,
		SQLiteBusyErrorsTotal:          sqliteBusyErrorsTotal,
		CleanupDurationSeconds:         cleanupDurationSeconds,
		SQLiteOpenConnections:          sqliteOpenConnections,
		SQLiteInUseConnections:         sqliteInUseConnections,
		SQLiteReadOpenConnections:      sqliteReadOpenConnections,
		SQLiteReadInUseConnections:     sqliteReadInUseConnections,
		SQLiteReadWaitCount:            sqliteReadWaitCount,
		SQLiteReadWaitDuration:         sqliteReadWaitDuration,
		CodecProbeTotal:                codecProbeTotal,
		CodecProbeDurationSeconds:      codecProbeDurationSeconds,
		ResolvedEncoding:               resolvedEncoding,
	}
}

// ObserveQueryDuration implements storage.QueryMetrics, recording a query latency into
// the nvr_sqlite_query_duration_seconds histogram. Called from hot DB methods.
func (m *Metrics) ObserveQueryDuration(queryName string, seconds float64) {
	if m == nil || m.SQLiteQueryDurationSeconds == nil {
		return
	}
	m.SQLiteQueryDurationSeconds.WithLabelValues(queryName).Observe(seconds)
}

// IncSQLiteBusyErrors implements storage.QueryMetrics, incrementing the
// nvr_sqlite_busy_errors_total counter. Called from RetryOnBusy on each SQLITE_BUSY retry.
func (m *Metrics) IncSQLiteBusyErrors() {
	if m == nil || m.SQLiteBusyErrorsTotal == nil {
		return
	}
	m.SQLiteBusyErrorsTotal.Inc()
}

// RecordMergeSuccess records a successful merge operation.
func (m *Metrics) RecordMergeSuccess(duration time.Duration, size int64) {
	if m == nil {
		return
	}
	m.MergeAttemptsTotal.Inc()
	m.MergeSuccessesTotal.Inc()
	m.MergeDurationSeconds.Observe(duration.Seconds())
	m.MergeSizeBytes.Observe(float64(size))
}

// RecordMergeFailure records a failed merge operation with the given reason.
func (m *Metrics) RecordMergeFailure(reason string) {
	if m == nil {
		return
	}
	m.MergeAttemptsTotal.Inc()
	m.MergeFailuresTotal.WithLabelValues(reason).Inc()
}

// UpdateMergePending updates the pending segment count gauge for a camera.
func (m *Metrics) UpdateMergePending(cameraID string, count float64) {
	if m == nil {
		return
	}
	m.MergePendingSegments.WithLabelValues(cameraID).Set(count)
}

// RecordRollingMergeLatency records the end-to-end latency of a rolling merge
// (segment close → merge complete) for a camera.
func (m *Metrics) RecordRollingMergeLatency(cameraID string, latency time.Duration) {
	if m == nil || m.RollingMergeLatencySeconds == nil {
		return
	}
	m.RollingMergeLatencySeconds.WithLabelValues(cameraID).Observe(latency.Seconds())
}

// UpdateRollingMergeBucketSegments sets the current segment count in a camera's
// active rolling merge window bucket.
func (m *Metrics) UpdateRollingMergeBucketSegments(cameraID string, count int) {
	if m == nil || m.RollingMergeBucketSegments == nil {
		return
	}
	m.RollingMergeBucketSegments.WithLabelValues(cameraID).Set(float64(count))
}

// IncStorageWriteErrors increments the storage write errors counter.
func (m *Metrics) IncStorageWriteErrors() {
	if m == nil {
		return
	}
	m.StorageWriteErrors.Inc()
}

// SetPlaybackLiveLatency records a player-reported end-to-end live latency
// (ms) for a camera+protocol, aggregated from /api/telemetry beacons.
func (m *Metrics) SetPlaybackLiveLatency(cameraID, protocol string, latencyMS float64) {
	if m == nil || m.PlaybackLiveLatencyMS == nil {
		return
	}
	m.PlaybackLiveLatencyMS.WithLabelValues(cameraID, protocol).Set(latencyMS)
}

// IncPlaybackStall records a player-reported playback stall (buffering/freeze).
func (m *Metrics) IncPlaybackStall(cameraID, protocol string) {
	if m == nil || m.PlaybackStallsTotal == nil {
		return
	}
	m.PlaybackStallsTotal.WithLabelValues(cameraID, protocol).Inc()
}

// ObserveSegmentWrite records the wall time to persist one MP4 segment —
// sustained growth is the SD-card/USB degradation early-warning signal.
func (m *Metrics) ObserveSegmentWrite(cameraID string, d time.Duration) {
	if m == nil || m.SegmentWriteDurationSeconds == nil {
		return
	}
	m.SegmentWriteDurationSeconds.WithLabelValues(cameraID).Observe(d.Seconds())
}

// IncRecordingAudit records one recording-integrity audit outcome
// (ok | zero_duration | probe_error).
func (m *Metrics) IncRecordingAudit(cameraID, result string) {
	if m == nil || m.RecordingAuditTotal == nil {
		return
	}
	m.RecordingAuditTotal.WithLabelValues(cameraID, result).Inc()
}

// IncRecordingDeepCheck records one decode-level deep check outcome
// (ok | decode_error) — ffmpeg sampling per #489.
func (m *Metrics) IncRecordingDeepCheck(cameraID, result string) {
	if m == nil || m.RecordingDeepCheckTotal == nil {
		return
	}
	m.RecordingDeepCheckTotal.WithLabelValues(cameraID, result).Inc()
}
