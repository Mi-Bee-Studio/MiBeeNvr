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
// to support HLS live streaming. The api handler uses this interface
// to obtain codec parameters for starting an HLS stream.
// Frame delivery uses internal/streamhub.StreamHub Subscribe/Unsubscribe directly.
type HLSProvider interface {
	// CodecParams returns the current codec parameters detected from the stream.
	// Returns nil slices if codec info frames have not been received yet.
	CodecParams() (codec Format, sps, pps, vps []byte)
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
	ID            string    `json:"id"`
	CameraID      string    `json:"camera_id"`
	FilePath      string    `json:"file_path"`
	Format        Format    `json:"format"`
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
	Duration      float64   `json:"duration"`
	FileSize      int64     `json:"file_size"`
	FrameCount    int       `json:"frame_count"`
	MergeStatus   string    `json:"merge_status"`
	MergePath     string    `json:"merge_path"`
	MergeTier     string    `json:"merge_tier"`
	MergeProgress int       `json:"merge_progress"`
	MergeError    string    `json:"merge_error"`
	MergeQuality  string    `json:"merge_quality"` // complete, fragmented, short
	RetryCount    int       `json:"retry_count"`
	Archived      bool      `json:"archived"`
	// AI processing state (MiBeeVision integration). DB columns exist since v21
	// but were previously not mapped to Go — this fixes the断层.
	AIStatus      string     `json:"ai_status,omitempty"`       // pending, processing, completed, failed
	AIProcessedAt *time.Time `json:"ai_processed_at,omitempty"` // nil = never processed; a zero time.Time serializes as 0001-01-01 (omitempty can't omit structs), which clients misread
	AIError       string     `json:"ai_error,omitempty"`
	// Motion score (issue #435): compressed-domain activity score in [0,1]
	// computed by the offline motion analyzer from the per-frame size series
	// (P-frame size = motion proxy, no decode). MotionScoreUnanalyzed (-1)
	// means the analyzer has not processed this recording yet — cleanup
	// ordering treats unanalyzed as neutral. ActivityFlags is a comma-separated
	// vocabulary: "static", "motion", "scene_cut".
	MotionScore   float64 `json:"motion_score"`
	ActivityFlags string  `json:"activity_flags,omitempty"`
	// TimelineMap (#496) is set on rolling-merge products whose sparse
	// timelapse dwells were compressed to a fast cadence: compact JSON
	// "[[wallSec,fileSec],...]" breakpoints mapping the recording's
	// wall-clock span onto the (shorter) file timeline. Clients resolve
	// wall-clock seeks (?at=, day-timeline clicks) through it; empty for
	// uncompressed recordings (identity mapping).
	TimelineMap string `json:"timeline_map,omitempty"`
}

// MotionScoreUnanalyzed marks a recording the motion analyzer has not scored
// yet (DB default). Distinct from 0 (analyzed, fully static).
const MotionScoreUnanalyzed = -1.0

// Motion activity flag vocabulary (recordings.activity_flags).
const (
	ActivityFlagStatic   = "static"    // no meaningful activity — boring segment
	ActivityFlagMotion   = "motion"    // activity above threshold
	ActivityFlagSceneCut = "scene_cut" // bitrate discontinuity (scene change / exposure step)
)

// TimelapseMerge represents one periodic-merge output for a camera — the
// multi-hour / multi-day timelapse video synthesized from many short timelapse
// segments (or from video recordings when recording_enabled=true).
//
// One row per (camera, window-start, duration-label). The actual MP4 lives at
// OutputPath; SourceSegmentIDs is a JSON array of the recordings.id values
// that were folded into this merge.
type TimelapseMerge struct {
	ID               int64     `json:"id"`
	CameraID         string    `json:"camera_id"`
	WindowStart      time.Time `json:"window_start"`
	WindowEnd        time.Time `json:"window_end"`
	DurationLabel    string    `json:"duration_label"` // "1h","8h","24h","natural-day","7d","30d"
	OutputPath       string    `json:"output_path"`    // periodic-merge/<cam>/periodic_<windowLabel>.mp4
	FileSize         int64     `json:"file_size"`
	FrameCount       int       `json:"frame_count"`
	Codec            string    `json:"codec"` // h264 / h265 / mjpeg
	FPS              int       `json:"fps"`
	Status           string    `json:"status"` // pending/merging/completed/failed
	Error            string    `json:"error,omitempty"`
	SourceSegmentIDs string    `json:"source_segment_ids"` // JSON array of recordings.id
	CreatedAt        time.Time `json:"created_at"`
	CompletedAt      time.Time `json:"completed_at,omitempty"`
}

