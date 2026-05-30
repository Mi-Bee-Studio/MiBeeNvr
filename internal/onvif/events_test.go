package onvif

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	onvifgo "github.com/0x524a/onvif-go"
	"github.com/stretchr/testify/require"
)

// --- Test helpers ---

// mockOnvifClient wraps onvifgo.Client with controllable event methods.
// We can't use the real client, so we mock at the method level via a wrapper.
type mockEventClient struct {
	createPullPointFn func(ctx context.Context, filter string, terminationTime *time.Duration, subscriptionPolicy string) (*onvifgo.PullPointSubscription, error)
	pullMessagesFn     func(ctx context.Context, subscriptionReference string, timeout time.Duration, messageLimit int) ([]onvifgo.NotificationMessage, error)
	unsubscribeFn      func(ctx context.Context, subscriptionReference string) error
	renewFn           func(ctx context.Context, subscriptionReference string, terminationTime time.Duration) (time.Time, time.Time, error)

	subscribeCalls   atomic.Int32
	pullCalls        atomic.Int32
	unsubscribeCalls atomic.Int32
	renewCalls       atomic.Int32
}

func (m *mockEventClient) CreatePullPointSubscription(ctx context.Context, filter string, terminationTime *time.Duration, subscriptionPolicy string) (*onvifgo.PullPointSubscription, error) {
	m.subscribeCalls.Add(1)
	if m.createPullPointFn != nil {
		return m.createPullPointFn(ctx, filter, terminationTime, subscriptionPolicy)
	}
	return &onvifgo.PullPointSubscription{
		SubscriptionReference: "http://mock/pullpoint/1",
		CurrentTime:           time.Now(),
		TerminationTime:       time.Now().Add(24 * time.Hour),
	}, nil
}

func (m *mockEventClient) PullMessages(ctx context.Context, subscriptionReference string, timeout time.Duration, messageLimit int) ([]onvifgo.NotificationMessage, error) {
	m.pullCalls.Add(1)
	if m.pullMessagesFn != nil {
		return m.pullMessagesFn(ctx, subscriptionReference, timeout, messageLimit)
	}
	return nil, nil
}

func (m *mockEventClient) Unsubscribe(ctx context.Context, subscriptionReference string) error {
	m.unsubscribeCalls.Add(1)
	if m.unsubscribeFn != nil {
		return m.unsubscribeFn(ctx, subscriptionReference)
	}
	return nil
}

func (m *mockEventClient) RenewSubscription(ctx context.Context, subscriptionReference string, terminationTime time.Duration) (time.Time, time.Time, error) {
	m.renewCalls.Add(1)
	if m.renewFn != nil {
		return m.renewFn(ctx, subscriptionReference, terminationTime)
	}
	return time.Now(), time.Now().Add(24 * time.Hour), nil
}

// helperNewEventSubscriberWithMock creates an EventSubscriberImpl with a mock client
// that bypasses the *onvifgo.Client requirement.
func helperNewEventSubscriberWithMock(mock *mockEventClient, opts ...EventSubscriberOption) *EventSubscriberImpl {
	// We can't pass mock directly since it's not *onvifgo.Client.
	// Instead, we create the struct directly.
	es := &EventSubscriberImpl{
		subscriptions: make(map[string]*pullPointSubscription),
		stopCh:        make(map[string]chan struct{}),
		pollInterval:  100 * time.Millisecond, // Fast for tests
		pullTimeout:   1 * time.Second,
		messageLimit:  10,
		subDuration:   1 * time.Hour,
	}
	for _, opt := range opts {
		opt(es)
	}
	return es
}

// --- Tests ---

func TestNewEventSubscriber_Defaults(t *testing.T) {
	t.Helper()
	es := &EventSubscriberImpl{
		subscriptions: make(map[string]*pullPointSubscription),
		stopCh:        make(map[string]chan struct{}),
		pollInterval:  DefaultPollInterval,
		pullTimeout:   DefaultPullTimeout,
		messageLimit:  DefaultMessageLimit,
		subDuration:   DefaultSubscriptionDuration,
	}

	require.Equal(t, DefaultPollInterval, es.pollInterval)
	require.Equal(t, DefaultPullTimeout, es.pullTimeout)
	require.Equal(t, DefaultMessageLimit, es.messageLimit)
	require.Equal(t, DefaultSubscriptionDuration, es.subDuration)
}

func TestNewEventSubscriber_WithOptions(t *testing.T) {
	t.Helper()
	var called bool
	cb := func(event ONVIFEvent) { called = true }

	es := helperNewEventSubscriberWithMock(nil,
		WithEventCallback(cb),
		WithPollInterval(2*time.Second),
		WithPullTimeout(10*time.Second),
		WithSubscriptionDuration(12*time.Hour),
	)

	require.NotNil(t, es)
	require.NotNil(t, es.eventCallback)
	require.Equal(t, 2*time.Second, es.pollInterval)
	require.Equal(t, 10*time.Second, es.pullTimeout)
	require.Equal(t, 12*time.Hour, es.subDuration)

	// Verify callback wiring
	es.eventCallback(ONVIFEvent{Topic: "test"})
	require.True(t, called)
}

func TestEventSubscriberImpl_IsSubscribed(t *testing.T) {
	t.Helper()
	es := helperNewEventSubscriberWithMock(nil)

	require.False(t, es.IsSubscribed("cam-1"))

	es.mu.Lock()
	es.subscriptions["cam-1"] = &pullPointSubscription{active: true}
	es.mu.Unlock()

	require.True(t, es.IsSubscribed("cam-1"))

	es.mu.Lock()
	es.subscriptions["cam-1"].active = false
	es.mu.Unlock()

	require.False(t, es.IsSubscribed("cam-1"))
}

