package onvif

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	onvifgo "github.com/mickeyzzc/onvif-go/v2/onvif"
)

var eventLogger = slogx.Component("onvif-events")

// ErrEventsNotSupported indicates the device does not implement the ONVIF event
// PullPoint subscription (some cameras advertise the event service in
// GetCapabilities but return "Action Not Implemented" on
// CreatePullPointSubscription). Callers can cache this result to avoid
// repeatedly attempting a subscription that will never succeed.
var ErrEventsNotSupported = errors.New("onvif: device does not support event pull-point subscription")

// isEventsNotSupportedError reports whether err represents the device rejecting
// event subscription as unimplemented. Devices phrase this variously:
// "Action Not Implemented", "ActionNotSupported", "NotImplemented", etc.
func isEventsNotSupportedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not implemented") ||
		strings.Contains(msg, "notimplemented") ||
		strings.Contains(msg, "action not supported") ||
		strings.Contains(msg, "actionnotsupported")
}

// DefaultPollInterval is the interval between PullMessages calls when no events are pending.
const DefaultPollInterval = 5 * time.Second

// DefaultPullTimeout is the timeout passed to PullMessages SOAP calls.
const DefaultPullTimeout = 30 * time.Second

// DefaultMessageLimit is the max number of messages requested per PullMessages call.
const DefaultMessageLimit = 10

// DefaultSubscriptionDuration is the requested PullPoint subscription lifetime.
const DefaultSubscriptionDuration = 24 * time.Hour

// SubscriptionRenewBefore is the upper bound on how much remaining lifetime
// triggers a renewal. The effective threshold is min(this, half the GRANTED
// lifetime): devices that grant short terms regardless of the request (the
// mibee_cam contract grants exactly 1h) must not renew on every poll tick.
const SubscriptionRenewBefore = 1 * time.Hour

// terminalPollFailures is how many consecutive failed PullMessages calls
// presume the subscription dead (expired after a 120s no-pull window,
// SubscriptionReferenceDereferenced, device reboot) and trigger a rebuild.
// Devices that long-poll block up to PullTimeout per call, so this bound also
// caps the time-to-recovery.
const terminalPollFailures = 5

// Resubscribe backoff bounds: start at resubscribeMinBackoff, double per
// failure, capped at resubscribeMaxBackoff.
const (
	resubscribeMinBackoff = 5 * time.Second
	resubscribeMaxBackoff = 60 * time.Second
)

// Subscription states reported by Status.
const (
	StateActive        = "active"
	StateResubscribing = "resubscribing"
	StateUnsupported   = "unsupported"
)

// EventCallback is a function that receives parsed ONVIF events.
// The camera manager wires this to publish events to the EventBus.
type EventCallback func(event ONVIFEvent)

// eventsAPI is the events-service surface EventSubscriberImpl drives.
// Satisfied by the onvif-go events service; a narrow interface keeps the
// subscription lifecycle unit-testable with an in-memory fake.
type eventsAPI interface {
	CreatePullPointSubscription(ctx context.Context, filter string, initialTerminationTime *time.Duration, subscriptionPolicy string) (*onvifgo.PullPointSubscription, error)
	PullMessages(ctx context.Context, subscriptionReference string, timeout time.Duration, messageLimit int) ([]onvifgo.NotificationMessage, error)
	RenewSubscription(ctx context.Context, subscriptionReference string, terminationTime time.Duration) (time.Time, time.Time, error)
	Unsubscribe(ctx context.Context, subscriptionReference string) error
}

// EventSubscriberImpl manages PullPoint subscriptions and event polling
// for a single ONVIF device. It wraps an onvif-go Client and handles
// the full subscription lifecycle: create, poll, renew, resubscribe on
// terminal failures, unsubscribe.
type EventSubscriberImpl struct {
	events eventsAPI

	mu            sync.Mutex
	subscriptions map[string]*pullPointSubscription // cameraID → subscription
	stopCh        map[string]chan struct{}          // cameraID → stop channel

	eventCallback EventCallback // called when events are received
	pollInterval  time.Duration // interval between poll cycles
	pullTimeout   time.Duration // SOAP timeout for PullMessages
	messageLimit  int           // max messages per PullMessages call
	subDuration   time.Duration // requested subscription lifetime
	resubBackoff  time.Duration // initial rebuild backoff (doubles to max)
}

type pullPointSubscription struct {
	subscriptionRef string    // PullPoint subscription reference URL
	terminationTime time.Time // subscription expiry time
	grantedDur      time.Duration
	active          bool   // whether the polling goroutine is running
	state           string // active | resubscribing | unsupported (diagnostics)
	eventCount      int64
	lastEventAt     time.Time
	lastError       string
	pollErrs        int
}

