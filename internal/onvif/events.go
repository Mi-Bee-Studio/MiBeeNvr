package onvif

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	onvifgo "github.com/0x524a/onvif-go"
)

var eventLogger = slog.Default().With("component", "onvif-events")

// DefaultPollInterval is the interval between PullMessages calls when no events are pending.
const DefaultPollInterval = 5 * time.Second

// DefaultPullTimeout is the timeout passed to PullMessages SOAP calls.
const DefaultPullTimeout = 30 * time.Second

// DefaultMessageLimit is the max number of messages requested per PullMessages call.
const DefaultMessageLimit = 10

// DefaultSubscriptionDuration is the requested PullPoint subscription lifetime.
const DefaultSubscriptionDuration = 24 * time.Hour

// SubscriptionRenewBefore is how long before expiry to attempt renewal.
const SubscriptionRenewBefore = 1 * time.Hour

// EventCallback is a function that receives parsed ONVIF events.
// The camera manager wires this to publish events to the EventBus.
type EventCallback func(event ONVIFEvent)

// EventSubscriberImpl manages PullPoint subscriptions and event polling
// for a single ONVIF device. It wraps an onvif-go Client and handles
// the full subscription lifecycle: create, poll, renew, unsubscribe.
type EventSubscriberImpl struct {
	client *onvifgo.Client

	mu            sync.Mutex
	subscriptions map[string]*pullPointSubscription // cameraID → subscription
	stopCh        map[string]chan struct{}           // cameraID → stop channel

	eventCallback EventCallback       // called when events are received
	pollInterval  time.Duration       // interval between poll cycles
	pullTimeout   time.Duration       // SOAP timeout for PullMessages
	messageLimit  int                 // max messages per PullMessages call
	subDuration   time.Duration       // requested subscription lifetime
}

type pullPointSubscription struct {
	subscriptionRef string    // PullPoint subscription reference URL
	terminationTime time.Time // subscription expiry time
	active         bool      // whether the polling goroutine is running
}

// NewEventSubscriber creates an EventSubscriber backed by an onvif-go client.
func NewEventSubscriber(client *onvifgo.Client, opts ...EventSubscriberOption) *EventSubscriberImpl {
	es := &EventSubscriberImpl{
		client:        client,
		subscriptions: make(map[string]*pullPointSubscription),
		stopCh:        make(map[string]chan struct{}),
		pollInterval:  DefaultPollInterval,
		pullTimeout:   DefaultPullTimeout,
		messageLimit:  DefaultMessageLimit,
		subDuration:   DefaultSubscriptionDuration,
	}
	for _, opt := range opts {
		opt(es)
	}
	return es
}

// EventSubscriberOption configures EventSubscriberImpl.
type EventSubscriberOption func(*EventSubscriberImpl)

// WithEventCallback sets the callback invoked when events are received.
func WithEventCallback(cb EventCallback) EventSubscriberOption {
	return func(es *EventSubscriberImpl) {
		es.eventCallback = cb
	}
}

// WithPollInterval sets the polling interval between PullMessages calls.
func WithPollInterval(d time.Duration) EventSubscriberOption {
	return func(es *EventSubscriberImpl) {
		es.pollInterval = d
	}
}

// WithPullTimeout sets the SOAP timeout for PullMessages requests.
func WithPullTimeout(d time.Duration) EventSubscriberOption {
	return func(es *EventSubscriberImpl) {
		es.pullTimeout = d
	}
}

// WithSubscriptionDuration sets the requested PullPoint subscription lifetime.
func WithSubscriptionDuration(d time.Duration) EventSubscriberOption {
	return func(es *EventSubscriberImpl) {
		es.subDuration = d
	}
}

// Subscribe creates a PullPoint subscription for the camera and starts
// background polling. Safe to call multiple times — returns nil if already
// subscribed.
func (e *EventSubscriberImpl) Subscribe(ctx context.Context, cameraID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.subscriptions[cameraID]; exists {
		return nil // Already subscribed
	}

	// Create PullPoint subscription via onvif-go
	sub, err := e.client.CreatePullPointSubscription(ctx, "", &e.subDuration, "")
	if err != nil {
		return fmt.Errorf("onvif: create PullPoint subscription for camera %q: %w", cameraID, err)
	}

	ps := &pullPointSubscription{
		subscriptionRef: sub.SubscriptionReference,
		terminationTime: sub.TerminationTime,
		active:          true,
	}
	e.subscriptions[cameraID] = ps

	stopCh := make(chan struct{})
	e.stopCh[cameraID] = stopCh

	eventLogger.Info("created PullPoint subscription",
		"camera_id", cameraID,
		"subscription_ref", sub.SubscriptionReference,
		"termination", sub.TerminationTime)

	// Start background polling goroutine
	go e.publishEvents(context.Background(), cameraID, ps, stopCh)

	return nil
}

