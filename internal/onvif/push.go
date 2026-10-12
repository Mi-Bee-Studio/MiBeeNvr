package onvif

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mickeyzzc/onvif-go/v2/events"
)

// WS-BaseNotification push subscriptions (#922): instead of the NVR polling
// PullMessages, the device POSTs wsnt:Notify messages to an HTTP consumer
// endpoint on the NVR (/api/onvif/notify/{cameraID}/{token}). Push is
// attempted first whenever the NVR has an externally reachable base URL
// configured (server.advertise_url); a device that answers the Subscribe
// probe with a SOAP fault keeps the Pull-Point path unchanged.
//
// The SOAP/WSEN primitives (subscribe probe, renew, unsubscribe, Notify
// parsing) live in onvif-go's events package (onvif-go#127, ported from this
// implementation). What stays here is NVR policy: the probe→fallback
// decision, the half-TTL renew loop with its floor, the diagnostics the
// events panel shows, and the ONVIFEvent conversion the SSE bus consumes.

const (
	// DefaultPushDuration is the requested push subscription lifetime.
	DefaultPushDuration = time.Hour
	// pushProbeTimeout bounds the one-shot Subscribe probe — it runs inline
	// in the subscription reconcile, so it must not stall camera startup.
	pushProbeTimeout = 5 * time.Second
	// pushRenewFloor is the minimum delay before a half-TTL renew fires —
	// devices that grant very short terms must not turn the renew loop into
	// a busy cycle.
	pushRenewFloor = 2 * time.Second
)

// ErrPushNotSupported reports that the device answered the wsnt:Subscribe
// probe with a SOAP (Sender) fault or otherwise rejected push — the caller
// falls back to the Pull-Point subscription instead. The library's sentinel,
// re-exported so callers keep one errors.Is target.
var ErrPushNotSupported = events.ErrPushNotSupported

var pushLogger = eventLogger

// pushEventsAPI is the library events surface the push subscriber drives.
// The production implementation is (*onvifgo.Client).Events(); tests
// substitute an in-memory fake.
type pushEventsAPI interface {
	Subscribe(ctx context.Context, consumerReference string, initialTermination time.Duration) (*events.PushSubscription, error)
	RenewPush(ctx context.Context, managerAddress string, termination time.Duration) (time.Time, error)
	UnsubscribePush(ctx context.Context, managerAddress string) error
}

// EventDeliverer is implemented by subscribers that receive events pushed
// from the device (the notify HTTP handler hands parsed events to the live
// subscriber so its counters/diagnostics stay meaningful).
type EventDeliverer interface {
	DeliverEvent(evt ONVIFEvent)
}

// PushOption configures a PushSubscriber.
type PushOption func(*PushSubscriber)

// WithPushCallback sets the callback invoked for each event the device
// pushes. Required — a push subscription without a callback is useless.
func WithPushCallback(cb EventCallback) PushOption {
	return func(p *PushSubscriber) { p.callback = cb }
}

// WithPushNotifyURL sets the consumer endpoint the device POSTs Notify
// messages to (server.advertise_url + /api/onvif/notify/{cameraID}/{token}).
func WithPushNotifyURL(url string) PushOption {
	return func(p *PushSubscriber) { p.notifyURL = url }
}

// WithPushFallback sets the one-shot hook fired when a renewal fails — the
// camera manager degrades the camera to the Pull-Point transport.
func WithPushFallback(f func(cameraID, reason string)) PushOption {
	return func(p *PushSubscriber) { p.onFallback = f }
}

// WithPushDuration overrides the requested subscription lifetime.
func WithPushDuration(d time.Duration) PushOption {
	return func(p *PushSubscriber) { p.duration = d }
}

// PushSubscriber keeps a wsnt:Subscribe alive against one camera (see the
// file comment), then feeds device-pushed events to the callback. It
// satisfies EventSubscriber so the camera manager's subscriber registry,
// teardown and diagnostics work identically for both transports.
//
// Subscriptions are NOT persisted: after an NVR restart the old subscription
// is bounded by its TTL (and the device-side "3 failed deliveries" cleanup),
// and the new process simply subscribes again.
type PushSubscriber struct {
	ev         pushEventsAPI
	callback   EventCallback
	notifyURL  string
	onFallback func(cameraID, reason string)
	duration   time.Duration

	mu              sync.Mutex
	subscriptionRef string // SubscriptionReference address (diagnostics)
	managerAddr     string // SubscriptionManager address (Renew/Unsubscribe)
	terminationTime time.Time
	grantedDur      time.Duration
	state           string
	lastError       string
	eventCount      int64
	lastEventAt     time.Time
	lastEvent       string
	stopCh          chan struct{}
	unsubscribeOnce sync.Once
	fallbackOnce    sync.Once
}

var _ EventSubscriber = (*PushSubscriber)(nil)

