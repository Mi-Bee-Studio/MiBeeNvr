package gb28181

import "time"

// PlaybackInfo is the API-facing state of an active device-recording fetch
// (playback INVITE). Shared by the SIP server (producer) and the REST layer
// (consumer).
type PlaybackInfo struct {
	Active       bool      `json:"active"`
	ChannelID    string    `json:"channel_id"`
	DeviceID     string    `json:"device_id"`
	CameraID     string    `json:"camera_id"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	Frames       int64     `json:"frames"`
	LastPTSTicks int64     `json:"last_pts_ticks"`
	StartedAt    time.Time `json:"started_at"`
	Paused       bool      `json:"paused"`
	Scale        float64   `json:"scale"`
}
