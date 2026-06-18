package relay

import "time"

// RelayStatus is the lifecycle state of a single push-out target. It is a
// distinct type from model.RecorderStatus on purpose: a target reporting
// "streaming" must not be confused with a camera "recording" to disk, and the
// camera status UI/health system keys off RecorderStatus.
type RelayStatus string

const (
	// StatusIdle means the target exists but is disabled (not running).
	StatusIdle RelayStatus = "idle"
	// StatusConnecting means the target is attempting to establish a connection.
	StatusConnecting RelayStatus = "connecting"
	// StatusStreaming means frames are actively being pushed to the target.
	StatusStreaming RelayStatus = "streaming"
	// StatusReconnecting means the connection dropped and a retry is scheduled.
	StatusReconnecting RelayStatus = "reconnecting"
	// StatusError means the target is in a persistent error state (e.g. the
	// source camera codec is incompatible with the target protocol).
	StatusError RelayStatus = "error"
)

// TargetStatus is the JSON-serializable runtime status of one push-out target,
// returned by GET /api/cameras/{id}/push-status and surfaced in the camera card.
type TargetStatus struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Protocol  string      `json:"protocol"`        // "rtmp" or "rtsp"
	URL       string      `json:"url"`             // target URL (masked in UI as needed)
	Status    RelayStatus `json:"status"`          // idle/connecting/streaming/reconnecting/error
	Kbps      float64     `json:"kbps"`            // recent outbound bitrate (kbps)
	Enabled   bool        `json:"enabled"`         // whether the target is active
	Uptime    string      `json:"uptime"`          // human duration since streaming started
	Error     string      `json:"error,omitempty"` // last error message (empty when healthy)
	UpdatedAt time.Time   `json:"updated_at"`      // last status change / sample time

	// Extended runtime status fields (populated when target is streaming).
	Platform            string  `json:"platform"`             // platform preset name
	TranscodePolicy     string  `json:"transcode_policy"`     // auto/force_sw/off
	TranscodeStatus     string  `json:"transcode_status"`     // "idle"/"active_hw"/"active_sw"/"throttled"/"error"
	TranscodeResolution string  `json:"transcode_resolution"` // current output resolution
	AudioCodec          string  `json:"audio_codec"`          // "aac"/"g711_mu"/"g711_a"/"silent"
	TemperatureC        int     `json:"temperature_c"`        // 0 when not transcoding
	RestartCount        int     `json:"restart_count"`        // transcoder restart count
	AVDriftMs           float64 `json:"av_drift_ms"`          // current A/V PTS drift in ms
}
