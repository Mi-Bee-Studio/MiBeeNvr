package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics holds all Prometheus collectors and a custom registry for the NVR.
type Metrics struct {
	Registry *prometheus.Registry

	RecordingBytesTotal *prometheus.CounterVec // labels: camera_id, codec
	ActiveCameras      prometheus.Gauge
	ActiveRecordings   prometheus.Gauge
	SegmentsCreated    *prometheus.CounterVec // labels: camera_id, codec
	CleanupDeleted     *prometheus.CounterVec // labels: reason
	StorageUsedBytes   prometheus.Gauge
	StorageTotalBytes  prometheus.Gauge
	RecordingCount     prometheus.Gauge
	CameraErrors       *prometheus.CounterVec // labels: camera_id, error_type
	HLSFramesDropped  *prometheus.CounterVec // labels: camera_id
	WebRTCActivePeers   *prometheus.GaugeVec   // labels: camera_id
	WebRTCFramesSent    *prometheus.CounterVec // labels: camera_id
	WebRTCFramesDropped *prometheus.CounterVec // labels: camera_id
	FLVActiveStreams    *prometheus.GaugeVec   // labels: camera_id
	FLVFramesSent       *prometheus.CounterVec // labels: camera_id
	FLVFramesDropped    *prometheus.CounterVec // labels: camera_id
	FLVGOPCacheHits         *prometheus.CounterVec // labels: camera_id
	XiaomiDisconnects       *prometheus.CounterVec // labels: camera_id, reason
	XiaomiReconnects        *prometheus.CounterVec // labels: camera_id
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
	hlsFramesDropped := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_hls_frames_dropped_total",
		Help: "Total HLS frames dropped due to buffer full, partitioned by camera.",
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

	xiaomiDisconnects := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_xiaomi_disconnects_total",
		Help: "Total Xiaomi camera disconnects, partitioned by camera and reason.",
	}, []string{"camera_id", "reason"})
	xiaomiReconnects := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvr_xiaomi_reconnects_total",
		Help: "Total Xiaomi camera reconnects, partitioned by camera.",
	}, []string{"camera_id"})
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
		hlsFramesDropped,
		webrtcActivePeers,
		webrtcFramesSent,
		webrtcFramesDropped,
		flvActiveStreams,
		flvFramesSent,
		flvFramesDropped,
		flvGOPCacheHits,
		xiaomiDisconnects,
		xiaomiReconnects,
	)

	return &Metrics{
		Registry:            reg,
		RecordingBytesTotal: recordingBytesTotal,
		ActiveCameras:       activeCameras,
		ActiveRecordings:    activeRecordings,
		SegmentsCreated:     segmentsCreated,
		CleanupDeleted:      cleanupDeleted,
		StorageUsedBytes:    storageUsedBytes,
		StorageTotalBytes:   storageTotalBytes,
		RecordingCount:      recordingCount,
		CameraErrors:        cameraErrors,
		HLSFramesDropped:    hlsFramesDropped,
		WebRTCActivePeers:   webrtcActivePeers,
		WebRTCFramesSent:    webrtcFramesSent,
		WebRTCFramesDropped: webrtcFramesDropped,
		FLVActiveStreams:    flvActiveStreams,
		FLVFramesSent:       flvFramesSent,
		FLVFramesDropped:    flvFramesDropped,
		FLVGOPCacheHits:     flvGOPCacheHits,
		XiaomiDisconnects:       xiaomiDisconnects,
		XiaomiReconnects:        xiaomiReconnects,
	}
}
