// Package camera exposes the camera Manager interface for external
// (out-of-module) consumers, primarily the commercial P2P module that
// needs to enumerate cameras, read status, and subscribe to media
// frames for WebRTC tracks.
//
// The concrete implementation lives at internal/camera.CameraManager.
// Adapters in internal/camera satisfy this interface.
//
// Mutating operations (Update, Add, Remove) are intentionally NOT
// exposed here. Out-of-module callers may only OBSERVE camera state.
// Configuration changes must go through the public REST API or CLI.
package camera

import (
	"errors"
	"fmt"

	"github.com/Mi-Bee-Studio/MiBeeNvr/pkg/streamhub"
)

// ErrCameraNotFound is returned when a camera with the given ID does not
// exist or has been removed.
var ErrCameraNotFound = errors.New("camera not found")

// Camera is a single managed camera device.
//
// All accessors are safe to call concurrently. Values reflect a snapshot
// at the time of the call; subsequent calls may return updated values
// if the underlying configuration changed.
type Camera interface {
	// ID is the unique camera identifier (kebab-case, e.g. "front-door").
	ID() string

	// Name is the human-readable camera name (may be empty).
	Name() string

	// Protocol is the wire protocol used to ingest from this camera.
	// Common values: "rtsp", "http_jpeg", "onvif", "xiaomi",
	// "srt", "rtmp".
	Protocol() string

	// Encoding is the video codec ("h264", "h265", "mjpeg", "jpeg").
	Encoding() string

	// AudioEnabled reports whether audio capture is configured for
	// this camera.
	AudioEnabled() bool
}

// Status is a point-in-time snapshot of a camera's runtime state.
//
// FPS and BitrateKbps are computed over recent activity; they may be
// zero for cameras that have not yet produced frames.
type Status struct {
	// ID is the camera ID this status refers to.
	ID string

	// Online reports whether the recorder is currently connected and
	// producing frames.
	Online bool

	// Recording reports whether the camera is actively writing segments
	// to disk.
	Recording bool

	// FPS is the measured video frame rate (frames per second).
	FPS float64

	// BitrateKbps is the measured video bitrate in kilobits per second.
	BitrateKbps int

	// Error is a non-empty string when the camera is in an error state
	// (connection failure, authentication error, etc.). Empty when healthy.
	Error string
}

// Manager manages the lifecycle and observation of all configured cameras.
//
// Implementations must be safe for concurrent use. The returned slices
// and structs are safe to retain without copying.
type Manager interface {
	// List returns all configured cameras in arbitrary order.
	// The returned slice is a fresh copy; callers may mutate it freely.
	List() []Camera

	// Get returns the camera with the given ID.
	// Returns ErrCameraNotFound if no such camera exists.
	Get(id string) (Camera, error)

	// Status returns the runtime status of the camera with the given ID.
	// Returns ErrCameraNotFound if no such camera exists.
	Status(id string) (Status, error)

	// Hub returns the frame distribution hub for the camera with the
	// given ID. Multiple callers requesting Hub for the same camera
	// receive the same Hub instance; each must Subscribe under a unique
	// consumerID.
	//
	// Returns ErrCameraNotFound if no such camera exists.
	Hub(id string) (streamhub.Hub, error)
}

// IsNotFound reports whether err is an ErrCameraNotFound.
// Convenience helper for callers that wrap the error.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrCameraNotFound)
}

// NewNotFoundError returns an ErrCameraNotFound wrapped with the given
// cameraID for context.
func NewNotFoundError(cameraID string) error {
	return fmt.Errorf("%w: %s", ErrCameraNotFound, cameraID)
}
