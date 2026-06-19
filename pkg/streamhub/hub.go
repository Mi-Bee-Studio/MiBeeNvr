// Package streamhub exposes the frame distribution Hub interface for
// external (out-of-module) consumers, primarily the commercial P2P module
// that needs to subscribe to camera frames for WebRTC tracks.
//
// The concrete implementation lives at internal/model.StreamHub. Adapters
// in internal/model satisfy this interface via HubBridge.
package streamhub

// FrameCallback is invoked for each video access unit.
//
//   - pts is the presentation timestamp in microseconds (clock rate 90000)
//   - au is the access unit (one or more NAL units for H.264/H.265,
//     or a full JPEG frame for MJPEG)
//
// Implementations MUST be non-blocking: if the hub's internal buffer is
// full when the callback is invoked, frames are silently dropped to
// protect the recording and streaming pipeline.
type FrameCallback func(pts int64, au [][]byte)

// AudioCallback is invoked for each decoded audio frame.
//
//   - pts is the presentation timestamp in microseconds
//   - codec identifies the audio codec ("aac", "g711")
//   - data is the raw audio payload
//
// Implementations MUST be non-blocking.
type AudioCallback func(pts int64, codec string, data []byte)

// Hub distributes frames from a single camera to multiple subscribers.
//
// Each camera has its own Hub instance. Subscribers register callbacks
// under a unique consumerID; the same consumerID cannot subscribe twice
// without first unsubscribing. Hubs are safe for concurrent use after
// the camera recorder starts publishing.
type Hub interface {
	// Subscribe registers a video frame callback for the given consumer ID.
	// Returns an error if consumerID is already registered.
	Subscribe(consumerID string, cb FrameCallback) error

	// Unsubscribe removes the video consumer with the given ID.
	// Idempotent: calling with an unknown consumerID is a no-op.
	Unsubscribe(consumerID string)

	// SubscribeAudio registers an audio frame callback for the given
	// consumer ID. Returns an error if already registered.
	SubscribeAudio(consumerID string, cb AudioCallback) error

	// UnsubscribeAudio removes the audio consumer with the given ID.
	// Idempotent.
	UnsubscribeAudio(consumerID string)

	// Sends returns the count of frames successfully delivered to the
	// given consumer. Returns 0 for unknown consumers.
	Sends(consumerID string) int64

	// Drops returns the count of frames dropped for the given consumer
	// (buffer full, slow consumer). Returns 0 for unknown consumers.
	Drops(consumerID string) int64

	// ConsumerCount returns the current number of video subscribers.
	ConsumerCount() int
}
