package event

import (
	"context"
	"sync"

	pkgeventbus "github.com/Mi-Bee-Studio/MiBeeNvr/pkg/eventbus"
)

// Compile-time assertion that *busAdapter satisfies pkgeventbus.Bus.
var _ pkgeventbus.Bus = (*busAdapter)(nil)

// busAdapter wraps *EventBus to satisfy pkgeventbus.Bus.
//
// Event types between the two packages are structurally identical but
// distinct Go types, so channel-based Subscribe/Unsubscribe methods
// require a bridge goroutine that converts pkgeventbus.Event ↔ Event
// at the boundary. Publish delegates directly (any == interface{}).
//
// Construct with NewBusAdapter; do not allocate directly.
type busAdapter struct {
	bus *EventBus

	mu      sync.Mutex
	bridges []*bridgeEntry
}

// bridgeEntry tracks one subscription bridge: the internal channel
// registered with *EventBus and the external channel the caller owns.
type bridgeEntry struct {
	externalCh chan pkgeventbus.Event
	internalCh chan Event
	stopCh     chan struct{}
	topic      string
	isPrefix   bool
}

// NewBusAdapter wraps b as a pkgeventbus.Bus.
// Returns nil if b is nil so callers can pass through directly.
func NewBusAdapter(b *EventBus) pkgeventbus.Bus {
	if b == nil {
		return nil
	}
	return &busAdapter{bus: b}
}

// Subscribe registers an external channel for the given topic. A bridge
// goroutine forwards events from the internal bus to the caller's channel,
// converting Event types at the boundary.
func (a *busAdapter) Subscribe(topic string, ch chan pkgeventbus.Event, bufferSize int) error {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	intCh := make(chan Event, bufferSize)
	stopCh := make(chan struct{})

	if err := a.bus.Subscribe(topic, intCh, bufferSize); err != nil {
		return err
	}

	entry := &bridgeEntry{
		externalCh: ch,
		internalCh: intCh,
		stopCh:     stopCh,
		topic:      topic,
	}

	a.mu.Lock()
	a.bridges = append(a.bridges, entry)
	a.mu.Unlock()

	go a.bridgeLoop(entry)

	return nil
}

// Unsubscribe removes the external channel from the given topic.
// Idempotent — safe to call multiple times.
func (a *busAdapter) Unsubscribe(topic string, ch chan pkgeventbus.Event) {
	entry := a.removeBridge(topic, ch, false)
	if entry == nil {
		return
	}

	close(entry.stopCh)
	a.bus.Unsubscribe(topic, entry.internalCh)
	close(entry.internalCh)
}

// SubscribeByPrefix registers an external channel for all topics starting
// with the given prefix. An empty prefix matches all topics.
func (a *busAdapter) SubscribeByPrefix(prefix string, ch chan pkgeventbus.Event, bufferSize int) error {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	intCh := make(chan Event, bufferSize)
	stopCh := make(chan struct{})

	if err := a.bus.SubscribeByPrefix(prefix, intCh, bufferSize); err != nil {
		return err
	}

	entry := &bridgeEntry{
		externalCh: ch,
		internalCh: intCh,
		stopCh:     stopCh,
		topic:      prefix,
		isPrefix:   true,
	}

	a.mu.Lock()
	a.bridges = append(a.bridges, entry)
	a.mu.Unlock()

	go a.bridgeLoop(entry)

	return nil
}

// UnsubscribeByPrefix removes the prefix subscription. Idempotent.
func (a *busAdapter) UnsubscribeByPrefix(prefix string, ch chan pkgeventbus.Event) {
	entry := a.removeBridge(prefix, ch, true)
	if entry == nil {
		return
	}

	close(entry.stopCh)
	a.bus.UnsubscribeByPrefix(prefix, entry.internalCh)
	close(entry.internalCh)
}

// Publish sends an event to all matching subscribers (exact topic and
// prefix subscribers). Non-blocking per the underlying bus contract.
func (a *busAdapter) Publish(ctx context.Context, topic string, data any) {
	a.bus.Publish(ctx, topic, data)
}

// bridgeLoop forwards events from the internal channel to the external
// caller channel, converting Event types. Exits when stopCh is closed
// or the internal channel is closed (on unsubscribe).
func (a *busAdapter) bridgeLoop(entry *bridgeEntry) {
	for {
		select {
		case <-entry.stopCh:
			return
		case evt, ok := <-entry.internalCh:
			if !ok {
				return
			}
			select {
			case entry.externalCh <- pkgeventbus.Event{Topic: evt.Topic, Data: evt.Data}:
			case <-entry.stopCh:
				return
			}
		}
	}
}

// removeBridge finds and removes a bridge entry matching topic, channel,
// and subscription type. Returns the entry and the updated bridges slice.
// Caller must close stopCh/internalCh after removal.
// removeBridge finds and removes a bridge entry matching topic, channel,
// and subscription type. Returns the removed entry, or nil if not found.
// The removal (swap-drop + slice shrink) is performed entirely under the
// lock to prevent a data race with concurrent Subscribe.
// Caller must close stopCh/internalCh after removal.
func (a *busAdapter) removeBridge(topic string, ch chan pkgeventbus.Event, isPrefix bool) *bridgeEntry {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i, e := range a.bridges {
		if e.topic == topic && e.externalCh == ch && e.isPrefix == isPrefix {
			a.bridges[i] = a.bridges[len(a.bridges)-1]
			a.bridges = a.bridges[:len(a.bridges)-1]
			return e
		}
	}
	return nil
}