// TimelineSegment is the lightweight projection of a Recording used only for
// timeline rendering (the recordings-page day strip + the player DVR bar).
// It omits file_path/merge_*/file_size/etc. to minimize bandwidth when a day
// has thousands of segments — a full-day window for a fragmentation-prone
// camera (Xiaomi AVI reconnect storms, ~5000+ segments/day) is ~10x smaller
// than the full Recording projection. Issue #115: the full-row endpoint caps
// at 500 rows and silently truncated the afternoon; this endpoint caps at
// maxTimelineSegments (10k) and the rows are cheap enough to ship in bulk.
//
// Fields are exactly what DayTimeline.svelte / TimelineBar.svelte read:
// id (seek navigation), camera_id + started_at + ended_at (band position),
// duration (fallback when ended_at is null on the in-progress last segment),
// format (color band), merge_status (pending "(N ⚠)" counter).
type TimelineSegment struct {
	ID          string    `json:"id"`
	CameraID    string    `json:"camera_id"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
	Duration    float64   `json:"duration"`
	Format      Format    `json:"format"`
	MergeStatus string    `json:"merge_status"`
	MotionScore float64   `json:"motion_score"`
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
	Merged    *bool // nil = all, true = merged only, false = unmerged only (translates to merge_status filter)
	Search    string
	Limit     int
	Offset    int
	SortBy    string // started_at, duration, file_size, camera_id; default: started_at
	SortOrder string // asc, desc; default: desc
	Archived  *bool  // nil = all, true = archived only, false = not archived
	// AiClass filters to recordings that have at least one AI event with this class_name
	// (e.g. "person", "car"). Translates to an EXISTS subquery against ai_events joined on
	// recording_id. Empty = no AI filter. Requires ai_events.class_name to be populated by
	// the AI event ingest path.
	AiClass string
	// MinMotionScore filters to recordings with motion_score >= the value
	// (issue #435). nil = no filter. Unanalyzed recordings (score -1) never pass.
	MinMotionScore *float64
	// Activity filters by activity_flags membership (e.g. "static", "motion",
	// "scene_cut"). Empty = no filter.
	Activity string
	// Cursor enables keyset (seek) pagination for O(1) deep-page performance.
	// When set AND the sort is the default (started_at DESC), ListRecordings uses
	// WHERE started_at < cursor instead of OFFSET, avoiding the O(N) scan-skip that
	// makes OFFSET 10000+ take seconds. The cursor is the started_at of the last row
	// on the current page (RFC3339 format from the API layer). Ignored for non-default
	// sort orders (falls back to OFFSET).
	Cursor string
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
	Date         string           `json:"date"`
	Recordings   int              `json:"recordings"`
	TotalSize    int64            `json:"total_size"`
	CameraCounts map[string]int   `json:"cameras,omitempty"`      // camera name → recording count
	CameraSizes  map[string]int64 `json:"camera_sizes,omitempty"` // camera name → total bytes (for per-camera storage chart)
}

// RecordingDaySummary is a per-day aggregate used by the recordings calendar.
// Date is in the client's local timezone ("YYYY-MM-DD"). Formats contains
// category names ("video", "timelapse", "mjpeg") present that day.
type RecordingDaySummary struct {
	Date    string   `json:"date"`
	Count   int      `json:"count"`
	Formats []string `json:"formats"`
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
	// HealthEventQualityChanged marks a Xiaomi auto quality transition
	// (HD→SD fallback after repeated no-media failures, or the bounded
	// SD→HD recovery probe). Issue #502.
	HealthEventQualityChanged HealthEventType = "quality_changed"
)

// HealthReporter is the interface for reporting health events.
// Implementations must be safe for concurrent use.
type HealthReporter interface {
	ReportHealth(cameraID string, event HealthEvent)
}

// Protocol constants (transport-only). 0.10.0+: combined protocol strings
// like "rtsp_h264" are no longer used. protocol and encoding are always separate.
const (
	ProtoONVIF     Protocol = "onvif"
	ProtoXiaomi    Protocol = "xiaomi"
	ProtoTimelapse Protocol = "timelapse"
	ProtoRTSP      Protocol = "rtsp"
	ProtoHTTP      Protocol = "http"
	// Push/ingest protocols: a remote publisher pushes the stream TO the NVR
	// (SRT listener, RTMP server, WHIP endpoint over the main HTTP listener).
	// Unlike the pull protocols above, the NVR does not dial out; frames arrive
	// via the ingest server callbacks.
	ProtoSRT     Protocol = "srt"
	ProtoRTMP    Protocol = "rtmp"
	ProtoWHIP    Protocol = "whip"
	ProtoGB28181 Protocol = "gb28181"
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
	FormatAVI       Format = "avi" // AVI container (MJPEG video + G.711 audio)
)

// Audio format constants
const (
	FormatAAC  Format = "aac"  // AAC audio
	FormatG711 Format = "g711" // G.711 mu-law/a-law audio
)

// AudioCodec represents the audio codec type for AudioFrame.
type AudioCodec string

const (
	AudioAAC   AudioCodec = "aac"   // AAC audio codec
	AudioG711  AudioCodec = "g711"  // G.711, law unspecified (legacy producers)
	AudioG711A AudioCodec = "g711a" // G.711 A-law (PCMA)
	AudioG711U AudioCodec = "g711u" // G.711 μ-law (PCMU)
	AudioOpus  AudioCodec = "opus"  // Opus audio codec
)

// Merge status constants.
const (
	MergeStatusPending      = "pending"
	MergeStatusMerged       = "merged"
	MergeStatusMerging      = "merging"
	MergeStatusFailed       = "failed"
	MergeStatusIncompatible = "incompatible"
	MergeStatusDark         = "dark" // segment is too dark to be useful (night, no IR)
	// MergeStatusDailyMerged marks a timelapse segment that has already been
	// folded into a periodic-merge output (a "daily" / 8h / 24h / 7d / 30d
	// window). Such segments are excluded from re-merge in subsequent windows.
	MergeStatusDailyMerged = "daily_merged"
)

// TimelapseMergeStatus constants for the timelapse_merges table.
const (
	TimelapseMergeStatusPending   = "pending"
	TimelapseMergeStatusMerging   = "merging"
	TimelapseMergeStatusCompleted = "completed"
	TimelapseMergeStatusFailed    = "failed"
)

// TimelapseMergeCodec constants identify the codec of a periodic-merge output
// MP4, used by the frontend to decide between <video> playback (h264/h265) and
// the JPEG frame cycler fallback (mjpeg / mjpa).
const (
	TimelapseMergeCodecH264  = "h264"
	TimelapseMergeCodecH265  = "h265"
	TimelapseMergeCodecMJPEG = "mjpeg"
)

// Merge quality constants — describe the continuity of a merged recording.
const (
	MergeQualityComplete   = "complete"   // normal merge, no significant gaps
	MergeQualityFragmented = "fragmented" // has time gaps (ended-started >> duration)
	MergeQualityShort      = "short"      // merged but below minimum duration threshold
	MergeQualityDark       = "dark"       // segment classified as dark/night vision
)

// AudioFrame represents a single audio frame for distribution through StreamHub.
type AudioFrame struct {
	PTS   int64      // Presentation timestamp (same clock as video)
	Codec AudioCodec // Audio codec type
	Data  []byte     // Encoded audio data (AAC frames, G.711 samples, etc.)
}

// CodecInfo holds the complete codec parameters for a camera stream,
// including video SPS/PPS/VPS and audio codec details. Used by the relay
// engine and streaming subsystems to initialize target tracks.
type CodecInfo struct {
	SPS             []byte // H.264/H.265 SPS NAL unit (without start code)
	PPS             []byte // H.264/H.265 PPS NAL unit (without start code)
	VPS             []byte // H.265 VPS NAL unit (without start code), nil for H.264
	IsH264          bool   // true when video codec is H.264
	AudioCodec      string // "aac", "g711", or "" for no audio
	AudioConfig     []byte // AudioSpecificConfig for AAC, or codec flag bytes for G.711
	AudioSampleRate int    // Sample rate in Hz (e.g. 8000, 44100, 48000), 0 when no audio
	AudioChannels   int    // Number of audio channels (1=mono, 2=stereo), 0 when no audio
}

// ValidEncodingsForProtocol maps transport protocol to supported encodings
var ValidEncodingsForProtocol = map[string][]string{
	string(ProtoRTSP):      {string(FormatH264), string(FormatH265), string(FormatMJPEG)},
	string(ProtoHTTP):      {string(EncJPEG)},
	string(ProtoONVIF):     {string(FormatH264), string(FormatH265), string(EncJPEG)},
	string(ProtoXiaomi):    {string(FormatH264), string(FormatH265)},
	string(ProtoTimelapse): {""}, // empty string for auto-detect
	// Push/ingest protocols. SRT config-layer accepts h264/h265, but the current
	// SRT MPEG-TS demuxer only emits H.264 NALUs (H.265 over SRT is a follow-up).
	// RTMP accepts H.265 via Enhanced-RTMP (hvc1 fourcc) — publishers must send
	// the fourcc sequence header (our relay passthrough policy and FFmpeg do).
	string(ProtoSRT):  {string(FormatH264), string(FormatH265)},
	string(ProtoRTMP): {string(FormatH264), string(FormatH265)},
	// WHIP (browser/OBS WebRTC push-in) is H.264 only — matches WHEP egress
	// (browser WebRTC H.265 support is still fragmented).
	string(ProtoWHIP): {string(FormatH264)},
	// GB28181 is an ingest protocol: the camera registers via SIP and the NVR
	// INVITEs it; the codec is auto-detected from the PS stream_type at runtime.
	string(ProtoGB28181): {string(FormatH264), string(FormatH265)},
}

// ValidateProtocolEncoding checks if the protocol+encoding combination is valid.
// Empty encoding is allowed for ONVIF/Timelapse (auto-detect) and the push
// protocols srt/rtmp/gb28181 (encoding is derived from the published stream).
func ValidateProtocolEncoding(protocol, encoding string) error {
	encodings, ok := ValidEncodingsForProtocol[protocol]
	if !ok {
		return fmt.Errorf("unknown protocol: %s", protocol)
	}
	// ONVIF, Timelapse, and push protocols allow empty encoding (auto-detect / derived from stream)
	if (protocol == string(ProtoONVIF) || protocol == string(ProtoTimelapse) ||
		protocol == string(ProtoSRT) || protocol == string(ProtoRTMP) ||
		protocol == string(ProtoWHIP) ||
		protocol == string(ProtoGB28181)) && encoding == "" {
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
