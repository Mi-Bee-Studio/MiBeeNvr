package event

import (
	"context"
	"sync"
	"testing"
	"time"
)

func helperNewBus(t *testing.T, bufSize int) *EventBus {
	t.Helper()
	return NewEventBus(bufSize)
}

func helperSubscribe(t *testing.T, bus *EventBus, topic string, bufSize int) chan Event {
	t.Helper()
	ch := make(chan Event, bufSize)
	err := bus.Subscribe(topic, ch, bufSize)
	if err != nil {
		t.Fatalf("Subscribe(%q) failed: %v", topic, err)
	}
	return ch
}

func helperDrain(t *testing.T, ch chan Event, timeout time.Duration) []Event {
	t.Helper()
	var events []Event
	deadline := time.After(timeout)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, e)
		case <-deadline:
			return events
		}
	}
}

func TestNewEventBus_DefaultBufferSize(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 0)
	if bus == nil {
		t.Fatal("NewEventBus returned nil")
	}
	if bus.bufferSize != 64 {
		t.Fatalf("expected default buffer 64, got %d", bus.bufferSize)
	}
}

func TestNewEventBus_CustomBufferSize(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 128)
	if bus.bufferSize != 128 {
		t.Fatalf("expected buffer 128, got %d", bus.bufferSize)
	}
}

func TestPublishSubscribe(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	ch := helperSubscribe(t, bus, "test.topic", 16)

	bus.Publish(context.Background(), "test.topic", "hello")

	events := helperDrain(t, ch, time.Second)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "hello" {
		t.Fatalf("expected data 'hello', got %v", events[0].Data)
	}
	if events[0].Topic != "test.topic" {
		t.Fatalf("expected topic 'test.topic', got %q", events[0].Topic)
	}
}

func TestPublish_NoSubscribers(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	// Must not panic
	bus.Publish(context.Background(), "no.subscribers", "orphan")
}

func TestPublish_MultipleSubscribers(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	ch1 := helperSubscribe(t, bus, "multi", 16)
	ch2 := helperSubscribe(t, bus, "multi", 16)

	bus.Publish(context.Background(), "multi", "broadcast")

	events1 := helperDrain(t, ch1, time.Second)
	events2 := helperDrain(t, ch2, time.Second)

	if len(events1) != 1 || events1[0].Data != "broadcast" {
		t.Fatalf("subscriber 1: expected 1 event 'broadcast', got %v", events1)
	}
	if len(events2) != 1 || events2[0].Data != "broadcast" {
		t.Fatalf("subscriber 2: expected 1 event 'broadcast', got %v", events2)
	}
}

func TestUnsubscribe(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	ch := helperSubscribe(t, bus, "unsub", 16)

	bus.Unsubscribe("unsub", ch)

	// Publish after unsubscribe — should not panic, subscriber should not receive
	bus.Publish(context.Background(), "unsub", "after_unsub")

	events := helperDrain(t, ch, 100*time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("expected 0 events after unsubscribe, got %d", len(events))
	}
}

func TestUnsubscribe_Idempotent(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	ch := helperSubscribe(t, bus, "idem", 16)

	// Multiple unsubscribes must not panic
	bus.Unsubscribe("idem", ch)
	bus.Unsubscribe("idem", ch)
	bus.Unsubscribe("idem", ch)
}

func TestOverflow_DropsOldest(t *testing.T) {
	t.Parallel()
	// Use a very small buffer and don't consume.
	bus := NewEventBus(3)
	ch := make(chan Event, 3)
	err := bus.Subscribe("overflow", ch, 3)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Publish 6 events into a buffer of 3 — first 3 should be dropped
	for i := range 6 {
		bus.Publish(context.Background(), "overflow", i)
	}

	events := helperDrain(t, ch, time.Second)
	if len(events) != 3 {
		t.Fatalf("expected 3 events (overflow dropped 3), got %d", len(events))
	}
	// Should have dropped 0,1,2 and kept 3,4,5
	expected := []int{3, 4, 5}
	for i, e := range events {
		if e.Data != expected[i] {
			t.Fatalf("event[%d]: expected %d, got %v", i, expected[i], e.Data)
		}
	}
}