// Unsubscribe terminates the PullPoint subscription for the camera and
// stops the polling goroutine.
func (e *EventSubscriberImpl) Unsubscribe(ctx context.Context, cameraID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	ps, exists := e.subscriptions[cameraID]
	if !exists {
		return nil // Not subscribed
	}

	// Signal polling goroutine to stop
	if stopCh, ok := e.stopCh[cameraID]; ok {
		close(stopCh)
		delete(e.stopCh, cameraID)
	}

	ps.active = false

	// Unsubscribe via onvif-go (fire-and-forget on error, nil client means test mode)
	if e.client != nil {
		if err := e.client.Unsubscribe(ctx, ps.subscriptionRef); err != nil {
			eventLogger.Warn("failed to unsubscribe from PullPoint",
				"camera_id", cameraID,
				"subscription_ref", ps.subscriptionRef,
				"error", err)
		} else {
			eventLogger.Info("unsubscribed from PullPoint",
				"camera_id", cameraID,
				"subscription_ref", ps.subscriptionRef)
		}
	}

	delete(e.subscriptions, cameraID)
	return nil
}

// GetEventMessages returns nil — events are delivered via callback in real-time.
// This method exists to satisfy the EventSubscriber interface.
func (e *EventSubscriberImpl) GetEventMessages(_ context.Context) ([]ONVIFEvent, error) {
	return nil, nil
}

// IsSubscribed returns whether the camera has an active PullPoint subscription.
func (e *EventSubscriberImpl) IsSubscribed(cameraID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	ps, exists := e.subscriptions[cameraID]
	return exists && ps.active
}

// StopAll unsubscribes from all cameras and stops all polling goroutines.
func (e *EventSubscriberImpl) StopAll(ctx context.Context) {
	e.mu.Lock()
	cameraIDs := make([]string, 0, len(e.subscriptions))
	for id := range e.subscriptions {
		cameraIDs = append(cameraIDs, id)
	}
	e.mu.Unlock()

	for _, id := range cameraIDs {
		_ = e.Unsubscribe(ctx, id)
	}
}

// SetEventCallback updates the event callback function. Thread-safe.
func (e *EventSubscriberImpl) SetEventCallback(cb EventCallback) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.eventCallback = cb
}

// publishEvents polls the PullPoint subscription and publishes events.
// This runs as a background goroutine until the stop channel is closed or
// context is cancelled.
func (e *EventSubscriberImpl) publishEvents(ctx context.Context, cameraID string, ps *pullPointSubscription, stopCh <-chan struct{}) {
	ticker := time.NewTicker(e.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !e.pollAndPublish(ctx, cameraID, ps) {
				// Subscription may have expired or been terminated
				return
			}
		}
	}
}

// pollAndPublish performs a single PullMessages call and publishes results.
// Returns false if the subscription should be terminated.
func (e *EventSubscriberImpl) pollAndPublish(ctx context.Context, cameraID string, ps *pullPointSubscription) bool {
	// Check subscription renewal
	if time.Until(ps.terminationTime) < SubscriptionRenewBefore {
		if !e.renewSubscription(ctx, cameraID, ps) {
			eventLogger.Warn("subscription renewal failed, stopping polling",
				"camera_id", cameraID)
			ps.active = false
			return false
		}
	}

	// Pull messages from the subscription
	messages, err := e.client.PullMessages(ctx, ps.subscriptionRef, e.pullTimeout, e.messageLimit)
	if err != nil {
		eventLogger.Warn("PullMessages failed",
			"camera_id", cameraID,
			"error", err)
		return true // Continue polling — transient errors
	}

	// Parse and publish events
	e.mu.Lock()
	callback := e.eventCallback
	e.mu.Unlock()

	for _, msg := range messages {
		event := parseNotificationMessage(msg, cameraID)
		if event.Topic == "" {
			continue // Skip messages without topics
		}

		if callback != nil {
			callback(event)
		}

		eventLogger.Debug("received ONVIF event",
			"camera_id", cameraID,
			"topic", event.Topic,
			"timestamp", event.Timestamp)
	}

	return true
}

// renewSubscription attempts to renew the PullPoint subscription before expiry.
func (e *EventSubscriberImpl) renewSubscription(ctx context.Context, cameraID string, ps *pullPointSubscription) bool {
	_, newTermination, err := e.client.RenewSubscription(ctx, ps.subscriptionRef, e.subDuration)
	if err != nil {
		eventLogger.Warn("failed to renew PullPoint subscription",
			"camera_id", cameraID,
			"error", err)
		return false
	}

	ps.terminationTime = newTermination
	eventLogger.Info("renewed PullPoint subscription",
		"camera_id", cameraID,
		"new_termination", newTermination)
	return true
}

// parseNotificationMessage converts an onvif-go NotificationMessage to an ONVIFEvent.
func parseNotificationMessage(msg onvifgo.NotificationMessage, cameraID string) ONVIFEvent {
	event := ONVIFEvent{
		Topic:    msg.Topic,
		Data:     make(map[string]any),
		CameraID: cameraID,
	}

	// Use message UTC time if available
	if !msg.Message.UtcTime.IsZero() {
		event.Timestamp = msg.Message.UtcTime
	} else {
		event.Timestamp = time.Now().UTC()
	}

	// Extract data items from the message
	for _, item := range msg.Message.Data {
		if item.Name != "" {
			event.Data[item.Name] = item.Value
		}
	}

	// Extract source items as metadata
	for _, item := range msg.Message.Source {
		if item.Name != "" {
			event.Data["source."+item.Name] = item.Value
		}
	}

	return event
}

// Ensure EventSubscriberImpl satisfies the EventSubscriber interface.
var _ EventSubscriber = (*EventSubscriberImpl)(nil)
