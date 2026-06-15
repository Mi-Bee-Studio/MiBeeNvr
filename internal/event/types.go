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
	CameraID    string
	FilePath    string
	Format      string
	StartedAt   string // RFC3339Nano or DB timestamp format
	EndedAt     string
	FileSize    int64
	RecordingID string
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
	BBox        [4]float64 `json:"bbox"`
	Confidence  float64    `json:"confidence"`
	ClassID     int        `json:"class_id"`
	ClassLabel  string     `json:"class_label"`
}