func TestContextCancellation(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	ch := helperSubscribe(t, bus, "cancel", 16)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	bus.Publish(ctx, "cancel", "should_not_arrive")

	events := helperDrain(t, ch, 100*time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("expected 0 events with cancelled context, got %d", len(events))
	}
}

func TestConcurrentPublish(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 100)
	ch := helperSubscribe(t, bus, "concurrent", 10000)

	var wg sync.WaitGroup
	const writers = 10
	const eventsPerWriter = 100

	for w := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range eventsPerWriter {
				bus.Publish(context.Background(), "concurrent", id*1000+i)
			}
		}(w)
	}
	wg.Wait()

	events := helperDrain(t, ch, 2*time.Second)
	if len(events) != writers*eventsPerWriter {
		t.Fatalf("expected %d events, got %d", writers*eventsPerWriter, len(events))
	}
}

func TestSegmentCompletedTopic(t *testing.T) {
	t.Parallel()
	if TopicSegmentCompleted != "segment.completed" {
		t.Fatalf("expected 'segment.completed', got %q", TopicSegmentCompleted)
	}
}

func TestSegmentCompletedStruct(t *testing.T) {
	t.Parallel()
	sc := SegmentCompleted{
		CameraID:    "front-door",
		FilePath:    "/data/segments/front-door_20260601.mp4",
		Format:      "h264",
		StartedAt:   "2026-06-01T00:00:00Z",
		EndedAt:     "2026-06-01T00:01:00Z",
		FileSize:    1024000,
		RecordingID: "rec-001",
	}

	bus := helperNewBus(t, 16)
	ch := helperSubscribe(t, bus, TopicSegmentCompleted, 16)

	bus.Publish(context.Background(), TopicSegmentCompleted, sc)

	events := helperDrain(t, ch, time.Second)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	data, ok := events[0].Data.(SegmentCompleted)
	if !ok {
		t.Fatal("event data is not SegmentCompleted")
	}
	if data.CameraID != "front-door" {
		t.Fatalf("expected CameraID 'front-door', got %q", data.CameraID)
	}
	if data.FileSize != 1024000 {
		t.Fatalf("expected FileSize 1024000, got %d", data.FileSize)
	}
}

func helperSubscribeByPrefix(t *testing.T, bus *EventBus, prefix string, bufSize int) chan Event {
	t.Helper()
	ch := make(chan Event, bufSize)
	err := bus.SubscribeByPrefix(prefix, ch, bufSize)
	if err != nil {
		t.Fatalf("SubscribeByPrefix(%q) failed: %v", prefix, err)
	}
	return ch
}

func TestSubscribeByPrefix_Matches(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	ch := helperSubscribeByPrefix(t, bus, "onvif.", 16)

	bus.Publish(context.Background(), "onvif.motion", "moved")
	bus.Publish(context.Background(), "onvif.tamper", "tampered")
	bus.Publish(context.Background(), "segment.completed", "done")

	events := helperDrain(t, ch, time.Second)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Topic != "onvif.motion" {
		t.Fatalf("expected topic 'onvif.motion', got %q", events[0].Topic)
	}
	if events[1].Topic != "onvif.tamper" {
		t.Fatalf("expected topic 'onvif.tamper', got %q", events[1].Topic)
	}
}

