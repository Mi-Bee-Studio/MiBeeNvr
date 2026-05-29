package transcoding

import (
	"context"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// EncoderType represents the encoder backend.
type EncoderType string

const (
	EncoderSoftware EncoderType = "software"
	EncoderV4L2M2M  EncoderType = "v4l2m2m"
	EncoderVAAPI    EncoderType = "vaapi"
	EncoderNVENC    EncoderType = "nvenc"
)

// TranscodeStatus represents the status of a transcoding task.
type TranscodeStatus string

const (
	StatusPending   TranscodeStatus = "pending"
	StatusRunning   TranscodeStatus = "running"
	StatusCompleted TranscodeStatus = "completed"
	StatusFailed    TranscodeStatus = "failed"
	StatusCancelled TranscodeStatus = "cancelled"
)

// HardwareCapabilities holds the results of a hardware probe.
type HardwareCapabilities struct {
	Arch                 string      `json:"arch"`
	TotalCores           int         `json:"total_cores"`
	TotalMemoryMB        uint64      `json:"total_memory_mb"`
	H264Encoder          string      `json:"h264_encoder"`           // encoder name (e.g. "libx264", "h264_v4l2m2m")
	H265Encoder          string      `json:"h265_encoder"`           // encoder name (e.g. "libx265", "h265_v4l2m2m")
	H264EncoderType      EncoderType `json:"h264_encoder_type"`      // backend type
	H265EncoderType      EncoderType `json:"h265_encoder_type"`      // backend type
	H264Decoder          string      `json:"h264_decoder"`           // decoder name (e.g. "h264_v4l2m2m", "" means software-only)
	H265Decoder          string      `json:"h265_decoder"`           // decoder name (e.g. "hevc_v4l2m2m", "" means software-only)
	H264DecoderType      EncoderType `json:"h264_decoder_type"`     // reuse EncoderType enum for decoders
	H265DecoderType      EncoderType `json:"h265_decoder_type"`     // reuse EncoderType enum for decoders
	MaxEncodeWidth       int         `json:"max_encode_width"`       // max output width supported by encoder (0 = unlimited)
	MaxEncodeHeight      int         `json:"max_encode_height"`      // max output height supported by encoder (0 = unlimited)
	Devices              []string    `json:"devices"`                // /dev/video* paths
	MaxConcurrentStreams int         `json:"max_concurrent_streams"` // estimated safe concurrency
	EstimatedFPS         float64     `json:"estimated_fps"`          // estimated encoding FPS
	FFmpegAvailable      bool        `json:"ffmpeg_available"`       // whether FFmpeg is installed
	FFmpegPath           string      `json:"ffmpeg_path"`            // path to FFmpeg binary
}

// TranscodeOptions holds parameters for building an FFmpeg command.
type TranscodeOptions struct {
	InputPath     string `json:"input_path"`
	OutputPath    string `json:"output_path"`
	InputCodec    string `json:"input_codec"`    // "h264", "h265", "mjpeg"
	OutputCodec   string `json:"output_codec"`   // "h264", "h265"
	Width         int    `json:"width"`          // output width
	Height        int    `json:"height"`         // output height
	Bitrate       string `json:"bitrate"`        // e.g. "2M"
	Framerate     int    `json:"framerate"`      // output framerate
	ForceSoftware bool   `json:"force_software"` // bypass hardware encoders
	Preset        string `json:"preset"`         // "ultrafast", "faster", "medium"
}

// TranscodeTask represents a transcoding task stored in the database.
type TranscodeTask struct {
	ID              int64           `json:"id"`
	CameraID        string          `json:"camera_id"`
	RecordingID     string          `json:"recording_id"`
	InputPath       string          `json:"input_path"`
	InputFormat     string          `json:"input_format"`
	OutputPath      string          `json:"output_path"`
	OutputFormat    string          `json:"output_format"`
	Status          TranscodeStatus `json:"status"`
	Progress        float64         `json:"progress"`
	Error           string          `json:"error"`
	CreatedAt       string          `json:"created_at"`
	StartedAt       *string         `json:"started_at"`
	CompletedAt     *string         `json:"completed_at"`
	OriginalDeleted bool            `json:"original_deleted"`
}

// DownloadStatus represents the FFmpeg download state for the frontend.
type DownloadStatus struct {
	Status         string  `json:"status"`          // "not_installed", "downloading", "available", "failed"
	Progress       float64 `json:"progress"`        // 0.0-1.0
	Version        string  `json:"version"`
	Error          string  `json:"error"`
	TotalBytes     int64   `json:"total_bytes"`     // total size of download in bytes
	DownloadedBytes int64  `json:"downloaded_bytes"` // bytes downloaded so far
}

// MediaInfo holds the result of an ffprobe invocation.
type MediaInfo struct {
	CodecName string  `json:"codec_name"`
	Duration  float64 `json:"duration"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
}

// QueueAPI defines the interface for transcode queue operations.
// Both TranscodeQueue and test mocks implement this interface.
type QueueAPI interface {
	Enqueue(ctx context.Context, task *storage.TranscodeTask) error
	CancelTask(ctx context.Context, id int64) error
}

// ManagerStatus is returned by the API status endpoint.
type ManagerStatus struct {
	Enabled         bool                  `json:"enabled"`
	DisabledReason  string                `json:"disabled_reason"`
	Hardware        *HardwareCapabilities `json:"hardware"`
	QueueLength     int                   `json:"queue_length"`
	ActiveJobs      int                   `json:"active_jobs"`
	RecentResults   []TranscodeTask       `json:"recent_results"`
}
