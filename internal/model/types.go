package model

import (
	"context"
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
	PinRecording(id string) error
	UnpinRecording(id string) error
	GetStats() (StorageStats, error)
}

// Camera represents a camera source configuration
type Camera struct {
	ID       string
	Name     string
	Protocol Protocol
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
	Pinned     bool      `json:"pinned"`
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
	Pinned    *bool // nil = all, true = pinned only, false = unpinned only
	Limit     int
	Offset    int
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
)

// Formats used for recordings/segments
const (
	FormatH264  Format = "h264"
	FormatMJPEG Format = "mjpeg"
)
