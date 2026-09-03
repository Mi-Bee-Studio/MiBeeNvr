package event

import "time"

// Event represents a single event published to the bus.
type Event struct {
	Topic string
	Data  interface{}
}

// StorageHealthChanged is published when storage health state transitions occur.
type StorageHealthChanged struct {
	CameraID      string `json:"camera_id"`
	PreviousState string `json:"previous_state"`
	CurrentState  string `json:"current_state"`
	Message       string `json:"message"`
}

// SegmentCompleted is published when a recording segment finishes writing.
type SegmentCompleted struct {
	CameraID    string `json:"camera_id"`
	FilePath    string `json:"file_path"` // relative to storage root for cross-server compatibility
	Format      string `json:"format"`
	Encoding    string `json:"encoding"`   // h264, h265, mjpeg — enables MiBeeVision to choose decoder
	StartedAt   string `json:"started_at"` // RFC3339Nano or DB timestamp format
	EndedAt     string `json:"ended_at"`
	FileSize    int64  `json:"file_size"`
	RecordingID string `json:"recording_id"`
	// Layer marks the recording tier (#637): 0 = main, 1 = continuous
	// sub-stream (tiered). Consumers that only handle main-stream segments
	// (Vision push) filter on it; the motion analyzer scores both.
	Layer int `json:"layer,omitempty"`
}

// SegmentDeleted is published when a recording segment is deleted (retention
// expiry or manual deletion). MiBeeVision subscribes to cancel in-progress
// processing tasks and orphan associated AI event snapshots.
type SegmentDeleted struct {
	RecordingID string `json:"recording_id"`
	CameraID    string `json:"camera_id"`
	FilePath    string `json:"file_path"`
	Reason      string `json:"reason"` // "retention_expired", "manual", "disk_threshold"
}

// AIDetectionEvent is published when AI inference detects objects in a camera frame.
type AIDetectionEvent struct {
	CameraID    string        `json:"camera_id"`
	Timestamp   string        `json:"timestamp"`
	Detections  []AIDetection `json:"detections"`
	FrameWidth  int           `json:"frame_width"`
	FrameHeight int           `json:"frame_height"`
	Model       string        `json:"model"`
}

type AIDetection struct {
	BBox       [4]float64 `json:"bbox"`
	Confidence float64    `json:"confidence"`
	ClassID    int        `json:"class_id"`
	ClassLabel string     `json:"class_label"`
}

// CameraSnapshotEvent is published when a snapshot is captured and persisted
// under {storage_root}/snapshots/ (TopicCameraSnapshot). FilePath is relative
// to the storage root for cross-server compatibility.
type CameraSnapshotEvent struct {
	CameraID  string    `json:"camera_id"`
	FilePath  string    `json:"file_path"`
	Timestamp time.Time `json:"timestamp"`
	Trigger   string    `json:"trigger"` // e.g. "mqtt"
}

// GB28181AlarmEvent is published when a GB/T 28181 device pushes an alarm
// notification (TopicGB28181Alarm).
type GB28181AlarmEvent struct {
	CameraID         string    `json:"camera_id,omitempty"`
	DeviceID         string    `json:"device_id"`
	ChannelID        string    `json:"channel_id,omitempty"`
	AlarmPriority    string    `json:"alarm_priority,omitempty"` // 1高 2中 3低
	AlarmMethod      string    `json:"alarm_method,omitempty"`   // 2 motion, 5 offline...
	AlarmType        string    `json:"alarm_type,omitempty"`
	AlarmTime        string    `json:"alarm_time,omitempty"`
	AlarmDescription string    `json:"alarm_description,omitempty"`
	ReceivedAt       time.Time `json:"received_at"`
}
