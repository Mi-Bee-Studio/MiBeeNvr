// Package ai defines the interface for AI-powered object detection.
//
// Two provider modes are supported:
//   - Internal: local ONNX inference (RPi 3B, YOLOv8n-Q4, ~300MB peak)
//   - External: remote GPU server via HTTP/gRPC API
//
// AIManager (future) coordinates lifecycle, feature toggle, lazy loading, and fallback.
package ai

import "context"

// Detection represents a single object detection result.
type Detection struct {
	Label      string    `json:"label"`
	Confidence float32   `json:"confidence"`
	Box        [4]float32 `json:"box"` // [x, y, width, height] in normalized coordinates
}

// Detector performs object detection on a single video frame.
// Implementations must be safe for concurrent use.
type Detector interface {
	Detect(ctx context.Context, frame []byte) ([]Detection, error)
}

// AIProvider is the top-level interface for an AI backend.
// It abstracts the lifecycle of a detection capability without
// committing to local or remote implementation details.
type AIProvider interface {
	Name() string
	IsAvailable() bool
	NewDetector(model string) (Detector, error)
}

// LocalProvider is an AIProvider that runs inference on-device (ONNX).
// Models are lazily loaded into a fixed memory budget.
type LocalProvider interface {
	AIProvider
}

// RemoteProvider is an AIProvider that delegates inference to a remote GPU server.
// The Endpoint method returns the base URL of the remote service.
type RemoteProvider interface {
	AIProvider
	Endpoint() string
}
