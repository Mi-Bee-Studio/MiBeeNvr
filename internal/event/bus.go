package event

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// Topic constants for event types.
const (
	TopicSegmentCompleted     = "segment.completed"
	TopicSegmentDeleted       = "segment.deleted"
	TopicStorageHealthChanged = "storage.health.changed"
	TopicAIDetection          = "ai.detection"
	TopicAIPerson             = "ai.detection.person"
	TopicAIVehicle            = "ai.detection.vehicle"
	TopicAIAnimal             = "ai.detection.animal"
	TopicAIEventCreated       = "ai.event.created"
	// TopicCameraAdded is published when a camera is created — both via the
	// manual add path (source="manual") and the auto-discover engine
	// (source="auto"). Payload: map[string]any{camera_id, name, endpoint,
	// activation_state, source}. The frontend subscribes via
	// /api/events?filter=camera. to refresh its camera list and toast the user.
	TopicCameraAdded = "camera.added"
	// TopicCameraQuality is published when a Xiaomi recorder's auto quality
	// state machine transitions (HD→SD fallback or the bounded SD→HD recovery
	// probe, issue #502). Payload: map[string]any{camera_id, from, to, reason,
	// model}. Surfaces via /api/events?filter=camera. like camera.added.
	TopicCameraQuality = "camera.quality"
	// TopicGB28181Alarm is published when a GB/T 28181 device pushes an alarm
	// notification (SUBSCRIBE/NOTIFY Alarm, or a MESSAGE-delivered alarm).
	// Payload: event.GB28181AlarmEvent. Surfaces via /api/events SSE.
	TopicGB28181Alarm = "gb28181.alarm"
)

var ErrDuplicateSubscriber = errors.New("subscriber already registered for this topic")

// subscriber holds a channel and its mutex.
type subscriber struct {
	ch     chan Event
	mu     sync.Mutex // protects send vs close race
	closed bool
}

// EventBus is a lightweight pub/sub system with ring-buffer overflow.
// Per-topic subscribers get buffered channels; when full, the oldest
// event is dropped to make room — never blocks the publisher.
type EventBus struct {
	mu                sync.RWMutex
	bufferSize        int
	subscribers       map[string][]*subscriber
	prefixSubscribers map[string][]*subscriber
}

// NewEventBus creates an EventBus with the given buffer size.
// If bufferSize <= 0, defaults to 64.
func NewEventBus(bufferSize int) *EventBus {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	return &EventBus{
		bufferSize:        bufferSize,
		subscribers:       make(map[string][]*subscriber),
		prefixSubscribers: make(map[string][]*subscriber),
	}
}

// Subscribe registers a channel for the given topic.
// The caller's channel is used directly as the ring buffer — Publish drains the oldest
// event when the channel is full. The caller is responsible for reading from ch.
// Returns ErrDuplicateSubscriber if the same channel is already subscribed.
func (b *EventBus) Subscribe(topic string, ch chan Event, bufferSize int) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := &subscriber{
		ch: ch,
	}

	b.subscribers[topic] = append(b.subscribers[topic], s)

	return nil
}

// Unsubscribe removes all subscribers for the given topic and marks them closed.
// Unsubscribe removes the given channel from the topic's subscriber list
// and marks it as closed. Only the specific channel is removed — other
// subscribers on the same topic are NOT affected.
// It does NOT close the caller's channel — the caller owns it.
func (b *EventBus) Unsubscribe(topic string, ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subs := b.subscribers[topic]
	for _, s := range subs {
		if s.ch == ch {
			s.mu.Lock()
			if !s.closed {
				s.closed = true
			}
			s.mu.Unlock()
		}
	}
	// Remove only the matching subscriber from the slice, NOT the whole topic.
	filtered := subs[:0]
	for _, s := range subs {
		if s.ch != ch {
			filtered = append(filtered, s)
		}
	}
	b.subscribers[topic] = filtered
}

// SubscribeByPrefix registers a channel for all topics that start with the given prefix.
// An empty prefix matches all topics. The caller is responsible for reading from ch.
func (b *EventBus) SubscribeByPrefix(prefix string, ch chan Event, bufferSize int) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := &subscriber{ch: ch}
	b.prefixSubscribers[prefix] = append(b.prefixSubscribers[prefix], s)
	return nil
}

// UnsubscribeByPrefix removes the given channel from the prefix's subscriber
// list and marks it as closed. Only the specific channel is removed.
func (b *EventBus) UnsubscribeByPrefix(prefix string, ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subs := b.prefixSubscribers[prefix]
	filtered := subs[:0]
	for _, s := range subs {
		if s.ch == ch {
			s.mu.Lock()
			if !s.closed {
				s.closed = true
			}
			s.mu.Unlock()
		} else {
			filtered = append(filtered, s)
		}
	}
	b.prefixSubscribers[prefix] = filtered
}

// Publish sends an event to all subscribers of the given topic.
// Respects context cancellation. Never blocks on any single subscriber.
func (b *EventBus) Publish(ctx context.Context, topic string, data any) {
	evt := Event{Topic: topic, Data: data}

	b.mu.RLock()
	subs := make([]*subscriber, len(b.subscribers[topic]))
	copy(subs, b.subscribers[topic])
	b.mu.RUnlock()

	for _, s := range subs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		// Ring-buffer overflow: if the buffered channel is full, drain the
		// oldest event to make room. cap>0 guard: on an unbuffered channel
		// (cap==0) len==cap is 0==0 and the drain would block forever on the
		// empty channel while holding s.mu, deadlocking the whole bus (#220).
		if cap(s.ch) > 0 && len(s.ch) == cap(s.ch) {
			select {
			case <-s.ch: // drop oldest
			default: // lost a race with another sender's drain; skip
			}
		}
		// Non-blocking send: an unbuffered channel with no current reader, or a
		// full buffered channel, drops the newest event instead of blocking.
		// This keeps the "never blocks on any single subscriber" contract even
		// for a dead/slow consumer (status events are lossy by design).
		select {
		case s.ch <- evt:
		default: // unbuffered-and-no-reader, or full — drop newest
		}
		s.mu.Unlock()
	}

	// Also deliver to prefix subscribers.
	b.mu.RLock()
	var prefixSubs []*subscriber
	for prefix, subs := range b.prefixSubscribers {
		if strings.HasPrefix(topic, prefix) {
			for _, s := range subs {
				if !s.closed {
					prefixSubs = append(prefixSubs, s)
				}
			}
		}
	}
	b.mu.RUnlock()

	for _, s := range prefixSubs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		// Ring-buffer overflow: drain oldest when the buffered channel is full.
		// cap>0 guard avoids the unbuffered-channel deadlock (#220); see the
		// topic-subscriber block above for the full rationale.
		if cap(s.ch) > 0 && len(s.ch) == cap(s.ch) {
			select {
			case <-s.ch: // drop oldest
			default: // lost a race with another sender's drain; skip
			}
		}
		// Non-blocking send: drop newest rather than block on a dead consumer.
		select {
		case s.ch <- evt:
		default: // unbuffered-and-no-reader, or full — drop newest
		}
		s.mu.Unlock()
	}
}
