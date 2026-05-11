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

// StorageProvider manages recording storage and metadata
type StorageProvider interface {
	CreateSegment(cameraID string, meta SegmentMeta) (*Segment, error)
	CloseSegment(segmentID string) (*Recording, error)
	WriteFrame(segmentID string, data []byte) (int, error)
	ListRecordings(filter RecordingFilter) ([]Recording, error)
	GetRecording(id string) (*Recording, error)
	DeleteRecording(id string) error
	GetStats() (StorageStats, error)
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
	Merged    *bool // nil = all, true = merged only, false = unmerged only
	Search    string
	Limit     int
	Offset    int
	SortBy    string // started_at, duration, file_size, camera_id; default: started_at
	SortOrder string // asc, desc; default: desc
}

type RecorderStatus string

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

// Protocol implementations
const (
	ProtoRTSPH264  Protocol = "rtsp_h264"
	ProtoRTSPMJPEG Protocol = "rtsp_mjpeg"
	ProtoHTTPJPEG  Protocol = "http_jpeg"
	ProtoRTSPH265 Protocol = "rtsp_h265"
	ProtoONVIF    Protocol = "onvif"
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
	FormatH264  Format = "h264"
	FormatMJPEG Format = "mjpeg"
	FormatH265  Format = "h265"
)

// ValidEncodingsForProtocol maps transport protocol to supported encodings
var ValidEncodingsForProtocol = map[string][]string{
	string(ProtoRTSP):  {string(FormatH264), string(FormatH265), string(FormatMJPEG)},
	string(ProtoHTTP):  {string(EncJPEG)},
	string(ProtoONVIF): {string(FormatH264), string(FormatH265)},
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
	// ONVIF allows empty encoding (auto-detect)
	if protocol == string(ProtoONVIF) && encoding == "" {
		return nil
	}
	for _, e := range encodings {
		if e == encoding {
			return nil
		}
	}
	return fmt.Errorf("encoding %q not valid for protocol %q", encoding, protocol)
}
