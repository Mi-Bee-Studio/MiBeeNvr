package event

// Event represents a single event published to the bus.
type Event struct {
	Topic string
	Data  interface{}
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