func TestSubscribeByPrefix_EmptyPrefixMatchesAll(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	ch := helperSubscribeByPrefix(t, bus, "", 16)

	bus.Publish(context.Background(), "onvif.motion", "moved")
	bus.Publish(context.Background(), "segment.completed", "done")

	events := helperDrain(t, ch, time.Second)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

func TestSubscribeByPrefix_NoMatch(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	ch := helperSubscribeByPrefix(t, bus, "xiaomi.", 16)

	bus.Publish(context.Background(), "onvif.motion", "moved")

	events := helperDrain(t, ch, 100*time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestUnsubscribeByPrefix(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	ch := helperSubscribeByPrefix(t, bus, "onvif.", 16)

	bus.UnsubscribeByPrefix("onvif.", ch)

	bus.Publish(context.Background(), "onvif.motion", "after_unsub")

	events := helperDrain(t, ch, 100*time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("expected 0 events after unsubscribe, got %d", len(events))
	}
}

func TestSubscribeByPrefix_AndExactSubscribe(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	exactCh := helperSubscribe(t, bus, "onvif.motion", 16)
	prefixCh := helperSubscribeByPrefix(t, bus, "onvif.", 16)

	bus.Publish(context.Background(), "onvif.motion", "moved")
	bus.Publish(context.Background(), "onvif.tamper", "tampered")

	exactEvents := helperDrain(t, exactCh, time.Second)
	prefixEvents := helperDrain(t, prefixCh, time.Second)

	if len(exactEvents) != 1 {
		t.Fatalf("exact: expected 1 event, got %d", len(exactEvents))
	}
	if len(prefixEvents) != 2 {
		t.Fatalf("prefix: expected 2 events, got %d", len(prefixEvents))
	}
}

// TestPublishUnbufferedChannelDoesNotDeadlock guards the fix for #220: a
// subscriber registered with an UNBUFFERED channel (cap==0) and no concurrent
// reader must not deadlock Publish. Previously the len==cap drain guard
// evaluated 0==0 → true and the blocking <-s.ch wedged the bus while holding
// s.mu. Publish must return promptly (within the guard timeout) and the event
// is simply dropped (non-blocking send).
func TestPublishUnbufferedChannelDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	// Unbuffered topic subscriber, never read from.
	unbuf := make(chan Event)
	if err := bus.Subscribe("unbuf.topic", unbuf, 0); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		bus.Publish(context.Background(), "unbuf.topic", "no-reader")
		close(done)
	}()
	select {
	case <-done:
		// Publish returned without blocking — pass.
	case <-time.After(2 * time.Second):
		t.Fatal("Publish deadlocked on an unbuffered channel with no reader (#220)")
	}
}

// TestPublishUnbufferedPrefixChannelDoesNotDeadlock is the prefix-subscriber
// counterpart of the test above, exercising the second drain+send site.
func TestPublishUnbufferedPrefixChannelDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 16)
	unbuf := make(chan Event) // unbuffered, never read
	if err := bus.SubscribeByPrefix("unbuf.", unbuf, 0); err != nil {
		t.Fatalf("SubscribeByPrefix failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		bus.Publish(context.Background(), "unbuf.something", "no-reader")
		close(done)
	}()
	select {
	case <-done:
		// pass
	case <-time.After(2 * time.Second):
		t.Fatal("Publish deadlocked on an unbuffered prefix channel with no reader (#220)")
	}
}

// TestPublish_DeadConsumerDoesNotBlockPublisher ensures a full buffered channel
// (consumer stopped reading) cannot stall the publisher — the non-blocking send
// drops the newest event instead. This is the defense-in-depth guarantee.
func TestPublish_DeadConsumerDoesNotBlockPublisher(t *testing.T) {
	t.Parallel()
	bus := helperNewBus(t, 4)
	full := make(chan Event, 1) // tiny buffer, will fill immediately
	if err := bus.Subscribe("full.topic", full, 1); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		// Publish far more than the buffer can hold with no reader.
		for range 50 {
			bus.Publish(context.Background(), "full.topic", "fill")
		}
		close(done)
	}()
	select {
	case <-done:
		// pass — publisher was never blocked by the saturated subscriber
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full buffered channel (dead consumer should not stall publisher)")
	}
}
