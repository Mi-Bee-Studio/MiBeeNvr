package event

import (
	"context"
	"sync"
	"testing"
	"time"

	pkgeventbus "github.com/Mi-Bee-Studio/MiBeeNvr/pkg/eventbus"
)

func helperAdapterDrain(t *testing.T, ch chan pkgeventbus.Event, timeout time.Duration) []pkgeventbus.Event {
	t.Helper()
	var events []pkgeventbus.Event
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

func helperNewBusAdapter(t *testing.T, bufSize int) *busAdapter {
	t.Helper()
	bus := helperNewBus(t, bufSize)
	adapter := NewBusAdapter(bus)
	if adapter == nil {
		t.Fatal("NewBusAdapter returned nil")
	}
	return adapter.(*busAdapter)
}

func TestNewBusAdapter_NilSafe(t *testing.T) {
	t.Parallel()
	if NewBusAdapter(nil) != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestBusAdapter_RoundTrip(t *testing.T) {
	t.Parallel()
	adapter := helperNewBusAdapter(t, 16)

	ch := make(chan pkgeventbus.Event, 16)
	err := adapter.Subscribe("test.topic", ch, 16)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	adapter.Publish(context.Background(), "test.topic", "hello")

	events := helperAdapterDrain(t, ch, time.Second)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Topic != "test.topic" {
		t.Fatalf("expected topic 'test.topic', got %q", events[0].Topic)
	}
	if events[0].Data != "hello" {
		t.Fatalf("expected data 'hello', got %v", events[0].Data)
	}
}

func TestBusAdapter_PrefixMatch(t *testing.T) {
	t.Parallel()
	adapter := helperNewBusAdapter(t, 16)

	ch := make(chan pkgeventbus.Event, 16)
	err := adapter.SubscribeByPrefix("test.", ch, 16)
	if err != nil {
		t.Fatalf("SubscribeByPrefix failed: %v", err)
	}

	adapter.Publish(context.Background(), "test.one", "one")
	adapter.Publish(context.Background(), "test.two", "two")
	adapter.Publish(context.Background(), "other", "three")

	events := helperAdapterDrain(t, ch, 200*time.Millisecond)
	if len(events) != 2 {
		t.Fatalf("expected 2 events (only test. prefix matches), got %d", len(events))
	}
	if events[0].Topic != "test.one" || events[1].Topic != "test.two" {
		t.Fatalf("expected topics 'test.one' and 'test.two', got %q and %q", events[0].Topic, events[1].Topic)
	}
}

func TestBusAdapter_UnsubscribeIdempotent(t *testing.T) {
	t.Parallel()
	adapter := helperNewBusAdapter(t, 16)

	ch := make(chan pkgeventbus.Event, 16)
	err := adapter.Subscribe("idem", ch, 16)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Must not panic.
	adapter.Unsubscribe("idem", ch)
	adapter.Unsubscribe("idem", ch)
	adapter.Unsubscribe("idem", ch)
}

func TestBusAdapter_UnsubscribeStopsDelivery(t *testing.T) {
	t.Parallel()
	adapter := helperNewBusAdapter(t, 16)

	ch := make(chan pkgeventbus.Event, 16)
	err := adapter.Subscribe("unsub", ch, 16)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	adapter.Unsubscribe("unsub", ch)

	adapter.Publish(context.Background(), "unsub", "after_unsub")

	events := helperAdapterDrain(t, ch, 100*time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("expected 0 events after unsubscribe, got %d", len(events))
	}
}

func TestBusAdapter_UnsubscribeByPrefixIdempotent(t *testing.T) {
	t.Parallel()
	adapter := helperNewBusAdapter(t, 16)

	ch := make(chan pkgeventbus.Event, 16)
	err := adapter.SubscribeByPrefix("test.", ch, 16)
	if err != nil {
		t.Fatalf("SubscribeByPrefix failed: %v", err)
	}

	adapter.UnsubscribeByPrefix("test.", ch)
	adapter.UnsubscribeByPrefix("test.", ch)
	adapter.UnsubscribeByPrefix("test.", ch)
}

func TestBusAdapter_PrefixMatch_EmptyMatchesAll(t *testing.T) {
	t.Parallel()
	adapter := helperNewBusAdapter(t, 16)

	ch := make(chan pkgeventbus.Event, 16)
	err := adapter.SubscribeByPrefix("", ch, 16)
	if err != nil {
		t.Fatalf("SubscribeByPrefix failed: %v", err)
	}

	adapter.Publish(context.Background(), "any.topic", "hello")

	events := helperAdapterDrain(t, ch, 200*time.Millisecond)
	if len(events) != 1 {
		t.Fatalf("expected 1 event with empty prefix, got %d", len(events))
	}
}

func TestBusAdapter_ConcurrentSubUnsub(t *testing.T) {
	t.Parallel()
	adapter := helperNewBusAdapter(t, 64)
	ctx := context.Background()

	var wg sync.WaitGroup
	// Concurrent subscribers subscribing and unsubscribing
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := make(chan pkgeventbus.Event, 16)
			_ = adapter.Subscribe("test.topic", ch, 16)
			adapter.Publish(ctx, "test.topic", "data")
			adapter.Unsubscribe("test.topic", ch)
		}()
	}
	// Concurrent publishers
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			adapter.Publish(ctx, "test.topic", "data")
		}()
	}
	wg.Wait()
}