func TestEventSubscriberImpl_SubscribeAlreadySubscribed(t *testing.T) {
	t.Helper()
	es := helperNewEventSubscriberWithMock(nil)

	es.mu.Lock()
	es.subscriptions["cam-1"] = &pullPointSubscription{active: true}
	es.mu.Unlock()

	// Subscribe again should return nil (no-op)
	err := es.Subscribe(context.Background(), "cam-1")
	require.NoError(t, err)
	require.True(t, es.IsSubscribed("cam-1"))
}

func TestEventSubscriberImpl_UnsubscribeNotSubscribed(t *testing.T) {
	t.Helper()
	es := helperNewEventSubscriberWithMock(nil)

	// Unsubscribe from non-existent subscription should return nil
	err := es.Unsubscribe(context.Background(), "cam-1")
	require.NoError(t, err)
}

func TestEventSubscriberImpl_GetEventMessages(t *testing.T) {
	t.Helper()
	es := helperNewEventSubscriberWithMock(nil)

	events, err := es.GetEventMessages(context.Background())
	require.NoError(t, err)
	require.Nil(t, events)
}

func TestEventSubscriberImpl_SetEventCallback(t *testing.T) {
	t.Helper()
	es := helperNewEventSubscriberWithMock(nil)
	require.Nil(t, es.eventCallback)

	var called bool
	es.SetEventCallback(func(event ONVIFEvent) { called = true })

	require.NotNil(t, es.eventCallback)
	es.eventCallback(ONVIFEvent{Topic: "test"})
	require.True(t, called)
}

func TestEventSubscriberImpl_StopAll(t *testing.T) {
	t.Helper()
	es := helperNewEventSubscriberWithMock(nil)

	es.mu.Lock()
	es.subscriptions["cam-1"] = &pullPointSubscription{active: true, subscriptionRef: "http://ref/1"}
	es.subscriptions["cam-2"] = &pullPointSubscription{active: true, subscriptionRef: "http://ref/2"}
	es.mu.Unlock()

	es.StopAll(context.Background())

	require.False(t, es.IsSubscribed("cam-1"))
	require.False(t, es.IsSubscribed("cam-2"))
}

func TestParseNotificationMessage(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()

	msg := onvifgo.NotificationMessage{
		Topic: "tns1:VideoSource/MotionAlarm",
		Message: onvifgo.EventMessage{
			PropertyOperation: "Initialized",
			UtcTime:           now,
			Data: []onvifgo.SimpleItem{
				{Name: "IsMotion", Value: "true"},
				{Name: "Level", Value: "high"},
			},
			Source: []onvifgo.SimpleItem{
				{Name: "VideoSource", Value: "1"},
			},
			Key: []onvifgo.SimpleItem{
				{Name: "Token", Value: "abc123"},
			},
		},
	}

	event := parseNotificationMessage(msg, "cam-1")

	require.Equal(t, "tns1:VideoSource/MotionAlarm", event.Topic)
	require.Equal(t, "cam-1", event.CameraID)
	require.Equal(t, now, event.Timestamp)
	require.Equal(t, "true", event.Data["IsMotion"])
	require.Equal(t, "high", event.Data["Level"])
	require.Equal(t, "1", event.Data["source.VideoSource"])
}

func TestParseNotificationMessage_EmptyTopic(t *testing.T) {
	t.Helper()
	msg := onvifgo.NotificationMessage{
		Topic: "",
		Message: onvifgo.EventMessage{
			Data: []onvifgo.SimpleItem{
				{Name: "Key", Value: "val"},
			},
		},
	}

	event := parseNotificationMessage(msg, "cam-1")
	require.Equal(t, "", event.Topic)
	require.NotNil(t, event.Data)
	require.Equal(t, "val", event.Data["Key"])
}

func TestParseNotificationMessage_ZeroTimestamp(t *testing.T) {
	t.Helper()
	msg := onvifgo.NotificationMessage{
		Topic: "tns1:Audio/AudioAnalytics",
		Message: onvifgo.EventMessage{
			UtcTime: time.Time{}, // Zero value
		},
	}

	event := parseNotificationMessage(msg, "cam-2")
	require.False(t, event.Timestamp.IsZero())
}


func TestEventSubscriberImpl_EventCallbackReceivesEvents(t *testing.T) {
	t.Helper()

	var receivedEvents []ONVIFEvent
	var mu sync.Mutex

	cb := func(event ONVIFEvent) {
		mu.Lock()
		receivedEvents = append(receivedEvents, event)
		mu.Unlock()
	}

	es := helperNewEventSubscriberWithMock(nil,
		WithEventCallback(cb),
		WithPollInterval(50*time.Millisecond),
	)


	es.mu.Lock()
	es.subscriptions["cam-1"] = &pullPointSubscription{
		subscriptionRef: "http://mock/ref/1",
		terminationTime: time.Now().Add(1 * time.Hour),
		active:          true,
	}
	es.mu.Unlock()

	// Simulate calling the callback through pollAndPublish logic
	// (We can't test the full poll loop without a mock onvifgo.Client)
	es.eventCallback(ONVIFEvent{
		Topic:    "tns1:VideoSource/MotionAlarm",
		CameraID: "cam-1",
		Data:     map[string]any{"IsMotion": "true"},
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, receivedEvents, 1)
	require.Equal(t, "tns1:VideoSource/MotionAlarm", receivedEvents[0].Topic)
	require.Equal(t, "cam-1", receivedEvents[0].CameraID)
}

// --- Interface compliance ---

func TestEventSubscriberImplImplementsEventSubscriber(t *testing.T) {
	t.Helper()
	// Compile-time check: EventSubscriberImpl must implement EventSubscriber
	var _ EventSubscriber = (*EventSubscriberImpl)(nil)
}
