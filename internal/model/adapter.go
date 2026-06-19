package model

import (
	pkgstreamhub "github.com/Mi-Bee-Studio/MiBeeNvr/pkg/streamhub"
)

// Compile-time assertion that *HubAdapter satisfies pkg/streamhub.Hub.
var _ pkgstreamhub.Hub = (*HubAdapter)(nil)

// HubAdapter wraps *StreamHub to satisfy pkg/streamhub.Hub.
//
// StreamHub's native callback types (model.FrameCallback,
// model.AudioCallback) are structurally similar but not identical to
// pkg/streamhub's types — model.AudioCallback uses AudioCodec (a
// defined string type) while pkg/streamhub uses plain string. The
// adapter bridges these via zero-cost type conversions.
//
// Construct with NewHubAdapter; do not allocate directly.
type HubAdapter struct {
	Hub *StreamHub
}

// NewHubAdapter wraps h as a pkg/streamhub.Hub.
// Returns nil if h is nil so callers can pass through directly.
func NewHubAdapter(h *StreamHub) pkgstreamhub.Hub {
	if h == nil {
		return nil
	}
	return &HubAdapter{Hub: h}
}

// Subscribe registers a video frame callback. The pkg-streamhub
// callback is converted to the internal FrameCallback signature.
func (a *HubAdapter) Subscribe(consumerID string, cb pkgstreamhub.FrameCallback) error {
	return a.Hub.Subscribe(consumerID, FrameCallback(cb))
}

// Unsubscribe removes the video consumer. Idempotent.
func (a *HubAdapter) Unsubscribe(consumerID string) {
	a.Hub.Unsubscribe(consumerID)
}

// SubscribeAudio registers an audio frame callback. The pkg-streamhub
// callback (which receives a plain string codec) is bridged to the
// internal AudioCallback (which receives AudioCodec).
func (a *HubAdapter) SubscribeAudio(consumerID string, cb pkgstreamhub.AudioCallback) error {
	return a.Hub.SubscribeAudio(consumerID, func(pts int64, codec AudioCodec, data []byte) {
		cb(pts, string(codec), data)
	})
}

// UnsubscribeAudio removes the audio consumer. Idempotent.
func (a *HubAdapter) UnsubscribeAudio(consumerID string) {
	a.Hub.UnsubscribeAudio(consumerID)
}

// Sends returns the count of frames delivered to the consumer.
func (a *HubAdapter) Sends(consumerID string) int64 {
	return a.Hub.Sends(consumerID)
}

// Drops returns the count of frames dropped for the consumer.
func (a *HubAdapter) Drops(consumerID string) int64 {
	return a.Hub.Drops(consumerID)
}

// ConsumerCount returns the current number of video subscribers.
func (a *HubAdapter) ConsumerCount() int {
	return a.Hub.ConsumerCount()
}
