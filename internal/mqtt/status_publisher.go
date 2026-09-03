package mqtt

import (
	"context"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// MQTTPublisher is the publish surface the status publisher needs.
// Satisfied by *mqtt.Client; narrow interface keeps the publisher testable
// without a broker (same duck-typing as health.AlertPipeline).
type MQTTPublisher interface {
	Publish(topic string, payload any) error
}

// statusTopics is the whitelist of event-bus topics forwarded to MQTT.
// Published as `{topicPrefix}/event/<event-topic>` with the event payload
// passed through unchanged (JSON-marshaled by Client.Publish). High-frequency
// topics (ai.detection.*) stay off the whitelist on purpose.
var statusTopics = []string{
	event.TopicSegmentCompleted,
	event.TopicCameraAdded,
	event.TopicCameraQuality,
	event.TopicStorageHealthChanged,
	// Persisted snapshot metadata (file_path relative to storage root) so
	// smart-home automations can react to MQTT-triggered captures (#656).
	event.TopicCameraSnapshot,
}

var statusLogger = slogx.Component("mqtt-status")

// StatusPublisher forwards whitelisted event-bus topics to MQTT so smart-home
// platforms (Home Assistant etc.) can consume NVR state without REST polling
// or an SSE bridge. Implements the pkg/app Service interface.
type StatusPublisher struct {
	bus  *event.EventBus
	mqtt MQTTPublisher

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	subCh  chan event.Event
}

// NewStatusPublisher builds the publisher. mqtt may be nil until the broker
// client connects — events observed before then are dropped with a warning.
func NewStatusPublisher(bus *event.EventBus, mqtt MQTTPublisher) *StatusPublisher {
	return &StatusPublisher{bus: bus, mqtt: mqtt}
}

// Name implements pkg/app.Service.
func (p *StatusPublisher) Name() string { return "mqtt-status" }

// Start subscribes to the whitelisted topics and launches the drain loop.
func (p *StatusPublisher) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.cancel != nil {
		p.mu.Unlock()
		return nil // already running
	}
	runCtx, cancel := context.WithCancel(ctx)
	subCh := make(chan event.Event, 64)
	p.cancel = cancel
	p.subCh = subCh
	done := make(chan struct{})
	p.done = done
	p.mu.Unlock()

	for _, topic := range statusTopics {
		if err := p.bus.Subscribe(topic, subCh, 64); err != nil {
			p.stopLocked()
			return err
		}
	}

	go func() {
		defer close(done)
		p.run(runCtx, subCh)
	}()
	return nil
}

// Stop unsubscribes and joins the drain goroutine.
func (p *StatusPublisher) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
	return nil
}

func (p *StatusPublisher) stopLocked() {
	cancel, done, subCh := p.cancel, p.done, p.subCh
	p.cancel, p.done, p.subCh = nil, nil, nil

	if cancel != nil {
		cancel()
	}
	if subCh != nil {
		for _, topic := range statusTopics {
			p.bus.Unsubscribe(topic, subCh)
		}
	}
	if done != nil {
		<-done
	}
}

// run drains events until ctx is cancelled. A slow broker can block here —
// that is intentional backpressure: the bus drops the oldest event when the
// channel is full, so recorders are never blocked.
func (p *StatusPublisher) run(ctx context.Context, ch <-chan event.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-ch:
			p.forward(evt)
		}
	}
}

func (p *StatusPublisher) forward(evt event.Event) {
	if p.mqtt == nil {
		return
	}
	topic := "event/" + evt.Topic
	if err := p.mqtt.Publish(topic, evt.Data); err != nil {
		statusLogger.Warn("failed to forward event to MQTT", "topic", topic, "error", err)
	}
}
