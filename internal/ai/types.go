package ai

import (
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Detection represents a single object detection result from the AI inference engine.
// BBox is a bounding box in normalized coordinates [x1, y1, x2, y2] where each value
// is in the range [0, 1] relative to the frame dimensions.
type Detection struct {
	BBox       [4]float64 `json:"bbox"`        // [x1, y1, x2, y2] normalized coordinates
	Confidence float64    `json:"confidence"`  // detection confidence score in [0, 1]
	ClassID    int        `json:"class_id"`    // numeric class identifier from the model
	ClassLabel string     `json:"class_label"` // human-readable class name (e.g. "person", "car")
}

// DetectionEvent is published to the event bus whenever a frame has been processed
// by the AI inference engine. It carries the full list of detections along with
// camera identity and frame dimensions for coordinate mapping.
type DetectionEvent struct {
	CameraID    string      `json:"camera_id"`    // kebab-case camera identifier
	Timestamp   time.Time   `json:"timestamp"`    // UTC time when the frame was captured
	Detections  []Detection `json:"detections"`   // all detections above the configured threshold
	FrameWidth  int         `json:"frame_width"`  // original frame width in pixels
	FrameHeight int         `json:"frame_height"` // original frame height in pixels
}

// ROI/ROIZone are part of the config schema and live in model; these aliases
// keep package-qualified references compiling.
type ROI = model.ROI

type ROIZone = model.ROIZone
