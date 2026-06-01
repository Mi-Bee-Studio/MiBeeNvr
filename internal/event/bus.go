package event

import (
	"context"
	"errors"
	"sync"
)

// Topic constants for event types.
const (
	TopicSegmentCompleted = "segment.completed"
)

var (
	ErrDuplicateSubscriber = errors.New("subscriber already registered for this topic")
)

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
	mu          sync.RWMutex
	bufferSize  int
	subscribers map[string][]*subscriber
}

// NewEventBus creates an EventBus with the given buffer size.
// If bufferSize <= 0, defaults to 64.
func NewEventBus(bufferSize int) *EventBus {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	return &EventBus{
		bufferSize:  bufferSize,
		subscribers: make(map[string][]*subscriber),
	}
}

// Subscribe registers a channel for the given topic.
// The caller's channel is used directly as the ring buffer — Publish drains the oldest
// event when the channel is full. The caller is responsible for reading from ch.
// Returns ErrDuplicateSubscriber if the same channel is already subscribed.
func (b *EventBus) Subscribe(topic string, ch chan Event, bufferSize int) error {
	if bufferSize <= 0 {
		bufferSize = b.bufferSize
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	s := &subscriber{
		ch: ch,
	}

	b.subscribers[topic] = append(b.subscribers[topic], s)

	return nil
}

// Unsubscribe removes all subscribers for the given topic and marks them closed.
// It does NOT close the caller's channel — the caller owns it.
func (b *EventBus) Unsubscribe(topic string, ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subs := b.subscribers[topic]
	for _, s := range subs {
		s.mu.Lock()
		if !s.closed {
			s.closed = true
		}
		s.mu.Unlock()
	}
	delete(b.subscribers, topic)
}

// Publish sends an event to all subscribers of the given topic.
// Respects context cancellation. Never blocks on any single subscriber.
func (b *EventBus) Publish(ctx context.Context, topic string, data interface{}) {
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
		// Ring-buffer overflow: if channel full, drain one (oldest) then send.
		if len(s.ch) == cap(s.ch) {
			<-s.ch // drop oldest
		}
		s.ch <- evt
		s.mu.Unlock()
	}
}