// NewEventSubscriber creates an EventSubscriber backed by an onvif-go client.
func NewEventSubscriber(client *onvifgo.Client, opts ...EventSubscriberOption) *EventSubscriberImpl {
	return newEventSubscriber(client.Events(), opts...)
}

// newEventSubscriber builds the subscriber around an eventsAPI — the test seam.
func newEventSubscriber(svc eventsAPI, opts ...EventSubscriberOption) *EventSubscriberImpl {
	es := &EventSubscriberImpl{
		events:        svc,
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

// WithPollInterval sets the polling interval between PullMessages calls. The
// mibee_cam contract suggests 0.5–1s for motion-trigger use; the default 5s is
// compliant (the device only expires a subscription after 120s without a
// pull) but adds that much trigger latency.
func WithPollInterval(d time.Duration) EventSubscriberOption {
	return func(es *EventSubscriberImpl) {
		if d > 0 {
			es.pollInterval = d
		}
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

// withResubscribeBackoff overrides the initial rebuild backoff (test seam for
// fast loops; production keeps resubscribeMinBackoff).
func withResubscribeBackoff(d time.Duration) EventSubscriberOption {
	return func(es *EventSubscriberImpl) {
		if d > 0 {
			es.resubBackoff = d
		}
	}
}

// renewThreshold is the remaining-lifetime threshold that triggers renewal:
// half the granted duration, clamped to [2×pollInterval, SubscriptionRenewBefore].
func renewThreshold(grantedDur, pollInterval time.Duration) time.Duration {
	th := grantedDur / 2
	if th > SubscriptionRenewBefore {
		th = SubscriptionRenewBefore
	}
	if minTh := 2 * pollInterval; th < minTh {
		th = minTh
	}
	return th
}

// Subscribe creates a PullPoint subscription for the camera and starts
// background polling. Safe to call multiple times — returns nil if already
// subscribed (including the "unsupported" tombstone, so unsupported devices
// are not re-probed on every reconcile).
func (e *EventSubscriberImpl) Subscribe(ctx context.Context, cameraID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.subscriptions[cameraID]; exists {
		return nil // Already subscribed (or tombstoned as unsupported)
	}

	ps, err := e.createSubscriptionLocked(ctx, cameraID)
	if err != nil {
		return err
	}
	e.subscriptions[cameraID] = ps

	stopCh := make(chan struct{})
	e.stopCh[cameraID] = stopCh

	eventLogger.Info("created PullPoint subscription",
		"camera_id", cameraID,
		"subscription_ref", ps.subscriptionRef,
		"granted", ps.grantedDur.String(),
		"termination", ps.terminationTime)

	// Start the supervised polling goroutine. context.Background(): the loop
	// owns its per-call timeouts and is stopped via stopCh (Unsubscribe).
	go e.run(cameraID, stopCh)

	return nil
}

// createSubscriptionLocked performs one CreatePullPointSubscription call and
// returns the resulting pullPointSubscription. Caller holds e.mu.
func (e *EventSubscriberImpl) createSubscriptionLocked(ctx context.Context, cameraID string) (*pullPointSubscription, error) {
	sub, err := e.events.CreatePullPointSubscription(ctx, "", &e.subDuration, "")
	if err != nil {
		// Some cameras advertise the event service in GetCapabilities but reject
		// the actual subscription as unimplemented. Tombstone the camera so
		// callers stop probing instead of retrying forever.
		if isEventsNotSupportedError(err) {
			eventLogger.Info("device does not support ONVIF events; skipping subscription",
				"camera_id", cameraID, "error", err)
			e.subscriptions[cameraID] = &pullPointSubscription{
				active:  false,
				state:   StateUnsupported,
				lastError: err.Error(),
			}
			return nil, fmt.Errorf("%w (camera %q): %w", ErrEventsNotSupported, cameraID, err)
		}
		return nil, fmt.Errorf("onvif: create PullPoint subscription for camera %q: %w", cameraID, err)
	}

	term := sub.TerminationTime
	if term.IsZero() {
		term = time.Now().Add(e.subDuration)
	}
	granted := time.Until(term)
	if granted <= 0 {
		granted = e.subDuration
	}
	return &pullPointSubscription{
		subscriptionRef: sub.SubscriptionReference,
		terminationTime: term,
		grantedDur:      granted,
		active:          true,
		state:           StateActive,
	}, nil
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

	// Signal the run goroutine to stop, then let it drain.
	if stopCh, ok := e.stopCh[cameraID]; ok {
		close(stopCh)
		delete(e.stopCh, cameraID)
	}

	ps.active = false

	// Unsubscribe via onvif-go (fire-and-forget on error; nil service = test mode)
	if e.events != nil && ps.subscriptionRef != "" {
		if err := e.events.Unsubscribe(ctx, ps.subscriptionRef); err != nil {
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

// Status reports the subscription diagnostics for a camera — the "订阅挂了 or
// 相机没事件" discriminator for the UI diagnostics line.
func (e *EventSubscriberImpl) Status(cameraID string) EventSubscriptionStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	ps, exists := e.subscriptions[cameraID]
	st := EventSubscriptionStatus{State: ""}
	if !exists {
		return st
	}
	st.Subscribed = ps.active
	st.State = ps.state
	st.SubscriptionRef = ps.subscriptionRef
	st.TerminationTime = ps.terminationTime
	st.EventCount = ps.eventCount
	st.LastEventAt = ps.lastEventAt
	st.LastError = ps.lastError
	st.ConsecutivePollErrors = ps.pollErrs
	st.PollInterval = e.pollInterval.String()
	return st
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

// EventSubscriptionStatus is the per-camera subscription diagnostic snapshot.
type EventSubscriptionStatus struct {
	Subscribed           bool      `json:"subscribed"`
	State                string    `json:"state"`
	SubscriptionRef      string    `json:"subscription_ref,omitempty"`
	TerminationTime      time.Time `json:"termination_time,omitempty"`
	PollInterval         string    `json:"poll_interval,omitempty"`
	EventCount           int64     `json:"event_count"`
	LastEventAt          time.Time `json:"last_event_at,omitempty"`
	LastError            string    `json:"last_error,omitempty"`
	ConsecutivePollErrors int       `json:"consecutive_poll_errors"`
}

// run is the supervised polling loop for one camera: poll until a terminal
// condition, then rebuild the subscription with backoff. Terminal-but-curable
// conditions (expiry, dereference, device reboot) loop forever; only
// "device does not implement events" (tombstone) and stopCh end the loop.
func (e *EventSubscriberImpl) run(cameraID string, stopCh <-chan struct{}) {
	backoff := e.resubBackoff
	if backoff <= 0 {
		backoff = resubscribeMinBackoff
	}
	for {
		select {
		case <-stopCh:
			return
		default:
		}

		ps := e.subLocked(cameraID)
		if ps == nil {
			return // Unsubscribe removed us
		}
		if ps.state == StateUnsupported {
			return // tombstone — never retry
		}
		if !ps.active || ps.state != StateActive {
			// Rebuild path: create a fresh subscription (the device's
			// single-subscription model means the new create replaces any
			// stale server-side state — later client wins).
			newPS, err := e.rebuildSubscription(cameraID, ps)
			if err != nil {
				if errors.Is(err, ErrEventsNotSupported) {
					return
				}
				if !sleepInterruptible(stopCh, backoff) {
					return
				}
				backoff = min(backoff*2, resubscribeMaxBackoff)
				continue
			}
			ps = newPS
			backoff = resubscribeMinBackoff
		}

		if !e.pollLoop(cameraID, ps, stopCh) {
			return // stopCh closed
		}
		// pollLoop hit a terminal condition — brief backoff, then rebuild.
		e.setPS(cameraID, func(p *pullPointSubscription) { p.active = false; p.state = StateResubscribing })
		if !sleepInterruptible(stopCh, backoff) {
			return
		}
		backoff = min(backoff*2, resubscribeMaxBackoff)
	}
}

// rebuildSubscription tears down the current subscription server-side and
// installs a fresh one in the registry (keeping the same stop channel).
func (e *EventSubscriberImpl) rebuildSubscription(cameraID string, old *pullPointSubscription) (*pullPointSubscription, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if old.subscriptionRef != "" && e.events != nil {
		_ = e.events.Unsubscribe(ctx, old.subscriptionRef) // best-effort; refs are usually already dead
	}
	var fresh *pullPointSubscription
	e.mu.Lock()
	err := func() error {
		var cerr error
		fresh, cerr = e.createSubscriptionLocked(ctx, cameraID)
		return cerr
	}()
	if err != nil {
		e.mu.Unlock()
		e.setPS(cameraID, func(p *pullPointSubscription) { p.lastError = err.Error() })
		return nil, err
	}
	e.subscriptions[cameraID] = fresh
	e.mu.Unlock()
	eventLogger.Info("rebuilt PullPoint subscription",
		"camera_id", cameraID, "subscription_ref", fresh.subscriptionRef,
		"granted", fresh.grantedDur.String())
	return fresh, nil
}

// pollLoop ticks PullMessages until a terminal condition (subscription expired
// or terminalPollFailures consecutive errors) or stop. Returns false only when
// stopCh closed; true means "resubscribe and continue".
func (e *EventSubscriberImpl) pollLoop(cameraID string, ps *pullPointSubscription, stopCh <-chan struct{}) bool {
	ticker := time.NewTicker(e.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return false
		case <-ticker.C:
		}

		// Renewal check: renew before the term lapses. An already-expired
		// term is NOT terminal by itself — devices with loose expiry may still
		// honor a Renew (and the pre-#711 behavior relied on that); the ref
		// is only presumed dead when pulls keep failing (below).
		if remaining := time.Until(e.termOf(cameraID)); remaining < renewThreshold(e.grantedOf(cameraID), e.pollInterval) {
			if !e.renewSubscription(cameraID) {
				e.setPS(cameraID, func(p *pullPointSubscription) { p.pollErrs++ })
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), e.pullTimeout+10*time.Second)
		messages, err := e.events.PullMessages(ctx, e.refOf(cameraID), e.pullTimeout, e.messageLimit)
		cancel()
		if err != nil {
			e.setPS(cameraID, func(p *pullPointSubscription) {
				p.pollErrs++
				p.lastError = err.Error()
			})
			if e.pollErrsOf(cameraID) >= terminalPollFailures {
				eventLogger.Warn("PullMessages failing repeatedly; rebuilding subscription",
					"camera_id", cameraID, "consecutive_errors", e.pollErrsOf(cameraID))
				return true
			}
			continue
		}

		e.setPS(cameraID, func(p *pullPointSubscription) { p.pollErrs = 0 })

		for _, msg := range messages {
			event := parseNotificationMessage(msg, cameraID)
			if event.Topic == "" {
				continue // Skip messages without topics
			}

			e.setPS(cameraID, func(p *pullPointSubscription) {
				p.eventCount++
				p.lastEventAt = event.Timestamp
			})

			e.mu.Lock()
			callback := e.eventCallback
			e.mu.Unlock()
			if callback != nil {
				callback(event)
			}
		}
	}
}

// renewSubscription attempts to renew the PullPoint subscription before expiry.
func (e *EventSubscriberImpl) renewSubscription(cameraID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ref := e.refOf(cameraID)
	if ref == "" {
		return false
	}
	_, newTerm, err := e.events.RenewSubscription(ctx, ref, e.subDuration)
	if err != nil {
		eventLogger.Warn("failed to renew PullPoint subscription",
			"camera_id", cameraID, "error", err)
		e.setPS(cameraID, func(p *pullPointSubscription) { p.lastError = err.Error() })
		return false
	}
	if newTerm.IsZero() {
		newTerm = time.Now().Add(e.subDuration)
	}
	granted := time.Until(newTerm)
	if granted <= 0 {
		granted = e.subDuration
	}
	e.setPS(cameraID, func(p *pullPointSubscription) {
		p.terminationTime = newTerm
		p.grantedDur = granted
	})
	eventLogger.Debug("renewed PullPoint subscription",
		"camera_id", cameraID, "new_termination", newTerm)
	return true
}

// --- small guarded accessors so the poll loop never holds e.mu across I/O ---

func (e *EventSubscriberImpl) subLocked(cameraID string) *pullPointSubscription {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.subscriptions[cameraID]
}

func (e *EventSubscriberImpl) setPS(cameraID string, fn func(p *pullPointSubscription)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.subscriptions[cameraID]; ok {
		fn(p)
	}
}

func (e *EventSubscriberImpl) refOf(cameraID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.subscriptions[cameraID]; ok {
		return p.subscriptionRef
	}
	return ""
}

func (e *EventSubscriberImpl) termOf(cameraID string) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.subscriptions[cameraID]; ok {
		return p.terminationTime
	}
	return time.Time{}
}

func (e *EventSubscriberImpl) grantedOf(cameraID string) time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.subscriptions[cameraID]; ok {
		return p.grantedDur
	}
	return e.subDuration
}

func (e *EventSubscriberImpl) pollErrsOf(cameraID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.subscriptions[cameraID]; ok {
		return p.pollErrs
	}
	return 0
}

// sleepInterruptible waits d, returning false if stopCh closed first.
func sleepInterruptible(stopCh <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stopCh:
		return false
	case <-t.C:
		return true
	}
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
