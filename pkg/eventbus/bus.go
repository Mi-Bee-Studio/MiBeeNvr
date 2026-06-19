// Package eventbus exposes the pub/sub event Bus interface for external
// (out-of-module) consumers, primarily the commercial P2P module that
// needs to receive motion detection, camera status, and storage events
// to push to mobile apps.
//
// The concrete implementation lives at internal/event.EventBus. Adapters
// in internal/event satisfy this interface.
//
// # Standard topics (subject to expansion)
//
//   - "camera.started"           — camera began streaming
//   - "camera.stopped"           — camera stopped streaming
//   - "camera.error"             — camera error (data: CameraErrorDetail)
//   - "motion.detected"          — motion was detected (data varies)
//   - "segment.completed"        — recording segment finished
//   - "segment.deleted"          — recording segment deleted
//   - "storage.health.changed"   — disk health transition
//   - "system.health.changed"    — system-wide health change
//
// Use SubscribeByPrefix("") to receive all events.
package eventbus

import "context"

// Event is a single published event on the bus.
//
// Topic is the routing key (dot-separated, e.g. "camera.stopped").
// Data is the event payload; consumers type-assert to the expected type
// based on the topic. For topics with no payload, Data is nil.
type Event struct {
	Topic string
	Data  any
}

// Bus is a topic-based pub/sub event bus.
//
// Subscriptions match either exact topic or a prefix. Channels buffer
// events; if a channel is full when an event is published, the event is
// dropped (non-blocking publish). All methods are safe for concurrent use.
type Bus interface {
	// Subscribe registers ch to receive events published to the exact
	// topic. bufferSize controls the channel's capacity (events beyond
	// capacity are dropped). Returns an error if ch is already
	// registered for the topic.
	Subscribe(topic string, ch chan Event, bufferSize int) error

	// Unsubscribe removes ch from the given topic. Idempotent.
	Unsubscribe(topic string, ch chan Event)

	// SubscribeByPrefix registers ch for all topics starting with
	// prefix. An empty prefix ("") matches all topics. bufferSize
	// controls channel capacity.
	SubscribeByPrefix(prefix string, ch chan Event, bufferSize int) error

	// UnsubscribeByPrefix removes the prefix subscription. Idempotent.
	UnsubscribeByPrefix(prefix string, ch chan Event)

	// Publish sends an event to all matching subscribers (exact topic
	// and prefix subscribers). Publish is non-blocking and never
	// returns an error due to a full subscriber channel — events are
	// silently dropped instead. The provided ctx is currently unused
	// but reserved for future tracing/cancellation hooks.
	Publish(ctx context.Context, topic string, data any)
}
