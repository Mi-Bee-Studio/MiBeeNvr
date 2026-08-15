// Shared GB28181 types consumed by the API layer — kept in the root package
// (not gb28181/sip) so internal/api can reference them without clashing with
// the gosip sip import (same pattern as PlaybackInfo).
package gb28181

import "time"

// TalkStatus reports an active voice-intercom session's state.
type TalkStatus struct {
	Active    bool   `json:"active"`
	CameraID  string `json:"camera_id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	Packets   int64  `json:"packets"`
	BytesSent int64  `json:"bytes_sent"`
	StartedAt string `json:"started_at,omitempty"`
}

// GBPosition is one mobile-position report (SUBSCRIBE MobilePosition).
type GBPosition struct {
	DeviceID  string    `json:"device_id"`
	Time      string    `json:"time"`
	Longitude string    `json:"longitude"`
	Latitude  string    `json:"latitude"`
	Speed     string    `json:"speed,omitempty"`
	Direction string    `json:"direction,omitempty"`
	Altitude  string    `json:"altitude,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}
