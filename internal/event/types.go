package event

// Event represents a single event published to the bus.
type Event struct {
	Topic string
	Data  interface{}
}

// StorageHealthChanged is published when storage health state transitions occur.
type StorageHealthChanged struct {
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