// NewPushSubscriber builds a PushSubscriber over the library events service.
func NewPushSubscriber(ev pushEventsAPI, opts ...PushOption) *PushSubscriber {
	p := &PushSubscriber{
		ev:       ev,
		duration: DefaultPushDuration,
		state:    StateResubscribing,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Subscribe probes the device with a wsnt:Subscribe. A sender-side SOAP
// fault maps to ErrPushNotSupported; on success the half-TTL renew loop
// starts.
func (p *PushSubscriber) Subscribe(ctx context.Context, cameraID string) error {
	if p.notifyURL == "" {
		return fmt.Errorf("push subscriber for camera %q: no notify URL configured", cameraID)
	}
	probeCtx, cancel := context.WithTimeout(ctx, pushProbeTimeout)
	defer cancel()

	sub, err := p.ev.Subscribe(probeCtx, p.notifyURL, p.duration)
	if err != nil {
		if errors.Is(err, events.ErrPushNotSupported) {
			return fmt.Errorf("%w (camera %q): %w", ErrPushNotSupported, cameraID, err)
		}
		return fmt.Errorf("push subscribe for camera %q: %w", cameraID, err)
	}

	granted := time.Until(sub.TerminationTime)
	if granted <= 0 {
		granted = p.duration
	}

	p.mu.Lock()
	p.subscriptionRef = sub.SubscriptionReference
	p.managerAddr = sub.ManagerAddress
	p.terminationTime = sub.TerminationTime
	p.grantedDur = granted
	p.state = StateActive
	p.lastError = ""
	stopCh := make(chan struct{})
	p.stopCh = stopCh
	p.mu.Unlock()

	pushLogger.Info("created push subscription",
		"camera_id", cameraID,
		"subscription_ref", sub.SubscriptionReference,
		"granted", granted.String())

	go p.renewLoop(cameraID, stopCh)
	return nil
}

// renewLoop renews the subscription at half the granted TTL. Any renew
// failure trips the fallback hook once — the camera manager then degrades the
// camera to the Pull-Point transport.
func (p *PushSubscriber) renewLoop(cameraID string, stopCh chan struct{}) {
	for {
		p.mu.Lock()
		delay := p.grantedDur / 2
		p.mu.Unlock()
		if delay < pushRenewFloor {
			delay = pushRenewFloor
		}
		select {
		case <-stopCh:
			return
		case <-time.After(delay):
		}

		p.mu.Lock()
		addr := p.managerAddr
		termination := p.terminationTime
		p.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), pushProbeTimeout)
		newTerm, err := p.ev.RenewPush(ctx, addr, p.duration)
		cancel()
		if err != nil {
			p.mu.Lock()
			p.state = StateResubscribing
			p.lastError = err.Error()
			p.mu.Unlock()
			pushLogger.Warn("push subscription renew failed; degrading to pull-point",
				"camera_id", cameraID, "error", err)
			p.fallbackOnce.Do(func() {
				if p.onFallback != nil {
					p.onFallback(cameraID, err.Error())
				}
			})
			return
		}

		granted := time.Until(newTerm)
		if granted <= 0 {
			granted = time.Until(termination) / 2
		}
		p.mu.Lock()
		p.terminationTime = newTerm
		p.grantedDur = granted
		p.mu.Unlock()
	}
}

// DeliverEvent feeds one parsed Notify event into the callback and updates
// the diagnostics counters. Called by the notify HTTP handler.
func (p *PushSubscriber) DeliverEvent(evt ONVIFEvent) {
	p.mu.Lock()
	p.eventCount++
	p.lastEventAt = time.Now().UTC()
	p.lastEvent = formatRawEvent(evt)
	cb := p.callback
	p.mu.Unlock()
	if cb != nil {
		cb(evt)
	}
}

// GetEventMessages is part of the EventSubscriber interface. Push events
// arrive via the HTTP notify endpoint, not a client-side queue — always nil.
func (p *PushSubscriber) GetEventMessages(_ context.Context) ([]ONVIFEvent, error) {
	return nil, nil
}

// Status reports the push subscription diagnostics.
func (p *PushSubscriber) Status(string) EventSubscriptionStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return EventSubscriptionStatus{
		Subscribed:      p.state == StateActive,
		State:           p.state,
		Transport:       "push",
		SubscriptionRef: p.subscriptionRef,
		TerminationTime: p.terminationTime,
		EventCount:      p.eventCount,
		LastEventAt:     p.lastEventAt,
		LastEvent:       p.lastEvent,
		LastError:       p.lastError,
	}
}

// Unsubscribe stops the renew loop and fires a best-effort wsnt:Unsubscribe
// at the SubscriptionManager. Idempotent.
func (p *PushSubscriber) Unsubscribe(ctx context.Context, cameraID string) error {
	p.mu.Lock()
	stopCh := p.stopCh
	addr := p.managerAddr
	p.state = ""
	p.mu.Unlock()

	var err error
	p.unsubscribeOnce.Do(func() {
		if stopCh != nil {
			close(stopCh)
		}
		if addr != "" {
			unsubCtx, cancel := context.WithTimeout(ctx, pushProbeTimeout)
			serr := p.ev.UnsubscribePush(unsubCtx, addr)
			cancel()
			if serr != nil {
				pushLogger.Debug("push unsubscribe failed (TTL will reclaim it)",
					"camera_id", cameraID, "error", serr)
			}
			err = serr
		}
	})
	return err
}

// ParseNotifyEvents parses a wsnt:Notify SOAP body (what the device POSTs to
// the consumer endpoint) into events, mirroring the Pull-Point
// parseNotificationMessage semantics: Topic, Message/@UtcTime, Data items and
// source.* metadata. The tolerant XML walking (prefix dialects, canonical
// double-layer payload) lives in the library parser; this adapter only maps
// the library's NotificationMessage into the NVR's ONVIFEvent form.
func ParseNotifyEvents(body []byte, cameraID string) ([]ONVIFEvent, error) {
	msgs, err := events.ParseNotifyEvents(body)
	if err != nil {
		return nil, err
	}
	out := make([]ONVIFEvent, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, parseNotificationMessage(msg, cameraID))
	}
	return out, nil
}
