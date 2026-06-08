package model

import (
	"context"
	"fmt"
	"time"
)

// Recorder records video from a camera source
type Recorder interface {
	Start(ctx context.Context) error
	Stop() error
	Status() RecorderStatus
}

// HLSProvider is an optional interface that recorders can implement
// to support HLS live streaming. The api handler checks for this
// interface via type assertion when starting an HLS stream.
//
// Deprecated: This interface is being replaced by StreamHub-based frame fan-out.
// SetOnHLSFrame will be removed in a future version. New code should use
// StreamHub.Subscribe/Unsubscribe directly. CodecParams remains useful
// for codec detection and will be kept.
type HLSProvider interface {
	// CodecParams returns the current codec parameters detected from the stream.
	// Returns nil slices if codec info frames have not been received yet.
	CodecParams() (codec Format, sps, pps, vps []byte)
	// SetOnHLSFrame registers a callback for HLS frame delivery.
	// The callback must be non-blocking — frames are dropped if buffer is full.
	// Deprecated: Use StreamHub.Subscribe() instead.
	SetOnHLSFrame(cb func(pts int64, au [][]byte))
}

// Camera represents a camera source configuration
type Camera struct {
	ID       string
	Name     string
	Protocol Protocol
	Encoding Format
	URL      string
	Username string
	Password string
	Enabled  bool

	CreatedAt time.Time
}

