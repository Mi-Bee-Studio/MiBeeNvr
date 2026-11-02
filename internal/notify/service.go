package notify

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// ServiceOptions carries everything the service needs, resolved from config
// by the app wiring. Primitives only — the package stays independent of
// internal/config so config can import it for vocabulary validation.
type ServiceOptions struct {
	Bus         *event.EventBus
	Sinks       []SinkOptions
	CameraNamer CameraNamer
	AILookup    AILookup
	Snapshots   Snapshots
}

// busTopics is the whitelist of event-bus topics the dispatcher subscribes
// to. Mirrors the mqtt status whitelist policy: high-frequency topics stay
// off unless they carry alert value (ai.event.created and onvif.motionalarm
// are the deliberate exceptions — motion/AI alerts are the whole point of a
// notification hub, and their volume is the receiver's aggregation problem).
var busTopics = []string{
	event.TopicAIEventCreated,
	event.TopicStorageHealthChanged,
	event.TopicCameraAdded,
	event.TopicCameraQuality,
	event.TopicGB28181Alarm,
	event.TopicDoorbellRing,
	event.TopicDoorbellAnswered,
	event.TopicDoorbellRejected,
	event.TopicDoorbellEnded,
	TopicONVIFMotionAlarm,
}

// ErrNoSinks marks an enabled notifications section without any sink.
var ErrNoSinks = errors.New("notifications.enabled is true but no sinks are configured")

// Service subscribes to the event bus, maps whitelisted events to the alert
// envelope, and fans them out to the configured webhook sinks. It also
// implements the health pipeline's alert sink interface (camera
// offline/frozen/etc. events do not travel the event bus — the pipeline
// dispatches them directly). Implements the pkg/app Service interface.
type Service struct {
	bus    *event.EventBus
	sinks  []*WebhookSink
	mapper *mapper

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	subCh  chan event.Event
}

// NewService builds the service and its sinks. Returns an error on the first
// invalid sink options (config-time failure — callers surface it).
func NewService(opts ServiceOptions) (*Service, error) {
	if len(opts.Sinks) == 0 {
		return nil, ErrNoSinks
	}
	if opts.Bus == nil {
		return nil, errors.New("notify: event bus is nil")
	}
	sinks := make([]*WebhookSink, 0, len(opts.Sinks))
	for _, so := range opts.Sinks {
		sink, err := NewWebhookSink(so)
		if err != nil {
			return nil, err
		}
		sinks = append(sinks, sink)
	}
	return &Service{
		bus:   opts.Bus,
		sinks: sinks,
		mapper: &mapper{
			nameOf:    opts.CameraNamer,
			aiLookup:  opts.AILookup,
			snapshots: opts.Snapshots,
		},
	}, nil
}

// Name implements pkg/app.Service.
func (s *Service) Name() string { return "notifications" }

// Start subscribes to the bus and launches the sink workers. Idempotent.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	subCh := make(chan event.Event, 64)
	done := make(chan struct{})
	s.cancel = cancel
	s.subCh = subCh
	s.done = done
	s.mu.Unlock()

	for _, topic := range busTopics {
		if err := s.bus.Subscribe(topic, subCh, 64); err != nil {
			s.Stop()
			return err
		}
	}
	for _, sink := range s.sinks {
		sink.Start(runCtx)
	}

	go func() {
		defer close(done)
		s.run(runCtx, subCh)
	}()
	return nil
}

// Stop unsubscribes, joins the drain loop, and stops every sink.
func (s *Service) Stop() error {
	s.mu.Lock()
	cancel, done, subCh := s.cancel, s.done, s.subCh
	s.cancel, s.done, s.subCh = nil, nil, nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if subCh != nil {
		for _, topic := range busTopics {
			s.bus.Unsubscribe(topic, subCh)
		}
	}
	if done != nil {
		<-done
	}
	for _, sink := range s.sinks {
		sink.Stop()
	}
	return nil
}

// PublishAlert implements the health pipeline's alert sink interface: map a
// health event (already cooldown-filtered and escalated) and fan out. Never
// blocks — enqueue is non-blocking by contract.
func (s *Service) PublishAlert(cameraID string, evt model.HealthEvent) {
	alert, ok := s.mapper.mapHealthEvent(cameraID, evt)
	if !ok {
		return
	}
	for _, sink := range s.sinks {
		sink.Enqueue(alert, false)
	}
}

// SendTest enqueues a notify.test alert to every sink, bypassing filters —
// the receiver setup must be verifiable regardless of the sink's event
// filter (that is the point of the button).
func (s *Service) SendTest(severity, title string) {
	if s == nil {
		// Armor against a typed-nil *Service reaching the NotifyService
		// interface: a no-op beats panicking the endpoint.
		return
	}
	a := newAlert(timeNow())
	a.Type = TypeNotifyTest
	a.Severity = normalizeSeverity(severity, SeverityInfo)
	a.Title = title
	if a.Title == "" {
		a.Title = "Test alert from MiBeeNvr"
	}
	a.Body = "This is a test notification triggered from the NVR settings."
	a.SourceRef = SystemSourceRef
	for _, sink := range s.sinks {
		sink.Enqueue(a, true)
	}
}

// Stats snapshots every sink's counters for the status endpoint.
func (s *Service) Stats() []SinkStats {
	if s == nil {
		// Armor against a typed-nil *Service reaching the NotifyService
		// interface: report empty counters instead of panicking.
		return nil
	}
	out := make([]SinkStats, 0, len(s.sinks))
	for _, sink := range s.sinks {
		out = append(out, sink.Stats())
	}
	return out
}

func (s *Service) run(ctx context.Context, ch <-chan event.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-ch:
			s.dispatch(evt)
		}
	}
}

func (s *Service) dispatch(evt event.Event) {
	alert, ok := s.mapper.mapBusEvent(evt)
	if !ok {
		return
	}
	for _, sink := range s.sinks {
		sink.Enqueue(alert, false)
	}
}

// timeNow is a seam for tests.
var timeNow = time.Now
