package ai

import "time"

// Detection represents a single object detection result from the AI inference engine.
// BBox is a bounding box in normalized coordinates [x1, y1, x2, y2] where each value
// is in the range [0, 1] relative to the frame dimensions.
type Detection struct {
	BBox        [4]float64 `json:"bbox"`        // [x1, y1, x2, y2] normalized coordinates
	Confidence  float64    `json:"confidence"`  // detection confidence score in [0, 1]
	ClassID     int        `json:"class_id"`    // numeric class identifier from the model
	ClassLabel  string     `json:"class_label"` // human-readable class name (e.g. "person", "car")
}

// DetectionEvent is published to the event bus whenever a frame has been processed
// by the AI inference engine. It carries the full list of detections along with
// camera identity and frame dimensions for coordinate mapping.
type DetectionEvent struct {
	CameraID    string       `json:"camera_id"`    // kebab-case camera identifier
	Timestamp   time.Time    `json:"timestamp"`    // UTC time when the frame was captured
	Detections  []Detection  `json:"detections"`   // all detections above the configured threshold
	FrameWidth  int          `json:"frame_width"`  // original frame width in pixels
	FrameHeight int          `json:"frame_height"` // original frame height in pixels
}

// ROI defines a region of interest as a closed polygon. Points are specified in
// normalized coordinates (each coordinate in [0, 1]) relative to the frame dimensions.
// The polygon is assumed to be implicitly closed (last point connects back to first).
type ROI struct {
	Name   string      `json:"name"`   // human-readable name for the region (e.g. "driveway", "front-door")
	Points [][2]float64 `json:"points"` // polygon vertices in normalized coordinates
}

// ROIZone binds an ROI to a specific camera and controls whether AI detection
// should be filtered to only include objects within this zone.
type ROIZone struct {
	CameraID string `json:"camera_id"` // kebab-case camera identifier
	Zone     ROI    `json:"zone"`      // the region of interest definition
	Enabled  bool   `json:"enabled"`   // whether this zone filter is active
}