type Recording struct {
	ID         string    `json:"id"`
	CameraID   string    `json:"camera_id"`
	FilePath   string    `json:"file_path"`
	Format     Format    `json:"format"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	Duration   float64   `json:"duration"`
	FileSize   int64     `json:"file_size"`
	FrameCount int       `json:"frame_count"`
	Merged     bool      `json:"merged"`
	MergeStatus string   `json:"merge_status"`
	MergePath   string    `json:"merge_path"`
	MergeTier   string    `json:"merge_tier"`
	MergeProgress int     `json:"merge_progress"`
	MergeError  string    `json:"merge_error"`
	RetryCount  int       `json:"retry_count"`
	Archived   bool      `json:"archived"`
}

type Segment struct {
	ID         string
	CameraID   string
	FilePath   string
	Format     Format
	StartedAt  time.Time
	TempPath   string
	FrameCount int
}

type SegmentMeta struct {
	CameraID string
	Format   Format
}

type RecordingFilter struct {
	CameraID  string
	StartTime time.Time
	EndTime   time.Time
	Format    Format
	Formats   []Format
	Merged    *bool // nil = all, true = merged only, false = unmerged only
	Search    string
	Limit     int
	Offset    int
	SortBy    string // started_at, duration, file_size, camera_id; default: started_at
	SortOrder string // asc, desc; default: desc
	Archived  *bool  // nil = all, true = archived only, false = not archived
}

type RecorderStatus string

// CameraErrorDetail represents a camera error with type classification and message.
// This is used to provide detailed error info to the frontend (e.g. TUTK errors).
type CameraErrorDetail struct {
	Type       string    `json:"type"`
	Message    string    `json:"message"`
	DetectedAt time.Time `json:"detected_at"`
}

type StorageStats struct {
	TotalBytes     int64 `json:"total_bytes"`
	UsedBytes      int64 `json:"used_bytes"`
	RecordingCount int   `json:"recording_count"`
	CameraCount    int   `json:"camera_count"`
}

// DailyStats represents aggregated recording statistics for a single day.
type DailyStats struct {
	Date         string         `json:"date"`
	Recordings   int            `json:"recordings"`
	TotalSize    int64          `json:"total_size"`
	CameraCounts map[string]int `json:"cameras,omitempty"`
}

type Protocol string

type Format string

// Constants for statuses
const (
	StatusRecording    RecorderStatus = "recording"
	StatusStopped      RecorderStatus = "stopped"
	StatusError        RecorderStatus = "error"
	StatusReconnecting RecorderStatus = "reconnecting"
)

// HealthStatus represents the health status of a camera or component.
type HealthStatus string

// Health status constants.
const (
	HealthStatusHealthy HealthStatus = "healthy"
	HealthStatusWarning HealthStatus = "warning"
	HealthStatusError   HealthStatus = "error"
	HealthStatusUnknown HealthStatus = "unknown"
)

// HealthEventType represents the type of a health monitoring event.
type HealthEventType string

// Health event type constants.
const (
	HealthEventConnectionLost     HealthEventType = "connection_lost"
	HealthEventConnectionRestored HealthEventType = "connection_restored"
	HealthEventStreamAnomaly      HealthEventType = "stream_anomaly"
	HealthEventFreezeDetected     HealthEventType = "freeze_detected"
	HealthEventFreezeRecovered    HealthEventType = "freeze_recovered"
)

// HealthReporter is the interface for reporting health events.
// Implementations must be safe for concurrent use.
type HealthReporter interface {
	ReportHealth(cameraID string, event HealthEvent)
}

// Protocol implementations
const (
	ProtoRTSPH264  Protocol = "rtsp_h264"
	ProtoRTSPMJPEG Protocol = "rtsp_mjpeg"
	ProtoHTTPJPEG  Protocol = "http_jpeg"
	ProtoRTSPH265  Protocol = "rtsp_h265"
	ProtoONVIF     Protocol = "onvif"
	ProtoXiaomi    Protocol = "xiaomi"
	ProtoTimelapse Protocol = "timelapse"
)

// Transport-only protocol constants
const (
	ProtoRTSP Protocol = "rtsp"
	ProtoHTTP Protocol = "http"
)

// Encoding constants
const (
	EncJPEG Format = "jpeg"
)

// Formats used for recordings/segments
const (
	FormatH264      Format = "h264"
	FormatH265      Format = "h265"
	FormatMJPEG     Format = "mjpeg"
	FormatTimelapse Format = "timelapse"
)

// Audio format constants
const (
	FormatAAC  Format = "aac"  // AAC audio
	FormatG711 Format = "g711" // G.711 mu-law/a-law audio
)

// AudioCodec represents the audio codec type for AudioFrame.
type AudioCodec string

const (
	AudioAAC  AudioCodec = "aac"  // AAC audio codec
	AudioG711 AudioCodec = "g711" // G.711 mu-law (PCMU) and a-law (PCMA)
)

// Merge status constants.
const (
	MergeStatusPending = "pending"
	MergeStatusMerged  = "merged"
	MergeStatusMerging = "merging"
	MergeStatusFailed  = "failed"
)

// AudioFrame represents a single audio frame for distribution through StreamHub.
type AudioFrame struct {
	PTS   int64      // Presentation timestamp (same clock as video)
	Codec AudioCodec // Audio codec type
	Data  []byte     // Encoded audio data (AAC frames, G.711 samples, etc.)
}

// ValidEncodingsForProtocol maps transport protocol to supported encodings
var ValidEncodingsForProtocol = map[string][]string{
	string(ProtoRTSP):   {string(FormatH264), string(FormatH265), string(FormatMJPEG)},
	string(ProtoHTTP):   {string(EncJPEG)},
	string(ProtoONVIF):  {string(FormatH264), string(FormatH265)},
	string(ProtoXiaomi): {string(FormatH264), string(FormatH265)},
	string(ProtoTimelapse): {""}, // empty string for auto-detect
}

// ParseLegacyProtocol splits old combined protocol strings (e.g. "rtsp_h264") into separate protocol and encoding
func ParseLegacyProtocol(old string) (protocol, encoding string, err error) {
	switch old {
	case "rtsp_h264":
		return "rtsp", "h264", nil
	case "rtsp_h265":
		return "rtsp", "h265", nil
	case "rtsp_mjpeg":
		return "rtsp", "mjpeg", nil
	case "http_jpeg":
		return "http", "jpeg", nil
	case "onvif":
		return "onvif", "", nil
	case "timelapse":
		return "timelapse", "", nil
default:
		return "", "", fmt.Errorf("unknown legacy protocol: %s", old)
	}
}

// ValidateProtocolEncoding checks if the protocol+encoding combination is valid.
// Empty encoding is allowed for ONVIF (auto-detect).
func ValidateProtocolEncoding(protocol, encoding string) error {
	encodings, ok := ValidEncodingsForProtocol[protocol]
	if !ok {
		return fmt.Errorf("unknown protocol: %s", protocol)
	}
	// ONVIF and Timelapse allow empty encoding (auto-detect / no encoding needed)
	if (protocol == string(ProtoONVIF) || protocol == string(ProtoTimelapse)) && encoding == "" {
		return nil
	}
	for _, e := range encodings {
		if e == encoding {
			return nil
		}
	}
	return fmt.Errorf("encoding %q not valid for protocol %q", encoding, protocol)
}

// HealthEvent represents a single camera health check result stored in camera_health_events.
type HealthEvent struct {
	ID        int64     `json:"id"`
	CameraID  string    `json:"camera_id"`
	EventType string    `json:"event_type"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	Metadata  string    `json:"metadata"`
	CreatedAt time.Time `json:"created_at"`
}

// CameraHealth represents the latest health status summary for a camera.
type CameraHealth struct {
	CameraID      string    `json:"camera_id"`
	LatestStatus  string    `json:"latest_status"`
	LatestEvent   string    `json:"latest_event"`
	LatestMessage string    `json:"latest_message"`
	LastEventAt   time.Time `json:"last_event_at"`
	Score         int       `json:"score"`
	ScoreFactors  []string  `json:"score_factors,omitempty"`
}
